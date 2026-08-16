# Enterprise Media Streaming Engine — Documentation

**Phase 11B** · `packages/go/media` · Status: **PROPOSED — awaiting approval**

Real-time audio transport between a producer and a consumer. Built from scratch —
**no Pion, Janus, mediasoup, LiveKit, Twilio Media, Agora, Daily or Jitsi SDK, no
RTP stack, no WebRTC stack, and no streaming framework of any kind.**

---

## Documents

| # | Document | What it answers |
|---|---|---|
| 1 | README.md | This page — the index and the short version |
| 2 | [MEDIA_ARCHITECTURE.md](MEDIA_ARCHITECTURE.md) | The six runtime components, the layering, the ports, ten invariants |
| 3 | [STREAMING_PIPELINE.md](STREAMING_PIPELINE.md) | The five stages a frame passes through, and every way it can be dropped |
| 4 | [BUFFER_LIFECYCLE.md](BUFFER_LIFECYCLE.md) | Both buffers, overflow, underflow, and the measured backpressure behaviour |
| 5 | [STATE_TRANSITIONS.md](STATE_TRANSITIONS.md) | The nine states and the complete declared transition table |
| 6 | [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md) | Brief compliance, six defects found and fixed, open findings |
| 7 | [PERFORMANCE.md](PERFORMANCE.md) | 12 benchmarks, the allocation finding, and what is not measured |
| 8 | [SECURITY_REVIEW.md](SECURITY_REVIEW.md) | MEDIA-PCM-1, snapshots as recordings, DoS, the borrowed-payload hazard |
| 9 | [EVALUATION_REPORT.md](EVALUATION_REPORT.md) | 105 tests measured — and what they do **not** establish |

---

## The short version

**It moves frames. It does not understand them.** There is no RTP, no WebRTC, no
SIP, no socket, no codec, no resampler, no voice activity detection, no speech
recognition and no synthesis. Not "not yet" — those are other layers, and their
absence here is the design. If this package ever needs to parse an RTP header,
something has gone wrong upstream of it.

**No implicit transitions.** A stream is in one of nine states, and every legal
move is declared in one table. A transition not in the table is refused at run
time; a malformed table is refused at construction. There is no code path that
assigns a state directly.

**Two first-party dependencies, no third-party ones.** `packages/go/runtime` for
the clock and the FSM, `packages/go/metrics` for instruments. Both are
dependency-free, so the transitive closure of this module is the Go standard
library.

**Every clock is injected.** Jitter windows, drift detection, frame deadlines and
recovery timeouts all measure against `runtime.Clock`, so a test advances a
`FakeClock` and observes a late frame in microseconds without sleeping. There is
exactly one deliberate exception — the shutdown drain budget — and
[ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md) explains why converting it would
reintroduce a Phase 11A bug.

**PCM is ephemeral by default.** A stream snapshot may carry audio, which makes a
snapshot store a recording system. `IncludeAudio` defaults to false, capture is
bounded, events never carry PCM, and **Phase 11B ships no durable audio storage
at all**. The policy is MEDIA-PCM-1 in
[SECURITY_REVIEW.md](SECURITY_REVIEW.md), and it gates any future persistence on
six controls.

**Backpressure is expressed, not hidden.** Under a stalled consumer the engine
refuses frames and tells the producer, rather than accepting them and discarding
them silently. Measured: 82 accepted, 418 refused across 500 offered, with both
buffers bounded and the stream still healthy.
