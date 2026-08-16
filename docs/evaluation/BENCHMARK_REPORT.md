# Benchmark Report — Phase 10F

**Machine:** 11th Gen Intel Core i7-11800H @ 2.30 GHz, 16 logical CPUs,
windows/amd64, Go 1.26.5.
**Two measurement systems are reported here** and they are not interchangeable —
see §1.

---

## 1. Why there are two sets of numbers

**Go benchmarks** (`go test -bench`) amortise over thousands of iterations.
`testing.B` divides total elapsed time by the iteration count, so a 13 µs figure
is a genuine measurement even when a single iteration is far below the clock's
granularity. **These numbers are trustworthy.**

**The platform's own benchmark engine** measures individual scenario runs
against real engines. On this machine the clock's granularity is **≈520–526 µs**
and every scenario in the library completes in less than that. So:

- its **amortised means are trustworthy** — total time over iteration count, the
  same arithmetic `testing.B` performs
- its **percentiles are not**, and every line it emits says so:

```
memory.store-retrieve on memory: amortised=82.995µs n=40 p50=0s p95=0s p99=0s max=552µs σ=86.18µs
  [BELOW CLOCK RESOLUTION 525.6µs — trust the amortised mean, not the percentiles]
```

A p50 of `0s` next to an amortised mean of 83 µs is not a contradiction. It
means more than half the samples quantised to zero — which is the honest report
of what a 525 µs clock can see of an 83 µs operation.

Before this was discovered, the platform reported those zeroes as fact. See
ENGINEERING_AUDIT F1.

---

## 2. Platform benchmarks

22 benchmarks, `-benchtime=300ms`.

```
BenchmarkExecuteScenario-16              26787     13387 ns/op     7876 B/op     74 allocs/op
BenchmarkExecuteScenarioNoBaseline-16    28424     12227 ns/op     7692 B/op     68 allocs/op
BenchmarkExecuteTenSteps-16               2952    118813 ns/op    61265 B/op    370 allocs/op
BenchmarkRunSuiteSerial-16                 740    410283 ns/op   254077 B/op   2049 allocs/op
BenchmarkRunSuiteParallel-16               639    488288 ns/op   264648 B/op   2113 allocs/op
BenchmarkBehaviourPrint-16               26515     12537 ns/op     4488 B/op     11 allocs/op
BenchmarkBehaviourPrintLarge-16           3324    120183 ns/op    57480 B/op     18 allocs/op
BenchmarkCompareMatching-16              10185     37215 ns/op     9856 B/op    140 allocs/op
BenchmarkCompareDrifting-16               8550     49268 ns/op    18088 B/op    189 allocs/op
BenchmarkObservationClone-16             20044     19015 ns/op    17824 B/op     73 allocs/op
BenchmarkValuesFingerprint-16           375358      1007 ns/op      344 B/op      7 allocs/op
BenchmarkValuesDiff-16                  255504      1352 ns/op      192 B/op     13 allocs/op
BenchmarkScenarioDigest-16              113656      3174 ns/op     1192 B/op     18 allocs/op
BenchmarkRegistrySnapshot-16             10000     36361 ns/op    40960 B/op      1 allocs/op
BenchmarkRegistryRegister-16              2167    146210 ns/op    47312 B/op    727 allocs/op
BenchmarkGoldenBaselineLookup-16       8281191        50.36 ns/op      0 B/op      0 allocs/op
BenchmarkDetectRegressions-16             3849    102086 ns/op    51664 B/op    606 allocs/op
BenchmarkSummarise-16                   149401      2788 ns/op     1848 B/op      3 allocs/op
BenchmarkScorecards-16                   49926      7877 ns/op     3920 B/op     34 allocs/op
BenchmarkDashboard-16                    10000     52723 ns/op    42136 B/op     99 allocs/op
BenchmarkStoragePutRun-16                49034      7405 ns/op     3102 B/op     13 allocs/op
BenchmarkClockResolution-16          181950565         2.084 ns/op    0 B/op      0 allocs/op
```

### Observations

**Execution is fingerprinting.** `BehaviourPrint` at 12.5 µs against
`ExecuteScenario` at 13.4 µs means 94% of a scenario execution is producing the
behaviour fingerprint. That is the right place for the cost: it is the operation
the platform's correctness rests on.

**Golden lookup is free.** 50 ns, zero allocations, on the hot path of every
execution.

**Registry snapshot costs one allocation** for the entire scenario set. The
copy-on-write design copies a slice header and a map pointer, not contents.

**Ten steps is 8.9× one step** across a 10× step count — linear, with fixed
per-scenario overhead making up the difference. No super-linear term.

**Parallel suites are 19% slower than serial.** With per-scenario cost at 20 µs,
coordination and golden-store contention exceed the concurrency gain. A
threshold effect, not a defect; suites are not parallel by default. See
ENGINEERING_AUDIT A5.

---

## 3. Scenario benchmarks against the real frozen engines

40 iterations, 5 warmup, via `EvaluationRuntime.Benchmark`. **All 18 fall below
clock resolution**; amortised means only.

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
| `memory.oversized-refused` | memory | 187.0 µs |
| `governance.risk-raises` | governance | 196.8 µs |
| `tool.idempotency` | toolruntime | 199.6 µs |
| `governance.baseline-decides` | governance | 243.7 µs |
| `governance.emergency-containment` | governance | 277.5 µs |
| `governance.consent-gate` | governance | 288.2 µs |
| `conversation.turn-taking` | conversation | 326.3 µs |
| `conversation.escalation` | conversation | 1,831.2 µs |

By subject (mean of scenario means): memory 98.8 µs, runtime 87.6 µs,
toolruntime 142.7 µs, governance 229.9 µs, conversation 1,078.8 µs.

`conversation.escalation` is the only scenario above a millisecond and the only
one whose percentiles begin to be measurable (p95 2.39 ms, p99 2.63 ms). It
drives several engine calls across multiple turns.

**These are evaluation costs, not production request costs.** They are not
comparable to ADR-0011's budget (p50 ≤ 900 ms, p95 ≤ 1500 ms, p99 ≤ 2500 ms),
which covers an end-to-end call turn including network, ASR and inference. What
they establish is that the frozen engines' in-process work sits three orders of
magnitude below that budget.

---

## 4. Behaviour stability during measurement

`BenchmarkResult` reports `BehaviourStable` and the distinct fingerprints
observed, **alongside the timings and on purpose**. A benchmark over a scenario
that behaved differently on different iterations is a benchmark of several
different things averaged together, and its percentiles mean nothing.

All 18 scenarios were behaviourally stable across all 40 iterations.

---

## 5. The regression this report exists to have caught

First measurement of the suite benchmark:

```
BenchmarkRunSuiteSerial-16    18,479,786 ns/op    21,753,166 B/op    2,365 allocs/op
```

21.7 MB for 20 trivial scenarios, from 2,365 allocations — a small number of
very large ones. Cause: a gauge set by deep-copying every pending golden, once
per candidate filed, growing with the review backlog.

| | Before | After | Factor |
|---|---:|---:|---:|
| ns/op | 18,479,786 | 410,283 | **45×** |
| B/op | 21,753,166 | 254,077 | **86×** |

Full detail in [PERFORMANCE_REPORT.md](PERFORMANCE_REPORT.md) §5 and
ENGINEERING_AUDIT F4.

---

## 6. Reproducing

```console
cd packages/go/evaluation
go test -run XXX -bench=. -benchmem -benchtime=300ms .

cd ../evalsubjects
go test -run TestVerification_Benchmarks -v .
```

The second prints the measured clock resolution first, so any figure below it
can be read in context. On Linux, expect the resolution to drop by roughly three
orders of magnitude and the `BELOW CLOCK RESOLUTION` labelling to largely
disappear — at which point the per-scenario percentiles become meaningful for
the first time. See ENGINEERING_AUDIT A8.

---

## 7. Related

- [PERFORMANCE_REPORT.md](PERFORMANCE_REPORT.md)
- [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md)
