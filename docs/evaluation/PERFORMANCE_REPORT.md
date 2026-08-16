# Performance Report — Phase 10F

**Machine:** 11th Gen Intel Core i7-11800H @ 2.30 GHz, 16 logical CPUs
**Platform:** windows/amd64, Go 1.26.5
**Command:** `go test -run XXX -bench=. -benchmem -benchtime=300ms`
**Measured clock resolution:** ≈520–526 µs (see §1)

---

## 1. Read this first: the clock

Windows `time.Now()` has a granularity of roughly **520 µs**. Every scenario in
the library completes in less than that.

This is not a footnote. Before it was discovered, every step duration the
platform recorded was `0s` and every percentile it produced was meaningless.
See ENGINEERING_AUDIT F1.

Two consequences run through this whole document:

1. **Go benchmark numbers below are trustworthy.** `testing.B` amortises over
   thousands of iterations, so a 13 µs figure is a real measurement of total
   time divided by count — not a single sub-resolution sample.
2. **Per-scenario latency figures are amortised means, and the platform says
   so.** Percentiles at this scale measure the clock, and every line that
   reports them is labelled.

---

## 2. Platform benchmarks

### Scenario execution

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `ExecuteScenario` | 13,387 | 7,876 | 74 |
| `ExecuteScenarioNoBaseline` | 12,227 | 7,692 | 68 |
| `ExecuteTenSteps` | 118,813 | 61,265 | 370 |

Ten steps costs 8.9× one step across a 10× step count — close to linear, with
the fixed per-scenario overhead (registry lookup, golden lookup, storage) making
up the difference. There is no super-linear term.

`ExecuteScenarioNoBaseline` being *cheaper* than the baseline case is the
comparison not running. It still files a golden candidate.

### Suite execution

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `RunSuiteSerial` (20 scenarios) | 410,283 | 254,077 | 2,049 |
| `RunSuiteParallel` (20 scenarios) | 488,288 | 264,648 | 2,113 |

20.5 µs per scenario serial, against 13.4 µs for a bare `Execute` — 7 µs of
per-scenario suite overhead (result aggregation, observation storage, trend
points).

**Parallel is 19% slower than serial.** This is honest and worth explaining
rather than hiding: with per-scenario cost at 20 µs, goroutine coordination and
lock contention on the shared golden store cost more than the concurrency saves.
It is a threshold effect, not a bug. Parallel execution pays when subjects are
slow — real adapters against loaded engines, network-bound subsystems — and does
not pay for 20 µs of in-process work. Suites are not parallel by default. See
ENGINEERING_AUDIT A5.

These numbers are **45× faster and 86× leaner** than the first measurement, which
found a quadratic metric on the hot path. See §5.

### Fingerprinting and comparison

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `BehaviourPrint` | 12,537 | 4,488 | 11 |
| `BehaviourPrintLarge` | 120,183 | 57,480 | 18 |
| `CompareMatching` | 37,215 | 9,856 | 140 |
| `CompareDrifting` | 49,268 | 18,088 | 189 |
| `ValuesFingerprint` | 1,007 | 344 | 7 |
| `ValuesDiff` | 1,352 | 192 | 13 |
| `ScenarioDigest` | 3,174 | 1,192 | 18 |
| `ObservationClone` | 19,015 | 17,824 | 73 |

`BehaviourPrint` is the platform's hottest primitive — every execution, every
determinism run, every replay, every trend point calls it. At 12.5 µs with 11
allocations it dominates `ExecuteScenario`'s 13.4 µs, which means **scenario
execution is essentially fingerprinting**. That is the right place for the cost
to be: it is the operation the platform's correctness depends on.

Drifting comparison costs 32% more than matching, because it builds and sorts a
difference list. Paying more on the interesting path is correct.

### Registry

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `RegistrySnapshot` | 36,361 | 40,960 | 1 |
| `RegistryRegister` | 146,210 | 47,312 | 727 |

One allocation for a snapshot of the whole scenario set: the copy-on-write
design copies the slice header and the map pointer, not the contents.
Registration is 4× the cost of a snapshot because it rebuilds and re-digests the
set — the correct trade, since registration happens at startup and snapshots
happen on every read.

### Goldens, regression, reporting, storage

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `GoldenBaselineLookup` | **50.36** | 0 | 0 |
| `DetectRegressions` | 102,086 | 51,664 | 606 |
| `Summarise` | 2,788 | 1,848 | 3 |
| `Scorecards` | 7,877 | 3,920 | 34 |
| `Dashboard` | 52,723 | 42,136 | 99 |
| `StoragePutRun` | 7,405 | 3,102 | 13 |
| `ClockResolution` | 2.084 | 0 | 0 |

Baseline lookup at 50 ns with zero allocations is a map read under an `RWMutex`
returning a value copy. It is on the hot path of every execution and costs
nothing.

`DetectRegressions` at 102 µs for a 20-scenario pair is dominated by calling
`BehaviourPrint` twice per scenario (40 × 12.5 µs ≈ 500 µs would be the naive
cost — the actual figure is lower because the fingerprints are over smaller
synthetic observations).

---

## 3. Per-scenario latency against the real frozen engines

Measured by the platform's own benchmark engine, 40 iterations after 5 warmup,
against the five real engines. **All 18 fall below clock resolution**, so these
are amortised means and the platform labels every one.

| Scenario | Subject | Amortised mean |
|---|---|---:|
| `memory.missing-is-not-found` | memory | 40.7 µs |
| `runtime.clock-advances` | runtime | 43.1 µs |
| `memory.store-retrieve` | memory | 83.0 µs |
| `memory.forget-is-complete` | memory | 84.6 µs |
| `runtime.breaker-opens` | runtime | 92.1 µs |
| `tool.plan-is-inert` | toolruntime | 110.7 µs |
| `tool.failure-is-classified` | toolruntime | 111.5 µs |
| `runtime.window-evicts` | runtime | 127.6 µs |
| `governance.malformed-refused` | governance | 143.2 µs |
| `tool.execute` | toolruntime | 149.1 µs |
| `governance.risk-raises` | governance | 196.8 µs |
| `memory.oversized-refused` | memory | 187.0 µs |
| `tool.idempotency` | toolruntime | 199.6 µs |
| `governance.baseline-decides` | governance | 243.7 µs |
| `governance.emergency-containment` | governance | 277.5 µs |
| `governance.consent-gate` | governance | 288.2 µs |
| `conversation.turn-taking` | conversation | 326.3 µs |
| `conversation.escalation` | conversation | 1,831.2 µs |

Range: **40.7 µs to 1.83 ms**, a 45× spread.

These are **evaluation** costs — one scenario's steps through an adapter into a
real engine — not production request costs. They are not comparable to ADR-0011's
budget (p50 ≤ 900 ms, p95 ≤ 1500 ms, p99 ≤ 2500 ms), which covers an end-to-end
call turn including network, ASR and model inference. What they do establish is
that the frozen engines' in-process work is three orders of magnitude below that
budget, leaving essentially all of it to I/O and inference.

`conversation.escalation` at 1.83 ms is the only scenario above a millisecond
and the only one whose percentiles begin to be measurable (p95 2.39 ms, p99
2.63 ms). It is a multi-turn scenario driving several engine calls; the cost is
proportionate.

---

## 4. Scaling

| Exercise | Result |
|---|---|
| 25 full runs (450 scenario executions) | 131 ms — 291 µs per scenario including storage, trend and metric work |
| 192 evaluations across 16 goroutines | completed within one test tick |
| 200 evaluations against 40 concurrent registry publications | no degradation observed |

Storage stayed bounded throughout: 25 runs, 468 observations, 450 trend points,
against configured bounds of 200/50/500.

---

## 5. The optimisation that mattered

The first benchmark run reported:

```
BenchmarkRunSuiteSerial-16    18,479,786 ns/op    21,753,166 B/op    2,365 allocs/op
```

18.5 ms and **21.7 MB** for 20 trivial scenarios — 924 µs per scenario against
13.4 µs for a bare `Execute`. A 69× overhead with only 2,365 allocations behind
it, which is the tell: a small number of very large allocations.

Cause: `Execute` set a gauge with `len(goldens.PendingApprovals())`, and that
function deep-copies every pending golden — each carrying a whole observation —
across every key, then sorts. Once per candidate filed. Cost grows with the
pending count, so a platform left running in CI with a review backlog gets
slower the longer nobody reviews it.

Fix: an O(1) counter maintained on record and approve.

| | Before | After | Factor |
|---|---:|---:|---:|
| ns/op | 18,479,786 | 410,283 | **45×** |
| B/op | 21,753,166 | 254,077 | **86×** |

The general lesson is one this codebase has now hit twice — Phase 10D's ledger
was the other: **a metric that walks a collection is a metric that costs
whatever the collection costs.** Gauges over unbounded sets need incremental
counters, not derived reads.

---

## 6. Optimisations considered and refused

**Caching behaviour fingerprints on the observation.** `BehaviourPrint` is
called several times per observation and dominates execution cost. Caching it
would help — and would introduce a field that can disagree with the data it
fingerprints. An observation whose cached fingerprint is stale produces a wrong
verdict silently, which is the one failure mode this platform must not have.
Refused. If it becomes a real bottleneck, the correct fix is to compute it once
at construction and make the observation immutable.

**Pooling observation allocations.** 74 allocations per execution is not high
enough to justify a pool, and a pooled observation that escapes into a golden is
a use-after-free with a wrong verdict as its symptom.

**Parallel comparison within a run.** `Compare` is pure and would parallelise
cleanly, but at 37 µs against 20 µs of per-scenario budget the coordination
would cost more than it saves — the same threshold that makes parallel suites
slower than serial today.

---

## 7. What is not measured

- **Durable storage.** There is no durable `Storage` implementation, so nothing
  here reflects database latency. Expect it to dominate: 50 ns for an in-memory
  baseline lookup will become a network round trip.
- **Linux and macOS.** All figures are windows/amd64. The clock-resolution
  labelling will largely disappear on Linux, where `time.Now()` is roughly three
  orders of magnitude finer, and the per-scenario percentiles will become
  meaningful for the first time.
- **Real load.** The adapters drive in-process engines with fake clocks. A
  deployment evaluating against live subsystems will be dominated by those
  subsystems.

---

## 8. Related

- [BENCHMARK_REPORT.md](BENCHMARK_REPORT.md)
- [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md)
