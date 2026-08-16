# Enterprise Speech Pipeline & STT/TTS Orchestration — Documentation

**Phase 11C** · `packages/go/speech` · Status: **PROPOSED — awaiting approval**

The layer between media transport and language. Built from scratch —
**no Google Speech, Deepgram, OpenAI, Anthropic, ElevenLabs, Cartesia, Sarvam,
Whisper or Piper SDK, and no speech or ML runtime of any kind.**

---

## Documents

| # | Document | What it answers |
|---|---|---|
| 1 | README.md | This page — the index and the short version |
| 2 | [SPEECH_ARCHITECTURE.md](SPEECH_ARCHITECTURE.md) | The fifteen subsystems, the ports, the dependency rule, the invariants |
| 3 | [STT_ARCHITECTURE.md](STT_ARCHITECTURE.md) | The recognition contract, queues, endpointing, frame cloning |
| 4 | [TTS_ARCHITECTURE.md](TTS_ARCHITECTURE.md) | Sentence streaming, the generation counter, queue asymmetry |
| 5 | [TRANSCRIPT_LIFECYCLE.md](TRANSCRIPT_LIFECYCLE.md) | Partial → final, the five assembly outcomes, why a final is immutable |
| 6 | [PROVIDER_ROUTING.md](PROVIDER_ROUTING.md) | Tiers, health, circuit states, adapter boundaries |
| 7 | [BARGE_IN.md](BARGE_IN.md) | The seven-point contract and the 20 ms budget |
| 8 | [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md) | Brief compliance, defects found and fixed, open findings |
| 9 | [PERFORMANCE.md](PERFORMANCE.md) | 18 benchmarks against the frozen budget, and what is not measured |
| 10 | [SECURITY_REVIEW.md](SECURITY_REVIEW.md) | Content boundaries, retention, isolation, DoS |
| 11 | [EVALUATION_REPORT.md](EVALUATION_REPORT.md) | 72 tests, all 25 mandatory cases — and what they do **not** establish |

---

## The short version

**It orchestrates speech. It does not perform it.** There is no model, no
inference runtime, no acoustic code, no VAD, no LLM, no RTP and no carrier. This
package decides which provider to use, what order results arrive in, what a
final transcript means, when to stop talking, and what happens when a provider
fails. It never decides what a word is.

**Provider agnosticism is checkable.** `STTProvider` and `TTSProvider` name no
vendor, and no type in this package holds a vendor request, a vendor response, a
`map[string]any` or an API key. `go list -deps` resolves to three first-party
modules plus the Go standard library.

**It does not import `packages/go/conversation`.** A speech session is created
*for* a conversation but does not need to know what one is. `conversation`
already owns the dialogue floor (`TurnManager`) and interruption semantics
(`InterruptionEngine`); this package owns the audio lifecycle of one utterance.
A service composes them.

**A final transcript is immutable.** Once a turn is finalised nothing rewrites
it — not a late partial, not a second final, not a provider retry. Providers
legitimately answer after we stop asking, and a transcript that changed after
the conversation engine acted on it is worse than one that loses a word.

**Barge-in is deterministic.** Cancellation bumps a generation counter before
any blocking work, so audio already inside a provider stream is stale the
instant the caller interrupts. Inbound audio is never flushed — the words that
caused the interruption are already arriving into it.

**The latency budget is frozen, not invented here.** ADR-0011 and ADR-0005/0007
own the numbers. Of the hops this package touches, only two are ours end to end:
sentence segmentation (15 ms p50 / 40 ms p95) and barge-in (≤ 20 ms). See
[PERFORMANCE.md](PERFORMANCE.md) for what was measured and what was not.

**No accuracy claim is made about any provider**, because none was ever called.
