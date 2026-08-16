# Performance Report

**Phase 11C** · `packages/go/speech` · Measured 2026-08-11

Every number below is copied from a benchmark run. Nothing here is estimated.

---

## Environment

```
goos: windows
goarch: amd64
pkg: github.com/callscreen/callscreen-platform/packages/go/speech
cpu: 11th Gen Intel(R) Core(TM) i7-11800H @ 2.30GHz
go version go1.26.5 windows/amd64
```

Command: `go test -bench=. -benchmem -run XXX ./...` (Go's default auto-scaling
benchtime).

## Results

```
BenchmarkAssembler_ApplyPartial-16          5032821    327.0 ns/op      60 B/op    0 allocs/op
BenchmarkAssembler_Finalize-16              1865695    680.1 ns/op     904 B/op    7 allocs/op
BenchmarkAssembler_RejectStalePartial-16   14104554     90.56 ns/op      0 B/op    0 allocs/op
BenchmarkChunker_SegmentEnglish-16           494689   2507    ns/op    3256 B/op   19 allocs/op
BenchmarkChunker_SegmentDevanagari-16        425391   2945    ns/op    3107 B/op   16 allocs/op
BenchmarkChunker_SegmentHinglish-16          460582   2295    ns/op    3096 B/op   20 allocs/op
BenchmarkChunker_FirstClause-16             1000000   1438    ns/op    2648 B/op   11 allocs/op
BenchmarkRouter_PickSTT-16                 28982566     45.87 ns/op       0 B/op    0 allocs/op
BenchmarkRouter_PickUnderOpenCircuit-16    16490130     73.06 ns/op       0 B/op    0 allocs/op
BenchmarkRouter_Report-16                  41797282     28.35 ns/op       0 B/op    0 allocs/op
BenchmarkEvents_Publish-16                  3276354    438.6 ns/op     788 B/op    0 allocs/op
BenchmarkEvents_Topic-16                   75545342     15.69 ns/op       0 B/op    0 allocs/op
BenchmarkTurn_BeginAndConclude-16            222933   7415    ns/op    4732 B/op   42 allocs/op
BenchmarkTurn_NotePartial-16               40473676     28.67 ns/op       0 B/op    0 allocs/op
BenchmarkSTT_PushFrame-16                   3737941    351.3 ns/op     859 B/op    1 allocs/op
BenchmarkSession_Interrupt-16                 84417  12512    ns/op    5411 B/op   44 allocs/op
BenchmarkSessions_10-16                        2260 503934    ns/op  604992 B/op  222 allocs/op
BenchmarkSessions_100-16                        506   2591367 ns/op 6045921 B/op 2208 allocs/op
```

---

## Against the frozen budget

ADR-0011 owns the end-to-end budget. **Only two of its hops are ones this package
owns end to end.** The other two speech hops are dominated by a provider network
call this phase never makes, so what is measured here is *orchestration
overhead*, reported as that and never presented as the hop total.

### Hop 7 — sentence segmentation: 15 ms p50 / 40 ms p95

The budgeted operation is "first speakable clause".

| Measurement | Value | Budget | Margin |
|---|---|---|---|
| `BenchmarkChunker_FirstClause` | **1.44 µs** | 15 000 µs p50 | **~10 400×** |
| Full English utterance | 2.51 µs | — | |
| Full Devanagari utterance | 2.95 µs | — | |
| Full Hinglish utterance | 2.30 µs | — | |

**PASS, with enormous margin.** Devanagari costs about 17% more than English per
utterance, which is the rune-decoding cost of a multi-byte script — expected, and
irrelevant at this scale.

### Barge-in: ≤ 20 ms (one frame interval)

| Measurement | Value | Budget |
|---|---|---|
| `TestBargeIn_LatencyIsWithinFrozenBudget`, worst of 10 real-clock runs | **0 s** | 20 ms |
| `BenchmarkSession_Interrupt` | **12.5 µs** | 20 000 µs |

**PASS.** The test reports 0 s because the operation completes below the clock's
resolution on this machine; the benchmark, which amortises over many iterations,
puts it at **12.5 µs** — about **1 600×** inside budget.

**Read this precisely.** It measures the *orchestration* cost of an interruption:
generation bump, provider stream close, turn transition, new turn. It does **not**
measure end-to-end barge-in, which additionally includes endpoint detection, the
media relay hop and the carrier leg — none of which this package implements.

### Hops this package does not own

| Hop | Budget | Why not measured here |
|---|---|---|
| STT final after end-of-speech (ADR-0005) | 120 / 250 ms | Dominated by a provider network call. This package contributes `BenchmarkAssembler_ApplyPartial` at 327 ns and `Finalize` at 680 ns — under 0.001% of the budget |
| TTS time-to-first-byte (ADR-0007) | 90 / 180 ms | Same. This package contributes segmentation (1.44 µs) and chunk scheduling |

The honest summary: **this package's own overhead is negligible against every hop
it participates in.** Whether the pipeline meets ADR-0011 depends almost entirely
on provider latency, which is unmeasured here because no provider was called.

---

## Allocation

### Zero-allocation, confirmed

| Operation | Cost | Allocs |
|---|---|---|
| `EventSpeechPartial.Topic()` | 15.7 ns | **0** |
| `Router.Report` | 28.4 ns | **0** |
| `SpeechTurnManager.NotePartial` | 28.7 ns | **0** |
| `Router.PickSTT` | 45.9 ns | **0** |
| `Router.PickSTT` under an open circuit | 73.1 ns | **0** |
| `Assembler.Apply` rejecting a stale partial | 90.6 ns | **0** |
| `Assembler.Apply` applying a partial | 327 ns | **0** |

`TestZeroAllocation_HotPath` guards the operations measured at zero.

Two entries report bytes with zero allocations (`ApplyPartial` 60 B,
`Events_Publish` 788 B): that is amortised slice growth, not a per-operation
allocation.

### Allocating by design

| Operation | Allocs | Why |
|---|---|---|
| `STTOrchestrator.Push` | 1 × 859 B | **The frame clone.** Deliberate: `media.Frame` payloads are borrowed and this is where retention begins. Removing it would corrupt audio |
| `Assembler.Finalize` | 7 × 904 B | Turn-state creation plus map allocation, once per turn |
| Chunker, per utterance | 16–20 × ~3.1 kB | String building. Once per reply, not per frame |
| `Turn.Begin`+conclude | 42 × 4.7 kB | FSM construction per turn. **The largest single figure here** |
| `Session.Interrupt` | 44 × 5.4 kB | Dominated by the new turn's FSM |

**On the 42-allocation turn cost (F-5):** a turn is created once per utterance,
not once per frame. At a generous 20 turns per minute that is 14 allocations per
second per call — three orders of magnitude below the frame path Phase 11B
worries about. It is recorded rather than optimised because optimising it would
mean pooling FSMs, and a pooled state machine that leaked state between turns
would be a correctness bug traded for an irrelevant allocation win.

### Session scaling

| Sessions | Time per open+close cycle | Per session |
|---|---|---|
| 10 | 504 µs | 50.4 µs |
| 100 | 2 591 µs | 25.9 µs |

Per-session cost **halves** between 10 and 100, which is fixed setup amortising.
Memory is linear: 605 kB for 10, 6.05 MB for 100 — about **60 kB per session**,
dominated by the two bounded queues each session pre-allocates.

At the default `MaxSessions` of 1 000 that projects to roughly 60 MB of queue
memory. That projection is arithmetic, **not measured** — `BenchmarkSessions_1000`
was not run.

---

## What is **not** measured

Stated plainly, because a performance report that implies more coverage than it
has is worse than none.

- **No provider was ever called.** Every measurement uses in-process fakes.
  Nothing here says anything about Google, Deepgram, Sarvam, ElevenLabs,
  Cartesia, Whisper or Piper latency — or accuracy.
- **No network.** No sockets, no RPC, no TLS handshake, no connection setup.
- **No real audio.** Frames are zeroed buffers of the correct size. No codec, no
  resampling, no acoustic work.
- **No end-to-end turn.** The ADR-0011 900 ms p50 figure is a pipeline property;
  this package is one participant and cannot measure it alone.
- **p50/p95 are not reported for most operations.** Go benchmarks report a mean.
  Where the brief asks for percentiles, the honest answer is that the
  distribution was not captured — only the barge-in test reports a worst case,
  over 10 runs.
- **No soak.** The longest run is a benchmark loop, not an endurance test.
- **Single machine, single hardware profile.** One laptop-class CPU, Windows,
  amd64. No ARM, no server part, no container limits.
- **`-race` was never run** — no C compiler in this environment. Benchmarks say
  nothing about data races. See F-1 in
  [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md).
