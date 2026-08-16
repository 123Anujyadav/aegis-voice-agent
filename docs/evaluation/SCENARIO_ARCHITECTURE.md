# Scenario Architecture

**Phase 10F** · `packages/go/evaluation/scenario.go`, `packages/go/evalsubjects/scenarios.go`

---

## 1. A scenario is a question, not an expectation

```go
type Scenario struct {
    ID          ScenarioID
    Version     int
    Kind        ScenarioKind
    Title       string
    Owner       string
    SubjectName SubjectName
    Steps       []Step
    Requires    []Capability
    Tolerances  Tolerances
    Timeout     time.Duration
}
```

There is no `Expected` field. A scenario describes **what to do**; the golden
describes what happened when somebody approved it. Keeping the two apart is what
stops an author from encoding their assumption about a subsystem into the thing
that checks the subsystem.

A `Step` names an operation and its arguments. A `StepResult` carries an outcome
code, an optional failure flag, outputs, state and events. The subject decides
all of it; the platform records it.

---

## 2. Eight kinds

| Kind | Question it asks |
|---|---|
| `runtime` | Does the runtime core still behave as approved? |
| `conversation` | Does the conversation engine still turn-take and escalate as approved? |
| `memory` | Does memory store, retrieve, forget and refuse as approved? |
| `tool` | Does the tool runtime plan, execute, deduplicate and classify as approved? |
| `governance` | Does the governance engine decide, gate and refuse as approved? |
| `failure` | Under an injected failure, does the subsystem degrade as approved? |
| `recovery` | After a failure, does it come back as approved? |
| `emergency` | Under an emergency override, does containment hold? |

The kind drives suite membership, the regression vocabulary and the coverage
report. `Coverage.UncoveredKinds()` names kinds with no scenarios at all —
reported rather than failed, because which kinds a library covers is a fact
about the library, not a platform invariant.

---

## 3. The selection criterion

**Each scenario exercises a property some other phase's design argument rests
on.** A scenario that checks something no design decision depends on costs time
and buys nothing.

The library holds **18 scenarios across 5 subjects and 8 kinds**:

| Subject | Count | Scenarios |
|---|---|---|
| `runtime` | 3 | `breaker-opens`, `window-evicts`, `clock-advances` |
| `conversation` | 2 | `turn-taking`, `escalation` |
| `memory` | 4 | `store-retrieve`, `forget-is-complete`, `missing-is-not-found`, `oversized-refused` |
| `toolruntime` | 4 | `execute`, `idempotency`, `plan-is-inert`, `failure-is-classified` |
| `governance` | 5 | `baseline-decides`, `consent-gate`, `risk-raises`, `emergency-containment`, `malformed-refused` |

By kind: runtime 3, memory 3, tool 3, governance 3, failure 3, conversation 1,
emergency 1, recovery 1.

Each of these tracks a claim a frozen phase made:

- `runtime.breaker-opens` — four later phases assume the breaker opens. One that
  silently stopped opening would leave every retry loop hammering a dead
  downstream.
- `memory.forget-is-complete` — Phase 10C's DPDP argument. A forget that leaves
  a fragment is a compliance failure, not a bug.
- `tool.plan-is-inert` — Phase 10D's central rule: the conversation engine never
  executes tools. A plan that executed anything would break it.
- `governance.emergency-containment` — Phase 10E's override path, which the
  audit found was a no-op in an earlier draft.
- `governance.malformed-refused` — default deny, on the input nobody designed
  for.

**They are deliberately small.** A forty-step scenario that fails tells you
nothing about which of its forty assumptions broke.

---

## 4. Versioning and digests

A scenario carries a `Version`, and the golden key is
`(Scenario, Version, Subject)` — all three, because the same scenario at a
different version is a different question and the same scenario against a
different subject is a different answer.

A golden recorded at v1 is **refused** as a baseline for v3 rather than compared
against it: the resulting drift would be real and unexplainable.

`Scenario.Digest()` fingerprints the steps. It catches the case the version
check cannot: a scenario whose steps were edited without a version bump. The
golden stores the digest it was recorded against, so the mismatch surfaces even
though the version matched.

The registry publishes a `Digest()` over the whole scenario set, and every `Run`
records the `RegistryVersion` and `RegistryDigest` it ran under. That is the
replay anchor for the *questions*, as the golden is for the *answers*.

---

## 5. Capabilities and skipping

```go
Requires: []Capability{ev.InjectionCapability(ev.FailGovernance)}
```

A subject declares what it can do. A scenario declares what it needs. A subject
lacking a requirement produces a **skip naming the missing capability**, not a
failure and — critically — not a pass.

`TestVerification_InjectionCapabilitiesAreDeclared` checks every injection a
scenario requires against its subject's declaration. Without it, a scenario
asking for an injection the subject cannot perform would run, do nothing, and
pass: the quietest possible way for safety coverage to evaporate.

---

## 6. Suites

Six suite kinds; three suites in the library.

| Suite | Kind | Gating | Scenarios | Membership rule |
|---|---|---|---|---|
| `acceptance` | acceptance | **yes** | 15 | everything that is not failure injection |
| `compliance` | compliance | **yes** | 4 | governance and emergency kinds |
| `benchmark` | benchmark | no | 6 | memory and governance kinds, run **serially** |

Failure-injection scenarios are excluded from acceptance on purpose: a release
gate containing scenarios whose point is that something broke is a gate nobody
can read at a glance.

The benchmark suite is not marked `Parallel`, and the registry refuses it if it
is. A benchmark suite sharing a CPU measures contention.

---

## 7. Registry

Copy-on-write with an atomic snapshot pointer. A reader takes a snapshot and
finishes its run against a coherent scenario set even as new versions are
published beneath it.

Verified under load: `TestVerification_ConcurrentRegistryMutation` performs 200
evaluations against 40 concurrent registry publications, ending at version 42
with 58 scenarios registered, with no failed evaluation.

Registration is validated: duplicate IDs, unknown subjects, empty steps, a suite
naming a scenario that does not exist, a parallel benchmark suite — all refused
at registration rather than discovered at run time.

---

## 8. What a scenario may not do

- **It may not assert.** There is no mechanism to.
- **It may not reach into a subsystem's internals.** It calls a `Subject`, which
  calls exported API.
- **It may not depend on wall time.** Every adapter opens its session with a
  `FakeClock`, so clock advances are part of the scenario rather than of the
  machine.
- **It may not carry business logic.** No prompts, no fraud rules, no telephony.

---

## 9. Related

- [EVALUATION_ARCHITECTURE.md](EVALUATION_ARCHITECTURE.md)
- [REPLAY_ARCHITECTURE.md](REPLAY_ARCHITECTURE.md)
- [EVALUATION_REPORT.md](EVALUATION_REPORT.md)
