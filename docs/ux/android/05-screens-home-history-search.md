# Android · 5 · Screens — Home, History, Search, Protection

The feed the user opens the app for, and the section that proves the product is
working.

---

## A20 · Calls feed — the app home

```
┌────────────────────────────────────────────────┐
│  CallScreen              [Personal ⌄]    🔍  ⧉ │  ← line switcher only if
├────────────────────────────────────────────────┤     the user has >1 line
│ ⚠ Calls aren't being screened right now.       │  ← A71, conditional
│   Your phone is ringing normally.     Fix it → │
├────────────────────────────────────────────────┤
│ ┌────────────────────────────────────────────┐ │
│ │ ●  Screening now              0:14   Take →│ │  ← live card, elevated,
│ │    Unknown caller · +91 98765 43210        │ │     does not scroll away
│ │    "calling about your recent order"       │ │
│ └────────────────────────────────────────────┘ │
│                                                │
│  Today                                         │  ← section header, sticky
│ ┌────────────────────────────────────────────┐ │
│ │ Unknown caller                       3:42pm│ │
│ │ ⬡ Asked for an OTP, refused to give a      │ │
│ │   reference number                          │ │
│ │ ⚠ Possible fraud · low confidence          │ │
│ │ +91 98765 43210                             │ │
│ └────────────────────────────────────────────┘ │
│ ┌────────────────────────────────────────────┐ │
│ │ Blue Dart              ✓            1:10pm │ │  ← verified business
│ │ ⬡ Delivery arriving between 4 and 6 pm     │ │
│ │ +91 80 4567 8900                            │ │
│ └────────────────────────────────────────────┘ │
│ ┌────────────────────────────────────────────┐ │
│ │ Amma                                11:02am│ │  ← pre-filtered, rang
│ │ Rang through · known contact                │ │     through. No summary,
│ └────────────────────────────────────────────┘ │     no verdict — nothing
│                                                │     was screened
│  Yesterday                                     │
│  ⋮                                             │
├────────────────────────────────────────────────┤
│    ☰            ⛨             ⬡            ⚙   │
│  Calls     Protection    Assistant    Settings │
└────────────────────────────────────────────────┘
```

**Purpose** — Answer "what happened while I wasn't paying attention?" in one
glance, and put a live screening one tap away.

**Inputs** — Paged call list (newest first); live screening state; forwarding
health; connectivity; active filters; line selection; plan tier.

**Outputs** — Navigate to `A22`, `A21`, `A24`, `A25`, `A26`, `A52` · block /
allow via swipe · line switch.

**Components** — `TopAppBar` (elevation 0 → 2 on scroll) · `CallerCard` ×N ·
sticky date headers · banner (`A71`) · chip (`A70`) · `BottomNav` ·
`Skeleton` · `Snackbar` · `Dropdown` (line switcher).

**Animation** — List items fade + 8 dp rise, 20 ms stagger, capped at 5
([`05 §5.5`](../../design/05-motion.md)). Live card insertion per
[`02 §2.6`](../02-interaction-language.md). Swipe reveals with `spring.default`.
Pull-to-refresh spinner. Section headers stick without a shadow animation.

**Edge cases**

| Condition | Behaviour |
|---|---|
| Live screening starts while scrolled down | No scroll steal. "Screening now" pill at the top edge |
| Two lines, business line selected | Feed filters to that line. The switcher label is always visible so the user cannot forget they are filtered |
| Filter active and the result is empty | Filtered empty tier with `Clear filters`, never the first-run illustration |
| Very high call volume (100+/day) | Date headers become hour headers within today. Virtualised list |
| Same number calls repeatedly | Cards are **not** collapsed into a thread. Each screening is a distinct event with a distinct transcript, and merging them hides the one that mattered |
| Call in progress that we are not screening | Not shown. We show what we did, not what the phone is doing |
| First launch, before any call | First-run empty with the test-call action |
| Retention boundary crossed | Older calls fall off with a final row: "Calls older than 90 days are deleted." — stated, not silent |

**Accessibility** — Each `CallerCard` announces as **one** node, composed
deliberately ([`01 §1.10`](../01-cross-surface-conventions.md)). Date headers
are headings, so a screen reader user can navigate by heading. Swipe actions are
duplicated in the long-press sheet (`A28`) because swipe is unavailable to
Switch Access. Live card is the first item and announces `polite` on
appearance. **This screen is part of flows AF1, AF2 and AF6.**

**Loading** — 300 ms–2 s tier. Skeleton: 6 cards matching the two-line card
shape exactly, varied text widths 60–90%. Header and nav render immediately.
Pagination loads a 3-card skeleton at the bottom, never a full-screen one.

**Empty**

| Tier | Trigger | Copy |
|---|---|---|
| First-run | No calls ever | **"No screened calls yet"** / "Calls from numbers you don't know will appear here after they're screened." / `Try a test call` |
| Recurring | Filter = today, none today | **"Nothing screened today"** / — |
| Filtered | Filter matched nothing | **"No calls match these filters"** / `Clear filters` |
| Offline, no cache | First launch offline | **"Connect to see your calls"** / "Your calls are still being screened while you're offline." |

**Error** — Contextual banner above the list, list keeps showing cached content:
"Couldn't refresh. Screening is running normally." with `Retry`. **Never
blocking** — a cached feed is far more useful than an error page. Blocking only
if there is no cache and no network, in which case it is the offline empty
state, not an error.

**Success** — Swipe-to-block shows a 5 s undo snackbar. Swipe-to-allow shows a
brief confirmation. Refresh completing with no new calls shows nothing.

**Security** — Caller summaries carry `AiBadge`. Caller-claimed names are never
rendered at title weight — only verified business identities are
([`02 §2.6`](../02-interaction-language.md)). Numbers are `tnum`, announced digit
by digit. Screenshots permitted. Content is not shown in the app switcher preview
if the user enables the privacy setting.

**Analytics** — `android.history.feed_viewed` (`item_count`, `load_ms`,
`has_live`) · `android.history.card_tapped` (`position`, `verdict_level`) ·
`android.history.swipe_action` (`action`, `undone`) ·
`android.history.filter_applied` (`filter_type`) ·
`android.history.line_switched`.

---

## A24 · Search

**Purpose** — Find a specific call, or every call about a topic.

**Inputs** — Query; scope (all / cached-only when offline); recent queries;
suggested filters.

**Outputs** — Navigate to `A22` or `A26`; apply a filter.

**Components** — `SearchField` · `ListItem` (results) · `CallerCard` (compact) ·
chips (suggested filters) · `Skeleton`.

**Animation** — Enters as a full-screen surface, `short`. Results replace in
place with no exit animation ([`02 §2.11`](../02-interaction-language.md)).

**Edge cases** — Query is a phone number → an exact-match caller result is
pinned above text matches · query matches a transcript but the transcript is past
retention → the call still appears, with the match unavailable and the reason
stated · offline → scope stated as "50 downloaded calls" · query < 2 characters
→ recents and suggestions, not results · very common word → results capped at
100 with a note.

**Accessibility** — Keyboard opens on entry. Result count announced `polite` on
settle: "12 results". Each result announces as one node. Escape/back returns
with query intact. Highlight is weight, not colour.

**Loading** — Local results appear immediately; server results merge in with a
skeleton row while pending. Debounce 250 ms.

**Empty** — Zero-state: recent searches (≤ 8, local, clearable) then suggested
filters ("Possible fraud", "This week", "Blocked"). No results: Filtered tier.

**Error** — Transient. Local results remain: "Couldn't search everything —
showing downloaded calls."

**Success** — Results. No confirmation.

**Security** — Recent queries are **local only, never synced**
([`01 §1.7`](../01-cross-surface-conventions.md)) and clearable in one action.
Query text is never emitted in analytics.

**Analytics** — `android.history.search_performed` (`result_count`,
`scope`, `latency_ms`) · `android.history.search_result_tapped` (`position`) ·
`android.history.search_abandoned`. **No query strings.**

---

## A25 · Filters

**Purpose** — Narrow the feed.

**Inputs** — Available filter dimensions; current selection.

**Outputs** — Applied filter set.

**Components** — `BottomSheet` · chip groups · `Button` (Primary apply,
Tertiary clear).

**Dimensions** — Outcome (screened / rang through / blocked / missed) · Verdict
(safe / spam / fraud / not assessed) · Time (today / week / month / all) · Line
(if > 1) · Has recording (premium).

**Edge cases** — A combination that can never match (e.g. "rang through" +
"possible fraud") is disabled with a one-line reason, not silently empty ·
filters reset on cold start, not on tab switch
([`02 §2.6`](02-navigation-graph.md)).

**Accessibility** — Chips are toggle buttons with selected state announced.
Applied count announced on apply. Sheet dismissible three ways.

**Loading / Error** — None. Local.

**Empty** — Not applicable.

**Success** — Sheet closes, feed updates, the top bar shows an active-filter
indicator with a count. **The indicator is mandatory** — a silently filtered
feed is the most common way a list looks broken.

**Analytics** — `android.history.filter_applied` (`dimensions`, `result_count`).

---

## A26 · Caller profile

**Purpose** — Everything we know about one number, in one place.

```
┌────────────────────────────────────────────────┐
│  ←   Caller                              ⋮     │
├────────────────────────────────────────────────┤
│  +91 98765 43210                               │
│  Not in your contacts                          │
│  Blocked · since 12 March                      │
│                                                │
│  ⚠ Flagged as possible fraud in 2 of 4 calls   │
│                                                │
│  [ Unblock ]  [ Report ]  [ Add contact ]      │
│                                                │
│  ── 4 calls ───────────────────────────────    │
│  ⋮ (CallerCard list)                           │
└────────────────────────────────────────────────┘
```

**Inputs** — Number; contact match; block/allow state; all calls from this
number; aggregate verdict history; reputation signal if any.

**Outputs** — Block / unblock / allow · report · add contact · navigate to
`A22`.

**Components** — Header block · `RiskIndicator` (Inline) · `Button` ×3 ·
`CallerCard` ×N.

**Edge cases** — Number is a known contact → contact name and photo from the
system, with a note that we read contacts but do not store them · verified
business → business identity, registry source, and a link to what "verified"
means · aggregate verdicts disagree across calls → **we show the disagreement**
("flagged in 2 of 4"), never an averaged verdict. Averaging judgements across
calls invents a certainty no single call supports · number is the user's own →
handled, does not crash, shows "This is your number".

**Accessibility** — Number announced digit by digit. Aggregate line announces
fully. Actions ≥ 48 dp.

**Loading** — Skeleton for the call list; header renders from the passed
arguments immediately.

**Empty** — Only reachable with at least one call, so no empty state. If all
calls fall past retention: "Calls from this number are older than 90 days and
have been deleted." with the block state still shown.

**Error** — Contextual on the list; the header and actions remain usable.

**Success** — Block/unblock shows a 5 s undo.

**Security** — Reputation signals are shown as our assessment, never as a
community verdict — there is no social layer
([`01 §1.4`](01-information-architecture.md)). No cross-subscriber information
is ever exposed here.

**Analytics** — `android.history.caller_viewed` (`call_count`, `is_blocked`) ·
`android.protection.number_blocked` (`source_screen`).

---

## A28 · Call actions

**Purpose** — Every action on a call, in one reachable place.

**Components** — `BottomSheet` · `ListItem` ×N.

**Actions** — Block · Allow always · Add to contacts · Report · Share transcript
· Delete this call · Caller profile.

**Edge cases** — Actions unavailable for the call type are **absent, not
disabled** — there is no "share transcript" on a call that was never screened,
and a greyed row implies we are withholding something · Delete requires
confirmation and states that deletion is permanent and does not affect the
carrier's records.

**Accessibility** — Long-press opens it; it is also reachable from `A22`'s
overflow, so no action is swipe-or-long-press-only. Destructive rows are
`status.fraud.text` and are never first.

**Security** — Delete removes our copy. The copy says exactly that: "This
deletes our copy. Your carrier's call log is separate and we can't change it."

**Analytics** — `android.history.action_sheet_opened` ·
`android.history.action_taken` (`action`).

---

## A30 · Protection overview

Where the product proves it is working. Principle 2 — show the work — is the
whole design of this screen.

```
┌────────────────────────────────────────────────┐
│  Protection                              🔍    │
├────────────────────────────────────────────────┤
│                                                │
│  This month                                    │
│  ┌──────────────────────┬───────────────────┐ │
│  │  4                   │  38               │ │
│  │  possible fraud      │  likely spam      │ │
│  ├──────────────────────┼───────────────────┤ │
│  │  12                  │  216              │ │
│  │  blocked             │  rang through     │ │
│  └──────────────────────┴───────────────────┘ │
│                                                │
│  ⬡ Every number here links to the call that   │  ← AiBadge on the framing
│    got it flagged.                             │     line — it is our claim
│                                                │
│  Possible fraud                        4    →  │
│  Likely spam                          38    →  │
│  Blocked numbers                      12    →  │
│  Always allowed                        6    →  │
│                                                │
│  Report a number                            →  │
│                                                │
├────────────────────────────────────────────────┤
│    ☰            ⛨             ⬡            ⚙   │
└────────────────────────────────────────────────┘
```

**Purpose** — Show what was kept away, with a path to the evidence for every
number of it.

**Inputs** — Rolling-period counts by category; recent flagged calls; list
sizes; plan tier.

**Outputs** — Navigate to `A31`, `A36`, `A32`, `A33`, `A34`.

**Components** — Stat tiles (`Card`, Filled) · `ListItem` ×N · `AiBadge`.

**Animation** — Counts do **not** count up. A number that animates from 0 is a
delight pattern in a security tool, and it makes the figure feel like a score
rather than a fact. They render resolved.

**Edge cases** — Zero of everything in a new account → first-run empty
explaining what will appear · counts differ from what the user remembers → every
count is tappable to its list, because an unverifiable statistic is a claim
(Principle 2) · plan does not include fraud detail → the fraud count is still
shown truthfully, and tapping it reaches a Gated state, not a paywall pretending
to be an error.

**Accessibility** — Stat tiles announce as "4, possible fraud", value then
label. Tiles are ≥ 48 dp and tappable. The framing line is not decorative and is
read.

**Loading** — Skeleton for the tile grid and list rows, exact sizes.

**Empty** — First-run: "Nothing to report yet" / "Numbers we flag or block will
appear here."

**Error** — Contextual banner; lists remain reachable with their own loading.

**Success** — Not applicable.

**Security** — No cross-user aggregates, no "X people blocked this number"
social proof. Our assessment, our evidence.

**Analytics** — `android.protection.overview_viewed` (`fraud_count`,
`spam_count`, `blocked_count`) · `android.protection.category_opened`
(`category`).

---

## A31 · Fraud list · A31d · Fraud detail

**Purpose** — `A31`: every call we flagged as possible fraud. `A31d`: why, on
what evidence, and what to do.

**Inputs** — Flagged calls with verdict, confidence, evidence turn references,
pattern classification (OTP request, impersonation, advance fee, job scam,
KYC…), user's prior actions.

**Outputs** — Navigate to `A22` deep-linked to the flagged turn · block · report
· mark as not fraud (a model-evaluation signal).

**Components** — `CallerCard` (fraud state) · `RiskIndicator` (Detailed) ·
`FraudBadge` · `Transcript` excerpt · `Button` ×3.

**A31d structure**

```
  Verdict          Possible fraud · low confidence
  Pattern          Asked for a one-time password
  Evidence         The two turns that produced it, quoted,
                   with "Open in transcript →"
  What this is     Two sentences of plain-language explanation
                   of this scam pattern. Static copy, not model
                   output, and therefore no AiBadge.
  What to do       Block · Report · It wasn't fraud
```

**Edge cases** — Confidence is low → **"Possibly"** prefix everywhere, outlined
badge, and the explanation opens by saying we are not sure
([`09 §RiskIndicator`](../../design/09-components.md)) · evidence turn deleted at
retention → the verdict remains with "The transcript for this call has been
deleted", and `A31d`'s evidence section says so rather than showing a broken
link · user marks "It wasn't fraud" → the verdict is visibly downgraded on all
surfaces immediately, and the signal is recorded. **The user's correction wins in
their own interface, always** · pattern unknown → the pattern row is absent, not
"Unknown".

**Accessibility** — `FraudBadge` announces `assertive` on first appearance and
is always tappable to the evidence. Evidence quotes are attributed. "It wasn't
fraud" is a 48 dp target and not visually subordinate — dissent must be as easy
as agreement.

**Loading** — Skeleton matching the verdict card.

**Empty** — Recurring: "Nothing flagged as fraud" / "We'll flag calls here if
someone tries something." Gated (free plan, detail): names what premium adds,
with the count still honest.

**Error** — Contextual. The verdict, once loaded, never disappears due to a
subsequent error.

**Success** — Block: 5 s undo. Report: acknowledgement, one line. Not-fraud:
immediate visible downgrade, `haptic.tick`.

**Security** — Static explanations are product copy and carry **no** `AiBadge`;
model-generated summaries carry one. Mixing them would misattribute our
editorial claims to the model, and the model's to us.

**Analytics** — `android.protection.fraud_list_viewed` (`count`) ·
`android.protection.fraud_detail_viewed` (`confidence`, `pattern`) ·
`android.protection.evidence_opened` ·
`android.protection.verdict_disputed` (`level`, `confidence`) — **the most
important quality signal the product emits.**

---

## A36 · Spam list

As `A31`, with `status.spam`, without the evidence-detail screen — a spam
verdict is lower stakes and does not warrant a dedicated evidence surface. The
transcript is one tap away and carries the evidence.

**Distinguishing behaviour** — Bulk selection is available here and **not** on
fraud. Blocking 30 spam numbers at once is reasonable; blocking 4 fraud numbers
without reading them is not, and the friction is deliberate.

**Analytics** — `android.protection.spam_list_viewed` ·
`android.protection.bulk_blocked` (`count`).

---

## A32 · Blocklist · A32d · Blocked number detail · A33 · Allowlist

**Purpose** — The two lists the user directly controls, and the reason each
entry exists.

**Inputs** — Entries with number, optional label, source (manual / from a call /
imported), created-at, last-attempt-at.

**Outputs** — Add · remove · edit label · navigate to `A26`.

**Components** — `SearchField` · `ListItem` ×N · `BottomSheet` (`A35` add) ·
`Snackbar`.

**Edge cases** — Number in both lists → **impossible**; adding to one removes
from the other, stated in a snackbar, never silently · blocked number calls
again → `last-attempt-at` updates; the list is proof the block is working, which
is the reason to show it · very large list (500+) → search and virtualisation ·
allowlisted number gets flagged as fraud → allowlist wins for routing, the
verdict is still recorded and shown. **The user's explicit instruction is
obeyed; our opinion is still reported.** · block added offline → applied to the
on-device pre-filter immediately and queued for sync.

**Accessibility** — Rows announce number (digit by digit), label, source and
date as one node. Remove is a trailing action with a 48 dp target, plus a
long-press sheet.

**Loading** — Skeleton rows.

**Empty** — Blocklist, first-run: "No blocked numbers" / "Numbers you block
won't ring or be screened." Cleared tier if the user emptied it: one line, no
action.

**Error** — Optimistic add/remove with animated revert on failure
([`01 §1.1`](../01-cross-surface-conventions.md)).

**Success** — Add: row inserts, `haptic.confirm`. Remove: row collapses, 5 s
undo.

**Security** — Blocking is local-first — the on-device pre-filter enforces it
without a server round trip (ADR-0002 §5), so a blocked number is blocked even
offline, even if our backend is down. **The screen says this**, once, in the
first-run empty state, because it is a genuine architectural guarantee and
users do not assume it.

**Analytics** — `android.protection.list_viewed` (`list`, `size`) ·
`android.protection.entry_added` (`list`, `source`) ·
`android.protection.entry_removed` (`list`, `undone`).

---

## A34 · Report a number

**Purpose** — Let the user tell us we were wrong, or that something was worse
than we said.

**Inputs** — Number; optional call reference; category list.

**Outputs** — Report submission.

**Components** — `BottomSheet` · radio list (categories) · `TextField`
(optional, multiline) · `Button` (Primary).

**Categories** — Fraud or scam · Unwanted marketing · Wrong verdict — this was
fine · Wrong verdict — this was worse · Someone impersonated a business · Other.

**Edge cases** — Report without a call reference (typed number) → accepted,
flagged as unverified · duplicate report for the same call → accepted, silently
deduplicated. Telling a user "you already reported this" punishes engagement we
want · offline → queued, with the queue state visible.

**Accessibility** — Radio list, not chips — a single choice from six is a radio
group. Free text is optional and labelled as such.

**Loading** — Submit shows inline button loading with preserved width.

**Empty** — Not applicable.

**Error** — Transient with retry; the draft is preserved.

**Success** — Sheet closes with one line: "Thanks — we've recorded that." **No
promise of action**, because we cannot make one per report, and a promise we do
not keep is worse than none.

**Security** — Free text is user-authored and treated as untrusted on ingestion.
It is never rendered back to any other user, and it is classified `PERSONAL`.

**Analytics** — `android.protection.report_submitted` (`category`,
`has_call_reference`, `has_free_text`). **No free-text content.**
