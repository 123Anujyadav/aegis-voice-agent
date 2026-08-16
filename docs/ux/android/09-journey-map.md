# Android · 9 · User Journey Map

The experience over time, not over screens. Where trust is built, where it is
spent, and where the product is most likely to lose someone.

---

## 9.1 The arc

```
  TRUST
   high │                                                    ╭────────────
        │                                          ╭─────────╯
        │                              ╭───────────╯
        │            ╭─────╮          ╱
        │           ╱       ╲    ╭───╯
        │      ╭───╯         ╲──╯
   base │─────╯               ▼
        │                  the dip
    low │
        └────┬──────┬─────────┬────────┬─────────┬──────────┬──────────▶
          Install  Setup   First     Days      First      Month 2+   TIME
                           screening  2–7      fraud
                                              catch
```

| Stage | Duration | Emotional state | The product's job |
|---|---|---|---|
| **Consideration** | Minutes | Sceptical, slightly alarmed | Be legible. Do not oversell |
| **Setup** | ~3 min | Anxious — carrier change, permissions | Earn each step. Leave something working behind each one |
| **First screening** | Seconds | Curious, then relieved or confused | Be fast and be honest |
| **The dip** | Days 2–7 | Doubt: "is this actually doing anything?" | Prove it without nagging |
| **Habit** | Weeks 2–4 | Ambient trust | Be quiet |
| **The fraud catch** | One moment | Vindication, then loyalty | Show the work |
| **Long tail** | Months | Forgetting it exists — the goal | Do not break. Do not lapse silently |

---

## 9.2 Stage by stage

### Consideration

**Where they come from** — A friend's recommendation, an app store listing, or a
recent scam call. The last of these is the highest-intent and the most anxious.

**What they are thinking** — *Will this app listen to my calls?* This is the
first question, always, and it is asked before any feature question.

**What we do** — `A01` states the function in one sentence. `A09b`'s rationale
answers the listening question with the platform fact: no app can access call
audio, including this one. Converting the architecture's biggest limitation into
the trust claim is the highest-leverage copy decision in the product.

**Where we lose them** — A permissions list at install. A persona. Any sentence
containing "AI-powered".

---

### Setup

**Peak anxiety: `A06` and `A07`.** We are asking for a carrier configuration
change, dialled from a code they do not understand, that may cost them money.

**What we do**

- Show the announcement text verbatim before asking for consent to use it
- State the possible carrier charge before, not after
- Show the undo code (`##61#`) **before** the setup code
- Show the MMI string itself, selectable
- Warn that the dialer will open and it will look like a call

**The undo-first move is the flow's most counterintuitive decision and its most
important.** A product that shows you the exit before the entrance is one you
will walk into.

**Where we lose them** — A dialer that opens without warning. A verification
that fails without a carrier-specific next step. Bundled consent.

**Instrumented** — Per-step dwell and drop. Carrier-segmented, because ADR-0002
§16 makes a > 2% forwarding failure rate an architecture review trigger, and
setup drop-off is where it will show first.

---

### First screening

**The moment the product becomes real.** Usually the test call (`A13`), which is
why the test call is the most valuable screen in onboarding: it makes the
abstraction concrete and validates the entire pipeline in one action.

**What they are thinking** — *Did it actually talk to them? What did it say?*

**What we do** — Show the transcript immediately after the test call. Not a
success screen: the artefact.

**Where we lose them** — A test call that does not arrive. A transcript that
reads like a robot. A screening that takes so long the caller hangs up — the
agent's brevity is a UX preference **and** a cost control (ADR-0002 §11).

---

### The dip · days 2–7

The most dangerous stage, and the one most products ignore.

**What they are thinking** — *Has anything happened? Is it still on? Did I break
my phone?*

**The pull toward the wrong solution** is a weekly digest, a stats card, a
"you're protected!" banner. All of them are engagement devices (Principle 6) and
all of them train the user to treat our notifications as noise.

**What we actually do**

| Instead of | We do |
|---|---|
| A weekly digest | Nothing. Silence is the correct output when nothing happened |
| A "you're protected" hero | The Protection tab, which they can check when they wonder — and every number in it links to its evidence |
| An engagement notification | The forwarding health indicator, which answers the real question ("is it on?") factually and on demand |
| A streak | Nothing |

**The real risk in this window is a silent forwarding lapse** (ADR-0002 §15,
likelihood high). A user who wonders whether it is working, and for whom it
genuinely is not, is lost permanently. This is why Invariant U2 exists and why
`A52` is a destination rather than a settings row.

**Instrumented** — Day-2 through day-7 retention against forwarding-health
uptime. If those two correlate, the lapse is the churn cause, and it is a
platform problem rather than a product one.

---

### Habit · weeks 2–4

**What they are thinking** — Nothing. That is the goal.

**Behaviour** — They open the app when they get a notification, and roughly once
a day otherwise, for under 20 seconds. They read a summary and close it.

**What we do** — Be fast to cold start (< 500 ms to content, S1). Put the answer
in the feed so most sessions need no navigation. Interrupt for three things
only.

**What we must not do** — Add a reason to open the app. A call-screening product
whose engagement metrics are going up is failing at its actual job.

---

### The fraud catch

The moment that converts a subscriber into an advocate. It happens once,
somewhere between week 2 and month 3, and everything about `A31d` is designed
for it.

**What they are thinking** — *It caught one.* Then, immediately: *was it
right?*

**What we do** — Show the work. Verdict, pattern, the exact quoted turns, a
plain-language explanation of the scam, and three actions including **"It wasn't
fraud"** at equal weight.

**The dispute path being first-class is the counterintuitive part.** Making
disagreement easy is what makes agreement meaningful — and the resulting event
is the product's only real-world precision signal.

**Where we lose them** — A false positive on a legitimate call, rendered with
false confidence. Which is why confidence is always shown, and why "Possibly"
exists as a distinct product state
([`09 §RiskIndicator`](../../design/09-components.md)).

---

### Long tail · month 2 onward

**What they are thinking** — Nothing, until something breaks or a bill arrives.

**The three moments that matter**

| Moment | Risk | Design response |
|---|---|---|
| Forwarding lapses | Silent failure, churn | `A71` + `forwarding_health` channel + `A52` |
| Renewal | Value questioned at the exact moment of payment | `A64` states what they get, honestly, and cancel is in plain words |
| A new phone | Account moves or is lost | `F12`, with the old-device notification as the anti-social-engineering control |

**The success condition of this stage is that the user cannot remember the last
time they thought about the product.** That is an unusual North Star and it
should be stated as one.

---

## 9.3 Persona-specific journeys

Not demographic personas. **Situational** ones — the same person can be all
three in a month.

### The harassed subscriber

40+ spam calls a week. Came for silence.

- **Success by day 2.** The volume drop is immediate and unmistakable
- Lives in the feed and the blocklist. Rarely opens a transcript
- Bulk spam blocking (`A36`) is their most-used feature
- **Churn risk:** a false positive on a delivery or a school call. Mitigation is
  the allowlist, surfaced from the first missed-important-call moment

### The cautious subscriber

Few calls, high anxiety about scams, often older, often set up by a family
member.

- **Setup is done by someone else**, which means `A56`-style clarity about who
  can see what matters even for personal accounts
- Reads every transcript. `A22` is their home screen, not `A20`
- **The accessibility flows are their normal path**, not an alternative: larger
  text, full verdict descriptions (`A57`)
- **Churn risk:** confusion about whether it is working. Mitigation is `A52`
  reachable in one tap and stated in plain language

### The deaf or hard-of-hearing subscriber

The product's highest-value user, and the one for whom it is transformative
rather than convenient.

- Screening turns an unusable device into a usable one
- **Every flow is a reading flow.** Live transcript latency is the single
  quality metric that matters to them
- Takeover is used differently — often to a text relay or a video-relay service,
  not to speech
- **Invariant U6 is written for this user**, and the product's most important
  accessibility decision is that nothing is ever available by listening that is
  not available by reading

### The business member

Has a personal line and a work line on one phone.

- Their first question is: **can my employer read my personal calls?**
- `A56` answers it explicitly and prominently, and that answer is the reason
  they keep the app installed
- Uses the line switcher constantly; never opens the portal on mobile
- **Churn risk:** ambiguity about the boundary. Mitigation is that the boundary
  is stated in the product, not only in a policy

---

## 9.4 Where trust is spent, and where it is earned

| Moment | Trust | Why |
|---|---|---|
| Asking for the screening role | **Spent** | It sounds like we want to listen |
| Saying "we can't hear your calls — no app can" | **Earned** | It is true, verifiable, and unexpected |
| Dialling an MMI code | **Spent** heavily | Opaque, telephony-altering, irreversible-feeling |
| Showing `##61#` before asking | **Earned** | Exit before entrance |
| The test call | **Earned** | It works, and they experienced it |
| A screening that takes 40 s | **Spent** | The caller hung up |
| Showing confidence on a verdict | **Earned** | Nobody else does |
| A false positive rendered confidently | **Spent**, catastrophically | One is enough to discredit all of them |
| "It wasn't fraud" being easy | **Earned** | It says we would rather be corrected than believed |
| A silent forwarding lapse | **Spent**, terminally | We billed them for nothing and did not say |
| Not sending a weekly digest | **Earned** slowly | They notice the absence of noise |
| A paywall over a fraud verdict | **Spent** — which is why it does not exist | |

---

## 9.5 The metrics that describe this journey

| Stage | Primary metric | Failure threshold |
|---|---|---|
| Setup | Completion rate to `A08` verified, **by carrier** | Any carrier below the cohort median − 10 pts is a carrier-matrix defect |
| First screening | Test-call success rate | < 90% is a pipeline defect |
| The dip | Day-7 retention **correlated with forwarding uptime** | Correlation above 0.4 means lapse is the churn cause |
| Habit | Sessions per day, and session length | **Rising engagement is a warning**, not a win |
| Fraud catch | Verdict-to-action agreement rate | Disagreement > 20% is a model precision problem |
| Long tail | Forwarding uptime per subscriber | < 98% over 7 days triggers ADR-0002 review |
| Everywhere | Time from screening start to takeover | > 6 s means the live surface is too slow to be useful |
| Everywhere | Emergency false-positive rate | **Launch blocker.** Any non-trivial rate disables the feature |
