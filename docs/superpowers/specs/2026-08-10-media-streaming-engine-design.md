# Phase 11B — Enterprise Media Streaming Engine — Design

**Module:** `packages/go/media` · **Docs:** `docs/media/` · **Date:** 2026-08-10

---

## 1. Status — what is approved, and what is not

Two different things have been called "complete" in discussion, and conflating
them would misrepresent the state of the work. They are separated here
deliberately.

| | State as of 2026-08-10 |
|---|---|
| **Design approval** | **GRANTED.** This document is approved. |
| **Implementation** | **NOT COMPLETE. NOT STARTED under this plan.** |
| **Phase 11B overall** | **IN PROGRESS — awaiting implementation, then approval.** |

**"Completed in place" means the strategy, not the status.** It was chosen over
"delete and rewrite from scratch," and it says only this: the existing
`packages/go/media` module is the Phase 11B work-in-progress and will be
finished where it stands. It does **not** mean the module is finished. It is
not.

The measured gap between the module and the Phase 11B stop condition:

| Stop condition | Status | Evidence |
|---|---|---|
| Media Runtime implemented | Present | `MediaRuntime`, `MediaCoordinator`, `MediaScheduler`, `MediaDispatcher`, `MediaRegistry`, `MediaMetrics` all exist |
| Streaming Pipeline implemented | Present, **defective** | 5 stages in `pipeline.go`; see §6 |
| Buffer Engine implemented | Present, **defective** | `buffer.go`, `jitter.go`; see §6 |
| **Tests passing** | **NO — 84 pass, 8 fail** | `go test ./...` |
| **Benchmarks completed** | **NO — zero exist** | `grep -c "func Benchmark"` = 0 |
| **Engineering Audit** | **NO** | `docs/media/` does not exist |
| **Performance Report** | **NO** | `docs/media/` does not exist |
| **Security Review** | **NO** | `docs/media/` does not exist |
| **Evaluation Report** | **NO** | `docs/media/` does not exist |

Five of nine stop conditions are unmet. Approving this design authorises the
work that closes them; it does not assert they are closed.

**Phase 11B is complete only when all nine are met and verified.** Phase 11C is
not started.

---

## 2. Scope

Complete `packages/go/media` in place and produce `docs/media/`.

The module stays **standard-library-only** apart from two first-party,
dependency-free modules — `packages/go/runtime` (Phase 10A) and
`packages/go/metrics` (Phase 10.5). Its transitive closure remains the Go
standard library, the property every module in this plane has held since
Phase 10A.

**Integration surface matches Phase 11A exactly.** Phase 11A shipped
`packages/go/telephony` plus `docs/telephony/` and wired nothing: no proto
contracts, no service integration, no Python. Phase 11B does the same. Kafka,
Redis, Aurora and gRPC are honoured as **ports and naming**, not clients:

- **Redis / Aurora** → the existing `StreamStore` interface (`recovery.go:25`).
  No driver, no connection, no SQL. Adapters are a later phase.
- **Kafka** → a new `events.go`: event types, topic naming in the
  `packages/go/eventbus` shape, and an `EventPublisher` port. **No Kafka
  client.** This mirrors `telephony/events.go`.
- **gRPC / Protobuf** → out of scope for 11B, as in 11A.

### Frozen — not modified

Phases 10A, 10B, 10C, 10D, 10E, 10F, 10.5 and 11A. In particular
`packages/go/runtime` and `packages/go/metrics` are consumed, never edited.

### Explicitly not implemented

SIP · RTP networking · WebRTC · carrier APIs · microphone APIs · speaker APIs ·
speech recognition · speech synthesis · voice activity detection · LLM · fraud
detection · codecs · resamplers · DSP.

No Pion, Janus, mediasoup, LiveKit, Twilio Media, Agora, Daily or Jitsi SDK. No
RTP framework. No streaming framework. Their absence is the design, not a
deferral.

---

## 3. What the module already establishes

Recorded so the implementation does not re-litigate settled ground.

- **Nine states, no implicit transitions.** Every legal move is declared in
  `transitionSpec`; `runtime.NewFSM` refuses a malformed table at construction.
  Nothing assigns a state directly.
- **Ports, not drivers.** `StreamStore` names no database.
- **Injected clocks.** Every deadline, drift measurement and jitter window
  measures against `runtime.Clock` — *except one defect, D1 below.*
- **Frame payloads are borrowed, not owned.** `FramePool` plus a ring buffer
  that owns its sample storage and never reallocates. The sharpest edge in the
  package.
- **Media time, not wall time.** The jitter buffer releases against a playout
  position advanced by audio consumed, which is what makes reordering
  deterministic.

---

## 4. Design decisions

### D1 — Delivery advances on an injected-clock ticker, plus read-through

**Problem.** `runtime.go:400` drives the pump with `time.NewTicker`, a real wall
clock, while the rest of the package measures against `runtime.Clock`. Frames
land in the jitter buffer on `Push` and reach the readable output ring only on
`Pump`. Under a `FakeClock` test nothing pumps, so `Read()` returns
`ErrBufferEmpty` on healthy, paused and draining streams alike. This is a
determinism defect that falsifies a claim `doc.go` already makes.

**Decision.** Both halves:

1. `time.NewTicker(d)` → `r.clock.NewTicker(d)`. `runtime.Clock` has offered
   `NewTicker(d) Ticker` since Phase 10A (`runtime/clock.go:34`); the wall-clock
   call was an oversight, and the fix is a one-line swap.
2. `Stream.Read()` pumps when the output ring is empty, then retries once.

```go
func (s *Stream) Read() (Frame, error) {
    if !s.fsm.State().DeliversFrames() {
        return Frame{}, ErrStreamClosed
    }
    f, err := s.pipeline.Buffer().Read()
    if err == nil {
        return f, nil
    }
    if s.pipeline.Pump() > 0 {        // read-through
        return s.pipeline.Buffer().Read()
    }
    return Frame{}, err
}
```

**Why both.** The ticker keeps playout a clocked activity, which is what it
physically is — a slow consumer must not silently grow the jitter buffer.
Read-through means a consumer never starves merely because the pump was late,
and it lets a test read without advancing a clock. `Pump()` stays public.

**Rejected:** read-through only (deletes the pump loop and makes playout timing
the consumer's problem, voiding `PumpInterval` and `PumpAll`); ticker only (a
stalled pump goroutine starves every reader in the process with no fallback).

### D2 — Snapshots capture both buffering stages

**Problem.** `stream.go:511` captures only `pipeline.Buffer().Snapshot()`, the
output ring. Audio in flight inside the jitter buffer is invisible, so opting
into `IncludeAudio` captures nothing and recovery restores nothing — defeating
the stated purpose of buffer recovery.

**Decision.** Capture the union in playout order: jitter-held frames (via a new
`JitterBuffer.Peek()`, which clones) followed by ring frames.
`MaxAudioFrames` bounds the **union**, keeping the newest, and
`BufferedDropped` reports what was omitted so a reader can distinguish a partial
capture from a complete one.

**Consequence, stated plainly.** This decision *increases* the quantity of PCM a
snapshot can hold. It is therefore governed by §5, which is not optional.

### D3 — The too-early window does not narrow under adaptation

**Problem.** `jitter.go:317` bounds early frames at `playout + current +
MaxDelay`. `playout` advances only inside `Get()`, and `current` shrinks 5 ms per
`Put` toward `MinDelay` when measured jitter is low. At a clean 50 fps with an
idle consumer the bound falls from 200 ms to 160 ms while frame 9 arrives at
180 ms — refused `too_early` on a *perfect* input sequence.

**Decision.** Anchor the early bound on `MaxDelay` measured from the stream's own
media position, independent of the adaptive `current` and of a `playout` that a
stalled consumer has frozen.

**Consequence, stated plainly.** A producer that outruns a stalled consumer is
then bounded by jitter-buffer **capacity** (32 frames) rather than by a
media-time window. This is the correct signal: it surfaces as backpressure and
overflow, which the buffer engine exists to express, instead of masquerading as
`too_early` on clean input.

### D4 — `events.go`

The one structural gap against Phase 11A. Event types
(`stream_created`, `stream_started`, `stream_paused`, `stream_resumed`,
`stream_drained`, `stream_recovered`, `stream_failed`, `stream_closed`, plus
recovery outcomes), topic naming in the `eventbus` shape, and an
`EventPublisher` port.

**Events carry identifiers, states and counts. Events never carry PCM.** A
media event that embedded audio would put conversation content into a Kafka
topic with an entirely different retention posture from a snapshot store.

### D5 — Benchmarks, honestly reported

`doc.go` states that every steady-state operation is zero-allocation and "there
are benchmarks that fail if that changes." **There are currently zero
benchmarks**, so that claim is unbacked. Phase 11A has 24.

The suite asserts `0 allocs/op` for frame write/read/validate/sequence, ring
write/read, and `JitterBuffer.Put`/`Get`; and measures throughput at 1, 100 and
1,000 concurrent streams.

**If the zero-allocation claim proves false — `JitterBuffer.Put` calls
`f.Clone()` and `seen` is a map, so it may well — the benchmark is not
weakened to fit the claim.** The measured number is reported and `doc.go` is
corrected.

---

## 5. PCM snapshot policy — MEDIA-PCM-1

**Binding policy. Governs D2 and any future storage work.**

`StreamSnapshot` may carry PCM. That makes it categorically more sensitive than
Phase 11A's call snapshot, which carries no conversation content at all. A store
that persists snapshots containing audio **is a recording system**, with every
obligation that follows.

### The policy

1. **PCM in `StreamSnapshot` is EPHEMERAL BY DEFAULT.** `IncludeAudio` defaults
   to `false`. A deployment must opt in knowingly, per stream.
2. **Phase 11B introduces NO durable audio recording.** The only `StreamStore`
   that ships in this phase is `MemoryStreamStore`, which is in-process and
   dies with the process. No Redis adapter, no Aurora adapter, no file, no
   object store, no disk of any kind.
3. **Audio is always bounded.** `MaxAudioFrames` caps captured frames, and
   `BufferedDropped` records what was dropped. There is no unbounded capture
   path.
4. **Events never carry PCM** (D4).

### Required controls for any future persistent audio storage

Any later phase that persists snapshot audio to durable storage **must** provide
all six. This list is a gate, not a recommendation, and no partial subset
satisfies it:

| # | Control |
|---|---|
| 1 | **Encryption** — at rest and in transit |
| 2 | **Retention** — a defined, enforced maximum lifetime |
| 3 | **Deletion** — verified erasure, including on subject request |
| 4 | **Access control** — authenticated, authorised, least-privilege |
| 5 | **Legal hold** — the ability to suspend deletion under obligation |
| 6 | **Audit** — a tamper-evident record of every read, write and deletion |

`SECURITY_REVIEW.md` carries this policy as its central subject, and
`StreamStore`'s doc comment cites it.

---

## 6. Defect remediation

| ID | Defect | Fix | Site |
|---|---|---|---|
| RC-1 | Pump driven by wall clock; nothing pumps under `FakeClock` | D1 | `runtime.go:400`, `stream.go:366` |
| RC-2 | `Snapshot()` sees only the output ring | D2 | `stream.go:510`, new `JitterBuffer.Peek()` |
| RC-3 | Too-early window collapses under adaptive shrink | D3 | `jitter.go:317` |
| RC-4 | Gap-fill synthesises no silence | Bisect after RC-1/RC-3 land; fix what survives | `pipeline.go:304` |

The eight failing tests and their root causes:

| Failing test | Cause |
|---|---|
| `TestLifecycle_OpenWriteReadClose` | RC-1 + RC-3 |
| `TestPipeline_DeliversInOrder` | RC-1 + RC-3 |
| `TestLifecycle_PauseRefusesWritesButKeepsBuffer` | RC-1 |
| `TestLifecycle_DrainRefusesWritesButDeliversBuffer` | RC-1 |
| `TestSnapshot_ExcludesAudioByDefault` | RC-2 |
| `TestSnapshot_KeepsNewestAudioWhenBounded` | RC-2 |
| `TestRecovery_ResumesStreamsWithAnAttachedSource` | RC-2 |
| `TestPipeline_FillsGapsWithBoundedSilence` | RC-4 |

**RC-4 is diagnosed, not assumed.** It is plausibly downstream of RC-1 and RC-3;
it will be bisected once those land, and whatever survives will be fixed on its
own evidence. No fix is written against a guessed cause.

---

## 7. Documentation — `docs/media/`, nine documents

Eight `GENERATE` artifacts plus a README index. This is nine files, matching
`docs/telephony/`'s nine exactly.

| # | Document | What it answers | `GENERATE` item |
|---|---|---|---|
| 1 | `README.md` | Index and the short version | — (index) |
| 2 | `MEDIA_ARCHITECTURE.md` | Subsystems, ordering rules, invariants | Media Streaming Architecture |
| 3 | `STREAMING_PIPELINE.md` | The five pipeline stages, end to end | Streaming Pipeline Diagram |
| 4 | `BUFFER_LIFECYCLE.md` | Ring and jitter buffers, overflow, underflow, backpressure | Buffer Lifecycle Diagram |
| 5 | `STATE_TRANSITIONS.md` | The nine states and the declared transition table | State Transition Diagram |
| 6 | `ENGINEERING_AUDIT.md` | Brief compliance, defects fixed, open findings | Engineering Audit |
| 7 | `PERFORMANCE.md` | Benchmarks, allocation results, what is not measured | Performance Report |
| 8 | `SECURITY_REVIEW.md` | MEDIA-PCM-1, DoS, untrusted producer input | Security Review |
| 9 | `EVALUATION_REPORT.md` | Tests measured — and what they do **not** establish | Media Evaluation Report |

Diagrams are Mermaid, inside documents 3, 4 and 5.

**Every number in documents 6–9 is produced by a command that was actually run.**
No figure is written that was not measured.

---

## 8. Verification gates

Phase 11B is not complete until all of these pass and their output is recorded:

| Gate | Command | Bar |
|---|---|---|
| Build | `go build ./...` | clean |
| Vet | `go vet ./...` | clean |
| Tests | `go test ./...` | **0 failures.** The suite is 92 today (84 pass, 8 fail); adding tests raises the total, so the bar is "every test passes", not a fixed count |
| Race | `go test -race ./...` | clean |
| Flake | `go test -count=10 ./...` | clean |
| Lint | `golangci-lint run` | clean per repo `.golangci.yml` |
| Standalone module | `GOWORK=off go build ./...` | clean |
| Benchmarks | `go test -bench=. -benchmem` | recorded verbatim into `PERFORMANCE.md` |
| Frozen phases | no file outside `packages/go/media` and `docs/media/` modified | verified |
| Dependencies | `go.mod` requires only `runtime` and `metrics` | verified |

Test coverage required by the brief — unit, integration, streaming, concurrency,
stress, recovery, buffer, failure injection — is audited against the existing
suite in `EVALUATION_REPORT.md`, and any category genuinely unrepresented is
added.

---

## 9. Risks

| Risk | Response |
|---|---|
| D3 changes drop behaviour under sustained overrun | Add an explicit overflow/backpressure test asserting the new signal; document in `BUFFER_LIFECYCLE.md` |
| Zero-allocation claim is false | Report the measured number, correct `doc.go` (D5). Do not weaken the benchmark |
| D2 increases PCM held in memory | Bounded by `MaxAudioFrames`; governed by MEDIA-PCM-1 |
| RC-4 has an independent cause | Bisect before fixing; do not assume |
| Read-through pumping under `-race` | Race gate is mandatory, not optional |

---

## 10. Notes

**This repository is not a git repository** (`git rev-parse` fails, and the
environment reports the same). The brainstorming workflow would normally commit
this document; that step is skipped because there is nothing to commit to. The
document is written to disk and nothing else is claimed about it.

**Phase 11C is not started.**
