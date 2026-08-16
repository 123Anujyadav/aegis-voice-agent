package evaluation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func newHarness(t *testing.T) *Harness {
	t.Helper()
	h, err := NewHarness()
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	return h
}

// ---------------------------------------------------------------------------
// Values and fingerprints
// ---------------------------------------------------------------------------

// TestValues_FingerprintIgnoresMapOrder is the property every golden comparison
// rests on. If an observation's fingerprint depended on map iteration order,
// every scenario would drift on every run and the platform would report nothing
// but noise.
func TestValues_FingerprintIgnoresMapOrder(t *testing.T) {
	t.Parallel()

	a := Values{"zulu": S("z"), "alpha": N(1), "mid": B(true)}
	b := Values{"mid": B(true), "alpha": N(1), "zulu": S("z")}

	if a.Fingerprint() != b.Fingerprint() {
		t.Fatalf("same values fingerprinted differently: %s vs %s",
			a.Fingerprint(), b.Fingerprint())
	}
	first := a.Fingerprint()
	for i := 0; i < 200; i++ {
		if got := a.Fingerprint(); got != first {
			t.Fatalf("fingerprint drifted on run %d", i)
		}
	}
}

func TestValues_DistinctValuesDoNotCollide(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		a, b Values
	}{
		{"string vs number", Values{"k": S("1")}, Values{"k": N(1)}},
		{"bool vs string", Values{"k": B(true)}, Values{"k": S("true")}},
		{"absent vs missing", Values{"k": Absent()}, Values{}},
		{"key/value swap", Values{"a": S("b")}, Values{"b": S("a")}},
	}
	for _, tc := range cases {
		if tc.a.Fingerprint() == tc.b.Fingerprint() {
			t.Errorf("%s: distinct values share a fingerprint", tc.name)
		}
	}
}

func TestValues_DiffNamesWhatChanged(t *testing.T) {
	t.Parallel()

	was := Values{"same": S("x"), "changed": N(1), "removed": B(true)}
	now := Values{"same": S("x"), "changed": N(2), "added": S("new")}

	diff := was.Diff(now)
	if fmt.Sprint(diff) != "[added changed removed]" {
		t.Fatalf("diff is %v, want sorted [added changed removed]", diff)
	}
}

// ---------------------------------------------------------------------------
// Observations: behaviour versus time
// ---------------------------------------------------------------------------

// TestObservation_BehaviourExcludesTiming is the platform's most important
// encoding decision, stated as a test. Two runs of a deterministic system
// produce identical outputs and different durations.
func TestObservation_BehaviourExcludesTiming(t *testing.T) {
	t.Parallel()

	base := Observation{
		Scenario: "s", Subject: "subj", ScenarioVersion: 1,
		Steps: []StepObservation{{
			Name: "a", Op: "op", Outcome: "ok",
			Output: Values{"x": N(1)}, State: Values{"n": N(1)},
			Duration: time.Millisecond,
		}},
	}

	slower := base.Clone()
	slower.Steps[0].Duration = time.Hour
	if base.BehaviourPrint() != slower.BehaviourPrint() {
		t.Fatal("a slower run produced a different behaviour print; determinism " +
			"checks would fail on timing jitter")
	}

	// Detail is excluded too: improving an error message must not be drift.
	reworded := base.Clone()
	reworded.Steps[0].Detail = "a much better explanation"
	if base.BehaviourPrint() != reworded.BehaviourPrint() {
		t.Fatal("changing a detail string produced drift")
	}

	// Seeds and timestamps are excluded.
	reseeded := base.Clone()
	reseeded.Seed = 999
	reseeded.StartedAt = time.Now()
	if base.BehaviourPrint() != reseeded.BehaviourPrint() {
		t.Fatal("a seed or timestamp leaked into the behaviour print")
	}
}

func TestObservation_BehaviourCapturesEveryObservableChange(t *testing.T) {
	t.Parallel()

	base := Observation{
		Scenario: "s", Subject: "subj", ScenarioVersion: 1,
		Steps: []StepObservation{{
			Name: "a", Op: "op", Outcome: "ok",
			Output: Values{"x": N(1)}, State: Values{"n": N(1)},
			Events: []EventRecord{{Type: "emitted", Fields: Values{"k": S("v")}}},
		}},
	}

	mutations := []struct {
		name   string
		mutate func(*Observation)
	}{
		{"outcome", func(o *Observation) { o.Steps[0].Outcome = "denied" }},
		{"output", func(o *Observation) { o.Steps[0].Output = Values{"x": N(2)} }},
		{"state", func(o *Observation) { o.Steps[0].State = Values{"n": N(2)} }},
		{"failure", func(o *Observation) { o.Steps[0].Failed = true }},
		{"event type", func(o *Observation) { o.Steps[0].Events[0].Type = "other" }},
		{"event field", func(o *Observation) { o.Steps[0].Events[0].Fields = Values{"k": S("w")} }},
		{"injection", func(o *Observation) { o.Steps[0].Injected = "timeout" }},
		{"scenario version", func(o *Observation) { o.ScenarioVersion = 2 }},
		{"step name", func(o *Observation) { o.Steps[0].Name = "b" }},
	}

	for _, m := range mutations {
		changed := base.Clone()
		m.mutate(&changed)
		if changed.BehaviourPrint() == base.BehaviourPrint() {
			t.Errorf("%s changed but the behaviour print did not", m.name)
		}
	}
}

func TestObservation_CloneIsIndependent(t *testing.T) {
	t.Parallel()

	base := Observation{
		Scenario: "s", Steps: []StepObservation{{
			Name: "a", Output: Values{"x": N(1)}, State: Values{"n": N(1)},
			Events: []EventRecord{{Type: "e", Fields: Values{"k": S("v")}}},
		}},
		Metrics: map[string]float64{"m": 1},
	}
	print := base.BehaviourPrint()

	c := base.Clone()
	c.Steps[0].Output["x"] = N(99)
	c.Steps[0].State["n"] = N(99)
	c.Steps[0].Events[0].Fields["k"] = S("hijacked")
	c.Metrics["m"] = 99

	if base.BehaviourPrint() != print {
		t.Fatal("mutating a clone changed the original; a report renderer could " +
			"corrupt a golden")
	}
}

// ---------------------------------------------------------------------------
// Scenarios
// ---------------------------------------------------------------------------

func TestScenario_ValidationCatchesTheDangerousMistakes(t *testing.T) {
	t.Parallel()

	base := func() Scenario {
		return Scenario{ID: "s", Version: 1, Owner: "team", SubjectName: "subj",
			Steps: []Step{{Name: "a", Op: "op"}}}
	}

	cases := []struct {
		name     string
		mutate   func(*Scenario)
		contains string
	}{
		{"no id", func(s *Scenario) { s.ID = "" }, "id is required"},
		{"no owner", func(s *Scenario) { s.Owner = "" }, "owner is required"},
		{"no subject", func(s *Scenario) { s.SubjectName = "" }, "subject is required"},
		{"version zero", func(s *Scenario) { s.Version = 0 }, "version must be at least 1"},
		{"no steps", func(s *Scenario) { s.Steps = nil }, "passes forever"},
		{"anonymous step", func(s *Scenario) { s.Steps[0].Name = "" }, "name is required"},
		{"no op", func(s *Scenario) { s.Steps[0].Op = "" }, "op is required"},
		{"duplicate step names", func(s *Scenario) {
			s.Steps = append(s.Steps, Step{Name: "a", Op: "other"})
		}, "duplicate step name"},
		{"unknown injection", func(s *Scenario) {
			s.Steps[0].Inject = &Failure{Kind: "made_up"}
		}, "unknown injection kind"},
		{"tolerance below one", func(s *Scenario) {
			s.Tolerances = Tolerances{LatencyRatio: 0.5}
		}, "fails every run that is not faster"},
	}

	for _, tc := range cases {
		s := base()
		tc.mutate(&s)
		err := s.Validate()
		if err == nil {
			t.Errorf("%s: expected a validation error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.contains) {
			t.Errorf("%s: expected %q, got %v", tc.name, tc.contains, err)
		}
	}
}

func TestScenario_DigestIgnoresDocumentation(t *testing.T) {
	t.Parallel()

	a := SimpleScenario("s", "subj", "op")
	b := a
	b.Title = "a much better title"
	b.Description = "and a description"
	b.Owner = "a different team"
	b.Tags = map[string]string{"x": "y"}

	if a.Digest() != b.Digest() {
		t.Fatal("documentation changed the digest; a change review would report a " +
			"question change that did not happen")
	}

	c := a
	c.Steps = append(c.Steps, Step{Name: "extra", Op: "op"})
	if a.Digest() == c.Digest() {
		t.Fatal("adding a step did not change the digest")
	}
}

// ---------------------------------------------------------------------------
// Goldens
// ---------------------------------------------------------------------------

// TestGolden_RecordingNeverApproves is the platform's central safeguard. A
// platform that updates its own baseline reports no drift, ever.
func TestGolden_RecordingNeverApproves(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := SimpleScenario("s", "fake", "op")
	h.Register(s)

	res := h.Runtime.Execute(context.Background(), s)
	candidate, err := h.Runtime.Goldens().Record(s, res.Observation)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Approved() {
		t.Fatal("recording produced an approved golden; the platform would agree " +
			"with whatever it saw last")
	}
	if _, err := h.Runtime.Goldens().Baseline(s.Key()); !errors.Is(err, ErrNoGolden) {
		t.Fatalf("a candidate became the baseline: %v", err)
	}
	// Two candidates: Execute auto-filed one when it found no baseline, and the
	// explicit Record above filed another. Both are unapproved, which is the
	// property under test — a platform that accumulates candidates is fine, a
	// platform that promotes one is not.
	pending := h.Runtime.Goldens().PendingApprovals()
	if len(pending) != 2 {
		t.Fatalf("expected 2 candidates awaiting review, got %d", len(pending))
	}
	for _, p := range pending {
		if p.Approved() {
			t.Fatal("a candidate was approved without anybody approving it")
		}
	}
}

func TestGolden_ApprovalRequiresAnAuthorAndAReason(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := SimpleScenario("s", "fake", "op")
	h.Register(s)

	res := h.Runtime.Execute(context.Background(), s)
	candidate, _ := h.Runtime.Goldens().Record(s, res.Observation)

	if _, err := h.Runtime.Goldens().Approve(candidate.ID, "", "reason"); err == nil {
		t.Fatal("an unattributed approval was accepted")
	}
	if _, err := h.Runtime.Goldens().Approve(candidate.ID, "someone", ""); err == nil {
		t.Fatal("an unjustified approval was accepted")
	}

	g, err := h.Runtime.Goldens().Approve(candidate.ID, "reviewer", "checked and correct")
	if err != nil {
		t.Fatal(err)
	}
	if !g.Approved() || g.ApprovedBy != "reviewer" {
		t.Fatalf("approval not recorded: %+v", g)
	}
	if _, err := h.Runtime.Goldens().Approve(candidate.ID, "other", "again"); !errors.Is(err, ErrAlreadyApproved) {
		t.Fatalf("a golden was approved twice: %v", err)
	}
}

func TestGolden_SupersededBaselinesAreRetained(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := SimpleScenario("s", "fake", "op")
	h.Register(s)

	first := h.Approve(s)
	h.Fake.Handlers["op"] = func(Step, int) StepResult {
		return StepResult{Outcome: "ok", Output: Values{"changed": B(true)}}
	}
	second := h.Approve(s)

	if second.Supersedes != first.ID {
		t.Errorf("the new baseline does not name what it replaced")
	}
	history := h.Runtime.Goldens().History(s.Key())
	if len(history) < 2 {
		t.Fatalf("history holds %d records; 'what did we consider correct in March' "+
			"must be answerable", len(history))
	}
}

// ---------------------------------------------------------------------------
// Comparison
// ---------------------------------------------------------------------------

func TestCompare_DistinguishesDriftFromFailure(t *testing.T) {
	t.Parallel()

	baseline := Observation{Scenario: "s", ScenarioVersion: 1,
		Steps: []StepObservation{{Name: "a", Op: "op", Outcome: "ok",
			Output: Values{"x": N(1)}}}}
	g := Golden{ID: "g", State: GoldenApproved, Observation: baseline,
		Behaviour: baseline.BehaviourPrint()}

	same := Compare(g, baseline, DefaultTolerances())
	if same.Verdict != VerdictPass {
		t.Fatalf("an identical observation produced %s", same.Verdict)
	}

	changed := baseline.Clone()
	changed.Steps[0].Output = Values{"x": N(2)}
	drift := Compare(g, changed, DefaultTolerances())
	if drift.Verdict != VerdictDrift {
		t.Fatalf("a changed output produced %s, want drift", drift.Verdict)
	}
	if len(drift.Differences) == 0 {
		t.Error("drift reported no differences")
	}

	broken := baseline.Clone()
	broken.Steps[0].Failed = true
	fail := Compare(g, broken, DefaultTolerances())
	if fail.Verdict != VerdictFail {
		t.Fatalf("a failed step produced %s, want fail", fail.Verdict)
	}
	if drift.Verdict.Blocking() {
		t.Error("drift is blocking; a deliberate improvement would stop a release")
	}
	if !fail.Verdict.Blocking() {
		t.Error("failure is not blocking")
	}
}

// TestCompare_RefusesACrossVersionComparison covers the trap that produces
// drift nobody can explain.
func TestCompare_RefusesACrossVersionComparison(t *testing.T) {
	t.Parallel()

	baseline := Observation{Scenario: "s", ScenarioVersion: 1,
		Steps: []StepObservation{{Name: "a", Outcome: "ok"}}}
	g := Golden{ID: "g", State: GoldenApproved, Observation: baseline,
		Behaviour: baseline.BehaviourPrint()}

	newer := baseline.Clone()
	newer.ScenarioVersion = 2

	c := Compare(g, newer, DefaultTolerances())
	if c.Verdict != VerdictFail || c.Reason != "scenario_version_mismatch" {
		t.Fatalf("a cross-version comparison produced %s/%s", c.Verdict, c.Reason)
	}
}

// TestCompare_BehaviourHasNoTolerance is the line between measurement and
// behaviour: a "mostly the same answer" threshold is a threshold somebody
// raises until the drift stops being reported.
func TestCompare_BehaviourHasNoTolerance(t *testing.T) {
	t.Parallel()

	baseline := Observation{Scenario: "s", ScenarioVersion: 1,
		Steps: []StepObservation{{Name: "a", Outcome: "ok", Output: Values{"n": N(100)}}}}
	g := Golden{ID: "g", State: GoldenApproved, Observation: baseline,
		Behaviour: baseline.BehaviourPrint()}

	// A one-part-in-a-hundred output change, with the loosest tolerances the
	// type can express. Still drift.
	tiny := baseline.Clone()
	tiny.Steps[0].Output = Values{"n": N(101)}

	c := Compare(g, tiny, Tolerances{LatencyRatio: 1000, LatencyFloor: time.Hour,
		MetricDeltas: map[string]float64{"n": 100}})
	if c.Verdict != VerdictDrift {
		t.Fatalf("a behaviour change was tolerated away: %s", c.Verdict)
	}
}

func TestCompare_LatencyFloorSuppressesNoise(t *testing.T) {
	t.Parallel()

	baseline := Observation{Scenario: "s", ScenarioVersion: 1,
		Steps: []StepObservation{{Name: "a", Outcome: "ok", Duration: 3 * time.Microsecond}}}
	g := Golden{ID: "g", State: GoldenApproved, Observation: baseline,
		Behaviour: baseline.BehaviourPrint()}

	// A 3 µs step becoming 9 µs is a 3× ratio and pure scheduler noise.
	noisy := baseline.Clone()
	noisy.Steps[0].Duration = 9 * time.Microsecond

	c := Compare(g, noisy, Tolerances{LatencyRatio: 2, LatencyFloor: 50 * time.Microsecond})
	if c.Verdict != VerdictPass {
		t.Fatalf("sub-floor jitter reported as %s: %v", c.Verdict, c.Differences)
	}

	// The same ratio above the floor is reported.
	real := baseline.Clone()
	real.Steps[0].Duration = 300 * time.Millisecond
	baseline.Steps[0].Duration = 100 * time.Millisecond
	g.Observation = baseline
	g.Behaviour = baseline.BehaviourPrint()
	real.Steps[0].Duration = 300 * time.Millisecond

	c = Compare(g, real, Tolerances{LatencyRatio: 2, LatencyFloor: 50 * time.Microsecond})
	if c.Verdict != VerdictDrift || c.Reason != "measurement_changed" {
		t.Fatalf("a real slowdown produced %s/%s", c.Verdict, c.Reason)
	}
}

// ---------------------------------------------------------------------------
// Clock resolution
// ---------------------------------------------------------------------------

// TestResolution_FloorIsRaisedToWhatTheClockCanSee covers the defect that made
// every latency figure on this platform a measurement of nothing.
func TestResolution_FloorIsRaisedToWhatTheClockCanSee(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.Tolerances.LatencyFloor = time.Nanosecond // deliberately absurd

	r, err := New(cfg, NewSubjectSet(NewFakeSubject("fake")))
	if err != nil {
		t.Fatal(err)
	}

	resolution := r.ClockResolution()
	if resolution <= 0 {
		t.Fatal("a real clock reported zero resolution")
	}
	if r.cfg.Tolerances.LatencyFloor < resolution {
		t.Fatalf("the latency floor (%s) is below the clock resolution (%s); the "+
			"platform would report drift it cannot see",
			r.cfg.Tolerances.LatencyFloor, resolution)
	}
	t.Logf("measured clock resolution: %s, effective floor: %s",
		resolution, r.cfg.Tolerances.LatencyFloor)
}

func TestResolution_FakeClockNeedsNoFloor(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	if got := h.Runtime.ClockResolution(); got != 0 {
		t.Fatalf("a fake clock reported a resolution of %s; it advances only when "+
			"told to, so there is nothing to measure", got)
	}
}

func TestMeasurableAt(t *testing.T) {
	t.Parallel()

	res := 500 * time.Microsecond
	if MeasurableAt(100*time.Microsecond, res) {
		t.Error("a duration below the resolution was called measurable")
	}
	if !MeasurableAt(10*time.Millisecond, res) {
		t.Error("a duration far above the resolution was called unmeasurable")
	}
	if !MeasurableAt(time.Nanosecond, 0) {
		t.Error("with no resolution limit everything is measurable")
	}
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

func TestRegistry_RegistrationIsAtomic(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	good := SimpleScenario("good", "fake", "op")
	bad := SimpleScenario("bad", "fake", "op")
	bad.Owner = ""

	if err := h.Runtime.Registry().RegisterScenarios(good, bad); err == nil {
		t.Fatal("a batch containing an invalid scenario was accepted")
	}
	if h.Runtime.Registry().Len() != 0 {
		t.Fatalf("a failed batch left %d scenarios registered; the platform would "+
			"evaluate half a set", h.Runtime.Registry().Len())
	}
}

func TestRegistry_VersionsDoNotGoBackwards(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	s := SimpleScenario("s", "fake", "op")
	s.Version = 3
	h.Register(s)

	s.Version = 2
	if err := h.Runtime.Registry().RegisterScenarios(s); err == nil {
		t.Fatal("a scenario went backwards, which would compare a run against a " +
			"golden recorded from a later question")
	}
}

func TestRegistry_SuiteReferencingAnUnknownScenarioIsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(SimpleScenario("known", "fake", "op"))

	err := h.Runtime.Registry().RegisterSuites(Suite{
		ID: "s", Owner: "team", Scenarios: []ScenarioID{"known", "ghost"}})
	if err == nil || !strings.Contains(err.Error(), "unregistered scenario ghost") {
		t.Fatalf("a suite naming a missing scenario was accepted: %v", err)
	}
}

func TestRegistry_BenchmarkSuiteCannotRunInParallel(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(SimpleScenario("s", "fake", "op"))

	err := h.Runtime.Registry().RegisterSuites(Suite{
		ID: "b", Kind: KindBenchmark, Owner: "team",
		Scenarios: []ScenarioID{"s"}, Parallel: true})
	if err == nil || !strings.Contains(err.Error(), "measure contention") {
		t.Fatalf("a parallel benchmark suite was accepted: %v", err)
	}
}

func TestRegistry_CoverageNamesWhatIsMissing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(StepsScenario("s", "fake", KindMemory, "op"))

	cov := h.Runtime.Registry().Coverage()
	if cov.ByKind[KindMemory] != 1 {
		t.Errorf("memory coverage is %d", cov.ByKind[KindMemory])
	}
	uncovered := cov.UncoveredKinds()
	if len(uncovered) != len(AllScenarioKinds())-1 {
		t.Fatalf("uncovered kinds: %v", uncovered)
	}
	for _, k := range uncovered {
		if k == KindMemory {
			t.Error("a covered kind was reported as uncovered")
		}
	}
}

// ---------------------------------------------------------------------------
// Execution
// ---------------------------------------------------------------------------

func TestExecute_MissingCapabilityIsSkippedNotFailed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Fake.Caps = []Capability{"basic"}

	s := SimpleScenario("s", "fake", "op")
	s.Requires = []Capability{"streaming", "basic"}
	h.Register(s)

	res := h.Runtime.Execute(context.Background(), s)
	if res.Verdict != VerdictSkipped {
		t.Fatalf("a missing capability produced %s; an adapter that has not "+
			"implemented streaming is not a broken subsystem", res.Verdict)
	}
	if len(res.Comparison.Skipped) != 1 || res.Comparison.Skipped[0] != "streaming" {
		t.Fatalf("the skip did not name what was missing: %v", res.Comparison.Skipped)
	}
}

func TestExecute_UnsupportedInjectionIsSkipped(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Fake.Caps = []Capability{"basic"} // no injection capabilities

	s := SimpleScenario("s", "fake", "op")
	s.Steps[0].Inject = &Failure{Kind: FailTimeout}
	h.Register(s)

	res := h.Runtime.Execute(context.Background(), s)
	if res.Verdict != VerdictSkipped {
		t.Fatalf("an unsupported injection produced %s; silently ignoring it would "+
			"produce a scenario that appears to test failure handling and tests "+
			"nothing", res.Verdict)
	}
}

func TestExecute_AdapterPanicBecomesAFailedStep(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Fake.PanicOnStep = "boom"

	s := Scenario{ID: "s", Version: 1, Owner: "test", SubjectName: "fake",
		Steps: []Step{{Name: "boom", Op: "op"}}}
	h.Register(s)

	res := h.Runtime.Execute(context.Background(), s)
	if res.Verdict != VerdictFail {
		t.Fatalf("a panicking adapter produced %s", res.Verdict)
	}
	step := res.Observation.Steps[0]
	if !step.Failed || step.Outcome != "adapter_panic" {
		t.Fatalf("the panic was not recorded as a step failure: %+v", step)
	}
	if !strings.Contains(step.Detail, "scripted panic") {
		t.Error("the panic value was lost; a broken adapter would be undiagnosable")
	}
}

func TestExecute_SubjectThatCannotOpenIsRecordedNotLost(t *testing.T) {
	t.Parallel()

	h, err := NewHarness(WithHarnessSubjects(
		FailingSubject{SubjectName: "broken", Err: errors.New("cannot start")}))
	if err != nil {
		t.Fatal(err)
	}
	s := SimpleScenario("s", "broken", "op")
	h.Register(s)

	res := h.Runtime.Execute(context.Background(), s)
	if res.Verdict != VerdictFail {
		t.Fatalf("a subject that cannot open produced %s", res.Verdict)
	}
	if !strings.Contains(res.Observation.Error, "cannot start") {
		t.Fatalf("the reason was discarded: %q", res.Observation.Error)
	}
}

func TestExecute_UnregisteredSubjectIsAnOperatorProblem(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	s := SimpleScenario("s", "nobody", "op")
	h.Register(s)

	res := h.Runtime.Execute(context.Background(), s)
	if res.Verdict != VerdictFail || res.Comparison.Reason != "subject_not_registered" {
		t.Fatalf("got %s/%s", res.Verdict, res.Comparison.Reason)
	}
}

func TestExecute_FirstRunHasNoBaselineAndFilesACandidate(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := SimpleScenario("s", "fake", "op")
	h.Register(s)

	res := h.Runtime.Execute(context.Background(), s)
	if res.Verdict != VerdictNoBaseline {
		t.Fatalf("a first run produced %s; a new scenario is not a red result",
			res.Verdict)
	}
	if res.Candidate == "" {
		t.Fatal("no candidate was filed for review")
	}
	if res.Verdict.Blocking() {
		t.Error("a missing baseline blocks a release; adding a scenario would be a " +
			"release event")
	}
}

// ---------------------------------------------------------------------------
// Determinism
// ---------------------------------------------------------------------------

func TestDeterminism_DetectsANonDeterministicSubject(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Fake.NonDeterministic = true

	s := SimpleScenario("s", "fake", "op")
	h.Register(s)

	d := h.Runtime.CheckDeterminism(context.Background(), s, 4)
	if d.Deterministic {
		t.Fatal("a non-deterministic subject was reported deterministic")
	}
	if len(d.Behaviours) < 2 {
		t.Fatalf("only %d distinct behaviours observed", len(d.Behaviours))
	}
	if d.FirstDivergentRun != 2 {
		t.Errorf("first divergence reported at run %d, want 2", d.FirstDivergentRun)
	}
	if len(d.Divergences) == 0 {
		t.Error("the divergence was not explained")
	}
}

func TestDeterminism_IgnoresTiming(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Fake.Clock = h.Clock
	h.Fake.Delay = 0 // durations vary naturally with a system clock

	s := SimpleScenario("s", "fake", "op")
	h.Register(s)

	d := h.Runtime.CheckDeterminism(context.Background(), s, 5)
	if !d.Deterministic {
		t.Fatalf("timing variation was reported as non-determinism: %v", d.Divergences)
	}
	if len(d.Timings) != 5 {
		t.Errorf("timings were not reported alongside the verdict")
	}
}

// ---------------------------------------------------------------------------
// Replay
// ---------------------------------------------------------------------------

func TestReplay_ReproducesARecording(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := SimpleScenario("s", "fake", "op")
	h.Register(s)

	recorded := h.Runtime.Execute(context.Background(), s).Observation
	r := h.Runtime.Replay(context.Background(), s, recorded)
	if !r.Reproduced {
		t.Fatalf("a deterministic scenario did not reproduce: %v", r.Differences)
	}
}

func TestReplay_ReportsWhatChanged(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := SimpleScenario("s", "fake", "op")
	h.Register(s)

	recorded := h.Runtime.Execute(context.Background(), s).Observation
	h.Fake.Handlers["op"] = func(Step, int) StepResult {
		return StepResult{Outcome: "different", Output: Values{"x": N(9)}}
	}

	r := h.Runtime.Replay(context.Background(), s, recorded)
	if r.Reproduced {
		t.Fatal("a changed subject reproduced the recording")
	}
	if len(r.Differences) == 0 {
		t.Fatal("the divergence was not explained")
	}
}

// TestReplay_NeedsNoApproval is the difference between replay and comparison: a
// recording is what happened, not what anybody agreed was correct.
func TestReplay_NeedsNoApproval(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := SimpleScenario("s", "fake", "op")
	h.Register(s)

	recorded := h.Runtime.Execute(context.Background(), s).Observation
	if _, err := h.Runtime.Goldens().Baseline(s.Key()); !errors.Is(err, ErrNoGolden) {
		t.Fatal("this test needs no approved golden to exist")
	}
	if r := h.Runtime.Replay(context.Background(), s, recorded); !r.Reproduced {
		t.Fatal("replay required an approval it should not need")
	}
}

// ---------------------------------------------------------------------------
// Regression
// ---------------------------------------------------------------------------

func TestRegression_ReportsBothDirections(t *testing.T) {
	t.Parallel()

	base := Run{ID: "base", Label: "v1", Results: []ScenarioResult{
		{Scenario: "steady", Subject: "s", Verdict: VerdictPass,
			Observation: obsWith("steady", "ok", 0)},
		{Scenario: "breaks", Subject: "s", Verdict: VerdictPass,
			Observation: obsWith("breaks", "ok", 0)},
		{Scenario: "heals", Subject: "s", Verdict: VerdictFail,
			Observation: obsWith("heals", "ok", 0)},
	}}
	cur := Run{ID: "cur", Label: "v2", Results: []ScenarioResult{
		{Scenario: "steady", Subject: "s", Verdict: VerdictPass,
			Observation: obsWith("steady", "ok", 0)},
		{Scenario: "breaks", Subject: "s", Verdict: VerdictFail,
			Observation: obsWith("breaks", "ok", 0)},
		{Scenario: "heals", Subject: "s", Verdict: VerdictPass,
			Observation: obsWith("heals", "ok", 0)},
		{Scenario: "brand-new", Subject: "s", Verdict: VerdictNoBaseline,
			Observation: obsWith("brand-new", "ok", 0)},
	}}

	report := DetectRegressions(base, cur, 2, 0)
	if len(report.Regressions) != 1 || report.Regressions[0].Scenario != "breaks" {
		t.Fatalf("regressions: %v", report.Regressions)
	}
	if len(report.Improvements) != 1 || report.Improvements[0].Scenario != "heals" {
		t.Fatalf("improvements: %v", report.Improvements)
	}
	if len(report.NewScenarios) != 1 || report.NewScenarios[0] != "brand-new" {
		t.Fatalf("new scenarios: %v", report.NewScenarios)
	}
	if report.Clean() {
		t.Error("a report with a regression called itself clean")
	}
	if len(report.Blocking()) != 1 {
		t.Error("a pass becoming a fail should block")
	}
}

func TestRegression_LatencyIsComparedIndependentlyOfBehaviour(t *testing.T) {
	t.Parallel()

	base := Run{ID: "base", Results: []ScenarioResult{
		{Scenario: "s", Subject: "subj", Verdict: VerdictPass,
			Observation: obsWith("s", "ok", 10*time.Millisecond)}}}
	cur := Run{ID: "cur", Results: []ScenarioResult{
		{Scenario: "s", Subject: "subj", Verdict: VerdictPass,
			Observation: obsWith("s", "ok", 100*time.Millisecond)}}}

	report := DetectRegressions(base, cur, 2, 0)
	if len(report.Regressions) != 1 || report.Regressions[0].Kind != RegressionLatency {
		t.Fatalf("an identical answer taking ten times as long was not reported: %v",
			report.Regressions)
	}
}

func TestRegression_NoticesAChangedScenarioSet(t *testing.T) {
	t.Parallel()

	base := Run{ID: "base", RegistryDigest: "aaa"}
	cur := Run{ID: "cur", RegistryDigest: "bbb"}

	if !DetectRegressions(base, cur, 2, 0).RegistryChanged {
		t.Fatal("a changed scenario set was not reported; a reader could not tell " +
			"whether they are comparing answers or questions")
	}
}

func obsWith(id ScenarioID, outcome string, d time.Duration) Observation {
	return Observation{Scenario: id, ScenarioVersion: 1,
		Steps: []StepObservation{{Name: "a", Op: "op", Outcome: outcome, Duration: d}}}
}

// ---------------------------------------------------------------------------
// Benchmarks and distributions
// ---------------------------------------------------------------------------

func TestSummarise_ReportsPercentilesNotJustAMean(t *testing.T) {
	t.Parallel()

	samples := make([]time.Duration, 100)
	for i := range samples {
		samples[i] = time.Millisecond
	}
	samples[99] = time.Second // the tail that a mean hides

	d := Summarise(samples)
	if d.P50 != time.Millisecond {
		t.Errorf("p50 is %s", d.P50)
	}
	if d.Max != time.Second {
		t.Errorf("max is %s", d.Max)
	}
	if d.Mean <= time.Millisecond {
		t.Error("the mean should have been dragged up by the tail")
	}
}

func TestSummarise_DoesNotReorderTheCallersSlice(t *testing.T) {
	t.Parallel()

	samples := []time.Duration{3, 1, 2}
	_ = Summarise(samples)
	if samples[0] != 3 || samples[1] != 1 || samples[2] != 2 {
		t.Fatal("Summarise sorted the caller's slice in place")
	}
}

func TestBenchmark_FlagsUnstableBehaviour(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Fake.NonDeterministic = true

	s := SimpleScenario("s", "fake", "op")
	h.Register(s)

	b := h.Runtime.Benchmark(context.Background(), s, 10, 2, "test")
	if b.BehaviourStable {
		t.Fatal("a benchmark over a non-deterministic scenario called itself stable; " +
			"its percentiles average different things")
	}
	if !strings.Contains(b.Summary(), "UNSTABLE") {
		t.Error("the summary did not warn the reader")
	}
}

// ---------------------------------------------------------------------------
// Reports
// ---------------------------------------------------------------------------

func TestScorecard_ExcludesSkipsFromTheDenominator(t *testing.T) {
	t.Parallel()

	run := Run{Results: []ScenarioResult{
		{Scenario: "a", Subject: "s", Verdict: VerdictPass},
		{Scenario: "b", Subject: "s", Verdict: VerdictFail},
		{Scenario: "c", Subject: "s", Verdict: VerdictSkipped},
		{Scenario: "d", Subject: "s", Verdict: VerdictSkipped},
		{Scenario: "e", Subject: "s", Verdict: VerdictNoBaseline},
	}}

	cards := Scorecards(run)
	if len(cards) != 1 {
		t.Fatalf("scorecards: %d", len(cards))
	}
	if cards[0].PassRate != 0.5 {
		t.Fatalf("pass rate is %v; adding unrunnable scenarios must not improve a "+
			"score", cards[0].PassRate)
	}
	if cards[0].Coverage != 0.6 {
		t.Fatalf("coverage is %v, want 0.6", cards[0].Coverage)
	}
	if cards[0].Healthy() {
		t.Error("a scorecard with a failure called itself healthy")
	}
}

func TestScorecard_DriftIsNotUnhealthy(t *testing.T) {
	t.Parallel()

	run := Run{Results: []ScenarioResult{
		{Scenario: "a", Subject: "s", Verdict: VerdictDrift}}}
	if !Scorecards(run)[0].Healthy() {
		t.Fatal("drift was reported as unhealthy; every deliberate improvement " +
			"would look like an outage")
	}
}

func TestDashboard_HeatmapsAreStablyOrdered(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(
		StepsScenario("m1", "fake", KindMemory, "op"),
		StepsScenario("g1", "fake", KindGovernance, "op"),
	)

	run := h.Run("test")
	first := fmt.Sprint(h.Runtime.Coordinator().Dashboard(run, 10).Failures)
	for i := 0; i < 20; i++ {
		if got := fmt.Sprint(h.Runtime.Coordinator().Dashboard(run, 10).Failures); got != first {
			t.Fatal("heatmap ordering is unstable")
		}
	}
}

// ---------------------------------------------------------------------------
// Runtime lifecycle
// ---------------------------------------------------------------------------

func TestRuntime_StartsOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	if err := h.Runtime.Start(); err != nil {
		t.Fatal(err)
	}
	if err := h.Runtime.Start(); !errors.Is(err, ErrInvariant) {
		t.Fatalf("INV-EVAL-10: a second Start should be refused, got %v", err)
	}
	if err := h.Runtime.Stop(); err != nil {
		t.Fatal(err)
	}

	s := SimpleScenario("s", "fake", "op")
	if res := h.Runtime.Execute(context.Background(), s); res.Comparison.Reason != "runtime_closed" {
		t.Fatalf("a stopped runtime executed a scenario: %s", res.Comparison.Reason)
	}
}

func TestRuntime_RefusesToStartWithNoSubjects(t *testing.T) {
	t.Parallel()

	_, err := New(DefaultConfig(), NewSubjectSet())
	if err == nil || !strings.Contains(err.Error(), "perfect score over nothing") {
		t.Fatalf("a platform with nothing to evaluate was accepted: %v", err)
	}
}

func TestRuntime_TwoRuntimesShareNothing(t *testing.T) {
	t.Parallel()
	a, b := newHarness(t), newHarness(t)

	a.Register(SimpleScenario("only-in-a", "fake", "op"))

	if b.Runtime.Registry().Len() != 0 {
		t.Fatal("a scenario registered in one runtime appeared in another")
	}
}

func TestMetrics_RatesExcludeSkipsFromTheDenominator(t *testing.T) {
	t.Parallel()
	m := NewMetrics()

	if m.PassRate() != 0 || m.DriftRate() != 0 || m.FailRate() != 0 {
		t.Fatal("rates over no observations should be zero rather than NaN")
	}
	m.Verdicts.Inc(VerdictPass.String(), "s")
	m.Verdicts.Inc(VerdictFail.String(), "s")
	m.Verdicts.Inc(VerdictSkipped.String(), "s")

	if got := m.PassRate(); got != 0.5 {
		t.Fatalf("pass rate is %v, want 0.5", got)
	}
	if got := m.CoverageRate(); got < 0.66 || got > 0.67 {
		t.Fatalf("coverage rate is %v, want 2/3", got)
	}
}
