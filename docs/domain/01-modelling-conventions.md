# 1 · Modelling Conventions

How every bounded context in this set is specified, and what each attribute
means. Read once; it applies to all eleven.

---

## 1.1 The context template

Every context document answers the same sixteen questions, in the same order.

| # | Section | Answers |
|---|---|---|
| 1 | **Purpose** | The one sentence that justifies the context's existence |
| 2 | **Responsibilities** | What it owns, and — explicitly — what it does not |
| 3 | **Domain Entities** | Things with identity and a lifecycle |
| 4 | **Value Objects** | Things defined wholly by their attributes, immutable |
| 5 | **Aggregates** | Consistency boundaries, with their roots |
| 6 | **Domain Services** | Behaviour belonging to no single entity |
| 7 | **Repositories** | Collection-like access to aggregate roots. One per aggregate, never more |
| 8 | **Domain Events** | Facts, past tense, published |
| 9 | **Commands** | Requests that may be refused |
| 10 | **Queries** | Reads that never mutate |
| 11 | **Policies** | "When X, then Y" rules that cross aggregates |
| 12 | **Invariants** | Conditions that are always true. Violating one is a defect |
| 13 | **State Machines** | Legal transitions, with terminal states named |
| 14 | **Ownership** | Team, service, store, and the CODEOWNERS handle |
| 15 | **External Dependencies** | Systems outside the domain, and the ACL that guards each |
| 16 | **Security Constraints** | What this context must refuse, and what it must record |

---

## 1.2 The entity template

Every entity is specified with six attributes. An entity missing any of them is
not specified.

| Attribute | Means |
|---|---|
| **Attributes** | Fields, with type intent and classification. Not a column list — types are stated as domain intent (`PhoneNumber`, not `varchar(20)`) |
| **Relationships** | What it references, in which direction, and whether by identity or containment |
| **Lifecycle** | Creation, mutation, archival, deletion. Who may create it, who may end it |
| **Validation Rules** | What must be true for an instance to exist. Enforced at construction, not at save |
| **Privacy Classification** | `Sensitivity` and `Retention` per `annotations.proto`, plus residency binding |
| **Audit Requirements** | What access or change must produce an `AuditEntry` |

### Notation used in attribute tables

```
name : Type                      required
name : Type?                     optional
name : Type[]                    collection
name : Type <owned>              contained in this aggregate
name : TypeId <ref>              referenced by identity, another aggregate
```

---

## 1.3 Privacy classification

Drawn **only** from `contracts/proto/callscreen/common/v1/annotations.proto`.
No parallel scheme exists, and inventing one would defeat the purpose of
classifying at the schema level.

### Sensitivity

| Value | Applied to | Handling |
|---|---|---|
| `PUBLIC` | Service names, region codes, durations, enum values | Logged in full |
| `INTERNAL` | Opaque identifiers, shard keys, model versions | Logged; never exposed to clients |
| `PERSONAL` | MSISDN, caller number, device identifiers, IP, install ID | Redacted in logs, encrypted at rest, residency-bound, retention-bounded |
| `SENSITIVE` | Call audio, transcripts, summaries, contact data, free-text reports | `PERSONAL` handling **plus** never exported to analytics, and access audited individually |
| `SECRET` | Session tokens, refresh tokens, OTP hashes, attestation nonces, API keys | Never logged at any level, never persisted in plaintext, never in an error or a crash report |

### Retention

| Value | Applied to | Typical |
|---|---|---|
| `EPHEMERAL` | Live session state, interim ASR, amplitude frames | Duration of the call |
| `SHORT` | Diagnostics, telemetry, delivery state | Days |
| `STANDARD` | Call records, transcripts, preferences | 30–90 days, subscriber-configurable within bounds |
| `LEGAL_HOLD` | Invoices, consent records, audit entries | Survives an erasure request, per ADR-0012 |

### The two rules that make this work

**An unclassified attribute is `PERSONAL`.** Invariant I8 — the default is the
control. In this document set, every attribute carries an explicit
classification, and the absence of one in a future addition fails closed rather
than open.

**A message-level default may be tightened by a field, never loosened.**
Inherited from `MessageClassification`. An entity classified `SENSITIVE` cannot
contain a field marked `PUBLIC` merely because it looks harmless.

---

## 1.4 Audit requirements

Three levels, and the level is a property of the data, not of the actor.

| Level | Triggers an `AuditEntry` | Applies to |
|---|---|---|
| **None** | — | `PUBLIC` and `INTERNAL` attributes read by their owning context |
| **Change** | Every mutation | `PERSONAL` attributes, consent, entitlement, role, routing, forwarding |
| **Access** | Every read **and** every mutation | `SENSITIVE` and `SECRET` attributes; anything read through Administration |

**Access-level auditing is per-field, not per-page** (INV-AD-2). An operator who
opens a subscriber record and reveals two fields produces two entries, because
"what did they actually see" is the question an access review has to answer.

Every `AuditEntry` carries the frozen `AuditContext`: actor, actor type,
timestamp, source IP (partially redacted), and region. It is `LEGAL_HOLD` and
append-only, and no role can delete one.

---

## 1.5 Identity conventions

| Rule | Detail |
|---|---|
| Every aggregate root has an opaque `ResourceId` | Typed by `ResourceId.type`, so a `call_session` id cannot be passed where a `subscriber` id is expected |
| Identifiers are never sequential integers | They leak business volume and invite enumeration (`types.proto`) |
| Identifiers are never derived from personal data | A hash of an MSISDN is still an identifier for a person and is `PERSONAL` |
| An entity inside an aggregate may have a local identity | Unique within its root, not globally |
| Cross-aggregate references are by `ResourceId`, always | Never an object reference, never a foreign-key traversal in the model |
| An erased subject's identifiers are tombstoned | Never reused. Reuse would resurrect a deleted person |

---

## 1.6 Command, query and event conventions

### Commands

Imperative, present tense, named for intent rather than mechanism:
`WithdrawConsent`, not `UpdateConsentRow`.

| Rule | Detail |
|---|---|
| A command may be **refused**, and the refusal is a domain outcome, not an error | `RejectedBecause(reason)` is part of the model |
| A command targets exactly one aggregate | If it needs two, it is a saga, and the saga is specified in §13 of the owning context |
| A mutating command carries an idempotency key | `RequestMetadata.idempotency_key`, mandatory (frozen in `types.proto`) |
| A command never returns domain data | It returns acceptance or refusal. Reading is a query's job |

### Queries

| Rule | Detail |
|---|---|
| Never mutate | A query that changes state is a command that lied |
| Scoped at the query layer, not the render layer | A support agent's lookup does not retrieve what their role cannot open. Filtering after retrieval is the most common accidental privilege escalation in an admin tool |
| May cross aggregates within a context; never across contexts | Cross-context reads go through the owning context's published interface |

### Events

| Rule | Detail |
|---|---|
| Past tense, always | `ForwardingLapsed`, never `ForwardingNeedsRepair` |
| Named for what happened, never for the consumer's reaction | An event that names its consumer has coupled them |
| Carry identifiers, not content | Invariant I7 (see [00 §0.8](00-strategic-design.md)) |
| Published through the transactional outbox | ADR-0009 §5. An event is part of the database transaction that produced it |
| Immutable | A correction is a new event, never an edit |

---

## 1.7 State machine conventions

Every state machine specifies: states, legal transitions with their triggers,
terminal states, and what happens to an aggregate stuck in a non-terminal state.

| Rule | Detail |
|---|---|
| Terminal states are named explicitly | An unnamed terminal state is a leak |
| Every non-terminal state has a timeout or an external trigger | A state with neither is a hang |
| Illegal transitions are refused, not logged | Refusing is a domain behaviour; logging is a hope |
| A state machine is owned by exactly one aggregate | Two aggregates sharing one is a missing aggregate |

---

## 1.8 Policy notation

Policies are numbered `P-<CONTEXT>-<n>` and written as `When <event>, then
<consequence>`. They are the reactive glue between aggregates and contexts.

Invariants are numbered `INV-<CONTEXT>-<n>` and written as statements of fact
that are always true.

**The distinction matters.** A policy can be violated temporarily during a
saga and compensated. An invariant cannot be violated at all — it is enforced at
construction, so an object that would violate it cannot be built.

Context prefixes: `ID` Identity · `CO` Consumer · `TE` Telephony · `VO` Voice ·
`AI` AI Orchestration · `FR` Fraud · `BU` Business · `BI` Billing ·
`NO` Notifications · `AN` Analytics · `AD` Administration.

---

## 1.9 Traceability to the frozen invariants

Every architectural invariant and every UX invariant with domain content is
carried into this model as a named domain invariant. This table is the index; it
is also the checklist for review.

| Frozen | Statement | Carried as |
|---|---|---|
| **I1** | Every screened call opens with a deterministic, model-free announcement | INV-TE-1, INV-AI-1 |
| **I2** | Personal data does not leave India except under the consent gate | INV-ID-8, and residency binding on every `PERSONAL` attribute |
| **I3** | Thinking stays enabled on tool-calling LLM tiers | INV-AI-2 |
| **I4** | Caller speech is untrusted; agent tools are read-mostly and cannot disclose subscriber PII | INV-AI-3, INV-CO-2, P-BU-5 |
| **I5** | No secret ships in the APK; the device credential is on-device and non-exportable | INV-ID-3 |
| **I6** | Services drain before terminating | P-TE-6 |
| **I7** | Kafka payloads carry identifiers, not personal content | [00 §0.8](00-strategic-design.md), enforced on every event |
| **I8** | An unclassified schema field is treated as `PERSONAL` | [§1.3](#13-privacy-classification), INV-AN-3 |
| **I9** | `media-relay` never writes audio to disk | INV-VO-1 |
| **I10** | Undiverted inbound calls are refused as hostile | INV-TE-2, P-TE-1 |
| **I11** | Under load, shed at admission or downgrade a tier — never skip fraud scoring or the safety layer | P-TE-2, P-AI-4, P-FR-3, INV-AD-6 |
| **I12** | Each service owns its schema; no cross-service table access | [00 §0.7](00-strategic-design.md), rule 7 |
| **U4** | Every model-generated string carries provenance; every verdict its confidence | INV-AI-6, INV-FR-2 |
| **U5** | Recording state is visible whenever recording and is never suppressible | INV-VO-4, INV-NO-4 |
| **U7** | Emergency intent exits the AI flow and hands over a dialer | P-AI-10, INV-AI-9 |
| **U8** | Nothing implies the agent is listening unless it is | INV-AI-7, P-VO-3 |
| **U9** | Consent is per-purpose, with one-tap withdrawal; nothing bundled | INV-ID-4, INV-ID-6 |
| **U10** | No caller-supplied string is rendered as interface chrome | P-AI-5, P-NO-4 |
| **U11** | No personal data in analytics, crash reports or logs | INV-AN-1, P-AN-1 |
| **U12** | Console reveals PII only through an audited, time-boxed grant | INV-AD-2, INV-AD-3, P-AD-1 |

---

## 1.10 What this model deliberately does not contain

| Absent | Why |
|---|---|
| A `Password` concept, anywhere | ADR-0010. MSISDN + OTP + device credential. There is no password in this product, and modelling one would invite adding one |
| A `Role` on `Subscriber` | Subscribers do not have roles. Operators do (Administration), and organisation members do (Business). Conflating them is how a consumer account acquires admin capability |
| An `Impersonation` concept | INV-AD-5. The most-abused capability in every admin tool ever built. It is absent from the domain so it cannot be added by configuration |
| A global `TranscriptSearch` query | INV-AD-6. Full-text search over subscriber conversations is a surveillance capability, not a feature |
| A `CommunityReport` or shared reputation feed visible to subscribers | Invites brigading of legitimate numbers. `NumberReputation` is an internal signal with a k-anonymity floor |
| An `EngagementScore`, `Streak` or `Digest` | The product succeeds when it is thought about less |
| A `Persona` on the assistant | We answer as the subscriber. A named character invites over-trust in both directions |
| A mutable `Announcement` | INV-AI-1. Modelled as an immutable value object precisely so it cannot become configurable |
| A `PaymentInstrument` entity | INV-BI-3. Only a provider reference enters our domain |
