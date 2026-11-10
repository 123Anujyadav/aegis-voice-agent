# Local-model adapters: what is tested, and how to run the real-runtime tests (2026-11-10)

- **As of:** first written at `main` `3e84a40` (#294). **Updated 2026-11-10 by the remediation PR** that fixed the findings in §5. Every test name, gate and message below is taken from the code; the local runs in §4 are dated.
- **Scope:** the four development adapters in `packages/go/voice/providers`: `whispercpp` and `whispercli` (speech to text), `piper` (text to speech) and `ollama` (language model). They are **development tools, not production tiers** (`PRODUCTION-READINESS-PLAN.md`; `ollama`'s own `TestProvider_IsNotAProductionTier`). No service builds an audio pipeline, so nothing here changes a running system.
- **What this is not:**
  - It is not a speech or model evaluation. Speech evaluation is deferred by the owner (OD-17), and nothing here measures accuracy.
  - It activates no vendor and downloads no model. Installing a runtime or a model is the operator's choice (§3).
- **May `packages/go/voice` be edited?** Yes, as ordinary non-frozen code: see §6.

## 1. Three kinds of coverage, kept apart

| Kind | What it proves | Where it runs |
|---|---|---|
| **Test double** | The adapter's own logic (argument building, parsing, bounds, error typing, process handling, cleanup) against a stub file, a stand-in engine or a fake HTTP daemon | Everywhere, including every CI run |
| **Port conformance** | The frozen port's documented contract (`packages/go/providerconformance`, #292) | **Against the ports' fakes only.** No local adapter is run through it |
| **Real local runtime** | The adapter driving the real binary or daemon with a real model | **Only on a machine where the runtime is installed.** Never in CI: no workflow installs Whisper, Piper or Ollama |

A green CI run is evidence of the first two kinds only.

## 2. Per adapter

| Adapter (port) | Test-double coverage (runs in CI) | Real-runtime coverage | How the real tests are gated |
|---|---|---|---|
| `whispercpp` (`speech.STTProvider`) | Stub-file tests, plus **a stand-in engine** compiled at test time that the adapter really starts, feeds and reaps: a transcript, silence, a crash, a hung engine, the child's environment (`standin_test.go`) | **None.** whisper.cpp has never run here | — |
| `whispercli` (`speech.STTProvider`; the openai-whisper CLI, batch only) | Tests with a stub executable that is never started | `TestAdapter_RealToolProducesNoFabricatedTranscript` (a 2 s tone), `TestAdapter_RealToolTranscribesRealSpeech` (Windows SAPI speech, expects "balance" and "account"), `TestAdapter_FailedChildIsReportedNotSilent`. **Each fails if the recogniser fails** (§5, finding 1) | Skipped under `-short`; skipped when `whisper` is not on `PATH`. The speech test also needs Windows (SAPI) and `ffmpeg`. The model is fixed to `tiny` |
| `piper` (`speech.TTSProvider`) | Tests against a **stand-in engine** compiled at test time (needs `go` on `PATH`) | **None.** `TestAdapter_RealRuntimeAvailability` only logs whether `piper` is on `PATH` | — |
| `ollama` (`runtime.Provider`) | Tests against an **`httptest` fake daemon**, plus the endpoint policy (`endpoint_test.go`) | **None that generates.** `TestProvider_RealDaemonAvailability` probes `http://127.0.0.1:11434` and only logs | — |
| End to end (`packages/go/voice`, `TestE2E_LocalIntegration`) | — | Runs whatever is available: real whisper CLI STT, Ollama generation, Piper TTS. Prints an EXECUTED / NOT RUN / BLOCKED / **FAILED** table, and **fails if any stage FAILED** (a runtime present but broken) | Skipped under `-short`; a missing runtime is NOT RUN, which does not fail the test |

No real-runtime test on `main` reads an environment variable or uses a build tag. Availability is decided by `exec.LookPath`, the Whisper model cache (`~/.cache/whisper/*.pt`) and the Ollama daemon's `/api/tags`.

## 3. Reproducing the real-runtime runs

**Prerequisites:** operator choices, installed by hand. None is needed for CI, and nothing in this repository installs them.

| Runtime | Install | Check |
|---|---|---|
| openai-whisper CLI | `pip install openai-whisper`. The `tiny` model is fetched by the tool on first use (about 75 MB) unless `~/.cache/whisper/tiny.pt` already exists. **Its dependency numba needs NumPy 2.4 or less.** If the global Python has a newer NumPy, use the isolated environment below rather than changing it | `whisper` imports without error |
| ffmpeg (for the speech fixture) | Any build on `PATH` | `ffmpeg -version` |
| Windows SAPI (for the speech fixture) | Built into Windows; at least one installed voice | The test skips if it fails |
| Piper | A release binary from `https://github.com/rhasspy/piper/releases` and a voice (`.onnx` and `.onnx.json`) from `https://huggingface.co/rhasspy/piper-voices` | `piper --help` |
| Ollama | `ollama serve`, then `ollama pull <model>` (the model is the operator's choice; several GB). **Loopback only:** the adapter refuses any other host ([`OLLAMA-ENDPOINT-POLICY.md`](OLLAMA-ENDPOINT-POLICY.md)) | `curl http://127.0.0.1:11434/api/tags` lists the model |

**An isolated environment for openai-whisper** (no change to the global Python; free packages from PyPI; no credential; no model download when `tiny` is cached). Paths are examples; keep it outside the repository.

```sh
python -m venv --system-site-packages <dir>/whisper-venv
<dir>/whisper-venv/Scripts/python -m pip install "numpy>=2.0,<2.5"
<dir>/whisper-venv/Scripts/python -m pip install --no-deps --ignore-installed openai-whisper==20250625
export PATH="<dir>/whisper-venv/Scripts:$PATH"     # Linux and macOS: bin instead of Scripts
```

- **What is isolated:** NumPy and the `whisper` entry point live in the virtual environment and take precedence there.
- **What is shared, read-only:** everything else (torch, numba, tiktoken) is read from the global site-packages through `--system-site-packages`. Nothing is written to them.
- **Removing it** is deleting `<dir>/whisper-venv`.

**Commands,** from the repository root, in a clean worktree. **Never use `-short`**, which skips every real-runtime test.

```sh
cd packages/go/voice

# 1. The adapters' own tests (test doubles; what CI runs)
go test -count=1 ./...

# 2. whispercli against the real CLI
go test -count=1 -v -run 'TestAdapter_Real|TestAdapter_FailedChild' ./providers/whispercli/

# 3. The local end-to-end report (whisper, Ollama, Piper: whatever is installed)
go test -count=1 -v -run 'TestE2E_LocalIntegration' .

# 4. Availability logs for Piper and Ollama
go test -count=1 -v -run 'RealRuntimeAvailability|RealDaemonAvailability' ./providers/piper/ ./providers/ollama/
```

**Reading the results:** a failure in step 2 or 3 that quotes the recogniser's stderr means the runtime is installed but broken; it is a real failure, not a skipped test.

## 4. Local runs (this development machine, Windows 11)

**Run 1: 2026-11-10, `main` `3e84a40`, before the remediation.**

| Runtime | State |
|---|---|
| whisper CLI | On `PATH` (openai-whisper 20250625, Python 3.12); models `tiny` and `base` cached. **It does not run:** `ImportError: Numba needs NumPy 2.4 or less. Got NumPy 2.5.` (numba 0.66.0, NumPy 2.5.3) |
| ffmpeg, SAPI | Available (three SAPI voices). The fixture's speech is real: 4.12 s, mean −20.7 dB |
| Piper | Not on `PATH` |
| Ollama | The daemon answers; **no model pulled** |

| Test | Result | What it meant |
|---|---|---|
| `TestAdapter_RealToolProducesNoFabricatedTranscript` | PASS | **Vacuous:** Whisper crashed |
| `TestAdapter_RealToolTranscribesRealSpeech` | FAIL: "real speech produced no transcript at all" | The crash, reported as an empty transcript |
| `TestAdapter_FailedChildIsReportedNotSilent` | PASS | `Err()` carried the failure |
| `TestE2E_LocalIntegration` | PASS (report) | STT "EXECUTED" with **0 characters** |

**Run 2: 2026-11-10, with the remediation, the same broken global Whisper.**

| Test | Result |
|---|---|
| `TestAdapter_RealToolProducesNoFabricatedTranscript` | **FAIL:** "the recogniser failed, so its silence proves nothing: ... ImportError: Numba needs NumPy 2.4 or less" |
| `TestAdapter_RealToolTranscribesRealSpeech` | **FAIL:** "the recogniser failed: ... ImportError: Numba needs NumPy 2.4 or less" |
| `TestE2E_LocalIntegration` | **FAIL:** stage 4 "FAILED"; "1 stage(s) FAILED: a runtime on this machine is present but does not work" |

**Run 3: 2026-11-10, with the remediation, Whisper from the isolated environment in §3** (venv NumPy 2.4.6; global NumPy left at 2.5.3).

| Test | Result |
|---|---|
| `TestAdapter_RealToolProducesNoFabricatedTranscript` | **PASS:** a real run on 2 s of tone produced no transcript (5.3 s) |
| `TestAdapter_RealToolTranscribesRealSpeech` | **PASS:** spoke and transcribed "Hello, I would like to check my account balance please." (`tiny`, 4.1 s of speech, 5.1 s total) |
| `TestAdapter_FailedChildIsReportedNotSilent` | PASS |
| `TestE2E_LocalIntegration` | **PASS:** 6 EXECUTED (stages 1–6, including real whisper STT with 55 characters), 2 NOT RUN (Ollama: no model; Piper: not installed), 2 BLOCKED, 0 FAILED |

**Actual real-runtime coverage today:** openai-whisper only. **whisper.cpp, Piper and Ollama generation have never run against a real runtime on this machine**; their coverage is test doubles only.

## 5. Findings and their status

| # | Finding | Status |
|---|---|---|
| 1 | whispercli's tone and speech tests ignored `Err()`, so a crashed recogniser passed as silence or failed as "no transcript" | **Fixed:** both check `Err()` after draining |
| 2 | `TestE2E_LocalIntegration` recorded a failed STT stage as "EXECUTED" (and a caught error as "NOT RUN") | **Fixed:** `runRealSTT` checks `Close()` when Results closes; a new FAILED status fails the test |
| 3 | `PROVIDER_COMPATIBILITY.md` said whispercpp's contract was "proven against a stand-in"; its tests never started a process | **Fixed:** a stand-in engine now exercises the process path; a dated note is added to that document (its text is kept, rule 11) |
| 4 | `ollama` accepted any `http(s)` host | **Fixed:** loopback only, at construction, at dial time and against redirects ([`OLLAMA-ENDPOINT-POLICY.md`](OLLAMA-ENDPOINT-POLICY.md)) |
| 5 | `whispercpp` passed its child the full parent environment | **Fixed:** `process.DefaultInheritEnv()`, as piper and whispercli; a stand-in test proves a parent secret does not reach the child |
| 6 | Paths were checked for existence, not that they are absolute | **Fixed:** whispercpp, whispercli and piper refuse relative executable, model and work-directory paths |
| 7 | **Found during the fix:** whispercpp itself turned an engine crash into silence. `process.Stop` returns nil for a child that has already exited, and whispercpp had no `Err()` | **Fixed:** whispercpp records a non-zero exit before Results closes (`Err()`, and `Close` returns it), as whispercli does; a stream the caller closed is not a failure |

Every fix has a test that fails when the fix is reverted (checked by mutation on 2026-11-10).

## 6. Why `packages/go/voice` was treated as do-not-edit, and why it is not frozen

- **`ARCHITECTURE_FREEZE.md` §1** freezes the *decisions* of ADR-0001 to ADR-0012, and lists "internal design of a service" and "test strategy" as **not frozen**. It names no module.
- **The frozen-module list** has 13 modules (`docs/phase13/DEPENDENCY_MAP.md` §8; `docs/phase14/DEPENDENCY_MAP.md` §13). **`voice` is not among them,** and Phase 14 says so: "`packages/go/voice` (Phase 11E, not in the Phase 14 frozen 13)".
- **Where "frozen `voice`" comes from:** Phase 12 and 13 scoping. ADR-0016's decision drivers say "Do not modify frozen modules. 10A–10F, 10.5, 11A–11E are closed", and `voice` is a Phase 11E module. That is why `docs/phase13/LIMITATIONS.md` §12 and `packages/go/voiceintel/go.mod` call it frozen. These were constraints on that phase's work, not a standing decision.
- **Precedent on `main`:** #13 fixed barge-in races in `voice`, and #66 cleared its lint baseline, describing it as "`packages/go/voice` (not frozen)".
- **Reading:** a phase-era implementation convention, not a formal architecture or security boundary. The historical records keep their text (rule 11). **The owner holds final authority (rule 6)** and may classify it otherwise.
- **The owner's local work** (the primary worktree, uncommitted) modifies 20 files in this package, including `e2e_test.go` and `whispercli_test.go`, which the remediation also edits. Checked on 2026-11-10 by 3-way applying the owner's diff (read-only, in scratch worktrees):
  - applied to `main`, it conflicts in six files (`pipeline.go`, `fsm.go`, `defaults.go`, `pipeline_test.go`, `failure_test.go`, `security_test.go`): divergence that already exists;
  - applied to the remediation, it conflicts in **the same six files and no others**, and every file the remediation touches merges cleanly;
  - with it applied, the providers build and their tests pass. The owner's opt-in gate (`AEGIS_REAL_PROVIDERS`) and the new `Err()` checks coexist.

## 7. Keeping it current

A change to these adapters' tests, gates or messages updates this file in the same PR. A new local run is added to §4 as a dated run, without replacing earlier runs.
