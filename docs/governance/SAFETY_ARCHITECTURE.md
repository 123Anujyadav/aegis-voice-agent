# Enterprise Safety, Policy & Governance Engine — Architecture

**Phase 10E** · `packages/go/governance` · Status: **PROPOSED — awaiting approval**

The final authority on whether the platform may act. Built from scratch on the
Go standard library and the frozen Phase 10A runtime.

---

## 1 · One door

Every conversation decision, every tool execution, every memory write and every
external action passes through **`Engine.Decide`**. One entry point, one input
type ([`Request`](../../packages/go/governance/action.go) wrapping an `Action`),
one output type (`Decision`).

That is the whole architecture, and it is what makes "no subsystem may bypass
this engine" enforceable rather than aspirational.

| Five typed entry points | One door |
|---|---|
| Five rules to enforce | One rule |
| Five sets of policy-matching code | One evaluator |
| Each grows its own special cases | No special cases to grow |
| The sixth caller quietly gets its own | The sixth caller uses the same door |

**A bypass is a missing call** — visible in review, and countable in production:
`Engine.Decisions()` against each subsystem's own action count. A subsystem that
took more actions than it asked about has a bypass, and the arithmetic says so.

---

## 2 · It imports nothing it governs

```
   conversation (10B) ─┐
   memory       (10C) ─┼──▶ Engine.Decide(Request) → Decision
   toolruntime  (10D) ─┘              │
                                      │ imports
                            ┌─────────▼─────────┐
                            │  runtime (10A)    │  Clock only
                            └─────────┬─────────┘
                                      │
                                ┌─────▼──────┐
                                │  Go stdlib │
                                └────────────┘
```

`go list -deps` reports `runtime` and the standard library. Nothing else.

The callers do not appear in this module's dependency graph — not merely because
the import cycle would not compile, but because **a governance engine that knows
the shape of its callers acquires a special case per caller**, and a special case
per caller is how something eventually gets an exemption.

---

## 3 · No external safety framework

Verified absent: OpenAI Moderation · Anthropic Safety · Gemini Safety ·
LangChain Guardrails · Guardrails AI · NeMo Guardrails · Llama Guard ·
Microsoft Prompt Shields.

`go.mod` has one `require` line, first-party. That is checkable in one command.

**This is not framework avoidance for its own sake.** A safety decision made by
a remote service is a safety decision with an availability dependency and a
latency budget, and the honest failure mode of "the moderation endpoint timed
out" is either shipping unreviewed output or dropping a call. This engine
decides locally, in **12 µs**, from policies an operator can read.

---

## 4 · Policies are data; evaluation is a pure function

`Evaluator.Evaluate(snapshot, request, instant) → Decision`

No I/O. No mutation. No hidden state. **No clock of its own** — the instant is a
parameter.

Everything the brief asks for falls out of that one property:

| Requirement | How it follows |
|---|---|
| **Deterministic** | Same inputs, same output, forever |
| **Every decision traceable** | A pure function over declared rules can name which rule decided |
| **Every denial explainable** | The reason is in the rule that fired |
| **Replayable** | The snapshot version travels in the decision |
| **Testable** | The whole evaluator suite runs with no clock, no broker, no store |

`Engine.Evaluator()` exports a copy, so a policy-authoring tool or a what-if
console can evaluate with **no side effects at all** — no metrics, no audit, no
events.

---

## 5 · The default is deny

An engine with no policies loaded denies everything. `Config.Default` exists so
the choice is visible in a configuration review, and **validation refuses
anything but `OutcomeDeny`**.

`Outcome`'s zero value is also `OutcomeDeny`, so a `Decision` that was never
populated — dropped by a bug, lost in a struct copy — refuses rather than
permits.

The consequence is deliberate and slightly uncomfortable: **a runtime that boots
with an empty registry cannot do anything at all.** That is the correct
discomfort. It converts "we forgot to load policies" from a silent security
incident into a loud outage.

`BaselinePolicies()` is an explicit call rather than a default, for the same
reason: a safety engine that ships with permissive defaults is one whose most
common production configuration was never reviewed by anybody.

---

## 6 · Ten outcomes, not two

| Outcome | Severity | Meaning |
|---|---:|---|
| **Deny** | 5 | Refused. Refusing is always safe |
| **RequireSupervisor** | 4 | An accountable human, before anything happens |
| **RequireHuman** | 3 | Any authorised human |
| **RequireConsent** | 2 | A legal precondition |
| **RequireConfirmation** | 2 | A user precondition |
| **Escalate** | 2 | Route to review |
| **RetryLater** | 1 | Fine, but not now |
| **Queue** | 1 | Fine, asynchronously |
| **Defer** | 1 | Fine, but not on the request path |
| **Allow** | 0 | Proceeding is never the safe side of a disagreement |

**A system that can only say yes or no says no to things it should have asked
about**, and callers learn to route around it.

Consent, confirmation and escalation share rank 2 because none dominates: they
are three different preconditions, and a request carrying two must satisfy
**both**. That is why `Decision.Obligations` is a set rather than a field.

Only `Allow` returns true from `Permits()`. Queue and Defer do permit the action
eventually, and still return false, because a caller asking "may I do this now"
must get a straight answer.

---

## 7 · Nine scopes, one fixed precedence

| # | Scope | Overridable | Why here |
|---:|---|---|---|
| 0 | **Compliance** | **No** | Legal. Changed by a lawyer, not by an incident |
| 1 | **Emergency** | — | Bounded, attributed incident override |
| 2 | **Global** | Yes | Platform safety floor, above tenants |
| 3 | Organization | Yes | A tenant's own rules |
| 4 | Business | Yes | One business within a tenant |
| 5 | User | Yes | A subscriber's preferences as policy |
| 6 | Session | Yes | "Don't record this call" |
| 7 | Temporary | Yes | Below session: a migration must not override what a person asked for |
| 8 | FeatureFlag | Yes | Lowest. A flag may restrict; it can never be the reason something dangerous happened |

**Global sits above Organization** so a tenant cannot configure below the
platform floor.

`Scope.Overridable()` is a **method, not a configuration field**. A configurable
"can compliance be overridden" flag is a flag that gets set to true during an
incident.

---

## 8 · Compliance cannot be overridden

Emergency overrides exist because **systems that cannot be overridden get
overridden anyway** — at 3 a.m., by somebody with database access, leaving no
record. Making the path explicit moves an action that was going to happen anyway
into somewhere it can be bounded and reviewed.

Structural limits, all refused at construction rather than advised:

| Rule | Enforced by |
|---|---|
| Must expire | `ExpiresAt` required |
| Must name a human | `AuthorisedBy` required |
| Must state a reason | `Reason` required |
| Must install something | Empty policy list refused |
| **May not target compliance** | Listing `ScopeCompliance` is a configuration error |
| Installs emergency-scope policies only | A non-emergency policy is refused |

An emergency **is** its policies: activating one registers them, so the
relaxation appears in the trace as an ordinary emergency-scope policy rather
than through a side channel the trace cannot see. `Sweep` ends expired
emergencies whether anybody remembered or not — which is the entire reason
`ExpiresAt` is mandatory.

Measured: **7 of 7 overridable scopes relaxed, compliance held** —
GOVERNANCE_EVALUATION §E3.

---

## 9 · Consent

Four distinct negative outcomes, because the distinction decides whether the
platform asks the subject again — and it is legal as much as technical:

| Outcome | Meaning | What a caller does |
|---|---|---|
| `ErrConsentNotFound` | Never asked | Ask |
| `ErrConsentExpired` | Agreed, lapsed | Ask again |
| `ErrConsentRevoked` | **Said no** | Do not ask again casually |
| `ErrConsentSuperseded` | Agreed to different terms | Ask about the new terms |

**Revocation is immediate.** No cache, no grace period. A subject who withdraws
consent has withdrawn it, and a system that keeps processing for another five
minutes is processing without a basis for five minutes.

**Grants are append-only in effect.** Every grant mints a new record and the
previous one moves to history, so "when did they consent, under which terms, by
what method" has an answer for every point in time. Three edges deliberately do
not exist — `Revoked → Granted`, `Expired → Granted`, `Superseded → Granted` —
because re-consenting mints a **new** record and a revocation that a later grant
can erase is a revocation nobody can prove happened.

Raising a basis's terms version supersedes every older record. That is
deliberately disruptive: a platform that could carry old consent across changed
terms would have no reason ever to ask again.

Full lifecycle: [CONSENT_LIFECYCLE.md](CONSENT_LIFECYCLE.md).

---

## 10 · Risk is a framework, not a model

This module runs **no fraud model, no anomaly detector and no scoring
heuristic**. A `Signal` is a fact another phase asserts; this engine combines
facts deterministically and explains the combination.

**Four bands, not a score.** A continuous score invites threshold-tuning as a
substitute for policy, and "deny above 0.73" is a rule nobody can review.

**Aggregation is monotonic** (INV-GOV-11): adding a signal never lowers the
level. A non-monotonic aggregator — where a reassuring signal cancels an
alarming one — is a system where an attacker who can produce cheap reassuring
signals suppresses expensive alarming ones. Reassurance belongs in policy, where
the trace shows it.

**The caller cannot assert a level.** `Decide` recomputes the aggregate from the
signals, so a compromised subsystem cannot declare itself low-risk.

**Thresholds only raise.** A risk signal that could lower an outcome would let a
detector overrule a written policy, inverting the whole precedence model.

Measured: **50 signals added, 0 aggregate drops; risk loosened a denial 0 times
at every level** — GOVERNANCE_EVALUATION §E6.

---

## 11 · Privacy expresses itself as obligations

Rather than a separate enforcement path with its own precedence, privacy rules
produce `Obligation` values on the one decision every action already passes
through. That is what stops a business policy silently overriding a retention
rule.

`Detector`, `Classifier` and `Masker` are **interfaces with no real
implementation here**. PII detection is a model problem — language-specific,
script-specific, and for an India-first platform involving Aadhaar, PAN, UPI
handles and eleven scripts. **A regular expression that catches 60% of phone
numbers is worse than nothing, because it teaches everyone downstream to trust
it.**

Retention mirrors Phase 10C exactly: standard is 90 days, matching ADR-0012.
A governance layer whose retention differs from the data it governs will be
overruled by whichever number is shorter.

---

## 12 · Human override

Five escalation kinds, because "a human should look at this" is five different
requests with five different response times and five different people:
confirmation (30 s) · approval (5 min) · supervisor (15 min) · takeover (60 s) ·
business (4 h).

Three properties worth stating:

**Raising is idempotent per decision.** Two entries for one decision means two
humans asked to approve one thing, and the second approval approves something
already decided.

**The first resolution wins.** A second is refused with `ErrAlreadyResolved`.
Two humans resolving one escalation differently is a race with a real-world
consequence.

**Expiry resolves to a refusal, never an allowance.** Letting an unanswered
escalation time out into approval turns every approval gate into a delay, which
is the single most common way an approval control is quietly defeated.

---

## 13 · Invariants

| # | Invariant | Enforced by |
|---|---|---|
| **INV-GOV-1** | One entry point; no bypass path exists | `Decide` is the only decision function |
| **INV-GOV-2** | The default is deny, and the zero `Outcome` is deny | `Config.validate`, the constant's position |
| **INV-GOV-3** | Compliance cannot be overridden | `Scope.Overridable()`, `Emergency.validate` |
| **INV-GOV-4** | Consent state changes follow the declared table | `canTransition` |
| **INV-GOV-5** | Every decision names a deciding policy and a bounded reason | `Rule.validate`, `checkReasonCode` |
| **INV-GOV-6** | Every policy consulted appears in the trace | `evaluateScope` |
| **INV-GOV-7** | Decisions, events and audit carry fingerprints and codes, not content | No content field; `Validator`, `checkReasonCode` |
| **INV-GOV-8** | Evaluation is pure — no I/O, no mutation, no clock | `Evaluator.Evaluate` signature |
| **INV-GOV-9** | A decision is made against exactly one snapshot | Copy-on-write registry |
| **INV-GOV-10** | An engine starts once | `Engine.Start` |
| **INV-GOV-11** | Risk aggregation is monotonic; thresholds only raise | `Aggregator.Aggregate`, `Thresholds.Apply` |
| **INV-GOV-12** | No global mutable state | Construction — everything is engine-owned |

**Most are enforced by absence or by construction** — a missing import, a
missing field, a missing constructor, a single decision path. Enforcement by
absence cannot be forgotten, misconfigured, or switched off during an incident.

---

## 14 · Determinism

| Source | Closed by |
|---|---|
| Wall clock | The instant is a parameter to `Evaluate` |
| Map iteration | Sorted: attribute keys, scope order, obligations, sweeps, conflicts |
| Ordering ties | Every comparator falls back to a stable identifier |
| Policy ordering | Pre-sorted per scope at registration; the read path never sorts |

Measured: **50 runs, 0 outcome / 0 trace / 0 obligation divergences** —
GOVERNANCE_EVALUATION §E5.

---

## 15 · Testing

| Suite | Count |
|---|---|
| Unit (`governance_test.go`) | **53** |
| Integration, concurrency, stress, failure injection (`integration_test.go`) | **20** |
| Evaluation (`eval_test.go`) | **9** |
| Benchmarks (`bench_test.go`) | **20** |

**82 tests, 10,123 lines** (7,337 source + 2,786 test). `gofmt` clean · `go vet`
clean · passes `-count=5 -shuffle=on` · all of 10A–10D still pass.

**Not verified: `-race`** — no C toolchain locally. ENGINEERING_AUDIT §A2, now
applying to **five** concurrent modules and blocking.

---

## 16 · Deliberate omissions

| Excluded | Evidence |
|---|---|
| Fraud detection models | No model, no scoring heuristic; `Signal` is an input |
| Telephony intelligence | No import, no type |
| Conversation prompts | None |
| LLM safety APIs | No provider named anywhere |
| Business logic | Capabilities, operations and bases are opaque strings |
| Payment policies | None |
| External vendor policies | None |

Also absent by design: policy serialisation (policies are Go values — audit
§A5), a durable audit store, Kafka, Redis, Aurora and the Python utilities —
ENGINEERING_AUDIT §A3, where the gap is a stated decision rather than an
omission.
