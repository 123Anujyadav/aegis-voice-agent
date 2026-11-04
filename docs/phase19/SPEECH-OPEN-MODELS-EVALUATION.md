# Free and open speech models for the six languages: what is known, and how to decide

- **Status: desk research only. Nothing was downloaded, run or measured,** and nothing is wired into the product.
- **ADRs:** ADR-0005 (speech-to-text) and ADR-0007 (text-to-speech) are **Accepted and unchanged**. Choosing between them and open models is the owner's decision (**OD-17**), with the independent human review that a vendor or model processing call audio needs (OD-12).
- **Builds on:** [`SPEECH-PROVIDER-POLICY-CONFLICT.md`](SPEECH-PROVIDER-POLICY-CONFLICT.md), which sets out the conflict and the options.
- **Followed by:** [`SPEECH-EVALUATION-PREPARATION.md`](SPEECH-EVALUATION-PREPARATION.md): the two AI4Bharat models side by side, the measurement method, the offline scorer (`tools/speech-eval`), the cost model and the owner actions.
- **As of:** 2026-11-03. AI-performed research (`ARCHITECTURE_FREEZE.md` §6.1 rule 9), not an independent human review.

## 1. The app's language is not the voice's language

The app now lets a subscriber choose the language of its **screens** (A58l, `LOCALISATION.md` §4). That choice:
- never selects a speech model;
- never changes what the assistant speaks or understands on calls;
- never sends anything to a speech service.

The voice languages are A43's and ADR-0005's. They depend on the models below, and **none is decided**.

## 2. What a model must meet (from the accepted ADRs)

| Need | Speech-to-text (ADR-0005) | Text-to-speech (ADR-0007) |
|---|---|---|
| Latency | Final transcript ≤ 120 ms p50 / 250 ms p95 after end of speech, with streaming interim results (C1) | First byte ≤ 90 ms p50 / 180 ms p95 (C1); streaming, abandonable mid-utterance for barge-in (C2) |
| Audio | 8 kHz narrowband telephony, ideally a telephony-tuned model (C2) | 8 kHz telephony output (C3) |
| Languages | Indian English, Hindi, Hinglish; the owner's list adds Tamil, Telugu, Malayalam and Kannada (OD-17) | The same |
| Residency | Audio processed in India, or a consent and transfer basis (C4, ADR-0012) | — |
| Swappable | Behind `asr-gateway`, without touching `ai-orchestrator` (C5) | Without touching `ai-orchestrator` (C5) |

Self-hosting in India meets the residency need (C4) by construction. It does not meet the others by itself: latency, streaming and 8 kHz accuracy have to be measured (§5).

## 3. Candidates

**Licence:** "Commercial use" means the licence allows a paid product. **"Gated"** means a Hugging Face account must accept the publisher's conditions before download, so **the owner's account, not Claude Code's**. "Primary" means the publisher's own page was read for this document.

### Speech-to-text

| Model | Licence | Of the six languages | Streaming | Size and input | Access | Source checked |
|---|---|---|---|---|---|---|
| **AI4Bharat IndicConformer 600M multilingual** | **MIT** | Hindi, Tamil, Telugu, Malayalam, Kannada (22 Indian languages); not English | Not stated on the card | 600M parameters; hybrid CTC + RNN-T Conformer; the example uses **16 kHz** audio | **Gated** | Primary: [model card](https://huggingface.co/ai4bharat/indic-conformer-600m-multilingual). It reports Hindi WER 13.2 on the Vaani benchmark, and it points to a newer "Indic-Transcribe" model (25 languages), not reviewed here |
| **OpenAI Whisper** | **MIT** (code and weights) | All six | No: 30-second windows. Streaming wrappers chunk and stitch, which ADR-0005 rejected for latency and unstable partials | tiny to large | Open | Primary, in the conflict document. Reported WER is poor for Telugu, Kannada and Malayalam |
| **Vosk** | **Apache-2.0** (toolkit) | Hindi and Indian English in its documented language list; no Tamil, Telugu, Malayalam or Kannada found | Yes (Kaldi-based, CPU) | Small models run on phones | Open | **Secondary only:** package listings of its language list. The model page was not reached |
| **sherpa-onnx** (runtime) | **Apache-2.0** | Depends on the model: community ONNX exports exist for Hindi and several Indian languages | Yes; runs on CPU and on Android | — | Open | **Secondary only.** The Indian-language streaming models found are community exports whose licences must be checked at their sources (for example an export of ARTPARK-IISc's SraVaani, which states MIT) |
| **Meta MMS** | **CC-BY-NC 4.0: non-commercial** | Many | — | — | Open | **Excluded:** its licence forbids a paid product |

### Text-to-speech

| Model | Licence | Of the six languages | Streaming | Size | Access | Source checked |
|---|---|---|---|---|---|---|
| **AI4Bharat Indic Parler-TTS** | **Apache-2.0** | **All six**, English included (20 Indian languages and English) | Not stated on the card (an external inference guide mentions streaming) | **0.9B parameters**, F32: about 3.6 GB of weights; GPU recommended by the example code, CPU fallback | **Gated** | Primary: [model card](https://huggingface.co/ai4bharat/indic-parler-tts). It reports native-speaker scores, for example Kannada 88.2, Tamil 75.5, Malayalam 86.5, Hindi 84.8 and Telugu 88.5 (%). No latency figures |
| **AI4Bharat IndicF5** | **MIT** | Hindi, Tamil, Telugu, Malayalam, Kannada (11 Indian languages); not English | Not stated | — | **Gated**, with terms of use not reviewed here | Secondary: the AIKosh listing and a Hugging Face page summary. It clones a reference voice: any voice used needs that speaker's consent |
| **Piper** | Engine and per-voice licences vary, and voices "may have restrictive licences" | Voices named for Hindi, Malayalam and Telugu appear in a third-party listing | Fast on CPU | Small | Open | **Not verified.** Each voice's licence must be read before use |
| **Coqui XTTS-v2** | **Coqui Public Model License: non-commercial** | Hindi among others | — | — | Open | **Excluded:** non-commercial, and its publisher has closed, so no commercial licence can be bought (secondary sources) |

### On the phone

Android's own speech recogniser and text-to-speech engines are free to use, but they are provided by Google or the phone's maker, not open source. Language coverage and whether audio leaves the phone depend on the engine and the device. They could serve the subscriber-side Assistant tab, not the caller-facing assistant, whose call audio reaches the servers over telephony (ADR-0004). Not reviewed in detail here.

## 4. What this leaves

- **The only open candidates with all six languages, or near it, are AI4Bharat's:**
  - IndicConformer for speech-to-text, with Whisper or another model for English;
  - Indic Parler-TTS for text-to-speech.
  
  Both are **gated** (an owner account and accepted conditions), and **neither card states streaming or latency**.
- **Fast, small, streaming CPU options** (Vosk, sherpa-onnx with community models, Piper) **do not cover Tamil, Telugu, Malayalam and Kannada** with verified, licensable models.
- **Excluded by licence:** Meta MMS and Coqui XTTS-v2.
- **The deciding facts are unmeasured:**
  - streaming time-to-final and time-to-first-byte against ADR-0011's budget;
  - accuracy on 8 kHz, noisy, code-switched calls. IndicConformer's example uses 16 kHz input, so 8 kHz audio would be upsampled, which ADR-0005 C2 warns about;
  - the hardware each needs at the product's concurrency.

## 5. How to decide on evidence (OD-17 option C)

1. **Owner actions first:**
   - accept the gated models' conditions with an owner Hugging Face account, after reading them;
   - decide whether paid *hosting* for open models is allowed (the free-only rule forbids paid *services*);
   - decide the hardware budget (X5).
2. **A consented test set:**
   - recordings in each of the six languages, plus Hinglish;
   - 8 kHz telephony audio, with reference transcripts;
   - collected under ADR-0012's consent rules.
   
   **None exists.** It must not be synthesised from the same text-to-speech under test.
3. **Measure on the existing harness:**
   - per language: word error rate, and character error rate for Indic scripts, where vowel signs matter;
   - streaming time-to-final against C1, and time-to-first-byte against ADR-0007 C1;
   - real-time factor and memory per stream on CPU and on one GPU type.
   
   `packages/go/evaluation` and `tools/audio-fixtures` are built for this kind of comparison.
4. **Native-speaker review** of synthesised speech in each language. A score from the model card is not this product's callers.
5. **Then** the owner chooses among:
   - superseding ADR-0005 and ADR-0007 with an open-model ADR;
   - exempting speech from the free-only rule;
   - mixing the two by language.

## 6. Costs and accounts the owner would take on

| Item | What | Recurring |
|---|---|---|
| Hugging Face account | For the gated AI4Bharat models; accepting their conditions | No charge for the account itself; the conditions' content is unread |
| Hosting | Servers in India, probably with GPUs for streaming at 0.6–0.9B parameters | Yes. **Not estimated:** depends on concurrency, which no load test has measured |
| Test recordings | Consented call audio in six languages, with transcripts | One-off collection and review effort |

**Nothing here has been created, downloaded or activated.**
