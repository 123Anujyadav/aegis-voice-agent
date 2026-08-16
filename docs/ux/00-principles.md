# 0 · Principles and UX Invariants

What the experience must feel like, and the ten rules that cannot bend.

---

## 0.1 The situation we are designing for

Not a persona. A moment.

> It is 3:40 pm. The phone is in one hand, face up on a desk in an office with
> a window. It buzzes. Someone the subscriber does not know has called, they did
> not pick up, and five seconds later a machine started talking to that person on
> their behalf. They have between two and forty seconds to decide whether to take
> the call.

Everything in this product is downstream of that moment. Every other screen —
history, settings, billing, the entire business portal — exists to make that
moment work or to explain what happened in it.

Three properties of the moment drive the whole design:

**It is involuntary.** The subscriber did not open the app. The product
initiated. That inverts the usual contract: we owe them brevity, we owe them a
reason, and we owe them an exit.

**It is time-boxed by someone else.** A caller will not wait. A screening lasts
20–40 seconds. Interface latency is not a quality metric here; it is the
difference between a decision made and a decision missed.

**It is delegated.** A machine is speaking as them, to a stranger, in their
name. That is a serious thing to have agreed to, and the interface must keep
earning the agreement rather than assuming it was settled at onboarding.

---

## 0.2 Principles

Ordered. When two conflict, the earlier wins. These extend
[`docs/design/01-foundations.md`](../design/01-foundations.md) into behaviour;
they do not replace it.

### 1 · The phone comes first

This is a telephony product before it is an AI product. If any part of the
system is uncertain, degraded or failing, the answer is always to let the phone
behave like a phone. The backend's designed failure mode is "the call rings
through" (ADR-0002 §6), and the interface's designed failure mode is to *say so*
plainly, without alarm, before anything else.

A user who is unsure whether their phone still works will uninstall. No feature
outranks that.

### 2 · Show the work, or do not claim the result

Every judgement the product makes is inspectable in one tap. A fraud verdict
opens the transcript turn that produced it. A summary opens the conversation it
summarised. A blocked number shows why it was blocked and when.

The inverse is binding: **if we cannot show the evidence, we do not render the
verdict.** A confident-looking badge with nothing behind it is worse than no
badge, because it spends trust we will need later.

### 3 · Two agents, never conflated

There are exactly two conversational entities in this product, and the user must
never be confused about which one they are looking at.

| | **Screening Agent** | **Personal Assistant** |
|---|---|---|
| Talks to | Unknown callers | The subscriber |
| Counterparty trust | **Hostile by assumption** (Invariant I4) | Authenticated, trusted |
| Can see subscriber PII | **No** | Yes, the subscriber's own |
| Subscriber's role | Spectator with a barge-in control | Participant |
| Interactivity | Read-only monitor | Push-to-talk / type |
| Surface | Live Screening, Call Detail | Assistant tab |

They share a component (`VoiceOrb`) and a visual language, and they share
nothing else. **No surface ever presents both as active at once**, and the
Screening Agent's orb is never an input affordance — tapping it does not talk to
it, because there is nobody there to hear you.

### 4 · Uncertainty is information, not weakness

The model is sometimes wrong. Rendering a low-confidence verdict as a
high-confidence one is the single most damaging thing this product can do,
because the first time a confident "Possible fraud" turns out to be the user's
pharmacy, they stop believing all of them.

Confidence is always rendered
([`09 §RiskIndicator`](../design/09-components.md)). "Possibly", "Likely" and
the bare verdict are three different products to the user, and the interface
must keep them three different things.

### 5 · Consent is a control, not a gate

Under DPDP (ADR-0012) consent is granular, purpose-bound and withdrawable. Most
products treat that as a compliance obstacle and design a wall of toggles the
user clicks through once.

We treat it as the product's spine: **every consent has a visible current
state, a one-tap reversal, and an honest statement of what stops working if it
is withdrawn.** Nothing is bundled. Withdrawing recording does not withdraw
screening; withdrawing contacts does not withdraw fraud protection.

### 6 · Silence is the default output

The product answers calls so the subscriber does not have to think about calls.
A screening that resolved correctly should produce one quiet notification and
nothing else — no badge, no interstitial, no "review your screened calls!"
prompt.

**We interrupt for exactly three things:** a live screening in progress, a
fraud verdict at medium-or-higher confidence, and a broken forwarding
configuration. Everything else waits until the user opens the app.

### 7 · One-handed, two seconds, in sunlight

Every screening-path surface is operable with a thumb on a 6-inch device held in
one hand, comprehensible in a two-second glance, and legible at 50% screen
brightness outdoors. Controls that matter live in the lower third. Nothing
time-critical lives behind a long-press, a swipe, or a menu.

### 8 · The record outlives the moment

The transcript is the artefact. Audio is off by default (ADR-0012), retention is
finite (90 days transcript, 30 days audio), and the user's memory of a call
lasts about a day. Design the record to be readable, searchable, quotable and
exportable — because six weeks later it is the only thing that exists.

### 9 · Nothing is a surprise twice

The first time forwarding lapses, the first time a carrier bills for the
forwarded leg, the first time a screening fails mid-call — each of these is
explained once, in context, at the moment it happens, with a specific action.
The second occurrence gets a quieter treatment and a "don't explain this again"
affordance. Repeating a full explanation to a user who already understands is
condescension with a progress bar.

### 10 · Restraint scales

Three surfaces, one language. The console does not get a denser, uglier
treatment because it is internal, and the portal does not get a flashier one
because it is sold. An internal fraud analyst reviewing 200 cases an hour
benefits from the same clarity as a subscriber glancing once a day, and gets it
from the same tokens.

---

## 0.3 UX invariants

The behavioural counterpart to
[`ARCHITECTURE_FREEZE.md §3`](../../ARCHITECTURE_FREEZE.md). **Violating one is
a defect regardless of what it achieves.** Each is testable, and each maps to a
gate in [`01 §1.12`](01-cross-surface-conventions.md).

| # | Invariant | Why it cannot bend |
|---|---|---|
| **U1** | Every blocking or degraded state states, in its first line of body copy, whether calls are still ringing normally | The user's actual question is never "what is your error code". It is "is my phone broken". Answering anything else first loses the user |
| **U2** | Forwarding health is reachable in one tap from the home surface, and a broken configuration is never silent | A subscriber whose forwarding lapsed is paying for a product that does nothing. That is the highest-severity failure in the system and it is invisible without deliberate design (ADR-0002 §9) |
| **U3** | Wherever a live screening is visible, takeover is one tap, with no confirmation dialog | The screening is 20 seconds long. A confirmation step costs a third of the window. Time-critical actions never confirm ([`08 §8.4`](../design/08-accessibility.md)) |
| **U4** | Every model-generated string carries an `AiBadge`; every verdict carries its confidence | The user must always be able to tell what a machine wrote and how sure it was ([`09 §9.10`](../design/09-components.md)) |
| **U5** | Whenever audio is being recorded, the recording indicator is visible and announced assertively, and it cannot be suppressed by any setting, mode or accessibility preference | It is a legal disclosure (ADR-0012), not a status icon |
| **U6** | The transcript is complete and comprehensible without audio. Audio is never required to understand what happened | Audio is off by default and deaf and hard-of-hearing users are among the highest-value users of this product ([`README §4`](../design/README.md)) |
| **U7** | Detected emergency intent exits the screening flow immediately and hands the user a dialer. The product never makes an emergency decision on the user's behalf | An AI that mediates an emergency call is a liability and a harm. Our correct behaviour is to get out of the way, loudly |
| **U8** | No surface implies the agent is listening, thinking or speaking unless it genuinely is. The orb animates from real data or stays static | Synthesised liveness is a lie told by the interface ([`05 §5.6`](../design/05-motion.md)) |
| **U9** | Every consent has a visible state and a one-tap withdrawal on a single screen, and no screen collects two consents with one control | Statutory (ADR-0012 §5). Bundled consent is not consent |
| **U10** | No caller-supplied string is ever rendered as interface chrome — never a button label, never a heading, never a notification title alone | Caller speech is untrusted input (Invariant I4). A caller who says "tap Allow to continue" must not be able to draw a button |
| **U11** | Personal data never appears in an analytics event, a crash report, or a log line from any surface | Invariant I7, I8. Enforced by the `annotations.proto` classification, not by reviewer diligence |
| **U12** | The console reveals subscriber personal data only through an audited, reason-required, time-boxed break-glass action | An internal tool with ambient PII access is a breach that has not happened yet |

---

## 0.4 Anti-patterns

Named so they can be rejected in review without re-arguing.

| Anti-pattern | Why it is banned here |
|---|---|
| **The anthropomorphic assistant** — a name, a face, a personality, "Hi! I'm Aria 👋" | We answer the phone as *the subscriber*. A persona invites the caller to relate to a character and invites the subscriber to over-trust one. `AiBadge` says "Assistant", never a name ([`09 §AiBadge`](../design/09-components.md)) |
| **The engagement loop** — streaks, weekly digests, "you blocked 14 spammers this week!" | This product succeeds when the user thinks about it less. Gamifying a security tool trains people to open it for dopamine and ignore it in an actual emergency |
| **The confidence launder** — hiding low confidence to look decisive | Principle 4. It is the fastest way to destroy the product's only asset |
| **The consent wall** — one screen, twelve toggles, one "Accept all" | Invariant U9. "Accept all" is the design pattern that makes consent meaningless |
| **The alarm aesthetic** — red banners, hazard stripes, sirens for spam | Shouting reads as insecurity ([`design README`](../design/README.md), principle 1). We have exactly one red, and it means fraud |
| **The chat metaphor for transcripts** — left/right bubbles, typing dots, read receipts | A screening is a record of a conversation the user was not in. Chat alignment implies peers ([`09 §Transcript`](../design/09-components.md)) |
| **The dark pattern paywall** — obscured close button, "are you sure you want to stay poor" | Premium is a label, never an alert ([`09 §PremiumBadge`](../design/09-components.md)). The paywall closes in one obvious tap |
| **The infinite skeleton** | A skeleton that never resolves is a hang wearing a costume ([`07 §7.4`](../design/07-states.md)) |
| **The silent degradation** | If we downgraded a model tier, dropped audio, or failed to score fraud, the user is told in plain language. Invariant I11 fails safe; the interface says so |

---

## 0.5 How to use these

- A screen contract that contradicts a principle is wrong, and the principle
  does not need re-justifying in review.
- An invariant violation is a **blocking** defect, in the same class as a
  failing test.
- New surfaces inherit all twelve invariants. A surface that cannot satisfy one
  needs a documented exception with an owner, not a quiet omission.
- Where a principle and a business goal conflict, that is a product decision to
  be made explicitly and recorded — not resolved silently in a pull request.
