# Android · 4 · Screens — Screening, Calls, Emergency

The hot path. Nine of these screens carry the product; the rest of the app
explains them.

Every screen here meets a stricter bar: **one-handed, comprehensible in two
seconds, no blocking dialog, and a defined behaviour under every degradation**
in [`01 §1.3`](../01-cross-surface-conventions.md).

---

## A21 · Live Screening

The product. Everything else is support.

```
┌────────────────────────────────────────────────┐
│  ✕                            ● Recording  1:04│  ← recording only if consented
├────────────────────────────────────────────────┤
│                                                │
│                    ◯                           │  ← VoiceOrb, 96dp
│                 Listening                      │     state label always present
│                                                │
│  Unknown caller                                │  ← title.md
│  +91 98765 43210                               │  ← body.sm, tertiary
│                                                │
│  ⚠ Possible fraud · low confidence         ⓘ  │  ← appears only when assessed
│                                                │
├────────────────────────────────────────────────┤
│  Announcement                            0:00  │
│  This call is being answered by an automated   │
│  assistant and is being recorded.              │
│                                                │
│  Caller                                  0:06  │
│  "Hello sir, I'm calling from the bank         │
│  regarding your account"                       │
│                                                │
│  ⬡ Assistant                             0:11  │
│  Which bank, and can I take a reference        │
│  number?                                       │
│                                                │
│  Caller                                  0:15  │
│  "It's regarding a security matter, I need     │
│  to verify your OTP"                           │
│                                                │
│  ⚠ ─────────────────────────────────────────  │  ← inline verdict marker
│                                       ▼        │     at the producing turn
├────────────────────────────────────────────────┤
│  ┌──────────────────────┐  ┌────────────────┐ │
│  │     Take call        │  │    Decline     │ │  ← thumb zone
│  └──────────────────────┘  └────────────────┘ │
│           ⊘ Block             ◑ Listen         │  ← tertiary row
└────────────────────────────────────────────────┘
```

**Purpose** — Let the subscriber understand a call in progress and decide,
within seconds, whether to take it. Nothing else.

**Inputs** — Live session ID; streamed transcript turns (interim + final);
verdict with confidence, arriving asynchronously; TTS amplitude stream; caller
number; resolved caller identity if any; recording consent state; live-listen
availability; elapsed duration.

**Outputs** — `takeover_requested` · `decline_requested` ·
`block_requested` · `live_listen_toggled` · navigation to `A22` on end ·
`A27` post-call summary.

**Components** — `VoiceOrb` · `Transcript` (live) · `RiskIndicator` ·
`RecordingIndicator` · `TakeoverButton` · `EndCallButton` ·
`LiveListenToggle` · `Button` (Danger, Block) · `Snackbar`.

**Animation** — Enters shared-Z, `long`/`emphasized`. Turns enter fade + 8 dp
rise, `short`/`decelerate`, no stagger. Verdict badge scale 0.8 → 1.0 + fade,
`short`. Orb per [`02 §2.1`](../02-interaction-language.md). Duration timer 1 Hz,
no tween. Exits `medium`, fade.

**Edge cases**

| Condition | Behaviour |
|---|---|
| Screening ends while the user is reading | Surface does **not** close. It freezes, the header changes to "Call ended", actions become `Block` and `Report`, and a `Continue` returns to Calls. Yanking the screen away mid-sentence is the worst possible moment to do it |
| Verdict arrives after the call ended | Renders in place with the same entrance. `A27` reflects it |
| Transcript stream dies, call continues | Header chip "Transcript unavailable — the call is still being screened." Take call remains enabled ([`02 §2.3`](../02-interaction-language.md)) |
| User scrolled up when a turn arrives | No auto-scroll. "1 new" pill at the bottom edge |
| Second screening starts | Impossible by design — one screening per line at a time. If the platform reports one, the first is shown and the second is queued to `A20` with a note |
| App backgrounded | Surface state preserved exactly, including scroll. Notification continues to update |
| Takeover tapped twice | Second tap is a no-op; the button is in loading state and disabled |
| Call ends between tap and connect | "The call ended before we could connect you." → `A27` |
| Device rotates | Landscape reflows to transcript-left, controls-right. Controls never leave the thumb zone |
| Battery saver active | No change. This surface is exempt from any reduction that removes information |

**Accessibility** — Reading order: header status → orb state → caller identity →
verdict → transcript (newest last) → actions. `Take call` is the first
focusable action and reachable by a single Switch Access scan.
Transcript is a `polite` live region on completed turns only. Verdict announces
`assertive` once. Recording announces `assertive` on entry and on start.
Orb carries `stateDescription`, waveform is hidden. All actions ≥ 48 dp, in the
lower third. Works at 200% text: the transcript scrolls, the action row does
not. **This screen is flow AF2** ([`01 §1.10`](../01-cross-surface-conventions.md)).

**Loading** — Entry is instant; the surface renders with the header and an empty
transcript within one frame. Connecting state is the orb's real state, never a
spinner. If the session is not yet joined after 1.5 s: "Connecting to the
screening…" beneath the orb.

**Empty** — Not applicable. There is always at least the announcement turn. If
the session yields nothing within 3 s, the transcript shows "Waiting for the
caller to speak" in `content.tertiary` — a truthful statement, not a skeleton.

**Error** — Session join failure: contextual, "Couldn't connect to this
screening. The call is still being screened and your phone is working normally."
with `Retry` and `Go back`. **Never blocking** — a blocking error here removes
the takeover control from a live call.

**Success** — Takeover: handset rings, system UI takes over
([`02 §2.7`](../02-interaction-language.md)). Decline/Block: `A27` with the
outcome stated and, for Block, a 5 s undo.

**Security** — Every caller string quoted and attributed (Invariant U10). No
caller string is rendered in the header, in a button, or in the notification
title. Live listen requires explicit consent and shows `RecordingIndicator` only
if audio is actually being persisted — listening is not recording, and conflating
them would be a false disclosure. Screenshots permitted (it is the user's own
call), but the surface is `VISIBILITY_PRIVATE` on the lock screen.

**Analytics** — `android.screening.live_viewed` (`entry_point`, `latency_ms`) ·
`android.screening.takeover_engaged` (`time_to_action_ms`, `turn_count`,
`verdict_present`) · `android.screening.declined` · `android.screening.blocked` ·
`android.screening.live_listen_toggled` · `android.screening.abandoned`
(`dwell_ms`) · `android.screening.transcript_stalled` (`stall_ms`).
**No transcript text, no number, in any of them** (Invariant U11).

---

## A23 · Takeover — connecting

Not a screen. An in-place state of `A21`, documented separately because its
failure modes are distinct and consequential.

**Purpose** — Bridge the subscriber into the live call without dropping it.

**Inputs** — Session ID; subscriber MSISDN; current agent state.

**Outputs** — Handset inbound call, or a failure returning `A21` to armed state.

**Components** — `TakeoverButton` in loading state (width preserved) ·
`Transcript` (continues live) · `Snackbar`.

**Animation** — Storyboard S4 ([`02 §2.8`](../02-interaction-language.md)).
`haptic.heavy` fires at tap, before any network work — the tap must feel
registered even if the network is slow.

**Edge cases** — Handset busy on another call → "You're on another call. End it
and we'll connect you." with the screening continuing · subscriber does not
answer the callback within 20 s → agent resumes screening, transcript records
the attempt, `A21` returns to armed · carrier fails the callback → manual
fallback: "Call them back on +91…" with a one-tap dial · caller hangs up during
connect → "The caller hung up." → `A27`.

**Accessibility** — Button state change announces `assertive`: "Connecting you
to the call". Failure announces `assertive` with the recovery.

**Loading** — The button itself. Nothing else changes; the transcript keeps
streaming, because the user may still be reading while the connection is made.

**Empty / Error / Success** — Success is the handset ringing. Failure is
transient tier with the specific reason. **A failed takeover never ends the
call** — the screening continues and the button re-arms.

**Security** — The callback dials the subscriber's verified MSISDN from server
state, never from a client-supplied value. Timeout 8 s.

**Analytics** — `android.screening.takeover_connected` (`connect_ms`) ·
`android.screening.takeover_failed` (`failure_reason`).

---

## A22 · Call Detail

The record. The most-visited screen after the feed, and the one that has to
still make sense in six weeks.

```
┌────────────────────────────────────────────────┐
│  ←   Call detail                        ⋮  🔍 │
├────────────────────────────────────────────────┤
│                                                │
│  Unknown caller                                │  ← title.lg
│  +91 98765 43210                               │
│  Today, 3:42 pm · Screened · 1 min 12 s        │  ← numeric.sm, tertiary
│                                                │
│  ┌──────────────────────────────────────────┐ │
│  │ ⚠  Possible fraud · low confidence       │ │  ← RiskIndicator, Detailed
│  │                                          │ │
│  │ ⬡ Claimed to be from a bank and asked    │ │  ← AiBadge — model output
│  │   for a one-time password. Refused to    │ │
│  │   give a branch or reference number.     │ │
│  │                                          │ │
│  │ Why we think so                       →  │ │  ← Principle 2: show the work
│  └──────────────────────────────────────────┘ │
│                                                │
│  [ Block ]  [ Report ]  [ Add contact ]        │
│                                                │
│  ── Transcript ──────────────────────  ⧉ Copy │
│                                                │
│  Announcement                            0:00  │
│  This call is being answered by an automated   │
│  assistant and is being recorded.              │
│                                                │
│  Caller                                  0:06  │
│  "Hello sir, I'm calling from the bank…"       │
│                                                │
│  ⬡ Assistant                             0:11  │
│  Which bank, and can I take a reference        │
│  number?                                       │
│                                                │
│  ⋮                                             │
│                                                │
│  ── What happened ───────────────  ⌄ 12 events│  ← CallTimeline, collapsed
│                                                │
└────────────────────────────────────────────────┘
```

**Purpose** — Present one call completely: who, what was said, what we
concluded, on what evidence, and what the user did or can do.

**Inputs** — Call ID (opaque); call record; screening record if any; transcript
turns; verdict + confidence + evidence turn references; timeline events;
recording availability and consent state; caller identity and verification
status; user actions taken.

**Outputs** — Block · Report · Add contact · Allow · Share/export · Delete ·
navigate to `A26` Caller Profile · `A22s` transcript search · `A22a` playback ·
deep link into the transcript from the verdict.

**Components** — `TopAppBar` · `RiskIndicator` (Detailed) · `AiBadge` ·
`Transcript` · `CallTimeline` · `Button` (Secondary ×3) · `ListItem` ·
`BottomSheet` (actions) · `Snackbar` · `Skeleton`.

**Animation** — Push, shared X + 30 dp, `medium`/`standard`. Verdict card
renders with content, no separate entrance. Timeline expansion `medium`.
Deep-linked highlight per [`02 §2.4`](../02-interaction-language.md).

**Edge cases**

| Condition | Behaviour |
|---|---|
| Call was **not screened** (pre-filtered through) | No transcript section, no verdict. Timeline shows the pre-filter decision and why. Header states "Rang through — known contact" |
| Call was **blocked on device** | No transcript. Timeline shows the block and the rule that caused it, with an unblock action |
| Verdict pending | Skeleton in the verdict slot, exact final size. Never "Unknown" |
| Verdict absent and not pending | `Unknown` — "Not assessed" — with the reason on tap ("Call too short to assess") |
| Recording exists but consent later withdrawn | Playback control absent, with one line: "Audio was deleted when you turned off recording." Never a disabled play button |
| Transcript beyond retention (90 days) | Metadata and verdict remain; transcript section states the retention policy and the date it was deleted. This is a **correct** state, presented without apology |
| Transcript empty (caller said nothing) | Announcement turn only, then "The caller didn't speak." |
| Very long transcript | Virtualised beyond 40 turns; jump-to control appears |
| Call is currently live | This screen is not reachable — `A21` is shown instead |
| Business line call, user lacks the role | Gated empty: which role is needed and who grants it |

**Accessibility** — Reading order: identity → time/outcome → verdict →
evidence → actions → transcript → timeline. Verdict announces level **and**
confidence. Transcript is `body.lg`, fully selectable. Timeline is an ordered
list, not a graphic. Copy action copies speaker labels and timestamps. **This
screen is flow AF3.** Works fully without audio (Invariant U6).

**Loading** — 300 ms–2 s tier. Skeleton matches final layout exactly: header
block, verdict card, three transcript turns, collapsed timeline. Transcript
loads progressively; the verdict does **not** render until its confidence has
loaded ([`01 §1.1`](../01-cross-surface-conventions.md)).

**Empty** — Not applicable to the screen. Per-section empties are in edge cases.

**Error** — Blocking tier if the call record fails to load: "Couldn't load this
call. Screening is running normally and your other calls are unaffected."
`Retry` · `Go back`. Transcript-only failure is contextual within its section,
leaving verdict and metadata usable.

**Success** — Block: 5 s undo snackbar. Report: sheet confirms and closes,
one-line acknowledgement. Add contact: hands off to the system contacts intent.
Export: system share sheet.

**Security** — Caller turns quoted and attributed. Number rendered in
`numeric` with `tnum`, announced digit by digit. Export carries the automated-
assistant footer, **non-removable** ([`02 §2.4`](../02-interaction-language.md)).
Share uses the system sheet — we never upload to share. Screenshot allowed. Deep
link uses the opaque ID.

**Analytics** — `android.history.call_viewed` (`entry_point`, `screened`,
`verdict_level`, `verdict_confidence`, `load_ms`) ·
`android.history.evidence_opened` · `android.history.transcript_copied` ·
`android.history.exported` · `android.protection.number_blocked`
(`source_screen`) · `android.protection.number_reported` (`report_category`).

---

## A22s · Transcript search

**Purpose** — Find a phrase within one transcript.

**Inputs** — Transcript turns; query.

**Outputs** — Scroll to match; match index.

**Components** — `SearchField` · `Transcript` · match counter.

**Animation** — Slides down from the app bar, `short`. Scroll to match
`medium`/`standard`.

**Edge cases** — Match in an interim turn (impossible — interims are not
persisted) · match count > 99 shows "99+" · query matches the announcement
(searchable, and it should be — users look for what the caller was told).

**Accessibility** — Match count announced `polite` on change: "3 of 7 matches".
Next/previous are 48 dp targets. Highlight is weight, not colour
([`01 §1.7`](../01-cross-surface-conventions.md)).

**Loading** — None. Local.

**Empty** — Filtered tier, inline: "No matches".

**Error** — Not applicable.

**Success** — First match scrolled to and announced.

**Security** — Query is local, never sent, never stored.

**Analytics** — `android.history.transcript_searched` (`match_count`). No query
text.

---

## A22a · Audio playback

Conditional. Exists only when a recording exists, which requires explicit
consent (ADR-0012 — audio off by default).

**Purpose** — Play the recorded audio of a screening, synchronised to the
transcript.

**Inputs** — Recording availability; consent state; plan; transcript turn
timings; playback position.

**Outputs** — Playback control; transcript position sync.

**Components** — `Waveform` (historical) · play/pause · scrubber ·
`Transcript` (with an active-turn indicator).

**Animation** — Waveform scrubs `linear` — audio is linear. Active turn is
indicated by a left rail, not by scrolling the transcript under the user unless
they enabled follow.

**Edge cases** — Audio deleted at retention while transcript remains → control
absent with the one-line explanation · playback interrupted by an incoming call
→ pauses, resumes only on explicit tap · Bluetooth route change → pauses ·
audio and transcript out of sync → transcript wins; we never move the transcript
to a position we are not confident in.

**Accessibility** — Playback is an **enhancement**, never the only path
(Invariant U6). Scrubber is keyboard and Switch accessible with 5 s steps.
Waveform hidden from screen readers. Position announced on scrub end, not
continuously.

**Loading** — Buffering shows an inline indeterminate indicator on the play
control only.

**Empty** — The control does not exist. Absence is the empty state.

**Error** — Transient: "Couldn't play this recording." with retry. The
transcript is unaffected and the screen remains fully useful.

**Success** — Playback. No confirmation — the audio is the confirmation.

**Security** — Audio is `SENSITIVE`. Streamed, never cached to external storage,
never exported without a second explicit confirmation naming what is being
shared. `RecordingIndicator` is **not** shown during playback — nothing is being
recorded, and showing it would be a false disclosure.

**Analytics** — `android.history.audio_played` (`duration_played_ms`,
`completion_pct`).

---

## A27 · Post-call summary

The moment a screening ends. Transient, and it must not overstay.

```
┌────────────────────────────────────────────────┐
│                    ▁▁▁▁                        │
│                                                │
│  Call ended                                    │
│  Unknown caller · +91 98765 43210 · 1:12       │
│                                                │
│  ⚠ Possible fraud · low confidence             │
│                                                │
│  ⬡ Asked for a one-time password and refused   │
│    to give a branch or reference number.       │
│                                                │
│  ┌──────────────┐  ┌──────────────┐            │
│  │    Block     │  │  See details │            │
│  └──────────────┘  └──────────────┘            │
│                  Dismiss                        │
└────────────────────────────────────────────────┘
```

**Purpose** — Deliver the outcome of a screening the user was watching, and
offer the one action most likely to matter.

**Inputs** — Completed call record; verdict; summary; outcome.

**Outputs** — Block · navigate to `A22` · dismiss.

**Components** — `BottomSheet` · `RiskIndicator` · `AiBadge` · `Button`
(Primary, Secondary) · `Button` (Tertiary, dismiss).

**Animation** — Rises `medium`/`spring.gentle`. Content is already resolved when
it appears — this sheet never shows a skeleton, because a loading state on a
summary sheet is worse than a delayed sheet.

**Edge cases** — Verdict still pending when the call ends → the sheet waits up
to 2 s, then shows without a verdict and updates in place if one arrives while
it is open · user already navigated away → the sheet does **not** appear; the
feed card carries the outcome instead · call ended because the user took it →
**no sheet at all**; they were on the call and know how it went · screening
failed → the sheet states the failure and that the call rang through.

**Accessibility** — Announced `polite` on appearance with the outcome and
verdict. Focus moves to the sheet. Dismissible by back, by scrim tap, and by an
explicit `Dismiss` — three ways, because a modal with one exit is a trap.
Auto-dismiss after **12 s** only if the user has not interacted; a fraud verdict
never auto-dismisses.

**Loading** — See edge cases. No skeleton.

**Empty** — Not applicable.

**Error** — If the record cannot be loaded, no sheet appears. The feed carries
the outcome. A broken sheet is worse than no sheet.

**Success** — Block shows a 5 s undo and closes.

**Security** — Summary carries `AiBadge`. Caller content is quoted where quoted
at all. Nothing here is shown over the lock screen.

**Analytics** — `android.screening.summary_viewed` (`verdict_level`,
`auto_dismissed`) · `android.screening.summary_action` (`action`).

---

## A29 · Share / export transcript

**Purpose** — Get a transcript out of the app, for a bank, an employer, or the
police.

**Inputs** — Call record; transcript; verdict; export format.

**Outputs** — System share intent with a generated artefact.

**Components** — `BottomSheet` · `ListItem` (format options) · system share
sheet.

**Animation** — Sheet `medium`/`spring.gentle`.

**Edge cases** — Transcript past retention → export unavailable with the reason
· audio export requires a **second** confirmation naming the file and its
sensitivity · very long transcript → generation takes > 2 s, shows determinate
progress with cancel.

**Accessibility** — Format options are a radio list, not a segmented control.
Each option states what it contains in one line.

**Loading** — > 2 s tier: progress + cancel.

**Empty** — Not applicable.

**Error** — Transient with retry. A partially generated file is never handed to
the share sheet.

**Success** — The system share sheet is the success state. No additional
confirmation.

**Security** — Every export carries a footer stating it was produced by an
automated assistant, **non-removable**. Audio export is separately confirmed.
Files are generated to app-private storage and shared by content URI with a
time-limited grant — never written to shared storage.

**Analytics** — `android.history.export_started` (`format`) ·
`android.history.export_completed`.

---

## A52 · Forwarding health

The screen Invariant U2 exists for. Most products would make this a settings
row; here it is a destination, because a subscriber whose forwarding lapsed is
paying for nothing and cannot tell.

```
┌────────────────────────────────────────────────┐
│  ←   Forwarding                                │
├────────────────────────────────────────────────┤
│                                                │
│  ● Active                                      │  ← status.success, or
│  Unknown callers are being screened.           │     warning / fraud
│                                                │
│  Last checked 4 minutes ago                    │
│                                                │
│  ── Setup ─────────────────────────────────    │
│  SIM              Airtel · +91 98765 43210  →  │
│  Forwards to      Our number, after 5 seconds  │
│  Verified         Today, 3:38 pm               │
│                                                │
│  ── If something looks wrong ─────────────     │
│  Check now                                  →  │
│  Set up again                               →  │
│  Turn off forwarding                        →  │
│                                                │
│  ── Good to know ──────────────────────────    │
│  Your carrier may charge for the forwarded     │
│  leg on some plans. We can't see those         │
│  charges — check your bill if you're unsure.   │
│                                                │
└────────────────────────────────────────────────┘
```

**Purpose** — Tell the truth about whether screening is actually working, and
fix it when it is not.

**Inputs** — Last interrogation result and timestamp; configured SIM and
subscription; carrier; DID; ring delay; verification history; known
carrier-specific quirks.

**Outputs** — Re-interrogate · re-activate (→ `A07`) · change SIM (→ `A05`) ·
disable forwarding.

**Components** — Status block · `ListItem` ×N · `Button` (Secondary) ·
`Dialog` (disable confirmation) · `Snackbar`.

**Animation** — Status change on re-check: the status block cross-updates,
`short`. Verified transition draws a check, `medium`/`decelerate`, with
`haptic.confirm`.

**Edge cases**

| Condition | Behaviour |
|---|---|
| Never configured | First-run empty: what forwarding is, why it is needed, `Set up` |
| Lapsed | Status `status.fraud`, **"Not active — your calls are ringing through without screening."** Primary action `Set up again`. Banner `A71` appears app-wide |
| Configured to an unexpected DID | Status `status.warning`, "Forwarding is set to a number we don't recognise." Offers to reset. This is the SIM-swap and hostile-reconfiguration case |
| Dual-SIM, wrong SIM forwarded | Named explicitly, with a one-tap correction that clears the old configuration first |
| Interrogation unsupported by carrier | Status "Can't verify on this carrier", with a manual test-call action instead. Honest about the limit rather than showing a green tick we cannot justify |
| Carrier cleared it (`##002#`, network event, SIM swap) | Treated as lapsed, with the likely cause named |
| Offline | Last-known status with its age, prominently. Actions disabled with the reason |

**Accessibility** — Status is text first, colour second. The status sentence is
the screen's accessible heading. Every action is a 48 dp list row.
**This screen is one tap from the home surface** — via the persistent banner
when broken, and via Settings when not.

**Loading** — Re-check is 2–10 s: inline progress on the status block with text
("Asking your carrier…"). The rest of the screen stays usable.

**Empty** — First-run tier, as above.

**Error** — Interrogation failure is contextual within the status block:
"Couldn't check with your carrier. Screening may still be working." with
`Try again` and a link to a manual test call. **We do not claim it is broken
when we merely could not check** — that would be as damaging as claiming it
works.

**Success** — Verified: status becomes Active, `haptic.confirm`, timestamp
updates. No dialog, no celebration.

**Security** — The MMI string is constructed client-side from a validated DID,
never from a server-supplied string (ADR-0002 §10). Disabling requires a
confirmation dialog naming the consequence: "Calls will ring straight through.
Nothing will be screened." Displaying the DID is deliberate — it is how a user
verifies we are not forwarding somewhere unexpected.

**Analytics** — `android.settings.forwarding_viewed` (`status`) ·
`android.settings.forwarding_checked` (`result`, `check_ms`) ·
`android.settings.forwarding_reactivated` · `android.settings.forwarding_disabled`
(`confirmed`).

---

## A71 · Forwarding broken — persistent banner

**Purpose** — Make a silent failure loud, once, without becoming wallpaper.

**Inputs** — Forwarding health state; last user dismissal.

**Outputs** — Navigate to `A52`.

**Components** — Banner (`status.warning.subtle`) · `Button` (Tertiary).

**Animation** — Enters with a height expansion from the top app bar,
`short`/`decelerate`. This is one of two sanctioned height animations, justified
because it must displace content rather than cover it — a banner that overlays
the feed would hide a call.

```
┌────────────────────────────────────────────────┐
│ ⚠ Calls aren't being screened right now.       │
│   Your phone is ringing normally.     Fix it → │
└────────────────────────────────────────────────┘
```

**Edge cases** — Appears on all four tab roots, not on detail screens · never
dismissible while the condition holds, because dismissing a broken core function
is not a choice we should offer · re-appears immediately after any navigation ·
does not appear during a live screening (`A21` has no banner) · if forwarding is
broken **and** we are offline, offline takes precedence, since we cannot verify
and should not claim.

**Accessibility** — Announced `assertive` once on first appearance per
occurrence, then `polite` on subsequent screen entries. Focusable, and reachable
as the first element after the app bar.

**Loading / Empty** — Not applicable.

**Error** — This *is* an error surface.

**Success** — Disappears when forwarding verifies. `haptic.confirm` once.

**Security** — Copy never names the carrier issue speculatively. It states what
we observed.

**Analytics** — `android.forwarding.banner_shown` (`days_broken`) ·
`android.forwarding.banner_tapped` · `android.forwarding.recovered`
(`hours_to_recovery`).

---

## A70 · Offline

**Purpose** — Tell the user their calls are still being screened.

**Components** — Chip below the top app bar, `status.warning.subtle`, one line.

```
  ⌁  You're offline. Calls are still being screened.
```

**Edge cases** — Metered/slow connection is **not** offline and gets no chip ·
offline during a live screening → `A21` is not enterable and the feed card
shows "Screening — details unavailable offline" · offline at cold start → cached
feed with its age, or a Recurring empty state saying calls will appear when
connected.

**Accessibility** — Announced `polite` on transition to offline, once. Not
re-announced per screen.

**Error** — This is a mode, not an error, and it is presented as one.

**Success** — Clears on reconnect, `short`/`accelerate`, with one transient
confirmation only if queued changes were synced.

**Analytics** — `android.system.offline_entered` · `android.system.offline_exited`
(`duration_s`, `queued_mutations`).

---

## A72 · Emergency handoff

Invariant U7. The product's job here is to get out of the way, immediately and
unmistakably.

```
┌────────────────────────────────────────────────┐
│                                                │
│                                                │
│              This may be urgent                │  ← headline.lg
│                                                │
│  The caller said something that sounds like    │
│  an emergency. We've stopped screening and     │
│  connected the call to your phone.             │
│                                                │
│  "there's been an accident, is this            │  ← quoted, attributed
│  Priya's phone"                                │
│                                                │
│  ┌──────────────────────────────────────────┐ │
│  │            Take the call                 │ │
│  └──────────────────────────────────────────┘ │
│  ┌──────────────────────────────────────────┐ │
│  │            Call them back                │ │
│  └──────────────────────────────────────────┘ │
│                                                │
│              Not an emergency                  │  ← tertiary, small
│                                                │
└────────────────────────────────────────────────┘
```

**Purpose** — Hand control of a possible emergency back to the human being,
without making a decision on their behalf.

**Inputs** — Emergency classification with confidence; the triggering caller
turn; caller number; session state.

**Outputs** — Immediate takeover · outbound dial · dismiss as false positive
(feedback signal).

**Components** — `Button` (Primary ×1, Secondary ×1) · `Button` (Tertiary) ·
quoted caller turn.

**Animation** — Enters `long`/`emphasized`, overriding whatever is on screen.
`haptic.heavy`. **No sound beyond the notification channel's** — an alarm tone
here would compete with the user's own reasoning at the worst possible moment.

**Edge cases**

| Condition | Behaviour |
|---|---|
| False positive | `Not an emergency` dismisses, returns to `A21`, and screening **resumes if the call is still live**. The signal is logged for model evaluation |
| Call already ended | Screen states so and offers `Call them back` only |
| User is on another call | Both actions state the consequence; we never end an existing call |
| Detected after the call ended | Not shown as a takeover. Delivered as a high-priority notification and a prominent `A22` marker instead — a full-screen emergency interrupt about a call that is over is alarming and useless |
| Emergency **and** fraud both detected | Emergency wins the presentation. The fraud verdict is shown as a secondary line, because "urgent" and "possibly a scam" together is exactly the pretext-call case and the user needs both facts |
| Repeated from the same number | Still shown. We do not suppress an emergency because we saw one before |

**Accessibility** — Announced `assertive` immediately, in full, including the
quoted turn. Focus lands on `Take the call`. Both primary actions are ≥ 56 dp.
**Back is disabled**; exit is by explicit action only. Works at 200% text
without truncating the quote — the quote scrolls if it must, the buttons do not
move.

**Loading** — None. This screen never waits for anything.

**Empty** — Not applicable.

**Error** — If takeover fails, the screen stays and offers `Call them back` with
the number pre-filled, one tap to the system dialer.

**Success** — The user is on the call, or dialling.

**Security** — The caller's words are quoted and attributed — this is the one
screen where caller-supplied text is most prominent, and therefore where
Invariant U10 matters most. It is never a heading, never a button label. **We do
not dial emergency services on the user's behalf**, ever, under any confidence.

**Analytics** — `android.emergency.handoff_shown` (`confidence`,
`call_active`) · `android.emergency.action` (`action`) ·
`android.emergency.dismissed_false_positive`. **The false-positive rate on this
screen is a launch-blocking metric**, tracked in `tests/eval`.

---

## A76 · App update required

**Purpose** — Block an app version that can no longer be trusted on the wire.

**Inputs** — Minimum-supported-version signal from the edge API.

**Outputs** — Play Store intent.

**Components** — Full-screen blocking state · `Button` (Primary).

**Edge cases** — Play Store unavailable (sideload, no Play Services) → APK
guidance and a support link · update required while a screening is live →
**the screening is unaffected** and the screen says so; the block applies to the
app, not to the service.

**Accessibility** — Announced `assertive`. One action, focused.

**Error copy** — "Update CallScreen to keep using it. **Your calls are still
being screened** — this only affects the app."

**Security** — The minimum version is server-asserted and signature-checked.
This is the kill switch for a client with a known vulnerability.

**Analytics** — `android.system.update_required_shown` (`current_version`).

---

## A77 · Service degraded

Invariant I11 requires degradation to fail safe. This screen requires it to be
**honest**.

**Purpose** — Tell the user when the product is running at reduced capability.

**Inputs** — Degradation signal with a class: `tier_downgraded` ·
`audio_unavailable` · `language_reduced` · `queue_shed`.

**Components** — Contextual banner, `status.warning.subtle`, on `A20` and `A21`.

**Copy, per class**

| Class | Copy |
|---|---|
| `tier_downgraded` | "Screening is running in a simpler mode right now. Answers may be shorter." |
| `audio_unavailable` | "Recording is paused. Transcripts are still being saved." |
| `language_reduced` | "Only English and Hindi are available right now." |
| `queue_shed` | "We're busy. Some calls are ringing through without screening." |

**Edge cases** — Never shown for a degradation the user cannot perceive · never
shown more than once per degradation episode · **never** shown for fraud scoring
or the safety layer, because those are never shed (Invariant I11) — if they were,
that is an incident, not a banner.

**Accessibility** — `polite`, once.

**Security** — Copy never names an upstream provider or an internal service.

**Analytics** — `android.system.degraded_shown` (`class`, `duration_s`).
