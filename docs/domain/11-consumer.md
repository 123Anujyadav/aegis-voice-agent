# 11 · Consumer

**Subdomain:** Supporting · **Prefix:** `CO` · **Topic domain:** `consumer`

---

## 11.1 Purpose

Hold the Subscriber's own instructions about how their calls should be handled,
and their own record of what happened — including the decisions they made about
it.

## 11.2 Responsibilities

**Owns**

- Screening preferences and availability rules
- The `AssistantProfile` — how the Screening Agent should answer, in this
  Subscriber's name
- `DisclosureScope` — what the Screening Agent may reveal about them (I4)
- Blocklist and allowlist
- The **on-device pre-filter decision** (ADR-0002 Stage 1)
- `CallHistoryEntry` — the read model the app displays
- `UserCallAction` and `VerdictDispute` — the Subscriber's own decisions, which
  are part of the record

**Does not own**

| Not owned | Owned by |
|---|---|
| The `CallSession` itself | Telephony |
| The `Transcript` and `CallSummary` | AI Orchestration |
| The `FraudAssessment` | Fraud Detection |
| Consent and retention | Identity |
| Entitlements | Billing |
| Notification channel preferences | Notifications |

> **`CallHistoryEntry` is a projection, never a source of truth** (INV-CO-4). It
> is assembled from Telephony, AI Orchestration and Fraud events for display. No
> decision anywhere in the platform reads it.

---

## 11.3 Domain Entities

### `ScreeningPreferences` — aggregate root, one per Subscriber

**Attributes**

```
subscriberId     : SubscriberId <ref>          INTERNAL · STANDARD
availabilityMode : AvailabilityMode            PUBLIC   · STANDARD
windows          : AvailabilityWindow[] <owned> PUBLIC  · STANDARD
screenUnknownOnly: Boolean                     PUBLIC   · STANDARD
updatedAt        : Instant                     INTERNAL · STANDARD
```

**Relationships** — References `Subscriber` by identity. Consulted by Telephony
at admission and by the on-device pre-filter.

**Lifecycle** — Created with defaults at subscriber activation; never deleted
while the Subscriber exists; destroyed on erasure.

**Validation Rules** — A configuration that would disable screening entirely is
permitted but must be explicit — it is the Subscriber's call — and the command
records that the consequence was stated. Windows may not overlap ambiguously; a
gap is legal and is reported.

**Privacy Classification** — `PUBLIC`/`INTERNAL` throughout. Preferences are not
personal data; the identifier they attach to is.

**Audit Requirements** — **Change** level.

---

### `AssistantProfile` — aggregate root, one per Line

**Attributes**

```
id               : AssistantProfileId          INTERNAL  · STANDARD
lineId           : LineId <ref>                INTERNAL  · STANDARD
displayName      : PersonName?                 PERSONAL  · STANDARD · residency-bound
greetingStyle    : GreetingStyle               PUBLIC    · STANDARD
customInstruction: CustomInstruction?          SENSITIVE · STANDARD · residency-bound
voiceId          : VoiceId <ref>               PUBLIC    · STANDARD
languages        : LanguageTag[]               PUBLIC    · STANDARD
disclosureScope  : DisclosureScope             PERSONAL  · STANDARD
updatedAt        : Instant                     INTERNAL  · STANDARD
```

**Relationships** — References a Telephony `Line` and a Voice `VoiceProfile` by
identity. Consumed by AI Orchestration when constructing a
`ScreeningConversation`.

**Lifecycle** — Created at onboarding with defaults. `disclosureScope` is
created **empty** and widened only by explicit per-item command.

**Validation Rules** — `customInstruction` is bounded at 500 characters and must
pass `InstructionValidationService`; instructions that would alter the
Announcement (I1), extract subscriber data beyond `disclosureScope` (I4), or
instruct the agent to claim to be a human are **rejected at the command
boundary, not sanitised**. `displayName` is optional; absent, the agent says
"the person you're calling", which is a valid and more private choice.

**Privacy Classification** — `customInstruction` is `SENSITIVE`: it is
free text written by the Subscriber and may contain anything about their life.
`disclosureScope` is `PERSONAL` because its contents describe the Subscriber.

**Audit Requirements** — **Change** level, and specifically on every widening of
`disclosureScope`, because that is the moment more of the Subscriber becomes
visible to strangers.

---

### `CallerList` — aggregate root, one per Subscriber

**Attributes**

```
subscriberId : SubscriberId <ref>        INTERNAL · STANDARD
entries      : CallerListEntry[] <owned>
version      : Int                       INTERNAL · STANDARD
```

### `CallerListEntry`

```
id           : EntryId                   INTERNAL · STANDARD
number       : PhoneNumber               PERSONAL · STANDARD · residency-bound
listType     : ListType                  PUBLIC   · STANDARD
label        : String?                   PERSONAL · STANDARD
source       : ListSource                PUBLIC   · STANDARD
callReference: CallSessionId?  <ref>     INTERNAL · STANDARD
createdAt    : Instant                   INTERNAL · STANDARD
lastAttemptAt: Instant?                  PERSONAL · STANDARD
removedAt    : Instant?                  INTERNAL · STANDARD
```

**Relationships** — Contained in `CallerList`. `lastAttemptAt` is updated by a
Telephony event, making the list evidence that the block is working.

**Lifecycle** — Added manually, from a call, or by import. Removed by the
Subscriber, with a 5-second undo window during which removal is compensable.
Adding to one list removes from the other atomically (P-CO-1) — the two lists
are one aggregate precisely so that this is a single transaction.

**Validation Rules** — A number is in at most one list (INV-CO-1). A Subscriber's
own MSISDN cannot be blocked. Emergency service numbers cannot be blocked.

**Privacy Classification** — `PERSONAL`. A blocklist is a statement about the
Subscriber's relationships as much as about the numbers.

**Audit Requirements** — **Change** level. `lastAttemptAt` updates are not
audited individually — they are high-frequency and derived.

---

### `CallHistoryEntry` — read model, **not an aggregate**

**Attributes**

```
callSessionId    : CallSessionId <ref>       INTERNAL  · STANDARD
subscriberId     : SubscriberId <ref>        INTERNAL  · STANDARD
lineId           : LineId <ref>              INTERNAL  · STANDARD
callerNumber     : PhoneNumber               PERSONAL  · STANDARD · residency-bound
callerIdentity   : CallerIdentity?           PERSONAL  · STANDARD
occurredAt       : Instant                   PERSONAL  · STANDARD
duration         : Duration?                 INTERNAL  · STANDARD
outcome          : Outcome                   INTERNAL  · STANDARD
screened         : Boolean                   PUBLIC    · STANDARD
verdictState     : VerdictState              PUBLIC    · STANDARD
riskLevel        : RiskLevel?                PUBLIC    · STANDARD
confidence       : Confidence?               PUBLIC    · STANDARD
summaryRef       : SummaryId?  <ref>         INTERNAL  · STANDARD
transcriptRef    : TranscriptId? <ref>       INTERNAL  · STANDARD
recordingAvailable: Boolean                  PUBLIC    · STANDARD
actions          : UserCallAction[] <ref>    INTERNAL  · STANDARD
```

**Relationships** — Projected from `telephony.call.*`, `ai.transcript.*`,
`ai.summary.*` and `fraud.assessment.*`. References everything, owns nothing.

**Lifecycle** — Created on `telephony.call.received`. Enriched as events arrive.
Fields decay as their sources reach retention: when a `Transcript` is deleted at
90 days, `transcriptRef` becomes unresolvable and the entry states so as policy,
not as an error. Deleted on erasure or on explicit per-call deletion.

**Validation Rules** — `verdictState` distinguishes `PENDING`, `ASSESSED` and
`NOT_ASSESSED`. **Pending and unassessable are different facts** and the model
keeps them different — a pending verdict renders as a skeleton, never as
`UNKNOWN`. A non-screened call has no `summaryRef`, no `transcriptRef` and no
`riskLevel`: claiming otherwise would assert we assessed something we never
touched.

**Privacy Classification** — Inherits the strictest classification of its
sources. `callerNumber` and `occurredAt` are `PERSONAL`; the referenced
transcript remains `SENSITIVE` in its owning context and is not copied here.

**Audit Requirements** — **Change** on deletion. Reads by the Subscriber are not
audited; reads through Administration are **Access** level.

---

### `UserCallAction`

**Attributes**

```
id            : ActionId                  INTERNAL · STANDARD
callSessionId : CallSessionId <ref>       INTERNAL · STANDARD
subscriberId  : SubscriberId <ref>        INTERNAL · STANDARD
actionType    : UserActionType            PUBLIC   · STANDARD
occurredAt    : Instant                   PERSONAL · STANDARD
compensates   : ActionId?                 INTERNAL · STANDARD
```

**Relationships** — References a `CallSession`. Displayed in the Telephony
`CallTimeline` — the user's decisions are part of the call's record, not a
separate log.

**Lifecycle** — **Append-only.** Undo appends a compensating action referencing
the original; nothing is deleted (INV-CO-5).

**Validation Rules** — `compensates` must reference an action by the same
Subscriber on the same call, within the undo window where one applies.

**Privacy Classification** — `PERSONAL`. What someone chose to block is about
them.

**Audit Requirements** — **Change** level.

---

### `VerdictDispute`

**Attributes**

```
id              : DisputeId               INTERNAL  · STANDARD
assessmentId    : AssessmentId <ref>      INTERNAL  · STANDARD
callSessionId   : CallSessionId <ref>     INTERNAL  · STANDARD
subscriberId    : SubscriberId <ref>      INTERNAL  · STANDARD
assertedLevel   : RiskLevel               PUBLIC    · STANDARD
note            : String?                 SENSITIVE · STANDARD
raisedAt        : Instant                 PERSONAL  · STANDARD
```

**Relationships** — References a Fraud `FraudAssessment`. **Does not mutate it**
(P-CO-4).

**Lifecycle** — Created by the Subscriber. Immediately downgrades the verdict's
presentation in *their* interface. Consumed by Fraud Detection as a
prioritisation and precision signal.

**Validation Rules** — One open dispute per assessment per subscriber.

**Privacy Classification** — `note` is `SENSITIVE` free text.

**Audit Requirements** — **Change** level. This is the product's highest-value
quality signal and its provenance must be intact.

---

## 11.4 Value Objects

| Value object | Definition |
|---|---|
| `PhoneNumber` | Shared kernel. E.164, normalised. Equality on the normalised form |
| `ListType` | `BLOCK` · `ALLOW`. Shared kernel with Telephony |
| `ListSource` | `MANUAL` · `FROM_CALL` · `IMPORTED` · `SUGGESTED` |
| `GreetingStyle` | `BRIEF` · `BALANCED` · `DETAILED` |
| `CustomInstruction` | Bounded text ≤ 500 chars, validated. Not raw string |
| `DisclosureScope` | Set of `DisclosureItem`: `FIRST_NAME` · `AVAILABILITY` · `CALLBACK_PREFERENCE` · `ALTERNATE_CONTACT_EXISTS`. **Default empty.** Shared kernel with AI Orchestration |
| `AvailabilityMode` | `ALWAYS` · `OUTSIDE_CONTACT_HOURS` · `SCHEDULE` · `NEVER` |
| `AvailabilityWindow` | dayOfWeek + start + end + timezone |
| `PreFilterDecision` | `RING_THROUGH` · `REJECT` · `SILENCE` · `SCREEN`. Shared kernel with Telephony |
| `UserActionType` | `BLOCKED` · `UNBLOCKED` · `ALLOWED` · `REPORTED` · `DISPUTED` · `TOOK_OVER` · `DECLINED` · `DELETED` |
| `VerdictState` | `PENDING` · `ASSESSED` · `NOT_ASSESSED` |
| `CallerIdentity` | `Verified(businessIdentityId)` · `Contact(localName)` · `Unknown`. **Never a caller-claimed name** |

**`CallerIdentity` deserves emphasis.** A name a caller *said* is untrusted
speech and lives in the transcript, quoted and attributed. Only a
`VerifiedBusinessIdentity` or a local contact match becomes a `CallerIdentity` —
which is what makes it safe to render at title weight (U10).

---

## 11.5 Aggregates

| Aggregate | Root | Contains | Why this boundary |
|---|---|---|---|
| **ScreeningPreferences** | `ScreeningPreferences` | `AvailabilityWindow[]` | Per subscriber; low churn |
| **AssistantProfile** | `AssistantProfile` | — | Per line, so a business member's work line and personal line differ |
| **CallerList** | `CallerList` | `CallerListEntry[]` | **Both lists in one aggregate** so that moving a number between them is atomic (P-CO-1) |
| **UserCallAction** | `UserCallAction` | — | Append-only, high volume, no cross-entity invariant |
| **VerdictDispute** | `VerdictDispute` | — | Independent lifecycle from the assessment it disputes |

`CallHistoryEntry` is a read model and appears in no aggregate.

```
┌──────────────────────┐   ┌──────────────────────────┐
│ ScreeningPreferences │   │  CallerList  «root»      │
│ «root»               │   │  ┌────────────────────┐  │
│  availabilityMode    │   │  │ CallerListEntry[]  │  │
│  windows[]           │   │  │ number · listType  │  │
└──────────────────────┘   │  │ ONE aggregate so a │  │
                           │  │ move is atomic     │  │
┌──────────────────────┐   │  └────────────────────┘  │
│ AssistantProfile     │   └──────────────────────────┘
│ «root»               │
│  displayName?        │   ┌──────────────────────────┐
│  customInstruction?  │   │ UserCallAction  «root»   │
│  disclosureScope ◀───┼───┤ append-only              │
│    DEFAULT EMPTY     │   └──────────────────────────┘
└──────────────────────┘   ┌──────────────────────────┐
                           │ VerdictDispute  «root»   │
   ┌ ─ ─ ─ ─ ─ ─ ─ ─ ─ ┐   └──────────────────────────┘
     CallHistoryEntry
   │ READ MODEL        │   projected from telephony.* ·
     not an aggregate      ai.* · fraud.* events
   └ ─ ─ ─ ─ ─ ─ ─ ─ ─ ┘
```

---

## 11.6 Domain Services

| Service | Responsibility | Notes |
|---|---|---|
| **`PreFilterDecisionService`** | Given an inbound `PhoneNumber`, produce a `PreFilterDecision` from contacts, `CallerList`, and cached `NumberReputation` | **Executes on the handset.** The only domain service in the platform whose execution location is part of its specification — ADR-0002 §5 makes on-device evaluation the reason the unit economics work |
| `InstructionValidationService` | Reject a `CustomInstruction` that would alter the Announcement, exceed `DisclosureScope`, or instruct impersonation | Rejects; never sanitises. A silently rewritten instruction is worse than a refused one |
| `CallHistoryProjectionService` | Assemble and maintain `CallHistoryEntry` from upstream events | Idempotent; tolerates out-of-order arrival |
| `ProtectionSummaryService` | Compute the counts shown on the Protection surface | Every count must be traceable to a list — an unverifiable statistic is a claim |

### Why the pre-filter is a domain service and not an entity method

It reads three sources across two contexts (Consumer's list, the device's
contacts, Fraud's cached reputation) and belongs to none of them. Modelling it
on `CallerList` would make the list responsible for reputation it does not own;
modelling it in Telephony would put a subscriber preference in the call path.

Its inputs and output cross a boundary, which is why `PreFilterDecision` is a
Shared Kernel value object.

---

## 11.7 Repositories

`ScreeningPreferencesRepository` · `AssistantProfileRepository` ·
`CallerListRepository` · `UserCallActionRepository` · `VerdictDisputeRepository`

`CallHistoryEntry` is served by a **read-model query service**, not a
repository — the distinction is deliberate, because a repository implies an
aggregate you may mutate.

---

## 11.8 Domain Events

| Event | Payload | Consumers |
|---|---|---|
| `consumer.pre_filter.decided.v1` | callRef, decision, ruleSource | Telephony, Analytics |
| `consumer.caller_list.entry_added.v1` | subscriberId, entryId, listType, source | Telephony, Fraud, Analytics |
| `consumer.caller_list.entry_removed.v1` | subscriberId, entryId, listType, compensated | Telephony, Fraud |
| `consumer.assistant_profile.updated.v1` | lineId, changedFields[] | AI Orchestration, Voice |
| `consumer.disclosure_scope.widened.v1` | lineId, item | AI Orchestration, Administration |
| `consumer.screening_preferences.updated.v1` | subscriberId, availabilityMode | Telephony |
| `consumer.call_action.recorded.v1` | callSessionId, actionType, compensates? | Telephony, Fraud, Analytics |
| `consumer.verdict.disputed.v1` | assessmentId, assertedLevel | **Fraud Detection**, Analytics |
| `consumer.call.deleted.v1` | callSessionId | AI Orchestration, Voice |

**`consumer.verdict.disputed.v1` is the most valuable event the platform
emits.** It is real-world precision, measured by the only judge who knows.

---

## 11.9 Commands

| Command | Refused when |
|---|---|
| `BlockNumber(subscriberId, number, source, callRef?)` | Number is the Subscriber's own, or an emergency service number |
| `UnblockNumber(subscriberId, entryId)` | Entry not found |
| `AllowNumber(subscriberId, number)` | — (removes from blocklist atomically) |
| `UpdateAssistantProfile(lineId, changes)` | Validation fails on any changed field |
| `SetCustomInstruction(lineId, text)` | Over 500 chars, or `InstructionValidationService` rejects |
| `WidenDisclosureScope(lineId, item)` | Item unrecognised. **One item per command** |
| `NarrowDisclosureScope(lineId, item)` | — (always permitted) |
| `SetAvailability(subscriberId, mode, windows)` | Windows malformed |
| `RecordCallAction(callSessionId, actionType)` | Call not owned by this Subscriber |
| `UndoCallAction(actionId)` | Outside the undo window, or already compensated |
| `DisputeVerdict(assessmentId, assertedLevel, note?)` | Dispute already open for this assessment |
| `DeleteCall(callSessionId)` | Call not owned by this Subscriber |

**`WidenDisclosureScope` takes one item.** A batch form does not exist, for the
same reason `GrantConsent` takes one purpose — the model refuses to make
over-disclosure convenient (I4).

---

## 11.10 Queries

| Query | Scope |
|---|---|
| `GetCallHistory(subscriberId, filter, page)` | Own calls only |
| `SearchCalls(subscriberId, query, scope)` | Own calls; transcript full-text within own data only |
| `GetCallerList(subscriberId, listType)` | Own |
| `GetAssistantProfile(lineId)` | Own line, or a business line the Subscriber is assigned to |
| `GetProtectionSummary(subscriberId, period)` | Own; every count links to its list |
| `GetCallerProfile(subscriberId, number)` | Own calls from that number. **Never cross-subscriber** |
| `EvaluatePreFilter(number)` | **Local, on-device.** No network call |

---

## 11.11 Policies

| # | Policy |
|---|---|
| **P-CO-1** | When a number is added to one list, remove it from the other in the same transaction, and state it |
| **P-CO-2** | When a block is recorded, apply it to the on-device pre-filter **immediately**; sync to the server eventually. A block works offline and works when our backend is down |
| **P-CO-3** | When an allowlisted number is assessed as risky, the allowlist wins for routing and the verdict is **still recorded and shown**. The user's explicit instruction is obeyed; our opinion is still reported |
| **P-CO-4** | When a verdict is disputed, downgrade its presentation immediately in this Subscriber's interface and emit a quality signal. **Do not mutate the assessment** — it belongs to Fraud and it is immutable |
| **P-CO-5** | When a `CustomInstruction` fails validation, reject it with the specific reason. Never sanitise and save |
| **P-CO-6** | When a `Transcript` reaches retention, the `CallHistoryEntry` states the deletion as policy, not as an error |
| **P-CO-7** | When a call was not screened, render no summary, no verdict and no transcript. Absence is the honest state |
| **P-CO-8** | When the Subscriber is removed from an Organisation, their personal `CallHistoryEntry` set is untouched |

---

## 11.12 Invariants

| # | Invariant | Source |
|---|---|---|
| **INV-CO-1** | A `PhoneNumber` is in at most one `CallerList` at a time | |
| **INV-CO-2** | `DisclosureScope` defaults to empty; widening is explicit and per-item | **I4** |
| **INV-CO-3** | No `CustomInstruction` can alter, suppress or replace the Announcement | **I1** |
| **INV-CO-4** | `CallHistoryEntry` is a projection and is never read to make a decision | |
| **INV-CO-5** | `UserCallAction` is append-only; undo appends a compensating action | |
| **INV-CO-6** | A non-screened call carries no summary, verdict or transcript reference | |
| **INV-CO-7** | `CallerIdentity` is never derived from caller speech | **U10** |
| **INV-CO-8** | A Subscriber's queries are scoped to their own data at the query layer, not the render layer | |
| **INV-CO-9** | Emergency service numbers cannot be blocked | |

---

## 11.13 State Machines

### `CallerListEntry`

```
  (none) ──add──▶ ACTIVE ──remove──▶ PENDING_REMOVAL ──window elapses──▶ REMOVED
                    ▲                      │                            «terminal»
                    └────── undo ──────────┘
```

`PENDING_REMOVAL` exists because the undo window is a domain concept, not a UI
timer — the entry is still enforcing during it.

### `UserCallAction`

```
  RECORDED ──undo within window──▶ COMPENSATED «terminal»
      │
      └──window elapses──▶ FINAL «terminal»
```

### `VerdictDispute`

```
  RAISED ──▶ ACKNOWLEDGED ──▶ UPHELD | REJECTED  «terminal»
     │                            (by Fraud review)
     └── presentation downgrades IMMEDIATELY at RAISED,
         regardless of the eventual outcome
```

The immediate downgrade at `RAISED` is deliberate: in their own interface, the
Subscriber's correction takes effect at once. Review determines whether the
*assessment* is superseded, not what they see.

---

## 11.14 Ownership

| Aspect | Value |
|---|---|
| Team | `callscreen/consumer` |
| Services | `edge-api`, `contacts-sync`, and the Android client for the pre-filter |
| Durable store | `identity` Aurora, schema `consumer` |
| Ephemeral | Redis — reputation cache; on-device storage for the pre-filter |
| CODEOWNERS | `docs/domain/11-consumer.md`, `android/feature/**` |

---

## 11.15 External Dependencies

| Dependency | Purpose | Guard |
|---|---|---|
| Android Contacts provider | Pre-filter known-contact matching | **Read-only, on-device, never uploaded.** The consent's own consequence line states this |
| `CallScreeningService` role | Executing the pre-filter decision | Loss of the role degrades the pre-filter only; forwarding-based screening is unaffected |
| Fraud Detection reputation cache | Pre-filter input | Cached, TTL-bounded, k-anonymised. Stale reputation degrades to `SCREEN`, never to `REJECT` |

**The degradation direction matters.** A stale or missing reputation causes a
call to be screened, which costs money. It never causes a call to be rejected,
which would cost the Subscriber a call they wanted.

---

## 11.16 Security Constraints

| # | Constraint |
|---|---|
| 1 | **Contacts are read on-device and never uploaded.** The pre-filter matches locally; the server never receives a contact book |
| 2 | **`DisclosureScope` defaults to empty and widens one item at a time** (I4) |
| 3 | **`CustomInstruction` is untrusted input to our own prompt pipeline** and is validated server-side, not only on the client |
| 4 | **No caller-supplied string becomes a `CallerIdentity`** (U10) |
| 5 | **Blocks are enforced on-device**, so a compromised or unavailable backend cannot un-block a number |
| 6 | **Every query is scoped to the authenticated Subscriber at the query layer** |
| 7 | **`customInstruction` and dispute `note` are `SENSITIVE`** and excluded from analytics entirely |
| 8 | **Search history is local, never synced**, and clearable in one action |
| 9 | **Sign-out clears the cached history projection from the device** |
| 10 | **No cross-subscriber query exists in this context**, including reputation — the pre-filter receives a verdict, never another subscriber's activity |
