# Phase 14 — Final Report

Status labels follow [DEPENDENCY_MAP.md](DEPENDENCY_MAP.md): IMPLEMENTED ·
VERIFIED · MEASURED · CI-VERIFIED · NOT RUN · BLOCKED.

**Phase status: AWAITING MERGE OF PR #14.**
- T1–T12 are complete and verified on `main`.
- T13 was approved as option B+A. It is IMPLEMENTED and CI-VERIFIED on PR #14
  (head `c5db6f60b1e15e803d774595a639a5dc75edaf73`), but **not yet on `main`**.
- Phase 14 closes when PR #14 is merged and hardening passes on `main` with
  `services/go/voice` included (§12).

## 1. Executive summary

Phase 14 makes the Phase 13 intelligence path **reachable from a real service
entrypoint**. Before it, `conversation.NewEngine` appeared in production in
exactly one place, voiceintel's own bridge, and no service reached that bridge.

`services/go/voice` now registers a `platform.Runner` (`voiceIntelligence`)
that owns a `voiceintel.Bridge`, built with the real deterministic classifier:

```
run() → buildService → platform.Service.Register
      → voiceintel.Bridge → conversation.Engine (WithClassifier)
      → intent.Classifier → conversation.Plan
```

- The wiring shipped in `beb46df` ("feat(phase14): wire voice intelligence into
  service"): 3 production files, no third-party dependency, no new listener,
  no global state. See [ADR 0017](../adr/0017-service-wiring.md).
- T13 brings the service under hardening's gates: repeated shuffled race, the
  unchanged 60% coverage floor, and benchmarks.

**Not claimed:** audio processing (no provider can be constructed; §9),
production or staging readiness (§11).

## 2. Scope

In scope: the deterministic service-level intelligence path, and its
registration, lifecycle, isolation, failure, concurrency, determinism,
performance, acceptance and CI-wiring evidence, plus T13's CI-policy decision.

Out of scope, recorded rather than engineered around (ADR 0017 §8):
constructing a `voice.Pipeline`. Every shipped provider (`ollama`, `piper`,
`process`, `whispercli`, `whispercpp`) shells out to an external binary or
model, and Phase 14's policy was no model download, no external service and no
API key.

## 3. Task status

Each T2–T12 task is recorded in the header of its test file under
`services/go/voice/cmd/server/`.

| Task | Subject | Evidence | Status |
|---|---|---|---|
| T1 | Service dependency / wiring audit | `DEPENDENCY_MAP.md` | COMPLETE |
| T2 | Service wiring | `main.go`, `main_test.go` (7 tests) | COMPLETE |
| T3 | Real service construction and registration | `registration_test.go` (6) | COMPLETE |
| T4 | End-to-end intelligence path | `e2e_test.go` (9) | COMPLETE |
| T5 | Service-level session isolation under load | `isolation_test.go` (5) | COMPLETE |
| T6 | Lifecycle and shutdown integrity | `lifecycle_test.go` (6) | COMPLETE |
| T7 | Failure propagation | `failure_test.go` (8) | COMPLETE |
| T8 | Concurrency with lifecycle events | `concurrency_test.go` (7) | COMPLETE |
| T9 | Service-level determinism (golden) | `determinism_test.go` (11) | COMPLETE; one unreproduced failure observed (§8) |
| T10 | Benchmarks | `bench_test.go` (1 test, 12 benchmarks) | COMPLETE |
| T11 | Acceptance / evaluation fixtures | `acceptance_test.go` (8) | COMPLETE |
| T12 | CI wiring verification | `ci_wiring_test.go` (5) | COMPLETE; tripwires updated by T13 |
| T13 | Hardening `AI_MODULES` decision | §6 and PR #14 | **IMPLEMENTED, CI-VERIFIED on PR #14 — awaiting merge** |

T13's definition, from DEPENDENCY_MAP §15 and ADR 0017 ("Consequences"):
adding a `services/` entry to hardening's `AI_MODULES` "would be a **new** CI
decision. **T13 will assess it and STOP for approval rather than editing CI.**"
The assessment (§6) was presented, and **option B+A was approved.**

## 4. Implemented components

- **`services/go/voice/cmd/server/main.go` (177 lines; T13 did not change it):**
  - `buildService` constructs `platform.Service` and registers
    `voiceIntelligence`;
  - `newVoiceIntelligence` calls `voiceintel.New()`, which builds
    `intent.New(intent.DefaultConfig())` into `conversation.NewEngine` through
    `WithClassifier`;
  - `voiceIntelligence` implements `platform.Runner`. `Run` blocks until the
    service's context ends, because the bridge owns no goroutine.
  - VERIFIED by inspection.
- **No package-level bridge, session registry or `init()`.** Session identity
  stays `conversation.ConversationID`, supplied by the caller.
- **T13 (PR #14), test files and one workflow line only:**
  - `run_test.go`, `run_unix_test.go`, `run_other_test.go`;
  - `services/go/voice` appended to hardening's `AI_MODULES`;
  - the two T12 tripwires now expect the entry.

## 5. Verification results

### 5.1 T1–T12, on `main` at `c294fc6fc813a97e9d221e447109653121aa885a`

All runs on go1.26.9 linux/amd64 (Docker `golang:1.26.9-bookworm`, gcc 12.2),
2026-10-17.

| Command (in the module directory) | Result |
|---|---|
| `services/go/voice`: `go test -race -count=1 -v ./...` | 73/73 PASS, 0 FAIL, 0 SKIP, VERIFIED |
| `services/go/voice`: `go test -race -count=5 -shuffle=on -timeout=30m ./...` | PASS, VERIFIED |
| `packages/go/voiceintel`, `conversation`, `intent`: `go test -race -count=3 -shuffle=on ./...` | PASS, VERIFIED |

The 73 test functions match the count recorded in `beb46df`. **Not
re-verified:** `beb46df`'s record that 28 mutations were run and all caught.

### 5.2 T13, on PR #14 head `c5db6f60b1e15e803d774595a639a5dc75edaf73`

Locally, go1.26.9 linux/amd64:

| Check | Result |
|---|---|
| Coverage: `cd services/go/voice && go test -count=1 -covermode=atomic -coverprofile=c.out ./...` (hardening's command) | **41.7% → 80.6%** (15/36 → 29/36 statements; the floor needs 22), MEASURED |
| Full suite, `-race -count=1 -v` | 76/76 PASS (73 + 3 new) |
| T13 tests, `-race -count=20` | PASS |
| Module, `-race -count=5 -shuffle=on` | PASS in 7 runs, plus 1 unexplained T9 failure (§8) |
| T12 tripwires (updated) | PASS |
| gofmt, gofumpt, `go vet` (also `GOOS=windows`) | clean |
| golangci-lint, `services/go/voice` | 29 findings, the same as `main`; none new |
| Benchmarks; platform verification | 36 run; PASS |
| Secret scan of the diff | clean |

## 6. T13 — hardening `AI_MODULES` decision

### 6.1 The gap (before T13)

- **What `pr-go.yml` already covered:** `services/go/voice` was discovered
  from `go.work`, so it got build, vet, single-pass `-race`, golangci-lint and
  govulncheck.
- **What it did not get:** it was not in hardening's hard-coded 16-entry
  `AI_MODULES`, so it had no repeated shuffled race, no 60% coverage floor and
  no benchmark execution.
- **How it was held open:** T12 pinned the gap with two tripwire tests.

Measured on `main`, against hardening's three extra gates:
- repeated shuffled race: **passes** (195 s);
- benchmarks: 12 ran;
- coverage floor: **fails at 41.7%**.

All 21 uncovered statements were entry-point error handling in `main.go`:

| Function | Uncovered statements |
|---|---|
| `main` | 3 |
| `run` | 16 |
| `buildService`, `newVoiceIntelligence` (one error branch each) | 2 |

### 6.2 Options assessed

| Option | Files | Effect on the 60% gate | Policy decision |
|---|---|---|---|
| A. Add to `AI_MODULES` as it stands | `hardening.yml`, T12 tripwires | **fails** (41.7%) | yes |
| B. Tests for `run()` with no production change | new test files | **80.6%** | no |
| C. Refactor `run()` for injection | `main.go` and tests | above 60% | no; a production change B makes unnecessary |
| D. Keep it outside `AI_MODULES` | none | status quo; gap persists | a decision to defer |
| E. Lower the floor or exempt the module | `hardening.yml` | passes by weakening the gate | **rejected** |

### 6.3 Decision and implementation: B+A (approved)

- **Branch:** `phase14/t13-voice-service-hardening`.
- **PR:** [#14](https://github.com/123Anujyadav/aegis-voice-agent/pull/14).
- **Commit:** `c5db6f60b1e15e803d774595a639a5dc75edaf73` (5 files, +175/−27).

What the commit does:
- `run_test.go` (portable) covers configuration that cannot be loaded
  (`CS_VOICE_HEALTH_PORT=not-a-port`) and configuration that fails validation
  (`CS_VOICE_ENVIRONMENT=nowhere`).
- `run_unix_test.go` (`//go:build unix`) covers a full start on free loopback
  ports. It waits for `/healthz`, sends SIGTERM to the test process and
  expects a clean `nil`.
  - **Why the signal is safe:** `platform.Service.Run` installs
    `signal.NotifyContext` before serving, so an answering `/healthz` proves
    the handler is registered.
- `run_other_test.go` (`//go:build !unix`) declares the same test and
  **skips it with a reason**. A non-Unix run therefore reports its lower
  coverage instead of hiding it.
- None of the tests is parallel: `t.Setenv` is process-wide and the signal is
  process-wide. Each restores the default logger that `run()` replaces.
- `services/go/voice` is appended to `AI_MODULES`, the first `services/` entry.
  **`floor=60` is unchanged, and no exception or exclusion was added.**
- `TestT12_ServiceModuleIsAbsentFromHardeningAllowlist` became
  `TestT12_ServiceModuleIsInHardeningAllowlist`, and
  `TestT12_HardeningAllowlistIsIntact` now expects 17 entries.
- **No production code, dependency or other workflow changed.**

## 7. CI status

| Revision | Run | Result |
|---|---|---|
| `main` `c294fc6` | Backend Foundation 39033784832 | pass |
| | hardening 39033784795 | pass |
| | pr-go 39033784959 | 97 jobs pass; 40 lint jobs fail (baseline) |
| PR #14 `c5db6f6` | Backend Foundation 39040987348 | **pass** |
| | hardening 39040987433 | **pass**: race single, race repeated, coverage, platform verification, benchmarks, release gate |
| | pr-go 39040987412 | 97 jobs pass, including 45/45 govulncheck; 40 lint jobs fail (baseline) |

**Hardening on PR #14 included `services/go/voice`:**
- its coverage summary records **`services/go/voice 80.6%`**, and the floor
  check passed;
- its single and repeated race logs both end `ok`, with no failure and no
  data race;
- its benchmarks ran (36);
- the toolchain was go1.26.9 linux/amd64.

**Lint:** the same 40 golangci-lint jobs fail on `main` and on PR #14, with
1,022 findings each. That is a repository-wide baseline; this work adds none.
The only per-module difference is `persistence`, where the linter labels the
same two identifiers (`SqlUnitOfWork`, `NewSqlUnitOfWork`) under a different
`revive` rule from run to run, in code that is not changed.

## 8. Known limitations and open observations

- **Audio:** the service starts and exposes the intelligence path, but **does
  not process audio** (§9).
- **Other boundaries, carried from `beb46df`:**
  - tool execution and governance are not reachable through the `Planner`
    seam;
  - interruption and cancellation are observed at turn boundaries;
  - which session is evicted is unspecified when timestamps tie.
- **Windows coverage:** `run()`'s serve-and-shutdown test is Unix-only, so
  Windows coverage is lower. The hardening floor is measured on Linux CI.
- **Open: one unexplained T9 failure.**
  `TestT9_FreshRunningServiceSignatureIsStable` failed once, in the first
  local `-race -count=5 -shuffle=on` module run on the T13 branch. Its message
  was not captured.
  - **What the timing shows:** 4.03 s, against 4.23–4.30 s across 75 passing
    runs. It stopped at roughly its 19th of 20 fresh services, consistent with
    a late signature drift rather than a startup error.
  - **Not reproduced in 115 further runs:**
    - 3 replays of the same shuffle seed;
    - 6 module runs;
    - 30 runs interleaved with the T13 tests under shuffle;
    - 40 isolated runs on `main`'s code under load;
    - plus CI's repeated race job on PR #14.
  - **No evidence ties it to T13:** both tests are sequential, and the T13
    tests restore the environment and the logger.
  - **Status:** open, not dismissed. With T13, CI's repeated race job now
    covers this module, so a recurrence will be captured with its output.

## 9. Open dependency: provider provisioning (ADR 0017)

A full `voice.Pipeline` needs a `ProviderRegistry`. Every provider needs one of
the following:

- **Local engines** (Ollama with a model, whisper.cpp or whisper-cli with a
  model, Piper with a voice): **model downloads** and local installation, with
  no credentials.
- **Cloud STT/TTS/LLM providers:** **credentials and possibly spending.**

Decisions required before that stage:
- local or cloud, per stage (STT, LLM, TTS);
- which models, voices, sizes and licences;
- where binaries and models live in development and CI;
- whether CI may download models;
- for cloud providers, credential storage (never pasted into chat) and a
  spending limit.

**None of this was authorized, and none was done.** No model was downloaded,
no provider was provisioned and no credential was requested.

## 10. Open blockers (outside Phase 14, unchanged)

D8 unapproved; D9 and D10 unresolved; D11 deferred; `prod-global` unresolved;
INFRA-AUTH (A7) unapproved; F-3, F-4 and F-7 open
(docs/telephony/P1-40…P1-47).

## 11. Production-readiness statement

- **Staging: NO-GO.**
- **Exotel AgentStream production media: NO-GO**, because client-certificate
  mTLS support is missing.
- Phase 14 made no deployment, no infrastructure change, no live carrier call
  and no provider provisioning, and changes neither status.

## 12. Closure condition

Phase 14 closes when **PR #14 is reviewed and merged**, and hardening on the
resulting `main` passes with `services/go/voice` included. T13 is then
COMPLETE.

Until then, T13 is IMPLEMENTED and CI-VERIFIED on PR #14 only, and Phase 14 is
**AWAITING PR #14 MERGE**.
