# Phase 13 — Engineering Audit

Every numeric claim traces to a command, test, benchmark or source line. **No CI
run ID exists**, because CI has not executed; none is fabricated.

## Status classification

| Label | Meaning |
|---|---|
| IMPLEMENTED | the code exists and does the thing |
| VERIFIED LOCALLY | a test proves it on this machine, mutation-checked where applicable |
| MEASURED | a number produced by an actual command run |
| CI-PENDING | configured to run in CI; CI has not executed |
| NOT RUN | not executed anywhere |
| BLOCKED | cannot proceed without approval |
| OUT OF SCOPE | deliberately untouched |

## Files changed by Phase 13

Command: `git status --short`

| Path | Change | Kind |
|---|---|---|
| `packages/go/intent/` | new module — 4 non-test + 7 test files | production + tests |
| `packages/go/voiceintel/` | new module — 1 non-test + 7 test files, plus `testdata/determinism.golden` | production + tests |
| `go.work` | +2 `use` entries | workspace |
| `.github/workflows/hardening.yml` | +2 lines in `AI_MODULES` | CI |
| `docs/adr/0016-intent-classification.md` | new | docs |
| `docs/adr/README.md` | +1 index row | docs |
| `docs/phase13/` | 15 documents | docs |

**No frozen module file was changed.** No other workflow was changed
(`pr-go.yml`, `ci.yml`, `pr-contracts.yml`, `pr-python.yml` untouched).

## Frozen-module integrity — VERIFIED LOCALLY

Command: SHA-256 over each module's `*.go`, sorted, re-hashed; compared to the
Phase 13 baseline.

**Result: 13/13 digest-identical** — speech, runtime, governance, conversation,
memory, toolruntime, audiointel, audiobridge, media, metrics, evaluation,
evalsubjects, telephony. Re-verified before and after every task from T7 to T15.

## Dependency closure — VERIFIED LOCALLY

Command: `go list -deps -test ./...`

| Module | third-party | governance | toolruntime | memory | net/http |
|---|---|---|---|---|---|
| `intent` | **0** | absent | absent | absent | absent |
| `voiceintel` | **0** | present (transitive via `voice`, `// indirect` since T6) | absent | absent | absent |

`intent`'s own `go.mod` requires only callscreen modules; its compiled set is 4
first-party packages plus stdlib.

## Test counts — MEASURED

Command: `go test -count=1 -v ./... | grep -c '^--- PASS'`

| Module | top-level PASS | sub-tests PASS | FAIL |
|---|---|---|---|
| `intent` | **72** | 14 | 0 |
| `voiceintel` | **74** | 51 | 0 |

Workspace: **45/45 modules** build and test OK
(`go work edit -json` enumeration, then per-module `go build && go test`).

## Mutation counts — VERIFIED LOCALLY

| Task | Subject | Mutations | Caught |
|---|---|---|---|
| T5 | security guards | 6 | 6 |
| T6 | bridge wiring | 4 | 4 |
| T8 | turn semantics | 5 | 5 |
| T9 | failure handling | 7 | 7 |
| T10 | concurrency/isolation | 5 | 5 |
| T11 | determinism | 6 | 6 |
| T13 | evaluation fixtures | 6 | 6 |
| T14 | CI coverage selection | 5 | 5 |
| **Total** | | **44** | **44** |

**Mutations that required correction before counting** — recorded because a
mutation that does not apply proves nothing:

- T8 M5 — `time.Now().UnixNano()%2` was **inert** on Windows (coarse clock
  granularity); replaced with map-iteration order.
- T9 M1/M5 — **inert**; exposed two real test gaps (the bridge never calls
  `ClassifyTurn`; `assertNoStaleOutput` bypassed the planner). Both fixed.
- T10 M3/M5 — **inert**; the Bridge has no cancellation API and the frozen engine
  deletes terminated conversations, so those defects are not expressible there.
  Re-targeted.
- T11 M1/M2 — M1 undetectable until a distinct-confidence fixture was added; M2
  inert because slot tables are already sorted.
- T13 M1 non-compiling, M5 inert (bridge-per-goroutine). Both fixed.

## Benchmark counts — MEASURED

`go test -run '^$' -bench .` reports **26** benchmark results in `intent` and
**19** in `voiceintel`. Full figures in [PERFORMANCE.md](PERFORMANCE.md).

## Build / vet / format — VERIFIED LOCALLY

| Gate | intent | voiceintel |
|---|---|---|
| `gofmt -l` | clean | clean |
| `go vet ./...` | exit 0 | exit 0 |
| `go build ./...` | exit 0 | exit 0 |
| `GOWORK=off go build ./...` | exit 0 | exit 0 |
| `GOWORK=off go vet ./...` | exit 0 | exit 0 |
| `go test -count=10 -shuffle=on` | ok | ok |

Repo-wide `gofmt -l` reports 22 unformatted files; **none is a Phase 13 file**
(verified count 0). Pre-existing, untouched.

## Frozen constants cited — source-verified

| Constant | Value | Source |
|---|---|---|
| `AcceptThreshold` | 0.75 | `conversation/intent.go:206` |
| `RejectThreshold` | 0.45 | `conversation/intent.go:207` |
| `AmbiguityMargin` | 0.15 | `conversation/intent.go:208` |
| `MinASRConfidence` | 0.40 | `conversation/intent.go:209` |
| `DefaultTTL` | 10m | `conversation/context.go:123` |
| `TemporaryTTL` | 30s | `conversation/context.go:124` |
| `MaxEntriesPerScope` | 256 | `conversation/context.go:125` |
| `MaxSnapshots` | 8 | `conversation/context.go:126` |
| `maxTokens` | 512 | `intent/lexicon.go:217` |
| `MaxTurns` (default persona) | 40 total turns = 20 caller round-trips | `conversation/persona.go:138`, `planner.go:135`, `turn.go:469` |
| `ClarificationBudget` (default) | 3 | `conversation/persona.go:137` |
| Coverage floor | 60% | `.github/workflows/hardening.yml` |

## CI workflow modification — VERIFIED LOCALLY, EXECUTION CI-PENDING

`hardening.yml`: **+2 / -0 lines, one hunk** — `packages/go/intent` and
`packages/go/voiceintel` appended to `AI_MODULES` (14 -> 16), preserving the
file's dependency-order convention.

`pr-go.yml`: **UNCHANGED**. Its `go.work` discovery already yields 45 modules
including both Phase 13 modules — verified by running its own expression.

Selection proven by parsing the YAML and expanding the folded scalar as the shell
word-splits it (not by grepping names): 16 modules, 3 loops all on
`${AI_MODULES}`, floor 60%. Mutation-verified 5/5.

Local coverage pre-check against the 60% floor: `intent` **94.5%**,
`voiceintel` **66.7%**.

## Status summary

| Item | Status |
|---|---|
| Classifier, slots, turn semantics, bridge | IMPLEMENTED · VERIFIED LOCALLY |
| Context isolation, failure handling, concurrency, determinism | VERIFIED LOCALLY |
| Benchmarks | MEASURED |
| Evaluation fixtures | VERIFIED LOCALLY (fixture pass, **not** NLU accuracy) |
| CI workflow change | IMPLEMENTED · VERIFIED LOCALLY |
| CI execution, CI race, CI coverage | **CI-PENDING** (no run ID) |
| Local `-race` | **NOT RUN** — no C compiler |
| F2 `LatencyRatio: 2.0` | **OUT OF SCOPE** — untouched, still open |
| Trace export | **BLOCKED** — needs ADR + third-party dep |
| Service wiring / deployment | **NOT RUN** — no service imports Phase 13 |
| API key / credential | none required, none present |
| Production readiness | **NOT CLAIMED** |
