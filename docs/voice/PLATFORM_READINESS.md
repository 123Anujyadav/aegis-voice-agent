# Platform Readiness

**Phase 11E is implementation-complete. Production traffic is NOT approved.**

Those are two different statements and this document keeps them apart.

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
| **R1** | **`-race` has NEVER run** — no C compiler | Data races could exist that behavioural tests miss | Install MinGW-w64/TDM-GCC or run CI on a machine with a C toolchain; `go test -race ./...` clean |
| **R2** | **Complete voice loop never executed** — no TTS runtime, no LLM model | The full turn has not been observed once, end to end | Install Piper + a voice; pull a model; re-run Task 17 |
| **R3** | **Real provider inference largely unmeasured** | One `tiny`-model STT run is not a characterisation | Measure each provider on target hardware |
| **R4** | **No `govulncheck`; no standalone SAST** | Unknown dependency/code vulnerabilities | Install and run; note the third-party surface is zero |
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

It means five specific things have not been demonstrated:

1. No race-detector run (**R1**) — the single largest gap.
2. The complete loop has never run once (**R2**).
3. Provider inference is uncharacterised on target hardware (**R3**).
4. No vulnerability scanning (**R4**).
5. Production-scale concurrency is untested (**R5**).

## 3. Deployment prerequisites

Before this serves a real caller:

- [ ] `go test -race ./...` clean on a machine with a C toolchain
- [ ] A TTS runtime installed and a full E2E turn executed
- [ ] An LLM model available **and the ADR-0006 production tier used**, not Ollama
- [ ] `govulncheck` clean
- [ ] Load test at production concurrency on target hardware
- [ ] A streaming STT provider selected (not whispercli)
- [ ] A durable governance auditor wired (the engine refuses to start without one)
- [ ] Media `FrameSink` confirmed to honour its context

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
