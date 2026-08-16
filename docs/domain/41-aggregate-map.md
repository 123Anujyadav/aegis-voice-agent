# 41 · Aggregate Map

Every aggregate in the platform, its root, its contents, and how it relates to
the others.

---

## 41.1 The complete inventory

**37 aggregates across eleven contexts.**

| Context | Aggregate root | Contains | Store | Lifetime |
|---|---|---|---|---|
| **Identity** | `Subscriber` | `Device[]`, `ConsentRecord[]`, `RetentionPreference` | `identity` | Account lifetime |
| | `AuthSession` | — | `identity` | 90 days |
| | `VerificationChallenge` | — | Redis | Minutes |
| | `ErasureRequest` | `StoreErasureStatus[]` | `identity` | `LEGAL_HOLD` |
| **Consumer** | `ScreeningPreferences` | `AvailabilityWindow[]` | `identity` | Account lifetime |
| | `AssistantProfile` | — | `identity` | Line lifetime |
| | `CallerList` | `CallerListEntry[]` | `identity` | Account lifetime |
| | `UserCallAction` | — | `identity` | Retention-bound |
| | `VerdictDispute` | — | `identity` | Retention-bound |
| **Telephony** | `Line` | `ForwardingConfiguration`, `ForwardingVerification[]` | `telephony` | Line lifetime |
| | `DirectInwardDialNumber` | — | `telephony` | Pool lifetime |
| | `CallSession` | `CallLeg[]`, `CallTimelineEvent[]` | `telephony` | Retention-bound |
| | `CarrierProfile` | `CarrierQuirk[]` | `telephony` | Reference |
| **Voice** | `VoiceSession` | `SpeechSegment[]` | **Redis only** | Seconds |
| | `RecordingArtefact` | — | `content` + S3 | 7–90 days |
| | `VoiceProfile` | — | `content` | Reference |
| **AI Orchestration** | `ScreeningConversation` | `ConversationTurn[]`, `TierTransition[]`, `ToolInvocation[]`, `SafetyEvent[]` | **Redis only** | Seconds |
| | `Transcript` | `TranscriptTurn[]`, `TranscriptAnnotation[]` | `content` | 7–180 days |
| | `CallSummary` | — | `content` | With transcript |
| | `AssistantSession` | `AssistantMessage[]`, `ProposedAction[]` | Redis; `content` if pinned | Session |
| | `PromptTemplate` | — | `content` | Permanent |
| | `PromptRollout` | `RollbackTrigger[]` | `content` | Permanent |
| | `EvaluationRun` | `GateResult[]` | `content` | Permanent |
| **Fraud** | `FraudAssessment` | `EvidenceReference[]`, `RiskSignal[]` | `content` | Retention-bound |
| | `NumberReputation` | — | `content` | Decaying |
| | `FraudCase` | `CaseResolution` | `content` | Permanent |
| | `AbuseReport` | — | `content` | Retention-bound |
| | `FraudPattern` | — | `content` | Reference |
| | `DetectionRule` | — | `content` | Permanent |
| **Business** | `Organisation` | `Membership[]`, `Invitation[]` | `identity` | Org lifetime |
| | `BusinessLine` | `RoutingPolicy`, `RoutingRule[]`, `BusinessHours` | `telephony` | Line lifetime |
| | `BusinessContact` | `ContactNote[]` | `identity` | Org lifetime |
| | `ApiKey` | — | `identity` | Until revoked |
| | `WebhookEndpoint` | `DeliveryAttempt[]` | `identity` | Until removed |
| | `VerifiedBusinessIdentity` | — | `identity` | Reference |
| **Billing** | `Subscription` | `Entitlement[]` | `billing` | Account lifetime |
| | `Invoice` | `InvoiceLineItem[]`, `PaymentAttempt[]` | `billing` | `LEGAL_HOLD` |
| | `UsageRecord` | — | `billing` | `LEGAL_HOLD` |
| | `Plan` | `Entitlement[]`, `UsageQuota[]` | `billing` | Reference |
| | `Credit` | — | `billing` | `LEGAL_HOLD` |
| **Notifications** | `NotificationPreference` | `ChannelSetting[]` | `identity` | Account lifetime |
| | `Notification` | `DeliveryAttempt[]` | `identity` | Days |
| | `DeviceToken` | — | `identity` | Until invalidated |
| | `NotificationChannel` | — | `identity` | Reference |
| **Analytics** | `EventDefinition` | `DimensionSpec[]` | Analytical | Permanent |
| | `MetricDefinition` | `MetricThreshold[]` | Analytical | Permanent |
| | `Cohort` | — | Analytical | Permanent |
| **Administration** | `Operator` | `OperatorRole[]` | `identity` | `LEGAL_HOLD` |
| | `AccessGrant` | `RevealedField[]` | `identity` | `LEGAL_HOLD` |
| | `AuditEntry` | — | **Dedicated audit store** | `LEGAL_HOLD` |
| | `FeatureFlag` | `RolloutSpec` | `identity` | Until retired |
| | `Incident` | `IncidentEntry[]` | `identity` | `LEGAL_HOLD` |
| | `SupportAction` | — | `identity` | `LEGAL_HOLD` |
| | `AccessReview` | `Finding[]` | `identity` | `LEGAL_HOLD` |

---

## 41.2 The call graph

Every aggregate touched by one screened call, and how they reference each other.
**All references are by identity. No arrow is an object reference.**

```
                        ┌────────────────────────┐
                        │  Subscriber  «ID»      │
                        │   Device[]             │
                        │   ConsentRecord[]      │
                        └──────┬─────────────────┘
                               │ owns
              ┌────────────────┼──────────────────┬───────────────┐
              ▼                ▼                  ▼               ▼
    ┌──────────────────┐ ┌──────────────┐ ┌────────────┐ ┌──────────────────┐
    │ ScreeningPrefs   │ │ CallerList   │ │AssistantPr.│ │ Subscription «BI»│
    │ «CO»             │ │ «CO»         │ │ «CO»       │ │  Entitlement[]   │
    └──────────────────┘ └──────┬───────┘ └─────┬──────┘ └──────────────────┘
                                │               │
                     PreFilterDecision          │ read at conversation
                     (SHARED KERNEL,            │ start, held IMMUTABLY
                      computed ON DEVICE)       │
                                │               │
                                ▼               │
    ┌──────────────────┐  ┌──────────────────┐  │
    │ Line  «TE»       │─▶│ CallSession «TE» │  │
    │  ForwardingConfig│  │  CallLeg[]       │  │
    └────────┬─────────┘  │  Timeline[]      │  │
             │            └────┬─────────────┘  │
             ▼                 │                │
    ┌──────────────────┐       │  ◀─────────────┼──── referenced BY, never
    │ DID  «TE»        │       │                │     referencing
    └──────────────────┘       │                │
                               │                │
        ┌──────────────────────┼────────────────┼─────────────────┐
        ▼                      ▼                ▼                 ▼
  ┌─────────────┐   ┌────────────────────┐  ┌──────────────┐  ┌──────────────┐
  │VoiceSession │   │ScreeningConversation│  │FraudAssess.  │  │ UsageRecord  │
  │ «VO» REDIS  │──▶│ «AI»  REDIS         │  │ «FR»         │  │ «BI»         │
  │ SpeechSeg[] │   │  Announcement (VO)  │  │ Evidence[]───┼─┐│              │
  └──────┬──────┘   │  ConversationTurn[] │  │ RiskSignal[] │ │└──────────────┘
         │          └──────────┬──────────┘  └──────────────┘ │
   consent gate                │                              │
         ▼                     ▼                              │
  ┌──────────────┐   ┌──────────────────┐                     │
  │RecordingArt. │   │ Transcript «AI»  │◀────────────────────┘
  │ «VO»  S3     │   │  TranscriptTurn[]│   EvidenceReference
  │ consentRef ✱ │   │  APPEND-ONLY     │   points here — a POINTER,
  └──────────────┘   └────────┬─────────┘   never a copy
                              │
                              ▼
                     ┌──────────────────┐
                     │ CallSummary «AI» │
                     │ provenance=MODEL │
                     └──────────────────┘

                  ┌ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ┐
                    CallHistoryEntry  «CO»
                  │ READ MODEL — projected from  │
                    every box above. Never the
                  │ source of truth for anything.│
                  └ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ┘

  ✱ consentRecordRef is LEGAL_HOLD and OUTLIVES the audio it authorised
```

---

## 41.3 The business graph

```
  ┌──────────────────────────────┐         ┌──────────────────────────────┐
  │  Organisation  «BU»          │         │  Subscription  «BI»          │
  │   [identity cluster]         │────────▶│   [billing cluster]          │
  │   ┌────────────────────────┐ │         │   Entitlement[]              │
  │   │ Membership[]           │ │         │    RiskVerdictVisible ALWAYS │
  │   │  role · assignedLines  │ │         └──────────────────────────────┘
  │   │  consentRef            │ │
  │   ├────────────────────────┤ │         ┌──────────────────────────────┐
  │   │ Invitation[]           │ │         │  BusinessContact  «BU»       │
  │   │  bound to invitedMsisdn│ │────────▶│   ContactNote[]  SENSITIVE   │
  │   │  SERVER-SIDE           │ │         └──────────────────────────────┘
  │   └────────────────────────┘ │
  └──────────────┬───────────────┘         ┌──────────────────────────────┐
                 │                         │  ApiKey · WebhookEndpoint    │
        ╔════════╧═════════════╗           │  plaintext ONCE · auto-      │
        ║  CLUSTER BOUNDARY    ║           │  disable after 24 h          │
        ║  ⇒ THIS IS A SAGA    ║           └──────────────────────────────┘
        ╚════════╤═════════════╝
                 ▼
  ┌──────────────────────────────┐         ┌──────────────────────────────┐
  │  BusinessLine  «BU»          │────────▶│  Line  «TE»                  │
  │   [telephony cluster]        │  <ref>  │   ForwardingConfiguration    │
  │   ┌────────────────────────┐ │         └──────────────────────────────┘
  │   │ RoutingPolicy          │ │
  │   │  fallback MANDATORY    │ │         ┌──────────────────────────────┐
  │   │  ┌──────────────────┐  │ │         │ VerifiedBusinessIdentity «BU»│
  │   │  │ RoutingRule[]    │  │ │         │  NOT attached to Organisation│
  │   │  └──────────────────┘  │ │         │  — a verified business need  │
  │   │ BusinessHours          │ │         │    not be our customer       │
  │   └────────────────────────┘ │         └──────────────────────────────┘
  └──────────────────────────────┘

  ╔══════════════════════════════════════════════════════════════════════╗
  ║  A PERSONAL Line APPEARS NOWHERE IN THIS GRAPH, AT ANY ROLE, UNDER   ║
  ║  ANY CONFIGURATION.  INV-BU-1 IS AN ABSENCE, NOT A PERMISSION CHECK. ║
  ╚══════════════════════════════════════════════════════════════════════╝
```

---

## 41.4 Aggregates that are deliberately small

Small aggregates are a design achievement, not an oversight. Each of these was
argued for a larger boundary and reduced.

| Aggregate | Rejected larger boundary | Why the smaller one wins |
|---|---|---|
| `AuthSession` | Inside `Subscriber` | Sessions churn on every token refresh. Containing them would write the subscriber aggregate dozens of times a day for no invariant |
| `UserCallAction` | Inside `CallHistoryEntry` | The entry is a projection. Actions are facts the user created and must survive a projection rebuild |
| `VerdictDispute` | Inside `FraudAssessment` | The assessment is immutable and owned by Fraud. A dispute is a Consumer fact **about** it, and merging them would make disputes mutate assessments |
| `CallSummary` | Inside `Transcript` | Regenerating a summary must not touch the transcript's append-only guarantee |
| `RecordingArtefact` | Inside `CallSession` | Audio has its own consent gate, its own retention, and its own store. Merging would put an S3 lifecycle inside a telephony aggregate |
| `Invoice` | Inside `Subscription` | An invoice is `LEGAL_HOLD` and immutable; a subscription changes monthly |
| `AccessGrant` | Inside `Operator` | A grant is about a **subject**, not only an operator, and must be queryable by subject for DPDP |
| `Notification` | Inside `NotificationPreference` | Preferences are long-lived; notifications live for days and are high-volume |

---

## 41.5 Aggregates that are deliberately large

| Aggregate | Contains | Why the boundary must be this wide |
|---|---|---|
| `Subscriber` | Devices **and** consents | Enrolling a device displaces another (P-ID-1) and consent state must be atomic with the identity it belongs to. Splitting either would make a legal record eventually consistent with the person it concerns |
| `CallerList` | **Both** blocklist and allowlist | Moving a number between them must be atomic (INV-CO-1). Two aggregates would permit a number in both, which is a routing contradiction |
| `FraudAssessment` | Verdict **and** evidence | An assessment saved before its evidence is exactly the failure this context exists to prevent (INV-FR-3) |
| `Line` | Line **and** forwarding configuration | A line claiming to be screened while its forwarding lapsed is the platform's highest-severity silent failure |
| `Organisation` | Memberships **and** invitations | Exactly one owner, at all times (INV-BU-2), across both accepted members and pending invitations |
| `Subscription` | Plan **and** entitlements | A plan change and its capability consequences are one decision, and two sources of truth is how a cancelled plan keeps working |

---

## 41.6 The three ephemeral aggregates

Only three aggregates in the platform never reach durable storage. All three are
on the hot path, and that is not a coincidence — ADR-0009 C3 puts live call
state in Redis precisely so no disk write sits inside the latency budget.

| Aggregate | Lifetime | What survives it |
|---|---|---|
| `VoiceSession` | Seconds | `RecordingArtefact` if consented; finalised utterances as transcript turns |
| `ScreeningConversation` | Seconds | `Transcript`, `CallSummary`, `FraudAssessment` |
| `AssistantSession` | Session | Nothing, unless pinned |

**Nothing durable depends on their survival** (INV-VO-8). If Redis is lost
mid-call, the call ends gracefully; it does not hang, and no record is
corrupted.

---

## 41.7 Aggregates by privacy classification

The compliance view: what an erasure request must reach, and what survives it.

| Highest classification | Aggregates |
|---|---|
| **`SECRET`** | `Subscriber` (credential ref), `AuthSession`, `VerificationChallenge`, `DeviceToken`, `ApiKey`, `Operator` (hardware key), `Subscription` (provider token) |
| **`SENSITIVE`** | `Transcript`, `CallSummary`, `RecordingArtefact`, `AssistantSession`, `AssistantProfile` (custom instruction), `AbuseReport` (note), `BusinessContact` (notes), `AccessGrant` (reason), `VerdictDispute` (note) |
| **`PERSONAL`** | `Subscriber`, `Line`, `CallSession`, `CallerList`, `UserCallAction`, `CallHistoryEntry`, `Organisation`, `Invitation`, `Invoice` (tax identity), `Operator` |
| **`INTERNAL` / `PUBLIC` only** | `DirectInwardDialNumber`, `CarrierProfile`, `VoiceProfile`, `NumberReputation`, `FraudPattern`, `DetectionRule`, `Plan`, `NotificationChannel`, `FeatureFlag`, **every Analytics aggregate** |

### What survives erasure

| Aggregate | Survives | Why |
|---|---|---|
| `ConsentRecord` | **Yes**, pseudonymised | Lawfulness must stay provable after the person stops being identifiable |
| `Invoice`, `UsageRecord`, `Credit` | **Yes** | Financial and tax obligation |
| `AuditEntry`, `AccessGrant` | **Yes** | The record of access must outlive the data accessed |
| `Operator`, `SupportAction`, `Incident` | **Yes** | Operator actions remain attributable |
| `Announcement` version reference | **Yes** | Proof of what a caller was told on a given date |
| `NumberReputation` | **Yes** | It never contained a subscriber identifier to erase (INV-FR-5) |
| Everything else | **No** | Deleted across six stores plus backups by `ErasureRequest` |

**Every survival is a named `RetentionException` on the `ErasureRequest`**, and
the Subscriber is told which and why — not discovered later.

---

## 41.8 Reading the map

| If you are asking | Look at |
|---|---|
| "Where does this data live?" | [§41.1](#411-the-complete-inventory) |
| "What does one call touch?" | [§41.2](#412-the-call-graph) |
| "Can an employer see my personal calls?" | [§41.3](#413-the-business-graph) — there is nothing to see |
| "Why isn't X inside Y?" | [§41.4](#414-aggregates-that-are-deliberately-small) |
| "Why is this aggregate so big?" | [§41.5](#415-aggregates-that-are-deliberately-large) |
| "What happens if Redis dies?" | [§41.6](#416-the-three-ephemeral-aggregates) |
| "What does erasure have to reach?" | [§41.7](#417-aggregates-by-privacy-classification) |
| "Who talks to whom?" | [40 — interaction matrix](40-cross-context-interaction-matrix.md) |
