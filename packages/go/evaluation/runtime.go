package evaluation

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Config configures an [EvaluationRuntime].
type Config struct {
	// ScenarioTimeout bounds one scenario when it declares none.
	ScenarioTimeout time.Duration

	// MaxParallel bounds concurrent scenarios in a parallel suite.
	MaxParallel int

	// Tolerances apply to scenarios that declare none.
	Tolerances Tolerances

	// DeterminismRuns is how many times the determinism engine repeats a
	// scenario.
	//
	// Three by default. Two can agree by luck when the nondeterminism is a map
	// iteration that happened to land the same way; three is the smallest
	// number that makes coincidence unlikely without tripling every suite's
	// cost, and the engine reports the count it used so the confidence is
	// legible.
	DeterminismRuns int

	// BenchmarkIterations is the default sample count for a benchmark.
	BenchmarkIterations int

	// BenchmarkWarmup is how many iterations are discarded before measuring.
	BenchmarkWarmup int

	// AutoRecordCandidates records an observation as a golden CANDIDATE when
	// no baseline exists.
	//
	// On by default, and it is worth being precise about what it does NOT do:
	// it never approves. A candidate is filed for review; the run still reports
	// [VerdictNoBaseline]. Turning it off means a new scenario produces nothing
	// to approve, which makes adding one a two-step dance for no benefit.
	AutoRecordCandidates bool
}

// DefaultConfig returns the platform baseline.
func DefaultConfig() Config {
	return Config{
		ScenarioTimeout:      30 * time.Second,
		MaxParallel:          8,
		Tolerances:           DefaultTolerances(),
		DeterminismRuns:      3,
		BenchmarkIterations:  50,
		BenchmarkWarmup:      5,
		AutoRecordCandidates: true,
	}
}

func (c Config) validate() []string {
	var problems []string
	if c.ScenarioTimeout <= 0 {
		problems = append(problems, "config: ScenarioTimeout must be positive; an "+
			"unbounded scenario is an evaluation run that never finishes")
	}
	if c.MaxParallel <= 0 {
		problems = append(problems, "config: MaxParallel must be positive")
	}
	if c.DeterminismRuns < 2 {
		problems = append(problems, "config: DeterminismRuns must be at least 2; one "+
			"run cannot disagree with itself")
	}
	if c.BenchmarkIterations <= 0 {
		problems = append(problems, "config: BenchmarkIterations must be positive")
	}
	if c.BenchmarkWarmup < 0 {
		problems = append(problems, "config: BenchmarkWarmup must not be negative")
	}
	problems = append(problems, c.Tolerances.validate("config")...)
	return problems
}

// Option customises a runtime.
type Option func(*options)

type options struct {
	clock   rt.Clock
	metrics *Metrics
	logger  *slog.Logger
	storage Storage
}

// WithClock injects a clock.
func WithClock(c rt.Clock) Option { return func(o *options) { o.clock = c } }

// WithMetrics injects an instrument set.
func WithMetrics(m *Metrics) Option { return func(o *options) { o.metrics = m } }

// WithLogger injects a logger.
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.logger = l } }

// WithStorage injects a storage backend.
func WithStorage(s Storage) Option { return func(o *options) { o.storage = s } }

// RunState is an evaluation run's stage.
type RunState uint8

// The run states.
const (
	RunPending RunState = iota
	RunRunning
	RunComplete
	RunAborted
)

// String renders the state.
func (s RunState) String() string {
	switch s {
	case RunRunning:
		return "running"
	case RunComplete:
		return "complete"
	case RunAborted:
		return "aborted"
	default:
		return "pending"
	}
}

// ScenarioResult is one scenario's outcome within a run.
type ScenarioResult struct {
	// Scenario and Subject identify what ran.
	Scenario ScenarioID
	Subject  SubjectName
	Kind     ScenarioKind
	// Verdict is the comparison outcome.
	Verdict Verdict
	// Comparison carries the detail.
	Comparison Comparison
	// Observation is what was recorded.
	Observation Observation
	// Candidate names a golden candidate filed for review, empty when none.
	Candidate GoldenID
	// Duration is the wall time including platform overhead.
	Duration time.Duration
}

// Run is one evaluation run.
type Run struct {
	// ID identifies it.
	ID RunID
	// Suite names what ran, empty for an ad-hoc scenario set.
	Suite SuiteID
	// State is the stage.
	State RunState
	// RegistryVersion and RegistryDigest anchor the run to a scenario set.
	// THE REPLAY ANCHOR for the questions, as the golden is for the answers.
	RegistryVersion uint64
	RegistryDigest  Fingerprint
	// Results are the scenario outcomes, sorted by subject then scenario.
	Results []ScenarioResult
	// StartedAt and FinishedAt bound it.
	StartedAt  time.Time
	FinishedAt time.Time
	// Label is an operator-supplied name: a commit, a release candidate.
	Label string
}

// Duration returns the run's wall time.
func (r Run) Duration() time.Duration { return r.FinishedAt.Sub(r.StartedAt) }

// Counts returns the verdict tally.
func (r Run) Counts() map[Verdict]int {
	out := map[Verdict]int{
		VerdictPass: 0, VerdictDrift: 0, VerdictFail: 0,
		VerdictNoBaseline: 0, VerdictSkipped: 0,
	}
	for _, res := range r.Results {
		out[res.Verdict]++
	}
	return out
}

// Blocking returns the results that should stop a release.
func (r Run) Blocking() []ScenarioResult {
	var out []ScenarioResult
	for _, res := range r.Results {
		if res.Verdict.Blocking() {
			out = append(out, res)
		}
	}
	return out
}

// Drifted returns the results that changed behaviour against a baseline.
func (r Run) Drifted() []ScenarioResult {
	var out []ScenarioResult
	for _, res := range r.Results {
		if res.Verdict == VerdictDrift {
			out = append(out, res)
		}
	}
	return out
}

// EvaluationRuntime executes evaluation runs.
//
// It owns everything and shares nothing. Two runtimes in one process have
// separate registries, golden stores, storage and metrics, which is what makes
// the test suite parallel-safe and "no global mutable state" a checkable
// property rather than an aspiration.
type EvaluationRuntime struct {
	cfg      Config
	clock    rt.Clock
	logger   *slog.Logger
	metrics  *Metrics
	registry *Registry
	goldens  *GoldenStore
	subjects *SubjectSet
	storage  Storage

	// resolution is the measured granularity of the injected clock. Every
	// latency comparison is floored by it, because a platform cannot report a
	// change smaller than its clock can see. See [ClockResolution].
	resolution time.Duration

	started atomic.Bool
	stopped atomic.Bool

	runs      atomic.Uint64
	scenarios atomic.Uint64
}

// New builds an evaluation runtime.
func New(cfg Config, subjects *SubjectSet, opts ...Option) (*EvaluationRuntime, error) {
	o := &options{clock: rt.SystemClock{}, logger: slog.Default()}
	for _, opt := range opts {
		opt(o)
	}

	problems := cfg.validate()
	if subjects == nil || subjects.Len() == 0 {
		problems = append(problems, "runtime: at least one subject is required; a "+
			"platform with nothing to evaluate reports a perfect score over nothing")
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, &ConfigError{Problems: problems}
	}

	metrics := o.metrics
	if metrics == nil {
		metrics = NewMetrics()
	}
	storage := o.storage
	if storage == nil {
		storage = NewMemoryStorage()
	}

	resolution := ClockResolution(o.clock)
	cfg.Tolerances.LatencyFloor = resolutionFloor(cfg.Tolerances.LatencyFloor, resolution)

	return &EvaluationRuntime{
		cfg: cfg, clock: o.clock, logger: o.logger, metrics: metrics,
		registry: NewRegistry(o.clock, metrics),
		goldens:  NewGoldenStore(o.clock),
		subjects: subjects, storage: storage,
		resolution: resolution,
	}, nil
}

// ClockResolution returns the measured granularity of the runtime's clock.
//
// Reported in every performance document and every readiness report, because a
// reader comparing two latency figures needs to know whether the difference is
// larger than what the clock can distinguish.
func (e *EvaluationRuntime) ClockResolution() time.Duration { return e.resolution }

// Registry returns the scenario and suite registry.
func (e *EvaluationRuntime) Registry() *Registry { return e.registry }

// Goldens returns the golden store.
func (e *EvaluationRuntime) Goldens() *GoldenStore { return e.goldens }

// Subjects returns the subject set.
func (e *EvaluationRuntime) Subjects() *SubjectSet { return e.subjects }

// Storage returns the storage backend.
func (e *EvaluationRuntime) Storage() Storage { return e.storage }

// Metrics returns the instrument set.
func (e *EvaluationRuntime) Metrics() *Metrics { return e.metrics }

// Clock returns the injected clock.
func (e *EvaluationRuntime) Clock() rt.Clock { return e.clock }

// Config returns the configuration.
func (e *EvaluationRuntime) Config() Config { return e.cfg }

// Coordinator returns the cross-cutting operations handle.
func (e *EvaluationRuntime) Coordinator() *Coordinator { return &Coordinator{runtime: e} }

// Runs returns how many runs have executed.
func (e *EvaluationRuntime) Runs() uint64 { return e.runs.Load() }

// ScenariosExecuted returns how many scenarios have executed.
func (e *EvaluationRuntime) ScenariosExecuted() uint64 { return e.scenarios.Load() }

// Start marks the runtime live.
//
// A runtime starts once (INV-EVAL-10). The platform has no background workers,
// so Start exists to make the lifecycle explicit and to give Stop something to
// refuse against — a runtime that could be restarted after Stop would let a run
// begin against a storage backend somebody had already closed.
func (e *EvaluationRuntime) Start() error {
	if e.stopped.Load() {
		return ErrRunClosed
	}
	if !e.started.CompareAndSwap(false, true) {
		return invariant("INV-EVAL-10", "runtime already started")
	}
	return nil
}

// Stop halts the runtime.
func (e *EvaluationRuntime) Stop() error {
	e.stopped.Store(true)
	return nil
}

// Execute runs one scenario and compares it against its baseline.
//
// The single scenario path, and the one every other engine is built from:
// determinism repeats it, replay compares against a stored observation,
// regression compares two runs of it, and a benchmark samples it.
func (e *EvaluationRuntime) Execute(ctx context.Context, s Scenario) ScenarioResult {
	start := e.clock.Now()
	res := ScenarioResult{Scenario: s.ID, Subject: s.SubjectName, Kind: s.Kind}

	if e.stopped.Load() {
		res.Verdict = VerdictFail
		res.Comparison = Comparison{Verdict: VerdictFail, Scenario: s.ID,
			Subject: s.SubjectName, Reason: "runtime_closed"}
		return res
	}

	sub, ok := e.subjects.Get(s.SubjectName)
	if !ok {
		res.Verdict = VerdictFail
		res.Comparison = Comparison{Verdict: VerdictFail, Scenario: s.ID,
			Subject: s.SubjectName, Reason: "subject_not_registered"}
		e.count(res)
		return res
	}

	// A scenario the subject cannot run is SKIPPED, with the missing
	// capabilities named. Reporting it as a failure would make an adapter that
	// has not implemented streaming look like a broken subsystem.
	required := append(append([]Capability(nil), s.Requires...), RequiredInjections(s)...)
	if missing := MissingCapabilities(sub, required); len(missing) > 0 {
		res.Verdict = VerdictSkipped
		res.Comparison = Comparison{Verdict: VerdictSkipped, Scenario: s.ID,
			Subject: s.SubjectName, Reason: "missing_capability", Skipped: missing}
		res.Duration = e.clock.Since(start)
		e.count(res)
		if e.metrics != nil {
			e.metrics.Skipped.Inc(string(s.SubjectName), "missing_capability")
		}
		return res
	}

	obs := e.observe(ctx, sub, s)
	res.Observation = obs
	res.Duration = e.clock.Since(start)

	tol := s.Tolerances
	if tol.LatencyRatio == 0 && len(tol.MetricDeltas) == 0 {
		tol = e.cfg.Tolerances
	}
	// A scenario's own floor is raised to what the clock can actually see. A
	// scenario author cannot know the resolution of the machine the platform
	// will run on, and honouring a floor below it would report drift the clock
	// cannot distinguish from rounding.
	tol.LatencyFloor = resolutionFloor(tol.LatencyFloor, e.resolution)

	golden, err := e.goldens.Baseline(s.Key())
	switch {
	case err == nil:
		res.Comparison = Compare(golden, obs, tol)
	case obs.Failed():
		// No baseline AND the run failed. The failure is the finding; a
		// missing baseline is not what anybody needs to hear about first.
		res.Comparison = Comparison{Verdict: VerdictFail, Scenario: s.ID,
			Subject: s.SubjectName, ObservedBehaviour: obs.BehaviourPrint(),
			Reason: "scenario_failed_no_baseline"}
	default:
		res.Comparison = Comparison{Verdict: VerdictNoBaseline, Scenario: s.ID,
			Subject: s.SubjectName, ObservedBehaviour: obs.BehaviourPrint(),
			Reason: "no_approved_golden"}
	}
	res.Verdict = res.Comparison.Verdict

	// ---- file a candidate for anything a human must decide about ----------
	//
	// BOTH the no-baseline case and the DRIFT case, and the second is the one
	// that matters. Drift means an approved baseline says one thing and the
	// subsystem now does another; the entire review workflow is "here is what
	// changed, decide". A platform that filed a candidate for a brand-new
	// scenario — low stakes, nobody has an opinion yet — and filed nothing for
	// drift would leave the reviewer with nothing to promote at exactly the
	// moment their decision is required, and they would have to reconstruct the
	// observation by hand.
	//
	// Still never an approval. A candidate sitting beside an approved golden is
	// the state a drift review needs: here is what we agreed, here is what
	// happens now. See ENGINEERING_AUDIT F2.
	if e.cfg.AutoRecordCandidates &&
		(res.Verdict == VerdictNoBaseline || res.Verdict == VerdictDrift) {
		if candidate, rerr := e.goldens.Record(s, obs); rerr == nil {
			res.Candidate = candidate.ID
			if e.metrics != nil {
				e.metrics.GoldensRecorded.Inc(string(s.SubjectName))
				e.metrics.PendingGoldens.Set(float64(e.goldens.PendingCount()))
			}
		}
	}

	e.count(res)
	if err := e.storage.PutObservation(obs); err != nil && e.metrics != nil {
		e.metrics.StorageErrors.Inc("observation")
	} else if e.metrics != nil {
		e.metrics.StorageWrites.Inc("observation")
	}
	return res
}

// observe runs a scenario against a subject and records what happened.
//
// It never returns an error. A scenario that could not run produces an
// observation carrying the reason — because an evaluation platform that throws
// away the record when things go wrong has thrown away the only evidence of the
// thing it exists to detect.
func (e *EvaluationRuntime) observe(ctx context.Context, sub Subject, s Scenario) Observation {
	obs := Observation{
		Scenario: s.ID, Subject: s.SubjectName, ScenarioVersion: s.Version,
		Seed: s.Seed, StartedAt: e.clock.Now(), Metrics: map[string]float64{},
	}

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = e.cfg.ScenarioTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	session, err := sub.Open(runCtx, SessionSpec{
		Scenario: s.ID, Seed: s.Seed, Params: s.Params.Clone()})
	if err != nil {
		obs.Error = "open_failed: " + err.Error()
		obs.FinishedAt = e.clock.Now()
		return obs
	}
	defer func() {
		if cerr := session.Close(); cerr != nil && obs.Error == "" {
			obs.Error = "close_failed: " + cerr.Error()
		}
	}()

	e.metrics.InFlight.Add(1)
	defer e.metrics.InFlight.Add(-1)

	sequence := 0
	for _, step := range s.Steps {
		if runCtx.Err() != nil {
			obs.Error = "timeout"
			break
		}

		stepStart := e.clock.Now()
		result := e.runStep(runCtx, session, step)
		duration := e.clock.Since(stepStart)
		if result.Duration > 0 {
			// An adapter that measured the operation itself is more accurate
			// than the platform's wrapper, which includes the platform's own
			// overhead.
			duration = result.Duration
		}

		events := session.Events()
		for i := range events {
			sequence++
			events[i].Sequence = sequence
		}

		so := StepObservation{
			Name: step.Name, Op: step.Op, Output: result.Output.Clone(),
			Outcome: result.Outcome, Failed: result.Failed, Detail: result.Detail,
			State: session.State().Clone(), Events: events, Duration: duration,
		}
		if step.Inject != nil {
			so.Injected = step.Inject.String()
			if e.metrics != nil {
				e.metrics.Injections.Inc(string(step.Inject.Kind), string(s.SubjectName))
			}
		}
		obs.Steps = append(obs.Steps, so)

		if e.metrics != nil {
			e.metrics.StepTime.ObserveDuration(duration, string(s.SubjectName))
		}

		// A failed step stops the scenario. Continuing past one produces an
		// observation whose later steps ran against a state nobody planned,
		// and a golden recorded from that is a baseline for a broken run.
		if result.Failed {
			break
		}
	}

	obs.FinishedAt = e.clock.Now()
	obs.Metrics["step_count"] = float64(len(obs.Steps))
	obs.Metrics["total_step_seconds"] = obs.TotalStepTime().Seconds()
	return obs
}

// runStep executes one step, converting a panic into a failed observation.
//
// An adapter that panics is a broken adapter, not a broken subject — and the
// platform recording that as a step failure with the detail is far more useful
// than the whole evaluation run dying because one adapter dereferenced a nil.
func (e *EvaluationRuntime) runStep(ctx context.Context, session Session, step Step) (result StepResult) {
	defer func() {
		if rec := recover(); rec != nil {
			result = StepResult{
				Failed: true, Outcome: "adapter_panic",
				// The panic value is included here and NOWHERE else: Detail is
				// excluded from behaviour fingerprints, so an adapter's stack
				// trace cannot become drift, and a platform that hid it would
				// make a broken adapter undiagnosable.
				Detail: fmt.Sprint(rec),
			}
		}
	}()

	if step.Advance > 0 {
		// Carried to the adapter rather than performed here: only the adapter
		// knows which clock to move, and a platform that reached for a clock
		// would be a platform that knows what it is evaluating.
		if adv, ok := session.(interface{ Advance(time.Duration) }); ok {
			adv.Advance(step.Advance)
		}
	}
	return session.Execute(ctx, step)
}

func (e *EvaluationRuntime) count(res ScenarioResult) {
	e.scenarios.Add(1)
	if e.metrics == nil {
		return
	}
	e.metrics.ScenariosRun.Inc(string(res.Subject), res.Kind.String())
	e.metrics.Verdicts.Inc(res.Verdict.String(), string(res.Subject))
	e.metrics.ScenarioTime.ObserveDuration(res.Duration, string(res.Subject))
}

// RunSuite executes a suite.
func (e *EvaluationRuntime) RunSuite(ctx context.Context, id SuiteID, label string) (Run, error) {
	suite, ok := e.registry.Suite(id)
	if !ok {
		return Run{}, fmt.Errorf("%w: suite %s", ErrNotRegistered, id)
	}
	scenarios, err := e.registry.SuiteScenarios(id)
	if err != nil {
		return Run{}, err
	}

	run := e.runScenarios(ctx, scenarios, suite.Parallel, label)
	run.Suite = id
	if e.metrics != nil {
		e.metrics.Runs.Inc(string(id))
	}
	if err := e.storage.PutRun(run); err != nil && e.metrics != nil {
		e.metrics.StorageErrors.Inc("run")
	}
	return run, nil
}

// RunAll executes every registered scenario.
func (e *EvaluationRuntime) RunAll(ctx context.Context, label string) Run {
	run := e.runScenarios(ctx, e.registry.Scenarios(), false, label)
	if e.metrics != nil {
		e.metrics.Runs.Inc("<all>")
	}
	if err := e.storage.PutRun(run); err != nil && e.metrics != nil {
		e.metrics.StorageErrors.Inc("run")
	}
	return run
}

func (e *EvaluationRuntime) runScenarios(ctx context.Context, scenarios ScenarioSet, parallel bool, label string) Run {
	start := e.clock.Now()
	snap := e.registry.snapshot.Load()

	run := Run{
		ID: NewRunID(), State: RunRunning, Label: label,
		RegistryVersion: snap.version, RegistryDigest: snap.digest,
		StartedAt: start,
	}
	e.runs.Add(1)

	results := make([]ScenarioResult, len(scenarios))
	if parallel {
		sem := make(chan struct{}, e.cfg.MaxParallel)
		var wg sync.WaitGroup
		for i, s := range scenarios {
			wg.Add(1)
			go func(i int, s Scenario) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				results[i] = e.Execute(ctx, s)
			}(i, s)
		}
		wg.Wait()
	} else {
		for i, s := range scenarios {
			results[i] = e.Execute(ctx, s)
		}
	}

	// Sorted by subject then scenario, NOT by completion order. A parallel
	// suite completes in whatever order the scheduler chose, and a report that
	// reordered between runs could not be diffed against yesterday's.
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Subject != results[j].Subject {
			return results[i].Subject < results[j].Subject
		}
		return results[i].Scenario < results[j].Scenario
	})

	run.Results = results
	run.State = RunComplete
	run.FinishedAt = e.clock.Now()
	if e.metrics != nil {
		e.metrics.RunTime.ObserveDuration(run.Duration())
	}
	return run
}
