# Sequence Diagrams

**Phase 10D** · `packages/go/toolruntime`

Eight sequences, each one a case that shaped a design decision. Every one is
covered by a named test.

---

## 1 · The happy path

```mermaid
sequenceDiagram
    autonumber
    participant C as conversation (10B)
    participant R as ToolRuntime
    participant PL as Planner
    participant D as Discovery
    participant S as Scheduler
    participant E as Executor
    participant P as Permissions
    participant SB as Sandbox
    participant T as Tool
    participant EV as Events
    participant M as memory (10C)

    C->>R: Execute(ToolIntent{capability: "lookup"})
    R->>PL: Plan(intent)
    PL->>D: Resolve(capability)
    D-->>PL: [candidate@1.0.0]
    PL->>PL: checkStatic · levelise · assemble
    PL-->>R: Plan (inert, version PINNED)

    R->>S: Acquire(interactive)
    S-->>R: slot
    R->>E: Execute(step)
    E->>P: Evaluate(contract, grant)
    P-->>E: allowed
    E->>E: ValidateInput — defaults applied
    E->>SB: Enter(descriptor, budget)
    SB-->>E: lease
    E->>EV: EventStarted
    E->>T: Invoke(ctx, invocation)
    T-->>E: Result
    E->>E: ValidateOutput · charge output budget
    E->>EV: EventCompleted (fingerprints only)
    EV-->>M: tool.execution.completed.v1
    E-->>R: ExecutionResult
    R-->>C: PlanResult
```

Note what the conversation engine never sees: which tool, which version, which
fallback. It asked for a capability and got an answer.

*Test:* `TestIntegration_SingleToolEndToEnd`

---

## 2 · Retry, then success

```mermaid
sequenceDiagram
    autonumber
    participant E as Executor
    participant B as Breaker
    participant T as Tool
    participant RE as RetryEngine
    participant EV as Events

    E->>B: Allow()
    B-->>E: allowed
    E->>T: Invoke (attempt 1)
    T-->>E: transient error
    E->>B: report(err)
    E->>E: Classify(err) → retryable
    E->>EV: EventRetried
    E->>RE: Wait(backoff on the INJECTED clock)

    E->>B: Allow()
    E->>T: Invoke (attempt 2)
    T-->>E: transient error
    E->>EV: EventRetried
    E->>RE: Wait(backoff × multiplier + jitter)

    E->>T: Invoke (attempt 3)
    T-->>E: Result
    E->>EV: EventCompleted
```

**The tool does not get a vote on retryability.** `Classify` lives in the
runtime and sees the sentinel set. A tool marking its own errors retryable will,
eventually, mark a permission denial retryable, and the runtime will hammer a
downstream with a request that cannot ever succeed.

**Jitter comes from a seeded, runtime-owned source**, never the global one. Two
runtimes in one process would otherwise interfere, and a test's backoff sequence
would depend on whatever ran first.

*Test:* `TestIntegration_RetriesTransientFailureThenSucceeds`

---

## 3 · Timeout on the injected clock

```mermaid
sequenceDiagram
    autonumber
    participant TE as test
    participant E as Executor
    participant CL as FakeClock
    participant T as Tool (slow)

    E->>CL: NewTimer(contract.Timeout)
    E->>T: Invoke on its own goroutine
    T->>CL: Sleep(longer than the timeout)

    TE->>CL: BlockUntil(2) — both waiters registered
    TE->>CL: Advance(2s)

    CL-->>E: timer fires → cancel(context.DeadlineExceeded)
    CL-->>T: sleep returns ctx.Err()
    E->>E: context.Cause(ctx) == DeadlineExceeded
    E-->>TE: ErrTimeout + EventTimedOut
```

Two lessons are encoded here, and one of them was learned twice.

**Deadlines come from the injected clock.** `context.WithDeadline` schedules
against real time; a runtime whose deadlines come from a `FakeClock` but whose
timers come from the OS has tests that either hang or expire instantly. This is
Phase 10A's finding F1, applied here rather than rediscovered.

**The deadline is read from `context.Cause`, not `ctx.Err()`.** A context
cancelled with a cause reports `Err() == context.Canceled` regardless of why, so
reading `Err()` turns every timeout into an anonymous cancellation. That one
*was* rediscovered — ENGINEERING_AUDIT §F2.

*Test:* `TestIntegration_TimeoutIsDrivenByTheInjectedClock`

---

## 4 · Concurrent duplicates share one invocation

```mermaid
sequenceDiagram
    autonumber
    participant A as caller A
    participant B as caller B
    participant L as Ledger
    participant E as Executor
    participant T as Tool

    par identical requests, same correlation
        A->>E: Execute
    and
        B->>E: Execute
    end

    E->>L: Claim(key) for A
    L-->>E: fresh claim
    E->>L: Claim(key) for B
    L-->>E: ErrDuplicate + the in-flight entry

    E->>L: Await(entry) for B
    Note over B: B does NOT invoke the tool

    E->>T: Invoke (once, for A)
    T-->>E: Result
    E->>L: Settle(key, result)
    L-->>E: wakes B
    E-->>A: Result
    E-->>B: the SAME result, Replayed=true
```

This is what a client retry storm looks like, and it produces **one tool call
and N answers**. Measured at 64 callers → 1 invocation, 64 served
(EXECUTION_EVALUATION §E2).

**Deduplication happens before capacity is claimed.** An execution served from
the ledger costs no tool call and no downstream load, so shedding it for want of
a sandbox slot would throw away an answer the runtime already has. The first
ordering got this backwards — ENGINEERING_AUDIT §F3.

*Test:* `TestStress_ConcurrentDuplicatesInvokeTheToolOnce`

---

## 5 · Partial failure and rollback

```mermaid
sequenceDiagram
    autonumber
    participant D as Dispatcher
    participant TA as tool A (compensable)
    participant TB as tool B (compensable)
    participant TC as tool C (fails)
    participant J as Journal
    participant CP as Compensator
    participant EV as Events

    D->>TA: invoke
    TA-->>D: ok
    D->>J: Record(A)
    D->>TB: invoke
    TB-->>D: ok
    D->>J: Record(B)
    D->>TC: invoke
    TC-->>D: error

    D->>CP: Rollback(journal)
    Note over CP: OWN context from Background —<br/>the execution's context is usually dead

    CP->>TB: Compensate(B) — reverse order
    TB-->>CP: ok
    CP->>EV: EventRolledBack
    CP->>TA: Compensate(A)
    TA-->>CP: ok
    CP->>EV: EventRolledBack

    CP-->>D: CompensationReport{compensated: 2, complete: true}
    D-->>D: PlanResult.Err = the original failure
```

**Reverse order is not a preference.** Later steps may depend on state earlier
ones created; undoing the earlier one first can make the later one's
compensation impossible.

**Read-only steps are never journalled.** There is nothing to undo about a
lookup, and recording them would fill a rollback report with steps that were
"compensated" by doing nothing.

*Test:* `TestIntegration_FailedPlanRollsBackInReverseOrder`

---

## 6 · A rollback that fails

```mermaid
sequenceDiagram
    autonumber
    participant D as Dispatcher
    participant CP as Compensator
    participant TA as tool A
    participant AU as Audit

    D->>CP: Rollback(journal)
    CP->>TA: Compensate(A)
    TA-->>CP: error — undo endpoint down
    CP->>AU: AuditCompensationFailed
    CP-->>D: report{failed: 1, complete: false}

    Note over D: the compensation failure REPLACES<br/>the original error
    D-->>D: PlanResult.Err = ErrCompensationFailed
```

**The worst outcome this runtime can produce.** The world is in a state nobody
chose, and no further automation can be trusted to fix it.

The original failure is recoverable by retrying; a failed rollback is not.
Surfacing the lesser problem would let a caller retry into a world it does not
understand — so the greater one wins.

*Test:* `TestIntegration_CompensationFailureReplacesTheOriginalError`

---

## 7 · Cancellation mid-call

```mermaid
sequenceDiagram
    autonumber
    participant U as caller hangs up
    participant CO as ToolCoordinator
    participant R as ToolRuntime
    participant E as Executor
    participant T as Tool
    participant SU as Supervisor

    U->>CO: Cancel(correlation)
    CO->>R: every tracked cancel func
    R->>E: context cancelled

    alt tool honours cancellation
        E->>T: ctx.Done()
        T-->>E: returns promptly
    else tool ignores it
        E->>SU: abandon(descriptor)
        SU->>SU: Abandoned()++ · drain the channel
        Note over E: Go cannot kill a goroutine.<br/>The runtime moves on and COUNTS it.
    end

    E->>E: compensate completed mutating work
    E-->>CO: ErrCancelled
```

Abandonment is the honest part of this design. A tool that ignores cancellation
cannot be stopped, so it is abandoned and counted — turning an invisible
goroutine leak into a number on a dashboard with an owner's name against it.

*Tests:* `TestIntegration_CoordinatorCancelsAWholeCorrelation`,
`TestFailure_AbandonedGoroutineIsCounted`

---

## 8 · Load shedding at admission

```mermaid
sequenceDiagram
    autonumber
    participant A as request A
    participant B as request B
    participant C as request C
    participant S as Scheduler
    participant E as Executor

    A->>S: Acquire(interactive)
    S-->>A: slot (MaxConcurrent reached)
    B->>S: Acquire(interactive)
    S-->>B: queued
    C->>S: Acquire(interactive)
    S-->>C: ErrQueueFull — SHED, not queued

    Note over C: a caller receiving ErrQueueFull can degrade;<br/>a caller waiting behind a thousand others cannot

    A->>E: ...finishes
    E->>S: release
    S-->>B: slot passes straight across
```

Frozen invariant **I11**: shed at admission rather than degrade mid-flight.

A shed execution never reaches the tool at all — asserted, along with the
counters adding up, by `TestStress_OverloadShedsCleanlyAndIsAccountedFor`.

*Test:* `TestFailure_QueueFullShedsRatherThanQueueing`

---

## 9 · Streaming with a budget

```mermaid
sequenceDiagram
    autonumber
    participant T as StreamingTool
    participant M as meteredSink
    participant L as Lease
    participant S as caller's sink
    participant E as Executor

    loop per chunk
        T->>M: Write(chunk)
        M->>M: stamp sequence (monotonic from 1)
        M->>L: ChargeOutput(size)
        alt within budget
            L-->>M: ok
            M->>S: forward
        else over budget
            L-->>M: ErrBudgetExceeded
            M-->>T: stop the stream
        end
    end

    T-->>E: final Result (always complete)
    E->>E: ValidateOutput
```

**An unbounded stream is the same denial of service as an oversized result,
arriving more slowly.** Charging per chunk is what catches it; checking only the
final result never sees it.

The metered sink wraps whatever the caller supplied, so this holds even when the
caller is using a `NoopSink` and would never have noticed.

*Tests:* `TestIntegration_StreamingDeliversPartialResultsInOrder`,
`TestIntegration_UnboundedStreamHitsTheOutputBudget`
