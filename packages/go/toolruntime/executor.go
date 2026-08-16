package toolruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// ExecutionResult is the outcome of one step.
type ExecutionResult struct {
	// Execution identifies the attempt sequence.
	Execution ExecutionID
	// Step and Ref locate it in the plan.
	Step StepID
	Ref  string
	// Descriptor is the pinned tool that ran, empty when nothing ran.
	Descriptor Descriptor
	// Result is the tool's output.
	Result Result
	// Err is the terminal error, nil on success.
	Err error
	// Attempts counts invocations, including the successful one.
	Attempts int
	// Duration is the total elapsed time including retries and waiting.
	Duration time.Duration
	// Phase names where a failure occurred.
	Phase Phase
	// Replayed reports that the result came from the idempotency ledger rather
	// than from invoking the tool.
	Replayed bool
	// Skipped reports that a conditional step's condition did not hold. NOT a
	// failure: a plan that skips a step because the condition said so has done
	// exactly what it was asked to.
	Skipped bool
	// FallbackRank is which candidate served it. 0 is the preferred tool.
	FallbackRank int
	// Chunks counts stream chunks emitted.
	Chunks uint64
}

// OK reports success.
func (r ExecutionResult) OK() bool { return r.Err == nil && !r.Skipped }

// Executor runs one invoke step, with retries, budgets and validation.
//
// It is the only place in this module that calls a [Tool]. Everything else
// plans, permits, records or reports. Keeping invocation to one function is
// what makes it possible to state with confidence that no tool is ever called
// without a permission check, a budget, a deadline and an audit entry — there
// is exactly one path, and it is this one.
type Executor struct {
	registry    *Registry
	permissions *PermissionEngine
	ledger      *Ledger
	sandbox     Sandbox
	retries     *RetryEngine
	events      *EventDispatcher
	audit       Auditor
	metrics     *Metrics
	clock       rt.Clock
	supervisor  *ExecutionSupervisor

	defaultRetry  RetrySpec
	defaultBudget Budget
	dedupeReads   bool
}

// Execute runs one invoke step to completion.
//
// The phase order is deliberate and each stage is cheaper or safer than the
// next:
//
//	permission  — refused work should never reach a ledger, a slot or a tool.
//	validate    — the last chance to fail with no side effect, and the point at
//	              which arguments are final enough to derive a key from.
//	idempotency — a duplicate is answered from the ledger here, before it costs
//	              any capacity at all.
//	sandbox     — capacity is claimed only for work that will actually invoke
//	              something.
//	invoke      — the only step that changes the world.
//
// Getting this order wrong is not a style question, and it was wrong in the
// first version. Claiming the key before checking permission means a denied
// execution blocks the corrected retry. Claiming a sandbox slot before the
// ledger means a burst of identical requests is shed for concurrency it was
// never going to use. Both were found by tests; see ENGINEERING_AUDIT F3.
func (e *Executor) Execute(ctx context.Context, step Step, plan Plan, grant Grant, sink StreamSink, journal *Journal) ExecutionResult {
	start := e.clock.Now()
	execID := NewExecutionID()

	res := ExecutionResult{Execution: execID, Step: step.ID, Ref: step.Ref,
		Descriptor: step.Descriptor}

	contract := step.Contract
	budget := plan.Budget.tighten(contract.Budget.withDefaults(e.defaultBudget))
	retry := contract.Retry.withDefaults(e.defaultRetry)
	if budget.MaxAttempts > 0 && budget.MaxAttempts < retry.MaxAttempts {
		retry.MaxAttempts = budget.MaxAttempts
	}

	// ---- permission -------------------------------------------------------
	decision := e.permissions.Evaluate(contract, grant)
	if !decision.Allowed {
		res.Err = decision.Error()
		res.Phase = PhasePermission
		res.Duration = e.clock.Since(start)
		e.emit(EventPermissionDenied, plan, step, execID, 0, PhasePermission,
			decision.Reason, Arguments(step.Args).Fingerprint(), nil, res.Duration)
		e.record(AuditExecutionFailed, plan, step, execID, 0, PhasePermission,
			decision.Reason, step.Args, nil, res.Duration)
		return res
	}

	// ---- argument assembly and input budget -------------------------------
	args := step.Args.Clone()
	if args == nil {
		args = Arguments{}
	}
	if budget.InputBytes > 0 && args.SizeBytes() > budget.InputBytes {
		res.Err = fmt.Errorf("%w: %s arguments are %d bytes, budget is %d",
			ErrBudgetExceeded, step.Descriptor, args.SizeBytes(), budget.InputBytes)
		res.Phase = PhaseValidateIn
		res.Duration = e.clock.Since(start)
		if e.metrics != nil {
			e.metrics.BudgetRefused.Inc(string(step.Descriptor.Tool), "input_bytes")
		}
		e.emit(EventFailed, plan, step, execID, 0, PhaseValidateIn, "input_too_large",
			args.Fingerprint(), nil, res.Duration)
		return res
	}

	validated, err := contract.ValidateInput(args)
	if err != nil {
		res.Err = err
		res.Phase = PhaseValidateIn
		res.Duration = e.clock.Since(start)
		e.emit(EventFailed, plan, step, execID, 0, PhaseValidateIn, "invalid_input",
			args.Fingerprint(), nil, res.Duration)
		e.record(AuditExecutionFailed, plan, step, execID, 0, PhaseValidateIn,
			"invalid_input", args, nil, res.Duration)
		return res
	}
	inputPrint := validated.Fingerprint()

	// ---- idempotency ------------------------------------------------------
	//
	// DEDUPLICATION HAPPENS BEFORE CAPACITY IS CLAIMED. An execution that will
	// be served from the ledger costs no tool call, no connection and no
	// downstream load, so shedding it for want of a sandbox slot throws away an
	// answer the runtime already has. The first ordering admitted to the
	// sandbox first, and a burst of twenty-four identical requests then had
	// most of them refused for concurrency they were never going to use. See
	// ENGINEERING_AUDIT F3.
	//
	// Claimed for mutating effects always, and for reads only when the runtime
	// is configured to. A deduplicated read returns a stored answer, and a
	// stored answer to "is this slot still free" is exactly the wrong thing to
	// be confident about.
	var (
		claim *LedgerEntry
		key   IdempotencyKey
	)
	if contract.Effect.Mutating() || e.dedupeReads {
		key = DeriveKey(step.Descriptor, validated, grant.Actor, string(plan.Correlation))

		// Bounded, because a holder that repeatedly claims and releases could
		// otherwise spin this loop. Three rounds is generous: each one requires
		// a distinct holder to have abandoned its claim without an outcome.
		for round := 0; round < 3 && claim == nil; round++ {
			fresh, existing, claimErr := e.ledger.Claim(key, step.Descriptor, execID)
			switch {
			case fresh != nil:
				claim = fresh
			case existing != nil && !errors.Is(claimErr, ErrDuplicate):
				// A settled entry: replay it rather than doing the work again.
				return e.replay(existing, res, plan, step, execID, inputPrint, start)
			default:
				// Another execution holds the key right now. Wait for its
				// outcome rather than running the same mutating call twice.
				waited, waitErr := e.ledger.Await(ctx, existing)
				if waitErr != nil {
					res.Err = fmt.Errorf("%w: %v", ErrCancelled, waitErr)
					res.Phase = PhaseIdempotency
					res.Duration = e.clock.Since(start)
					return res
				}
				if waited != nil && waited.Settled() {
					return e.replay(waited, res, plan, step, execID, inputPrint, start)
				}
				// The holder released without an outcome, meaning it never
				// invoked the tool. Claim the key ourselves on the next round.
			}
		}
		if claim == nil {
			res.Err = fmt.Errorf("%w: could not claim %s after three rounds", ErrDuplicate, key)
			res.Phase = PhaseIdempotency
			res.Duration = e.clock.Since(start)
			return res
		}
	}

	// A claim that is never settled blocks every duplicate until the TTL
	// expires, so settlement is deferred rather than written at each exit.
	settled := false
	defer func() {
		if claim == nil || settled {
			return
		}
		e.ledger.Release(key)
	}()

	// ---- sandbox ----------------------------------------------------------
	lease, err := e.sandbox.Enter(step.Descriptor, budget)
	if err != nil {
		res.Err = err
		res.Phase = PhaseAdmit
		res.Duration = e.clock.Since(start)
		if e.metrics != nil {
			e.metrics.BudgetRefused.Inc(string(step.Descriptor.Tool), "slots")
		}
		e.emit(EventFailed, plan, step, execID, 0, PhaseAdmit, shortReason(err),
			inputPrint, nil, res.Duration)
		return res
	}
	defer lease.Release()

	// ---- attempts ---------------------------------------------------------
	e.emit(EventStarted, plan, step, execID, 1, PhaseInvoke, "", inputPrint, nil, 0)
	e.record(AuditExecutionStarted, plan, step, execID, 1, PhaseInvoke, "",
		validated, nil, 0)
	if e.metrics != nil {
		e.metrics.Started.Inc(string(step.Descriptor.Tool), string(step.Capability))
	}

	metered := newMeteredSink(sink, lease, e.clock.Now)
	breaker := e.retries.Breaker(step.Descriptor.Tool)

	var lastErr error
	for attempt := 1; attempt <= retry.MaxAttempts; attempt++ {
		res.Attempts = attempt

		if err := e.budgetRemaining(ctx, plan, budget, start); err != nil {
			lastErr = err
			break
		}

		allowed, report := breaker.Allow()
		if !allowed {
			lastErr = fmt.Errorf("%w: %s", ErrCircuitOpen, step.Descriptor.Tool)
			break
		}

		out, invokeErr := e.invokeOnce(ctx, step, contract, validated, execID, attempt,
			key, plan, grant, budget, metered, lease)
		report(invokeErr)

		if invokeErr == nil {
			if vErr := contract.ValidateOutput(out); vErr != nil {
				// A contract violation is NOT retried. The tool answered; it
				// answered wrongly, and asking again produces the same wrong
				// answer while a downstream waits.
				lastErr = vErr
				res.Phase = PhaseValidateOut
				break
			}
			if chargeErr := lease.ChargeOutput(out.SizeBytes()); chargeErr != nil {
				lastErr = chargeErr
				res.Phase = PhaseValidateOut
				break
			}

			res.Result = out
			res.Duration = e.clock.Since(start)
			res.Chunks = metered.Emitted()
			res.Phase = PhaseComplete
			settled = true
			if claim != nil {
				e.ledger.Settle(key, out, "completed", true)
			}
			e.succeed(plan, step, execID, attempt, inputPrint, out, res.Duration)
			e.journal(journal, step, contract, execID, attempt, validated, out, key, plan, grant, budget)
			return res
		}

		lastErr = invokeErr
		res.Phase = PhaseInvoke

		if attempt >= retry.MaxAttempts || !retry.retryable(invokeErr) || !contract.Effect.AutoRetryable() {
			break
		}

		delay := e.retries.Backoff(retry, attempt+1)
		if e.metrics != nil {
			e.metrics.Retried.Inc(string(step.Descriptor.Tool), fmt.Sprintf("%d", attempt+1))
		}
		e.emit(EventRetried, plan, step, execID, attempt, PhaseInvoke,
			shortReason(invokeErr), inputPrint, nil, e.clock.Since(start))

		if waitErr := e.retries.Wait(ctx, delay); waitErr != nil {
			lastErr = waitErr
			break
		}
	}

	// ---- terminal failure -------------------------------------------------
	res.Err = &ExecutionError{
		Tool: step.Descriptor.Tool, Version: step.Descriptor.Version, Step: step.ID,
		Attempt: res.Attempts, Phase: res.Phase, Retryable: Classify(lastErr), Err: lastErr,
	}
	res.Duration = e.clock.Since(start)
	res.Chunks = metered.Emitted()

	if claim != nil {
		// A failure that never invoked the tool releases the key; one that did
		// stores the failure, so a retry of a mutating call gets the same
		// answer instead of a second attempt at something already tried.
		if res.Phase == PhaseInvoke {
			settled = true
			e.ledger.Settle(key, nil, shortReason(lastErr), false)
		}
	}
	e.fail(plan, step, execID, res, inputPrint, lastErr)
	return res
}

// invokeOnce performs one attempt under a per-attempt deadline.
func (e *Executor) invokeOnce(ctx context.Context, step Step, contract Contract,
	args Arguments, execID ExecutionID, attempt int, key IdempotencyKey,
	plan Plan, grant Grant, budget Budget, sink *meteredSink, lease *Lease) (Result, error) {

	deadline := e.clock.Now().Add(contract.Timeout)
	if !plan.Deadline.IsZero() && plan.Deadline.Before(deadline) {
		deadline = plan.Deadline
	}

	attemptCtx, cancel := e.deadlineContext(ctx, deadline)
	defer cancel()

	inv := Invocation{
		Execution: execID, Step: step.ID, Attempt: attempt,
		Descriptor: step.Descriptor, Args: args, Idempotency: key,
		Correlation: plan.Correlation, Session: plan.Session, Actor: grant.Actor,
		Deadline: deadline, Budget: budget,
	}

	reg, ok := e.registry.Get(step.Descriptor)
	if !ok || reg.Tool == nil {
		return nil, fmt.Errorf("%w: %s has no implementation", ErrNotRegistered, step.Descriptor)
	}
	if !reg.Lifecycle.Dispatchable() && reg.Lifecycle != LifecycleDraining {
		// Draining is deliberately still invocable: a plan pinned to a version
		// must be able to finish after somebody decided that version is going
		// away. That is the entire purpose of the draining stage.
		return nil, fmt.Errorf("%w: %s is %s", ErrNotRegistered, step.Descriptor, reg.Lifecycle)
	}

	start := e.clock.Now()
	out, err := e.callTool(attemptCtx, reg.Tool, inv, sink, contract.Streaming)
	elapsed := e.clock.Since(start)

	if e.metrics != nil {
		e.metrics.InvokeLatency.Observe(elapsed.Seconds(), string(step.Descriptor.Tool))
	}

	if err != nil {
		// Distinguish a deadline from a cancellation and from a tool's own
		// error, because they lead to three different operator responses:
		// tune the timeout, look at who cancelled, or look at the tool.
		//
		// The deadline is read from context.Cause, NOT from ctx.Err().
		// deadlineContext is built on WithCancelCause, and a context cancelled
		// with a cause reports Err() == context.Canceled regardless of why.
		// Reading Err() therefore turns every timeout into an anonymous
		// cancellation — exactly the distinction this switch exists to
		// preserve. See ENGINEERING_AUDIT F2.
		switch {
		case errors.Is(context.Cause(attemptCtx), context.DeadlineExceeded):
			if e.metrics != nil {
				e.metrics.TimedOut.Inc(string(step.Descriptor.Tool))
			}
			return nil, fmt.Errorf("%w after %s: %v", ErrTimeout, contract.Timeout, err)
		case ctx.Err() != nil:
			return nil, fmt.Errorf("%w: %v", ErrCancelled, err)
		}
		return nil, err
	}
	if sink.Exceeded() {
		return nil, fmt.Errorf("%w: %s exceeded its stream output budget", ErrBudgetExceeded, step.Descriptor)
	}
	_ = lease
	return out, nil
}

// callTool invokes the tool, taking the streaming path where both sides support
// it, and guarantees the call cannot outlive its context from the runtime's
// point of view.
//
// The goroutine is the honest part. A tool that ignores context cancellation
// cannot be stopped — Go has no way to kill a goroutine — so the runtime
// ABANDONS it and moves on, counting the abandonment. That counter is the
// early warning for a tool that is quietly leaking a goroutine per timeout, and
// it is the reason [ExecutionSupervisor] exists.
func (e *Executor) callTool(ctx context.Context, tool Tool, inv Invocation, sink *meteredSink, streaming bool) (Result, error) {
	ch := make(chan invokeOutcome, 1)

	go func() {
		var (
			res Result
			err error
		)
		defer func() {
			if r := recover(); r != nil {
				// A panicking tool must not take the runtime down with it. It
				// becomes a failed execution like any other, and the panic
				// value is deliberately NOT put in the error message: a panic
				// value can contain caller content, and errors travel into
				// logs and metrics.
				err = fmt.Errorf("%w: tool %s panicked", ErrInvariant, inv.Descriptor)
				res = nil
			}
			ch <- invokeOutcome{res, err}
		}()
		if st, ok := tool.(StreamingTool); ok && streaming {
			res, err = st.InvokeStream(ctx, inv, sink)
			return
		}
		res, err = tool.Invoke(ctx, inv)
	}()

	select {
	case o := <-ch:
		return o.res, o.err
	case <-ctx.Done():
		if e.supervisor != nil {
			e.supervisor.abandon(inv.Descriptor, ch)
		}
		if e.metrics != nil {
			e.metrics.Abandoned.Inc(string(inv.Descriptor.Tool))
		}
		return nil, ctx.Err()
	}
}

// deadlineContext derives a context whose deadline is driven by the INJECTED
// clock rather than by wall time.
//
// This is Phase 10A's finding F1 applied here rather than rediscovered.
// context.WithDeadline schedules against real time; a runtime whose deadlines
// come from a FakeClock but whose timers come from the OS has tests that either
// hang or expire instantly, depending on which way the two clocks disagree.
// Deriving the timer from the same clock keeps a timeout test deterministic and
// microsecond-fast.
func (e *Executor) deadlineContext(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)

	remaining := deadline.Sub(e.clock.Now())
	if remaining <= 0 {
		cancel(context.DeadlineExceeded)
		return ctx, func() { cancel(context.Canceled) }
	}

	timer := e.clock.NewTimer(remaining)
	stop := make(chan struct{})
	go func() {
		defer timer.Stop()
		select {
		case <-timer.C():
			cancel(context.DeadlineExceeded)
		case <-stop:
		case <-ctx.Done():
		}
	}()

	var once sync.Once
	return ctx, func() {
		once.Do(func() { close(stop) })
		cancel(context.Canceled)
	}
}

// budgetRemaining reports whether there is time left for another attempt.
func (e *Executor) budgetRemaining(ctx context.Context, plan Plan, budget Budget, start time.Time) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrCancelled, err)
	}
	now := e.clock.Now()
	if budget.WallClock > 0 && now.Sub(start) >= budget.WallClock {
		return fmt.Errorf("%w: execution wall clock %s elapsed", ErrBudgetExceeded, budget.WallClock)
	}
	if !plan.Deadline.IsZero() && !now.Before(plan.Deadline) {
		return fmt.Errorf("%w: plan deadline passed", ErrTimeout)
	}
	return nil
}

func (e *Executor) replay(entry *LedgerEntry, res ExecutionResult, plan Plan, step Step,
	execID ExecutionID, inputPrint Fingerprint, start time.Time) ExecutionResult {

	res.Result = entry.Result.Clone()
	res.Replayed = true
	res.Attempts = 0
	res.Phase = PhaseIdempotency
	res.Duration = e.clock.Since(start)

	if entry.State == LedgerFailed {
		res.Err = fmt.Errorf("%w: prior execution failed with %s", ErrDuplicate, entry.Reason)
	}

	e.emit(EventCompleted, plan, step, execID, 0, PhaseIdempotency, "replayed",
		inputPrint, res.Result, res.Duration)
	e.record(AuditReplayed, plan, step, execID, 0, PhaseIdempotency, "replayed",
		nil, res.Result, res.Duration)
	return res
}

func (e *Executor) succeed(plan Plan, step Step, execID ExecutionID, attempt int,
	inputPrint Fingerprint, out Result, d time.Duration) {

	if e.metrics != nil {
		e.metrics.Completed.Inc(string(step.Descriptor.Tool), string(step.Capability))
		e.metrics.ExecutionLatency.Observe(d.Seconds(), string(step.Descriptor.Tool))
	}
	e.registry.RecordOutcome(step.Descriptor, true)
	e.emit(EventCompleted, plan, step, execID, attempt, PhaseComplete, "",
		inputPrint, out, d)
	e.record(AuditExecutionCompleted, plan, step, execID, attempt, PhaseComplete, "",
		nil, out, d)
}

func (e *Executor) fail(plan Plan, step Step, execID ExecutionID, res ExecutionResult,
	inputPrint Fingerprint, cause error) {

	reason := shortReason(cause)
	if e.metrics != nil {
		e.metrics.Failed.Inc(string(step.Descriptor.Tool), string(res.Phase), reason)
		e.metrics.ExecutionLatency.Observe(res.Duration.Seconds(), string(step.Descriptor.Tool))
	}
	e.registry.RecordOutcome(step.Descriptor, false)

	eventType := EventFailed
	switch {
	case errors.Is(cause, ErrTimeout):
		eventType = EventTimedOut
	case errors.Is(cause, ErrCancelled):
		eventType = EventCancelled
		if e.metrics != nil {
			e.metrics.Cancelled.Inc(string(step.Descriptor.Tool), reason)
		}
	}

	e.emit(eventType, plan, step, execID, res.Attempts, res.Phase, reason,
		inputPrint, nil, res.Duration)
	e.record(AuditExecutionFailed, plan, step, execID, res.Attempts, res.Phase, reason,
		nil, nil, res.Duration)

	if e.retries != nil && e.retries.dead != nil {
		e.retries.dead.Add(DeadLetter{
			Execution: execID, Step: step.ID, Descriptor: step.Descriptor,
			Correlation: plan.Correlation, Actor: plan.Actor, Attempts: res.Attempts,
			Phase: res.Phase, InputPrint: inputPrint, Reason: reason,
			FailedAt: e.clock.Now(),
		})
	}
}

func (e *Executor) journal(j *Journal, step Step, contract Contract, execID ExecutionID,
	attempt int, args Arguments, out Result, key IdempotencyKey, plan Plan, grant Grant, budget Budget) {

	if j == nil || !contract.Effect.Mutating() {
		return
	}
	comp, _ := e.toolFor(step.Descriptor).(CompensatingTool)
	j.Record(CompletedWork{
		Step: step.ID, Execution: execID, Descriptor: step.Descriptor,
		Invocation: Invocation{
			Execution: execID, Step: step.ID, Attempt: attempt,
			Descriptor: step.Descriptor, Args: args, Idempotency: key,
			Correlation: plan.Correlation, Session: plan.Session, Actor: grant.Actor,
			Budget: budget,
		},
		Produced: out.Clone(), Tool: comp, CompletedAt: e.clock.Now(), Effect: contract.Effect,
	})
}

func (e *Executor) toolFor(d Descriptor) Tool {
	reg, ok := e.registry.Get(d)
	if !ok {
		return nil
	}
	return reg.Tool
}

func (e *Executor) emit(t EventType, plan Plan, step Step, execID ExecutionID, attempt int,
	phase Phase, reason string, in Fingerprint, out Result, d time.Duration) {

	if e.events == nil {
		return
	}
	ev := Event{
		Type: t, Execution: execID, Step: step.ID, Plan: plan.ID, Intent: plan.Intent,
		Correlation: plan.Correlation, Session: plan.Session, Actor: plan.Actor,
		Descriptor: step.Descriptor, Capability: step.Capability, Effect: step.Effect,
		Attempt: attempt, Phase: phase, InputPrint: in, Reason: reason, Duration: d,
	}
	if out != nil {
		ev.OutputPrint = out.Fingerprint()
		ev.OutputBytes = out.SizeBytes()
	}
	e.events.Dispatch(ev)
}

func (e *Executor) record(kind AuditKind, plan Plan, step Step, execID ExecutionID, attempt int,
	phase Phase, reason string, args Arguments, out Result, d time.Duration) {

	if e.audit == nil {
		return
	}
	entry := AuditEntry{
		At: e.clock.Now(), Kind: kind, Execution: execID, Step: step.ID, Plan: plan.ID,
		Correlation: plan.Correlation, Session: plan.Session, Actor: plan.Actor,
		Descriptor: step.Descriptor, Attempt: attempt, Phase: phase, Reason: reason,
		Duration: d,
	}
	if args != nil {
		entry.InputPrint = args.Fingerprint()
	}
	if out != nil {
		entry.OutputPrint = out.Fingerprint()
	}
	if err := e.audit.Record(entry); err != nil {
		if e.metrics != nil {
			e.metrics.AuditFailed.Inc(string(kind))
		}
		return
	}
	if e.metrics != nil {
		e.metrics.AuditWritten.Inc(string(kind))
	}
}
