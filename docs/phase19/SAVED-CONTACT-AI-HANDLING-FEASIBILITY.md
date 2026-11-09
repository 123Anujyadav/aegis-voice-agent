# Saved-contact AI handling: technical feasibility (2026-11-09)

- **Question (owner, 2026-11-09):** Lily AI should answer **unknown callers** and also, when the person enables it, **saved contacts**. Blocked numbers keep the highest priority and must never be bypassed. Example greeting: "Hello! Aap Anush se kya kaam ke liye baat karna chahte hain?"
- **Status:** an assessment and a proposal only. **Nothing here is implemented or approved.** The current call screening is unchanged.
- **Review:** AI-performed, from the repository, the platform's documented API surface and this project's device evidence. Statements about Android behaviour that this project has **not** measured are marked *unverified*.

## 1. What the phone can and cannot do

| Capability | API | Can this app use it? | Evidence |
|---|---|---|---|
| Decide whether a call rings, is silenced or is rejected | `CallScreeningService` with `ROLE_CALL_SCREENING` (API 29+) | **Yes**, today | `PreFilterScreeningService`; device cases M1, M7, M8 |
| See calls from **saved contacts** | The same service, with `READ_CONTACTS` granted | **Yes, with contacts access.** Without it, Android rings saved contacts without asking the app | M7 passed (`TC@228`: "contact exists", app consulted). M6: without access, saved contacts rang unscreened (one exception, `TC@218`, root cause inconclusive) |
| Silence a call without rejecting it | `CallResponse.Builder.setSilenceCall` (API 29+) | **Yes**: already mapped (`ScreeningResponse.silenceCall`) | `PreFilterScreeningService.toCallResponse` |
| Answer a cellular call and **speak to the caller from the phone** | No supported API for a third-party app. Answering needs the default-dialer role (`ROLE_DIALER`, an `InCallService`) or `ANSWER_PHONE_CALLS`. Even then, an app cannot inject audio into the call's uplink or capture its downlink: that is reserved to system apps (`CAPTURE_AUDIO_OUTPUT`) | **No** | Platform restriction (Android 9+ blocks call-audio capture for third-party apps). *Unverified on this project's devices*; not attempted |
| Hand a call to an "audio processing" screening mode | `setShouldScreenCallViaAudioProcessing` (API 30) | **Not for this app**: it is honoured only for the system dialer's own screening, *unverified* | Not attempted |

**Conclusion:**
- **The phone cannot host the AI conversation.** Becoming the default dialer would not change that, and replacing the person's dialer is a large, policy-sensitive step the project has not chosen.
- **The AI conversation must happen off the phone**, as the accepted architecture already plans: carrier call forwarding to a number (DID) on the telephony gateway, where the assistant answers (ADR-0002, ADR-0003).

## 2. The supported design

The app decides **per call** what reaches the AI. Only **no-reply** forwarding (CFNRy) is provisioned on the SIM (ADR-0002 §5).

| Caller | App's decision (`CallScreeningService`) | What the caller experiences |
|---|---|---|
| **On the blocklist** (highest priority) | **Reject** (as today) | Busy. **CFNRy does not apply to a rejected call,** so a blocked caller never reaches the AI. Device case M3 measures this on each carrier |
| **Unknown**, with AI screening on | **Silence** | The phone does not ring. After the carrier's no-reply delay, the call forwards to the DID, and the AI answers with the greeting |
| **Saved contact**, with "let the AI answer my contacts" on | **Silence** (needs `READ_CONTACTS`, which the app already asks for) | As for an unknown caller: the AI answers. The person can still pick up while it silently rings |
| **Saved contact**, preference off (the default) | **Allow** (as today) | Rings normally. If unanswered, CFNRy still forwards it to the AI |
| **Allowlist** | **Allow** | Rings normally |

- **Why silence, not reject:** reject means busy, and with only no-reply forwarding a busy call is **not** forwarded. That property is exactly what keeps blocked callers away from the AI. Busy forwarding (CFB) must therefore **never** be provisioned, or blocked callers would be forwarded too.
- **The greeting** ("Aap Anush se kya kaam ke liye baat karna chahte hain?") is spoken by the assistant at the gateway. It needs the person's display name on the server, which is a data-sharing decision (see §4).

## 3. Limits

- **Android 8 and 9 (API 26–28):** no `ROLE_CALL_SCREENING` and no `setSilenceCall`. The app cannot screen (device case M9 expects "This version of Android doesn't let apps block calls"), so on those versions every unanswered call forwards, contacts included, and nothing can be silenced.
- **Contacts access is required** for saved contacts to reach the app at all (M6, M7). If the person denies it, saved contacts ring unscreened and, if unanswered, still forward.
- **The one M6 exception** (`TC@218`: Android consulted the app for a saved contact without contacts access) is unexplained. Saved-contact behaviour must be verified per device before any claim.
- **Carriers:**
  - CFNRy codes, the ring delay and VoLTE behaviour differ (Jio in particular), and so may whether a rejected call is forwarded;
  - device cases M3–M5 (`FORWARDING-TEST-PREPARATION.md`) measure this;
  - none is run (X4).
- **OEMs:** battery and auto-start restrictions (device case M12) are not run. The service is bound per call, so they should not matter. *Unverified.*
- **Silence is not invisible:** Android still shows the incoming call (silently) and logs it. The person can answer at any time, which is the intended human takeover (U3).

## 4. Privacy, consent and security

**Personal data sent to the server:**
- Forwarding sends the caller's voice to the gateway, which is personal data under ADR-0012 and DPDP.
- It needs the consent records (OD-4), the recording and processing disclosures (U5), the DPDP review (E9, L1), and the speech and LLM processors' terms (OD-17, the LLM category approval).

**A new consent:**
- Letting the AI answer **contacts** is a separate choice from answering unknown callers.
- It should be its own, default-off, revocable setting. U9 requires independent consent controls.

**Data sharing:**
- The greeting needs the person's name at the gateway.
- Per-contact choices could stay on the phone, since the app silences or allows locally, so **no contact list leaves the phone**. That avoids the server-side list sync (OD-2, high-risk H3).

**Never weakened:**
- The blocklist precedes everything (`PreFilter` order).
- Busy forwarding is never provisioned.
- The app never reads or uploads contacts beyond Android's own lookup.

## 5. What can be built now, and what cannot

| Piece | Status | Needs |
|---|---|---|
| A "Let the AI answer my saved contacts" setting (default off), stored on the phone | Can be built and tested locally | **Owner decision:** it changes call handling, so a decision is needed before building |
| `PreFilter`: silence saved contacts when the setting is on; blocklist first; tests for every row of §2 | Can be built and unit-tested | Same decision |
| The forwarding itself (CFNRy to a DID) | **Cannot** | X4 (DID pool, CPaaS; paid), the forwarding category approval, C8 |
| The assistant answering and greeting | **Cannot** | The telephony gateway (staging NO-GO, P1-38), speech (OD-17, deferred), the LLM approval |
| End-to-end evidence | **Cannot** | Device cases M3–M5 on each carrier, plus M9, M10, M12 |

## 6. Tests the feature would need

- **Unit (`PreFilter`):**
  - blocked contact → reject;
  - blocked unknown → reject;
  - unknown with screening on → silence;
  - contact with the setting on → silence;
  - contact with the setting off → allow;
  - allowlisted → allow;
  - a lookup failure → allow (fail open, as today);
  - API < 29 → no silence.
- **Emulator:** the setting's persistence and the screening role.
- **Device (per carrier):**
  - M3: a rejected call is **not** forwarded;
  - M4: an allowlisted caller rings, and is forwarded only if unanswered;
  - M5: an unknown caller is answered by the AI;
  - a new case for a saved contact with the setting on.
- **Privacy:** no contact data in any request; the name is sent only with consent.

## 7. Recommendation

1. **Adopt the §2 design:** reject for blocked, silence for AI-handled, no-reply forwarding only. It uses supported APIs, keeps the blocklist absolute, and keeps contacts on the phone.
2. **Owner decisions needed first:**
   - the saved-contacts setting and its default (proposed: off);
   - consent and disclosure wording for callers reaching the AI (with OD-4 and U5);
   - the forwarding and LLM category approvals;
   - OD-17 when speech is undeferred.
3. **Then:** build the setting and the `PreFilter` change behind the decision, with the §6 unit tests. It stays dormant until forwarding exists, since silencing without forwarding would only stop the phone ringing. The UI must say "Not available yet" until then.
