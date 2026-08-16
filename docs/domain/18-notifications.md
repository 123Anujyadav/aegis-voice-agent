# 18 · Notifications

**Subdomain:** Supporting · **Prefix:** `NO` · **Topic domain:** `notifications`

---

## 18.1 Purpose

Interrupt the Subscriber for exactly three things, deliver those reliably, and
never for anything else.

## 18.2 Responsibilities

**Owns**

- `NotificationChannel` — the six channels and their importance
- `NotificationPreference` — per-subscriber, per-channel
- `Notification` and its delivery lifecycle
- `DeviceToken` registration and invalidation
- **The interruption policy** — the rule that makes silence the default

**Does not own**

| Not owned | Owned by |
|---|---|
| Whether a verdict is alert-worthy | Fraud Detection states it; Notifications delivers |
| The content being referenced | The owning context |
| Push transport | Google, behind an ACL |
| Whether recording is displayed | The interface, bound by U5 |

> **The interruption policy is this context's reason to exist.** Any context can
> ask for a notification. Only Notifications decides whether the platform's
> promise of silence permits it.

---

## 18.3 Domain Entities

### `NotificationChannel` — reference entity

Six channels. **There is no seventh** (INV-NO-3).

```
key         : ChannelKey            PUBLIC · STANDARD
importance  : ChannelImportance     PUBLIC · STANDARD
interruptive: Boolean               PUBLIC · STANDARD
ongoing     : Boolean               PUBLIC · STANDARD
dndBypassAllowed : Boolean          PUBLIC · STANDARD
defaultEnabled   : Boolean          PUBLIC · STANDARD
```

| Channel | Importance | Interruptive | Ongoing | DND bypass | Default |
|---|---|---|---|---|---|
| `screening_live` | HIGH | **Yes** | Yes | No | On |
| `fraud_alert` | HIGH | **Yes** | No | Opt-in | On |
| `forwarding_health` | HIGH | **Yes** | Yes | Opt-in | On |
| `screening_summary` | DEFAULT | No | No | No | On |
| `account` | DEFAULT | No | No | No | On |
| `product` | LOW | No | No | No | **Off** |

**The three interruptive channels are the three permitted interruptions.**
Adding a fourth is a domain change requiring the same scrutiny as an invariant
amendment, not a configuration entry.

**Validation Rules** — `product` is off by default and cannot be defaulted on by
any command. `dndBypassAllowed` is true only for `fraud_alert` and
`forwarding_health`, and even then the Subscriber must opt in explicitly.

---

### `NotificationPreference` — aggregate root, one per Subscriber

```
subscriberId    : SubscriberId <ref>            INTERNAL · STANDARD
channelSettings : ChannelSetting[] <owned>      PUBLIC   · STANDARD
lockScreenVisibility : LockScreenVisibility     PUBLIC   · STANDARD
updatedAt       : Instant                       INTERNAL · STANDARD
```

`ChannelSetting`: `channelKey · enabled · dndBypass · soundEnabled`

**Lifecycle** — Created with channel defaults at activation. Mirrors, but does
**not duplicate**, Android's own channel settings: where the OS owns a control,
we deep-link to it rather than maintaining a second set that can disagree.

**Validation Rules** — **No preference can suppress the recording indicator**
(INV-NO-4) — it is a legal disclosure, not a notification. `dndBypass` is
settable only on channels where `dndBypassAllowed` is true.
`lockScreenVisibility` defaults to `PRIVATE`; raising it states the consequence.

**Privacy Classification** — `PUBLIC`/`INTERNAL`. Preferences are not personal
content.

**Audit Requirements** — **Change** level.

---

### `Notification` — aggregate root

```
id            : NotificationId          INTERNAL · SHORT
subscriberId  : SubscriberId <ref>      INTERNAL · SHORT
deviceIds     : DeviceId[] <ref>        INTERNAL · SHORT
channel       : ChannelKey              PUBLIC   · SHORT
template      : TemplateKey             PUBLIC   · SHORT
parameters    : TemplateParameter[]     INTERNAL · SHORT
subjectRefs   : ResourceId[]            INTERNAL · SHORT
state         : NotificationState       PUBLIC   · SHORT
supersedes    : NotificationId?         INTERNAL · SHORT
attempts      : DeliveryAttempt[] <owned>
createdAt     : Instant                 INTERNAL · SHORT
lastUpdatedAt : Instant?                INTERNAL · SHORT
expiresAt     : Instant                 INTERNAL · SHORT
```

**Relationships** — References a `Subscriber`, `Device`s, and the subject
resources by identity. **It contains no content** — only a `template` key and
enumerated `parameters` (INV-NO-1).

**Lifecycle** — Created when a policy permits an interruption. A
`screening_live` notification **updates in place at most 1 Hz** (P-NO-2) and
**becomes** the `screening_summary` when the call ends — it never produces both
(P-NO-1). Expires; superseded; delivered; failed.

**Validation Rules**

- `parameters` are enumerated values and template-bound. **No caller-supplied
  string may be a parameter of a title template** (P-NO-4, U10) — a caller who
  says "tap Allow to continue" must not be able to draw a notification.
- No interim ASR text may be a parameter (P-NO-3) — a notification that rewrites
  itself under the reader's eyes reads as a malfunction.
- A `screening_live` notification is **not dismissible** while its session is
  live (INV-NO-2).

**Privacy Classification** — `SHORT` retention throughout, `INTERNAL`. The
resolved display text is `PERSONAL` and is rendered **on the device**, never
stored server-side.

**Audit Requirements** — **Change** on dispatch and delivery outcome. Content is
never audited because content is never held.

---

### `DeliveryAttempt`

```
id            : AttemptId            INTERNAL · SHORT
notificationId: NotificationId <ref> INTERNAL · SHORT
deviceId      : DeviceId <ref>       INTERNAL · SHORT
outcome       : DeliveryOutcome      PUBLIC   · SHORT
providerCode  : String?              INTERNAL · SHORT
attemptedAt   : Instant              INTERNAL · SHORT
```

**Validation Rules** — A `TOKEN_INVALID` outcome triggers token invalidation,
not a retry. Retrying a dead token forever is how a delivery pipeline becomes a
cost centre.

---

### `DeviceToken`

```
id            : TokenId              INTERNAL · SHORT
deviceId      : DeviceId <ref>       INTERNAL · SHORT
subscriberId  : SubscriberId <ref>   INTERNAL · SHORT
token         : PushToken            SECRET   · SHORT
platform      : Platform             PUBLIC   · SHORT
registeredAt  : Instant              INTERNAL · SHORT
invalidatedAt : Instant?             INTERNAL · SHORT
```

**Lifecycle** — Registered on app start, refreshed by the OS, invalidated on
provider rejection, device revocation, sign-out, or erasure. **Invalidated
immediately on `identity.device.revoked.v1`** — a revoked device must stop
receiving a subscriber's call notifications at once.

**Privacy Classification** — `token` is `SECRET`: it is a capability to
interrupt a specific person's phone.

**Audit Requirements** — **Change** on registration and invalidation.

---

## 18.4 Value Objects

| Value object | Definition | Notes |
|---|---|---|
| `ChannelKey` | The six keys | Closed set |
| `ChannelImportance` | `HIGH` · `DEFAULT` · `LOW` | Maps to Android channel importance |
| `TemplateKey` | Identifies a localised template | Content lives in the client's string catalogue |
| `TemplateParameter` | Enumerated value or `ResourceId`. **Never free text** | The mechanism enforcing U10 |
| `LockScreenVisibility` | `PRIVATE` (default) · `PUBLIC` | `PRIVATE` renders "Screening a call" and nothing more |
| `DeliveryOutcome` | `DELIVERED` · `TOKEN_INVALID` · `RATE_LIMITED` · `PROVIDER_ERROR` · `SUPPRESSED_BY_POLICY` | |
| `NotificationState` | `PENDING` · `DISPATCHED` · `DELIVERED` · `SUPERSEDED` · `FAILED` · `EXPIRED` | |
| `Interruption` | `LIVE_SCREENING` · `FRAUD_ALERT` · `FORWARDING_BROKEN` | **The three. There is no fourth** |
| `StalenessThreshold` | Per channel | A late alert is suppressed rather than delivered as stale news |

### `TemplateParameter` is how U10 is enforced

A notification cannot contain a rendered sentence. It contains a template key
and a list of parameters that are either enumerated domain values or opaque
resource identifiers.

That means there is **no field in which a caller's speech could arrive** — the
model does not have a place to put it. Enforcing U10 by validating strings would
require every producer to remember; enforcing it by having no string field
requires nobody to remember anything.

---

## 18.5 Aggregates

| Aggregate | Root | Contains |
|---|---|---|
| **NotificationPreference** | `NotificationPreference` | `ChannelSetting[]` |
| **Notification** | `Notification` | `DeliveryAttempt[]` |
| **DeviceToken** | `DeviceToken` | — |
| **NotificationChannel** | `NotificationChannel` | Reference data |

```
┌────────────────────────────────────────────────────────┐
│  NotificationChannel  ×6   ── THERE IS NO SEVENTH      │
│  screening_live · fraud_alert · forwarding_health      │
│      ↑ THE THREE PERMITTED INTERRUPTIONS ↑             │
│  screening_summary · account · product (off default)   │
└────────────────────────────────────────────────────────┘
              │
              ▼
┌──────────────────────────┐   ┌──────────────────────────────┐
│ NotificationPreference   │   │  Notification  «root»        │
│ «root» per subscriber    │   │   template + parameters      │
│  ┌────────────────────┐  │   │   NO CONTENT FIELD EXISTS ✱  │
│  │ ChannelSetting[]   │  │   │   subjectRefs (identifiers)  │
│  │ cannot suppress    │  │   │   supersedes ──▶ live becomes│
│  │ RECORDING (U5)     │  │   │                  summary     │
│  └────────────────────┘  │   │   ┌────────────────────────┐ │
└──────────────────────────┘   │   │ DeliveryAttempt[]      │ │
                               │   └────────────────────────┘ │
┌──────────────────────────┐   └──────────────────────────────┘
│ DeviceToken  «root»      │
│  token = SECRET —        │   ✱ U10 is enforced by the ABSENCE
│  a capability to         │     of a field, not by validating
│  interrupt a person      │     one.
└──────────────────────────┘
```

---

## 18.6 Domain Services

| Service | Responsibility | Notes |
|---|---|---|
| **`InterruptionPolicyService`** | Decide whether an interruption is permitted | **The heart of the context.** Three permitted interruptions; everything else waits until the app is opened |
| **`CoalescingService`** | Ensure one notification per event | A live notification **becomes** the summary. Multiple summaries in a quiet period collapse into a group carrying a count, never caller content |
| `QuietPolicyService` | Respect Do Not Disturb | We implement no competing schedule. Only `fraud_alert` and `forwarding_health` may bypass, opt-in |
| `StalenessService` | Suppress a late alert rather than deliver stale news | |
| `TokenLifecycleService` | Register, refresh, invalidate | Invalidates immediately on device revocation |
| `TemplateResolutionService` | Bind a template key and parameters, **on the device** | Resolved text never exists server-side |

---

## 18.7 Repositories

`NotificationPreferenceRepository` · `NotificationRepository` ·
`DeviceTokenRepository` · `NotificationChannelRepository`

---

## 18.8 Domain Events

| Event | Payload | Consumers |
|---|---|---|
| `notifications.notification.dispatched.v1` | notificationId, channel, template, subscriberId | Analytics |
| `notifications.notification.delivered.v1` | notificationId, deviceId, latencyMs | Analytics |
| `notifications.notification.failed.v1` | notificationId, outcome | Administration, Analytics |
| `notifications.notification.suppressed.v1` | channel, reason | **Analytics** — measures whether silence is working |
| `notifications.preference.changed.v1` | subscriberId, channel, enabled | Analytics |
| `notifications.token.registered.v1` | deviceId, platform | — |
| `notifications.token.invalidated.v1` | deviceId, reason | Identity |

**No event carries a template parameter or a resolved string.** The suppression
event is deliberately measured: a rising suppression rate means upstream
contexts are asking for interruptions the policy is correctly refusing, and that
is worth knowing.

---

## 18.9 Commands

| Command | Refused when |
|---|---|
| `RegisterDeviceToken(deviceId, token, platform)` | Device revoked |
| `RequestNotification(subscriberId, channel, template, parameters, subjectRefs)` | **Interruption policy refuses.** Channel disabled. Parameters contain free text. Stale beyond threshold |
| `UpdateLiveNotification(notificationId, parameters)` | Session ended; **rate exceeds 1 Hz** |
| `SupersedeNotification(notificationId, newNotificationId)` | — |
| `SetChannelPreference(subscriberId, channel, enabled, dndBypass?)` | `dndBypass` on a channel where it is not allowed |
| `SetLockScreenVisibility(subscriberId, visibility)` | — (raising states the consequence) |
| `InvalidateToken(tokenId, reason)` | — |

**`RequestNotification` is a request, not an instruction.** Any context may ask;
Notifications decides. That asymmetry is what keeps the platform quiet.

---

## 18.10 Queries

| Query | Scope |
|---|---|
| `GetPreferences(subscriberId)` | Own |
| `GetChannelCatalogue()` | Public |
| `GetDeliveryStatus(notificationId)` | Internal, Administration |
| `GetSuppressionRate(channel, period)` | Administration, Analytics |

---

## 18.11 Policies

| # | Policy |
|---|---|
| **P-NO-1** | A screening produces the live notification, which **becomes** the summary. Never both |
| **P-NO-2** | A live notification updates in place at most 1 Hz, never as a new notification per turn |
| **P-NO-3** | Interim ASR text is never a notification parameter |
| **P-NO-4** | No caller-supplied string appears in a notification, and especially not in a title (U10) |
| **P-NO-5** | Lock-screen content defaults to `PRIVATE`. Raising it states the consequence |
| **P-NO-6** | A **low-confidence** fraud verdict does not notify. It renders and waits to be found |
| **P-NO-7** | No badge counts. A number on an app icon is an engagement device |
| **P-NO-8** | Quiet hours are the system's job. We implement no competing schedule |
| **P-NO-9** | A `TOKEN_INVALID` outcome invalidates the token; it does not retry |
| **P-NO-10** | A late alert past its staleness threshold is suppressed, not delivered as stale news |
| **P-NO-11** | On `identity.device.revoked.v1`, invalidate that device's token immediately |
| **P-NO-12** | When nothing happened, send nothing. **Silence is a correct output** |

---

## 18.12 Invariants

| # | Invariant | Source |
|---|---|---|
| **INV-NO-1** | A `Notification` carries a template key, enumerated parameters and identifiers. **There is no free-text content field** | **I7**, **U10** |
| **INV-NO-2** | A `screening_live` notification is not dismissible while its session is live | |
| **INV-NO-3** | There are exactly six channels. Adding one is a domain change | Principle 6 |
| **INV-NO-4** | No preference, mode, or accessibility setting can suppress the recording indicator | **U5** |
| **INV-NO-5** | Exactly three interruptive channels exist | Principle 6 |
| **INV-NO-6** | `product` is off by default and cannot be defaulted on | |
| **INV-NO-7** | `DeviceToken.token` is `SECRET` and never appears in an event, a log or an error | |
| **INV-NO-8** | Resolved notification text exists only on the device, never server-side | |

---

## 18.13 State Machines

### `Notification`

```
   PENDING ──policy permits──▶ DISPATCHED ──ack──▶ DELIVERED «terminal»
      │                            │
      │                            ├──update (≤1 Hz)──▶ DISPATCHED
      │                            │
      │                            ├──call ends──▶ SUPERSEDED «terminal»
      │                            │               (live BECOMES summary)
      │                            │
      │                            └──token invalid / provider error──▶ FAILED
      │
      └──policy refuses──▶ SUPPRESSED «terminal»
                           reason recorded and measured
```

### `DeviceToken`

```
  REGISTERED ──OS refresh──▶ REGISTERED
      │
      ├── provider says invalid ──┐
      ├── device revoked ─────────┤
      ├── sign-out ───────────────┼──▶ INVALIDATED «terminal»
      └── erasure ────────────────┘
```

### The live-to-summary transition

```
  telephony.call.answered
        │
        ▼
  Notification(screening_live)  ── ongoing, not dismissible
        │
        ├── ai.conversation.turn_completed ──▶ update in place, ≤ 1 Hz
        │                                      (FINAL turns only — P-NO-3)
        │
        ├── fraud.assessment.completed, confidence ≥ MEDIUM
        │        └──▶ update in place + ONE re-alert
        │             confidence = LOW ──▶ NO re-alert (P-NO-6)
        │
        └── telephony.call.ended
                 └──▶ SUPERSEDED by Notification(screening_summary), silent
                      ONE notification total, not two.
```

---

## 18.14 Ownership

| Aspect | Value |
|---|---|
| Team | `callscreen/platform` |
| Service | `notification-fanout` (Go) |
| Durable store | `identity` Aurora, schema `notifications` |
| Ephemeral | Redis — delivery state, rate limiting, coalescing windows |
| CODEOWNERS | `docs/domain/18-notifications.md`, `services/go/notification-fanout/**` |

---

## 18.15 External Dependencies

| Dependency | Purpose | Guard |
|---|---|---|
| **Firebase Cloud Messaging** | Push delivery to Android | **ACL.** Provider error codes translate to domain `DeliveryOutcome`s. **Payloads carry template keys and identifiers only** — the push transport is a third party and never receives content |
| Android notification channels | The OS-side settings | We deep-link rather than duplicate. A second set of switches that disagrees with the OS is a support nightmare |
| Every upstream context | Notification requests | **Conformist.** Notifications adapts; it never asks an upstream to change |

**The FCM constraint is a privacy property, not just an integration detail.**
Because payloads contain no content, a compromise of the push channel yields
template keys and opaque identifiers — not transcripts, names or numbers.

---

## 18.16 Security Constraints

| # | Constraint |
|---|---|
| 1 | **There is no free-text field in a `Notification`.** U10 is enforced by absence, not by validation (INV-NO-1) |
| 2 | **Push payloads carry no content**, so the transport provider never receives personal data |
| 3 | **Notification text is resolved on the device** from a local string catalogue |
| 4 | **Lock-screen content is `PRIVATE` by default.** The lock screen is the most common shoulder-surf surface |
| 5 | **`DeviceToken` is `SECRET`** — it is a capability to interrupt a specific person's phone |
| 6 | **Tokens invalidate immediately on device revocation.** A revoked device must stop receiving call notifications at once |
| 7 | **The recording indicator is not a notification** and cannot be suppressed by any preference (U5) |
| 8 | **Interim ASR never reaches a notification** |
| 9 | **Any context may request; only Notifications decides.** The interruption policy is not overridable by a caller |
| 10 | **Suppression is measured**, so a context asking for interruptions it should not is visible rather than silently refused |
