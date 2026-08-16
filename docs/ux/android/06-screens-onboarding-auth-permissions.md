# Android · 6 · Screens — Onboarding, Authentication, Permissions

The hardest sequence in the product. It asks a stranger to let a machine answer
their phone, hand over their contacts, and dial a code that reconfigures their
carrier — before they have seen the product do anything useful.

---

## 6.0 Onboarding strategy

### The problem, stated honestly

Conventional mobile onboarding advice — defer permissions, show value first,
keep it to three screens — does not survive contact with this architecture. We
cannot demonstrate value before setup, because the product's value *is* the
setup: until forwarding is provisioned and the screening role is held, nothing
happens.

So the strategy is not deferral. It is **earned escalation with a working
product at every step.**

### The escalation ladder

| After step | The product does | The user has given |
|---|---|---|
| Identity (`A03`) | Nothing yet | Their number |
| Screening role (`A09b`) | **Blocks and silences known-bad numbers, free, on-device** | A role |
| Forwarding (`A08`) | **Screens unknown callers with the assistant** | A carrier change |
| Contacts (`A09c`) | Stops screening people they know — cheaper and less annoying | Contacts |
| Consent (`A11`) | Whatever they chose, and only that | Specific consents |
| Assistant setup (`A12`) | Answers in their language, their way | Preferences |

**Each step leaves a working product behind it.** A user who abandons after the
screening role has a functioning on-device blocker. A user who abandons after
forwarding has the full product with a generic assistant. Nobody ends up with an
app that does nothing, and nobody is held hostage to the next screen.

### Rules

1. **No step is skippable that would leave the product broken; every other step
   is skippable.** The skip is labelled "Not now", never "Skip" — one is a
   deferral, the other is a dismissal.
2. **No screen asks for two things.** Invariant U9.
3. **Progress is shown as a step count, not a progress bar.** "3 of 8" is a
   fact; a 37%-full bar is a guess, and it is always wrong.
4. **Every screen is completable one-handed**, with the primary action in the
   thumb zone.
5. **Total target: under 3 minutes**, of which the carrier handoff is the
   longest and least controllable part.
6. **Nothing is celebrated until the test call.** Confetti before the product
   has worked once is a promise we have not kept.

---

## A01 · Welcome

**Purpose** — State what the product does in one sentence, and start.

**Inputs** — None.

**Outputs** — Proceed to `A02`; open legal documents.

**Components** — Illustration · `Text` (`display.sm` + `body.lg`) · `Button`
(Primary) · `Text` (legal, `body.sm`).

**Animation** — Static on entry. The illustration does not animate. **The first
frame the user sees must not drop a frame** ([`02 §2.8`](../02-interaction-language.md), S1).

**Copy** — "An assistant answers calls from numbers you don't know, finds out
what they want, and tells you." One sentence, no adjectives, no exclamation.

**Edge cases** — Returning user with a cleared app → same screen, and `A02`
recognises the number · region outside India → a clear statement that the
product is India-only at launch, with no dead end.

**Accessibility** — Single heading, single action. Legal links are real links,
announced as such.

**Loading / Empty / Error** — None.

**Success** — Navigation.

**Security** — Terms and privacy policy are linked before any data is collected,
not after.

**Analytics** — `android.onboarding.welcome_viewed` ·
`android.onboarding.started`.

---

## A02 · Phone number

**Purpose** — Collect the identity primitive (ADR-0010).

**Inputs** — Country code (default +91); SIM-derived number as a suggestion
where `READ_PHONE_STATE` is not required to obtain it.

**Outputs** — OTP request.

**Components** — `PhoneField` · `Button` (Primary) · `Text` (rationale).

**Animation** — Field focuses on entry, keyboard opens. Formatting reformats on
group boundaries only.

**Edge cases** — Number already registered on another device → proceeds
normally; the device-trust step (`A16`) handles the conflict, because telling an
unauthenticated caller whether a number is registered is an enumeration oracle ·
invalid format → inline error, no submission · landline → rejected inline with a
plain reason · number typed with a leading 0 → normalised silently · SIM
suggestion wrong → freely editable, never locked.

**Accessibility** — Label above the field, always visible
([`09 §TextField`](../../design/09-components.md)). Number announced digit by
digit with grouping. `tnum` prevents jitter.

**Loading** — Button inline loading, width preserved.

**Empty** — Not applicable.

**Error** — Inline for validation. Transient for send failure, with retry.
Rate-limited: "Too many attempts. Try again in 10 minutes." with a real
countdown, not a vague wait.

**Success** — Navigation to `A03`.

**Security** — No indication of whether the number is already registered.
Rate-limited server-side per number and per device. The number is `PERSONAL`
and is not logged.

**Analytics** — `android.onboarding.number_submitted` (`had_sim_suggestion`).
**No number.**

---

## A03 · OTP verification

**Purpose** — Prove control of the number.

**Inputs** — Number; OTP; resend timer; attempt count.

**Outputs** — Session; device credential generation.

**Components** — OTP field (6 digits) · `Button` (Primary) · resend link with
countdown · "Change number".

**Animation** — Auto-fill populates with a 40 ms per-digit stagger, then
auto-submits after 300 ms ([`02 §2.11`](../02-interaction-language.md)).

**Edge cases** — SMS retriever fires → auto-fill · SMS never arrives → resend
after 30 s, then 60 s, then a voice-call fallback, then support · wrong code →
inline error with attempts remaining stated · code expired → stated as expired,
distinctly from wrong, with a resend · attempts exhausted → lockout with a real
duration · app backgrounded during SMS → state preserved · dual-SIM, OTP to the
other SIM → the resend copy names the number we sent to.

**Accessibility** — Single field with 6 characters, not six fields — six fields
are a screen-reader and paste hazard. Announced as "6 digit code". Auto-submit
is announced before it happens: "Code complete, verifying".

**Loading** — Inline button loading; the field locks during verification.

**Empty** — Not applicable.

**Error** — Inline for wrong/expired. Transient for network. **Never blocking**
— the user has a code in hand and needs to be able to retry.

**Success** — `haptic.confirm`, then the invisible device-trust step, then `A05`
or `A06`.

**Security** — Device credential is generated **on-device in the Keystore and is
non-exportable** (Invariant I5). Play Integrity runs here. Failure goes to
`A04`, never to a generic error. OTP is never logged, never in analytics, never
in a crash report.

**Analytics** — `android.onboarding.otp_submitted` (`attempt`, `autofilled`) ·
`android.onboarding.otp_failed` (`reason`) · `android.onboarding.verified`
(`time_from_send_ms`).

---

## A04 · Device trust failed

**Purpose** — Explain an integrity failure without teaching an attacker
anything, and without stranding a legitimate user.

**Inputs** — Failure class (device integrity / app integrity / unrecognised
device).

**Outputs** — Retry · support.

**Components** — Full-screen blocking state · `Button` (Primary retry,
Secondary support).

**Copy** — "We couldn't verify this device. This can happen on modified devices,
on some custom software, or if the app wasn't installed from the Play Store."
Then: what to try. Then: contact support.

**Edge cases** — Rooted or custom ROM → the honest explanation above; we do not
pretend it is a network problem · Play Services missing → a distinct message
naming the actual requirement · transient integrity API failure → retry
succeeds, which is why retry is the primary action.

**Accessibility** — Announced `assertive`. Support path is reachable without an
account.

**Security** — The message is deliberately non-specific about which check
failed. Enumerating our integrity checks to a failing client is free
reconnaissance.

**Analytics** — `android.onboarding.integrity_failed` (`class`).

---

## A05 · SIM selection

Dual-SIM only. Skipped entirely on single-SIM devices — an unskippable screen
with one option is an insult.

**Purpose** — Choose which line gets screened.

**Inputs** — `SubscriptionManager` subscriptions; carrier names; last four
digits where available.

**Outputs** — Selected subscription ID.

**Components** — Radio list (`ListItem` with leading radio) · `Button`
(Primary).

**Edge cases** — `SubscriptionManager` reports unreliable labels across OEMs
(ADR-0002 §7) → we show what we have and let the user correct it; **the label is
never the only identifier**, the number's last four digits are shown where
readable · eSIM → treated identically · SIM changed later → detected, and the
user is asked once, not silently re-provisioned · both SIMs on the same carrier
→ disambiguated by slot and number.

**Accessibility** — Radio group. Each option announces carrier, slot and partial
number.

**Loading** — Subscriptions read synchronously; no loading state.

**Empty** — No subscriptions readable → manual entry with an explanation.

**Error** — Read failure → manual selection, never a dead end.

**Success** — Navigation.

**Security** — Subscription ID is stored locally and sent as an opaque
reference. `READ_PHONE_STATE` is requested here, immediately before use, and
declining leaves manual selection available.

**Analytics** — `android.onboarding.sim_selected` (`sim_count`,
`used_manual_entry`).

---

## A06 · How screening works

The screen that earns the carrier change. It is longer than any other onboarding
screen and that is deliberate.

**Purpose** — Explain conditional forwarding, honestly, including the parts that
cost the user something — before asking for it.

**Inputs** — Detected carrier; carrier-specific notes from the carrier matrix.

**Outputs** — Proceed to `A09a`.

**Components** — Illustration (static, a three-step diagram) · `Text` blocks ·
`Card` (Filled, the billing disclosure) · `Button` (Primary) · `Button`
(Tertiary, "How do I undo this?").

**Content, in order**

1. **What happens** — Your phone rings for 5 seconds. If you don't answer, your
   carrier passes the call to our number, and the assistant answers it.
2. **What the caller hears** — The exact announcement text, verbatim. This is
   Invariant I1's text and the user should see it before agreeing to it.
3. **What it costs** — "Your carrier may charge for the forwarded call on some
   plans. We can't see those charges. If you're not sure, check with your
   carrier first." (ADR-0002 §7 — a real, poorly-documented cost we own the
   support burden for even though we do not control it.)
4. **How to undo it** — One code, `##61#`, shown plainly. ADR-0002 §14 requires
   this to be prominent, not buried.

**Edge cases** — Carrier not detected → generic copy, no false specificity ·
carrier known to have quirks → the quirk is named here, not discovered at
failure · user taps "How do I undo this?" → a sheet with the code and a
one-tap dial, available before they have even set it up. **A product that shows
you the exit before the entrance is one you trust with the entrance.**

**Accessibility** — Diagram has a full text alternative that stands alone.
Reading level Class 8. Announcement text is selectable.

**Loading / Empty / Error** — None.

**Success** — Navigation.

**Analytics** — `android.onboarding.explainer_viewed` (`carrier`,
`dwell_ms`) · `android.onboarding.undo_info_opened` — a high rate here is a
signal the explanation is frightening, not that the feature is.

---

## A09a/b/c · Permission rationales

Three instances of one contract ([`01 §1.5`](../01-cross-surface-conventions.md)).

**Purpose** — Ask for one permission, having earned it, with an honest account
of what "no" costs.

**Structure** — Four blocks, in this order: what we will ask for (in the system
dialog's own words) · what it enables (one concrete capability) · what we will
never do with it · what happens if you decline. Two actions: Primary → system
dialog; Tertiary → "Not now".

| | `A09a` Notifications | `A09b` Screening role | `A09c` Contacts |
|---|---|---|---|
| Enables | "We can tell you when a call is being screened, while it's happening." | "We can block numbers you've blocked, and let people you know ring through, without the call ever leaving your phone." | "People in your contacts ring through instead of being screened." |
| Never | "We won't send you marketing." | "We can't hear your calls. Android doesn't allow that for any app, including this one." | "Your contacts stay on your phone. We don't upload them." |
| If declined | "Screening still works. You'll find out afterwards instead of during." | "Every unknown call gets forwarded, which may cost you carrier charges and means people you know get screened too." | "People you know will be screened like anyone else." |

**`A09b`'s "never" line is the most important sentence in onboarding.** The
single largest fear a call-screening app triggers is "this app is listening to
my calls". Stating the platform constraint — that no app can, including us —
converts the product's biggest architectural limitation (ADR-0002 §2) into its
strongest trust claim.

**Edge cases** — Permission already granted → screen skipped entirely ·
previously denied → rationale shows once more with the loss stated in past tense
("You're currently missing…") · permanently denied → **the screen is not shown**;
`A74` handles it contextually later.

**Accessibility** — Four short blocks, each a heading + one line. Both actions
≥ 48 dp; "Not now" is not visually suppressed.

**Success** — System dialog result, either way, advances the flow.

**Analytics** — `android.onboarding.rationale_viewed` (`permission`) ·
`android.onboarding.permission_result` (`permission`, `granted`,
`dialog_shown`).

---

## A07 · Forwarding activation

**Purpose** — Provision conditional call forwarding, with the user watching.

**Inputs** — Selected subscription; allocated DID; ring delay (5 s);
constructed MMI string.

**Outputs** — `ACTION_CALL` to the MMI; navigation to `A08`.

**Components** — Full-screen state · `Text` (the MMI string, `numeric.md`) ·
`Button` (Primary) · `Button` (Tertiary, "Do it manually").

**Animation** — Storyboard S6 ([`02 §2.8`](../02-interaction-language.md)).

```
┌────────────────────────────────────────────────┐
│                                                │
│  One step from your carrier                    │
│                                                │
│  We'll dial this code. It tells Airtel to      │
│  pass unanswered calls to us after 5 seconds.  │
│                                                │
│         **61*8047001234**5#                    │  ← shown, selectable
│                                                │
│  Your dialer will open. It may look like a     │
│  call — it isn't one, and it's free.           │
│                                                │
│  ┌──────────────────────────────────────────┐ │
│  │              Set up forwarding           │ │
│  └──────────────────────────────────────────┘ │
│              Do it manually                     │
└────────────────────────────────────────────────┘
```

**Edge cases** — `ACTION_CALL` unavailable or blocked → manual instructions with
a copyable string · user cancels in the dialer → returns here, no penalty, retry
available · carrier returns an error tone → `A08` detects non-verification and
routes to `A08e` · dual-SIM dialer asks which SIM → we cannot control this;
`A06` warned about it and `A08e` covers the wrong-SIM outcome · MMI silently
succeeds with no acknowledgement (carrier-dependent) → `A08`'s interrogation is
the source of truth, not the dialer's response.

**Accessibility** — The MMI string is announced character by character and is
selectable and copyable, because a user may need to read it to a support agent.
"It may look like a call — it isn't one" is announced; this is the single most
common moment of alarm in the flow.

**Loading** — Handoff to the dialer is immediate. Return is detected by
lifecycle, not by a timer.

**Empty / Error** — Errors surface at `A08`; this screen only hands off.

**Success** — Navigation to `A08`.

**Security** — **The MMI string is constructed client-side from a
signature-verified DID.** It is never taken from a server response as a
ready-made dial string (ADR-0002 §10) — a server-supplied `tel:` payload is a
telephony-reconfiguration primitive and would be the highest-value target in the
product. Showing the string to the user is itself a control: they can see where
their calls are being sent.

**Analytics** — `android.onboarding.forwarding_dial_started`
(`carrier`, `manual`) · `android.onboarding.forwarding_dial_returned`
(`elapsed_ms`).

---

## A08 · Forwarding verification · A08e · Manual instructions

**Purpose** — Establish the truth about whether forwarding is active, and fix it
if not.

**Inputs** — Interrogation result (`*#61#`); expected DID; carrier; retry count.

**Outputs** — Verified → `A09c`. Not verified → `A08e`.

**Components** — Indeterminate state → result state · `Button` ×2 ·
carrier-specific instruction list (`A08e`).

**Animation** — Verified: check mark draws in, `medium`/`decelerate`,
`haptic.confirm`, auto-advance after 1200 ms. The user does not need to
acknowledge a success they can see.

**Edge cases**

| Condition | Behaviour |
|---|---|
| Forwarding set to a different number | "Your calls are going somewhere else." Named explicitly. This is the SIM-swap / pre-existing-forwarding case and it must not be silently overwritten — we show what we found and ask |
| Interrogation unsupported | "We can't check this on your carrier." Offer a **test call** as the alternative proof. Never a green tick we cannot justify |
| Set on the wrong SIM | Named, with a one-tap correction that clears the old configuration first |
| Verified but ring delay differs | Accepted, and the actual delay is shown. A 10 s delay is worse but functional; failing the user over 5 seconds would be pedantry |
| Repeated failure (3+) | Stop retrying. `A08e` with carrier-specific steps and a support path. **A retry button that we know will fail is a lie** ([`01 §1.3`](../01-cross-surface-conventions.md)) |

**Accessibility** — Result announced `assertive`, stating both the outcome and
what it means for calls. Instructions in `A08e` are an ordered list, one action
per step.

**Loading** — 2–10 s tier: indeterminate with descriptive text, "Checking with
your carrier…".

**Empty** — Not applicable.

**Error** — `A08e` is the error state, and it is designed as a helping screen
rather than a failure screen: carrier name, exact steps, the code, a copy
action, a support link, and a `Skip for now` that leaves the user with a
working on-device blocker.

**Success** — Check, haptic, auto-advance.

**Security** — Verification compares against our expected DID. A mismatch is
reported to the user, never silently corrected — silently re-dialling an MMI
because we did not like what we found is exactly the behaviour ADR-0002 §10
warns about.

**Analytics** — `android.onboarding.forwarding_verified` (`attempts`,
`elapsed_ms`, `carrier`) · `android.onboarding.forwarding_failed`
(`failure_class`, `carrier`) — **per-carrier failure rate is a launch-blocking
metric** (ADR-0002 §16: > 2% triggers ADR review).

---

## A10 · Contacts sync consent · A11 · Privacy and consent

**Purpose** — Collect specific, purpose-bound, withdrawable consents. Invariant
U9.

**Inputs** — Consent catalogue with current state; jurisdiction copy.

**Outputs** — Per-consent grant/withhold.

**Components** — `ListItem` with trailing `Switch` · consequence line beneath
each · `Text` (plain-language summary) · link to the full policy.

```
┌────────────────────────────────────────────────┐
│  What we do with your data                     │
│                                                │
│  You can change any of these later, and        │
│  turning one off doesn't turn off the others.  │
│                                                │
│  Read your contacts              [●━]          │
│  People you know ring through instead of       │
│  being screened. Contacts stay on your phone.  │
│                                                │
│  Record call audio               [━○]          │
│  Off. Transcripts are always saved; audio is   │
│  extra. Callers are told either way.           │
│                                                │
│  Keep transcripts for 90 days    [90 ⌄]        │
│  Choose between 7 and 180 days.                │
│                                                │
│  Help improve the product        [●━]          │
│  Anonymous usage data. Never call content,     │
│  never your number.                            │
│                                                │
│  Crash reports                   [●━]          │
│  Technical diagnostics only.                    │
│                                                │
│  ┌──────────────────────────────────────────┐ │
│  │                 Continue                 │ │
│  └──────────────────────────────────────────┘ │
└────────────────────────────────────────────────┘
```

**Edge cases** — All consents withheld → **the product still works**; screening
proceeds on the announcement's lawful basis (ADR-0012 §5.1), and `Continue`
proceeds without argument · recording toggled on → a second, specific
confirmation naming what is recorded and for how long, because audio is
`SENSITIVE` · retention set below the default → applied immediately to existing
data, with the deletion date stated · analytics off → we stop emitting
client-side, not discard server-side
([`01 §1.9`](../01-cross-surface-conventions.md)).

**Accessibility** — Each row is one switch with one label and one consequence
line, read as a unit. Consequence text updates **after** the toggle, with a
`short` fade, so cause precedes effect
([`02 §2.11`](../02-interaction-language.md)). No consent is pre-checked that
grants more than the minimum.

**Loading / Empty** — None.

**Error** — Save failure is transient; the toggle reverts visibly. A consent
whose state we are unsure of is shown as **off**, never as on (Invariant I8 —
fail closed).

**Success** — No confirmation. The switch state is the confirmation.

**Security** — Consent records are timestamped, versioned against the policy
text shown, and retained as required. **There is no "Accept all".**

**Analytics** — `android.onboarding.consent_set` (`purpose`, `granted`) —
per-purpose, never as a bundle. Consent events are exempt from the analytics
opt-out because they are a legal record, not product analytics, and this is
stated in the analytics consent's own consequence line.

---

## A12 · Assistant setup · A13 · Test call · A14 · Ready

**A12 Purpose** — Make the assistant sound like it belongs to this person.

**Inputs** — Name to use; language(s); voice options by plan; greeting style
presets.

**Outputs** — Assistant configuration.

**Components** — `TextField` (name) · `Dropdown` (language) · voice list with
preview playback · greeting style radio (Brief / Balanced / Detailed).

**Edge cases** — Name left blank → the assistant says "the person you're
calling", which is a valid and slightly more private choice, and the placeholder
says so · Indic script name → typography handles it
([`03`](../../design/03-typography.md)); the TTS pronunciation preview is
available before committing · premium voices → previewable, gated at selection,
with the paywall as a sheet not a wall.

**A13 Purpose** — Prove the product works, once, before the user has to trust
it with a real call.

**Behaviour** — We place a call **to** the user's number from our DID, the
assistant greets them as though they were an unknown caller, and they experience
the screening from the caller's side. Then the app shows them the transcript
that was generated.

This is the single most valuable screen in onboarding. It converts an
abstraction — "an AI will talk to people" — into a thing that just happened to
them, and it validates forwarding, media, ASR, LLM and TTS end to end in one
action.

**Edge cases** — Call does not arrive within 20 s → "We couldn't reach your
phone" with a diagnostic path to `A52` · user does not answer → offer to retry,
skippable · user is on another call → deferred with a retry action · skipped →
`A14` proceeds; the test call remains available in the Assistant tab forever.

**Accessibility** — The test call is a real phone call, so it is inherently
audio. **A text-only alternative is offered**: run the screening against a
recorded caller and show the transcript, no audio required. Invariant U6 —
nothing is available by listening that is not available by reading.

**A14 Purpose** — End the flow and get out of the way.

**Content** — One line confirming screening is active, one line on what happens
next ("You'll get a notification the first time someone unknown calls"), one
action: `Done`. No feature tour, no checklist, no upsell.

**Analytics** — `android.onboarding.assistant_configured` (`language`,
`voice_tier`, `greeting_style`, `named`) · `android.onboarding.test_call_result`
(`outcome`, `used_text_alternative`) · `android.onboarding.completed`
(`total_elapsed_s`, `steps_skipped`).

---

## A15 · Re-authentication · A16 · New device enrolment · A73 · Session revoked

**A15 Purpose** — Restore a session on a device we already trust.

**Behaviour** — MSISDN pre-filled and locked, OTP only. The device credential
already exists in the Keystore and is re-attested rather than re-generated. No
password, because this product has none anywhere.

**A16 Purpose** — Move the account to a new device, safely.

**Behaviour** — Number → OTP → integrity → **an explicit statement that this
replaces the existing device**, naming the old device and when it was last
active → confirmation. The old device's session is revoked and it receives
`A73`.

**Edge cases** — Old device is the only one with unsynced blocklist entries →
those are server-held, so nothing is lost, and the screen says so · user is
being socially engineered into enrolling an attacker's device → the old device
gets a `HIGH`-importance `account` notification naming the new device, before
the switch completes, with a `This wasn't me` action · both devices online →
the old device's revocation is immediate.

**A73 Purpose** — Tell a device its session is over, without ambiguity.

**Copy** — "You're signed out on this device." Then the reason, when we can give
one: signed in elsewhere / you signed out / we couldn't verify this device.
Then: **"Screening is still active on your account"** if it is, because signing
out of the app does not stop the carrier from forwarding — and a user who does
not know that will think the product is off when it is not.

**Security** — Sign-out clears cached transcripts, contacts and search history
from the device ([`01 §1.11`](../01-cross-surface-conventions.md)). A resold or
shared handset is the realistic threat, and it is more likely than a targeted
attack.

**Analytics** — `android.auth.reauthenticated` · `android.auth.device_enrolled`
(`replaced_device`) · `android.auth.session_revoked` (`reason`).

---

## A47 · Microphone rationale · A74 · Permission recovery · A75 · Screening role lost

**A47** — The `RECORD_AUDIO` rationale, shown **only** when the user first taps
the Assistant's push-to-talk. Never in onboarding.

Its "never" line: **"We can't record your phone calls. Android doesn't allow
that. This is only for talking to the assistant in the app."** That sentence is
why this permission is deferred — asked at install it reads as surveillance;
asked at the microphone button it reads as obvious.

**A74** — Contextual recovery banner for a permanently-denied permission,
shown at the point of loss, at most once per feature per 30 days, deep-linking
to the exact system settings page ([`01 §1.5`](../01-cross-surface-conventions.md)).

**A75** — The screening role was taken by another app, which Android allows and
users do routinely without understanding the consequence.

**Copy** — "Another app is now screening your calls. Numbers you've blocked here
won't be blocked until you switch back." One action: re-request the role. This
is a **blocking banner** on the home surface rather than a full screen, because
the rest of the product — forwarding, transcripts, history — is unaffected, and
blocking the whole app over a degraded pre-filter would be disproportionate.

**Analytics** — `android.permission.recovery_shown` (`permission`) ·
`android.permission.recovery_tapped` · `android.permission.role_lost` ·
`android.permission.role_recovered` (`hours_lost`).
