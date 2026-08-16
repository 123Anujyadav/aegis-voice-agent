# Performance Report

**Phase 11B** · `packages/go/media` · Measured 2026-08-10

Every number below is copied from a benchmark run. Nothing here is estimated.

---

## Environment

```
goos: windows
goarch: amd64
pkg: github.com/callscreen/callscreen-platform/packages/go/media
cpu: 11th Gen Intel(R) Core(TM) i7-11800H @ 2.30GHz
go version go1.26.5 windows/amd64
```

Command: `go test -bench=. -benchmem -run XXX ./...` (Go's default auto-scaling
benchtime).

## Results

```
BenchmarkFrame_Validate-16              154108790      7.943 ns/op       0 B/op    0 allocs/op
BenchmarkFrame_Duration-16              736066264      1.650 ns/op       0 B/op    0 allocs/op
BenchmarkFrame_Clone-16                  15991428     84.94  ns/op     320 B/op    1 allocs/op
BenchmarkRingBuffer_WriteRead-16         21483584     56.45  ns/op       0 B/op    0 allocs/op
BenchmarkRingBuffer_Write-16             36464521     37.34  ns/op       0 B/op    0 allocs/op
BenchmarkRingBuffer_Peek-16              55862280     21.53  ns/op       0 B/op    0 allocs/op
BenchmarkJitterBuffer_PutGet-16           2809537    411.0   ns/op     416 B/op    2 allocs/op
BenchmarkPipeline_PushPump-16             1564785    812.6   ns/op     415 B/op    1 allocs/op
BenchmarkStreams_1-16                      887886   1156     ns/op     470 B/op    3 allocs/op
BenchmarkStreams_100-16                     11787  97914     ns/op   41658 B/op  201 allocs/op
BenchmarkStreams_1000-16                      990 1020767    ns/op  414526 B/op 1974 allocs/op
BenchmarkStream_ConcurrentWriteRead-16    2043991    566.8   ns/op      27 B/op    0 allocs/op
```

`BenchmarkStreams_N` performs one write, one pump and a full drain **per stream**
per iteration, so one iteration of `_1000` moves 1,000 frames.

---

## The allocation finding

`doc.go` previously claimed: *"Every steady-state operation in this package —
write, read, validate, sequence — is zero-allocation, and there are benchmarks
that fail if that changes."* **There were no benchmarks.** The claim was unbacked,
and measurement showed it was too broad.

### What is zero-allocation (confirmed)

| Operation | Cost | Allocs |
|---|---|---|
| `Frame.Duration` | 1.65 ns | **0** |
| `Frame.Validate` | 7.94 ns | **0** |
| `RingBuffer.Peek` | 21.5 ns | **0** |
| `RingBuffer.Write` | 37.3 ns | **0** |
| `RingBuffer` write+read | 56.5 ns | **0** |

`TestZeroAllocation_SteadyState` asserts these with `testing.AllocsPerRun` and
fails if any regresses.

### What allocates, and why

| Operation | Cost | Allocs | Reason |
|---|---|---|---|
| `Frame.Clone` | 84.9 ns | 1 × 320 B | Copies the payload — that is the point |
| `JitterBuffer` put+get | 411 ns | 2 × 416 B | `Put` **clones**: the buffer retains frames across calls, so borrowing the caller's payload would be a use-after-free hazard |
| `Pipeline` push+pump | 813 ns | 1 × 415 B | The same clone, seen through the pipeline |
| Full stream path | ~1,021 ns/frame | ~1.97 × 415 B | Sum of the above |

**The blanket claim was false and `doc.go` has been corrected** to state exactly
which operations are zero-allocation and which allocate by design, with these
figures. The benchmark was not weakened to preserve the claim.

### What this costs at scale

At 1,000 concurrent streams and 50 fps — 50,000 frames per second:

| Measure | Value |
|---|---|
| Allocations | ~98,500 /s |
| Garbage produced | ~20.7 MB/s |

This is precisely the pressure `doc.go` warns about, and it is real. **It is the
largest remaining optimisation in the package.** The fix is to give the jitter
buffer its own backing array, as `RingBuffer` already has; it is not done here
because the jitter buffer reorders — frames move position after insertion — and a
contiguous arena that supports reordering is a materially harder structure than a
ring. Recorded as F-2 in [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md).

---

## Frame budget

A 20 ms frame at 50 fps gives each frame a **20,000,000 ns** wall-clock budget per
stream. The engine's cost per frame at 1,000 streams is **~1,021 ns**.

| Measure | Value |
|---|---|
| Frames moved per second (measured) | ~980,000 |
| Frames per second required at 1,000 streams | 50,000 |
| **Headroom** | **~19.6×** |
| Engine work per 20 ms window at 1,000 streams | 1.02 ms (~5.1%) |

Scaling is close to linear: 1 stream 1,156 ns, 100 streams 97,914 ns (979 ns per
stream), 1,000 streams 1,020,767 ns (1,021 ns per stream). Per-stream cost rises
about 4% between 100 and 1,000 streams, which is registry sharding and metric
observation, not contention on a shared buffer.

`BenchmarkStream_ConcurrentWriteRead` — a producer and a consumer hammering one
stream from parallel goroutines — costs 567 ns/op at **0 allocs/op**, confirming
the per-stream lock is not a bottleneck at this frame rate.

---

## What is **not** measured

Stated plainly, because a performance report that implies more coverage than it
has is worse than none.

- **No network.** No socket, no packet loss, no real jitter. Arrival variation is
  injected through a `FakeClock`.
- **No carrier.** No RTP, no SIP, no real telephony provider.
- **No codec.** PCM only — no encode, decode, or resample cost is included.
- **No multi-process or multi-host.** One process, one machine.
- **No soak test.** The longest sustained run is
  `TestStress_LongRunningStreamDoesNotGrow` at 10,000 frames (200 s of audio),
  which is not an endurance test.
- **No GC pause measurement.** Allocation *rate* is measured; the resulting pause
  distribution under a production heap is not.
- **Single hardware profile.** One laptop-class CPU, Windows, amd64. No ARM, no
  server-class part, no container limits.
- **`-race` was never run** — the environment has no C compiler. Benchmarks say
  nothing about data races. See F-1 in
  [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md).
