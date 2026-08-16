# 0 · Strategic Design

The context map, subdomain classification, integration patterns, the shared
kernel, and how eleven domain contexts sit on four persistence contexts.

---

## 0.1 Subdomain classification

Not all contexts deserve equal care. Classification decides where the best
modelling effort goes and where a conventional solution is correct.

| Subdomain | Contexts | Meaning |
|---|---|---|
| **Core** | Telephony · AI Orchestration · Fraud Detection | Where the product is differentiated. Model with maximum rigour. Never outsource, never simplify for convenience |
| **Supporting** | Consumer · Voice · Business · Notifications · Administration | Specific to us and necessary, but not where we win. Model carefully, build pragmatically |
| **Generic** | Identity · Billing · Analytics | Solved problems. Use standard patterns, buy where sensible, resist the urge to be clever |

### Why each

**Telephony is core** because ADR-0002 is the load-bearing decision of the
entire platform. Carrier-side screening with an on-device pre-filter is the only
architecture that can converse on a stock handset, and every economic and
regulatory property of the business follows from it. Nobody else can copy it
without repeating the same reasoning.

**AI Orchestration is core** because the quality of a twenty-second conversation
with a stranger *is* the product. The four-tier ladder, the announcement
guarantee, and the injection defence are not infrastructure.

**Fraud Detection is core** because it is the second promise and the primary
reason a subscriber upgrades. Its differentiator is not the model — it is
**calibrated confidence with retrievable evidence**, which is a domain
discipline rather than a modelling one.

**Identity is generic despite being statutory.** MSISDN + OTP + device
attestation is a solved shape. What is *not* generic is the consent model, which
is why `ConsentRecord` is specified with unusual precision inside an otherwise
conventional context.

**Analytics is generic and deliberately impoverished.** Its most important
domain service rejects things. A rich analytics domain would be a privacy
failure.

---

## 0.2 The context map

```
                    ┌────────────────────────────────────────┐
                    │            IDENTITY                    │
                    │   Subscriber · Device · Consent        │   Open Host Service
                    │            [generic]                   │   Published Language
                    └───────┬───────────────────────┬────────┘
                            │ U                     │ U
             ┌──────────────┴──────┐         ┌──────┴──────────────┐
             │ D  (conformist)     │         │ D  (conformist)     │
             ▼                     ▼         ▼                     ▼
    ┌─────────────────┐   ┌─────────────────────────┐   ┌──────────────────┐
    │    CONSUMER     │   │       BUSINESS          │   │     BILLING      │
    │  Preferences    │◀──┤ Organisation · Line     │   │ Subscription     │
    │  CallerList     │ SK│ Routing · Contact · Key │   │ Entitlement      │
    │  AssistantProf. │   │      [supporting]       │   │    [generic]     │
    │  [supporting]   │   └──────────┬──────────────┘   └────────┬─────────┘
    └────────┬────────┘              │ C/S                       │ OHS
             │ C/S                   │                           │ entitlements
             │                       ▼                           │ published to all
             │            ┌──────────────────────────┐           │
             │            │       TELEPHONY          │◀──────────┘
             └───────────▶│  Line · Forwarding · DID │
              pre-filter  │  CallSession · Outcome   │
              SHARED      │        [CORE]            │
              KERNEL      └───┬──────────────────┬───┘
                              │  PARTNERSHIP     │  PARTNERSHIP
                              ▼                  ▼
                  ┌───────────────────┐  ┌────────────────────────┐
                  │      VOICE        │  │   AI ORCHESTRATION     │
                  │ ASR · TTS         │◀─┤ ScreeningConversation  │
                  │ Recording         │C/S│ Transcript · Summary  │
                  │  [supporting]     │  │ Announcement · Prompt  │
                  └─────────┬─────────┘  │ PersonalAssistant      │
                            │            │        [CORE]          │
                        ACL │            └───────────┬────────────┘
                            ▼                        │ C/S
                  ┌───────────────────┐              ▼
                  │ ASR / TTS vendors │   ┌────────────────────────┐
                  │ Google · Deepgram │   │   FRAUD DETECTION      │
                  │ ElevenLabs·Sarvam │   │ Assessment · Reputation│
                  └───────────────────┘   │ Case · Pattern         │
                                          │        [CORE]          │
                                          └───────────┬────────────┘
                                                      │ C/S (published verdict)
             ┌────────────────────────────────────────┴──────────┐
             ▼                                                   ▼
   ┌──────────────────────┐                          ┌──────────────────────┐
   │    NOTIFICATIONS     │                          │      CONSUMER        │
   │  [supporting]        │                          │  (verdict displayed) │
   │  CONFORMIST to all   │                          └──────────────────────┘
   └──────────────────────┘

   ┌──────────────────────────────┐   ┌──────────────────────────────────┐
   │        ANALYTICS             │   │        ADMINISTRATION            │
   │ CONFORMIST + hard ACL        │   │ CONFORMIST + redaction ACL       │
   │ (classification guard)       │   │ (break-glass) over every context │
   │        [generic]             │   │        [supporting]              │
   └──────────────────────────────┘   └──────────────────────────────────┘

   U = upstream · D = downstream · C/S = customer–supplier
   SK = shared kernel · ACL = anticorruption layer · OHS = open host service
```

---

## 0.3 Relationship patterns, stated

Every pair that communicates has exactly one declared pattern. An undeclared
relationship is how a distributed monolith starts.

| Upstream | Downstream | Pattern | Why this pattern |
|---|---|---|---|
| Identity | All contexts | **Open Host Service + Published Language** | Everyone needs "who is this subscriber and what did they consent to". A published, versioned language prevents eleven bespoke integrations |
| Billing | All contexts | **Open Host Service** | `Entitlement` is consumed everywhere. Published as a language so no context re-derives entitlement logic |
| Telephony | AI Orchestration | **Partnership** | They co-evolve on the hot path under one latency budget. Neither can change the turn contract unilaterally |
| Telephony | Voice | **Partnership** | Media path and session lifetime are shared concerns; ADR-0004 binds them |
| AI Orchestration | Voice | **Customer–Supplier** | AI requests speech and receives utterances. Voice serves AI's needs and is not shaped by AI's internals |
| AI Orchestration | Fraud Detection | **Customer–Supplier** | Fraud consumes finalised turns. AI does not depend on Fraud's model |
| Fraud Detection | Consumer | **Customer–Supplier** | The published verdict is the contract. Consumer never reaches into scoring |
| Fraud Detection | Notifications | **Customer–Supplier** | The alert-worthiness rule belongs to Fraud; delivery belongs to Notifications |
| Telephony | Consumer | **Customer–Supplier** | Call outcomes flow one way |
| Consumer | Telephony | **Shared Kernel** | The on-device pre-filter is a Consumer decision executed inside a Telephony flow. `PhoneNumber`, `ListType` and `PreFilterDecision` are jointly owned |
| Business | Telephony | **Customer–Supplier** | A `BusinessLine` references a Telephony `Line`. Routing is resolved by Business and executed by Telephony |
| Business | Identity | **Conformist** | Business accepts Identity's `Subscriber` as given |
| Notifications | Everything | **Conformist** | Notifications adapts to whatever upstream publishes. It never asks an upstream context to change |
| Analytics | Everything | **Conformist + hard ACL** | Consumes events, and its anticorruption layer *rejects* anything carrying personal data (U11) |
| Administration | Everything | **Conformist + redaction ACL** | Reads across contexts; its ACL redacts by default and requires an `AccessGrant` to reveal (U12) |
| Carrier / CPaaS | Telephony | **Anticorruption Layer** | Exotel and Plivo have different models of a call. ADR-0003 requires a provider port; the ACL is that port |
| ASR / TTS vendors | Voice | **Anticorruption Layer** | Three ASR and three TTS vendors with incompatible streaming models (ADR-0005, ADR-0007) |
| LLM provider | AI Orchestration | **Anticorruption Layer** | Tier ladder and tool protocol are ours; the provider's shape does not leak into the domain |
| Payment provider | Billing | **Anticorruption Layer** | No payment instrument data enters our domain at all |
| Identity | Analytics | **Separate Ways** | Analytics never learns who anyone is. This is the point |

### The two Partnerships, and why they are not Customer–Supplier

Telephony↔AI and Telephony↔Voice are the only Partnerships in the map, and the
designation is a real commitment: **neither side may change the turn contract
without the other**. They share a p95 latency budget of 1500 ms allocated per
hop (ADR-0011), and a unilateral change to endpointing, barge-in or turn
boundaries breaks the budget for everyone.

Everywhere else, upstream can evolve and downstream conforms. On the hot path,
that would be a defect.

---

## 0.4 The shared kernel

Deliberately small. A shared kernel is a coordination cost paid by every team
that touches it, so admission is restricted.

### Tier 1 — platform primitives (already frozen)

From `contracts/proto/callscreen/common/v1`. Owned by `callscreen/platform`.
Nothing here has business meaning.

`ResourceId` · `Money` · `Region` · `Environment` · `Platform` ·
`PageRequest`/`PageResponse` · `ClientContext` · `RequestMetadata` ·
`AuditContext` · the classification annotations (`Sensitivity`,
`RedactionStrategy`, `Retention`).

### Tier 2 — domain value objects genuinely shared

Each admitted only because at least two contexts must agree on its *meaning*,
not merely its shape.

| Value object | Shared by | Why it cannot be duplicated |
|---|---|---|
| `PhoneNumber` | Identity · Consumer · Telephony · Fraud · Business | Normalisation and equality must be identical everywhere. Two definitions means a blocked number that is not blocked |
| `RiskLevel` | Fraud · Consumer · Notifications · AI | The user-visible judgement. Five values, fixed by the design system's `RiskIndicator`. A sixth value invented in one context would be unrenderable |
| `Confidence` | Fraud · Consumer · Notifications | Low must never render as high (U4). One definition, one calibration |
| `ConsentPurpose` | Identity · Consumer · Voice · Analytics · Business | A purpose that means different things in two contexts is not a lawful consent |
| `DisclosureScope` | Consumer · AI Orchestration | Consumer sets it; AI enforces it. Divergence is a PII leak (I4) |
| `ModelTier` | AI Orchestration · Administration · Analytics | ADR-0006's four-tier ladder is a fixed vocabulary |
| `Sensitivity` / `Retention` | Every context | Already frozen; restated here because every entity attribute carries one |

### Explicitly not in the shared kernel

| Rejected | Where it lives instead |
|---|---|
| `CallSession` | Telephony only. Consumer holds a `CallHistoryEntry` projection, which is a different concept with different rules |
| `Transcript` | AI Orchestration only. Others hold references |
| `Subscriber` | Identity only. Others hold a `SubscriberId` |
| `Entitlement` | Billing only, exposed as a Published Language — consumed, not co-owned |
| `FraudAssessment` | Fraud only. Consumer receives a published verdict projection |
| Anything with a lifecycle | A shared kernel entity is a shared database by another name |

**Rule:** the shared kernel contains value objects and classifications. **No
entity, no aggregate, and nothing with a lifecycle is ever admitted.**

---

## 0.5 Context boundaries that were contested

Recorded so they are not silently re-drawn.

### Where does the call history live?

**Telephony owns `CallSession`.** Consumer owns `CallHistoryEntry`, which is a
**read model** assembled from Telephony, AI Orchestration and Fraud events — and
is explicitly not an aggregate and never the source of truth for any decision
(INV-CO-4).

*Rejected:* Consumer owning the call. It would make the consumer app's read
model authoritative for telephony operations, which is backwards, and it would
put a subscriber-facing concern on the hot path.

### Where does emergency detection live?

**AI Orchestration**, not Fraud Detection. Emergency intent is a classification
of the conversation with a *control-flow consequence* — terminate and hand over
(U7). Fraud produces a judgement for display; emergency produces an immediate
transfer of control. They are different domain concepts that happen to both be
classifications.

`RiskLevel.EMERGENCY` exists in the shared kernel for presentation, populated by
AI Orchestration, and Fraud never assigns it.

### Where do prompts and evaluations live?

**AI Orchestration owns `PromptTemplate`, `PromptRollout` and `EvaluationRun`.**
Administration provides the operator surface and issues the commands.

*Rationale:* the invariants that guard prompts are AI-domain invariants —
the announcement cannot be model-generated (I1), thinking stays enabled on
tool-calling tiers (I3), a rollout requires a passing evaluation. Putting the
data in Administration would put those invariants in a context that does not
understand them, and enforcement would degrade to a UI check.

### Where does the on-device pre-filter live?

**Consumer**, as a domain service (`PreFilterDecisionService`) that executes
**on the handset**. This is the only domain service in the platform whose
execution location is part of its specification, and it is part of the
specification because ADR-0002 §5 makes on-device evaluation the reason the unit
economics work — a pre-filter that requires a server round trip is a different,
worse product.

Its inputs cross a boundary (Telephony supplies the inbound number, Fraud
supplies a cached reputation), which is why `PreFilterDecision` is a Shared
Kernel value object rather than a Consumer-private one.

### Where does verified business caller identity live?

**Business**, as `VerifiedBusinessIdentity` — a registry entity distinct from
`Organisation`. A verified business may call our subscribers without being our
customer, so it cannot hang off `Organisation`.

This is the only place in the platform where a caller-supplied name is rendered
at title weight, and only because it came from a verified registry rather than
from the caller's mouth (`UX_FREEZE` / `02 §2.6`).

---

## 0.6 Consistency boundaries

DDD's hardest rule, and the one most often ignored: **one aggregate per
transaction.** Everything else is eventual, mediated by events.

| Must be transactionally consistent | May be eventually consistent |
|---|---|
| A `Subscriber` and its `Device` set (enrolment revokes the previous device) | A `Subscriber` and their `Subscription` |
| A `ConsentRecord` append and the consent state it produces | A consent withdrawal and the deletion it triggers |
| A `Line` and its `ForwardingConfiguration` | A `Line` and its `CallSession` history |
| A `DID` allocation and its owning `Line` | A `CallSession` and its `Transcript` |
| A `CallSession` and its legs | A `CallSession` and its `FraudAssessment` |
| A `FraudAssessment` and its evidence references | A `FraudAssessment` and the `CallHistoryEntry` showing it |
| An `Organisation` and its `Membership` set (exactly one owner) | An `Organisation` and its `BusinessContact` set |
| A `Subscription` and its `Entitlement` set | An `Entitlement` change and every context observing it |
| An `AccessGrant` and its `AuditEntry` | An `AuditEntry` and any report over it |
| An `Invoice` and its line items | Usage recording and invoicing |

### The gaps we accept, named

ADR-0009 §7 accepts eventual consistency between contexts. These are the
specific gaps, and each has a defined behaviour in the window:

| Gap | Window behaviour |
|---|---|
| Plan cancelled → entitlement observed elsewhere | Contexts hold the last-known entitlement and fail **closed** on premium capability, **open** on `RiskVerdictVisible` (INV-BI-6) |
| Consent withdrawn → data deleted | The capability stops immediately; deletion completes asynchronously and is confirmed to the subscriber with counts |
| Device revoked → sessions invalid | Access tokens are 15 minutes (ADR-0010); immediate revocation is achieved by a revocation check at validation, not by aggregate consistency |
| Call ends → transcript finalised | The `CallHistoryEntry` shows the call with a pending transcript. Never an empty entry, never a missing one |
| Assessment published → notification sent | Late alerts are suppressed past a staleness threshold rather than delivered as stale news |
| Member removed → routing target invalid | Routing falls back and raises an alert; the routing screen shows the broken target rather than pretending it resolved (P-BU-7) |

---

## 0.7 Domain contexts and persistence contexts

**ADR-0009 fixes four Aurora clusters and calls them bounded contexts. This
document defines eleven bounded contexts. Both are correct, at different
granularities, and the reconciliation is recorded here rather than left to be
discovered.**

ADR-0009's four are **persistence contexts** — units of physical isolation
chosen so that a cross-boundary `JOIN` is impossible rather than merely
discouraged (§6). This document's eleven are **domain contexts** — units of
linguistic and modelling consistency.

A domain context may share a cluster with another; what it may never do is read
another context's schema. That rule is unchanged, and it is enforced the same
way ADR-0009 enforces it: **separate schemas with separate credentials.**

| Domain context | Durable store | Schema | Ephemeral | Objects |
|---|---|---|---|---|
| Identity | `identity` Aurora | `identity` | Redis (challenges, revocation list) | — |
| Consumer | `identity` Aurora | `consumer` | Redis (pre-filter cache) | — |
| Telephony | `telephony` Aurora | `telephony` | Redis (live session state) | — |
| Voice | `content` Aurora | `voice` | Redis (stream state) | **S3** (audio) |
| AI Orchestration | `content` Aurora | `content` | Redis (conversation state) | S3 (transcript artefacts) |
| Fraud Detection | `content` Aurora | `fraud` | Redis (reputation cache) | — |
| Business | `identity` Aurora (org, membership) · `telephony` Aurora (line, routing) | `business_*` | — | — |
| Billing | `billing` Aurora | `billing` | — | — |
| Notifications | `identity` Aurora | `notifications` | Redis (delivery state) | — |
| Analytics | **None in production** | — | — | Separate analytical store, fed from Kafka (ADR-0009 §14 step 4) |
| Administration | `identity` Aurora (operator, grant) · dedicated audit store | `admin` | Redis (active grants) | — |

**Two consequences worth stating.**

**Business spans two clusters.** `Organisation` and `Membership` are identity-
shaped; `BusinessLine` and `RoutingPolicy` are telephony-shaped. A single
transaction cannot span them, so adding a line to an organisation is a saga, not
a transaction — and [16 §16.13](16-business.md) specifies its compensations.

**Analytics has no production store, by design.** ADR-0009 §14 closes the path
from production databases to reporting deliberately. Analytics is a consumer of
Kafka and nothing else, which is also what makes INV-AN-1 achievable.

---

## 0.8 Event-carried identifiers, not content

Invariant I7, and the constraint that shapes every domain event in this model.

> **Kafka cannot delete an individual record.** Erasure depends on events
> carrying identifiers rather than personal content, and it cannot be
> retrofitted (ADR-0009 §10).

Consequences, binding on every event definition in this document set:

1. An event payload carries `ResourceId`s, enumerations, timestamps, counts and
   classifications. **Never** a phone number, a name, a transcript turn, a
   summary, a caller utterance, or free text authored by any human.
2. A consumer that needs content **fetches it** from the owning context,
   subject to that context's access rules. The event tells it that something
   happened and where to look.
3. Where a display string is genuinely required in an event — a notification
   payload, for example — it is a **reference to a template plus enumerated
   parameters**, never rendered prose.
4. Topics carrying any personal field use compaction with tombstones and bounded
   retention. Most of ours carry none, which is the objective.

**Test applied to every event in this model:** *if this topic were retained
forever and could never be deleted, would that be a compliance failure?* If yes,
the payload is wrong.

---

## 0.9 Domain event naming

Fixed by `packages/go/eventbus`, which validates it at construction:

```
<domain>.<entity>.<event>.v<major>
```

Lowercase, underscores permitted inside a segment, **hyphens prohibited** (they
collide with Prometheus metric normalisation). Version suffix mandatory from the
first topic.

| Segment | Rule |
|---|---|
| `domain` | The bounded context's short name: `identity` · `consumer` · `telephony` · `voice` · `ai` · `fraud` · `business` · `billing` · `notifications` · `analytics` · `admin` |
| `entity` | The aggregate root the event is about, singular, snake_case |
| `event` | Past tense, always. `answered`, `revoked`, `lapsed`, `escalated` |
| `version` | Major only. Additive changes do not bump it; breaking changes create a new topic |

**Events are named for what happened, never for what should happen next.**
`telephony.forwarding.lapsed.v1`, not `telephony.forwarding.needs_repair.v1`.
An event that names its consumer's reaction has coupled producer to consumer.

---

## 0.10 Rules of this model

1. **One aggregate per transaction.** Everything across aggregates is eventual.
2. **Aggregates reference each other by identity**, never by object reference.
3. **The shared kernel holds value objects only.** No entity, no lifecycle.
4. **Every architectural invariant I1–I12 is restated as a domain invariant** on
   a named aggregate, enforced at construction.
5. **Events carry identifiers, not content** (I7).
6. **Every entity attribute carries a `Sensitivity` and a `Retention`.** An
   unclassified attribute is `PERSONAL` (I8).
7. **A context never reads another context's schema**, regardless of which
   cluster hosts it.
8. **Commands may be rejected; events may not.** An event has already happened.
9. **Queries never mutate.** A query that changes state is a command that lied.
10. **A domain service exists only when the behaviour belongs to no single
    entity.** A service holding state is an aggregate that has not been named.
