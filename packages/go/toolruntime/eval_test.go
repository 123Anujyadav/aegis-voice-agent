package toolruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Evaluation harness for docs/tools/EXECUTION_EVALUATION.md.
//
// The other suites answer "is this built correctly". These answer a different
// question: does the runtime execute the right things, refuse the right things,
// and leave a truthful account of both? Every figure in the evaluation report
// comes from here, so a change that weakens deduplication or leaves a failure
// unattributed shows up as a changed number rather than as a document nobody
// reprinted.
//
// They assert as well as measure. A report produced by tests that cannot fail
// is a press release.

// evalHarness builds a runtime with a small tool catalogue covering every
// effect class.
func evalHarness(t *testing.T) *Harness {
	t.Helper()
	h := newHarness(t)

	h.Register(ReadContract("lookup", "1.0.0", "lookup"), &FakeTool{})
	h.Register(ReadContract("enrich", "1.0.0", "enrich"), &FakeTool{})
	h.Register(WriteContract("booker", "1.0.0", "book"), &CompensatingFake{})
	h.Register(WriteContract("notifier", "1.0.0", "notify"), &CompensatingFake{})
	return h
}

func evalIntent(reqs ...CapabilityRequest) ToolIntent {
	return ToolIntent{
		ID: NewIntentID(), Correlation: NewCorrelationID(), Session: "sess",
		Actor: "actor", Grant: Grant{Actor: "actor"}, Requests: reqs,
	}
}

func read(ref string, cap CapabilityID, deps ...string) CapabilityRequest {
	return CapabilityRequest{Ref: ref, Capability: cap, Version: AnyVersion(),
		Args: Arguments{"query": String(ref)}, DependsOn: deps}
}

func write(ref string, cap CapabilityID, deps ...string) CapabilityRequest {
	return CapabilityRequest{Ref: ref, Capability: cap, Version: AnyVersion(),
		Args: Arguments{"subject": String(ref)}, DependsOn: deps}
}

// ---------------------------------------------------------------------------
// E1 · Execution fidelity
// ---------------------------------------------------------------------------

// TestEvaluation_EveryPlanShapeExecutesAsPlanned measures whether what runs
// matches what was planned: the same tools, the same count, no extras.
//
// The number that matters is not "did it succeed" but "did it do exactly what
// the plan said". A runtime that quietly invokes a tool the plan did not name
// is one whose plans cannot be reviewed, which removes the point of having them.
func TestEvaluation_EveryPlanShapeExecutesAsPlanned(t *testing.T) {
	cases := []struct {
		name  string
		reqs  []CapabilityRequest
		shape string
		steps int
	}{
		{"single", []CapabilityRequest{read("a", "lookup")}, "single", 1},
		{"sequential", []CapabilityRequest{read("a", "lookup"), read("b", "enrich", "a")}, "sequential", 2},
		{"parallel", []CapabilityRequest{read("a", "lookup"), read("b", "enrich")}, "parallel", 2},
		{"mixed", []CapabilityRequest{read("a", "lookup"), read("b", "enrich"),
			write("c", "book", "a", "b")}, "mixed", 3},
	}

	matched := 0
	for _, tc := range cases {
		h := evalHarness(t)
		intent := evalIntent(tc.reqs...)

		plan, err := h.Runtime.Plan(intent)
		if err != nil {
			t.Fatalf("%s: plan: %v", tc.name, err)
		}
		res, err := h.Runtime.Run(context.Background(), plan, intent.Grant, NoopSink{})
		if err != nil {
			t.Fatalf("%s: run: %v", tc.name, err)
		}

		planned := map[Descriptor]int{}
		for _, s := range plan.Steps() {
			planned[s.Descriptor]++
		}
		executed := map[Descriptor]int{}
		for _, s := range res.Steps {
			if s.OK() {
				executed[s.Descriptor]++
			}
		}

		ok := plan.Shape == tc.shape && plan.StepCount() == tc.steps && len(executed) == len(planned)
		for d, n := range planned {
			if executed[d] != n {
				ok = false
			}
		}
		if ok {
			matched++
		} else {
			t.Errorf("%s: plan and execution disagree\nplan=%v\nexecuted=%v\n%s",
				tc.name, planned, executed, plan.Explain())
		}
		t.Logf("%-12s shape=%-10s steps=%d executed=%d fidelity=%v",
			tc.name, plan.Shape, plan.StepCount(), len(executed), ok)
	}

	t.Logf("plan/execution fidelity: %d of %d shapes", matched, len(cases))
	if matched != len(cases) {
		t.Errorf("only %d of %d shapes executed exactly as planned", matched, len(cases))
	}
}

// ---------------------------------------------------------------------------
// E2 · Deduplication effectiveness
// ---------------------------------------------------------------------------

// TestEvaluation_DuplicateSuppressionUnderARetryStorm measures the property the
// idempotency ledger exists for, under the condition it exists for.
func TestEvaluation_DuplicateSuppressionUnderARetryStorm(t *testing.T) {
	sizes := []int{2, 8, 32, 64}

	for _, n := range sizes {
		h := evalHarness(t)
		tool := &CompensatingFake{}
		h.Register(WriteContract("target", "1.0.0", "target"), tool)

		corr := NewCorrelationID()
		var wg sync.WaitGroup
		var served atomic.Int64
		start := make(chan struct{})

		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				res, _ := h.Runtime.Execute(context.Background(), ToolIntent{
					ID: NewIntentID(), Correlation: corr, Actor: "actor",
					Grant:    Grant{Actor: "actor"},
					Requests: []CapabilityRequest{write("t", "target")},
				})
				if res.OK() {
					served.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()

		invocations := int64(tool.Calls())
		suppression := 1 - float64(invocations)/float64(n)
		t.Logf("callers=%-3d invocations=%d served=%d suppression=%.1f%%",
			n, invocations, served.Load(), 100*suppression)

		if invocations != 1 {
			t.Errorf("%d identical callers produced %d tool invocations, want 1", n, invocations)
		}
		if served.Load() != int64(n) {
			t.Errorf("%d of %d callers went away without an answer", int64(n)-served.Load(), n)
		}
	}
}

// ---------------------------------------------------------------------------
// E3 · Compensation completeness
// ---------------------------------------------------------------------------

// TestEvaluation_CompensationUndoesEverythingItCan measures the fraction of
// completed mutating work that is undone when a plan fails partway.
//
// The interesting number is not the success rate but whether anything is left
// silently un-undone. A rollback that reports itself complete while something
// survived is worse than one that refuses.
func TestEvaluation_CompensationUndoesEverythingItCan(t *testing.T) {
	depths := []int{1, 2, 4}

	for _, depth := range depths {
		h := newHarness(t)

		tools := make([]*CompensatingFake, depth)
		var reqs []CapabilityRequest
		for i := 0; i < depth; i++ {
			tools[i] = &CompensatingFake{}
			cap := CapabilityID(fmt.Sprintf("step%d", i))
			h.Register(WriteContract(ToolID(fmt.Sprintf("t%d", i)), "1.0.0", cap), tools[i])
			var deps []string
			if i > 0 {
				deps = []string{fmt.Sprintf("s%d", i-1)}
			}
			reqs = append(reqs, write(fmt.Sprintf("s%d", i), cap, deps...))
		}

		boom := WriteContract("boom", "1.0.0", "boom")
		boom.Compensable = false
		boom.Retry = RetrySpec{MaxAttempts: 1}
		h.Register(boom, &WriteFake{FakeTool: FakeTool{FailAlways: true}})
		reqs = append(reqs, write("boom", "boom", fmt.Sprintf("s%d", depth-1)))

		res, _ := h.Runtime.Execute(context.Background(), evalIntent(reqs...))
		if res.OK() {
			t.Fatalf("depth %d: plan should have failed", depth)
		}
		if res.Compensation == nil {
			t.Fatalf("depth %d: no compensation report", depth)
		}

		undone := 0
		for _, tool := range tools {
			if tool.Compensations() == 1 {
				undone++
			}
		}
		rate := float64(undone) / float64(depth)
		t.Logf("mutating steps=%d compensated=%d skipped=%d failed=%d rate=%.0f%% complete=%v",
			depth, res.Compensation.Compensated, res.Compensation.Skipped,
			res.Compensation.Failed, 100*rate, res.Compensation.Complete)

		if undone != depth {
			t.Errorf("depth %d: %d of %d completed mutations were left in place", depth, depth-undone, depth)
		}
		if !res.Compensation.Complete {
			t.Errorf("depth %d: every journalled step was undone, so the report should "+
				"say so: %+v", depth, res.Compensation)
		}
	}

	// The measure above is "was everything the runtime KNOWS about undone". It
	// is deliberately not the same as "is the world back where it started": the
	// step that failed may have taken partial effect before failing, and no
	// runtime can know whether it did. That gap is recorded in
	// EXECUTION_EVALUATION.md rather than papered over with a success rate.
	//
	// The second case is the one where the runtime CAN tell: a completed step
	// that cannot be undone. The report must refuse to claim completeness.
	h := newHarness(t)

	stuck := WriteContract("stuck", "1.0.0", "stuck")
	stuck.Compensable = false
	h.Register(stuck, &WriteFake{})

	boom := WriteContract("boom", "1.0.0", "boom")
	boom.Compensable = false
	boom.Retry = RetrySpec{MaxAttempts: 1}
	h.Register(boom, &WriteFake{FakeTool: FakeTool{FailAlways: true}})

	res, _ := h.Runtime.Execute(context.Background(),
		evalIntent(write("s", "stuck"), write("b", "boom", "s")))

	if res.Compensation == nil {
		t.Fatal("no compensation report for a completed un-compensable step")
	}
	t.Logf("un-compensable completed step: compensated=%d skipped=%d complete=%v",
		res.Compensation.Compensated, res.Compensation.Skipped, res.Compensation.Complete)

	if res.Compensation.Skipped != 1 {
		t.Errorf("expected 1 skipped compensation, got %+v", res.Compensation)
	}
	if res.Compensation.Complete {
		t.Error("a rollback that could not undo a completed mutation must not report itself complete")
	}
}

// ---------------------------------------------------------------------------
// E4 · Refusal quality
// ---------------------------------------------------------------------------

// TestEvaluation_EveryRefusalIsNamedAndActionable measures whether the runtime's
// refusals can be acted on.
//
// A caller receiving a generic failure can only retry or give up. A caller
// receiving ErrConsentRequired can ask for consent; one receiving ErrQueueFull
// can degrade; one receiving ErrPermissionDenied can stop. The measure is what
// fraction of refusals carry a sentinel a caller can branch on.
func TestEvaluation_EveryRefusalIsNamedAndActionable(t *testing.T) {
	type probe struct {
		name  string
		build func(*Harness) (ToolIntent, error)
		want  error
	}

	probes := []probe{
		{"no such capability", func(h *Harness) (ToolIntent, error) {
			return evalIntent(read("a", "nonexistent")), ErrNoCapability
		}, ErrNoCapability},
		{"unhealthy tool", func(h *Harness) (ToolIntent, error) {
			c := ReadContract("sick", "1.0.0", "sick")
			h.RegisterAt(c, &FakeTool{}, LifecycleActive, HealthUnhealthy, 0)
			return evalIntent(read("a", "sick")), ErrNoHealthyProvider
		}, ErrNoHealthyProvider},
		{"version unsatisfiable", func(h *Harness) (ToolIntent, error) {
			return evalIntent(CapabilityRequest{Ref: "a", Capability: "lookup",
				Version: MajorVersion(7), Args: Arguments{"query": String("q")}}), ErrVersionUnsatisfiable
		}, ErrVersionUnsatisfiable},
		{"missing permission", func(h *Harness) (ToolIntent, error) {
			c := ReadContract("guarded", "1.0.0", "guarded")
			c.RequiredPermissions = []Permission{"needed"}
			h.Register(c, &FakeTool{})
			return evalIntent(read("a", "guarded")), ErrPermissionDenied
		}, ErrPermissionDenied},
		{"missing consent", func(h *Harness) (ToolIntent, error) {
			c := ReadContract("recorded", "1.0.0", "recorded")
			c.RequiresConsent = "recording"
			h.Register(c, &FakeTool{})
			return evalIntent(read("a", "recorded")), ErrConsentRequired
		}, ErrConsentRequired},
		{"invalid input", func(h *Harness) (ToolIntent, error) {
			return evalIntent(CapabilityRequest{Ref: "a", Capability: "lookup",
				Version: AnyVersion(), Args: Arguments{}}), ErrInvalidInput
		}, ErrInvalidInput},
		{"invalid output", func(h *Harness) (ToolIntent, error) {
			c := ReadContract("liar", "1.0.0", "liar")
			c.Retry = RetrySpec{MaxAttempts: 1}
			h.Register(c, &FakeTool{Produce: func(Invocation) (Result, error) {
				return Result{"undeclared": String("x")}, nil
			}})
			return evalIntent(read("a", "liar")), ErrInvalidOutput
		}, ErrInvalidOutput},
		{"oversized input", func(h *Harness) (ToolIntent, error) {
			c := ReadContract("big", "1.0.0", "big")
			c.Input = []FieldSpec{{Name: "query", Kind: ValueString, Required: true}}
			c.Budget = Budget{InputBytes: 16}
			h.Register(c, &FakeTool{})
			return evalIntent(CapabilityRequest{Ref: "a", Capability: "big", Version: AnyVersion(),
				Args: Arguments{"query": String("a string well over sixteen bytes long")}}), ErrBudgetExceeded
		}, ErrBudgetExceeded},
	}

	named := 0
	for _, p := range probes {
		h := evalHarness(t)
		intent, _ := p.build(h)

		res, err := h.Runtime.Execute(context.Background(), intent)
		got := err
		if got == nil {
			got = res.Err
		}

		ok := got != nil && errors.Is(got, p.want)
		if ok {
			named++
		} else {
			t.Errorf("%s: expected %v, got %v", p.name, p.want, got)
		}
		t.Logf("%-24s actionable=%v", p.name, ok)
	}

	t.Logf("refusals carrying an actionable sentinel: %d of %d", named, len(probes))
	if named != len(probes) {
		t.Errorf("%d refusals were not actionable", len(probes)-named)
	}
}

// ---------------------------------------------------------------------------
// E5 · Determinism
// ---------------------------------------------------------------------------

// TestEvaluation_IdenticalIntentsProduceIdenticalExecutions measures whether the
// runtime is replayable.
//
// An execution runtime that cannot be replayed cannot be audited, and a system
// that takes actions in the world on a person's behalf has to be able to answer
// "why did it do that" with something better than a log line.
func TestEvaluation_IdenticalIntentsProduceIdenticalExecutions(t *testing.T) {
	const runs = 25

	fingerprint := func() (Fingerprint, string, string) {
		h := evalHarness(t)
		intent := evalIntent(
			read("z", "lookup"),
			read("a", "enrich"),
			write("m", "book", "a", "z"),
		)
		plan, err := h.Runtime.Plan(intent)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		res, err := h.Runtime.Run(context.Background(), plan, intent.Grant, NoopSink{})
		if err != nil {
			t.Fatalf("run: %v", err)
		}

		var steps string
		for _, s := range res.Steps {
			steps += string(s.Step) + ":" + s.Descriptor.String() + ";"
		}
		var events string
		for _, e := range h.Events.Events() {
			events += string(e.Type) + "/" + string(e.Step) + ";"
		}
		return plan.Fingerprint(), steps, events
	}

	planPrint, steps0, events0 := fingerprint()
	planDiv, stepDiv, eventDiv := 0, 0, 0
	for i := 1; i < runs; i++ {
		p, s, e := fingerprint()
		if p != planPrint {
			planDiv++
		}
		if s != steps0 {
			stepDiv++
		}
		if e != events0 {
			eventDiv++
		}
	}

	t.Logf("runs=%d plan divergences=%d step-order divergences=%d event-sequence divergences=%d",
		runs, planDiv, stepDiv, eventDiv)

	if planDiv+stepDiv != 0 {
		t.Errorf("determinism broken: plan=%d steps=%d", planDiv, stepDiv)
	}
	// Event ORDER across a parallel group is genuinely nondeterministic — two
	// branches racing is the point of running them in parallel. The report
	// records that rather than pretending otherwise; what must be stable is the
	// plan, the step set and the outcome, and those are asserted above.
	if eventDiv > 0 {
		t.Logf("note: event interleaving varies across parallel branches, as expected; "+
			"%d of %d runs differed in interleaving only", eventDiv, runs-1)
	}
}

// ---------------------------------------------------------------------------
// E6 · Failure attribution
// ---------------------------------------------------------------------------

// TestEvaluation_EveryFailureIsAttributed measures whether a failed execution
// says where it failed and why, in machine-readable terms.
//
// "Something went wrong" is not an operational signal. A phase and a bounded
// reason code are, and they are what the metrics and the dead-letter queue are
// built from.
func TestEvaluation_EveryFailureIsAttributed(t *testing.T) {
	type scenario struct {
		name  string
		setup func(*Harness) ToolIntent
		phase Phase
	}

	scenarios := []scenario{
		{"permission", func(h *Harness) ToolIntent {
			c := ReadContract("guarded", "1.0.0", "guarded")
			c.RequiredPermissions = []Permission{"needed"}
			h.Register(c, &FakeTool{})
			return evalIntent(read("a", "guarded"))
		}, PhasePermission},
		{"tool error", func(h *Harness) ToolIntent {
			c := ReadContract("broken", "1.0.0", "broken")
			c.Retry = RetrySpec{MaxAttempts: 1}
			h.Register(c, &FakeTool{FailAlways: true})
			return evalIntent(read("a", "broken"))
		}, PhaseInvoke},
		{"invalid output", func(h *Harness) ToolIntent {
			c := ReadContract("liar", "1.0.0", "liar")
			c.Retry = RetrySpec{MaxAttempts: 1}
			h.Register(c, &FakeTool{Produce: func(Invocation) (Result, error) {
				return Result{"nope": String("x")}, nil
			}})
			return evalIntent(read("a", "liar"))
		}, PhaseValidateOut},
	}

	attributed := 0
	for _, sc := range scenarios {
		h := evalHarness(t)
		intent := sc.setup(h)
		res, _ := h.Runtime.Execute(context.Background(), intent)

		if res.OK() {
			t.Errorf("%s: expected a failure", sc.name)
			continue
		}
		failed := res.Failed()
		if len(failed) == 0 {
			t.Errorf("%s: plan failed but no step reported a failure", sc.name)
			continue
		}

		step := failed[0]
		reason := ""
		for _, e := range h.Events.Events() {
			if e.Type == EventFailed || e.Type == EventTimedOut || e.Type == EventPermissionDenied {
				reason = e.Reason
			}
		}

		ok := step.Phase == sc.phase && reason != ""
		if ok {
			attributed++
		} else {
			t.Errorf("%s: phase=%s (want %s) reason=%q", sc.name, step.Phase, sc.phase, reason)
		}
		t.Logf("%-16s phase=%-16s reason=%-20s attributed=%v", sc.name, step.Phase, reason, ok)
	}

	t.Logf("failures carrying a phase and a bounded reason: %d of %d", attributed, len(scenarios))
	if attributed != len(scenarios) {
		t.Errorf("%d failures were unattributed", len(scenarios)-attributed)
	}
}

// ---------------------------------------------------------------------------
// E7 · Audit completeness
// ---------------------------------------------------------------------------

// TestEvaluation_EveryExecutionLeavesATerminalRecord measures whether the audit
// trail is complete: an execution that started must have finished in the record,
// one way or another.
//
// A started-with-no-terminal entry is the shape of an execution whose fate
// nobody can establish afterwards, which is exactly what an audit exists to
// prevent.
func TestEvaluation_EveryExecutionLeavesATerminalRecord(t *testing.T) {
	h := evalHarness(t)

	failing := ReadContract("broken", "1.0.0", "broken")
	failing.Retry = RetrySpec{MaxAttempts: 1}
	h.Register(failing, &FakeTool{FailAlways: true})

	intents := []ToolIntent{
		evalIntent(read("a", "lookup")),
		evalIntent(write("b", "book")),
		evalIntent(read("c", "broken")),
		evalIntent(read("d", "lookup"), read("e", "enrich")),
	}
	for _, in := range intents {
		_, _ = h.Runtime.Execute(context.Background(), in)
	}

	started := map[ExecutionID]bool{}
	terminal := map[ExecutionID]bool{}
	for _, e := range h.Audit.Entries() {
		switch e.Kind {
		case AuditExecutionStarted:
			started[e.Execution] = true
		case AuditExecutionCompleted, AuditExecutionFailed, AuditReplayed:
			terminal[e.Execution] = true
		}
	}

	orphans := 0
	for id := range started {
		if !terminal[id] {
			orphans++
		}
	}

	t.Logf("audited executions started=%d terminal=%d orphaned=%d",
		len(started), len(terminal), orphans)

	if len(started) == 0 {
		t.Fatal("nothing was audited at all")
	}
	if orphans != 0 {
		t.Errorf("%d executions started and never reached a terminal audit entry", orphans)
	}
}

// ---------------------------------------------------------------------------
// E8 · Overhead against the frozen latency budget
// ---------------------------------------------------------------------------

// TestEvaluation_RuntimeOverheadIsNegligibleAgainstTheBudget measures what the
// runtime itself costs a conversational turn.
//
// ADR-0011 gives a whole turn 900 ms at p50. The runtime's own work — planning,
// permission, validation, admission, dispatch, events, audit — has to be
// invisible next to the tool call it governs, or the governance is the problem.
func TestEvaluation_RuntimeOverheadIsNegligibleAgainstTheBudget(t *testing.T) {
	h := evalHarness(t)

	const iterations = 500
	intent := evalIntent(read("a", "lookup"))

	start := time.Now()
	for i := 0; i < iterations; i++ {
		in := intent
		in.ID = NewIntentID()
		in.Correlation = NewCorrelationID()
		if _, err := h.Runtime.Execute(context.Background(), in); err != nil {
			t.Fatalf("execute: %v", err)
		}
	}
	elapsed := time.Since(start)
	per := elapsed / iterations

	const budget = 900 * time.Millisecond
	share := 100 * float64(per) / float64(budget)

	t.Logf("executions=%d total=%s per-execution=%s budget=%s share=%.4f%%",
		iterations, elapsed, per, budget, share)

	// A generous ceiling: one percent of the turn budget. Anything approaching
	// it means the runtime has become the thing worth optimising, which would
	// be a finding rather than a passing test.
	if share > 1.0 {
		t.Errorf("runtime overhead is %.3f%% of the turn budget, over the 1%% ceiling", share)
	}
}
