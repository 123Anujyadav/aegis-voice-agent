# 15 · Fraud Detection

**Subdomain:** **CORE** · **Prefix:** `FR` · **Topic domain:** `fraud`

> The differentiator is not the model. It is **calibrated confidence with
> retrievable evidence** — which is a domain discipline, enforced by aggregate
> invariants rather than by careful presentation.

---

## 15.1 Purpose

Judge the risk of a screened call, with a confidence we can defend and evidence
we can show — and make it structurally impossible to publish a verdict we cannot
justify.

## 15.2 Responsibilities

**Owns**

- `FraudAssessment` — the judgement, its confidence, and its evidence
- `NumberReputation` — the cross-subscriber signal, k-anonymised
- `FraudPattern` — the named scam shapes and their editorial explanations
- `FraudCase` — the analyst review object
- `AbuseReport` — what Subscribers tell us
- Detection rules and their versioning

**Does not own**

| Not owned | Owned by |
|---|---|
| Emergency detection | AI Orchestration — it is control-flow, not a risk grade |
| The transcript the evidence points at | AI Orchestration |
| Whether a flagged call is blocked | Consumer decides; the allowlist can override routing |
| Whether a verdict notifies | Fraud states alert-worthiness; Notifications delivers |
| The analyst's identity and access | Administration |

> **The verdict is free on every plan, permanently** (INV-BI-6). Charging
> someone to learn a call was probably a scam is not a business model. Fraud
> publishes; Billing gates only depth.

---

## 15.3 Domain Entities

### `FraudAssessment` — aggregate root

**Attributes**

```
id             : AssessmentId             INTERNAL  · STANDARD
callSessionId  : CallSessionId <ref>      INTERNAL  · STANDARD
subscriberId   : SubscriberId <ref>       INTERNAL  · STANDARD
level          : RiskLevel                PUBLIC    · STANDARD
confidence     : Confidence               PUBLIC    · STANDARD
calibratedScore: CalibratedScore          INTERNAL  · STANDARD
pattern        : FraudPatternId? <ref>    PUBLIC    · STANDARD
evidence       : EvidenceReference[] <owned>
signals        : RiskSignal[] <owned>
provenance     : AssessmentProvenance     INTERNAL  · STANDARD
unknownReason  : UnknownReason?           PUBLIC    · STANDARD
state          : AssessmentState          PUBLIC    · STANDARD
supersedes     : AssessmentId?  <ref>     INTERNAL  · STANDARD
assessedAt     : Instant                  INTERNAL  · STANDARD
```

**Relationships** — References a `CallSession` and, through
`EvidenceReference`, specific turns of a `Transcript`. Referenced by Consumer's
`CallHistoryEntry` and `VerdictDispute`, and by `FraudCase`. **It references no
Consumer object** — a dispute does not reach into the assessment.

**Lifecycle** — Created asynchronously during or shortly after a screening.
Published once complete. **Immutable thereafter** (INV-FR-1): a revision creates
a new assessment with `supersedes` set. Marked `EVIDENCE_EXPIRED` when its
transcript reaches retention — the judgement survives, the proof does not, and
the interface says so rather than showing a dead link.

**Validation Rules**

- **Cannot be constructed with `level ∈ {SPAM, FRAUD}` and zero
  `EvidenceReference`s** (INV-FR-3). This is the central invariant of the
  context: an unevidenced verdict cannot exist to be rendered.
- `level = UNKNOWN` requires an `unknownReason` — "we did not assess this" is a
  claim that needs a reason, and it is the opposite claim from `SAFE`.
- `confidence` is mandatory. There is no unspecified-confidence assessment
  (INV-FR-2).
- `level = EMERGENCY` cannot be assigned here; it originates in AI Orchestration
  (INV-FR-7).

**Privacy Classification** — `INTERNAL`/`PUBLIC` at the assessment level. The
evidence it points at is `SENSITIVE` in its owning context and is **not copied**
here — the assessment holds references, so retrieving evidence goes through
Transcript's access controls, including break-glass.

**Audit Requirements** — **Change** on publication and supersession. **Access**
level when evidence is resolved through Administration.

---

### `RiskSignal`

```
id           : SignalId              INTERNAL · STANDARD
assessmentId : AssessmentId <ref>    INTERNAL · STANDARD
signalType   : SignalType            PUBLIC   · STANDARD
weight       : Weight                INTERNAL · STANDARD
sourceContext: ContextName           PUBLIC   · STANDARD
turnRef      : TurnId?  <ref>        INTERNAL · STANDARD
```

**Lifecycle** — Contained in the assessment, created during scoring, immutable
with it.

**Validation Rules** — `signalType` must be a published type; an unrecognised
signal contributes zero weight rather than an unpredictable one. A signal
sourced from AI Orchestration's `ai.injection.detected.v1` is admitted as a
`RiskSignal` but **does not by itself constitute a fraud verdict** — an injection
attempt is a security event first.

**Privacy Classification** — `INTERNAL`. Signals are structural, not content.

---

### `NumberReputation` — aggregate root

**Attributes**

```
numberKey       : HashedPhoneNumber       INTERNAL · STANDARD
score           : ReputationScore         INTERNAL · STANDARD
observationCount: Int                     INTERNAL · STANDARD
distinctSubjects: Int                     INTERNAL · STANDARD
dominantPattern : FraudPatternId?         PUBLIC   · STANDARD
firstObservedAt : Instant                 INTERNAL · STANDARD
lastObservedAt  : Instant                 INTERNAL · STANDARD
decayAppliedAt  : Instant                 INTERNAL · STANDARD
suppressed      : Boolean                 PUBLIC   · STANDARD
```

**Relationships** — Keyed by a **keyed hash** of the phone number, not the
number. References no `Subscriber`, no `CallSession`, and no assessment
(INV-FR-5) — it is an aggregate of many observations and must not be traversable
back to any one of them.

**Lifecycle** — Accumulated from assessments and reports across subscribers.
**Decays over time**: a number that was a nuisance in March is not necessarily
one in September, and a reputation that only ever increases becomes a permanent
sentence. Suppressed below the k-anonymity threshold.

**Validation Rules** — `suppressed` is true whenever `distinctSubjects` is below
the k-anonymity floor, and a suppressed reputation is **not served** — not to
the pre-filter, not to an analyst, not to a dashboard. Decay is applied on read
if stale, so a stale score is never served as current.

**Privacy Classification** — `INTERNAL`, and it must remain so: the moment a
reputation could be traversed back to an individual subscriber's activity it
becomes `PERSONAL` and the pre-filter could no longer consume it freely.

**Audit Requirements** — **Change** on suppression state. Individual reads are
not audited — it is the one non-personal signal in the context, by construction.

---

### `FraudPattern` — reference entity

```
id            : FraudPatternId       PUBLIC · STANDARD
code          : PatternCode          PUBLIC · STANDARD
displayName   : String               PUBLIC · STANDARD
explanation   : String               PUBLIC · STANDARD
provenance    : Provenance           PUBLIC · STANDARD   ── always EDITORIAL
prevalenceRank: Int                  PUBLIC · STANDARD
status        : PatternStatus        PUBLIC · STANDARD
```

**Patterns at launch** — `OTP_REQUEST` · `BANK_IMPERSONATION` ·
`KYC_UPDATE` · `ADVANCE_FEE` · `JOB_SCAM` · `COURIER_CUSTOMS` ·
`DIGITAL_ARREST` · `LOAN_OFFER` · `INVESTMENT_TIP` · `LOTTERY_PRIZE` ·
`TECH_SUPPORT` · `UTILITY_DISCONNECTION`.

**Validation Rules** — `provenance` is **always `EDITORIAL`**. The
plain-language explanation of a scam pattern is written by us, not generated,
and it therefore carries no AiBadge. Mixing editorial and model provenance in
one surface would misattribute our claims to the model and the model's to us
(`UX_FREEZE` / `A31d`).

**Privacy Classification** — `PUBLIC`. Reference data.

**Audit Requirements** — **Change** on publication.

---

### `AbuseReport`

```
id            : ReportId             INTERNAL  · STANDARD
subscriberId  : SubscriberId <ref>   INTERNAL  · STANDARD
number        : PhoneNumber          PERSONAL  · STANDARD · residency-bound
callSessionId : CallSessionId? <ref> INTERNAL  · STANDARD
category      : ReportCategory       PUBLIC    · STANDARD
note          : String?              SENSITIVE · STANDARD · residency-bound
verified      : Boolean              PUBLIC    · STANDARD
submittedAt   : Instant              PERSONAL  · STANDARD
```

**Lifecycle** — Submitted by a Subscriber, deduplicated silently against
existing reports for the same call. **Never rejected as a duplicate** — telling
someone "you already reported this" punishes exactly the engagement we want.
Feeds `NumberReputation` and may open a `FraudCase`.

**Validation Rules** — A report without a `callSessionId` is accepted and marked
`verified = false`; it contributes less weight than one anchored to a real call.

**Privacy Classification** — `note` is `SENSITIVE` free text authored by a
person and is **never rendered back to any other user**, ever.

**Audit Requirements** — **Change** level.

---

### `FraudCase` — aggregate root

```
id              : CaseId               INTERNAL · STANDARD
assessmentId    : AssessmentId <ref>   INTERNAL · STANDARD
priority        : CasePriority         PUBLIC   · STANDARD
state           : CaseState            PUBLIC   · STANDARD
assignedTo      : OperatorId? <ref>    INTERNAL · STANDARD
softLockUntil   : Instant?             INTERNAL · SHORT
subscriberAction: UserActionType?      PUBLIC   · STANDARD
disputed        : Boolean              PUBLIC   · STANDARD
resolution      : CaseResolution?      PUBLIC   · STANDARD
openedAt        : Instant              INTERNAL · STANDARD
resolvedAt      : Instant?             INTERNAL · STANDARD
```

**Relationships** — References one `FraudAssessment` and, when assigned, an
Administration `Operator`.

**Lifecycle** — Opened when an assessment crosses the review threshold, or when
a Subscriber disputes. **A disputed case is prioritised above all others** — a
dispute is the strongest precision signal available, and it should be the first
thing an analyst sees. Soft-locked while an analyst holds it; a **hard** lock
would strand cases at shift change.

**Validation Rules** — Resolution requires a resolvable assessment. A case whose
transcript has passed retention is resolvable on metadata alone, **with the
limitation recorded** on the resolution.

**Privacy Classification** — `INTERNAL`. Reading the evidence requires an
`AccessGrant` scoped to the case (INV-FR-8).

**Audit Requirements** — **Change** on assignment and resolution; **Access** on
every evidence retrieval.

---

### `DetectionRule`

```
id            : RuleId           INTERNAL · STANDARD
version       : RuleVersion      PUBLIC   · STANDARD
expression    : RuleExpression   INTERNAL · STANDARD
targetPattern : FraudPatternId   PUBLIC   · STANDARD
status        : RuleStatus       PUBLIC   · STANDARD
proposedBy    : OperatorId <ref> INTERNAL · STANDARD
approvedBy    : OperatorId? <ref>INTERNAL · STANDARD
```

**Lifecycle** — Proposed by an analyst, reviewed, published, retired. **An
analyst cannot publish their own proposal** — proposal and approval are
different operators.

---

## 15.4 Value Objects

| Value object | Definition | Notes |
|---|---|---|
| `RiskLevel` | `SAFE` · `UNKNOWN` · `SPAM` · `FRAUD` · `EMERGENCY` | Shared kernel. **`EMERGENCY` is populated by AI Orchestration only** |
| **`Confidence`** | `LOW` · `MEDIUM` · `HIGH`, each bound to a calibrated score band | Shared kernel. Mandatory on every assessment |
| `CalibratedScore` | Numeric, calibrated against held-out outcomes | Internal. The band, not the number, is what users see |
| `EvidenceReference` | transcriptId + turnId + excerptOffset | A pointer, never a copy |
| `AssessmentProvenance` | modelVersion + ruleSetVersion + patternVersion | A verdict whose origin cannot be reconstructed cannot be evaluated when the model changes |
| `UnknownReason` | `CALL_TOO_SHORT` · `NO_SPEECH` · `LANGUAGE_UNSUPPORTED` · `SCORING_UNAVAILABLE` · `TRANSCRIPT_UNAVAILABLE` | Required when `level = UNKNOWN` |
| `SignalType` | `CLAIMED_IDENTITY_UNVERIFIED` · `CREDENTIAL_REQUEST` · `URGENCY_PRESSURE` · `PAYMENT_REQUEST` · `CALLBACK_REFUSAL` · `REPUTATION_NEGATIVE` · `INJECTION_ATTEMPT` · … | |
| `ReputationScore` | Score + decay function + last application | Decays. A reputation that only rises is a permanent sentence |
| `HashedPhoneNumber` | Keyed hash, per `REDACTION_STRATEGY_HASH` | Correlatable, not reversible |
| `CasePriority` | `DISPUTED` · `HIGH` · `NORMAL` · `LOW` | **`DISPUTED` outranks `HIGH`** |
| `CaseResolution` | `CONFIRMED` · `FALSE_POSITIVE` · `RULE_CHANGE_NEEDED` · `ESCALATED` · `INSUFFICIENT_DATA` | |
| `ReportCategory` | `FRAUD_OR_SCAM` · `UNWANTED_MARKETING` · `WRONG_VERDICT_TOO_HIGH` · `WRONG_VERDICT_TOO_LOW` · `BUSINESS_IMPERSONATION` · `OTHER` | |
| `AssessmentState` | `PENDING` · `PUBLISHED` · `SUPERSEDED` · `EVIDENCE_EXPIRED` | |

### Why `Confidence` is three bands and not a number

A calibrated score is a continuous internal quantity. Exposing it would invite
false precision — "73% fraud" reads as a measurement rather than a judgement,
and it makes a 71 and a 75 look meaningfully different when they are not.

Three bands, each with a distinct label ("Possibly", "Likely", and the bare
verdict), is the largest vocabulary a user can hold and the smallest we can
defend. It is fixed by the design system's `RiskIndicator` and is not a
presentation choice.

---

## 15.5 Aggregates

| Aggregate | Root | Contains | Consistency boundary |
|---|---|---|---|
| **FraudAssessment** | `FraudAssessment` | `EvidenceReference[]`, `RiskSignal[]` | **Evidence and verdict must be atomic.** A verdict that saved before its evidence is the failure this boundary prevents |
| **NumberReputation** | `NumberReputation` | — | Keyed by hash; no subscriber linkage |
| **FraudCase** | `FraudCase` | `CaseResolution` | |
| **AbuseReport** | `AbuseReport` | — | |
| **FraudPattern** | `FraudPattern` | — | Reference data |
| **DetectionRule** | `DetectionRule` | — | Proposal and approval are separate operators |

```
┌──────────────────────────────────────────────────────┐
│  FraudAssessment  «aggregate root»    IMMUTABLE       │
│   level · confidence  ◀── BOTH MANDATORY (INV-FR-2)  │
│   ┌────────────────────────┐ ┌────────────────────┐  │
│   │ EvidenceReference[]    │ │ RiskSignal[]       │  │
│   │  transcriptId + turnId │ │  type · weight     │  │
│   │  POINTERS, NEVER COPIES│ │                    │  │
│   │  ≥1 REQUIRED for       │ │                    │  │
│   │  SPAM / FRAUD          │ │                    │  │
│   └────────────────────────┘ └────────────────────┘  │
│   supersedes ──▶ previous assessment                 │
└───────────────┬──────────────────────────────────────┘
                │ <ref>
                ▼
┌───────────────────────────┐      ┌──────────────────────────────┐
│ FraudCase  «root»         │      │ NumberReputation  «root»     │
│  priority: DISPUTED first │      │  numberKey = KEYED HASH      │
│  softLock (not hard)      │      │  NO subscriber linkage       │
│  subscriberAction ◀── the │      │  decays over time            │
│  most valuable field      │      │  suppressed below k-anonymity│
└───────────────────────────┘      └──────────────────────────────┘

   consumer.verdict.disputed ──▶ raises priority, records a signal
                                 DOES NOT MUTATE THE ASSESSMENT
```

---

## 15.6 Domain Services

| Service | Responsibility | Notes |
|---|---|---|
| `RiskScoringService` | Produce signals and a calibrated score from a transcript | **Never shed under load** (I11) |
| **`ConfidenceCalibrationService`** | Map a score to a `Confidence` band against held-out outcomes | The service that makes "low never renders as high" true rather than aspirational |
| **`EvidenceBindingService`** | Bind an assessment to the specific turns that produced it | An assessment cannot be published without passing through it |
| `PatternClassificationService` | Identify the scam shape | Returns no pattern rather than a low-confidence guess |
| `ReputationAggregationService` | Accumulate, decay, and enforce the k-anonymity floor | Suppresses below threshold; suppressed reputations are not served |
| `RescoringService` | Re-score historical calls against a new model | **Emits new assessments; never edits old ones** (P-FR-6). Kafka replay is the mechanism (ADR-0009 §6) |
| `AlertWorthinessService` | Decide whether a verdict warrants an interruption | Fraud owns the rule; Notifications owns delivery |

---

## 15.7 Repositories

`FraudAssessmentRepository` · `NumberReputationRepository` ·
`FraudCaseRepository` · `AbuseReportRepository` · `FraudPatternRepository` ·
`DetectionRuleRepository`

---

## 15.8 Domain Events

| Event | Payload | Consumers |
|---|---|---|
| `fraud.assessment.completed.v1` | assessmentId, callSessionId, level, confidence, patternId?, evidenceCount | **Consumer, Notifications**, Analytics |
| `fraud.assessment.superseded.v1` | assessmentId, supersedesId, level, confidence | Consumer, Analytics |
| `fraud.assessment.evidence_expired.v1` | assessmentId | Consumer |
| `fraud.reputation.updated.v1` | numberKey, scoreBand, suppressed | **Consumer** (pre-filter cache) |
| `fraud.report.submitted.v1` | reportId, category, verified | Administration, Analytics |
| `fraud.case.opened.v1` | caseId, assessmentId, priority | Administration |
| `fraud.case.resolved.v1` | caseId, resolution, timeToResolveS | Analytics, AI Orchestration |
| `fraud.rule.published.v1` | ruleId, version, targetPattern | Administration |
| `fraud.quality.signal_recorded.v1` | assessmentLevel, confidence, subscriberAgreed | **Analytics** — real-world precision |

**No event carries a phone number, an evidence excerpt, or a report note.**
`fraud.reputation.updated.v1` carries a keyed hash and a **band**, not a score —
publishing a precise score to every consumer would let a determined consumer
reconstruct individual observations.

---

## 15.9 Commands

| Command | Refused when |
|---|---|
| `AssessCall(callSessionId, transcriptId)` | Transcript unavailable → publishes `UNKNOWN` with a reason rather than failing silently |
| `PublishAssessment(draft)` | **Level is `SPAM`/`FRAUD` with no evidence.** Confidence absent. Level is `EMERGENCY` |
| `SupersedeAssessment(assessmentId, newDraft)` | Original not found |
| `SubmitReport(subscriberId, number, category, callRef?, note?)` | — (duplicates deduplicate silently) |
| `OpenCase(assessmentId, priority)` | Case already open for this assessment |
| `AssignCase(caseId, operatorId)` | Soft-locked by another operator within the window |
| `ResolveCase(caseId, resolution, note)` | Operator lacks `fraud_analyst` |
| `ProposeRule(expression, targetPattern)` | Expression invalid |
| `PublishRule(ruleId)` | **Proposer and approver are the same operator** |
| `RescoreHistorical(criteria, modelVersion)` | Would exceed the rescoring budget |

**`PublishAssessment` is where the context's central invariant lives.** A draft
that cannot show its work is refused at the boundary, so an unevidenced verdict
never reaches a database, an event, or a screen.

---

## 15.10 Queries

| Query | Scope |
|---|---|
| `GetAssessment(callSessionId)` | Subscriber's own; Administration under redaction |
| `GetEvidence(assessmentId)` | Resolves through Transcript's access control — **requires an `AccessGrant` for an operator** |
| `GetReputation(numberKey)` | Internal. **Returns nothing if suppressed** |
| `ListCases(filter)` | `fraud_analyst`. Disputed first |
| `GetQualityMetrics(period)` | Agreement and dispute rates by pattern and confidence band |
| `GetPatternExplanation(patternId)` | Public editorial copy |

**There is no `SearchAssessmentsByNumber` for operators without a case.** Fraud
review is per-case, from a queue, not an open search over subscribers' calls.

---

## 15.11 Policies

| # | Policy |
|---|---|
| **P-FR-1** | An assessment is never rendered without its confidence (U4) |
| **P-FR-2** | An assessment with no retrievable evidence is **not published**. If we cannot show the work, we do not render the verdict |
| **P-FR-3** | Fraud scoring is **never shed** under load. Degrade the model tier, shed at admission — never skip the judgement (I11) |
| **P-FR-4** | When a Subscriber disputes, raise the case to `DISPUTED` priority and record a precision signal. **Do not mutate the assessment** |
| **P-FR-5** | Reputation is served only above the k-anonymity floor, and never exposes another Subscriber's activity |
| **P-FR-6** | Rescoring emits new assessments superseding old ones. **It never edits history** |
| **P-FR-7** | When a transcript expires, mark the assessment `EVIDENCE_EXPIRED`. The verdict survives; the interface states why the proof does not |
| **P-FR-8** | An allowlist entry never suppresses assessment. It changes routing only (mirrors P-CO-3) |
| **P-FR-9** | A duplicate report is accepted and deduplicated silently |
| **P-FR-10** | When scoring is unavailable, publish `UNKNOWN` with `SCORING_UNAVAILABLE`. **Never publish `SAFE` by default** — absence of a judgement is not a judgement of safety |

**P-FR-10 deserves emphasis.** The tempting default when scoring fails is to
show nothing, which the interface renders as "no risk found". `SAFE` and
`UNKNOWN` are opposite claims, and defaulting to the reassuring one is how a
product tells its most dangerous lie.

---

## 15.12 Invariants

| # | Invariant | Source |
|---|---|---|
| **INV-FR-1** | A published `FraudAssessment` is immutable. Revision creates a new assessment | |
| **INV-FR-2** | `confidence` is always present. There is no unspecified-confidence assessment | **U4** |
| **INV-FR-3** | An assessment with `level ∈ {SPAM, FRAUD}` has at least one `EvidenceReference`. `UNKNOWN` requires an `unknownReason` | Principle 2 |
| **INV-FR-4** | Evidence is a reference, never a copy. Retrieval goes through Transcript's access control | |
| **INV-FR-5** | `NumberReputation` contains no subscriber identifier and is not traversable to any individual observation | |
| **INV-FR-6** | A reputation below the k-anonymity floor is suppressed and not served to any consumer | |
| **INV-FR-7** | `RiskLevel.EMERGENCY` is never assigned by this context | **U7** |
| **INV-FR-8** | Evidence retrieval by an operator requires an `AccessGrant` scoped to the case | **U12** |
| **INV-FR-9** | `FraudPattern.provenance` is always `EDITORIAL`; pattern explanations are never model-generated | **U4** |
| **INV-FR-10** | Scoring is never skipped, at any load, under any flag. No feature flag disabling it exists | **I11** |

---

## 15.13 State Machines

### `FraudAssessment`

```
   PENDING ──scoring completes──▶ (draft)
                                     │
                    ┌────────────────┴────────────────┐
              evidence present               no evidence, level
              or UNKNOWN reason              is SPAM or FRAUD
                    │                                 │
                    ▼                                 ▼
               PUBLISHED                      REFUSED AT CONSTRUCTION
                    │                          (never reaches a store,
        ┌───────────┼──────────────┐            an event, or a screen)
        ▼           ▼              ▼
   SUPERSEDED  EVIDENCE_      (remains
   «terminal»   EXPIRED        PUBLISHED)
                «terminal»
```

### `FraudCase`

```
  OPEN ──assign──▶ ASSIGNED ──open──▶ UNDER_REVIEW ──resolve──▶ RESOLVED
   ▲                  │                    │                    «terminal»
   │            soft lock expires    soft lock expires
   └──────────────────┴────────────────────┘
                (returns to the queue — never stranded)

  Priority ordering:  DISPUTED  >  HIGH  >  NORMAL  >  LOW
```

### `NumberReputation`

```
  (none) ──first observation──▶ ACCUMULATING
                                     │
                    ┌────────────────┴────────────────┐
              above k-floor                    below k-floor
                    │                                 │
                    ▼                                 ▼
                 SERVED ⇄ decay applied           SUPPRESSED
                    │                             (not served to
              no observations                      anyone, including
              past decay horizon                   analysts)
                    ▼
                 EXPIRED «terminal»
```

---

## 15.14 Ownership

| Aspect | Value |
|---|---|
| Team | `callscreen/ai` |
| Service | `fraud-engine` (Python 3.12) — a **critical module**, ≥ 90% coverage gate |
| Durable store | `content` Aurora, schema `fraud` |
| Ephemeral | Redis — reputation cache served to the on-device pre-filter |
| CODEOWNERS | `docs/domain/15-fraud-detection.md`, `services/python/fraud-engine/**` |

---

## 15.15 External Dependencies

| Dependency | Purpose | Guard |
|---|---|---|
| AI Orchestration | Finalised turns to score | Customer–Supplier. Fraud consumes; AI does not depend on Fraud's model |
| Transcript store | Evidence resolution | Access-controlled. Fraud holds references, never copies |
| `tests/eval` fraud-recall gate | Model quality | A recall regression blocks a model rollout, as a safety regression blocks a prompt rollout |
| External threat intelligence *(post-launch)* | Reputation enrichment | **ACL.** Third-party reputation is a signal with a weight, never a verdict. A vendor's opinion must not become our judgement |

---

## 15.16 Security Constraints

| # | Constraint |
|---|---|
| 1 | **An unevidenced verdict cannot be constructed.** The invariant is at the aggregate boundary, not in the renderer |
| 2 | **`SAFE` is never a default.** Scoring failure yields `UNKNOWN` with a reason (P-FR-10) |
| 3 | **Evidence is never copied into this context.** It is resolved through Transcript's access control, including break-glass |
| 4 | **`NumberReputation` is keyed by keyed hash** and carries no subscriber linkage |
| 5 | **k-anonymity is enforced at aggregation, not at query.** A suppressed reputation is not stored in servable form |
| 6 | **Report notes are `SENSITIVE`** and are never rendered back to any other user |
| 7 | **Rule proposal and approval are separate operators** |
| 8 | **Case-scoped evidence access only.** There is no open search over subscriber calls for analysts |
| 9 | **No feature flag can disable scoring** (I11). Representing it as toggleable would be a lie in the shape of a UI |
| 10 | **An injection attempt is a security event first**, admitted as a `RiskSignal` but never sufficient alone for a fraud verdict |
