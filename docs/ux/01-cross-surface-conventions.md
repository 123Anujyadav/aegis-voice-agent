# 1 · Cross-Surface Conventions

Loading, error, empty, offline, recovery, permissions, notifications, search,
accessibility, analytics and security posture. Every surface obeys these unless
its own document states an exception and says why.

---

## 1.1 Loading

The tiers are frozen in [`07 §7.3`](../design/07-states.md). This section says
which tier applies where, because choosing the tier is a UX decision and
choosing it wrong is the most common way software feels slow.

### Selection

| Measured p95 | Treatment | Applies to |
|---|---|---|
| < 100 ms | **Nothing** | Local reads, cached lists, navigation between loaded tabs |
| 100–300 ms | Optimistic, reconcile on arrival | Toggles, blocklist add/remove, mark-as-read, filter changes |
| 300 ms – 2 s | **Skeleton** matching final layout | Call feed, call detail, console tables, portal dashboards |
| 2–10 s | Skeleton + progress text | Forwarding verification, contact sync, export generation |
| > 10 s | Determinate progress + **cancel** | Data export, bulk operations, eval runs |
| Unknown | Indeterminate + descriptive text | Carrier operations, payment confirmation, session connection |

### Rules that follow

**The 100 ms floor is implemented as a delay, not a judgement.** Indicators are
scheduled at +100 ms and cancelled if data arrives first. No screen decides at
call time whether to show a spinner.

**Skeletons never exceed one screenful.** Below the fold, nothing renders until
data arrives.

**Every skeleton has a timeout.** On expiry it becomes an error state of the
appropriate tier, never a longer skeleton. The timeout is the p99 of the
operation plus 2 s, and it is a named constant, not a magic number.

**Progressive disclosure over blocking.** A screen with three data sources
renders each section as it arrives rather than waiting for all three. The
exception is any screen where a partial render could be *misread* as a complete
one — a fraud verdict is never shown before its confidence has loaded.

**Optimistic UI requires a reconciliation path.** If the server rejects an
optimistic change, the revert is animated (`duration.short`) and accompanied by
a transient error, never a silent snap-back.

### Live-data loading

Streaming surfaces — the live transcript, the console session monitor — do not
use skeletons after the first frame. They use the **stream-idle** treatment: the
last received content stays rendered at full opacity, and a connection state
chip appears in the header. Blanking live content to show a skeleton is a
regression in information, not a loading state.

---

## 1.2 Empty states

Tiered by frequency ([`07 §7.1`](../design/07-states.md)). The tier is a
property of the *situation*, not the screen — the same list is first-run once
and recurring forever after.

| Tier | Trigger | Treatment | Illustration |
|---|---|---|---|
| **First-run** | User has never had data of this type | Illustration + heading + body + primary action | Yes |
| **Recurring** | Genuinely empty right now | Icon + one line + action if one exists | No |
| **Filtered** | Search or filter matched nothing | One line + clear-filter | No |
| **Cleared** | User emptied it deliberately | One line only, no action | No |
| **Gated** | Content exists but the plan or permission does not allow it | One line + the specific unlock action | No |

**"Gated" is ours, and it is not a paywall.** It appears when a user
legitimately has no access — a support agent without fraud-review scope, a
portal member without billing role. It names the missing permission and who
grants it. It never sells anything.

### Copy discipline

Heading is a statement of fact. Body is one sentence, ≤ 100 characters, saying
what will appear here and when. No apology, no humour, no emoji, no exclamation
mark ([`07 §7.1`](../design/07-states.md)).

| Situation | Heading | Body |
|---|---|---|
| No screened calls, first run | No screened calls yet | Calls from numbers you don't know will appear here after they're screened. |
| No screened calls, recurring | Nothing screened today | — |
| Search, no match | No results for "courier" | — |
| Blocklist emptied by user | Blocklist is empty | — |
| Fraud queue clear (console) | Queue clear | Cases appear here when a screening scores above the review threshold. |
| No team members (portal) | You're the only member | Invite people to give them access to calls and numbers. |

---

## 1.3 Errors

Tiers frozen in [`07 §7.2`](../design/07-states.md). Placement is determined by
severity, and severity by **what the user can no longer do**.

| Tier | The user cannot… | Placement |
|---|---|---|
| **Inline** | …submit this field | Below the field |
| **Contextual** | …use this section, but the rest works | Banner within the section |
| **Blocking** | …use this screen at all | Full screen: icon, heading, body, retry |
| **Transient** | …see the result of one action, but nothing is lost | Snackbar, 5 s, with action |
| **Destructive** | …undo what they are about to do | Dialog, explicit confirmation |

### The telephony clause — Invariant U1

Every **blocking** and **contextual** error on the Android surface states the
call-path status in its first line of body copy. This is not optional garnish;
it is the answer to the question the user is actually asking.

```
✗  Couldn't load your calls
   Something went wrong. Please try again.
   [ Retry ]

✓  Couldn't load your calls
   Screening is still running and your calls are ringing normally.
   This is a display problem, not a call problem.
   [ Retry ]
```

Where the call path *is* affected, say that with equal directness:

```
✓  Screening is paused
   Calls are ringing straight through to your phone right now.
   We're reconnecting — this usually takes under a minute.
   [ Check forwarding ]
```

### Error copy rules

Beyond [`07 §7.2`](../design/07-states.md):

| Rule | Detail |
|---|---|
| Never a code in primary copy | Trace ID lives in a collapsed **Details** row, copyable, for support |
| Never blame the user | "That number isn't valid" not "You entered an invalid number" |
| Never "unexpected error" | If we cannot name the cause, name the action |
| Never "Oops" / "Uh oh" / "Whoops" | Cute copy in failure reads as unserious |
| One action, or two at most | A retry and an escape. Three actions in an error is an unmade decision |
| **Retry is real** | A retry button that re-runs a request guaranteed to fail again is a lie. If we know it will fail, the action is something else |

### Error taxonomy

Every error maps to exactly one of these classes. The class determines tier,
copy template, retry behaviour and analytics dimension.

| Class | Example | Tier | Retry |
|---|---|---|---|
| `network.offline` | No connectivity | Contextual → cached content | Automatic on reconnect |
| `network.timeout` | Request exceeded budget | Transient | Manual, backoff |
| `auth.expired` | Access token expired | Silent | Automatic refresh; blocking only if refresh fails |
| `auth.revoked` | Device credential revoked | Blocking | No retry — re-auth flow |
| `auth.integrity` | Play Integrity verdict failed | Blocking | No retry — support path |
| `permission.denied` | Runtime permission refused | Contextual | Deep link to system settings |
| `permission.role` | Call-screening role not held | Blocking (screening features only) | Re-request flow |
| `carrier.forwarding_lapsed` | Interrogation shows no forwarding | **Contextual, persistent** | Guided re-activation |
| `carrier.mmi_failed` | MMI dial rejected | Blocking within flow | Manual instructions + carrier-specific help |
| `service.degraded` | Tier downgraded, feature reduced | Contextual | None — informational |
| `service.unavailable` | Upstream down | Contextual | Automatic, with backoff shown |
| `validation.*` | Field-level | Inline | N/A |
| `payment.*` | Checkout failure | Blocking within flow | Provider-specific |
| `quota.exceeded` | Plan limit reached | Contextual | Upgrade or wait, both stated |
| `not_found` | Resource gone | Blocking | Navigate up |
| `forbidden` | Role lacks scope | **Gated empty**, not an error | Name the role required |

**`forbidden` is deliberately not an error.** Telling a user they hit a wall
they were never meant to reach frames a correct system as a broken one.

---

## 1.4 Offline

Offline is a **first-class mode**, not an error, and on the Android surface it
is common — the target market has intermittent connectivity by default.

### The offline contract

| Capability | Offline behaviour |
|---|---|
| **Call screening itself** | **Unaffected.** It happens at the carrier and in our cloud. The handset's connectivity is irrelevant to whether calls get screened |
| On-device pre-filter | **Unaffected.** Contacts and cached reputation are local (ADR-0002 §5) |
| Call history | Cached; last synced state visible, with an age indicator |
| Call detail and transcript | Cached for the last 50 calls; older calls show a specific offline empty state |
| Live screening | Unavailable. The live surface is not enterable |
| Search | Local-only, over cached calls, clearly scoped: "Searching 50 downloaded calls" |
| Blocklist changes | **Queued**, applied locally to the pre-filter immediately, synced on reconnect |
| Assistant (personal) | Unavailable. Push-to-talk is disabled with a stated reason |
| Settings changes | Queued where safe; consent and privacy changes are **blocked offline** and say why |
| Payments | Blocked |

**The most important line in the offline state:** *"Your calls are still being
screened."* Users assume an offline app is an offline product. It is not, and
saying so converts an anxiety into a demonstration of the architecture.

### Presentation

- A persistent, low-emphasis **status bar chip** below the top app bar, not a
  modal and not a snackbar. `status.warning.subtle`, one line, non-dismissible
  while offline.
- Individually unavailable controls are `Disabled` with a reason available on
  tap — never hidden. Hiding a control offline makes the user think the feature
  was removed.
- On reconnect: the chip animates out (`duration.short`, `accelerate`), queued
  mutations flush, and **one** transient confirmation appears if anything was
  synced. Nothing appears if nothing was queued.

### Stale data

Cached content older than **15 minutes** carries a relative age label in the
list header ("Updated 2 hours ago"). Older than **24 hours**, the label moves
to `status.warning.text`. We never render stale data as fresh.

---

## 1.5 Permissions

Permission requests are the highest-abandonment moment in any Android product,
and this product needs an unusual number of them. The strategy is **earned
request, never bundled, always recoverable**.

### The ladder

Requested in this order, each immediately before its first genuine use, each
preceded by an in-app rationale screen the user can decline without leaving the
flow.

| # | Permission / role | Requested at | If denied |
|---|---|---|---|
| 1 | `POST_NOTIFICATIONS` (API 33+) | After account creation, before forwarding setup | Screening works; the user loses live-screening alerts. Stated plainly. Re-offered once, at the first screening they missed |
| 2 | **Call screening role** (`RoleManager.ROLE_CALL_SCREENING`) | During forwarding setup | **The on-device pre-filter cannot run.** Every unknown call is forwarded, which costs the user carrier minutes and us money. This is the one denial that materially degrades the product, and the rationale says so in those terms |
| 3 | `READ_CONTACTS` | At contacts-sync consent, after the role | Pre-filter falls back to the local allow list only. Known callers get screened, which is annoying but correct. Offered again from the first "why was my mother screened?" moment |
| 4 | `READ_PHONE_STATE` | Dual-SIM devices only, at SIM selection | SIM selection becomes manual. No other loss |
| 5 | `RECORD_AUDIO` | **Only** if the user enables the Personal Assistant's voice input | Assistant remains usable by text. Never requested for screening — we never record from the handset microphone during a screening, and the rationale says so explicitly |

**`RECORD_AUDIO` is never requested during onboarding.** A call-screening app
asking for the microphone at install is indistinguishable from spyware, and the
architecture does not need it (ADR-0002 §2 — the handset cannot access call
audio anyway). Requesting it late, for an obviously voice-shaped feature, is
both honest and higher-converting.

### The rationale screen contract

Every rationale screen states, in this order:

1. **What we will ask for** — the system dialog's own words, so the user is not
   surprised by different wording
2. **What it enables** — one concrete capability, not a benefit statement
3. **What we will never do with it** — the specific adjacent fear
4. **What happens if you say no** — the honest degradation, never a threat

Two actions: the primary proceeds to the system dialog; the secondary is
labelled **"Not now"** and returns to the flow without penalty. There is no
third "learn more" — if the screen needs a link, the screen failed.

### Permanent denial recovery

Android's "don't ask again" is terminal for the app-initiated dialog. On the
second denial we stop asking and instead show a **contextual banner at the point
of loss** — not on the home screen, not in settings — with a deep link to the
exact system settings page. `Intent.ACTION_APPLICATION_DETAILS_SETTINGS` with
the specific permission highlighted where the OEM supports it.

The banner appears at most once per feature per 30 days, and dismissing it is
permanent for that period.

### Console and Portal permissions

Both web surfaces use **role-scoped capability**, not runtime permissions. A
capability the current role lacks renders as a **Gated empty state**
([§1.2](#12-empty-states)) naming the role required and the person who grants
it — never a disabled control with no explanation, and never a hidden one.

Hidden controls make an internal tool undiscoverable and make a customer portal
feel arbitrary.

---

## 1.6 Notifications

Governed by Principle 6 — we interrupt for three things.

### Android channels

Separate channels so the user can tune each independently. Channel importance is
chosen deliberately; a channel set to `HIGH` that the user mutes takes the
important ones with it if they are bundled.

| Channel | Importance | Fires for | Sound | Ongoing |
|---|---|---|---|---|
| `screening_live` | `HIGH` | A screening has started | Short, distinct (§[`02 §2.9`](02-interaction-language.md)) | **Yes** — foreground-service-style, updates in place |
| `fraud_alert` | `HIGH` | Fraud verdict ≥ medium confidence | Distinct, more urgent | No |
| `screening_summary` | `DEFAULT` | Screening ended, no alert-worthy verdict | Silent | No |
| `forwarding_health` | `HIGH` | Forwarding lapsed or failed verification | Silent, but heads-up | **Yes** until resolved |
| `account` | `DEFAULT` | New device sign-in, plan change, payment failure | Silent | No |
| `product` | `LOW` | Feature announcements. **Off by default** | Silent | No |

### The live screening notification

The most important notification in the product, and the only one designed for
sub-second comprehension.

```
┌──────────────────────────────────────────────┐
│  ● Screening  ·  +91 98765 43210        0:12 │
│                                              │
│  "…calling about your recent order"          │
│                                              │
│  [ Take call ]            [ Decline ]        │
└──────────────────────────────────────────────┘
```

| Aspect | Behaviour |
|---|---|
| Updates | In place, at most **1 Hz**. Never a new notification per turn |
| Body | The most recent *completed* caller turn, truncated to two lines. Never interim ASR text — it changes under the user's eyes and reads as instability |
| Caller text | Rendered in quotation marks, attributed. **Never in the title** (Invariant U10) |
| Actions | `Take call` and `Decline`. Both act immediately, no app launch required, no confirmation (Invariant U3) |
| Tap | Opens the Live Screening screen |
| Dismissal | **Not dismissible** while the screening is live. It ends when the call ends |
| Timer | Elapsed screening duration, `tnum`, 1 Hz |
| Fraud escalation | If a fraud verdict lands mid-screening, this notification updates to carry the `RiskIndicator` and re-alerts once with `haptic.heavy` |

### Rules

- **One notification per event.** A screening produces the live notification,
  which becomes the summary notification. It never produces both.
- **Summary notifications are silent and grouped.** Multiple screenings in a
  quiet period collapse into one group; the group summary carries a count, never
  caller content.
- **No badge counts.** A number on the app icon is an engagement device
  (Principle 6, anti-patterns).
- **Nothing personal on the lock screen by default.** Caller name and transcript
  content are `VISIBILITY_PRIVATE`; the public version reads "Screening a call".
  A setting can raise this, and the setting states the consequence.
- **Quiet hours are the system's job**, not ours. We respect Do Not Disturb and
  do not implement a competing schedule — except that `fraud_alert` and
  `forwarding_health` may be set by the user to bypass DND, opt-in, explicitly.

### Console and Portal notifications

| Surface | Mechanism |
|---|---|
| Console | In-app toast + persistent incident banner. Email/pager for P1 incidents via the existing alerting path, not built here |
| Portal | In-app + email digests, per-member preference. **No web push** at launch — an unrequested browser permission prompt is the web equivalent of the microphone-at-install problem |

---

## 1.7 Search

Search is a surface, not a control. All three products share this model.

| Aspect | Convention |
|---|---|
| Entry | Search icon in the top app bar → full-screen search surface. Never an inline field that expands and pushes content |
| Focus | Keyboard opens immediately on entry. Escaping returns to the previous surface with scroll position intact |
| Debounce | 250 ms. Below that we spend requests on typos |
| Minimum query | 2 characters. Below that, recent searches and suggested filters render instead |
| Result latency budget | 300 ms p95 local, 800 ms p95 server. Above 300 ms, skeleton |
| Scope | Always stated in the header when it is anything other than "everything" — "50 downloaded calls", "This organisation", "Last 90 days" |
| Highlighting | Matched substring in `content.primary` at `weight.medium` against surrounding `content.secondary`. **Never a colour highlight** — colour is reserved for risk |
| Empty | Filtered tier. "No results for 'x'" + clear |
| History | Last 8 queries, local only, **never synced**, clearable in one action |
| Zero-state | Recent searches, then suggested filters. Never trending, never popular — there is no social layer here |

**Search never searches what the user cannot otherwise see.** A support agent's
search does not surface PII their role cannot open; a portal search is scoped to
the organisation. Search is the most common accidental privilege-escalation path
in an admin tool, and scoping it at the query layer rather than the render layer
is a security requirement, not a performance one.

### Query semantics

| Surface | Searchable |
|---|---|
| Android | Caller name, number, transcript full text, AI summary, tags. Not settings |
| Console | Subscriber MSISDN (hashed lookup), session ID, case ID, incident ID, prompt name, flag key. **Never transcript full text** — see [§1.11](#111-security-posture) |
| Portal | Caller name, number, transcript, contact, member, number label, invoice ID |

---

## 1.8 Recovery flows

Every state a user can get stuck in has a named way out. A product that can
reach a state it cannot leave is broken regardless of how rare the state is.

| Stuck state | Recovery | Entry point |
|---|---|---|
| Forwarding lapsed | Guided re-activation: interrogate → explain → dial MMI → verify | Persistent banner (U2), Settings → Forwarding, notification |
| MMI dial rejected by carrier | Carrier-specific manual instructions from the carrier matrix, then verify | Inline in the activation flow |
| Wrong SIM forwarded | SIM re-selection, clears old forwarding first (`##61#`) | Settings → Forwarding → Change SIM |
| Permission permanently denied | Deep link to system settings, at the point of loss | Contextual banner |
| Call-screening role lost to another app | Re-request the role, explaining that another app took it | Blocking banner on home |
| Signed out / token revoked | Re-auth by OTP on the same device; device credential re-derived | Full-screen auth |
| New device, same number | Device-trust re-enrolment: OTP + integrity + explicit "this replaces your other device" | Auth flow |
| Number changed | **Guided migration**: verify new number, re-provision forwarding, carry history. Never an account deletion |
| Lost access entirely | Support path with identity verification, exposed in-app under Settings → Account → Can't sign in |
| Payment failed | Grace period stated in days, feature-by-feature degradation listed, one retry action |
| Subscription lapsed | Revert to free tier with a stated, specific list of what stopped. Data retained per policy, not deleted |
| Consent withdrawn, feature broken | The withdrawal screen itself stated the consequence and offers immediate re-grant |
| Export failed midway | Resumable, or restart with the partial discarded. Never a silently truncated file |
| Portal org orphaned (sole admin left) | Ownership transfer flow; the org is never unrecoverable |
| Console access expired mid-investigation | Re-authenticate in place, investigation context preserved |

**The universal rule:** a recovery flow never begins by asking the user to
diagnose. It states what we detected, what we will do, and asks for the one
thing only they can provide.

---

## 1.9 Analytics

### Naming

```
<surface>.<area>.<object>_<verb_past_tense>
```

| Segment | Values |
|---|---|
| `surface` | `android` · `console` · `portal` |
| `area` | The IA section — `onboarding`, `screening`, `history`, `protection`, `assistant`, `settings`, `premium`, `fraud`, `billing`, `team` |
| `object_verb` | `takeover_engaged`, `forwarding_verified`, `paywall_dismissed` |

Examples: `android.screening.takeover_engaged` ·
`android.onboarding.forwarding_verified` · `console.fraud.case_resolved` ·
`portal.billing.plan_upgraded`.

### Mandatory properties

Every event carries: `surface`, `app_version`, `session_id` (ephemeral,
rotating), `locale`, `theme`, `reduced_motion`, `text_scale`, `network_class`,
`plan_tier`. Screen-view events additionally carry `screen_id` from the
inventory and `entry_point`.

### Prohibited properties — Invariant U11

**No event may carry any field classified `PERSONAL` or `SENSITIVE` in
`contracts/proto/callscreen/common/v1/annotations.proto`.** Not hashed, not
truncated, not "just the first three digits".

Specifically prohibited: MSISDN, caller number, caller name, transcript text,
summary text, contact data, location, IP, device identifiers that persist across
reinstall, payment instrument data.

Where a count is needed, count. Where a category is needed, categorise
server-side against the classified schema. **The classification drives the
analytics exclusion automatically** — a new field is `PERSONAL` until proven
otherwise (Invariant I8), so a schema addition cannot leak into analytics by
default.

### Event families

| Family | Fires on | Key dimensions |
|---|---|---|
| `*_viewed` | Screen entry | `screen_id`, `entry_point`, `load_ms` |
| `*_engaged` | The screen's primary action | `time_to_action_ms` |
| `*_abandoned` | Exit without the primary action | `dwell_ms`, `exit_method` |
| `*_failed` | Any error surfaced to the user | `error_class` from [§1.3](#13-errors), `retry_count` |
| `*_recovered` | A recovery flow completed | `recovery_path`, `attempts` |
| `perf.*` | Frame drops, cold start, TTI | `p50`, `p95`, `device_class` |

### What we measure that most products do not

| Metric | Why it is the important one |
|---|---|
| **Time from screening start to takeover** | The product's core interaction. If it exceeds ~6 s the surface is too slow to be useful mid-call |
| **Forwarding health uptime per subscriber** | The silent-failure metric. A subscriber at 90% is being under-served and does not know |
| **Pre-filter hit rate** | Directly determines unit economics (ADR-0002 §11) and whether contacts permission is worth re-requesting |
| **Fraud verdict → user action agreement** | Whether the user blocks after we flag. Disagreement is the model's real-world precision signal |
| **Screening duration** | A cost control (ADR-0002 §11), not only a UX number |
| **Transcript reads without audio** | Validates Invariant U6 empirically |
| **Consent withdrawal rate per purpose** | The honest measure of whether a consent was understood when granted |

### Consent

Analytics on the Android surface is **opt-out**, disclosed at onboarding under
its own control (never bundled — Invariant U9), and the opt-out is honoured
client-side by not emitting, not server-side by discarding. Crash reporting is
a separate control with its own consent, because a crash report and a product
analytic are different purposes under DPDP.

---

## 1.10 Accessibility flows

The baseline is [`08-accessibility.md`](../design/08-accessibility.md). This
section specifies the *flows* — the end-to-end journeys that must work, which is
where accessibility is usually lost even when every component is compliant.

### Flows that must work end to end, verified as flows

| # | Flow | Assistive path |
|---|---|---|
| **AF1** | Complete onboarding, from install to first successful screening | TalkBack only, no vision |
| **AF2** | Learn a screening is live, read the transcript, take the call | TalkBack + heads-up notification. **The critical flow** |
| **AF3** | Read a completed transcript and understand the verdict | TalkBack, 200% text, no audio |
| **AF4** | Block a number from a fraud alert | Switch Access, ≤ 8 switch actions |
| **AF5** | Change a consent and confirm it took effect | Voice Access, spoken commands only |
| **AF6** | Operate the entire app one-handed at 200% text | No assistive tech, large text |
| **AF7** | Console: triage a fraud case | Keyboard only, no mouse |
| **AF8** | Portal: invite a member and assign a role | Keyboard only + screen reader |

### The deaf and hard-of-hearing path — the product's highest-value case

A call screener that transcribes is the difference between "cannot use the
phone" and "can". Consequences, binding:

- The transcript is **complete**, not summarised, and available live
- Live transcript latency is budgeted and surfaced — if we fall behind, we say
  so rather than silently lagging
- Takeover from the live transcript surface is one tap, in the thumb zone
- **Audio-only affordances do not exist anywhere in the product.** No
  information is available by listening that is not available by reading
- Announcement text (Invariant I1) is shown to the subscriber verbatim, so they
  know what the caller heard

### Screen reader conventions

| Element | Announcement |
|---|---|
| `CallerCard` | Composed deliberately: "Unknown caller. Possible fraud, low confidence. Said they were calling about a bank account. 4 minutes ago. Plus 9 1, 9 8 7 6 5, 4 3 2 1 0." — one node, not eleven ([`09 §ListItem`](../design/09-components.md)) |
| Phone number | **Digit by digit with grouping pauses**, never as a cardinal number |
| Relative time | Spoken in full — "4 minutes ago", never "4m" |
| `RiskIndicator` | Level, then confidence, always both. "Possible fraud, low confidence" |
| Live transcript | `polite` live region on **completed** turns only. Interim ASR is never announced (it would re-announce on every revision) |
| Recording | **`assertive`**, on start, and re-announced on entering any screen where recording is active |
| Fraud verdict arrival | **`assertive`**, once |
| Voice orb | Its state label, `polite`. It is never announced as an image |
| Waveform | **Hidden.** Decorative; the transcript carries the information |

### Motion, text and contrast

- Reduced motion is `ANIMATOR_DURATION_SCALE == 0`
  ([`05 §5.7`](../design/05-motion.md)), and every state communicated by motion
  has a static equivalent that is **tested**, not assumed
- Every screen is usable at **200% text** with no truncation of anything
  load-bearing and no horizontal scroll
- High-contrast theme is a full theme, not an override layer
- Colour is never the sole carrier of meaning anywhere, including charts

### Cognitive and situational

- No screening-path interaction has a timeout the user can lose to
- Destructive actions are undoable for 5 s via snackbar, or confirmed by dialog
  where undo is impossible. Never both, never neither
- Plain language: the reading level target is Class 8. Regulatory text is
  summarised in plain language above the formal text, never instead of it
- Every flow is completable **one-handed** on a 6-inch device

---

## 1.11 Security posture in the interface

Where the interface itself is a control, not a view of one.

| Rule | Applies to | Why |
|---|---|---|
| **Caller-supplied text is quoted, attributed, and never chrome** | All surfaces | Invariant I4/U10. Caller speech is an injection vector aimed at both the model and the user |
| **No screen renders a server-supplied string as an action label** | All | A compromised or confused backend must not be able to relabel "Block" as "Allow" |
| **MMI strings are constructed client-side from a validated DID** | Android | ADR-0002 §10. A server-supplied dial string is a telephony-reconfiguration primitive |
| **PII is redacted by default in the console; revealing it is an audited, reason-required, time-boxed action** | Console | Invariant U12 |
| **Transcript full text is not searchable in the console** | Console | Full-text search over subscriber conversations is a surveillance capability. Access is per-session, from a case, with a reason |
| **API keys are revealed exactly once, at creation** | Portal | A key retrievable later is a key stored retrievably |
| **Session and device lists are user-visible and revocable** | Android, Portal | ADR-0010. A user who cannot see their sessions cannot detect a compromise |
| **Payment instrument data never touches our surfaces** | Portal, Android | Provider-hosted fields only |
| **Nothing personal on a lock screen or a browser tab title by default** | Android, Portal | The most common shoulder-surf surface |
| **Screenshot restriction on PII-revealing console views** | Console | `FLAG_SECURE` equivalent; deters the most common exfiltration path |
| **Deep links carry opaque identifiers only** | All | A link is copied, pasted, logged and indexed |
| **Sign-out clears cached transcripts and contacts from the device** | Android | A shared or resold handset is the realistic threat |

---

## 1.12 Verification gates

Each convention above is testable. These are the gates a screen must pass in
Phase 4 before it is considered done.

| # | Gate | Method |
|---|---|---|
| G1 | Every state in the screen's contract renders | Screenshot test per state |
| G2 | Loading tier matches measured p95 | Performance test asserts the tier |
| G3 | No skeleton exceeds its timeout without becoming an error | Instrumented test with a stalled fake |
| G4 | Every blocking/contextual error on Android states call-path status | Copy lint over error resources |
| G5 | Offline mode reachable and correct for every screen | Airplane-mode test pass |
| G6 | Every permission denial has a recovery path | Manual matrix, per permission, per denial type |
| G7 | Screen reader traversal completes AF1–AF8 | Manual, per release, on a real device |
| G8 | 200% text, high contrast, RTL, colour-blind ×4, reduced motion | Screenshot variant matrix ([`09 §9.0`](../design/09-components.md)) |
| G9 | No `PERSONAL`/`SENSITIVE` field in any emitted analytic | Static analysis against `annotations.proto` |
| G10 | No caller-supplied string in a label, title or action | Static analysis + review checklist |
| G11 | Every screen in the inventory has all 13 contract attributes | Doc lint |
| G12 | Every route in the navigation graph is reachable and every one has a defined back behaviour | Navigation test |
