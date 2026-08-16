package evaluation

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Benchmarks for the evaluation platform's own hot path. Every number in
// docs/evaluation/PERFORMANCE.md comes from these, on the machine named there.
//
// WHAT THESE MEASURE, AND WHY IT IS AWKWARD. This module measures other
// software for a living, so its own cost is the one thing it cannot measure
// with itself: [EvaluationRuntime.Benchmark] would be timing the thing doing
// the timing. These are ordinary Go benchmarks, and they exist to answer one
// question — is the platform's overhead small enough that a scenario's reported
// duration is mostly the SUBJECT?
//
// The answer matters because every latency number the platform reports about
// every subsystem is measured through this code.

func benchHarness(b *testing.B) *Harness {
	b.Helper()
	h, err := NewHarness()
	if err != nil {
		b.Fatal(err)
	}
	return h
}

// BenchmarkExecuteScenario is the headline number: one scenario, end to end,
// including comparison against a baseline.
func BenchmarkExecuteScenario(b *testing.B) {
	h := benchHarness(b)
	s := SimpleScenario("bench", "fake", "op")
	h.Register(s)
	h.Approve(s)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Runtime.Execute(ctx, s)
	}
}

// BenchmarkExecuteScenarioNoBaseline measures the first-run path, which also
// records a golden candidate.
func BenchmarkExecuteScenarioNoBaseline(b *testing.B) {
	cfg := DefaultConfig()
	cfg.AutoRecordCandidates = false // isolate execution from candidate filing
	h, err := NewHarness(WithHarnessConfig(cfg))
	if err != nil {
		b.Fatal(err)
	}
	s := SimpleScenario("bench", "fake", "op")
	h.Register(s)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Runtime.Execute(ctx, s)
	}
}

// BenchmarkExecuteTenSteps measures how the cost scales with scenario length.
func BenchmarkExecuteTenSteps(b *testing.B) {
	h := benchHarness(b)
	ops := make([]string, 10)
	for i := range ops {
		ops[i] = "op"
	}
	s := StepsScenario("bench", "fake", KindRuntime, ops...)
	h.Register(s)
	h.Approve(s)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Runtime.Execute(ctx, s)
	}
}

// BenchmarkRunSuiteSerial and its parallel twin measure the run machinery
// rather than one scenario.
func BenchmarkRunSuiteSerial(b *testing.B) {
	h := benchHarness(b)
	var ids []ScenarioID
	for i := 0; i < 20; i++ {
		id := ScenarioID(fmt.Sprintf("s%02d", i))
		ids = append(ids, id)
		h.Register(SimpleScenario(id, "fake", "op"))
	}
	h.RegisterSuite(Suite{ID: "serial", Owner: "bench", Scenarios: ids})
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := h.Runtime.RunSuite(ctx, "serial", "bench"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRunSuiteParallel(b *testing.B) {
	h := benchHarness(b)
	var ids []ScenarioID
	for i := 0; i < 20; i++ {
		id := ScenarioID(fmt.Sprintf("s%02d", i))
		ids = append(ids, id)
		h.Register(SimpleScenario(id, "fake", "op"))
	}
	h.RegisterSuite(Suite{ID: "parallel", Owner: "bench", Scenarios: ids, Parallel: true})
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := h.Runtime.RunSuite(ctx, "parallel", "bench"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBehaviourPrint is the platform's most-executed pure function: every
// execution, every determinism run, every replay and every trend point computes
// one.
func BenchmarkBehaviourPrint(b *testing.B) {
	obs := benchObservation(10)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = obs.BehaviourPrint()
	}
}

func BenchmarkBehaviourPrintLarge(b *testing.B) {
	obs := benchObservation(100)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = obs.BehaviourPrint()
	}
}

func BenchmarkCompareMatching(b *testing.B) {
	obs := benchObservation(10)
	g := Golden{ID: "g", State: GoldenApproved, Observation: obs,
		Behaviour: obs.BehaviourPrint()}
	tol := DefaultTolerances()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Compare(g, obs, tol)
	}
}

// BenchmarkCompareDrifting measures the expensive path: every difference has to
// be found and described.
func BenchmarkCompareDrifting(b *testing.B) {
	baseline := benchObservation(10)
	g := Golden{ID: "g", State: GoldenApproved, Observation: baseline,
		Behaviour: baseline.BehaviourPrint()}

	drifted := baseline.Clone()
	for i := range drifted.Steps {
		drifted.Steps[i].Output = Values{"changed": N(float64(i))}
	}
	tol := DefaultTolerances()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Compare(g, drifted, tol)
	}
}

func BenchmarkObservationClone(b *testing.B) {
	obs := benchObservation(10)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = obs.Clone()
	}
}

func BenchmarkValuesFingerprint(b *testing.B) {
	v := Values{"answer": S("a moderately long observed value"),
		"count": N(42), "ok": B(true), "state": S("awaiting")}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = v.Fingerprint()
	}
}

func BenchmarkValuesDiff(b *testing.B) {
	was := Values{"a": S("x"), "b": N(1), "c": B(true), "d": S("y")}
	now := Values{"a": S("x"), "b": N(2), "c": B(true), "e": S("z")}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = was.Diff(now)
	}
}

func BenchmarkScenarioDigest(b *testing.B) {
	ops := make([]string, 10)
	for i := range ops {
		ops[i] = "op"
	}
	s := StepsScenario("bench", "fake", KindRuntime, ops...)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.Digest()
	}
}

func BenchmarkRegistrySnapshot(b *testing.B) {
	h := benchHarness(b)
	for i := 0; i < 200; i++ {
		h.Register(SimpleScenario(ScenarioID(fmt.Sprintf("s%03d", i)), "fake", "op"))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Runtime.Registry().Scenarios()
	}
}

// BenchmarkRegistryRegister measures the copy-on-write write path, which is
// what the lock-free read path is paid for with.
func BenchmarkRegistryRegister(b *testing.B) {
	h := benchHarness(b)
	for i := 0; i < 100; i++ {
		h.Register(SimpleScenario(ScenarioID(fmt.Sprintf("s%03d", i)), "fake", "op"))
	}
	s := SimpleScenario("churn", "fake", "op")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Version = i + 1
		if err := h.Runtime.Registry().RegisterScenarios(s); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGoldenBaselineLookup(b *testing.B) {
	h := benchHarness(b)
	s := SimpleScenario("bench", "fake", "op")
	h.Register(s)
	h.Approve(s)
	key := s.Key()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := h.Runtime.Goldens().Baseline(key); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDetectRegressions(b *testing.B) {
	var baseResults, curResults []ScenarioResult
	for i := 0; i < 50; i++ {
		id := ScenarioID(fmt.Sprintf("s%02d", i))
		obs := Observation{Scenario: id, ScenarioVersion: 1,
			Steps: []StepObservation{{Name: "a", Op: "op", Outcome: "ok",
				Duration: time.Millisecond}}}
		baseResults = append(baseResults, ScenarioResult{
			Scenario: id, Subject: "fake", Verdict: VerdictPass, Observation: obs})
		curResults = append(curResults, ScenarioResult{
			Scenario: id, Subject: "fake", Verdict: VerdictPass, Observation: obs})
	}
	base := Run{ID: "base", Results: baseResults}
	cur := Run{ID: "cur", Results: curResults}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = DetectRegressions(base, cur, 2, 0)
	}
}

func BenchmarkSummarise(b *testing.B) {
	samples := make([]time.Duration, 200)
	for i := range samples {
		samples[i] = time.Duration(i) * time.Microsecond
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Summarise(samples)
	}
}

func BenchmarkScorecards(b *testing.B) {
	var results []ScenarioResult
	for i := 0; i < 50; i++ {
		results = append(results, ScenarioResult{
			Scenario: ScenarioID(fmt.Sprintf("s%02d", i)),
			Subject:  SubjectName(fmt.Sprintf("subj%d", i%5)),
			Verdict:  Verdict(i % 5), Kind: ScenarioKind(i % 8),
			Observation: benchObservation(3),
		})
	}
	run := Run{ID: "r", Results: results}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Scorecards(run)
	}
}

func BenchmarkDashboard(b *testing.B) {
	h := benchHarness(b)
	for i := 0; i < 20; i++ {
		h.Register(StepsScenario(ScenarioID(fmt.Sprintf("s%02d", i)), "fake",
			ScenarioKind(i%8), "op"))
	}
	run := h.Run("bench")
	coord := h.Runtime.Coordinator()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = coord.Dashboard(run, 20)
	}
}

func BenchmarkStoragePutRun(b *testing.B) {
	storage := NewBoundedMemoryStorage(1000, 1000, 1000)
	run := Run{ID: "r", Results: []ScenarioResult{
		{Scenario: "s", Subject: "fake", Verdict: VerdictPass,
			Observation: benchObservation(5)}}}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		run.ID = RunID(fmt.Sprintf("r%08d", i))
		if err := storage.PutRun(run); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkClockResolution measures platform construction's one expensive step.
// It spins until the clock moves, so on a coarse clock it costs a full tick.
func BenchmarkClockResolution(b *testing.B) {
	h := benchHarness(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ClockResolution(h.Runtime.Clock())
	}
}

func benchObservation(steps int) Observation {
	obs := Observation{Scenario: "bench", Subject: "fake", ScenarioVersion: 1,
		Metrics: map[string]float64{"step_count": float64(steps)}}
	for i := 0; i < steps; i++ {
		obs.Steps = append(obs.Steps, StepObservation{
			Name: fmt.Sprintf("s%02d", i), Op: "op", Outcome: "ok",
			Output:   Values{"answer": S("an observed value"), "n": N(float64(i))},
			State:    Values{"steps": N(float64(i + 1)), "last": S("op")},
			Events:   []EventRecord{{Type: "step_done", Fields: Values{"op": S("op")}}},
			Duration: time.Duration(i) * time.Microsecond,
		})
	}
	return obs
}
