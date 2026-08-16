# Performance Report — Enterprise Tool Calling Runtime

**Phase 10D** · `packages/go/toolruntime` · 2026-08

---

## 1 · Method

| | |
|---|---|
| CPU | 11th Gen Intel Core i7-11800H @ 2.30 GHz |
| Logical CPUs | 16 |
| Platform | windows/amd64 |
| Go | 1.26.5 |
| Command | `go test -run XXX -bench=. -benchmem -benchtime=300ms .` |
| Benchmarks | 22, in `bench_test.go` |

**What these do and do not measure.** The tool is a fake that returns
immediately, so these measure the **runtime's own cost** — planning, permission,
validation, idempotency, admission, dispatch, events, audit — and nothing else.

A real tool call is network work measured in milliseconds and will dominate
every number here by three orders of magnitude. **That is the point.** The
runtime's overhead has to be invisible next to the work it governs, and these
say whether it is.

Single-shot runs on a laptop with background load. Treat differences under ~5%
as noise. Where a comparison matters — §4 — it was re-measured rather than
inferred.

---

## 2 · Results

### End to end

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `ExecuteSingle` | 18,267 | 18,100 | 109 |
| `ExecuteSingleParallel` | **7,524** | 17,484 | 109 |
| `ExecuteMutating` | 22,703 | 21,339 | 129 |
| `Replay` | 8,077 | 9,897 | 66 |

**The parallel number is 2.4× *faster* than the serial one.** Nothing in the
execution path is globally contended: the registry is copy-on-write and takes no
read lock at all, metrics are per-series atomics, and the scheduler's mutex is
held for a few instructions. Sixteen cores do sixteen executions.

**`ExecuteMutating` costs 4.4 µs more than `ExecuteSingle`** — that is what the
idempotency ledger costs: a derived key, a claim, and a settle. For a call that
changes the world, 4.4 µs to guarantee it happens once is not a decision worth
agonising over.

**A replay is 2.3× cheaper than an execution** and, more importantly, invokes
nothing. Under the retry storm in EXECUTION_EVALUATION §E2 that is the
difference between one downstream call and sixty-four.

### Planning

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `PlanSingle` | 2,906 | 3,931 | 19 |
| `PlanFiveStep` | 17,584 | 36,130 | 107 |
| `PlanFingerprint` | 1,267 | 832 | 18 |

Planning a five-step dependency graph costs 17.6 µs — roughly 3.5 µs per step,
which is discovery plus a contract clone plus static validation. A plan is built
once per turn and a turn has 900 ms; this is 0.002% of it.

### Registry and discovery

| Benchmark | ns/op | B/op | allocs/op | Scale |
|---|---:|---:|---:|---|
| `RegistryGet` | 266 | 464 | 3 | |
| `DiscoveryResolve` | 3,966 | 7,392 | 18 | 50 registrations |
| `RegistryRegister` | 375,323 | 260,840 | 3,576 | 50 registrations |

**Registration is expensive and that is the trade.** Copy-on-write rebuilds the
descriptor map, the capability index and each capability's preference ordering
on every write. Reads then take **no lock at all** — and reads happen on every
execution while registrations happen on deploy.

375 µs per registration means a hundred-tool boot costs ~37 ms. That is a
startup cost, once, and it buys a lock-free read path plus the guarantee that a
plan cannot have its tool changed underneath it mid-execution.

### Components

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `PermissionEvaluate` | 165 | 16 | 1 |
| `ValidateInput` | 455 | 1,072 | 2 |
| `DeriveKey` | 913 | 608 | 12 |
| `ArgumentsFingerprint` | 668 | 424 | 8 |
| `LedgerClaimSettle` | 5,801 | 1,405 | 6 |
| `SchedulerAcquireRelease` | 148 | 40 | 2 |
| `SchedulerAcquireContended` | 342 | 40 | 2 |
| `SandboxEnterRelease` | 104 | 128 | 1 |
| `EventDispatch` | **34.7** | **0** | **0** |
| `AuditRecord` | 402 | 1,446 | 0 |
| `StreamChunk` | **55.0** | **0** | **0** |
| `ValueCanonicalLarge` | 2,558 | 9,760 | 5 |

**Event dispatch and stream chunks are allocation-free.** Both are per-chunk and
per-event costs on the hot path; a streaming tool emitting a chunk per token
pays 55 ns per token, which is nothing next to the token itself.

**The scheduler is 2.3× under contention** (148 → 342 ns), which for a mutex
held across a few comparisons is the expected shape and not worth removing.

**`DeriveKey` at 913 ns** is dominated by canonical encoding and SHA-256. It runs
once per mutating execution. Making it cheaper would mean a weaker hash or a
non-canonical encoding, and the canonical encoding is the reason the key means
anything.

---

## 3 · Against the frozen latency budget

ADR-0011: **p50 ≤ 900 ms**, p95 ≤ 1500 ms, p99 ≤ 2500 ms.

Measured end to end over 500 executions through the public API
(EXECUTION_EVALUATION §E8):

| | |
|---|---:|
| Per execution | **20.9 µs** |
| Turn budget | 900 ms |
| **Share of budget** | **0.0023%** |

**The runtime is not a latency risk in this architecture.** The tool call is.

The engineering consequence is that **this module should not be optimised
further without evidence** — effort spent here buys nothing a person on a phone
can perceive. The evaluation suite asserts a 1% ceiling; the measurement is 430×
inside it.

---

## 4 · Three defects found by benchmarking

Every one of these was a *correctness-adjacent* problem that presented as a
strange number, which is the argument for reading benchmarks rather than
glancing at them.

### F4 · The ledger walked itself on every claim

`Ledger.Claim` ran a full expiry sweep, and `evictOverflowLocked` rebuilt the
whole queue whenever the ledger was at capacity — which, in steady state, is
always.

| | ns/op |
|---|---:|
| Full sweep per claim | 88,263 |
| After front-only expiry, still rebuilding overflow | 133,773 |
| **After bounded overflow eviction** | **5,801** |
| | **−93%** |

The middle row is the interesting one: the first fix made it *worse*, because
removing the expiry walk left the overflow rebuild as the only cost and it ran
more often. **A partial fix to a quadratic path can look like a regression**,
and stopping at the first change would have shipped something slower than what
it replaced.

Knock-on: `ExecuteMutating` went from 130 µs to 22.7 µs — **5.7×**.

The fix is a front-of-queue eviction that is amortised O(1). Entries are
appended in claim order under a uniform TTL, so the queue is ordered by expiry:
once the head has not expired, nothing behind it has. In-flight entries are
never evicted, and the full pass moved to `Sweep`, which runs on the maintenance
timer.

### F5 · Discovery cloned every candidate before truncating

`Resolve` appended a full `Registration` per match and cloned every one, then
discarded all but the first three.

| | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| Clone-then-truncate | 18,323 | 66,400 | 17 |
| **Filter on descriptors, materialise survivors** | **3,966** | **7,392** | 18 |
| | **−78%** | **−89%** | |

A descriptor is two strings; a `Registration` carries a whole contract.

**This is the same mistake Phase 10C made in its retrieval path** — filtering on
heavy objects and throwing most away. Two phases, two independent occurrences.
The lesson recorded there was "filter on metadata, materialise only the
survivors", and it was not applied here until a benchmark said 66 KB.

### The one that was not a performance bug

`RetrySpec.InitialBackoff: 0` meant "unset, take the default" rather than "no
delay", so a test asking for no backoff silently got 50 ms and **hung forever**
on the fake clock. Fixed with an explicit `NoBackoff` flag — the same resolution
Phase 10C reached for `FailAfter: -1`, and the same lesson: **zero cannot mean
both "unset" and "none".** ENGINEERING_AUDIT §F1.

---

## 5 · Optimisations considered and refused

| Idea | Refused because |
|---|---|
| Cache resolved plans by intent fingerprint | A cached plan would pin a version the registry has since drained — exactly the failure INV-TOOL-9 exists to prevent |
| Skip `ValidateInput` when the plan already checked statically | Static checking cannot see bound arguments; the invoke-time check is the authoritative one and skipping it makes validation a report |
| Pool `Arguments` maps | A pooled map returned to the wrong execution is a cross-subject data leak. Not worth 1 KB |
| Lock-free scheduler queue | 2.3× contention is not a problem; a hand-rolled lock-free queue without `-race` verification is |
| Reuse one `Journal` across plans | Would keep completed work — the most sensitive data the runtime touches — alive after the plan that produced it ended |
| Weaken the idempotency hash | The canonical encoding and SHA-256 are the reason the key means anything |

**Every optimisation attempted across Phases 10A–10D has introduced or exposed a
defect.** That is an argument for benchmark-then-reason discipline, not against
optimising — but it does mean an unmeasured optimisation here is a net negative.

---

## 6 · Scaling behaviour

| Operation | Complexity | At 10× |
|---|---|---|
| `RegistryGet` | O(1) | 266 ns |
| `DiscoveryResolve` | O(registrations for the capability) | ~40 µs at 500 |
| `RegistryRegister` | **O(total registrations)** | ~3.8 ms at 500 |
| `Ledger.Claim` | O(1) amortised | 5.8 µs |
| `Plan` | O(steps + edges) | ~176 µs at 50 steps |
| `Execute` | O(1) per step | 18 µs |

**`RegistryRegister` is the one to watch.** At 500 registrations a rollout that
re-registers everything costs ~1.9 s of CPU. That is acceptable for a boot and
uncomfortable for a hot-reload loop, which is one reason there is no config
hot-reload in this module.

**Memory:** ~5 KB per registration (contract, capability index entries, posting
lists) and ~200 B per live ledger entry. A 200-tool registry with a full 10,000
-entry ledger is ~3 MB. Not a constraint at any scale this platform will reach.

---

## 7 · Not measured

Stated plainly rather than left implied:

| Not measured | Why |
|---|---|
| **Race detector** | `-race` requires cgo; no C toolchain on this machine. Blocking — ENGINEERING_AUDIT §A2 |
| Real tool latency | No real adapter exists in this phase, by design |
| Kafka publish latency | `Publisher` is an interface; only `Noop` and `Recording` exist here |
| Durable ledger latency | In-memory only; Redis/Aurora backing is Phase 10E |
| Cross-replica behaviour | Single-process runtime |
| Sustained multi-hour load | No load harness in scope |
| Behaviour under GC pressure at scale | Needs a long-running host |

The concurrency tests exercise concurrent plans, concurrent duplicates, registry
churn during execution and overload shedding, and pass at
`-count=5 -shuffle=on`. **That is evidence, not proof.** Without `-race`, "no
data race was observed" is the strongest honest claim available.
