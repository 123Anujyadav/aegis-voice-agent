# Governance Evaluation Report — Safety, Policy & Governance Engine

**Phase 10E** · `packages/go/governance` · 2026-08

An engineering audit asks *is this built correctly*. This report asks a
different question: **does the engine decide the right way, refuse explainably,
and leave a record anybody could audit?**

Every figure below is produced by
[`eval_test.go`](../../packages/go/governance/eval_test.go) and reproducible:

```
cd packages/go/governance
go test -run TestEvaluation -v .
```

The suite measures **and asserts**. A report produced by tests that cannot fail
is a press release.

---

## 1 · What can and cannot be evaluated in this phase

**Can be:** what happens when nobody wrote a rule · whether every decision names
a policy and a bounded reason · whether the precedence order and the code agree ·
whether consent states are distinguishable to a caller · whether identical
inputs produce identical decisions · whether risk can be suppressed or can
loosen · whether every decision leaves a record · what governance costs a turn.

**Cannot be:** whether the policies are the *right* policies. This module
evaluates rules; it does not write them. A perfectly correct engine loaded with
a bad rule set produces bad decisions correctly, and nothing here measures rule
quality.

That boundary matters for reading §3: these are measures of the **mechanism**,
not of the policy.

---

## 2 · Corpus

The five baseline policies plus a small realistic tenant set — an organisation
consent rule, a business deferral rule and a compliance export ban — and, for the
scaling and precedence tests, generated sets of 10 to 200 policies distributed
across six scopes and five action kinds.

---

## 3 · Results

### E1 · Fail-closed coverage

**Question:** what does the engine do when nobody wrote a rule?

```
memory read    outcome=deny denied=true reason=no_policy_matched
memory write   outcome=deny denied=true reason=no_policy_matched
tool invoke    outcome=deny denied=true reason=no_policy_matched
notification   outcome=deny denied=true reason=no_policy_matched
external       outcome=deny denied=true reason=no_policy_matched
ungoverned actions denied: 5 of 5 (100%)
```

**100%, across every action kind.** And every denial carries a reason — even the
default one, because `Config.DefaultReason` is required.

This is the single most important number in the report. A governance engine
whose gaps default to "allow" has, at exactly the moment somebody forgot a
policy, stopped being one.

*`TestEvaluation_UngovernedActionsAreDenied`*

### E2 · Explainability

**Question:** can the platform defend what it did?

```
allowed read         → allow                by org.recording          reason=consent_satisfied
consented write      → require_consent      by baseline.personal-data reason=personal_data_retention
unconsented write    → require_consent      by baseline.personal-data reason=personal_data_retention
irreversible notify  → require_consent      by baseline.personal-data reason=personal_data_retention
denied export        → deny                 by baseline.personal-data reason=sensitive_data_external
secret refused       → deny                 by baseline.secrets       reason=secret_material
decisions naming a policy and a bounded reason: 6 of 6
traces accounting for every policy consulted:   6 of 6
```

**6 of 6 on both measures**, with a trace of 8 entries each — every policy in the
registry accounted for, including the ones that were skipped and why.

The bar is deliberately two-part. A decision that names a policy but carries free
text is not explainable in a way a metric or an alert can use; one with a bounded
code but no policy cannot be argued with. Both are required.

*`TestEvaluation_EveryDecisionIsExplainable`*

### E3 · Precedence correctness

**Question:** do the precedence constant and the code agree?

```
scope pairs where the safer outcome won: 36 of 36
scopes an emergency may relax:            7 of 7
compliance held against an override:      1 of 1
```

The matrix walks **every ordered pair** of the nine scopes with opposed outcomes —
the higher scope allowing, the lower denying — and checks the safer outcome wins
without an explicit override. It does, in all 36.

Then the exception: an emergency override relaxes all 7 overridable scopes, and
**compliance holds**.

This is the engine's most consequential constant, and a matrix is the only way to
be sure the constant and the code agree. It is also the test that would have
caught F3 — the version where every emergency override silently did nothing — on
the day it was written rather than three files later.

*`TestEvaluation_PrecedenceMatrix`*

### E4 · Consent lifecycle

**Question:** can a caller tell the four failure states apart, and can the
history answer a regulator?

```
never asked  outcome=require_consent  obligation_reason=not_found    distinguishable=true
granted      outcome=require_consent  obligation_reason=             distinguishable=true
expired      outcome=require_consent  obligation_reason=expired      distinguishable=true
revoked      outcome=require_consent  obligation_reason=revoked      distinguishable=true
superseded   outcome=require_consent  obligation_reason=superseded   distinguishable=true
consent states distinguishable to a caller: 5 of 5
```

**5 of 5.** Each state produces a distinct, machine-readable reason on the
obligation, so a caller knows whether to ask, ask again, stop asking, or ask
about new terms.

Worth reading the second column carefully: **every row shows
`outcome=require_consent`, including the granted one.** That is correct and it is
the point of obligations being a set — the baseline demands a `data_processing`
consent for personal writes, which this corpus has not granted, so the decision
legitimately still waits on a different basis. The `call_recording` obligation is
satisfied and gone; another remains.

A single-outcome view would have made that look like a failure. The per-basis
reason is what makes it legible.

*`TestEvaluation_ConsentLifecycleIsCompleteAndDistinguishable`*

### E5 · Determinism

**Question:** is the engine replayable?

```
runs=50 outcome divergences=0 trace divergences=0 obligation divergences=0
```

**Zero across all three fingerprints**, over 50 independent engine
constructions — not 50 calls to one engine, so map-layout luck is not doing the
work.

Outcome, deciding policy and reason are stable; the trace is stable entry by
entry including which policies were skipped and why; the obligation set is stable
in content and order.

**A governance engine that decides differently on identical inputs cannot be
audited**, and a platform that acts on somebody's behalf must be able to answer
"why did it do that" with something better than a log line.

*`TestEvaluation_IdenticalRequestsProduceIdenticalDecisions`*

### E6 · Risk cannot be suppressed and cannot loosen

**Question:** can an attacker flood reassuring signals, or can a detector
overrule a policy?

```
signals added=50 aggregate drops=0 final=critical

policy=deny risk=low      → deny
policy=deny risk=medium   → deny
policy=deny risk=high     → deny
policy=deny risk=critical → deny
```

**Monotonic: 50 low-risk signals added to one critical signal, 0 drops.** An
attacker who can produce cheap reassuring signals cannot suppress an expensive
alarming one.

**Only-raises: 0 loosenings across all four levels.** A risk signal cannot
overrule a written denial, which is what keeps the precedence model intact —
policy decides, risk may only tighten.

*`TestEvaluation_RiskIsMonotonicAndOnlyRaises`*

### E7 · Audit completeness

**Question:** does every decision leave a record, and does the record leak?

```
decisions=5 audit entries=5 events=5
audit entries or events carrying an action resource: 0
```

**One audit entry and one event per decision, exactly** — no decision without a
record, no record without a decision.

**Zero leaks.** The corpus deliberately used a distinctive resource name and
checked every audit entry and every event for it.

The caveat, and it is R2 in the security review: this measures that the engine
**emits** a complete trail, not that a durable store **retains** one. Audit
writes are best-effort by design and the only auditor in this module is
in-memory.

*`TestEvaluation_EveryDecisionLeavesARecord`*

### E8 · Overhead against the frozen budget

**Question:** what does governance cost a conversational turn?

```
policies=8 decisions=500 per-decision=12.326µs
per-turn(10 decisions)=123.26µs budget=900ms share=0.014%
```

| | |
|---|---:|
| Per decision | **12.3 µs** |
| Decisions per turn (assumed, pessimistic) | 10 |
| Per turn | **123 µs** |
| ADR-0011 p50 budget | 900 ms |
| **Share** | **0.014%** |
| Asserted ceiling | 1% |

**70× inside the ceiling.** Unlike the other phases, this engine is on the
critical path of *every* action, so the per-turn figure rather than the
per-decision one is the honest measure — and it is still negligible.

*`TestEvaluation_GovernanceOverheadAgainstTheTurnBudget`*

### E9 · Scaling with policy count

```
policies=10   per-decision=11.985µs  per-policy=1.198µs
policies=50   per-decision=29.583µs  per-policy=591ns
policies=100  per-decision=48.197µs  per-policy=481ns
policies=200  per-decision=80.742µs  per-policy=403ns
```

**Linear, ~7 µs fixed plus ~370 ns per policy.** The falling per-policy figure is
the fixed cost amortising.

The engine visits every policy in order to produce a complete trace. At 200
policies that is 80 µs per decision, or 0.09% of a turn at ten decisions.
Extrapolated to 1,000 policies it is 380 µs per decision and 0.4% per turn —
still workable, and the point at which the trade discussed in PERFORMANCE §5
becomes worth revisiting.

**The number to watch is the policy count, not the decision count.**

*`TestEvaluation_ScalingWithPolicyCount`*

---

## 4 · What the engine is deliberately bad at

An evaluation listing only strengths is not an evaluation.

| Weakness | Consequence | Deliberate? |
|---|---|---|
| **Cannot detect its own bypass** | A subsystem that stops calling is invisible | Structural — SECURITY_REVIEW R3 |
| **Content exclusion is bounded, not proved** | A short identifier in `Resource` reaches a trace | Partial — R1 |
| **No durable audit store** | The trail is best-effort and in-memory | Yes — Phase 10F |
| **Policies are Go values, not files** | Policy changes need a deploy; no independent diff | Yes — audit A5 |
| **Evaluation is O(policies)** | 403 ns per policy, every decision | Yes — the alternative loses the trace |
| **Nine selectors, no arithmetic** | Some real rules are inexpressible | Yes — they belong in a `Detector` |
| **Authorises nobody** | Anything holding an `*Engine` rewrites every policy | Accepted — R5 |
| **Escalation queue is in-memory** | A restart discards pending approvals | Yes — R7 |
| **Cannot judge whether the policies are right** | A bad rule set produces bad decisions correctly | Out of scope |

**The first two will be felt.** The bypass gap makes the module's headline
architectural rule unobservable from inside it, and the content bound is the
difference between "no personal data in the audit trail" as a guarantee and as a
strong default.

---

## 5 · Readiness

| Consumer | Ready | Needs |
|---|---|---|
| Conversation engine (10B) | **Yes** | Call `Decide` before acting |
| Memory engine (10C) | **Yes** | Same |
| Tool runtime (10D) | **Yes** | Same |
| Future subsystems | **Yes** | It consumes a struct, not a package |

**Before production**, in order: `-race` in CI (A2) · pattern-based content
refusal (R1) · durable audit store (R2) · platform-level bypass reconciliation
(R3) · durable escalation queue (R7) · two-person emergency control (R4) ·
authorisation on operator surfaces (R5).

---

## 6 · Assessment

Measured, not asserted: 5 of 5 ungoverned action kinds denied; 6 of 6 decisions
naming a policy and a bounded reason with a complete trace; 36 of 36 ordered
scope pairs resolving to the safer outcome; 7 of 7 overridable scopes relaxed by
an emergency with compliance holding; 5 of 5 consent states distinguishable to a
caller; 0 divergences across 50 identical runs; 0 aggregate drops across 50
suppressing signals and 0 loosenings across four risk levels; 1 audit entry and 1
event per decision with 0 content leaks; and 0.014% of a conversational turn.

Its limits are the ones the brief drew — no fraud model, no prompts, no business
logic — and three it did not. **It cannot see a caller that stops calling it, it
cannot prove that what callers pass it is free of content, and it has nowhere
durable to write what it decided.** Those three are the distance between this and
a governance engine a regulator could be shown.

The gap is **observability, content enforcement and durability** — not
correctness of the decision model.
