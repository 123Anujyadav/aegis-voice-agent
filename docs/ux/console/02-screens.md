# Console · 2 · Screens

Full contracts. Wireframes where composition is load-bearing; prose where the
frozen component catalogue already determines the layout.

---

## C01 · Sign in

**Purpose** — Authenticate an operator to an internal tool that can see
subscriber data.

**Inputs** — SSO identity; hardware security key; device posture.

**Outputs** — Session with role claims; audit entry.

**Components** — Centred `Card` · `Button` (Primary) · environment indicator.

**Animation** — None. A sign-in screen with motion is a sign-in screen that
takes longer.

**Edge cases** — No hardware key enrolled → enrolment flow, no fallback to a
weaker factor · role revoked since last session → `C99` with who to contact ·
session expired mid-investigation → re-auth **in place**, preserving context
([`01 §1.8`](../01-cross-surface-conventions.md)) · signing into `prod` from an
unrecognised device → additional approval.

**Accessibility** — Single focusable action. Errors announced `assertive`.

**Loading** — Inline button loading.

**Empty** — Not applicable.

**Error** — Blocking, generic. "Couldn't sign you in." No enumeration of which
factor failed.

**Success** — Land on `C02`, or on the deep-link target if one was preserved
server-side.

**Security** — **SSO plus hardware key, no password, no SMS fallback.** Session
lifetime 8 hours, idle timeout 30 minutes. Every sign-in writes an audit entry
with device and location.

**Analytics** — `console.auth.signin_succeeded` (`role`, `device_known`) ·
`console.auth.signin_failed` (`stage`).

---

## C02 · Overview — live operations

```
┌────────────────────────────────────────────────────────────────────┐
│ Overview                                        prod · 14:22 IST   │
├────────────────────────────────────────────────────────────────────┤
│ ┌──────────────┬──────────────┬──────────────┬──────────────────┐ │
│ │ 1,284        │ 912 ms       │ 1,410 ms     │ 2 open           │ │
│ │ live sessions│ p50 latency  │ p95 latency  │ incidents        │ │
│ │              │ budget 900   │ budget 1500  │ 1 P2 · 1 P3      │ │
│ └──────────────┴──────────────┴──────────────┴──────────────────┘ │
│                                                                    │
│ Latency by hop                                    last 15 min      │
│ ┌────────────────────────────────────────────────────────────────┐│
│ │ carrier→gw   ▓▓░░░░░░░░  24/25 ms                              ││
│ │ gw→relay     ▓▓░░░░░░░░  11/15 ms                              ││
│ │ ASR          ▓▓▓▓▓▓░░░░ 180/200 ms                             ││
│ │ LLM          ▓▓▓▓▓▓▓▓░░ 410/450 ms                             ││
│ │ TTS          ▓▓▓▓▓░░░░░ 190/210 ms   ← actual / budget         ││
│ └────────────────────────────────────────────────────────────────┘│
│                                                                    │
│ Carrier health                     Admission                       │
│ Jio      ▲ 99.4%                   shed rate     0.0%              │
│ Airtel   ▲ 99.1%                   tier downgrade 0.2%             │
│ Vi       ▼ 96.2%  ⚠                queue depth    12               │
│ BSNL     ▲ 98.8%                                                   │
└────────────────────────────────────────────────────────────────────┘
```

**Purpose** — Tell an operator, in one screen, whether the platform is healthy
against its stated budgets.

**Inputs** — Concurrency; per-hop latency against the ADR-0011 allocation;
carrier answer-success rates; admission and degradation state; open incidents.

**Outputs** — Navigate to `C10`, `C41`, `C43`, `C50`.

**Components** — Stat tiles · `Chart` (bar, ≤ 5 series) · `DataTable` (carrier) ·
`ListItem` (incidents).

**Animation** — Values update in place, no tween, no count-up. Charts draw in on
first render only, never on update ([`09 §Chart`](../../design/09-components.md)).

**Edge cases** — Metrics pipeline lagging → each tile carries its own freshness
timestamp and greys past 60 s. **A stale dashboard shown as live is how an
incident gets missed** · a hop exceeding budget → the bar crosses into
`status.warning`, and the tile is tappable to `C41` · no data (cold environment)
→ Recurring empty per tile, not a broken chart.

**Accessibility** — Every chart has a data-table equivalent, reachable by a
control, not a hover ([`09 §Chart`](../../design/09-components.md)). Tiles
announce value then label then budget. Colour is never the sole carrier of
"over budget" — the number and a label carry it.

**Loading** — Skeleton per tile and per chart, exact sizes. The page frame
renders immediately.

**Empty** — Per-widget Recurring tier.

**Error** — Per-widget contextual. **One failing widget never blocks the page** —
an operator needs the eleven metrics that did load.

**Success** — Not applicable.

**Security** — No subscriber identifiers anywhere on this screen. Aggregates
only.

**Analytics** — `console.overview.viewed` (`role`) ·
`console.overview.widget_failed` (`widget`).

---

## C10 · Sessions — live list · C11 · Session detail — live

**Purpose** — See screening sessions in flight, and inspect one.

**Inputs** — Live session stream: opaque session ID, redacted MSISDN, carrier,
duration, turn count, current tier, verdict state, latency, region.

**Outputs** — Open `C11`; filter; export (audited).

**Components** — `DataTable` (virtualised) · filter chips · `Chart`
(sparkline per row).

**Wireframe — C11**

```
┌────────────────────────────────────────────────────────────────────┐
│ ← Sessions / sess_8f2a…                        ● live · 0:42       │
├────────────────────────────────────────────────────────────────────┤
│ Subscriber  +91 98••• ••210  ⧉        Carrier   Airtel             │
│ DID         +91 80 4700 1234          Region    ap-south-1         │
│ Tier        haiku-4-5 → sonnet-5      Turns     6                  │
│ Verdict     fraud · low (0.41)        Latency   p50 880 ms         │
├────────────────────────────────────────────────────────────────────┤
│ Timeline                                                            │
│  0:00 answered · 0:00 announcement · 0:06 turn · 0:11 turn         │
│  0:19 tier escalated → sonnet-5 · 0:24 verdict fraud/low           │
├────────────────────────────────────────────────────────────────────┤
│ Transcript                                                          │
│ ┌────────────────────────────────────────────────────────────────┐ │
│ │  🔒 Content is hidden.                                          │ │
│ │  6 turns · caller 4 · assistant 2 · 38 s of speech             │ │
│ │                                                                 │ │
│ │  [ Request access ]     Requires a reason. Expires in 30 min.  │ │
│ │                          Logged to the audit trail.            │ │
│ └────────────────────────────────────────────────────────────────┘ │
└────────────────────────────────────────────────────────────────────┘
```

**Edge cases** — Session ends while being viewed → the header changes to
"ended", the view **stays**, and it becomes `C12`. Nothing is yanked away ·
1,000+ concurrent sessions → virtualised, and the live list buffers updates
behind a "12 new" control rather than reordering under the cursor · session
appears with no diversion header → flagged as a **hostile inbound** (Invariant
I10), highlighted, and linked to the toll-fraud runbook · break-glass active →
transcript renders with a persistent unlock bar showing remaining time.

**Accessibility** — Table is a real table with row and column headers. `j`/`k`
navigation. Live updates are announced `polite` as a count, never as content.

**Loading** — Skeleton table rows, exact heights.

**Empty** — Recurring: "No live sessions". Filtered tier when filtered.

**Error** — Contextual banner over the table; last-known rows stay
([`01 §1.1`](../01-cross-surface-conventions.md), stream-idle).

**Security** — **Metadata is visible; content is not.** MSISDN redacted.
Transcript, summary and audio require `C22`. There is no live audio tap and
there never will be (Invariant I9). Session IDs are opaque.

**Analytics** — `console.sessions.list_viewed` (`filter`, `row_count`) ·
`console.sessions.detail_viewed` (`live`) ·
`console.sessions.hostile_inbound_flagged`.

---

## C12 · Session detail — historical

As `C11`, with playback of the timeline and, under break-glass, the transcript.
Audio is available only where the subscriber consented to recording, and playing
it is a **separately audited** action from reading the transcript — they are
different intrusions and are logged as such.

**Edge case** — Session past retention → metadata remains, content section
states the retention policy and deletion date. **This is correct behaviour and
is not an error.**

---

## C20 · Subscriber lookup · C21 · Subscriber detail

**Purpose** — Answer a support ticket without exposing more than the ticket
requires.

**Inputs** — Lookup by MSISDN (hashed server-side), account ID, session ID or
ticket reference.

**Outputs** — `C21`; `C23`; `C22`; `C25`.

**Components** — `SearchField` · `DataTable` · redacted detail panels ·
`Button` · break-glass bar.

**C21 sections** — Account state (redacted) · Plan and billing summary ·
Forwarding health and history · Device list (models and last-seen only, never
identifiers) · Recent sessions (metadata) · Consent state · Open tickets.

**Edge cases** — Lookup returns nothing → **"No subscriber matches"**, with no
indication of whether the number exists as an unregistered one. An admin tool is
an enumeration oracle if it distinguishes those two · subscriber has erased
their account → tombstone: erasure date and audit reference, no data · operator
looks up their own number → permitted and **flagged in the audit log**, because
self-lookup is a known internal-abuse pattern · looking up a number with no
associated ticket → permitted, logged, and surfaced in the operator's own access
review.

**Accessibility** — Redaction is a text state ("hidden"), not a visual blur —
blurred text is not hidden to a screen reader and is a real leak.

**Loading** — Skeleton panels.

**Empty** — Filtered tier for lookup. Gated tier where the role lacks a section.

**Error** — Contextual per panel.

**Security** — Everything on `C21` is redacted by default. **`FLAG_SECURE`
equivalent — screenshot restriction — on any view rendering revealed PII.** No
"log in as". No forwarding modification. Every field reveal is a separate audit
entry, not one entry for the page.

**Analytics** — `console.subscribers.lookup_performed` (`method`,
`had_ticket_reference`) · `console.subscribers.detail_viewed` ·
`console.subscribers.self_lookup` — **alerted on, not merely logged.**

---

## C22 · Break-glass request

The screen Invariant U12 exists for.

```
┌────────────────────────────────────────────────────────────┐
│  Request access to protected data                          │
│                                                            │
│  You're requesting        Transcript · sess_8f2a…          │
│  Subscriber               +91 98••• ••210                  │
│                                                            │
│  Why do you need this?                                     │
│  ┌──────────────────────────────────────────────────────┐ │
│  │                                                      │ │
│  └──────────────────────────────────────────────────────┘ │
│  Minimum 20 characters. Reviewed weekly.                   │
│                                                            │
│  Ticket reference (optional)                               │
│  ┌──────────────────────────────────────────────────────┐ │
│  └──────────────────────────────────────────────────────┘ │
│                                                            │
│  Access expires in    ( 15 min )  ( 30 min )  ( 60 min )  │
│                                                            │
│  This is logged with your name, the reason, and what you   │
│  opened. The subscriber can request this record.           │
│                                                            │
│              [ Cancel ]      [ Request access ]            │
└────────────────────────────────────────────────────────────┘
```

**Purpose** — Make accessing a subscriber's private data a deliberate,
attributable act rather than a click.

**Inputs** — Target resource; requester identity and role; reason; optional
ticket; requested duration.

**Outputs** — A time-boxed grant, or a pending approval.

**Components** — `Dialog` · `TextField` (multiline) · radio (duration) ·
`Button` (Primary, Secondary).

**Edge cases** — Role requires approval (`support` reading a transcript) →
becomes a pending request; a `support_lead` approves in `C91`, and the requester
is notified · reason under 20 characters → inline validation. The friction is
the point · grant expires while the operator is reading → **content is replaced
by the locked state with a re-request control**. Not a modal over content that
stays visible underneath · repeated requests for the same subscriber → permitted,
and surfaced in access review as a pattern · emergency access during a P1 → a
distinct `incident` justification path that self-approves and escalates the
review to immediate.

**Accessibility** — Dialog with a focus trap. The consequence sentence is
announced with the dialog title, not buried at the end.

**Loading** — Inline button loading.

**Empty** — Not applicable.

**Error** — Inline. A failed request grants nothing — fails closed.

**Success** — Content unlocks. **A persistent bar appears in the app frame**
showing what is unlocked and the time remaining, on every screen, until it
expires or is released. An operator must never be unsure whether they are
currently holding elevated access.

**Security** — Audit entry contains: operator, role, resource, reason, ticket,
duration, timestamp, and every field actually revealed. **The subscriber can
request this record** (DPDP), and the dialog says so — which is the strongest
behavioural control on this screen.

**Analytics** — `console.breakglass.requested` (`resource_type`, `duration`,
`had_ticket`) · `console.breakglass.approved` / `_denied` / `_expired` ·
`console.breakglass.released_early` — a healthy signal, and worth tracking as
one.

---

## C23 · Forwarding diagnostics

The most-used support screen, because a lapsed forwarding configuration is the
product's most common failure (ADR-0002 §15).

**Purpose** — Determine, in under a minute, why a subscriber's calls are not
being screened.

**Inputs** — Last interrogation result and history; expected vs. observed DID;
carrier and circle; SIM/subscription; ring delay; recent call attempts to the
DID; carrier matrix notes.

**Outputs** — A diagnosis with a scripted explanation the agent can read to the
subscriber; a runbook link.

**Components** — Status block · `DataTable` (interrogation history) ·
`ListItem` (diagnosis) · copy-to-clipboard script block.

**The diagnosis output is a sentence the agent can say aloud.** Not a status
code. "Their forwarding was cleared on 12 March, probably by a SIM change. They
need to set it up again in the app under Settings → Forwarding." An internal
tool whose output requires interpretation before it can be used has done half a
job.

**Edge cases** — Carrier does not support interrogation → stated, with the
manual test-call procedure · observed DID belongs to us but is a different pool
number → benign, explained, not an alarm · observed DID is unknown → **flagged
as potentially hostile**, escalation path · no data at all → the subscriber may
never have completed setup, and the screen says which step they stopped at.

**Accessibility** — Diagnosis is the accessible heading of the screen.

**Security** — Read-only. The console **cannot** change a subscriber's
forwarding; it is their carrier configuration. The screen states this so agents
stop asking.

**Analytics** — `console.support.forwarding_diagnosed` (`diagnosis_class`,
`carrier`, `time_to_diagnosis_s`) — the support-efficiency metric that matters.

---

## C30 · Fraud queue · C31 · Fraud case detail

**Purpose** — Review flagged screenings at volume, accurately, and feed the
result back into model quality.

**Inputs** — Cases with verdict, confidence, pattern, model tier, subscriber
action (blocked / disputed / ignored), age, assignment.

**Outputs** — Case resolution: confirmed · false positive · needs rule change ·
escalate. Rule proposals.

**Components** — `DataTable` (queue) · case panel · `Transcript` (behind
break-glass) · `RiskIndicator` · `Button` group.

**C31 structure** — Verdict and confidence · pattern · the evidence turns ·
model tier and routing decision · **what the subscriber did** · similar recent
cases · resolution actions.

**"What the subscriber did" is the most valuable field on the screen.** A
verdict the subscriber acted on and a verdict they disputed are entirely
different data points, and putting them side by side is what turns a review
queue into a quality signal.

**Edge cases** — Subscriber disputed the verdict → the case is **prioritised**,
because a dispute is the strongest available precision signal · case older than
transcript retention → resolvable on metadata alone, with the limitation stated
· two analysts open the same case → soft lock with the holder's name, not a hard
lock; a hard lock during a shift change strands cases · bulk resolution →
available only for cases sharing a pattern and a confidence band, capped, and
each still writes its own audit entry.

**Accessibility** — **Flow AF7: complete triage by keyboard alone.** `j`/`k`
navigate, `Enter` opens, number keys resolve, `Esc` returns. Every resolution
action has a keyboard binding shown in its label.

**Loading** — Skeleton rows; case panel skeleton matches its final layout.

**Empty** — Recurring: "Queue clear" / "Cases appear here when a screening
scores above the review threshold."

**Error** — Contextual. A failed resolution does not lose the analyst's notes.

**Success** — Row leaves the queue with a `short` collapse; the next case opens
automatically **only if the analyst is in keyboard mode** — auto-advancing a
mouse user is disorienting.

**Security** — Transcript access is per-case break-glass, with the case
reference as the reason. An analyst **cannot** change what the subscriber was
told; they annotate, and the annotation is attributed.

**Analytics** — `console.fraud.case_opened` (`confidence`, `pattern`,
`subscriber_action`) · `console.fraud.case_resolved` (`resolution`,
`time_to_resolve_s`) · `console.fraud.rule_proposed`.

---

## C40–C44 · Analytics

**Purpose** — Product, latency, cost, carrier and quality dashboards.

**Shared contract**

- **Inputs** — Time range; segmentation (carrier, region, plan, model tier,
  language); comparison period.
- **Outputs** — Drill-through to the underlying list; audited export.
- **Components** — `Chart` (sparkline, bar, donut only) · `DataTable` ·
  `Dropdown` (range and segment).
- **Animation** — Draw-in on first render only, never on update.
- **Edge cases** — Sparse data → the chart states "not enough data", never
  renders a misleading line · a range crossing a schema change → annotated on
  the axis · segment with fewer than 20 subjects → **suppressed**, to prevent
  re-identification from an aggregate.
- **Accessibility** — Every chart has a data-table equivalent behind a control.
  Max 5 series. Never colour-only ([`09 §Chart`](../../design/09-components.md)).
- **Loading** — Skeleton per chart.
- **Empty** — Recurring per widget.
- **Error** — Per widget; the page survives.
- **Security** — **Aggregates only. No row-level subscriber data on any
  analytics screen.** k-anonymity threshold enforced server-side, and the UI
  states when a segment was suppressed rather than showing an empty chart.
- **Analytics** — `console.analytics.viewed` (`dashboard`, `range`, `segment`) ·
  `console.analytics.exported` (`dashboard`, `row_count`).

**C41 Latency** is rendered **against the ADR-0011 per-hop allocation**, not as
raw numbers. A hop at 190 ms means nothing; a hop at 190 ms against a 210 ms
budget means everything.

**C42 Cost** shows per-screened-minute cost decomposed by component (telephony,
ASR, LLM, TTS, egress) and the **pre-filter hit rate**, because ADR-0002 §11
names it the single largest cost lever in the product.

**C43 Carrier health** is the operational form of the carrier matrix — a launch
blocker per ADR-0002 §9. Per-carrier, per-circle: answer success, forwarding
verification rate, MMI acknowledgement format anomalies, ring-to-answer latency.

**C44 Quality** shows verdict-agreement and dispute rates from
`android.protection.verdict_disputed`, segmented by pattern and confidence
band — the product's real-world precision measurement.

---

## C50–C52 · Incidents and runbooks

**Purpose** — Run an incident from the tool that shows the symptoms.

**Inputs** — Alerts; severity; timeline; affected components; linked sessions
and subscribers (redacted); runbook references.

**Outputs** — Status updates; severity changes; runbook execution records.

**Notable behaviour**

- **The runbook is embedded, not linked.** During a P1 nobody opens another tab.
- Timeline entries are appendable by any responder and are attributed.
- A production-affecting action from within an incident **confirms by typing the
  target's name**, even at 3 am, especially at 3 am.
- Incident detail links to the flags and prompts changed in the preceding hour —
  the first question in any incident is "what changed", and the tool answers it
  without being asked.

**Security** — Subscriber references in an incident are redacted like everywhere
else. An incident is not an implicit break-glass grant, except through the
explicit `incident` justification path in `C22`.

**Analytics** — `console.incidents.opened` (`severity`) ·
`console.incidents.runbook_step_completed` · `console.incidents.resolved`
(`duration_s`, `severity`).

---

## C60–C62 · Prompts · C70–C71 · Evals

**Purpose** — Change what the models do, safely, with evidence.

**C60/C61** — Registry of versioned prompts per tier. Detail shows a **diff**
against the live version, the eval results for the candidate, and which services
consume it.

**C62 Rollout** — Percentage rollout with automatic rollback triggers on
accuracy, safety, injection resistance, latency and cost — the six gates from
`ARCHITECTURE_FREEZE.md §9` item 7. **A rollout cannot start without a passing
eval run attached.** The button is not disabled with a tooltip; the screen
states which gate has not been satisfied.

**C70/C71 Evals** — Run list and detail: accuracy, fraud recall, safety,
injection resistance, latency, cost, each against its threshold, with per-case
drill-through.

**Edge cases** — A prompt edit that would remove the announcement or make it
model-generated is **rejected at save** — Invariant I1 is enforced in the tool,
not only in review · disabling thinking on a tool-calling tier is **rejected** —
Invariant I3 · an eval regression on safety or injection **blocks rollout at any
percentage**, with no override in the UI. Overriding it requires a code change
and a review, which is the correct amount of friction.

**Security** — Prompt changes are attributed, versioned and auditable. Eval
fixtures containing real transcript excerpts are access-controlled like
transcripts, because that is what they are.

**Analytics** — `console.ai.prompt_edited` (`tier`, `diff_size`) ·
`console.ai.rollout_started` (`tier`, `percentage`, `eval_run_id`) ·
`console.ai.rollout_rolled_back` (`trigger`) · `console.ai.eval_completed`
(`gates_passed`, `gates_failed`).

---

## C80–C81 · Feature flags

**Purpose** — Change platform behaviour without a deploy, visibly.

**Notable behaviour** — Every flag shows its owner, its expiry date, its current
rollout and **who changed it last**. A flag past its expiry is highlighted;
permanent flags are configuration and are marked as such. Changing a flag in
`prod` confirms by typing the flag key.

**Edge cases** — A flag that would disable fraud scoring or the safety layer
**does not exist as a flag** — Invariant I11 makes those unsheddable, and
representing them as toggleable would be a lie in the shape of a UI · a flag
change during an active incident is annotated onto the incident timeline
automatically.

**Analytics** — `console.flags.changed` (`flag`, `from`, `to`, `environment`) ·
`console.flags.expired_shown` (`count`).

---

## C90–C92 · Admin and audit

**C90 Users and roles** — Grant, revoke, review. **No operator can grant
themselves a role**; a second `admin` approves. Role changes are effective
immediately and notify the affected operator.

**C91 Access requests** — Pending break-glass approvals, with the requester, the
reason and the target. Approving states what will be revealed.

**C92 Audit log** — Every privileged action: sign-ins, break-glass grants and
what was revealed, exports, flag and prompt changes, role changes, support
actions.

| Property | Behaviour |
|---|---|
| Immutability | Append-only. **No role can delete an entry, including `admin`** |
| Searchable by | Operator, subscriber (hashed), resource, action, time range |
| Exportable | Yes, and the export is itself audited |
| Retention | Per the compliance schedule, longer than any operational data |
| Subscriber access | A subscriber can request the record of access to their data (DPDP). This screen is where that request is fulfilled |

**Accessibility** — A real table, sortable, keyboard-navigable, with a stated
row count. Long reasons truncate with an accessible expansion, never a tooltip.

**Security** — This screen is the reason the rest of the surface can be trusted.
Its integrity is a higher priority than its convenience.

**Analytics** — `console.admin.role_granted` (`role`, `approver`) ·
`console.admin.audit_searched` (`filter_type`) ·
`console.admin.audit_exported` (`row_count`).

---

## C99 · Access denied

**Purpose** — Explain a wall the operator was never meant to reach, without
framing a correct system as a broken one.

**Treatment** — **Gated empty state, not an error**
([`01 §1.2`](../01-cross-surface-conventions.md)). It names the role required
and who can grant it, and offers a request action where one exists.

**Copy** — "You need the `fraud_analyst` role to open cases. Ask a workspace
admin — currently P. Nair or R. Menon."

**Analytics** — `console.access.denied_shown` (`resource`, `role_required`) — a
high rate on one resource means the role model is wrong, not that operators are.
