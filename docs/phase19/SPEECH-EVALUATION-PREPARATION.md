# Evaluating IndicConformer and Indic Parler-TTS: what is known, and what is ready

- **Status: preparation only.** Nothing has been:
  - downloaded, run or measured;
  - accepted on Hugging Face;
  - hosted;
  - recorded.
- **ADRs:** ADR-0005 (speech-to-text) and ADR-0007 (text-to-speech) are **Accepted and unchanged**. Choosing between them and open models is the owner's decision (**OD-17**). Any model that processes call audio also needs an independent human review (OD-12).
- **For deciding:** [`OD-17-DECISION-BRIEF.md`](OD-17-DECISION-BRIEF.md) is the owner's short form: the evidence gaps and the choices.
- **Builds on:** [`SPEECH-OPEN-MODELS-EVALUATION.md`](SPEECH-OPEN-MODELS-EVALUATION.md), which found these two models as the only open candidates near all six languages.
- **As of:** 2026-11-04. AI-performed research from the publishers' public pages (`ARCHITECTURE_FREEZE.md` §6.1 rule 9). **"Computed"** marks arithmetic from a stated figure; **"inferred"** marks an expectation that a measurement must confirm.

**The app's screen language is separate from all of this.** Choosing a language in the app (A58l) never selects, configures or calls a speech model, and the translations of the app's text never pass through one.

## 1. The two models side by side

| | **IndicConformer 600M multilingual** (speech-to-text) | **Indic Parler-TTS** (text-to-speech) |
|---|---|---|
| **Publisher's page** | [ai4bharat/indic-conformer-600m-multilingual](https://huggingface.co/ai4bharat/indic-conformer-600m-multilingual) | [ai4bharat/indic-parler-tts](https://huggingface.co/ai4bharat/indic-parler-tts) |
| **Of the owner's six** | **Hindi, Tamil, Telugu, Malayalam, Kannada.** **Not English**, and code-switched Hindi-English is not claimed | **All six:** English is in its official list. The page's language counts are inconsistent (20, 18 and 21) |
| **Licence** | MIT. Commercial use is allowed by the licence | Apache-2.0. Commercial use is allowed by the licence |
| **Access** | **Gated.** A Hugging Face account must accept conditions and share its account information. **The conditions are only shown after signing in, so they have not been read** | **Gated**, the same way. **The conditions have not been read** |
| **Size** | 600M parameters. Weights about 2.4 GB at 32-bit, 1.2 GB at 16-bit (computed) | 0.9B parameters, F32 safetensors. About 3.6 GB at 32-bit, 1.8 GB at 16-bit (computed) |
| **Memory while running** | Not stated. More than the weights: activations, and a buffer per stream (inferred) | Not stated. More than the weights: a key-value cache per stream grows with the length of speech (inferred) |
| **Architecture** | Conformer, hybrid CTC + RNN-T; either decoder can be chosen | Fine-tuned from Indic Parler-TTS Pretrained, itself based on Parler-TTS Mini. Speech is generated as audio-codec tokens, step by step. **The base model, Parler-TTS Mini v1, uses the DAC codec at 44.1 kHz, 86 frames a second.** Indic Parler-TTS's own configuration is behind the gate |
| **Audio** | **16 kHz input** (the example resamples to it). Telephony audio is 8 kHz, so it would be upsampled: no new information, and the accuracy loss is unmeasured (ADR-0005 C2) | If like its base, 44.1 kHz output, which is downsampled to 8 kHz for a call. That is routine (inferred) |
| **CPU or GPU** | Not stated. The example's install line includes `onnxruntime-gpu` | Not stated. The example uses `cuda:0` when present, otherwise the CPU |
| **Streaming** | **Not stated.** A full-context Conformer normally needs the whole utterance before its final transcript; streaming needs a model trained for it (inferred). **This is the first thing to measure** | **Yes, in chunks:** `ParlerTTSStreamer` in the `parler_tts` library emits audio every `play_steps` frames. Smaller chunks give earlier first audio, until generation falls behind playback |
| **Latency, as published** | None | "Time-to-first audio in under 500ms on a modern GPU", from the library's inference guide. No GPU or setup is named |
| **Against the accepted budget** | ADR-0005 C1: final transcript **≤ 120 ms p50 / 250 ms p95 after end of speech**. Unknown | ADR-0007 C1: first byte **≤ 90 ms p50 / 180 ms p95**. **The publisher's own "under 500 ms" is over 5 times the p50 budget**, even before it is measured on this product's hardware |
| **Real-time on a call** | Unknown: the real-time factor must be under 1 per stream, with room for concurrent calls | Unknown. Generation must stay ahead of playback: more than 86 codec frames a second per stream if it uses its base's codec (computed from the base) |
| **Published quality** | Hindi WER 13.2 on ARTPARK-IISc's Vaani benchmark. **No other language is given** | Native-speaker scores (%), fine-tuned model: **Hindi 84.79, Tamil 75.48, Telugu 88.54, Malayalam 86.54, Kannada 88.17.** English is not in the table. Wideband audio, not telephony |
| **Voices** | — | 69 named speakers from its training data, chosen and styled by a text description. Whether the publisher's terms let a commercial product use a named speaker's voice is part of the unread conditions |
| **Code it needs** | `transformers` with **`trust_remote_code=True`**: loading **runs Python code from the model's repository**, which needs a security review and a pinned revision. Also `torchaudio`, `torch`, ONNX Runtime | `parler_tts`: Apache-2.0, installed from its GitHub repository. Also `transformers`, `torch`, `soundfile` |
| **Note** | The publisher now recommends a newer **"Indic-Transcribe"** model (25 languages). It is not reviewed here | — |

**What the comparison already shows:**
1. **Neither model covers the whole voice path alone.** Speech-to-text has no English and claims no Hinglish, though ADR-0005 targets Indian English and Hinglish. Another model would have to handle those, and something must route each call to the right one.
2. **Indic Parler-TTS's published latency misses ADR-0007's budget** by its publisher's own figure. It could still win on quality and coverage, but only if the owner accepts a slower first response or finds a faster setup. **That is a measured question, then an owner decision.**
3. **The decisive unknowns are measurable:**
   - streaming and time-to-final on 8 kHz audio;
   - time-to-first-audio on the hardware the product would use;
   - concurrent streams per GPU.

## 2. How to measure them

**Prerequisite:** the owner actions in §4. None is done.

### 2.1 The test set

- **Real phone calls at 8 kHz**, recorded with consent under ADR-0012: the participants consent, and every recording carries a consent record. **None exists today.**
- **Per language:** English (Indian), Hindi, Tamil, Telugu, Malayalam, Kannada, and Hindi-English code-switched.
  - Suggested at least **200 utterances, or 30 minutes**, per language;
  - several speakers, ages and regions;
  - quiet and noisy conditions;
  - the sentences callers actually say to a screening assistant: names, numbers, "who is this", reasons for calling.
- **Reference transcripts by two people** who agree on them. **Never text from a text-to-speech model, and never the model under test.**
- **The manifest** is a JSON Lines file, `tools/speech-eval/score.py validate` checks it before use. It requires, for each utterance:
  - an id and a language;
  - a reference transcript;
  - **a consent record;**
  - with `--audio-root`, an 8 kHz, mono, 16-bit WAV.

### 2.2 Speech-to-text

1. **Stream each recording** into the model in 20 ms frames at real speed, the way a call arrives.
2. **Record:**
   - the final transcript;
   - **time-to-final:** from the end of speech, marked in the test set, to the final transcript;
   - the audio and processing seconds.
3. **Score with `tools/speech-eval/score.py stt`.** Per language it reports:
   - **WER**, plus **CER** for Indian scripts, where a wrong vowel sign is one character error;
   - the missing results;
   - time-to-final p50 / p95, **against ADR-0005's 120 / 250 ms**;
   - the real-time factor.

   Text is compared after normalisation: Unicode NFC, punctuation and the danda removed, any script's digits as 0–9, and zero-width joiners dropped. `--fold-nukta` also treats "ज़" as "ज", for transcripts that write it inconsistently.
4. **Concurrency:** increase simultaneous streams on one GPU until p95 time-to-final passes 250 ms. That number of streams is the input to the cost model in §3.

### 2.3 Text-to-speech

1. **Synthesise the assistant's real sentences** in each language, streaming, with a fixed voice description.
2. **Record:**
   - **time-to-first-audio:** from the request to the first audio chunk;
   - the audio and processing seconds.
3. **Score with `tools/speech-eval/score.py tts`:** p50 / p95, **against ADR-0007's 90 / 180 ms**, and the real-time factor per language.
4. **Concurrency:** as above, until p95 time-to-first-audio passes 180 ms.
5. **Listening test:**
   - native speakers rate naturalness and intelligibility from 1 to 5 on the **8 kHz telephone version** of each clip;
   - they also check names and numbers word for word;
   - at least 10 listeners per language, clips in random order;
   - **the model card's scores were on wideband audio with other sentences, and do not replace this.**

### 2.4 Where it plugs in

- **The Go speech layer's interfaces are ready for an adapter:**
  - `packages/go/speech/provider.go`: `STTProvider`, `STTStream`, `TTSProvider`, `TTSStream`, and `Capabilities.Supports(Language)`;
  - `packages/go/speech/harness.go`: a scripted fake.
- **The existing metrics already have the right names:**
  - `speech_final_transcript_seconds` (time-to-final);
  - `speech_first_audio_seconds` (time-to-first-audio);
  - `speech_first_partial_seconds`.
- **The model-serving gateways** (`services/python/asr-gateway`, `tts-gateway`) are **bootstrap only.**
- **A self-hosted model** would sit behind them as an adapter, without touching `ai-orchestrator` (ADR-0005 C5, ADR-0007 C5).

**Nothing is wired.**

## 3. Self-hosting and its costs: a model, not a quote

**Hosting in India** meets ADR-0012's residency need by construction.

**What it costs depends on two numbers nobody has yet:**
- **peak simultaneous calls:** no load model or traffic forecast exists (X5);
- **streams per GPU** within the latency budget, from §2.2 and §2.3.

**The model:**
```
GPUs for speech-to-text  = ceil(peak simultaneous calls / STT streams per GPU)
GPUs for text-to-speech  = ceil(peak simultaneous calls / TTS streams per GPU)
Servers                  = those GPUs + one spare per role, in two zones
Monthly cost             = servers × hourly price × 730 hours
                           + storage for the weights (a few GB per model) + egress
```
- **Hourly prices are not given here.** They change, and must be quoted from the provider and region the owner chooses, on the day of the decision.
- **A recurring charge starts as soon as a GPU server runs**, even idle.

**Cheaper ways to measure first:**
- one GPU for the hours of a test only;
- or a GPU the owner already has;
- **a CPU-only run** on a laptop can measure the real-time factor and show whether CPU serving is plausible. It says nothing about GPU latency.

**Operating costs beyond the server:**
- someone to keep the models, drivers and CUDA versions patched;
- monitoring;
- the evaluation re-run on each model update.

## 4. Owner actions: none taken, each needs explicit approval

**Owner, 2026-11-04: all of A1–A5 are DEFERRED** ([`OWNER-DECISION-PACKET-2026-11-04.md`](OWNER-DECISION-PACKET-2026-11-04.md) §3). None may start without a separate, explicit approval.

| # | Action | Provider and purpose | Account | Cost | Data shared and privacy | Free or local alternative |
|---|---|---|---|---|---|---|
| A1 | Accept the two models' conditions, and download them | **Hugging Face**, for **AI4Bharat**'s gated weights | **The owner's** Hugging Face account, not Claude Code's | No charge for a basic account. **The conditions are unread** and may add terms | The account's information is shared with the publisher, as the gate says | Ungated models: Whisper (rejected on ADR-0005 C1 for latency), Vosk (no Tamil, Telugu, Malayalam or Kannada found) |
| A2 | Collect the consented test set (§2.1) | People recorded, with consent under ADR-0012 | — | People's time; possibly paid participants | **Personal data: voices.** Consent, storage location and retention must be decided **before** recording. **Never uploaded to a third party** | None: no public 8 kHz consented call set in these languages was found |
| A3 | Hardware for measurement | A GPU the owner has, or a cloud GPU in India for the test hours | A cloud account if cloud | **Recurring while running** | The test set goes onto that machine | CPU-only on a laptop, for the real-time factor only |
| A4 | Allow `trust_remote_code` for IndicConformer | — | — | — | Runs the publisher's code when the model loads | Pin a reviewed revision; run it in an isolated container with no network |
| A5 | Decide OD-17 from the measurements | — | — | Hosting (§3) if open models win | — | Exempt speech from the free-only rule (keep ADR-0005/0007's vendors), or mix by language |

## 5. What is ready now

- **`tools/speech-eval/score.py`** (standard library only, offline): validates a test set's manifest and consent records, and scores speech-to-text and text-to-speech against the ADR budgets. 12 tests, run in `pr-python`, include a run with networking disabled.
- **This plan, and the Go interfaces and metrics it plugs into (§2.4).**
- **Not ready:**
  - the test set, the models, the hardware;
  - an adapter;
  - any measurement.
