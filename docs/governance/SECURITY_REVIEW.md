# Security Review — Safety, Policy & Governance Engine

**Phase 10E** · `packages/go/governance` · 2026-08
**Verdict: APPROVE FOR INTEGRATION — NOT FOR PRODUCTION UNTIL R2, R3 AND A2 CLOSE**

This engine decides whether the platform may act. Compromising it does not leak
data and does not take one action — it removes the constraint on **every** action
the platform takes. That makes it the highest-value target in the system, and
this review is written accordingly.

---

## 1 · Threat model

**Assets:** the constraint itself · the audit record proving what was decided ·
consent records and their history · the fact that a decision was made about a
person · the emergency mechanism.

| # | Adversary | Capability |
|---|---|---|
| T1 | Compromised in-process caller | Constructs arbitrary `Request` values |
| T2 | **A subsystem that simply does not call** | Bypass by omission |
| T3 | Policy author | Writes policies, possibly badly or maliciously |
| T4 | Operator with engine access | Registers policies, declares emergencies |
| T5 | Log / event-stream reader | Reads Kafka, metrics, logs |
| T6 | Subject of the data | Exercises access, correction and erasure rights |
| T7 | Regulator / auditor | Asks "on what basis, decided by what, and when" |
| T8 | Prompt-injected conversation | Causes a plausible-looking request to be emitted |

**Out of scope for 10E:** transport security, credential storage, host
compromise, and the durable audit store (none exists yet).

---

## 2 · Controls

### C1 · Fail closed, in four places — T1, T3, T4

| Where | Behaviour |
|---|---|
| No policy matched | Deny |
| `Config.Default` | **Validation refuses anything but deny** |
| Zero-valued `Outcome` | Deny — a dropped decision refuses |
| Evaluation panics | Deny, counted; `FailClosedOnPanic` cannot be set false |

**A safety engine whose failure mode is "allow" has stopped being one at the
exact moment it broke.** The engine boots denying everything until policies are
loaded, which converts "we forgot the policies" from a silent security incident
into a loud outage.

### C2 · One entry point — T2, partially

`Decide` is the only decision function. No fast path, no bypass flag, no
per-subsystem variant. A bypass is a **missing call**, which is visible in
review. Partial: see R3.

### C3 · Compliance cannot be overridden — T4

`Scope.Overridable()` is false only for compliance, and it is a **method, not a
configuration field**. A configurable "can compliance be overridden" flag is a
flag that gets set to true during an incident.

An `Emergency` that lists `ScopeCompliance` is refused at construction — a
configuration error rather than a silently ignored entry, so an operator who
tried finds out.

Measured: **7 of 7 overridable scopes relaxed, compliance held** —
GOVERNANCE_EVALUATION §E3.

### C4 · Emergencies are bounded, attributed and self-ending — T4

| Rule | Enforced at |
|---|---|
| `ExpiresAt` required | Construction |
| `AuthorisedBy` required | Construction |
| `Reason` required | Construction |
| Must install policies | Construction |
| Cannot extend in place | `Activate` refuses an already-active name |
| Ends whether or not anybody remembers | `Sweep` |
| Audited at **declaration**, not only at use | `Activate` |
| Visible in the health report | `Coordinator.Health` |

The declaration/use distinction matters: an emergency declared and never used is
a false alarm; one used a thousand times is a policy gap wearing an incident's
clothes. Both are worth knowing.

### C5 · The caller cannot assert a risk level — T1, T8

`Decide` recomputes the aggregate from the signals. A caller-asserted level would
let a compromised subsystem declare itself low-risk and walk past every
risk-aware policy.

### C6 · Risk aggregation is monotonic — T1, T8

Adding a signal never lowers the level. **A non-monotonic aggregator is a system
where an attacker who can produce cheap reassuring signals suppresses expensive
alarming ones.** Reassurance belongs in policy, where the trace shows it.

Measured: 50 low signals added to a critical one, **0 drops**.

### C7 · Risk thresholds only raise — T3

A threshold that could lower an outcome would let a detector overrule a written
policy. Measured across all four levels against a deny policy: **0 loosenings**.

### C8 · Events and audit carry codes and fingerprints, never content — T5, T6

Frozen invariant **I7**. `Event` and `AuditEntry` have no field capable of
holding an action's resource, an attribute value, or anything a person said.

The design test: *if this topic were retained forever and could never be
deleted, would that be a compliance failure?* It must be no. Kafka cannot delete
an individual record, so an erasure right that depends on deleting from a topic
is not an erasure right.

Three specific narrowings:

- `ActionLabel` is kind and operation only — **never the resource**, which is
  frequently a business or memory identifier.
- Events carry obligation **kinds**, not targets. A consumer learns consent was
  required, not which basis — because a basis name in a permanent topic is a
  statement about a person.
- Escalation queue entries carry `ActionLabel` too, because a queue is rendered
  in consoles and a resource identifier in it is one in a screenshot.

Asserted by `TestEvents_CarryNoContent` and
`TestIntegration_EscalationCarriesNoResourceIdentifier`.

### C9 · Attributes and reasons are bounded — T1, T3, T5

| Bound | Value | Enforced at |
|---|---|---|
| Attribute string length | 256 | Validator |
| Attribute contains a line break | Refused | Validator |
| Attributes per action | 32 | Validator |
| Resource length | 512 | Validator |
| Reason code charset | `[a-z0-9_]` | Policy registration |
| Reason code length | 64 | Policy registration |

Reason codes are bounded for **two independent reasons**: they become metric
labels, and they travel into a permanent topic. The reason-code rule was added
because a test caught a fixture writing a consent basis into one.

Partial, not airtight — see R1.

### C10 · One decision, one snapshot — T4

Copy-on-write. A policy reload part-way through an evaluation cannot produce a
decision that half-obeys each rule set, and the snapshot version travels in the
decision so it can be recomputed against the same rules later.

An operator (or an attacker with registry access) cannot change what an
in-flight decision is being evaluated against.

### C11 · Revocation is immediate — T6

No cache, no grace period, no TTL on the answer. **A subject who withdraws
consent has withdrawn it**, and a system that keeps processing for another five
minutes is processing without a basis for five minutes.

`RevokeAll` covers every basis, because a subject must not have to enumerate
bases they never knew existed.

### C12 · Consent history is append-only in effect — T6, T7

Every grant mints a new record; the previous moves to history. Three revival
edges deliberately do not exist. **A revocation a later grant can erase is a
revocation nobody can prove happened.**

### C13 · Escalation expiry does not permit — T4, T8

An unanswered escalation resolves to `ResolutionExpired`, which does not permit
the action. Letting it time out into approval **turns every approval gate into a
delay** — the single most common way an approval control is quietly defeated.

Resolution requires a named human; the first resolution wins and a second is
refused.

### C14 · Structural validation precedes policy — T1

A malformed request is denied with `malformed_action` and the specific problem,
rather than with a plain denial. A caller with a typo should fix the typo rather
than conclude the platform is over-restrictive and start arguing with the policy
team.

---

## 3 · Findings

### R1 · Content exclusion is bounded, not proved — **must close before production**

**Severity: high.**

INV-GOV-7 says decisions, events and audit records carry no content. The
mechanical defences are a 256-character attribute limit, a line-break refusal,
and a bounded reason-code charset.

**None of that proves an attribute is not content.** A phone number is twelve
characters. A subject's name is eight. `Action.Resource` is bounded at 512 and
is free text by design — it is a capability name, a memory key, a notification
channel, and nothing stops a caller putting an identifier there. It does not
reach events (C8), but it does reach `Decision.Trace` and
`Decision.Explanation`, which an operator console renders.

What exists: the failure that actually happens — somebody passing a whole
utterance because it was convenient — is blocked.

**What is required before production:** a pattern-based refusal at admission
(E.164, long digit runs, `@`), aligned with the Identity context's own rules
rather than invented here, and a decision about whether `Resource` should be
fingerprinted rather than carried. Both are contained changes to `Validator`.

### R2 · The audit trail is best-effort and in-memory — **must close before production**

**Severity: high.**

An audit write failure is **counted and the decision proceeds**. That is the
right call — failing would deny an action the policies permit, turning an
audit-store outage into a platform outage — but two things follow:

**The audit trail is not guaranteed complete.** A governance decision with no
record is precisely the thing T7 asks about.

**There is no durable auditor in this module.** `RecordingAuditor` is in-memory
and bounded; `NoopAuditor` discards. `Config.RequireAuditor` defaults true, so an
engine cannot start with *no* auditor — that stops the accidental case, not the
storage gap.

**Required:** an append-only, durable audit store (Aurora, per the brief's
stack), plus a decision about what to do when it is unavailable for a sustained
period. "Proceed and count" is correct for seconds and questionable for hours.

### R3 · The engine cannot detect its own bypass — **must close before production**

**Severity: high, structural.**

The architectural rule is that no subsystem bypasses this engine. The engine
enforces it by **being the only door** — but a subsystem that never knocks is
indistinguishable from one that had nothing to do.

`Engine.Decisions()` is a counter. Detecting a bypass means comparing it against
each subsystem's own action count, **outside this module**, and nothing here
compels that comparison.

**Required:** a platform-level invariant check — a periodic reconciliation
between `governance_decisions_total` and each subsystem's action counters, with
an alert on divergence. It is a monitoring change rather than a code change
here, and it is the only thing that makes INV-GOV-1 observable.

Until it exists, "no subsystem may bypass this engine" is enforced by code review.

### R4 · A single person can declare an emergency — T4

**Severity: medium.**

`Emergency` requires an author, a reason, an expiry and a ticket. It does **not**
require a second approver.

One person with engine access can relax every scope below compliance for up to
whatever expiry they choose. It is bounded, attributed and audited at
declaration — which is the point of the mechanism — but it is not two-person
control.

**Accepted for 10E** because two-person control needs an approval workflow, and
that workflow is `HumanRuntime`, which is in this module and would be circular.
**Recommended:** a deployment policy requiring emergencies to be declared through
the escalation queue with a supervisor resolution, which the existing pieces
support without a code change.

Also note: an emergency's expiry is unbounded upward. Nothing refuses a
ten-year emergency. A `MaxEmergencyDuration` in `Config` would be a one-line
improvement and is recommended.

### R5 · No authorisation on any operator surface — T4

**Severity: medium in-process, high if exposed.**

`Policies().Register`, `Policies().Unregister`, `SetEnabled`,
`Emergency().Activate`, `Consent().SetTermsVersion`, `Human().Approve` and
`Coordinator().ForgetSubject` have **no access control**. Anything holding an
`*Engine` can rewrite the platform's entire policy set.

Consistent with Phases 10C and 10D — this module authorises nothing — and
acceptable in-process. **Any deployment that exposes these over a network or a
console must authenticate and authorise at that edge.**

Policy registration and emergency activation are audited; `SetEnabled` and
`SetTermsVersion` are audited; `ForgetSubject` is audited through the
revocations it performs. That is the full coverage, and it is adequate — the gap
is authorisation, not attribution.

### R6 · Consent evidence is a fingerprint, not proof — T7

**Severity: medium, by design.**

`ConsentRecord.Evidence` is a fingerprint of the artefact — a recording, a signed
form — not the artefact. Holding the artefact would make this engine an
unmanaged personal-data store with a different retention schedule from the
system that captured it.

**The limitation is real:** a fingerprint proves two records reference the same
artefact. It does not prove the artefact exists, is retrievable, or says what
anybody claims. Producing consent evidence for a regulator requires the
capturing system to still hold it, and **nothing in this module verifies that it
does.**

**Recommended:** a periodic reconciliation between consent records and the
evidence store, so a missing artefact is discovered before a regulator asks
rather than during.

### R7 · The escalation queue does not survive a restart — T4

**Severity: medium.**

`HumanRuntime` is in-memory and bounded. A deploy loses every pending approval:
the decisions that raised them have already returned `require_supervisor` to
their callers, so the actions do not proceed — **fail-safe, but it means a
rolling restart silently discards work humans were asked to do.**

`Engine.Stop` deliberately does **not** expire pending escalations, because a
deploy is not an answer from a human. That is the right choice and it does not
help: the queue is gone either way.

**Required before production:** a durable escalation store. Aurora, per A3.

### R8 · Policy authors are trusted — T3

**Severity: medium, accepted.**

A policy author can write a rule that allows anything their scope permits. The
protections are scope precedence (a business author cannot loosen the global
floor), `Override` being refused outside emergency and compliance, static
conflict detection, and a mandatory owner on every policy.

What is **not** protected: an author with compliance-scope access can write
anything at all, and a global-scope author can loosen everything below.

**Accepted** — somebody must be able to write the rules — but it means **scope
access is the real access-control boundary of this engine**, and R5 says there
is none. The two findings compound: an unauthorised operator surface plus
trusted policy authors means anything with an `*Engine` can rewrite the
platform's safety posture.

### R9 · Prompt injection reaches the engine as a well-formed request — T8

**Severity: medium, mitigated but residual.**

An injected conversation cannot forge a decision, cannot assert a risk level
(C5), and cannot reach an action the actor lacks scope for. But it **can** cause
a legitimate action to be requested with attacker-chosen parameters.

What the engine contributes: irreversible actions require confirmation by
baseline; risk thresholds raise; every decision is fingerprinted and audited;
`Decision.Obligations` gives the caller a machine-readable precondition to
satisfy before acting.

What it cannot contribute: judgement about whether the request was really the
subscriber's. **The defence against T8 is upstream**, in the conversation
engine's safety layer, and this module's job is to make the confirmation
requirement expressible — which the baseline does.

---

## 4 · Attack scenarios

| # | Scenario | Outcome |
|---|---|---|
| 1 | Boot with no policies and act | **Everything denied** |
| 2 | Configure the engine to default to allow | **Refused at construction** |
| 3 | Drop a decision and use the zero value | **Denies** |
| 4 | Crash the evaluator with a malformed snapshot | **Denies, counted** — and the handler no longer crashes (F1) |
| 5 | Business policy permits what the platform forbids | **Refused** — the safer outcome wins |
| 6 | Business policy sets `Override` | **Refused at registration** |
| 7 | Emergency relaxes a compliance rule | **Refused** — 36/36 matrix, compliance held |
| 8 | Emergency declared and never ended | **Sweep ends it at expiry** |
| 9 | Caller asserts low risk with critical signals | **Recomputed to critical** |
| 10 | Flood cheap low-risk signals to mask a critical one | **Monotonic — 0 drops in 50** |
| 11 | Approval left unanswered until it times out | **Expires to refusal, not approval** |
| 12 | Two humans resolve one escalation differently | **First wins; second refused** |
| 13 | Subject revokes; platform keeps processing | **Next check denies — no grace period** |
| 14 | Re-grant to erase a revocation | **New record; the revocation stays in history** |
| 15 | Lower a terms version to revalidate old consent | **Refused** |
| 16 | Read Kafka to reconstruct what a person was told | **Codes and fingerprints only** |
| 17 | Read the escalation queue for identifiers | **`ActionLabel` only** |
| 18 | Pass an utterance as an attribute | **Refused — length and line break** |
| 19 | Write a subject identifier into a reason code | **Refused — charset** |
| 20 | Write a short identifier into `Action.Resource` | **Succeeds — R1** |
| 21 | Reload policies mid-decision to change the outcome | **Cannot — one snapshot** |
| 22 | A subsystem stops calling `Decide` | **Undetected — R3** |
| 23 | One person declares a platform-wide emergency | **Succeeds, bounded and audited — R4** |
| 24 | Anything with an `*Engine` rewrites every policy | **Succeeds — R5 + R8** |

**Twenty-one of twenty-four are refused by construction.** The three that are not
are R1, R3 and the R5/R8 pair, and they are the findings.

---

## 5 · DPDP alignment

| Obligation | Mechanism | Gap |
|---|---|---|
| Lawful basis before processing | Consent obligations resolved against the registry; personal writes gated by baseline | — |
| Consent freely given, informed, specific | `TermsVersion`, `Purpose`, `Method` required | **R6** — evidence is a fingerprint |
| Withdrawal as easy as giving | `Revoke`, `RevokeAll` — immediate, no grace period | — |
| Purpose limitation | Scope + `Match` narrowing | Not enforced across bases |
| Notice of what was decided | `Decision.Explanation`, obligations | Operator-facing; not subject-facing |
| Accountability / traceability | Trace, audit, correlation, replay metadata | **R2** — best-effort, in-memory |
| Right to information about processing | `Consent().History` answers what, when, terms, method | — |
| Data minimisation | Fingerprints everywhere; no content fields | **R1** — bounded, not proved |
| Erasure | `ForgetSubject` withdraws every basis | Deletion is Phase 10C's; this withdraws the basis |

**The strongest DPDP position this module has is that it stores almost nothing
about the subject** — consent records and their history, and nothing else. The
personal-data risk sits in what callers put into attributes and resources (R1)
and in the audit store that does not exist yet (R2).

---

## 6 · Verdict

**APPROVE FOR INTEGRATION. NOT APPROVED FOR PRODUCTION.**

The engine's safety posture is structural rather than procedural: it fails closed
in four independent places, it has one door and no bypass path, compliance cannot
be reached by any override, risk cannot be asserted or suppressed, consent
revocation is immediate and unerasable, escalation silence is never consent, and
nothing that leaves the engine can carry content. Twenty-one of twenty-four
attack scenarios are refused by construction.

**Three items block production:**

| | | Owner |
|---|---|---|
| **R1** | Content exclusion is bounded, not proved — `Resource` and short identifiers pass | 10E follow-up |
| **R2** | No durable audit store; the trail is best-effort and in-memory | Phase 10F |
| **R3** | The engine cannot detect its own bypass — INV-GOV-1 is unobservable | Platform monitoring |
| **A2** | Race detector never run, now across five concurrent modules | CI |

Then, in order: **R7** (durable escalation queue), **R4** (two-person emergency
control and a maximum duration), **R5/R8** (authorisation on operator surfaces),
**R6** (consent evidence reconciliation).

**R5 and R8 deserve a closing sentence together.** This engine authorises nobody
and trusts every policy author. It is safe only inside a trust boundary that
something else enforces — and because it is the component that constrains every
other component, a compromise of that boundary is not one incident. It is the
removal of the constraint on all of them.
