# Performance Verification Report — Phase 10.5

**Machine:** 11th Gen Intel Core i7-11800H @ 2.30 GHz, 16 logical CPUs,
windows/amd64, Go 1.26.5.
**Suite:** 131 benchmarks across eight modules.

---

## 1. A benchmark that had never run to convergence

The first thing this section found was a broken benchmark, and it was found by
running the suite at a fixed `-benchtime` — which CI now does and nobody had
done before.

```console
$ cd packages/go/memory && go test -bench=BenchmarkStore -benchtime=100ms .
BenchmarkStore-16    --- FAIL: BenchmarkStore-16
    bench_test.go:36: memory: budget exceeded
```

`BenchmarkStore` writes `b.N` records under a single subject. `DefaultPolicy`
caps a subject at **50,000** records. Go sizes `b.N` by timing, so at ~1.4 µs per
store, `-benchtime=100ms` lands near 71,000 iterations and the store correctly
refuses — the engine enforcing its own invariant against a benchmark that
ignored it.

`BenchmarkStoreWithAttributes` has the identical flaw and was passing at 47,667
iterations, i.e. **under the cap by luck**, not by design.

This is a small bug with an interesting implication: Phase 10C reported 21
benchmarks for the memory engine, and two of them could not survive an
iteration count the tooling picks on its own. They had only ever been run at
counts low enough to hide it. A benchmark that cannot run to convergence has not
really been measured.

**Fixed** by refreshing the store every 40,000 iterations with the timer
stopped, so the reset never enters the measurement. Reusing keys would have been
simpler and wrong — it silently converts an insert benchmark into an update
benchmark, which is a different and much cheaper path.

Result, and the number is unchanged from the low-N runs, which is the check that
the fix does not distort:

```
BenchmarkStore-16                  413427    1315 ns/op    1909 B/op     8 allocs/op
BenchmarkStoreWithAttributes-16    235221    2887 ns/op    3339 B/op    17 allocs/op
```

**Sweep:** every module re-run at `-benchtime=300ms`. **Zero failures across all
131 benchmarks.**

---

## 2. The shared metrics package

Every hot path is zero-allocation, which matters because these are now on the
hot path of all six subsystems rather than one.

```
BenchmarkCounterInc-16                17793858     20.56 ns/op    0 B/op   0 allocs/op
BenchmarkCounterIncParallel-16         6162494     58.27 ns/op    0 B/op   0 allocs/op
BenchmarkGaugeSet-16                  74492518      4.779 ns/op   0 B/op   0 allocs/op
BenchmarkHistogramObserve-16          11717680     30.08 ns/op    0 B/op   0 allocs/op
BenchmarkHistogramObserveParallel-16   3040861    126.8 ns/op     0 B/op   0 allocs/op
BenchmarkHistogramQuantile-16         17036499     20.78 ns/op    0 B/op   0 allocs/op
BenchmarkSnapshot-16                      3927  87911 ns/op   55264 B/op 1238 allocs/op
```

The parallel figures are the honest ones for production: `CounterInc` costs
2.8× more under 16-way contention (20.6 → 58.3 ns) and `HistogramObserve` 4.2×
(30.1 → 126.8 ns). Both take a read lock to find the label series and then an
atomic add, so the contention is on the map's `RWMutex`. At these absolute
numbers it does not warrant a lock-free redesign — a sharded map would add
complexity to something already costing under 130 ns at full contention.

`Snapshot` allocates and should: it is the scrape path, called on a timer, and
it materialises cumulative bucket maps that **did not previously exist in three
of the six subsystems**. 88 µs on a scrape to make histogram data reachable at
all is the trade the migration exists to make.

---

## 3. The frozen phases after migration

Representative hot paths, `-benchtime=100ms`:

| Subsystem | Benchmark | ns/op | B/op | allocs/op |
|---|---|---:|---:|---:|
| runtime | `Scheduler_AdmitRelease` | 531.6 | 336 | 6 |
| runtime | `Scheduler_ShedPath` | 72.17 | 24 | 1 |
| runtime | `Dispatcher_AbortLatency` | 5,759 | 272 | 5 |
| conversation | `DecisionCycle` | 7,283 | 3,896 | 27 |
| conversation | `PolicyEvaluate` | 313.2 | 384 | 1 |
| conversation | `Planner` | 51.94 | **0** | **0** |
| memory | `Retrieve` | 511.8 | 336 | 3 |
| memory | `RetrieveContended` | 487.0 | 336 | 3 |
| toolruntime | `ExecuteSingle` | 19,677 | 17,992 | 109 |
| toolruntime | `PlanSingle` | 3,315 | 3,931 | 19 |
| governance | `DecideSmall` | 6,645 | 8,772 | 56 |
| governance | `DecideLarge` | 77,871 | 98,535 | 266 |
| governance | `EvaluatePure` | 7,941 | 6,264 | 39 |

Two things worth drawing out:

**`memory.RetrieveContended` (487 ns) is faster than `memory.Retrieve`
(511.8 ns).** Not a contradiction — retrieval takes the shard *write* lock
because a read updates access statistics, and the contended variant spreads
across shards while the serial one hammers one. The sharding is doing its job.

**`governance.DecideLarge` is 11.7× `DecideSmall` and allocates 11.2× as much.**
Linear in policy-set size with no super-linear term, which is what the
copy-on-write registry and pure evaluator predict.

---

## 4. On comparing these numbers to Phase 10F's

**They are not comparable, and this section exists to say so before somebody
reads a 40% improvement into them.**

Post-migration figures for the evaluation module run substantially faster than
the ones published in Phase 10F:

| Benchmark | Phase 10F | This session |
|---|---:|---:|
| `ExecuteScenario` | 13,387 ns | 7,655 ns |
| `RunSuiteSerial` | 410,283 ns | 252,830 ns |
| `BehaviourPrint` | 12,537 ns | 6,579 ns |
| `GoldenBaselineLookup` | 50.36 ns | 34.42 ns |
| `Dashboard` | 52,723 ns | 31,780 ns |

**This is not a speedup from the migration.** Two observations rule that out:

1. **Allocation counts are byte-identical** — `ExecuteScenario` 7,876 B / 74
   allocs, `RunSuiteSerial` 253,861 B / 2,049 allocs, `BehaviourPrint` 4,488 B /
   11 allocs, `Dashboard` 42,136 B / 99 allocs. Identical allocations mean
   identical code paths.
2. **The gap appears on functions that never touch metrics.**
   `GoldenBaselineLookup` is a map read under an `RWMutex` and `BehaviourPrint`
   is string encoding. Neither calls an instrument. A metrics change cannot make
   them 40% faster.

Measured within-session variance is **±3%**:

```
BenchmarkBehaviourPrint-16    6025 ns    6228 ns    6189 ns
BenchmarkGoldenBaselineLookup-16   33.66 ns   34.48 ns   34.75 ns
```

So the ~40% between-session gap is machine state — thermal, background load,
CPU frequency — not code.

**The correct conclusions:** the migration caused no regression, evidenced by
identical allocation counts; and **absolute timings from this machine are not
comparable across sessions**, which retroactively qualifies every wall-clock
figure published in the Phase 10A–10F performance documents. Allocation counts
are the durable measurement; nanoseconds on a laptop are not.

This is the same class of error as Phase 10F's clock-resolution defect: a number
that looks like a measurement but is mostly an artifact of the machine.

---

## 5. Allocation analysis

Zero-allocation hot paths, which is where the design intent shows:

| Path | allocs/op |
|---|---:|
| `metrics.Counter.Inc` | 0 |
| `metrics.Gauge.Set` | 0 |
| `metrics.Histogram.Observe` | 0 |
| `metrics.Histogram.Quantile` | 0 |
| `conversation.Planner` | 0 |
| `evaluation.GoldenBaselineLookup` | 0 |

The expensive paths are expensive for legible reasons:
`toolruntime.ExecuteSingle` at 109 allocations builds an intent, a contract
lookup, a sandbox admission, an idempotency key and a ledger entry;
`governance.DecideLarge` at 266 walks a large policy set and accumulates a
decision trace. Neither is a leak, and both scale linearly.

---

## 6. CPU profiles

Not collected. Nothing in the current numbers points at a hot spot worth
profiling: every path is either sub-microsecond or linear in a legible input,
and the one super-linear defect this platform has found (Phase 10F's
`PendingApprovals` gauge, 45× and 86×) was found by reading an allocation
figure, not a profile.

Profiling is warranted when a real workload exists — Phase 11 — because a
profile of a synthetic benchmark mostly measures the benchmark.

---

## 7. Memory pressure

No unbounded growth remains in the AI plane:

- `MemoryStorage` bounds runs (200), observations per scenario (50), benchmarks
  per scenario (50, added in Phase 10F) and trend points (500).
- `MemoryRepository` is bounded by the retention schedule, which now covers
  **every** record kind — construction refuses a schedule that leaves one
  uncovered, because an uncovered kind is kept forever and nobody is told.
- `memory.Store` enforces `MaxRecordsPerSubject`, which is what §1's benchmark
  ran into.
- `metrics` instruments grow with **label cardinality**, which is unbounded in
  principle. In practice every label value in the platform is an enum, an
  outcome code or a subsystem name. Worth stating as a constraint: a label whose
  value is a user identifier or a phone number would grow a counter map without
  limit. Nothing currently does this.

---

## 8. Findings

| ID | Finding | Severity | Status |
|---|---|---|---|
| P1 | Two memory benchmarks failed past 50,000 iterations | medium | **fixed** |
| P2 | Wall-clock figures not comparable across sessions | — | documented; qualifies earlier reports |
| P3 | Metric label cardinality unbounded in principle | low | open, constraint documented |
| — | Regression from the metrics migration | — | **none** — allocations identical |
| — | Benchmark failures across 131 benchmarks | — | **none** after P1 |

---

## 9. Related

- [METRICS_MIGRATION_REPORT.md](METRICS_MIGRATION_REPORT.md)
- [../evaluation/BENCHMARK_REPORT.md](../evaluation/BENCHMARK_REPORT.md)
