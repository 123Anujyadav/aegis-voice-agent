# 10 · Identity

**Subdomain:** Generic, with a statutory core · **Prefix:** `ID` ·
**Topic domain:** `identity`

---

## 10.1 Purpose

Establish who a Subscriber is, which Devices act on their behalf, and what they
have consented to — such that every other context can ask those three questions
and get one authoritative answer.

## 10.2 Responsibilities

**Owns**

- Subscriber identity, anchored on MSISDN (ADR-0010)
- Device enrolment, attestation and revocation
- Authentication sessions and their revocation
- **Consent** — per-purpose, append-only, withdrawable (ADR-0012)
- Retention preferences within statutory bounds
- Erasure orchestration across every store

**Does not own**

| Not owned | Owned by |
|---|---|
| What a Subscriber may *do* (entitlements) | Billing |
| A Subscriber's screening preferences | Consumer |
| Operator identity and roles | Administration |
| Organisation membership | Business |
| Notification preferences | Notifications |
| The Announcement — it is a lawful basis, not a consent | AI Orchestration |

> **The most important boundary in this context:** Identity answers *who* and
> *what did they agree to*. It never answers *what are they allowed to do*.
> Merging authentication with authorisation is how a consumer account acquires
> administrative capability.

---

## 10.3 Domain Entities

### `Subscriber` — aggregate root

**Attributes**

```
id              : SubscriberId                    INTERNAL  · STANDARD
msisdn          : MSISDN                          PERSONAL  · STANDARD · residency-bound
status          : SubscriberStatus                INTERNAL  · STANDARD
region          : Region                          PUBLIC    · STANDARD
devices         : Device[] <owned>
consents        : ConsentRecord[] <owned>
retention       : RetentionPreference <owned>
registeredAt    : Instant                         INTERNAL  · STANDARD
activatedAt     : Instant?                        INTERNAL  · STANDARD
suspendedAt     : Instant?                        INTERNAL  · STANDARD
erasedAt        : Instant?                        INTERNAL  · LEGAL_HOLD
```

**Relationships** — Owns `Device`, `ConsentRecord`, `RetentionPreference` by
containment. Referenced by identity from Consumer, Telephony, Billing, Business,
Notifications. References nothing outside itself.

**Lifecycle** — Created by `RegisterSubscriber` after a successful
`VerificationChallenge`. Activated when the first Device is enrolled. Suspended
by Administration or by billing lapse beyond grace. Ended by `ErasureRequest`
completion, which tombstones the identifier permanently.

**Validation Rules** — Exactly one active MSISDN. MSISDN must be a valid mobile
E.164 number for a supported region; landlines and virtual numbers are refused
at construction. A Subscriber cannot reach `ACTIVE` with zero enrolled Devices.
An erased Subscriber cannot be reactivated.

**Privacy Classification** — Entity default `PERSONAL`, `STANDARD`,
residency-bound. `erasedAt` is `LEGAL_HOLD` because proving *when* we erased is
itself a compliance obligation.

**Audit Requirements** — **Change** level on every attribute. **Access** level
when read through Administration. MSISDN reveal is individually audited.

---

### `Device`

**Attributes**

```
id                  : DeviceId                    INTERNAL  · STANDARD
subscriberId        : SubscriberId <ref>          INTERNAL  · STANDARD
credentialRef       : DeviceCredentialReference   SECRET    · STANDARD
model               : String                      PERSONAL  · STANDARD
manufacturer        : String                      PERSONAL  · STANDARD
osVersion           : String                      INTERNAL  · SHORT
lastIntegrity       : IntegrityVerdict            INTERNAL  · SHORT
status              : DeviceStatus                INTERNAL  · STANDARD
enrolledAt          : Instant                     INTERNAL  · STANDARD
lastSeenAt          : Instant                     PERSONAL  · SHORT
revokedAt           : Instant?                    INTERNAL  · STANDARD
revocationReason    : RevocationReason?           INTERNAL  · STANDARD
```

**Relationships** — Contained in `Subscriber`. Referenced by `AuthSession` and
by Notifications' `DeviceToken`.

**Lifecycle** — Created by `EnrolDevice` after attestation. Enrolment when the
device limit is reached revokes the least-recently-active Device (P-ID-1).
Revoked explicitly, by enrolment displacement, by erasure, or by a failed
re-attestation. Revocation is terminal — a revoked Device is re-enrolled as a
new Device, never reactivated.

**Validation Rules** — `credentialRef` must reference a key generated on-device
and marked non-exportable; a credential presented as a transmitted secret is
refused at construction (I5). Attestation must be current within a bounded
window for a Device to be `TRUSTED`. `model` and `manufacturer` are recorded
because OEM-specific telephony defects are a recurring incident class
(`types.proto`), not for profiling.

**Privacy Classification** — `credentialRef` is `SECRET` and is a *reference*;
the private key never exists in our domain at all. `lastSeenAt` is `PERSONAL`
because a location-free activity timeline still singles out a person.

**Audit Requirements** — **Change** on enrolment and revocation, always,
including the notification sent to the displaced Device.

---

### `ConsentRecord`

The statutory heart of the context.

**Attributes**

```
id              : ConsentRecordId                 INTERNAL  · LEGAL_HOLD
subscriberId    : SubscriberId <ref>              INTERNAL  · LEGAL_HOLD
purpose         : ConsentPurpose                  PUBLIC    · LEGAL_HOLD
granted         : Boolean                         PUBLIC    · LEGAL_HOLD
policyVersion   : PolicyVersion                   PUBLIC    · LEGAL_HOLD
source          : ConsentSource                   INTERNAL  · LEGAL_HOLD
recordedAt      : Instant                         INTERNAL  · LEGAL_HOLD
supersedes      : ConsentRecordId?                INTERNAL  · LEGAL_HOLD
```

**Relationships** — Contained in `Subscriber`. Forms an append-only chain per
purpose via `supersedes`. Consulted by Consumer, Voice, Analytics and Business.

**Lifecycle** — **Append-only.** `GrantConsent` and `WithdrawConsent` both
create a new record superseding the previous one for that purpose. **No record
is ever mutated or deleted**, including on erasure — the consent history is the
evidence that processing was lawful, and destroying it would destroy the defence.

**Validation Rules** — Exactly one purpose per record (INV-ID-4). `policyVersion`
must reference the policy text actually shown to the subscriber. A record whose
purpose is unrecognised is treated as **not granted** (fail closed, I8). The
Announcement has no `ConsentPurpose` and cannot be recorded here (INV-ID-5).

**Privacy Classification** — `LEGAL_HOLD` throughout, and it **survives
erasure** of every other personal attribute. The record references the subject
by a retained pseudonymous key after erasure, so lawfulness remains provable
without the person remaining identifiable.

**Audit Requirements** — **Access** level. Every read and every write produces an
`AuditEntry`, and the subscriber may obtain the full record (DPDP).

---

### `AuthSession`

**Attributes**

```
id              : AuthSessionId                   INTERNAL  · SHORT
subscriberId    : SubscriberId <ref>              INTERNAL  · SHORT
deviceId        : DeviceId <ref>                  INTERNAL  · SHORT
tokenFamily     : TokenFamily                     SECRET    · SHORT
issuedAt        : Instant                         INTERNAL  · SHORT
accessExpiresAt : Instant                         INTERNAL  · SHORT
refreshExpiresAt: Instant                         INTERNAL  · SHORT
revokedAt       : Instant?                        INTERNAL  · SHORT
sourceRegion    : Region                          PUBLIC    · SHORT
```

**Relationships** — Own aggregate. References `Subscriber` and `Device` by
identity. Deliberately **not** contained in `Subscriber`: sessions churn far
faster than the subscriber, and containing them would make every token refresh a
write to the subscriber aggregate.

**Lifecycle** — Created on successful verification or refresh. Access token 15
minutes, refresh 90 days rotating (ADR-0010, frozen). Revoked by sign-out,
device revocation, integrity failure, or subscriber erasure. Immediate
revocation is achieved by a revocation check at token validation, **not** by
aggregate consistency — see [00 §0.6](00-strategic-design.md).

**Validation Rules** — A session cannot outlive its Device's trust. Refresh
token reuse invalidates the entire `tokenFamily` — the standard detection for a
stolen refresh token.

**Privacy Classification** — `tokenFamily` is `SECRET`: never logged at any
level, never in an error, never in a crash report.

**Audit Requirements** — **Change** on issue and revoke. Revocation records its
reason, because "why am I signed out" is a real support question.

---

### `VerificationChallenge`

**Attributes**

```
id            : ChallengeId          INTERNAL  · EPHEMERAL
msisdn        : MSISDN               PERSONAL  · EPHEMERAL · residency-bound
codeHash      : Hash                 SECRET    · EPHEMERAL
attempts      : Int                  INTERNAL  · EPHEMERAL
maxAttempts   : Int                  PUBLIC    · EPHEMERAL
issuedAt      : Instant              INTERNAL  · EPHEMERAL
expiresAt     : Instant              INTERNAL  · EPHEMERAL
consumedAt    : Instant?             INTERNAL  · EPHEMERAL
deliveryChannel : DeliveryChannel    PUBLIC    · EPHEMERAL
```

**Relationships** — Standalone aggregate. Deliberately references no Subscriber:
at issue time we do not know, and must not reveal, whether one exists.

**Lifecycle** — Issued, then consumed, expired, or locked. Never retained beyond
its window.

**Validation Rules** — The code is stored hashed and never in plaintext.
Attempts are bounded; exhaustion locks the MSISDN for a stated window (P-ID-5).
Expiry and wrong-code are **distinct outcomes** — collapsing them makes an
expired code look like user error.

**Privacy Classification** — `EPHEMERAL` throughout. `codeHash` is `SECRET`.

**Audit Requirements** — **Change** on failure and lockout, for abuse detection.
Successful verification is audited at the `Subscriber` level, not here.

---

### `ErasureRequest`

**Attributes**

```
id              : ErasureRequestId       INTERNAL  · LEGAL_HOLD
subscriberId    : SubscriberId <ref>     INTERNAL  · LEGAL_HOLD
requestedAt     : Instant                INTERNAL  · LEGAL_HOLD
scopes          : ErasureScope[]         PUBLIC    · LEGAL_HOLD
storeProgress   : StoreErasureStatus[]   INTERNAL  · LEGAL_HOLD
completedAt     : Instant?               INTERNAL  · LEGAL_HOLD
exceptions      : RetentionException[]   PUBLIC    · LEGAL_HOLD
```

**Relationships** — Own aggregate. Coordinates deletion across Identity,
Consumer, Telephony, AI Orchestration, Voice, Fraud, Notifications and the
object store — six stores plus backups (ADR-0009 §10).

**Lifecycle** — Requested by the Subscriber, executed as a saga across stores,
completed only when every store reports done. **Never partially completed
silently** — an incomplete erasure is an open incident, not a background job that
gave up.

**Validation Rules** — Must enumerate every store. Must record `exceptions`
explicitly — invoices and consent records survive under `LEGAL_HOLD`, and the
subscriber is told which and why. **Forwarding must be cleared at the carrier
before the Subscriber record is destroyed** (P-ID-4); leaving a former
subscriber's calls diverted to a platform that no longer holds their account
would be a serious defect.

**Privacy Classification** — `LEGAL_HOLD`. The record of an erasure must outlive
the erasure.

**Audit Requirements** — **Access** level throughout. Per-store completion is
individually recorded.

---

## 10.4 Value Objects

| Value object | Definition | Notes |
|---|---|---|
| `MSISDN` | E.164, normalised, mobile only | Shared kernel. Equality is on the normalised form — two representations of one number are one number |
| `DeviceCredentialReference` | Keystore alias + public key | The private key is never in our domain (I5) |
| `IntegrityVerdict` | device / app / basic integrity + evaluatedAt | From Play Integrity, via ACL |
| `ConsentPurpose` | `CONTACT_SYNC` · `CALL_RECORDING` · `TRANSCRIPT_RETENTION` · `PRODUCT_ANALYTICS` · `CRASH_DIAGNOSTICS` · `BUSINESS_LINE_VISIBILITY` | Shared kernel. **The Announcement is not in this enum** (INV-ID-5) |
| `ConsentSource` | `ONBOARDING` · `SETTINGS` · `CONTEXTUAL_PROMPT` · `ORGANISATION_INVITATION` | Where it was collected. Needed to prove it was informed |
| `PolicyVersion` | Semantic version of the policy text shown | |
| `RetentionPreference` | transcriptDays (7–180) · audioDays (7–90) | Bounds are statutory-adjacent and frozen in ADR-0012 |
| `SubscriberStatus` | `PENDING_VERIFICATION` · `ACTIVE` · `SUSPENDED` · `ERASURE_REQUESTED` · `ERASED` | |
| `DeviceStatus` | `PENDING_ATTESTATION` · `TRUSTED` · `UNTRUSTED` · `REVOKED` | |
| `TokenFamily` | Rotating refresh lineage identifier | `SECRET` |
| `RevocationReason` | `USER_SIGNED_OUT` · `DISPLACED_BY_ENROLMENT` · `INTEGRITY_FAILED` · `ADMIN_REVOKED` · `ERASURE` | Shown to the user — "why am I signed out" is a real question |
| `ErasureScope` | `PROFILE` · `CALLS` · `TRANSCRIPTS` · `AUDIO` · `CONTACTS` · `ANALYTICS_LINKAGE` | |
| `RetentionException` | store + reason + legalBasis + expiresAt | What survives erasure, and why |

---

## 10.5 Aggregates

| Aggregate | Root | Contains | Consistency boundary |
|---|---|---|---|
| **Subscriber** | `Subscriber` | `Device[]`, `ConsentRecord[]`, `RetentionPreference` | Device enrolment displacing another device, and consent append, must be atomic |
| **AuthSession** | `AuthSession` | — | High churn; separated so token refresh does not write the subscriber |
| **VerificationChallenge** | `VerificationChallenge` | — | Short-lived, references no subscriber by design |
| **ErasureRequest** | `ErasureRequest` | `StoreErasureStatus[]` | Saga state must be consistent with itself; the stores it coordinates are eventual |

```
┌─────────────────────────────────────────────┐
│  Subscriber  «aggregate root»               │
│  ─────────────────────────────              │
│  id · msisdn · status · region              │
│                                             │
│  ┌────────────────┐  ┌────────────────────┐ │
│  │ Device[]       │  │ ConsentRecord[]    │ │
│  │ credentialRef  │  │ purpose · granted  │ │
│  │ integrity      │  │ policyVersion      │ │
│  │ status         │  │ APPEND-ONLY        │ │
│  └────────────────┘  └────────────────────┘ │
│  ┌──────────────────────┐                   │
│  │ RetentionPreference  │                   │
│  └──────────────────────┘                   │
└─────────────────────────────────────────────┘
         ▲                    ▲
         │ <ref>              │ <ref>
┌────────┴────────┐   ┌───────┴───────────┐
│  AuthSession    │   │  ErasureRequest   │
│  «root»         │   │  «root»           │
└─────────────────┘   └───────────────────┘

┌──────────────────────────┐
│ VerificationChallenge    │   references NO subscriber —
│ «root»                   │   at issue time we must not
└──────────────────────────┘   reveal whether one exists
```

---

## 10.6 Domain Services

| Service | Responsibility | Why not on an entity |
|---|---|---|
| `MsisdnNormalisationService` | Normalise, validate, classify a number as mobile / landline / virtual | Needs a numbering-plan reference no entity should hold |
| `DeviceAttestationService` | Evaluate a Play Integrity verdict against policy | Policy is a platform decision, not a device's own |
| `ConsentEvaluationService` | Answer "may we do X for this Subscriber, right now" | Spans records, purposes and policy versions. **The single authority** — no other context re-derives consent |
| `ErasureOrchestrationService` | Drive the saga across six stores and backups | Coordinates aggregates in other contexts |
| `SessionRevocationService` | Publish and evaluate the revocation list | Sits between an aggregate and a fast-path check |

---

## 10.7 Repositories

`SubscriberRepository` · `AuthSessionRepository` ·
`VerificationChallengeRepository` · `ErasureRequestRepository`

One per aggregate root, no more. Each returns whole aggregates; none exposes a
`Device` or a `ConsentRecord` independently of its `Subscriber`.

---

## 10.8 Domain Events

| Event | Payload (identifiers only — I7) | Consumers |
|---|---|---|
| `identity.subscriber.registered.v1` | subscriberId, region | Billing, Notifications, Analytics |
| `identity.subscriber.activated.v1` | subscriberId | Consumer, Telephony, Billing |
| `identity.subscriber.suspended.v1` | subscriberId, reasonCode | Telephony, Billing, Notifications |
| `identity.subscriber.erased.v1` | subscriberId, scopes, completedAt | Every context |
| `identity.device.enrolled.v1` | subscriberId, deviceId, displacedDeviceId? | Notifications, Administration |
| `identity.device.revoked.v1` | subscriberId, deviceId, reason | Notifications, Consumer |
| `identity.consent.granted.v1` | subscriberId, purpose, policyVersion | Consumer, Voice, Analytics, Business |
| `identity.consent.withdrawn.v1` | subscriberId, purpose | Consumer, Voice, Analytics, Business |
| `identity.retention.changed.v1` | subscriberId, transcriptDays, audioDays | AI Orchestration, Voice |
| `identity.session.revoked.v1` | subscriberId, sessionId, reason | Administration |
| `identity.verification.failed.v1` | challengeId, attemptCount, reasonCode | Administration, Analytics |
| `identity.erasure.requested.v1` | subscriberId, scopes | Every context |
| `identity.erasure.completed.v1` | subscriberId, exceptions | Administration, Analytics |

**No event in this context carries an MSISDN.** The subscriber identifier is
opaque and the number is fetched by contexts entitled to it.

---

## 10.9 Commands

| Command | Refused when |
|---|---|
| `RequestVerification(msisdn, channel)` | MSISDN locked, rate limit exceeded, number is not mobile |
| `VerifyChallenge(challengeId, code)` | Expired, wrong, attempts exhausted |
| `RegisterSubscriber(challengeId, region)` | Challenge unconsumed or already used |
| `EnrolDevice(subscriberId, credentialRef, attestation)` | Attestation fails, credential is not device-bound |
| `RevokeDevice(deviceId, reason)` | Device already revoked |
| `GrantConsent(subscriberId, purpose, policyVersion)` | Purpose unrecognised, policy version stale, **more than one purpose supplied** |
| `WithdrawConsent(subscriberId, purpose)` | Purpose not currently granted |
| `SetRetentionPreference(subscriberId, transcriptDays, audioDays)` | Outside statutory bounds |
| `ChangeMsisdn(subscriberId, newMsisdn, challengeId)` | New number already active elsewhere; old forwarding not yet cleared |
| `RequestErasure(subscriberId, scopes)` | Erasure already in progress |
| `SuspendSubscriber(subscriberId, reason)` | Operator lacks role |

**`GrantConsent` accepts exactly one purpose.** A batch form of this command
does not exist, which is how Invariant U9 is enforced at the model level rather
than in the interface.

---

## 10.10 Queries

| Query | Scope |
|---|---|
| `GetSubscriber(subscriberId)` | Own context; redacted through Administration |
| `ListDevices(subscriberId)` | Subscriber's own |
| `GetConsentState(subscriberId)` | Current state per purpose, plus history |
| `IsConsentGranted(subscriberId, purpose)` | The hot-path check. **Fails closed on unknown** |
| `GetRetentionPreference(subscriberId)` | |
| `GetSubjectAccessRecord(subscriberId)` | DPDP fulfilment; joined with Administration's audit trail |
| `FindSubscriberByMsisdn(msisdn)` | **Internal only.** Never exposed in a way that reveals existence to an unauthenticated caller |

---

## 10.11 Policies

| # | Policy |
|---|---|
| **P-ID-1** | When a Device is enrolled and the limit is reached, revoke the least-recently-active Device **and notify it before the switch completes**. The notification is the anti-social-engineering control |
| **P-ID-2** | When a consent is withdrawn, delete data collected solely under that purpose, and report the count to the Subscriber |
| **P-ID-3** | When consent state is unknown or unrecognised, treat it as **not granted** (I8) |
| **P-ID-4** | When erasure is requested, clear carrier forwarding **before** destroying the Subscriber record |
| **P-ID-5** | When verification attempts are exhausted, lock the MSISDN for a stated window and surface a real countdown |
| **P-ID-6** | When a Device fails re-attestation, mark it `UNTRUSTED` and revoke its sessions. Do not delete it — the user needs to see what happened |
| **P-ID-7** | When retention is lowered, apply it to existing data immediately and state the deletion count before confirming |
| **P-ID-8** | When a refresh token is reused, invalidate the whole `tokenFamily` |

---

## 10.12 Invariants

| # | Invariant | Source |
|---|---|---|
| **INV-ID-1** | A Subscriber has exactly one active MSISDN | ADR-0010 |
| **INV-ID-2** | An MSISDN maps to at most one active Subscriber | ADR-0010 |
| **INV-ID-3** | A device credential is generated on-device, is non-exportable, and never enters our domain | **I5** |
| **INV-ID-4** | A `ConsentRecord` carries exactly one purpose; no command grants two | **U9** |
| **INV-ID-5** | The Announcement is a lawful basis, not a consent. It has no `ConsentPurpose` and cannot be withdrawn | **I1**, ADR-0012 §5.1 |
| **INV-ID-6** | `ConsentRecord` is append-only. Withdrawal appends; nothing is mutated or deleted | ADR-0012 |
| **INV-ID-7** | An erased Subscriber's identifiers are tombstoned and never reused | DPDP |
| **INV-ID-8** | Every `PERSONAL` attribute is residency-bound and does not leave the India region except under the four-condition consent gate | **I2** |
| **INV-ID-9** | A Subscriber cannot be `ACTIVE` with zero `TRUSTED` Devices | |
| **INV-ID-10** | There is no password, passphrase or knowledge factor anywhere in this context | ADR-0010 |

---

## 10.13 State Machines

### `Subscriber`

```
  PENDING_VERIFICATION ──verified + device enrolled──▶ ACTIVE
           │                                            │  ▲
           │                                  suspended  │  │ reinstated
        expired                                          ▼  │
           ▼                                        SUSPENDED
      (discarded)                                       │
                                                        │
       ACTIVE ────────── RequestErasure ──▶ ERASURE_REQUESTED
                                                        │
                                               saga completes
                                                        ▼
                                                    ERASED  «terminal»
```

`ERASED` is terminal and irreversible. `PENDING_VERIFICATION` times out.

### `Device`

```
  PENDING_ATTESTATION ──attested──▶ TRUSTED ──re-attestation fails──▶ UNTRUSTED
           │                           │                                  │
      attestation fails                │ revoke / displaced / erasure      │
           │                           ▼                                   │
           └──────────────────────▶ REVOKED «terminal» ◀───────────────────┘
```

A `REVOKED` Device is never reactivated; the same handset re-enrolling becomes a
new `Device`.

### `ConsentRecord`

```
   (none) ──GrantConsent──▶ GRANTED ──WithdrawConsent──▶ WITHDRAWN «terminal»
                               ▲                              │
                               └────── GrantConsent ──────────┘
                                    (new record, supersedes)
```

Each transition **creates a record**; none mutates one.

### `VerificationChallenge`

```
  ISSUED ──correct code──▶ CONSUMED «terminal»
    │  │
    │  └──attempts exhausted──▶ LOCKED «terminal»
    └─────────ttl elapsed─────▶ EXPIRED «terminal»
```

### `ErasureRequest`

```
  REQUESTED ──▶ IN_PROGRESS ──all stores done──▶ COMPLETED «terminal»
                     │
                     └──any store fails──▶ BLOCKED ──▶ incident
```

`BLOCKED` is **not** terminal and **not** silent. An incomplete erasure is an
open incident, per ADR-0009 §15.

---

## 10.14 Ownership

| Aspect | Value |
|---|---|
| Team | `callscreen/identity` |
| Services | `identity`, `edge-api` (auth edge only) |
| Durable store | `identity` Aurora, schema `identity` |
| Ephemeral | Redis — challenges, revocation list |
| CODEOWNERS | `docs/domain/10-identity.md`, `contracts/proto/callscreen/identity/**` |
| Data owner (compliance) | `callscreen/identity` |

---

## 10.15 External Dependencies

| Dependency | Purpose | Guard |
|---|---|---|
| SMS / voice OTP provider | Deliver `VerificationChallenge` | **ACL.** Provider failure is a delivery outcome, not a domain error. Voice fallback after two failed SMS |
| Google Play Integrity | Device and app attestation | **ACL.** A verdict is translated into `IntegrityVerdict`; the provider's shape never reaches the domain |
| AWS KMS | Encryption of `PERSONAL` at rest | Key policy separate from data-plane IAM (ADR-0009 §10) |
| Numbering-plan reference data | MSISDN classification | Versioned reference data, not a live call |

---

## 10.16 Security Constraints

| # | Constraint |
|---|---|
| 1 | **The device private key never exists in our domain.** Only a public key and a Keystore alias (I5) |
| 2 | **No enumeration oracle.** `RequestVerification` and `FindSubscriberByMsisdn` return identical outcomes whether or not the number is registered |
| 3 | **OTP codes are stored hashed**, never logged, never in an error, never in a crash report |
| 4 | **Integrity failure messages are deliberately non-specific.** Enumerating which check failed is free reconnaissance for an attacker |
| 5 | **Device enrolment notifies the displaced device before the switch completes.** An attacker holding the OTP must still beat a notification on the victim's own phone |
| 6 | **Consent records survive erasure** under `LEGAL_HOLD`, referenced by a retained pseudonymous key. Lawfulness stays provable; the person stops being identifiable |
| 7 | **Sign-out clears cached transcripts and contacts on the device.** A shared or resold handset is the realistic threat |
| 8 | **Consent is never inferred.** Absence of a record is absence of consent, not silence-as-agreement |
| 9 | **All `PERSONAL` attributes are residency-bound** (I2), enforced by the platform egress policy rather than by review |
| 10 | **Administration reads this context only through its redaction ACL**, with an `AccessGrant` for any `PERSONAL` reveal (U12) |
