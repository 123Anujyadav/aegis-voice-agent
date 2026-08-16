# Engineering Audit — Phase 10F

**Scope:** `packages/go/evaluation` (18 files, 5,992 lines) and
`packages/go/evalsubjects` (7 files, 1,458 lines).
**Coverage:** 85 tests (66 platform + 19 verification), 22 benchmarks,
2,890 lines of test code.

---

## 1. Brief compliance

| Requirement | Status | Evidence |
|---|---|---|
| No external evaluation framework | ✅ | `go list -deps` shows one first-party dependency and no third party |
| Built from scratch in Go | ✅ | 7,450 production lines, zero external modules |
| Evaluates all five frozen phases | ✅ | 18 scenarios across 5 subjects, all executing against real engines |
| Evaluates **without modifying** them | ✅ | Core `go.mod` requires only `runtime`; adapters use exported API only |
| Not a test suite | ✅ | No assertion decides a verdict; `Compare` is a pure function |
| Behaviour, correctness, determinism, latency, governance, memory, conversation, regression | ✅ | 8 scenario kinds; determinism, replay, regression, benchmark engines |
| Provider agnostic | ✅ | `Subject` is an interface; the core knows no subsystem by name |
| Final platform verification | ✅ | `evalsubjects/verification_test.go`, 19 tests |
| Consolidated readiness report | ✅ | [PLATFORM_READINESS_REPORT.md](PLATFORM_READINESS_REPORT.md) |
| No LLM prompts, telephony, fraud, business logic, UI, models | ✅ | absent; `DashboardModel` is a model, not an interface |
| Phases 10A–10E unmodified | ✅ | all five modules pass unchanged; see §4 |

---

## 2. Defects found and fixed

Eight. Every one was found by running something, not by reading it — which is
the argument for building the measurement suite rather than asserting numbers in
a document.

### F1 — Every latency measurement was zero *(blocking, fixed)*

Windows `time.Now()` has a granularity of roughly **520 µs**. Every scenario in
the library completes in less than that. Every step duration the platform
recorded was therefore `0s`, every percentile was `0s`, and every latency number
in this documentation would have been fiction.

Found by running the benchmark engine and reading the output rather than the
summary.

**Fix:** `resolution.go`. `ClockResolution()` measures the injected clock's
granularity at runtime construction. `BenchmarkResult` gained `AmortisedMean`
(total time over iterations, which *is* measurable), `Resolution` and
`BelowResolution`. Percentiles below resolution are labelled rather than
reported as fact:

```
memory.store-retrieve on memory: amortised=82.995µs n=40 p50=0s p95=0s p99=0s
  [BELOW CLOCK RESOLUTION 525.6µs — trust the amortised mean, not the percentiles]
```

**18 of 18 scenarios** fall below resolution on this machine. The platform says
so, on every line.

### F2 — Drift filed no golden candidate *(fixed)*

`AutoRecordCandidates` fired only on `VerdictNoBaseline`. Drift — the case the
whole platform exists to surface — filed nothing.

The consequence is precisely inverted from the intent: a brand-new scenario,
where nobody has an opinion yet, got a candidate; drift, where a human decision
is required *now*, left the reviewer with nothing to promote. The remedy would
have been to hand-record a golden, which is the manual path the design exists to
avoid.

**Fix:** candidate recording moved outside the switch to cover
`VerdictNoBaseline || VerdictDrift`. Found by `TestIntegration_FullCycle`.

### F3 — Approving a replacement erased what it replaced *(fixed)*

`GoldenStore.Approve` ranged over the history slice, later appended the
superseded record to `s.history[key]` — producing a **new slice header** — then
wrote the captured stale slice back. The append was silently discarded.

So approving a new baseline destroyed the record of the old one. "What did we
consider correct in March" became unanswerable the moment somebody answered it
differently in April — the one thing the history exists to keep.

**Fix:** index through the map on every access rather than through a slice
captured by `range`. Found by `TestIntegration_FullCycle`.

### F4 — The pending-golden metric was quadratic *(fixed)*

`Execute` set a gauge with `len(e.goldens.PendingApprovals())`. That function
walks every key's history and **deep-copies every pending golden — each carrying
a whole observation** — then sorts. It ran once per candidate filed.

Cost, for a 20-scenario suite:

| | Before | After |
|---|---|---|
| `BenchmarkRunSuiteSerial` | 18,479,786 ns/op | **410,283 ns/op** (45×) |
| allocated | 21,753,166 B/op | **254,077 B/op** (86×) |

Per scenario that is 924 µs against a single `Execute` at 13.4 µs — a 69×
overhead that was entirely metric bookkeeping. It grows with the pending count,
so a platform left running in CI with a review backlog gets slower the longer
nobody reviews.

**Fix:** an O(1) `pending` counter maintained on record/approve, exposed as
`PendingCount()`. `PendingApprovals()` still copies, because the operator queue
wants the records themselves — it is simply no longer on the hot path.

### F5 — A busy machine produced drift *(blocking, fixed)*

`latencyDifferences` skipped a step only when **both** the baseline and the
observation were below the latency floor. A contended run whose step crossed the
floor was therefore divided by a baseline the clock could not resolve — 6 ms
over a 525 µs sample whose true value lay anywhere between zero and a
millisecond.

Measured: **2 drift verdicts out of 192 evaluations** under sixteen-way
concurrency. Same code, same engines, different verdict because the machine was
busy.

An evaluation platform whose headline verdict moves with machine load cannot
gate a release, which is the entire point of it.

**Fix:** the floor applies to the **baseline alone**. A ratio needs a measurable
denominator. The cost — a step that was genuinely quick and is now slow goes
unreported *here* — is the intended division of labour: single observations do
not measure latency, distributions do, and that is what the benchmark engine is
for. After the fix: 192/192 pass.

### F6 — Losing a baseline was invisible to the regression gate *(fixed)*

`DetectRegressions` reported a regression when a scenario started failing, or
when its behaviour fingerprint moved. A scenario that **lost its approved
golden** did neither: its behaviour is unchanged and its verdict is
`NoBaseline`, not `Fail`.

So retiring a golden, wiping the store, or bumping a scenario version without
re-approving all produce a run that reports **"0 regressions"** while a scenario
has silently dropped out of the gate. An unevaluated scenario's fingerprint is
perfectly stable, which is exactly why a fingerprint check cannot see this.

**Fix:** `Verdict.Compared()` and a new `RegressionCoverage` kind, checked
*first* in the switch — a scenario going from `Fail` to `NoBaseline` is not "no
longer failing", it is no longer being asked. Found by
`TestVerification_RegressionDetectsARealChange`, which retires a baseline and
expects the gate to notice:

```
- coverage conversation.escalation on conversation:
    scenario lost its baseline and is no longer evaluated
```

### F7 — Regression reports were mostly noise *(fixed)*

The latency arm of `DetectRegressions` guarded only `wasTime > 0`. With total
step time quantising to zero (F1), a clean run reported fourfold
**improvements**:

```
+ improvement memory.store-retrieve on memory: faster: 2.748ms → 0s
+ improvement governance.consent-gate on governance: faster: 1.0828ms → 0s
  ... eight of them in a single verification pass
```

A report that is mostly noise is a report nobody reads, and the real regressions
go with it.

**Fix:** both sides must clear the floor, which `Coordinator.CompareRuns` now
supplies from the runtime's resolution-adjusted tolerance. After the fix a clean
run reports **0 regressions, 0 improvements**.

### F8 — One unbounded collection *(fixed)*

`MemoryStorage` bounds runs (200), observations per scenario (50) and trend
points (500). `PutBenchmark` appended without a bound — the one unbounded map in
a store whose entire stated purpose is being bounded.

Nothing failed as a result: the growth is slow and a test run is short. That is
why it survived until somebody read the file rather than ran it.

**Fix:** benchmarks share the observation bound.
`TestIntegration_BenchmarkStorageIsBounded` covers it.

---

## 3. Open findings

### A1 — Sixth copy of the metrics primitives *(medium)*

`runtime.Metrics` has unexported constructors, so it is closed for extension.
Every phase since 10A has therefore carried its own counter, gauge and histogram
plumbing. This is the **sixth** copy, and the fourth consecutive phase to name
it as the first handover item.

The cost is not the duplication — it is that six independent implementations of
a histogram will not agree on bucket boundaries, and a cross-subsystem latency
dashboard built on them will be quietly wrong.

**Recommendation:** before Phase 11, export a constructor from
`packages/go/runtime` and collapse all six. This is an additive change to a
frozen phase and requires an explicit decision.

### A2 — `-race` has never been run *(BLOCKING)*

The race detector requires cgo, and this machine has no C toolchain. It has now
never been run against **seven modules**, including this platform's copy-on-write
registry, its golden store, its shared metrics and the concurrency exercises in
§5 below.

The concurrency tests pass. That is weaker evidence than it looks: without
`-race` they demonstrate that no race was severe enough to corrupt a verdict in
these runs, not that no race exists.

**This is the single blocking item for the platform.** It is an infrastructure
gap, not a code defect, and it is cheap to close:

```console
go test -race ./...    # on any Linux or macOS CI runner
```

### A3 — Three canonical value encoders *(low)*

Phase 10D's `Value`, Phase 10E's `Attr` and Phase 10F's `Value` each implement
canonical encoding with sorted map keys, for the same reason — stable
fingerprints. Three implementations, three chances for one of them to sort
differently.

**Recommendation:** a shared `packages/go/canonical` before a fourth appears.

### A4 — No durable storage implementation *(medium)*

`Storage` is an interface; `MemoryStorage` is the only implementation. Goldens
live in memory and are lost on restart.

For a **permanent evaluation infrastructure** — the brief's words — this is the
largest functional gap. Everything needed to close it is in place: the interface
is defined, the engines use it, the bounds are explicit, and
`TrendPoint` deliberately carries no observation so a durable trend table stays
small. What is missing is an Aurora-backed implementation and a migration.

**Not a defect** — it is Phase 11 scope — but it must not be mistaken for done.

### A5 — Parallel suite execution is currently a pessimisation *(low)*

With F4 fixed, per-scenario cost dropped to roughly 20 µs, and goroutine
coordination plus lock contention on the shared golden store now costs more than
it saves:

```
BenchmarkRunSuiteSerial-16     410,283 ns/op
BenchmarkRunSuiteParallel-16   488,288 ns/op    (19% slower)
```

This is not wrong — it is a threshold effect. Parallel execution pays when
subjects are slow, which is what real adapters against loaded engines or
network-bound subsystems will be. It does not pay for 20 µs in-process
scenarios.

**Recommendation:** keep the capability; do not default suites to parallel;
revisit when a subject exceeds ~1 ms per scenario.

### A6 — The scenario library is small *(medium)*

18 scenarios is enough to verify the platform. It is **not** enough to gate a
release of the five engines. Governance has 5 scenarios against 9 scopes and 10
outcomes; conversation has 2.

The platform reports this honestly — `Coverage`, `UncoveredKinds()` and
`UnevaluatedSubjects` all exist for it — but a green readiness report should be
read as "the 18 questions we asked were answered as approved", not as "the
platform is correct".

**Recommendation:** treat scenario-library growth as ongoing work owned by each
subsystem's team, not as a Phase 10F deliverable.

### A7 — Observations are unbounded in size *(low, see Security Review)*

Nothing caps how much a subject may return in a `Value`. A subject returning a
large output grows the observation, the golden and the stored history linearly.
Adapter-side discipline currently prevents this; the platform does not enforce
it. See [SECURITY_REVIEW.md](SECURITY_REVIEW.md) §4.

### A8 — Benchmarks measured on one machine *(low)*

Every number in [BENCHMARK_REPORT.md](BENCHMARK_REPORT.md) comes from a single
11th Gen Intel Core i7-11800H running Windows. The clock-resolution work makes
the numbers *honest* on this machine; it does not make them representative.
Linux `time.Now()` resolution is roughly three orders of magnitude finer, so the
"below resolution" labelling will largely disappear there and the percentiles
will become meaningful.

---

## 4. The frozen phases were not modified

Phases 10A–10E are approved and frozen. This phase changed **nothing** in them.

Verified — all seven modules, `go vet` clean, `-count=1 -shuffle=on`:

```
runtime       ok    0.970s
conversation  ok    0.832s
memory        ok    0.788s
toolruntime   ok    0.922s
governance    ok    0.830s
evaluation    ok    0.723s
evalsubjects  ok    1.125s
```

No additive fix to a frozen phase was required. Every adapter reached what it
needed through exported API — in most cases each phase's exported test harness.
The one place this was tight is worth recording: several adapters can only
observe a subsystem through its harness, so a phase that had not exported one
would not be evaluable without modification. Phases 10A–10E all did.

---

## 5. Concurrency

| Exercise | Result |
|---|---|
| 16 goroutines × 12 evaluations on one runtime | 192/192 pass |
| 200 evaluations against 40 concurrent registry publications | no failure; ended at version 42, 58 scenarios |
| 25 full runs = 450 scenario executions | 131 ms, storage bounded at 25 runs / 468 observations / 450 trend points |
| Full suite at `-count=5 -shuffle=on` | pass |

Structural properties supporting these:

- **No package-level mutable state.** Two runtimes in one process share nothing
  — separate registry, golden store, storage and metrics. This is what lets the
  verification file run `t.Parallel()` without one test's goldens deciding
  another's verdicts.
- **Copy-on-write registry** with an atomic snapshot pointer; readers never
  block writers.
- **`Compare` is pure** — no shared state, so verdict computation cannot race.

Caveat: see A2. Without `-race`, these results are evidence, not proof.

---

## 6. Test inventory

| Suite | File | Tests | Covers |
|---|---|---|---|
| Platform unit | `evaluation/platform_test.go` | 47 | values, observations, scenarios, goldens, comparison, registry, storage, failure, resolution |
| Platform integration | `evaluation/integration_test.go` | 19 | full cycle, drift/approval workflow, determinism, replay, regression, dashboard, readiness, bounded storage, concurrency, stress |
| Benchmarks | `evaluation/bench_test.go` | 22 | execution, fingerprinting, comparison, registry, regression, reporting, storage |
| Final verification | `evalsubjects/verification_test.go` | 19 | end-to-end against all five real engines, coverage, gating suites, drift filing, determinism, replay, regression, benchmarks, concurrency, registry churn, stress, failure injection, capability declaration, readiness, dashboard, metrics |

---

## 7. Verdict

The platform does what the brief asks. It evaluates five frozen subsystems
without importing them, it reaches its verdicts by comparison against approved
recordings rather than by assertion, and it reports what it cannot measure
instead of reporting a number it made up.

Eight defects were found and fixed during this phase. Five of them — F1, F4, F5,
F6, F7 — were defects in the platform's *judgement*: it would have reported
numbers that were zero, verdicts that moved with machine load, and gates that
stayed green while coverage disappeared. Those are the failure modes that matter
for a tool whose only output is a judgement, and finding them required running
it under load against real engines rather than reading it.

**One blocking item remains: A2.** The race detector has never been run against
any of the seven modules.
