// Final platform verification for Phase 10F.
//
// This file is the brief's end-to-end pass: every scenario in the library run
// against the five REAL frozen engines — Runtime Core, Conversation Engine,
// Memory Engine, Tool Runtime, Governance Engine — with determinism, replay,
// regression, benchmark, concurrency, stress and failure-injection coverage,
// terminating in the consolidated platform readiness report.
//
// What makes it a verification rather than a test suite: nothing here asserts
// what a subsystem should do. Every check is either a platform-level property
// (the same input reproduces, a clean run has no regressions against itself) or
// a comparison against an approved golden. A subsystem changing its behaviour
// shows up as drift for a human to rule on, not as a failed assertion written
// by whoever happened to author the scenario.
package evalsubjects

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	ev "github.com/callscreen/callscreen-platform/packages/go/evaluation"
)

// platform builds a runtime carrying all five real adapters and the full
// scenario library.
//
// Each test gets its own runtime. Two runtimes share nothing — separate
// registry, golden store, storage and metrics — which is what lets this file
// run parallel without the golden store of one test deciding the verdicts of
// another.
func platform(t *testing.T) *ev.EvaluationRuntime {
	t.Helper()

	r, err := ev.New(ev.DefaultConfig(), ev.NewSubjectSet(All()...))
	if err != nil {
		t.Fatalf("build runtime: %v", err)
	}
	if err := r.Registry().RegisterScenarios(Library()...); err != nil {
		t.Fatalf("register scenarios: %v", err)
	}
	if err := r.Registry().RegisterSuites(Suites()...); err != nil {
		t.Fatalf("register suites: %v", err)
	}
	if err := r.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Stop() })
	return r
}

// bootstrap approves a baseline for every registered scenario.
//
// A verification run against a store with no goldens reports NoBaseline for
// everything, which is not a verification of anything. This performs the
// approval a human would perform on first adoption, with the attribution the
// store demands — the point being that the approval is explicit and recorded,
// not that it is difficult.
//
// It approves THE CANDIDATE THE PLATFORM FILED, rather than recording a second
// golden from the same observation. That is the real adoption path — execute,
// the platform files a candidate, a human rules on it — and going through it
// here means the verification exercises the workflow operators will use instead
// of a parallel one that only tests exercise. The first draft recorded a second
// golden, which worked but left every auto-filed candidate sitting unapproved:
// eighteen scenarios, eighteen phantom entries in the review queue.
func bootstrap(t *testing.T, r *ev.EvaluationRuntime) {
	t.Helper()

	ctx := context.Background()
	for _, s := range r.Registry().Scenarios() {
		res := r.Execute(ctx, s)
		if res.Observation.Failed() {
			t.Fatalf("scenario %s failed against the real subject before any "+
				"baseline existed: %s", s.ID, res.Comparison.Reason)
		}
		if res.Verdict != ev.VerdictNoBaseline {
			t.Fatalf("scenario %s reported %s on a store with no goldens",
				s.ID, res.Verdict)
		}
		if res.Candidate == "" {
			t.Fatalf("scenario %s filed no candidate: nothing to approve", s.ID)
		}
		if _, err := r.Goldens().Approve(res.Candidate,
			"phase-10f-verification", "initial baseline from the frozen engines"); err != nil {
			t.Fatalf("approve baseline for %s: %v", s.ID, err)
		}
	}

	if pending := r.Goldens().PendingCount(); pending != 0 {
		t.Fatalf("bootstrap left %d candidates unapproved", pending)
	}
}

// ---------------------------------------------------------------------------
// Registration and coverage
// ---------------------------------------------------------------------------

// TestVerification_EverySubjectIsEvaluated is the check the readiness report
// exists to make loud.
//
// A subsystem with no scenarios scores nothing and appears nowhere. It would
// leave a readiness report entirely green while saying nothing whatsoever about
// the subsystem, which is worse than a red one.
func TestVerification_EverySubjectIsEvaluated(t *testing.T) {
	t.Parallel()
	r := platform(t)

	scenarioSubjects := make(map[ev.SubjectName]int)
	for _, s := range r.Registry().Scenarios() {
		scenarioSubjects[s.SubjectName]++
	}

	for _, name := range r.Subjects().Names() {
		if scenarioSubjects[name] == 0 {
			t.Errorf("subject %s has no scenarios: it would be invisible in the "+
				"readiness report", name)
			continue
		}
		t.Logf("%-14s %d scenarios", name, scenarioSubjects[name])
	}

	if len(scenarioSubjects) != 5 {
		t.Errorf("expected scenarios across all 5 frozen phases, got %d subjects",
			len(scenarioSubjects))
	}
}

// TestVerification_Coverage records what the library reaches, and reports what
// it does not.
//
// Uncovered scenario kinds are logged rather than failed. The library covering
// eight kinds is a fact about this phase's scenario set, not a platform
// invariant, and a test that failed on it would be a test of my own scenario
// authoring rather than of the platform.
func TestVerification_Coverage(t *testing.T) {
	t.Parallel()
	r := platform(t)

	cov := r.Registry().Coverage()
	t.Logf("scenarios=%d subjects=%d kinds=%d suites=%d registry-digest=%s",
		r.Registry().Len(), len(cov.BySubject), len(cov.ByKind),
		len(r.Registry().Suites()), r.Registry().Digest())

	for _, k := range ev.AllScenarioKinds() {
		t.Logf("  %-14s %d", k, cov.ByKind[k])
	}
	if uncovered := cov.UncoveredKinds(); len(uncovered) > 0 {
		t.Logf("KINDS WITH NO COVERAGE: %v", uncovered)
	}
}

// ---------------------------------------------------------------------------
// End-to-end evaluation against the five frozen engines
// ---------------------------------------------------------------------------

// TestVerification_EndToEnd runs every scenario against the real engines.
//
// The brief's central pass. It runs twice: once to establish that the engines
// execute at all and to take baselines, once to compare against them. The
// second run is the one that means something — a platform that only ever
// records is a platform that agrees with itself.
func TestVerification_EndToEnd(t *testing.T) {
	t.Parallel()
	r := platform(t)
	bootstrap(t, r)

	run := r.RunAll(context.Background(), "phase-10f-verification")
	report := r.Coordinator().Report(run)

	t.Logf("%s", report.Summary())

	counts := run.Counts()
	if counts[ev.VerdictPass] != len(run.Results) {
		t.Errorf("expected every scenario to reproduce its approved baseline, got %v",
			counts)
		for _, res := range run.Results {
			if res.Verdict != ev.VerdictPass {
				t.Errorf("  %s/%s: %s — %s",
					res.Subject, res.Scenario, res.Verdict, res.Comparison.Summary())
			}
		}
	}
	if !report.Releasable() {
		t.Errorf("run is not releasable: %d blockers", len(report.Blocking))
	}
}

// TestVerification_GatingSuites runs the two suites that gate a release.
func TestVerification_GatingSuites(t *testing.T) {
	t.Parallel()
	r := platform(t)
	bootstrap(t, r)

	ctx := context.Background()
	for _, suite := range r.Registry().Suites() {
		run, err := r.RunSuite(ctx, suite.ID, "phase-10f-verification")
		if err != nil {
			t.Fatalf("run suite %s: %v", suite.ID, err)
		}
		blocking := run.Blocking()
		t.Logf("suite %-11s kind=%-11s gating=%-5v scenarios=%2d blockers=%d in %s",
			suite.ID, suite.Kind, suite.Kind.Gating(), len(run.Results),
			len(blocking), run.Duration().Round(time.Microsecond))
		for _, b := range blocking {
			t.Errorf("  BLOCKER %s/%s: %s", b.Subject, b.Scenario, b.Comparison.Reason)
		}
	}
}

// TestVerification_DriftIsDetectedAndFiled proves the platform's whole purpose
// on the real engines: a changed baseline produces drift, and drift produces a
// candidate for a human to rule on.
//
// Constructed by approving a baseline and then evaluating a scenario whose
// steps differ — the same shape as an engine changing behaviour under a
// baseline that was approved before the change.
func TestVerification_DriftIsDetectedAndFiled(t *testing.T) {
	t.Parallel()
	r := platform(t)

	ctx := context.Background()
	original := Library()[0]

	res := r.Execute(ctx, original)
	if _, err := r.Goldens().RecordAndApprove(original, res.Observation,
		"phase-10f-verification", "baseline"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// Same ID and version, different steps: what a scenario edited without a
	// version bump looks like, and what an engine whose behaviour moved looks
	// like from the platform's side.
	changed := original
	changed.Steps = append(append([]ev.Step(nil), original.Steps...), original.Steps[0])

	drift := r.Execute(ctx, changed)
	if drift.Verdict != ev.VerdictDrift {
		t.Fatalf("expected drift when behaviour changes under an approved "+
			"baseline, got %s: %s", drift.Verdict, drift.Comparison.Summary())
	}
	if drift.Candidate == "" {
		t.Error("drift filed no golden candidate: the reviewer has nothing to " +
			"promote at exactly the moment their decision is required")
	}
	if r.Goldens().PendingCount() == 0 {
		t.Error("pending count did not move: the operator queue is empty during drift")
	}
	t.Logf("drift detected and filed as candidate %s: %s",
		drift.Candidate, drift.Comparison.Summary())
}

// ---------------------------------------------------------------------------
// Determinism
// ---------------------------------------------------------------------------

// TestVerification_Determinism repeats every scenario against its real engine.
//
// Determinism is a precondition for every other claim this platform makes. A
// subsystem that does not reproduce makes drift meaningless: the platform
// cannot distinguish "the engine changed" from "the engine is noisy", and every
// golden becomes a coin flip.
func TestVerification_Determinism(t *testing.T) {
	t.Parallel()
	r := platform(t)

	ctx := context.Background()
	var nondeterministic []string

	for _, s := range r.Registry().Scenarios() {
		d := r.CheckDeterminism(ctx, s, 3)
		if !d.Deterministic {
			nondeterministic = append(nondeterministic, d.Summary())
		}
	}

	if len(nondeterministic) > 0 {
		t.Errorf("%d scenarios did not reproduce across 3 runs:", len(nondeterministic))
		for _, s := range nondeterministic {
			t.Errorf("  %s", s)
		}
		return
	}
	t.Logf("all %d scenarios deterministic across 3 runs against the real engines",
		r.Registry().Len())
}

// ---------------------------------------------------------------------------
// Replay
// ---------------------------------------------------------------------------

// TestVerification_Replay re-executes every approved golden.
//
// Replay differs from a fresh run in what it holds fixed: the recorded
// observation, not merely the scenario. It answers "does the engine still do
// what we recorded", which is the question a regression investigation asks
// months after the recording, when the scenario file may itself have moved.
func TestVerification_Replay(t *testing.T) {
	t.Parallel()
	r := platform(t)
	bootstrap(t, r)

	ctx := context.Background()
	var diverged int

	for _, s := range r.Registry().Scenarios() {
		rep, err := r.ReplayLatest(ctx, s)
		if err != nil {
			t.Errorf("replay %s: %v", s.ID, err)
			continue
		}
		if !rep.Reproduced {
			diverged++
			t.Errorf("replay diverged: %s", rep.Summary())
		}
	}
	if diverged == 0 {
		t.Logf("all %d approved goldens replayed against the real engines",
			r.Registry().Len())
	}
}

// TestVerification_ReplayDetectsADivergentRecording confirms replay can fail.
//
// A replay engine that reproduces everything is indistinguishable from a replay
// engine that compares nothing, and the second is the more likely bug. Replaying
// a scenario against a recording taken from a DIFFERENT scenario must diverge.
func TestVerification_ReplayDetectsADivergentRecording(t *testing.T) {
	t.Parallel()
	r := platform(t)

	ctx := context.Background()
	lib := Library()

	// Two scenarios on the same subject, so the mismatch is behavioural rather
	// than a subject lookup failure.
	var a, b ev.Scenario
	for i := range lib {
		for j := range lib {
			if i != j && lib[i].SubjectName == lib[j].SubjectName {
				a, b = lib[i], lib[j]
			}
		}
		if a.ID != "" {
			break
		}
	}
	if a.ID == "" {
		t.Skip("library has no two scenarios sharing a subject")
	}

	foreign := r.Execute(ctx, b).Observation
	foreign.Scenario = a.ID // as if recorded for a

	rep := r.Replay(ctx, a, foreign)
	if rep.Reproduced {
		t.Errorf("replaying %s against a recording of %s reproduced: the replay "+
			"engine is not comparing", a.ID, b.ID)
	}
	t.Logf("divergence correctly reported: %s", rep.Summary())
}

// ---------------------------------------------------------------------------
// Regression
// ---------------------------------------------------------------------------

// TestVerification_RegressionCleanRunOverRun compares two identical runs.
//
// The false-positive check. A regression engine that reports a regression
// between a run and itself is an engine nobody will read after the second week,
// and a noisy gate is functionally the same as no gate.
func TestVerification_RegressionCleanRunOverRun(t *testing.T) {
	t.Parallel()
	r := platform(t)
	bootstrap(t, r)

	ctx := context.Background()
	baseline := r.RunAll(ctx, "baseline")
	current := r.RunAll(ctx, "current")

	// A generous latency ratio: two runs on a loaded CI box differ in wall time
	// for reasons that have nothing to do with the engines, and the point of
	// this test is behavioural regression.
	report := ev.DetectRegressions(baseline, current, 4.0,
		r.Config().Tolerances.LatencyFloor)
	t.Logf("%s", report.Summary())

	for _, reg := range report.Blocking() {
		t.Errorf("regression between two identical runs: %s", reg)
	}
}

// TestVerification_RegressionDetectsARealChange confirms the engine fires.
func TestVerification_RegressionDetectsARealChange(t *testing.T) {
	t.Parallel()
	r := platform(t)
	bootstrap(t, r)

	ctx := context.Background()
	baseline := r.RunAll(ctx, "baseline")

	// Retire one baseline. The next run reports NoBaseline for that scenario,
	// which is a genuine change in the platform's state between the two runs.
	scenarios := r.Registry().Scenarios()
	target := scenarios[0]
	if err := r.Goldens().Retire(target.Key(), "phase-10f-verification",
		"verifying the regression engine reacts"); err != nil {
		t.Fatalf("retire: %v", err)
	}

	current := r.RunAll(ctx, "current")
	report := ev.DetectRegressions(baseline, current, 4.0,
		r.Config().Tolerances.LatencyFloor)

	if report.Clean() {
		t.Errorf("retiring the baseline for %s produced no regression: the "+
			"engine is not comparing verdicts", target.ID)
	}
	t.Logf("%s", report.Summary())
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

// TestVerification_Benchmarks measures every scenario against its real engine.
//
// The measurements are LOGGED, not asserted. A latency threshold in a test that
// runs on unknown hardware fails for reasons that have nothing to do with the
// code, and a suite that fails for reasons unrelated to the code is a suite
// people learn to re-run rather than read.
//
// What IS checked is that the platform reports honestly when a measurement sits
// below the clock's resolution — on this machine roughly 520 µs, which is
// larger than most of these scenarios take.
func TestVerification_Benchmarks(t *testing.T) {
	t.Parallel()
	r := platform(t)

	ctx := context.Background()
	res := r.ClockResolution()
	t.Logf("measured clock resolution: %s", res)

	var below int
	for _, s := range r.Registry().Scenarios() {
		b := r.Benchmark(ctx, s, 40, 5, "phase-10f-verification")
		t.Logf("%s", b.Summary())

		if b.Iterations != 40 {
			t.Errorf("%s: expected 40 measured iterations, got %d", s.ID, b.Iterations)
		}
		if b.AmortisedMean <= 0 {
			t.Errorf("%s: amortised mean is zero — the benchmark measured nothing",
				s.ID)
		}
		if b.BelowResolution {
			below++
			if b.Resolution != res {
				t.Errorf("%s: reported resolution %s, runtime measured %s",
					s.ID, b.Resolution, res)
			}
		}
	}
	t.Logf("%d of %d scenarios have per-iteration latency below clock resolution "+
		"and are reported as amortised", below, r.Registry().Len())
}

// ---------------------------------------------------------------------------
// Concurrency and stress
// ---------------------------------------------------------------------------

// TestVerification_ConcurrentEvaluation hammers one runtime from many
// goroutines against the real engines.
//
// Without -race this proves less than it looks like it does — see the audit's
// blocking finding A2 — but it still exercises the copy-on-write registry, the
// golden store's lock and the shared metrics under genuine contention, and a
// data race severe enough to corrupt a verdict would surface here.
func TestVerification_ConcurrentEvaluation(t *testing.T) {
	t.Parallel()
	r := platform(t)
	bootstrap(t, r)

	const goroutines, iterations = 16, 12

	scenarios := r.Registry().Scenarios()
	ctx := context.Background()

	var (
		mu       sync.Mutex
		verdicts = map[ev.Verdict]int{}
		start    = make(chan struct{})
		wg       sync.WaitGroup
	)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			<-start
			local := map[ev.Verdict]int{}
			for i := 0; i < iterations; i++ {
				s := scenarios[(g+i)%len(scenarios)]
				local[r.Execute(ctx, s).Verdict]++
			}
			mu.Lock()
			for v, n := range local {
				verdicts[v] += n
			}
			mu.Unlock()
		}(g)
	}

	close(start)
	wg.Wait()

	total := goroutines * iterations
	if verdicts[ev.VerdictPass] != total {
		t.Errorf("expected %d passes under concurrency, got %v", total, verdicts)
	}
	t.Logf("%d concurrent evaluations across %d goroutines: %v",
		total, goroutines, verdicts)
}

// TestVerification_ConcurrentRegistryMutation evaluates while the registry is
// being rewritten.
//
// The copy-on-write claim under load: a reader holding a snapshot must finish
// its run against a coherent scenario set even as new versions are published
// beneath it.
func TestVerification_ConcurrentRegistryMutation(t *testing.T) {
	t.Parallel()
	r := platform(t)
	bootstrap(t, r)

	ctx := context.Background()
	scenarios := r.Registry().Scenarios()

	done := make(chan struct{})
	var writes int
	go func() {
		defer close(done)
		for i := 0; i < 40; i++ {
			s := scenarios[i%len(scenarios)]
			s.ID = ev.ScenarioID(fmt.Sprintf("churn.%d", i))
			if err := r.Registry().RegisterScenarios(s); err == nil {
				writes++
			}
		}
	}()

	var evaluations int
	for i := 0; i < 200; i++ {
		res := r.Execute(ctx, scenarios[i%len(scenarios)])
		if res.Verdict == ev.VerdictFail {
			t.Errorf("evaluation failed during registry churn: %s",
				res.Comparison.Reason)
		}
		evaluations++
	}
	<-done

	t.Logf("%d evaluations against %d concurrent registry publications; "+
		"final version=%d len=%d",
		evaluations, writes, r.Registry().Version(), r.Registry().Len())
}

// TestVerification_Stress runs sustained load through the whole pipeline.
func TestVerification_Stress(t *testing.T) {
	if testing.Short() {
		t.Skip("stress run skipped under -short")
	}
	t.Parallel()
	r := platform(t)
	bootstrap(t, r)

	ctx := context.Background()
	const runs = 25

	started := time.Now()
	for i := 0; i < runs; i++ {
		run := r.RunAll(ctx, fmt.Sprintf("stress-%d", i))
		for _, b := range run.Blocking() {
			t.Fatalf("stress run %d blocked on %s: %s", i, b.Scenario,
				b.Comparison.Reason)
		}
	}
	elapsed := time.Since(started)

	stats := r.Storage().(*ev.MemoryStorage).Stats()
	t.Logf("%d full runs (%d scenario executions) in %s; storage: %d runs, "+
		"%d observations, %d trend points (bounded)",
		runs, runs*r.Registry().Len(), elapsed.Round(time.Millisecond),
		stats.Runs, stats.Observations, stats.TrendPoints)

	if r.ScenariosExecuted() < uint64(runs*r.Registry().Len()) {
		t.Errorf("runtime counted %d scenario executions, expected at least %d",
			r.ScenariosExecuted(), runs*r.Registry().Len())
	}
}

// ---------------------------------------------------------------------------
// Failure injection
// ---------------------------------------------------------------------------

// TestVerification_FailureInjection drives each engine's declared failure
// modes through the real adapters.
//
// The property under check is not that the engine fails — it is that an
// injected failure is reported as an OUTCOME rather than as a platform error. A
// governance engine denying a request is the engine working correctly; a
// platform that recorded that as its own failure would make every safety
// scenario unreadable.
func TestVerification_FailureInjection(t *testing.T) {
	t.Parallel()
	r := platform(t)

	ctx := context.Background()
	var injected int

	for _, s := range r.Registry().Scenarios() {
		if s.Kind != ev.KindFailure {
			continue
		}
		injected++
		res := r.Execute(ctx, s)

		if res.Verdict == ev.VerdictFail {
			t.Errorf("failure-injection scenario %s was recorded as a PLATFORM "+
				"failure: %s", s.ID, res.Comparison.Reason)
			continue
		}
		var outcomes []string
		for _, step := range res.Observation.Steps {
			outcomes = append(outcomes, fmt.Sprintf("%s=%s", step.Name, step.Outcome))
		}
		t.Logf("%-34s %s [%s]", s.ID, res.Verdict, strings.Join(outcomes, " "))
	}

	if injected == 0 {
		t.Error("no failure-injection scenarios in the library: the injection " +
			"framework is unexercised against the real engines")
	}
	t.Logf("%d failure-injection scenarios exercised against the real engines",
		injected)
}

// TestVerification_InjectionCapabilitiesAreDeclared checks every injection a
// scenario requires is one its subject admits to supporting.
//
// A scenario asking for an injection the subject cannot perform would otherwise
// run, do nothing, and pass — the quietest possible way for safety coverage to
// evaporate.
func TestVerification_InjectionCapabilitiesAreDeclared(t *testing.T) {
	t.Parallel()
	r := platform(t)

	for _, s := range r.Registry().Scenarios() {
		required := ev.RequiredInjections(s)
		if len(required) == 0 {
			continue
		}
		subject, ok := r.Subjects().Get(s.SubjectName)
		if !ok {
			t.Errorf("scenario %s names unknown subject %s", s.ID, s.SubjectName)
			continue
		}
		declared := make(map[ev.Capability]bool)
		for _, c := range subject.Capabilities() {
			declared[c] = true
		}
		for _, need := range required {
			if !declared[need] {
				t.Errorf("scenario %s requires %s but subject %s does not declare it",
					s.ID, need, s.SubjectName)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Consolidated platform readiness
// ---------------------------------------------------------------------------

// TestVerification_PlatformReadiness is the brief's final artifact.
//
// It runs everything against all five frozen phases and emits the consolidated
// verdict. The log output of this test is the evidence behind
// docs/evaluation/PLATFORM_READINESS_REPORT.md — the document quotes it rather
// than restating it from memory, which is the whole reason the platform exists.
func TestVerification_PlatformReadiness(t *testing.T) {
	t.Parallel()
	r := platform(t)
	bootstrap(t, r)

	ctx := context.Background()
	readiness := r.Coordinator().Readiness(ctx, "phase-10f-final-verification", 3)

	t.Logf("\n%s", readiness.Summary())

	if len(readiness.UnevaluatedSubjects) > 0 {
		t.Errorf("subsystems with no scenarios: %v", readiness.UnevaluatedSubjects)
	}
	for _, d := range readiness.Determinism {
		if !d.Deterministic {
			t.Errorf("subsystem not deterministic: %s", d.Summary())
		}
	}
	for _, b := range readiness.Blockers {
		t.Errorf("BLOCKER %s/%s: %s", b.Subject, b.Scenario, b.Comparison.Reason)
	}
	if !readiness.Ready {
		t.Error("platform readiness reports not ready")
	}
}

// TestVerification_Dashboard exercises the reporting engine end to end.
//
// The dashboard is a model, not a UI — the brief excludes the UI. What is
// verified is that the model an interface would render is populated: every
// subsystem present, the heatmaps addressable, the trend series non-empty.
func TestVerification_Dashboard(t *testing.T) {
	t.Parallel()
	r := platform(t)
	bootstrap(t, r)

	ctx := context.Background()
	for i := 0; i < 4; i++ {
		r.RunAll(ctx, fmt.Sprintf("history-%d", i))
	}
	run := r.RunAll(ctx, "current")

	dash := r.Coordinator().Dashboard(run, 5)

	if len(dash.Subjects) != 5 {
		t.Errorf("dashboard shows %d subsystems, expected all 5 frozen phases",
			len(dash.Subjects))
	}
	if len(dash.Trends) == 0 {
		t.Error("dashboard has no trend series: the run history is not reaching it")
	}
	if !dash.Summary.Releasable {
		t.Errorf("dashboard headline reports not releasable: %d failed",
			dash.Summary.Failed)
	}

	names := make([]string, 0, len(dash.Subjects))
	for _, s := range dash.Subjects {
		names = append(names, fmt.Sprintf("%s(%d)", s.Subject, s.Scorecard.Total))
	}
	sort.Strings(names)

	var unstable int
	for _, tr := range dash.Trends {
		if !tr.Stable {
			unstable++
			t.Errorf("scenario %s changed behaviour %d times across identical "+
				"runs", tr.Scenario, tr.BehaviourChanges)
		}
	}
	t.Logf("dashboard: subsystems=%s trends=%d unstable=%d",
		strings.Join(names, " "), len(dash.Trends), unstable)

	for _, cell := range dash.Failures.Cells {
		if cell.Value > 0 {
			t.Logf("failure heatmap %s/%s = %.2f over %d samples",
				cell.Row, cell.Col, cell.Value, cell.Count)
		}
	}
}

// TestVerification_MetricsAreEmitted checks the platform observes itself.
func TestVerification_MetricsAreEmitted(t *testing.T) {
	t.Parallel()
	r := platform(t)
	bootstrap(t, r)

	r.RunAll(context.Background(), "metrics")

	if r.Runs() == 0 {
		t.Error("runtime counted no runs")
	}
	if r.ScenariosExecuted() == 0 {
		t.Error("runtime counted no scenario executions")
	}
	t.Logf("runs=%d scenarios=%d goldens=%d pending=%d",
		r.Runs(), r.ScenariosExecuted(), r.Goldens().Len(), r.Goldens().PendingCount())
}
