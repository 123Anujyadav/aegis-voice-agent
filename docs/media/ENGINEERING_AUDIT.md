# Engineering Audit

**Phase 11B** · `packages/go/media` · Audited 2026-08-10

Every figure in this document was produced by a command that was run. Where
something could not be verified, it says so.

---

## 1. Brief compliance

| § | Requirement | Status | Where |
|---|---|---|---|
| 1 | Media Runtime — `MediaRuntime`, `MediaCoordinator`, `MediaScheduler`, `MediaDispatcher`, `MediaRegistry`, `MediaMetrics` | **Met** — all six exist | `runtime.go`, `registry.go`, `metrics.go` |
| 2 | Stream lifecycle — Create, Start, Pause, Resume, Drain, Flush, Stop, Recover, Destroy | **Met**, under this module's names | `Registry.Create`, `Coordinator.Open/Pause/Resume/Drain/Close/Fail`, `Stream.Flush`, `Runtime.Start/Stop`, `recoverStreams`, `Registry.Remove` |
| 3 | State machine — nine states, no implicit transitions | **Met** | `state.go`; see [STATE_TRANSITIONS.md](STATE_TRANSITIONS.md) |
| 4 | Frame model — PCM16/PCM32, mono/stereo, rate, timestamp, sequence, duration, codec extensibility | **Met** | `frame.go` — `SampleFormat`, `ChannelLayout`, `SampleRate`, `Codec` |
| 5 | Buffer engine — ring, queue, backpressure, overflow, underflow, snapshots | **Met** | `buffer.go`, `jitter.go`; see [BUFFER_LIFECYCLE.md](BUFFER_LIFECYCLE.md) |
| 6 | Streaming pipeline — input, validation, ordering, timestamp, sequencing, delivery, drop policy, recovery | **Met** — five stages | `pipeline.go`; see [STREAMING_PIPELINE.md](STREAMING_PIPELINE.md) |
| 7 | Jitter — measurement, buffer, reordering, late and early handling | **Met** | `jitter.go` — `JitterEstimator`, `JitterBuffer` |
| 8 | Clock and timing — media clock, frame clock, sync, monotonic, drift | **Met** | `clock.go` — `MediaClock`, `FrameClock` |
| 9 | Recovery — reconnect, resume, buffer recovery, frame recovery, graceful shutdown | **Met** | `recovery.go`, `stream.go` (`RestoreStream`), `runtime.go` (`drain`) |
| 10 | Metrics — frame rate, drops, buffer depth, latency, recovery count, throughput | **Met** | `metrics.go` |
| 11 | Testing — unit, integration, streaming, concurrency, stress, recovery, buffer, failure injection | **Met** — all eight categories | see [EVALUATION_REPORT.md](EVALUATION_REPORT.md) |

Naming note for §2: the brief's "Start" and "Stop" are runtime-level verbs here
(`MediaRuntime.Start`/`Stop`); a stream's equivalent pair is `Open` and `Close`.
"Destroy" is `MediaRegistry.Remove`, which is what frees the buffer. No verb in
the brief is unimplemented; three are spelled differently.

## 2. Prohibited technology

```
grep -rniE "pion|janus|mediasoup|livekit|twilio|agora|daily|jitsi|webrtc|\brtp\b|\bsip\b" \
  packages/go/media/*.go | grep -v "_test.go"
```

Three hits, all in comments that **disclaim** the technology:

```
doc.go:13:  // There is no RTP, no WebRTC, no SIP, no socket, no codec, no resampler, no
doc.go:20:  // this package ever needs to parse an RTP header, something has gone wrong
jitter.go:16: // This engine implements no RTP. But the jitter formula RTP uses is simply the
```

No import, no use. `jitter.go:16` is worth reading in full: the engine implements
no RTP transport but does use RFC 3550's *jitter formula*, because a smoothed
mean deviation between media interval and arrival interval is simply the right
estimator, and reinventing a worse one to avoid resembling a specification would
be perverse.

## 3. Dependencies

`go.mod` requires exactly two modules — `packages/go/runtime` and
`packages/go/metrics` — both dependency-free. The transitive closure is the Go
standard library. No broker client, no database driver, no media library.

`GOWORK=off go build ./...` passes, proving the module is self-sufficient rather
than leaning on the workspace.

## 4. Defects found and fixed

Six. Four were predicted from the failing tests; **two were found only because
fixing the first four exposed them.**

### D-1 — The pump and sweep loops ran on a wall clock

`pumpLoop` and `sweepLoop` built their tickers with `time.NewTicker` while every
other timing decision in the package measured against the injected
`runtime.Clock`. Under a `FakeClock` neither loop could be driven, so frames
never moved from the jitter buffer to the readable ring and `Read` returned
`ErrBufferEmpty` on healthy, paused and draining streams alike.

**Fixed:** both now use `r.clock.NewTicker`. `Stream.Read` additionally pumps
read-through when the ring is empty, so a consumer never starves because the pump
was late. Guarded by `TestRead_PumpsThroughWhenRingIsEmpty`.

**This defect was hiding two others** — see D-5 and D-6.

### D-2 — Snapshots captured only the output ring

`Stream.Snapshot` read `pipeline.Buffer().Snapshot()` and nothing else. Audio held
in the jitter buffer was invisible, so opting into `IncludeAudio` on a live stream
routinely captured **nothing** and recovery restored silence — defeating the
entire purpose of buffer recovery.

**Fixed:** captures both stages in playout order via a new `JitterBuffer.Peek`,
with `MaxAudioFrames` bounding the union. Recovery now restores 5 frames in the
case that previously restored 0. Guarded by
`TestSnapshot_CapturesJitterHeldAudioNotJustTheRing` and
`TestSnapshot_BoundAppliesToBothStagesCombined`.

### D-3 — The jitter window collapsed under adaptation

Two halves of one defect.

**(a)** The too-early bound was `playout + current + MaxDelay`. `playout` advances
only on release, and `current` shrinks 5 ms per frame toward `MinDelay` when the
line is clean. With an idle consumer the bound fell from 200 ms to 160 ms while
frame 9 arrived at 180 ms — **a perfectly clean 50 fps sequence was refused.**

**(b)** Fixing (a) exposed that the *release gate* had the same shape: `playout`
was anchored under the delay in force at the time, so shrinking `current`
afterwards moved `playout + current` **backwards**, retroactively making already-due
frames un-due and stalling the buffer.

**Fixed:** (a) the too-early bound is anchored on a new `frontier` that advances
on arrival, so a stalled consumer cannot freeze it. (b) a monotonic
`releaseFrontier` latches the high-water mark of `playout + current`, so
adaptation can no longer retract a decision already made. `playout` itself is
untouched, because late detection is gated on it — an earlier attempt to shift
`playout` to compensate reported the next perfectly-on-time frame as late.

Guarded by `TestJitter_CleanSequenceIsFullyAccepted` and
`TestJitter_RefusesFramesFarAheadOfTheData`.

### D-4 — A single lost frame stalled the stream permanently

Presented as "gap fill synthesises no silence". **It was not a gap-fill defect.**

Diagnosis (instrumented, not assumed): `fillGaps` was never reached for the frame
after the gap. The frame was accepted and held, but never became due —
`playout=60ms`, frame at `140ms`, `held=1`, `ready=0`, forever. `playout` advances
only on release, and release requires `playout` to reach the frame. When a
sequence goes missing, **neither can ever move.** One lost packet ends the stream.

**Fixed:** when a hole blocks the head and nothing is due, the buffer steps over
it — moves `playout` to the head and releases — and the pipeline synthesises
bounded silence. Waiting cannot help: the missing sequences are older than audio
already held, so if they arrive they arrive late and are refused as late, which is
the same outcome minus the stall. Holes stepped over are counted (`skipped`), so
a rising value reads as packet loss.

Guarded by `TestJitter_LostFrameDoesNotStallTheStream`.

**Answering the plan's question directly:** RC-4 was an **independent defect**,
not a downstream symptom of D-1/D-3. It survived all three earlier fixes, and its
real cause was materially more serious than the symptom suggested.

### D-5 — Stall detection timed out healthy streams *(found by D-1)*

`Sweep` measured staleness from `Stream.UpdatedAt()`, which moves only on a state
**transition**. A healthy stream carrying audio for an hour transitions exactly
never, so every long stream was declared stalled the moment it exceeded
`StallTimeout`.

Invisible before D-1 because `sweepLoop` ran on a wall clock and never fired
inside a 50 ms test. The moment the sweep became testable it started killing
streams — `TestStress_LongRunningStreamDoesNotGrow` failed with
`stream is timeout` at frame 254.

**Fixed:** a `lastFrameAt` field and a `LastActivity()` predicate returning the
later of the two. A second refinement followed: `lastFrameAt` is recorded whether
or not the frame was **accepted**, because a frame refused for backpressure still
proves the source is alive. Counting only accepted frames meant a stalled
*consumer* eventually looked like a dead *producer*, and the runtime destroyed a
stream whose source was healthy throughout.

### D-6 — The pump destroyed frames instead of applying backpressure *(found by D-3)*

`Pump` took frames out of the jitter buffer and handed them to a full ring, which
refused them. The frame was gone and **the producer was never told**. Push kept
accepting, so an overloaded stream silently destroyed audio indefinitely while
reporting healthy acceptance.

Additionally, drops occurring during `Pump` never reached `MediaMetrics` — only
push-path drops were counted — so the loss was invisible to operators.

Masked before D-3 because spurious `too_early` refusals were producing the drops
the test was actually observing.

**Fixed:** the pump stops when the ring is full and leaves frames held, so the
jitter buffer fills to capacity and `Put` begins refusing — real backpressure the
producer sees. `DropOldest` is exempt, since that policy is a deliberate statement
that the newest audio wins. A `Pipeline.DroppedTotal()` counter lets `PumpAll`
report pump-path drops to the metrics.

Measured result: 500 frames offered to a stalled consumer → 82 accepted, **418
refused and reported**, both buffers bounded, stream still `active`.

## 5. Open findings

| # | Finding | Severity | Status |
|---|---|---|---|
| F-1 | **`-race` was never run.** The environment has no C compiler (`gcc` absent, `CGO_ENABLED=1` fails), and Go's race detector requires cgo | **High** | **Unresolved — environment limitation.** Mitigated by `-count=10` and three concurrency tests, neither of which is a substitute |
| F-2 | The full frame path costs ~2 allocations and 415 B per frame, against a package doc that claimed zero | Medium | Measured, `doc.go` corrected, analysis in [PERFORMANCE.md](PERFORMANCE.md). Not fixed — see below |
| F-3 | The shutdown drain budget uses real time, not the injected clock | Low | **Deliberate.** Documented at `runtime.go:335`. Phase 11A shipped a drain taking its deadline from the injected clock while polling a real ticker, and under a `FakeClock` `Stop` spun forever. Converting it would reintroduce that bug |
| F-4 | `events.go` defines the event model but the runtime does not yet publish | Low | Matches Phase 11A, which shipped the same port unwired. Wiring belongs to the service phase |
| F-5 | Benchmarks run on one machine, single process, no network | Low | Stated in [PERFORMANCE.md](PERFORMANCE.md) |
| F-6 | **`golangci-lint` was never run.** Not installed in this environment | Medium | **Unresolved — environment limitation.** `go vet` passes, but the repo's `.golangci.yml` configures considerably more than vet. Should be run in CI before merge |

**On F-2**, the fix is real but out of scope for this phase: it means giving the
jitter buffer its own backing array, as `RingBuffer` already has. The jitter
buffer reorders — frames move position after insertion — and a contiguous arena
supporting reordering is a materially harder structure than a ring. It is the
largest remaining optimisation in the package and it is recorded rather than
attempted.

**On F-1**, this is the most significant gap in the phase. Three concurrency
tests pass and the suite is stable across ten repetitions, but neither detects a
data race the way the race detector does. Any environment with a C toolchain
should run `go test -race ./...` before this module carries production traffic.

## 6. Scope

Files modified or created: **only** under `packages/go/media/` and `docs/media/`.
No file in any frozen phase was touched. `packages/go/runtime` and
`packages/go/metrics` are consumed, never edited.
