# 2 · Interaction Language

Behaviour of the product's signature elements: the orb, the transcript, the
timeline, the caller card, thinking, streaming, transfer. Plus the animation
storyboard, micro-interactions, haptics and sound.

> [`docs/design/05-motion.md`](../design/05-motion.md) defines the *tokens* —
> durations, curves, haptic primitives. This document defines the *behaviour* —
> what animates, when, in response to what, and what it means. A token without a
> behaviour is unused; a behaviour without a token is drift.

---

## 2.1 Voice orb behaviour

The orb is a **state indicator with an honesty contract**. It is not a
character, not a mascot, and not decoration.

### The two orbs

| | Screening orb | Assistant orb |
|---|---|---|
| Where | Live Screening, Call Detail (historical replay) | Assistant tab |
| Represents | The Screening Agent talking to a caller | The Personal Assistant talking to the subscriber |
| Interactive | **No.** Tapping does nothing | **Yes.** Press-and-hold to talk |
| Amplitude source | TTS output amplitude, streamed from the session | Device mic RMS (listening) / TTS output (speaking) |
| Default state | Idle, static | Idle, static |

They are the same component with different bindings. Principle 3 requires the
user never confuse them; the surfaces do that work, not the orb itself.

### State machine

```
                    ┌────────────────────────────────┐
                    ▼                                │
   ┌──────┐    ┌──────────┐    ┌──────────┐    ┌──────────┐
   │ Idle │───▶│ Listening│───▶│ Thinking │───▶│ Speaking │
   └──────┘    └──────────┘    └──────────┘    └──────────┘
       ▲             │              │               │
       │             └──────────────┴───────────────┘
       │                            │
       │                       ┌────▼────┐
       └───────────────────────│  Error  │
                               └─────────┘
```

| Transition | Trigger | Duration | Motion |
|---|---|---|---|
| Idle → Listening | Session opens / mic opens | `micro` | Colour role change, scale settles at 1.0 |
| Listening → Thinking | Endpoint detected, model request in flight | `short` | Amplitude scale releases to 1.0, inner arc begins rotating |
| Thinking → Speaking | First TTS audio frame received | `micro` | Arc dissolves, amplitude binding switches to TTS |
| Speaking → Listening | TTS stream complete, mic reopens | `micro` | Amplitude binding switches back |
| Speaking → Listening (barge-in) | Caller speaks over the agent | **≤ 20 ms** | Immediate. No transition animation — barge-in is one frame (ADR-0011) |
| any → Error | Session fault | `short` | Single shake ±4dp, 3 cycles, settle to Idle |
| any → Idle | Session ends | `medium` | Fade to `status.telephony.subtle`, static |

### The honesty contract — Invariant U8

> **The orb animates from real amplitude data or it does not animate.**

Enumerated, because this is the rule most likely to be violated by a
well-meaning implementation:

| Condition | Orb behaviour |
|---|---|
| `amplitude == null` (no data available) | **Static** at the current state's colour. Never a synthesised pulse |
| Amplitude stream stalls > 500 ms | Freeze at last value, then **static**. Not a decay animation — a decay implies fading audio, which is a claim |
| Thinking, but no model request actually in flight | **Illegal state.** The thinking arc renders only while a request is genuinely outstanding |
| Session connected but silent (nobody talking) | `Listening`, amplitude near zero, orb near 1.0 scale. Truthful and nearly static |
| Reduced motion enabled | Static in every state, with the state's **text label** rendered adjacent ([`07 §7.6`](../design/07-states.md)) |
| Screen is not visible | Animation stops entirely. No off-screen work |

### Rendering constraints

- **Non-anthropomorphic by construction:** a circle and an arc. No eyes, no
  mouth, no gradient that reads as a face, no tilt, no bounce, ever.
- Scale range **1.00–1.08**. Beyond that it reads as a bouncing ball.
- Amplitude is smoothed with a one-pole filter, attack faster than release, so
  the orb does not flicker on plosives.
- Sampled at **30 Hz**, interpolated to display refresh
  ([`05 §5.6`](../design/05-motion.md)).
- Transform and opacity only. The colour role changes are step changes at state
  boundaries, not animated transitions — animating colour is prohibited on the
  hot path.

### Accessibility

The orb carries a `stateDescription`, not a `contentDescription` — the label
changes with state and screen readers should re-announce state, not the element.
Announcements are `polite` for Idle/Listening/Thinking/Speaking and
**`assertive`** for Error. The orb is never the sole indicator of any state; the
adjacent status text is always present, not only under reduced motion.

---

## 2.2 AI thinking behaviour

"Thinking" is a claim about the system's internal state, and Invariant U8 makes
it a factual one.

### What "thinking" means and does not

| Renders thinking | Does not render thinking |
|---|---|
| A model request is in flight | Network request queued but not sent |
| Tool call executing | Waiting for the caller to speak |
| Fraud scoring in progress before a verdict renders | TTS synthesising (that is `Speaking` — audio is coming) |

### Presentation by context

| Context | Treatment |
|---|---|
| **Live screening** | Orb thinking state. **No text.** The user is watching a live conversation and a "Thinking…" label competes with the transcript |
| **Call detail, verdict pending** | Inline skeleton in the verdict slot, with "Assessing" text after 2 s. Never a spinner |
| **Personal Assistant** | Orb thinking state + a single-line status that names the actual work when we know it: "Searching your calls", "Reading a transcript". Generic "Thinking" only when we do not |
| **Console, model operation** | Determinate progress where the operation has stages; the stage name is shown |

### The disclosure rule

When the assistant used a tool to answer, the surface says which class of
tool — "Searched 90 days of calls", "Read the call from Tuesday". Not the
reasoning, not the prompt, not the chain of thought. **The user gets to know
what was consulted, not what was thought.**

Chain-of-thought text is never rendered to any user on any surface. It is
enabled on tool-calling tiers because disabling it silently drops tool calls
(Invariant I3), which is an engineering fact, not a feature.

### Latency thresholds

| Elapsed | Treatment |
|---|---|
| 0–900 ms | Orb thinking only. This is the p50 budget (ADR-0011) — normal operation, no commentary |
| 900 ms – 2.5 s | Orb + status text where the context allows it |
| > 2.5 s | Past the p99 ceiling. Something is wrong: the surface says so and offers the relevant escape (takeover on a screening; retry on the assistant) |

**We never render a fake progress bar for model latency.** We do not know how
long it will take, and pretending we do is the same lie as the fake orb pulse.

---

## 2.3 Voice streaming behaviour

How partial results reach the screen.

### The three streams

| Stream | Source | Cadence | Rendering |
|---|---|---|---|
| **ASR interim** | `asr-gateway`, pre-endpoint | 100–300 ms | `content.tertiary`, appended in place, **not announced** to screen readers, **never persisted** |
| **ASR final** | Post-endpoint | Per turn | Promotes to `content.primary`, becomes a `TranscriptTurn`, announced `polite` |
| **TTS output** | `tts-gateway`, sentence-level | Per sentence | Assistant turn appears **sentence by sentence as it is spoken**, not all at once |

### Interim text rules

Interim ASR is the most-abused affordance in voice UI. It creates the sensation
of responsiveness and the reality of instability, because it rewrites itself.

| Rule | Reason |
|---|---|
| Rendered in `content.tertiary`, never `primary` | Visually subordinate to confirmed text |
| **Never announced** to screen readers | Every revision would re-announce; the surface becomes unusable |
| **Never in a notification** | A notification that rewrites itself under the user's eyes reads as a malfunction |
| Never copied by "select all" | It is not part of the record |
| Replaced atomically by the final, no crossfade | A crossfade between two texts is unreadable |
| Discarded on session end | It never becomes a transcript turn |
| Suppressed entirely below a stability threshold | Text that has revised 4+ times in 1 s is noise; hold and wait |

### Turn arrival animation

New completed turn enters with **fade + 8 dp rise**, `duration.short`,
`easing.decelerate`. No stagger — turns arrive one at a time, so stagger has
nothing to sequence.

### Auto-scroll

The behaviour users notice only when it is wrong.

| Condition | Behaviour |
|---|---|
| User is at the bottom | Auto-scroll to keep the newest turn visible, animated, `duration.short` |
| User has scrolled up | **Do not scroll.** A "N new" pill appears at the bottom edge; tapping it scrolls to bottom |
| User scrolls back to bottom | Auto-scroll resumes silently, pill disappears |
| New turn is taller than the viewport | Scroll to the turn's **top**, not its bottom |

**Scroll position is never stolen.** A user reading an earlier turn while the
call continues is doing the most important thing this product supports.

### Degradation

| Failure | Behaviour |
|---|---|
| ASR stream stalls > 2 s | Connection chip in the header: "Transcript delayed". Existing content stays. Orb reflects the real state, which is probably still `Listening` |
| ASR stream dies | "Transcript unavailable — the call is still being screened." Takeover remains available. **This is the case Invariant U6 makes survivable in reverse**: audio-off users lose the transcript, so takeover must not depend on it |
| TTS stream dies | Screening ends; session orchestrator handles the call. The surface reports the outcome, not the mechanism |
| WebRTC live-listen fails | Silently degrades to transcript-only, with a one-line notice. Never blocks the screen ([`09 §LiveListenToggle`](../design/09-components.md)) |

---

## 2.4 Transcript behaviour

The product's record and its accessibility surface.

### Structure

Both parties render **left-aligned in reading order**. Not chat bubbles
([`09 §Transcript`](../design/09-components.md)) — a screening is a record of a
conversation the subscriber was not in, and chat alignment implies peers.

```
  ┌─────────────────────────────────────────┐
  │ Announcement                    0:00    │   ← deterministic, I1, distinct
  │ This call is being answered by an       │     treatment: surface.sunken,
  │ automated assistant and is being        │     no AiBadge (not model output)
  │ recorded.                               │
  ├─────────────────────────────────────────┤
  │ Caller                          0:04    │   ← surface.sunken
  │ "Yeah hi, I'm calling from the bank     │     quoted, attributed
  │ about your account, there's been some   │
  │ unusual activity"                       │
  ├─────────────────────────────────────────┤
  │ ⬡ Assistant                     0:09    │   ← surface.default + AiBadge
  │ Which bank are you calling from, and    │
  │ can I take a reference number?          │
  ├─────────────────────────────────────────┤
  │ ⚠ Possible fraud · low confidence       │   ← inline verdict marker at the
  │   Claimed bank identity, refused        │     turn that produced it
  │   verification                    ⓘ     │
  └─────────────────────────────────────────┘
```

### Contract

| Aspect | Behaviour |
|---|---|
| Type | `body.lg` — the most generous in the system. This is the reading surface |
| Line length | Capped ≈ 66 characters even on wide screens |
| Speaker | A text label plus spacing. **Never colour alone** |
| Caller turns | `surface.sunken`, **in quotation marks**, attributed. Untrusted content, marked as such (Invariant U10) |
| Assistant turns | `surface.default` + `AiBadge` on every turn without exception |
| Announcement | Its own treatment. Not an assistant turn — it is deterministic and model-free (Invariant I1), and giving it an `AiBadge` would misrepresent it |
| Timestamps | Relative to call start, `numeric.sm`, `tnum` |
| Selection | Fully selectable and copyable. Copy includes speaker labels and timestamps |
| Verdict markers | Inline, at the turn that produced them, tappable to the evidence |
| Live region | `polite`, **completed turns only** |
| Interim | `content.tertiary`, not announced, not selectable |

### Deep-linked highlight

Arriving from a `FraudBadge` ([`09 §FraudBadge`](../design/09-components.md)):

1. Scroll the target turn to **one third from the top** — not centre, not top;
   the preceding turn is context the user needs
2. Highlight with `status.fraud.subtle` background, `duration.medium`,
   `easing.decelerate`
3. Hold for 2 s, then release over `duration.long` to normal
4. Announce the turn `assertive` on arrival, once
5. The highlight does not return on scroll — it was a pointer, not a state

### Long transcripts

- Virtualised beyond 40 turns
- A **jump-to** control appears beyond 20 turns: first turn, first verdict,
  last turn
- Search within transcript from the top app bar, matches highlighted by weight
  not colour ([`01 §1.7`](01-cross-surface-conventions.md))

### Export

A transcript exports as plain text with speaker labels, absolute timestamps, the
verdict and its confidence, and a footer stating it was produced by an automated
assistant. **The footer is not removable.** An exported transcript is evidence
in someone's dispute, and it must not be able to masquerade as a human record.

---

## 2.5 Timeline behaviour

`CallTimeline` ([`09 §CallTimeline`](../design/09-components.md)) — the
structural view of one call, complementary to the transcript's conversational
view.

### Events

In order, with their node treatment:

| Event | Node colour | Always present |
|---|---|---|
| Call received (handset rang) | `content.tertiary` | Yes |
| Pre-filter decision | `content.tertiary` | Yes |
| Forwarded to screening | `status.telephony` | Only if screened |
| Answered | `status.telephony` | Only if screened |
| **Announcement played** | `status.telephony` | Only if screened. **Never omitted** — it is the lawful basis (I1) |
| Recording started | `status.recording` | Only if recording |
| Conversation turns | `content.tertiary`, collapsed into one node with a count | Only if screened |
| Verdict | Semantic to the verdict | Only if assessed |
| Escalation (fraud, emergency) | `status.fraud` / `status.emergency` | Conditional |
| Takeover | `status.telephony` | Conditional |
| Transfer | `status.telephony` | Conditional |
| Recording stopped | `status.recording` | Only if recording |
| Ended | `content.tertiary` | Yes |
| Outcome (allowed / declined / blocked / voicemail) | Semantic | Yes |

### Behaviour

| Aspect | Behaviour |
|---|---|
| Default | **Collapsed** beyond 10 events, showing first, verdict, escalation and last |
| Expansion | `duration.medium`, height animation is the one permitted exception and only because the timeline is short and off the hot path |
| Turn nodes | Collapsed into a single "12 exchanges" node; expanding jumps to the transcript rather than inlining turns. Two views of the same content in one scroll is redundancy, not richness |
| Timestamps | Relative to call start; absolute available on tap |
| Live | During a screening the timeline appends in real time, newest at the bottom, with the same auto-scroll rules as the transcript |
| Accessibility | An ordered list. Each node announces "event name, at 12 seconds". Not a graphic |

### Why the timeline exists separately from the transcript

The transcript answers *what was said*. The timeline answers *what happened* —
including everything that was not speech: the announcement, the recording
boundaries, the verdict moment, the takeover. In a dispute, in a support call,
and in a fraud review, the timeline is the artefact that matters, and burying it
inside the transcript would make it unusable for exactly those cases.

---

## 2.6 Caller card behaviour

`CallerCard` ([`09 §CallerCard`](../design/09-components.md)) is the most-read
surface in the product. Its **hierarchy is fixed**: who, what, risk, when,
number.

### States

| State | Rendering |
|---|---|
| **Screening live** | Pulsing `status.telephony` left rail, live duration timer, summary slot shows the latest completed caller turn, truncated to one line. Card is elevated above the feed and does not scroll away |
| **Screened, assessed** | Standard. Summary from the model with `AiBadge`, `RiskIndicator` if assessed |
| **Screened, verdict pending** | Summary present, risk slot **skeleton**, not "Unknown". A pending verdict and an unassessable one are different facts |
| **Screened, not assessable** | Risk slot shows `Unknown` — "Not assessed" — with a reason on tap. Honest, not blank |
| **Pre-filtered, known contact** | Minimal: name, time, "Rang through". No summary, no risk — nothing was screened, so claiming otherwise would be false |
| **Pre-filtered, blocked** | Name or number, "Blocked", reason, unblock action |
| **Verified business** | Business name and `VerifiedMark`, logo where supplied. The **only** case where a caller-supplied name renders at `title` weight, because it came from a verified registry, not from the caller's mouth |
| **Missed, not screened** | Number, time, "Not screened", reason (forwarding lapsed, offline, plan limit). Links to the fix |

### The colour rule

**At most one coloured element per card.** If a risk is present, nothing else on
the card is coloured — not the business mark, not the premium badge, not the
duration ([`09 §CallerCard`](../design/09-components.md)).

### Interaction

| Gesture | Action |
|---|---|
| Tap | Call Detail |
| Long press | Context sheet: Block, Add contact, Report, Share transcript, Delete |
| Swipe right | Allow / add to contacts. `haptic.confirm` |
| Swipe left | Block. `haptic.reject`. **Undoable for 5 s** via snackbar |
| Tap risk indicator | Transcript, deep-linked to the flagged turn |

**Swipes are shortcuts, never the only path.** Every swipe action is also in the
long-press sheet, because swipe is unavailable to Switch Access and unreliable
at 200% text.

### Live card promotion

When a screening starts while the user is on the Calls surface, the card does
not simply appear at the top of the list. It:

1. Inserts at the top with fade + 8 dp rise, `duration.short`
2. The list below settles down, `spring.gentle`, no reflow flash
3. `haptic.tick`
4. Announces `polite`: "Screening a call from an unknown number"

If the user is scrolled down, the card does **not** scroll them to the top. A
"Screening now" pill appears at the top edge instead.

---

## 2.7 Call transfer behaviour

Two distinct operations, routinely conflated, with different mechanics and
different UX.

### Takeover — the subscriber joins the call

The core interaction. The subscriber's handset is called back and bridged to the
live leg, and the agent withdraws.

```
  tap "Take call"
        │
        ▼
  ┌──────────────────────────────────────┐
  │ Immediate: haptic.heavy              │  0 ms
  │ Button → "Connecting…", width held   │
  │ Orb → Speaking (agent hands off)     │
  └──────────────────────────────────────┘
        │
        ▼
  ┌──────────────────────────────────────┐
  │ Agent speaks the handoff line to     │  ~0.3–1.5 s
  │ the caller (deterministic, not model │
  │ generated). Transcript shows it.     │
  │ Handset begins to ring.              │
  └──────────────────────────────────────┘
        │
        ▼
  ┌──────────────────────────────────────┐
  │ Subscriber answers → system in-call  │
  │ UI takes over. Our surface exits to  │
  │ Call Detail underneath.              │
  └──────────────────────────────────────┘
```

| Rule | Detail |
|---|---|
| **No confirmation** | Invariant U3. Time-critical |
| Haptic | `haptic.heavy` at tap, before any network work |
| Button state | Loading with **preserved width** ([`09 §9.2`](../design/09-components.md)). Never a jumping button |
| Handoff line | Deterministic, not model-generated. The caller must not be told something invented at the moment of transfer |
| Failure | "Couldn't connect you — the call is still being screened." Screening continues. The button returns to armed state. **The call is never dropped by a failed takeover** |
| Caller-side | Brief hold; the caller is told a person is joining |
| Timeout | 8 s. Beyond that, fail as above |
| Analytics | `android.screening.takeover_engaged` with `time_to_action_ms` from screening start — the product's key metric |

### Transfer — the call goes somewhere else

Business-tier only. The agent routes the call to a person, a team, or voicemail
per configured rules.

| Aspect | Behaviour |
|---|---|
| Initiated by | The agent, per routing rules configured in the Business Portal. **Never by the caller's request alone** — a caller who asks to be transferred to "the manager" is untrusted input (Invariant I4) |
| Subscriber-visible | Timeline node, transcript marker, and the destination in the call record |
| Announced to caller | Deterministically: "I'm connecting you now." |
| Failure | Falls back to the configured fallback — voicemail or message-taking. Never a dropped call, never a dead-air hold |
| Portal record | Transfer target, latency, and answer/no-answer outcome, for routing analytics |

### Decline and block from the live surface

| Action | Behaviour |
|---|---|
| **Decline** | Agent closes politely and deterministically, call ends. `haptic.reject`. Undoable? **No** — the call is over. The action therefore appears as `Secondary`, not adjacent to `Take call`, and is not swipe-reachable |
| **Block** | Decline plus add to blocklist. Confirmation is **not** a dialog — it is a 5 s undo snackbar, because a dialog mid-screening costs the takeover window |

---

## 2.8 Animation storyboard

The sequences that define how the product feels, in tokens from
[`05-motion.md`](../design/05-motion.md).

### S1 · Cold start → Calls

```
  0 ms     Splash: static wordmark on surface.default. No animation.
           (An animated splash on a 60 Hz mid-range device is the first
            frame drop the user sees, and first impressions of performance
            are disproportionately sticky.)
  ~200 ms  Wordmark cross-dissolves out, duration.short
  ~250 ms  Calls surface: top app bar and bottom nav render immediately,
           list renders as skeleton
  on data  Skeleton → content, no crossfade. Items already in the same
           position simply resolve. First 5 items stagger 20 ms.
```

**Target: content visible under 500 ms from tap, on a mid-range device, cold.**

### S2 · Screening starts while the app is closed

```
  0 ms     Notification posts, channel screening_live
           Sound: screening tone (§2.9). Haptic: system default for the channel
  0 ms     Heads-up notification, showing caller number and a live timer
  ~1 s     Notification body populates with the first completed caller turn
  1 Hz     Body updates in place. Never a new notification.
  tap      → Live Screening, entering with shared-Z (scale + fade),
             duration.long, easing.emphasized — a full-screen takeover
```

### S3 · Screening starts while the app is open, on Calls

Described in [§2.6](#26-caller-card-behaviour). Insert, settle, tick, announce.
No navigation, no modal, no interstitial. **The user is not interrupted; they
are informed.**

### S4 · Live Screening → takeover → in-call

```
  tap      haptic.heavy immediately
  0 ms     Button label → spinner, width preserved
  0 ms     Orb: whatever it truly is. Not forced to any state.
  ~500 ms  Handoff line appears in the transcript as an assistant turn
  ~1 s     Handset rings — system incoming-call UI takes the screen
  answer   System in-call UI. Our surface is beneath it.
  hang up  Returns to Call Detail for that call, entered with a fade,
           duration.medium, scrolled to the outcome
```

### S5 · Fraud verdict arrives mid-screening

The product's most consequential animation. It must be **noticed** without
being alarming — Principle 1 of the design system: confidence is expressed by
not raising our voice.

```
  0 ms     haptic.heavy (once, and only for medium-or-higher confidence)
  0 ms     RiskIndicator badge appears: scale 0.8 → 1.0 + fade,
           duration.short, easing.decelerate
  0 ms     Screen reader: assertive announcement, once
  0 ms     Inline verdict marker appears in the transcript at the
           producing turn, same entrance
  ~150 ms  The card's left rail transitions from telephony to fraud colour.
           Step change at the state boundary, not an animated colour tween.
  never    No full-screen takeover. No modal. No sound beyond the haptic.
           No red flash, no shake, no pulsing border.
```

**Low-confidence verdicts do not haptic and do not re-alert.** They render, and
they say "Possibly".

### S6 · Onboarding forwarding activation

The tensest moment in onboarding — the user is about to let us dial a code that
reconfigures their telephony.

```
  Explainer screen: static. No animation. Reading, not delighting.
  tap "Set up forwarding"
        │
  ~0 ms  Full-screen state: "Dialling your carrier…"
         Indeterminate, with the actual MMI string shown in numeric.md.
         (Showing the string is a trust act. It is also how a user
          reports what happened when it fails.)
        │
  handoff System dialer takes over. We are gone from the screen.
        │
  return  "Checking it worked…" — indeterminate, then interrogation
        │
  ┌─────┴──────┐
  ▼            ▼
 verified    not verified
 │            │
 │            └─ Carrier-specific manual instructions.
 │               No blame, no retry-loop.
 ▼
 Success: check mark draws in, duration.medium, easing.decelerate.
 haptic.confirm. Then auto-advance after 1200 ms — the user does
 not need to acknowledge a success they can see.
```

### S7 · Empty → first screened call

The single moment that proves the product works. It gets more motion budget
than anything else outside the screening path.

```
  First card inserts: fade + 12 dp rise, duration.medium, easing.decelerate
  Empty-state illustration exits first: fade + 4 dp fall, duration.short
  haptic.tick
  A one-time inline note appears beneath the card explaining what
  they are looking at, dismissible, never shown again.
```

### S8 · Screen transitions, universal

| Transition | Motion | Token |
|---|---|---|
| Push | Shared X, +30 dp, fade | `medium` / `standard` |
| Pop | Reverse, faster | `short` / `accelerate` |
| Tab switch | Shared X, no depth | `short` |
| Sheet | Y from bottom + scrim | `medium` / `spring.gentle` |
| Dialog | Scale 0.96 → 1.0 + fade | `short` / `decelerate` |
| Full-screen takeover (Live Screening) | Shared Z, scale + fade | `long` / `emphasized` |
| Reduced motion | All of the above → instant | `instant` |

---

## 2.9 Sound design

**The product is nearly silent.** It lives on a phone, which is already the most
sound-permissive object a person owns, and it exists to reduce the number of
times that object demands attention.

### The complete inventory

Four sounds. There is not a fifth.

| Sound | Fires | Character | Duration | Respects |
|---|---|---|---|---|
| **Screening started** | `screening_live` notification | Two-tone, ascending, soft attack. Distinct from any stock Android tone | ≤ 600 ms | Ringer mode, DND, channel setting |
| **Fraud alert** | `fraud_alert`, medium+ confidence | Three-tone, descending, harder attack. Recognisable at low volume in a pocket | ≤ 800 ms | Ringer mode, DND unless user opted to bypass |
| **Takeover connecting** | Takeover engaged | Single soft tone, confirming the tap registered before the handset rings | ≤ 200 ms | Ringer mode. **Suppressed** if the media stream is active |
| **Screening ended** | Screening completes with no alert | **Silence.** Listed here to record that the decision was made | — | — |

### Rules

| Rule | Reason |
|---|---|
| **No UI sound effects.** No tap sounds, no swipe sounds, no success chimes | The phone's own keypress feedback is the user's setting, not ours |
| The two alert sounds are **maximally distinguishable** from each other | A user must know which fired from the next room without looking |
| Neither resembles a ringtone, a message tone, or a system alarm | Confusion with the ringtone is a specific, testable failure |
| **Never plays over an active call** | Including a call the user took over |
| **Never plays through a Bluetooth call channel** | It would sound to the caller like our system is malfunctioning |
| Media volume, not ring volume, for the takeover tone | It is feedback, not an alert |
| Ringer silent → no sound at all, alerts included | The system setting wins. Haptics still fire |
| Every sound has a **haptic and a visual equivalent** | Invariant U6, and deaf users are core users |
| Sounds are **not customisable** | Four sounds with fixed meanings are learnable. A customisable alert is a meaningless one |

### Voice output

The Screening Agent's voice is heard by **callers**, not subscribers, except
during opt-in live listen. It is not a UX sound and is not in this inventory —
it is a product characteristic configured in the Assistant surface. The
Personal Assistant's voice plays through the media stream, is duckable, and
stops on any other audio focus request without argument.

---

## 2.10 Haptics

Tokens in [`05 §5.8`](../design/05-motion.md). This is the assignment.

| Event | Token | Notes |
|---|---|---|
| Button press | `tick` | Primary and Danger only. Not Tertiary or Ghost |
| Toggle | `click` | |
| Sheet snap | `tick` | |
| Allow / add contact / swipe right | `confirm` | |
| Block / decline / swipe left | `reject` | |
| **Takeover engaged** | `heavy` | Fires at tap, before network work |
| **Fraud verdict, medium+ confidence** | `heavy` | Once per verdict. Never repeats |
| Forwarding verified | `confirm` | The onboarding payoff moment |
| Forwarding lapsed detected | `reject` | Once, at detection |
| Emergency handoff | `heavy` | |
| Error | `reject` | Blocking and destructive tiers only |
| Undo snackbar action | `tick` | |
| Scroll, hover, typing, list stagger, tab change | **none** | |

### The scarcity rule

**Fewer than 20% of interactions in the product produce a haptic.** Vibrating
on everything trains the user to ignore vibration, so that the fraud alert — the
one that matters — lands as background noise. The haptic budget is spent on
consequence, not on feedback.

**The system setting is absolute.** If haptic feedback is disabled we emit
nothing, including the fraud alert. There is no override and no persuasion
screen ([`05 §5.8`](../design/05-motion.md)).

---

## 2.11 Micro-interactions

Beyond the base matrix in [`05 §5.5`](../design/05-motion.md), the ones specific
to this product.

| Interaction | Behaviour |
|---|---|
| **Live duration timer** | `tnum`, updates at 1 Hz, no animation between values. A tweening timer is unreadable and costs a frame every second |
| **Risk badge first appearance** | Scale 0.8 → 1.0 + fade, `short`, `decelerate`. Never a pulse, never a repeat |
| **Recording dot** | 1 Hz opacity 1.0 → 0.4. Under reduced motion, static with the text label. **Never removed** ([`05 §5.6`](../design/05-motion.md)) |
| **Undo snackbar** | Rises, holds 5 s, exits. A hairline progress track shows the remaining window — the only determinate progress in the product that is decorative, justified because the user is deciding against a clock |
| **Consent toggle** | Thumb slide `spring.snappy`, then the consequence text below updates with fade + 4 dp, `short`. The consequence updating *after* the toggle is deliberate: the user sees cause then effect |
| **Blocklist add** | Row collapses, `short`, list settles `spring.gentle`. No flash, no colour change |
| **Pull to refresh** | Standard. Spinner is one of the three sanctioned spinner uses ([`07 §7.3`](../design/07-states.md)) |
| **Number formatting while typing** | Reformats on group boundaries only, never mid-group. `tnum` prevents jitter ([`09 §PhoneField`](../design/09-components.md)) |
| **OTP auto-fill** | Fields populate with a 40 ms per-digit stagger, then auto-submit after 300 ms. The stagger exists so the user can see it happened and is not confused by an instant jump |
| **Tab reselect** | Scrolls that tab's list to top, `medium`. If already at top, no-op — never a bounce |
| **Search result update** | Results replace in place, no exit animation. Animating out results the user is reading is the most common search-UI mistake |
| **Verdict pending → resolved** | Skeleton dissolves, badge enters. No layout shift — the skeleton occupied the badge's exact final size ([`07 §7.4`](../design/07-states.md)) |

---

## 2.12 Conversation UX — the Personal Assistant

The subscriber's conversational surface over their own call history. Distinct in
every way from the Screening Agent (Principle 3).

### What it is for

Answering questions about calls that have already happened, and performing a
narrow set of actions on them. It is not a general chatbot, and it does not
pretend to be.

| In scope | Out of scope |
|---|---|
| "Did the courier call?" | General knowledge |
| "What did the bank say on Tuesday?" | Anything outside the user's own data |
| "Block everyone who called about loans" (proposes, does not execute) | Making calls |
| "Read me the last screening" | Changing consents, payments, or forwarding |
| "How many spam calls this week?" | Anything irreversible without confirmation |

### Interaction model

| Aspect | Behaviour |
|---|---|
| Input | **Text by default.** Voice by press-and-hold on the orb, only after `RECORD_AUDIO` is granted, requested at that moment |
| Voice | Press-and-hold, not tap-to-toggle. Hold-to-talk cannot be left accidentally open, which matters for a microphone |
| Release | Ends capture immediately. Releasing before 400 ms is treated as a mis-tap and discarded, with a hint |
| Output | Streams token by token, `content.primary`, with `AiBadge` on the message |
| Citations | Every claim about a call links to that call. **A claim with no citation is not rendered** — Principle 2 |
| Actions | Proposed as buttons the user taps, never executed inline. "Block these 4 numbers? [Review] [Block all]" |
| Destructive actions | Always reviewed as a list first. The assistant never blocks, deletes or reports without an explicit list-level confirmation |
| History | Ephemeral per session by default. Persisted only if the user pins a conversation. There is no infinite chat log to mine or leak |
| Offline | Unavailable, with a stated reason. Local search remains available and is offered as the alternative |

### The trust boundary, stated in the interface

The Assistant surface carries a permanent, quiet line in its empty state and in
its settings: **"This assistant can see your calls. The assistant that answers
your phone cannot."**

That sentence is the entire security model of Invariant I4 expressed in eleven
words, and the user needs it, because "an AI that reads my transcripts" and "an
AI that talks to strangers" being the same thing is the exact fear this product
must not confirm.

---

## 2.13 Rules

1. **The orb animates from real data or stays static.** No exceptions, no
   fallbacks, no "just for the demo".
2. **Thinking renders only while a request is genuinely in flight.**
3. **Interim ASR is never announced, never persisted, never in a notification.**
4. **Scroll position is never stolen** from a user who scrolled.
5. **Caller text is quoted and attributed, everywhere, always.**
6. **The announcement is not an assistant turn** and never carries an `AiBadge`.
7. **Takeover has no confirmation** and haptics before it networks.
8. **A failed takeover never drops the call.**
9. **Fraud verdicts get one haptic and no modal.** Low confidence gets neither.
10. **Four sounds exist.** Adding a fifth is a design-system change.
11. **Under 20% of interactions produce a haptic**, and the system setting is
    absolute.
12. **The two agents never share a surface**, and the interface says which is
    which in plain words.
