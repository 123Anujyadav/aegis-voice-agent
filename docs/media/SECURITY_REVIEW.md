# Security Review

**Phase 11B** · `packages/go/media` · Reviewed 2026-08-10

---

## 1. MEDIA-PCM-1 — the PCM snapshot policy

**This is the central security fact of this module, and it is binding.**

Every other snapshot in this platform is content-free. Phase 11A's call snapshot
carries identifiers, states and timings — nothing anyone said. **A media stream
snapshot may carry PCM**, because the entire point of buffer recovery is not
losing the audio that was in flight.

That makes a store holding these snapshots **a recording system**, with every
retention, encryption and consent obligation that follows. It is not a cache with
audio in it. It is a recording of a conversation.

### The policy

| # | Rule | Enforced by |
|---|---|---|
| 1 | **PCM in `StreamSnapshot` is EPHEMERAL BY DEFAULT.** `SnapshotConfig.IncludeAudio` defaults to `false`; a deployment must opt in knowingly, per stream | `DefaultSnapshotConfig()`; `TestSnapshot_AudioIsOffByDefault`, `TestSnapshot_ExcludesAudioByDefault` |
| 2 | **Phase 11B introduces NO durable audio recording.** The only `StreamStore` that ships is `MemoryStreamStore` — in-process, dies with the process. No Redis adapter, no Aurora adapter, no file, no object store, no disk of any kind | `recovery.go`; no other implementation exists in the module |
| 3 | **Audio capture is always bounded.** `MaxAudioFrames` caps the capture and `BufferedDropped` records what was omitted. There is no unbounded capture path | `Stream.Snapshot`; `TestSnapshot_BoundAppliesToBothStagesCombined`, `TestSnapshot_KeepsNewestAudioWhenBounded` |
| 4 | **Events never carry PCM** | `TestMediaEvent_CarriesNoAudio` — by reflection |

### Required controls for any future persistent audio storage

Any later phase that persists snapshot audio to durable storage **must** provide
all six. **This is a gate, not a recommendation, and no partial subset satisfies
it.**

| # | Control | What it means here |
|---|---|---|
| 1 | **Encryption** | At rest and in transit, for the audio and for anything referencing it |
| 2 | **Retention** | A defined, enforced maximum lifetime — not "until someone cleans up" |
| 3 | **Deletion** | Verified erasure, including on data-subject request |
| 4 | **Access control** | Authenticated, authorised, least-privilege; reading stored audio is a privileged operation |
| 5 | **Legal hold** | The ability to suspend deletion under legal obligation, without disabling deletion generally |
| 6 | **Audit** | A tamper-evident record of every read, write and deletion |

`StreamStore`'s doc comment cites this policy at the interface itself, so an
implementer meets it before writing an adapter rather than after.

### Phase 11B increased the amount of PCM a snapshot holds

Stated plainly because it would be easy to omit. Defect D-2
([ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md)) fixed snapshots that captured only
the output ring; they now capture the jitter buffer as well. That is strictly
*more* audio per snapshot than before.

It is acceptable because: capture remains opt-in and off by default; the bound
applies to the **union** of both stages, not per stage; and no durable store
exists in this phase to write it to. Under the old behaviour the feature simply
did not work — recovery restored silence — so the alternative to more captured
PCM was a recovery path that was decorative.

## 2. The borrowed-payload hazard

**The sharpest memory-safety edge in this package.**

`Frame` payloads returned from `RingBuffer.Read` are **borrowed** — sub-slices of
the ring's single backing array, which is reused as the ring wraps. A consumer
that retains a frame without cloning will, within one buffer revolution, be
reading audio that now belongs to a different point in the stream.

Consequences if violated:

- **Correctness:** a transcriber holding frames sees audio silently mutate.
- **Cross-stream disclosure:** the risk is bounded by the fact that each stream
  owns its own ring, so a stale read exposes *other audio from the same stream*,
  not another caller's conversation. That bound is the reason per-stream ring
  ownership is not negotiable — a shared arena across streams would turn a
  retention bug into a cross-tenant audio leak.

Mitigations: documented on `Frame`, on `RingBuffer`, and on `Read`; `Frame.Clone`
and `CloneInto` are provided; the jitter buffer and `Peek` both clone
deliberately, so the two places most likely to retain frames already do the right
thing.

## 3. Denial of service

| Vector | Control | Where |
|---|---|---|
| Unbounded stream creation | `MediaScheduler.Admit` refuses past `MaxStreams` and `MaxStreamsPerSource`; reservation and decision are one atomic step | `runtime.go` |
| Oversized frames | `MaxFrameBytes` — refused, never truncated | `pipeline.go` stage 1 |
| Timestamp poisoning | `MaxTimestampSkew` checked against the pipeline's own media position, which one bad frame cannot move | `pipeline.go` stage 2 |
| Producer outrunning consumer | Jitter capacity (32) and ring capacity (50) both bound memory; refusal is reported to the producer | `jitter.go`, `buffer.go` |
| Unbounded event accumulation | `RecordingEventPublisher` is bounded and counts what it dropped | `events.go` |
| Unbounded history growth | `maxHistory` = 64 transitions per stream | `stream.go` |
| Stalled source holding a slot | `StallTimeout` sweep moves the stream to `timeout`, freeing it | `runtime.go` |
| **A single lost frame stalling a stream forever** | Hole-skipping in `JitterBuffer.Get` | `jitter.go` |

That last row was a live availability defect until this phase — one lost packet
permanently stalled the stream, which an adversary able to drop a single frame
could have triggered deliberately. See D-4.

**Backpressure is a security property, not only a performance one.** Before D-6,
an overloaded pipeline destroyed frames silently while reporting acceptance. A
system that cannot distinguish "delivered" from "discarded" cannot detect an
overload attack. Measured after the fix: 500 offered → 82 accepted, 418 refused
*and reported*, memory bounded throughout.

## 4. Untrusted producer input

Everything arriving from a media source is untrusted: it originates, ultimately,
from a carrier and therefore from a caller.

The pipeline validates **before** it trusts. Format, frame shape and size are
checked before the timestamp is read; the timestamp is checked before it can
influence the jitter buffer's playout position. Sequence numbers are never
trusted to be contiguous, ordered, or unique — reordering, duplication and gaps
are all first-class outcomes with their own counters rather than error paths.

No input from a frame reaches a metric label, a topic name or a log key. Labels
are bounded enums (`direction`, `reason`, `state`) or authored identifiers
(`SourceID`), and `SourceID` is validated to lowercase alphanumerics, hyphen and
underscore precisely because it becomes both a Prometheus label and a Kafka topic
segment.

## 5. What this review does not cover

- **No `-race` run.** The environment has no C compiler, so the race detector
  could not be run. Concurrency correctness is supported by three concurrency
  tests and ten-repetition stability, neither of which detects a data race the
  way the detector does. **This is the most significant unverified property in
  the module** (F-1).
- **No transport security.** There is no network here; TLS, authentication and
  authorisation of a media source belong to the carrier adapter phase.
- **No fuzzing.** Frame input is validated but not fuzzed.
- **No supply-chain review.** Not applicable — the dependency closure is the Go
  standard library plus two first-party modules.
- **No threat model for the future durable store**, because it does not exist
  yet. MEDIA-PCM-1 §2 is what prevents one appearing without this review being
  redone.
