package evaluation

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Determinism engine
// ---------------------------------------------------------------------------

// DeterminismResult is the outcome of repeating a scenario.
type DeterminismResult struct {
	// Scenario and Subject identify what was checked.
	Scenario ScenarioID
	Subject  SubjectName
	// Runs is how many times it ran.
	Runs int
	// Deterministic reports that every run produced the same behaviour.
	Deterministic bool
	// Behaviours lists the distinct behaviour fingerprints observed, sorted.
	// More than one is the finding.
	Behaviours []Fingerprint
	// Divergences describes what differed, comparing every later run against
	// the first.
	Divergences []Difference
	// FirstDivergentRun is the 1-based index of the first run that differed.
	FirstDivergentRun int
	// Timings are each run's total step time, in run order. Reported but NEVER
	// used to decide determinism — see [Observation.BehaviourPrint].
	Timings []time.Duration
}

// Summary renders the result.
func (d DeterminismResult) Summary() string {
	if d.Deterministic {
		return fmt.Sprintf("%s on %s: deterministic across %d runs (%s)",
			d.Scenario, d.Subject, d.Runs, d.Behaviours[0])
	}
	return fmt.Sprintf("%s on %s: NON-DETERMINISTIC — %d distinct behaviours across "+
		"%d runs, first divergence at run %d",
		d.Scenario, d.Subject, len(d.Behaviours), d.Runs, d.FirstDivergentRun)
}

// CheckDeterminism runs a scenario repeatedly and compares behaviour.
//
//	Same input → same decisions → same state → same events → same outputs
//
// COMPARED ON BEHAVIOUR, NEVER ON TIME. Two runs of a deterministic system
// produce identical outputs and different durations; a system producing
// identical durations would be a system with no clock. Folding timings into the
// comparison would make this check fail on a busy CI box and teach everybody to
// ignore it.
func (e *EvaluationRuntime) CheckDeterminism(ctx context.Context, s Scenario, runs int) DeterminismResult {
	if runs < 2 {
		runs = e.cfg.DeterminismRuns
	}

	result := DeterminismResult{Scenario: s.ID, Subject: s.SubjectName, Runs: runs}

	var (
		first        Observation
		seen         = map[Fingerprint]bool{}
		observations []Observation
	)
	for i := 0; i < runs; i++ {
		res := e.Execute(ctx, s)
		obs := res.Observation
		observations = append(observations, obs)
		result.Timings = append(result.Timings, obs.TotalStepTime())

		print := obs.BehaviourPrint()
		if !seen[print] {
			seen[print] = true
			result.Behaviours = append(result.Behaviours, print)
		}
		if i == 0 {
			first = obs
			continue
		}
		if print != first.BehaviourPrint() && result.FirstDivergentRun == 0 {
			result.FirstDivergentRun = i + 1
			result.Divergences = behaviourDifferences(first, obs)
			sortDifferences(result.Divergences)
		}
	}

	sort.Slice(result.Behaviours, func(i, j int) bool {
		return result.Behaviours[i] < result.Behaviours[j]
	})
	result.Deterministic = len(result.Behaviours) == 1

	if e.metrics != nil {
		outcome := "deterministic"
		if !result.Deterministic {
			outcome = "divergent"
			e.metrics.DeterminismDiverged.Inc(string(s.SubjectName))
		}
		e.metrics.DeterminismChecks.Inc(string(s.SubjectName), outcome)
	}
	return result
}

// ---------------------------------------------------------------------------
// Replay engine
// ---------------------------------------------------------------------------

// ReplayResult is the outcome of replaying a recorded observation.
type ReplayResult struct {
	// Scenario and Subject identify what was replayed.
	Scenario ScenarioID
	Subject  SubjectName
	// Reproduced reports that the replay matched the recording behaviourally.
	Reproduced bool
	// Recorded and Replayed are the two behaviour fingerprints.
	Recorded Fingerprint
	Replayed Fingerprint
	// Differences describe what changed.
	Differences []Difference
	// RecordedAt is when the original observation was taken.
	RecordedAt time.Time
	// Reason is a short machine-readable code.
	Reason string
}

// Summary renders the result.
func (r ReplayResult) Summary() string {
	if r.Reproduced {
		return fmt.Sprintf("%s on %s: reproduced (%s)", r.Scenario, r.Subject, r.Recorded)
	}
	return fmt.Sprintf("%s on %s: NOT REPRODUCED (%s → %s, %d differences, %s)",
		r.Scenario, r.Subject, r.Recorded, r.Replayed, len(r.Differences), r.Reason)
}

// Replay re-runs a scenario and compares against a recorded observation.
//
// The difference from [EvaluationRuntime.Execute] is what it compares against.
// Execute compares against an APPROVED golden — what somebody decided is
// correct. Replay compares against a SPECIFIC recording — what happened at a
// particular moment, whether or not anybody approved it.
//
// That is what makes it the incident tool: "reproduce the run from Tuesday" is a
// question about a recording, not about a baseline, and answering it must not
// require anybody to have approved Tuesday.
func (e *EvaluationRuntime) Replay(ctx context.Context, s Scenario, recorded Observation) ReplayResult {
	result := ReplayResult{
		Scenario: s.ID, Subject: s.SubjectName,
		Recorded: recorded.BehaviourPrint(), RecordedAt: recorded.StartedAt,
	}

	if recorded.ScenarioVersion != s.Version {
		// Refused rather than compared: replaying a v1 recording against a v3
		// scenario produces differences that are real and meaningless.
		result.Reason = "scenario_version_mismatch"
		result.Differences = []Difference{{
			Kind: DiffScenario,
			Was:  fmt.Sprintf("v%d", recorded.ScenarioVersion),
			Now:  fmt.Sprintf("v%d", s.Version)}}
		e.countReplay(s, false)
		return result
	}

	res := e.Execute(ctx, s)
	result.Replayed = res.Observation.BehaviourPrint()
	result.Reproduced = result.Recorded == result.Replayed

	if !result.Reproduced {
		result.Differences = behaviourDifferences(recorded, res.Observation)
		sortDifferences(result.Differences)
		result.Reason = "behaviour_differs"
	} else {
		result.Reason = "reproduced"
	}

	e.countReplay(s, result.Reproduced)
	return result
}

// ReplayLatest replays a scenario against its most recent stored observation.
func (e *EvaluationRuntime) ReplayLatest(ctx context.Context, s Scenario) (ReplayResult, error) {
	stored := e.storage.Observations(s.ID)
	if len(stored) == 0 {
		return ReplayResult{}, fmt.Errorf("%w: no stored observation for %s",
			ErrNoBaselineRun, s.ID)
	}
	return e.Replay(ctx, s, stored[len(stored)-1]), nil
}

func (e *EvaluationRuntime) countReplay(s Scenario, ok bool) {
	if e.metrics == nil {
		return
	}
	outcome := "reproduced"
	if !ok {
		outcome = "diverged"
	}
	e.metrics.Replays.Inc(string(s.SubjectName), outcome)
}

// ---------------------------------------------------------------------------
// Regression engine
// ---------------------------------------------------------------------------

// RegressionKind classifies a detected change.
//
// The six the brief enumerates, plus improvement — because a platform that can
// only report deterioration teaches people that its output is always bad news,
// and a subsystem that got twice as fast is a fact worth recording next to the
// one that got twice as slow.
type RegressionKind string

// The regression kinds.
const (
	RegressionBehaviour    RegressionKind = "behaviour"
	RegressionPolicy       RegressionKind = "policy"
	RegressionConversation RegressionKind = "conversation"
	RegressionMemory       RegressionKind = "memory"
	RegressionLatency      RegressionKind = "latency"
	RegressionPlanning     RegressionKind = "planning"
	RegressionImprovement  RegressionKind = "improvement"

	// RegressionCoverage means the scenario stopped being evaluated. Not a
	// behaviour change — an absence of one, which is why it needs its own kind:
	// a reader scanning for "behaviour" would never find it.
	RegressionCoverage RegressionKind = "coverage"
)

// Deterioration reports whether the kind is bad news.
func (k RegressionKind) Deterioration() bool { return k != RegressionImprovement }

// Regression is one detected change between two runs.
type Regression struct {
	// Kind classifies it.
	Kind RegressionKind
	// Scenario and Subject identify where.
	Scenario ScenarioID
	Subject  SubjectName
	// WasVerdict and NowVerdict are the two outcomes.
	WasVerdict Verdict
	NowVerdict Verdict
	// WasBehaviour and NowBehaviour are the two fingerprints.
	WasBehaviour Fingerprint
	NowBehaviour Fingerprint
	// Ratio is populated for latency regressions.
	Ratio float64
	// Detail explains it.
	Detail string
}

// String renders the regression.
func (r Regression) String() string {
	s := fmt.Sprintf("%s %s on %s: %s", r.Kind, r.Scenario, r.Subject, r.Detail)
	if r.Ratio != 0 {
		s += fmt.Sprintf(" (×%.2f)", r.Ratio)
	}
	return s
}

// RegressionReport compares two runs.
type RegressionReport struct {
	// Baseline and Current name the runs.
	Baseline RunID
	Current  RunID
	// BaselineLabel and CurrentLabel are their operator-supplied names.
	BaselineLabel string
	CurrentLabel  string
	// Regressions are the deteriorations, sorted.
	Regressions []Regression
	// Improvements are the changes for the better.
	Improvements []Regression
	// NewScenarios and RemovedScenarios describe set changes, which are not
	// regressions but do explain why the counts moved.
	NewScenarios     []ScenarioID
	RemovedScenarios []ScenarioID
	// RegistryChanged reports that the scenario set itself differs, so a
	// reader knows whether they are comparing answers or questions.
	RegistryChanged bool
	// At is when the comparison was made.
	At time.Time
}

// Clean reports that nothing deteriorated.
func (r RegressionReport) Clean() bool { return len(r.Regressions) == 0 }

// Blocking returns the regressions that should stop a release: anything where a
// scenario that passed now fails.
func (r RegressionReport) Blocking() []Regression {
	var out []Regression
	for _, reg := range r.Regressions {
		if reg.NowVerdict == VerdictFail && reg.WasVerdict != VerdictFail {
			out = append(out, reg)
		}
	}
	return out
}

// Summary renders the report.
func (r RegressionReport) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "regression %s (%s) → %s (%s): %d regressions, %d improvements\n",
		r.Baseline, r.BaselineLabel, r.Current, r.CurrentLabel,
		len(r.Regressions), len(r.Improvements))
	if r.RegistryChanged {
		b.WriteString("  NOTE: the scenario set changed between these runs\n")
	}
	for _, reg := range r.Regressions {
		fmt.Fprintf(&b, "  - %s\n", reg)
	}
	for _, imp := range r.Improvements {
		fmt.Fprintf(&b, "  + %s\n", imp)
	}
	for _, id := range r.NewScenarios {
		fmt.Fprintf(&b, "  new: %s\n", id)
	}
	for _, id := range r.RemovedScenarios {
		fmt.Fprintf(&b, "  removed: %s\n", id)
	}
	return b.String()
}

// DetectRegressions compares two runs.
//
// A PURE FUNCTION over the two runs and a latency tolerance. It reads no
// storage and no clock beyond the timestamp it stamps, so a regression report
// can be recomputed from two stored runs years later and compared against the
// one that was read at the time.
// latencyFloor is the shortest duration a ratio may be computed from. Steps
// below it on either side are not compared, because a ratio between two
// quantised samples measures the clock rather than the code. Supply the
// runtime's resolution-adjusted floor; [Coordinator.CompareRuns] does.
func DetectRegressions(baseline, current Run, latencyRatio float64,
	latencyFloor time.Duration) RegressionReport {
	report := RegressionReport{
		Baseline: baseline.ID, Current: current.ID,
		BaselineLabel: baseline.Label, CurrentLabel: current.Label,
		RegistryChanged: baseline.RegistryDigest != current.RegistryDigest,
		At:              current.FinishedAt,
	}

	base := make(map[ScenarioID]ScenarioResult, len(baseline.Results))
	for _, r := range baseline.Results {
		base[r.Scenario] = r
	}
	now := make(map[ScenarioID]ScenarioResult, len(current.Results))
	for _, r := range current.Results {
		now[r.Scenario] = r
	}

	for id := range now {
		if _, ok := base[id]; !ok {
			report.NewScenarios = append(report.NewScenarios, id)
		}
	}
	for id := range base {
		if _, ok := now[id]; !ok {
			report.RemovedScenarios = append(report.RemovedScenarios, id)
		}
	}
	sort.Slice(report.NewScenarios, func(i, j int) bool {
		return report.NewScenarios[i] < report.NewScenarios[j]
	})
	sort.Slice(report.RemovedScenarios, func(i, j int) bool {
		return report.RemovedScenarios[i] < report.RemovedScenarios[j]
	})

	for _, cur := range current.Results {
		was, ok := base[cur.Scenario]
		if !ok {
			continue
		}

		wasPrint := was.Observation.BehaviourPrint()
		nowPrint := cur.Observation.BehaviourPrint()

		switch {
		// Checked FIRST, ahead of the fail arms. A scenario going from Fail to
		// NoBaseline is not "no longer failing" — it is no longer being asked.
		case was.Verdict.Compared() && !cur.Verdict.Compared():
			// LOSING A BASELINE IS A REGRESSION.
			//
			// A scenario that was checked against an approved golden and is now
			// checked against nothing has stopped being evaluated. Its behaviour
			// fingerprint is unchanged and its verdict is not Fail, so every
			// other arm of this switch passes it over — which is how the first
			// version reported "0 regressions" for a run in which a scenario had
			// silently dropped out of the gate. Retiring a golden, wiping the
			// store, bumping a scenario version without re-approving: all three
			// look like this, and all three mean the platform is now blind to a
			// scenario it used to watch. See ENGINEERING_AUDIT F6.
			report.Regressions = append(report.Regressions, Regression{
				Kind: RegressionCoverage, Scenario: cur.Scenario, Subject: cur.Subject,
				WasVerdict: was.Verdict, NowVerdict: cur.Verdict,
				WasBehaviour: wasPrint, NowBehaviour: nowPrint,
				Detail: "scenario lost its baseline and is no longer evaluated",
			})
		case was.Verdict != VerdictFail && cur.Verdict == VerdictFail:
			report.Regressions = append(report.Regressions, Regression{
				Kind: kindForSubject(cur.Kind), Scenario: cur.Scenario, Subject: cur.Subject,
				WasVerdict: was.Verdict, NowVerdict: cur.Verdict,
				WasBehaviour: wasPrint, NowBehaviour: nowPrint,
				Detail: "scenario now fails: " + cur.Comparison.Reason,
			})
		case was.Verdict == VerdictFail && cur.Verdict != VerdictFail:
			report.Improvements = append(report.Improvements, Regression{
				Kind: RegressionImprovement, Scenario: cur.Scenario, Subject: cur.Subject,
				WasVerdict: was.Verdict, NowVerdict: cur.Verdict,
				WasBehaviour: wasPrint, NowBehaviour: nowPrint,
				Detail: "scenario no longer fails",
			})
		case wasPrint != nowPrint:
			report.Regressions = append(report.Regressions, Regression{
				Kind: kindForSubject(cur.Kind), Scenario: cur.Scenario, Subject: cur.Subject,
				WasVerdict: was.Verdict, NowVerdict: cur.Verdict,
				WasBehaviour: wasPrint, NowBehaviour: nowPrint,
				Detail: "behaviour changed",
			})
		}

		// Latency is compared independently of behaviour: a subsystem that
		// answers identically and takes four times as long has regressed, and
		// one whose behaviour changed may also have got faster.
		if latencyRatio > 0 {
			wasTime := was.Observation.TotalStepTime()
			nowTime := cur.Observation.TotalStepTime()
			// BOTH sides must clear the floor.
			//
			// Guarding only `wasTime > 0` let a run whose total step time
			// quantised to zero report a fourfold IMPROVEMENT — "faster: 975.6µs
			// → 0s" — and a single verification pass produced eight of them. A
			// report that is mostly noise is a report nobody reads, and the
			// regressions it does contain go with it. See ENGINEERING_AUDIT F7.
			if wasTime >= latencyFloor && nowTime >= latencyFloor {
				ratio := float64(nowTime) / float64(wasTime)
				switch {
				case ratio > latencyRatio:
					report.Regressions = append(report.Regressions, Regression{
						Kind: RegressionLatency, Scenario: cur.Scenario, Subject: cur.Subject,
						WasVerdict: was.Verdict, NowVerdict: cur.Verdict, Ratio: ratio,
						Detail: fmt.Sprintf("slower: %s → %s", wasTime, nowTime),
					})
				case ratio < 1/latencyRatio:
					report.Improvements = append(report.Improvements, Regression{
						Kind: RegressionImprovement, Scenario: cur.Scenario, Subject: cur.Subject,
						Ratio:  ratio,
						Detail: fmt.Sprintf("faster: %s → %s", wasTime, nowTime),
					})
				}
			}
		}
	}

	sortRegressions(report.Regressions)
	sortRegressions(report.Improvements)
	return report
}

// kindForSubject maps a scenario kind to the regression vocabulary, so a
// regression report speaks in the brief's terms rather than the scenario's.
func kindForSubject(k ScenarioKind) RegressionKind {
	switch k {
	case KindConversation:
		return RegressionConversation
	case KindMemory:
		return RegressionMemory
	case KindGovernance:
		return RegressionPolicy
	case KindTool:
		return RegressionPlanning
	default:
		return RegressionBehaviour
	}
}

func sortRegressions(r []Regression) {
	sort.SliceStable(r, func(i, j int) bool {
		if r[i].Subject != r[j].Subject {
			return r[i].Subject < r[j].Subject
		}
		if r[i].Scenario != r[j].Scenario {
			return r[i].Scenario < r[j].Scenario
		}
		return r[i].Kind < r[j].Kind
	})
}

// ---------------------------------------------------------------------------
// Benchmark framework
// ---------------------------------------------------------------------------

// Distribution summarises a sample of durations.
//
// PERCENTILES, NOT A MEAN. A mean hides the tail, and the tail is where a
// conversational system lives or dies: a p50 of 40 ms with a p99 of 4 s is a
// system that fails one call in a hundred, and a mean of 80 ms says nothing
// about it.
type Distribution struct {
	// Samples is how many observations.
	Samples int
	// Min, Max, Mean, P50, P95 and P99 summarise them.
	Min, Max, Mean, P50, P95, P99 time.Duration
	// StdDev measures spread. Reported because a wide spread with a good p50
	// is a subsystem whose behaviour depends on something nobody has modelled.
	StdDev time.Duration
}

// String renders the distribution.
func (d Distribution) String() string {
	return fmt.Sprintf("n=%d p50=%s p95=%s p99=%s max=%s σ=%s",
		d.Samples, d.P50, d.P95, d.P99, d.Max, d.StdDev)
}

// Summarise builds a distribution from durations.
//
// A PURE FUNCTION over a copy: the input slice is not reordered, because a
// caller that passed its own recorded samples would find them sorted afterwards
// and any ordering it relied on silently gone.
func Summarise(samples []time.Duration) Distribution {
	if len(samples) == 0 {
		return Distribution{}
	}

	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	d := Distribution{
		Samples: len(sorted),
		Min:     sorted[0],
		Max:     sorted[len(sorted)-1],
		P50:     percentile(sorted, 0.50),
		P95:     percentile(sorted, 0.95),
		P99:     percentile(sorted, 0.99),
	}

	var total float64
	for _, s := range sorted {
		total += float64(s)
	}
	mean := total / float64(len(sorted))
	d.Mean = time.Duration(mean)

	var variance float64
	for _, s := range sorted {
		diff := float64(s) - mean
		variance += diff * diff
	}
	d.StdDev = time.Duration(math.Sqrt(variance / float64(len(sorted))))
	return d
}

func percentile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(q * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// BenchmarkResult is one scenario's measured performance.
type BenchmarkResult struct {
	// Scenario and Subject identify what was measured.
	Scenario ScenarioID
	Subject  SubjectName
	// Iterations is how many times it ran, excluding warmup.
	Iterations int
	// Warmup is how many were discarded before measuring.
	Warmup int
	// Total is the distribution of whole-scenario step time.
	Total Distribution
	// PerStep is each step's distribution, keyed by step name.
	PerStep map[string]Distribution
	// BehaviourStable reports that every iteration behaved identically.
	//
	// REPORTED ALONGSIDE THE TIMINGS ON PURPOSE. A benchmark over a scenario
	// that behaved differently on different iterations is a benchmark of
	// several different things averaged together, and its percentiles mean
	// nothing. A reader must be able to see that before trusting the numbers.
	BehaviourStable bool
	// Behaviours lists the distinct fingerprints observed.
	Behaviours []Fingerprint
	// AmortisedMean is the total elapsed time across every measured iteration
	// divided by the iteration count.
	//
	// THE ONLY TRUSTWORTHY FIGURE WHEN THE WORK IS FASTER THAN THE CLOCK.
	// Per-iteration timing of a 300-nanosecond operation on a clock with
	// half-millisecond granularity yields a distribution of zeros and
	// occasional half-milliseconds — a p50 of 0 and a p99 that measures the
	// scheduler. Timing the whole loop once and dividing is immune to that,
	// because the quantisation error is amortised across every iteration
	// instead of applied to each.
	//
	// It cannot tell you about the tail, which is why the percentiles are still
	// reported alongside it rather than replaced by it.
	AmortisedMean time.Duration
	// Resolution is the clock granularity the measurement was taken at.
	Resolution time.Duration
	// BelowResolution reports that the per-iteration percentiles are smaller
	// than the clock can distinguish and must not be believed.
	BelowResolution bool
	// At is when it ran.
	At time.Time
	// Label is the operator-supplied run name.
	Label string
}

// Summary renders the result.
//
// The amortised mean leads when the percentiles are below the clock's
// resolution, because in that case the percentiles are noise and putting them
// first invites somebody to read them.
func (b BenchmarkResult) Summary() string {
	notes := ""
	if !b.BehaviourStable {
		notes += " [UNSTABLE BEHAVIOUR — percentiles average different things]"
	}
	if b.BelowResolution {
		notes += fmt.Sprintf(" [BELOW CLOCK RESOLUTION %s — trust the amortised mean, "+
			"not the percentiles]", b.Resolution)
	}
	return fmt.Sprintf("%s on %s: amortised=%s %s%s",
		b.Scenario, b.Subject, b.AmortisedMean, b.Total, notes)
}

// Benchmark measures a scenario over many iterations.
//
// Warmup iterations are discarded. Not superstition: the first run of anything
// in this platform allocates maps, warms a policy snapshot and populates a
// registry cache, and including it makes a p99 that describes the first call
// rather than the steady state.
func (e *EvaluationRuntime) Benchmark(ctx context.Context, s Scenario, iterations, warmup int, label string) BenchmarkResult {
	if iterations <= 0 {
		iterations = e.cfg.BenchmarkIterations
	}
	if warmup < 0 {
		warmup = e.cfg.BenchmarkWarmup
	}

	result := BenchmarkResult{
		Scenario: s.ID, Subject: s.SubjectName, Iterations: iterations,
		Warmup: warmup, PerStep: map[string]Distribution{},
		At: e.clock.Now(), Label: label, BehaviourStable: true,
	}

	for i := 0; i < warmup; i++ {
		_ = e.Execute(ctx, s)
	}

	totals := make([]time.Duration, 0, iterations)
	perStep := make(map[string][]time.Duration)
	seen := make(map[Fingerprint]bool)

	// The whole loop is timed once as well as each iteration, so the amortised
	// mean survives a clock too coarse to time one iteration.
	loopStart := e.clock.Now()
	for i := 0; i < iterations; i++ {
		obs := e.Execute(ctx, s).Observation
		totals = append(totals, obs.TotalStepTime())
		for _, step := range obs.Steps {
			perStep[step.Name] = append(perStep[step.Name], step.Duration)
		}
		print := obs.BehaviourPrint()
		if !seen[print] {
			seen[print] = true
			result.Behaviours = append(result.Behaviours, print)
		}
	}
	elapsed := e.clock.Since(loopStart)
	if iterations > 0 {
		result.AmortisedMean = elapsed / time.Duration(iterations)
	}
	result.Resolution = e.resolution

	sort.Slice(result.Behaviours, func(i, j int) bool {
		return result.Behaviours[i] < result.Behaviours[j]
	})
	result.BehaviourStable = len(result.Behaviours) <= 1
	result.Total = Summarise(totals)
	for name, samples := range perStep {
		result.PerStep[name] = Summarise(samples)
	}
	result.BelowResolution = !MeasurableAt(result.Total.P50, e.resolution)

	if err := e.storage.PutBenchmark(result); err != nil && e.metrics != nil {
		e.metrics.StorageErrors.Inc("benchmark")
	}
	return result
}

// BenchmarkComparison compares two benchmark results.
type BenchmarkComparison struct {
	Scenario ScenarioID
	Subject  SubjectName
	// P50Ratio, P95Ratio and P99Ratio are current over baseline.
	P50Ratio, P95Ratio, P99Ratio float64
	// Regressed reports that any percentile exceeded the tolerance.
	Regressed bool
	// Improved reports that every percentile got better by more than the
	// tolerance.
	Improved bool
	// Detail explains the verdict.
	Detail string
}

// CompareBenchmarks measures one benchmark against another.
//
// It compares p50, p95 AND p99. A change that moves only the tail is the most
// dangerous kind — it is invisible in a mean, invisible in a p50, and it is
// exactly what a queue starting to back up looks like.
func CompareBenchmarks(baseline, current BenchmarkResult, tolerance float64) BenchmarkComparison {
	c := BenchmarkComparison{Scenario: current.Scenario, Subject: current.Subject}

	ratio := func(was, now time.Duration) float64 {
		if was <= 0 {
			return 0
		}
		return float64(now) / float64(was)
	}
	c.P50Ratio = ratio(baseline.Total.P50, current.Total.P50)
	c.P95Ratio = ratio(baseline.Total.P95, current.Total.P95)
	c.P99Ratio = ratio(baseline.Total.P99, current.Total.P99)

	if tolerance <= 0 {
		tolerance = 1.5
	}

	var worst float64
	for _, r := range []float64{c.P50Ratio, c.P95Ratio, c.P99Ratio} {
		if r > worst {
			worst = r
		}
	}
	c.Regressed = worst > tolerance

	c.Improved = c.P50Ratio > 0 && c.P95Ratio > 0 && c.P99Ratio > 0 &&
		c.P50Ratio < 1/tolerance && c.P95Ratio < 1/tolerance && c.P99Ratio < 1/tolerance

	// When either side's percentiles are below the clock's resolution, the
	// comparison uses the amortised mean instead. Comparing two distributions
	// of zeros produces a ratio of zero and a confident "within tolerance",
	// which is the exact false negative this whole mechanism exists to avoid.
	if baseline.BelowResolution || current.BelowResolution {
		if baseline.AmortisedMean > 0 {
			r := float64(current.AmortisedMean) / float64(baseline.AmortisedMean)
			c.P50Ratio, c.P95Ratio, c.P99Ratio = r, r, r
			c.Regressed = r > tolerance
			c.Improved = r > 0 && r < 1/tolerance
			c.Detail = fmt.Sprintf("compared on the amortised mean (percentiles are "+
				"below the clock resolution): ×%.2f", r)
			return c
		}
	}

	switch {
	case !baseline.BehaviourStable || !current.BehaviourStable:
		// Stated first, because it invalidates everything below it.
		c.Detail = "behaviour was unstable; the percentiles average different things"
	case c.Regressed:
		c.Detail = fmt.Sprintf("slower: p50 ×%.2f p95 ×%.2f p99 ×%.2f",
			c.P50Ratio, c.P95Ratio, c.P99Ratio)
	case c.Improved:
		c.Detail = fmt.Sprintf("faster: p50 ×%.2f p95 ×%.2f p99 ×%.2f",
			c.P50Ratio, c.P95Ratio, c.P99Ratio)
	default:
		c.Detail = "within tolerance"
	}
	return c
}
