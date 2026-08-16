# AI Runtime Core — Architecture and Runtime Documentation

**Phase 10A** · `packages/go/runtime` · Status: **PROPOSED — awaiting approval**

The execution substrate every AI interaction in the platform runs on, built from
scratch on the Go standard library.

---

## 1 · What this is, and what it is not

| It is | It is not |
|---|---|
| A runtime, in the sense a kernel is a runtime | An agent framework |
| Admission, scheduling, sessions, context, providers, streaming, cancellation | Conversation logic |
| Vendor-agnostic by construction | An SDK wrapper |
| Stdlib-only, zero external dependencies | Built on LangChain, CrewAI, AutoGen, Semantic Kernel or an Agents SDK |

**Explicitly not implemented, per the Phase 10A brief:** conversation logic,
prompt templates, telephony, fraud detection, memory reasoning, tool calling,
agent behaviour. Nothing in the package refers to a caller, a subscriber, a
screening or a verdict.

The one apparent exception is `ModelTier`, which names ADR-0006's four-tier
ladder. It is present because tier is a *routing input the runtime must act on*,
not because the runtime understands why a tier was chosen.

---

## 2 · The zero-dependency constraint

`packages/go/runtime` has **no external dependencies**. This is the same rule
`packages/go/platform` follows, applied for a stronger reason: this module sits
directly on the path between a hostile caller's speech and a language model.

Three consequences, all load-bearing:

1. **The full test suite runs offline** — no network, no broker, no database, no
   model provider. That is why the suite completes in ~1.5 s.
2. **"No framework lock-in" is structural, not aspirational.** There is no
   framework to be locked into.
3. **The supply-chain surface of the most security-sensitive component in the
   platform is exactly the Go standard library.**

Every integration point is an interface defined here and implemented in a
sibling module that services opt into:

| Concern | Interface | Adapter lives in |
|---|---|---|
| Model vendors | `Provider` | `packages/go/provider-*` (Phase 10B) |
| Tracing | `Tracer`, `Span` | `packages/go/telemetry` |
| Metrics export | `Metrics.Snapshot()` | exporter module |
| Session persistence | `SessionManager` + store | `packages/go/redis` |
| Event publication | consumer of `StreamResult` | `packages/go/eventbus` |

---

## 3 · The seventeen subsystems

| # | Subsystem | Type | File |
|---|---|---|---|
| 1 | Runtime Kernel | `Kernel` | `kernel.go` |
| 2 | Runtime Scheduler | `Scheduler` | `scheduler.go` |
| 3 | Session Manager | `SessionManager`, `Session` | `session.go` |
| 4 | Context Manager | `ContextWindow`, `TokenCounter` | `contextwindow.go` |
| 5 | Provider Abstraction | `Provider`, `TokenStream`, `Chunk` | `provider.go` |
| 6 | Model Registry | `ModelRegistry`, `ModelTier` | `model.go` |
| 7 | Prompt Registry | `PromptRegistry` | `prompt.go` |
| 8 | Runtime State Machine | `FSM[S]` | `fsm.go` |
| 9 | Streaming Runtime | `Dispatcher` | `dispatcher.go` |
| 10 | Token Dispatcher | `Dispatcher.Run`, `sinkWriter` | `dispatcher.go` |
| 11 | Runtime Metrics | `Metrics`, `Counter`, `Gauge`, `Histogram` | `metrics.go` |
| 12 | Runtime Logging | `Kernel.Logger` (`log/slog`) | `kernel.go` |
| 13 | Runtime Tracing | `Tracer`, `Span`, `NoopTracer` | `kernel.go` |
| 14 | Runtime Health | `HealthState`, `Kernel.Health` | `kernel.go` |
| 15 | Runtime Configuration | `Config` + per-subsystem configs | `kernel.go` |
| 16 | Runtime Lifecycle | `Kernel.Start` / `Stop` | `kernel.go` |
| 17 | Testing Harness | `Harness`, `FakeProvider`, `FakeClock` | `harness.go`, `clock.go` |

Supporting: `errors.go` (typed errors, `ProviderErrorKind`), `ids.go` (opaque
identifiers), `breaker.go` (circuit breaker, retry policy).

---

## 4 · Request lifecycle

```
  Kernel.Generate(ctx, GenerateSpec, sinks…)
        │
   1 ▸ Scheduler.Admit(class, deadline)
        │    ClassSafety / ClassSystem bypass capacity ......... I11
        │    ClassStandard / ClassInteractive shed above 85%
        │    refused ──▶ ErrShed  (not a failure: the call rings through)
        ▼
   2 ▸ SessionManager.Get + Session.BeginRequest
        │    a draining session refuses here
        ▼
   3 ▸ ContextWindow.Assemble(budget)
        │    local, cheap, may fail on budget — before any network
        ▼
   4 ▸ ModelRegistry.ResolveTier(tier, avoid…)
        │    ModelRegistry.BuildRequest → enforces I3
        ▼
   5 ▸ Breaker.Allow → Provider.Generate
        │    retry (budget-aware) · fallback (bounded hops)
        ▼
   6 ▸ Dispatcher.Run  ◀── returns to caller BEFORE first token
        │    caller holds the dispatcher and may Abort()
        ▼
   7 ▸ finalizers: session complete → scheduler release → metrics
        │
        ▼   Dispatcher.Done closes  ·  Result() valid
```

**Step 6 is the shape that makes barge-in possible.** `Generate` returns as soon
as the provider stream is open, so the caller holds something it can cancel. A
`Generate` that blocked until completion would give the caller nothing.

**Step 7 runs before `Done` closes.** A caller pacing itself on `Done` must not
observe completion while the runtime still holds the scheduler slot — that is a
capacity bug that only appears under load. See ENGINEERING_AUDIT §F2.

---

## 5 · How barge-in meets its 20 ms budget

ADR-0011 fixes barge-in at one frame interval. The mechanism:

```
  Dispatcher.Run
      │
      ├─ reader goroutine ──▶ stream.Recv() [BLOCKS]
      │                            │
      │                       recvCh (buffered 1)
      │                            │
      └─ pump: select {  ◀─────────┘
             case <-abortCh:   ── observed in ONE select iteration
             case <-ctx.Done():
             case <-gapTimer:
             case r := <-recvCh:
         }
```

`TokenStream.Recv` blocks. A pump calling it inline could not observe an abort
until the provider happened to send something — and on a stalled provider, never.
Running `Recv` on its own goroutine and selecting over the delivery channel makes
the abort observable in microseconds instead.

The orphaned `Recv` is not leaked: closing the stream unblocks it, it observes
`readerDone`, and it exits. This is asserted by a goroutine-count test
(`TestDispatcher_NoGoroutineLeakAfterAbort`), not assumed.

**Measured: 5.3 µs against a 20 ms budget** — 0.027% of the allowance.

---

## 6 · Load shedding and Invariant I11

> Under load, shed at admission or downgrade a tier — never skip fraud scoring
> or the safety layer.

Encoded as a scheduling `Class`:

| Class | Sheddable | Under saturation |
|---|---|---|
| `ClassStandard` | **Yes** | Refused first |
| `ClassInteractive` | **Yes** | Refused after standard |
| `ClassSafety` | **No** | Admitted unconditionally, **overshooting `MaxConcurrent` if necessary** |
| `ClassSystem` | **No** | Admitted unconditionally |

`Class.Sheddable()` is a method with no configuration input. There is no flag,
no override parameter and no config field that changes it — because a knob that
can disable a safety guarantee is a guarantee that will be disabled during an
incident by someone trying to restore throughput.

Overshooting the concurrency limit is deliberate and is the lesser harm. The
alternative is queueing safety work behind ordinary work under load, which is
precisely the failure I11 forbids. The overshoot is **measured**
(`runtime_scheduler_overshoot_total`) so the assumption that safety work is a
small fraction of volume is checked rather than assumed.

---

## 7 · Invariant enforcement

Most invariants are enforced by an **absence** — a missing state-machine edge, a
missing field, a missing command. Enforcement by absence cannot be forgotten,
misconfigured, or optimised away by someone who never read this document.

| Invariant | Enforced where | Mechanism | Test |
|---|---|---|---|
| **I3** thinking on tool-calling tiers | `ModelSpec.validate`, `BuildRequest` | Registration refuses; explicit disable refused | `TestModelRegistry_I3_*`, `TestKernel_I3_*` |
| **I11** never skip safety | `Class.Sheddable`, `Scheduler.Admit` | No config path; capacity bypass | `TestScheduler_SafetyClassIsNeverShed` |
| **I6** drain before terminate | `Kernel.Stop`, `Scheduler.Wait` | Ready false → drain → close | `TestKernel_StopDrains` |
| **INV-AI-10** no chain-of-thought leak | `Dispatcher.deliver` | Sink must opt in; default is safe | `TestDispatcher_ThinkingNotDeliveredToUnoptedSink` |
| **INV-AI-12** rollout needs evaluation | `PromptRegistry.Activate` | Refuses empty `EvaluationRef` | `TestPromptRegistry_RefusesActivationWithoutEvaluation` |
| **ADR-0011** barge-in ≤ 20 ms | `Dispatcher.Run` | Preemptible read | `TestDispatcher_AbortPreemptsBlockedProviderRead` |
| **ADR-0006** four-tier ladder | `ModelTier` | Single-step escalation, bounded | `TestModelTier_LadderIsMonotonicAndBounded` |

---

## 8 · No global mutable state

Every subsystem is a struct with explicit dependencies, constructed by `New` and
owned by a `Kernel`. There is no package-level registry, no `init()` side effect
and no singleton.

The one package-level mutable value is `idCounter atomic.Uint64` in `ids.go`. It
is permitted because it is write-only, monotonic, carries no semantics and is
read by nothing. **It is not state; it is entropy.**

Consequences: two kernels run in one process with no interference; the test
suite is `t.Parallel()` throughout; multi-tenancy remains possible later.

---

## 9 · Concurrency contract

Every exported type is safe for concurrent use unless documented otherwise. The
exceptions are `Harness` and its fakes, which are single-test-scoped.

| Structure | Strategy | Why |
|---|---|---|
| `SessionManager` | Sharded map, power-of-two mask | High-churn create/delete plus periodic full scan is `sync.Map`'s worst case and a sharded map's best |
| `Scheduler` | Buffered channel semaphore | Acquisition must be selectable against context and timeout; `sync.Cond` cannot express that |
| `Gauge` | Atomic float64 bits | Written on every admission and release; a mutex there is hot-path contention |
| `Counter`, `Histogram` | RWMutex on the label map, atomics per series | Label sets are created once and incremented forever |
| `ModelRegistry`, `PromptRegistry` | RWMutex, copy on read | Read-heavy, written at boot and rollout only |
| `FSM` | RWMutex, hooks run unlocked | A hook may read the FSM without deadlocking |
| `Dispatcher` | One goroutine per sink per stream | See PERFORMANCE §4 |

---

## 10 · Configuration

Typed, validated once at construction, never re-read. A runtime that reloads
configuration under itself must make every subsystem tolerate its parameters
changing mid-flight — a large amount of complexity for a capability nothing here
needs. **A config change is a deploy.**

Validation aggregates *every* problem rather than failing on the first, matching
`platform.LoadConfig`: an operator correcting a misconfigured deployment needs
the complete list, not one problem per restart cycle.

Defaults are derived from the frozen budget, not from round numbers:

| Setting | Default | Derivation |
|---|---|---|
| `DefaultDeadline` | 2500 ms | ADR-0011 p99 ceiling |
| `Dispatcher.AbortBudget` | 20 ms | ADR-0011 one frame |
| `Retry.MaxAttempts` | 2 | A third cannot fit inside a 900 ms p50 |
| `Breaker.Window` | 10 s | Shorter is jumpy at trough volume, longer is slow at peak |
| `Scheduler.SheddingThreshold` | 0.85 | Headroom for `ClassSafety` must exist, not be hoped for |
| `Session.IdleTTL` | 2 min | Screening sessions are tens of seconds (ADR-0002 §13) |

---

## 11 · Observability

**Metrics.** 30 instruments across scheduler, streaming, sessions, context,
providers and models. Latency histogram buckets are placed around the frozen
budget (0.9 / 1.5 / 2.5 s) rather than on a generic exponential scale, because
generic buckets put only two boundaries in the range we actually care about.
Abort buckets straddle 20 ms so a breach is a visible bucket crossing rather
than an inferred percentile.

`Metrics.Snapshot()` renders everything in one pass; an exporter in a sibling
module turns that into a wire format. **That is what keeps OpenTelemetry out of
the kernel.**

**Logging.** `log/slog`, kernel-scoped. The runtime logs identifiers,
enumerations and durations — never content. `Message.Content` and `Chunk.Text`
carry `SENSITIVE` data on the screening path and appear in no log statement.

**Tracing.** A narrow `Tracer`/`Span` interface, defaulting to `NoopTracer` so
the runtime works with no collector configured. Span attributes are restricted
to identifiers and enumerations for the same reason as logs.

**Health.** `Kernel.Health()` reports readiness, drain state, per-provider
breaker state, utilisation and session count — mapping onto
`platform.Health`'s liveness/readiness split.

---

## 12 · Testing

| Suite | Count | What it proves |
|---|---|---|
| Unit (`runtime_test.go`) | 35 | FSM, breaker, scheduler, model, context, session, metrics, clock, prompt |
| Integration (`integration_test.go`) | 18 | Barge-in, leak, thinking filter, end-to-end, retry, shed, drain, concurrency |
| Benchmarks (`bench_test.go`) | 21 | Every hot path |

Verified: `go vet` clean · `gofmt` clean · **53 tests pass, `-count=5
-shuffle=on`** · benchmarks run clean.

**Not verified locally: `-race`.** No C toolchain on the development machine.
This is a gap and is tracked in ENGINEERING_AUDIT §A2 — the race detector must
run in CI before this module is approved for production.

The harness is exported rather than test-only because every service embedding
this runtime needs it. A harness only the runtime's own tests can use pushes
every consumer into real time and real network.

---

## 13 · Deliberate omissions

| Absent | Why |
|---|---|
| Prompt templating / interpolation | Phase 10A scope excludes prompt templates. The registry stores and serves; it does not render |
| Tool-call execution | Out of scope. The runtime carries `ChunkToolCall`; it does not interpret it |
| Conversation history semantics | The context window manages **size**, not meaning. `Pinned` lets the layer above mark what must survive without the runtime knowing why |
| Provider implementations | Adapters are Phase 10B. Zero-dependency is the point |
| Session persistence | `SessionManager` is in-memory; a Redis-backed store is an adapter |
| Config hot-reload | See §10 |
| A global metrics registry | Would make parallel tests and multi-tenancy impossible |

---

## 14 · Deviations from the frozen record

| Item | Frozen | Phase 10A | Resolution |
|---|---|---|---|
| Go version | Not pinned in `ARCHITECTURE_FREEZE.md §5` (language per plane only); `go.work` said 1.23.0 | Go 1.25+ | **Adopted.** `go.work` raised to 1.25.0, this module declares 1.25.0. Siblings remain 1.23.0 and still build — a workspace directive is a ceiling, not a floor. Verified |
| Python version | **3.12, explicitly pinned** | 3.13 | **Requires a superseding ADR.** Not taken unilaterally |
| Product name | CallScreen | Aegis AI | Not resolved. Module paths, packages and docs remain `callscreen` |
| Phases 5–9 | — | Skipped | No dependency; noted |

---

## 15 · Status

| Aspect | State |
|---|---|
| Seventeen subsystems | **Implemented** |
| Unit + integration tests | **53 passing** |
| Benchmarks | **21, measured** |
| `go vet`, `gofmt` | **Clean** |
| Race detector | **Not run** — see audit §A2 |
| Provider adapters | **Not started** — Phase 10B |
| Production deployment | **Not started** |
