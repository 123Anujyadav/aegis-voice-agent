# Phase 13 — Final Report

## 1. Executive summary

Phase 13 makes the platform's existing conversational intelligence **reachable**.

The intelligence machinery — intent resolution, confidence thresholds,
clarification budgets, turn-taking, context, planning — already existed, frozen,
in `packages/go/conversation`. What was missing was an implementation of the
extension port that engine declared, and anybody calling it. Before this phase
`conversation.NewEngine` appeared exactly once in the repository, in a test,
without a classifier — so every production utterance resolved to the fallback
intent.

Phase 13 adds two leaf modules: `intent` (a deterministic, closed-vocabulary
classifier implementing `conversation.IntentClassifier`) and `voiceintel` (a
composition root calling `conversation.WithClassifier` and handing the result to
`voice` as a `voice.Planner`). **No second intelligence engine, no second FSM, no
second context or memory system, no model, no network, no third-party
dependency.**

Verification was mutation-driven throughout: 44 mutations, 44 caught — and
several mutations that turned out inert were corrected rather than counted,
exposing real gaps in the tests themselves.

**Not claimed:** NLU accuracy, race safety, CI verification, production
readiness.

## 2. Scope

**In:** the classifier, slot shape extraction, turn/interruption semantics, the
voice-conversation bridge, and verification of context isolation, failure
handling, concurrency, determinism, performance and evaluation — plus CI
coverage configuration.

**Out:** F2 (`LatencyRatio: 2.0`), trace export, service wiring, any frozen
module change, any model or provider.

## 3. Task status

| Task | Description | Status |
|---|---|---|
| T1 | ADR-0016 + dependency map | COMPLETE |
| T2 | `intent` module scaffold | COMPLETE |
| T3 | Deterministic classifier core | COMPLETE |
| T4 | Bounded slot extraction | COMPLETE |
| T5 | Security guards + mutation | COMPLETE |
| T6 | Voice-conversation bridge | COMPLETE |
| T7 | Session context isolation | COMPLETE |
| T8 | Turn / interruption semantics | COMPLETE |
| T9 | Failure injection (13 cases) | COMPLETE |
| T10 | Concurrency, isolation, cancellation | COMPLETE — LOCAL |
| T11 | Determinism contract + golden | COMPLETE |
| T12 | Benchmarks | COMPLETE |
| T13 | Evaluation fixtures | COMPLETE |
| T14 | CI coverage (workflow edit) | COMPLETE — LOCAL |
| T15 | Documentation | COMPLETE |
| — | CI execution / CI race / CI coverage | **CI PENDING** |
| — | Local `-race` | **NOT RUN** |
| — | F2 `LatencyRatio: 2.0` | **OUT OF SCOPE** |
| — | Trace export | **BLOCKED** |

T10 and T14 are marked COMPLETE — LOCAL because their central evidence
(race detector; CI execution) can only come from CI.

## 4. Architecture

`voice` → `conversation` → `intent.Classifier` → the frozen
`conversation.IntentClassifier` port. `voiceintel` is a leaf imported by nothing.
Full detail and diagram: [ARCHITECTURE.md](ARCHITECTURE.md).

## 5. Implemented components

- **`packages/go/intent`** — 4 non-test files, 7 test files. Closed 11-name
  vocabulary; bounded input (512 tokens); canonical candidate and slot ordering;
  no clock read, no randomness, no package mutable state beyond one read-only
  table.
- **`packages/go/voiceintel`** — 1 non-test file (`bridge.go`), 7 test files, one
  golden. `Bridge` holds exactly one field: the frozen engine.
- **`intent.ClassifyTurn`** — pure function; every field of its input and output
  is a frozen `conversation.*` type, so it cannot name an FSM state.

## 6. Verification results

| Area | Result |
|---|---|
| `intent` tests | **72** top-level PASS, 0 FAIL |
| `voiceintel` tests | **74** top-level PASS, 0 FAIL |
| Full workspace | **45/45** modules OK |
| Mutations | **44/44 caught** |
| Determinism | golden matched across 6 processes and 6 shuffle seeds; 100+ in-process repetitions |
| `gofmt` / `vet` / `build` / `GOWORK=off` | clean, exit 0 |
| `-count=10 -shuffle=on` | ok, both modules |

## 7. Security

Verified: no credentials, no network, no model, **0 third-party dependencies**,
no `toolruntime`/`memory` dependency, no governance reference in `intent`, no
tool-execution capability, bounded vocabulary/candidates/slots, transcript and
slot values treated as sensitive, no credential-shaped or PCM-shaped content in
operational fields, mutation-verified guards.

**Not claimed:** complete security assurance, penetration testing, fuzzing, or
third-party review. See [SECURITY.md](SECURITY.md).

## 8. Concurrency

Verified locally: shared immutable classifier gives identical results under 64
goroutines vs a serial baseline; 16-session isolation under classification,
context churn, cancellation, interruption, termination and mixed failure
pressure; outcomes deterministic across 21 repeats.

**Race-detector evidence: NOT RUN — CI PENDING.** Exact reason: no C compiler
(`cgo: C compiler "gcc" not found`). The phrases *race-safe*, *race detector
clean* and *fully concurrency verified* are **not** claimed. See
[CONCURRENCY.md](CONCURRENCY.md).

## 9. Performance

Category A (orchestration) only; **Category B (model inference) NOT RUN — none
exists**. Headline measured medians: classification 1.4–2.3 microseconds; turn
classification 19 ns (silence) to 332 ns; context `Get` 45 ns; eviction 3.7
microseconds (~29x insert); a warm-session full turn **8.1 microseconds**;
1→16 goroutines scales 1.87x. Three benchmark-harness defects were found and
fixed, one of which had been inflating `Get` cost by ~64%. No optimization
claimed. See [PERFORMANCE.md](PERFORMANCE.md).

## 10. Evaluation

12 required categories; 9 tests, 18 sub-tests, all PASS; 6/6 mutation guards
caught; inventory re-run 26 times identically.

**These are deterministic behavioural fixtures, NOT NLU accuracy measurements.**
Any rate quoted is a **fixture pass rate**. See [EVALUATION.md](EVALUATION.md)
and [EVALUATION_FIXTURES.md](EVALUATION_FIXTURES.md).

## 11. CI status

**CI NOT RUN. PUSH NOT PERFORMED. No CI run ID exists.**

- `pr-go.yml` — unchanged; already discovers all 45 modules from `go.work`,
  including both Phase 13 modules (verified by running its own expression).
- `hardening.yml` — **+2 lines**, `AI_MODULES` 14 → 16, placing both modules into
  the race, coverage and benchmark loops. Selection verified by parsing the YAML,
  and mutation-verified 5/5.

Workflow configured is **not** CI executed. Pushing is outward-facing and awaits
explicit approval; `gh` is also not installed, so a run could not be inspected.

## 12. Frozen-module integrity

**13/13 digest-identical** to the Phase 13 baseline, re-verified before and after
every task from T7 onward.

## 13. Known limitations

Eighteen are documented in full in [LIMITATIONS.md](LIMITATIONS.md). The ones
that matter most: not model-based NLU and no accuracy claim; no real-world
corpus; `-race` not run; CI pending; tied-timestamp eviction order-unspecified
(frozen); `ScopeShared` does not share (frozen); the slot canonical sort is
currently a no-op; a pre-existing Phase 11E barge-in flake under CPU
oversubscription; and Phase 13 is **not wired into any running service**.

## 14. Open blockers

| Blocker | Status |
|---|---|
| Push approval for the T14 CI change | awaiting explicit user approval |
| CI race + coverage evidence | CI PENDING |
| **F2** `LatencyRatio: 2.0` | OPEN, OUT OF SCOPE — still the sole cause of the red release gate |
| Trace export | BLOCKED — needs a new ADR and a third-party dependency |
| Phase 11E barge-in flake | open, belongs to Phase 11E |
| 22 repo-wide gofmt deviations | open, Phase 12 lint policy |

When hardening runs, `evalsubjects` is in `AI_MODULES`, so **the release gate may
still be red solely because of F2**. That is a pre-existing Phase 12 failure and
must be classified separately from Phase 13 CI coverage.

## 15. API / credential status

**API KEY REQUIRED: NO.** No API key, cloud model, external service, model
download, network call or third-party Go dependency was introduced or is needed.
This does **not** mean a model capability exists — it means there is no model.

## 16. Production-readiness statement

**PRODUCTION READINESS: NOT CLAIMED.**

Phase 13 is verified locally, unpushed, unrun in CI, without race-detector
evidence, and **not wired into any running service** — `voiceintel` is a leaf
imported by nothing. There has been no load testing, no operational runbook, no
rollback plan, no SLO, and no production traffic approval. The release gate
remains red because of F2.

What Phase 13 does claim: the classifier is implemented, deterministic, bounded,
structurally isolated, and verified locally to the standard its tests describe.

## 17. Next phase recommendation

In dependency order:

1. **Commit and push** T1–T14 (with approval) so CI executes, and read the
   result — that converts CI-PENDING into evidence and supplies the `-race`
   verdict outstanding since T8.
2. **Classify the hardening result honestly**, separating Phase 13 coverage from
   the pre-existing F2 red gate.
3. **Decide F2** — it is the only thing keeping the release gate red and has been
   open since Phase 12.
4. **Service wiring** — Phase 13 is reachable but unused; wiring `voiceintel`
   into a service is what turns it into behaviour a caller experiences. That is a
   new phase with its own approval, not an extension of this one.
5. **Trace export** — needs an ADR and a dependency decision.

Not recommended yet: a model-backed classifier. The port exists and the
deterministic path is verified; adding inference before CI evidence and service
wiring would stack unverified work on unverified work.
