# Performance

**Phase 11D** · Measured 2026-08-11

Every number here came from a command in this document. Nothing is estimated.

---

## Environment

```
goos: windows
goarch: amd64
cpu: 11th Gen Intel(R) Core(TM) i7-11800H @ 2.30GHz
go version go1.26.5 windows/amd64
```

**Windows clock resolution is roughly 520 µs** (established in Phase 10F).
Anything reported as `0 s` below means *under that floor*, not instant.

## Command

```bash
cd packages/go/audiointel
go test -bench=. -benchmem -run=XXX -benchtime=200ms ./...
```

## Per-frame cost

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `FrameAnalyzer_Analyze` (8 kHz PCM16, 160 samples) | 488.1 | 0 | **0** |
| `FrameAnalyzer_AnalyzePCM32` (16 kHz PCM32, 320 samples) | 1037 | 0 | **0** |
| `FeatureWindow_PushOnly` | 12.83 | 0 | **0** |
| `FeatureWindow_PushAndStats` (100-frame window) | 351.0 | 0 | **0** |
| `NoiseAnalyzer_Observe` | 218.1 | 0 | **0** |
| `SpeechDetector_Observe` | 57.88 | 0 | **0** |
| `ContinuityDetector_Observe` | 29.64 | 0 | **0** |
| `QualityAnalyzer_Observe` | 73.97 | 0 | **0** |
| `DetectorRig_FullChain` | 1520 | 0 | **0** |
| `Session_Analyze` (recording publisher) | 1737 | 5 | **0** |
| `Session_AnalyzeWithoutRecording` | 1703 | **0** | **0** |

### On the 5 B/op

`Session_Analyze` uses a `RecordingEventPublisher`, which **retains** every event
and therefore grows a slice. Those bytes belong to the test recorder, not to the
analysis. `Session_AnalyzeWithoutRecording` runs the same path with
`NopEventPublisher` and measures **0 B/op** — which is what §19's bounded
hot-path claim is about.

### What 1.7 µs per frame means

At a 20 ms cadence one session consumes about **0.0085% of one core**. A
thousand concurrent sessions is roughly 8.5% of one core for analysis.

## Scaling across sessions

```bash
go test -bench=BenchmarkSession_ConcurrentSessions -benchmem -run=XXX ./...
```

| Sessions | ns/op | allocs/op |
|---:|---:|---:|
| 1 | 1665 | 0 |
| 8 | 348.0 | 0 |
| 64 | 406.5 | 0 |
| 256 | 417.7 | 0 |

`ns/op` here is wall time per operation across parallel goroutines, so it falls
as work spreads over cores and then flattens. The flatness from 8 to 256 is the
point: **sessions do not contend.** Two sessions share no detector, no window,
no floor estimate and no lock, so the only shared structure is the sharded
registry, which is not on the frame path.

## Latency against the frozen budgets

Two budgets exist. This phase invented neither and creates no new SLA.

### Endpoint detection — ADR-0011 §5.2 hop 1, 250 ms p50 / 350 ms p95

| Measurement | Result |
|---|---|
| Endpoints observed | 10 |
| Worst silence held at confirmation | **260 ms** |
| Configured window | 250 ms |

The 10 ms is one frame of overshoot at a 20 ms cadence — confirmation happens on
the first frame at or past the window.

**This is a synthetic measurement, not a production distribution.** Ten turns of
generated audio is not a p50 or a p95, and presenting it as one would be
inventing an SLA. What is asserted is that the engine confirms within one frame
of its configured window, which is the part this package controls. The window
itself is the product decision, and ADR-0011 §7 says it is tuned by measuring
false-endpoint rate rather than latency.

### Barge-in — ADR-0004 §12, one frame interval (20 ms)

| Measurement | Result |
|---|---|
| Worst orchestration latency, 10 runs, **real clock** | **0 s** |
| Phase 11C's own cancellation, same run | **0 µs** |

Measured on `rt.SystemClock`, not a `FakeClock` — every other test injects a
fake, and a wall-clock claim measured on a fake clock asserts nothing. Phase 11C
does the same for its equivalent test.

**Read it precisely.** It means the orchestration cost — the detection stamp,
the policy checks, the call through the port, Phase 11C's generation bump and
stream close — is below this machine's clock resolution. It does **not** mean
end-to-end barge-in is instant. The real path additionally includes the media
relay and the carrier leg, neither of which this package implements or measures.

### Costs this phase adds to the barge-in path

| Component | Cost | Note |
|---|---|---|
| Onset confirmation (`MinOnsetFrames` = 2) | 40 ms | **Upstream of the 20 ms budget**, which runs from detection |
| `ConfirmFrames` = 0 (default) | 0 ms | — |
| `ConfirmFrames` = 3 (measured) | 60 ms | Why the default is 0 |

The 40 ms is reported as `BargeInDecision.OnsetLatency`, separately from
`Latency`, so the two can never be added together by accident.

## Detection bounds

Structural bounds, measured against synthetic worst cases.

| Property | Bound | Measured |
|---|---|---|
| Steady tone treated as speech before reclassification | 440 ms | **340 ms** |
| Onsets under 60 ms speech/silence alternation | ≤ 11 in 136 frames | **1** |
| Noise floor rise from one full-scale ungated frame | 0.12 dB | within clamp |

## Memory

Every window is a fixed-size array sized at construction:

| Structure | Default size | Bytes |
|---|---:|---:|
| `FeatureWindow` | 100 frames | ~9.6 KB |
| `NoiseAnalyzer` ring | 100 floats | 800 B |
| `ContinuityDetector` ring | 100 bools | 100 B |
| `OverlapDetector` echo rings | 2 × 16 floats | 256 B |

About **11 KB per session**, constant. `TestWindow_NeverGrows` pushes a million
frames and asserts the backing array is unchanged in length and capacity.

`Reset` rebuilds the two FSM-backed detectors and allocates a map each. It is a
recovery operation and never on the frame path.

## What is not measured here

- **Real telephony audio.** Every fixture is synthetic. See
  [EVALUATION_REPORT.md](EVALUATION_REPORT.md).
- **End-to-end conversation latency.** ADR-0011's other ten hops belong to other
  services.
- **Behaviour under `-race`.** The race detector could not run — no C compiler
  in this environment. See [EVALUATION_REPORT.md](EVALUATION_REPORT.md) §1.
