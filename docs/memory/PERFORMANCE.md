# Performance Report — Enterprise Memory Engine

**Phase 10C** · `packages/go/memory` · 2026-08

---

## 1 · Method

| | |
|---|---|
| CPU | 11th Gen Intel Core i7-11800H @ 2.30 GHz |
| Logical CPUs | 16 |
| Platform | windows/amd64 |
| Go | 1.26.5 |
| Command | `go test -bench=. -benchmem -benchtime=1s ./...` |
| Benchmarks | 21, in `bench_test.go` |

**What is and is not measured.** The engine is in-process and in-memory. These
numbers are the engine's own cost with **no network, no broker, no database**. A
production deployment puts a durable store behind it (Aurora per ADR-0009) and
Kafka beside it; that latency is Phase 10D and is not modelled here. Treat every
number below as a floor, not a forecast.

Runs are single-shot at `-benchtime=1s` on a laptop with background load. Treat
differences under ~5% as noise. Where a comparison matters — §5 — it was
re-measured rather than inferred.

---

## 2 · Results

### Write path

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Store` | 1,529 | 1,810 | 8 |
| `StoreWithAttributes` | 2,652 | 3,432 | 17 |
| `UpdateCAS` | 487 | 1,048 | 3 |

Two attributes cost **+1,123 ns and +9 allocations** — the secondary index is
maintained per attribute, and each maintains a map entry and a posting list. A
record binding conversation and session pays 1.7× a bare write. That is the
price of the 682 ns lookup in the read path, and it is the right way round:
memories are read far more often than written.

`UpdateCAS` at 487 ns is *cheaper than Store* because it does not touch the
secondary or time index — only the payload and the version change.

### Read path

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Retrieve` | 287 | 336 | 3 |
| `RetrieveParallel` (256 keys, 16 shards) | 487 | 337 | 3 |
| `RetrieveContended` (all goroutines, one key) | 388 | 337 | 3 |

The three allocations are the clone: record, payload, attribute map. `Clone`
alone is 73 ns, so **the clone is a quarter of a retrieve** — the cost of
INV-MEM-1, paid deliberately (see §3).

**The contended case is faster than the spread case.** 256 keys across 16 shards
touch 16 cache lines and 256 records; one key stays in one core's cache. Cache
locality beats lock spreading at this scale, which is worth knowing before
anyone "optimises" the sharding.

### Index

| Benchmark | ns/op | B/op | allocs/op | Records |
|---|---:|---:|---:|---:|
| `IndexByAttribute` | 682 | 4,096 | 1 | 100 |
| `IndexByTimeRange` | 7,041 | 37,120 | 4 | 500 |
| `IndexBySubject` | 29,750 | 5,912 | 10 | 1,000 |
| `SearchByKind` | 77,477 | 75,872 | 549 | 200 → 20 |

`IndexByAttribute` is **one allocation** — the result slice, sized from the
posting list. That is what an index is supposed to look like.

`IndexBySubject` at 29.8 µs / 1,000 records is the deliberately unindexed full
scan (≈30 ns per record examined). Erasure and diagnostics pay it so that every
write does not maintain a subject index. At 100,000 records it becomes ~3 ms —
still acceptable for an erasure request, and the point at which the trade should
be revisited is a subject scan on a request path, which does not exist today.

### Decision logic

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `PromotionEvaluate` | 7.76 | 0 | 0 |
| `PolicyAdmit` | 14.30 | 0 | 0 |
| `RecordClone` | 73.0 | 0 | 0 |
| `EventDispatch` | 46.5 | 0 | 0 |
| `TokenEstimateLatin` | 25.5 | 0 | 0 |
| `TokenEstimateDevanagari` | 41.5 | 0 | 0 |
| `FakeClockOverhead` | 12.7 | 0 | 0 |

**Zero allocations across every decision function.** `PromotionEvaluate` being a
pure function at 7.8 ns is what makes a 10,000-record sweep viable; if it
allocated, the sweep would be a GC event every 30 seconds.

`RecordClone` reports 0 B/op because the benchmark's escape analysis keeps the
copy on the stack; in `Retrieve` the same clone escapes and shows as 3 allocs /
336 B. The 73 ns is the real work; the allocation count in context is the honest
one.

Devanagari estimation is 1.6× Latin — it decodes multi-byte runes rather than
counting bytes. 41.5 ns on a 100-byte string is not a cost worth optimising, and
the alternative (one ratio for all scripts) mis-sizes Hindi contexts by ~2×.

### Composite operations

| Benchmark | ns/op | B/op | allocs/op | Scale |
|---|---:|---:|---:|---|
| `Sweep` | 428,714 | 496 | 7 | 10,000 records |
| `ContextBuild` | 62,230 | 44,232 | 237 | 52 records → 2,048 tokens |
| `CompressionPlan` | 88,395 | 92,696 | 425 | 100 records |
| `ForgetSubject` | 135,429 | 148,632 | 634 | 100 records, 4 namespaces |

**`Sweep` allocates 496 bytes to evaluate 10,000 records.** The metadata
snapshot is reused and `Evaluate` is allocation-free, so the entire pass costs
seven allocations regardless of store size. At a 30 s interval this is 429 µs of
work per 30,000,000 µs — **0.0014% duty cycle**.

`ForgetSubject` at 135 µs for 100 records includes four unindexed subject scans
(one per namespace) plus deletion, tombstoning and event dispatch. An erasure
request budget is measured in seconds; this is three orders of magnitude inside
it.

---

## 3 · The read path takes a write lock — measured, kept

`Index.Touch` updates `AccessCount` and `AccessedAt` on every retrieve, so the
read path acquires the shard **write** lock.

| | ns/op | vs serial |
|---|---:|---:|
| Serial | 287 | 1.0× |
| Parallel, spread | 487 | **1.70×** |
| Parallel, single key | 388 | 1.35× |

**Kept, for a reason that is not performance.** Access statistics drive
promotion, and promotion is what makes the memory ladder explainable rather
than heuristic — "why does it remember that about me" must be answerable with a
rule. Statistics updated asynchronously would make the answer approximate.

**The documented alternative, not taken:** move `AccessCount` to `atomic.Uint64`
and `AccessedAt` to an `atomic.Int64` of Unix nanos, then read under the RLock.
Estimated ~330 ns parallel. Not taken in 10C because it splits a record's state
between mutex-protected and atomic fields, and every future field would need a
decision about which half it lives in. If a load test shows read contention
mattering, this is the first change to make and the benchmark to prove it with
is `RetrieveParallel`.

---

## 4 · Against the frozen latency budget

ADR-0011: **p50 ≤ 900 ms, p95 ≤ 1500 ms, p99 ≤ 2500 ms**, barge-in ≤ 20 ms.

A single conversational turn's memory work:

| Step | Cost |
|---|---:|
| `ContextBuild` | 62 µs |
| 3 × `Retrieve` | 0.9 µs |
| 1 × `Store` (turn record) | 1.5 µs |
| 1 × `ByConversation` | 0.7 µs |
| **Total** | **~65 µs** |

**0.007% of the 900 ms p50 budget.** Memory is not a latency risk in this
architecture; the model call is. The engineering consequence is that **the
memory engine should not be optimised further without evidence** — effort spent
here buys nothing a user can perceive.

Barge-in (≤ 20 ms, one frame) touches no memory path at all.

---

## 5 · The one optimisation made, and its honest result

`Search` originally cloned every candidate, sorted the clones and discarded most.

| | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| Clone-everything | 87,684 | — | 910 |
| **Meta projection** | **77,477** | 75,872 | **549** |
| Change | **−11.6%** | | **−39.7%** |

**The allocation reduction is the result; the time saving is modest.** The
benchmark is dominated by the unindexed kind scan, not by cloning — so removing
the cloning removed 40% of the garbage and 12% of the time. Reported as measured
rather than rounded into a better headline.

It also fixed a latent race: `Index.Get` had been cloning *outside* the read
lock. That correctness fix, not the 12%, is why the change was worth making.

### Optimisations considered and refused

| Idea | Refused because |
|---|---|
| Subject index | Maintained on every write to serve erasure and diagnostics — wrong trade, and §2 quantifies it |
| Sorted time index | O(log n) read, O(n) insert; the engine writes far more than it range-queries |
| Atomic access counters | §3 — splits record state across two synchronisation regimes for a cost nothing yet feels |
| Payload pooling | A pooled payload returned to a caller by mistake is a cross-subject data leak. Not worth 336 bytes |
| Lock-free primary index | The 1.7× contention is not a problem; a hand-rolled lock-free map without `-race` verification is |

**Every optimisation attempted in Phases 10A–10C introduced a defect** (10A F3
deadlock, 10B F3 race hazard, 10C the `Index.Get` race found while optimising
Search). That is an argument for benchmark-then-reason discipline, not against
optimising — but it does mean an unmeasured optimisation here is a net negative.

---

## 6 · Scaling behaviour

| Operation | Complexity | At 10× |
|---|---|---|
| `Retrieve` | O(1) | 287 ns |
| `ByAttribute` | O(posting list) | ~6.8 µs at 1,000 |
| `ByTimeRange` | O(overlapping buckets) | ~70 µs at 5,000 |
| `BySubject` | **O(n)** | ~298 µs at 10,000 |
| `Search` | O(candidates) + O(k log k) | ~775 µs at 2,000 |
| `Sweep` | O(n) | ~4.3 ms at 100,000 |

**`Sweep` is the one to watch.** At 1,000,000 records a pass costs ~43 ms. It
runs on a 30 s ticker so the duty cycle stays negligible, but the pass holds
read locks while snapshotting metadata. `SweepBudget` exists exactly for this:
it caps work per pass, does what fits, reports what it left, and continues on
the next tick.

**Memory footprint:** ~1.8 KB per record in the primary index, plus ~200 B per
indexed attribute, plus 8 B per time-bucket entry. 1,000,000 records with two
attributes each ≈ **2.2 GB**. That is the number that decides when the durable
store in Phase 10D stops being optional.

---

## 7 · Not measured

Stated plainly rather than left implied:

| Not measured | Why |
|---|---|
| **Race detector** | `-race` requires cgo; no C toolchain on this machine. Blocking finding — ENGINEERING_AUDIT §A2 |
| Sustained multi-hour load | No load harness in scope for 10C |
| Behaviour under memory pressure / GC at scale | Needs a long-running host |
| Durable-store latency | Phase 10D |
| Kafka publish latency | `NoopPublisher` and `RecordingPublisher` only |
| Cross-process, multi-instance behaviour | Single-process engine |

The concurrency tests in `integration_test.go` exercise concurrent readers,
writers, sweeps and erasures and pass at `-count=10 -shuffle=on`. **That is
evidence, not proof.** Without `-race`, "no data race was observed" is the
strongest honest claim available.
