# AI Runtime Core — Performance Report

**Phase 10A** · Measured 2026-08-08

Every number here is produced by `packages/go/runtime/bench_test.go` on the
machine named below. **Nothing is estimated, extrapolated or modelled.**

---

## 1 · Method and its limits

| | |
|---|---|
| Machine | 11th Gen Intel Core i7-11800H @ 2.30 GHz, 16 logical CPUs |
| OS / arch | `windows/amd64` |
| Toolchain | Go 1.26.5 |
| Command | `go test -run='^$' -bench=. -benchtime=3000x` |
| Provider | `FakeProvider`, returns instantly |

### What is measured, and what is not

These measure the **runtime's own overhead** with an instant provider. That is
deliberate: a real model call is 400–900 ms (ADR-0011), which is four orders of
magnitude larger than anything below. Benchmarking end-to-end against a real
provider would measure the provider and tell us nothing about this code.

**Limits worth stating plainly:**

- A developer laptop, not a Graviton EKS node. Absolute figures will differ; the
  relative costs and the allocation counts will not.
- `benchtime=3000x` is a fixed iteration count, not a time-based run. Adequate
  for ranking and for allocation counts; not a substitute for a soak test.
- **Single-process only.** No network, no Kafka, no Aurora, no Redis. Those are
  where real latency lives and they are Phase 10B.
- Windows timer granularity inflates the sub-microsecond figures somewhat.

---

## 2 · Headline result

**Runtime overhead per generation: 18.6 µs serial, 7.8 µs under parallel load.**

Against the ADR-0011 p50 budget of 900 ms, the runtime consumes **0.002%**. The
budget is spent entirely on the model, the network and the carrier — which is
the correct answer, and the reason to measure was to confirm it rather than
assume it.

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Kernel_GenerateEndToEnd` | 18,562 | 9,220 | 77 |
| `Kernel_GenerateParallel` | **7,828** | 9,254 | 77 |

Parallel is *faster per operation* than serial because the serial case
serialises on one session's request accounting while the parallel case spreads
across sessions and shards. That is the shape production has.

---

## 3 · Subsystem detail

### Scheduler

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `AdmitRelease` | 548.9 | 336 | 6 |
| `AdmitReleaseParallel` | 674.4 | 352 | 6 |
| **`ShedPath`** | **64.4** | 24 | 1 |

**The shed path is 8.5× cheaper than the admit path.** This is the property that
matters: under overload, refusal is the branch taken most, and a runtime whose
refusal costs more than its acceptance collapses precisely when it is most
loaded. The ordering in `Scheduler.Admit` — cheap local checks before any
wait — is what produces this.

### Streaming

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Run64Chunks` (1 sink, 66 chunks) | 62,115 | 19,251 | **29** |
| `FanOut4` (4 sinks, 5 chunks) | 23,769 | 25,949 | 74 |
| **`AbortLatency`** | **5,290** | 272 | 5 |

Per chunk with one sink: **≈ 940 ns, 0.44 allocations.**

**Abort: 5.3 µs against a 20 ms budget — 0.027% of the allowance**, a 3,700×
margin. The measurement is against a provider that has accepted the request and
gone silent, which is the worst case: there is no chunk arriving to give the
pump an opportunity to notice.

### Sessions

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `CreateGetRemove` | 2,458 | 1,894 | 24 |
| **`GetParallel`** | **26.6** | 0 | **0** |
| `Sweep` (10,000 sessions) | 1,142,920 | 0 | 0 |

**Lookup is 26.6 ns and allocation-free under parallel contention.** This is the
sharded map earning its place: lookup is on the request path for every
generation, and `sync.Map` would be materially worse on a create/delete-heavy
workload with a periodic full scan.

**The sweep is the one number that deserves attention.** 1.14 ms for 10,000
sessions, every 5 s by default, is 0.023% duty cycle — acceptable. But it grows
linearly, and at 100,000 sessions (the configured `MaxSessions`) it would be
~11 ms. The scan holds only one shard's lock at a time, so no single lookup
waits 11 ms; the practical worst case is ~11 ms / 64 shards ≈ 180 µs of lock
hold. **Recommendation: re-measure at realistic session counts before raising
`MaxSessions`, and consider a time-ordered index if sweeps exceed ~5 ms.**

### Context window

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Append` | 118.5 | 174 | 0 |
| `AppendWithEviction` | 463.9 | 0 | 0 |
| `Assemble` (200 messages) | 7,198 | 12,496 | 2 |

Steady-state append with eviction is **464 ns and allocation-free** — the
in-place compaction avoids reallocating on every eviction, which matters because
it runs on the request path for every long session.

`Assemble` allocates 12 KB for 200 messages because it returns a **copy**. That
is intentional: handing out a slice into live state would let the next `Append`
mutate a request already in flight at a provider.

### Token counting

| Benchmark | ns/op | allocs/op |
|---|---:|---:|
| `Latin` (688 chars) | 271.3 | 0 |
| `Devanagari` (~480 chars) | 1,265 | 0 |

Devanagari costs 4.7× more per call because `utf8.RuneCountInString` scans
multi-byte sequences. Both are allocation-free and both are negligible against
a model call. The heuristic **deliberately over-counts** — under-counting causes
a provider to reject an over-long request *after* the latency is spent.

### Instruments, breaker, FSM, IDs

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Metrics_CounterInc` (parallel) | 72.4 | 24 | 1 |
| `Metrics_HistogramObserve` (parallel) | 124.4 | 0 | 0 |
| `Breaker_AllowReport` | 90.9 | 137 | 2 |
| `FSM_Transition` (incl. construction) | 838.8 | 1,136 | 14 |
| `NewID` (parallel) | 43.9 | 64 | 2 |

`FSM_Transition` includes constructing the FSM — the maps dominate. Transitions
alone are far cheaper, and an FSM is built once per session, not per request.

---

## 4 · The optimisation this report caused

The first implementation spawned a goroutine and allocated a timer **per sink,
per chunk**, so a blocking sink could be abandoned without holding the barge-in
budget open. It was correct and it was expensive:

| | Before | After | Change |
|---|---:|---:|---:|
| `Run64Chunks` ns/op | 172,114 | 62,115 | **−64%** |
| `Run64Chunks` allocs/op | 482 | 29 | **−94%** |
| `FanOut4` ns/op | 38,646 | 23,769 | −38% |
| `FanOut4` allocs/op | 178 | 74 | −58% |

**The fix:** one writer goroutine per sink for the life of the stream, with a
32-deep buffered handover channel. The common case is a non-blocking channel
send — no goroutine, no timer, no allocation. The timeout is paid only when a
sink's buffer is already full, which is the case it was always meant for.

The guarantee is unchanged: `deliver` never blocks longer than
`SinkWriteTimeout`, and only when the sink is genuinely behind.

**This optimisation introduced a deadlock, which the test suite caught.**
`stop()` waited unconditionally for a writer that could be blocked inside a slow
sink. Fixed by bounding the wait and abandoning the goroutine — it exits when
`Write` eventually returns. Recorded in ENGINEERING_AUDIT §F3, because a
performance change that introduces a hang is exactly the kind that ships
unnoticed.

---

## 5 · Capacity implications

Extrapolating carefully from the parallel figure — with the caveats in §1:

| Quantity | Figure | Basis |
|---|---|---|
| Runtime overhead per generation | 7.8 µs | measured |
| Overhead as share of p50 budget | 0.002% | 7.8 µs / 900 ms |
| Theoretical generations/sec/core, runtime-bound | ~128,000 | 1 / 7.8 µs |
| Practical ceiling | **Provider-bound, not runtime-bound** | — |

The runtime will not be the bottleneck. Concurrency is limited by
`MaxConcurrent` (default `NumCPU × 32`), by provider rate limits, and by model
latency — in that order. **The default `MaxConcurrent` is a starting point to be
re-baselined against measured production load, exactly as ADR-0011 re-baselines
the latency budget at 30 days.**

Memory: 9.2 KB per generation transient, plus per-session context (default
budget 8,192 tokens). At 10,000 concurrent sessions, context dominates and is
the figure to size on.

---

## 6 · What has not been measured

Stated so the gaps are not mistaken for results.

| Not measured | Why | When |
|---|---|---|
| Race-detector overhead | No C toolchain locally | CI, Phase 10A close |
| Real provider latency | No adapter exists yet | Phase 10B |
| Behaviour under sustained overload | Requires a load generator | Phase 10B |
| GC pressure over hours | Requires a soak test | Pre-production |
| Sweep at 100,000 sessions | Benchmarked at 10,000 | Before raising `MaxSessions` |
| Cross-AZ Redis hop | No adapter | Phase 10B |
| p99 under contention | `benchtime=3000x` reports means | Load test |

**The last one matters most.** These are averages. ADR-0011 budgets p95 and p99,
and a mean tells you nothing about a tail. Tail latency must be measured under a
realistic load generator before this runtime carries production traffic.
