# Platform Readiness

**Phase 11E is implementation-complete. Production traffic is NOT approved.**

Those are two different statements and this document keeps them apart.

> **Updated 2026-08-16 (Phase 12).** CI has now executed. **R1 (`-race`) and R4
> (`govulncheck`) are CLOSED** on run-identified evidence; R2, R3 and R5 remain
> open and **production traffic is still not approved**. The release gate is
> currently **RED** for a frozen Phase 10F defect unrelated to voice. Superseded
> lines are struck through rather than deleted. See
> [§6](#6-phase-12-what-ci-changed).

---

## 1. Readiness matrix

### IMPLEMENTED — the code exists and compiles

| Component | Location |
|---|---|
| Voice orchestration core | `packages/go/voice` |
| Provider registry over the frozen router | `registry.go` |
| Session lifecycle FSM (11 states, 38 edges) | `fsm.go`, `state.go` |
| Streaming pipeline | `pipeline.go` |
| Barge-in orchestration | `bargein.go` |
| Governed-action gateway | `governed.go` |
| Process supervision | `providers/process` |
| Four provider adapters | `providers/{whispercpp,whispercli,piper,ollama}` |
| Bounded events and metrics | `events.go`, `metrics.go` |
| Documentation | `docs/voice/` (14 documents) |

### VERIFIED — an executed test or command asserts it

| Property | Evidence |
|---|---|
| **`go test -race`, both CI modes** | **EXECUTED 2026-08-16 — zero findings** ([§6](#6-phase-12-what-ci-changed)). Not a claim of race-freedom; see the limits there |
| `gofmt`, `go vet`, `go test ./...` | all PASS, 6 packages |
| `go test -count=10 -shuffle=on ./...` | **5 consecutive clean runs** |
| `GOWORK=off go build ./...` | PASS — module-isolated |
| Dependency closure | zero third-party; no cycle; no `toolruntime`/`memory` |
| State machine | all 121 ordered pairs; all 38 edges executed |
| Failure matrix | 17/17 named cases |
| Concurrency isolation | 12 sessions over shared router/providers/metrics |
| Behavioural determinism | 8 identical replays × 3 scenarios |
| Governance cannot be bypassed | 27 tests + mutation testing |
| Barge-in correctness | 0 stale frames, measured at the media sink |
| Security | 28 areas audited; 24 tests |
| Frozen phases untouched | mtime verification |

**285 tests, 28 benchmarks.**

### MEASURED — orchestration only, no inference

| Quantity | Value |
|---|---|
| Provider routing | 43.1 ns/op, 0 allocs |
| Governance gate | 138.6 ns/op, 0 allocs |
| Generation guard (per frame) | 0.553 ns/op |
| Whole turn | 95,474 ns/op, 406 allocs |
| Process spawn + reap | 7.17 ms |

Full methodology and limits in [PERFORMANCE.md](PERFORMANCE.md).

### DEVELOPMENT-ONLY

| Item | Status |
|---|---|
| Ollama adapter | Not an ADR-0006 tier. Refused by the registry in production mode. |
| Local open-weight models | Environment facts, never approved production models. |

### OPEN RISKS — decisions owed before production

| # | Risk | Impact | What would close it |
|---|---|---|---|
| ~~**R1**~~ | ~~**`-race` has NEVER run** — no C compiler~~ **CLOSED 2026-08-16** | — | **Closed by CI, not locally.** The stated action was *"run CI on a machine with a C toolchain"*; that happened. Every Phase 11E package passed `-race` in both modes with zero findings — [§6](#6-phase-12-what-ci-changed) and [EVALUATION_REPORT §9.1](EVALUATION_REPORT.md#91-race-detector--executed-zero-findings). The local machine still has no C compiler. |
| **R2** | **Complete voice loop never executed** — no TTS runtime, no LLM model | The full turn has not been observed once, end to end | Install Piper + a voice; pull a model; re-run Task 17 |
| **R3** | **Real provider inference largely unmeasured** | One `tiny`-model STT run is not a characterisation | Measure each provider on target hardware |
| ~~**R4**~~ | ~~**No `govulncheck`; no standalone SAST**~~ **CLOSED 2026-08-16** | — | **Ran in CI.** 4 findings, **all Go standard library**, all fixed in go1.25.13 (runner had go1.25.12); **zero third-party**. `golangci-lint` also now genuinely runs in CI and reports real pre-existing findings — [EVALUATION_REPORT §9.3, §9.5](EVALUATION_REPORT.md#93-govulncheck--executed). *Closed on the vulnerability half. gosec and staticcheck still run only bundled inside `golangci-lint`, never standalone — [EVALUATION_REPORT §4](EVALUATION_REPORT.md#4-static-and-security-tooling) — which remains accurate and is not claimed otherwise.* |
| **R5** | **Load tested to 64 concurrent sessions on a laptop** | Behaviour at production concurrency is unknown | Load test on target hardware |
| **R6** | **whispercli is batch-only** | Cannot serve a low-latency voice loop | Use whisper.cpp streaming or a streaming provider |
| **R7** | **Ollama daemon observed flapping** | A dev dependency is unreliable | Probe, never assume — the adapter already does |

### KNOWN LIMITATIONS — understood and accepted

| Limitation | Rationale |
|---|---|
| A `FrameSink` ignoring its context may play one in-flight frame after a barge-in | Blocking the abort behind a slow media write would be worse |
| A whispercli temp directory persists if a stream is never `Close`d | A finalizer would make deterministic cleanup unpredictable (SEC-2) |
| The AST content-leak guard is heuristic | Narrow by design; broadening it would flag `len(text)`, the remedy |
| Single-shot timings below ~950 µs are unmeasurable here | Clock granularity; reported as such, never invented |
| A bounded 2 s wait for the stderr drain | Prevents a grandchild wedging exit notification |

## 2. What "not production ready" means concretely

It does **not** mean the code is unfinished or lightly tested. Within its
verified envelope this is well-covered work: the gates found six production
defects and every one was reproduced, fixed and mutation-verified.

It originally meant five specific things had not been demonstrated. **Two have
since been demonstrated in CI; three have not.**

| # | | Status |
|---|---|---|
| 1 | No race-detector run (**R1**) — *"the single largest gap"* | **CLOSED 2026-08-16** — ran in CI, both modes, zero findings |
| 2 | The complete loop has never run once (**R2**) | **STILL OPEN** — needs a TTS runtime and an LLM model |
| 3 | Provider inference uncharacterised on target hardware (**R3**) | **STILL OPEN** |
| 4 | No vulnerability scanning (**R4**) | **CLOSED 2026-08-16** — `govulncheck` ran; 4 stdlib findings, zero third-party |
| 5 | Production-scale concurrency untested (**R5**) | **STILL OPEN** — CI runs the test suite, not a load test |

Closing R1 and R4 removes two blockers. It does **not** make this production
ready: R2, R3 and R5 are untouched, and R5 in particular is not addressed by
anything CI does — running a test suite under `-race` is not a load test.

## 3. Deployment prerequisites

Before this serves a real caller:

- [x] `go test -race ./...` clean on a machine with a C toolchain — **done in CI
      2026-08-16**, both modes, zero findings ([§6](#6-phase-12-what-ci-changed))
- [ ] A TTS runtime installed and a full E2E turn executed
- [ ] An LLM model available **and the ADR-0006 production tier used**, not Ollama
- [x] `govulncheck` clean — **ran 2026-08-16**; 4 findings, all Go stdlib, all
      fixed in go1.25.13, zero third-party. *Ticked as "executed and understood",
      not as "zero findings": deploying on go1.25.13 or later is the remedy.*
- [ ] Load test at production concurrency on target hardware
- [ ] A streaming STT provider selected (not whispercli)
- [ ] A durable governance auditor wired (the engine refuses to start without one)
- [ ] Media `FrameSink` confirmed to honour its context
- [ ] **The release gate is green.** It is currently RED for a reason unrelated
      to voice — see F2 in [§6](#6-phase-12-what-ci-changed)

## 4. What is safe to do today

- Run the **development loop** locally with no API key, no credential and no
  paid service.
- Develop against the frozen ports, knowing a cloud provider swaps in as a
  configuration change.
- Rely on the **governance gate**: it cannot be bypassed without editing
  `packages/go/voice`, and that is a code review.
- Rely on the **failure guarantees**: one provider hiccup costs one turn, not the
  call.

## 5. Statement

> Phase 11E's implementation is complete and verified within a clearly bounded
> envelope. It is **not** approved for production traffic. The blocking gaps are
> R1–R5, each with a concrete closing action. No claim of race-safety,
> end-to-end verification or production readiness is made anywhere in this
> documentation set.

**Amendment, 2026-08-16.** R1 and R4 are now closed by CI evidence (§6). The
statement otherwise stands unchanged: **this is still not approved for production
traffic**, R2/R3/R5 remain open, and no claim of race-safety or end-to-end
verification is made. The release gate is currently **RED**.

---

## 6. Phase 12: what CI changed

Phase 11E was verified on one Windows laptop with no C compiler. Phase 12 put
the repository under version control and ran the CI that had existed, unexecuted,
since Phase 10.5. Full detail and run IDs:
[EVALUATION_REPORT §9](EVALUATION_REPORT.md#9-phase-12-ci-execution-the-race-detector-has-now-run).

### What closed

**R1 — the race detector ran.** `ubuntu-24.04`, go1.25.12, `cc` 13.3.0. All
Phase 11E packages passed both `-race -count=1` and `-race -count=5 -shuffle=on`
(seed `1786872847521114476`), and 39 of 40 workspace modules passed in `pr-go`.
**Zero data races.**

Phase 12 also found that `hardening.yml`'s nightly race job had a hard-coded
module list **excluding every Phase 11 module** — so the most concurrent code in
the platform was outside the deepest race gate. Task 3 added `telephony`,
`media`, `audiointel`, `audiobridge`, `speech` and `voice`.

**R4 — `govulncheck` ran.** 4 findings, all Go standard library, all fixed in
go1.25.13; zero third-party.

### What R1's closure does NOT mean

- The detector reports only interleavings that **actually occur**. Clean runs
  mean none was observed, not that none exists.
- One platform, one Go version: `ubuntu-24.04`/amd64/go1.25.12. No Windows, no arm64.
- Five shuffled executions is not exhaustive scheduling.
- Statements never executed were never examined; coverage is 60.8–100%.
- `evalsubjects` is **unverified under `-race`** — its tests fail for the
  unrelated reason below, so the detector's verdict there is unknown.

**No claim of "race-safe" or "concurrency fully verified" is made.**

### What is still red

**F2 — frozen Phase 10F, UNRESOLVED.** `packages/go/evalsubjects` fails two
verification tests and is the **sole** cause of every red hardening job and of
the red release gate. It is not a race and not a behavioural regression: the
behaviour digest is identical on both sides of every drift, and only wall-clock
latency differs, by ×2.19–×6.84 against the `LatencyRatio: 2.0` in frozen
`packages/go/evaluation/compare.go:120`.

Both measurements come from the **same process seconds apart** — the test
baselines itself at run time — so this is differential run-to-run jitter, not
machine-to-machine skew. It did not reproduce in 72 local executions across seven
condition sets, and it is intermittent in CI. **Neither frozen module was
modified.** Resolving it requires a decision about frozen code.

**F5 — `gofumpt`, 129 files, UNRESOLVED.** 121 in frozen or non-voice modules,
8 under `packages/go/voice`. This does **not** contradict the `gofmt` PASS above:
`gofmt -l packages/go/voice` is still 0 files; `gofumpt` is a stricter superset
no prior phase ran. Reformatting frozen modules is a policy decision, untaken.

**Lint findings are now visible.** CI's `golangci-lint` had never actually run —
a prebuilt binary built with go1.23 refused every `go 1.25.0` module, so 39 of 40
jobs failed having produced zero findings. Task 3 fixed the tooling and **changed
no lint rule**; CI now reproduces Phase 11E's local figures exactly (voice 172,
speech 47). Those jobs are red for genuine pre-existing findings.

**`go.work.sum` is gitignored** (`.gitignore:69`) and never reaches CI, so a true
staleness gate is not implementable without a repository-policy change. Recorded,
not taken.

### Decisions this leaves open

1. **F2** — change a frozen tolerance, or accept a permanently red release gate.
2. **`go.work.sum`** — track it, or accept that no staleness gate can exist.
3. **F5 and lint findings** — whether frozen modules may be reformatted or fixed.
