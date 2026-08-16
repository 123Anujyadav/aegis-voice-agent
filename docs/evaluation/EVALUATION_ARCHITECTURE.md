# Evaluation Architecture

**Phase 10F** · `packages/go/evaluation` + `packages/go/evalsubjects`

---

## 1. What this is

A permanent evaluation infrastructure for the five frozen phases. Not a test
suite — the distinction is the whole design and is worth stating precisely.

A test suite encodes an expectation and asserts it:

```go
if got != "escalate" {
    t.Fatal("expected escalation")
}
```

Somebody wrote `"escalate"`. When the engine changes, that line fails, and the
person who changed the engine edits the line. The expectation and the code drift
together, and the suite ends up asserting whatever the code currently does.

This platform does not assert. **There is not one assertion in it.** It runs a
scenario, records an observation, and compares that observation against a
previously **approved** one:

```
Scenario  →  Observation  →  Compare(Golden, Observation)  →  Verdict
```

A behaviour change produces **drift**, which is a question for a human, not a
failure. Nobody can make drift go away by editing an expected value, because
there is no expected value to edit — there is an approved recording, and
replacing it requires an author and a reason.

---

## 2. The dependency boundary

The brief requires the platform to evaluate five subsystems **without modifying
them**. "Without modifying" is easy to claim and hard to check, so the
architecture makes it checkable with one command.

Two Go modules:

| Module | Imports | Contains |
|---|---|---|
| `packages/go/evaluation` | `runtime` only | The platform. 18 files, 5,982 lines. |
| `packages/go/evalsubjects` | all five phases | The five adapters. 7 files, 1,458 lines. |

**The platform core imports nothing it evaluates.** Verified:

```console
$ cd packages/go/evaluation && go list -deps ./... | grep callscreen
github.com/callscreen/callscreen-platform/packages/go/runtime
github.com/callscreen/callscreen-platform/packages/go/evaluation
```

Conversation, memory, tool runtime and governance do not appear. They cannot: a
module cannot import what its `go.mod` does not require, and the core's `go.mod`
has exactly one `require` line.

This matters beyond tidiness. A platform that imported the engines could acquire
a special case per engine — `if subject == "memory" { ... }` — and each such
case is a place where the platform's answer depends on who is being evaluated.
The boundary makes that impossible rather than discouraged.

The adapters go the other way: `evalsubjects` imports all five, and uses **only
exported API** — in most cases each phase's exported test harness, which those
phases introduced saying that services embedding the engine would need it. This
is that need arriving. No build tags, no reflection, no `go:linkname`, no
unexported access. Where an adapter cannot observe something through the public
API, that is recorded as a finding about the phase rather than worked around.

---

## 3. The fourteen subsystems

| # | Subsystem | File | What it owns |
|---|---|---|---|
| 1 | Evaluation Runtime | `runtime.go` | `Execute`, `RunSuite`, `RunAll`; the one path a scenario travels |
| 2 | Evaluation Registry | `registry.go` | Copy-on-write scenario and suite registry, versioned and fingerprinted |
| 3 | Scenario Engine | `scenario.go` | `Scenario`, 8 kinds, steps, digests, versioning |
| 4 | Golden Framework | `golden.go` | Candidates, approval, supersession, history |
| 5 | Comparison | `compare.go` | `Compare`, 5 verdicts, tolerances, differences |
| 6 | Observation model | `observation.go` | The behaviour/time split; `BehaviourPrint`, `Timings` |
| 7 | Subject model | `subject.go` | `Subject`, `Session`, `Step`, `StepResult`, capabilities |
| 8 | Value model | `value.go` | Canonical, sorted-key encoding for stable fingerprints |
| 9 | Determinism Engine | `engines.go` | `CheckDeterminism` — same input, same behaviour |
| 10 | Replay Engine | `engines.go` | `Replay`, `ReplayLatest` — against a recording, not a baseline |
| 11 | Regression Engine | `engines.go` | `DetectRegressions` — run against run |
| 12 | Benchmark Framework | `engines.go`, `resolution.go` | Distributions, amortised means, clock-resolution honesty |
| 13 | Failure Injection | `failure.go` | 7 failure kinds, declared capabilities |
| 14 | Reporting Engine | `report.go` | Scorecards, dashboard model, `PlatformReadiness` |

Supporting: `metrics.go`, `storage.go`, `ids.go`, `errors.go`, `harness.go`,
`doc.go`.

---

## 4. Five verdicts, not two

Pass/fail is the wrong shape for this problem. A binary verdict forces two
genuinely different situations into the same bucket and loses the distinction
that matters.

| Verdict | Meaning | Blocks a release |
|---|---|---|
| `Pass` | Matched the approved golden | no |
| `Drift` | Behaved differently from the approved golden | **no** |
| `Fail` | The scenario could not be performed at all | **yes** |
| `NoBaseline` | No approved golden exists yet | no |
| `Skipped` | The subject lacks a required capability | no |

**Only `Fail` blocks.** Drift does not, and that is deliberate. Drift means the
platform noticed a change; whether the change is acceptable is a judgement, and
a platform that blocked on every judgement would be routed around within a
month. Drift files a golden candidate and shows up in the review queue.

`Fail` is reserved for "the scenario could not be performed": the adapter broke,
the subject panicked, the scenario timed out. That is not a judgement call.

`Skipped` exists so a subject that cannot do something says so rather than
failing. A scenario requiring `inject:governance` against a subject that does
not declare it is skipped with the missing capability named — because a silent
pass would be indistinguishable from working coverage.

---

## 5. Behaviour and time are separated

`Observation.BehaviourPrint()` fingerprints **what the subsystem did**:

- included: step names and operations, outputs, outcome codes, failure flags,
  state, event types and fields, the scenario version
- excluded, deliberately: **durations, timestamps, seeds, free-text detail**

`Observation.Timings()` carries durations, separately.

Without this split, determinism is unachievable. Two runs of a correct system
produce identical outputs and different durations; a system producing identical
durations would be a system with no clock. Folding time into the behaviour
fingerprint would make every determinism check fail on a busy machine and teach
everyone to ignore the platform.

Free-text detail is excluded for a related reason: improving an error message
should not be drift.

---

## 6. A golden is never recorded automatically

`GoldenStore.Record` produces a **candidate**. Promotion requires
`GoldenStore.Approve` with an author and a reason, both refused if empty.

The alternative — a platform that updates its own baseline when it sees a change
— is a platform that reports no drift, ever. That is the classic golden-file
failure mode and it is the worst thing an evaluation platform can do, because it
converts a silent regression into a silent regression with a green dashboard.

`AutoRecordCandidates` (on by default) files a candidate on `NoBaseline` **and**
on `Drift`. It never approves. Covering drift matters more than covering
no-baseline: filing for a brand-new scenario is low stakes, but filing for drift
puts a concrete artifact in front of the reviewer at exactly the moment their
decision is required.

History is retained. A superseded golden is kept, because "what did we consider
correct in March" is a question a regression investigation asks.

---

## 7. The clock is measured, not assumed

`ClockResolution()` measures the injected clock's granularity at runtime
construction. On the development machine it measures **≈520–526 µs** — Windows
`time.Now()` granularity — which is larger than most scenarios take.

Every latency-sensitive decision is then taken relative to that measurement:

- the comparison latency floor is raised to `10 × resolution`
- benchmark results carry `AmortisedMean`, `Resolution` and `BelowResolution`
- percentiles below resolution are labelled `BELOW CLOCK RESOLUTION` rather than
  reported as though they meant something

This is not a portability nicety. Before it existed, every step duration in the
platform measured `0s` and every latency number in the documentation would have
been fiction. See ENGINEERING_AUDIT F1.

---

## 8. What this platform does not do

Excluded by the brief, and absent from the code:

- LLM prompts, prompt templates, model calls
- telephony intelligence, fraud detection, business logic
- an actual UI — `DashboardModel` is a model an interface would render, nothing
  more
- actual AI models

Excluded by choice:

- **no external evaluation framework.** No OpenAI Evals, Anthropic Evaluation
  SDK, DeepEval, Ragas, Promptfoo, LangSmith, LangFuse, Phoenix, TruLens or DSPy
  Evaluation. The platform's only dependency is `packages/go/runtime`.
- **no scoring model.** Nothing here judges whether an answer is *good*. It
  judges whether behaviour matches what a human approved. Quality is a human
  input to this system, not an output of it.

---

## 9. Invariants

| ID | Invariant | Enforced by |
|---|---|---|
| INV-EVAL-1 | The core imports nothing it evaluates | `go.mod`, checkable with `go list -deps` |
| INV-EVAL-2 | No assertion decides a verdict | `Compare` is a pure function of golden and observation |
| INV-EVAL-3 | A golden is never approved without an author and a reason | `GoldenStore.Approve` |
| INV-EVAL-4 | An observation is only recorded under its own scenario | `GoldenStore.Record` |
| INV-EVAL-5 | Behaviour fingerprints exclude time | `Observation.BehaviourPrint` |
| INV-EVAL-6 | Only `Fail` blocks a release | `Verdict.Blocking` |
| INV-EVAL-7 | A latency ratio requires a measurable denominator | `latencyDifferences`, `DetectRegressions` |
| INV-EVAL-8 | Every stored collection is bounded | `MemoryStorage` |
| INV-EVAL-9 | Losing a baseline is reported, not ignored | `Verdict.Compared`, `RegressionCoverage` |
| INV-EVAL-10 | Two runtimes share nothing | no package-level mutable state |

---

## 10. Related

- [SCENARIO_ARCHITECTURE.md](SCENARIO_ARCHITECTURE.md)
- [REPLAY_ARCHITECTURE.md](REPLAY_ARCHITECTURE.md)
- [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md)
- [PLATFORM_READINESS_REPORT.md](PLATFORM_READINESS_REPORT.md)
