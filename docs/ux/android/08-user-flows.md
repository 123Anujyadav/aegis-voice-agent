# Android · 8 · User Flows

Every flow, end to end, including the failure branches. A flow without its
failure branches is a happy path, not a design.

---

## Flow index

| # | Flow | Trigger | Terminal states |
|---|---|---|---|
| F1 | First run to first screening | Install | Screening active · Partially set up · Abandoned |
| F2 | **A call gets screened** | Inbound unknown call | Taken · Declined · Blocked · Ended · Missed |
| F3 | Reviewing a screened call | Notification or app open | Reviewed · Acted on |
| F4 | Fraud detected | Verdict during or after screening | Blocked · Reported · Disputed · Ignored |
| F5 | Emergency detected | Emergency intent | Taken · Called back · Dismissed |
| F6 | Spam management | Protection tab | Blocked · Bulk blocked |
| F7 | Forwarding lapses and recovers | Health check failure | Recovered · Unrecovered · Abandoned |
| F8 | Asking the assistant | Assistant tab | Answered · No answer · Action taken |
| F9 | Upgrading to premium | Gated feature tap | Purchased · Dismissed · Failed |
| F10 | Changing a consent | Settings | Changed · Reverted |
| F11 | Exporting or erasing data | Settings | Complete · Abandoned |
| F12 | Moving to a new device | New install, existing number | Migrated · Blocked |
| F13 | Joining and leaving an organisation | Invitation | Joined · Left · Removed |
| F14 | Losing and regaining the screening role | Another app takes it | Recovered · Degraded |
| F15 | Going offline and coming back | Connectivity | Synced · Nothing queued |

---

## F1 · First run to first screening

```
Install ──▶ A01 Welcome
              │
              ▼
            A02 Number ──▶ A03 OTP ──┬──▶ verified
              ▲                      └──▶ A04 integrity failed ──▶ support ─▶ ✖ blocked
              └── change number ─────┘
              │
              ▼
            A05 SIM  (dual-SIM only)
              │
              ▼
            A06 How screening works
              │            └── "How do I undo this?" ──▶ sheet ──▶ back
              ▼
            A09a Notifications ──┬── granted
              │                  └── denied ──▶ continue (degraded, noted)
              ▼
            A09b Screening role ──┬── granted ──▶ ✔ ON-DEVICE BLOCKING NOW WORKS
              │                   └── denied ──▶ continue (costly, stated)
              ▼
            A07 Forwarding ──▶ [system dialer] ──▶ A08 Verify
              │                                      │
              │                    ┌─────────────────┼─────────────────┐
              │               verified          not verified      unsupported
              │                    │                 │                 │
              │                    │                 ▼                 ▼
              │                    │            A08e manual      test-call proof
              │                    │                 │                 │
              │                    │            ┌────┴────┐            │
              │                    │        retry     skip            │
              │                    │            │         │            │
              │                    ◀────────────┘         │            │
              │                    │                      │            │
              ▼                    ▼                      ▼            ▼
              │            ✔ FULL SCREENING ACTIVE   ⚠ PARTIAL     ✔ PROBABLY ACTIVE
              ▼
            A09c Contacts ──▶ A10 Contacts consent
              │
              ▼
            A11 Privacy & consent  (nothing bundled, all optional)
              │
              ▼
            A12 Assistant setup
              │
              ▼
            A13 Test call ──┬── answered ──▶ transcript shown ──▶ A14
              │             ├── no answer ──▶ retry / skip ──▶ A14
              │             └── text alternative ──▶ transcript shown ──▶ A14
              ▼
            A14 Ready ──▶ A20 Calls
```

**Abandonment handling.** The step reached is persisted. Reopening the app
resumes at the next incomplete step — **except** that a user past `A09b` lands
on `A20` with a contextual prompt to finish, not back in the wizard. Once
something works, the wizard has no further claim on them.

**Target: under 3 minutes.** Instrumented as
`android.onboarding.completed.total_elapsed_s`, with per-step dwell.

---

## F2 · A call gets screened

The product's core loop. Note that it begins with a call the user did not
answer — there is no "screen this call?" prompt, because by the time we exist
the decision was already made by not picking up.

```
  Unknown number calls
        │
        ▼
  Handset rings, 5 s               ◀── ADR-0002: CFNRy ring delay
        │
   ┌────┴─────────────────┐
   │                      │
 answered            not answered
   │                      │
   ▼                      ▼
 Normal call        On-device pre-filter
                          │
        ┌─────────────────┼──────────────────┐
        ▼                 ▼                  ▼
   known contact      blocked            unknown
        │                 │                  │
        ▼                 ▼                  ▼
  rings through      rejected /        carrier forwards
  (no screening,     silenced          to our DID
   feed row only)    (feed row)              │
                                             ▼
                                     Gateway answers
                                             │
                                             ▼
                            ┌─── ANNOUNCEMENT (I1, deterministic) ───┐
                            │                                        │
                            ▼                                        │
                     Notification posts ─────▶ user sees it          │
                     (screening_live)                │               │
                            │                       │               │
                            ▼                       ▼               │
                     Conversation loop        A21 Live Screening    │
                     ┌──────────────┐               │               │
                     │ listen       │               │               │
                     │ → think      │◀──────────────┤               │
                     │ → speak      │        user watches           │
                     └──────┬───────┘               │               │
                            │                       │               │
                     fraud scoring ─── verdict ─────┤               │
                            │                       │               │
        ┌───────────────────┼───────────────────────┼───────────────┘
        ▼                   ▼                       ▼
   emergency          conversation ends       user acts
   detected                  │                      │
        │            ┌───────┴──────┐         ┌─────┼─────┬────────┐
        ▼            ▼              ▼         ▼     ▼     ▼        ▼
      F5      caller hangs up   agent ends  Take Decline Block  Listen
                     │              │         │     │     │        │
                     └──────┬───────┘         │     │     │        │
                            ▼                 ▼     ▼     ▼        │
                     A27 Post-call     handset  agent closes,     │
                       summary          rings     call ends       │
                            │             │           │           │
                            └─────────────┴───────────┴───────────┘
                                          │
                                          ▼
                                    A22 Call Detail
                                    (record persists)
```

**Degradation branches, all of which must be designed and none of which drop
the call:**

| Failure | Behaviour |
|---|---|
| Forwarding lapsed | The call simply rings and stops. No screening, no record beyond a missed-call row. `A71` banner already told the user this would happen |
| Gateway cannot answer | Carrier's forwarding fails → **the call rings through to the handset**. The designed failure mode (ADR-0002 §6) |
| ASR fails mid-call | Screening continues; transcript stalls; `A21` says so; takeover unaffected |
| LLM tier downgraded | Screening continues, shorter answers. `A77` banner, class `tier_downgraded` |
| Under load, admission shed | Some calls ring through unscreened. `A77`, class `queue_shed`. **Fraud scoring and the safety layer are never shed** (Invariant I11) |
| Subscriber offline | Irrelevant to screening. The live surface is unavailable; the record syncs later |

---

## F3 · Reviewing a screened call

```
  Entry: notification tap · feed card · search result · assistant citation
        │
        ▼
   A22 Call Detail
        │
   ┌────┼────┬─────────┬──────────┬────────┬──────────┐
   ▼    ▼    ▼         ▼          ▼        ▼          ▼
 read  open  play    search    block    report    share /
 turns evid- audio   within             (A34)     export
       ence  (prem)  (A22s)                        (A29)
        │      │
        │      └── audio deleted / never recorded ──▶ absent control + reason
        │
        ▼
   A22 transcript, deep-linked to the flagged turn, highlighted
```

**The retention branch.** Past 90 days the transcript is gone and the screen
says so as a fact, not an error. Metadata and the verdict remain. This is
correct behaviour and is presented without apology
([`01 §1.2`](../01-cross-surface-conventions.md)).

---

## F4 · Fraud detected

```
  Verdict arrives
        │
   ┌────┴──────────────────────┐
   │                           │
 during screening        after screening
   │                           │
   ▼                           ▼
 A21 inline badge         notification (fraud_alert)
 haptic.heavy             haptic.heavy
 assertive announce       ── only at medium+ confidence ──
 transcript marker              │
   │                            ▼
   │                       A22 or A31d
   │                            │
   └──────────┬─────────────────┘
              ▼
        A31d Fraud detail
        verdict · pattern · evidence · plain explanation
              │
   ┌──────────┼──────────┬──────────────┐
   ▼          ▼          ▼              ▼
 Block     Report    Open transcript  "It wasn't fraud"
   │          │       at flagged turn        │
   ▼          ▼                              ▼
 5s undo   acknowledged              verdict downgraded
                                     everywhere, immediately
                                             │
                                             ▼
                                   android.protection.
                                   verdict_disputed
```

**Low confidence changes the whole flow.** No haptic, no re-alert, no
notification — it renders, prefixed "Possibly", outlined, and waits to be found.
A low-confidence verdict that interrupts is how a product teaches users to
ignore its interruptions.

**The dispute path is first-class.** "It wasn't fraud" is a 48 dp target, not
visually subordinate, and its effect is immediate and visible everywhere. The
resulting event is the product's most important quality signal — it is
real-world precision, measured by the only judge who knows.

---

## F5 · Emergency detected

```
  Emergency intent classified during a screening
        │
        ▼
  Screening STOPS immediately
  Call is connected toward the subscriber
        │
        ▼
  A72 Emergency handoff  (overrides everything, back disabled)
  haptic.heavy · assertive announcement · quoted caller turn
        │
   ┌────┼──────────────────┬──────────────────┐
   ▼    ▼                  ▼                  ▼
 Take  Call them back   Not an emergency   call already ended
 call     │                  │                  │
   │      ▼                  ▼                  ▼
   │  system dialer,   resume screening    high-priority
   │  number pre-      if still live;      notification +
   │  filled           log false positive  A22 marker
   ▼
 handset rings ──▶ answered ──▶ system in-call UI
              └──▶ failed ──▶ A72 stays, offers Call them back
```

**We never dial emergency services on the user's behalf**, at any confidence, on
any surface. Our entire job here is to stop being in the way and to hand a
human the controls.

**Precedence:** `A72` outranks every other interrupt including a revoked session
([`02 §2.3`](02-navigation-graph.md)).

**The false-positive rate on this flow is a launch blocker**, tracked in
`tests/eval`. An emergency interrupt that fires on a delivery driver saying
"urgent" destroys the feature.

---

## F6 · Spam management

```
  A30 Protection ──▶ A36 Spam list
                        │
              ┌─────────┼──────────┐
              ▼         ▼          ▼
         open a call  select   block one
         (A22)        multiple      │
                        │           ▼
                        ▼      5 s undo
                   bulk block
                   (confirm count)
                        │
                        ▼
                   applied locally to the
                   pre-filter immediately,
                   queued if offline
```

Bulk selection exists on spam and **not** on fraud. Blocking thirty marketing
numbers unread is reasonable; blocking four suspected fraud numbers unread is
not, and the asymmetry is deliberate.

---

## F7 · Forwarding lapses and recovers

The highest-severity silent failure in the system (ADR-0002 §15 — likelihood
**high**).

```
  Periodic interrogation (forwarding-health job + on-device check)
        │
   ┌────┴────────────────────────┐
   ▼                             ▼
 verified                   not verified
   │                             │
 silent                          ▼
                          Classify
                          │
        ┌─────────────────┼──────────────────┬─────────────────┐
        ▼                 ▼                  ▼                 ▼
   cleared           wrong DID          wrong SIM        unverifiable
   (##002#, SIM      (hostile or        (dual-SIM        (carrier does
   swap, network)    pre-existing)      drift)           not support)
        │                 │                  │                 │
        └────────┬────────┴──────────────────┘                 │
                 ▼                                             ▼
     A71 persistent banner (all roots)                  A52 shows
     forwarding_health notification (HIGH, ongoing)     "Can't verify"
     haptic.reject once                                 + test-call proof
                 │                                      No false alarm.
                 ▼
           A52 Forwarding
                 │
        ┌────────┼────────┬──────────────┐
        ▼        ▼        ▼              ▼
    Check now  Set up   Change SIM    Turn off
               again                  (confirm)
                 │
                 ▼
            A07 → A08
                 │
        ┌────────┴────────┐
        ▼                 ▼
    verified         still failing
        │                 │
        ▼                 ▼
   banner clears     A08e manual, carrier-specific
   haptic.confirm    → support → known-issue page
```

**We never claim it is broken when we merely could not check.** "Unverifiable"
is its own state with its own copy, because a false alarm here is as corrosive
as a missed one.

---

## F8 · Asking the assistant

```
  A40 Ask
    │
  input: text │ press-and-hold voice
    │              │
    │         mic granted? ── no ──▶ A47 rationale ──▶ system dialog
    │              │ yes                                    │
    └──────┬───────┘◀───────────────────────────────────────┘
           ▼
     Thinking (real request in flight, named work where known)
           │
    ┌──────┼──────────┬─────────────────┬──────────────┐
    ▼      ▼          ▼                 ▼              ▼
 answer  no match  out of scope    action proposed   offline
 with      │          │                  │              │
 citations │          │                  ▼              ▼
    │      │          │             A44 review     disabled with
    ▼      ▼          ▼             list, per-item  reason; local
 tap ──▶ A22  offer search   one-line refusal  removal, one     search offered
                                              confirm
```

**No claim without a citation.** If the assistant cannot cite the call it is
describing, the sentence is not rendered. This is Principle 2 implemented as a
render-time constraint rather than a prompt instruction, because a prompt
instruction is a request and a render constraint is a guarantee.

---

## F9 · Upgrading to premium

```
  Gated feature tapped
        │
        ▼
  Shown 3+ times for this feature in 30 days? ── yes ──▶ suppressed
        │ no                                              (no paywall)
        ▼
  A60 Paywall (sheet, trigger-specific copy, Close top-left)
        │
   ┌────┴──────────┬──────────────┐
   ▼               ▼              ▼
 See plans      Subscribe      Close
   │               │              │
   ▼               ▼              ▼
 A61            A62 Checkout   return, unchanged
   │               │
   └──────▶────────┤
                   ▼
          [provider payment sheet]
                   │
        ┌──────────┼──────────┬──────────────┐
        ▼          ▼          ▼              ▼
    success    declined   cancelled    process death
        │          │          │              │
        ▼          ▼          ▼              ▼
    A63 ──▶   specific   return to     RESTART flow
    the feature reason +  A60          (never resume)
    they tapped retry
```

**Never sold against a live threat.** A paywall triggered from a fraud detail
speaks about evidence and depth, never about danger. The verdict itself is
always free.

---

## F10 · Changing a consent

```
  A53 Privacy and data
        │
        ▼
  Toggle one consent  (exactly one; nothing is bundled)
        │
   ┌────┴─────────────────────────┐
   ▼                              ▼
 grant                        withdraw
   │                              │
   ▼                              ▼
 effective immediately     consequence stated with counts
 consequence text          ("12 recordings will be deleted")
 updates after the toggle          │
                              ┌────┴────┐
                              ▼         ▼
                          confirm    cancel
                              │         │
                              ▼         ▼
                        applied +    toggle
                        data deleted reverts
                              │      visibly
                              ▼
                        consent record written
                        (timestamped, versioned
                         against the policy text
                         the user saw)
```

Consent events are exempt from the analytics opt-out because they are a legal
record rather than product analytics — and the analytics consent's own
consequence line says so.

---

## F11 · Exporting or erasing data

```
  Export:  A53e ──▶ choose scope ──▶ audio? (second explicit confirm)
                ──▶ generate (>10 s: determinate + cancel)
                ──▶ system share sheet.  We never email it.

  Erase:   A53d ──▶ 1. what will be deleted, itemised with counts
                ──▶ 2. what will not (carrier records, retained billing)
                ──▶ 3. type last 4 digits of the number
                ──▶ forwarding cleared on the carrier  ◀── mandatory
                ──▶ account erased
                ──▶ app returns to A01

  Failure: transactional. Complete, or not started. Never partial.
```

Clearing forwarding as part of erasure is not a nicety. Leaving a former
subscriber's calls forwarding to a platform that no longer holds their account
would be a serious defect.

---

## F12 · Moving to a new device

```
  New install ──▶ A02 number ──▶ A03 OTP ──▶ integrity
                                                │
                                                ▼
                                    Existing device detected
                                                │
                                                ▼
                              A16: "This will sign out <model>,
                                    last active <when>."
                                                │
                                    ┌───────────┴───────────┐
                                    ▼                       ▼
                                confirm                  cancel
                                    │                       │
                                    ▼                       ▼
                        old device notified BEFORE       exit
                        the switch completes, with
                        a "This wasn't me" action
                                    │
                                    ▼
                        old device ──▶ A73 session revoked,
                                       local caches cleared
                                    │
                                    ▼
                        new device: history syncs,
                        forwarding re-verified (same SIM
                        → usually still active; different
                        SIM → F1 from A05)
```

Notifying the old device **before** the switch completes is the anti-social-
engineering control. An attacker who has the OTP still has to beat a
notification on the victim's own phone.

---

## F13 · Joining and leaving an organisation

```
  Admin invites (Business Portal) ──▶ SMS/email to the member
        │
        ▼
  Member opens link ──▶ authenticated? ── no ──▶ A02 (invite preserved
        │ yes                                     server-side, NOT in
        ▼                                         the deep link)
  A56-style consent screen:
  "Your organisation will be able to see calls,
   transcripts and verdicts on the business line.
   They will not be able to see your personal line."
        │
   ┌────┴────┐
   ▼         ▼
 accept    decline
   │         │
   ▼         ▼
 business  nothing changes
 line appears in the switcher
 A56 appears in Settings
        │
        ▼
  ── later ──
        │
   ┌────┴──────────────┬──────────────┐
   ▼                   ▼              ▼
 member leaves    admin removes    org deleted
   │                   │              │
   └─────────┬─────────┴──────────────┘
             ▼
  Business line and A56 disappear.
  One-time notification explaining it.
  PERSONAL HISTORY IS UNTOUCHED, and the
  notification says so explicitly.
```

The invite is preserved server-side against the authenticated identity, never
carried in the deep link — an org-joining link that works for whoever holds it
is a privilege-escalation primitive.

---

## F14 · Losing and regaining the screening role

```
  Another app takes ROLE_CALL_SCREENING  (Android permits this freely)
        │
        ▼
  Detected on next foreground
        │
        ▼
  A75 blocking banner on A20:
  "Another app is now screening your calls. Numbers you've
   blocked here won't be blocked until you switch back."
        │
   ┌────┴────┐
   ▼         ▼
 Switch    ignore
 back        │
   │         ▼
   │    banner persists; forwarding-based screening
   │    CONTINUES to work — only the free on-device
   │    pre-filter is lost. The copy says which.
   ▼
 system role request ──▶ granted ──▶ banner clears, haptic.confirm
```

Not a full-screen block, because forwarding, transcripts and history are all
unaffected. Blocking the entire app over a degraded pre-filter would be
disproportionate — and would teach the user that our alarms are exaggerated.

---

## F15 · Going offline and coming back

```
  Connectivity lost
        │
        ▼
  A70 chip: "You're offline. Calls are still being screened."
        │
        ├── feed: cached, with age. >24 h → age in warning colour
        ├── detail: cached for last 50 calls
        ├── search: local scope, stated
        ├── block/allow: applied to on-device pre-filter NOW, queued for sync
        ├── live screening: not enterable
        ├── assistant: disabled with reason, local search offered
        └── consents & payments: blocked, with reason
        │
        ▼
  Reconnect
        │
        ▼
  chip exits (short/accelerate)
  queued mutations flush
        │
   ┌────┴─────────────┐
   ▼                  ▼
 something         nothing
 was queued        was queued
   │                  │
   ▼                  ▼
 one transient     silence
 confirmation
```

Blocks apply offline because the on-device pre-filter enforces them without a
server round trip (ADR-0002 §5). This is a genuine architectural guarantee and
the product states it once, in the blocklist's first-run empty state, because
users do not assume it.
