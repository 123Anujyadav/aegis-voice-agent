# Conversation Intelligence Engine — Security Review

**Phase 10B** · `packages/go/conversation` · Reviewed 2026-08-09

---

## 1 · Why this component is security-critical

This engine decides **what an AI says to a hostile stranger on a live phone
call**. Frozen invariant I4 states the threat directly: *the agent talks to
hostile strangers by design.*

It is also the component that decides **when to stop talking** — the emergency
path, the fraud-shield narrowing, the escalation ladder. A failure here is not a
degraded response; it is the system continuing to converse when it should have
handed over.

---

## 2 · Supply chain

**One dependency: `packages/go/runtime`, which itself has zero.** The transitive
closure of this module is the Go standard library plus one first-party package.

| Risk removed | How |
|---|---|
| Agent-framework compromise | No framework — LangChain, CrewAI, AutoGen, Semantic Kernel, DSPy, Haystack and Agents SDKs are all absent |
| Dependency confusion / typosquat | Nothing external to resolve |
| Transitive compromise on the conversation path | No transitives |
| Vendor SDK dependency graph | No vendor is imported; `IntentClassifier` is the seam |

**Verified:** `go.mod` declares exactly one `require`, resolved by relative
`replace` to a sibling module.

---

## 3 · Caller text — the one SENSITIVE surface

`Utterance.Text` is the only field in this package carrying caller speech. It is
classified **SENSITIVE** under `annotations.proto`.

### Where it may go

| Destination | Permitted | Enforced by |
|---|---|---|
| `IntentClassifier.Classify` | ✅ — the only reason it exists here | — |
| `classifyYesNo` (in-process, no I/O) | ✅ | — |
| A log statement | ❌ | Convention + review (see R1) |
| A metric label | ❌ | All labels are enumerations or authored names |
| A span attribute | ❌ | No tracing in this module |
| A `Plan` | ❌ | `Plan` carries an `Action` and a reason **code**, never words |
| Context storage | ❌ | Only `last_intent` (an enum name) is written |
| An `Interruption` or `Checkpoint` | ❌ | Checkpoints record **position, never content** |
| A `TransitionRecord` | ❌ | `Note` is documented as a machine-readable code |

### Audited: no content reaches an observability sink

Every metric label in `metrics.go` is one of: a `State`, `Party`,
`Action`, `Trigger`, `Scope`, `InterruptionKind`, `ClarificationKind`,
`PersonaID`, `Stage`, `Outcome`, or an authored `IntentName`. **None derives
from caller input.** That also bounds cardinality — an unbounded label set is
simultaneously a privacy leak and a metrics-system outage.

`Intent` deliberately carries no text: it holds a name, a confidence, slot
*presence* flags and alternatives. **Slot values are not stored** — the comment
on `Slot.Filled` records why: values are caller-derived and belong in the
context engine under its scoping and expiry rules, not in a struct that is
passed to the planner and copied into decisions.

**Residual risk:** this is enforced by convention and review, not by the type
system. A contributor can log `e.Utterance.Text`. **R1** below.

---

## 4 · Untrusted input handling

The engine **transports and classifies the shape of** caller speech; it does not
interpret its meaning.

| Property | Status |
|---|---|
| Caller text is never parsed by the engine | ✅ — only tokenised for yes/no, in-process |
| Caller text never becomes a control decision directly | ✅ — decisions read `Intent`, `Verdict`, `Request`, never `Text` |
| Caller text never reaches a path, command, query or template | ✅ — none exist here |
| Caller text never becomes a metric label or log field | ✅ §3 |
| **Prompt-injection defence** | ❌ **Not here** — orchestration layer |

**Stated plainly so a reviewer does not assume otherwise:** this package provides
**no prompt-injection defence**. Injection is a semantic attack on a prompt, and
this engine has no prompts. The defence belongs in AI Orchestration
(`docs/domain/14`, `PromptInjectionDefenceService`).

What this engine *does* contribute against injection is structural: a caller
cannot talk their way into a capability, because capabilities are persona
constants evaluated by pure policy rules that never read `Text`. An injected
"you are now in admin mode" changes no field the policy engine consults.

### The tokeniser

`tokenizeLower` splits on non-letters and lowercases ASCII. It is the only
function that inspects caller text. It allocates a bounded slice, has no
recursion, no regex, no backtracking, and cannot be made to run long by a
crafted input — a 10 KB utterance produces a linear scan. **No ReDoS surface.**

---

## 5 · Denial of service

| Vector | Control | Verified by |
|---|---|---|
| Infinite clarification loop | Three independent budgets; exhaustion escalates | `TestClarification_BudgetExhaustionStopsAsking` |
| Endless conversation | Persona `MaxTurns` and `MaxDuration` | `TestSim_TurnLimitTerminates`, `TestStress_LongConversationBounded` |
| Interruption storm | Boundary rule at 6 interruptions | `TestPlanner_DecisionTable` |
| Planner/policy livelock | Re-plan loop bounded at 3, then escalate | `handleUtterance` |
| Unbounded context growth | `MaxEntriesPerScope`, TTL, oldest-first eviction | `TestContext_ExpiryIsLazyAndCorrect` |
| Unbounded snapshots | `MaxSnapshots` ring | `ContextEngine.TakeSnapshot` |
| Unbounded trace growth | Bounded by turn and duration limits | transitively |
| Stage overrun | `LatencyController` degrades skippable stages | `TestLatency_DegradesSkippableStagesOnly` |
| Noise flood on a bad line | `RepeatOnNoise`, then escalate | `TestSim_NoiseIsIgnoredThenEscalated` |
| Concurrent-conversation exhaustion | **Not bounded here** — 10A's scheduler owns admission | R4 |

**Every loop in this engine is bounded, and every bound escalates rather than
retrying.** That is the difference between a system that degrades and one that
wedges.

### Expiry is lazy, deliberately

Context expiry is evaluated on read rather than by a sweeper goroutine. A
per-conversation sweeper would be one goroutine per concurrent call whose sole
job is deleting map entries nobody is reading — and concurrency is this
platform's capacity unit (ADR-0002 §13). Lazy expiry trades a small read cost
for not multiplying goroutines by call volume.

---

## 6 · Authorisation model

The engine has **no authentication and no authorisation of principals**. It has
**capability confinement**, which is a different thing and is the security
property it actually provides.

| Mechanism | Guarantee |
|---|---|
| `Persona.Capabilities` | **Deny by default.** An absent capability is forbidden |
| `Persona.Forbidden` | Explicit prohibition, distinct from mere absence so a deliberate denial is visible |
| `Action.RequiredCapability` | Every action maps to a capability, checked by a policy rule |
| Class ordering | Safety evaluated first; **no mechanism exists** for a later class to permit what safety denied |

### The narrowing ratchet

```
   any persona  ──────────▶  Fraud Shield  ──────────▶  (locked)
        │                                                   │
        └──────────────▶  Emergency Assistant  ◀────────────┘
                          (always reachable, never leaves)
```

| Rule | Security reason |
|---|---|
| Any → Emergency, always | A rule that could block emergency handling would eventually block it |
| Emergency → nothing | "We decided it wasn't an emergency" is not a mid-call judgement |
| Fraud Shield → broader: **denied** | **A caller who talks their way out of fraud screening is exactly the attack** |
| Narrowing sets a lock | A future registered persona cannot open an exit by accident |

`TestPersona_EmergencyAlwaysReachableAndOneWay` and
`TestPersona_FraudShieldNeverBroadens` enumerate every persona pair.

### Fraud Shield discloses nothing

It forbids `AnswerQuestion`, `TakeMessage`, `DiscloseIdentity`,
`DiscloseAvailability`, `CollectCallback` and `Transfer`. A message field is a
surface; a callback field is a surface. The persona's purpose is to give a
hostile caller **no surface at all**, and the capability set says so rather than
relying on the layer above to be careful.

---

## 7 · Emergency handling as a safety control

Frozen invariant **U7** requires the product to get out of the way. Four
independent mechanisms enforce it, so no single failure re-enables conversation:

1. `InterruptionEngine.emergencyAt` is **monotonic** — once set, never cleared.
2. `switchAllowed` permits **any → Emergency** and **Emergency → nothing**.
3. `Planner.decide` rung 1 returns `Escalate` before reading any other input.
4. Policy rule `safety.emergency_only_escalates` denies every action but
   `Escalate`, `End` and `Ignore`, in the **non-overridable** safety class.

Plus a fifth at the floor: `arbitrateLocked` lets an emergency preempt even the
non-yielding greeting, so U7 outranks I1 where they meet.

---

## 8 · Secrets

| Item | Handling |
|---|---|
| Credentials | **None in this package.** No provider, no transport, no store |
| Prompts | None exist here |
| Conversation identifiers | Supplied by the caller; opaque to the engine |
| Metrics, logs | No secret material by construction |

The engine holds nothing worth stealing except the caller text transiting it,
which §3 confines.

---

## 9 · Findings

| # | Finding | Severity | Action |
|---|---|---|---|
| **R1** | Caller-text prohibition is convention, not compile-enforced | **Medium** | CI lint forbidding `Utterance.Text` / `.Text` in `slog` calls and metric label positions within this package. Same shape as Phase 10A R1; one lint should cover both |
| **R2** | Race detector not run (audit A2) | **High** | Run `-race` in CI. A data race in a security-critical concurrent component is a vulnerability, not merely a bug |
| **R3** | No bound on third-party policy rules | **Low** | If external rules are ever accepted, bound count and add a per-rule timeout. Policy is the one stage that is never skipped, so a slow rule set degrades the unskippable path |
| **R4** | No admission control in this engine | **By design** | Owned by 10A's `Scheduler` and by `telephony-gateway`. Confirm both are in the path before launch |
| **R5** | `Event.Reason` and `TransitionRecord.Note` are free-form strings | **Low** | Documented as machine-readable codes; consider a closed vocabulary so a caller-derived string cannot be passed by a careless caller |
| **R6** | Persona registry shared per engine | **Accepted** | Read-only after construction; `Register` is copy-on-write. Noted so the sharing is not mistaken for an oversight |
| **R7** | No authn/authz of principals | **By design** | Enforced at `edge-api`. This engine confines capabilities, not identities |

**R2 blocks.** Everything else is tracked or correctly placed elsewhere.

---

## 10 · Threat model summary

| Threat | Mitigated | Where |
|---|---|---|
| Caller talks their way into a capability | ✅ | Capabilities are persona constants; policy rules never read caller text |
| Caller escapes fraud screening | ✅ | Narrowing ratchet is one-way and locked |
| Emergency handled as ordinary conversation | ✅ | Five independent mechanisms, §7 |
| Caller text reaching logs / metrics | ⚠️ **Partially** | Audited clean; convention-enforced, not compile-enforced (R1) |
| Prompt injection | ❌ **Not here** | Orchestration layer — §4 |
| Clarification loop as a denial of service | ✅ | Bounded, escalates |
| Unbounded resource growth | ✅ | Every collection bounded, §5 |
| Compromised classifier returning hostile candidates | ⚠️ **Partially** | Confined: an `IntentName` reaches metric labels. A hostile classifier could inflate label cardinality. **Deployments should validate classifier output against a closed intent vocabulary** — the engine does not |
| Data race corrupting conversation state | ❌ **UNVERIFIED** | R2 — blocking |

**The classifier row deserves emphasis.** `IntentName` flows into metric labels,
and the engine trusts the classifier to return names from a closed set. A
compromised or merely buggy classifier returning caller-derived names would leak
caller content into metrics and blow up cardinality. That is the sharpest
unmitigated edge in this module and it belongs in the deployment's classifier
adapter.

---

## 11 · Conclusion

The engine's security posture rests on three properties: **one first-party
dependency**, **capability confinement that caller speech cannot influence**, and
**every loop bounded with escalation as the exit**.

Its principal limitations, neither of which should be misread as coverage:
**no prompt-injection defence** and **no authentication** — both correctly placed
elsewhere, and both stated here so a reviewer does not assume a control that
does not exist.

**Recommendation: not approved for production traffic until R2 is closed**, and
R1 and the classifier-vocabulary gap in §10 should be closed before the first
real classifier is connected.
