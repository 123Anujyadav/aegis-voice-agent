# Replay Architecture

**Phase 10F** · `packages/go/evaluation/engines.go`

---

## 1. Replay is not re-running

`Execute` and `Replay` both run a scenario. The difference is **what they
compare against**, and it decides what each one is for.

| | Compares against | Answers |
|---|---|---|
| `Execute` | the **approved golden** | "does this still do what we agreed is correct?" |
| `Replay` | a **specific recording** | "does this still do what it did on Tuesday?" |

The second question does not require anybody to have approved Tuesday. That is
what makes replay the incident tool: during an investigation, the interesting
recording is usually one nobody reviewed, taken from the run that went wrong.

```go
func (e *EvaluationRuntime) Replay(ctx context.Context, s Scenario,
    recorded Observation) ReplayResult

func (e *EvaluationRuntime) ReplayLatest(ctx context.Context,
    s Scenario) (ReplayResult, error)
```

---

## 2. What replay compares

Behaviour fingerprints, and nothing else:

```
result.Recorded   = recorded.BehaviourPrint()
result.Replayed   = observed.BehaviourPrint()
result.Reproduced = Recorded == Replayed
```

Durations are excluded, for the same reason they are excluded everywhere: a
replay that failed because the machine was busier on Thursday than on Tuesday
would be a replay nobody trusted.

On divergence, `behaviourDifferences` produces a sorted, field-level diff —
which step, which field, what it was, what it is now — so the result names the
change rather than reporting that a hash moved.

---

## 3. Version mismatch is refused, not compared

```go
if recorded.ScenarioVersion != s.Version {
    result.Reason = "scenario_version_mismatch"
    // refused, with both versions named
}
```

Replaying a v1 recording against a v3 scenario produces differences that are
real and meaningless. Refusing is more useful than reporting a diff whose every
line is explained by the version gap.

---

## 4. The replay anchor

A recording alone is not enough to reproduce a run. The **questions** must also
be reproducible, and scenarios change.

Every `Run` records:

- `RegistryVersion` — the monotonic snapshot number
- `RegistryDigest` — a fingerprint over the whole registered scenario set

So a run from three months ago names the exact scenario set it ran under.
`RegressionReport.RegistryChanged` surfaces the case where two runs being
compared did not ask the same questions — otherwise a "regression" could be
nothing more than a scenario that was edited between them.

---

## 5. Determinism is a precondition

Replay is only meaningful if the subject reproduces. `CheckDeterminism` runs a
scenario N times (default 3) and reports the distinct behaviour fingerprints
observed.

**Three, not two.** Two runs can agree by luck when the nondeterminism is a map
iteration that happened to land the same way. Three is the smallest number that
makes coincidence unlikely without tripling every suite's cost — and the engine
reports the count it used, so the confidence is legible rather than implied.

Verified: **all 18 scenarios deterministic across 3 runs** against the real
frozen engines.

If a subsystem did not reproduce, drift would become meaningless — the platform
could not distinguish "the engine changed" from "the engine is noisy", and every
golden would be a coin flip. This is why determinism failure makes
`PlatformReadiness.Ready` false even when nothing blocks.

---

## 6. Proving replay can fail

A replay engine that reproduces everything is indistinguishable from one that
compares nothing, and the second is the more likely bug.

`TestVerification_ReplayDetectsADivergentRecording` takes a recording from one
scenario, relabels it as another on the same subject, and replays. Observed:

```
runtime.breaker-opens on runtime: NOT REPRODUCED
  (47fbca2b51f3745c → 8a6de857d2c25471, 9 differences, behaviour_differs)
```

Nine differences, named. The engine compares.

---

## 7. Storage and retention

`ReplayLatest` pulls from `Storage.Observations(scenarioID)`, which is
**bounded** — 50 observations per scenario by default. Bounded storage means a
long-running platform cannot replay arbitrarily far back, and that is the
intended trade: an unbounded observation history is a permanent archive of
everything every subsystem ever did, which is a liability under the platform's
own 90-day retention rule (ADR-0012).

`TrendPoint` deliberately carries **no observation** — only the run, scenario,
subject, verdict, behaviour fingerprint, total step seconds and timestamp. A
years-long trend history therefore stores fingerprints, not content.
`TestIntegration_TrendPointCarriesNoObservation` enforces it.

The durable `Storage` implementation is **not built** — see ENGINEERING_AUDIT
A4. `MemoryStorage` is the only implementation, and the interface exists so a
durable one can be added without touching the engines.

---

## 8. Metrics

Every replay increments `Replays` labelled `reproduced` or `diverged`, per
subject. A rising divergence rate on one subsystem is the signal that
subsystem's behaviour has become unstable — visible before anybody opens a
report.

---

## 9. Related

- [SCENARIO_ARCHITECTURE.md](SCENARIO_ARCHITECTURE.md)
- [REGRESSION_REPORT.md](REGRESSION_REPORT.md)
- [EVALUATION_ARCHITECTURE.md](EVALUATION_ARCHITECTURE.md)
