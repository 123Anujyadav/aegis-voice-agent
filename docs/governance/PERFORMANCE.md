# Performance Report — Safety, Policy & Governance Engine

**Phase 10E** · `packages/go/governance` · 2026-08

---

## 1 · Method

| | |
|---|---|
| CPU | 11th Gen Intel Core i7-11800H @ 2.30 GHz |
| Logical CPUs | 16 |
| Platform | windows/amd64 |
| Go | 1.26.5 |
| Command | `go test -run XXX -bench=. -benchmem -benchtime=300ms .` |
| Benchmarks | 20, in `bench_test.go` |

**Why these matter more than the other phases'.** Evaluation is a pure
in-memory function — no network, no broker, no store — so these numbers are the
whole cost. But this engine sits on the critical path of **every action the
platform takes**, so its overhead is multiplied by however many decisions a turn
makes. Unlike 10C and 10D, "negligible" here has to be checked rather than
assumed.

Single-shot runs on a laptop with background load. Treat differences under ~5%
as noise.

---

## 2 · Results

### Decisions

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `DecideSmall` (12 policies) | 9,612 | 8,728 | 53 |
| `DecideLarge` (200 policies) | 96,573 | 96,654 | 242 |
| `DecideParallel` (50 policies) | 7,524→**13,356** | 26,766 | 92 |
| `EvaluatePure` (12 policies) | **7,582** | 6,192 | 36 |
| `DecideWithConsent` | 7,964 | 4,711 | 50 |
| `DecideWithRisk` (3 signals) | 17,762 | 12,921 | 85 |

**The pure evaluator is 79% of a decision.** The remaining 2 µs is validation,
fingerprinting, metrics, audit and events combined — the machinery around the
decision costs less than the decision.

**Risk aggregation nearly doubles the cost** (9.6 → 17.8 µs). Three signals cost
8 µs, which is dominated by sorting and the explanation string. It runs only when
signals are present.

### Components

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `SnapshotLoad` | **0.63** | **0** | **0** |
| `ConditionMatch` | 49.1 | **0** | **0** |
| `ConditionMatchRoleSet` | 89.3 | **0** | **0** |
| `ConsentCheck` | 108.3 | **0** | **0** |
| `ValidatorCheck` | 233.2 | **0** | **0** |
| `EventDispatch` | 54.5 | **0** | **0** |
| `AuditRecord` | 611.2 | 1,322 | 0 |
| `ConsentGrant` | 1,752 | 810 | 7 |
| `RiskAggregate` (4 signals) | 1,508 | 1,184 | 11 |
| `RequestFingerprint` | 1,827 | 1,120 | 17 |
| `EscalationRaiseResolve` | 3,849 | 5,853 | 13 |
| `DecisionExplain` | 12,015 | 5,171 | 58 |

**Snapshot acquisition is 0.63 ns and allocation-free** — an atomic pointer load.
That is the copy-on-write registry paying off: every decision reads the entire
policy set without taking a lock.

**Condition matching is allocation-free at 49 ns**, which is what makes a
200-policy registry viable at all: a decision against 200 policies performs
roughly 600 condition evaluations.

**Consent checks are allocation-free at 108 ns.** They happen per consent
obligation per decision, so this being cheap is what lets the engine resolve
consent inline rather than caching it — and caching is what would let a
revocation take five minutes to bite.

### Write paths

| Benchmark | ns/op | B/op | allocs/op | Scale |
|---|---:|---:|---:|---|
| `PolicyRegister` | 257,872 | 113,584 | 1,178 | 50 policies |
| `ConflictsIn` | 135,470 | **0** | **0** | 200 policies |

**Registration is expensive and that is the trade.** Copy-on-write rebuilds the
policy map, the per-scope index and each scope's priority ordering on every
write. Reads then take no lock at all — and reads happen on every action while
registrations happen on deploy.

258 µs per policy means a 200-policy boot costs ~52 ms, once.

**`ConflictsIn` is allocation-free** and runs at boot and after every policy
load, never on the request path.

---

## 3 · Scaling with policy count

Measured end to end through `Decide`
(`TestEvaluation_ScalingWithPolicyCount`):

| Policies | Per decision | Marginal per policy |
|---:|---:|---:|
| 10 | 11.99 µs | 1.20 µs |
| 50 | 29.58 µs | 592 ns |
| 100 | 48.20 µs | 482 ns |
| 200 | 80.74 µs | 403 ns |

**Linear, with a fixed overhead of ~7 µs.** The marginal cost falls with size
because the fixed part amortises; the true marginal cost is roughly **370 ns per
policy**.

Extrapolated: 1,000 policies ≈ 380 µs per decision. At ten decisions per turn
that is 3.8 ms, or 0.4% of the turn budget — still acceptable, and the point at
which the optimisation in §5 becomes worth taking.

---

## 4 · Against the frozen latency budget

ADR-0011: **p50 ≤ 900 ms** for a whole conversational turn.

Measured over 500 decisions against the baseline plus a tenant policy set
(8 policies), with a deliberately pessimistic ten decisions per turn:

| | |
|---|---:|
| Per decision | **12.3 µs** |
| Decisions per turn (assumed) | 10 |
| Per turn | **123 µs** |
| Turn budget | 900 ms |
| **Share** | **0.014%** |

**Governance is not a latency risk at this policy count.** The engine is 70×
inside its own 1% ceiling.

The honest caveat: this is 8 policies. At 200 the per-turn cost is 807 µs, or
0.09% — still comfortable. The number to watch is the policy count, not the
decision count.

---

## 5 · The optimisation not taken

**Index policies by action kind**, so a decision visits only policies whose
`Match.Kinds` includes the action's kind. Most policies declare one. That would
turn O(all policies) into O(relevant policies) — plausibly a 3–5× reduction on a
large registry.

**It is refused because it would make the trace incomplete.**

Every policy consulted appears in the trace, including those skipped on kind
(INV-GOV-6). That is what answers *"why did the rule I wrote do nothing"* — the
single most common operator question about a policy engine. An index would
replace `skipped: match_kind` entries with silence, and silence is
indistinguishable from "your policy is not loaded".

The trade is stated rather than taken: **at 200 policies the complete trace costs
80 µs and 0.09% of a turn.** If a deployment reaches a policy count where that
stops being true, the change to make is a `TraceLevel` configuration — full for
authoring and incident review, decisive-only for steady state — not a silent
index.

### Other options considered and refused

| Idea | Refused because |
|---|---|
| Cache decisions by request fingerprint | A cached decision would outlive a consent revocation, which must bite on the very next check |
| Cache consent lookups | Same. Revocation is immediate or it is not revocation |
| Skip the trace for allowed decisions | The allow path is where an unexplained decision is least likely to be noticed and most likely to matter later |
| Lazy fingerprinting | See §6 — the 1.8 µs buys a recovery path that cannot fail |
| Pool decision slices | A pooled slice returned to the wrong decision is a cross-subject leak in an audit record |

---

## 6 · One deliberate waste, and why it stays

`Decide` computes the request fingerprint **twice**: once in `evaluateSafely`
before evaluation, and once inside `Evaluate`. That is ~1.8 µs, or **19% of a
small decision.**

It stays because the precomputed copy is what the panic handler uses. Computing
it lazily inside the deferred handler would mean that a panic *originating in
fingerprinting* would panic again inside the recovery — which is exactly the bug
ENGINEERING_AUDIT §F1 records and fixes.

**1.8 µs is 0.0002% of a turn budget. A recovery path that can fail the same way
as the thing it recovers from is not worth 1.8 µs.**

---

## 7 · Concurrency

| | ns/op |
|---|---:|
| `DecideSmall` serial (12 policies) | 9,612 |
| `DecideParallel` (50 policies, 16 goroutines) | 13,356 |

Not directly comparable — different policy counts — but the parallel figure at
50 policies is **below** the serial figure at 50 (29.6 µs), so sixteen cores are
doing genuine parallel work.

Nothing on the decision path is globally contended: the registry is lock-free to
read, metrics are per-series atomics, and consent takes a read lock for 108 ns.

---

## 8 · Not measured

| Not measured | Why |
|---|---|
| **Race detector** | `-race` requires cgo; no C toolchain. Blocking — ENGINEERING_AUDIT §A2 |
| Durable audit-store latency | None exists; `RecordingAuditor` is in-memory |
| Kafka publish latency | `Publisher` is an interface; only Noop and Recording exist |
| Cross-replica policy propagation | Single-process engine |
| Policy loading from files | No serialisation — audit §A5 |
| Sustained multi-hour load | No load harness in scope |

The concurrency tests exercise concurrent decisions, policy churn during
decisions, concurrent consent grants and revocations, and racing escalation
resolutions, and pass at `-count=5 -shuffle=on`. **That is evidence, not proof.**
Without `-race`, "no data race was observed" is the strongest honest claim.
