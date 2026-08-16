# 12 · Telephony

**Subdomain:** **CORE** · **Prefix:** `TE` · **Topic domain:** `telephony`

> The load-bearing context. ADR-0002 is the decision every other ADR is a
> consequence of, and this is the domain model of that decision.

---

## 12.1 Purpose

Own the call: get it diverted to us, prove it was diverted legitimately, answer
it, keep the media path alive while the Assistant converses, let the Subscriber
take it, and record what happened.

## 12.2 Responsibilities

**Owns**

- `Line` — a phone number under screening
- `ForwardingConfiguration` and its continuous verification (ADR-0002 §9)
- `DirectInwardDialNumber` allocation and reclamation
- `CallSession` — the authoritative record of one call
- Call admission, diversion validation, and load shedding
- Takeover and transfer execution
- The MMI command, constructed client-side from a validated DID

**Does not own**

| Not owned | Owned by |
|---|---|
| What the Assistant says | AI Orchestration |
| The audio itself | Voice |
| The verdict | Fraud Detection |
| Where a business call routes to | Business decides; Telephony executes |
| The pre-filter decision | Consumer (executed on-device) |
| The subscriber's history view | Consumer |

---

## 12.3 Domain Entities

### `Line` — aggregate root

**Attributes**

```
id                : LineId                         INTERNAL · STANDARD
ownerRef          : SubscriberId | OrganisationId  INTERNAL · STANDARD
msisdn            : MSISDN                         PERSONAL · STANDARD · residency-bound
simSubscriptionRef: SimSubscriptionRef?            PERSONAL · STANDARD
carrier           : CarrierIdentity                INTERNAL · STANDARD
lineType          : LineType                       PUBLIC   · STANDARD
forwarding        : ForwardingConfiguration <owned>
status            : LineStatus                     INTERNAL · STANDARD
createdAt         : Instant                        INTERNAL · STANDARD
```

**Relationships** — Owns exactly one `ForwardingConfiguration` by containment.
References a `Subscriber` or an `Organisation` by identity. Referenced by
`CallSession`, by Business's `BusinessLine`, and by Consumer's
`AssistantProfile`.

**Lifecycle** — Created when a Subscriber or Organisation brings a number under
screening. Suspended when the owner is suspended or a plan lapses beyond grace.
Ended by erasure or by explicit removal — **and removal always clears carrier
forwarding first**.

**Validation Rules** — `msisdn` must be verified as belonging to the owner
before screening begins; screening a number you do not own is an interception.
On dual-SIM devices, `simSubscriptionRef` must resolve to a real subscription —
and because `SubscriptionManager` labels are unreliable across OEMs (ADR-0002
§7), the model never treats the label as the sole identifier.

**Privacy Classification** — `msisdn` is `PERSONAL` and residency-bound.
`carrier` is `INTERNAL` — it is operationally essential and not identifying on
its own.

**Audit Requirements** — **Change** level, especially every forwarding
provisioning and every SIM change.

---

### `ForwardingConfiguration`

The entity that exists because a lapsed forwarding is the platform's
highest-severity silent failure.

**Attributes**

```
lineId          : LineId <ref>                INTERNAL · STANDARD
didId           : DidId? <ref>                INTERNAL · STANDARD
expectedDid     : PhoneNumber?                INTERNAL · STANDARD
observedDid     : PhoneNumber?                INTERNAL · STANDARD
ringDelay       : RingDelay                   PUBLIC   · STANDARD
state           : ForwardingState             PUBLIC   · STANDARD
provisionedAt   : Instant?                    INTERNAL · STANDARD
lastVerifiedAt  : Instant?                    INTERNAL · STANDARD
lastVerdict     : ForwardingVerdict?          PUBLIC   · STANDARD
verifications   : ForwardingVerification[] <owned>
consecutiveFails: Int                         INTERNAL · SHORT
```

**Relationships** — Contained in `Line`. References an allocated
`DirectInwardDialNumber`.

**Lifecycle** — Provisioned by an MMI dialled on the handset, verified by
interrogation, then **verified continuously** (P-TE-3). Transitions to `LAPSED`,
`WRONG_TARGET`, `WRONG_SIM` or `UNVERIFIABLE` on a failing verification.
Disabled explicitly, or cleared as part of erasure or line removal.

**Validation Rules** — `ringDelay` ≥ 5 seconds (ADR-0002 §7; below that,
legitimate calls the Subscriber wanted get forwarded). `observedDid` differing
from `expectedDid` produces `WRONG_TARGET` and is **never silently corrected** —
re-dialling an MMI because we disliked what we found is exactly the behaviour
ADR-0002 §10 warns about. A carrier that does not support interrogation yields
`UNVERIFIABLE`, which is **not** a failure state.

**Privacy Classification** — `INTERNAL`/`PUBLIC`. The DID is ours, not the
subscriber's, so it is not personal data — and showing it to the Subscriber is
itself a control, letting them verify we are not diverting elsewhere.

**Audit Requirements** — **Change** on every state transition. Verification
history is retained because "when did this stop working, and what did we do" is
the first support question.

---

### `DirectInwardDialNumber` — aggregate root

**Attributes**

```
id            : DidId                    INTERNAL · STANDARD
number        : PhoneNumber              INTERNAL · STANDARD
provider      : CarrierProvider          INTERNAL · STANDARD
region        : Region                   PUBLIC   · STANDARD
circle        : Circle                   PUBLIC   · STANDARD
poolState     : DidPoolState             INTERNAL · STANDARD
allocatedTo   : LineId? <ref>            INTERNAL · STANDARD
allocatedAt   : Instant?                 INTERNAL · STANDARD
quarantineUntil: Instant?                INTERNAL · STANDARD
```

**Relationships** — Allocated to at most one `Line`. Referenced by
`CallSession` as the leg's destination.

**Lifecycle** — Procured from Exotel or Plivo (ADR-0003), held in a pool,
allocated to a Line, reclaimed on line removal, then **quarantined** before
reallocation. Provisioning has lead time and is not elastic (ADR-0002 §13), so
pool depth is a monitored resource.

**Validation Rules** — Allocated to at most one Line at a time (INV-TE-5). Never
reallocated inside the quarantine window (P-TE-5) — a reallocated number
inherits the previous subscriber's stray callers, which is both a privacy
problem and a confusing one.

**Privacy Classification** — `INTERNAL`. Our numbers, not a person's.

**Audit Requirements** — **Change** on allocation and reclamation.

---

### `CallSession` — aggregate root

The authoritative record of one call.

**Attributes**

```
id                : CallSessionId             INTERNAL  · STANDARD
lineId            : LineId <ref>              INTERNAL  · STANDARD
didId             : DidId? <ref>              INTERNAL  · STANDARD
callerNumber      : PhoneNumber               PERSONAL  · STANDARD · residency-bound
diversion         : DiversionHeader?          INTERNAL  · STANDARD
preFilterDecision : PreFilterDecision         PUBLIC    · STANDARD
state             : CallState                 PUBLIC    · STANDARD
outcome           : Outcome?                  PUBLIC    · STANDARD
legs              : CallLeg[] <owned>
timeline          : CallTimelineEvent[] <owned>
receivedAt        : Instant                   PERSONAL  · STANDARD
answeredAt        : Instant?                  INTERNAL  · STANDARD
announcedAt       : Instant?                  INTERNAL  · STANDARD
endedAt           : Instant?                  INTERNAL  · STANDARD
mediaPath         : MediaPath                 INTERNAL  · SHORT
shedReason        : ShedReason?               PUBLIC    · SHORT
```

**Relationships** — References `Line`, `DID`. Referenced by AI Orchestration's
`ScreeningConversation` and `Transcript`, by Voice's `VoiceSession` and
`RecordingArtefact`, by Fraud's `FraudAssessment`, and by Consumer's
`CallHistoryEntry` and `UserCallAction`. **It references none of them** — the
call does not depend on what was said about it.

**Lifecycle** — Created on receipt of a forwarded call, or on an on-device
pre-filter decision for calls that never leave the handset. Live state lives in
Redis (ADR-0009 §5, C3); the durable record is written after the turn, never
synchronously inside the latency budget. Ended by any of eight outcomes.
Retained per the Subscriber's preference; deleted on erasure.

**Validation Rules** — Cannot exist without a valid `DiversionHeader` **if it
arrived at a DID** (INV-TE-2). Every session that reached `ANSWERED` has exactly
one `announcedAt` (INV-TE-1) — the announcement is the caller's lawful basis and
its absence invalidates the session. A session cannot transition to
`TAKEN_OVER` without an established takeover leg.

**Privacy Classification** — `callerNumber` and `receivedAt` are `PERSONAL`;
the rest is `INTERNAL` or `PUBLIC`. Transcript and audio live elsewhere and
retain their own stricter classification.

**Audit Requirements** — **Change** on state transitions. **Access** level when
read through Administration; `callerNumber` reveal is individually audited.

---

### `CallLeg`

```
id           : LegId                INTERNAL · SHORT
sessionId    : CallSessionId <ref>  INTERNAL · SHORT
legType      : LegType              PUBLIC   · SHORT
remoteNumber : PhoneNumber?         PERSONAL · STANDARD
transport    : MediaTransport       INTERNAL · SHORT
establishedAt: Instant?             INTERNAL · SHORT
endedAt      : Instant?             INTERNAL · SHORT
endReason    : LegEndReason?        PUBLIC   · SHORT
```

**Lifecycle** — Inbound leg is created at answer. A takeover leg is created when
the Subscriber engages and dials **from server-held state, never from a
client-supplied number**. A transfer leg is created by a `RoutingPolicy`
decision. Legs end independently; **the session survives a failed takeover leg**
(P-TE-4).

**Validation Rules** — At most one active leg per `LegType` per session. A
takeover leg's `remoteNumber` must equal the Line's verified MSISDN.

**Audit Requirements** — **Change** level.

---

### `CallTimelineEvent`

```
id        : EventId          INTERNAL · STANDARD
sessionId : CallSessionId    INTERNAL · STANDARD
eventType : TimelineEventType PUBLIC  · STANDARD
occurredAt: Instant          INTERNAL · STANDARD
detail    : TimelineDetail?  INTERNAL · STANDARD
```

**Lifecycle** — Append-only. Contains the structural record of the call —
including the announcement, recording boundaries, verdict moment, takeover and
the Subscriber's own actions. **Never contains speech**; the Transcript carries
that.

**Validation Rules** — `ANNOUNCEMENT_PLAYED` is present on every answered
session and can never be removed (INV-TE-1).

**Privacy Classification** — `INTERNAL`. It carries structure, not content —
which is what makes it usable in a support call or a fraud review without
break-glass.

---

### `CarrierProfile` — reference entity

```
carrier             : CarrierIdentity     PUBLIC · STANDARD
circle              : Circle              PUBLIC · STANDARD
mmiFormat           : MmiFormat           PUBLIC · STANDARD
ringDelayGranularity: Int                 PUBLIC · STANDARD
interrogationSupport: Boolean             PUBLIC · STANDARD
knownQuirks         : CarrierQuirk[]      PUBLIC · STANDARD
```

The domain form of the carrier matrix — a **launch blocker** per ADR-0002 §9.
Reference data, versioned, no personal content, and the source of the
carrier-specific instructions shown when provisioning fails.

---

## 12.4 Value Objects

| Value object | Definition | Notes |
|---|---|---|
| `MmiCommand` | The GSM supplementary-service string | **Constructed only by `MmiCommandFactory` from a signature-verified DID** (INV-TE-3). Never parsed from a server response |
| `DiversionHeader` | SIP header proving forwarding, naming the diverting subscriber | Absence means hostile (I10) |
| `RingDelay` | Seconds, ≥ 5, snapped to carrier granularity | |
| `CarrierIdentity` | `JIO` · `AIRTEL` · `VI` · `BSNL` · `OTHER` + `Circle` | Behaviour varies by circle, not only by carrier |
| `ForwardingState` | `UNCONFIGURED` · `PROVISIONING` · `PROVISIONED` · `VERIFIED` · `LAPSED` · `WRONG_TARGET` · `WRONG_SIM` · `UNVERIFIABLE` · `DISABLED` | |
| `ForwardingVerdict` | The outcome of one interrogation, with its evidence | |
| `CallState` | See [§12.13](#1213-state-machines) | |
| `Outcome` | `RANG_THROUGH` · `REJECTED` · `SILENCED` · `TAKEN_OVER` · `TRANSFERRED` · `DECLINED` · `CALLER_ENDED` · `VOICEMAIL` · `REFUSED` · `SHED` | |
| `MediaPath` | Provider WebSocket at the carrier leg, WebRTC at the app leg | Fixed by ADR-0004 |
| `LegType` | `INBOUND` · `TAKEOVER` · `TRANSFER` | |
| `ShedReason` | `CAPACITY` · `RATE_LIMIT` · `QUOTA_EXCEEDED` · `DEPENDENCY_DOWN` | Shown honestly to the Subscriber |
| `DidPoolState` | `AVAILABLE` · `ALLOCATED` · `QUARANTINED` · `RETIRED` | |
| `AdmissionDecision` | `ADMIT` · `REFUSE_UNDIVERTED` · `SHED` · `RATE_LIMITED` | |

---

## 12.5 Aggregates

| Aggregate | Root | Contains | Consistency boundary |
|---|---|---|---|
| **Line** | `Line` | `ForwardingConfiguration`, `ForwardingVerification[]` | A line and its forwarding must be consistent — a line claiming to be screened while its forwarding lapsed is the failure this context exists to prevent |
| **DirectInwardDialNumber** | `DID` | — | Allocation must be atomic; two lines sharing a DID would cross-route calls |
| **CallSession** | `CallSession` | `CallLeg[]`, `CallTimelineEvent[]` | Legs and timeline must be consistent with the session's state |
| **CarrierProfile** | `CarrierProfile` | `CarrierQuirk[]` | Reference data |

```
┌────────────────────────────────────────────┐
│  Line  «aggregate root»                    │
│  id · msisdn · carrier · simSubscriptionRef│
│  ┌──────────────────────────────────────┐  │
│  │ ForwardingConfiguration              │  │
│  │  expectedDid vs observedDid          │  │
│  │  state · lastVerdict                 │  │
│  │  ┌──────────────────────────────┐    │  │
│  │  │ ForwardingVerification[]     │    │  │
│  │  └──────────────────────────────┘    │  │
│  └──────────────────────────────────────┘  │
└────────────────┬───────────────────────────┘
                 │ <ref>                     ┌────────────────────────┐
                 │                           │ DirectInwardDialNumber │
                 │        ┌──────────────────┤ «root»                 │
                 │        │  <ref>           │ poolState · quarantine │
                 ▼        ▼                  └────────────────────────┘
┌───────────────────────────────────────────┐
│  CallSession  «aggregate root»            │
│  callerNumber · diversion · state         │
│  ┌────────────────┐  ┌──────────────────┐ │
│  │ CallLeg[]      │  │ CallTimelineEvent│ │
│  │ INBOUND        │  │ [] append-only   │ │
│  │ TAKEOVER       │  │ ANNOUNCEMENT_    │ │
│  │ TRANSFER       │  │ PLAYED always    │ │
│  └────────────────┘  └──────────────────┘ │
└───────────────────────────────────────────┘
        │ referenced BY (never references)
        ▼
  Transcript · FraudAssessment · RecordingArtefact · CallHistoryEntry
```

---

## 12.6 Domain Services

| Service | Responsibility | Notes |
|---|---|---|
| `CallAdmissionService` | Decide `ADMIT` / `REFUSE_UNDIVERTED` / `SHED` / `RATE_LIMITED` | Enforces per-DID and per-source-number rate limits. A publicly-dialable DID attached to an expensive AI pipeline is a resource-exhaustion target (ADR-0002 §10) |
| `DiversionValidationService` | Validate the SIP diversion header against the forwarding subscriber | The single control that makes public DIDs safe (I10) |
| `ForwardingHealthService` | Interrogate, classify into a `ForwardingVerdict`, produce a **diagnosis a support agent can read aloud** | Its output is a sentence, not a status code |
| `DidAllocationService` | Allocate, reclaim, quarantine, and report pool depth | Pool exhaustion is an alertable condition, not a runtime surprise |
| **`MmiCommandFactory`** | The **only** way to construct an `MmiCommand` | Takes a signature-verified DID and a validated ring delay. Cannot be constructed any other way (INV-TE-3) |
| `TakeoverService` | Establish a takeover leg to the Line's verified MSISDN | Dials from server state; an 8-second timeout; failure never ends the session |
| `DrainService` | Readiness false, then close, on shutdown | Invariant I6 — a pod restart otherwise drops live calls |

---

## 12.7 Repositories

`LineRepository` · `DidRepository` · `CallSessionRepository` ·
`CarrierProfileRepository`

Live session state is held in Redis and is **not** a repository concern — it is
not a system of record (ADR-0009 §5). Anything that must survive is written to
Postgres via an event.

---

## 12.8 Domain Events

| Event | Payload | Consumers |
|---|---|---|
| `telephony.call.received.v1` | callSessionId, lineId, didId, preFilterDecision | Consumer, AI, Analytics |
| `telephony.call.refused_undiverted.v1` | didId, sourceHash, attemptCount | **Administration**, Analytics |
| `telephony.call.shed.v1` | lineId, shedReason | Consumer, Notifications, Analytics |
| `telephony.call.answered.v1` | callSessionId, answeredAt | AI, Voice |
| **`telephony.call.announcement_played.v1`** | callSessionId, announcementVersion, locale | **AI, Administration, Analytics** — the I1 evidence trail |
| `telephony.call.taken_over.v1` | callSessionId, connectMs | Consumer, Notifications, Analytics |
| `telephony.call.transferred.v1` | callSessionId, targetType, answered | Business, Analytics |
| `telephony.call.ended.v1` | callSessionId, outcome, durationMs | Consumer, AI, Voice, Fraud, Billing |
| `telephony.forwarding.provisioned.v1` | lineId, didId, ringDelay, carrier | Consumer, Analytics |
| `telephony.forwarding.verified.v1` | lineId, verifiedAt, attempts | Consumer, Notifications |
| `telephony.forwarding.lapsed.v1` | lineId, verdict, carrier | **Notifications, Consumer, Administration** |
| `telephony.forwarding.unverifiable.v1` | lineId, carrier | Consumer, Administration |
| `telephony.did.allocated.v1` | didId, lineId | Analytics |
| `telephony.did.pool_low.v1` | region, available, threshold | **Administration** |
| `telephony.line.suspended.v1` | lineId, reason | Consumer, Business |

**No event carries a caller's number.** `telephony.call.refused_undiverted.v1`
carries a keyed hash for correlation, which is the redaction strategy the frozen
`annotations.proto` names for identifiers.

---

## 12.9 Commands

| Command | Refused when |
|---|---|
| `ProvisionForwarding(lineId, didId, ringDelay)` | DID not allocated to this line; ring delay below 5 s |
| `VerifyForwarding(lineId)` | Verification already in flight |
| `DisableForwarding(lineId)` | Requires explicit confirmation naming the consequence |
| `AllocateDid(lineId, region)` | Pool exhausted for the region |
| `ReclaimDid(didId)` | DID has an active session |
| `AdmitCall(didId, callerNumber, diversion)` | **No valid diversion — refused as hostile.** Rate limit exceeded. Capacity exhausted → shed |
| `AnswerCall(callSessionId)` | Session not admitted |
| `PlayAnnouncement(callSessionId)` | Session not answered. **Cannot be skipped** |
| `EngageTakeover(callSessionId)` | Session not live. **Never confirmed — time-critical** |
| `TransferCall(callSessionId, target)` | Target unresolvable; no fallback configured |
| `EndCall(callSessionId, outcome)` | — |
| `ChangeSim(lineId, newSubscriptionRef)` | Old forwarding not yet cleared |

**`PlayAnnouncement` has no "skip" parameter and no conditional form.** The
absence is the enforcement of I1.

---

## 12.10 Queries

| Query | Scope |
|---|---|
| `GetLine(lineId)` | Owner or assigned member |
| `GetForwardingHealth(lineId)` | Owner. Returns state, last verification, and a **readable diagnosis** |
| `DiagnoseForwarding(lineId)` | Administration. Returns the agent-readable sentence plus history |
| `GetCallSession(callSessionId)` | Owner, or Administration under redaction |
| `ListLiveSessions(filter)` | Administration only. **Metadata; content requires an `AccessGrant`** |
| `GetCarrierProfile(carrier, circle)` | Internal |
| `GetDidPoolDepth(region)` | Administration |

---

## 12.11 Policies

| # | Policy |
|---|---|
| **P-TE-1** | When an inbound call arrives at a DID with no valid diversion, refuse it as hostile and record the attempt for toll-fraud analysis (I10) |
| **P-TE-2** | When under load, shed at admission or downgrade a tier — **never** skip fraud scoring or the safety layer (I11). A shed call rings through; it is never dropped |
| **P-TE-3** | Verify forwarding continuously, not once. Re-assert on SIM change, network change, and on a schedule |
| **P-TE-4** | When a takeover fails for any reason, the session continues screening and the control re-arms. **A failed takeover never ends the call** |
| **P-TE-5** | When a DID is reclaimed, quarantine it before reallocation |
| **P-TE-6** | On shutdown, set readiness false and drain before closing (I6) |
| **P-TE-7** | When a carrier does not support interrogation, record `UNVERIFIABLE` and raise **no alarm**. We do not claim a fault we did not observe |
| **P-TE-8** | When `observedDid` differs from `expectedDid`, report it and ask. **Never silently re-provision** |
| **P-TE-9** | When a Line is removed or erased, clear carrier forwarding **before** deleting the Line |
| **P-TE-10** | When the DID pool falls below threshold, alert. Number provisioning has lead time and is not elastic |

---

## 12.12 Invariants

| # | Invariant | Source |
|---|---|---|
| **INV-TE-1** | Every `CallSession` that reached `ANSWERED` has exactly one `ANNOUNCEMENT_PLAYED` timeline event | **I1** |
| **INV-TE-2** | A `CallSession` originating at a DID cannot exist without a valid `DiversionHeader` | **I10** |
| **INV-TE-3** | An `MmiCommand` is constructible only from a signature-verified DID, client-side. There is no constructor that accepts a server-supplied string | **I10**, ADR-0002 §10 |
| **INV-TE-4** | A `Line` has at most one active `ForwardingConfiguration` | |
| **INV-TE-5** | A `DID` is allocated to at most one `Line` at a time | |
| **INV-TE-6** | Live call audio is never written to disk by the media path | **I9** |
| **INV-TE-7** | `RingDelay` ≥ 5 seconds | ADR-0002 §7 |
| **INV-TE-8** | A takeover leg dials the Line's server-held verified MSISDN, never a client-supplied number | |
| **INV-TE-9** | `UNVERIFIABLE` and `LAPSED` are distinct states and are never collapsed | |
| **INV-TE-10** | A shed call rings through to the handset. There is no outcome in which we cause a call to fail | ADR-0002 §6 |

---

## 12.13 State Machines

### `CallSession`

```
                        RECEIVED
                           │
              ┌────────────┴─────────────┐
              │                          │
      on-device pre-filter          forwarded to DID
              │                          │
   ┌──────────┼──────────┐               ▼
   ▼          ▼          ▼         ┌──ADMISSION──┐
RANG_THROUGH REJECTED SILENCED     │             │
«terminal»  «terminal» «terminal»  ▼             ▼
                              REFUSED         ADMITTED
                             «terminal»          │
                             (undiverted)        ▼
                                              ANSWERED
                                                 │
                                                 ▼
                                            ANNOUNCED  ◀── I1, mandatory
                                                 │
                                                 ▼
                                             SCREENING
                                                 │
        ┌──────────┬──────────┬─────────────┬────┴──────┬────────────┐
        ▼          ▼          ▼             ▼           ▼            ▼
   TAKEN_OVER  TRANSFERRED  DECLINED   CALLER_ENDED  VOICEMAIL   FAILED
        │          │          │             │           │            │
        └──────────┴──────────┴─────────────┴───────────┴────────────┘
                                    │
                                    ▼
                                  ENDED  «terminal»

   SHED «terminal» ── reachable from ADMISSION under load (I11).
                      The call rings through; it is never dropped.
```

**`ANNOUNCED` is not skippable.** There is no transition from `ANSWERED` to
`SCREENING`. That absence is Invariant I1 expressed as a state machine.

### `ForwardingConfiguration`

```
  UNCONFIGURED ──provision──▶ PROVISIONING ──dial returns──▶ PROVISIONED
                                                                  │
                                                            interrogate
                                                                  │
              ┌───────────┬──────────────┬────────────┬───────────┴───┐
              ▼           ▼              ▼            ▼               ▼
          VERIFIED     LAPSED      WRONG_TARGET   WRONG_SIM    UNVERIFIABLE
              │           │              │            │               │
              │           └──────────────┴────────────┘         no alarm.
              │                     re-provision              A distinct
              │                          │                    state, not
              └──────────────────────────┘                    a failure.
                          │
                    disable (explicit, confirmed)
                          ▼
                      DISABLED  «terminal until re-provisioned»
```

`VERIFIED` re-enters the verification cycle continuously (P-TE-3). It is a
**steady state under active maintenance**, not a completed step.

### `DirectInwardDialNumber`

```
  AVAILABLE ──allocate──▶ ALLOCATED ──reclaim──▶ QUARANTINED
      ▲                                              │
      └────────── quarantine window elapses ─────────┘
                                                     │
                                                 retire
                                                     ▼
                                                 RETIRED «terminal»
```

---

## 12.14 Ownership

| Aspect | Value |
|---|---|
| Team | `callscreen/telephony` |
| Services | `telephony-gateway`, `media-relay`, `session-orchestrator` (Go — sized on concurrent sessions, not RPS) |
| Durable store | `telephony` Aurora, schema `telephony` |
| Ephemeral | Redis — live session state, rate-limit counters, admission tokens |
| Objects | None. **`media-relay` never writes audio to disk** (I9) |
| CODEOWNERS | `docs/domain/12-telephony.md`, `services/go/telephony-gateway/**`, `services/go/media-relay/**`, `services/go/session-orchestrator/**` |

---

## 12.15 External Dependencies

| Dependency | Purpose | Guard |
|---|---|---|
| **Exotel** (primary), **Plivo** (secondary) | DID termination, media, call control | **ACL — the provider port** (ADR-0003). Both live. Each provider's model of a call differs; neither shape reaches the domain |
| Indian carriers — Jio, Airtel, Vi, BSNL | Conditional forwarding, MMI, interrogation | Behaviour differs by carrier **and circle**. `CarrierProfile` is the domain form of the carrier matrix, and it is a launch blocker |
| Android `CallScreeningService` | Executes the on-device pre-filter decision | Role loss degrades the pre-filter only |
| Android `ACTION_CALL` | Dials the MMI | The string is constructed client-side; the intent is the delivery mechanism, not the source |
| Redis | Live session state | Unavailability is `DEGRADED`, not `UNHEALTHY`; session-state loss ends the call gracefully rather than hanging (ADR-0009 §7) |

---

## 12.16 Security Constraints

| # | Constraint |
|---|---|
| 1 | **Our DIDs are publicly dialable.** Every inbound call is authenticated by its diversion header, and an undiverted call is treated as hostile (I10) |
| 2 | **Toll fraud is an assumed, continuous threat.** Per-DID and per-source rate limits plus admission control from day one (ADR-0002 §10) |
| 3 | **MMI is a dangerous primitive.** `MmiCommandFactory` is the only constructor, it takes a signature-verified DID, and no code path accepts a dial string from a server response |
| 4 | **A takeover dials server-held state**, never a client-supplied number (INV-TE-8) |
| 5 | **`media-relay` never writes audio to disk** (I9). Persistence is a policy-gated act in Voice, never a side effect here |
| 6 | **Caller audio is `SENSITIVE`** and subject to ADR-0012 in full while it transits |
| 7 | **Forwarding is never modified by an operator.** Administration diagnoses; only the Subscriber changes their own carrier configuration |
| 8 | **The DID is shown to the Subscriber deliberately** — it is how they verify we are not diverting their calls somewhere unexpected |
| 9 | **Undiverted-attempt records carry a keyed hash of the source**, never the number itself |
| 10 | **Services drain before terminating** (I6). Terminating a pod with live sessions drops real phone calls |
