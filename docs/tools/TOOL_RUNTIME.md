# Enterprise Tool Calling Runtime — Architecture

**Phase 10D** · `packages/go/toolruntime` · Status: **PROPOSED — awaiting approval**

The component that turns a stated intent into a bounded, permitted, audited,
compensable execution. Built from scratch on the Go standard library and the
frozen Phase 10A runtime.

---

## 1 · The one decision everything else follows from

**A `ToolIntent` names a CAPABILITY, not a tool.**

The conversation engine says *"check availability for this business on
Tuesday"*. It does not say *"call `calendar_lookup_v3`"*.

That sounds like a small indirection. It is not.

| If the caller named the tool | Because it names a capability |
|---|---|
| Every caller owns version resolution | The runtime resolves once |
| Every caller owns health checking | The registry knows |
| Every caller owns fallback selection | The planner builds the chain |
| Every caller owns permission scoping | One permission engine |
| Provider agnosticism is a convention | Provider agnosticism is structural |

The last row is the load-bearing one. A vendor's tool-calling protocol — OpenAI
function calling, Anthropic tool use, Gemini function declarations — is a wire
format for *"call this named function with this JSON"*. **A capability request
is not expressible in that format.** That is precisely why translating a
provider's tool call into a `ToolIntent` is an adapter's job, and why this
package has never heard of any provider.

Nothing in this module changes if the platform changes model vendor. Nothing in
it would change if the platform stopped using a language model at all.

---

## 2 · Position in the platform

```
   ┌──────────────────┐
   │  conversation    │  emits ToolIntent (a struct, not a call)
   │  (10B, frozen)   │
   └────────┬─────────┘
            │
   ┌────────▼─────────┐        events        ┌──────────────────┐
   │  toolruntime     │ ───────────────────▶ │  memory (10C)    │
   │  (10D)           │  tool.execution.*.v1 │  subscribes      │
   └────────┬─────────┘                      └──────────────────┘
            │ imports
   ┌────────▼─────────┐
   │  runtime (10A)   │  Clock · Breaker · RetryPolicy · FSM · errors
   └────────┬─────────┘
            │
      ┌─────▼──────┐
      │  Go stdlib │
      └────────────┘
```

**One dependency, first-party.** `go list -deps` reports `runtime` and the
standard library. Nothing else.

### It does not import memory, deliberately

The Phase 10D architectural rule is that the tool runtime never modifies memory.
Memory learns what happened by subscribing to events.

A dependency on the memory engine would make that rule a matter of discipline —
the writer would be one call away, and eventually somebody would use it during
an incident. **Without the import, "the tool runtime cannot write memory" is a
fact about the build, not a promise in a document.**

### It does not import conversation either

The conversation engine produces intents; this module consumes a *struct*, not a
package. That keeps the tool runtime usable by anything that can describe what
it wants — a scheduled job, an operator console, a future multi-agent planner —
rather than only by a dialogue.

---

## 3 · Subsystems

| Brief section | Type | File |
|---|---|---|
| 1 · Runtime kernel | `ToolRuntime`, `ToolCoordinator`, `Lifecycle` | `runtime.go`, `registry.go` |
| 1 · Scheduler / queue | `ToolScheduler`, `Class` | `queue.go` |
| 1 · Dispatcher / supervisor | `ExecutionDispatcher`, `ExecutionSupervisor` | `dispatcher.go` |
| 2 · Registry | `Registry`, `Registration`, `Health` | `registry.go` |
| 3 · Contracts | `Contract`, `FieldSpec`, `Effect`, `Tool` | `contract.go`, `value.go` |
| 4 · Discovery | `Discovery`, `Request`, `Candidate` | `discovery.go` |
| 5 · Planner | `Planner`, `Plan`, `Step`, `Binding`, `Condition` | `plan.go`, `intent.go` |
| 6 · Executor | `Executor`, `Invocation`, `ExecutionResult` | `executor.go` |
| 7 · Sandbox | `Budget`, `Sandbox`, `BudgetSandbox`, `Lease` | `sandbox.go` |
| 8 · Permissions | `PermissionEngine`, `Grant`, `Override`, `Decision` | `permission.go` |
| 9 · Retry | `RetrySpec`, `RetryEngine`, `DeadLetterQueue`, `Classify` | `retry.go` |
| 10 · Idempotency | `DeriveKey`, `Ledger`, `LedgerEntry` | `idempotency.go` |
| 11 · Compensation | `Journal`, `Compensator`, `CompensationReport` | `compensation.go` |
| 12 · Events | `Event`, `EventType`, `Publisher`, `EventDispatcher` | `events.go` |
| 13 · Metrics | `Metrics` | `metrics.go` |
| 14 · Audit | `AuditEntry`, `Auditor`, `Trace` | `audit.go` |
| 15 · Streaming | `Chunk`, `StreamSink`, `BufferedSink` | `streaming.go` |
| 16 · Testing | `Harness` and doubles | `harness.go` |

**23 source files, 8,788 lines.**

---

## 4 · Effect classification — the field to read first

`Contract.Effect` is the most consequential field on a contract. It drives
retry safety, idempotency requirements and permission strictness — three
decisions that would otherwise each be configured separately and inconsistently.

| Effect | Changes the world | Auto-retryable | Notes |
|---|---|---|---|
| `EffectRead` | No | Yes | Safe to run twice, safe to run speculatively |
| `EffectIdempotentWrite` | Yes | Yes | Twice with one key = once |
| `EffectWrite` | Yes | Yes, **under a key** | Twice does it twice |
| `EffectIrreversible` | Yes | **Never** | Sent message, placed call, released payment |

**`EffectIrreversible` is never retried automatically, under any circumstances.**
An unanswered call to "send the message" might have sent it, and the only safe
assumption is that it did. The contract validator refuses an irreversible tool
that declares more than one attempt, and the planner refuses a fallback chain
over one — "try the next one" after an unanswered irreversible call means
possibly doing it twice.

An irreversible contract also cannot be `Compensable`: *if it can be undone it
is not irreversible*, and the validator says so.

---

## 5 · Plans are inert data

**Building a plan executes nothing** (INV-TOOL-8). No tool is invoked, no
registry is mutated, no event is published.

That is what makes a plan:

- **reviewable** before it runs — `Plan.Explain()` renders it as a tree;
- **replayable** after it runs — `Plan.Fingerprint()` says whether today's plan
  differs from yesterday's;
- **testable** without a tool in sight — every plan-shape test in this module
  registers contracts with a **nil implementation**.

Five step kinds cover the five shapes the brief requires:

| Kind | Behaviour |
|---|---|
| `StepInvoke` | Calls one tool at one pinned version. **The only kind that executes anything** |
| `StepSequence` | Children in order, stopping at the first failure |
| `StepParallel` | Children concurrently — **waits for all of them even after one fails** |
| `StepConditional` | Child runs only when the condition holds |
| `StepFallback` | Children in order until one succeeds |

### A tree, not a general graph

A DAG would let one step feed two others without re-running, which sounds better
and is worse: the execution order of a general DAG is not obvious from reading
it, and **a plan whose order is not obvious from reading it cannot be reviewed
by the person who has to approve it**. Data dependencies are expressed with
`Binding`, which reads from a shared result map rather than requiring an edge.

### Ordering comes from dependencies, never declaration order

`levelise` is Kahn's algorithm with a **sorted** ready set. Independent requests
become a parallel group; dependent ones become a sequence of groups. That gives
the maximum concurrency the dependencies permit without the caller thinking
about it, and it is deterministic because each level is sorted by ref.

### Versions are pinned at plan time

Resolving again at execution time would let a registry change between planning
and execution silently substitute a different implementation, and **the audit
record would name a tool that never ran** (INV-TOOL-9).

### `FullyCompensable` is answered before anything runs

A caller that learns *after* a partial failure that half of what it did cannot
be undone has learned it too late. `Plan.Mutates()`, `Plan.Irreversible()` and
`Plan.FullyCompensable()` let the decision be made while it is still a decision.

---

## 6 · Execution phase order

```
permission → validate → idempotency → sandbox → invoke
```

Each stage is cheaper or safer than the next, and **two of these were wrong in
the first version**:

| Ordering mistake | Consequence |
|---|---|
| Key claimed before permission | A denied execution blocks the corrected retry |
| Sandbox slot claimed before the ledger | A burst of identical requests is shed for concurrency it was never going to use |

Both were found by tests — ENGINEERING_AUDIT §F3.

`Executor.Execute` is **the only place in this module that calls a `Tool`**.
That is what makes it possible to state with confidence that no tool is ever
called without a permission check, a budget, a deadline and an audit entry:
there is exactly one path.

---

## 7 · Idempotency

**The key is derived, never supplied by a caller.**

A caller-chosen key is a caller-chosen bug: two different requests share a key
and one silently returns the other's result, or one request generates a fresh
key per attempt and deduplication never fires.

```
key = fingerprint( descriptor | actor | scope | canonical(arguments) )
```

| Included | Why |
|---|---|
| Pinned descriptor | A different version may do a different thing with the same arguments |
| Arguments, canonically | Sorted map keys — see §11 |
| Actor | Two subscribers asking the same question are two questions |
| Scope (normally the correlation) | Retries within one turn deduplicate; a later turn does not |

| Excluded | Why |
|---|---|
| Attempt number, execution ID, timestamps | Any of them would make every retry a fresh key |

**Concurrent duplicates share one invocation.** A second execution holding the
same key does not run the tool; it waits on the first and receives its result.
That is what a client retry storm looks like, and it produces one tool call and
N answers — measured at 64 callers → 1 invocation in EXECUTION_EVALUATION §E2.

**Reads are not deduplicated by default.** A stored answer to "is this slot
still free" is exactly the wrong thing to be confident about. `Config.DedupeReads`
turns it on for deployments whose reads are genuinely stable.

**A permission denial releases the key rather than storing a failure.** Storing
it would mean a later attempt with a corrected grant is served the old denial.

### The honest limit

The ledger is **in-memory and per-process**. Two runtime replicas do not share
one, so **exactly-once holds within a replica and at-least-once holds across
them**. Making it cross-replica means backing it with Redis or Aurora — the
declared seam, and Phase 10E's work. "Exactly-once where possible" is the
brief's own phrase and the honest one.

---

## 8 · Compensation

A `Journal` records completed **mutating** work per plan. Read-only steps are
not recorded: there is nothing to undo about a lookup, and recording them would
make a rollback report's success count meaningless.

On failure, `Compensator.Rollback` undoes work in **reverse completion order**,
attempting **every** step even after one fails.

| Rule | Why it is not a preference |
|---|---|
| Reverse order | Later steps may depend on state earlier ones created; undoing the earlier one first can make the later one's compensation impossible |
| Every step attempted | The failure of one rollback says nothing about whether the next would work; stopping guarantees more of the world stays wrong |
| Own context, from `Background` | Compensation is most often needed *because* the execution's context died; inheriting it would mean rollback never runs in the case it exists for |

**A failed compensation replaces the original error.** The original failure is
recoverable by retrying; a failed rollback is not, and surfacing the lesser
problem would let a caller retry into a world it does not understand.

`CompensationReport` distinguishes **skipped** (no compensation was possible)
from **failed** (we tried and it did not work). Those are different facts and
lead to different operator actions.

---

## 9 · The sandbox is a budget, not a jail

**Stated plainly because the name invites the wrong assumption.**

An in-process Go tool runs on the same goroutine scheduler, in the same address
space, with the same file descriptors and network access as the runtime that
invoked it. `BudgetSandbox` enforces exactly three things:

1. Total concurrent slots across the runtime.
2. Concurrent executions per tool.
3. Output bytes per execution, charged **incrementally** — so an unbounded
   stream is caught, not just an oversized final result.

It does **not** enforce memory or CPU, and it cannot. Real isolation requires
the tool to run somewhere the runtime can kill: another process, another
container, another machine. `Sandbox` is the declared seam for that.

Deadlines are enforced by the executor through context cancellation, not here,
because enforcing a deadline requires *cancelling* the work and a sandbox that
only accounts cannot cancel.

**Go cannot kill a goroutine.** A tool that ignores cancellation is *abandoned*
and counted — `ExecutionSupervisor.Abandoned()`. A rising count names a tool
that does not honour cancellation, turning an invisible leak into a bug report
with an owner.

Full treatment: SECURITY_REVIEW §R1.

---

## 10 · Permissions

`PermissionEngine` combines permissions, roles, consent and overrides. It does
**not** know what a "receptionist" may do — the role map is supplied. That
separation is the difference between a policy engine and a policy, and it stops
this module becoming a second, drifting definition of the platform's permission
vocabulary.

Evaluation order, cheapest first: **grant expiry → consent → permissions and
roles → overrides**.

Overrides are consulted **last**, so an override is only ever reached for
something that would otherwise be denied — and therefore always appears in the
audit trail as having actually mattered.

### An override must cover everything missing

Waiving two of three requirements is not a decision anybody made about the
third. A partial override does not allow the call.

### An override must expire and must be attributed

`ExpiresAt` and `AuthorisedBy` are required at construction. An override with no
expiry is a permanent policy change wearing an emergency's clothes; an anonymous
one cannot be reviewed afterwards. Installation is audited at install time, not
only at use, and active overrides appear in `Coordinator.Health()` — an override
nobody notices is an override nobody withdraws.

---

## 11 · Determinism

| Source of nondeterminism | Closed by |
|---|---|
| Wall clock | Everything goes through `rt.Clock` |
| Retry jitter | Seeded, runtime-owned `rand.Rand` — never the global source |
| Map iteration | Sorted: argument keys, plan levels, bindings, sweep actions, overrides |
| Ordering ties | Every comparator falls back to a stable identifier |
| Result ordering | `PlanResult.Steps` sorted by step ID, not completion order |

Measured: 25 runs of the same intent produce **0 plan divergences and 0
step-order divergences** (EXECUTION_EVALUATION §E5).

**The honest exception:** event *interleaving* across a parallel group varies,
because two branches racing is the point of running them in parallel. The plan,
the step set and the outcomes are stable; the order two concurrent branches
publish their events in is not, and the evaluation report records that rather
than pretending otherwise.

---

## 12 · Events

Eight types, topic-named per the frozen `packages/go/eventbus` convention:
`tool.execution.<event>.v1`.

**Events carry identifiers and fingerprints, never payloads** (frozen invariant
I7). There is deliberately no field capable of holding a tool's arguments or
results. The test applied during design: *if this topic were retained forever
and could never be deleted, would that be a compliance failure?* It must be no.

This is also the mechanism behind the rule that the tool runtime never writes
memory: memory subscribes. A consumer needing an actual result asks the runtime
for it, subject to the runtime's access rules — which a Kafka topic does not have.

A failing publisher is **counted and swallowed**: one broken subscriber must not
fail tool executions for everybody else. But the loss is visible, because a
dropped event means a memory update that never happens.

---

## 13 · Invariants

| # | Invariant | Enforced by |
|---|---|---|
| **INV-TOOL-1** | The runtime never writes memory; it publishes events | **Absence of an import** |
| **INV-TOOL-2** | No tool is invoked without a permission decision | `Executor.Execute` — the single invocation path |
| **INV-TOOL-3** | Every mutating execution carries a derived idempotency key | `DeriveKey`, `Ledger.Claim` |
| **INV-TOOL-4** | Every invocation is bounded by a deadline and a budget | `Contract.validate`, `Executor.deadlineContext` |
| **INV-TOOL-5** | Output that violates a contract fails the execution | `Contract.ValidateOutput` |
| **INV-TOOL-6** | Irreversible effects are never retried or fallen back over | `RetrySpec.validate`, `Planner.planRequest` |
| **INV-TOOL-7** | Events and audit records carry fingerprints, not payloads | `Event`, `AuditEntry` — no payload field exists |
| **INV-TOOL-8** | Building a plan executes nothing | `Planner.Plan` |
| **INV-TOOL-9** | A registry change cannot alter a plan already built | Pinned descriptor + copy-on-write snapshot |
| **INV-TOOL-10** | A runtime starts once | `ToolRuntime.Start` |
| **INV-TOOL-11** | Compensation runs in reverse order and attempts every step | `Compensator.Rollback` |
| **INV-TOOL-12** | No global mutable state; two runtimes share nothing | Construction — everything is runtime-owned |

**Most are enforced by absence** — a missing import, a missing field, a missing
constructor. Enforcement by absence cannot be forgotten, misconfigured, or
switched off during an incident.

---

## 14 · Concurrency

| Structure | Strategy |
|---|---|
| `Registry` | **Copy-on-write**; reads take no lock at all |
| `Ledger` | One mutex; per-entry `done` channel for waiters |
| `ToolScheduler` | One mutex + per-waiter channel (context-cancellable, unlike `sync.Cond`) |
| `BudgetSandbox` | One mutex |
| `Metrics` | RWMutex on label maps, atomics per series |
| `EventDispatcher` | Atomic sequence |

The registry's copy-on-write gives a property that matters more than the speed:
**a plan resolved against one snapshot cannot have its tool changed underneath
it mid-execution**, because the snapshot it read is immutable. Registry churn
during a rollout cannot corrupt an in-flight execution, and a stress test
asserts it.

---

## 15 · Testing

| Suite | Count |
|---|---|
| Unit (`toolruntime_test.go`) | **46** |
| Integration, concurrency, stress, failure injection (`integration_test.go`) | **33** |
| Evaluation (`eval_test.go`) | **8** |
| Benchmarks (`bench_test.go`) | **22** |

**87 tests, 12,134 lines total** (8,788 source + 3,346 test). `gofmt` clean ·
`go vet` clean · passes `-count=5 -shuffle=on` · workspace builds and all of
10A, 10B and 10C still pass.

**Not verified: `-race`** — no C toolchain locally. ENGINEERING_AUDIT §A2, now
applying to **four** concurrent modules and blocking.

---

## 16 · Deliberate omissions

Per the brief's DO NOT IMPLEMENT list, verified absent:

| Excluded | Evidence |
|---|---|
| Telephony providers, CRM, calendar, payments | **Not one real adapter.** `Tool` is an interface; the only implementations are named `FakeTool`, `WriteFake`, `CompensatingFake`, `StreamingFake`, `BlockingTool` |
| Real tool adapters | As above |
| LLM prompt templates | None |
| Fraud intelligence | No import, no type |
| Business logic | Capabilities are opaque strings |
| Any orchestration framework | `go.mod` has one first-party require |
| Any vendor tool protocol | No provider named anywhere in the module |

Also absent by design: persistence (`Ledger` is in-memory), a Kafka producer
(`Publisher` is the seam), a Redis cache, `.proto` definitions and Python
bindings — see ENGINEERING_AUDIT §A3, where the gap is flagged as a decision
rather than left as an omission.
