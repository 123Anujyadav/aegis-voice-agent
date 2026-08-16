# Engineering Audit — Enterprise Tool Calling Runtime

**Phase 10D** · `packages/go/toolruntime` · 2026-08
**Verdict: APPROVE WITH ONE BLOCKING FINDING (A2)**

A self-audit, written to be useful to a reviewer who wants to disagree with it.
Every defect found during construction is recorded with what it actually was
rather than what it looked like afterwards.

---

## 1 · Scope

| | |
|---|---|
| Module | `packages/go/toolruntime` |
| Files | 27 Go files — **23 source, 4 test** |
| Lines | **12,134** — 8,788 source, 3,346 test |
| Dependencies | **1** — `packages/go/runtime` (Phase 10A, frozen) |
| External dependencies | **0** (`go list -deps` shows stdlib only) |
| Tests | **87** — 46 unit, 33 integration/stress/failure, 8 evaluation |
| Benchmarks | 22 |
| `gofmt` | Clean |
| `go vet` | Clean |
| `-count=5 -shuffle=on` | Passes |
| `-race` | **NOT RUN — see A2** |

**Frozen artifacts untouched.** `packages/go/runtime` (10A),
`packages/go/conversation` (10B) and `packages/go/memory` (10C) are unmodified
and their suites still pass. `go.work` gained one line.

---

## 2 · Compliance with the brief

### Prohibited dependencies — verified absent

LangChain Tools · CrewAI Tools · Semantic Kernel Plugins · OpenAI Agents SDK ·
Anthropic Tool SDK · LlamaIndex · AutoGen · any orchestration framework.

`go.mod` has one `require` line, first-party. There is nothing to check beyond
that, and it is checkable in one command.

### Vendor tool protocols — verified absent

No mention of OpenAI function calling, Anthropic tool use or Gemini function
declarations anywhere in the module. The `ToolIntent` names a **capability**,
which is not expressible in any of those wire formats — see TOOL_RUNTIME §1.

### Excluded implementations — verified absent

| Excluded | Evidence |
|---|---|
| Telephony, CRM, calendar, payment integrations | **Not one real adapter.** `Tool` is an interface; every implementation is `FakeTool`, `WriteFake`, `CompensatingFake`, `StreamingFake` or `BlockingTool`, all in `harness.go` and named as doubles |
| Real tool adapters | As above |
| LLM prompt templates | None |
| Fraud intelligence | No import, no type |
| Business logic | Capabilities are opaque strings the runtime never interprets |

### Architectural rules — verified

| Rule | Enforcement |
|---|---|
| Conversation engine never executes tools | It emits a struct; it does not import this module |
| Memory engine never executes tools | No import either way |
| **Tool runtime never modifies memory** | **No import of `packages/go/memory`.** A fact about the build |
| Memory updates only through events | `Event` is the only outward channel |

### Required stack

Go 1.25+ ✓ (`go.work` 1.25.0, toolchain 1.26.5). Provider agnostic ✓. Built from
scratch ✓.

Python 3.12, protobuf, gRPC, Kafka, Redis and Aurora are named in the brief's
stack. **This phase delivers the Go runtime and the event contract only.** Topic
names follow the frozen `eventbus` convention; `Publisher`, `Auditor`, `Sandbox`
and the ledger's persistence are the declared seams. Flagged as **A3** rather
than silently scoped out.

---

## 3 · Defects found and fixed during construction

Five. Recorded because a phase report claiming none would be a report nobody
should trust.

### F1 · Zero meant both "unset" and "none" — retry backoff

**Severity: high. Found by a hung test suite, fixed.**

`RetrySpec.withDefaults` treated `InitialBackoff: 0` as "unset, take the
runtime default". A caller asking for *no* delay silently got 50 ms.

In production that is harmless. On a fake clock it is fatal: nothing advances
the clock, the retry sleeps forever, and **the test suite hangs instead of
failing**. The first full integration run timed out at 120 s with three
goroutines parked in `RetryEngine.Wait`.

Fixed with an explicit `NoBackoff bool`, and the runtime's default is inherited
so a deployment or a harness can turn delays off globally.

> Zero cannot mean both "unset" and "none", and `withDefaults` has to pick one.

**This is the same defect Phase 10C found in `FailAfter: -1`**, with the same
resolution. Two phases, two independent occurrences of one anti-pattern:
overloading a numeric zero with a sentinel meaning.

**Why it mattered beyond the test:** a hang is the worst failure mode a suite
can have, because it produces no signal — just a slow build that somebody
eventually cancels.

### F2 · `ctx.Err()` under `WithCancelCause` erased every timeout

**Severity: high. Found by test, fixed.**

`deadlineContext` cancels with `cancel(context.DeadlineExceeded)`. Under
`context.WithCancelCause`, **`ctx.Err()` returns `context.Canceled` regardless
of the cause** — the cause is only visible through `context.Cause`.

The executor read `ctx.Err()`, so every timeout was classified as an anonymous
cancellation: no `ErrTimeout`, no `EventTimedOut`, no `TimedOut` metric, and a
retry classifier that treats cancellation as non-retryable silently stopped
retrying timeouts.

Fixed by reading `context.Cause(attemptCtx)`.

**Why it mattered:** three different operator responses — tune the timeout, look
at who cancelled, look at the tool — collapsed into one, and the one that
survived was the least actionable.

### F3 · Sandbox admission ran before deduplication

**Severity: medium. Found by a stress test, fixed.**

The documented phase order was permission → sandbox → idempotency, justified as
"a shed execution does not hold a key it will not use".

The consequence, surfaced by twenty-four concurrent identical requests: **most
of them were refused for per-tool concurrency they were never going to use.** A
duplicate served from the ledger costs no tool call, no connection and no
downstream load; shedding it throws away an answer the runtime already has.

Reordered to permission → validate → idempotency → sandbox → invoke, and the
duplicate-handling loop was rewritten with an explicit bounded retry rather than
a fall-through that could execute with a nil claim.

**Why it mattered:** the ledger's entire purpose is to make a retry storm cheap.
Shedding the storm before consulting the ledger inverted that.

### F4 · The idempotency ledger was quadratic on the claim path

**Severity: medium (performance). Found by benchmark, fixed.**

`Claim` ran a full expiry sweep, and overflow eviction rebuilt the whole queue —
which, once the ledger sits at its cap in steady state, is every claim.

| | ns/op |
|---|---:|
| Original | 88,263 |
| After partial fix | 133,773 |
| Final | **5,801** |

**The partial fix made it worse**, which is worth recording: removing the expiry
walk left the overflow rebuild as the sole cost and it then ran more often.
Stopping after the first change would have shipped a regression.

Details and the knock-on effect on `ExecuteMutating` (130 µs → 22.7 µs) in
PERFORMANCE §4.

### F5 · Discovery cloned every candidate before truncating

**Severity: low (performance). Found by benchmark, fixed.**

`Resolve` built a full `Registration` per match, cloned each, then discarded all
but three — 66 KB of garbage to answer a question about three tools.

Fixed by filtering on descriptors and materialising only the survivors:
18.3 µs → 4.0 µs, 66 KB → 7.4 KB.

**This is the same mistake Phase 10C made in its retrieval path.** The lesson
recorded there — filter on metadata, materialise the survivors — was not applied
here until a benchmark said so, which is a finding about how the lessons travel
between phases rather than about this code.

---

## 4 · Open findings

### A1 · Fourth copy of the metrics primitives — accepted

**Severity: low. Not fixed. Third recurrence of the same finding.**

`runtime.Metrics` (10A) exports its types but keeps its constructors unexported,
so it is closed for extension. Phases 10B, 10C and now 10D each carry their own
counter/gauge/histogram plumbing — **four copies in the platform.**

Not fixed because 10A is frozen. The correct fix is a superseding ADR exporting
`runtime.NewCounter` and friends, then deleting three duplicates.

**This is now the third phase in a row to ship around it, and each one makes the
eventual correction larger.** It should be the first item of Phase 10E rather
than the first item of Phase 10F.

### A2 · Race detector never run — **BLOCKING**

**Severity: high. Not fixed. Cannot be fixed on this machine.**

`go test -race` requires cgo, cgo requires a C toolchain, and there is none here.
**This module has never been run under the race detector.**

That now applies to **four** concurrent modules, and 10D is the most
concurrency-dense yet: a copy-on-write registry read without any lock, a ledger
with per-entry wakeup channels, a scheduler passing slots between goroutines, a
supervisor draining abandoned channels, parallel plan branches sharing a result
map, and a compensator running on its own context while the execution's is dead.

**What exists instead:** concurrent-plan, concurrent-duplicate,
registry-churn-during-execution and overload tests, passing at
`-count=5 -shuffle=on`. Two of the five defects above were concurrency-adjacent
and both were found by reading or by a stress test, which argues for the finding
rather than against it.

**Required before production:** one CI job on Linux with cgo enabled running
`go test -race -count=5 ./...` across all four modules. Until then the strongest
honest claim is *"no data race was observed"*.

### A3 · Phase delivers the Go runtime only

**Severity: informational.** Python 3.12 utilities, `.proto` definitions, the
gRPC service, the Kafka producer, the Redis-backed ledger and the Aurora audit
store are named in the brief's stack and are **not** in this deliverable.

The seams are declared and exercised by doubles: `Publisher`, `Auditor`,
`Sandbox`, and the `Ledger`'s in-memory implementation. Flagged so the gap is a
decision rather than an omission.

### A4 · Idempotency is per-process

**Severity: medium, by design, stated in the code.**

The ledger is in-memory. Two runtime replicas do not share one, so
**exactly-once holds within a replica and at-least-once across them.**

For `EffectIdempotentWrite` tools that is harmless — the downstream deduplicates
on the key the runtime passes through. For `EffectWrite` tools it means a
cross-replica duplicate can do the thing twice.

Mitigations that exist today: the `Invocation` carries the key so a tool can
pass it downstream; correlation-scoped keys mean duplicates only arise from
genuine cross-replica retries. **The fix is a shared ledger, and it is Phase
10E's.** The brief's own phrase — "exactly-once where possible" — is the honest
description of what ships.

### A5 · The sandbox is not isolation

**Severity: high, by design, and the top item of SECURITY_REVIEW.**

`BudgetSandbox` bounds concurrency and output bytes. It cannot bound memory or
CPU, and it cannot stop a hostile in-process tool doing anything the runtime
process can do. See SECURITY_REVIEW §R1. Named here so the audit and the
security review agree rather than each assuming the other covered it.

### A6 · `Plan` is not serialisable out of the box

**Severity: low.** A plan is inert data and is designed to be reviewable, but
there is no marshalling in this module — `Value` has unexported fields and no
codec. A plan can be rendered (`Explain`) and compared (`Fingerprint`) but not
round-tripped through a queue or an approval UI.

Deliberate for 10D: a codec would mean choosing a wire format, and the wire
format is protobuf, which is A3's scope. Recorded so nobody assumes a plan can
be persisted today.

---

## 5 · Design decisions a reviewer should challenge

| Decision | The counter-argument |
|---|---|
| **Intents name capabilities, not tools** | A caller sometimes knows exactly which tool it wants. Rebuttal: `PreferTool` expresses that as a preference without giving up resolution, health and fallback |
| **Plan is a tree, not a DAG** | A DAG expresses shared sub-results without re-running. Rebuttal: a plan whose order is not obvious from reading it cannot be reviewed, and `Binding` covers the data case |
| **Six condition operators, no more** | Real workflows need more. Rebuttal: they need a *tool*, which is testable and owned; a condition language becomes a program nobody reviews |
| **Reads not deduplicated by default** | Wasted downstream calls. Rebuttal: a stored answer to "is this slot free" is confidently wrong; `DedupeReads` is one line for deployments where reads are stable |
| **Parallel waits for every branch** | Slower failure. Rebuttal: a cancelled mutating sibling leaves state nobody can determine |
| **Compensation replaces the original error** | Hides the root cause. Rebuttal: the report carries both; the *error* must be the unrecoverable one so a caller does not retry into a world it misunderstands |
| **Registry copy-on-write** | 375 µs registrations. Rebuttal: lock-free reads plus in-flight plan safety, and registrations happen on deploy |
| **Grants are passed in, not fetched** | The caller can lie. Rebuttal: fetching would put an availability dependency on Identity in the middle of a call, and the caller is inside the trust boundary — SECURITY_REVIEW §R3 |

---

## 6 · Invariant enforcement

| # | Invariant | Enforced at | Test |
|---|---|---|---|
| INV-TOOL-1 | Runtime never writes memory | **Absence of an import** | Structural — `go list -deps` |
| INV-TOOL-2 | No invocation without a permission decision | `Executor.Execute` | `TestEvaluation_EveryRefusalIsNamedAndActionable` |
| INV-TOOL-3 | Mutating executions carry a derived key | `DeriveKey`, `Ledger.Claim` | `TestIdempotency_KeyDependsOnWhatMattersAndNothingElse` |
| INV-TOOL-4 | Every invocation is bounded | `Contract.validate`, `deadlineContext` | `TestIntegration_TimeoutIsDrivenByTheInjectedClock` |
| INV-TOOL-5 | Contract-violating output fails the execution | `Contract.ValidateOutput` | `TestIntegration_ContractViolationIsNotRetried` |
| INV-TOOL-6 | Irreversible effects never retried or fallen back over | `RetrySpec.validate`, `Planner.planRequest` | `TestPlan_FallbackOverIrreversibleIsRefused` |
| INV-TOOL-7 | Events and audit carry fingerprints, not payloads | No payload field exists | `TestEvents_CarryNoPayload` |
| INV-TOOL-8 | Building a plan executes nothing | `Planner.Plan` | `TestPlan_BuildingAPlanExecutesNothing` |
| INV-TOOL-9 | Registry change cannot alter a built plan | Pinned descriptor + COW | `TestPlan_PinsVersionAtPlanTime`, `TestStress_RegistryChurnDuringExecution` |
| INV-TOOL-10 | A runtime starts once | `ToolRuntime.Start` | `TestRuntime_StartsOnce` |
| INV-TOOL-11 | Compensation is reverse-order and exhaustive | `Compensator.Rollback` | `TestIntegration_FailedPlanRollsBackInReverseOrder` |
| INV-TOOL-12 | No global mutable state | Construction | `TestRuntime_TwoRuntimesShareNothing` |

**Eight of twelve are enforced by absence or by construction** — a missing
import, a missing field, a missing constructor, a single invocation path.
Enforcement by absence cannot be forgotten, misconfigured, or switched off
during an incident.

---

## 7 · Test quality

| Property | Assessment |
|---|---|
| Behaviour vs implementation | Tests assert observable outcomes; the two exceptions (`lifecycleTransitions`, `canonicalBytes`) are tests *about* those structures |
| Failure injection | Tool error, tool panic, invalid output, audit failure, publisher failure, compensate failure, queue full, budget exceeded, circuit open, cancellation, abandonment |
| Determinism asserted | Plan fingerprint over 100 runs, discovery order over 50, step order, metric snapshot |
| Concurrency | Concurrent plans, concurrent duplicates, registry churn during execution, overload shedding, parallel branch overlap |
| Clock | Every time-dependent test uses `FakeClock`; **no test sleeps on real time except two deliberate 2–20 ms waits to establish overlap** |
| Flakiness | None observed at `-count=5 -shuffle=on` after F1 |

**Gap: no property-based or fuzz testing.** `Value.canonical` is an excellent
fuzz target — the property "distinct values never share an encoding" is exactly
what a fuzzer checks cheaply, and a collision there would silently break
idempotency. Recommended for 10E; not blocking.

**Gap: no benchmark regression gate.** F4 and F5 were both found by reading
benchmark output by hand. A CI gate on `ns/op` and `allocs/op` would have caught
them automatically, and would catch the next one.

**Gap: the panic path is tested once.** `TestFailure_PanickingToolBecomesAFailedExecution`
covers a panic in `Invoke`. A panic inside `Compensate` is **not** covered, and
that path runs on its own goroutine-free call stack inside `Rollback` — a panic
there would take the process down. Should be fixed in 10E.

---

## 8 · Verdict

**APPROVE WITH ONE BLOCKING FINDING.**

The runtime meets the brief: from scratch, provider agnostic, no orchestration
framework, no vendor protocol, no real adapters. The architectural rules hold
structurally rather than by convention — the tool runtime cannot write memory
because it cannot see memory. Plans are inert, versions are pinned, deduplication
is derived rather than trusted, compensation is reverse-order and exhaustive, and
every refusal carries a sentinel a caller can act on. Overhead is 0.0023% of the
frozen turn budget.

**A2 blocks production, not approval of the phase.** Four concurrent modules have
now been built without ever running the race detector, and this is the most
concurrency-dense of them. That must be a CI job before any of this places a
real call.

Second priority: **A4** (per-process idempotency), because it is the finding most
likely to cause a visible incident — a duplicate booking across two replicas —
and the one a reader is most likely to assume is already handled.

### Handover to Phase 10E

| Item | Action |
|---|---|
| **A2** | CI job: Linux, cgo, `-race -count=5` across all four modules |
| **A1** | Superseding ADR exporting `runtime` metric constructors; delete three duplicate sets |
| **A4** | Redis- or Aurora-backed ledger for cross-replica exactly-once |
| **A5 / R1** | Out-of-process `Sandbox` implementation |
| **A3** | `.proto`, gRPC service, Kafka producer, Aurora audit store, Python utilities |
| **A6** | Plan serialisation, once protobuf lands |
| §7 gaps | Fuzz `Value.canonical`; benchmark regression gate; cover a panic in `Compensate` |
