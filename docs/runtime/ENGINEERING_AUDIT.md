# AI Runtime Core — Engineering Audit

**Phase 10A** · `packages/go/runtime` · Audited 2026-08-08

An honest account of what was built, what was verified, what was found and
fixed, and what remains open. Written to be useful in review, not to look good.

---

## 1 · Verification status

| Gate | Result | Evidence |
|---|---|---|
| Compiles | ✅ | `go build`, standalone and in workspace |
| `go vet` | ✅ clean | no findings |
| `gofmt` | ✅ clean | `gofmt -l` empty |
| Unit tests | ✅ **35 passing** | `runtime_test.go` |
| Integration tests | ✅ **18 passing** | `integration_test.go` |
| Repeat runs | ✅ `-count=5 -shuffle=on` | no flakes |
| Benchmarks | ✅ 21 run clean | `bench_test.go` |
| **Race detector** | ❌ **NOT RUN** | see §A2 — blocking |
| Workspace intact | ✅ | siblings still build after `go.work` bump |
| External dependencies | ✅ **zero** | `go.mod` has no `require` |

**53 tests. One blocking gap.**

---

## 2 · Open findings

### A1 — Python 3.13 requires a superseding ADR · **Severity: process, blocking for the Python tier**

`ARCHITECTURE_FREEZE.md §5` pins **Python 3.12** explicitly. Phase 10A specifies
3.13. The freeze's own §6 provides the route — a superseding ADR with
architecture-team approval — and that route was not taken here because it is not
a decision one package may make for the platform.

**No Python was written in this phase**, so nothing is blocked today. It becomes
blocking the moment a Python provider adapter is built.

*Note the asymmetry:* the Go version was **not** pinned in the freeze (§5 names
the language per plane, not the version), so raising `go.work` to 1.25.0 is not
a freeze violation. Python 3.12 *is* pinned. Same brief, different answers,
because the frozen record says different things about them.

**Owner:** Platform · **Action:** ADR superseding §5's Python row, or stay on 3.12.

### A2 — Race detector not run · **Severity: HIGH, blocking approval**

`go test -race` requires cgo, and no C toolchain exists on the development
machine. For a package whose entire purpose is concurrent execution — sharded
maps, atomics, a semaphore, goroutine-per-sink streaming, lock-free gauges —
**an unraced test suite is not sufficient evidence of correctness.**

Repeat runs (`-count=5 -shuffle=on`) found no flakes, but that is a much weaker
signal: the race detector finds unsynchronised access that has not yet
manifested, which is exactly the class of bug that appears first in production.

**Owner:** Platform · **Action:** run `-race` in CI (Linux, cgo available) before
approval. Treat any finding as blocking.

### A3 — Session sweep is O(n) · **Severity: LOW, monitor**

`SessionManager.sweep` scans every session. Measured at **1.14 ms for 10,000**
sessions; `MaxSessions` defaults to 100,000, which extrapolates to ~11 ms per
sweep. The scan holds one shard lock at a time, so no single lookup waits the
full duration, but the cost is real and grows linearly.

**Action:** re-measure before raising `MaxSessions`. If sweeps exceed ~5 ms,
replace the scan with a time-ordered index. Not worth the complexity today.

### A4 — `MaxConcurrent` default is a guess · **Severity: LOW, expected**

`NumCPU × 32` is a starting point derived from "model calls are I/O-bound", not
from measurement. Every other number in the config is derived from a frozen
budget; this one is not, and the code says so.

**Action:** re-baseline against production load, as ADR-0011 re-baselines the
latency budget at 30 days.

### A5 — `HeuristicTokenCounter` is not a tokeniser · **Severity: LOW, by design**

Token counts are estimates. The ratios (3.5 bytes/token ASCII, 1.8 non-ASCII)
are calibrated conservatively and Indic-first, but they are not a BPE tokeniser
and will diverge from any provider's real count.

Consequence is bounded: the counter **over-counts**, so the failure mode is
trimming context slightly early — costing a little context and no time. The
opposite error, under-counting, causes a provider rejection *after* the latency
is spent.

**Action:** provider adapters carrying a real tokeniser should call
`ContextWindow.SetTokenCounter`. The seam exists.

### A6 — Product naming unresolved · **Severity: cosmetic**

Phase 10A says "Aegis AI"; the entire frozen record, every module path and every
package says "CallScreen". Module paths were left as `callscreen`, because
renaming Go module paths across a workspace is a mechanical but wide change that
should be one deliberate commit, not a side effect.

**Action:** product decision, then a single rename commit.

---

## 3 · Findings found and fixed during this phase

Recorded because how a defect was found is evidence about the test suite.

### F1 — Fake clock vs. `context.WithDeadline` · **Found by: integration tests**

`Kernel.Generate` derived a deadline from the injected `Clock` and then passed
it to `context.WithDeadline`, which schedules against **real wall-clock time**.
Under `FakeClock` — whose epoch is a fixed 2026-01-01 — every request expired
immediately.

This is not merely a test artefact. It means every deadline in the runtime was
only accidentally correct: the abstraction existed, and one call site silently
bypassed it.

**Fix:** `Kernel.deadlineContext` schedules cancellation on the kernel's clock
and delivers expiry as a `context.Cause`, so budget exhaustion stays
distinguishable from the caller going away. `Dispatcher` reads `context.Cause`
rather than `ctx.Err`.

### F2 — `Result()` returned before cleanup · **Found by: `-count=10 -shuffle=on`**

`Dispatcher.Done` closed when `Run` returned, but the kernel released the
scheduler slot and closed the session request *after* that, in a deferred call.
A caller pacing itself on `Done` would over-admit by however many generations
were mid-cleanup.

Passed at `-count=1`. Failed reliably at `-count=10`. **A single-run suite would
have shipped this**, and it would have appeared only under load.

**Fix:** `Dispatcher.OnComplete` finalizers run before `Done` closes; the kernel
releases inside a finalizer.

### F3 — Deadlock introduced by the streaming optimisation · **Found by: integration tests**

Replacing per-write goroutines with one writer per sink (PERFORMANCE §4)
introduced `stop()`, which waited unconditionally for the writer to drain. A
sink blocked inside `Write` — the exact pathological consumer the design defends
against — made `stop()` hang forever, converting one bad consumer into a hung
stream.

**Fix:** bounded wait; on timeout the goroutine is abandoned and exits when
`Write` returns. `Sink.Close` is documented as safe concurrently with `Write`.

**Worth noting:** a performance optimisation introduced a hang, and the test that
caught it (`TestDispatcher_SlowSinkIsDetachedNotBlocking`) existed only because
the slow-sink case was treated as a first-class requirement rather than an edge
case.

---

## 4 · Requirements conformance

| Requirement | Status | Evidence |
|---|---|---|
| No agent framework | ✅ | `go.mod` has zero `require` directives |
| Built from scratch | ✅ | stdlib only |
| Production ready | ⚠️ | blocked on A2 (race) and Phase 10B adapters |
| Event driven | ✅ | streaming chunks, finalizers, breaker callbacks, expiry hooks |
| Cloud native | ✅ | readiness/liveness split, drain on stop, no local state |
| Provider agnostic | ✅ | no vendor import; `Provider` is the only coupling |
| Streaming first | ✅ | `TokenStream` is primary; `Collect` is the derived case |
| Horizontally scalable | ✅ | no shared state between kernels; sessions are instance-local by design |
| Thread safe | ⚠️ | designed for it, **unverified without `-race`** (A2) |
| Fault tolerant | ✅ | breaker, budget-aware retry, bounded fallback, sink detach, stall detection |
| No global mutable state | ✅ | one write-only atomic counter (`ids.go`), documented |
| No framework lock-in | ✅ | every integration point is an interface |

**Two ⚠️. Both are honest.** "Production ready" cannot be claimed while the race
detector has not run, and "thread safe" is a design property that `-race` exists
to verify.

---

## 5 · Scope conformance

The brief forbade seven things. Verified absent:

| Forbidden | Verified |
|---|---|
| Conversation logic | No type refers to a turn, a speaker role beyond transport, or a dialogue |
| Prompt templates | `PromptRegistry` stores and serves; no interpolation exists |
| Telephony logic | No import, no type, no reference |
| Fraud detection | None. `ClassSafety` reserves capacity for it without knowing what it is |
| Memory reasoning | None. `ContextWindow` manages tokens, not meaning |
| Tool calling | `ChunkToolCall` is carried as an opaque string, never parsed or executed |
| Agent behaviour | None |

The single boundary crossing is `ModelTier`, justified in RUNTIME §1 and
`doc.go`: tier is a routing input the runtime must act on.

---

## 6 · Code quality observations

**Strengths.** Zero dependencies. Every invariant enforced structurally rather
than by convention — a missing state-machine edge, a missing command, a missing
field — so it cannot be forgotten. Errors classified once at the vendor boundary
rather than parsed at call sites. Configuration validated in aggregate.

**Weaknesses, stated plainly:**

- **`kernel.go` is doing a lot.** Config, tracing interfaces, provider registry,
  health and generation orchestration in one file. It is cohesive but it is the
  first file that will need splitting, likely at the provider registry.
- **`Kernel.openStream` has meaningful branching complexity** — retry, fallback,
  breaker and hop-limit interleave. It is the most likely place for a subtle
  bug and it deserves the most review attention. A table-driven test over
  failure sequences would be worth adding.
- **`FakeClock.BlockUntil` busy-waits.** Bounded by the test timeout and used in
  one test, but it is the only spin in the package.
- **The `runtime` package name shadows stdlib `runtime`**, requiring a
  `goruntime` alias in two files. Mildly unfortunate; the name is right for what
  it is.

---

## 7 · Recommendation

**Do not approve for production until A2 is closed.** Everything else is either
tracked, bounded, or a Phase 10B dependency.

For approval of Phase 10A as a *design and implementation milestone*, the
material questions are:

1. Is the zero-dependency constraint accepted as permanent for this module?
2. Is the `ClassSafety` capacity overshoot the right reading of I11? It
   deliberately exceeds `MaxConcurrent` rather than queueing safety work.
3. Is Python 3.13 worth a superseding ADR, or does the Python tier stay on 3.12?
4. Is "Aegis AI" a rename, and if so, when?

| Aspect | Status |
|---|---|
| Implementation | **Proposed** |
| Test coverage of invariants | **Proposed** |
| Race verification | **Blocked** |
| Production readiness | **Not claimed** |
