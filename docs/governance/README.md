# Enterprise Safety, Policy & Governance Engine — Documentation

**Phase 10E** · `packages/go/governance` · Status: **PROPOSED — awaiting approval**

The final authority on whether the platform may act. Built from scratch — **no
external safety framework, no vendor moderation API, no fraud model.**

---

## Documents

| # | Document | What it answers |
|---|---|---|
| 1 | [SAFETY_ARCHITECTURE.md](SAFETY_ARCHITECTURE.md) | What the engine is and why — one door, default deny, ten outcomes, nine scopes, twelve invariants |
| 2 | [POLICY_ARCHITECTURE.md](POLICY_ARCHITECTURE.md) | The policy model, the selector set, the registry, the baseline |
| 3 | [CONSENT_LIFECYCLE.md](CONSENT_LIFECYCLE.md) | Four states, three absent edges, immediate revocation, the DPDP answer |
| 4 | [DECISION_FLOW.md](DECISION_FLOW.md) | Request to decision, consent resolution, escalation, replay, failure behaviour |
| 5 | [POLICY_EVALUATION.md](POLICY_EVALUATION.md) | The five-stage pipeline, conflict resolution, the trace |
| 6 | [ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md) | Compliance, five defects found and fixed, seven open findings |
| 7 | [PERFORMANCE.md](PERFORMANCE.md) | 20 benchmarks, scaling, the optimisation refused and why |
| 8 | [SECURITY_REVIEW.md](SECURITY_REVIEW.md) | Threat model, fourteen controls, nine findings, twenty-four attack scenarios |
| 9 | [GOVERNANCE_EVALUATION.md](GOVERNANCE_EVALUATION.md) | Does it decide, refuse and record correctly — measured |

---

## The short version

**One door.** Every conversation decision, tool execution, memory write and
external action passes through `Engine.Decide`. A bypass is a missing call —
visible in review, countable in production.

**It imports nothing it governs.** Conversation, memory and tool runtime call in;
none appears in this module's dependency graph. A governance engine that knows
the shape of its callers acquires a special case per caller.

**Policies are data; evaluation is a pure function.** Determinism, replay,
explainability and infrastructure-free testing all fall out of that one property.

**The default is deny**, and validation refuses any other. So does the zero value
of `Outcome`, so a dropped decision refuses rather than permits.

**Compliance cannot be overridden.** An emergency may relax an organisation's own
rule; it may not relax a legal one, and there is no field that would let it.

**Ten outcomes, not two.** A system that can only say yes or no says no to things
it should have asked about.

**~12 µs per decision** — 0.014% of the frozen 900 ms turn budget at ten
decisions per turn.

---

## Verification

```
cd packages/go/governance
go vet .                              # clean
gofmt -l .                            # clean
go test -count=5 -shuffle=on .        # 82 tests
go test -run TestEvaluation -v .      # the numbers in GOVERNANCE_EVALUATION.md
go test -run XXX -bench=. -benchmem . # the numbers in PERFORMANCE.md
```

**Not verified: `-race`** — requires cgo, and there is no C toolchain on the
development machine. This is the one blocking finding
([ENGINEERING_AUDIT §A2](ENGINEERING_AUDIT.md)) and now applies to five
concurrent modules.

---

## Before production

| | Finding |
|---|---|
| 1 | `-race` in CI across all five Go modules — **A2** |
| 2 | Pattern-based content refusal at admission; `Resource` still passes short identifiers — **R1** |
| 3 | Durable, append-only audit store — **R2** |
| 4 | Platform-level bypass reconciliation; the engine cannot see a caller that stops calling — **R3** |
| 5 | Durable escalation queue — **R7** |
| 6 | Two-person emergency control and a maximum duration — **R4** |
| 7 | Authorisation on operator surfaces — **R5** |

---

## Frozen artifacts

Phases 10A (`runtime`), 10B (`conversation`), 10C (`memory`) and 10D
(`toolruntime`) are **unmodified** and their suites pass. `go.work` gained one
line for this module.
