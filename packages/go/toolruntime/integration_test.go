package toolruntime

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
// These run whole plans through the real runtime — planner, scheduler,
// permission engine, ledger, sandbox, executor, compensator, events, audit —
// with only the tool itself faked. The unit suite proves each part; this proves
// they agree with each other.

func mustExecute(t *testing.T, h *Harness, intent ToolIntent) PlanResult {
	t.Helper()
	res, err := h.Runtime.Execute(context.Background(), intent)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res
}

// ---------------------------------------------------------------------------
// End-to-end shapes
// ---------------------------------------------------------------------------

func TestIntegration_SingleToolEndToEnd(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	tool := &FakeTool{}
	h.Register(ReadContract("t", "1.0.0", "cap"), tool)

	res := mustExecute(t, h, h.Intent("cap", Arguments{"query": String("hello")}))

	if !res.OK() {
		t.Fatalf("plan failed: %v", res.Err)
	}
	if tool.Calls() != 1 {
		t.Fatalf("tool called %d times, want 1", tool.Calls())
	}
	if got, _ := res.Results["only"]["answer"].Str(); got != "ok:1" {
		t.Fatalf("unexpected result: %v", res.Results)
	}
	if h.Events.Count(EventStarted) != 1 || h.Events.Count(EventCompleted) != 1 {
		t.Fatalf("expected one started and one completed event, got %d/%d",
			h.Events.Count(EventStarted), h.Events.Count(EventCompleted))
	}
	if len(h.Audit.OfKind(AuditExecutionCompleted)) != 1 {
		t.Fatal("completion was not audited")
	}
}

func TestIntegration_SequentialStepsBindOutputsForward(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	first := ReadContract("first", "1.0.0", "lookup")
	second := ReadContract("second", "1.0.0", "enrich")

	var seen string
	h.Register(first, &FakeTool{Produce: func(Invocation) (Result, error) {
		return Result{"answer": String("from-first")}, nil
	}})
	h.Register(second, &FakeTool{Produce: func(in Invocation) (Result, error) {
		seen, _ = in.Args["query"].Str()
		return Result{"answer": String("done")}, nil
	}})

	intent := ToolIntent{ID: NewIntentID(), Correlation: NewCorrelationID(), Actor: "a",
		Grant: Grant{Actor: "a"},
		Requests: []CapabilityRequest{
			{Ref: "a", Capability: "lookup", Version: AnyVersion(), Args: Arguments{"query": String("x")}},
			{Ref: "b", Capability: "enrich", Version: AnyVersion(),
				Bindings: []Binding{{FromRef: "a", FromField: "answer", ToArg: "query", Required: true}}},
		}}

	res := mustExecute(t, h, intent)
	if !res.OK() {
		t.Fatalf("plan failed: %v", res.Err)
	}
	if seen != "from-first" {
		t.Fatalf("binding did not carry the first step's output, second saw %q", seen)
	}
}

func TestIntegration_ParallelStepsRunConcurrently(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	var inFlight, peak atomic.Int32
	gate := make(chan struct{})
	var once sync.Once

	probe := func(Invocation) (Result, error) {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		if n >= 2 {
			once.Do(func() { close(gate) })
		}
		<-gate
		inFlight.Add(-1)
		return Result{"answer": String("ok")}, nil
	}

	h.Register(ReadContract("t", "1.0.0", "cap"), &FakeTool{Produce: probe})

	intent := ToolIntent{ID: NewIntentID(), Actor: "a", Grant: Grant{Actor: "a"},
		Requests: []CapabilityRequest{
			{Ref: "a", Capability: "cap", Version: AnyVersion(), Args: Arguments{"query": String("a")}},
			{Ref: "b", Capability: "cap", Version: AnyVersion(), Args: Arguments{"query": String("b")}},
		}}

	res := mustExecute(t, h, intent)
	if !res.OK() {
		t.Fatalf("plan failed: %v", res.Err)
	}
	if peak.Load() < 2 {
		t.Fatalf("independent steps did not overlap; peak concurrency was %d", peak.Load())
	}
}

// TestIntegration_ParallelWaitsForEveryBranchEvenAfterAFailure covers the rule
// that keeps the compensation journal truthful. A sibling cancelled mid-flight
// may or may not have taken effect, and nothing downstream could tell which.
func TestIntegration_ParallelWaitsForEveryBranchEvenAfterAFailure(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	slow := &FakeTool{Produce: func(Invocation) (Result, error) {
		time.Sleep(20 * time.Millisecond)
		return Result{"answer": String("slow-finished")}, nil
	}}
	failing := &FakeTool{FailAlways: true, FailWith: ErrInvalidOutput}

	h.Register(ReadContract("slow", "1.0.0", "slow"), slow)
	h.Register(ReadContract("fail", "1.0.0", "fail"), failing)

	intent := ToolIntent{ID: NewIntentID(), Actor: "a", Grant: Grant{Actor: "a"},
		Requests: []CapabilityRequest{
			{Ref: "s", Capability: "slow", Version: AnyVersion(), Args: Arguments{"query": String("s")}},
			{Ref: "f", Capability: "fail", Version: AnyVersion(), Args: Arguments{"query": String("f")}},
		}}

	res, _ := h.Runtime.Execute(context.Background(), intent)
	if res.OK() {
		t.Fatal("plan should have failed")
	}
	if slow.Calls() != 1 {
		t.Fatalf("slow branch ran %d times", slow.Calls())
	}
	var slowResult *ExecutionResult
	for i := range res.Steps {
		if res.Steps[i].Ref == "s" {
			slowResult = &res.Steps[i]
		}
	}
	if slowResult == nil || slowResult.Err != nil {
		t.Fatalf("the slow branch was not allowed to reach a definite outcome: %+v", slowResult)
	}
}

func TestIntegration_ConditionalSkipsWithoutFailing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	gate := &FakeTool{Produce: func(Invocation) (Result, error) {
		return Result{"answer": String("no")}, nil
	}}
	guarded := &FakeTool{}

	h.Register(ReadContract("gate", "1.0.0", "gate"), gate)
	h.Register(ReadContract("guarded", "1.0.0", "guarded"), guarded)

	intent := ToolIntent{ID: NewIntentID(), Actor: "a", Grant: Grant{Actor: "a"},
		Requests: []CapabilityRequest{
			{Ref: "g", Capability: "gate", Version: AnyVersion(), Args: Arguments{"query": String("q")}},
			{Ref: "x", Capability: "guarded", Version: AnyVersion(), Args: Arguments{"query": String("q")},
				DependsOn: []string{"g"},
				Condition: &Condition{FromRef: "g", Field: "answer", Op: CondEquals, Value: String("yes")}},
		}}

	res := mustExecute(t, h, intent)
	if !res.OK() {
		t.Fatalf("a skipped conditional must not fail the plan: %v", res.Err)
	}
	if guarded.Calls() != 0 {
		t.Fatalf("guarded tool ran despite a false condition")
	}
	skipped := false
	for _, s := range res.Steps {
		if s.Ref == "x" && s.Skipped {
			skipped = true
		}
	}
	if !skipped {
		t.Fatal("the skipped step was not reported as skipped")
	}
}

func TestIntegration_FallbackUsesTheNextCandidate(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	broken := &FakeTool{FailAlways: true}
	working := &FakeTool{}

	h.RegisterAt(ReadContract("primary", "1.0.0", "cap"), broken, LifecycleActive, HealthHealthy, 10)
	h.RegisterAt(ReadContract("secondary", "1.0.0", "cap"), working, LifecycleActive, HealthHealthy, 1)

	intent := ToolIntent{ID: NewIntentID(), Actor: "a", Grant: Grant{Actor: "a"},
		Requests: []CapabilityRequest{{Ref: "f", Capability: "cap", Version: AnyVersion(),
			Args: Arguments{"query": String("q")}, Fallback: true}}}

	res := mustExecute(t, h, intent)
	if !res.OK() {
		t.Fatalf("fallback did not rescue the plan: %v", res.Err)
	}
	if working.Calls() == 0 {
		t.Fatal("the fallback candidate was never tried")
	}
	if h.Metrics.FallbacksUsed.Total() == 0 {
		t.Error("fallback use was not counted; an operator would not know the primary is broken")
	}
}

func TestIntegration_OptionalStepFailureDoesNotFailThePlan(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	h.Register(ReadContract("core", "1.0.0", "core"), &FakeTool{})
	h.Register(ReadContract("enrich", "1.0.0", "enrich"), &FakeTool{FailAlways: true})

	intent := ToolIntent{ID: NewIntentID(), Actor: "a", Grant: Grant{Actor: "a"},
		Requests: []CapabilityRequest{
			{Ref: "c", Capability: "core", Version: AnyVersion(), Args: Arguments{"query": String("q")}},
			{Ref: "e", Capability: "enrich", Version: AnyVersion(), Args: Arguments{"query": String("q")},
				Optional: true},
		}}

	res := mustExecute(t, h, intent)
	if !res.OK() {
		t.Fatalf("an optional failure sank the plan: %v", res.Err)
	}
	if len(res.Failed()) != 1 {
		t.Fatalf("the optional failure should still be recorded, got %d failures", len(res.Failed()))
	}
}

// ---------------------------------------------------------------------------
// Retry, timeout, breakers
// ---------------------------------------------------------------------------

func TestIntegration_RetriesTransientFailureThenSucceeds(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	c := ReadContract("t", "1.0.0", "cap")
	c.Retry = RetrySpec{MaxAttempts: 3, NoBackoff: true}
	tool := &FakeTool{FailTimes: 2}
	h.Register(c, tool)

	res := mustExecute(t, h, h.Intent("cap", Arguments{"query": String("q")}))
	if !res.OK() {
		t.Fatalf("retry did not recover: %v", res.Err)
	}
	if tool.Calls() != 3 {
		t.Fatalf("expected 3 attempts, got %d", tool.Calls())
	}
	if h.Events.Count(EventRetried) != 2 {
		t.Fatalf("expected 2 retry events, got %d", h.Events.Count(EventRetried))
	}
}

func TestIntegration_ContractViolationIsNotRetried(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	c := ReadContract("t", "1.0.0", "cap")
	c.Retry = RetrySpec{MaxAttempts: 5, NoBackoff: true}
	tool := &FakeTool{Produce: func(Invocation) (Result, error) {
		return Result{"wrong_field": String("x")}, nil // undeclared output
	}}
	h.Register(c, tool)

	res, _ := h.Runtime.Execute(context.Background(), h.Intent("cap", Arguments{"query": String("q")}))
	if res.OK() {
		t.Fatal("invalid output was accepted")
	}
	if tool.Calls() != 1 {
		t.Fatalf("a contract violation was retried %d times; asking again produces "+
			"the same wrong answer while a downstream waits", tool.Calls())
	}
	if !errors.Is(res.Err, ErrInvalidOutput) {
		t.Fatalf("expected ErrInvalidOutput, got %v", res.Err)
	}
}

// TestIntegration_TimeoutIsDrivenByTheInjectedClock is Phase 10A's F1 applied
// here: a deadline derived from a FakeClock but scheduled against wall time
// either hangs or expires instantly.
func TestIntegration_TimeoutIsDrivenByTheInjectedClock(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	c := ReadContract("slow", "1.0.0", "cap")
	c.Timeout = time.Second
	c.Retry = RetrySpec{MaxAttempts: 1}
	h.Register(c, &FakeTool{Delay: time.Minute, Clock: h.Clock})

	done := make(chan PlanResult, 1)
	go func() {
		res, _ := h.Runtime.Execute(context.Background(), h.Intent("cap", Arguments{"query": String("q")}))
		done <- res
	}()

	// Two waiters: the tool's sleep and the executor's deadline timer.
	h.Clock.BlockUntil(2)
	h.Clock.Advance(2 * time.Second)

	select {
	case res := <-done:
		if !errors.Is(res.Err, ErrTimeout) {
			t.Fatalf("expected ErrTimeout, got %v", res.Err)
		}
		if h.Events.Count(EventTimedOut) != 1 {
			t.Fatalf("expected one timed-out event, got %d", h.Events.Count(EventTimedOut))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("execution never returned: the deadline is not driven by the injected clock")
	}
}

func TestIntegration_OpenCircuitFailsFastWithoutCallingTheTool(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.Breaker.FailureThreshold = 2
	cfg.Breaker.MinimumRequests = 2
	h, err := NewHarness(WithHarnessConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}

	c := ReadContract("t", "1.0.0", "cap")
	c.Retry = RetrySpec{MaxAttempts: 1}
	tool := &FakeTool{FailAlways: true}
	h.Register(c, tool)

	for i := 0; i < 8; i++ {
		_, _ = h.Runtime.Execute(context.Background(), h.Intent("cap", Arguments{"query": String("q")}))
	}

	before := tool.Calls()
	res, _ := h.Runtime.Execute(context.Background(), h.Intent("cap", Arguments{"query": String("q")}))
	if tool.Calls() != before {
		t.Fatal("the tool was called with the circuit open")
	}
	if !errors.Is(res.Err, ErrCircuitOpen) {
		t.Fatalf("an open circuit should say so rather than present as a timeout, got %v", res.Err)
	}
}

func TestIntegration_ExhaustedRetriesLandInTheDeadLetterQueue(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	c := ReadContract("t", "1.0.0", "cap")
	c.Retry = RetrySpec{MaxAttempts: 2, NoBackoff: true}
	h.Register(c, &FakeTool{FailAlways: true})

	_, _ = h.Runtime.Execute(context.Background(), h.Intent("cap", Arguments{"query": String("secret-value")}))

	entries := h.Runtime.DeadLetters().Entries()
	if len(entries) != 1 {
		t.Fatalf("expected one dead letter, got %d", len(entries))
	}
	rendered := fmt.Sprintf("%+v", entries[0])
	if strings.Contains(rendered, "secret-value") {
		t.Fatalf("a dead letter carries caller content: %s", rendered)
	}
	if entries[0].InputPrint == "" {
		t.Error("a dead letter should carry a fingerprint so an operator can correlate it")
	}
}

// ---------------------------------------------------------------------------
// Idempotency
// ---------------------------------------------------------------------------

func TestIntegration_MutatingCallIsNotRepeatedForOneCorrelation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	tool := &CompensatingFake{}
	h.Register(WriteContract("booker", "1.0.0", "book"), tool)

	corr := NewCorrelationID()
	intent := func() ToolIntent {
		return ToolIntent{ID: NewIntentID(), Correlation: corr, Actor: "a",
			Grant: Grant{Actor: "a"},
			Requests: []CapabilityRequest{{Ref: "b", Capability: "book", Version: AnyVersion(),
				Args: Arguments{"subject": String("table-for-two")}}}}
	}

	first := mustExecute(t, h, intent())
	second := mustExecute(t, h, intent())

	if tool.Calls() != 1 {
		t.Fatalf("the same mutating call ran %d times within one correlation", tool.Calls())
	}
	firstRef, _ := first.Results["b"]["reference"].Str()
	secondRef, _ := second.Results["b"]["reference"].Str()
	if firstRef != secondRef {
		t.Fatalf("replay returned a different reference: %q vs %q", firstRef, secondRef)
	}
	if !second.Steps[0].Replayed {
		t.Error("the second execution was not reported as a replay")
	}
}

func TestIntegration_ADifferentTurnIsNotDeduplicated(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	tool := &CompensatingFake{}
	h.Register(WriteContract("booker", "1.0.0", "book"), tool)

	for i := 0; i < 2; i++ {
		mustExecute(t, h, ToolIntent{ID: NewIntentID(), Correlation: NewCorrelationID(),
			Actor: "a", Grant: Grant{Actor: "a"},
			Requests: []CapabilityRequest{{Ref: "b", Capability: "book", Version: AnyVersion(),
				Args: Arguments{"subject": String("same")}}}})
	}

	if tool.Calls() != 2 {
		t.Fatalf("two separate turns were deduplicated into %d call(s); a later "+
			"request for the same thing is a new request", tool.Calls())
	}
}

func TestIntegration_ReadsAreNotDeduplicatedByDefault(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	tool := &FakeTool{}
	h.Register(ReadContract("t", "1.0.0", "cap"), tool)

	corr := NewCorrelationID()
	for i := 0; i < 3; i++ {
		mustExecute(t, h, ToolIntent{ID: NewIntentID(), Correlation: corr, Actor: "a",
			Grant: Grant{Actor: "a"},
			Requests: []CapabilityRequest{{Ref: "r", Capability: "cap", Version: AnyVersion(),
				Args: Arguments{"query": String("is the slot free")}}}})
	}

	if tool.Calls() != 3 {
		t.Fatalf("reads were deduplicated %d/3; a stored answer to 'is this slot "+
			"still free' is exactly the wrong thing to be confident about", tool.Calls())
	}
}

func TestIntegration_PermissionDenialDoesNotPoisonTheKey(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	c := WriteContract("booker", "1.0.0", "book")
	c.RequiredPermissions = []Permission{"tool.book"}
	tool := &CompensatingFake{}
	h.Register(c, tool)

	corr := NewCorrelationID()
	build := func(perms ...Permission) ToolIntent {
		return ToolIntent{ID: NewIntentID(), Correlation: corr, Actor: "a",
			Grant: Grant{Actor: "a", Permissions: perms},
			Requests: []CapabilityRequest{{Ref: "b", Capability: "book", Version: AnyVersion(),
				Args: Arguments{"subject": String("s")}}}}
	}

	denied, _ := h.Runtime.Execute(context.Background(), build())
	if !errors.Is(denied.Err, ErrPermissionDenied) {
		t.Fatalf("expected a denial, got %v", denied.Err)
	}

	allowed := mustExecute(t, h, build("tool.book"))
	if !allowed.OK() {
		t.Fatalf("the corrected retry was blocked by the denied attempt's key: %v", allowed.Err)
	}
	if tool.Calls() != 1 {
		t.Fatalf("tool calls: %d", tool.Calls())
	}
}

// ---------------------------------------------------------------------------
// Compensation
// ---------------------------------------------------------------------------

func TestIntegration_FailedPlanRollsBackInReverseOrder(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	var order []string
	var mu sync.Mutex
	record := func(name string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, name)
	}

	first := &CompensatingFake{}
	second := &CompensatingFake{}
	h.Register(WriteContract("first", "1.0.0", "one"), first)
	h.Register(WriteContract("second", "1.0.0", "two"), second)

	failing := WriteContract("third", "1.0.0", "three")
	failing.Compensable = false
	failing.Retry = RetrySpec{MaxAttempts: 1}
	h.Register(failing, &WriteFake{FakeTool: FakeTool{FailAlways: true}})

	// Wrap the compensating tools so the rollback order is observable.
	first.Produce = func(Invocation) (Result, error) { return Result{"reference": String("r1")}, nil }
	second.Produce = func(Invocation) (Result, error) { return Result{"reference": String("r2")}, nil }
	first.CompensateErr, second.CompensateErr = nil, nil

	intent := ToolIntent{ID: NewIntentID(), Correlation: NewCorrelationID(), Actor: "a",
		Grant: Grant{Actor: "a"},
		Requests: []CapabilityRequest{
			{Ref: "a", Capability: "one", Version: AnyVersion(), Args: Arguments{"subject": String("a")}},
			{Ref: "b", Capability: "two", Version: AnyVersion(), Args: Arguments{"subject": String("b")},
				DependsOn: []string{"a"}},
			{Ref: "c", Capability: "three", Version: AnyVersion(), Args: Arguments{"subject": String("c")},
				DependsOn: []string{"b"}},
		}}

	res, _ := h.Runtime.Execute(context.Background(), intent)
	if res.OK() {
		t.Fatal("plan should have failed")
	}
	if res.Compensation == nil {
		t.Fatal("no compensation report was produced")
	}
	if res.Compensation.Compensated != 2 {
		t.Fatalf("expected 2 compensations, got %+v", res.Compensation)
	}

	// The report is ordered by rollback attempt, which is reverse completion.
	got := []StepID{res.Compensation.Outcomes[0].Step, res.Compensation.Outcomes[1].Step}
	if got[0] != "b" || got[1] != "a" {
		t.Fatalf("rollback order was %v, want [b a]; later steps may depend on "+
			"state earlier ones created", got)
	}
	_ = record
	if first.Compensations() != 1 || second.Compensations() != 1 {
		t.Fatalf("compensations: first=%d second=%d", first.Compensations(), second.Compensations())
	}
}

func TestIntegration_CompensationFailureReplacesTheOriginalError(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	broken := &CompensatingFake{CompensateErr: errors.New("undo endpoint down")}
	h.Register(WriteContract("first", "1.0.0", "one"), broken)

	failing := WriteContract("second", "1.0.0", "two")
	failing.Compensable = false
	failing.Retry = RetrySpec{MaxAttempts: 1}
	h.Register(failing, &WriteFake{FakeTool: FakeTool{FailAlways: true}})

	intent := ToolIntent{ID: NewIntentID(), Correlation: NewCorrelationID(), Actor: "a",
		Grant: Grant{Actor: "a"},
		Requests: []CapabilityRequest{
			{Ref: "a", Capability: "one", Version: AnyVersion(), Args: Arguments{"subject": String("a")}},
			{Ref: "b", Capability: "two", Version: AnyVersion(), Args: Arguments{"subject": String("b")},
				DependsOn: []string{"a"}},
		}}

	res, _ := h.Runtime.Execute(context.Background(), intent)
	if !errors.Is(res.Err, ErrCompensationFailed) {
		t.Fatalf("a failed rollback must surface above the original failure, got %v", res.Err)
	}
	if len(h.Audit.OfKind(AuditCompensationFailed)) != 1 {
		t.Fatal("a failed compensation was not audited; the world is in a state nobody chose")
	}
}

// TestIntegration_NonCompensableWorkIsReportedAsSkippedNotCompensated is the
// distinction between "we could not try" and "we tried and it did not work".
func TestIntegration_NonCompensableWorkIsReportedAsSkipped(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	uncompensable := WriteContract("first", "1.0.0", "one")
	uncompensable.Compensable = false
	h.Register(uncompensable, &WriteFake{})

	failing := WriteContract("second", "1.0.0", "two")
	failing.Compensable = false
	failing.Retry = RetrySpec{MaxAttempts: 1}
	h.Register(failing, &WriteFake{FakeTool: FakeTool{FailAlways: true}})

	intent := ToolIntent{ID: NewIntentID(), Correlation: NewCorrelationID(), Actor: "a",
		Grant: Grant{Actor: "a"},
		Requests: []CapabilityRequest{
			{Ref: "a", Capability: "one", Version: AnyVersion(), Args: Arguments{"subject": String("a")}},
			{Ref: "b", Capability: "two", Version: AnyVersion(), Args: Arguments{"subject": String("b")},
				DependsOn: []string{"a"}},
		}}

	res, _ := h.Runtime.Execute(context.Background(), intent)
	if res.Compensation == nil {
		t.Fatal("no compensation report")
	}
	if res.Compensation.Skipped != 1 || res.Compensation.Failed != 0 {
		t.Fatalf("expected 1 skipped and 0 failed, got %+v", res.Compensation)
	}
	if res.Compensation.Complete {
		t.Error("a rollback that could not undo everything must not report itself complete")
	}
}

func TestIntegration_ReadOnlyStepsAreNotJournalled(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	h.Register(ReadContract("reader", "1.0.0", "read"), &FakeTool{})
	failing := ReadContract("failer", "1.0.0", "fail")
	failing.Retry = RetrySpec{MaxAttempts: 1}
	h.Register(failing, &FakeTool{FailAlways: true})

	intent := ToolIntent{ID: NewIntentID(), Actor: "a", Grant: Grant{Actor: "a"},
		Requests: []CapabilityRequest{
			{Ref: "r", Capability: "read", Version: AnyVersion(), Args: Arguments{"query": String("q")}},
			{Ref: "f", Capability: "fail", Version: AnyVersion(), Args: Arguments{"query": String("q")},
				DependsOn: []string{"r"}},
		}}

	res, _ := h.Runtime.Execute(context.Background(), intent)
	if res.Compensation != nil {
		t.Fatalf("a read-only step produced a rollback report: %+v", res.Compensation)
	}
}

// ---------------------------------------------------------------------------
// Streaming
// ---------------------------------------------------------------------------

func TestIntegration_StreamingDeliversPartialResultsInOrder(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	c := ReadContract("streamer", "1.0.0", "stream")
	c.Streaming = true
	h.Register(c, &StreamingFake{Chunks: 5})

	sink := NewBufferedSink(16)
	res, err := h.Runtime.ExecuteStreaming(context.Background(),
		h.Intent("stream", Arguments{"query": String("q")}), sink)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !res.OK() {
		t.Fatalf("plan failed: %v", res.Err)
	}
	sink.Close(nil)

	var seq []uint64
	for c := range sink.Chunks() {
		seq = append(seq, c.Sequence)
	}
	if len(seq) != 5 {
		t.Fatalf("expected 5 chunks, got %d", len(seq))
	}
	for i, s := range seq {
		if s != uint64(i+1) {
			t.Fatalf("chunk sequence is %v; a consumer cannot detect a gap", seq)
		}
	}
	// The final result still arrives in full: a stream is an early view of the
	// answer, never the answer itself.
	if got, _ := res.Results["only"]["answer"].Str(); got == "" {
		t.Fatal("streaming replaced the final result instead of previewing it")
	}
}

func TestIntegration_UnboundedStreamHitsTheOutputBudget(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	c := ReadContract("firehose", "1.0.0", "stream")
	c.Streaming = true
	c.Retry = RetrySpec{MaxAttempts: 1}
	c.Budget = Budget{OutputBytes: 200}
	h.Register(c, &StreamingFake{Chunks: 500, ChunkPayload: strings.Repeat("x", 64)})

	res, _ := h.Runtime.ExecuteStreaming(context.Background(),
		h.Intent("stream", Arguments{"query": String("q")}), NoopSink{})

	if !errors.Is(res.Err, ErrBudgetExceeded) {
		t.Fatalf("an unbounded stream should exhaust the output budget, got %v", res.Err)
	}
}

// ---------------------------------------------------------------------------
// Failure injection
// ---------------------------------------------------------------------------

func TestFailure_PanickingToolBecomesAFailedExecution(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	c := ReadContract("panicky", "1.0.0", "cap")
	c.Retry = RetrySpec{MaxAttempts: 1}
	h.Register(c, &FakeTool{PanicOnCall: 1})

	res, _ := h.Runtime.Execute(context.Background(), h.Intent("cap", Arguments{"query": String("q")}))
	if res.OK() {
		t.Fatal("a panicking tool produced a successful plan")
	}
	if !errors.Is(res.Err, ErrInvariant) {
		t.Fatalf("expected an invariant error from a panic, got %v", res.Err)
	}
	// The panic value must not travel into the error: it can contain caller
	// content and errors end up in logs and metrics.
	if strings.Contains(res.Err.Error(), "scripted panic") {
		t.Fatal("the panic value leaked into the error message")
	}
}

func TestFailure_AuditFailureDoesNotFailTheExecution(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(ReadContract("t", "1.0.0", "cap"), &FakeTool{})
	h.Audit.FailWith(errors.New("audit store down"))

	res, err := h.Runtime.Execute(context.Background(), h.Intent("cap", Arguments{"query": String("q")}))
	if err != nil || !res.OK() {
		t.Fatalf("failing an already-completed action because its record could not "+
			"be written leaves the world changed AND unrecorded: %v", res.Err)
	}
	if h.Metrics.AuditFailed.Total() == 0 {
		t.Error("an audit failure should be counted")
	}
}

func TestFailure_AbandonedGoroutineIsCounted(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	c := ReadContract("rude", "1.0.0", "cap")
	c.Timeout = time.Second
	c.Retry = RetrySpec{MaxAttempts: 1}
	h.Register(c, &FakeTool{Delay: time.Minute, Clock: h.Clock, IgnoreCancellation: true})

	done := make(chan PlanResult, 1)
	go func() {
		res, _ := h.Runtime.Execute(context.Background(), h.Intent("cap", Arguments{"query": String("q")}))
		done <- res
	}()

	h.Clock.BlockUntil(2)
	h.Clock.Advance(2 * time.Second)

	select {
	case res := <-done:
		if !errors.Is(res.Err, ErrTimeout) {
			t.Fatalf("expected ErrTimeout, got %v", res.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the runtime waited for a tool that ignores cancellation")
	}

	if h.Runtime.Supervisor().Abandoned() == 0 {
		t.Fatal("an abandoned goroutine was not counted; the leak would be invisible")
	}
	// Let the abandoned goroutine finish so the test does not leave it parked.
	h.Clock.Advance(2 * time.Minute)
}

func TestFailure_QueueFullShedsRatherThanQueueing(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.Scheduler = SchedulerConfig{MaxConcurrent: 1, MaxQueuedInteractive: 0,
		MaxQueuedBackground: 0, MaxQueuedBulk: 0, StarvationRatio: 4}
	h, err := NewHarness(WithHarnessConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}

	blocker := NewBlockingTool()
	h.Register(ReadContract("blocking", "1.0.0", "cap"), blocker)

	go func() {
		_, _ = h.Runtime.Execute(context.Background(), h.Intent("cap", Arguments{"query": String("q")}))
	}()
	blocker.WaitEntered()

	res, _ := h.Runtime.Execute(context.Background(), h.Intent("cap", Arguments{"query": String("q")}))
	if !errors.Is(res.Err, ErrQueueFull) {
		t.Fatalf("expected load shedding at admission, got %v", res.Err)
	}
	blocker.Release()
}

// ---------------------------------------------------------------------------
// Cancellation
// ---------------------------------------------------------------------------

func TestIntegration_CoordinatorCancelsAWholeCorrelation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	blocker := NewBlockingTool()
	h.Register(ReadContract("blocking", "1.0.0", "cap"), blocker)

	corr := NewCorrelationID()
	intent := ToolIntent{ID: NewIntentID(), Correlation: corr, Actor: "a", Grant: Grant{Actor: "a"},
		Requests: []CapabilityRequest{{Ref: "b", Capability: "cap", Version: AnyVersion(),
			Args: Arguments{"query": String("q")}}}}

	done := make(chan PlanResult, 1)
	go func() {
		res, _ := h.Runtime.Execute(context.Background(), intent)
		done <- res
	}()
	blocker.WaitEntered()

	if n := h.Runtime.Coordinator().Cancel(corr, "caller_hung_up"); n == 0 {
		t.Fatal("coordinator found nothing to cancel")
	}

	select {
	case res := <-done:
		if res.OK() {
			t.Fatal("a cancelled execution reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not reach the execution")
	}
	blocker.Release()
}

// ---------------------------------------------------------------------------
// Concurrency and stress
// ---------------------------------------------------------------------------

func TestStress_ConcurrentPlansAcrossTools(t *testing.T) {
	t.Parallel()

	// The per-tool concurrency cap is raised for this test on purpose. The
	// default of eight is a production protection for a downstream that said
	// eight; this test is about throughput under concurrency, and leaving the
	// cap in place would make it assert two things at once and fail
	// intermittently on the wrong one. Shedding is tested separately, below.
	cfg := DefaultConfig()
	cfg.DefaultToolConcurrency = 64
	h, err := NewHarness(WithHarnessConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		h.Register(ReadContract(ToolID(fmt.Sprintf("t%d", i)), "1.0.0",
			CapabilityID(fmt.Sprintf("cap%d", i))), &FakeTool{})
	}

	const workers, each = 16, 20
	var wg sync.WaitGroup
	var failures atomic.Int64

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				cap := CapabilityID(fmt.Sprintf("cap%d", (w+i)%5))
				res, execErr := h.Runtime.Execute(context.Background(),
					h.Intent(cap, Arguments{"query": String("q")}))
				if execErr != nil || !res.OK() {
					failures.Add(1)
				}
			}
		}(w)
	}
	wg.Wait()

	if failures.Load() != 0 {
		t.Fatalf("%d of %d concurrent executions failed", failures.Load(), workers*each)
	}
	if got := h.Metrics.Completed.Total(); got != workers*each {
		t.Fatalf("completed counter is %d, want %d", got, workers*each)
	}
}

// TestStress_ConcurrentDuplicatesInvokeTheToolOnce is the property the ledger
// exists for, under the condition it exists for: a client retry storm.
func TestStress_ConcurrentDuplicatesInvokeTheToolOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	tool := &CompensatingFake{}
	h.Register(WriteContract("booker", "1.0.0", "book"), tool)

	corr := NewCorrelationID()
	const callers = 24
	var wg sync.WaitGroup
	refs := make([]string, callers)

	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			res, _ := h.Runtime.Execute(context.Background(), ToolIntent{
				ID: NewIntentID(), Correlation: corr, Actor: "a", Grant: Grant{Actor: "a"},
				Requests: []CapabilityRequest{{Ref: "b", Capability: "book", Version: AnyVersion(),
					Args: Arguments{"subject": String("one-booking")}}}})
			refs[i], _ = res.Results["b"]["reference"].Str()
		}(i)
	}
	close(start)
	wg.Wait()

	if tool.Calls() != 1 {
		t.Fatalf("%d concurrent duplicates produced %d tool calls", callers, tool.Calls())
	}
	for i, r := range refs {
		if r != refs[0] {
			t.Fatalf("caller %d received a different reference (%q vs %q)", i, r, refs[0])
		}
	}
}

func TestStress_RegistryChurnDuringExecution(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(ReadContract("t", "1.0.0", "cap"), &FakeTool{})

	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			c := ReadContract("t", Version(fmt.Sprintf("1.0.%d", i%50)), "cap")
			_ = h.Runtime.Registry().Register(Registration{Contract: c, Tool: &FakeTool{},
				Lifecycle: LifecycleActive, Health: HealthHealthy})
		}
	}()

	var failures atomic.Int64
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				res, err := h.Runtime.Execute(context.Background(),
					h.Intent("cap", Arguments{"query": String("q")}))
				if err != nil || !res.OK() {
					failures.Add(1)
				}
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	if failures.Load() != 0 {
		t.Fatalf("INV-TOOL-9: %d executions failed while the registry churned", failures.Load())
	}
}

func TestStress_DrainingVersionStillFinishesItsPlans(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	blocker := NewBlockingTool()
	c := ReadContract("t", "1.0.0", "cap")
	h.Register(c, blocker)

	done := make(chan PlanResult, 1)
	go func() {
		res, _ := h.Runtime.Execute(context.Background(), h.Intent("cap", Arguments{"query": String("q")}))
		done <- res
	}()
	blocker.WaitEntered()

	if _, err := h.Runtime.Coordinator().Drain(c.Descriptor); err != nil {
		t.Fatal(err)
	}
	blocker.Release()

	select {
	case res := <-done:
		if !res.OK() {
			t.Fatalf("draining interrupted an in-flight execution: %v", res.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("execution never finished")
	}

	// New work against a draining version is refused at planning.
	if _, err := h.Runtime.Plan(h.Intent("cap", Arguments{"query": String("q")})); !errors.Is(err, ErrNoHealthyProvider) {
		t.Fatalf("a draining version accepted new work: %v", err)
	}
}

func TestIntegration_HealthReportAnswersTheIncidentQuestions(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	h.RegisterAt(ReadContract("good", "1.0.0", "cap"), &FakeTool{}, LifecycleActive, HealthHealthy, 5)
	h.RegisterAt(ReadContract("bad", "1.0.0", "cap"), &FakeTool{}, LifecycleActive, HealthUnhealthy, 1)

	if err := h.Runtime.Permissions().AddOverride(Override{Name: "incident-42",
		Permissions: []Permission{"tool.anything"}, ExpiresAt: h.Clock.Now().Add(time.Hour),
		AuthorisedBy: "oncall", Reason: "outage"}); err != nil {
		t.Fatal(err)
	}

	rep := h.Runtime.Coordinator().Health()
	if rep.Registrations != 2 {
		t.Fatalf("registrations: %d", rep.Registrations)
	}
	if len(rep.Capabilities) != 1 || rep.Capabilities[0].Dispatchable != 1 || rep.Capabilities[0].Healthy != 1 {
		t.Fatalf("capability report is wrong: %+v", rep.Capabilities)
	}
	if len(rep.ActiveOverrides) != 1 || rep.ActiveOverrides[0] != "incident-42" {
		t.Fatalf("an active override must be visible in the health report, got %v", rep.ActiveOverrides)
	}
	if rep.Capabilities[0].Entries[0].Owner == "" {
		t.Error("the health report should name the owning team; it is the first incident question")
	}
}

// TestStress_OverloadShedsCleanlyAndIsAccountedFor is the other half of the
// concurrency story. Frozen invariant I11 says the platform sheds AT ADMISSION
// under load rather than degrading mid-flight, so a refusal here is correct
// behaviour — but it must be a NAMED refusal that a caller can act on, and it
// must be counted. Silent shedding is indistinguishable from a tool that
// mysteriously stopped being called.
func TestStress_OverloadShedsCleanlyAndIsAccountedFor(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.DefaultToolConcurrency = 2
	cfg.SandboxSlots = 64
	h, err := NewHarness(WithHarnessConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}

	tool := &FakeTool{Produce: func(Invocation) (Result, error) {
		time.Sleep(2 * time.Millisecond)
		return Result{"answer": String("ok")}, nil
	}}
	h.Register(ReadContract("t", "1.0.0", "cap"), tool)

	const callers = 32
	var wg sync.WaitGroup
	var shed, completed, other atomic.Int64

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, _ := h.Runtime.Execute(context.Background(),
				h.Intent("cap", Arguments{"query": String("q")}))
			switch {
			case res.OK():
				completed.Add(1)
			case errors.Is(res.Err, ErrBudgetExceeded):
				shed.Add(1)
			default:
				other.Add(1)
			}
		}()
	}
	wg.Wait()

	if other.Load() != 0 {
		t.Fatalf("%d executions failed for a reason other than shedding", other.Load())
	}
	if shed.Load() == 0 {
		t.Fatalf("a per-tool cap of 2 admitted all %d concurrent callers; the cap is not a cap", callers)
	}
	if completed.Load()+shed.Load() != callers {
		t.Fatalf("outcomes do not add up: %d completed + %d shed != %d",
			completed.Load(), shed.Load(), callers)
	}
	if h.Metrics.BudgetRefused.Total() != uint64(shed.Load()) {
		t.Fatalf("shedding was not counted: metric says %d, callers saw %d",
			h.Metrics.BudgetRefused.Total(), shed.Load())
	}
	// A shed execution must not have reached the tool at all.
	if int64(tool.Calls()) != completed.Load() {
		t.Fatalf("tool was entered %d times for %d completed executions", tool.Calls(), completed.Load())
	}
}
