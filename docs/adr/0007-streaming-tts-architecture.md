# ADR-0007: Streaming TTS — ElevenLabs Flash primary, Cartesia secondary, Sarvam for Indic evaluation

- **Status:** Accepted
- **Date:** 2026-08-06
- **Deciders:** Lead Staff Engineer
- **Consulted:** —
- **Informed:** All engineering, Product
- **Depends on:** ADR-0004, ADR-0006

---

## 1. Context

`tts-gateway` turns the agent's words into audio the caller hears. It is the last
hop before the caller's ear and the last chance to blow the latency budget
(ADR-0011 allocates **90 ms p50 / 180 ms p95** to time-to-first-byte).

It is also the component that determines whether the product sounds like a
capable assistant or like a 2010 IVR. The caller forms their impression of the
agent from its voice before they process a word of what it said, and a caller who
believes they are talking to a robot behaves differently — they hang up, or they
try to game it.

## 2. Problem Statement

Which speech synthesiser, and how do we structure the pipeline so that synthesis
overlaps generation rather than following it?

The naive design — wait for the LLM to finish, synthesise the complete reply, play
it — adds the full generation time and the full synthesis time in series. At a
three-sentence reply that is well over a second of dead air before the caller
hears anything. **Sentence-level streaming is not an optimisation here; it is the
only viable structure.**

Two further requirements shape the choice:

- **Indic and code-switched output.** The agent must reply in the caller's
  language, including Hinglish (ADR-0005 §2). A synthesiser with excellent English
  and mechanical Hindi is not sufficient for this market.
- **Barge-in.** The caller will interrupt. When they do, our audio must stop
  within one frame interval (ADR-0004 §12), which means synthesised audio must be
  streamed and abandonable — never queued as a complete file.

## 3. Constraints

| # | Constraint | Source |
|---|---|---|
| C1 | Time-to-first-byte ≤ 90 ms p50 / 180 ms p95 | ADR-0011 |
| C2 | Streaming synthesis, abandonable mid-utterance | ADR-0004 §12, barge-in |
| C3 | Output must be 8 kHz telephony-compatible; avoid a resample hop where possible | ADR-0004 |
| C4 | Indian English, Hindi, and code-switched Hinglish with natural prosody | Product, ADR-0005 C3 |
| C5 | Provider-swappable without touching `ai-orchestrator` | Risk |
| C6 | Per-character/per-minute cost within the inference budget | ADR-0003 §11 |
| C7 | Voice must be consistent for a subscriber across calls | Product |

## 4. Considered Options

1. **ElevenLabs Flash v2.5** — lowest published TTFB in class, multilingual
2. **Cartesia Sonic** — comparable latency, strong English, thinner Indic
3. **Sarvam AI (Bulbul)** — Indian vendor, Indic-native prosody
4. **Google Cloud TTS (Neural2 / Chirp)** — India region, broad language coverage
5. **Azure Neural TTS** — strong Indic voices, Central India region
6. **Self-hosted (Piper / XTTS / Kokoro-class)** — full control, no vendor

## 5. Decision

**A routed, multi-provider `tts-gateway` with ElevenLabs Flash v2.5 as the
default — structured around sentence-level streaming.**

- **Primary: ElevenLabs Flash v2.5.** Default for all traffic. Streaming output
  requested directly at **8 kHz µ-law** where the provider supports it, so the
  frame handed to `media-relay` needs no resample (C3).
- **Secondary: Cartesia Sonic.** Failover target and used for English-dominant
  sessions where its latency profile measurably helps.
- **Under evaluation: Sarvam Bulbul**, run in shadow against production text from
  launch, promoted for Indic-dominant sessions only on `tests/eval` evidence —
  the same discipline applied to ASR in ADR-0005 §5.

**The pipeline structure is the substantive decision:**

1. `ai-orchestrator` streams tokens from the LLM (ADR-0006 §12).
2. A **sentence segmenter** emits the first complete clause as soon as it is
   available — typically well before the model has finished the reply.
3. That clause is sent to `tts-gateway` immediately and synthesis begins.
4. Audio frames stream to `media-relay` and out to the caller while the LLM is
   still generating sentence two.
5. A **barge-in signal** from `media-relay` cancels the in-flight synthesis and
   discards the buffered frames within one frame interval.

An **output sanitiser** sits between the LLM and the segmenter, stripping
markdown, XML, list markers and stray `<thinking>` fragments before anything can
be read aloud (ADR-0006 §15).

Providers sit behind a `Synthesiser` port. Voice selection is per subscriber and
stable across calls (C7).

## 6. Why This Option Was Selected

**ElevenLabs Flash as default because it is the only candidate that is
simultaneously fastest-in-class and credible on Indic** (C1, C4). The latency
budget is tight enough that TTFB is close to a disqualifying criterion on its own;
among the providers that clear it, multilingual quality is the tiebreaker and
Flash wins it.

- **Direct 8 kHz µ-law output** (C3) removes a resample hop from the hottest path
  in the system. Small in isolation, meaningful when it is one of a dozen such
  decisions across the budget.
- **Streaming with cancellation** (C2) is a first-class part of the API rather
  than something we simulate by chunking.
- **Cartesia as secondary** for the same reason Deepgram is ADR-0005's secondary:
  a failover target on genuinely different infrastructure, with a latency profile
  good enough that failover is not a visible downgrade for English calls.
- **Sarvam in shadow, not in production**, for the same reason as ADR-0005 §6 —
  the Indic-specialist claim is testable and should be tested on our own traffic
  before it is trusted.

**The sentence-streaming structure is why this works at all.** With it,
perceived latency is time-to-first-*clause*, which is roughly LLM first-token +
TTS TTFB. Without it, perceived latency is full generation + full synthesis. The
same providers with the wrong pipeline structure would miss the budget by a
factor of three.

## 7. Trade-offs

**Accepted.**

- **Cross-border processing on the default path.** Neither ElevenLabs nor
  Cartesia offers Indian processing. Unlike ASR (ADR-0005 C4), the input here is
  *our agent's own generated text*, not the caller's speech — a materially lower
  sensitivity class. It is still a transfer and is treated as one: covered by
  consent and recorded in the data map (ADR-0012). This is the principal
  motivation for promoting Sarvam if it proves out.
- **Sentence segmentation can be wrong.** Splitting on the wrong boundary
  produces unnatural prosody — the synthesiser has less context than it would
  with the full reply, and abbreviations and numerals are classic failure points.
  We accept slightly worse prosody for dramatically better latency.
- **Multi-provider means normalising three different voice catalogues**, streaming
  protocols, and cancellation semantics behind one port. Voice identity (C7) must
  be mapped per provider so a failover does not change the agent's voice mid-call
  — which would be jarring and would undermine the caller's trust.
- **Cost is per character on the primary**, which couples spend directly to the
  agent's verbosity — the same lever as ADR-0006 §11, reinforcing it.

## 8. Alternatives Rejected

**Cartesia as primary.** Excellent latency and a clean streaming API. Rejected as
default on C4: Indic and code-switched coverage is thinner than the market
requires. Retained as secondary where that objection does not bind.

**Google Cloud TTS.** The residency-correct answer (India region) and the reason
this was a genuinely close call. Rejected on C1: its streaming TTFB is not
competitive with the Flash/Sonic class, and in a budget this tight that is
decisive. Reconsider immediately if its streaming latency improves — it would
resolve the §7 residency trade-off outright.

**Azure Neural TTS.** Strong Indic voices and a Central India region. Rejected on
the same latency grounds as Google, with the additional consideration that a
third first-class integration is cost without proportional benefit.

**Self-hosted (Piper / XTTS-class).** Rejected on quality and operational burden.
Open-weight TTS at telephony quality in Indic languages is not currently close to
the hosted providers, and self-hosting a latency-critical GPU workload has the
same TCO objection as ADR-0006 §8 Option 5. Revisit only if cost forces it.

## 9. Operational Impact

- **`tts-gateway` holds streaming connections per active turn** — shorter-lived
  than ASR streams but with the same shape. Cancellation must be reliable; a
  synthesis that keeps running after barge-in wastes money and, worse, can leak
  audio if the cancel loses a race with the frame writer.
- **Per-provider health signals:** time-to-first-byte, stream establishment
  failures, mid-stream disconnects, cancellation latency.
- **Voice catalogue is operational state.** Voice IDs are provider-specific and
  must be mapped, versioned, and kept stable per subscriber. A provider removing
  or changing a voice is a product-visible incident.
- **`tools/audio-fixtures` covers output too**, not only input: a golden set of
  agent utterances across languages, checked for pronunciation regressions after
  a provider model update.

## 10. Security Impact

- **The synthesiser receives the agent's generated text**, which may quote the
  caller and may reference subscriber context. It carries `PERSONAL` data in
  practice and the transfer is treated accordingly (§7, ADR-0012).
- **The output sanitiser is a security control, not only a quality one.** Without
  it, a successful prompt injection (ADR-0006 §10) could cause the agent to emit
  text that the TTS reads aloud verbatim to the caller — turning a prompt
  injection into a data-disclosure channel over the phone.
- **Voice cloning is out of scope and explicitly prohibited.** The agent uses a
  stock synthetic voice. Cloning the subscriber's voice would create an
  impersonation capability with obvious abuse potential and unclear consent
  status; it is not a feature we will build.
- **The agent must identify itself as an AI** at the start of the call
  (ADR-0012). This is a product requirement with regulatory weight, and it is
  enforced in the Tier 0 opening utterance (ADR-0006 §5) where it cannot be
  affected by model behaviour.
- Vendor credentials in the secret manager, rotated per `SECURITY.md`.

## 11. Cost Impact

TTS is the smallest of the three metered inference components (ASR ADR-0005 §11,
LLM ADR-0006 §11), but its cost model has a distinctive property:

- **Billing is per character of input text**, so cost is directly proportional to
  how much the agent says. This is the *same* lever as LLM output tokens and
  telephony duration — the agent's verbosity shows up three times in the bill.
  Brevity is therefore the highest-leverage cost control in the entire pipeline,
  and it improves the product simultaneously.
- **Cancelled synthesis is often still billed** for the characters submitted.
  Aggressive sentence-level streaming means we sometimes synthesise a clause the
  caller interrupts before hearing. This is an accepted cost of barge-in
  responsiveness; it is bounded by segmenting at clause rather than paragraph
  granularity.
- **Shadow evaluation is additive** on a sampled subset, capped explicitly.

## 12. Performance Impact

Budget: **90 ms p50 / 180 ms p95** time-to-first-byte (ADR-0011). Structure:

- **TTFB is what we budget, not synthesis duration.** Once frames are flowing, the
  caller hears continuous speech; total synthesis time is irrelevant provided the
  stream keeps up with playback.
- **The segmenter's first-clause latency is on the critical path** and is ours,
  not the vendor's. It must emit as early as a clause boundary permits.
- **Direct 8 kHz µ-law output removes a resample** (C3), saving a small but real
  amount on every frame.
- **Cancellation latency is a first-class SLI.** Barge-in that takes 300 ms to
  silence the agent feels broken regardless of how good the rest of the budget is.
- **Connection pre-warming.** Establishing a synthesis stream at the moment the
  first clause is ready adds handshake latency to every turn; the connection is
  established when the turn begins.

## 13. Scalability Impact

- Concurrent synthesis streams is the capacity unit, consistent with ADR-0002
  §13, ADR-0004 §13 and ADR-0005 §13.
- **Vendor quota is a hard ceiling** and must be contracted ahead of peak.
- Multi-provider routing gives overflow capacity: English-dominant traffic can
  shift to the secondary under primary saturation. As elsewhere, this only works
  if the secondary carries continuous traffic rather than sitting cold.
- Synthesis streams are shorter-lived than ASR streams, so `tts-gateway` churns
  connections faster — connection establishment cost matters more here than in
  `asr-gateway`, which is why pre-warming is called out in §12.

## 14. Migration Strategy

**The `Synthesiser` port plus the voice map is the migration strategy.**

1. **Phase 1 (launch).** ElevenLabs primary, Cartesia secondary, Sarvam shadow.
2. **Phase 2.** Promote Sarvam for Indic-dominant sessions **if and only if**
   `tests/eval` shows quality parity at acceptable TTFB. This would also move the
   Indic path onto in-country processing, resolving the §7 residency trade-off —
   which is a compliance benefit, not only a quality one.
3. **Phase 3 (optional).** Re-evaluate Google/Azure if their streaming latency
   becomes competitive; residency would then favour them for the default path.
4. **Rollback** is a routing-policy change at every phase. Voice identity must be
   preserved across the switch (C7).

## 15. Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| TTFB exceeds budget under load | Medium | High | Connection pre-warming; secondary overflow; TTFB gated in `tests/eval` |
| Barge-in cancellation is slow or races the frame writer | Medium | Critical | Cancellation latency as a dedicated SLI; frame writer checks cancellation before every write |
| Unnatural prosody from clause-level segmentation | High | Medium | Segment at clause not word; golden-utterance regression fixtures |
| Provider changes or removes a voice | Medium | High | Voice map versioned; provider notice period; secondary voice mapped in advance |
| Injected text read aloud to the caller | Low | Critical | Output sanitiser as a security control; injection suite in `tests/eval` |
| Cross-border transfer without valid consent | Low | Critical | Consent state gates routing; transfer recorded in the data map |
| Cancelled-but-billed synthesis inflates cost | High | Low | Clause-level granularity bounds the waste; measured |
| Failover changes the agent's voice mid-call | Medium | Medium | Voice identity mapped per provider; failover prefers same-voice target |

## 16. Future Review Trigger

Revisit when **any** holds:

- Sarvam Bulbul reaches quality parity on the golden utterance set at TTFB within
  budget — promote and resolve the residency trade-off
- Google or Azure streaming TTFB comes within **20%** of the Flash/Sonic class —
  the residency-correct option becomes viable as default
- Measured TTFB p95 exceeds **180 ms** sustained for 7 days
- Barge-in cancellation latency p95 exceeds **one frame interval**
- TTS cost exceeds **20%** of blended per-call inference spend
- Any provider's voice catalogue changes in a way that breaks subscriber voice
  stability

## 17. References

- ADR-0004 (media transport), ADR-0005 (streaming STT), ADR-0006 (LLM routing),
  ADR-0011 (latency budget), ADR-0012 (privacy and residency)
- `services/python/tts-gateway` — `Synthesiser` port, segmenter, voice map
- `tools/audio-fixtures`, `tests/eval`
