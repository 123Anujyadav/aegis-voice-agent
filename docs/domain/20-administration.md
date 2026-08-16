# 20 · Administration

**Subdomain:** Supporting · **Prefix:** `AD` · **Topic domain:** `admin`

> This context can see subscriber personal data. That capability is the reason
> it exists and the reason it is dangerous. Every model decision here is
> downstream of that sentence.

---

## 20.1 Purpose

Let the people who run the platform operate it accountably — and make every
sight of a subscriber's private data a deliberate, attributable, time-boxed act
the subscriber can later read about.

## 20.2 Responsibilities

**Owns**

- `Operator` and `OperatorRole`
- **`AccessGrant`** — the break-glass mechanism (U12)
- **`AuditEntry`** — append-only, `LEGAL_HOLD`, deletable by nobody
- `FeatureFlag` and its guard rails
- `Incident` and `Runbook`
- `SupportAction` and `AccessReview`
- The **redaction ACL** through which every other context is read

**Does not own**

| Not owned | Owned by |
|---|---|
| Any domain data | The eleven owning contexts. Administration **reads through their published interfaces** |
| Subscriber identity | Identity |
| Prompts and evaluations | AI Orchestration owns the data and its invariants; Administration commands them |
| Detection rules | Fraud Detection |
| The ability to impersonate | **Nobody. It does not exist** (INV-AD-5) |

---

## 20.3 Domain Entities

### `Operator` — aggregate root

```
id            : OperatorId              INTERNAL · LEGAL_HOLD
externalRef   : SsoSubjectRef           INTERNAL · LEGAL_HOLD
displayName   : String                  PERSONAL · LEGAL_HOLD
roles         : OperatorRole[] <owned>
status        : OperatorStatus          PUBLIC   · LEGAL_HOLD
hardwareKeyRef: CredentialRef           SECRET   · STANDARD
enrolledAt    : Instant                 INTERNAL · LEGAL_HOLD
lastActiveAt  : Instant                 PERSONAL · SHORT
```

**Relationships** — References an SSO identity. Referenced by `AccessGrant`,
`AuditEntry`, `FraudCase`, `SupportAction` and `Incident`.

**Lifecycle** — Enrolled by an `admin` with SSO plus a hardware key. Roles
granted and revoked, **never self-granted** (P-AD-2) — a second `admin`
approves. Deactivated on departure; the record persists under `LEGAL_HOLD`
because their past actions must remain attributable.

**Validation Rules** — No hardware key, no session; there is no weaker fallback
factor. An operator cannot grant themselves a role, and cannot approve their own
`AccessGrant` — the two-person rule is enforced at the aggregate, not by process
discipline.

**Privacy Classification** — `displayName` is `PERSONAL` — operators are people
too. Attribution is `LEGAL_HOLD`.

**Audit Requirements** — **Change** on enrolment, role change and deactivation.
Every role change is a two-party record.

---

### `AccessGrant` — aggregate root

The entity Invariant U12 exists for.

```
id            : AccessGrantId            INTERNAL · LEGAL_HOLD
operatorId    : OperatorId <ref>         INTERNAL · LEGAL_HOLD
resourceType  : ProtectedResourceType    PUBLIC   · LEGAL_HOLD
resourceRef   : ResourceId               INTERNAL · LEGAL_HOLD
subjectRef    : SubscriberId <ref>       INTERNAL · LEGAL_HOLD
reason        : BreakGlassReason         SENSITIVE· LEGAL_HOLD
ticketRef     : TicketReference?         INTERNAL · LEGAL_HOLD
justification : JustificationType        PUBLIC   · LEGAL_HOLD
duration      : GrantDuration            PUBLIC   · LEGAL_HOLD
state         : GrantState               PUBLIC   · LEGAL_HOLD
approvedBy    : OperatorId? <ref>        INTERNAL · LEGAL_HOLD
grantedAt     : Instant?                 INTERNAL · LEGAL_HOLD
expiresAt     : Instant?                 INTERNAL · LEGAL_HOLD
releasedAt    : Instant?                 INTERNAL · LEGAL_HOLD
revealedFields: RevealedField[] <owned>
```

**Relationships** — References an `Operator`, the protected resource, and the
**subject** — the subscriber whose data is being revealed. That subject
reference is what makes a DPDP subject-access request fulfillable.

**Lifecycle** — Requested with a typed reason, approved where the role requires
it, granted with a hard expiry, then released early or expired. **On expiry,
content re-locks in place** (P-AD-3) — not a modal over still-visible content,
which would leave the data on screen.

**Validation Rules**

- `reason` is at least 20 characters. **The friction is the feature.**
- `duration` ≤ 60 minutes (INV-AD-3). There is no unlimited grant.
- An operator cannot approve their own request.
- A `support` role reading a transcript requires approval by `support_lead`;
  `fraud_analyst` reading evidence within an assigned case is self-approving but
  fully audited.
- **Every field actually revealed appends a `RevealedField`** (INV-AD-2) — the
  question an access review must answer is "what did they actually see", not
  "which page did they open".

**Privacy Classification** — `reason` is `SENSITIVE` free text authored by an
operator and may reference a subscriber's situation. Everything is `LEGAL_HOLD`
and survives the subscriber's erasure, because the record of access must outlive
the data accessed.

**Audit Requirements** — **Access** level throughout. The grant *is* an audit
artefact, and it writes further entries as it is used.

---

### `AuditEntry` — aggregate root, append-only

```
id            : AuditEntryId          INTERNAL · LEGAL_HOLD
context       : AuditContext          PERSONAL · LEGAL_HOLD   ── frozen VO
action        : AuditAction           PUBLIC   · LEGAL_HOLD
resourceType  : String                PUBLIC   · LEGAL_HOLD
resourceRef   : ResourceId            INTERNAL · LEGAL_HOLD
subjectRef    : SubscriberId?  <ref>  INTERNAL · LEGAL_HOLD
grantRef      : AccessGrantId? <ref>  INTERNAL · LEGAL_HOLD
outcome       : AuditOutcome          PUBLIC   · LEGAL_HOLD
before        : RedactedSnapshot?     SENSITIVE· LEGAL_HOLD
after         : RedactedSnapshot?     SENSITIVE· LEGAL_HOLD
```

**Relationships** — References the acting `Operator` through the frozen
`AuditContext` value object (actor, actor type, timestamp, partially-redacted
source IP, region). References the subject, so a subject-access request can be
answered by query rather than by investigation.

**Lifecycle** — **Append-only. No role can delete an entry, including `admin`**
(INV-AD-1). Retained under `LEGAL_HOLD` longer than any operational data.
Exported only through an audited export.

**Validation Rules** — `before` and `after` snapshots are **redacted** — they
record that a `PERSONAL` field changed and its shape, not its value. An audit log
that stores plaintext personal data is a second copy of the database with weaker
access controls.

**Privacy Classification** — `LEGAL_HOLD` throughout, `PERSONAL` on the actor
context. This is the one store that survives essentially everything.

**Audit Requirements** — The audit log is itself audited: **reads and exports of
the audit log write audit entries.**

---

### `FeatureFlag` — aggregate root

```
id            : FlagId                INTERNAL · STANDARD
key           : FlagKey               PUBLIC   · STANDARD
owner         : TeamHandle            PUBLIC   · STANDARD
description   : String                PUBLIC   · STANDARD
environment   : Environment           PUBLIC   · STANDARD
rollout       : RolloutSpec           PUBLIC   · STANDARD
expiresAt     : Instant?              PUBLIC   · STANDARD
permanent     : Boolean               PUBLIC   · STANDARD
lastChangedBy : OperatorId <ref>      INTERNAL · LEGAL_HOLD
lastChangedAt : Instant               INTERNAL · LEGAL_HOLD
```

**Lifecycle** — Created with an owner and an expiry. A flag past its expiry is
**highlighted as configuration debt**. A `permanent` flag is configuration and is
marked as such, so the two do not blur.

**Validation Rules — enforced at creation**

> **A flag that would disable fraud scoring or the safety layer cannot be
> created** (INV-AD-6). Invariant I11 makes those unsheddable, and representing
> them as toggleable would be a lie in the shape of a UI.

A production change confirms by typing the flag key — not a checkbox, not "are
you sure". A change during an open `Incident` is **automatically annotated onto
its timeline**, because "what changed" is the first question in every incident.

**Privacy Classification** — `PUBLIC` except attribution.

**Audit Requirements** — **Change** level with before and after, always
attributed.

---

### `Incident` — aggregate root

```
id            : IncidentId             INTERNAL · LEGAL_HOLD
severity      : IncidentSeverity       PUBLIC   · LEGAL_HOLD
title         : String                 INTERNAL · LEGAL_HOLD
state         : IncidentState          PUBLIC   · LEGAL_HOLD
timeline      : IncidentEntry[] <owned>
responders    : OperatorId[] <ref>     INTERNAL · LEGAL_HOLD
affectedContexts : ContextName[]       PUBLIC   · LEGAL_HOLD
runbookRefs   : RunbookId[] <ref>      PUBLIC   · LEGAL_HOLD
recentChanges : ChangeReference[]      PUBLIC   · LEGAL_HOLD
openedAt      : Instant                INTERNAL · LEGAL_HOLD
resolvedAt    : Instant?               INTERNAL · LEGAL_HOLD
```

**`recentChanges` is populated automatically** with flag changes, prompt
rollouts and deploys from the preceding hour. The tool answers "what changed"
before anyone asks, because at 3 am nobody should be hunting for it.

**Lifecycle** — Opened by an alert or an operator. Timeline entries are
appendable by any responder and are attributed. Resolved; the timeline **is the
postmortem's first draft**, already timestamped and attributed.

**Validation Rules** — Subscriber references in an incident are redacted like
everywhere else. **An incident is not an implicit `AccessGrant`** — the only
route to content is the explicit `INCIDENT` justification path, which
self-approves but escalates review to same-day rather than weekly.

---

### `SupportAction` · `AccessReview` · `Runbook`

```
SupportAction
  id · operatorId <ref> · subjectRef <ref> · actionType · ticketRef
  amount? (Money) · performedAt         ── all LEGAL_HOLD
  actionType: ISSUE_CREDIT · RESEND_VERIFICATION · CLEAR_DIAGNOSTIC ·
              REGENERATE_EXPORT
  ── NOTABLY ABSENT: any action modifying a subscriber's forwarding,
     consent, blocklist, or assistant configuration.

AccessReview
  id · period · reviewedBy <ref> · grantsReviewed · findings[] · completedAt
  findings: NO_ACTION · COACHING · ROLE_REVOKED · INVESTIGATION_OPENED

Runbook
  id · title · steps[] · linkedAlerts[] · lastVerifiedAt
  ── EMBEDDED in the incident, not linked. Nobody opens a second tab at 3 am.
```

**`SupportAction`'s absences are the specification.** An operator can issue a
credit and resend a verification. They cannot change a subscriber's forwarding
(it is that person's carrier configuration), their consent (it is that person's
legal act), or their blocklist (it is that person's instruction).

---

## 20.4 Value Objects

| Value object | Definition | Notes |
|---|---|---|
| `OperatorRole` | `support` · `support_lead` · `fraud_analyst` · `sre` · `ai_engineer` · `admin` · `auditor` | Capability is role-scoped; a missing capability renders as a **gated** state naming the role and who grants it |
| `BreakGlassReason` | Free text, **≥ 20 characters** | The friction is the feature |
| `GrantDuration` | `MIN_15` · `MIN_30` · `MIN_60` | **No unlimited option exists** |
| `JustificationType` | `SUPPORT_TICKET` · `FRAUD_CASE` · `INCIDENT` · `LEGAL_REQUEST` | `INCIDENT` self-approves and escalates review to same-day |
| `ProtectedResourceType` | `TRANSCRIPT` · `RECORDING` · `SUMMARY` · `MSISDN` · `CONTACT_NOTE` · `REPORT_NOTE` | The `SENSITIVE` surface, enumerated |
| `RevealedField` | resourceRef + fieldName + revealedAt | **One per field, not one per page** |
| `RedactionView` | The default rendering: `+91 98••• ••210`, "6 turns, 38 s of speech, content hidden" | Redaction is a **text state**, never a visual blur — blurred text is not hidden to a screen reader |
| `AuditAction` | `SIGN_IN` · `REVEAL` · `EXPORT` · `ROLE_CHANGE` · `FLAG_CHANGE` · `PROMPT_ROLLOUT` · `SUPPORT_ACTION` · `AUDIT_READ` | |
| `IncidentSeverity` | `P1` · `P2` · `P3` · `P4` | |
| `RolloutSpec` | percentage + targeting + environment | |
| `GrantState` | See [§20.13](#2013-state-machines) | |

---

## 20.5 Aggregates

| Aggregate | Root | Contains |
|---|---|---|
| **Operator** | `Operator` | `OperatorRole[]` |
| **AccessGrant** | `AccessGrant` | `RevealedField[]` |
| **AuditEntry** | `AuditEntry` | — (append-only, never loaded for mutation) |
| **FeatureFlag** | `FeatureFlag` | `RolloutSpec` |
| **Incident** | `Incident` | `IncidentEntry[]` |
| **SupportAction** | `SupportAction` | — |
| **AccessReview** | `AccessReview` | `Finding[]` |

```
┌──────────────────────┐
│  Operator  «root»    │  SSO + HARDWARE KEY. No weaker fallback.
│   roles[]            │  Cannot self-grant a role.
│   hardwareKeyRef     │  Cannot approve own AccessGrant.
└──────────┬───────────┘
           │
           ▼
┌────────────────────────────────────────────────────────┐
│  AccessGrant  «root»            ◀── U12                │
│   reason ≥ 20 chars · duration ≤ 60 min                │
│   subjectRef ──▶ the subscriber whose data is revealed │
│   ┌──────────────────────────────────────────────────┐ │
│   │ RevealedField[]   ONE PER FIELD, NOT PER PAGE    │ │
│   └──────────────────────────────────────────────────┘ │
└──────────┬─────────────────────────────────────────────┘
           │ every use writes
           ▼
┌────────────────────────────────────────────────────────┐
│  AuditEntry  «root»   APPEND-ONLY · LEGAL_HOLD         │
│   AuditContext (frozen VO) · before/after REDACTED     │
│   NO ROLE CAN DELETE ONE, INCLUDING admin              │
│   subjectRef ──▶ makes DPDP subject access a QUERY     │
└────────────────────────────────────────────────────────┘
           ▲
           │ reading the audit log writes an audit entry
           │
┌──────────┴───────────┐  ┌────────────────────────────────┐
│ AccessReview «root»  │  │ FeatureFlag «root»             │
│  weekly · findings   │  │  a flag disabling fraud scoring│
└──────────────────────┘  │  or safety CANNOT BE CREATED   │
                          │  (I11 — INV-AD-6)              │
┌──────────────────────┐  └────────────────────────────────┘
│ Incident «root»      │
│  recentChanges auto- │  ╔═══════════════════════════════════╗
│  populated: "what    │  ║ ABSENT FROM THIS CONTEXT:         ║
│  changed" answered   │  ║  · Impersonation  (INV-AD-5)      ║
│  before it is asked  │  ║  · Global transcript search       ║
│  Runbook EMBEDDED    │  ║    (INV-AD-6b)                    ║
└──────────────────────┘  ║  · Live audio tap  (I9)           ║
                          ║  · Forwarding modification        ║
                          ║  · Audit deletion                 ║
                          ╚═══════════════════════════════════╝
```

---

## 20.6 Domain Services

| Service | Responsibility | Notes |
|---|---|---|
| **`BreakGlassService`** | Issue, approve, expire and release grants | Enforces the reason floor, the duration ceiling, and the two-person rule |
| **`RedactionService`** | The default rendering for every cross-context read | Redaction is the default, not a mode. A view is redacted unless a grant says otherwise |
| **`AuditWriteService`** | Append entries; **expose no delete** | The absence of a delete operation is the control |
| `AccessReviewService` | Assemble the weekly review: grants, self-lookups, no-ticket requests, repeated access, exports, early releases | An unreviewed audit log is a log, not a control |
| `FlagGuardService` | Refuse creation of a flag disabling fraud scoring or the safety layer | I11 enforced at the tool |
| `IncidentContextService` | Populate `recentChanges` automatically | Answers "what changed" before it is asked |
| `SubjectAccessService` | Assemble a subscriber's access record for a DPDP request | The reason `AccessGrant`'s consequence sentence is true |

---

## 20.7 Repositories

`OperatorRepository` · `AccessGrantRepository` · `AuditEntryRepository`
*(append and query only — no delete, no update)* · `FeatureFlagRepository` ·
`IncidentRepository` · `SupportActionRepository` · `AccessReviewRepository`

---

## 20.8 Domain Events

| Event | Payload | Consumers |
|---|---|---|
| `admin.access.requested.v1` | grantId, operatorId, resourceType, justification, hasTicket | Administration, Analytics |
| `admin.access.granted.v1` | grantId, operatorId, duration, approvedBy? | Analytics |
| `admin.access.denied.v1` | grantId, reasonCode | Analytics |
| `admin.access.released.v1` | grantId, heldSeconds | **Analytics** — early release is a healthy signal |
| `admin.access.expired.v1` | grantId, fieldsRevealed | Analytics |
| `admin.operator.role_granted.v1` | operatorId, role, approvedBy | Identity, Analytics |
| `admin.operator.role_revoked.v1` | operatorId, role, reason | Identity |
| `admin.operator.self_lookup.v1` | operatorId | **Alerted, not merely logged** |
| `admin.flag.changed.v1` | flagKey, environment, from, to, operatorId | Every context, Analytics |
| `admin.incident.opened.v1` | incidentId, severity, contexts | Notifications, Analytics |
| `admin.incident.resolved.v1` | incidentId, durationS | Analytics |
| `admin.audit.exported.v1` | rowCount, operatorId, period | **Administration** — the export is itself audited |
| `admin.support_action.performed.v1` | actionType, operatorId, subjectRef | Billing, Analytics |

**No event carries a reason string, a revealed value, or an audit snapshot.**
`admin.access.granted.v1` carries the grant's shape, not its content — the reason
is `SENSITIVE` and stays in the store where it is access-controlled.

---

## 20.9 Commands

| Command | Refused when |
|---|---|
| `EnrolOperator(ssoRef, roles)` | Requester lacks `admin`; no hardware key |
| `GrantRole(operatorId, role)` | **Requester is the target.** Approver is the requester |
| `RevokeRole(operatorId, role)` | — (immediate, notifies the operator) |
| `RequestAccess(resourceType, resourceRef, reason, ticketRef?, duration, justification)` | **Reason under 20 characters.** Duration over 60 minutes |
| `ApproveAccess(grantId)` | **Approver is the requester.** Approver lacks the approving role |
| `ReleaseAccess(grantId)` | — (always permitted; recorded as a positive signal) |
| `RecordRevealedField(grantId, resourceRef, fieldName)` | Grant expired |
| `CreateFeatureFlag(key, owner, expiry, environment)` | **Flag would disable fraud scoring or the safety layer** |
| `ChangeFeatureFlag(flagKey, rollout, typedKey)` | Typed key mismatch in production |
| `OpenIncident(severity, title, contexts)` | — |
| `AppendIncidentEntry(incidentId, note)` | Incident resolved |
| `ResolveIncident(incidentId)` | — |
| `PerformSupportAction(subjectRef, actionType, ticketRef)` | Role lacks capability; action type not in the closed set |
| `ExportAudit(period, filter)` | Requester lacks `admin` or `auditor` |
| `CompleteAccessReview(period, findings)` | — |

**Commands that do not exist**, and their absence is the specification:
`ImpersonateSubscriber` · `DeleteAuditEntry` · `SearchAllTranscripts` ·
`ModifyForwarding` · `ModifyConsent` · `TapLiveAudio` · `EditTranscript` ·
`ChangeVerdict`.

---

## 20.10 Queries

| Query | Scope |
|---|---|
| `LookupSubscriber(identifier)` | **Redacted by default.** Returns an identical result whether or not the subject exists — an admin tool that distinguishes is an enumeration oracle |
| `GetSubscriberDetail(subscriberId)` | Redacted; `PERSONAL` fields require a grant |
| `DiagnoseForwarding(lineId)` | Read-only. Returns the **agent-readable sentence**, not a status code |
| `ListLiveSessions(filter)` | **Metadata only.** Content requires a grant |
| `GetAuditTrail(filter)` | `admin`, `auditor`. **Reading it writes an entry** |
| `GetSubjectAccessRecord(subscriberId)` | DPDP fulfilment |
| `ListAccessGrants(period)` | For the weekly review |
| `GetFlagState(flagKey, environment)` | |
| `GetIncident(incidentId)` | Includes `recentChanges` and the embedded runbook |

---

## 20.11 Policies

| # | Policy |
|---|---|
| **P-AD-1** | `PERSONAL` and `SENSITIVE` data is **redacted by default**. Revealing requires an `AccessGrant` |
| **P-AD-2** | No operator grants themselves a role, and no operator approves their own grant. A second `admin` approves |
| **P-AD-3** | An expired grant **re-locks content in place** — not a modal over content that stays visible |
| **P-AD-4** | A self-lookup is permitted, **alerted**, and always reviewed |
| **P-AD-5** | No role deletes an `AuditEntry` |
| **P-AD-6** | A flag that would disable fraud scoring or the safety layer cannot be created (I11) |
| **P-AD-7** | A subscriber may obtain the record of access to their data, and the request dialog **tells the operator this is true** |
| **P-AD-8** | An incident is not an implicit grant. The `INCIDENT` justification self-approves and escalates review to same-day |
| **P-AD-9** | An operator's session expiring mid-investigation re-authenticates **in place**, preserving context |
| **P-AD-10** | Reading or exporting the audit log writes an audit entry |
| **P-AD-11** | A production-affecting change confirms by typing the target's name — at any hour, especially at 3 am |
| **P-AD-12** | A missing capability renders as a **gated** state naming the role required and who grants it. Never a disabled control, never a hidden one |

### Why P-AD-7's sentence is the strongest control here

The dialog tells the operator: *"This is logged with your name, the reason, and
what you opened. The subscriber can request this record."*

Every technical control on this surface — the reason floor, the time box, the
audit entry, the review — can be worked around by a sufficiently determined
insider. That sentence works on the ordinary case: a tired operator at the end
of a shift, about to reveal more than the ticket required, because it was one
click. It does more behavioural work than the rest of the context combined,
which is why it is a policy and not a piece of copy.

---

## 20.12 Invariants

| # | Invariant | Source |
|---|---|---|
| **INV-AD-1** | `AuditEntry` is append-only and `LEGAL_HOLD`. **No role can delete one, including `admin`** | |
| **INV-AD-2** | Every reveal of a `PERSONAL` or `SENSITIVE` field writes its own `AuditEntry` — per field, not per page | **U12** |
| **INV-AD-3** | An `AccessGrant` has a hard expiry ≤ 60 minutes. No unlimited grant exists | **U12** |
| **INV-AD-4** | Reading a transcript requires a grant scoped to a specific session | **U12** |
| **INV-AD-5** | **There is no impersonation capability in the domain** | |
| **INV-AD-6** | No `FeatureFlag` can disable fraud scoring or the safety layer | **I11** |
| **INV-AD-6b** | **Global transcript search does not exist as a query.** Access is per-session, from a case or a ticket | |
| **INV-AD-7** | An operator cannot grant themselves a role or approve their own grant | |
| **INV-AD-8** | Audit snapshots are redacted. The audit log is never a second copy of personal data | |
| **INV-AD-9** | Administration modifies no subscriber-owned configuration — forwarding, consent, lists, assistant profile | |
| **INV-AD-10** | Redaction is a text state, never a visual blur | Accessibility + security |
| **INV-AD-11** | There is no live audio tap | **I9** |
| **INV-AD-12** | Lookup returns an identical result whether or not the subject exists | |

---

## 20.13 State Machines

### `AccessGrant`

```
   REQUESTED
       │
       ├── reason < 20 chars ──▶ REFUSED AT CONSTRUCTION
       │
       ├── role requires approval ──▶ PENDING_APPROVAL
       │                                   │
       │                          ┌────────┴────────┐
       │                          ▼                 ▼
       │                      approved           denied
       │                          │                 │
       │                          │                 ▼
       └── self-approving ────────┤            DENIED «terminal»
           (fraud_analyst in an   │
            assigned case,        ▼
            or INCIDENT)      GRANTED
                                  │  content unlocks;
                                  │  persistent indicator in the frame
                                  │  showing WHAT and TIME REMAINING
                     ┌────────────┼────────────┬──────────────┐
                     ▼            ▼            ▼              ▼
                 RELEASED     EXPIRED      REVOKED     (grant used —
                 «terminal»   «terminal»   «terminal»   RevealedField
                 healthy      re-locks     by admin     appended each
                 signal       IN PLACE                  time)
```

**The persistent indicator matters.** An operator must never be unsure whether
they are currently holding elevated access — uncertainty is how a grant stays
open across a coffee break.

### `Operator`

```
  ENROLLED ──▶ ACTIVE ⇄ SUSPENDED ──departure──▶ DEACTIVATED «terminal»
                                                  record persists under
                                                  LEGAL_HOLD — past actions
                                                  remain attributable
```

### `FeatureFlag`

```
  (creation) ──FlagGuardService──┬── would disable fraud scoring
                                 │   or the safety layer
                                 ▼
                             REFUSED «terminal»  (I11)

             └── permitted ──▶ ACTIVE ──rollout──▶ ACTIVE
                                 │
                            expiry passes
                                 ▼
                              EXPIRED  ── highlighted as configuration debt,
                                 │        still functional
                              removed
                                 ▼
                             RETIRED «terminal»

  permanent = true ──▶ ACTIVE, marked as CONFIGURATION, no expiry
```

### `Incident`

```
  OPEN ──assign──▶ INVESTIGATING ──mitigate──▶ MITIGATED ──▶ RESOLVED «terminal»
    │                    │                                        │
    │              recentChanges auto-populated             timeline becomes
    │              (flags, prompts, deploys, last hour)     the postmortem's
    │                                                        first draft
    └── severity may change in either direction at any state
```

---

## 20.14 Ownership

| Aspect | Value |
|---|---|
| Team | `callscreen/security` |
| Services | Console backend; `edge-api` (internal surface) |
| Durable store | `identity` Aurora schema `admin`; **a dedicated audit store** with independent retention and independent credentials |
| Ephemeral | Redis — active grants, for fast-path revocation |
| CODEOWNERS | `docs/domain/20-administration.md`, `SECURITY.md` |
| Data owner | `callscreen/security` |

**The audit store has its own credentials and its own retention.** Placing it in
the same schema as the data it audits would let one compromised credential both
read the data and erase the evidence.

---

## 20.15 External Dependencies

| Dependency | Purpose | Guard |
|---|---|---|
| **Corporate SSO** | Operator identity | **ACL.** SSO plus hardware key. **No password, no SMS fallback** — a weaker fallback is the strength of the whole scheme |
| **Hardware security keys** | Second factor | Mandatory. No enrolment, no session |
| Every other bounded context | Read access under redaction | **ACL in both directions**: redaction inbound, command authorisation outbound |
| Alerting and paging | Incident triggers | Existing platform alerting; not modelled here |
| Ticketing system | `ticketRef` correlation | Referenced, never integrated as a source of truth |

---

## 20.16 Security Constraints

| # | Constraint |
|---|---|
| 1 | **Redaction is the default rendering**, not a mode to enable |
| 2 | **Every field reveal is individually audited** — "what did they actually see" is the question a review must answer |
| 3 | **Grants expire in ≤ 60 minutes and re-lock content in place** |
| 4 | **The two-person rule is at the aggregate**, not in process discipline: no self-granted role, no self-approved grant |
| 5 | **No impersonation capability exists in the domain** |
| 6 | **No global transcript search exists.** Access is per-session, from a case or a ticket |
| 7 | **No live audio tap exists** (I9) |
| 8 | **Audit entries cannot be deleted by any role** |
| 9 | **Audit snapshots are redacted**, so the audit log is never a second copy of personal data with weaker controls |
| 10 | **The audit store has independent credentials and retention** |
| 11 | **Self-lookup is alerted**, not merely logged — it is a known internal-abuse pattern |
| 12 | **Lookup is not an enumeration oracle** |
| 13 | **Administration changes no subscriber-owned configuration.** It diagnoses; the subscriber decides |
| 14 | **Views revealing `PERSONAL` data carry screenshot restriction**, deterring the most common exfiltration path |
| 15 | **Reading the audit log is itself audited** |
