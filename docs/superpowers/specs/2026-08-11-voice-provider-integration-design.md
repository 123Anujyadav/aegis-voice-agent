# Phase 11E — Local Voice AI Provider Integration: Design

**Date:** 2026-08-11 · **Status:** awaiting approval · **Depends on:** 10A–10F, 10.5, 11A–11D (frozen)

---

## 1. What already exists, and must be reused

| Contract | Location | Use in 11E |
|---|---|---|
| `STTProvider`, `STTStream`, `TTSProvider`, `TTSStream` | `speech/provider.go:169-231` | The local adapters implement these. No new speech port. |
| `ProviderRouter` | `speech/router.go` | **§7 in full**: tiers, circuit breaker, health, switch events, capability matching. Reused, not rebuilt. |
| `runtime.Provider` + `TokenStream` | `runtime/provider.go:20` | The LLM port. Nothing implements it yet; Ollama will. |
| `runtime.Kernel.Generate` | `runtime/kernel.go:523` | Text generation with sinks and dispatch. |
| `governance.Engine.Decide` | `governance/runtime.go:314` | Mandatory gate, unchanged. |
| `conversation.Engine.Begin/Get` | `conversation/engine.go:233` | Planner. Returns `Plan`, not text. |
| `audiointel.Session.Analyze` + `audiobridge.Adapter` | Phase 11D | Barge-in detection and the 11C cancellation path. |

## 2. Boundary correction: conversation is a planner, not a generator

§14 asks the voice layer to "receive response text/events" from the Conversation
Engine. It cannot: `conversation.Engine` returns `Plan{Action: ActionRespond, …}`
and never calls a model. Text generation is `runtime.Kernel.Generate`.

The orchestration is therefore:

```
transcript → conversation.Engine (plan) → governance.Decide (gate)
           → runtime.Kernel.Generate (text stream) → speech chunker → TTS
```

This is the frozen shape. No conversation internals are modified.

## 3. Modules

```
packages/go/voice/                  orchestration core
  providers/process/                shared external-process supervision
  providers/whispercpp/             speech.STTProvider  (spec-complete, unexecutable here)
  providers/whispercli/             speech.STTProvider  (executable here, batch-only)
  providers/piper/                  speech.TTSProvider  (spec-complete, unexecutable here)
  providers/ollama/                 runtime.Provider    (executable here, streaming)
```

`services/go/voice` already exists as a Phase-1 shell (go 1.23, `platform` only).
It is not a voice engine and is left alone; the name adjacency is recorded.

### Import direction

`voice` imports `speech`, `media`, `audiointel`, `audiobridge`, `conversation`,
`governance`, `runtime`, `metrics`. That is the normal direction — the consumer
imports the producer, as `speech` imports `media`.

No bridge module is needed here, unlike Phase 11D's `audiobridge`. That existed
because `audiointel` had to sit *below* frozen `speech` and the import would have
been an inversion. Voice sits *above* conversation and governance, so importing
them directly is the conventional direction.

**Each provider adapter subpackage imports only the port it implements plus the
standard library.** Verified per-package with `go list -deps`, not per-module.

## 4. Local provider reality

Checked, not assumed:

| Provider | Status | Consequence |
|---|---|---|
| whisper.cpp | **absent** | Adapter built to spec; e2e leg reports unavailable |
| `openai-whisper` + torch CPU | **present** | Second adapter; **batch-only, no partials** |
| Piper | **absent** | Adapter built to spec; e2e leg reports unavailable |
| Ollama 0.32.7 (a 12B model is pulled) | **present, working** | Real streaming LLM over HTTP. The model ID is configuration; no model name is a target. |
| ffmpeg | present | Available for fixture conversion |

No binaries will be installed. No API key is requested or stored.

## 5. Ollama over HTTP, not the CLI

stdlib `net/http` against `localhost:11434`. No shell, no argv from caller text,
so §18's injection surface is zero on that path. Genuine token streaming for §5,
and context cancellation for the 20 ms budget `runtime.Provider` documents.

The Ollama daemon is externally managed, so §10's process supervision applies to
whisper and Piper and is documented as **not** applying to Ollama.

## 5.1 Budgets: what is frozen, what is only observed

Verified against the sources before implementation.

| Quantity | Status | Source |
|---|---|---|
| **Abort / barge-in: 20 ms** | **FROZEN and applies** | `runtime/provider.go:37-41`, `runtime/dispatcher.go:54,274`, ADR-0011 §5.1, ADR-0004 §12 |
| **LLM time-to-first-token: 250 ms p50 / 550 ms p95** | **FROZEN, but set for a different system** | ADR-0011 §5.2 hop 6, ADR-0006 C1 |
| Any local-model latency target | **NOT FROZEN — does not exist** | — |

The 20 ms governs **cancellation responsiveness**, not first-token latency. It
is asserted, because a single component exceeding the whole-hop budget
definitively breaks it.

The 250/550 was set for `claude-sonnet-5` at `effort: "low"` over a network.
A local model on this laptop's CPU is **not held to it**. Its first-token
latency is measured and reported as an observation against that reference, with
no pass/fail assertion. **This phase creates no new SLA.**

## 5.2 ADR-0006 tension, recorded rather than resolved silently

ADR-0006 freezes a four-tier ladder, **all tiers on Claude**, with exact model
IDs, and explicitly **rejected** "self-hosted open-weight model (Llama/Mistral
class)" as Option 5.

Phase 11E mandates a local-first loop with no API keys. The two govern different
things: ADR-0006 is production model routing; 11E is a development loop.

The Ollama adapter is therefore **development-only**. It is not registered as an
ADR-0006 tier, does not claim to satisfy C1, and nothing in this phase amends
ADR-0006. A guard test enforces the first of those.

## 6. Session lifecycle

`rt.FSM` over the states §4 requires:

```
created → listening → speaking_detected → transcribing → thinking
        → synthesizing → speaking → listening
plus: interrupted, cancelled, failed, completed
```

Declared table, no implicit transitions, every edge reached by a test.

## 7. Streaming overlap

The measured claim: TTS synthesis of the first chunk **begins before** the model
token stream completes. Asserted by ordering, not by wall-clock.

## 8. What will not be built

Second router, second memory system, second governance engine, conversation
rewrite, cloud SDK, API key handling, SIP/RTP/WebRTC, carrier integration, fake
provider output.

## 9. Honest-failure contract

A missing binary or model produces a typed `ErrProviderUnavailable` carrying the
exact path checked and the install requirement. No fabricated transcript, no
fabricated audio, no test that passes by pretending.
