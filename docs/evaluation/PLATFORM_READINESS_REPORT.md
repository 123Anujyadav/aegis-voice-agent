# Platform Readiness Report

**Consolidated verdict across Phases 10A – 10F**
**Produced by:** `TestVerification_PlatformReadiness`, end-to-end against all
five frozen engines.

---

## 1. Machine verdict

```
platform readiness from run run_ddffa5gmy6o3yaaaadsnd3h75m: ready=true

  conversation    2 scenarios:  2 pass  0 drift  0 fail  0 skip  0 new | pass=100% coverage=100%
  governance      5 scenarios:  5 pass  0 drift  0 fail  0 skip  0 new | pass=100% coverage=100%
  memory          4 scenarios:  4 pass  0 drift  0 fail  0 skip  0 new | pass=100% coverage=100%
  runtime         3 scenarios:  3 pass  0 drift  0 fail  0 skip  0 new | pass=100% coverage=100%
  toolruntime     4 scenarios:  4 pass  0 drift  0 fail  0 skip  0 new | pass=100% coverage=100%

  conversation.escalation on conversation:   deterministic across 3 runs (890289e4d7eed883)
  governance.baseline-decides on governance: deterministic across 3 runs (308ffc4fcde44b13)
  memory.forget-is-complete on memory:       deterministic across 3 runs (a42c5863a1e7dd7b)
  runtime.breaker-opens on runtime:          deterministic across 3 runs (8a6de857d2c25471)
  tool.execute on toolruntime:               deterministic across 3 runs (8bf7fc0af6b4d34a)
```

No blockers, no unevaluated subsystems, no uncovered scenario kinds, every
subject deterministic.

**`ready=true` is the platform's verdict on the 18 questions it asked.** It is
not a statement that the system is ready for production traffic. §7 lists what
is.

---

## 2. What exists

| Phase | Module | Lines | Tests | Benchmarks |
|---|---|---:|---:|---:|
| 10A Runtime Core | `runtime` | 6,382 | 53 | 21 |
| 10B Conversation Engine | `conversation` | 5,495 | 70 | 18 |
| 10C Memory Engine | `memory` | 5,524 | 77 | 21 |
| 10D Tool Runtime | `toolruntime` | 8,788 | 87 | 22 |
| 10E Governance Engine | `governance` | 7,337 | 82 | 20 |
| 10F Evaluation Platform | `evaluation` | 5,992 | 66 | 22 |
| 10F Adapters | `evalsubjects` | 1,458 | 19 | — |
| **Total** | | **40,976** | **454** | **124** |

All seven modules: `go vet` clean, `go test -count=1 -shuffle=on` passing.
**Zero external dependencies** across the entire platform.

---

## 3. Architecture strengths

**One door per concern, enforced by the dependency graph rather than by
convention.** Governance imports nothing it governs; the evaluation core imports
nothing it evaluates. Both claims are checkable with `go list -deps`. This is
what makes "no subsystem may bypass the policy engine" auditable in review
instead of aspirational.

**Default deny, including in the zero value.** Phase 10E's `Outcome` zero value
denies, so a dropped decision refuses rather than permits. The same instinct
runs through the platform: unset means refuse, not "unspecified".

**Policies and scenarios are data; evaluation is a pure function.**
`Engine.Decide` and `Compare` are both pure. Determinism, replay, explainability
and infrastructure-free testing all fall out of that single property, and every
one of them would have needed separate machinery otherwise.

**Content never enters long-lived records.** Frozen invariant I7 — events carry
identifiers and fingerprints, never content, because Kafka cannot delete a
record — is honoured in Phase 10F's trend history too, which is the platform's
own long-lived table.

**The clock is injected everywhere and measured where it matters.** `rt.Clock`
throughout, `rt.FakeClock` in every harness, no test sleeping on real time — and
in 10F the clock's *resolution* is measured rather than assumed, which is what
turned a page of fabricated latency numbers into honest ones.

**Copy-on-write registries with atomic snapshot pointers**, in 10D, 10E and 10F.
Readers never block writers, and a reader finishes against a coherent snapshot.
Verified at 200 evaluations against 40 concurrent publications.

**Every phase's evaluation suite caught defects in that phase's own
documentation.** This is the strongest structural result of the programme: 10C's
suite corrected three documents about context-scope behaviour, 10D's found a
120-second hang, 10E's found that every emergency override was a no-op, and
10F's found that every latency number it was about to publish was zero. Building
the measurement before writing the claim is what produced that, repeatedly.

---

## 4. Engineering risks

**R1 — Scenario coverage is thin relative to what it gates.** *(high)*
18 scenarios against five substantial subsystems. Governance has 5 against 9
scopes and 10 outcomes; conversation has 2. A green readiness report means the
18 questions asked were answered as approved. Growing this is ongoing work per
subsystem, not a Phase 10F deliverable.

**R2 — Baselines are one session old.** *(medium)*
Every golden was approved during the verification run itself, from current
behaviour. The mechanism is proven; the baselines have no history. Regression
value compounds only once they are months old.

**R3 — Adapters can only see what a phase exported.** *(medium)*
Several adapters observe a subsystem exclusively through the test harness that
phase exported. A property not visible through exported API cannot be
scenario-checked — that is a fact about the phase, recorded rather than worked
around. All five phases happened to export a harness; a future phase that does
not would be unevaluable without modification.

**R4 — Six copies of the metrics primitives.** *(medium)*
`runtime.Metrics` has unexported constructors and is closed for extension, so
every phase since 10A carries its own counter, gauge and histogram. The risk is
not duplication — it is that six independent histogram implementations will not
agree on bucket boundaries, and a cross-subsystem latency dashboard built on
them will be quietly wrong.

**R5 — Three canonical value encoders.** *(low)*
10D's `Value`, 10E's `Attr`, 10F's `Value`, each independently implementing
sorted-key canonical encoding for stable fingerprints. Three chances for one to
sort differently.

---

## 5. Performance risks

**P1 — Metrics that walk collections.** *(pattern, twice observed)*
10D's ledger and 10F's pending-golden gauge were both O(n) reads on a hot path,
and 10F's cost 45× in throughput and 86× in allocation. Gauges over unbounded
sets need incremental counters. This has now happened twice and should be a
review checklist item, not a lesson learned twice more.

**P2 — All measurements come from one machine.** *(medium)*
Every number in this programme is windows/amd64 on an i7-11800H. The 520 µs
clock granularity that made 18 of 18 scenarios unmeasurable individually is a
Windows characteristic; Linux is roughly three orders of magnitude finer. The
numbers are honest about what they are, but they are not representative.

**P3 — Nothing reflects durable storage.** *(high for Phase 11)*
A 50 ns in-memory golden lookup becomes a network round trip. No figure in any
performance document survives that change.

**P4 — Parallel suite execution is currently a pessimisation.** *(low)*
488 µs parallel against 410 µs serial for 20 scenarios. A threshold effect that
reverses once subjects cost more than ~1 ms — which real adapters against loaded
engines will. Keep the capability, do not default to it.

---

## 6. Concurrency risks

**C1 — `-race` has never been run against any module.** *(BLOCKING)*
See §8.

**C2 — Concurrency evidence is behavioural, not analytical.** *(high)*
What has been demonstrated: 192 evaluations across 16 goroutines all passing;
200 evaluations against 40 concurrent registry publications; 25 full runs / 450
executions in 131 ms; full suites at `-count=5 -shuffle=on`. What that shows is
that no race was severe enough to corrupt a verdict **in those runs**. It is not
proof of absence, and stating it as such would be the kind of overclaim this
platform exists to prevent.

**C3 — A race in the golden store is a wrong verdict, not an outage.** *(high)*
Worth separating from ordinary concurrency risk. The platform's only output is a
judgement that is trusted without further checking. Corruption there is silent
and consequential in a way a crash is not.

**Structural mitigations in place:** no package-level mutable state anywhere in
10F (two runtimes in one process share nothing); copy-on-write registries;
`Compare` pure; every shared map behind an `RWMutex`; bounded storage everywhere.

---

## 7. Security risks

**S1 — Recordings may contain personal data, and no rule says they must not.**
*(medium; blocking for durable storage)*
The memory engine handles personal data by design, and its adapter surfaces
retrieved values into observations and goldens. Goldens are kept indefinitely.
**ADR-0012's 90-day retention rule governs transcripts, memory and governance
records — it has no counterpart in the evaluation platform.** Currently
theoretical: adapters use synthetic fixtures and storage dies with the process.
It stops being theoretical the moment durable storage lands.

**S2 — Observations are unbounded in size.** *(medium)*
Phase 10C caps memory records and refuses oversized ones (INV-MEM-8). The
evaluation platform has no equivalent. Adapter discipline is the only thing
preventing unbounded growth of observations, their clones and their goldens.

**S3 — Approval attribution is not authentication.** *(low, by design)*
`Approve` takes an author string and trusts it. Correct for an in-process
library, wrong for a service. Documented so nobody later mistakes a recorded
name for a verified one.

**S4 — No secret handling anywhere in the evaluation path.** *(informational)*
The platform has no listener, no credentials, no network and no external input.
Its realistic adversary is a well-meaning engineer under deadline pressure,
which is why the controls are shaped as "make the wrong thing unavailable"
rather than "detect the intruder".

Full analysis: [SECURITY_REVIEW.md](SECURITY_REVIEW.md).

---

## 8. Production blockers

### B1 — `-race` has never been run *(BLOCKING, all seven modules)*

The race detector requires cgo and this machine has no C toolchain. Seven
modules, 40,976 lines, 454 tests, extensive concurrent code — none of it checked
by the race detector.

This is an **infrastructure gap, not a code defect**, and it is cheap to close:

```console
go test -race ./...    # on any Linux or macOS CI runner
```

**Nothing should ship until this has been run once and its output read.** It has
been the first handover item for four consecutive phases and it is the one item
on this list that cannot be reasoned around.

### B2 — No durable storage *(BLOCKING for "permanent evaluation infrastructure")*

`Storage` is an interface; `MemoryStorage` is the only implementation. **Goldens
are lost on restart.** The brief calls for permanent evaluation infrastructure,
and a baseline store that does not survive a process restart is not that.

Everything needed is in place — the interface is defined, engines use it, bounds
are explicit, `TrendPoint` is deliberately content-free so a durable trend table
stays small. What is missing is an Aurora-backed implementation and a migration.
This is Phase 11 scope, but it must not be mistaken for done.

### B3 — Retention rule for evaluation recordings *(blocking for B2)*

S1. Must be decided **before** durable storage ships, not after.

---

## 9. Technical debt

| # | Debt | Cost of leaving it |
|---|---|---|
| D1 | Six copies of counter/gauge/histogram | Cross-subsystem metrics that silently disagree |
| D2 | Three canonical value encoders | One sorts differently; fingerprints diverge silently |
| D3 | No durable `Storage` implementation | Goldens do not survive restart |
| D4 | Scenario library at 18 | Green gate over thin coverage |
| D5 | No observation size cap | Unbounded growth of stored recordings |
| D6 | Single-platform measurements | Latency documentation not portable |
| D7 | `RegressionReport` and `Comparison` both compute latency thresholds | Two places to keep consistent; already caused F5/F7 as a matched pair |

D1 and D2 are the two that compound: each new phase adds a seventh copy and a
fourth encoder, and each is a place where a subtle disagreement produces wrong
numbers rather than an error.

---

## 10. Recommendations before Phase 11

**In order.**

1. **Run `go test -race ./...` on a Linux runner.** Read the output. Fix
   whatever it reports. Nothing else on this list matters as much, and it is
   hours of work, not days. *(B1)*

2. **Decide the retention rule for evaluation recordings** and align it with
   ADR-0012 — before durable storage exists, while the decision is still cheap.
   *(B3, S1)*

3. **Export metric constructors from `packages/go/runtime` and collapse the six
   copies.** This is an additive change to a frozen phase and needs an explicit
   approval. Doing it now costs one migration; doing it after Phase 11 costs
   seven. *(D1)*

4. **Extract `packages/go/canonical`** and move 10D, 10E and 10F onto it before
   a fourth encoder appears. *(D2)*

5. **Build the durable `Storage` implementation** with the retention rule from
   (2) already in it. *(B2)*

6. **Add an observation size cap**, following Phase 10C's INV-MEM-8 precedent
   rather than inventing a new one. *(D5, S2)*

7. **Assign scenario-library growth to each subsystem's owner.** Governance and
   conversation are the thinnest. Treat coverage as a standing obligation, not a
   phase deliverable. *(D4, R1)*

8. **Run the benchmark suite on Linux** and republish the latency figures. Most
   of the `BELOW CLOCK RESOLUTION` labelling should disappear and the
   percentiles become meaningful for the first time. *(D6, P2)*

---

## 11. Readiness statement

**The evaluation platform is complete and working.** It evaluates five frozen
subsystems without importing them, reaches verdicts by comparison against
approved recordings rather than assertion, and reports what it cannot measure
instead of inventing a number. Eighteen scenarios execute against the real
engines; all reproduce their baselines; all are deterministic; all replay.

**The platform is not ready for production traffic**, and the gap is not in the
code. Three things stand between here and that: the race detector has never
been run, evaluation state does not survive a restart, and no retention rule
governs what the platform records. Two of the three are decisions rather than
implementations.

**Recommendation: approve Phase 10F. Do not begin Phase 11 until B1 is
closed** — it costs hours, it has been outstanding for four phases, and every
concurrency claim in 40,976 lines of code currently rests on the absence of
observed symptoms rather than on evidence.

---

## 12. Related

- [EVALUATION_ARCHITECTURE.md](EVALUATION_ARCHITECTURE.md)
- [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md)
- [SECURITY_REVIEW.md](SECURITY_REVIEW.md)
- [PERFORMANCE_REPORT.md](PERFORMANCE_REPORT.md)
- [EVALUATION_REPORT.md](EVALUATION_REPORT.md)
