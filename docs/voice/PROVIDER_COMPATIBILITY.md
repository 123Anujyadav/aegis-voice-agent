# Provider Compatibility

**Status:** adapter contracts **VERIFIED** for all four · real-runtime execution
**varies**, and is stated exactly.

---

## 1. Summary

| Adapter | Port | Contract | Real runtime **on this machine** | Real inference |
|---|---|---|---|---|
| `whispercpp` | `speech.STTProvider` | VERIFIED | **NOT AVAILABLE** — whisper.cpp not installed | **NOT RUN** |
| `whispercli` | `speech.STTProvider` | VERIFIED | **AVAILABLE** — `whisper.exe`, models cached | **EXECUTED** |
| `piper` | `speech.TTSProvider` | VERIFIED | **NOT AVAILABLE** — not on PATH | **NOT RUN** |
| `ollama` | `runtime.Provider` | VERIFIED | daemon **AVAILABLE**, **zero models** | **NOT RUN** |

"Contract VERIFIED" means the adapter satisfies its frozen port and behaves
correctly against a deterministic stand-in. It is **not** a claim that the real
engine ran. Those are different claims and are never merged here.

## 2. Declared capabilities — read from the source

| Adapter | Streaming | PartialResults | Notes |
|---|---|---|---|
| `whispercpp` | configurable (`cfg.Streaming`) | tracks `Streaming` | a front-end that cannot stream cannot emit partials |
| `whispercli` | **`false`** | **`false`** | batch-only — see §4 |
| `piper` | `true` | n/a | qualified — see §5 |
| `ollama` | `true` | n/a | `Thinking=false`, `ToolCalling=false` — see §6 |

## 3. whispercpp — NOT AVAILABLE here

Adapter implemented to spec; contract proven against a stand-in; **whisper.cpp
is not installed on this machine**, so no real inference was executed and none is
claimed. 12 tests pass.

## 4. whispercli — EXECUTED, and it is **not** real-time

The only provider whose **real runtime ran end to end** (Task 17).

**MEASURED:**

| Quantity | Value |
|---|---|
| Executable | `…\Python312\Scripts\whisper.exe` |
| Model | `tiny` (cached; `tiny.pt` 72 MB, `base.pt` 138 MB already present) |
| Fixture | 4.12 s of real speech (Windows SAPI + ffmpeg) |
| Transcript | 55 characters recognised |
| **Provider inference time** | **3.36 s** |

> **This is provider inference latency, not orchestration overhead.** It belongs
> to category B and must never be compared with the ~95 µs whole-turn
> orchestration figure or with any ADR budget.

**KNOWN LIMITATION — batch, not streaming.** `Capabilities.Streaming = false`
and `PartialResults = false`, truthfully declared. The Python tool accepts a
file path, not a pipe: audio is buffered for the whole utterance, then
transcribed. It **must not be presented as a real-time STT solution.** It is
usable for development and for offline work, not for a low-latency voice loop.

It is also the one adapter that writes a temporary WAV — a documented, bounded
exception covered in [SECURITY_AUDIT.md](SECURITY_AUDIT.md) (SEC-2).

## 5. piper — NOT AVAILABLE here

Adapter implemented; contract proven against a **stand-in engine speaking
Piper's protocol** (line-in on stdin, raw PCM on stdout). 35 tests pass,
including ordering, framing, bounded queues, cancellation promptness, engine
death and orphan-process cleanup.

**No real Piper inference was executed, and no PCM was fabricated.** Piper is not
on PATH; no binary was installed.

`Capabilities.Streaming = true` carries a documented qualification: audio arrives
while the process runs, so playback can begin before the whole response is
synthesised — but the first sample of a chunk does **not** precede that chunk's
synthesis. **Chunk size is the latency knob**, and the package comment says so
rather than letting the flag overstate the case.

## 6. ollama — DEVELOPMENT-ONLY; no model, so **NOT RUN**

**Environment as measured (Task 17):**

| Fact | Value |
|---|---|
| Daemon | **running**, version 0.32.13 at `127.0.0.1:11434` |
| `/api/tags` | `{"models":[]}` |
| Blob store | **0 blobs** |
| Model pulled | **none** |

**No model was downloaded** (multi-GB; the phase installs nothing). Therefore
**real local model inference was NOT RUN**, and no generation figure appears
anywhere in this documentation set.

The daemon has been observed to **flap** — running at one probe and refusing
connections minutes later. Any consumer must probe rather than assume.

**DEVELOPMENT-ONLY, and structurally so.** ADR-0006 freezes a four-tier ladder,
every tier on Claude, and explicitly rejected self-hosted open-weight models as
its Option 5. Phase 11E **does not amend ADR-0006**, does not register this
adapter as a tier, and makes no claim against C1. Enforcement is listed in
[PROVIDER_ARCHITECTURE.md](PROVIDER_ARCHITECTURE.md) §7.

**No model name in this repository is an approved production model.** Whatever an
operator pulls is an environment fact.

Contract coverage without a model is real: 33 tests drive a real `httptest`
server — genuine sockets, streaming, cancellation semantics — covering
configuration, probe, streaming order, usage accounting, cancellation, daemon
unavailable, malformed responses, in-band errors, truncation, bounded lines,
status→taxonomy mapping, timeout and concurrency.

## 7. Design correction on the record

The Phase 11E design (`docs/superpowers/specs/2026-08-11-voice-provider-integration-design.md`,
§4) states Ollama is *"present, working (a 12B model is pulled)"*.

**That is no longer true.** Measured 2026-08-15/16: the model store is empty. The
design is left unedited — it records what was true when written — and this
document is the correction.

## 8. Credentials

**None.** No API key, token or credential is required, present or accepted by any
adapter. Every provider is local: subprocess or localhost HTTP. Cloud adapters
are a later phase and will implement the same frozen ports.
