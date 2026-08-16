# Business Portal · 3 · Flows and Journeys

---

## 3.1 Flow index

| # | Flow | Role | Terminal states |
|---|---|---|---|
| P1 | Signup to first screened business call | `owner` | Live · Partially configured · Abandoned |
| P2 | Adding and configuring a number | `owner` `admin` | Live · Porting · Failed |
| P3 | Inviting a member and assigning lines | `owner` `admin` | Joined · Declined · Expired |
| P4 | A business call is screened and transferred | — | Answered · Voicemail · Message taken · Missed |
| P5 | Reviewing calls and following up | `member` `admin` | Reviewed · Contact updated |
| P6 | A number stops forwarding | `owner` `admin` | Recovered · Unrecovered |
| P7 | Hitting a plan limit | `owner` `billing` | Upgraded · Absorbed overage · Degraded |
| P8 | Integrating via API | `admin` | Live · Endpoint disabled |
| P9 | A member leaves | `owner` `admin` | Removed cleanly |
| P10 | Cancelling | `owner` `billing` | Cancelled · Retained · Data exported |

---

## P1 · Signup to first screened business call

```
  B01 Sign in (MSISDN + OTP + device trust)
        │
        ▼
  B03 Create organisation
      name · timezone · GST (optional now, required before invoicing)
        │
        ▼
  B05 Onboarding — first number
        │
   ┌────┴────────────────┬─────────────────────┐
   ▼                     ▼                     ▼
 use our number    screen the number     port a number
 (from the DID     we already have             │
  pool)                  │                     ▼
   │                     ▼               multi-day process,
   ▼            verify ownership         tracked, NEVER
 provisioned    (call-back code)         presented as instant
   │                     │                     │
   │                     ▼                     │
   │            conditional forwarding         │
   │            — same honest disclosure       │
   │              as the Android app,          │
   │              including possible           │
   │              carrier charges              │
   │                     │                     │
   └──────────┬──────────┴─────────────────────┘
              ▼
        Business hours
              │
              ▼
        What the assistant should say
        (greeting · what it may share · voice · language)
              │
              ▼
        Who gets the calls
        (routing + fallback, with the PLAIN-LANGUAGE PREVIEW)
              │
              ▼
        Test call ──▶ transcript shown
              │
              ▼
        B10 Dashboard, live
```

**Target: under 10 minutes, with no implementation call.** The buyer is a clinic
manager, not an IT department. Every step that needs explaining is a step that
loses a customer.

**Abandonment** — The organisation persists with whatever was configured. The
dashboard's first-run state resumes the flow. A half-configured number is
**never** put into service; it shows as "not live" with the remaining step.

---

## P2 · Adding and configuring a number

```
  B30 ──▶ B32 Add a number
              │
   ┌──────────┼──────────────┬────────────────┐
   ▼          ▼              ▼                ▼
 new from   screen an     port in        add a member's
 the pool   existing                     existing business
   │        number                       line
   ▼          │                                │
 region       ▼                                ▼
 availability verify ownership          same forwarding flow,
 stated       (call-back code to        with the member's
   │           the number)              consent required
   │              │
   └──────┬───────┘
          ▼
    B31 Number detail
          │
   ┌──────┼───────┬─────────┬──────────┬────────────┐
   ▼      ▼       ▼         ▼          ▼            ▼
 label  assign  hours    routing   assistant   recording +
        members                                retention
                            │
                            ▼
                  PLAIN-LANGUAGE PREVIEW
                  rendered from the actual config
                            │
                     ┌──────┴──────┐
                     ▼             ▼
                  save         rule can never fire
                     │         → flagged at save
                     ▼            with an explanation
              effective time
              stated; live
```

**The preview is not optional.** Routing is configured as a form and understood
as a sentence. A business owner setting up a phone tree at 9 pm gets the
sentence.

---

## P3 · Inviting a member and assigning lines

```
  B40 ──▶ B42 Invite
            number · role · lines
              │
              ▼
      Invitation SMS to that number
      (bound server-side to that number —
       the link alone grants nothing)
              │
              ▼
      Member opens it
              │
      authenticated as that number?
        ┌─────┴──────┐
       yes           no
        │             │
        │             ▼
        │      B01 sign in as the INVITED number.
        │      A different number is refused with
        │      a clear statement.
        │             │
        └──────┬──────┘
               ▼
      Consent screen — the SAME SENTENCES the
      member sees in the Android app (A56):
      "Your organisation will be able to see calls,
       transcripts and verdicts on the business line.
       They will not be able to see your personal line."
               │
        ┌──────┴──────┐
        ▼             ▼
     accept        decline
        │             │
        ▼             ▼
  member joins   admin notified;
  business line  invitation closed;
  appears in     no partial state
  their Android
  app switcher
```

**Both sides read the same sentence.** The administrator sees it in `B91`, the
member sees it in `A56` and again here. That symmetry is what makes it a
boundary rather than a marketing claim.

---

## P4 · A business call is screened and transferred

```
  Caller dials the business number
        │
        ▼
  Rings for the configured delay ──▶ answered by a person ──▶ normal call
        │ not answered                                        (nothing to do)
        ▼
  Carrier forwards ──▶ gateway answers ──▶ ANNOUNCEMENT (I1)
        │
        ▼
  Assistant converses, per this number's configuration
        │
   ┌────┼───────────┬───────────────┬────────────────┐
   ▼    ▼           ▼               ▼                ▼
 within  outside  emergency     fraud/spam      caller asks
 hours   hours    detected      detected        to be transferred
   │       │          │             │                │
   ▼       ▼          ▼             ▼                ▼
 transfer after-   HANDOFF —    flagged;        UNTRUSTED INPUT.
 per      hours    the same     routing         Routing follows
 routing  message  Invariant    proceeds        CONFIGURED rules
   │      / vm     U7 rule      normally        only — never a
   │       │       applies                      caller's request
   ▼       │                                    (Invariant I4)
 target rings
   │
 ┌─┴──────────────┬──────────────┐
 ▼                ▼              ▼
answered      no answer      target removed
 │            in N seconds   from the team
 ▼                │               │
call proceeds     ▼               ▼
              fallback      falls back, alert
              (voicemail /  raised, routing
               message)     screen shows the
                            broken target
        │
        ▼
  Record appears in B20, with the transfer target
  and whether it was answered.
  UNANSWERED TRANSFERS ARE SURFACED PROMINENTLY —
  it is the metric a business actually cares about.
```

**A caller asking to be transferred is not a routing instruction.** Caller
speech is untrusted input (Invariant I4). Routing follows the configuration and
nothing else, and a caller who says "put me through to the manager" gets
whatever the rules say, not what they asked for.

---

## P5 · Reviewing calls and following up

```
  B20 Insights
     filter: number · outcome · verdict · member · date
        │
        ├── save this filter as a view (team-shared or private)
        │
        ▼
  B21 Call detail
        │
   ┌────┼──────────┬────────────┬──────────────┐
   ▼    ▼          ▼            ▼              ▼
 read  add to   add a note   block the      export
 turns CRM                    number
        │
        ▼
  B51 Contact detail — every call from this caller
```

**`member` role sees only assigned lines.** The filter does not offer other
lines, and the limitation is explained once, in place, rather than presented as
an empty result set.

---

## P6 · A number stops forwarding

The business analogue of `F7`, and more consequential — a clinic's reception
line not being screened for a day is a business problem, not an inconvenience.

```
  Health check fails for a number
        │
        ▼
  B10 Dashboard: BANNER AT THE TOP, naming the number
  Email to owner and admins
  B30/B31 show the number as unhealthy
        │
        ▼
  B35 Number forwarding health
        │
        ├── same diagnosis classes as A52
        ├── carrier-specific instructions
        └── the re-activation flow
        │
   ┌────┴─────────────────┐
   ▼                      ▼
 recovered           still failing
   │                      │
   ▼                      ▼
 banner clears      support path, with the
                    console's C23 diagnosis
                    available to the agent
```

**Calls to an unforwarded number simply ring.** The failure mode is "the phone
rings normally" (ADR-0002 §6) — which for a business means a human has to answer
it, which is inconvenient but never lost. The banner says exactly this, because
a business owner's first fear is that customers are getting a dead line.

---

## P7 · Hitting a plan limit

```
  Usage crosses 80%
        │
        ▼
  Sidebar indicator turns to warning
  Dashboard tile states the projection ("you'll reach your
  limit around the 24th at this rate")
        │
        ▼
  Usage crosses 100%
        │
   ┌────┴───────────────┬────────────────────┐
   ▼                    ▼                    ▼
 plan allows        plan has no        owner upgrades
 overage            overage                  │
   │                    │                    ▼
   ▼                    ▼              immediate, prorated,
 overage rate      CALLS RING          stated
 stated per unit   THROUGH
 on the dashboard  UNSCREENED
 and the invoice        │
                        ▼
                  Dashboard states this
                  plainly. Not a silent
                  degradation.
```

**A limit is never discovered from a failure.** Usage is visible in the sidebar
on every screen, the dashboard projects the crossing date, and the consequence
is named before it happens.

---

## P8 · Integrating via API

```
  B70 ──▶ B71 Create key
            scopes (default minimal)
              │
              ▼
        KEY SHOWN EXACTLY ONCE
        copy · explicit acknowledgement before close
        (closing without copying warns first)
              │
              ▼
        B72 Webhook endpoint
              │
        payload contains identifiers by default.
        Including content requires a separate opt-in
        that NAMES what will be transmitted.
              │
              ▼
        B73 Delivery logs
              │
   ┌──────────┼──────────┐
   ▼          ▼          ▼
 delivering  retrying  failing 24 h
                            │
                            ▼
                   ENDPOINT DISABLED + notification.
                   Not retried forever — an endpoint
                   that has been dead for a day is
                   dead, and hammering it is our
                   problem becoming theirs.
```

---

## P9 · A member leaves

```
  B41 ──▶ Remove
            │
            ▼
  Confirmation states:
    · which lines they lose access to
    · that their PERSONAL call history is untouched
    · that call records they handled REMAIN with the organisation
            │
            ▼
  Removed
            │
   ┌────────┼──────────────────┐
   ▼        ▼                  ▼
 sessions  routing targets  Android app:
 re-scoped pointing at      business line and
 immediately them → FALL    A56 disappear, with
           BACK, alert      a one-time notification
           raised, routing  explaining it and
           screen shows     stating personal history
           the gap          is untouched
```

**"Their personal call history is untouched" appears in the confirmation
dialog.** An administrator should never believe they are deleting a person's
data by removing them from a team.

---

## P10 · Cancelling

```
  B81 ──▶ Cancel
            │
            ▼
  What stops, and when — itemised
  What happens to the numbers — released after the
    retention window, recoverable until then
  What happens to the data — retained per policy,
    exportable NOW
            │
            ▼
  EXPORT OFFERED FIRST, prominently
            │
            ▼
  One retention offer. Exactly one.
            │
       ┌────┴────┐
       ▼         ▼
   confirm    stay
       │
       ▼
  Cancelled at period end, not immediately.
  Screening continues until then, because
  they paid for it.
```

---

## 3.2 Buyer journey

| Stage | Who | What they need | Where we win or lose |
|---|---|---|---|
| **Evaluation** | Owner, after missing calls | To believe it will answer their phone properly | The test call in `B05`. Nothing else convinces |
| **Setup** | Owner, alone, evening | To finish without help | Under 10 minutes, plain-language routing preview, no jargon |
| **First week** | Owner, anxious | To see it working | `B10` dashboard, and `B20` transcripts they can read |
| **Adoption** | Members | To not feel surveilled | `A56` and `P3`'s consent screen. If members distrust it, the owner abandons it |
| **Steady state** | Admin, weekly | Missed transfers, flagged calls | `B20` filtered views, saved |
| **Growth** | Owner | More numbers, more members | `P2` and `P3` must stay frictionless as they scale to 5 numbers |
| **Renewal** | Owner or bookkeeper | To reconcile usage against the invoice | `B61`. An unreconcilable invoice is a churn event |

### The adoption risk nobody plans for

> The owner buys it. The staff quietly resent it, because they believe it is
> monitoring them.

Two design decisions address this directly, and they are the reason the product
survives past month one in a business:

1. **Analytics show call handling, not member behaviour.** No per-member
   response-time leaderboard, no "who answered fastest" chart. A screen that
   reads as employee surveillance will be used as one.
2. **The personal/business boundary is stated to the member in their own app**,
   in the same words the administrator sees. The member does not have to take
   their employer's word for it.

---

## 3.3 Metrics

| Metric | Why | Direction |
|---|---|---|
| Time from signup to first screened business call | The activation metric | Down, target < 10 min |
| Share of organisations completing setup unaided | Whether it is self-serve | Up |
| **Unanswered transfers as a share of transfers** | The business's actual pain | Down |
| Number forwarding uptime | The `P6` failure | Up, > 99% |
| Member acceptance rate on invitation | The adoption risk above | Up |
| Members active weekly / members invited | Whether staff actually use it | Up |
| Usage reconciliation support tickets | Whether `B61` is doing its job | Down |
| Plan-limit surprises (limit hit with no prior 80% view) | Should be **zero** | Zero |
| Routing rules flagged as unreachable at save | The preview is working | Non-zero is healthy |
| Cancellations citing "didn't understand what it was doing" | A UX failure, not a product one | Down |
