# Android · 1 · Information Architecture

Surface 1. The primary product.

---

## 1.1 The organising question

Most call apps organise around **the phone**: dialer, recents, contacts,
voicemail. That IA is forty years old and it is wrong for this product, because
this product's user does not make calls with it. They receive an outcome from it.

CallScreen organises around **what happened, what protects you, what answers for
you, and what you control**:

| Section | The question it answers |
|---|---|
| **Calls** | What happened while I wasn't paying attention? |
| **Protection** | What is being kept away from me, and is it working? |
| **Assistant** | Who is answering my phone, and how? |
| **Settings** | What do I control? |

Four sections, four questions, no overlap. A screen that does not clearly belong
to exactly one of them is in the wrong place, and if it belongs to two, the IA
is wrong rather than the screen.

---

## 1.2 The hierarchy

```
CallScreen
│
├── ONBOARDING  (pre-account, linear, exits permanently)
│   ├── Welcome
│   ├── Identity          Phone number → OTP → device trust
│   ├── Line setup        SIM selection → carrier → forwarding → verify
│   ├── Permissions       Notifications → screening role → contacts
│   ├── Consent           Announcement · recording · retention · analytics
│   ├── Assistant setup   Name → language → voice → greeting style
│   └── Test call
│
├── CALLS ─────────────────────────────────────────── tab 1, default
│   ├── Feed                              all calls, newest first
│   │   ├── Filters                       sheet
│   │   └── Search                        full-screen surface
│   ├── Call Detail                       transcript · timeline · verdict
│   │   ├── Transcript search             in-screen
│   │   ├── Audio playback                conditional — only if recorded
│   │   └── Share / export
│   ├── Caller Profile                    every call from this number
│   └── Live Screening                    full-screen takeover, transient
│       └── Takeover → system in-call UI
│
├── PROTECTION ─────────────────────────────────────── tab 2
│   ├── Overview                          what was stopped, and how
│   ├── Fraud                             flagged calls, evidence
│   │   └── Fraud Detail                  → deep-links into transcript
│   ├── Spam                              flagged calls, patterns
│   ├── Blocklist
│   ├── Allowlist
│   └── Report a number
│
├── ASSISTANT ──────────────────────────────────────── tab 3
│   ├── Ask                               conversational, over own history
│   ├── Behaviour                         how it answers
│   │   ├── Greeting style
│   │   ├── Instructions                  free-text, bounded
│   │   ├── Information it may share      strict allow-list
│   │   └── When to screen                availability rules
│   ├── Voice                             picker with preview
│   └── Language                          language + script
│
└── SETTINGS ───────────────────────────────────────── tab 4
    ├── Account                           number, plan badge, devices
    ├── Forwarding                        health, SIM, carrier, re-activate
    ├── Privacy & data                    consents, retention, export, erasure
    ├── Notifications
    ├── Premium                           plan, billing, invoices
    ├── Business                          visible only to organisation members
    ├── Accessibility
    ├── Appearance                        theme, text size
    └── About                             legal, grievance officer, support
```

---

## 1.3 Section rationale

### Calls is the default and the home

The user opens the app to answer one question: *what happened?* Anything else on
the launch screen is a tax on the common case. There is no dashboard, no
"today's summary", no hero card. **The feed is the home screen.**

### Protection is a section, not a settings page

Fraud and spam handling is the product's second promise and its primary premium
driver. Burying blocklists in Settings — the conventional placement — makes the
protection invisible, and invisible protection is unbelievable protection.

It is also where Principle 2 lives: this section exists so the user can *check
our work*, which is why it leads with what was stopped and links every claim to
its evidence.

### Assistant is configuration and conversation in one place

The subscriber's mental model of "the AI" is singular even though the
architecture has two agents. Splitting configuration ("how it answers") from
conversation ("ask it something") across two sections would force the user to
learn our architecture.

They are unified in the IA and **separated in the interface** by Principle 3:
the `Ask` surface is clearly a conversation with the Personal Assistant, and
`Behaviour` is clearly configuration of the Screening Agent. The section header
copy does that work explicitly.

### Settings is deliberately conventional

Four sections is already an unusual IA for a phone app. Settings is where users
have the strongest existing expectations and the least tolerance for
inventiveness. It is a standard grouped list, in the order users look for
things, with Forwarding promoted to second position because it is the one
setting in this product that can silently break everything (Invariant U2).

---

## 1.4 What is deliberately absent

| Absent | Why |
|---|---|
| **A dialer** | We are not a phone app. Making calls is the system dialer's job and competing with it is a losing, permission-heavy fight |
| **A contacts manager** | We read contacts for the pre-filter. We do not own them |
| **Voicemail** | Out of scope. Carrier voicemail continues to work and the timeline says when a call went there |
| **A home dashboard** | See above — the feed is the home |
| **Notification inbox / activity feed** | A second inbox for things that were already notifications. Pure redundancy |
| **Onboarding checklist that persists after onboarding** | A permanent "3 of 5 steps complete" card is an engagement device, and a nag. Incomplete setup surfaces contextually, where it matters |
| **Social / community layer** | Number reputation is a backend signal, not a social feature. A user-visible "community" invites brigading of legitimate numbers |
| **Gamification of any kind** | Principle 6, anti-patterns |
| **A separate "AI" tab distinct from Assistant** | Two AI destinations would confirm exactly the confusion Principle 3 exists to prevent |

---

## 1.5 Cross-cutting surfaces

Surfaces reachable from more than one section, which therefore belong to none.

| Surface | Reachable from | Behaviour |
|---|---|---|
| **Live Screening** | Notification, Calls feed banner, any screen's persistent chip | Full-screen takeover. Not a tab, not in the back stack of any section. Exiting returns to wherever the user was |
| **Search** | Calls top bar, Protection top bar | Full-screen. Scope is stated and differs by entry point |
| **Call Detail** | Calls, Protection, Search, Caller Profile, Assistant citation, notification | One screen, one route, many entry points. `entry_point` is an analytics dimension |
| **Paywall** | Any gated feature | Modal sheet, never a screen replacement. Closes in one obvious tap |
| **Forwarding health** | Persistent banner, Settings, notification | Its own screen because Invariant U2 requires it to be reachable in one tap |
| **Emergency handoff** | Detection during a screening | Overrides everything. Not in any hierarchy — see [`04 §A72`](04-screens-screening-and-calls.md) |

---

## 1.6 Progressive disclosure

The app has three visible complexity levels, and a user only ever sees the one
they are in.

| Level | Who | What changes |
|---|---|---|
| **Consumer, free** | Default | Four tabs. Protection shows spam and blocklist; fraud detail is gated. Assistant shows behaviour basics |
| **Consumer, premium** | Paying subscriber | Fraud detail, longer retention, audio recording, multi-language, priority screening, advanced instructions |
| **Business member** | Belongs to an organisation | A **line switcher** appears in the Calls top bar. A **Business** section appears in Settings. Nothing else moves |

**Adding a line does not restructure the app.** The line switcher is a filter on
an existing IA, not a new navigation level. A user who joins their employer's
organisation on Monday finds the same app on Tuesday with one more control.

Business *administration* — team, numbers, billing, CRM, API keys — lives in the
Business Portal (Surface 3), not here. The Android app for a business user is
still a personal call-screening app that happens to also show a work line.

---

## 1.7 Content model

The objects the user manipulates, and their relationships. This is what the IA
is an arrangement *of*.

```
  Subscriber ─┬─ has many ─▶ Line (personal | business)
              │                 │
              │                 └─ has one ──▶ ForwardingConfig ──▶ health
              │
              ├─ has many ─▶ Device      (trusted, revocable)
              ├─ has one  ─▶ Plan        (free | premium | business seat)
              ├─ has many ─▶ Consent     (purpose-bound, withdrawable)
              └─ has many ─▶ Call
                              │
                              ├─ has one  ─▶ Caller     (number, optional identity)
                              ├─ has one  ─▶ Outcome    (rang | screened | blocked | missed)
                              ├─ has one  ─▶ Screening  (only if screened)
                              │                │
                              │                ├─ has one  ─▶ Announcement  (I1, always)
                              │                ├─ has many ─▶ Turn          (caller | assistant)
                              │                ├─ has zero-or-one ─▶ Verdict (level + confidence)
                              │                ├─ has zero-or-one ─▶ Recording (opt-in only)
                              │                └─ has one  ─▶ Timeline
                              │
                              └─ has many ─▶ UserAction (blocked, reported, allowed, taken over)
```

### Object rules that shape the IA

| Rule | Consequence |
|---|---|
| A **Call** always exists; a **Screening** may not | The feed shows every call, including the ones we never touched. A feed that only shows screened calls would misrepresent what the product did |
| A **Verdict** may be absent, and absent ≠ safe | `Unknown` — "Not assessed" — is a rendered state, not a blank ([`02 §2.6`](../02-interaction-language.md)) |
| A **Recording** exists only with consent | Audio playback is conditional UI, never a greyed-out control implying we have audio we are withholding |
| **Consents are per-purpose** | One screen, one list, one toggle each, one consequence line each (Invariant U9) |
| **Caller identity is either verified or claimed** | Verified identity renders as a name; claimed identity renders as quoted caller speech ([`02 §2.6`](../02-interaction-language.md)) |
| A **UserAction** is part of the record | The timeline shows what the user did, not only what the system did. It is their record too |

---

## 1.8 Naming

Product vocabulary. Fixed, because inconsistent naming across 50 screens is how
a product stops feeling designed.

| Concept | We say | We never say |
|---|---|---|
| The AI answering a call | **Assistant** | AI, bot, agent, a name, a persona |
| The process | **Screening** | Filtering, guarding, protecting, vetting |
| Joining a live screening | **Take call** | Barge in, intercept, interrupt, take over |
| Ending it from the app | **Decline** | Reject, hang up, dismiss |
| Adding to blocklist | **Block** | Ban, blacklist |
| The record | **Transcript** | Log, recording (unless it is literally audio) |
| The judgement | **Verdict** shown as its label | Score, rating, threat level |
| Call forwarding | **Forwarding** | Diversion, CFNRy, redirect |
| Number | **Your number** | MSISDN, line (except in Business context) |
| Paid tier | **Premium** | Pro, Plus, Gold |
| Uncertain verdict | **Possibly** / **Likely** | Maybe, might be, low confidence (in labels) |
| Data deletion | **Delete** and **Erase my data** | Purge, wipe, remove |

Regulatory terms — *consent*, *personal data*, *data principal*, *grievance
officer* — appear verbatim where the law requires them, with a plain-language
summary above, never instead of ([`01 §1.10`](../01-cross-surface-conventions.md)).
