# Policy Architecture

**Phase 10E** · Sourced from
[`policy.go`](../../packages/go/governance/policy.go),
[`registry.go`](../../packages/go/governance/registry.go),
[`baseline.go`](../../packages/go/governance/baseline.go)

---

## 1 · A policy is data

```go
Policy{
    ID, Version, Scope, Priority          // identity and precedence
    Title, Description, Owner             // who to ask
    Match{...}                            // which requests it applies to at all
    Rules []Rule{ When []Condition, Then Outcome, Reason, Obligations }
    Default Outcome, DefaultReason        // when it applies but no rule matches
    Override bool                         // emergency and compliance only
    Enabled, EffectiveFrom, EffectiveUntil
}
```

**No methods that reach anywhere. No closures. No callbacks.** A policy does
nothing when evaluated except answer questions about itself.

That is what lets a policy be reviewed before it is loaded, compared between two
deployments, and recomputed from an audit record years later.

---

## 2 · Match versus When — and why they are separate

| | `Match` | `Rule.When` |
|---|---|---|
| Question | Does this policy apply at all? | Does this rule fire? |
| Failure | Policy **skipped** | Policy **evaluated**, no rule matched |
| Trace | `skipped: match_kind` | `no rule matched` |

Those are different facts, and **the second is the one an operator wants** when a
rule they wrote appears to have done nothing. Collapsing them would leave "my
policy is not in the trace" as the only signal, which is indistinguishable from
"my policy is not loaded".

---

## 3 · Rules are first-match

Rules are evaluated in declaration order and **the first match wins**.

Not most-specific-match: "most specific" needs a specificity metric, and every
specificity metric surprises somebody. **A policy author reading top to bottom
can predict the outcome**, which is the only property that makes a policy file
reviewable by a non-author.

---

## 4 · Nine selectors, and no more

`==` · `!=` · `>` · `<` · `>=` · `in` · `not_in` · `exists` · `absent` ·
`prefix`

No arithmetic. No regular expressions. No boolean algebra beyond the implicit
AND across a rule's conditions.

**A policy language grows until it is a programming language, and then the policy
file is a program nobody reviews** — which defeats the point of writing policy
down. Anything needing more expressiveness belongs in a `Detector` or a `Signal`,
where it is named, owned and testable.

The same argument Phase 10D makes for its six condition operators, with more
force here because these rules decide whether the platform may act at all.

### Two traps the selector semantics close

**An absent field is false for every comparison except `absent`.** Notably for
`!=` too: *"the field is not X" is not true of a field that is not there*, and
treating it as true is how an author writes a deny rule that never fires.
Asserted for all eight comparison selectors.

**Built-in fields cannot be shadowed.** Resolution order is built-ins → action
attributes → request context. A caller passing an attribute named
`classification` cannot make a `classification >= sensitive` condition read
`public` instead. Asserted by test.

---

## 5 · Fields

| Field | Source |
|---|---|
| `kind`, `operation`, `resource` | The action |
| `classification`, `reversibility` | The action |
| `risk` | The aggregated assessment |
| `actor`, `subject`, `org`, `business` | The request |
| `role` | The request's roles — **a set**, so `in` means membership |
| anything else | Action attributes, then request context |

`risk` and `classification` accept their names as well as their ordinals, so a
policy reads `risk >= "high"` rather than `risk >= 2`. A policy file carrying an
engine's internal encoding is a policy file that breaks when the encoding
changes.

---

## 6 · Reason codes are bounded

Lowercase letters, digits and underscores, at most 64 characters. Refused at
registration.

**Two independent reasons, either alone sufficient:**

1. A reason becomes a **metric label**. Unbounded text is an
   unbounded-cardinality incident waiting for its first unusual policy.
2. A reason travels into a **permanent event topic**. An author who writes a
   subject identifier into a reason has put it somewhere no erasure request can
   reach.

This was found by a test that caught a fixture writing a consent basis name into
a reason — ENGINEERING_AUDIT §F-note. It does not make INV-GOV-7 airtight; a
determined author can still write a short identifier in snake_case. Stated as
partial in SECURITY_REVIEW §R1.

---

## 7 · What validation refuses

| Refusal | Why |
|---|---|
| No ID, version < 1 | An unversioned change is one no audit record can distinguish |
| No owner | A policy nobody owns is one nobody updates when the law changes |
| No `DefaultReason` | A policy that denies by default must say why |
| Rule with no name | A trace naming an anonymous rule cannot be acted on |
| Rule with no reason | Every denial must be explainable |
| Duplicate rule names | Two entries, one name, in every trace |
| `Override` outside emergency/compliance | A tenant exempting itself from the platform floor |
| Temporary scope with no `EffectiveUntil` | A permanent policy wearing a temporary label |
| No rules **and** default allow | A blanket permission that never appears in a trace |
| `retry_later` with no `RetryAfter` | Telling a caller to try again without saying when is telling it to spin |
| `in`/`not_in` with an empty set | Matches nothing; almost never what an author meant |
| Version that does not advance | A change no audit record can distinguish from the old one |

**Validated once, at registration, and never again.** Every downstream stage —
resolution, merge, conflict detection, trace assembly — assumes a registered
policy is well-formed, which is only safe because nothing enters without passing
this.

---

## 8 · The registry is copy-on-write

Reads take **no lock**: they load an immutable snapshot pointer and walk it.
Measured at **0.63 ns** to acquire a snapshot.

The trade is right because every decision reads the whole registry and policies
change on deploy. But the property that matters more than the speed:

**A decision is made against exactly one snapshot** (INV-GOV-9). A policy reload
part-way through an evaluation cannot produce a decision that half-obeys the old
rules and half the new, and the snapshot version travels in the `Decision` so it
can be recomputed against the same rules years later.

Asserted under churn by `TestStress_PolicyChurnDuringDecisions`.

### `RegisterAll` is atomic

A partial policy load is the worst possible state: the platform runs under half
a rule set, and which half depends on the order somebody wrote the calls in.

### Disable rather than unregister

Disabling keeps the policy resolvable, so an audit record naming it still means
something — the same argument Phase 10D makes for retiring a tool rather than
removing it.

### The snapshot digest

Fingerprints every policy's **decision-relevant** content: identity, scope,
priority, override, rules, conditions, outcomes, reasons. Excludes title,
description, owner and tags.

Two deployments with the same digest **decide identically** even if one has
better documentation. That is the question a change review actually asks.

---

## 9 · Conflict detection is static

`ConflictsIn(snapshot)` reports policy pairs at the same scope **and** the same
priority whose outcome sets differ.

It cannot prove two policies *will* disagree — that depends on the request — but
it can prove they *might*, which is the thing an operator can act on before a
decision goes the wrong way in production. It over-reports, and over-reporting a
possible conflict is the right direction to be wrong in.

The error names both policies, the shared priority and how to fix it:

```
governance: a and b both sit at priority 100 in the business scope and
disagree (allow vs deny); give one of them a distinct priority
```

Run at boot and after every policy load. Measured at **135 µs for 200
policies** — off the request path entirely.

### At runtime, an unresolvable conflict stays safe

Two policies at the same priority that disagree do **not** produce an error at
decision time. The engine takes the **more severe** outcome and leaves the
evidence in the trace. The platform stays safe; the operator finds out. Silently
picking by alphabetical order would be deterministic and would hide a real
mistake.

---

## 10 · The baseline

Five policies, an **explicit call** rather than a default.

| Policy | Scope | Priority | Effect |
|---|---|---:|---|
| `baseline.secrets` | **Compliance** | 1000 | Secret material is never stored or transmitted |
| `baseline.personal-data` | Global | 900 | Personal writes/exports need consent; sensitive data never leaves |
| `baseline.irreversible` | Global | 800 | Anything that cannot be undone requires confirmation |
| `baseline.external` | Global | 700 | Unclassified external actions need a human |
| `baseline.reads` | Global | 100 | Non-mutating actions are permitted |

Three notes on the shape:

**Secrets sit in the compliance scope**, so no emergency can relax them. There
is no incident that makes storing a credential in a conversation platform
acceptable, and a mechanism that could be used to permit it would eventually be.

**`baseline.irreversible` is the most valuable rule in the set.** An AI acting
on somebody's behalf will eventually be wrong; the difference between an
embarrassment and an incident is whether the wrong thing could be undone.

**`baseline.reads` states its permission as a RULE, not as a default.** Without
it an engine loaded with only the baseline denies its own reads — technically
safe, practically useless. Stating it explicitly means an operator reading a
trace sees `allowed by baseline.reads` rather than an absence of denial, and can
see exactly what to change.

Personal **reads** are deliberately not gated. A platform that cannot read a
subject's own preference without a consent check cannot answer their call, and
the basis for serving somebody who rang you is not the same question as the
basis for retaining what they said.

---

## 11 · Nine policy models, one type

The brief lists nine kinds of policy. They are **one `Policy` type distinguished
by `Scope`**, not nine types:

| Brief's model | Expressed as |
|---|---|
| Global | `ScopeGlobal` |
| Organization | `ScopeOrganization` + `Match.Orgs` |
| Business | `ScopeBusiness` + `Match.Businesses` |
| User | `ScopeUser` + `Match.Subjects` |
| Session | `ScopeSession` |
| Emergency | `ScopeEmergency` + `Override` + an `Emergency` activation |
| Compliance | `ScopeCompliance` — the only unoverridable one |
| Temporary | `ScopeTemporary` + a required `EffectiveUntil` |
| Feature flag | `ScopeFeatureFlag` — lowest, so it may restrict but never permit |

Nine types would mean nine validation paths, nine evaluation paths and nine
places for a precedence bug. One type with a scope means the precedence order is
**a single sorted constant** that a reviewer can read in one screen — and the
evaluation-order matrix in GOVERNANCE_EVALUATION §E3 can check all 36 ordered
pairs mechanically.

`TemporaryPolicy` and `FeatureFlagPolicy` are constructors over the same type
that make the correct thing the easy thing: the first takes the duration as a
required argument, the second produces a policy that structurally cannot permit.
