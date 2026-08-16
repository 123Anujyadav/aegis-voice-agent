# CallScreen Domain Model

The business domain of the platform, modelled with Domain-Driven Design.

> **This is the language layer.** Above it sit the frozen architecture
> ([`ARCHITECTURE_FREEZE.md`](../../ARCHITECTURE_FREEZE.md)) and the frozen
> experience ([`UX_FREEZE.md`](../../UX_FREEZE.md)). Below it sits Phase 5
> implementation. This document set says what the business *is* — what concepts
> exist, what they mean, what they may and may not do — in terms an engineer and
> a domain expert can both hold, and neither can misread.

---

## What this documents, and what it does not

| This defines | It does not define |
|---|---|
| Eleven bounded contexts and their boundaries | Services, deployments, or process boundaries |
| Entities, value objects, aggregates | Database tables, columns, indexes or migrations |
| Domain events, commands, queries | API contracts, protobuf messages, RPC signatures |
| Invariants, policies, state machines | Implementation of any of them |
| Ubiquitous language | Class names, package names, file layout |
| Privacy classification and audit requirements per entity | Encryption mechanics or key management |

**No table, no contract, no code follows from this document set alone.** The
domain model is the vocabulary and the rules; the schema and the wire format are
Phase 5, and both are constrained by what is written here.

---

## Reading order

| # | Document | Answers |
|---|---|---|
| 0 | [Strategic design](00-strategic-design.md) | What are the contexts, how do they relate, which are core, and how does this sit on the frozen architecture? |
| 1 | [Modelling conventions](01-modelling-conventions.md) | How is every context specified? What does each attribute of an entity mean? |
| 2 | [Ubiquitous language](02-ubiquitous-language.md) | What does every term mean, exactly, and what must we never say? |
| 3 | [Event storming summary](03-event-storming.md) | What actually happens, in time order, across the whole platform? |

### The eleven bounded contexts

Ordered by dependency — each depends only on those above it, except where the
context map records a Partnership.

| # | Context | Subdomain | Document |
|---|---|---|---|
| 10 | Identity | Generic + statutory | [10-identity.md](10-identity.md) |
| 11 | Consumer | Supporting | [11-consumer.md](11-consumer.md) |
| 12 | Telephony | **Core** | [12-telephony.md](12-telephony.md) |
| 13 | Voice | Supporting | [13-voice.md](13-voice.md) |
| 14 | AI Orchestration | **Core** | [14-ai-orchestration.md](14-ai-orchestration.md) |
| 15 | Fraud Detection | **Core** | [15-fraud-detection.md](15-fraud-detection.md) |
| 16 | Business | Supporting | [16-business.md](16-business.md) |
| 17 | Billing | Generic | [17-billing.md](17-billing.md) |
| 18 | Notifications | Supporting | [18-notifications.md](18-notifications.md) |
| 19 | Analytics | Generic | [19-analytics.md](19-analytics.md) |
| 20 | Administration | Supporting | [20-administration.md](20-administration.md) |

### Cross-cutting artefacts

| # | Document |
|---|---|
| 40 | [Cross-context interaction matrix](40-cross-context-interaction-matrix.md) |
| 41 | [Aggregate map](41-aggregate-map.md) |

---

## The domain in one paragraph

A **Subscriber** owns a **Line**. Their carrier is configured with a
**ForwardingConfiguration** that diverts unanswered calls to a
**DirectInwardDialNumber** we control. When an unknown **Caller** rings and is
not answered, the carrier forwards; we admit the call as a **CallSession**, play
a deterministic **Announcement**, and conduct a **ScreeningConversation** in
which an **Assistant** speaks on the subscriber's behalf. The conversation
produces a **Transcript**, a **CallSummary**, and — usually — a
**FraudAssessment** carrying a **RiskLevel** and a **Confidence**. The subscriber
watches, and may **take over**. What remains afterwards is the record.

Everything else in the platform exists to make that paragraph true, provable,
lawful, and payable.

---

## The five things this model is most careful about

**1 · Two agents, never conflated.** The **Screening Agent** talks to hostile
strangers and cannot see subscriber personal data (Invariant I4). The **Personal
Assistant** talks to the authenticated subscriber and can see their own data.
They live in the same bounded context and share no aggregate, no tool set and no
prompt. See [14 §14.12](14-ai-orchestration.md).

**2 · The Announcement is not a message the model writes.** It is a
deterministic, versioned, immutable value object, and it is the caller's lawful
basis under DPDP (Invariant I1). No subscriber setting, prompt, or operator
action can alter it. It is modelled as a value object precisely so that it
cannot be mutated.

**3 · A verdict without evidence does not exist.** `FraudAssessment` is
invalid without at least one `EvidenceReference` or an explicit reason for
`UNKNOWN`. This is not presentation logic — it is an aggregate invariant, so an
unevidenced verdict cannot be constructed in the first place.

**4 · Personal-line data is not an object in the Business context.** Not a
permission check, not a filter — an absence. An administrator cannot read an
employee's personal call because there is nothing in the model to address.

**5 · Consent is per-purpose, append-only, and fails closed.** An unknown
consent state is `NOT GRANTED` (Invariant I8). Withdrawal appends a record; it
never mutates one. And the Announcement is deliberately **not** a consent, so it
cannot be withdrawn.

---

## Relationship to the frozen layers

| Frozen artefact | How this model is bound by it |
|---|---|
| **Invariants I1–I12** (`ARCHITECTURE_FREEZE.md §3`) | Every one is restated as a domain invariant on a named aggregate, so it is enforceable at construction rather than by review |
| **UX invariants U1–U12** (`UX_FREEZE.md §3`) | Those with domain content (U4, U5, U7, U8, U9, U10, U11, U12) are restated as domain invariants or policies |
| **`annotations.proto`** | Supplies the privacy vocabulary. Every entity attribute carries a `Sensitivity`, a `Retention`, and a residency flag drawn from that enum set — no parallel scheme |
| **ADR-0002** (carrier-side screening) | Determines the Telephony aggregate shape: `Line`, `ForwardingConfiguration`, `DID`, `CallSession` |
| **ADR-0006** (four-tier LLM ladder) | `ModelTier` is a domain value object, not an implementation detail |
| **ADR-0009** (Aurora per context, Kafka) | Determines event-carried-identifier discipline (I7) and the persistence mapping in [00 §0.7](00-strategic-design.md) |
| **ADR-0010** (MSISDN identity, device trust) | Determines the Identity aggregate shape; there is no password concept anywhere in this domain |
| **ADR-0012** (DPDP, consent, retention) | Determines `ConsentRecord`, `RetentionPreference`, `ErasureRequest`, and the classification of every attribute |
| **`packages/go/eventbus`** | Fixes domain-event naming: `<domain>.<entity>.<event>.v<major>` |

---

## Status

| Aspect | State |
|---|---|
| Strategic design and context map | **Defined** |
| Ubiquitous language | **Defined** |
| Event storming | **Defined** |
| Eleven bounded contexts, 16 sections each | **Defined** |
| Entity specifications | **Defined** — 6 attributes each |
| Aggregate map and interaction matrix | **Defined** |
| Database schemas | **Not started.** Phase 5 |
| API contracts | **Not started.** Phase 5 |
| Implementation | **Not started.** Phase 5 |
