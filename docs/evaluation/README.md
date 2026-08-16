# Enterprise AI Evaluation Platform — Documentation

**Phase 10F** · `packages/go/evaluation` + `packages/go/evalsubjects` · Status:
**PROPOSED — awaiting approval**

Permanent evaluation infrastructure for every AI subsystem in the platform.
Built from scratch — **no OpenAI Evals, no Anthropic Evaluation SDK, no
DeepEval, Ragas, Promptfoo, LangSmith, LangFuse, Phoenix, TruLens or DSPy.**

---

## Documents

| # | Document | What it answers |
|---|---|---|
| 1 | [EVALUATION_ARCHITECTURE.md](EVALUATION_ARCHITECTURE.md) | What the platform is — no assertions, five verdicts, the two-module boundary, fourteen subsystems, ten invariants |
| 2 | [SCENARIO_ARCHITECTURE.md](SCENARIO_ARCHITECTURE.md) | Scenarios, eight kinds, versioning and digests, capabilities, suites, the registry |
| 3 | [REPLAY_ARCHITECTURE.md](REPLAY_ARCHITECTURE.md) | Replay against a recording, the registry anchor, determinism as a precondition |
| 4 | [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md) | Brief compliance, eight defects found and fixed, eight open findings |
| 5 | [PERFORMANCE_REPORT.md](PERFORMANCE_REPORT.md) | 22 benchmarks, the clock-resolution problem, the 45× optimisation, three optimisations refused |
| 6 | [SECURITY_REVIEW.md](SECURITY_REVIEW.md) | Threat model, thirteen controls, five findings |
| 7 | [EVALUATION_REPORT.md](EVALUATION_REPORT.md) | The end-to-end run against all five frozen engines — measured |
| 8 | [BENCHMARK_REPORT.md](BENCHMARK_REPORT.md) | Every measurement, and which ones the clock can actually see |
| 9 | [REGRESSION_REPORT.md](REGRESSION_REPORT.md) | Run-over-run comparison, the false-positive check, coverage loss |
| 10 | [PLATFORM_READINESS_REPORT.md](PLATFORM_READINESS_REPORT.md) | **Consolidated verdict across 10A–10F** — strengths, risks, blockers, debt, next steps |

---

## The short version

**It does not assert.** A test suite encodes an expectation and asserts it; when
the code changes, somebody edits the expectation, and the suite ends up
asserting whatever the code currently does. This platform runs a scenario,
records an observation, and compares it against a **previously approved**
observation. There is no expected value to edit.

```
Scenario  →  Observation  →  Compare(Golden, Observation)  →  Verdict
```

**A behaviour change is a question, not a failure.** Drift files a golden
candidate and goes to a human. Only `Fail` — the scenario could not be performed
at all — blocks a release.

**A golden is never recorded automatically.** Approval requires an author and a
reason, both refused if empty. A platform that updates its own baseline reports
no drift, ever.

**The core imports nothing it evaluates.** One command proves it:

```console
$ cd packages/go/evaluation && go list -deps ./... | grep callscreen
.../packages/go/runtime
.../packages/go/evaluation
```

**Behaviour and time are separated.** Fingerprints exclude durations,
timestamps, seeds and free-text detail. Two runs of a correct system produce
identical outputs and different durations.

**The clock is measured, not assumed.** ≈520 µs granularity on this machine —
larger than 18 of 18 scenarios take. The platform labels every percentile it
cannot see rather than reporting a zero as fact.

---

## Results

| | |
|---|---|
| Scenarios | 18 across 5 subjects, 8 kinds, 3 suites |
| End-to-end run | 18/18 pass, 0 drift, 0 fail, releasable |
| Determinism | 18/18 deterministic across 3 runs |
| Replay | 18/18 approved goldens reproduced |
| Regression | 0 regressions and 0 improvements between identical runs |
| Concurrency | 192 evaluations across 16 goroutines, all pass |
| Stress | 25 full runs / 450 executions in 131 ms |
| Readiness | `ready=true` |

**One blocking item:** `-race` has never been run, against any of the seven
modules. See [PLATFORM_READINESS_REPORT.md](PLATFORM_READINESS_REPORT.md) §8.

---

## Running it

```console
# platform tests and benchmarks
cd packages/go/evaluation
go test ./...
go test -run XXX -bench=. -benchmem -benchtime=300ms .

# final platform verification against all five frozen engines
cd ../evalsubjects
go test -v .

# the consolidated readiness report
go test -run TestVerification_PlatformReadiness -v .
```

---

## Related

- [../runtime/](../runtime/) — Phase 10A
- [../conversation/](../conversation/) — Phase 10B
- [../memory/](../memory/) — Phase 10C
- [../tools/](../tools/) — Phase 10D
- [../governance/](../governance/) — Phase 10E
