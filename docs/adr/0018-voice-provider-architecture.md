# ADR-0018 — Voice provider and audio architecture for Phase 15

- **Status:** Proposed (Phase 15 T1 — audit only, no implementation)
- **Date:** 2026-08-17
- **Supersedes:** nothing
- **Related:** [ADR-0006](0006-llm-routing-strategy.md) (model ladder),
  [ADR-0016](0016-intent-classification.md),
  [ADR-0017](0017-service-wiring.md),
  [Phase 15 dependency map](../phase15/DEPENDENCY_MAP.md)

## Context

Phase 14 wired the deterministic intelligence path into a running service and
stopped, deliberately, before any audio-bearing stage:

```
service → voiceintel.Bridge → conversation.Engine → intent.Classifier → Plan
```

It is CI-verified (`pr-go` run `32046055912`, `services/go/voice` green under
`-race`). ADR-0017 §8 deferred the provider half on the grounds that every
shipped provider needs an external binary or a model, and Phase 13/14 were
no-model/no-network by constraint.

Phase 15 is the first phase permitted to involve audio. Before writing any
provider code, this audit establishes what already exists.

The controlling discovery is that **`packages/go/voice` is not a frozen module**,
and it already contains a complete pipeline. The 13 frozen modules are speech,
runtime, governance, conversation, memory, toolruntime, audiointel, audiobridge,
media, metrics, evaluation, evalsubjects, telephony. `voice` is the Phase 11E
deliverable and is modifiable.

## Problem statement

Phase 15 does not need to design a voice pipeline; `voice.Pipeline` exists, with
five ports each satisfied by a frozen type without an adapter, and with barge-in,
bounded queues and a provider registry already implemented. The open question is
narrower:

> Which providers should the service construct, and can that be done without an
> external binary, a model download, a network dependency or a credential?

## Constraints

1. No frozen module may change.
2. No new state machine, session store or memory system.
3. No API key, no model download, no network egress, no external binary
   installation.
4. No third-party Go dependency (`packages/go/voice` currently has zero).
5. No weakening of the existing security boundary: no audio to disk, no
   transcript in logs/metrics/labels, no shell.
6. Determinism must remain testable without acoustic behaviour.

## Considered options

### Option A — Wire the pipeline with the exported deterministic fakes

Construct `voice.NewPipeline` in the service using `speech.FakeSTTProvider`,
`speech.FakeTTSProvider` and `rt.FakeProvider`, registered through the real
`voice.ProviderRegistry` and driven by the real `ProviderRouter`.

- **Files:** `services/go/voice/cmd/server/*` only. No `voice` change.
- **External requirements:** none.
- **Testability:** total — every provider is scriptable, with `FailNext`,
  `StallFor`, `Cancelled()`, `Closed()`.
- **Cancellation:** real; context reaches every provider entry point.
- **Security:** unchanged; no process, no file, no socket.
- **Frozen impact:** none.
- **Production suitability:** the *wiring* is production-shaped and CI-verifiable;
  the *providers* are not production providers and must never be presented as such.

### Option B — Wire the pipeline with the local process-backed providers

Register `whispercpp` (STT), `piper` (TTS) and `ollama` (generation).

- **Files:** service wiring plus provider configuration.
- **External requirements:** a built `whisper.cpp` binary, a `piper` binary, a
  running Ollama daemon, and **three separate model files**. None is present on
  this machine; a prior session recorded Ollama installed with **no models
  pulled**.
- **Testability:** poor without the binaries; every adapter correctly returns a
  typed error naming the missing path, so tests would assert absence, not
  behaviour.
- **Cancellation:** good — `process` guarantees no orphan, no goroutine leak,
  bounded stderr, deadlines.
- **Security:** larger surface — child processes, one temp audio file
  (`whispercli`), a localhost HTTP client.
- **Frozen impact:** none.
- **Production suitability:** explicitly **no**. `ollama` documents itself as not
  an ADR-0006 tier; `whispercli` is batch-only and not real-time; `piper`
  synthesises a whole utterance before its first sample.

### Option C — Write a new cloud provider adapter (Deepgram/Google/Sarvam class)

- **External requirements:** an API key and network egress — both prohibited.
- **Frozen impact:** none (the ports anticipate exactly this).
- **Assessment:** correct eventual destination, out of scope now.

## Decision

**Adopt Option A for Phase 15: wire the existing `voice.Pipeline` into the
service using the exported deterministic provider fakes, behind the real
registry and router.**

Options B and C are deferred, not rejected. The provider contract they target is
the same one Option A exercises, so neither is foreclosed.

## Why this option was selected

The pipeline's value is in its **orchestration** — floor control, barge-in,
bounded queues, governance-before-speech, chunk-level synthesis, failover — and
none of that requires a real recogniser to verify. A scripted provider that
emits exactly the segments it was handed makes ordering, deduplication, failover
and cancellation observable as a fixed sequence, which acoustic behaviour would
only obscure.

`speech.FakeSTTProvider`, `speech.FakeTTSProvider` and `rt.FakeProvider` are
**exported production files, not `_test.go` helpers**, and the `speech` harness
states why: consumers must be able to test against the engine, and forcing each
to rebuild the scaffolding is how six subtly different fakes come to exist. Using
them is the intended path, not a workaround.

This also keeps Phase 15 inside the constraint envelope that made Phases 13 and
14 auditable: no key, no model, no network, no third-party dependency.

## Trade-offs

- **Accepted:** no real audio is recognised or synthesised, so no latency or
  accuracy claim can be made. Phase 15 must say so as plainly as Phase 14 said
  its path was not end-to-end audio.
- **Accepted:** a fake that never mispronounces or mis-hears cannot surface
  acoustic edge cases. Those belong to the phase that adopts a real provider.
- **Gained:** the entire orchestration becomes CI-verifiable under `-race` with
  no runner dependency beyond Go.

## Alternatives rejected

- **Building whisper.cpp during CI** — a compiler and model download inside a
  gate, for a dev-only adapter.
- **Defaulting a model name for `ollama`** — the package deliberately has no
  default, so that no open-weight model becomes an implicit platform dependency.
- **A new pipeline in `voiceintel`** — would duplicate `voice.Pipeline`; the
  existing one is not frozen and needs no replacement.

## Operational impact

The service gains a constructed pipeline per session. Bounds are already
configuration: `MaxPendingFrames`, `MaxPendingSegments`, `MaxPendingAudio`, and
an unbounded queue is refused at configuration time.

## Security impact

Neutral-to-positive. Option A introduces no process, no file, no socket and no
credential. The existing assertions continue to apply: no audio to disk, errors
without content, bounded retained collections, no shell, governance
non-bypassable. The `media.Frame` **borrowed-payload** rule is the sharpest edge
and any retention across a call boundary must `Clone()`.

## Cost / performance / scalability impact

No provider cost. Phase 14 measured the intelligence path at ~7–9 µs per turn;
the fakes add scripted work only, so any Phase 15 benchmark measures
orchestration, and **provider/model inference remains NOT APPLICABLE**.

## Migration strategy

The registry is the migration seam: swapping a fake for `whispercpp`, `piper` or
a cloud adapter is a registration change, because `PickSTT`/`PickTTS` return the
interface and the router already owns health and failover.

## Risks

| Risk | Mitigation |
|---|---|
| Fakes mistaken for production providers | State the boundary in code and report, as Phase 14 did |
| Borrowed-payload misuse | Assert `Clone()` on any retained frame |
| Two confidences conflated (acoustic vs intent) | Keep them separately named; never merge |
| Pipeline load-flake inherited | Known Phase 11E flake; CI green under `-race` |

## Future review trigger

Revisit when **either** a real-time-capable STT provider is available on the CI
runner without a model download, **or** an approved credential and egress path
exists for a cloud provider. Not "when we need audio".

## References

- `packages/go/voice/pipeline.go:81–126` (ports and compile-time assertions)
- `packages/go/voice/registry.go` (`ProviderRegistry`)
- `packages/go/speech/provider.go:160–228` (STT/TTS contracts)
- `packages/go/speech/harness.go` (exported fakes)
- `packages/go/media/frame.go:236–259` (borrowed payload)
- `packages/go/voice/security_test.go` (existing boundary assertions)
- [Phase 15 dependency map](../phase15/DEPENDENCY_MAP.md)
