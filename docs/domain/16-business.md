# 16 · Business

**Subdomain:** Supporting · **Prefix:** `BU` · **Topic domain:** `business`

---

## 16.1 Purpose

Let an organisation run CallScreen as a receptionist — numbers, routing, hours,
who can see what — while making it structurally impossible for that organisation
to see a member's personal calls.

## 16.2 Responsibilities

**Owns**

- `Organisation`, `Membership`, `Invitation`
- `BusinessLine` — an organisation's screened number, and its assignment
- `RoutingPolicy` and `BusinessHours`
- `BusinessContact` — the lightweight CRM
- `ApiKey` and `WebhookEndpoint`
- `VerifiedBusinessIdentity` — the registry of businesses whose calls we can
  attribute confidently

**Does not own**

| Not owned | Owned by |
|---|---|
| The subscriber identity of a member | Identity |
| The telephony line itself | Telephony — Business references it |
| Executing a transfer | Business decides; Telephony executes |
| Billing for the organisation | Billing |
| Any member's personal line | **Nobody, here. It is not an object in this context** |

> **INV-BU-1 is the defining property of this context.** Personal lines are not
> addressable objects here — not filtered, not permission-checked, **absent**. A
> permission check can be misconfigured; an absence cannot.

---

## 16.3 Domain Entities

### `Organisation` — aggregate root

**Attributes**

```
id            : OrganisationId              INTERNAL · STANDARD
name          : String                      PERSONAL · STANDARD
timezone      : IanaTimezone                PUBLIC   · STANDARD
taxIdentity   : TaxIdentity?                PERSONAL · LEGAL_HOLD
memberships   : Membership[] <owned>
invitations   : Invitation[] <owned>
status        : OrganisationStatus          INTERNAL · STANDARD
createdAt     : Instant                     INTERNAL · STANDARD
deletingSince : Instant?                    INTERNAL · STANDARD
```

**Relationships** — Owns `Membership` and `Invitation` by containment.
References `BusinessLine` and `BusinessContact` by identity — those are separate
aggregates because they change on a different cadence and, in the case of
`BusinessLine`, live in a different persistence cluster.

**Lifecycle** — Created by a Subscriber, who becomes the sole `OWNER`. Suspended
on billing lapse beyond grace. Deleted only through an explicit flow that
itemises consequences and confirms by typing the organisation name. **Numbers
are released only after a retention window**, so a mistaken deletion is
recoverable.

**Validation Rules** — Exactly one `OWNER` at all times (INV-BU-2). `taxIdentity`
(GSTIN) is required before the first invoice, not at creation — collecting it up
front is friction at exactly the wrong moment. `timezone` is set once and used
everywhere, because business hours in the wrong timezone silently misroute calls.

**Privacy Classification** — `name` is `PERSONAL`: a sole trader's organisation
name is frequently their own name. `taxIdentity` is `LEGAL_HOLD`.

**Audit Requirements** — **Change** level on every attribute, and on the whole
lifecycle.

---

### `Membership`

```
id            : MembershipId              INTERNAL · STANDARD
organisationId: OrganisationId <ref>      INTERNAL · STANDARD
subscriberId  : SubscriberId <ref>        INTERNAL · STANDARD
role          : OrganisationRole          PUBLIC   · STANDARD
assignedLines : BusinessLineId[] <ref>    INTERNAL · STANDARD
joinedAt      : Instant                   INTERNAL · STANDARD
removedAt     : Instant?                  INTERNAL · STANDARD
consentRef    : ConsentRecordId <ref>     INTERNAL · LEGAL_HOLD
```

**Relationships** — Contained in `Organisation`. References a `Subscriber` and
the **specific `ConsentRecord`** under which they agreed to organisational
visibility — which is what makes the boundary provable rather than merely stated.

**Lifecycle** — Created when an `Invitation` is accepted. Role changes take
effect immediately and **re-scope the member's live session with a visible
notice**, never as a silent capability change. Removal is soft: the record
persists with `removedAt` so historical call attribution stays intact.

**Validation Rules** — Removing the last `OWNER` is refused until ownership
transfers (P-BU-2). A `BILLING` role cannot be assigned `assignedLines` — the
role has no call-content capability, and assigning lines to it would imply
otherwise.

**Privacy Classification** — `INTERNAL`. `consentRef` is `LEGAL_HOLD`.

**Audit Requirements** — **Change** on creation, role change, line assignment
and removal. Role changes are the highest-value audit target in this context.

---

### `Invitation`

```
id            : InvitationId          INTERNAL  · SHORT
organisationId: OrganisationId <ref>  INTERNAL  · SHORT
invitedMsisdn : MSISDN                PERSONAL  · SHORT · residency-bound
role          : OrganisationRole      PUBLIC    · SHORT
tokenHash     : Hash                  SECRET    · SHORT
state         : InvitationState       PUBLIC    · SHORT
expiresAt     : Instant               INTERNAL  · SHORT
invitedBy     : MembershipId <ref>    INTERNAL  · SHORT
```

**Relationships** — Contained in `Organisation`.

**Lifecycle** — Issued, delivered by SMS, accepted, declined, expired, or
revoked. **Bound server-side to `invitedMsisdn`** (P-BU-3): the token alone
grants nothing, and a different number presenting it is refused with a clear
statement rather than a silent failure.

**Validation Rules** — An invitation for a number that already holds an active
membership is refused. Re-inviting a number that declined is permitted once,
then rate-limited — persistence past that point is harassment with a UI.

**Privacy Classification** — `invitedMsisdn` is `PERSONAL`; `tokenHash` is
`SECRET`, stored hashed and never logged.

**Audit Requirements** — **Change** level throughout.

---

### `BusinessLine` — aggregate root

```
id            : BusinessLineId             INTERNAL · STANDARD
organisationId: OrganisationId <ref>       INTERNAL · STANDARD
lineId        : LineId <ref>               INTERNAL · STANDARD
label         : String                     PERSONAL · STANDARD
routingPolicy : RoutingPolicy <owned>
businessHours : BusinessHours <owned>
assistantProfileId : AssistantProfileId <ref> INTERNAL · STANDARD
recordingEnabled : Boolean                 PUBLIC   · STANDARD
status        : BusinessLineStatus         PUBLIC   · STANDARD
```

**Relationships** — References a Telephony `Line` — it does not contain one. The
two live in different persistence clusters, which makes adding a line a **saga**
rather than a transaction ([§16.13](#1613-state-machines)).

**Lifecycle** — Created by provisioning a new number, bringing an existing
business number under screening, porting one in, or adopting a member's line
**with that member's consent**. Removed by an explicit flow that confirms by
typing the number — it is a working phone line.

**Validation Rules** — The referenced Telephony `Line` must be owned by the
organisation or by a member who consented (INV-BU-3). A `BusinessLine` cannot be
put into service with an incomplete routing policy — a half-configured number is
never live.

**Privacy Classification** — `label` is `PERSONAL` (often a person's name —
"Dr Nair's line").

**Audit Requirements** — **Change** level on every routing, hours and assignment
change, with before and after.

---

### `RoutingPolicy` · `RoutingRule`

```
RoutingPolicy
  businessLineId : BusinessLineId <ref>   INTERNAL · STANDARD
  rules          : RoutingRule[] <owned>
  fallback       : RoutingTarget          PUBLIC   · STANDARD   ── MANDATORY
  effectiveFrom  : Instant                PUBLIC   · STANDARD
  version        : Int                    INTERNAL · STANDARD

RoutingRule
  id        : RuleId              INTERNAL · STANDARD
  condition : RoutingCondition    PUBLIC   · STANDARD
  target    : RoutingTarget       PUBLIC   · STANDARD
  timeout   : TransferTimeout     PUBLIC   · STANDARD
  order     : Int                 INTERNAL · STANDARD
```

**Lifecycle** — Versioned. A change states its effective time before applying,
and the **previous version stays live until the new one saves successfully** — a
half-applied routing rule is a business outage.

**Validation Rules**

- `fallback` is **mandatory** (INV-BU-4). A policy with no terminal fallback is
  invalid, because "the call goes nowhere" is not an acceptable outcome.
- A rule that can never fire — one ordered after a catch-all — is **rejected at
  save with an explanation** (P-BU-4), not silently accepted.
- A gap in `businessHours` is legal and is **named** in the preview: "nothing is
  configured between 22:00 and 09:00; callers will hear the after-hours
  message."
- A `target` referencing a removed member is detected and surfaced, not
  silently resolved to the fallback (P-BU-7).

**Privacy Classification** — `PUBLIC`/`INTERNAL`. Routing is configuration.

**Audit Requirements** — **Change** level with before and after. Routing is the
single most consequential configuration in this context.

---

### `BusinessContact` — aggregate root

```
id            : ContactId              INTERNAL  · STANDARD
organisationId: OrganisationId <ref>   INTERNAL  · STANDARD
number        : PhoneNumber            PERSONAL  · STANDARD · residency-bound
displayName   : String?                PERSONAL  · STANDARD
notes         : ContactNote[] <owned>
tags          : Tag[]                  PERSONAL  · STANDARD
firstSeenAt   : Instant                PERSONAL  · STANDARD
lastSeenAt    : Instant                PERSONAL  · STANDARD
```

**Lifecycle** — Created automatically on the first call from a number,
deduplicated by number within the organisation. Deleted on organisation deletion
or on the **caller's** erasure request — a caller is a data principal too, and
their rights apply to a business's contact record about them.

**Validation Rules** — Never linked to a member's personal contacts; the portal
has no visibility of those.

**Privacy Classification** — `PERSONAL` throughout. `ContactNote` content is
`SENSITIVE` — free text written by staff about a caller.

**Audit Requirements** — **Change** level; **Access** on note content.

---

### `ApiKey` · `WebhookEndpoint`

```
ApiKey
  id · organisationId <ref> · prefix · keyHash (SECRET) · scopes[]
  createdBy <ref> · createdAt · lastUsedAt · revokedAt

WebhookEndpoint
  id · organisationId <ref> · url · scopes[] · includeContent (default FALSE)
  secretHash (SECRET) · consecutiveFailures · state · disabledAt
```

**`ApiKey` lifecycle** — The plaintext exists **only in the creation response**
(INV-BU-5). Thereafter only `prefix` and `createdAt` are readable. A key
retrievable later is a key stored retrievably.

**`WebhookEndpoint` lifecycle** — Registered, delivering, failing, disabled.
After 24 hours of consecutive failure the endpoint is **disabled with a
notification** (P-BU-6), not retried indefinitely — hammering a dead endpoint
turns our problem into theirs.

**Validation Rules** — Endpoints must be HTTPS. `includeContent` defaults to
false and enabling it requires a confirmation **naming what will be
transmitted** — mirroring Invariant I7's discipline at the customer boundary.

**Audit Requirements** — **Change** on creation, rotation, revocation and
auto-disable.

---

### `VerifiedBusinessIdentity` — aggregate root

The registry that makes a caller's claimed identity safe to render.

```
id            : VerifiedBusinessId     PUBLIC · STANDARD
legalName     : String                 PUBLIC · STANDARD
displayName   : String                 PUBLIC · STANDARD
numbers       : PhoneNumber[]          PUBLIC · STANDARD
registrySource: RegistrySource         PUBLIC · STANDARD
verifiedAt    : Instant                PUBLIC · STANDARD
status        : VerificationStatus     PUBLIC · STANDARD
logoRef       : ObjectReference?       PUBLIC · STANDARD
```

**Relationships** — **Deliberately not attached to `Organisation`.** A verified
business may call our subscribers without being our customer, so hanging it off
`Organisation` would make verification a side effect of being a paying customer,
which is exactly backwards.

**Lifecycle** — Verified against a registry, published, re-verified on a
schedule, revoked on failure. A revocation propagates immediately — a business
whose verification lapsed must stop being rendered as verified at once.

**Validation Rules** — `displayName` originates from the registry, **never from
the caller's speech**. This is the one place a caller-associated name renders at
title weight, and it is safe only because of that provenance (INV-CO-7, U10).

**Privacy Classification** — `PUBLIC`. Business registry data.

**Audit Requirements** — **Change** on verification and revocation.

---

## 16.4 Value Objects

| Value object | Definition | Notes |
|---|---|---|
| `OrganisationRole` | `OWNER` · `ADMIN` · `MEMBER` · `BILLING` · `VIEWER` | **`BILLING` has no call-content capability** (INV-BU-6) |
| `RoutingTarget` | `Member(id)` · `Team(ids)` · `Number(e164)` · `Voicemail` · `TakeMessage` | |
| `RoutingCondition` | `WithinHours` · `OutsideHours` · `Holiday` · `Always` | |
| `TransferTimeout` | Seconds, bounded 10–60 | |
| `BusinessHours` | Weekly schedule + holidays + `IanaTimezone` | Timezone comes from the Organisation, set once |
| **`VisibilityBoundary`** | The set of `BusinessLineId`s an Organisation can see. **Personal lines are not expressible in this type** | The boundary encoded as a type |
| `ApiScope` | `CALLS_READ` · `NUMBERS_READ` · `NUMBERS_WRITE` · `CONTACTS_READ` · `CONTACTS_WRITE` · `WEBHOOKS_MANAGE` | Default minimal |
| `InvitationState` | `ISSUED` · `ACCEPTED` · `DECLINED` · `EXPIRED` · `REVOKED` | |
| `RegistrySource` | The verification authority | Named, so "verified" means something specific |
| `TaxIdentity` | GSTIN + legal name + address | `LEGAL_HOLD` |
| `RoutingPreview` | Plain-language prose rendered from a policy | See below |

### `RoutingPreview` as a modelled value object

Routing rules are **configured as a form and understood as a sentence**. The
plain-language preview is not a UI convenience — it is the artefact a business
owner actually reasons about at 9 pm, and it is what the domain asks them to
confirm.

Modelling it means the prose is generated from the policy by a domain service,
so it cannot drift from the rules it describes. A preview written by hand in the
interface would eventually describe a policy that no longer exists.

---

## 16.5 Aggregates

| Aggregate | Root | Contains | Cluster |
|---|---|---|---|
| **Organisation** | `Organisation` | `Membership[]`, `Invitation[]` | `identity` Aurora |
| **BusinessLine** | `BusinessLine` | `RoutingPolicy`, `RoutingRule[]`, `BusinessHours` | `telephony` Aurora |
| **BusinessContact** | `BusinessContact` | `ContactNote[]` | `identity` Aurora |
| **ApiKey** | `ApiKey` | — | `identity` Aurora |
| **WebhookEndpoint** | `WebhookEndpoint` | `DeliveryAttempt[]` | `identity` Aurora |
| **VerifiedBusinessIdentity** | `VerifiedBusinessIdentity` | — | Reference data |

```
┌───────────────────────────────────────┐    ┌────────────────────────────────┐
│ Organisation  «root»  [identity]      │    │ BusinessLine «root» [telephony]│
│  exactly ONE owner, always            │    │  lineId ──▶ Telephony Line     │
│  ┌─────────────────┐ ┌──────────────┐ │    │  ┌──────────────────────────┐  │
│  │ Membership[]    │ │ Invitation[] │ │◀──▶│  │ RoutingPolicy            │  │
│  │  role           │ │ bound to     │ │    │  │  fallback MANDATORY      │  │
│  │  assignedLines  │ │ invitedMsisdn│ │    │  │  ┌────────────────────┐  │  │
│  │  consentRef     │ │ SERVER-SIDE  │ │    │  │  │ RoutingRule[]      │  │  │
│  └─────────────────┘ └──────────────┘ │    │  │  │ unreachable rule   │  │  │
└───────────────────────────────────────┘    │  │  │ REJECTED at save   │  │  │
              ▲  DIFFERENT CLUSTERS          │  │  └────────────────────┘  │  │
              │  ⇒ ADDING A LINE IS A SAGA   │  │ BusinessHours            │  │
              └──────────────────────────────┤  └──────────────────────────┘  │
                                             └────────────────────────────────┘
┌──────────────────────┐ ┌─────────────┐ ┌──────────────────────────────────┐
│ BusinessContact «root│ │ ApiKey «root│ │ VerifiedBusinessIdentity «root»  │
│  notes SENSITIVE     │ │ plaintext   │ │  NOT attached to Organisation —  │
└──────────────────────┘ │ ONCE only   │ │  a verified business need not be │
                         └─────────────┘ │  our customer                    │
                                         └──────────────────────────────────┘

  ╔══════════════════════════════════════════════════════════════════╗
  ║  PERSONAL LINES DO NOT APPEAR IN THIS DIAGRAM BECAUSE THEY DO    ║
  ║  NOT EXIST IN THIS CONTEXT.  INV-BU-1 IS AN ABSENCE, NOT A CHECK.║
  ╚══════════════════════════════════════════════════════════════════╝
```

---

## 16.6 Domain Services

| Service | Responsibility | Notes |
|---|---|---|
| **`VisibilityService`** | Resolve what an Organisation and each role may see | **The central service of this context.** It can only return `BusinessLineId`s — the type system makes a personal line unreturnable |
| `RoutingResolutionService` | Resolve a call to a target given hours, rules and fallback | Executed at call time; the decision is passed to Telephony |
| `RoutingValidationService` | Detect unreachable rules, gaps in hours, and broken targets | Rejects at save with an explanation |
| **`RoutingPreviewService`** | Render a `RoutingPolicy` as prose | Generated from the policy, so it cannot drift |
| `InvitationBindingService` | Bind an invitation to the invited MSISDN, server-side | The token alone grants nothing |
| `OwnershipTransferService` | Move `OWNER` between memberships with the recipient's acceptance | The organisation is never orphaned |
| `LineProvisioningSaga` | Coordinate a `BusinessLine` and its Telephony `Line` across clusters | Compensations in [§16.13](#1613-state-machines) |

---

## 16.7 Repositories

`OrganisationRepository` · `BusinessLineRepository` ·
`BusinessContactRepository` · `ApiKeyRepository` · `WebhookEndpointRepository` ·
`VerifiedBusinessIdentityRepository`

---

## 16.8 Domain Events

| Event | Payload | Consumers |
|---|---|---|
| `business.organisation.created.v1` | organisationId, timezone | Billing, Analytics |
| `business.organisation.deleted.v1` | organisationId, lineCount, memberCount | Telephony, Billing, Notifications |
| `business.membership.invited.v1` | organisationId, invitationId, role | Notifications |
| `business.membership.accepted.v1` | organisationId, membershipId, role, consentRef | Consumer, Notifications |
| `business.membership.role_changed.v1` | membershipId, from, to | Identity, Notifications, Administration |
| `business.membership.removed.v1` | membershipId, removedBy | Consumer, Notifications, Telephony |
| `business.line.added.v1` | businessLineId, lineId, method | Telephony, Billing |
| `business.line.removed.v1` | businessLineId, lineId | Telephony, Billing |
| `business.routing.changed.v1` | businessLineId, version, ruleCount, hasFallback | Telephony, Administration |
| `business.routing.target_broken.v1` | businessLineId, targetType | **Notifications**, Administration |
| `business.api_key.created.v1` | keyId, prefix, scopes | Administration |
| `business.api_key.revoked.v1` | keyId, revokedBy | Administration |
| `business.webhook.disabled.v1` | endpointId, failureHours | **Notifications** |
| `business.identity.verified.v1` | verifiedBusinessId, numbers[] | Consumer, Fraud |
| `business.identity.revoked.v1` | verifiedBusinessId | **Consumer, Fraud** — propagates immediately |

**No event carries a member's MSISDN, a contact's number, a contact note, or a
key.** `business.membership.*` events carry membership identifiers; a consumer
needing the person resolves them through Identity.

---

## 16.9 Commands

| Command | Refused when |
|---|---|
| `CreateOrganisation(name, timezone)` | — |
| `InviteMember(orgId, msisdn, role, lines?)` | Number already an active member; `BILLING` role with lines assigned |
| `AcceptInvitation(invitationId, subscriberId)` | **Subscriber's MSISDN differs from `invitedMsisdn`**; expired; revoked |
| `ChangeRole(membershipId, role)` | Would remove the last `OWNER` |
| `RemoveMember(membershipId)` | Would remove the last `OWNER` |
| `AddBusinessLine(orgId, method, …)` | Number ownership unverified; saga step fails → compensated |
| `ConfigureRouting(businessLineId, rules, fallback)` | **No fallback**; a rule can never fire; a target is unresolvable |
| `SetBusinessHours(businessLineId, schedule)` | Malformed; gaps are legal and named, not refused |
| `CreateApiKey(orgId, scopes)` | Scope unrecognised |
| `RevokeApiKey(keyId)` | — |
| `RegisterWebhook(orgId, url, scopes, includeContent)` | Non-HTTPS; `includeContent` without the naming confirmation |
| `TransferOwnership(fromMembershipId, toMembershipId)` | Recipient has not accepted |
| `DeleteOrganisation(orgId, typedName)` | Typed name mismatch |

---

## 16.10 Queries

| Query | Scope |
|---|---|
| `GetOrganisation(orgId)` | Members, role-filtered |
| `ListMembers(orgId)` | `OWNER`, `ADMIN` |
| `GetRoutingPolicy(businessLineId)` | `OWNER`, `ADMIN`; read-only for `MEMBER` |
| **`PreviewRouting(policyDraft)`** | Returns prose, not a diagram |
| `ListBusinessCalls(orgId, filter)` | **Assigned `BusinessLine`s only.** A `MEMBER` sees only their lines |
| `GetContact(contactId)` | Members with contact scope |
| `GetUsage(orgId, period)` | `OWNER`, `BILLING` |
| `GetVerifiedIdentity(number)` | Internal; consumed by Consumer for `CallerIdentity` |

**No query in this context can return a personal `Line`, a personal
`CallSession`, or a personal `Transcript`.** Not filtered out — not expressible.

---

## 16.11 Policies

| # | Policy |
|---|---|
| **P-BU-1** | A member's personal-line data is never within any Organisation's visibility scope, under any role, by any configuration |
| **P-BU-2** | An Organisation always has exactly one `OWNER`. Removing the last one is refused until ownership transfers |
| **P-BU-3** | An `Invitation` is bound to `invitedMsisdn` server-side. The token alone grants nothing |
| **P-BU-4** | A `RoutingRule` that can never fire is rejected at save, with an explanation |
| **P-BU-5** | A caller's request to be transferred is **untrusted input** and never alters routing (I4) |
| **P-BU-6** | A `WebhookEndpoint` failing for 24 hours is disabled and the Organisation notified |
| **P-BU-7** | When a routing target becomes invalid, fall back, raise an alert, and **show the broken target** rather than pretending it resolved |
| **P-BU-8** | When a member is removed, their personal call history is untouched, and the confirmation says so. Call records they handled remain with the Organisation |
| **P-BU-9** | When a `VerifiedBusinessIdentity` is revoked, stop rendering it as verified immediately |
| **P-BU-10** | A routing change states its effective time before applying; the previous version stays live until the new one saves |

---

## 16.12 Invariants

| # | Invariant | Source |
|---|---|---|
| **INV-BU-1** | Personal `Line`s are not addressable objects in this context. `VisibilityBoundary` cannot express one | Principle boundary |
| **INV-BU-2** | Exactly one `OWNER` per `Organisation`, at all times | |
| **INV-BU-3** | A `BusinessLine` references a Telephony `Line` owned by the Organisation, or by a member who consented | |
| **INV-BU-4** | A `RoutingPolicy` always has a terminal `fallback` | |
| **INV-BU-5** | An `ApiKey`'s plaintext exists only in the creation response | |
| **INV-BU-6** | The `BILLING` role can never read call content | |
| **INV-BU-7** | `VerifiedBusinessIdentity.displayName` originates from a registry, never from caller speech | **U10** |
| **INV-BU-8** | A `Membership` references the `ConsentRecord` under which organisational visibility was agreed | ADR-0012 |
| **INV-BU-9** | Routing follows configuration only. No caller input reaches a routing decision | **I4** |
| **INV-BU-10** | An `Organisation` is never orphaned; deletion and ownership transfer are the only exits | |

---

## 16.13 State Machines

### `Invitation`

```
  ISSUED ──accepted by the invited MSISDN──▶ ACCEPTED «terminal»
    │  │
    │  ├──declined──▶ DECLINED «terminal»
    │  ├──revoked───▶ REVOKED  «terminal»
    │  └──WRONG MSISDN presents the token──▶ refused, state UNCHANGED
    └──ttl elapses──▶ EXPIRED «terminal»
```

A wrong-number presentation **does not** consume or invalidate the invitation —
it is refused, and the legitimate invitee can still accept.

### `Membership`

```
  INVITED ──accept──▶ ACTIVE ──role change──▶ ACTIVE (session re-scoped, visibly)
                        │
                     remove
                        ▼
                    REMOVED «terminal, soft»
              record persists so historical call
              attribution stays intact
```

### `WebhookEndpoint`

```
  ACTIVE ──delivery fails──▶ FAILING ──24 h consecutive──▶ DISABLED
    ▲                          │                            │
    └──── delivery succeeds ───┘                    manual re-enable
                                                            │
                                                            ▼
                                                         ACTIVE
```

### The `AddBusinessLine` saga

Business spans two persistence clusters ([00 §0.7](00-strategic-design.md)), so
this cannot be a transaction.

```
  1. Reserve BusinessLine (identity cluster)     ──fail──▶ abort, nothing created
              │
  2. Provision Telephony Line + DID              ──fail──▶ COMPENSATE 1:
     (telephony cluster)                                   release the reservation
              │
  3. Provision carrier forwarding                ──fail──▶ COMPENSATE 2:
              │                                            reclaim DID, delete Line
              │                                            COMPENSATE 1
  4. Verify forwarding                           ──fail──▶ line exists but NOT LIVE.
              │                                            Surfaced as "setup
              │                                            incomplete", never as
              │                                            silently broken.
  5. Activate RoutingPolicy                      ──fail──▶ line NOT LIVE, same
              │                                            treatment
              ▼
         BusinessLine LIVE
```

**A half-configured number is never put into service.** Step 4 or 5 failing
leaves a visible, resumable "setup incomplete" state — which is the difference
between a customer who finishes setup tomorrow and a business whose calls
silently go nowhere.

### `Organisation`

```
  ACTIVE ──billing lapse past grace──▶ SUSPENDED ──payment──▶ ACTIVE
     │
  delete (typed confirmation)
     ▼
  DELETING ──retention window──▶ DELETED «terminal»
     │                            numbers released
     └── recoverable during the window
```

---

## 16.14 Ownership

| Aspect | Value |
|---|---|
| Team | `callscreen/business` |
| Services | `edge-api` (portal), `identity` (membership), `telephony-gateway` (routing execution) |
| Durable store | `identity` Aurora schema `business_org`; `telephony` Aurora schema `business_line` |
| CODEOWNERS | `docs/domain/16-business.md` |

---

## 16.15 External Dependencies

| Dependency | Purpose | Guard |
|---|---|---|
| Telephony context | Line, DID, forwarding, transfer execution | Customer–Supplier. Business decides routing; Telephony executes |
| Identity context | Subscriber resolution, membership consent | Conformist |
| Number portability process | Porting a number in | Regulated, multi-day. The portal **tracks** it and never presents it as instant |
| Business registry *(verification)* | `VerifiedBusinessIdentity` | **ACL.** "Verified" names its source, so the claim is specific |
| Customer CRM systems | Contact export | **ACL.** Outbound only; credentials write-only after creation |

---

## 16.16 Security Constraints

| # | Constraint |
|---|---|
| 1 | **Personal lines are absent from this context**, not filtered. `VisibilityBoundary` cannot express one (INV-BU-1) |
| 2 | **Invitations bind to the invited MSISDN server-side.** An org-joining link that works for whoever holds it is a privilege-escalation primitive |
| 3 | **`BILLING` cannot read call content.** The finance person in a clinic has no business reading a patient's transcript |
| 4 | **API key plaintext exists once.** Keys are hashed at rest |
| 5 | **Webhook payloads carry identifiers by default**, mirroring Invariant I7 at the customer boundary. Content requires a confirmation naming what is transmitted |
| 6 | **Routing never follows caller input** (I4) |
| 7 | **A verified identity's display name comes from a registry**, never from speech (U10) |
| 8 | **Verification revocation propagates immediately** |
| 9 | **Role changes re-scope live sessions visibly**, never as a silent capability change |
| 10 | **Removing a member never touches their personal data**, and the confirmation states it |
| 11 | **Contact notes are `SENSITIVE`** and never rendered to anyone outside the Organisation |
| 12 | **A caller is a data principal too.** Their erasure request reaches `BusinessContact` records about them |
