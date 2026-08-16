# Execution Evaluation Report — Enterprise Tool Calling Runtime

**Phase 10D** · `packages/go/toolruntime` · 2026-08

An engineering audit asks *is this built correctly*. This report asks a
different question: **does the runtime execute the right things, refuse the
right things, and leave a truthful account of both?**

Every figure below is produced by
[`eval_test.go`](../../packages/go/toolruntime/eval_test.go) and reproducible:

```
cd packages/go/toolruntime
go test -run TestEvaluation -v .
```

The suite measures **and asserts**. A report produced by tests that cannot fail
is a press release.

---

## 1 · What can and cannot be evaluated in this phase

**Can be:** whether execution matches the plan · whether duplicates are
suppressed · whether compensation undoes what it can · whether refusals are
actionable · whether the runtime is replayable · whether failures are
attributed · whether the audit trail is complete · what the runtime costs.

**Cannot be:** whether a tool did the right thing in the world. There is not one
real adapter in this module, by design — the brief excludes them. Nothing here
measures whether a booking was correct, only whether the runtime invoked what it
said it would, once, with a record.

That boundary matters for reading §3: these are measures of **governance**, not
of outcomes.

---

## 2 · Corpus

A four-tool catalogue covering every effect class — a read tool, an enrichment
read, a compensable write and a second compensable write — plus per-scenario
tools for failure, streaming and un-compensable cases. All fakes, all named as
such.

---

## 3 · Results

### E1 · Plan/execution fidelity

**Question:** does what runs match what was planned — the same tools, the same
count, no extras?

```
single       shape=single     steps=1 executed=1 fidelity=true
sequential   shape=sequential steps=2 executed=2 fidelity=true
parallel     shape=parallel   steps=2 executed=2 fidelity=true
mixed        shape=mixed      steps=3 executed=3 fidelity=true
plan/execution fidelity: 4 of 4 shapes
```

**4 of 4.** The measure is not "did it succeed" but "did it do exactly what the
plan said". A runtime that quietly invokes a tool the plan did not name is one
whose plans cannot be reviewed — which removes the point of having them.

*`TestEvaluation_EveryPlanShapeExecutesAsPlanned`*

### E2 · Duplicate suppression under a retry storm

**Question:** how many tool invocations do N identical concurrent requests
produce?

```
callers=2   invocations=1 served=2  suppression=50.0%
callers=8   invocations=1 served=8  suppression=87.5%
callers=32  invocations=1 served=32 suppression=96.9%
callers=64  invocations=1 served=64 suppression=98.4%
```

| Callers | Invocations | Served | Suppression |
|---:|---:|---:|---:|
| 2 | **1** | 2 | 50.0% |
| 8 | **1** | 8 | 87.5% |
| 32 | **1** | 32 | 96.9% |
| 64 | **1** | 64 | **98.4%** |

**One invocation at every scale, and nobody goes away without an answer.** The
suppression figure rises with the storm because the invocation count does not
move — which is the shape you want, since a bigger storm is exactly when a
downstream can least afford the extra load.

Note the second column and the third together: this is not shedding. All 64
callers receive the *same* result, with `Replayed = true`.

*`TestEvaluation_DuplicateSuppressionUnderARetryStorm`*

### E3 · Compensation completeness

**Question:** after a plan fails partway, how much of the completed mutating
work is undone?

```
mutating steps=1 compensated=1 skipped=0 failed=0 rate=100% complete=true
mutating steps=2 compensated=2 skipped=0 failed=0 rate=100% complete=true
mutating steps=4 compensated=4 skipped=0 failed=0 rate=100% complete=true
un-compensable completed step: compensated=0 skipped=1 complete=false
```

**100% at every depth**, in reverse completion order, and the report refuses to
claim completeness when a completed step could not be undone.

**What `Complete` does not mean, and this is the honest part of the report.** It
answers *"was everything the runtime knows about undone"*, not *"is the world
back where it started"*. The step that **failed** may have taken partial effect
before failing — a downstream that timed out after committing, say — and **no
runtime can know whether it did.**

That residual is not measurable from inside this module and is not claimed away.
It is the reason `Plan.FullyCompensable()` is answerable *before* execution: the
only reliable defence against a half-changed world is deciding not to start one.

*`TestEvaluation_CompensationUndoesEverythingItCan`*

### E4 · Refusal quality

**Question:** what fraction of refusals carry a sentinel a caller can branch on?

```
no such capability       actionable=true
unhealthy tool           actionable=true
version unsatisfiable    actionable=true
missing permission       actionable=true
missing consent          actionable=true
invalid input            actionable=true
invalid output           actionable=true
oversized input          actionable=true
refusals carrying an actionable sentinel: 8 of 8
```

**8 of 8.** This is the measure that decides whether a caller can do anything
useful with a failure.

| Refusal | What a caller can do about it |
|---|---|
| `ErrNoCapability` | Page the deploy owner — nothing ships this |
| `ErrNoHealthyProvider` | Page the integration owner — it is an outage |
| `ErrVersionUnsatisfiable` | Relax the constraint |
| `ErrPermissionDenied` | Stop, or request the named permissions |
| `ErrConsentRequired` | **Ask for consent** |
| `ErrInvalidInput` | Fix the request |
| `ErrInvalidOutput` | Report the tool |
| `ErrBudgetExceeded` | Send less, or degrade |

A caller receiving a generic failure can only retry or give up. Every one of
these tells it something better.

*`TestEvaluation_EveryRefusalIsNamedAndActionable`*

### E5 · Determinism

**Question:** does the same intent produce the same execution twice?

```
runs=25 plan divergences=0 step-order divergences=0 event-sequence divergences=12
```

| Property | Divergences over 25 runs |
|---|---:|
| Plan fingerprint | **0** |
| Step set and order | **0** |
| Event *interleaving* | 12 |

**Plans and step ordering are byte-stable. Event interleaving is not, and that
is correct.** Two branches of a parallel group racing is the point of running
them in parallel; a runtime that serialised their event publication to look
tidy would have stopped being concurrent.

What must be stable is what an audit depends on — which tools ran, in what
order, with what outcome — and that is stable. The report records the
interleaving variance rather than quietly measuring something easier.

*`TestEvaluation_IdenticalIntentsProduceIdenticalExecutions`*

### E6 · Failure attribution

**Question:** does a failed execution say **where** it failed and **why**, in
machine-readable terms?

```
permission       phase=permission       reason=missing_permission   attributed=true
tool error       phase=invoke           reason=tool_error           attributed=true
invalid output   phase=validate_output  reason=invalid_output       attributed=true
failures carrying a phase and a bounded reason: 3 of 3
```

**3 of 3.** "Something went wrong" is not an operational signal. A phase and a
bounded reason code are — and they are what the metrics, the events and the
dead-letter queue are built from.

The reason vocabulary is **closed** (`shortReason`), so a downstream service's
error message cannot become this platform's metric cardinality.

*`TestEvaluation_EveryFailureIsAttributed`*

### E7 · Audit completeness

**Question:** does every execution that started reach a terminal audit entry?

```
audited executions started=5 terminal=5 orphaned=0
```

**0 orphans** across a mixed workload of reads, writes, failures and parallel
plans.

A started-with-no-terminal entry is the shape of an execution whose fate nobody
can establish afterwards, which is exactly what an audit exists to prevent.

**Caveat, and it is R5 in the security review:** audit writes are best-effort —
a failure is counted and the execution proceeds — and the only auditor in this
module is in-memory and bounded. This measures that the runtime *emits* a
complete trail, not that a durable store *retains* one.

*`TestEvaluation_EveryExecutionLeavesATerminalRecord`*

### E8 · Overhead against the frozen budget

**Question:** what does the runtime itself cost a conversational turn?

```
executions=500 total=10.16ms per-execution=20.323µs budget=900ms share=0.0023%
```

| | |
|---|---:|
| Per execution, end to end through the public API | **20.3 µs** |
| ADR-0011 p50 turn budget | 900 ms |
| **Share** | **0.0023%** |
| Asserted ceiling | 1% |

**430× inside the ceiling.** The engineering consequence is stated in
PERFORMANCE §3: this module should not be optimised further without evidence,
because effort here buys nothing a person on a phone can perceive.

*`TestEvaluation_RuntimeOverheadIsNegligibleAgainstTheBudget`*

---

## 4 · What the runtime is deliberately bad at

An evaluation listing only strengths is not an evaluation.

| Weakness | Consequence | Deliberate? |
|---|---|---|
| **No isolation** | A hostile tool owns the process | Yes — SECURITY_REVIEW R1. Out-of-process sandbox is 10E |
| **Per-process idempotency** | Two replicas can duplicate an `EffectWrite` | Yes — R2. Shared ledger is 10E |
| **Grants are not verified** | A compromised caller grants itself anything | Accepted — R3 |
| **Cannot know if a failed step took effect** | `Complete` means "all we know of", not "the world is clean" | Unavoidable — E3 |
| **No plan serialisation** | A plan can be reviewed in-process but not queued for approval | Yes — audit A6 |
| **Six condition operators** | Real workflows want more | Yes — a condition language becomes a program nobody reviews |
| **Registration is 375 µs** | No config hot-reload | Yes — it buys a lock-free read path |
| **Reads not deduplicated** | Repeated identical lookups hit the tool | Yes — a stored "is the slot free" is confidently wrong |

**The first two will be felt.** Everything else on this list is a boundary
somebody chose; R1 and R2 are gaps that a production deployment closes or lives
with knowingly.

---

## 5 · Readiness

| Consumer | Ready | Needs |
|---|---|---|
| Consumer AI Assistant | **Yes** | Real adapters (10E+) |
| Business AI Receptionist | **Yes** | Real adapters |
| Fraud Intelligence | **Yes** | Capability registration only |
| Telephony Intelligence | **Yes** | Capability registration only |
| Future multi-agent runtime | **Yes** | It consumes a struct, not a package |

**Before production**, in order: `-race` in CI (A2) · out-of-process sandbox
(R1) · shared idempotency ledger (R2) · durable audit store (R5) · confirmation
policy for irreversible plans (R4).

---

## 6 · Assessment

Measured, not asserted: execution matched the plan in 4 of 4 shapes; 64
concurrent identical requests produced 1 tool invocation and 64 answers;
compensation undid 100% of completed mutating work at every depth and refused to
claim completeness when it could not; 8 of 8 refusals carried an actionable
sentinel; 25 runs produced 0 plan and 0 step-order divergences; 3 of 3 failures
carried a phase and a bounded reason; 0 executions were left without a terminal
audit entry; and the runtime's own cost is 0.0023% of the frozen turn budget.

Its limits are the ones the brief drew and two it did not. **It has no real
tools, which is by design. It has no isolation and no cross-replica
deduplication, which is not** — those are the two things standing between this
and a runtime that can be trusted with a third-party tool or a second replica.

The gap between this and production is **isolation, a shared ledger, a durable
audit store and a race-verified concurrency story** — not correctness of the
execution model.
