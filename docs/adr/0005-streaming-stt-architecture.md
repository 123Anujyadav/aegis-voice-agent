# ADR-0005: Streaming STT — Google STT v2 primary, Deepgram secondary, Sarvam for Indic evaluation

- **Status:** Accepted
- **Date:** 2026-08-06
- **Deciders:** Lead Staff Engineer
- **Consulted:** —
- **Informed:** All engineering, Product
- **Depends on:** ADR-0004

---

## 1. Context

`asr-gateway` converts the caller's speech into text for the AI tier. It sits at
the front of the latency budget and at the front of the quality chain: every
downstream component — intent classification, fraud scoring, the LLM's reply — is
working from ASR output, and errors there propagate silently rather than failing
loudly.

The input is what ADR-0004 delivers: **8 kHz narrowband telephony audio**, lossy,
often noisy, from an Indian caller.

## 2. Problem Statement

Which speech recogniser, and how do we structure `asr-gateway` so that the choice
is revisable?

Three properties matter and they trade against each other:

1. **Latency** — partial transcripts must arrive continuously, and the final
   transcript must be available within ~120 ms of the caller stopping speaking
   (ADR-0011). Batch recognisers are disqualified outright.
2. **Indic and code-switched accuracy** — the majority case in our market is not
   English and not Hindi but **Hinglish**: a single utterance switching between
   them mid-sentence, often mid-clause. A recogniser that scores well on clean
   Hindi and clean Indian English can still be unusable on the actual traffic.
3. **Residency** — audio is `SENSITIVE` and personal (ADR-0012). Sending it to a
   recogniser that processes outside India is a compliance decision, not just a
   vendor decision.

## 3. Constraints

| # | Constraint | Source |
|---|---|---|
| C1 | True streaming with interim results; final transcript ≤ 120 ms p50 / 250 ms p95 after end-of-speech | ADR-0011 |
| C2 | 8 kHz narrowband input; must be a telephony-tuned model, not a re-sampled wideband one | ADR-0004 |
| C3 | Indian English, Hindi, and code-switched Hinglish; Bengali/Tamil/Telugu/Marathi as expansion | Product |
| C4 | Audio processed in India, or covered by an explicit consent and transfer basis | ADR-0012 |
| C5 | Provider-swappable without touching `ai-orchestrator` | Risk |
| C6 | Endpointing must be controllable — we decide when a turn ended, not the vendor's default | ADR-0011 |
| C7 | Per-minute cost must sit within the inference budget alongside LLM and TTS | ADR-0003 §11 |

## 4. Considered Options

1. **Google Cloud Speech-to-Text v2** — `telephony` model, `asia-south1`
2. **Deepgram Nova-3** — latency leader, streaming-native
3. **Sarvam AI (Saarika)** — Indian vendor, Indic-specialised
4. **Azure AI Speech** — broad language coverage, Central India region
5. **Self-hosted Whisper / faster-whisper** — full control, no vendor
6. **AssemblyAI** — strong English, weak Indic

## 5. Decision

**A routed, multi-provider `asr-gateway` with Google STT v2 as the default.**

- **Primary: Google Cloud STT v2**, `telephony` model family, pinned to
  `asia-south1` (Mumbai). Default for all traffic.
- **Secondary: Deepgram Nova-3**, used for English-dominant sessions where its
  lower time-to-final measurably helps, and as the failover target when Google
  degrades.
- **Under evaluation: Sarvam AI**, run in shadow mode against production audio
  from launch, promoted to primary for Indic-dominant sessions only when the
  eval suite (`tests/eval`) demonstrates it.

Routing is decided per session from the subscriber's locale, the detected
language of the first utterance, and a runtime-configurable policy. Providers sit
behind a `Recogniser` port in `asr-gateway`; no vendor SDK type crosses it.

**Endpointing is ours, not the vendor's.** `media-relay` runs VAD on the internal
audio bus and `session-orchestrator` decides when a turn has ended (ADR-0011).
Vendor endpointing is disabled or ignored.

## 6. Why This Option Was Selected

**Google STT v2 as default because it is the only candidate that satisfies C2,
C3 and C4 simultaneously.**

- **C4 is the decisive constraint.** `asia-south1` gives us in-country
  processing under a contractual arrangement we can put in a data map. Deepgram
  and AssemblyAI have no Indian processing region; using them as primary would
  force a cross-border transfer for every screened call — a defensible position
  with consent, but not the right default for the majority of traffic.
- **C2 is where naive comparisons go wrong.** A telephony-tuned model trained on
  8 kHz audio beats a better wideband model fed upsampled narrowband. Google's
  `telephony` model family is explicitly this; several competitors' streaming
  models are not.
- **C3 breadth.** Google covers the Indic expansion languages in one integration.
  Adding Bengali or Tamil later is a configuration change, not a new vendor.

**Deepgram as secondary because it is genuinely faster**, and because a failover
target on a *different* infrastructure is worth more than a second Google region.
For an English-dominant call its lower time-to-final buys back budget that the
LLM can spend.

**Sarvam in shadow rather than in production** because the Indic-specialist claim
is exactly the kind of claim that must be measured on our own audio before it is
trusted. Shadow mode costs a second inference on a sampled subset and gives us
the data to promote it honestly.

## 7. Trade-offs

**Accepted.**

- **Accuracy is capped by the input** (C2). Narrowband telephony audio has lost
  information no recogniser recovers. Word error rate on Hinglish over 8 kHz will
  be materially worse than published wideband benchmarks, and product design must
  assume the transcript is imperfect — the agent must be robust to
  misrecognition rather than assume clean input.
- **Multi-provider means multi-integration.** Three recognisers with different
  streaming protocols, different interim-result semantics, and different
  confidence scales. The `Recogniser` port must normalise all of it, and
  normalisation loses vendor-specific signal.
- **Shadow evaluation costs real money** — a second recognition on sampled
  traffic — and it is worth it.
- **We own endpointing** (C6), which means we own its failure modes. Vendor
  endpointing is easier and worse.

**Not accepted:** we do not accept a default path that ships call audio outside
India, and we do not accept a single-vendor dependency on the front of the
quality chain.

## 8. Alternatives Rejected

**Deepgram as primary.** The best pure latency and a genuinely good streaming
API. Rejected as *default* on C4 (no Indian processing region) and C3 (Indic and
code-switched coverage weaker than the market requires). Retained as secondary
where those objections do not bind.

**Azure AI Speech.** Credible on C3 and C4 (Central India). Rejected as a close
third: no dimension on which it clearly beats Google for our workload, and a
third first-class integration is cost without benefit. Reconsider if the Google
relationship degrades.

**Self-hosted Whisper / faster-whisper.** Rejected on C1. Whisper is a
sequence-to-sequence model over 30-second windows; streaming implementations
chunk and stitch, which produces either high latency or unstable partials — both
fatal for turn-taking. Also transfers the entire operational burden of GPU
inference to us for a quality result that is not better on narrowband Indic. Good
for offline transcript refinement (§14), wrong for the live path.

**AssemblyAI.** Rejected on C3 and C4 for the same reasons as Deepgram, without
Deepgram's compensating latency advantage.

## 9. Operational Impact

- **`asr-gateway` is a streaming proxy with vendor connections held open per
  session.** Its scaling and shutdown characteristics resemble `media-relay`
  (ADR-0004 §9) more than a request/response service.
- **Per-provider health is a distinct signal.** Time-to-first-partial,
  time-to-final, stream establishment failures, and mid-stream disconnects,
  tracked per provider and alerted independently.
- **Failover must be mid-session capable.** A recogniser that dies 8 seconds into
  a call cannot be retried from the beginning — the audio is gone. `asr-gateway`
  must be able to open a secondary stream and continue, accepting a gap.
- **`tools/audio-fixtures` is the regression harness.** Accuracy regressions are
  invisible without a fixed corpus: Indian English, Hindi, code-switched
  Hinglish, varying SNR, GSM/AMR-NB codec artefacts. Run in `tests/eval`, gated
  per ADR-0006 §9.

## 10. Security Impact

- **Raw audio and transcripts are both `SENSITIVE`.** The transcript is arguably
  more dangerous than the audio because it is searchable.
- **Cross-border transfer is a consent-gated path.** When routing to Deepgram, the
  subscriber's consent state must permit it and the routing decision must be
  recorded in the audit trail (ADR-0012). A residency decision made implicitly by
  a load balancer is a compliance failure.
- **Vendor credentials** in the secret manager, rotated per `SECURITY.md`, scoped
  to recognition only.
- **Prompt-injection surface.** The transcript is untrusted caller-controlled text
  that flows directly into an LLM prompt (ADR-0006). It must be delimited and
  never concatenated into instruction context — a caller can and will say "ignore
  your instructions".
- **No audio persisted by `asr-gateway`.** Retention is `transcript-service`'s
  job under an explicit policy, not a side effect of recognition.

## 11. Cost Impact

Per-minute recognition cost is one of three metered inference components
(alongside ADR-0006 and ADR-0007). Notes specific to ASR:

- **We are billed for audio duration, including silence.** Streaming a full
  screening interaction means paying for the caller's pauses and our own agent's
  speaking time unless we gate the stream. `media-relay` should suppress
  transmission during confirmed agent speech and long silences — a direct,
  material saving.
- **Shadow evaluation is additive cost** on a sampled subset. Budget it explicitly
  and cap the sample rate.
- Duration remains the master lever, as in ADR-0002 §11 and ADR-0003 §11.

## 12. Performance Impact

Budget: **120 ms p50 / 250 ms p95** from end-of-speech to final transcript
(ADR-0011). Structure:

- **Interim results arrive continuously** and are used speculatively —
  `ai-orchestrator` may begin prompt assembly and fraud pre-scoring on partials
  before the final transcript lands. This is how the ASR hop overlaps rather than
  serialises with the LLM hop.
- **Endpointing latency is the dominant term and is ours** (C6). The 250 ms
  endpoint-detection window in ADR-0011 is a product decision about how long a
  pause must be before we treat the turn as ended; it is not vendor latency.
- **The stream must be pre-warmed.** Opening a recogniser stream at the moment the
  caller starts speaking adds establishment latency to the first turn. The stream
  is opened when the call is answered.

## 13. Scalability Impact

- Concurrent streams, not requests, is again the capacity unit — consistent with
  ADR-0002 §13 and ADR-0004 §13.
- **Vendor quota is a hard ceiling** and must be contracted ahead of peak. Unlike
  our own services, it cannot be autoscaled.
- Multi-provider routing gives real overflow capacity: when Google quota
  saturates, English-dominant traffic can shift to Deepgram. This is the same
  mechanism as failover, which is why the secondary must carry continuous
  traffic rather than sitting idle.

## 14. Migration Strategy

**The `Recogniser` port is the migration strategy.** It normalises stream
lifecycle, interim/final results, confidence, and language identification.

1. **Phase 1 (launch).** Google primary, Deepgram secondary, Sarvam shadow.
2. **Phase 2.** Promote Sarvam to primary for Indic-dominant sessions **if and
   only if** `tests/eval` shows a WER improvement on the golden corpus at
   acceptable latency. Promotion is a routing-policy change, not a deploy.
3. **Phase 3 (optional).** Offline transcript refinement — a batch pass with a
   larger model (Whisper-class) over stored audio to improve the transcript the
   subscriber reads, distinct from the live path. This is where self-hosted
   Whisper earns its place.
4. **Rollback** is a routing-policy change at every phase.

## 15. Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Hinglish accuracy is below product-viable on narrowband | Medium | Critical | Golden-fixture evaluation before launch; Sarvam shadow; agent designed to tolerate misrecognition |
| Vendor streaming API change or deprecation | Medium | High | `Recogniser` port; secondary already live; contractual notice |
| Cross-border routing occurs without valid consent | Low | Critical | Consent state gates routing; residency recorded in audit trail; policy enforced in the port, not the caller |
| Mid-session recogniser failure | Medium | High | Mid-session failover with accepted gap; measured as a dedicated SLI |
| Vendor quota exhaustion at peak | Medium | High | Contracted headroom; multi-provider overflow; admission control upstream |
| Transcript used as trusted input to the LLM | Medium | Critical | Delimited untrusted-input framing (ADR-0006 §10); injection tests in `tests/eval` |
| Silent accuracy regression after a vendor model update | High | High | Golden-fixture regression suite gated in CI; vendor model version pinned where the API allows |

## 16. Future Review Trigger

Revisit when **any** holds:

- Sarvam (or another Indic-specialist) beats Google on the golden corpus by
  **≥15% relative WER** on code-switched Hinglish at equal or better latency
- Measured time-to-final p95 exceeds **250 ms** sustained for 7 days
- Wideband PSTN interconnect becomes available (ADR-0004 §16) — the whole
  accuracy ceiling moves and the comparison must be re-run
- ASR cost exceeds **30%** of blended per-call inference spend
- Any provider's Indian processing region becomes unavailable or is withdrawn

## 17. References

- ADR-0004 (media transport), ADR-0006 (LLM routing), ADR-0011 (latency budget),
  ADR-0012 (privacy and residency)
- `services/python/asr-gateway` — `Recogniser` port and routing policy
- `tools/audio-fixtures`, `tests/eval` — accuracy regression gating
