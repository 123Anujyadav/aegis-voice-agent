# Phase 16 T10 — WhisperCLI STT Wiring

**Status: IMPLEMENTED and VERIFIED LOCALLY.** A real openai-whisper subprocess
is reached through the production registry → router → pipeline → driver path.
No frozen module changed. No new event, registry, router or abstraction.
`-race NOT RUN LOCALLY`.

> **DETERMINISTIC INTERNAL CALL DRIVER ≠ REAL TELEPHONY**, and **no claim is
> made about recognition accuracy**. The one real inference here runs on a tone.

---

## 1. Three real defects, found by wiring a real provider

Sixteen phases of in-process fakes hid all three. Each was reachable in
production with any subprocess recogniser.

### D1 — audio after the endpoint killed the call

`onFrame` wrote **every** inbound frame to the open recognition stream, and
separately called `CloseSend()` when the endpoint was confirmed. But **audio
does not stop when an utterance does**: the carrier keeps delivering silence,
breath and the start of the next turn, and every non-terminal state accepts it.
Those frames were written to a stream whose send side the pipeline had already
closed.

An in-process fake tolerates that. openai-whisper refuses it —
`stream is closed: audio already submitted` — which `onFrame` classified as a
provider failure and used to **fail the session on the first caller turn**.

**Fix:** the pipeline tracks that the send side is closed (`sttSendClosed`) and
stops writing; `CloseSend` is issued exactly once, since the endpoint can be
confirmed on more than one frame.

### D2 — a refused end-of-utterance hung the call

`_ = stream.CloseSend()` discarded the error. For a **batch** recogniser that
call is where inference *starts* — it spawns the process — so discarding it
meant a recogniser that could not start left the turn waiting on a results
channel that would never close and never carry anything.

**Measured:** a turn that never completed, and a session stuck in `transcribing`
for two minutes.

**Fix:** the error is returned. A recogniser that cannot be told the caller
stopped speaking will fail identically on every later turn, so the session fails
and the Phase 16 T9 policy closes the conversation with a fault. Closing now
takes **0.2 s** instead of hanging.

### D3 — a failed spawn never closed `Results`

`speech.STTStream` says the provider closes `Results` when the stream ends
(`provider.go:189`). whispercli returned the spawn error and left the channel
open, so any consumer blocked on it waited forever.

**Fix:** close `Results` on both failure paths, and a new contract test —
`TestAdapter_AFailedSpawnStillClosesResults` — which is bounded, so the defect
fails the test rather than hanging it.

**D1 and D2 are in `packages/go/voice` (non-frozen); D3 is in the whispercli
adapter (non-frozen). No frozen module was touched.**

## 2. The wiring

```
LocalSTTConfig  (service-local)
  → whispercli.Available()   ← fails fast, names the path and the remedy
  → whispercli.New()
  → voice.ProviderRegistry.RegisterSTT(provider, spec)
      ↳ speech.ProviderRouter   (selection, health, failover)
  → voice.Pipeline → conversation → intent
```

`registerLocalSTT` is ~40 lines that construct and register. **No second
registry, router, factory, cache, process manager, global or session map.**

### Why the configuration is service-local

`packages/go/voice` already declares `STTProviderConfig`, and it does not fit:
its `ModelPath` is a **filesystem path, validated to exist**, because it was
shaped for whisper.cpp and Piper, which load a model file. The openai-whisper
CLI takes a model **NAME** (`tiny`) that the tool resolves against its own
cache. Passing a name where a path is validated fails at startup; passing a path
where a name is expected makes the tool **download a model** — the one thing
this phase must not do. The mismatch is real, so the service carries its own
struct. Nothing about the provider port changes.

## 3. Deployment mode — the guard was not touched

whispercli is registered `ClassDevelopment`. That is the honest classification:
it is a batch Python tool that cannot stream and begins inference only after the
caller stops speaking.

`LocalSTTConfig.Mode` is **required and has no default**, because defaulting it
is how a development-class recogniser reaches production traffic. In
`ModeProduction` the registry **refuses** the registration, naming ADR-0006, and
`registerLocalSTT` returns that refusal unchanged rather than logging and
carrying on — a service that started with no recogniser and no complaint would
be worse than one that failed.

**No reclassification, no weakening, no bypass.** Running a real recogniser in
production mode still requires the ADR-0006 decision Phase 16 T8 raised.

## 4. The audio fixture

`audiointel.SignalGenerator` — the repository's **existing** deterministic
fixture generator, frozen and exported, reproducible from `FixtureSeed`.

- No SAPI, no ffmpeg, no external runtime.
- No committed binary blob.
- Contains nobody's voice: it is a 220 Hz sine wave.

**What it cannot prove:** a tone is not speech, so the only correct transcript
for it is none — which is exactly what the real run asserts (the provider ran
and fabricated nothing). **"real transcript → real intent" is NOT proven**, and
cannot be until a speech fixture exists that can be produced without an external
speech engine. The intent path is proven separately, deterministically, in T7.

## 5. Evidence

```
REAL RUN: openai-whisper model=tiny reached through the production
registry/router/pipeline; 5 frames of non-speech audio,
turn outcomes=[completed/ok completed/empty_transcript], 4.73s wall
(PROVIDER INFERENCE, not orchestration overhead). NO ACCURACY CLAIM IS MADE.
```

Seven new service tests (registration, descriptor, router selection, unsupported
language, production refusal, disabled no-op, missing tool, configuration
validation, real run, real failure) plus two deterministic pipeline regression
tests and one provider contract test. The real-provider tests **skip rather than
fake** where the tool or the cached model is absent — including a check that the
model is cached, so a cold cache never triggers a download.

## 6. Mutations — 8 executed, **8 CAUGHT**

| | Mutation | Result | Caught by |
|---|---|---|---|
| M1 | provider constructed but never registered | **CAUGHT** | registry count, router selection, real run |
| M2 | wrong language declared | **CAUGHT** | `PickSTT: no registered stt provider declares en-IN` |
| M3 | production-mode refusal swallowed | **CAUGHT** | the guard test |
| M4 | reclassified as `ClassProduction` | **CAUGHT** | descriptor class + the guard test |
| M5 | `Results` left open on spawn failure | **CAUGHT** *(only after a guard was written — see below)* | the new contract test |
| M6 | transcript printed to stdout | **CAUGHT** *(only after a guard was written)* | the new standard-streams audit |
| M7 | late audio written again (D1 reintroduced) | **CAUGHT** | the late-audio regression test |
| M8 | `CloseSend` error discarded again (D2 reintroduced) | **CAUGHT** | the refused-end-of-utterance test |

**M5 and M6 escaped on first execution and are reported as such.** Both were
real fixes with no test guarding them: M5 passed the provider's entire suite,
and M6 — a `fmt.Println` of a caller's transcript — passed the **entire security
suite**. Each is now guarded:

- `TestAdapter_AFailedSpawnStillClosesResults` — bounded, so the defect fails
  rather than hangs.
- `TestSecurity_NoModuleWritesToStandardStreams` — a structural audit over every
  non-test file in the module and its five provider packages. A library writes
  to its injected logger, never to a process-wide stream; the invariant already
  held everywhere and simply was not asserted.

## 7. Security

No API key, no credential, no network, no model download. Argv is a vector built
only from validated configuration and a generated temp path. The child
environment is allowlisted (15 named variables). Stderr is a bounded ring. Temp
audio is removed on stream close — asserted by reading the work directory after
the real run. No transcript or PCM reaches a log, a metric label or a stream,
now enforced structurally.

## 8. What is still not true

- **Generation and TTS remain deterministic fakes.** No Ollama, no Piper.
- **No speech fixture**, so no real transcript → intent evidence.
- **Production mode still refuses this provider**, by design.
- **No telephony.** REAL INBOUND TRANSPORT IS NOT PRESENT.
