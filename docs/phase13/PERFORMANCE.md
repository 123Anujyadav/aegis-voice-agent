# Phase 13 — Performance

**Measurement, not optimization.** No production code was changed to improve any
number here. No optimization is claimed, because no before/after optimization
was performed.

## Category separation

**Category A — orchestration / deterministic local logic:** everything below.

**Category B — provider / model inference: NOT RUN.** Phase 13 performs **no**
provider or model inference. No model latency was invented, no TTFT comparison
was made, and no Category A number may be compared with a model figure.

Phase 11E voice-pipeline benchmarks exist elsewhere in the repository and are
**historical context only** — they are not reproduced here and must not be mixed
with these classifier figures.

## Environment

Go 1.26.5 · windows/amd64 · 11th Gen Intel Core i7-11800H @ 2.30GHz · 16 logical
CPUs (the `-16` suffix is GOMAXPROCS) · CGO_ENABLED=0.

Commands:
```
go test ./packages/go/intent     -run '^$' -bench . -benchmem -benchtime=1s -count=5
go test ./packages/go/voiceintel -run '^$' -bench . -benchmem -benchtime=1s -count=5
```

Values are the **median of 5 samples**; spread is (max-min)/median.

## Methodology

`testing.B` controls all iteration counts. No polling, no sleeps, no single-shot
wall-clock timing, no fixed-iteration assumptions. Every benchmark **validates
its fixture before `b.ResetTimer()`** and fails with `b.Fatalf` if the operation
does not produce the expected result — a benchmark that silently measures an
error path reports a fast number for the wrong work. Results are assigned to
package-level sinks so the compiler cannot elide the calls. `b.ReportAllocs()`
throughout.

### Clock-resolution caveat

Go's `ns/op` is an **amortized** figure across `testing.B`-calibrated iterations.
Sub-20 ns results are therefore valid despite Windows timer granularity being far
coarser — they are **not** single-operation wall-clock readings and must not be
quoted as such.

## Intent classification

| Benchmark | ns/op | spread | B/op | allocs |
|---|---|---|---|---|
| `Classify/Normal` | 2307 | 3% | 928 | 18 |
| `Classify/Ambiguous` | 1505 | 4% | 832 | 15 |
| `Classify/Unknown` | 1364 | 4% | 432 | 8 |

### Scaling by input length

| Tokens | ns/op | B/op | allocs |
|---|---|---|---|
| 1 | 452 | 312 | 3 |
| 8 | 2839 | 944 | 20 |
| 64 | 18760 | 3440 | 87 |
| 512 | 133951 | 24688 | 602 |
| 1024 | 135251 | **24688** | **602** |

1024 tokens matching 512 **exactly** on allocations is the `maxTokens = 512`
bound working — `tokenize` returns early (`lexicon.go:247`). The 1% time
difference is noise, not a speedup.

## Confidence calculation (internal, production `score()`)

| Benchmark | ns/op | B/op | allocs |
|---|---|---|---|
| `ConfidenceScoring/hit` | 139.5 | 0 | 0 |
| `ConfidenceScoring/miss` | 103.1 | 0 | 0 |
| `ConfidenceScoring/widest_rule` | 302.7 | 0 | 0 |
| `Tokenize/short` | 123.8 | 64 | 4 |
| `Tokenize/typical` | 460.9 | 320 | 13 |
| `Tokenize/long` | 26475 | 22896 | 522 |
| `SortCandidates` (incl. copy) | 174.2 | 96 | 3 |
| `ExtractSlots` | 272.8 | 200 | 4 |

Confidence scoring is **allocation-free**.

## Turn classification

| Benchmark | ns/op | B/op | allocs |
|---|---|---|---|
| `ClassifyTurn/NewRequest` | 331.6 | 144 | 7 |
| `ClassifyTurn/Cancellation` | 168.2 | 64 | 4 |
| `ClassifyTurn/Silence` | 19.0 | 0 | 0 |
| `ClassifyTurn/Interruption` | 18.6 | 0 | 0 |

## Context (frozen engine, via the bridge)

| Benchmark | ns/op | spread | B/op | allocs |
|---|---|---|---|---|
| `ContextInsert` window=1 / 16 / 128 | 130 / 129 / 132 | <=2% | 7 | 0 |
| `ContextLookup/Get/hit` | 45.1 | 2% | 0 | 0 |
| `ContextLookup/Get/miss` | 27.8 | 4% | 0 | 0 |
| `Lookup/hit_first_scope` | 85.4 | 2% | 0 | 0 |
| `Lookup/miss_all_scopes` | 130.1 | 2% | 0 | 0 |
| `Size` (128 entries) | 1966 | 2% | 0 | 0 |
| **`ContextEviction`** | **3727** | 2% | 7 | 0 |

Eviction is roughly **29x** an insertion — `evictOldestLocked` is a linear scan
of the 256-entry scope (`context.go:233`). The two are separate benchmarks so
that difference is visible rather than averaged away. `Size` is O(n) expiry
checking (~15 ns/entry).

## Response strategy (full turn through the production seam)

| Benchmark | ns/op | spread | B/op | allocs |
|---|---|---|---|---|
| `ResponseStrategy/Respond` | 61414 | 12% | 45028 | 711 |
| `ResponseStrategy/AskMissingSlot` | 58000 | 12% | 45694 | 741 |
| `ResponseStrategy/Unknown` | 54182 | 6% | 43931 | 694 |
| `SessionSetupOnly` (control) | 41599 | 6% | 37381 | 634 |
| **`TurnOnReusedSession`** | **8079** | 2% | 5100 | 51 |

The first three build a fresh bridge and session per iteration.
`SessionSetupOnly` exists so that cost can be subtracted honestly; the direct
warm-session measurement — **8.1 microseconds per turn** — is the figure to use.
Median subtraction across separately-calibrated benchmarks is indicative only.

## Concurrency

| Benchmark | ns/op | B/op | allocs |
|---|---|---|---|
| `Classify_Parallel` (RunParallel) | 1020 | 928 | 18 |
| `ConcurrencyScaling` 1 / 2 / 4 / 8 / 16 goroutines | 158921 / 123620 / 98853 / 87726 / 84854 | ~59.5-60.7 KB | ~1154-1169 |
| `ConcurrentSessions` 1 / 4 / 16 | 14782 / 47786 / 167901 | 5.3 / 20.2 / 79.6 KB | 56 / 208 / 817 |
| `ContextConcurrentAccess/read_only` | 137.1 | 0 | 0 |
| `ContextConcurrentAccess/mixed_read_write` | 214.2 | 2 | 0 |

`ConcurrencyScaling` holds total work constant (64 classifications per op), so
ns/op is directly comparable: **1 to 16 goroutines is 1.87x faster** —
sub-linear, as expected for ~2.3 microsecond units of work. Shared-classifier
parallel throughput (1020 ns) beats sequential (2307 ns), confirming the shared
immutable classifier does not serialize.

## Benchmark counts

26 benchmark results in `intent`, 19 in `voiceintel` (`go test -bench .`,
counting reported `Benchmark` lines).

## Harness defects found and fixed (T12)

1. **`fmt.Sprintf` inside timed loops** — the significant one. `Get/hit`
   reported 5 B/op while `Get/miss` reported 0, purely because the miss case used
   a constant key. After precomputing keys: `Get/hit` **126 -> 45.1 ns, 5 -> 0
   B/op**; insert 200 -> 130 ns; parallel read 213 -> 137 ns; mixed 494 -> 214 ns.
   Roughly **64% of the previously reported `Get` cost was harness overhead.**
2. **A mislabelled input-length case** — `tokens=1` actually fed 7 words.
3. **Unbounded-turn assumption** — benchmarks hit the frozen conversation-length
   bound; sessions are now rebuilt off the clock via `StopTimer`.

These are corrections to measurement, not to production code.

## Not claimed

- No performance target, budget or SLO.
- No optimization claim (no before/after optimization was done).
- No comparison to Phase 12 **F2** (`LatencyRatio: 2.0`), which is **OPEN and
  OUT OF SCOPE** — no Phase 13 number targets it or claims compliance with it.
- CI benchmark runs: **NOT RUN** (`hardening.yml` records benchmarks
  ungated at `-benchtime=100ms`, but CI has not executed).
