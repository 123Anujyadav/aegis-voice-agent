# Android · 7 · Screens — Assistant, Settings, Premium, Business

---

## A40 · Assistant — Ask

The Personal Assistant. Principle 3: this is **not** the agent that answers the
phone, and the surface says so.

```
┌────────────────────────────────────────────────┐
│  Assistant                                     │
│  ┌────────────┬──────────────┐                 │
│  │    Ask     │  Behaviour   │                 │  ← segmented
│  └────────────┴──────────────┘                 │
├────────────────────────────────────────────────┤
│                                                │
│  ⬡ Did the courier call today?                 │  ← user, right-anchored
│                                          you   │     label, not a bubble
│                                                │
│  ⬡ Assistant                                   │
│  Yes — Blue Dart called at 1:10 pm and said    │
│  your delivery is arriving between 4 and 6.    │
│                                                │
│    ┌──────────────────────────────────────┐    │
│    │ Blue Dart · 1:10 pm              →   │    │  ← citation, tappable
│    └──────────────────────────────────────┘    │
│                                                │
│  Searched 90 days of calls                     │  ← tool disclosure
│                                                │
├────────────────────────────────────────────────┤
│  ┌────────────────────────────────────┐  ┌──┐ │
│  │ Ask about your calls               │  │◯ │ │  ← hold to talk
│  └────────────────────────────────────┘  └──┘ │
└────────────────────────────────────────────────┘
```

**Purpose** — Answer questions about the user's own calls, and propose actions
on them.

**Inputs** — Query (text or voice); the user's own call data; conversation
context for the session.

**Outputs** — Streamed answer with citations · proposed actions (`A44`) ·
navigation to `A22`.

**Components** — `VoiceOrb` (interactive) · message list · `AiBadge` ·
citation `Card` · `TextField` · `Snackbar`.

**Animation** — Response streams token by token. Orb per
[`02 §2.1`](../02-interaction-language.md) — real amplitude or static. Citations
enter with the message, not after.

**Edge cases**

| Condition | Behaviour |
|---|---|
| No data matches | "I couldn't find a call about that." Never an invented answer. Offers a search instead |
| Question is out of scope | "I can only answer questions about your calls." One line, no lecture |
| Answer would require a claim with no citation | **Not rendered.** Principle 2 |
| Destructive action requested ("block all of these") | Proposed as a reviewable list (`A44`), never executed inline |
| Consent, payment, or forwarding change requested | Refused with a pointer to the screen. The assistant does not change consents — a conversational interface to a legal control is a bad idea in any product |
| Voice held under 400 ms | Treated as a mis-tap, discarded, hint shown |
| Microphone not granted | Push-to-talk opens `A47`, not a system dialog |
| Offline | Input disabled with the reason; local search offered as the alternative |
| Very long answer | Streams; a `Stop` control replaces the send control while streaming |

**Accessibility** — Assistant messages announce `polite` on completion, not
during streaming. Citations are links with descriptive names, not "tap here".
Push-to-talk is a press-and-hold, and a **tap-to-toggle alternative is provided
for Switch Access**, where press-and-hold is not available. Orb has a state
label.

**Loading** — Thinking state with the named work where we know it: "Searching
your calls" ([`02 §2.2`](../02-interaction-language.md)). Never a generic
spinner.

**Empty** — First-run: a short explanation, three example questions as tappable
chips, and the trust line — **"This assistant can see your calls. The assistant
that answers your phone cannot."**

**Error** — Transient with retry; the question stays in the input so it is not
lost.

**Success** — The answer is the success state.

**Security** — Scoped to the authenticated subscriber's own data, enforced
server-side. Conversation history is **ephemeral per session** unless pinned —
there is no permanent chat log to leak. Tool disclosure names the class of tool
consulted, never the reasoning, never the prompt
([`02 §2.2`](../02-interaction-language.md)).

**Analytics** — `android.assistant.query_sent` (`input_mode`, `session_turn`) ·
`android.assistant.answer_received` (`latency_ms`, `citation_count`,
`had_answer`) · `android.assistant.citation_tapped` ·
`android.assistant.action_proposed` (`action_type`, `item_count`).
**No query or answer text.**

---

## A40b · Assistant — Behaviour · A41–A46

Configuration of the **Screening Agent**. The section header says so:
"How the assistant answers your calls."

| Screen | Purpose | Notable behaviour |
|---|---|---|
| `A40b` Behaviour | The hub | Rows: Greeting style · Instructions · Voice · Language · What it may share · When to screen · Test call |
| `A41` Voice | Choose the voice callers hear | Preview plays a real sample of the actual announcement text, not a generic phrase. Premium voices are previewable before purchase — gating the preview would be selling something unheard |
| `A42` Instructions | Free-text guidance | **Bounded**: max 500 characters, with a live count. A preview shows how it changes a sample exchange. Instructions that attempt to change the announcement, extract subscriber data, or impersonate a person are **rejected at save with a specific reason** — the announcement is Invariant I1 and is not user-editable |
| `A43` Language | Language and script | Multi-select where the plan allows. Detection is automatic within the selected set; a language not selected is never used. Devanagari/Tamil/Bengali fixtures per [`03`](../../design/03-typography.md) |
| `A44` Bulk action review | Confirm an assistant-proposed action | Always a list of exactly what will happen, with per-item removal, before a single confirm |
| `A45` What it may share | The allow-list | Explicit, per-item: first name / no name · availability · a callback preference · nothing else. **Default is nothing.** This is the interface expression of Invariant I4 — the caller-facing agent cannot disclose subscriber PII, and this screen shows the user the boundary and lets them widen it deliberately |
| `A46` When to screen | Availability rules | Always / outside contact hours / a schedule / never. Premium beyond the default. A rule that would disable screening entirely states the consequence plainly |

**Shared contract for A41–A46**

- **Inputs** — Current configuration; plan tier; validation constraints.
- **Outputs** — Configuration mutation; a preview of the effect where one is
  meaningful.
- **Components** — `ListItem` · `Dropdown` · `TextField` · radio groups ·
  `Button` · `PremiumBadge` where gated.
- **Animation** — Standard push. Preview updates with `short` fade.
- **Accessibility** — Every setting states its current value in its row, so a
  screen reader user does not have to open a screen to learn its state.
- **Loading** — Optimistic; revert animated on failure.
- **Empty** — Not applicable.
- **Error** — Inline for validation, transient for save.
- **Success** — The changed value is the confirmation. No toast for a setting
  change the user can see.
- **Security** — `A42` instructions are untrusted input to our own prompt
  pipeline and are validated server-side, not only client-side. `A45` defaults
  closed.
- **Analytics** — `android.assistant.setting_changed` (`setting`, `from`, `to`)
  · `android.assistant.instructions_rejected` (`reason`) ·
  `android.assistant.share_scope_widened` (`item`).

---

## A50 · Settings root

**Purpose** — One conventional, scannable list.

**Structure** — Account · **Forwarding** · Privacy and data · Notifications ·
Premium · Business *(conditional)* · Assistant *(link back)* · Accessibility ·
Appearance · About.

**Notable** — Forwarding is **second**, above Privacy, because it is the one
setting that can silently break everything (Invariant U2). Each row shows its
current state as supporting text — "Active", "Recording off", "Premium" — so the
list answers most questions without being opened.

**Analytics** — `android.settings.root_viewed` · `android.settings.row_tapped`
(`row`).

---

## A51 · Account · A51n · Change number · A59 · Devices

| Screen | Contract highlights |
|---|---|
| `A51` Account | Number (masked by default, revealable by tap) · plan · member since · devices link · sign out · **delete account**, which is last, `status.fraud.text`, and routes to `A53d` rather than acting directly |
| `A51n` Change number | A migration, not a re-registration. Verify the new number → clear forwarding on the old SIM (`##61#`) → provision on the new → verify → **history is carried over**. If any step fails the user is left on the old configuration, working, never in between |
| `A59` Devices | Every trusted device: model, last active, current-device marker, revoke. Revoking the current device signs out immediately with a confirmation. Revoking another is immediate and notifies that device |

**Security** — The number is masked by default because the account screen is
the most likely screen to be visible over a shoulder. Device revocation is
immediate, not eventual — a revocation that takes effect in fifteen minutes is
not a security control.

**Analytics** — `android.settings.number_revealed` ·
`android.settings.device_revoked` (`was_current`) ·
`android.settings.number_change_started` / `_completed` / `_failed` (`step`).

---

## A53 · Privacy and data · A53e · Export · A53d · Erase

**A53 Purpose** — One screen where every consent is visible, current and
reversible in one tap. Invariant U9.

**Structure** — The same list as `A11`, plus: what we store (a plain-language
inventory), retention controls, `Export my data`, `Erase my data`, and the
Grievance Officer's contact details as required by DPDP.

**Edge cases** — Withdrawing recording consent → existing audio is deleted, and
the confirmation says exactly that, with the count of recordings affected ·
lowering retention → deletion applies immediately to older data, and the
confirmation states how many transcripts will be deleted · withdrawing analytics
→ effective immediately, client-side.

**A53e Export** — Generates a machine-readable archive: calls, transcripts,
verdicts, timelines, consents, settings. Audio included only on a separate
explicit confirmation. Delivered as a file the user shares themselves — **we do
not email it**, because emailing a subscriber's transcript archive is a data
transfer we should not perform on their behalf.

**A53d Erase** — High friction, deliberately. Three steps: what will be deleted
(itemised with counts) → what will not (carrier records, legally-retained
billing) → type the last four digits of the number to confirm. Then: forwarding
is cleared on the carrier as part of erasure, because leaving a subscriber's
calls forwarded to a platform that no longer holds their account would be a
serious defect.

**Accessibility** — Every consent row reads as label + state + consequence.
Erase confirmation is not time-limited.

**Loading** — Export is > 10 s: determinate progress with cancel.

**Error** — Erase failure must never leave a partial state: the operation is
transactional server-side and the screen reports either complete or not started.

**Security** — Erasure honours Invariant I7 — Kafka carries identifiers, not
content, so erasure is achievable. The screen does not promise more than the
architecture can deliver, and it names the one exception: "Calls already deleted
by retention can't be listed."

**Analytics** — `android.settings.consent_changed` (`purpose`, `granted`) ·
`android.settings.retention_changed` (`days`) ·
`android.settings.export_completed` (`included_audio`) ·
`android.settings.erasure_started` / `_completed` / `_abandoned` (`step`).

---

## A54 · Notifications · A57 · Accessibility · A58 · Appearance · A65 · About

| Screen | Contract highlights |
|---|---|
| `A54` | Per-channel controls that **deep-link to the system channel settings** rather than duplicating them — a second set of notification switches that disagree with Android's is a support nightmare. What we own: lock-screen content visibility, DND bypass for fraud and forwarding (opt-in, explicitly), and summary grouping |
| `A57` | Larger touch targets · always show state labels alongside the orb · disable swipe actions in favour of explicit buttons · transcript text size independent of system scale · a **"describe verdicts in full"** option that expands every abbreviated risk label |
| `A58` | Theme (system / light / dark / high contrast) · text size · reduced motion is **read from the system only**, not offered here — a second reduced-motion setting that disagrees with the OS is drift ([`05 §5.7`](../../design/05-motion.md)) |
| `A65` | Version · legal · privacy policy · **Grievance Officer** (name, email, response window — DPDP requirement, not buried) · open-source notices · support · a diagnostics bundle the user can generate and share, containing no call content |

**Analytics** — `android.settings.notification_pref_changed` (`channel`,
`setting`) · `android.settings.accessibility_pref_changed` (`pref`) ·
`android.settings.theme_changed` (`theme`) ·
`android.settings.diagnostics_generated`.

---

## A55 · Premium · A60 · Paywall · A61 · Plans · A62 · Checkout · A63 · Success · A64 · Manage

### Premium strategy

**What is free, permanently:** screening itself, the announcement, transcripts,
the on-device pre-filter, blocklist and allowlist, fraud and spam **verdicts**,
forwarding health, and everything in Settings.

**What premium adds:** fraud **evidence detail** (`A31d`), call recording,
extended retention, multiple languages, premium voices, custom instructions,
availability schedules, and unlimited screening volume beyond the free tier's
monthly cap.

**The line we do not cross:** the verdict is always free. Charging a user to
find out that a call was probably a scam — after we already know — is not a
business model, it is a hostage situation. We charge for depth, not for safety.

### `A60` Paywall

**Presentation** — Bottom sheet over the surface that triggered it. **Never a
screen replacement**, never full-bleed, never blocking the app.

**Structure** — What you tapped and why it is premium (specific to the trigger,
not a generic feature list) · price, monthly and annual, with the annual saving
stated as a number not a percentage · one primary action · a plain `Close` in
the top-left, always visible, never delayed, never disguised.

**Edge cases** — Triggered from a fraud detail → the copy is about evidence,
never about fear. **We do not sell against a live threat** · already premium →
never shown · payment previously failed → shows the failure state and the fix
instead of the offer · shown more than 3 times for the same feature in 30 days
→ suppressed. A paywall that will not stop asking becomes an argument for
uninstalling.

**Accessibility** — Close is the first focusable element. Price announced with
currency and period in full. Sheet dismissible three ways.

**Security** — No payment fields. Payment is provider-hosted throughout.

### `A62` Checkout · `A63` Success · `A64` Manage

- Checkout hands off to the payment provider's own sheet. Back during payment is
  the one place a confirmation dialog is correct
  ([`02 §2.4`](02-navigation-graph.md)).
- Process death during checkout **restarts the flow** rather than resuming it —
  a resumed payment state is how double charges happen.
- Success states what is now available and **auto-advances to the feature the
  user originally tapped**, not to a generic home. They bought a thing; give
  them the thing.
- Manage subscription shows next billing date, plan, invoices, and cancel.
  **Cancel is present, in plain words, not hidden behind "manage plan".**
  Cancelling states exactly what stops working and when, and does not offer a
  retention discount more than once.

**Analytics** — `android.premium.paywall_shown` (`trigger_feature`,
`shown_count_30d`) · `android.premium.paywall_dismissed` (`dwell_ms`) ·
`android.premium.checkout_started` (`plan`, `period`) ·
`android.premium.purchase_completed` (`plan`) ·
`android.premium.purchase_failed` (`reason`) ·
`android.premium.cancelled` (`tenure_days`, `reason_given`).

---

## A56 · Business

Visible only to members of an organisation. This is the **only** business
surface in the Android app; administration lives in the Business Portal
(Surface 3).

**Purpose** — Show a business member what their organisation controls, and let
them do the small number of things that make sense on a phone.

**Structure**

| Section | Content |
|---|---|
| Organisation | Name, the user's role, who administers it |
| Your lines | Personal and business lines, with the business line's screening rules shown **read-only** if the user is not an admin |
| What your organisation can see | **The most important section.** An explicit, itemised statement: business-line calls, transcripts and verdicts are visible to organisation administrators. Personal-line calls are not, ever |
| Business hours | Read-only unless the user is an admin |
| Open the portal | A link out, opened in the browser. **Not an embedded web view** — an in-app browser showing an authenticated admin surface is a session-handling hazard and a phishing template |

**Edge cases** — User is an admin → rows become editable for the small set of
settings that are safe on mobile (hours, out-of-hours message); everything else
still points to the portal · user is removed from the organisation → the section
disappears, the business line disappears from the switcher, and a one-time
notification explains it. **Their personal call history is untouched**, and the
notification says so · organisation is deleted → same.

**Accessibility** — The visibility section is a heading plus a plain list, and
it is announced in full.

**Security** — This screen is where the **personal/business privacy boundary is
stated to the user in plain language**. A business member must never be
surprised to learn their employer can read a transcript, and the app's own
statement of the boundary is a control, not documentation. Role is enforced
server-side; the UI reflects it and never grants it.

**Analytics** — `android.business.section_viewed` (`role`) ·
`android.business.portal_opened` · `android.business.line_switched` ·
`android.business.membership_removed_notice_shown`.
