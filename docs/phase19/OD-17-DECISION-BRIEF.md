# OD-17: speech providers. What is known, what is missing, and your choices

- **Status: OD-17 is open. The owner DEFERRED all speech-model access and evaluation on 2026-11-04** ([`OWNER-DECISION-PACKET-2026-11-04.md`](OWNER-DECISION-PACKET-2026-11-04.md) §3).
  - **Not authorized:** Hugging Face sign-in for these models, accepting their conditions, downloading or executing them, voice recordings, GPU or cloud provisioning, hosting or activating a speech model.
  - **Any future step (A1–A5) needs separate, explicit owner approval.**
  - ADR-0005 (speech-to-text) and ADR-0007 (text-to-speech) are **Accepted and unchanged**.
  - No Hugging Face account has been created, no model terms accepted, no model downloaded, no recording made or uploaded, and nothing hosted or bought.
- **Prepared:** 2026-11-04 by Claude Code, on main `7ed8f2f`. This is the short form of three documents: [`SPEECH-PROVIDER-POLICY-CONFLICT.md`](SPEECH-PROVIDER-POLICY-CONFLICT.md) (the conflict and the options), [`SPEECH-OPEN-MODELS-EVALUATION.md`](SPEECH-OPEN-MODELS-EVALUATION.md) (the candidates) and [`SPEECH-EVALUATION-PREPARATION.md`](SPEECH-EVALUATION-PREPARATION.md) (the method and the owner actions A1–A5). It is AI-prepared research from publishers' public pages (`ARCHITECTURE_FREEZE.md` §6.1 rule 9), not an independent review.
- **Separate from the app's screen language.** Choosing a language in the app never selects or calls a speech model, and the app's translations never pass through one.

## 1. The decision

**The conflict:**
- **The Accepted ADRs** name paid vendors: Google Cloud STT v2, Deepgram and Sarvam for speech-to-text; ElevenLabs, Cartesia and Sarvam for text-to-speech.
- **Your policy of 2026-11-01** allows free and open-source only.
- **Your six languages** include Malayalam and Kannada, which ADR-0005 does not name.

Both cannot hold. Until you decide, the voice pipeline can't be built under the policy, and the ADRs stay in force as written.

| Option | What it means | Effect on the ADRs |
|---|---|---|
| **A** | Replace the ADRs with open models: IndicConformer for Indian-language speech-to-text, something else for English, and Indic Parler-TTS | New ADRs, Proposed until you accept them, with the independent human review (OD-12) |
| **B** | Exempt speech from the free-only policy, and keep the ADRs' vendors | None. The policy gains an exception, and paid vendor accounts follow, each with its own approval |
| **C** (recommended) | Keep the ADRs as the target, measure the open models first, then choose A or B, or a mix by language | None now |

**Recommendation: C**, because the facts that decide between A and B are not known yet (§2), and measuring them needs no paid vendor. **But C needs some of the actions in §3**, at least A1 and A2.

## 2. What is known, and the remaining evidence gaps

**Known, from the publishers' pages:**

| | IndicConformer 600M (speech-to-text) | Indic Parler-TTS (text-to-speech) |
|---|---|---|
| Of your six languages | Hindi, Tamil, Telugu, Malayalam, Kannada. **Not English**, and Hinglish is not claimed | All six |
| Licence | MIT | Apache-2.0 |
| Access | **Gated:** terms shown only after signing in to Hugging Face. **Unread** | Gated, the same way. **Unread** |
| Published latency | None | "Under 500 ms" to first audio on an unnamed GPU: **over 5 times ADR-0007's 90 ms p50 budget** |
| Published quality | Hindi word error rate 13.2 on one benchmark; no other language | Native-speaker scores for the five Indian languages, on wideband audio; English not scored |
| Audio | Takes 16 kHz; phone calls are 8 kHz | Produces wideband audio; downsampling for calls is routine |
| Code it needs | **`trust_remote_code`**: loading runs code from the model's repository | A library installed from its GitHub repository |

**The gaps.** Each says whether it decides OD-17, and what closing it needs:

| # | Gap | Decisive? | Needs |
|---|---|---|---|
| **G1** | **Does IndicConformer stream, and how fast is its final transcript on 8 kHz call audio?** ADR-0005 allows 120 ms p50 / 250 ms p95 after the caller stops. Nothing published says it streams | **Yes:** without streaming it can't meet the budget | A1, A2, A3, A4 |
| **G2** | **Its accuracy on real 8 kHz calls** in each of the five languages. Only Hindi is published, on other audio | **Yes** | A1, A2 |
| **G3** | **English and Hinglish speech-to-text.** IndicConformer has neither. Whisper covers English, but ADR-0005 rejected it for latency. Something must also detect each call's language and route it | **Yes** for option A | A1, A2, and a candidate for English |
| **G4** | **Indic Parler-TTS's first audio on the hardware the product would use**, against 90 / 180 ms. The publisher's own figure misses it | **Yes:** if it can't be met, A needs you to accept a slower first response | A1, A3 |
| **G5** | **Its quality over a phone line**, rated by native listeners, English included | Yes | A1, A2 (listeners, not recordings), A3 |
| **G6** | **Calls per GPU within the budgets, and so the hosting cost.** It also needs a forecast of peak simultaneous calls, which doesn't exist (X5) | Yes, for the cost of A | A3, then X5 |
| **G7** | **The gated terms**, including whether a product may use the named speakers' voices commercially | **Yes:** terms you can't accept rule a model out | A1 (you read them) |
| **G8** | **The security review** of `trust_remote_code` and of the libraries | Must pass before any call audio reaches a model | A4, and the independent human review (OD-12) |
| **G9** | **Option B's own gaps:** whether the ADRs' vendors handle Malayalam and Kannada at the same budgets, and their cost. **Not verified here:** checking needs vendor accounts | Yes, for B | Vendor accounts, each needing your approval |

**What is already ready, and needs no approval:**
- the measurement method (`SPEECH-EVALUATION-PREPARATION.md` §2);
- an offline scorer, `tools/speech-eval/score.py`: it checks a test set's consent records and scores accuracy and latency against the ADR budgets;
- the Go interfaces an adapter would implement, and the metrics it would report.

**Nothing has been measured.**

## 3. Your choices: the actions, each needing your explicit approval

| # | Action | Cost | Data and privacy | Closes |
|---|---|---|---|---|
| **A1** | **You** create a Hugging Face account (or use yours), **read** the two models' terms, and accept them if acceptable. Then download them | None for a basic account | Your account information is shared with the publisher, as the gate says. The terms may add obligations | G7, and enables G1–G5 |
| **A2** | **Collect a consented test set:** real 8 kHz calls in each language, about 200 utterances or 30 minutes per language, transcribed by two people | People's time; possibly paid participants | **Voices are personal data.** Consent, storage location and retention must be decided **before** recording, under ADR-0012. Never uploaded to a third party | G1, G2, G3, G5 |
| **A3** | **Hardware for measuring:** a GPU you already have, or a cloud GPU in India for the test hours only | **A cloud GPU charges while it runs, even idle** | The test set goes onto that machine | G1, G4, G5, G6 |
| **A4** | **Allow `trust_remote_code`** for IndicConformer, after a review, on a pinned revision, in an isolated container with no network | None | It runs the publisher's code | G8 (with OD-12) |
| **A5** | **Decide OD-17** from the measurements | Hosting, if open models win | — | — |

**A cheaper first step, if you want one:** approve **A1 only**, and read the terms.
- If they are unacceptable, option A is ruled out with no cost and no data shared.
- If acceptable, a **CPU-only run** on a machine you already have can show whether IndicConformer streams and its real-time factor. That needs A1, A4 and a few consented recordings: your own voice, for example.
- It can't show GPU latency or cost (G4, G6).

## 4. What doesn't change, whatever you choose

- **The ADRs are unchanged** until you accept a replacement. A new ADR is Proposed until then.
- **Any model or vendor that processes call audio needs the independent human review** (OD-12) before it is used on real calls.
- **The app's screen language (A58l) and its translations stay separate** from speech.
- **Residency:** self-hosting in India meets ADR-0012 by construction; the ADRs' vendors need region pinning.
