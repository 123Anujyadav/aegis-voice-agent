# Conversation Intelligence Engine — Performance Report

**Phase 10B** · Measured 2026-08-09

Every number here comes from
[`bench_test.go`](../../packages/go/conversation/bench_test.go) on the machine
named below. **Nothing is estimated or extrapolated except where §6 says so.**

---

## 1 · Method and its limits

| | |
|---|---|
| Machine | 11th Gen Intel Core i7-11800H @ 2.30 GHz, 16 logical CPUs |
| OS / arch | `windows/amd64` |
| Toolchain | Go 1.26.5 |
| Command | `go test -run='^$' -bench=. -benchtime=5000x` |
| Classifier | `ScriptedClassifier`, returns instantly |

### What is measured

The engine's **own** cost with an instant classifier. A real classifier is a
model call at 40–200 ms — three to four orders of magnitude larger than
anything below — so benchmarking with one would measure the model and say
nothing about this code.

**Limits, stated plainly:**

- A developer laptop, not a Graviton EKS node. Absolute figures will differ;
  relative costs and allocation counts will not.
- `benchtime=5000x` is a fixed iteration count. Adequate for ranking and for
  allocations; **these are means, and a mean says nothing about a tail.**
- Single process. No Kafka, no Redis, no Aurora, no gRPC.
- Windows timer granularity inflates sub-microsecond figures somewhat.

---

## 2 · Headline result

**One decision cycle: 6.83 µs.**

The engine budgets itself 150 ms per cycle (see
[CONVERSATION_ENGINE §13](CONVERSATION_ENGINE.md)). It uses **0.0046% of its own
budget** and **0.00076% of the ADR-0011 900 ms p50 end-to-end allowance**.

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| **`DecisionCycle`** | **6,830** | 3,896 | 27 |
| `FullConversation` (4 turns, setup + teardown) | 25,330 | 25,370 | 210 |
| `ConcurrentConversations` (parallel) | 18,691 | 21,143 | 178 |

The conversation engine will not be a latency contributor. That was the question
worth answering, and it is answered rather than assumed.

---

## 3 · Subsystem detail

### Decision path

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Planner` | **44.4** | **0** | **0** |
| `ClarificationAssess` | 36.8 | 32 | 1 |
| `PolicyEvaluate` (allow) | 181.4 | 384 | 1 |
| `PolicyDenialPath` (deny) | 183.8 | 408 | 2 |
| `IntentResolve` | 229.2 | 136 | 2 |
| `IntentYesNo` | 388.9 | 272 | 9 |
| `StateTransition` | 755.0 | 160 | 2 |
| `LatencyController` (4 stages) | 600.0 | 256 | 8 |

**The planner is free: 44 ns and zero allocations.** That is the payoff for
making it a pure function of `PlanInput` — no clock, no map iteration, nothing
to allocate. The property that made it exhaustively table-testable also made it
the cheapest stage.

**Denial costs the same as allowance** (184 ns vs 181 ns). This matters: under a
narrow persona like Fraud Shield, denial is the common path, and a policy engine
whose refusal path is expensive degrades exactly where it is most exercised.

### Floor control

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `TurnOverlapArbitration` | **91.6** | 24 | 1 |
| `TurnAcquireRelease` | 290.6 | 494 | 1 |

Overlap arbitration runs on **every frame of overlapping audio**, so it is the
most frequently executed function in the package. At 92 ns it is comfortably
below the cost of the audio frame that triggers it.

### Context

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `ContextLookup` (5-scope precedence walk) | 138.0 | **0** | **0** |
| `ContextSetGet` | 151.3 | 7 | **0** |
| `ContextSnapshot` (32 entries) | 2,219 | 5,294 | 7 |

Lookup is allocation-free across the full precedence walk. Snapshots are the
expensive operation and are taken deliberately, not per turn.

### Other

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `MetricsCounter` (parallel) | 82.3 | 34 | 1 |
| `PersonaSwitch` (incl. standalone construction) | 11,341 | 12,000 | 139 |

`PersonaSwitch` constructs a private registry per iteration — that is what
`NewPersonaRuntime` is for. Engine-owned conversations use the shared registry
and do not pay it; see §4.

---

## 4 · Two optimisations this report caused

Both were invisible in the tests and obvious in the benchmarks.

### 4.1 The "fast path" was the slow path

`classifyYesNo` bypasses the classifier for confirmations — the
highest-stakes, lowest-complexity classification in a conversation. The first
implementation built two `map[string]bool` literals **on every call**:

| | Before | After | Change |
|---|---:|---:|---:|
| `IntentYesNo` ns/op | 1,395 | **388.9** | **−72%** |
| `IntentYesNo` allocs/op | 15 | **9** | **−40%** |

It was **6.6× slower than the general classification it exists to bypass**. The
fix was a `switch` on the token instead of map literals: Go compiles that to a
length-bucketed comparison chain that allocates nothing and needs no
package-level state to avoid the allocation.

**Why this mattered beyond the microseconds:** the alternative fix — hoisting the
maps to package level — would have introduced global mutable state into a module
whose design forbids it. The switch avoided the trade entirely.

### 4.2 Persona registry shared per engine

`BuiltinPersonas()` builds four personas with two maps each. It was called once
per conversation, rebuilding an identical value every time.

| | Before | After | Change |
|---|---:|---:|---:|
| `FullConversation` ns/op | 31,701 | **25,330** | **−20%** |
| `FullConversation` allocs/op | 228 | **210** | −8% |
| `ConcurrentConversations` allocs/op | 196 | **178** | −9% |

**My hypothesis was partly wrong and the measurement corrected it.** From the
standalone `PersonaSwitch` figure (139 allocs) I predicted persona construction
was ~60% of a conversation's allocations; the real saving was 18 allocations,
about 8%. Most of that 139 belongs to `NewPersonaRuntime`'s other work, not to
`BuiltinPersonas` alone. The time saving (−20%) was better than the allocation
saving, which suggests map construction cost rather than allocation count was
the dominant term.

The change required a second, non-obvious fix: `PersonaRuntime.Register` now
does **copy-on-write**, because a shared registry written in place would let one
conversation silently redefine a persona for every concurrent call — a data race
and a correctness failure at once. Sharing state safely is rarely just sharing.

---

## 5 · Capacity implications

Extrapolating from the parallel figure, with §1's caveats:

| Quantity | Figure | Basis |
|---|---|---|
| Decision cycle | 6.83 µs | measured |
| Share of the engine's 150 ms budget | 0.0046% | 6.83 µs / 150 ms |
| Share of ADR-0011 p50 (900 ms) | 0.00076% | measured / frozen budget |
| Full conversation, engine cost | 25.3 µs | measured |
| Theoretical conversations/sec/core | ~53,000 | 1 / 18.7 µs parallel |
| Practical ceiling | **Classifier-bound and provider-bound** | — |

Memory: ~25 KB transient per conversation, plus context (bounded at 256 entries
per scope). At 10,000 concurrent conversations the context bound dominates and
is the figure to size on.

**The engine is not the bottleneck and will not become one.** Concurrency is
limited by the classifier, by model latency, and by the Phase 10A scheduler's
`MaxConcurrent` — in that order.

---

## 6 · What has not been measured

Stated so the gaps are not mistaken for results.

| Not measured | Why | When |
|---|---|---|
| **Race-detector overhead** | No C toolchain locally | CI, before approval |
| **p95 / p99 under contention** | `benchtime` reports means | Load test |
| Real classifier latency | None exists — framework only | Integration |
| Behaviour under sustained overload | Needs a load generator | Pre-production |
| GC pressure over hours | Needs a soak test | Pre-production |
| Context growth in a 40-turn call | Benchmarked at 32 entries | Before raising `MaxEntriesPerScope` |
| Cross-process cost (gRPC, Kafka, Redis) | No adapters yet | Integration |

**The tail matters most.** ADR-0011 budgets p95 and p99; every figure here is a
mean. A load generator producing realistic concurrency must run before this
engine carries production traffic, and its result — not this document — is what
should be trusted about latency under load.
