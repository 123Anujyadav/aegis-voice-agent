# 19 · Analytics

**Subdomain:** Generic · **Prefix:** `AN` · **Topic domain:** `analytics`

> **This context's most important domain service rejects things.** A rich
> analytics domain here would be a privacy failure, and the model is
> deliberately impoverished.

---

## 19.1 Purpose

Measure the product accurately enough to run it, while making it structurally
impossible for personal data to enter the measurement.

## 19.2 Responsibilities

**Owns**

- `EventDefinition` — the catalogue of what may be emitted, and the guard that
  validates it against `annotations.proto`
- `MetricDefinition` and computed metrics
- `Cohort` and `QualitySignal`
- The k-anonymity floor and its suppression behaviour

**Does not own**

| Not owned | Owned by |
|---|---|
| Any production data | The owning contexts. **Production databases are never queried for analytics** (ADR-0009 §14) |
| Analytics consent | Identity |
| The operator dashboards | Administration renders; Analytics computes |
| Crash reporting | A separate purpose with its own consent |

### The `Separate Ways` relationship with Identity

Analytics and Identity are the one pair in the context map related by **Separate
Ways** ([00 §0.3](00-strategic-design.md)). Analytics never learns who anyone
is, and there is no integration to build. That absence is the design.

---

## 19.3 Domain Entities

### `EventDefinition` — aggregate root

The catalogue entry that must exist before an event may be emitted.

```
id            : EventDefinitionId       PUBLIC · STANDARD
name          : EventName               PUBLIC · STANDARD
surface       : Surface                 PUBLIC · STANDARD
area          : Area                    PUBLIC · STANDARD
dimensions    : DimensionSpec[] <owned> PUBLIC · STANDARD
sourceContext : ContextName             PUBLIC · STANDARD
status        : DefinitionStatus        PUBLIC · STANDARD
publishedAt   : Instant                 PUBLIC · STANDARD
deprecatedAt  : Instant?                PUBLIC · STANDARD
```

**Relationships** — References the schema fields each dimension derives from, so
the classification guard can resolve them. References nothing personal.

**Lifecycle** — Proposed, **validated against `annotations.proto`**, published,
deprecated, retired. **An event with no published definition is dropped at
ingestion** (INV-AN-2) — an undeclared event is one nobody reviewed, and the
whole control depends on review.

**Validation Rules**

- **A definition referencing any field classified `PERSONAL` or `SENSITIVE` is
  rejected at publication** (P-AN-1). Not hashed, not truncated, not "just the
  first three digits" — rejected.
- An **unclassified** field is `PERSONAL` (I8) and therefore also rejected. The
  guard fails closed, which means a new schema field cannot leak into analytics
  by default.
- `name` must match `<surface>.<area>.<object>_<verb_past_tense>`.

**Privacy Classification** — `PUBLIC` throughout. A catalogue of what we measure
is not itself sensitive, and publishing it is a transparency asset.

**Audit Requirements** — **Change** on publication and deprecation. A rejected
definition is recorded with its reason — the rejection log is evidence the
control is working.

---

### `AnalyticsEvent` — derived, not an aggregate

```
definitionId  : EventDefinitionId <ref>  PUBLIC · SHORT
occurredAt    : Instant                  PUBLIC · SHORT
dimensions    : DimensionValue[]         PUBLIC · SHORT
sessionRef    : EphemeralSessionRef      INTERNAL · SHORT
```

**Relationships** — References its definition. **References no subscriber, no
device, no call, and no organisation.**

**Lifecycle** — Emitted client- or server-side, validated against its
definition, ingested into the analytical store, aggregated, expired.

**Validation Rules** — `sessionRef` is an **ephemeral, rotating** identifier
that cannot be joined across sessions. A stable pseudonymous identifier would be
`PERSONAL` (`ClientContext.install_id` is classified exactly that way in the
frozen `types.proto`) and therefore inadmissible here.

**Privacy Classification** — `PUBLIC`, `SHORT`. If any value in an event were
higher than `PUBLIC`, the definition should not have been published.

**Audit Requirements** — None. There is nothing personal to audit, which is the
point.

---

### `MetricDefinition` — aggregate root

```
id             : MetricId              PUBLIC · STANDARD
name           : MetricName            PUBLIC · STANDARD
expression     : MetricExpression      PUBLIC · STANDARD
sourceEvents   : EventDefinitionId[]   PUBLIC · STANDARD
window         : AggregationWindow     PUBLIC · STANDARD
segments       : DimensionSpec[]       PUBLIC · STANDARD
kAnonymityFloor: Int                   PUBLIC · STANDARD
thresholds     : MetricThreshold[]     PUBLIC · STANDARD
owner          : TeamHandle            PUBLIC · STANDARD
```

**Lifecycle** — Defined, published, monitored, retired. Metrics with
`thresholds` raise `analytics.metric.threshold_breached.v1` — which is how the
frozen review triggers in ADR-0002 §16 and ADR-0011 §14 become observable rather
than aspirational.

**Validation Rules** — Every metric declares a `kAnonymityFloor`. A metric with
no floor cannot be published, because a segment of one is a person.

---

### `QualitySignal`

The product's feedback loop, modelled explicitly because it is the most valuable
measurement we take.

```
id            : SignalId          PUBLIC · STANDARD
signalType    : QualitySignalType PUBLIC · STANDARD
subjectClass  : SubjectClass      PUBLIC · STANDARD
observedValue : Value             PUBLIC · STANDARD
window        : AggregationWindow PUBLIC · STANDARD
```

| `QualitySignalType` | Measures | Why it matters |
|---|---|---|
| `VERDICT_AGREEMENT` | Subscribers acting in line with a verdict | Real-world precision, judged by the only person who knows |
| `VERDICT_DISPUTE_RATE` | Explicit disagreement, by pattern and confidence band | The strongest available precision signal |
| `EMERGENCY_FALSE_POSITIVE` | Emergency handovers dismissed as false | **A launch-blocking metric** |
| `TAKEOVER_LATENCY` | Screening start to takeover | > 6 s means the live surface is too slow to be useful |
| `PRE_FILTER_HIT_RATE` | Calls resolved on-device | Directly determines unit economics (ADR-0002 §11) |
| `FORWARDING_UPTIME` | Per-subscriber forwarding health | The silent-failure metric; ADR-0002 §16 makes 2% a review trigger |
| `TRANSCRIPT_READ_WITHOUT_AUDIO` | Validates Invariant U6 empirically | |
| `CONSENT_WITHDRAWAL_RATE` | Per purpose | The honest measure of whether a consent was understood when granted |
| `BREAK_GLASS_FREE_RESOLUTION` | Support tickets closed without revealing PII | The privacy health of the Console |
| `NOTIFICATION_SUPPRESSION_RATE` | Interruptions correctly refused | Measures whether silence is working |

**Every one of these is computed from `PUBLIC` dimensions only.** None requires
knowing who anyone is.

---

### `Cohort`

```
id            : CohortId            PUBLIC · STANDARD
definition    : CohortExpression    PUBLIC · STANDARD
minimumSize   : Int                 PUBLIC · STANDARD
suppressed    : Boolean             PUBLIC · STANDARD
```

**Validation Rules** — A cohort below `minimumSize` is suppressed, and the
suppression is **stated** rather than rendered as an empty chart (P-AN-3) —
"suppressed for privacy" and "no data" are different facts, and conflating them
teaches analysts to distrust the tool.

---

## 19.4 Value Objects

| Value object | Definition | Notes |
|---|---|---|
| `EventName` | `<surface>.<area>.<object>_<verb>` | `android.screening.takeover_engaged` |
| `Surface` | `android` · `console` · `portal` | |
| `DimensionSpec` | name + type + **allowed values** + source classification | Closed vocabularies only; a free-text dimension is inadmissible |
| `DimensionValue` | An enumerated value or a bounded numeric | |
| `KAnonymityThreshold` | Minimum distinct subjects for a segment to be served | |
| `AggregationWindow` | `HOUR` · `DAY` · `WEEK` · `MONTH` | |
| `SuppressionReason` | `BELOW_K_ANONYMITY` · `INSUFFICIENT_DATA` · `DEFINITION_RETIRED` | **Distinguished.** Suppressed and empty are different claims |
| `EphemeralSessionRef` | Rotating, non-joinable across sessions | A stable pseudonym would be `PERSONAL` |
| `MetricThreshold` | comparator + value + severity + linked review trigger | Ties a metric to an ADR review trigger |

---

## 19.5 Aggregates

| Aggregate | Root | Contains |
|---|---|---|
| **EventDefinition** | `EventDefinition` | `DimensionSpec[]` |
| **MetricDefinition** | `MetricDefinition` | `MetricThreshold[]` |
| **Cohort** | `Cohort` | — |

`AnalyticsEvent` and `QualitySignal` are derived records and belong to no
aggregate.

```
   contracts/proto/.../annotations.proto
              │  Sensitivity · Retention · residency
              ▼
   ┌─────────────────────────────────────────────────────┐
   │       ClassificationGuardService                    │
   │                                                     │
   │   PERSONAL   ──▶ REJECT                             │
   │   SENSITIVE  ──▶ REJECT                             │
   │   SECRET     ──▶ REJECT                             │
   │   unclassified ─▶ REJECT (I8 — fails closed)        │
   │   PUBLIC / INTERNAL ─▶ admit                        │
   └──────────────────────┬──────────────────────────────┘
                          ▼
   ┌──────────────────────────────────────────┐
   │  EventDefinition  «root»                 │
   │   PUBLIC dimensions only                 │
   │   ┌────────────────────────────────────┐ │
   │   │ DimensionSpec[]                    │ │
   │   │  closed vocabularies, no free text │ │
   │   └────────────────────────────────────┘ │
   └──────────────────────┬───────────────────┘
                          │  an event without a published
                          │  definition is DROPPED
                          ▼
   ┌──────────────────────────────────────────┐
   │  AnalyticsEvent (derived)                │
   │   ephemeral session ref — NOT JOINABLE   │
   └──────────────────────┬───────────────────┘
                          ▼
   ┌──────────────────────────────────────────┐
   │  MetricDefinition  «root»                │
   │   kAnonymityFloor MANDATORY              │
   │   thresholds ──▶ ADR review triggers     │
   └──────────────────────────────────────────┘

   ╔══════════════════════════════════════════════════════════╗
   ║  NO PRODUCTION DATABASE IS EVER QUERIED FROM HERE.       ║
   ║  Kafka is the only input.  (ADR-0009 §14)                ║
   ╚══════════════════════════════════════════════════════════╝
```

---

## 19.6 Domain Services

| Service | Responsibility | Notes |
|---|---|---|
| **`ClassificationGuardService`** | Reject any `EventDefinition` whose dimensions derive from a `PERSONAL`, `SENSITIVE`, `SECRET` or **unclassified** field | **The context's reason to exist.** Reads the protobuf descriptor at build time, so it cannot drift from the schema |
| **`KAnonymitySuppressionService`** | Suppress segments below the floor, and **state the suppression** | Enforced at aggregation, not at query — a suppressed segment is not stored in servable form |
| `MetricComputationService` | Compute metrics over windows and segments | |
| `IngestionValidationService` | Drop events with no published definition | |
| `ThresholdMonitorService` | Raise breaches tied to ADR review triggers | Makes frozen review triggers observable |

### Why the guard reads the descriptor rather than a list

A hand-maintained list of forbidden fields drifts from the schema within a
sprint — exactly the failure `annotations.proto` was written to prevent. Reading
the descriptor means adding a `PERSONAL` field to any contract **automatically**
makes it inadmissible to analytics, with no reviewer required to notice.

This is Gate G9 from `UX_FREEZE`, and it is why that gate says "not achievable
by review alone".

---

## 19.7 Repositories

`EventDefinitionRepository` · `MetricDefinitionRepository` ·
`CohortRepository`

**There is no `AnalyticsEventRepository`.** Events are a stream into a separate
analytical store, not aggregates we load and mutate.

---

## 19.8 Domain Events

| Event | Payload | Consumers |
|---|---|---|
| `analytics.definition.published.v1` | definitionId, name, sourceContext | Administration |
| `analytics.definition.rejected.v1` | proposedName, rejectionReason, offendingField | **Administration** — evidence the guard is working |
| `analytics.definition.deprecated.v1` | definitionId | Administration |
| `analytics.event.dropped.v1` | name, reason | Administration |
| `analytics.segment.suppressed.v1` | metricId, segment, reason | Administration |
| `analytics.metric.threshold_breached.v1` | metricId, value, threshold, severity, reviewTrigger? | **Administration** |
| `analytics.quality.signal_computed.v1` | signalType, value, window | Administration |

Every event in this context is `PUBLIC` by construction. This is the one context
where Invariant I7 is trivially satisfied, because there is nothing else to
carry.

---

## 19.9 Commands

| Command | Refused when |
|---|---|
| `ProposeEventDefinition(name, surface, area, dimensions)` | Name malformed |
| `PublishEventDefinition(definitionId)` | **Any dimension derives from a `PERSONAL`, `SENSITIVE`, `SECRET` or unclassified field** |
| `DeprecateEventDefinition(definitionId)` | — |
| `DefineMetric(name, expression, sources, kFloor)` | `kAnonymityFloor` absent; source event not published |
| `ComputeMetric(metricId, window, segment)` | Segment below the floor → **suppressed with a stated reason**, not empty |
| `DefineCohort(expression, minimumSize)` | Minimum size below the floor |

**There is no `QueryProductionDatabase` command, and no read path to one.** That
path is closed deliberately (ADR-0009 §14).

---

## 19.10 Queries

| Query | Scope |
|---|---|
| `GetMetric(metricId, window, segment)` | Aggregates only. Suppressed segments return a stated suppression |
| `GetQualitySignals(period)` | Aggregates only |
| `GetEventCatalogue()` | Public. A published catalogue of what we measure is a transparency asset |
| `GetRejectionLog(period)` | Administration. Evidence the guard is working |

**No query in this context can return a row about a person.** Not filtered —
there are no such rows.

---

## 19.11 Policies

| # | Policy |
|---|---|
| **P-AN-1** | An `EventDefinition` referencing a `PERSONAL` or `SENSITIVE` field is rejected at publication. Hashing, truncating and partial reveal are all rejections too |
| **P-AN-2** | **Production databases are never queried for analytics.** Kafka is the only input |
| **P-AN-3** | A segment below the k-anonymity floor is suppressed, and the suppression is **stated** — never rendered as an empty result |
| **P-AN-4** | Withdrawal of `PRODUCT_ANALYTICS` consent stops emission **client-side**. It does not merely discard server-side |
| **P-AN-5** | Consent records are a legal record, not product analytics, and are exempt from the analytics opt-out. The consent's own consequence line says so |
| **P-AN-6** | Crash diagnostics are a separate purpose with a separate consent |
| **P-AN-7** | An event with no published definition is dropped at ingestion and counted |
| **P-AN-8** | An unclassified field is `PERSONAL` and therefore inadmissible (I8) |
| **P-AN-9** | A metric threshold tied to an ADR review trigger raises a breach event, so a frozen review trigger cannot pass unnoticed |

---

## 19.12 Invariants

| # | Invariant | Source |
|---|---|---|
| **INV-AN-1** | No analytics artefact contains personal data at any stage — emission, transport, storage or query | **U11** |
| **INV-AN-2** | Every emitted event has a published `EventDefinition`. Undeclared events are dropped | |
| **INV-AN-3** | An unclassified field is `PERSONAL` and cannot appear in any definition | **I8** |
| **INV-AN-4** | Every `MetricDefinition` declares a k-anonymity floor | |
| **INV-AN-5** | `EphemeralSessionRef` is not joinable across sessions | |
| **INV-AN-6** | No dimension is free text. All dimensions are closed vocabularies or bounded numerics | |
| **INV-AN-7** | There is no read path from this context to any production database | ADR-0009 §14 |
| **INV-AN-8** | Suppressed and empty are distinguishable in every result | |

---

## 19.13 State Machines

### `EventDefinition`

```
  PROPOSED ──ClassificationGuardService──┬── any dimension is PERSONAL /
      │                                  │   SENSITIVE / SECRET / unclassified
      │                                  ▼
      │                              REJECTED «terminal»
      │                              reason + offending field recorded
      │                              (the rejection log is evidence)
      │
      └── all dimensions PUBLIC / INTERNAL ──▶ PUBLISHED
                                                   │
                                              deprecate
                                                   ▼
                                              DEPRECATED ──▶ RETIRED «terminal»
                                              (still ingested,     dropped
                                               flagged)
```

### Ingestion

```
  event arrives
      │
      ├── no published definition ──▶ DROPPED, counted
      │
      ├── dimension outside its declared vocabulary ──▶ DROPPED, counted
      │
      └── valid ──▶ INGESTED ──aggregate──▶ SERVED
                                              │
                                    segment below k-floor
                                              ▼
                                          SUPPRESSED
                                    (stated, not empty)
```

---

## 19.14 Ownership

| Aspect | Value |
|---|---|
| Team | `callscreen/platform` |
| Services | Ingestion pipeline; no dedicated production service |
| Durable store | **None in production.** A separate analytical store (ClickHouse or Redshift), fed from Kafka (ADR-0009 §14 step 4) |
| CODEOWNERS | `docs/domain/19-analytics.md` |
| Data owner | `callscreen/platform` |

---

## 19.15 External Dependencies

| Dependency | Purpose | Guard |
|---|---|---|
| **Kafka (MSK)** | The only input | Consumes published domain events, all of which already carry identifiers rather than content (I7) |
| **`annotations.proto` descriptors** | The classification source | Read at build time, so the guard cannot drift from the schema |
| Analytical store | Aggregation | Region-locked. Contains no personal data, which is what makes it safe to be a separate store |
| Crash reporting provider | Diagnostics | **Separate purpose, separate consent, separate pipeline.** Not modelled here |

---

## 19.16 Security Constraints

| # | Constraint |
|---|---|
| 1 | **No personal data enters this context at any stage** (INV-AN-1) |
| 2 | **The guard fails closed.** Unclassified is `PERSONAL` and therefore rejected (I8) |
| 3 | **Hashing is not a mitigation.** A hashed MSISDN identifies a person and is rejected like an unhashed one |
| 4 | **Session references rotate** and cannot be joined across sessions |
| 5 | **k-anonymity is enforced at aggregation**, not at query. A suppressed segment is not stored in servable form |
| 6 | **No production database is reachable from this context** |
| 7 | **Analytics opt-out is honoured by not emitting**, client-side, not by discarding server-side |
| 8 | **The rejection log is retained** as evidence the control operates |
| 9 | **The event catalogue is publishable** — we can show a regulator exactly what we measure |
| 10 | **Crash reporting is a separate purpose** with its own consent and its own pipeline |
