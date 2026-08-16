package evaluation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Integration, concurrency, stress and failure-injection tests.
//
// These run whole evaluation cycles through the real platform — registry,
// scenario engine, golden store, comparison, determinism, replay, regression,
// benchmark, storage and reports. The unit suite proves each part; this proves
// they agree with each other.

// ---------------------------------------------------------------------------
// The full cycle
// ---------------------------------------------------------------------------

// TestIntegration_FullCycle walks the lifecycle every scenario goes through:
// no baseline → candidate → approval → pass → change → drift → re-approval.
func TestIntegration_FullCycle(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := SimpleScenario("cycle", "fake", "op")
	h.Register(s)

	// 1. First run: no baseline, a candidate filed, not blocking.
	first := h.Runtime.Execute(context.Background(), s)
	if first.Verdict != VerdictNoBaseline || first.Candidate == "" {
		t.Fatalf("first run: %s candidate=%q", first.Verdict, first.Candidate)
	}

	// 2. A human approves it.
	g, err := h.Runtime.Goldens().Approve(first.Candidate, "reviewer", "reviewed and correct")
	if err != nil {
		t.Fatal(err)
	}

	// 3. Unchanged behaviour now passes.
	second := h.Runtime.Execute(context.Background(), s)
	if second.Verdict != VerdictPass {
		t.Fatalf("an unchanged run produced %s\n%s", second.Verdict,
			second.Comparison.Summary())
	}

	// 4. The subject changes: drift, with the difference named.
	h.Fake.Handlers["op"] = func(Step, int) StepResult {
		return StepResult{Outcome: "ok", Output: Values{"answer": S("different")}}
	}
	third := h.Runtime.Execute(context.Background(), s)
	if third.Verdict != VerdictDrift {
		t.Fatalf("a changed subject produced %s", third.Verdict)
	}
	if len(third.Comparison.BehaviouralDifferences()) == 0 {
		t.Fatal("drift named no behavioural difference")
	}
	if third.Verdict.Blocking() {
		t.Fatal("drift blocked a release")
	}

	// 5. Somebody decides the change is correct and re-approves.
	candidates := h.Runtime.Goldens().Candidates(s.Key())
	if len(candidates) == 0 {
		t.Fatal("drift filed no candidate to approve")
	}
	if _, err := h.Runtime.Goldens().Approve(candidates[len(candidates)-1].ID,
		"reviewer", "the new answer is the intended one"); err != nil {
		t.Fatal(err)
	}

	fourth := h.Runtime.Execute(context.Background(), s)
	if fourth.Verdict != VerdictPass {
		t.Fatalf("after re-approval the run produced %s", fourth.Verdict)
	}

	// And the history remembers what was correct before.
	history := h.Runtime.Goldens().History(s.Key())
	found := false
	for _, old := range history {
		if old.ID == g.ID && old.State == GoldenSuperseded {
			found = true
		}
	}
	if !found {
		t.Error("the superseded baseline was not retained")
	}
}

// ---------------------------------------------------------------------------
// Suites and runs
// ---------------------------------------------------------------------------

func TestIntegration_SuiteRunsAndReports(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(
		StepsScenario("a", "fake", KindMemory, "op"),
		StepsScenario("b", "fake", KindGovernance, "op", "op"),
	)
	h.RegisterSuite(Suite{ID: "acceptance", Kind: KindAcceptance, Owner: "team",
		Scenarios: []ScenarioID{"a", "b"}})

	run, err := h.Runtime.RunSuite(context.Background(), "acceptance", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Results) != 2 || run.Suite != "acceptance" {
		t.Fatalf("run: %d results, suite %s", len(run.Results), run.Suite)
	}
	if run.RegistryDigest == "" {
		t.Error("the run does not name the scenario set it came from")
	}

	report := h.Runtime.Coordinator().Report(run)
	if !report.Releasable() {
		t.Fatalf("a run with no failures is not releasable:\n%s", report.Summary())
	}
	if report.Pending != 2 {
		t.Errorf("expected 2 candidates awaiting review, got %d", report.Pending)
	}
}

// TestIntegration_ResultsAreOrderedIndependentlyOfCompletion is what lets two
// run reports be diffed line by line.
func TestIntegration_ResultsAreOrderedIndependentlyOfCompletion(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	var ids []ScenarioID
	for i := 0; i < 10; i++ {
		id := ScenarioID(fmt.Sprintf("s%02d", i))
		ids = append(ids, id)
		h.Register(SimpleScenario(id, "fake", "op"))
	}
	h.RegisterSuite(Suite{ID: "parallel", Owner: "team", Scenarios: ids, Parallel: true})

	var first []ScenarioID
	for run := 0; run < 5; run++ {
		r, err := h.Runtime.RunSuite(context.Background(), "parallel", "x")
		if err != nil {
			t.Fatal(err)
		}
		var order []ScenarioID
		for _, res := range r.Results {
			order = append(order, res.Scenario)
		}
		if run == 0 {
			first = order
			continue
		}
		if fmt.Sprint(order) != fmt.Sprint(first) {
			t.Fatalf("parallel run %d reordered its results: %v vs %v", run, order, first)
		}
	}
}

// ---------------------------------------------------------------------------
// Determinism and replay end to end
// ---------------------------------------------------------------------------

func TestIntegration_DeterminismAndReplayAgree(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := StepsScenario("s", "fake", KindRuntime, "op", "op", "op")
	h.Register(s)

	d := h.Runtime.CheckDeterminism(context.Background(), s, 5)
	if !d.Deterministic {
		t.Fatalf("a deterministic fake diverged: %v", d.Divergences)
	}

	recorded := h.Runtime.Execute(context.Background(), s).Observation
	r := h.Runtime.Replay(context.Background(), s, recorded)
	if !r.Reproduced {
		t.Fatalf("a deterministic scenario did not replay: %v", r.Differences)
	}
	// The two engines must agree: a scenario that is deterministic must replay,
	// and one that replays must be deterministic. A platform where they
	// disagreed would have two different definitions of "the same run".
	if d.Behaviours[0] != r.Recorded {
		t.Fatalf("determinism and replay saw different behaviour: %s vs %s",
			d.Behaviours[0], r.Recorded)
	}
}

func TestIntegration_ReplayLatestUsesStoredObservations(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := SimpleScenario("s", "fake", "op")
	h.Register(s)

	if _, err := h.Runtime.ReplayLatest(context.Background(), s); !errors.Is(err, ErrNoBaselineRun) {
		t.Fatalf("replaying with no history should say so: %v", err)
	}

	h.Runtime.Execute(context.Background(), s)
	r, err := h.Runtime.ReplayLatest(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Reproduced {
		t.Fatalf("replay of the stored observation failed: %v", r.Differences)
	}
}

// ---------------------------------------------------------------------------
// Regression end to end
// ---------------------------------------------------------------------------

func TestIntegration_RegressionAcrossTwoRuns(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := SimpleScenario("s", "fake", "op")
	h.Register(s)
	h.Approve(s)

	baseline := h.Run("v1")

	// The subject breaks.
	h.Fake.FailOnStep = "only"
	current := h.Run("v2")

	report, err := h.Runtime.Coordinator().CompareRuns(baseline.ID, current.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if report.Clean() {
		t.Fatalf("a scenario that started failing was not reported:\n%s", report.Summary())
	}
	if len(report.Blocking()) != 1 {
		t.Fatalf("a pass becoming a fail should block, got %d", len(report.Blocking()))
	}
}

func TestIntegration_CompareAgainstPrevious(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(SimpleScenario("s", "fake", "op"))

	if _, err := h.Runtime.Coordinator().CompareAgainstPrevious(""); !errors.Is(err, ErrNoBaselineRun) {
		t.Fatalf("comparing with one run should say so: %v", err)
	}
	h.Run("v1")
	h.Run("v2")

	report, err := h.Runtime.Coordinator().CompareAgainstPrevious("")
	if err != nil {
		t.Fatal(err)
	}
	if report.BaselineLabel != "v1" || report.CurrentLabel != "v2" {
		t.Fatalf("compared %s → %s", report.BaselineLabel, report.CurrentLabel)
	}
}

// ---------------------------------------------------------------------------
// Failure injection
// ---------------------------------------------------------------------------

func TestIntegration_InjectionAppearsInTheBehaviour(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	clean := SimpleScenario("clean", "fake", "op")
	injected := SimpleScenario("injected", "fake", "op")
	injected.Steps[0].Inject = &Failure{Kind: FailTimeout, Detail: "downstream"}
	h.Register(clean, injected)

	cleanObs := h.Runtime.Execute(context.Background(), clean).Observation
	injObs := h.Runtime.Execute(context.Background(), injected).Observation

	if cleanObs.BehaviourPrint() == injObs.BehaviourPrint() {
		t.Fatal("an injected failure produced the same behaviour as a clean run; " +
			"the injection was ignored")
	}
	if injObs.Steps[0].Injected == "" {
		t.Fatal("the observation does not record which failure was injected")
	}
	if h.Metrics.Injections.Total() == 0 {
		t.Error("the injection was not counted")
	}
}

func TestIntegration_FailedStepStopsTheScenario(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Fake.FailOnStep = "s1"

	s := StepsScenario("s", "fake", KindFailure, "op", "op", "op")
	h.Register(s)

	obs := h.Runtime.Execute(context.Background(), s).Observation
	if len(obs.Steps) != 2 {
		t.Fatalf("the scenario ran %d steps past a failure; later steps would run "+
			"against a state nobody planned", len(obs.Steps))
	}
	if !obs.Failed() {
		t.Error("the observation does not report the failure")
	}
}

func TestFailure_StorageErrorsDoNotFailARun(t *testing.T) {
	t.Parallel()

	h, err := NewHarness()
	if err != nil {
		t.Fatal(err)
	}
	failing := &failingStorage{}
	r, err := New(DefaultConfig(), NewSubjectSet(h.Fake),
		WithClock(h.Clock), WithMetrics(h.Metrics), WithStorage(failing))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Registry().RegisterScenarios(SimpleScenario("s", "fake", "op")); err != nil {
		t.Fatal(err)
	}

	run := r.RunAll(context.Background(), "v1")
	if len(run.Results) != 1 {
		t.Fatalf("a storage outage lost the run: %d results", len(run.Results))
	}
	if h.Metrics.StorageErrors.Total() == 0 {
		t.Error("storage failures were not counted")
	}
}

type failingStorage struct{}

func (failingStorage) PutRun(Run) error                        { return errors.New("storage down") }
func (failingStorage) GetRun(RunID) (Run, error)               { return Run{}, errors.New("storage down") }
func (failingStorage) Runs(int) []Run                          { return nil }
func (failingStorage) LatestRun(SuiteID) (Run, error)          { return Run{}, ErrNoBaselineRun }
func (failingStorage) PutObservation(Observation) error        { return errors.New("storage down") }
func (failingStorage) Observations(ScenarioID) []Observation   { return nil }
func (failingStorage) PutBenchmark(BenchmarkResult) error      { return errors.New("storage down") }
func (failingStorage) Benchmarks(ScenarioID) []BenchmarkResult { return nil }
func (failingStorage) PutTrend(TrendPoint) error               { return errors.New("storage down") }
func (failingStorage) Trend(ScenarioID) []TrendPoint           { return nil }

// ---------------------------------------------------------------------------
// Storage and trends
// ---------------------------------------------------------------------------

func TestIntegration_TrendRecordsBehaviourNotObservations(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := SimpleScenario("s", "fake", "op")
	h.Register(s)

	h.Run("v1")
	h.Fake.Handlers["op"] = func(Step, int) StepResult {
		return StepResult{Outcome: "ok", Output: Values{"changed": B(true)}}
	}
	h.Run("v2")

	trend := h.Storage.Trend("s")
	if len(trend) != 2 {
		t.Fatalf("trend has %d points", len(trend))
	}
	if trend[0].Behaviour == trend[1].Behaviour {
		t.Fatal("a behaviour change did not move the trend fingerprint")
	}
	// The trend carries a fingerprint, not the observation.
	rendered := fmt.Sprintf("%+v", trend[0])
	if strings.Contains(rendered, "Steps") {
		t.Fatal("a trend point carries an observation; a years-long history would " +
			"be a permanent archive of everything every subsystem ever did")
	}
}

func TestIntegration_StorageIsBoundedAndSaysSo(t *testing.T) {
	t.Parallel()

	h, err := NewHarness()
	if err != nil {
		t.Fatal(err)
	}
	bounded := NewBoundedMemoryStorage(3, 2, 4)
	r, err := New(DefaultConfig(), NewSubjectSet(h.Fake),
		WithClock(h.Clock), WithStorage(bounded))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Registry().RegisterScenarios(SimpleScenario("s", "fake", "op")); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 10; i++ {
		r.RunAll(context.Background(), fmt.Sprintf("v%d", i))
	}

	stats := bounded.Stats()
	if stats.Runs > 3 {
		t.Fatalf("the run bound was exceeded: %d", stats.Runs)
	}
	if !stats.Evicted {
		t.Fatal("eviction happened and was not reported; a reader could not tell " +
			"whether they are looking at the whole history")
	}
}

// TestIntegration_BenchmarkStorageIsBounded covers the collection that was not.
//
// Benchmarks were the one unbounded map in a store built around being bounded.
// Nothing failed as a result — the growth is slow and a test run is short — which
// is exactly why it survived until somebody read the file rather than ran it.
func TestIntegration_BenchmarkStorageIsBounded(t *testing.T) {
	t.Parallel()

	h, err := NewHarness()
	if err != nil {
		t.Fatal(err)
	}
	bounded := NewBoundedMemoryStorage(3, 2, 4)
	r, err := New(DefaultConfig(), NewSubjectSet(h.Fake),
		WithClock(h.Clock), WithStorage(bounded))
	if err != nil {
		t.Fatal(err)
	}
	s := SimpleScenario("s", "fake", "op")
	if err := r.Registry().RegisterScenarios(s); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 10; i++ {
		r.Benchmark(context.Background(), s, 2, 0, fmt.Sprintf("v%d", i))
	}

	if got := len(bounded.Benchmarks(s.ID)); got > 2 {
		t.Fatalf("benchmark history grew to %d against a bound of 2: a platform "+
			"benchmarking on a schedule would grow this map for as long as it ran",
			got)
	}
}

// ---------------------------------------------------------------------------
// Dashboard and readiness
// ---------------------------------------------------------------------------

func TestIntegration_DashboardModelIsComplete(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(
		StepsScenario("m", "fake", KindMemory, "op"),
		StepsScenario("g", "fake", KindGovernance, "op"),
	)
	h.Run("v1")
	run := h.Run("v2")

	model := h.Runtime.Coordinator().Dashboard(run, 10)

	if model.Summary.Total != 2 {
		t.Errorf("summary total: %d", model.Summary.Total)
	}
	if len(model.Subjects) != 1 {
		t.Errorf("subject panels: %d", len(model.Subjects))
	}
	if len(model.Trends) != 2 {
		t.Errorf("trend series: %d", len(model.Trends))
	}
	for _, series := range model.Trends {
		if len(series.Points) != 2 {
			t.Errorf("%s trend has %d points", series.Scenario, len(series.Points))
		}
		if !series.Stable {
			t.Errorf("%s reported unstable with no behaviour change", series.Scenario)
		}
	}
	if len(model.Failures.Cells) == 0 || len(model.Latency.Cells) == 0 {
		t.Error("heatmaps are empty")
	}
	if model.Coverage.Total != 2 {
		t.Errorf("coverage total: %d", model.Coverage.Total)
	}
}

// TestIntegration_ReadinessNamesUnevaluatedSubsystems is the field that stops a
// readiness report looking green because a subsystem was never asked anything.
func TestIntegration_ReadinessNamesUnevaluatedSubsystems(t *testing.T) {
	t.Parallel()

	evaluated := NewFakeSubject("evaluated")
	ignored := NewFakeSubject("ignored")

	h, err := NewHarness(WithHarnessSubjects(evaluated, ignored))
	if err != nil {
		t.Fatal(err)
	}
	h.Register(SimpleScenario("s", "evaluated", "op"))

	readiness := h.Runtime.Coordinator().Readiness(context.Background(), "v1", 3)

	if len(readiness.UnevaluatedSubjects) != 1 {
		t.Fatalf("unevaluated subsystems: %v", readiness.UnevaluatedSubjects)
	}
	if readiness.Ready {
		t.Fatal("a platform with an unevaluated subsystem reported itself ready")
	}
	if !strings.Contains(readiness.Summary(), "ignored") {
		t.Error("the readiness summary does not name the unevaluated subsystem")
	}
}

// ---------------------------------------------------------------------------
// Concurrency and stress
// ---------------------------------------------------------------------------

func TestStress_ConcurrentScenarios(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	var ids []ScenarioID
	for i := 0; i < 24; i++ {
		id := ScenarioID(fmt.Sprintf("s%02d", i))
		ids = append(ids, id)
		h.Register(StepsScenario(id, "fake", KindRuntime, "op", "op"))
	}
	h.RegisterSuite(Suite{ID: "parallel", Owner: "team", Scenarios: ids, Parallel: true})

	run, err := h.Runtime.RunSuite(context.Background(), "parallel", "stress")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Results) != 24 {
		t.Fatalf("results lost: %d of 24", len(run.Results))
	}
	for _, res := range run.Results {
		if res.Verdict == VerdictFail {
			t.Errorf("%s failed: %s", res.Scenario, res.Comparison.Reason)
		}
	}
	if h.Runtime.ScenariosExecuted() != 24 {
		t.Errorf("the runtime counted %d executions", h.Runtime.ScenariosExecuted())
	}
	// Each scenario opened its own session: sessions are never shared, which is
	// what lets adapters be written without locking.
	if h.Fake.Opens() != 24 {
		t.Errorf("sessions opened: %d of 24", h.Fake.Opens())
	}
}

func TestStress_ConcurrentGoldenApprovals(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := SimpleScenario("s", "fake", "op")
	h.Register(s)

	res := h.Runtime.Execute(context.Background(), s)

	const racers = 16
	var wg sync.WaitGroup
	var won atomic.Int64

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := h.Runtime.Goldens().Approve(res.Candidate,
				fmt.Sprintf("reviewer-%d", i), "concurrent"); err == nil {
				won.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if won.Load() != 1 {
		t.Fatalf("%d reviewers approved one candidate; a baseline must have exactly "+
			"one approver", won.Load())
	}
}

func TestStress_RegistryChurnDuringRuns(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(SimpleScenario("stable", "fake", "op"))

	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 2; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			s := SimpleScenario("churn", "fake", "op")
			s.Version = i
			_ = h.Runtime.Registry().RegisterScenarios(s)
		}
	}()

	var bad atomic.Int64
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				run := h.Runtime.RunAll(context.Background(), "churn")
				// Whatever the churn is doing, the run must name exactly one
				// scenario set — INV-EVAL-9.
				if run.RegistryDigest == "" || run.RegistryVersion == 0 {
					bad.Add(1)
				}
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	if bad.Load() != 0 {
		t.Fatalf("INV-EVAL-9: %d runs were not anchored to a scenario set", bad.Load())
	}
}

func TestStress_ScenarioTimeoutIsEnforced(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.ScenarioTimeout = 20 * time.Millisecond
	h, err := NewHarness(WithHarnessConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}

	// A handler that blocks past the timeout, using real time because the
	// timeout is enforced with a real context deadline.
	h.Fake.Handlers["slow"] = func(Step, int) StepResult {
		time.Sleep(60 * time.Millisecond)
		return StepResult{Outcome: "ok"}
	}
	s := StepsScenario("slow", "fake", KindRuntime, "slow", "slow", "slow")
	h.Register(s)

	obs := h.Runtime.Execute(context.Background(), s).Observation
	if obs.Error != "timeout" {
		t.Fatalf("a scenario past its budget reported %q", obs.Error)
	}
	if len(obs.Steps) >= 3 {
		t.Fatalf("the scenario ran all %d steps despite timing out", len(obs.Steps))
	}
}
