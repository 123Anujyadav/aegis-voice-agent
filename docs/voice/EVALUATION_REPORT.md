# Evaluation Report

**Date:** 2026-08-16 · Every row is a command that was run. Anything not run says
so.

---

## 1. Environment

| | |
|---|---|
| CPU | 11th Gen Intel Core i7-11800H, 8C/16T |
| OS | Windows 11 (26200) |
| Go | 1.26.5 windows/amd64, `GOMAXPROCS=16` |
| Machine | developer laptop — **not** an isolated test host |

## 2. Gate table

| # | Gate | Result | Evidence |
|---|---|---|---|
| 1 | `gofmt -l .` (6 pkgs) | **PASS** | no output |
| 2 | `go vet ./...` | **PASS** | exit 0 |
| 3 | `go test ./...` | **PASS** | 6/6 packages ok |
| 4 | `go test -count=10 -shuffle=on ./...` | **PASS** | 5 consecutive clean runs — §3 |
| 5 | `GOWORK=off go build ./...` | **PASS** | exit 0; `GOWORK=off go vet` also exit 0 |
| 6 | Per-package `go list -deps` | **PASS** | zero third-party; no cycle |
| 7 | Benchmarks execute | **PASS** | 24 voice + 4 process |
| 8 | Local E2E | **PARTIAL** | 6 stages EXECUTED, 2 NOT RUN, 2 BLOCKED — §5 |
| 9 | Security tests | **PASS** | 24 tests/subtests |
| 10 | Failure injection | **PASS** | 20 tests, 17 named cases |
| 11 | Concurrency/isolation | **PASS** | 13 tests/subtests |
| 12 | Provider adapters | **PASS** | 12 / 16 / 35 / 33 / 22 tests |
| 13 | **`go test -race ./...`** | **NOT RUN** | `cgo: C compiler "gcc" not found` — §6 |
| 14 | Frozen phases untouched | **PASS** | §7 |
| 15 | ADR-0006 boundary | **PASS** | 5 tests |

**Totals: 285 tests, 28 benchmarks** across six packages.

## 3. Gate 4 — the decisive gate

`go test -count=10 -shuffle=on ./...`, full output captured to disk
(`/tmp/gate4/final/`):

| Run | Exit | Test failures | voice package |
|---|---:|---:|---|
| 1 | 0 | 0 | 247.0 s |
| 2 | 0 | 0 | 238.7 s |
| 3 | 0 | 0 | 251.4 s |
| 4 | 0 | 0 | 234.3 s |
| 5 | 0 | 0 | 233.6 s |

Zero failures, zero panics, zero unresolved runs. No shuffle seed to report.

**This gate found three production defects** before it passed. It is the highest
value gate in the phase, and its history is in
[ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md).

## 4. Static and security tooling

| Tool | Status | Notes |
|---|---|---|
| `golangci-lint` | **RAN** (v1.64.8) | found in `GOPATH/bin`, not on PATH; used the repo's `.golangci.yml` |
| `gosec` | **RAN via golangci-lint** | 11 findings, all assessed — none a genuine defect |
| `staticcheck` | **RAN via golangci-lint** | bundled |
| `gosec` standalone | **NOT RUN** | not installed |
| `staticcheck` standalone | **NOT RUN** | not installed |
| `govulncheck` | **NOT RUN** | not installed |

**Correction on the record:** [SECURITY_AUDIT.md](SECURITY_AUDIT.md) originally
recorded gosec and staticcheck as NOT RUN. That was **incomplete** —
`golangci-lint` bundles both and is present. The correction is noted rather than
silently applied.

**172 lint findings** across six packages, largely style (godot 32, errcheck 36,
errorlint 28). Essential context: frozen, already-approved packages also have
findings — **speech 47, runtime 41, governance 41, audiointel 114**. Lint-clean is
not a bar this repository currently meets, so 172 across *six* packages is
consistent with the codebase norm rather than a Phase 11E deficit.

## 5. Local end-to-end (Task 17)

| # | Stage | Status | Detail |
|---|---|---|---|
| 1 | fixture audio | **EXECUTED** | SAPI + ffmpeg → 4.12 s real speech, 131,804 B |
| 2 | media handoff | **EXECUTED** | 205 real `media.Frame` values |
| 3 | audio intelligence | **EXECUTED** | real `audiointel.Session`: 205 frames, 2 onsets, 2 endpoints |
| 4 | STT | **EXECUTED** | real openai-whisper subprocess via registry→router; 55 chars; **3.36 s inference** |
| 5 | conversation | **EXECUTED** | real `conversation.Engine` → `Plan{ignore, floor_queued}` |
| 6 | governance | **EXECUTED** | real `Decide` → `deny (no_policy_matched)`; **invoked=0** |
| 7 | runtime/generation | **NOT RUN** | Ollama has zero models |
| 8 | TTS (Piper) | **NOT RUN** | not on PATH |
| 9 | media output | **BLOCKED** | no synthesised audio upstream |
| 10 | barge-in (real audio) | **BLOCKED** | needs stage 8 |

**6 EXECUTED · 2 NOT RUN · 2 BLOCKED.**

> **The complete local voice E2E loop was NOT fully executable on this machine.**
> Stages 1–6 ran against real software. Nothing was fabricated for 7–10.

Clean-failure behaviour was verified on the **genuine** absence of Piper: typed
`ErrProviderUnavailable`, zero audio, session not terminal, declared transitions
only, bounded queues, clean shutdown.

## 6. Race detector — OPEN RISK

```
$ go test -race ./...
go: -race requires cgo; enable cgo by setting CGO_ENABLED=1

$ CGO_ENABLED=1 go test -race ./...
# runtime/cgo
cgo: C compiler "gcc" not found: exec: "gcc": executable file not found in %PATH%
```

Probed and absent: `gcc`, `clang`, `cc`, `x86_64-w64-mingw32-gcc`, `tdm-gcc`.

**Concurrency evidence that DOES exist:** deterministic shuffled testing at
`-count=10` (five clean full runs); 12-session shared-state isolation; adversarial
mixed-failure fleets; goroutine-leak accounting; mutation-verified race fixes;
three concurrency defects found and fixed by these gates.

**What does not exist:** Go's race detector has never run against this code.
Behavioural evidence is not a substitute for it. This is an **OPEN RISK** and a
production-readiness blocker — see [PLATFORM_READINESS.md](PLATFORM_READINESS.md).

## 7. Frozen phases — untouched

No `.go` file under `speech`, `runtime`, `governance`, `conversation`, `memory`,
`toolruntime`, `audiointel`, `audiobridge`, `media`, `metrics` has been modified
since 2026-08-12. Newest mtimes are 2026-08-10/11, predating all Phase 11E
implementation work. Every change is confined to `packages/go/voice/**`.

## 8. Credentials

**No API key, credential, cloud service, model download or internet access was
required or used** for any gate in this table. No secret is present in the
repository. `go.mod` has zero third-party requires.
