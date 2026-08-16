# Business Portal · 2 · Screens

---

## B01 · Sign in · B02 · Organisation selection · B04 · Accept invitation

**Purpose** — Authenticate a business user against the same identity primitive
as the Android app.

**Inputs** — MSISDN; OTP; device trust; organisation memberships; pending
invitation token (server-held).

**Outputs** — Session scoped to one organisation.

**Components** — `PhoneField` · OTP field · `ListItem` (org selection) ·
`Button`.

**Edge cases** — User belongs to no organisation → `B03` create, or a message
that they need an invitation · exactly one organisation → `B02` is skipped ·
invitation for a different number than the one signing in → refused with a clear
statement; **the invitation is bound to the invited number**, not to whoever
holds the link · invitation expired → request a new one from the named
administrator · user is also a personal subscriber → same identity, and the
portal makes no reference to their personal data.

**Accessibility** — Same OTP conventions as the Android app: one field, six
characters, announced as a code, auto-submit announced before it happens.

**Loading** — Inline button loading.

**Empty** — `B02` with no memberships → first-run empty with `Create an
organisation`.

**Error** — Inline for wrong/expired codes; rate limits stated with a real
countdown.

**Success** — Land on `B10`, or on the preserved deep-link target.

**Security** — **No password.** MSISDN + OTP + device trust, matching ADR-0010.
Session 12 hours, idle timeout 60 minutes. **Invitations are bound to the
invited number server-side and are never carried in the link** — a link that
works for whoever holds it is a privilege-escalation primitive.

**Analytics** — `portal.auth.signin_succeeded` (`org_count`) ·
`portal.auth.invitation_accepted` · `portal.auth.invitation_rejected`
(`reason`).

---

## B03 · Create organisation · B05 · Onboarding — first number

**Purpose** — Get a business from signup to a working screened business line.

**B03 inputs** — Organisation name; timezone; industry (optional, used only to
seed sensible defaults).

**B05 flow** — Add a number → verify ownership or provision a new one →
business hours → what the assistant should say → who gets the calls → test call.

**Edge cases** — The business already has a number they want screened → the same
conditional-forwarding flow as the Android app, with the same honest disclosure
about possible carrier charges · they want a new number → provisioned from the
DID pool, with regional availability stated · porting an existing number →
initiated and tracked as a multi-day process, never presented as instant · they
stop midway → the organisation exists with no active number, and the dashboard's
first-run state resumes the flow.

**Accessibility** — Linear, step-counted ("3 of 6"), each step one decision.

**Loading** — Provisioning is 2–10 s with descriptive text.

**Empty** — Not applicable.

**Error** — Provisioning failure is specific: no numbers available in that
region, or verification failed, each with its own next step.

**Success** — A test call, exactly as in the Android app (`A13`), and for the
same reason: it converts an abstraction into something that just happened.

**Security** — Number ownership verification is required before screening any
number the business claims. Screening a number you do not own is an
interception.

**Analytics** — `portal.onboarding.org_created` (`timezone`) ·
`portal.onboarding.number_added` (`method`) ·
`portal.onboarding.completed` (`elapsed_s`, `steps_skipped`).

---

## B10 · Dashboard

```
┌────────────────────────────────────────────────────────────────────┐
│ Dashboard                          Sunrise Clinic · Today          │
├────────────────────────────────────────────────────────────────────┤
│ ⚠ +91 80 4700 1290 isn't forwarding. Calls to it are ringing       │
│   unanswered.                                            Fix it →  │
├────────────────────────────────────────────────────────────────────┤
│ ┌────────────┬────────────┬────────────┬──────────────────────┐   │
│ │ 42         │ 31         │ 6          │ 5                    │   │
│ │ calls      │ screened   │ transferred│ flagged              │   │
│ └────────────┴────────────┴────────────┴──────────────────────┘   │
│                                                                    │
│ Numbers                                                            │
│ ┌────────────────────────────────────────────────────────────────┐│
│ │ Reception   +91 80 4700 1234   ● active    18 calls  9–6       ││
│ │ Bookings    +91 80 4700 1256   ● active    21 calls  24h       ││
│ │ Emergency   +91 80 4700 1290   ▲ not forwarding  3 calls       ││
│ └────────────────────────────────────────────────────────────────┘│
│                                                                    │
│ Recent flagged                          Usage                      │
│ ⚠ Unknown · asked for staff details     412 / 500 minutes         │
│ ⚠ Unknown · claimed to be a supplier    ▓▓▓▓▓▓▓▓░░  82%           │
└────────────────────────────────────────────────────────────────────┘
```

**Purpose** — Answer "is everything working, and what happened today" in one
screen.

**Inputs** — Today's call counts by outcome; per-number health and volume; usage
against plan; recent flagged calls; open issues.

**Outputs** — Navigate to `B20`, `B30`, `B31`, `B35`, `B80`.

**Components** — Banner · stat tiles · `DataTable` (numbers) · `ListItem`
(flagged) · progress (usage).

**Animation** — Values render resolved. No count-up.

**Edge cases** — A number not forwarding → **banner at the top, above
everything**, naming the number. This is the business-surface analogue of
Invariant U2 and it is the highest-severity state on this screen · usage above
80% → inline warning with the specific consequence and the upgrade path · usage
exceeded → calls ring through unscreened, stated plainly, with what to do ·
first day, no calls → first-run empty explaining what will appear · `billing`
role → sees usage and billing tiles only, no call content.

**Accessibility** — The banner is the first focusable element and is announced
`assertive` once. Stat tiles announce value then label. The numbers table is a
real table.

**Loading** — Skeleton per widget; page frame immediate.

**Empty** — First-run: "No calls yet" / "Calls to your numbers will appear here
once someone rings."

**Error** — Per widget, contextual. One failing widget never blocks the page.

**Success** — Not applicable.

**Security** — Business-line data only. **No personal-line data exists in this
query, at any role.**

**Analytics** — `portal.dashboard.viewed` (`role`, `number_count`) ·
`portal.dashboard.health_banner_shown` (`number_count_unhealthy`).

---

## B20 · Call insights · B21 · Call detail

**Purpose** — The organisation's record of what its assistant did.

**Inputs** — Calls across assigned numbers, with caller, outcome, verdict,
duration, transfer target, member handling, and transcript.

**Outputs** — Open `B21` · filter · save a view · export · add to CRM · block.

**Components** — `DataTable` / `CallerCard` list · filter chips ·
`SearchField` · `Transcript` · `CallTimeline` · `RiskIndicator` · `AiBadge`.

**B21** is `A22`'s composition with organisational additions: which number, which
member it was transferred to, whether it was answered, and CRM linkage.

**Edge cases** — `member` role → sees only lines they are assigned to; the filter
does not offer others, and the absence is explained once rather than presented as
an empty result · call transferred and unanswered → surfaced prominently, because
an unanswered transfer is a missed customer and is the metric a business actually
cares about · caller is a known contact → CRM link shown inline · transcript past
retention → stated as policy, with the organisation's own retention setting
linked · call spans a plan-limit boundary → screened calls before the limit,
unscreened after, and the record says which.

**Accessibility** — Table with headers, keyboard navigation, `j`/`k`. Transcript
at `body.lg`, selectable. Verdicts announce level and confidence.

**Loading** — Skeleton rows.

**Empty** — First-run, Recurring, Filtered tiers as appropriate. `member` with
no assigned lines → Gated: "You're not assigned to any numbers yet. Ask
<admin name>."

**Error** — Contextual over the table; cached rows remain.

**Success** — Not applicable.

**Security** — **Business lines only.** Export is audited to `B92`. Caller
content is quoted and attributed. Every model-generated summary carries an
`AiBadge`.

**Analytics** — `portal.calls.list_viewed` (`filter`, `row_count`) ·
`portal.calls.detail_viewed` (`had_transfer`, `verdict_level`) ·
`portal.calls.exported` (`row_count`) · `portal.calls.view_saved`.

---

## B30 · Numbers · B31–B35 · Number configuration

**Purpose** — The operational core. Everything a business's phone behaviour
depends on.

**B31 sections** — Identity (label, number, region) · Health (forwarding, same
model as `A52`) · Assignment (which members) · Hours · Routing · Assistant ·
Recording and retention.

**B34 Routing** is the screen where a mistake costs the business real calls:

```
  When the assistant finishes
    ├── transfer to  →  [ member · team · another number · voicemail ]
    ├── if no answer in [ 20 s ]  →  [ fallback ]
    └── outside hours  →  [ message · voicemail · another number ]

  ┌──────────────────────────────────────────────────────────────┐
  │ What this means                                              │
  │ Callers between 9:00 and 18:00 will be transferred to        │
  │ Dr Nair. If she doesn't answer in 20 seconds, the assistant  │
  │ takes a message. Outside those hours, callers hear your      │
  │ after-hours message and can leave one.                       │
  └──────────────────────────────────────────────────────────────┘
                                          ← rendered from the actual
                                            configuration, before saving
```

**The plain-language preview is mandatory.** Routing rules are read as a
diagram and understood as a sentence, and a business owner configuring a
telephone tree at 9 pm needs the sentence.

**Edge cases** — Routing that can never fire (a rule after a catch-all) →
flagged at save with an explanation, not silently accepted · transfer target
removed from the team → the number falls back, an alert is raised, and the
routing screen shows the broken target rather than pretending it resolved ·
hours that leave a gap → the gap is named ("nothing is configured between 22:00
and 09:00; callers will hear the after-hours message") · a change during
business hours → the preview states when it takes effect, and it is immediate
unless scheduled · deleting a number → confirm by typing the number. It is a
working phone line.

**Accessibility** — Every rule is a form row with a label, not a drag-and-drop
canvas. The preview is a paragraph, read by screen readers as prose.

**Loading** — Optimistic for toggles; explicit save for routing, because a
half-applied routing rule is a business outage.

**Empty** — `B30` first-run → `B32` Add a number.

**Error** — Save failures are specific and non-destructive: the previous routing
stays live.

**Success** — Saved state with the effective time stated. No toast for a change
the preview already described.

**Security** — Only `owner` and `admin`. Every change is audited to `B92` with
before and after. Forwarding provisioning follows the same client-side MMI
construction rule as the Android app where the business is using their own
number.

**Analytics** — `portal.numbers.viewed` · `portal.numbers.routing_changed`
(`rule_count`, `has_fallback`) · `portal.numbers.hours_changed` ·
`portal.numbers.deleted` (`had_calls`) ·
`portal.numbers.forwarding_unhealthy_shown`.

---

## B40 · Team · B41 · Member detail · B42 · Invite

**Purpose** — Control who can see and do what.

**Inputs** — Members with role, assigned lines, last active, invitation state.

**Outputs** — Invite · change role · assign lines · remove.

**Components** — `DataTable` · `Dropdown` (role) · multi-select (lines) ·
`Dialog` (invite) · `Dialog` (remove).

**Edge cases** — Removing the last `owner` → refused, with the ownership
transfer path (`B93`) offered · a member belonging to another organisation →
irrelevant and not shown; cross-org membership is not the portal's business ·
inviting a number that already declined → permitted once more, then rate-limited
· changing a role while that member is signed in → effective immediately, and
their session re-scopes with a visible notice rather than a silent capability
change · removing a member → **their personal call history is untouched**, and
the confirmation dialog says so, because an administrator should not believe
they are deleting a person's data.

**Accessibility** — Role changes announce the new capability set, not just the
role name.

**Loading** — Optimistic for role changes with animated revert.

**Empty** — First-run: "You're the only member" / "Invite people to give them
access to calls and numbers."

**Error** — Inline for invalid numbers; transient for send failures.

**Success** — Invitation sent, with its expiry stated.

**Security** — Invitations are bound to the invited number server-side.
Role changes are audited. **No role grants access to a member's personal line**,
and the role picker states this next to every role.

**Analytics** — `portal.team.invited` (`role`) · `portal.team.role_changed`
(`from`, `to`) · `portal.team.removed` · `portal.team.lines_assigned` (`count`).

---

## B50–B52 · CRM

**Purpose** — Turn screened calls into a usable contact record, without becoming
a CRM.

**Inputs** — Callers, deduplicated by number, with call history, verdicts,
notes, tags and linkage to a real CRM where integrated.

**Outputs** — Notes, tags, export, integration sync.

**Scope discipline** — This is a contact list with call history attached. There
are no deals, no pipelines, no tasks, no email. Businesses that need those
already have a CRM, and `B52` exports to it. **Building half a CRM is how this
product loses focus**, and the boundary is recorded here so it is not re-argued.

**Edge cases** — Contact matches a member's personal contact → not linked; the
portal has no visibility of personal contacts · a caller blocked by one number
but not another → shown per-number, not merged into a single misleading state ·
integration credentials expire → the affected contacts are marked as unsynced
with a reconnect path, not silently stale · a contact requests erasure → handled
through `B91`, and the record says the caller's rights apply here too.

**Accessibility** — Tables with headers, keyboard navigation, inline editing
with explicit save.

**Loading** — Skeleton rows.

**Empty** — First-run: "No contacts yet" / "People who call your numbers appear
here."

**Error** — Contextual; local edits are preserved.

**Security** — Contacts are caller personal data and are classified as such.
Export is audited. Integration credentials are write-only after creation.

**Analytics** — `portal.crm.contact_viewed` · `portal.crm.note_added` ·
`portal.crm.integration_connected` (`provider`) ·
`portal.crm.sync_failed` (`provider`, `reason`).

---

## B60 · Analytics · B61 · Usage reports

**Purpose** — Show a business what its calls are doing, and what it is paying
for.

**Dashboards** — Volume and outcomes over time · answered vs. missed vs.
transferred · response and handling times · spam and fraud caught · per-number
and per-member breakdowns · usage against plan.

**Shared contract**

- **Components** — `Chart` (sparkline, bar, donut only, ≤ 5 series) ·
  `DataTable` · `Dropdown` (range, segment).
- **Animation** — Draw-in on first render only.
- **Edge cases** — Sparse data → "not enough data", never a misleading line ·
  a range spanning a plan change → annotated · per-member breakdown → available
  only to `owner` and `admin`, and it shows **call handling, not member
  behaviour**. This is deliberate: a screen that reads as employee surveillance
  will be used as one, and we are not building that.
- **Accessibility** — Every chart has a data-table equivalent behind a control.
  Never colour-only.
- **Loading / Empty / Error** — Per widget.
- **Security** — Business lines only. Exports audited.
- **Analytics** — `portal.analytics.viewed` (`dashboard`, `range`) ·
  `portal.analytics.exported` (`row_count`).

**B61 Usage reports** are the billing-facing view: minutes screened, calls
handled, per number, per period, downloadable, reconcilable against the invoice.
**A usage number the customer cannot reconcile against their invoice is a
support ticket waiting to happen**, and this screen exists to prevent it.

---

## B70 · API keys · B71 · Create key · B72 · Webhooks · B73 · Delivery logs

**Purpose** — Let a business integrate call events into their own systems.

**B71 contract** — The key is displayed **exactly once**, at creation, with a
copy action and an explicit acknowledgement before the dialog closes. Thereafter
only a prefix and a creation date are shown.

**Edge cases** — User closes the dialog without copying → the key is
unrecoverable and must be rotated; the dialog warns before closing · key scoped
too broadly → scopes are per-capability, default minimal, and widening one is a
separate deliberate action · webhook endpoint failing → delivery logs show
attempts and backoff, and after 24 hours of failure the endpoint is
**disabled with a notification**, not retried forever · webhook payload
containing PII → payloads carry identifiers, and content requires an explicit
per-webhook opt-in that names what will be transmitted.

**Accessibility** — The one-time reveal is announced, and the copy action is
keyboard-reachable and confirms.

**Loading** — Key generation is instant; do not show a spinner.

**Empty** — First-run: "No API keys" / "Create a key to send call events to your
own systems."

**Error** — Inline.

**Success** — The reveal dialog is the success state.

**Security** — **A key retrievable later is a key stored retrievably.** Keys are
hashed at rest. Webhook payloads default to identifiers only (mirroring
Invariant I7). Every key creation, rotation and revocation is audited. Endpoints
must be HTTPS.

**Analytics** — `portal.developers.key_created` (`scopes`) ·
`portal.developers.key_revoked` · `portal.developers.webhook_disabled`
(`failure_hours`).

---

## B80–B83 · Billing

**Purpose** — Take money without being unpleasant about it.

**Structure** — Overview (current period usage and cost) · Plan and subscription
· Invoices · Payment methods.

**Edge cases** — Payment fails → grace period stated **in days**, with a
feature-by-feature list of what stops and when. Calls keep being screened during
grace, because cutting a business's phone screening over a failed card is
disproportionate · usage exceeds plan → overage stated per unit, with the
upgrade comparison inline · downgrade below current usage → what will stop
working, named, before confirming · cancellation → what stops, when, what
happens to the data, and how to export it first. **One retention offer maximum**
· GST and invoicing details required for Indian businesses → collected once and
present on every invoice.

**Accessibility** — Amounts announced with currency and period in full. Invoice
table is a real table with a downloadable equivalent.

**Loading** — Provider-hosted payment fields load in place; the page frame does
not block on them.

**Empty** — No invoices yet → Recurring tier.

**Error** — Payment errors are provider-specific and actionable, never a code.

**Success** — Plan change confirmed with the effective date and the prorated
amount stated.

**Security** — **Payment instrument data never touches our surfaces.**
Provider-hosted fields throughout. `billing` role cannot read call content.

**Analytics** — `portal.billing.plan_changed` (`from`, `to`, `direction`) ·
`portal.billing.payment_failed` (`reason`) ·
`portal.billing.cancelled` (`tenure_days`, `reason_given`) ·
`portal.billing.overage_shown`.

---

## B90–B93 · Settings, privacy, audit, danger zone

**B90 Organisation** — Name, timezone, address, GST details, branding for the
assistant's identification of the business.

**B91 Data and privacy** — Retention per number, organisation-wide export,
erasure, the Data Processing Agreement, and the statement of what the
organisation can and cannot see. **This screen is where the personal/business
boundary is stated to the administrator**, in the same words the member sees in
the Android app (`A56`). Both sides reading the same sentence is what makes it
a boundary rather than a claim.

**B92 Audit log** — Every administrative action: role changes, routing changes,
number additions and deletions, exports, key creation, billing changes.
Append-only, searchable, exportable. **No role can delete an entry, including
`owner`.**

**B93 Danger zone** — Transfer ownership; delete the organisation.

**Edge cases** — Deleting the organisation → itemised consequences with counts,
what happens to each number (released or returned to the business), what happens
to members (their personal accounts are untouched, and this is stated), a data
export offered first, and confirmation by typing the organisation name · sole
owner leaving → refused until ownership is transferred; **the organisation is
never orphaned** · transfer of ownership → the recipient must accept, and both
parties are notified.

**Accessibility** — Danger zone actions are `Danger` variant, never the default
focus, and each states its consequence in the button's own accessible
description.

**Security** — Deletion is transactional. Numbers are released only after the
retention period, so a business that deletes an organisation by mistake can
recover its numbers within a stated window.

**Analytics** — `portal.settings.retention_changed` (`days`) ·
`portal.settings.export_requested` · `portal.settings.ownership_transferred` ·
`portal.settings.org_deleted` (`number_count`, `member_count`).

---

## B99 · Access denied

**Gated empty state, not an error**
([`01 §1.2`](../01-cross-surface-conventions.md)). Names the role required and
the specific person to ask, drawn from the team list.

**Copy** — "You need the Admin role to change routing. Ask Dr Nair or
S. Kulkarni."

**Analytics** — `portal.access.denied_shown` (`resource`, `role_required`).
