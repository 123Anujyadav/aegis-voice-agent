# Phase 15 — Voice Provider & Audio Architecture Audit

**Status:** AUDIT ONLY. No production code, no `go.mod`, no `go.work`, no CI change.
**Baseline:** `beb46df` (Phase 14 CI closeout).
**Method:** repository inspection. No external binary invoked, no model downloaded,
no network call made, no credential requested.

---

## 1. The finding that shapes everything

**`packages/go/voice` is NOT a frozen module.**

The 13 frozen modules are speech, runtime, governance, conversation, memory,
toolruntime, audiointel, audiobridge, media, metrics, evaluation, evalsubjects,
telephony. `voice` is absent from that list — it is the Phase 11E deliverable,
and it already contains a complete `Pipeline`, a `ProviderRegistry`, a
`SessionFSM`, barge-in handling and five provider adapters.

Phase 15 therefore does **not** need to build a voice pipeline. One exists.
Phase 15's question is narrower and safer: *should the service construct it, and
with which providers.*

## 2. What already exists

| Component | Package | Frozen? | State |
|---|---|---|---|
| `Pipeline`, `PipelineConfig`, `NewPipeline` | `voice` | no | complete |
| `ProviderRegistry`, `ProviderDescriptor`, `ProviderSpec` | `voice` | no | complete |
| `SessionFSM` | `voice` | no | complete |
| barge-in / `interruptController` | `voice` | no | complete |
| `STTProvider`/`STTStream`/`TTSProvider`/`TTSStream` | `speech` | **yes** | complete |
| `ProviderRouter`, health, failover | `speech` | **yes** | complete |
| `Frame`, `AudioFormat`, ring buffer, jitter | `media` | **yes** | complete |
| `Session.Analyze`, `SpeechController` | `audiointel` | **yes** | complete |
| `Adapter` (SpeechController impl) | `audiobridge` | **yes** | complete |
| ollama, piper, process, whispercli, whispercpp | `voice/providers` | no | complete |

## 3. The actual pipeline graph

`voice.Pipeline` declares five ports (`pipeline.go:81–120`), each satisfied by a
frozen type **without an adapter**, proven by compile-time assertions at
`pipeline.go:120–126`:

```
inbound media.Frame
  └─> Intelligence.Analyze          audiointel.Session      [frozen]  ctx ✓
        └─> STT via ProviderRegistry -> speech.ProviderRouter -> STTStream
              └─> TranscriptSegment (partial + final)
                    └─> Planner.Handle       conversation.Conversation [frozen]  ctx ✗ (synchronous)
                          └─> conversation.Plan
                                └─> Governor.Decide   governance.Engine [frozen]  ctx ✗ (pure)
                                      └─> Generator.Generate  rt.Kernel [frozen]  ctx ✓
                                            └─> response text
                                                  └─> speech.Chunker (clause split)
                                                        └─> TTSStream.Synthesize
                                                              └─> media.Frame
                                                                    └─> FrameSink.Deliver  ctx ✓
```

**Every edge exists.** There is no missing edge in the pipeline itself. The only
missing edge is the one Phase 14 deliberately did not draw:

```
services/go/voice/cmd/server  ──X──>  voice.NewPipeline
```

The service constructs `voiceintel.Bridge` (Planner only). It does not construct
a `Pipeline`. Verified: no production file under `services/go/voice` imports
`packages/go/voice`; only a Phase 14 *test* file does.

## 4. Provider matrix — every provider actually present

| Provider | Capability | Production? | External binary | Model | Network | Credential | Deterministic local test | Phase 15 suitability |
|---|---|---|---|---|---|---|---|---|
| `speech.FakeSTTProvider` | STT | no — test seam, **exported on purpose** | none | none | none | none | **yes** | **RECOMMENDED** |
| `speech.FakeTTSProvider` | TTS | no — test seam, **exported on purpose** | none | none | none | none | **yes** | **RECOMMENDED** |
| `rt.FakeProvider` | generation | no — test seam, exported | none | none | none | none | **yes** | **RECOMMENDED** |
| `whispercpp` | STT (streaming) | dev | `whisper.cpp` (must be built for host) | **yes** | no | no | typed error when absent | deferred |
| `whispercli` | STT (**batch only**) | dev convenience | Python `whisper` | **yes** | no | no | typed error when absent | deferred |
| `piper` | TTS | dev | `piper` | **yes** | no | no | typed error when absent | deferred |
| `ollama` | generation | **dev only, structurally** | none (HTTP) | **yes**, no default | localhost daemon | no | no daemon → error | deferred |
| `process` | supervisor (not a provider) | yes — support library | n/a | no | no | no | yes | already used |

Notes drawn from the source, not assumed:
- `ollama` documents itself as *not* an ADR-0006 production tier and has **no
  default model name** — deliberately, so no open-weight model becomes an
  implicit platform dependency.
- `whispercli` is **batch, not streaming**: `Capabilities.Streaming = false`, no
  partials, inference starts only after speech ends.
- `piper` synthesises a whole utterance before the first sample; **chunk size is
  the latency knob**, and `speech.Chunker` already feeds it clause by clause.
- `process` centralises child-process lifetime: no orphan, no goroutine leak,
  bounded stderr ring, deadline on every blocking operation, `exec.Command`
  argv only — **no shell anywhere**.

## 5. Audio data contract (`media`, frozen — do not modify)

| Property | Value |
|---|---|
| Baseline formats | `PCM16Mono8k()` (telephony), `PCM16Mono16k()` (wideband) |
| Sample format / layout | `FormatPCM16` / `LayoutMono` |
| Frame fields | `Sequence`, `Timestamp` (media time), `Arrival` (injected clock), `Format`, `Payload`, `Flags` |
| **Payload ownership** | **BORROWED.** Valid only until the next operation on the owning ring buffer or pool. `Clone()` is mandatory to retain a frame beyond the call. |
| Buffering | bounded ring buffer; `MaxPendingFrames`, `MaxPendingSegments`, `MaxPendingAudio` in `PipelineConfig` |
| Unbounded growth | refused at configuration — `TestSecurity_AnUnboundedQueueIsRefusedAtConfiguration` |
| TTS frames | **owned by the receiver**, unlike ring-buffer frames — an adapter reusing a buffer would corrupt queued audio |

**This borrowed-payload rule is the sharpest edge in the whole audio plane** and
is the single most likely source of a Phase 15 defect.

## 6. ASR / speech contract (`speech`, frozen)

`TranscriptSegment`: `Session`, `Turn`, `Segment`, `Sequence` (monotonic per
turn — detects provider repeat/reorder), `Text`, `IsFinal`, `Confidence` (0..1
or `ConfidenceUnknown`), `StartTime`/`EndTime`, `Language`, `Role`, `Meta`.

**Two different confidences exist and must never be merged:**

| | Meaning | Where |
|---|---|---|
| `TranscriptSegment.Confidence` | acoustic — how sure the recogniser is of the *words* | `speech` |
| `conversation.Intent.Confidence` | lexical — how sure the classifier is of the *goal* | `conversation`/`intent` |
| `conversation.Plan.Confidence` | planner's confidence in its *decision* (Phase 14 T11 finding) | `conversation` |

A third distinct quantity, `MinASRConfidence` (0.40), is the frozen gate the
`IntentEngine` applies to acoustic confidence before classifying at all.

## 7. TTS / output path

Complete inside `voice`: `Plan → Generator.Generate → text → speech.Chunker →
TTSStream.Synthesize → media.Frame → FrameSink.Deliver`, with
`MaxPendingAudio` as the slow-consumer bound. **It stops exactly where §3 says:
nothing constructs it from the service.**

## 8. Interruption / barge-in — already exists at two levels

| Level | Mechanism | Granularity |
|---|---|---|
| Audio | `audiointel` detects, calls `SpeechController.Interrupt(ctx, reason)`; `audiobridge.Adapter` implements it over a `speech.SpeechSession` | **frame level** |
| Dialogue | `conversation.EventInterrupt` + `InterruptionEngine` | **turn boundary** (Phase 14 T8 finding — `Handle` is synchronous) |
| Pipeline | `voice.interruptController` + `Pipeline.abortActiveTurn()`, counters `BargeIns()`, `SpeechInterruptRefusals()` | frame level |

`reason` is a bounded code from `classifications.go`, **never free text**. A
refusal is a legitimate outcome, not a fault: interrupting while the agent is
listening is not an interruption.

**No new state machine is required or permitted.**

## 9. Cancellation / lifecycle

```
service ctx ✓ → Runner.Run ✓ → [Phase 14 ends here]
              → Pipeline ✓ → Intelligence.Analyze ✓ → STT/TTS Open* ✓ → FrameSink.Deliver ✓
                            → Planner.Handle ✗ (no ctx parameter — by design, synchronous)
                            → Governor.Decide ✗ (pure function)
```

Context reaches every provider entry point. It does **not** reach
`conversation.Handle` — a frozen contract, and the reason Phase 14 reported that
cancellation and interruption are observed at turn boundaries.

Provider-side risk, from source inspection only: `process` explicitly guarantees
no orphan, no goroutine leak, bounded stderr, deadline on every blocking
operation. `ollama` uses stdlib `net/http` with no spawned process. No provider
was executed during this audit.

## 10. Security posture (already enforced by Phase 11E)

`voice/security_test.go` already asserts:
- hostile transcript never escapes as code or content
- errors do not embed content
- every retained collection is bounded
- unbounded queues refused at configuration
- no shell, no privilege escalation
- governance cannot be bypassed
- **no module writes audio to disk** — one documented exception, `whispercli`,
  which the Python CLI forces (file path, not a pipe), written to a per-stream
  temp dir and removed by `stream.cleanup`

`PipelineObserver` receives segments **in-process only** and its contract states
"lengths and codes, never content". `ollama/security_test.go` additionally
asserts malformed responses do not leak model output and transport error bodies
are bounded.

## 11. Dependencies

`go list -deps packages/go/voice` → first-party only: metrics, runtime, media,
audiointel, speech, audiobridge, conversation, governance. **Zero non-stdlib
external packages. Zero third-party modules. No CGO in the Go code** (whisper.cpp
is a separate C++ *binary*, not linked).

## 12. Known health note

`go test ./...` across `packages/go/voice` + its five provider packages fails
under parallel load; `go test ./` alone passes, and CI (`pr-go`, run
`32046055912`) reported `packages/go/voice` **success** under `-race`. This is
the documented Phase 11E load-sensitive flake, pre-existing and not owned by
Phase 15.

## 13. Deterministic test architecture — available today

A complete pipeline can be constructed with **no external binary, no model, no
network and no credential**, using seams that are exported production files
(not `_test.go`) precisely so consumers can do this:

| Port | Deterministic implementation |
|---|---|
| STT | `speech.NewFakeSTTProvider` — scripted segments, `FailNext`, `StallFor` |
| TTS | `speech.NewFakeTTSProvider` — `Cancelled()`, `Closed()`, `FailNext`, `StallFor` |
| Generator | `rt.NewFakeProvider` — scripted chunks |
| Audio in | `media.NewFrameGenerator`, `media.NewHarness`, `audiointel.NewSignalGenerator` |
| Clock | `rt.NewFakeClock` |
| Controller | `audiobridge.New` over a `speech.SpeechSession` |

`voice/e2e_test.go` already wires `NewProviderRegistry` + `NewPipeline` +
`conversation.NewEngine` + `governance.New` this way. **The deterministic path is
proven, not hypothetical.**

## 14. Frozen-module impact

**None.** Every port Phase 15 needs is already exported by a frozen module and
already satisfied by a frozen type without an adapter. The work is composition
inside non-frozen `voice` and `services/go/voice`.

No stop condition is triggered.
