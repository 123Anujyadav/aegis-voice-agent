# Local-model adapters: what is tested, and how to run the real-runtime tests (2026-11-10)

- **As of:** `main` `3e84a40`. Every test name, gate and message below is taken from the code on `main`; the local results in §4 were run on 2026-11-10 from a clean worktree of that commit.
- **Scope:** the four development adapters in `packages/go/voice/providers`: `whispercpp` and `whispercli` (speech to text), `piper` (text to speech) and `ollama` (language model). They are **development tools, not production tiers** (`PRODUCTION-READINESS-PLAN.md`; `ollama`'s own `TestProvider_IsNotAProductionTier`).
- **What this is not:**
  - It is not a speech or model evaluation. Speech evaluation is deferred by the owner (OD-17), and nothing here measures accuracy.
  - It activates no vendor and downloads nothing. Installing a runtime or a model is the operator's choice (§3).
- **`packages/go/voice` is treated as do-not-edit** ([matrix](INTEGRATION-DEPENDENCY-MATRIX.md) §3). The findings in §5 are recorded, not fixed.

## 1. Three kinds of coverage, kept apart

| Kind | What it proves | Where it runs |
|---|---|---|
| **Test double** | The adapter's own logic (argument building, parsing, bounds, error typing, cleanup) against a stub file, a stand-in engine or a fake HTTP daemon | Everywhere, including every CI run |
| **Port conformance** | The frozen port's documented contract (`packages/go/providerconformance`, #292) | **Against the ports' fakes only.** No local adapter is run through it |
| **Real local runtime** | The adapter driving the real binary or daemon with a real model | **Only on a machine where the runtime is installed.** Never in CI: no workflow installs Whisper, Piper or Ollama |

A green CI run is evidence of the first two kinds only.

## 2. Per adapter

| Adapter (port) | Test-double coverage (runs in CI) | Real-runtime coverage | How the real tests are gated |
|---|---|---|---|
| `whispercpp` (`speech.STTProvider`) | 12 tests with **stub files** (the bytes "stub" for binary and model); **no process is ever started** | **None.** No test runs whisper.cpp | — |
| `whispercli` (`speech.STTProvider`; the openai-whisper CLI, batch only) | 13 tests with a stub executable that is never started | `TestAdapter_RealToolProducesNoFabricatedTranscript` (a 2 s tone), `TestAdapter_RealToolTranscribesRealSpeech` (Windows SAPI speech, expects "balance" and "account"), `TestAdapter_FailedChildIsReportedNotSilent` | Skipped under `-short`; skipped when `whisper` is not on `PATH` ("LOCAL PROVIDER RUNTIME NOT AVAILABLE ... pip install openai-whisper"). The speech test also needs Windows (SAPI) and `ffmpeg` on `PATH`. The model is fixed to `tiny` |
| `piper` (`speech.TTSProvider`) | 34 tests against a **stand-in engine** compiled from Go source at test time (needs `go` on `PATH`) | **None.** `TestAdapter_RealRuntimeAvailability` only logs whether `piper` is on `PATH`; it never runs it | — |
| `ollama` (`runtime.Provider`) | 32 tests against an **`httptest` fake daemon** | **None that generates.** `TestProvider_RealDaemonAvailability` probes `http://127.0.0.1:11434` and only logs | — |
| End to end (`packages/go/voice`, `TestE2E_LocalIntegration`) | — | Runs whatever is available: real whisper CLI STT, Ollama generation (the daemon's first listed model), Piper TTS; prints an EXECUTED / NOT RUN / BLOCKED table | Skipped under `-short`; each stage reports NOT RUN when its runtime is missing. The test passes either way: it is a report, not an assertion |

No real-runtime test reads an environment variable, and none uses a build tag. Availability is decided by `exec.LookPath`, the Whisper model cache (`~/.cache/whisper/*.pt`) and the Ollama daemon's `/api/tags`.

## 3. Reproducing the real-runtime runs

**Prerequisites:** operator choices, installed by hand. None is needed for CI, and none is installed by any script in this repository.

| Runtime | Install | Check |
|---|---|---|
| openai-whisper CLI | `pip install openai-whisper`. The `tiny` model is fetched by the tool on first use (about 75 MB) unless `~/.cache/whisper/tiny.pt` already exists. **Its dependency numba needs NumPy 2.4 or less** (see §4) | `whisper --help` exits 0 |
| ffmpeg (for the speech fixture) | Any build on `PATH` | `ffmpeg -version` |
| Windows SAPI (for the speech fixture) | Built into Windows; at least one installed voice | The test skips if it fails |
| Piper | A release binary from `https://github.com/rhasspy/piper/releases` and a voice (`.onnx` and `.onnx.json`) from `https://huggingface.co/rhasspy/piper-voices` | `piper --help` |
| Ollama | `ollama serve`, then `ollama pull <model>` (the model is the operator's choice; several GB) | `curl http://127.0.0.1:11434/api/tags` lists the model |

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

**Reading the results:**
- **A PASS on step 2 is not enough on its own.** Confirm the speech test passed (it asserts on content words) **and** that Whisper runs at all: `whisper --help`. See §5, finding 1.
- **In step 3, an "EXECUTED" STT stage with 0 transcript characters means the tool failed,** not that it heard silence (§5, finding 2).

## 4. Local results on 2026-11-10 (this development machine, Windows 11, `main` `3e84a40`)

| Runtime | State |
|---|---|
| whisper CLI | On `PATH` (openai-whisper 20250625, Python 3.12); models `tiny` and `base` cached. **It does not run:** `ImportError: Numba needs NumPy 2.4 or less. Got NumPy 2.5.` (numba 0.66.0, NumPy 2.5.3) |
| ffmpeg, SAPI | Available (three SAPI voices). The fixture's speech is real: 4.12 s, mean −20.7 dB |
| Piper | Not on `PATH` |
| Ollama | The daemon answers; **no model pulled** |

| Test | Result | What it means |
|---|---|---|
| `TestAdapter_RealToolProducesNoFabricatedTranscript` | PASS | **Vacuous today:** Whisper crashed, so no transcript was produced for a different reason than the one the test is about |
| `TestAdapter_RealToolTranscribesRealSpeech` | **FAIL**: "real speech produced no transcript at all" | The crash above, reported as an empty transcript |
| `TestAdapter_FailedChildIsReportedNotSilent` | PASS | The adapter's `Err()` carried the failure (here the import error rather than the intended argparse exit 2) |
| `TestE2E_LocalIntegration` | PASS (report) | STT "EXECUTED" with **0 transcript characters**; generation and TTS NOT RUN; "the complete local E2E path was NOT executable on this machine" |
| Piper and Ollama availability | PASS (logs) | "NOT AVAILABLE"; "PARTIALLY AVAILABLE" (daemon without a model) |

**The earlier real-Whisper run recorded in `docs/voice/PROVIDER_COMPATIBILITY.md` (`tiny`, 3.36 s) cannot be reproduced on this machine today.** The cause is the local Python environment, not the adapter. The remedy is the operator's: install a NumPy that numba accepts (for example `pip install "numpy<2.5"`) or use a separate virtual environment for openai-whisper. It has not been applied here, because it changes the owner's Python installation.

## 5. Findings (recorded, not fixed: `packages/go/voice` is do-not-edit)

1. **A crashed recogniser can look like silence in two real-runtime tests.** The adapter does report the failure: `stream.Err()` is set and `Close` returns it (`whispercli.go`, `Err` and `Close`). But `TestAdapter_RealToolProducesNoFabricatedTranscript` and `TestAdapter_RealToolTranscribesRealSpeech` discard `Close`'s error and never check `Err()`. The first therefore passes when Whisper cannot start; the second fails with a misleading message. **A fix would assert `Err() == nil` after draining, in both tests.**
2. **`TestE2E_LocalIntegration` reports an STT stage that failed as "EXECUTED"** with 0 characters. It should report the stream's error.
3. **`docs/voice/PROVIDER_COMPATIBILITY.md` says the `whispercpp` contract is "proven against a stand-in".** Its tests use stub files and never start a process, so its streaming and parsing paths are covered only by direct calls to `parse`, and process handling not at all.
4. **`ollama` accepts any `http` or `https` host.** Nothing restricts it to loopback, although its only intended use is a local daemon (`DefaultBaseURL` is `http://127.0.0.1:11434`). A configuration mistake could send prompts, and so transcripts, to a remote host. That matters before any real call audio reaches it (OD-4, DPDP L1).
5. **`whispercpp` passes its child the full parent environment** (no `InheritEnv`), unlike `whispercli` and `piper`, which use `process.DefaultInheritEnv()`. Any secret in the developer's environment reaches the child process.
6. **The adapters check only that a configured path exists,** not that it is absolute, although their comments ask for absolute paths.

Each fix is small, but it edits `packages/go/voice`, whose freeze status is recorded inconsistently ([matrix](INTEGRATION-DEPENDENCY-MATRIX.md) §3). **They wait for the owner to confirm whether `voice` may be edited.** None of them affects a running service: no service builds an audio pipeline today.

## 6. Keeping it current

A change to these adapters' tests, gates or messages updates this file in the same PR. A new local run appends a dated row to §4 rather than replacing the earlier one.
