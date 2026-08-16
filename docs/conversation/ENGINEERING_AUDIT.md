# Conversation Intelligence Engine — Engineering Audit

**Phase 10B** · `packages/go/conversation` · Audited 2026-08-09

What was built, what was verified, what was found, and what remains open.
Written to be useful in review, not to look good.

---

## 1 · Verification status

| Gate | Result | Evidence |
|---|---|---|
| Compiles standalone | ✅ | `GOWORK=off go build` |
| Compiles in workspace | ✅ | added to `go.work`; 10A and platform still build |
| `go vet` | ✅ clean | no findings |
| `gofmt` | ✅ clean | `gofmt -l` empty |
| Unit tests | ✅ **47 passing** | `conversation_test.go` |
| Simulation & integration | ✅ **23 passing** | `simulation_test.go` |
| Repeat runs | ✅ `-count=10 -shuffle=on` | no flakes |
| Benchmarks | ✅ 18 run clean | `bench_test.go` |
| **Race detector** | ❌ **NOT RUN** | §A2 — blocking |
| Phase 10A untouched | ✅ | no file under `packages/go/runtime` modified |
| Dependency count | ✅ **one, first-party** | `runtime` only |

**70 tests. 7,387 lines. One blocking gap.**

---

## 2 · Open findings

### A1 — `runtime.Metrics` is closed for extension · **Severity: Medium**

Phase 10A exports `Counter`, `Gauge` and `Histogram` but keeps their
constructors unexported (`m.counter`, `m.gauge`, `m.histogram` are methods on an
unexported receiver path). A downstream module therefore **cannot register an
instrument into a 10A instrument set**, and this module carries a near-identical
implementation — roughly 380 lines duplicated.

This is a genuine API gap in 10A, discovered by being the first consumer.

**Consequences today:** two instrument sets per process, two `Snapshot()` calls
for an exporter to gather, and a real risk of the two drifting.

**Owner:** Platform.
**Action:** additive change to 10A — an exported `Metrics.NewCounter(name,
labels...)` and equivalents. At that point this file collapses to a thin
binding. It is additive and does not alter frozen behaviour, so it needs a minor
version rather than a superseding ADR.

### A2 — Race detector not run · **Severity: HIGH, blocking approval**

`go test -race` requires cgo and there is no C toolchain on the development
machine. This module is heavily concurrent: a sharded `sync.Map` of
conversations, per-conversation mutexes, atomic metric series, copy-on-write
registries, and a stress test that hammers one conversation from 64 goroutines.

`-count=10 -shuffle=on` found no flakes, but that is a much weaker signal. The
race detector finds unsynchronised access that has **not yet manifested** —
exactly the class that first appears in production.

Identical to Phase 10A finding A2, and it remains open there.

**Owner:** Platform.
**Action:** run `-race` in CI (Linux, cgo available) for both modules before
either is approved. Treat any finding as blocking.

### A3 — `Conversation.Handle` is the largest function in the module · **Severity: Low**

`handleUtterance` interleaves latency staging, intent resolution, context
writes, clarification reservation, the plan/policy re-plan loop, and the final
transition. It is cohesive and heavily commented, and it is still the place a
subtle bug is most likely to hide.

**Action:** consider extracting the plan/policy loop into a named method once a
second caller exists. Not worth doing speculatively — the extraction would
currently have one caller and would obscure the sequence.

### A4 — Context bounds are unvalidated against real traffic · **Severity: Low**

`MaxEntriesPerScope` (256) and `DefaultTTL` (10 min) are reasoned, not measured.
A 40-turn business call has never been profiled for context growth.

**Action:** profile a realistic long conversation before raising the bound. The
eviction path is exercised by test but its *rate* in production is unknown.

### A5 — Policy rule set is small and platform-specific · **Severity: Low, by design**

Nine built-in rules. A real deployment will add business rules, and the
extension point (`PolicyEngine.Add`) has no rule-count ceiling, no cycle
detection and no cost bound. A pathological rule set could make evaluation slow
on a path that is deliberately never skipped.

**Action:** if third-party rules are ever accepted, bound rule count and add a
per-rule timeout. Not needed while rules are first-party and reviewed.

### A6 — `ScriptedClassifier` is the only `IntentClassifier` · **Severity: None — required**

Stated so nobody mistakes it for an omission. The brief specifies the intent
engine as **framework only**, so shipping a classifier would breach scope. The
scripted one exists solely for test and is documented as such at its
declaration.

---

## 3 · Findings found and fixed during this phase

How a defect was found is evidence about the test suite.

### F1 — Confident contradictions were silently acted on · **Found by: integration test**

`Planner.decide` gated all clarification on `in.Verdict != IntentAccept`. A
contradiction, however, is **precisely the case where the utterance classified
confidently and conflicts with what we already know** — a caller who says "make
it Tuesday" after establishing Monday. The planner skipped clarification and
responded as though the change were unambiguous.

The same flaw applied to `ClarifyIncomplete`: a truncated utterance can classify
confidently on its fragment and still not mean what the fragment says.

**Fix:** `alwaysClarify` treats `ClarifyContradiction` and `ClarifyIncomplete`
as confidence-independent.

**Why it matters beyond the bug:** the clarification engine correctly *detected*
the contradiction; the planner discarded the detection. A subsystem being right
is not enough if its consumer filters the result away, and only an end-to-end
test could see it.

*Test:* `TestFailure_ContradictionTriggersConfirmation`

### F2 — The deterministic fast path was 6.6× slower than what it bypassed · **Found by: benchmark**

`classifyYesNo` built two map literals per call: 1,395 ns and 15 allocations
against `IntentResolve`'s 229 ns and 2. The function exists to be *faster* than
classification and was substantially slower.

**Fix:** a `switch` on the token. 388.9 ns, −72%.

Invisible to every test — the behaviour was correct throughout. Only the
benchmark could see it.

### F3 — Sharing the persona registry required copy-on-write · **Found by: reasoning about the fix in F2's neighbourhood**

Moving `BuiltinPersonas()` from per-conversation to per-engine shares a map
across every concurrent conversation. `PersonaRuntime.Register` wrote to it in
place, so a single conversation registering a bespoke persona would have
silently redefined it for every other live call — a data race and a correctness
failure together.

**Fix:** `Register` copies before writing. The copy is paid only by the rare
caller that registers, never on the conversation path.

**Worth noting:** the performance change introduced the hazard, and the hazard
was not caught by a test — it was caught by asking what sharing meant. Phase
10A's F3 had the same shape (an optimisation introducing a deadlock). Two for
two: **every optimisation in this codebase so far has introduced a defect**, and
that is an argument for the benchmark-then-reason discipline rather than against
optimising.

---

## 4 · Requirements conformance

| Requirement | Status | Evidence |
|---|---|---|
| No agent framework | ✅ | one dependency, first-party |
| Built from scratch | ✅ | stdlib + `runtime` |
| Production Ready | ⚠️ | blocked on A2 |
| Thread Safe | ⚠️ | designed for it, **unverified without `-race`** |
| Deterministic State Machine | ✅ | `TestSim_DeterministicReplay` — 16 runs, identical traces |
| Event Driven | ✅ | `Handle(Event) → Plan`; nothing polls |
| Provider Agnostic | ✅ | no vendor import; `IntentClassifier` is the only seam |
| Streaming First | ✅ | inherits 10A streaming; the engine is per-turn and never batches |
| No Hidden State | ✅ | `transition` is the sole writer; every change appears in the trace |
| No Global Mutable State | ✅ | engine-scoped registries; no package-level mutable var |
| No Framework Lock-In | ✅ | every seam is an interface defined here |

**Two ⚠️, both honest.** "Production ready" and "thread safe" cannot be claimed
while the race detector has not run.

---

## 5 · Scope conformance

The brief's STOP list. Verified absent by inspection of every file:

| Forbidden | Verified |
|---|---|
| LLM prompts | No prompt text. `Plan` carries an `Action`, never words |
| Memory reasoning | `ContextEngine` manages scope, TTL and snapshots — never meaning |
| Tool execution | `StateToolExecution` models the *wait*; nothing is invoked |
| Telephony intelligence | No import, no type, no reference |
| Fraud intelligence | `PersonaFraudShield` is a capability set; no scoring, no signals |
| Business workflows | Policy rules are boundaries; no process, no sequencing |

The single boundary crossing is `Utterance.Text`, which must reach the
classifier and does not go elsewhere — see the Security Review.

---

## 6 · Code quality observations

**Strengths.** One dependency. Every invariant enforced structurally — a missing
state-machine edge, a missing command, an absent capability — so it cannot be
forgotten. The planner and the policy rules are pure functions, which made
exhaustive table tests possible and made determinism assertable rather than
hoped for. The state table is one literal, so the machine can be read, diffed
and diagrammed in one place.

**Weaknesses, stated plainly:**

- **`engine.go` is doing a lot** — event dispatch, the decision cycle, lifecycle,
  and the `Engine`/`Conversation` split. It is the first file that will need
  splitting, likely at the event handlers.
- **`Simulator` has grown API surface faster than it has grown tests using it.**
  `Advance` is now used by one test. Unused helpers on a test harness rot.
- **Two metric implementations exist in the platform** (A1). Until 10A is
  extended, an exporter must gather from both, and they can drift.
- **The 150 ms cycle budget is unvalidated against a real classifier.** Every
  other number in the config traces to a frozen budget; this one is a reasoned
  allocation and the intent stage's 60 ms is the guess most likely to be wrong.

---

## 7 · Interaction with Phase 10A

Phase 10A is frozen and **no file under `packages/go/runtime` was modified** —
verified by directory listing and by the fact that 10A's suite still passes
unchanged.

Two 10A findings are affected:

| 10A finding | Status after 10B |
|---|---|
| **A1** — Python 3.13 conflicted with the frozen 3.12 pin | **CLOSED.** The Phase 10B brief specifies Python 3.12, matching `ARCHITECTURE_FREEZE.md §5`. No superseding ADR is needed |
| **A2** — race detector not run | **STILL OPEN**, and now applies to two modules |

One new 10A finding is raised by this phase: **A1 above** (`Metrics` closed for
extension), discoverable only by being 10A's first downstream consumer.

---

## 8 · Recommendation

**Do not approve for production until A2 is closed.** It is the same blocker as
Phase 10A and it now covers more concurrent code.

For approval of Phase 10B as a design-and-implementation milestone, the material
questions are:

1. Is the **non-yielding greeting with a queued barge-in** the right reading of
   I1 versus caller experience? It is the one place the engine deliberately does
   not yield the floor to a human.
2. Is **escalation-on-exhaustion** the right default everywhere? The engine
   escalates rather than repeating on clarification, noise, boundary and policy
   exhaustion. In a deployment without human fallback, escalation may be a worse
   outcome than one more attempt.
3. Should **`Metrics` extension in 10A** be taken now (A1), or should the
   duplication stand until an exporter is built?
4. Is the **150 ms cycle budget** acceptable before a real classifier exists to
   validate the 60 ms intent allocation?

| Aspect | Status |
|---|---|
| Twelve subsystems | **Proposed** |
| 70 tests, 18 benchmarks | **Proposed** |
| Determinism | **Demonstrated** |
| Race verification | **Blocked** |
| Production readiness | **Not claimed** |
