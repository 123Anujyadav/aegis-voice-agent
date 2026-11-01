# Speech providers: the accepted ADRs against the free and open-source policy

- **Status: analysis only. Nothing is superseded or changed.** ADR-0005 and ADR-0007 are **Accepted**. Superseding or amending an Accepted ADR is reserved to the owner (`ARCHITECTURE_FREEZE.md` §6.1). A vendor that processes personal data is high-risk, so a replacement also needs the independent human review (rule 8; OD-12). This document records the conflict and the evidence for the owner's decision (**OD-17**).
- **Review:** AI-performed research (§6.1 rule 9), not an independent human review.
- **As of:** 2026-11-01.

## 1. The conflict

| Source | Says |
|---|---|
| **ADR-0005** (Accepted): streaming speech-to-text | Google Cloud STT v2 primary, Deepgram secondary, Sarvam for Indic evaluation. All three are **paid** services. Languages: Indian English, Hindi and Hinglish; Bengali, Tamil, Telugu and Marathi as expansion |
| **ADR-0007** (Accepted): streaming text-to-speech | ElevenLabs Flash primary, Cartesia secondary, Sarvam for Indic evaluation. All **paid** |
| **Owner policy** (2026-11-01) | Free and open-source only: no paid API, SDK, subscription or cloud service |
| **Owner direction** (2026-11-01) | Hindi, English, Tamil, Telugu, **Malayalam and Kannada**, which ADR-0005 does not name |

Both cannot hold. Until the owner decides, the voice pipeline cannot be built under the policy, and the ADRs stay in force as written.

## 2. Free and open-source candidates (verified against primary sources)

| Model | Kind | Licence | Languages (of the six) | Access | Source |
|---|---|---|---|---|---|
| **AI4Bharat IndicConformer 600M multilingual** | Speech-to-text | **MIT** | Hindi, Tamil, Telugu, **Malayalam, Kannada** (22 Indian languages); **not English** | **Gated:** a Hugging Face account must accept conditions | [model card](https://huggingface.co/ai4bharat/indic-conformer-600m-multilingual), [repository](https://github.com/AI4Bharat/IndicConformerASR) |
| **AI4Bharat Indic Parler-TTS** | Text-to-speech | **Apache-2.0** | **All six**, English included (21 languages) | **Gated**, as above | [model card](https://huggingface.co/ai4bharat/indic-parler-tts) |
| **OpenAI Whisper** | Speech-to-text | **MIT** (code and weights) | All six, English included | Open | [repository](https://github.com/openai/whisper) |

**Whisper's Indic accuracy is doubtful.** OpenAI's reported word error rates include Telugu 82.8, Kannada 77.7 and Malayalam 101.1. Independent analyses show its text normalisation strips vowel signs from Brahmic scripts, which understates the error even for Hindi and Tamil ([Deepgram analysis](https://blog.deepgram.com/how-openai-s-text-normalization-hides-whisper-s-true-word-error-rate-for-south-asian-and-southeast-asian-languages); [arXiv 2409.02449](https://arxiv.org/pdf/2409.02449v3)). So it is a candidate for English, not for the Dravidian languages, unless fine-tuned and measured.

A plausible free stack is **IndicConformer** (Indic speech-to-text), plus **Whisper** for English, plus **Indic Parler-TTS**.

## 3. What "free" does not remove

- **Compute.** Self-hosted models need servers, probably with GPUs for streaming latency. That is an infrastructure cost, and the budget is the owner's (X5). The policy forbids paid *services*; whether paid *hosting* for open models is allowed is part of OD-17.
- **Accounts.** Both AI4Bharat models are gated behind a Hugging Face account and accepted conditions. Creating that account is the owner's; Claude Code does not create external accounts.
- **Unverified here:**
  - streaming support and latency at telephony quality (8 kHz, noisy, code-switched);
  - accuracy on this product's calls;
  - the content of the gated conditions.

  ADR-0005 C3 and ADR-0011's latency budget would have to be re-measured.
- **Residency improves.** Self-hosting in India keeps audio in-region under ADR-0012 and invariant I2, which the cloud vendors needed region pinning for.

## 4. Options for the owner (OD-17)

| # | Option | Effect on the ADRs |
|---|---|---|
| A | **Supersede ADR-0005 and ADR-0007** with an open-model architecture (§2), after an evaluation on recorded test calls | New ADRs, Proposed until the owner accepts them with the independent review |
| B | **Exempt speech from the free-only policy** and keep the ADRs | None; the policy gains an exception |
| C | **Keep the ADRs as the target, and evaluate open models first** (`packages/go/evaluation` exists), deciding with measurements | None now; decide on evidence |

**Recommended: C, then A or B on the evidence.** The deciding facts (Indic accuracy at 8 kHz, streaming latency, hosting cost) are not known yet, and an evaluation can run without any provider account beyond the model downloads.
