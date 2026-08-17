# Phase 13 — Concurrency

## Race-detector evidence: NOT RUN / CI PENDING

**Exact reason — no C compiler is available on this machine:**

```
$ CGO_ENABLED=1 go test -race -count=1 ./...
# runtime/cgo
cgo: C compiler "gcc" not found: exec: "gcc": executable file not found in %PATH%

$ go test -race -count=5 -shuffle=on ./...
go: -race requires cgo; enable cgo by setting CGO_ENABLED=1
```

`go env CC=gcc`, `CGO_ENABLED=0`, `GOOS=windows`. Searched and absent: `gcc`,
`clang`, `cc`, `x86_64-w64-mingw32-gcc`, `mingw32-gcc`, `tcc`; no mingw64,
msys64 or TDM-GCC install roots. GCC and Docker were **not** installed to
manufacture evidence.

**Nothing in this document is race-detector evidence.** The properties below are
value-isolation and determinism properties established by ordinary tests.
Ordinary tests passing is not race-freedom.

T14 added `intent` and `voiceintel` to `hardening.yml`'s `AI_MODULES`, so CI
will run `go test -race -count=1 -timeout=20m` and
`go test -race -count=5 -shuffle=on -timeout=30m` on both. **CI has not
executed** — nothing has been pushed.

### Historical context, not Phase 13 evidence

Phase 12 **Q1** was a real data race in frozen `toolruntime`
(`sandbox.go`), found by CI's race detector and fixed with race-detector
acceptance evidence. That history is why CI is the designated source here. It
says nothing about Phase 13, which has **no** race-detector result yet.

## What was verified locally

### Shared immutable classifier

One `Bridge`, one `intent.Classifier`, one config, shared by all sessions. The
classifier holds no mutable state — AST-guarded: no `*Classifier` method assigns
to a receiver field (`TestT10_ClassifierIsImmutableAfterConstruction`).

`Bridge` holds exactly one field, `*conversation.Engine`. A map, slice or `sync`
field would be a cross-session registry and fails
`TestT10_BridgeHoldsNoCrossSessionState`.

### Concurrent classification — VERIFIED LOCALLY

| Test | Shape | Result |
|---|---|---|
| `TestT10_SharedClassifierGivesIdenticalResultsUnderConcurrency` | 64 goroutines x 25 reps x 9 inputs vs a **serial baseline** | identical |
| `TestTurn_ConcurrentClassificationIsIsolated` (T8) | 64 goroutines x 50 iterations, pure `ClassifyTurn` | identical |
| `TestClassify_ConcurrentUseIsConsistent` (T3) | shared classifier | consistent |

Comparison is against a **serially computed baseline**, not against whatever the
concurrent run agreed on among itself.

### Concurrent session isolation — VERIFIED LOCALLY

| Test | Sessions | Property |
|---|---|---|
| `TestContext_SixteenConcurrentSessionsStayIsolated` (T7) | 16 x 40 iter | no cross-session context |
| `TestFailure13_ConcurrentFailuresStayIsolated` (T9) | 16 x 30 iter, half terminating | isolation under failure |
| `TestT10_SixteenSessionsClassifyAndStoreConcurrently` | 16 x 12 rounds | correct intent **and** marker per session |
| `TestT10_ConcurrentContextChurnStaysBoundedAndIsolated` | 16 x 320 inserts past the bound | bounded, isolated, eviction concurrent |
| `TestT13_ConcurrentSessionFixture` | 12 fixtures, **one shared bridge** | matches serial baseline; markers isolated |

Isolation is asserted by **observable value**, not object identity: every session
writes a unique marker under the same key and must read back its own.

### Cancellation, interruption, termination — VERIFIED LOCALLY

- `TestT10_CancellingOneSessionDoesNotCancelOthers` — 8 cancelled / 8
  classifying, released together; exactly 8 terminal and 8 live; survivors
  re-look-up their session each round, exercising the bridge lookup path under
  load.
- `TestT10_InterruptionAffectsOnlyTheInterruptedSession` — barge-in on half;
  uninterrupted neighbours keep their own `last_intent`.
- `TestT10_TerminationDuringConcurrentWorkLeavesNoStaleState` — terminate under
  load, then reuse the ids; every reused session starts clean and non-terminal.
- `TestT10_HealthySessionsSurviveMixedFailurePressure` — 16 healthy sessions
  alongside 4 with a failing classifier, interruptions, malformed input and
  termination; healthy sessions still produce the exact expected intent.

### Deterministic outputs under concurrency

`TestT10_RepeatedConcurrentRoundsAgree` — 21 runs of the full 16-session matrix,
identical per-session outcomes. Assertions are on **outcomes only**; goroutine
ordering and interleaving are never asserted, because they are not properties of
the system.

T11's concurrent-session scenario sorts per-session results before hashing, so
completion order cannot enter the determinism signature.

### Bounded mutable state

No package-level mutable state in either module's non-test source (AST guards in
T4, T7, T10, T11). The single package-level map is a read-only lookup table.
Benchmark sinks live in `_test.go` files, which the guards exclude by design.

## Repeat evidence

`go test -count=10 -shuffle=on ./...` — ok for both modules.
`go test -count=30 -run TestT10_ ./...` — ok.

## Honest summary

- Concurrent behaviour **VERIFIED LOCALLY** by value isolation and determinism.
- Race-detector verification **NOT RUN — CI PENDING**.
- The terms *race-safe*, *race detector clean* and *fully concurrency verified*
  are **not** claimed for Phase 13, and must not be until real `-race` output
  exists.
