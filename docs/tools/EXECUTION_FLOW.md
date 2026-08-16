# Execution Flow

**Phase 10D** · Sourced from
[`plan.go`](../../packages/go/toolruntime/plan.go),
[`executor.go`](../../packages/go/toolruntime/executor.go),
[`dispatcher.go`](../../packages/go/toolruntime/dispatcher.go)

---

## 1 · Intent to outcome

```mermaid
flowchart TD
    CE["conversation engine<br/>(10B, frozen)"] --> INT[ToolIntent<br/>names CAPABILITIES]

    INT --> VAL{Intent.Validate<br/>structure only}
    VAL -->|cycle, unknown ref,<br/>no actor| RJ1[ConfigError]
    VAL -->|ok| RES

    subgraph Planning ["Planning — executes nothing (INV-TOOL-8)"]
        RES[Discovery.Resolve<br/>per request] --> RJ2{resolvable?}
        RJ2 -->|nothing provides it| E1[ErrNoCapability<br/>DEPLOYMENT gap]
        RJ2 -->|exists, none usable| E2[ErrNoHealthyProvider<br/>OUTAGE]
        RJ2 -->|version matches nothing| E3[ErrVersionUnsatisfiable]
        RJ2 -->|yes| STATIC[checkStatic:<br/>args and bindings<br/>against the contract]
        STATIC --> LEV[levelise:<br/>Kahn, sorted ready set]
        LEV --> ASM[assemble:<br/>single / sequential /<br/>parallel / mixed]
    end

    ASM --> PLAN[("Plan — inert data<br/>versions PINNED")]

    PLAN --> DISP[ExecutionDispatcher.Run]

    subgraph Execution
        DISP --> WALK[walk the step tree]
        WALK --> SCHED[ToolScheduler.Acquire<br/>shed at admission]
        SCHED --> EXEC[Executor.Execute<br/>the ONLY path to a Tool]
    end

    EXEC --> EV[["tool.execution.*.v1<br/>identifiers and fingerprints"]]
    EV --> MEM["memory (10C) subscribes<br/>— never written directly"]

    EXEC --> OUT{plan succeeded?}
    OUT -->|yes| DONE[PlanResult]
    OUT -->|no, journal non-empty| ROLL[Compensator.Rollback<br/>reverse order]
    ROLL --> DONE

    style PLAN fill:#1F7A3D,stroke:#145227,color:#fff
    style E1 fill:#8B5A00,stroke:#5e3d00,color:#fff
    style E2 fill:#8B2635,stroke:#5e1a24,color:#fff
    style ROLL fill:#8B5A00,stroke:#5e3d00,color:#fff
```

**The two resolution failures are different colours on purpose.**
`ErrNoCapability` means nothing in the registry claims to do this — a deployment
gap, fixed by shipping something. `ErrNoHealthyProvider` means tools exist and
none is usable — an outage, fixed by fixing something. Collapsing them means an
on-call engineer cannot tell whether to page the deploy owner or the integration
owner.

---

## 2 · One step, in detail

```mermaid
flowchart TD
    S[Executor.Execute] --> P{PermissionEngine}
    P -->|denied| PD["ErrPermissionDenied<br/>+ EventPermissionDenied<br/>+ audit"]
    P -->|consent missing| CR[ErrConsentRequired]
    P -->|allowed| IB{input within<br/>budget?}

    IB -->|no| BE1[ErrBudgetExceeded]
    IB -->|yes| VI{Contract.ValidateInput<br/>defaults applied}
    VI -->|undeclared arg,<br/>bad type, out of range| II[ErrInvalidInput]
    VI -->|ok| ID{mutating<br/>or DedupeReads?}

    ID -->|no| SB
    ID -->|yes| CLAIM{Ledger.Claim}
    CLAIM -->|settled entry| RP["replay — no tool call<br/>EventCompleted reason=replayed"]
    CLAIM -->|in flight| AW[Await the holder]
    AW --> RP
    CLAIM -->|fresh| SB

    SB{Sandbox.Enter} -->|no slots| BE2[ErrBudgetExceeded]
    SB -->|lease| ATT

    subgraph ATT ["attempt loop, bounded by MaxAttempts and WallClock"]
        BUD{budget left?} -->|no| STOP1[ErrBudgetExceeded / ErrTimeout]
        BUD -->|yes| BRK{Breaker.Allow}
        BRK -->|open| CO[ErrCircuitOpen — tool NOT called]
        BRK -->|allowed| INV["invoke under<br/>deadlineContext (injected clock)"]
        INV -->|error| CLS{Classify + Effect.AutoRetryable}
        CLS -->|retryable| BACK[backoff + EventRetried] --> BUD
        CLS -->|not| STOP2[terminal]
        INV -->|ok| VO{ValidateOutput}
        VO -->|violates contract| STOP3["ErrInvalidOutput — NOT retried"]
        VO -->|ok| CHG{output within budget?}
        CHG -->|no| STOP4[ErrBudgetExceeded]
        CHG -->|yes| OK[success]
    end

    OK --> SET[Ledger.Settle + Journal.Record]
    SET --> DONE["EventCompleted + audit"]
    STOP2 --> DL[DeadLetterQueue]

    style RP fill:#1F7A3D,stroke:#145227,color:#fff
    style CO fill:#8B5A00,stroke:#5e3d00,color:#fff
    style STOP3 fill:#8B2635,stroke:#5e1a24,color:#fff
```

### Why this order

```
permission → validate → idempotency → sandbox → invoke
```

| Stage | Why here |
|---|---|
| **permission** | Refused work should never reach a ledger, a slot or a tool |
| **validate** | Last chance to fail with no side effect — and the point at which arguments are final enough to derive a key from |
| **idempotency** | A duplicate is answered here, **before it costs any capacity** |
| **sandbox** | Capacity is claimed only for work that will actually invoke something |
| **invoke** | The only step that changes the world |

**Two of these were wrong in the first version.** Claiming the key before
permission meant a denied execution blocked the corrected retry. Claiming a
sandbox slot before the ledger meant a burst of twenty-four identical requests
had most of them shed for concurrency they were never going to use. Both were
found by tests — ENGINEERING_AUDIT §F3.

### `ErrInvalidOutput` is never retried

The tool answered; it answered wrongly. Asking again produces the same wrong
answer while a downstream waits.

### `ErrCircuitOpen` fails fast and says so

Not presented as a timeout. An open circuit is the breaker saying stop, and
retrying past it defeats the point of having one.

---

## 3 · Composite shapes

```mermaid
flowchart LR
    subgraph SEQ [StepSequence]
        direction LR
        A1[a] --> B1[b] --> C1[c]
        note1["stops at the first failure"]
    end
```

```mermaid
flowchart TD
    subgraph PAR [StepParallel]
        direction TB
        P0[fork] --> A2[a]
        P0 --> B2[b]
        A2 --> J[join — WAITS FOR BOTH]
        B2 --> J
    end
```

**Parallel waits for every branch even after one fails.** Cancelling siblings on
the first failure sounds efficient and produces the worst possible state: a
mutating sibling cancelled mid-flight may or may not have taken effect, and
nothing downstream can tell which. Letting every branch reach a definite outcome
is what keeps the compensation journal truthful.

The reported error is the first in **child order**, not completion order, so the
same failure is reported the same way every time.

```mermaid
flowchart TD
    subgraph FB [StepFallback]
        F0[primary] -->|fails| F1[first fallback]
        F1 -->|fails| F2[second fallback]
        F0 -->|timeout AND mutating| STOPFB["STOP — may have taken effect"]
    end

    style STOPFB fill:#8B2635,stroke:#5e1a24,color:#fff
```

**A fallback stops on a timeout for a mutating step.** If the first candidate
timed out, it may have taken effect; trying the next risks doing it twice. The
planner refuses a fallback chain over an *irreversible* tool outright.

```mermaid
flowchart TD
    subgraph COND [StepConditional]
        C0{"condition over an<br/>earlier step's result"} -->|true| C1[run child]
        C0 -->|false| C2["Skipped — NOT a failure"]
    end
```

A skipped conditional does not fail the plan. `ExecutionResult.Skipped` is a
distinct field from `Err` for exactly that reason: a plan that skips a step
because the condition said so has done exactly what it was asked to.

### Six condition operators, and no more

`exists · absent · == · != · > · <`

No arithmetic, no regular expressions, no boolean algebra. **A condition
language grows until it is a programming language, and then the plan is a
program nobody reviews.** Anything more complex belongs in a tool, where it is
testable, ownable and named.

A missing source result evaluates **false** for every operator except `absent` —
treating it as an error would turn a skipped optional step into a plan failure,
which is the opposite of what "optional" means.

---

## 4 · Failure and rollback

```mermaid
sequenceDiagram
    participant D as Dispatcher
    participant J as Journal
    participant C as Compensator
    participant T3 as tool C
    participant T2 as tool B
    participant T1 as tool A

    Note over D: steps A, B succeed; C fails
    D->>J: Record(A) · Record(B)
    Note over J: read-only steps are NOT recorded
    D->>C: Rollback(journal, reason)

    Note over C: own context from Background —<br/>the execution's context is usually dead

    C->>T2: Compensate(B) — REVERSE order
    T2-->>C: ok
    C->>T1: Compensate(A)
    T1-->>C: error
    Note over C: continues anyway; the failure of one<br/>says nothing about the next

    C-->>D: CompensationReport{compensated:1, failed:1, complete:false}
    Note over D: a failed rollback REPLACES the original error
```

| Outcome | Meaning | Operator action |
|---|---|---|
| **Compensated** | Undone | None |
| **Skipped** | No compensation was possible — the tool has none | Decide whether it matters |
| **Failed** | We tried and it did not work | **The world is in a state nobody chose** |

`CompensationReport.Complete` is false whenever anything was skipped or failed.

**What it does not claim:** `Complete` answers *"was everything the runtime knows
about undone"*, not *"is the world back where it started"*. The step that failed
may have taken partial effect before failing, and no runtime can know whether it
did. That gap is recorded in EXECUTION_EVALUATION §E3 rather than papered over.

---

## 5 · Load shedding

```mermaid
flowchart TD
    A[Acquire class] --> C{in flight <<br/>MaxConcurrent?}
    C -->|yes| G[granted immediately]
    C -->|no| Q{class queue<br/>at capacity?}
    Q -->|yes| SHED[ErrQueueFull<br/>shed at admission]
    Q -->|no| W[wait on a channel]
    W -->|slot released| G
    W -->|context cancelled| RM[removed from the queue<br/>— no slot leak]

    style SHED fill:#8B5A00,stroke:#5e3d00,color:#fff
```

Frozen invariant **I11**: under load the platform sheds **at admission** rather
than degrading something mid-flight.

| Class | Queue bound | Rationale |
|---|---|---:|
| Interactive | 32 | **Shortest.** A deadline in hundreds of milliseconds means a deep queue admits work that will time out before it is served |
| Background | 128 | |
| Bulk | 512 | No deadline, so queueing is free |

The interactive queue being the shortest looks backwards and is not.

**Anti-starvation:** strict priority alone is correct until the interactive load
never drops, at which point background work stops entirely and nobody notices
for a week. After `StarvationRatio` consecutive higher-class grants while a
lower class waits, the oldest lower-class waiter is served.

**A cancelled waiter is removed from the queue.** Leaving it would mean a later
release grants a slot to a waiter that has gone — a leak that presents as
gradually falling throughput, which is a diagnosis that costs hours.

---

## 6 · Cancellation

```mermaid
sequenceDiagram
    participant U as caller hangs up
    participant CO as ToolCoordinator
    participant R as ToolRuntime
    participant E as Executor
    participant T as tool

    U->>CO: Cancel(correlation, "caller_hung_up")
    CO->>R: every tracked cancel func for that correlation
    R->>E: context cancelled
    E->>T: ctx.Done()
    alt tool honours cancellation
        T-->>E: returns promptly
    else tool ignores it
        E->>E: ABANDON — Go cannot kill a goroutine
        E->>E: Supervisor.Abandoned()++
    end
    E->>E: compensate completed mutating work
    E-->>CO: PlanResult with ErrCancelled
```

**Cancelled executions still compensate.** Hanging up must not leave a half-made
booking.

**Abandonment is counted, not hidden.** A tool with a rising abandonment count
does not honour cancellation, and that is a bug report with an owner rather than
an invisible goroutine leak.

---

## 7 · Streaming

```mermaid
flowchart LR
    T[StreamingTool] -->|Chunk| M[meteredSink]
    M -->|"stamp sequence<br/>charge output budget"| S[caller's sink]
    M -->|over budget| X[ErrBudgetExceeded<br/>stops the stream]
    S -->|buffer full| DROP["drop the NEWEST<br/>and count it"]

    T -->|final| R[Result — always complete]

    style X fill:#8B2635,stroke:#5e1a24,color:#fff
```

Three properties worth stating:

**A stream is an early view of the answer, never the answer itself.** The final
`Result` is returned in full, so a consumer that ignored the stream still
receives everything.

**`BufferedSink` drops the newest, not the oldest.** Early chunks are the ones a
caller has already started rendering; dropping those would rewrite what a person
has already seen. Dropping the tail degrades something the final result replaces.

**The metered sink wraps whatever the caller supplied**, so an oversized stream
is caught even when the caller is using a `NoopSink` and would never have
noticed. Budget enforcement that only works when someone is watching is not
enforcement.
