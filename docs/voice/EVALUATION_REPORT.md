# Evaluation Report

**Date:** 2026-08-16 · Every row is a command that was run. Anything not run says
so.

> **Sections 1–8 are the Phase 11E record, measured on one Windows laptop, and
> are preserved unedited except where a line is explicitly marked SUPERSEDED.
> [Section 9](#9-phase-12-ci-execution-the-race-detector-has-now-run) adds the
> Phase 12 CI results and is authoritative where the two differ** — most
> importantly Gate 13 (`-race`), which §§1–8 record as NOT RUN and which has
> since executed in CI with zero findings.

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
| 13 | **`go test -race ./...`** | **SUPERSEDED — now PASS in CI** | Was **NOT RUN** locally (`cgo: C compiler "gcc" not found`). §6 records that original finding unchanged; §9 records the CI execution that supersedes it |
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
| `govulncheck` | **SUPERSEDED — RAN in CI** | Not installed locally. Executed in CI on 2026-08-16 — see §9.3 |

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

## 6. Race detector — the local finding (SUPERSEDED by §9)

> **Superseded on 2026-08-16 by CI execution — see [§9](#9-phase-12-ci-execution-the-race-detector-has-now-run).**
> The section below is the original Phase 11E finding and is preserved exactly
> as written. It remains true of *this machine*: the local toolchain still has
> no C compiler. What changed is that CI has a C compiler and has now run the
> detector. Nothing here was deleted or edited.

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

**What did not exist at the time of writing:** Go's race detector had never run
against this code. Behavioural evidence is not a substitute for it. This was an
**OPEN RISK** and a production-readiness blocker — see
[PLATFORM_READINESS.md](PLATFORM_READINESS.md).

> **Correction (2026-08-16):** that last sentence is no longer accurate. The
> detector has now run in CI against 13 of 14 modules in two modes with zero
> findings. §9 records exactly what was and was not covered. The risk is closed
> on the evidence stated there and not one step further.

## 7. Frozen phases — untouched

No `.go` file under `speech`, `runtime`, `governance`, `conversation`, `memory`,
`toolruntime`, `audiointel`, `audiobridge`, `media`, `metrics` has been modified
since 2026-08-12. Newest mtimes are 2026-08-10/11, predating all Phase 11E
implementation work. Every change is confined to `packages/go/voice/**`.

## 8. Credentials

**No API key, credential, cloud service, model download or internet access was
required or used** for any gate in this table. No secret is present in the
repository. `go.mod` has zero third-party requires.

---

## 9. Phase 12 CI execution: the race detector has now run

**Added 2026-08-16 (Phase 12, Tasks 1–3).** Everything in §§1–8 was measured on
one Windows laptop. This section is measured on GitHub-hosted `ubuntu-24.04`
runners, and every claim below names the run that produced it.

Repository: `123Anujyadav/aegis-voice-agent`. Go on the runners: **go1.25.12**
(`GO_VERSION: 1.25.x`, `GOTOOLCHAIN: local`).

| Commit | What it is | Runs |
|---|---|---|
| `acaceae` | Phase 11E tree, unmodified | [ci 31935771472](https://github.com/123Anujyadav/aegis-voice-agent/actions/runs/31935771472) · [hardening 31935771464](https://github.com/123Anujyadav/aegis-voice-agent/actions/runs/31935771464) · [pr-go 31935771499](https://github.com/123Anujyadav/aegis-voice-agent/actions/runs/31935771499) |
| `25ca0c2` | T3: CI fixes + Phase 11 added to `AI_MODULES` | [ci 31938810461](https://github.com/123Anujyadav/aegis-voice-agent/actions/runs/31938810461) · [hardening 31938810468](https://github.com/123Anujyadav/aegis-voice-agent/actions/runs/31938810468) · [pr-go 31938810527](https://github.com/123Anujyadav/aegis-voice-agent/actions/runs/31938810527) |
| `5a23de2` | T3 follow-up: workspace gate + coverage loop | [ci 31939354663](https://github.com/123Anujyadav/aegis-voice-agent/actions/runs/31939354663) · [hardening 31939354697](https://github.com/123Anujyadav/aegis-voice-agent/actions/runs/31939354697) |

### 9.1 Race detector — EXECUTED, zero findings

Source: hardening run [31939354697](https://github.com/123Anujyadav/aegis-voice-agent/actions/runs/31939354697),
jobs `95146174219` (single) and `95146174222` (repeated). Precondition step
`Confirm cgo is available` reported `cc (Ubuntu 13.3.0-6ubuntu2~24.04.1) 13.3.0`.

Phase 11 modules — added to `AI_MODULES` by Phase 12 T3, and covered by the
**nightly** repeated-shuffled job for the first time:

| Module | `-race -count=1` | `-race -count=5 -shuffle=on` |
|---|---|---|
| `telephony` | ok 2.603s | **ok 9.534s** |
| `media` | ok 1.596s | **ok 5.824s** |
| `audiointel` | ok 1.526s | **ok 4.884s** |
| `audiobridge` | ok 1.012s | **ok 1.040s** |
| `speech` | ok 1.038s | **ok 1.147s** |
| `voice` | ok 19.310s | **ok 91.569s** |
| `voice/providers/ollama` | ok 11.016s | **ok 51.112s** |
| `voice/providers/piper` | ok 2.621s | **ok 8.206s** |
| `voice/providers/process` | ok 2.384s | **ok 7.707s** |
| `voice/providers/whispercli` | ok 1.018s | **ok 1.085s** |
| `voice/providers/whispercpp` | ok 1.009s | **ok 1.032s** |

Repeated-mode shuffle seed: `1786872847521114476`.

**Zero data races**, counting only detector output. The literal string
`WARNING: DATA RACE` appears in both job logs solely as echoed workflow script
(GitHub prefixes echoed script with the `[36;1m` colour code); excluding those,
**0 hits in both jobs**. Independently: the workflow tests for races *before*
the module check, so a real race would have emitted
`::error::the race detector reported a data race`. Both jobs emitted
`::error::failing modules under -race: packages/go/evalsubjects` instead.

`pr-go` run [31938810527](https://github.com/123Anujyadav/aegis-voice-agent/actions/runs/31938810527)
independently ran `go test -race -count=1` over all **40** workspace modules
(auto-discovered from `go.work`): **39 pass, 1 fail** — `evalsubjects`, §9.4.

### 9.2 What this does and does not establish

**Established:** the race detector executed against every Phase 11E package in
two modes on Linux/amd64 and reported nothing.

**NOT established — this is not a claim of race-freedom:**

- The detector only observes interleavings that **actually occur**. Clean runs
  mean no race was observed on the paths taken, not that none exists.
- One platform only: `ubuntu-24.04`, amd64, one Go version. No Windows, no arm64.
- `-count=5 -shuffle=on` is five executions, not exhaustive scheduling.
- Coverage is 60.8–100% by statement, so unexecuted statements were not examined
  by the detector either.
- `evalsubjects` remains **unverified under `-race`**: its tests fail for the
  unrelated reason in §9.4, so the detector's verdict on that module is unknown.

**No claim of "race-safe" or "concurrency fully verified" is made anywhere in
this document.** R1 is closed as *"the detector has run and found nothing"*,
which is exactly what the evidence supports.

### 9.3 govulncheck — EXECUTED

Source: `pr-go` run [31935771499](https://github.com/123Anujyadav/aegis-voice-agent/actions/runs/31935771499),
job `95137348387` (`packages/go/voice`), `govulncheck@v1.1.4`.

| ID | Issue | Found in | Fixed in |
|---|---|---|---|
| GO-2026-6218 | quadratic `resolvePath` | `net/url@go1.25.12` | go1.25.13 |
| GO-2026-6090 | post-handshake message limit | `crypto/tls@go1.25.12` | go1.25.13 |
| GO-2026-5972 | ASN.1 recursion depth | `encoding/asn1@go1.25.12` | go1.25.13 |
| GO-2026-5026 | Punycode label rejection | `net/http@go1.25.12` | go1.25.13 |

**All four are Go standard library**, every one remediated by go1.25.12 →
go1.25.13. **Zero third-party vulnerabilities**, consistent with §8: `go.mod`
still has zero third-party requires.

### 9.4 The release gate is RED — F2, a frozen Phase 10F defect

`packages/go/evalsubjects` fails `TestVerification_EndToEnd` and
`TestVerification_ConcurrentEvaluation`, and is the **sole** cause of every red
hardening job and of the red release gate.

It is **not** a race and **not** a behavioural regression. The behaviour digest
is identical on both sides of every reported drift
(`8bf7fc0af6b4d34a → 8bf7fc0af6b4d34a`); only wall-clock latency differs, by
×2.19 to ×6.84 against the `LatencyRatio: 2.0` in
`packages/go/evaluation/compare.go:120` (`DefaultTolerances`).

Phase 12 T2 established the mechanism: `verification_test.go` `bootstrap`
executes each scenario **in-process and approves that observation as the
baseline**, then `RunAll` executes again and compares. Both measurements happen
in the same process, seconds apart, on the same host — so this is **differential
run-to-run jitter**, not dev-machine-versus-CI baseline skew. Uniform load
cancels in a ratio; only differential variance trips it. It did not reproduce in
**72 local executions** across seven condition sets.

It is **intermittent**: platform verification *passed* on `25ca0c2` and *failed*
on `5a23de2` — same code, same workflow, different runner.

**Both modules are frozen Phase 10F and were not modified.** Closing this
requires a decision about frozen code and is not taken here. **F2 is UNRESOLVED.**

### 9.5 Other gates now executing

| Gate | Status | Evidence |
|---|---|---|
| `ci.yml` build, all 40 modules | **PASS** — first execution ever | run 31939354663 |
| `ci.yml` test, all 40 modules | FAIL — `evalsubjects` only | run 31939354663 |
| Coverage floor (60%) | **PASS** | job 95146174218; previously *skipped* |
| Benchmarks | **PASS** | run 31939354697 |
| `golangci-lint` | **RAN** — real findings | see below |
| `gofumpt` | FAIL — 129 files | run 31938810527 |

Coverage, all 13 measurable modules (job `95146174218`): `metrics` 84.9 ·
`runtime` 72.8 · `conversation` 76.0 · `memory` 80.7 · `toolruntime` 76.2 ·
`governance` 77.1 · `evaluation` 75.9 · **`telephony` 76.6** · **`media` 75.8** ·
**`audiointel` 89.1** · **`audiobridge` 100.0** · **`speech` 77.4** ·
**`voice` 83.3**. All above the floor.

**Correction to §4.** §4 records `golangci-lint` as having RUN locally at
v1.64.8. That is accurate, but it was **never running in CI**: the workflow used
a prebuilt v1.63.4 binary compiled with go1.23, which refuses a config targeting
`go 1.25.0`, so 39 of 40 lint jobs died before analysing a file and produced
**zero** findings. Phase 12 T3 fixed the tooling only — **no lint rule was
changed**. CI now reproduces §4's local figures exactly: **voice 172**,
**speech 47**. The lint jobs are red for legitimate pre-existing findings.

**F5 — `gofumpt`, 129 files, UNRESOLVED.** 121 are in frozen or non-voice
modules; 8 are under `packages/go/voice`. §1's `gofmt` PASS remains accurate and
is not contradicted: `gofmt -l packages/go/voice` is still **0 files**.
`gofumpt` is a stricter superset that no prior phase ran. Reformatting frozen
modules is a policy decision and was not taken.

**`go.work.sum`.** It is gitignored (`.gitignore:69`) and therefore never
reaches CI. A genuine staleness gate is **not implementable** until that changes,
which is a repository-policy decision recorded here and deliberately not taken.
`ci.yml` currently verifies what is verifiable: that the workspace build list
resolves across all 40 modules.
