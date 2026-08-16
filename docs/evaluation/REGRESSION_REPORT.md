# Regression Report — Phase 10F

**Engine:** `DetectRegressions` in `packages/go/evaluation/engines.go`
**Verified by:** `TestVerification_RegressionCleanRunOverRun`,
`TestVerification_RegressionDetectsARealChange`, plus integration coverage.

---

## 1. What a regression is here

A regression is a **difference between two runs**, not a difference from an
expectation. Two runs, both anchored to a registry version and digest, compared
scenario by scenario.

Seven kinds:

| Kind | Meaning |
|---|---|
| `behaviour` | the behaviour fingerprint moved |
| `policy` | a governance-kind scenario changed |
| `conversation` | a conversation-kind scenario changed |
| `memory` | a memory-kind scenario changed |
| `planning` | a tool-kind scenario changed |
| `latency` | total step time exceeded the ratio |
| **`coverage`** | **the scenario stopped being evaluated** |
| `improvement` | it got better |

`improvement` exists because a platform that can only report deterioration
teaches people its output is always bad news. A subsystem that got twice as fast
is a fact worth recording next to one that got twice as slow.

`coverage` was added during this phase. See §4.

---

## 2. The false-positive check

The most important property of a regression gate is that it does **not** fire
when nothing changed. A noisy gate is functionally identical to no gate, because
people learn to click through it.

Two identical runs against the real frozen engines:

```
regression run_ddffblcy3an6yaaaabekcebfpe (baseline)
         → run_ddffblc2siiiyaaaabf7a33liq (current):
  0 regressions, 0 improvements
```

Clean. This did not hold on the first attempt — see §5.

---

## 3. The true-positive check

An engine that never fires is indistinguishable from an engine that compares
nothing. `TestVerification_RegressionDetectsARealChange` retires one baseline
and runs again:

```
regression run_ddffblcy7mgtyaaaabfgb6xuhy (baseline)
         → run_ddffblc3jivdyaaaabgptkbnwu (current):
  1 regressions, 0 improvements
  - coverage conversation.escalation on conversation:
      scenario lost its baseline and is no longer evaluated
```

One regression, correctly classified, correctly attributed.

---

## 4. Losing a baseline is a regression

This was the phase's most consequential regression-engine defect.

The original engine reported a regression when a scenario **started failing** or
when its **behaviour fingerprint moved**. A scenario that lost its approved
golden did neither:

- its behaviour is unchanged — the engine still does exactly what it did
- its verdict is `NoBaseline`, not `Fail`

So retiring a golden, wiping the store, or bumping a scenario version without
re-approving all produced a run reporting **"0 regressions"** while a scenario
silently dropped out of the gate.

The reason a fingerprint check cannot catch this is worth stating directly: **an
unevaluated scenario's fingerprint is perfectly stable.** Stability is what the
check looks for, and an absence of evaluation looks exactly like it.

The fix introduces `Verdict.Compared()` — true for `Pass`, `Drift` and `Fail`,
false for `NoBaseline` and `Skipped` — and checks the transition **first**,
ahead of the fail arms. Ordering matters: a scenario going from `Fail` to
`NoBaseline` is not "no longer failing", it is no longer being asked, and the
original ordering would have reported it as an improvement.

See ENGINEERING_AUDIT F6.

---

## 5. Latency comparison needs a measurable denominator

The latency arm originally guarded only `wasTime > 0`. With total step time
quantising to zero on a 525 µs clock, two identical runs produced fourfold
**improvements**:

```
+ improvement memory.store-retrieve on memory:      faster: 2.748ms → 0s
+ improvement governance.consent-gate on governance: faster: 1.0828ms → 0s
+ improvement tool.plan-is-inert on toolruntime:     faster: 1.7013ms → 0s
   ... eight in a single verification pass
```

Every one of them was the clock, not the code.

`DetectRegressions` now takes a `latencyFloor` and requires **both** sides to
clear it. `Coordinator.CompareRuns` supplies the runtime's
resolution-adjusted floor, so the threshold is derived from the machine's
measured clock rather than guessed.

The same principle appears in `Compare` for single-observation latency
(ENGINEERING_AUDIT F5): a ratio needs a measurable denominator. Applied in one
place and not the other, it would produce a platform whose scenario verdicts and
run comparisons disagreed about whether anything had changed.

See ENGINEERING_AUDIT F7.

---

## 6. The registry anchor

Two runs can only be compared if they asked the same questions.

Every `Run` records `RegistryVersion` and `RegistryDigest`.
`RegressionReport.RegistryChanged` reports when they differ, and
`NewScenarios` / `RemovedScenarios` name what moved. Without this, a "behaviour
regression" could be nothing more than a scenario that was edited between the
two runs.

---

## 7. Blocking versus reporting

```go
func (r RegressionReport) Blocking() []Regression
func (k RegressionKind) Deterioration() bool { return k != RegressionImprovement }
```

`Blocking()` filters to deteriorations. Improvements are reported and never
block. `Clean()` reports the absence of any regression at all.

Consistent with the verdict model: the platform's job is to make change visible
and attributable, and only genuine inability to run a scenario stops a release
without a human in the loop.

---

## 8. Trend history

Beyond run-over-run comparison, `Storage.Trend(scenarioID)` keeps a bounded
history (500 points) and `TrendSeries` reports `BehaviourChanges` and `Stable`
over a window.

The headline of a trend is the change **count**: a scenario whose behaviour
changed nine times in twenty runs is a scenario nobody should be using as a
baseline, even if today's run passes. In the verification dashboard, all 18
trend series were stable across five consecutive runs.

`TrendPoint` carries no observation — only fingerprints and totals — so a
years-long history stays small and content-free. See SECURITY_REVIEW C11.

---

## 9. Performance

`BenchmarkDetectRegressions`: **102,086 ns/op**, 51,664 B/op, 606 allocs/op for
a 20-scenario pair. Dominated by two `BehaviourPrint` calls per scenario.

---

## 10. Related

- [REPLAY_ARCHITECTURE.md](REPLAY_ARCHITECTURE.md)
- [EVALUATION_REPORT.md](EVALUATION_REPORT.md)
- [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md)
