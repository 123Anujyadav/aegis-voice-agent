package evaluation

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// SuiteKind classifies what a suite is for.
//
// The six the brief enumerates. They differ in what a failing result MEANS,
// which is the only distinction worth encoding: an acceptance suite failing
// blocks a release, a benchmark suite failing means something got slower, and a
// golden suite failing means somebody has not reviewed a change.
type SuiteKind uint8

// The suite kinds.
const (
	// KindEvaluation is general behaviour evaluation.
	KindEvaluation SuiteKind = iota
	// KindGolden compares against approved baselines.
	KindGolden
	// KindBenchmark measures distributions rather than single observations.
	KindBenchmark
	// KindRegression compares a run against a previous run.
	KindRegression
	// KindAcceptance gates a release. The only kind whose failure is meant to
	// stop a deploy on its own.
	KindAcceptance
	// KindCompliance evidences a regulatory obligation. Its results are
	// retained longer and its failures are reported to a different audience.
	KindCompliance
)

// String renders the kind. Used as a metric label and a report heading.
func (k SuiteKind) String() string {
	switch k {
	case KindGolden:
		return "golden"
	case KindBenchmark:
		return "benchmark"
	case KindRegression:
		return "regression"
	case KindAcceptance:
		return "acceptance"
	case KindCompliance:
		return "compliance"
	default:
		return "evaluation"
	}
}

// AllSuiteKinds returns every kind, in declaration order.
func AllSuiteKinds() []SuiteKind {
	return []SuiteKind{KindEvaluation, KindGolden, KindBenchmark, KindRegression,
		KindAcceptance, KindCompliance}
}

// Gating reports whether a failure in this suite should stop a release.
func (k SuiteKind) Gating() bool { return k == KindAcceptance || k == KindCompliance }

// Suite is a named collection of scenarios.
type Suite struct {
	// ID identifies it.
	ID SuiteID
	// Kind classifies it.
	Kind SuiteKind
	// Title and Description document it.
	Title       string
	Description string
	// Owner names the team accountable.
	Owner string
	// Scenarios are the scenario identifiers, in declaration order.
	Scenarios []ScenarioID
	// Parallel permits scenarios to run concurrently.
	//
	// Off by default. Concurrency is correct for most suites and wrong for any
	// that measures latency, because sixteen scenarios sharing a CPU produce
	// timings that mean nothing. A benchmark suite that set this would be
	// measuring contention.
	Parallel bool
	// Tags carry operator metadata.
	Tags map[string]string
}

func (s Suite) validate(known map[ScenarioID]bool) []string {
	var problems []string
	where := string(s.ID)
	if where == "" {
		where = "<unnamed suite>"
		problems = append(problems, "suite: id is required")
	}
	if s.Owner == "" {
		problems = append(problems, where+": owner is required")
	}
	if s.Kind > KindCompliance {
		problems = append(problems, where+": unknown kind")
	}
	if len(s.Scenarios) == 0 {
		problems = append(problems, where+": at least one scenario is required; an "+
			"empty suite reports a perfect pass rate over nothing")
	}
	if s.Parallel && s.Kind == KindBenchmark {
		problems = append(problems, where+": a benchmark suite must not run in "+
			"parallel; scenarios sharing a CPU produce timings that measure "+
			"contention rather than the subject")
	}

	seen := make(map[ScenarioID]bool, len(s.Scenarios))
	for _, id := range s.Scenarios {
		if !known[id] {
			problems = append(problems, fmt.Sprintf(
				"%s: references unregistered scenario %s", where, id))
		}
		if seen[id] {
			problems = append(problems, fmt.Sprintf("%s: lists %s twice", where, id))
		}
		seen[id] = true
	}
	return problems
}

// Registry holds scenarios and suites.
//
// COPY-ON-WRITE. Reads take no lock: they load an immutable snapshot pointer.
//
// The property that matters more than the speed: **a run is executed against
// exactly one snapshot.** Registering a scenario part-way through a run cannot
// produce a report that covers half one set and half another, and the snapshot
// version travels in the [Run] so a report names the scenario set it came from.
type Registry struct {
	clock rt.Clock

	snapshot atomic.Pointer[registrySnapshot]
	writeMu  sync.Mutex
	version  atomic.Uint64
	metrics  *Metrics
}

type registrySnapshot struct {
	version   uint64
	scenarios map[ScenarioID]Scenario
	suites    map[SuiteID]Suite
	order     []ScenarioID
	suiteIDs  []SuiteID
	digest    Fingerprint
	builtAt   time.Time
}

// NewRegistry builds an empty registry.
func NewRegistry(clock rt.Clock, m *Metrics) *Registry {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	r := &Registry{clock: clock, metrics: m}
	r.snapshot.Store(&registrySnapshot{
		scenarios: map[ScenarioID]Scenario{},
		suites:    map[SuiteID]Suite{},
		builtAt:   clock.Now(),
	})
	return r
}

// RegisterScenarios adds scenarios atomically.
//
// ALL OR NOTHING. A partial registration leaves the platform evaluating half a
// scenario set, and which half depends on the order somebody wrote the calls in
// — the same argument Phase 10E makes about policy loading, with the same
// consequence: a report that looks complete and is not.
func (r *Registry) RegisterScenarios(scenarios ...Scenario) error {
	var problems []string
	seen := make(map[ScenarioID]bool, len(scenarios))
	for _, s := range scenarios {
		problems = append(problems, s.validate()...)
		if seen[s.ID] {
			problems = append(problems, fmt.Sprintf("%s: registered twice in one batch", s.ID))
		}
		seen[s.ID] = true
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return &ConfigError{Problems: problems}
	}

	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	old := r.snapshot.Load()
	for _, s := range scenarios {
		if prev, exists := old.scenarios[s.ID]; exists && s.Version < prev.Version {
			return &ConfigError{Problems: []string{fmt.Sprintf(
				"%s: version %d is below the registered version %d; a scenario does not "+
					"go backwards, and pretending it does compares a run against a golden "+
					"recorded from a later question", s.ID, s.Version, prev.Version)}}
		}
	}

	r.commit(old, func(sc map[ScenarioID]Scenario, su map[SuiteID]Suite) {
		for _, s := range scenarios {
			sc[s.ID] = s
		}
	})
	return nil
}

// RegisterSuites adds suites atomically.
//
// Validated against the CURRENT scenario set, so a suite naming a scenario that
// does not exist is refused rather than producing a run that silently skips it.
func (r *Registry) RegisterSuites(suites ...Suite) error {
	snap := r.snapshot.Load()
	known := make(map[ScenarioID]bool, len(snap.scenarios))
	for id := range snap.scenarios {
		known[id] = true
	}

	var problems []string
	seen := make(map[SuiteID]bool, len(suites))
	for _, s := range suites {
		problems = append(problems, s.validate(known)...)
		if seen[s.ID] {
			problems = append(problems, fmt.Sprintf("%s: registered twice in one batch", s.ID))
		}
		seen[s.ID] = true
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return &ConfigError{Problems: problems}
	}

	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.commit(r.snapshot.Load(), func(sc map[ScenarioID]Scenario, su map[SuiteID]Suite) {
		for _, s := range suites {
			su[s.ID] = s
		}
	})
	return nil
}

func (r *Registry) commit(old *registrySnapshot, mutate func(map[ScenarioID]Scenario, map[SuiteID]Suite)) {
	scenarios := make(map[ScenarioID]Scenario, len(old.scenarios)+1)
	for k, v := range old.scenarios {
		scenarios[k] = v
	}
	suites := make(map[SuiteID]Suite, len(old.suites)+1)
	for k, v := range old.suites {
		suites[k] = v
	}
	mutate(scenarios, suites)

	snap := &registrySnapshot{
		version:   r.version.Add(1),
		scenarios: scenarios,
		suites:    suites,
		order:     make([]ScenarioID, 0, len(scenarios)),
		suiteIDs:  make([]SuiteID, 0, len(suites)),
		builtAt:   r.clock.Now(),
	}
	for id := range scenarios {
		snap.order = append(snap.order, id)
	}
	sort.Slice(snap.order, func(i, j int) bool {
		a, b := scenarios[snap.order[i]], scenarios[snap.order[j]]
		if a.SubjectName != b.SubjectName {
			return a.SubjectName < b.SubjectName
		}
		return a.ID < b.ID
	})
	for id := range suites {
		snap.suiteIDs = append(snap.suiteIDs, id)
	}
	sort.Slice(snap.suiteIDs, func(i, j int) bool { return snap.suiteIDs[i] < snap.suiteIDs[j] })

	// The digest covers every scenario's executable content in snapshot order.
	// Two evaluation platforms with the same digest are asking the same
	// questions, which is what a cross-environment comparison needs to know
	// before it compares any answers.
	var buf []byte
	for _, id := range snap.order {
		buf = append(buf, scenarios[id].Digest()...)
		buf = append(buf, ';')
	}
	snap.digest = fingerprintOf(buf)

	r.snapshot.Store(snap)
	if r.metrics != nil {
		r.metrics.Registered.Set(float64(len(scenarios)))
	}
}

// Scenario returns a scenario.
func (r *Registry) Scenario(id ScenarioID) (Scenario, bool) {
	s, ok := r.snapshot.Load().scenarios[id]
	return s, ok
}

// Suite returns a suite.
func (r *Registry) Suite(id SuiteID) (Suite, bool) {
	s, ok := r.snapshot.Load().suites[id]
	return s, ok
}

// Scenarios returns every scenario, sorted by subject then identifier.
func (r *Registry) Scenarios() ScenarioSet {
	snap := r.snapshot.Load()
	out := make(ScenarioSet, 0, len(snap.order))
	for _, id := range snap.order {
		out = append(out, snap.scenarios[id])
	}
	return out
}

// SuiteScenarios resolves a suite's scenarios, in the suite's declared order.
//
// Declaration order rather than sorted, because a suite author who ordered
// scenarios deliberately — set up, exercise, tear down — meant it, and a
// registry that reordered them would break exactly the suites that were
// thought about.
func (r *Registry) SuiteScenarios(id SuiteID) (ScenarioSet, error) {
	snap := r.snapshot.Load()
	suite, ok := snap.suites[id]
	if !ok {
		return nil, fmt.Errorf("%w: suite %s", ErrNotRegistered, id)
	}
	out := make(ScenarioSet, 0, len(suite.Scenarios))
	for _, sid := range suite.Scenarios {
		s, ok := snap.scenarios[sid]
		if !ok {
			return nil, fmt.Errorf("%w: suite %s references scenario %s",
				ErrNotRegistered, id, sid)
		}
		out = append(out, s)
	}
	return out, nil
}

// Suites returns every suite, sorted.
func (r *Registry) Suites() []Suite {
	snap := r.snapshot.Load()
	out := make([]Suite, 0, len(snap.suiteIDs))
	for _, id := range snap.suiteIDs {
		out = append(out, snap.suites[id])
	}
	return out
}

// Version returns the current snapshot version.
func (r *Registry) Version() uint64 { return r.snapshot.Load().version }

// Digest fingerprints the whole scenario set's executable content.
func (r *Registry) Digest() Fingerprint { return r.snapshot.Load().digest }

// Len returns the scenario count.
func (r *Registry) Len() int { return len(r.snapshot.Load().scenarios) }

// Coverage reports scenario counts per kind and per subject.
//
// The operator-facing answer to "what are we actually evaluating". A subject
// with zero scenarios is the useful output: it means a subsystem is shipping
// with no evaluation at all, which no pass rate would ever reveal.
type Coverage struct {
	// ByKind counts scenarios per scenario kind, including kinds with none.
	ByKind map[ScenarioKind]int
	// BySubject counts scenarios per subject.
	BySubject map[SubjectName]int
	// BySuiteKind counts suites per suite kind, including kinds with none.
	BySuiteKind map[SuiteKind]int
	// Total is the scenario count.
	Total int
}

// Coverage builds a coverage report.
func (r *Registry) Coverage() Coverage {
	scenarios := r.Scenarios()

	c := Coverage{
		ByKind:      scenarios.Coverage(),
		BySubject:   make(map[SubjectName]int),
		BySuiteKind: make(map[SuiteKind]int, len(AllSuiteKinds())),
		Total:       len(scenarios),
	}
	for _, k := range AllSuiteKinds() {
		c.BySuiteKind[k] = 0
	}
	for _, s := range scenarios {
		c.BySubject[s.SubjectName]++
	}
	for _, s := range r.Suites() {
		c.BySuiteKind[s.Kind]++
	}
	return c
}

// UncoveredKinds returns the scenario kinds with no scenarios, sorted.
func (c Coverage) UncoveredKinds() []ScenarioKind {
	var out []ScenarioKind
	for _, k := range AllScenarioKinds() {
		if c.ByKind[k] == 0 {
			out = append(out, k)
		}
	}
	return out
}
