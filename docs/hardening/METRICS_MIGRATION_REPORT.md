# Metrics Migration Report — Phase 10.5

**Resolves:** finding A1, outstanding since Phase 10B and the first handover
item in four consecutive phase audits.
**New module:** `packages/go/metrics` — 686 lines, 26 tests, 7 benchmarks,
zero dependencies.

---

## 1. What was actually wrong

The finding had been recorded five times, and each time it was recorded
imprecisely. The stated risk was:

> six independent histogram implementations will not agree on bucket
> boundaries, and a cross-subsystem latency dashboard built on them will be
> quietly wrong

Measured, that is **not what was wrong**.

`Observe` and `Quantile` were semantically identical across all six — the hash
differences were comments and variable names. And the bucket boundaries differ
**correctly**: governance decides in hundreds of nanoseconds, a conversation
turn takes seconds, and one shared bucket set would have put every governance
observation in the first bucket and every conversation observation in the last.
A shared bucket set would have been the bug.

The real defect was the **scrape surface**, and it was worse:

| Modules | `Sample` fields | Histogram in `Snapshot()` |
|---|---|---|
| runtime | Name, Labels, Value, **Buckets, Count, Sum** | complete |
| conversation, memory | Name, Labels, Value, Count, Sum | count + sum, **no buckets** |
| toolruntime, governance, evaluation | Name, Labels, Value | **`name_count` only** |

Governance emitted its histograms as a single synthetic counter-shaped sample:

```go
out = append(out, Sample{Name: h.name + "_count", ...,
    Value: float64(s.count.Load())})
```

No bounds, no cumulative buckets, no sum. A consumer could not reconstruct a
percentile or even an average. **Half the platform's latency data was
unreachable from outside the process.** The dashboard was not "quietly wrong";
it could not be built.

That is the difference between a finding recorded and a finding investigated,
and it is why this phase re-derived it from the code instead of copying it
forward a sixth time.

---

## 2. The design

`packages/go/metrics`: `Registry`, `Counter`, `Gauge`, `Histogram`, `Sample`.

**Every constructor is exported.** That is the whole reason six copies existed —
`runtime.Metrics` kept its constructors unexported, so a downstream module could
not register an instrument into it, Phase 10A was frozen, and forking the file
was easier than extending it. The Phase 10B header predicted the fix exactly:

> The correct fix is a small additive change to the runtime (an exported
> Register or NewCounter), at which point this file collapses to a thin binding.

**No dependencies, not even first-party.** The same constraint `runtime` and
`platform` carry. It is what lets a service outside the AI plane — billing,
analytics — use the same instruments without importing a conversation kernel to
count HTTP requests. That was the reason for a new module rather than exporting
constructors from `runtime`, which would have cost nothing inside the AI plane
and a great deal outside it.

**Bucket boundaries stay per-subsystem.** The module supplies the machinery and
takes no position on the scale, because the scale is a domain decision.

---

## 3. How the migration stayed additive

The brief required no breaking API changes. The mechanism is **type aliases**:

```go
type (
    Counter   = metrics.Counter
    Gauge     = metrics.Gauge
    Histogram = metrics.Histogram
    Sample    = metrics.Sample
)
```

`=` rather than a new named type, so `*conversation.Counter` **is**
`*metrics.Counter`. Every existing reference keeps compiling with its full
method set, in this package and in anything importing it. No call site changed,
no signature changed.

Each module's `Metrics` struct kept every exported field and every domain rate
helper — `HitRate`, `SuccessRate`, `AllowRate`, `PassRate`. Only the private
plumbing was removed.

Two internal changes were required:

- `Counter.prefixSum` → `Counter.PrefixSum`. It was unexported and independently
  reimplemented in **three** modules, so it belongs in the shared package. All
  call sites are inside each module's own `metrics.go`.
- `memory.missesFor` reached into the counter's unexported map and carried its
  own copy of the label separator:

  ```go
  m.Misses.mu.RLock()
  for key, slot := range m.Misses.values {
      if strings.HasPrefix(key, lookup+"\x1f") || key == lookup { ... }
  ```

  That is `PrefixSum`, hand-rolled — the fourth independent implementation, and
  the one that had to reach through an encapsulation boundary to exist. Now a
  one-line delegation.

---

## 4. Result

| | Before | After |
|---|---:|---:|
| `metrics.go` across six modules | 3,028 lines | 1,310 lines |
| Shared implementation | — | 686 lines |
| **Total** | **3,028** | **1,996** |
| Distinct implementations | 6 | 1 |
| `Sample` shapes | 3 | 1 |
| Subsystems that can export a histogram | 3 of 6 | **6 of 6** |

Net **1,032 lines deleted**, and the six-way divergence replaced by one
implementation with 26 tests.

Verified across subsystems by `evalsubjects`, the only module importing all five
engines:

```
runtime      54 instruments, 8 histogram series exported in full
conversation 61 instruments, 11 histogram series exported in full
memory       59 instruments, 5 histogram series exported in full
toolruntime  67 instruments, 5 histogram series exported in full
governance   53 instruments, 3 histogram series exported in full
evaluation   37 instruments, 3 histogram series exported in full

183 instrument names across six subsystems, no collisions
```

---

## 5. Additions beyond parity

**`Sample.Kind`** — counter, gauge or histogram, stated rather than inferred
from which fields happen to be populated. A counter reading zero and an empty
histogram were previously indistinguishable.

**`Registry.Counters/Gauges/Histograms`** — lookup accessors. The registering
methods create on miss, so an exporter asking "is `name` a histogram?" via
`Histogram(name, nil)` would have **registered an empty histogram under that
name** and then exported it. Found while writing the cross-subsystem test, which
did exactly that.

**`Registry.KindConflicts()`** — the three kinds live in separate maps, so a
registry will hold a counter and a histogram both called `latency` with nothing
clashing at registration. `Snapshot` then emits two series with the same name
and different kinds, and a name-keyed exporter merges them. A shared
implementation makes this possible for the first time: the incompatible types
previously kept subsystems apart by accident.

**`Histogram.ObserveDuration`** in seconds, with a test that fails if it ever
uses nanoseconds — every bucket set in the platform is in seconds, and a helper
using a different unit would put every observation in the overflow bucket.

**Bounds are copied and sorted** at construction, so a caller reusing its slice
cannot re-bucket a live instrument.

---

## 6. Performance

Every hot path is zero-allocation.

```
BenchmarkCounterInc-16                17793858     20.56 ns/op    0 B/op   0 allocs/op
BenchmarkCounterIncParallel-16         6162494     58.27 ns/op    0 B/op   0 allocs/op
BenchmarkGaugeSet-16                  74492518      4.779 ns/op   0 B/op   0 allocs/op
BenchmarkHistogramObserve-16          11717680     30.08 ns/op    0 B/op   0 allocs/op
BenchmarkHistogramObserveParallel-16   3040861    126.8 ns/op     0 B/op   0 allocs/op
BenchmarkHistogramQuantile-16         17036499     20.78 ns/op    0 B/op   0 allocs/op
BenchmarkSnapshot-16                      3927  87911 ns/op   55264 B/op 1238 allocs/op
```

`Snapshot` allocates, and should: it is the scrape path, called on a timer, and
it materialises cumulative bucket maps that did not previously exist in three of
the six subsystems. Paying 88 µs on a scrape to make the data reachable at all
is the trade this migration exists to make.

**No regression in the migrated modules.** Allocation counts are byte-identical
before and after on every path measured — `ExecuteScenario` 7,876 B / 74 allocs,
`RunSuiteSerial` 253,861 B / 2,049 allocs, `BehaviourPrint` 4,488 B / 11 allocs.
Wall-clock timings from this session run faster than the Phase 10F figures, but
**that is not attributed to the migration**: the same gap appears on
`GoldenBaselineLookup` and `BehaviourPrint`, which never touch metrics, and
within-session variance is ±3% while the between-session gap is ~40%. It is
machine state. See [PERFORMANCE_VERIFICATION_REPORT.md](PERFORMANCE_VERIFICATION_REPORT.md) §4.

---

## 7. Verification

All eight modules: `gofmt` clean, `go vet` clean, `go test -count=1 -shuffle=on`
passing. 512 tests, 131 benchmarks.

Doc comments in all six modules were rewritten — each carried a paragraph
explaining why it had forked, and those paragraphs were now false. Leaving them
would have been the most misleading possible outcome of a deduplication.

---

## 8. Related

- [REPOSITORY_AUDIT.md](REPOSITORY_AUDIT.md) §2
- [OBSERVABILITY_AUDIT.md](OBSERVABILITY_AUDIT.md)
