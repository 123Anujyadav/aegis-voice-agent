# CallScreen Product UX Architecture

The complete experience architecture for every surface of the product.

> **This is not a screen gallery.** It is the specification of what the product
> *is* from the user's side: what exists, how it connects, what happens at every
> moment, and what must never happen. Screens are the last section, not the
> first, because a screen is a consequence of an information architecture and
> not a starting point.

---

## What this documents, and what it does not

| This document set defines | It does not define |
|---|---|
| Information architecture, per surface | Visual design — that is [`docs/design/`](../design/README.md) |
| Navigation graphs and every route | Compose code, layouts, or modules |
| Every screen's contract — 13 attributes | Pixel positions, exact spacing values |
| Every flow, every state, every recovery | API shapes — those are `contracts/proto` |
| Interaction, motion, haptic and sound behaviour | Animation implementation |
| Analytics event taxonomy | Analytics implementation |

**No implementation follows from reading this alone.** The design system is the
vocabulary, this is the grammar, and Phase 4 writes the sentences.

---

## The three surfaces

The product is three separate applications that share one design language and
share nothing else. They have different users, different threat models,
different devices and different jobs.

| # | Surface | Users | Platform | Primacy |
|---|---|---|---|---|
| **1** | [Android Consumer App](android/01-information-architecture.md) | Subscribers | Kotlin · Compose · minSdk 26 | **Primary.** The product |
| **2** | [Internal Operations Console](console/01-ia-navigation-inventory.md) | Support, fraud analysts, SRE, AI engineers | Web, desktop-first | Internal. Never ships to users |
| **3** | [Business Portal](business/01-ia-navigation-inventory.md) | Business-tier administrators | Web, desktop-first, responsive | External. Org-scoped |

### The separation rule

**Navigation never crosses a surface boundary.** There is no link from the
Android app into the console, no console view embedded in the portal, no shared
navigation shell. Each surface has its own information architecture, its own
navigation graph, its own session, and its own authentication.

This is not tidiness. The console can see subscriber personal data under
break-glass; the portal is operated by customers; the Android app is installed
on an untrusted device. Merging their navigation would merge their trust
boundaries, and a shared shell is exactly how an internal tool ends up one
misconfigured route away from a customer.

What they *do* share: the token set in `design/tokens/`, the component
catalogue in [`09-components.md`](../design/09-components.md), the state
taxonomy, the accessibility baseline, and the interaction language in
[`02-interaction-language.md`](02-interaction-language.md).

---

## Reading order

### Shared spine — read first, applies to all three surfaces

| # | Document | Answers |
|---|---|---|
| 0 | [Principles](00-principles.md) | What the experience must feel like, and the ten UX invariants |
| 1 | [Cross-surface conventions](01-cross-surface-conventions.md) | Loading, error, empty, offline, recovery, permissions, notifications, analytics, security posture |
| 2 | [Interaction language](02-interaction-language.md) | Voice orb, transcript, timeline, caller card, thinking, streaming, animation storyboard, haptics, sound |

### Surface 1 — Android Consumer App

| # | Document |
|---|---|
| 1 | [Information architecture](android/01-information-architecture.md) |
| 2 | [Navigation graph](android/02-navigation-graph.md) |
| 3 | [Screen inventory](android/03-screen-inventory.md) |
| 4 | [Screens — screening, calls, emergency](android/04-screens-screening-and-calls.md) |
| 5 | [Screens — home, history, search, protection](android/05-screens-home-history-search.md) |
| 6 | [Screens — onboarding, auth, permissions](android/06-screens-onboarding-auth-permissions.md) |
| 7 | [Screens — assistant, settings, premium, business](android/07-screens-settings-premium-business.md) |
| 8 | [User flows](android/08-user-flows.md) |
| 9 | [Journey map](android/09-journey-map.md) |

### Surface 2 — Internal Operations Console

| # | Document |
|---|---|
| 1 | [IA, navigation and screen inventory](console/01-ia-navigation-inventory.md) |
| 2 | [Screens](console/02-screens.md) |
| 3 | [Flows and journeys](console/03-flows-and-journeys.md) |

### Surface 3 — Business Portal

| # | Document |
|---|---|
| 1 | [IA, navigation and screen inventory](business/01-ia-navigation-inventory.md) |
| 2 | [Screens](business/02-screens.md) |
| 3 | [Flows and journeys](business/03-flows-and-journeys.md) |

---

## The screen contract

Every screen in every surface is specified against the same thirteen
attributes. A screen missing any of them is not specified.

| Attribute | Means |
|---|---|
| **Purpose** | The one job. If it needs two sentences, the screen does two things |
| **Inputs** | What arrives — route arguments, state, permissions, server data |
| **Outputs** | What leaves — navigation, mutations, events |
| **Components** | From [`09-components.md`](../design/09-components.md) only. A new component is a design-system change, not a screen decision |
| **Animation** | Enter, exit, and in-screen motion, in tokens from [`05-motion.md`](../design/05-motion.md) |
| **Edge cases** | The conditions that break naive implementations |
| **Accessibility** | Reading order, announcements, focus, touch targets, alternatives |
| **Loading** | Which tier from [`07 §7.3`](../design/07-states.md), and the skeleton shape |
| **Empty** | Which tier from [`07 §7.1`](../design/07-states.md), and the copy |
| **Error** | Which tier from [`07 §7.2`](../design/07-states.md), the copy, and the recovery |
| **Success** | What confirmation looks like, and whether one is warranted at all |
| **Security** | What data is exposed, what is redacted, what is logged, what the screen must refuse |
| **Analytics** | Events emitted, named per [`01 §1.9`](01-cross-surface-conventions.md) |

---

## Where the architecture constrains the experience

These are not UX preferences. They are consequences of frozen decisions, and
the experience is designed around them rather than against them.

| Architectural fact | Source | Experience consequence |
|---|---|---|
| The handset rings for **5 seconds** before forwarding fires | ADR-0002 §7 | Screening always begins *after* a missed call. There is no "incoming — screen it?" prompt, because by the time we exist the user already did not answer |
| Third-party apps **cannot access call audio** | ADR-0002 §2 | The subscriber is a spectator to the screening, not a participant. Every live surface is a monitor with a barge-in control |
| Forwarding is a **carrier-side setting that lapses** | ADR-0002 §7 | Forwarding health is a first-class object with its own screen, its own indicator and its own recovery flow — not a settings row |
| The failure mode of the entire backend is **"the phone rings normally"** | ADR-0002 §6 | Every error state answers "does my phone still work" before anything else |
| **Audio is off by default**; the transcript is the record | ADR-0012 | The transcript is the primary surface, not a fallback. Audio playback is a conditional enhancement |
| The **announcement is deterministic and model-free** | Invariant I1 | It is never editable, never skippable, and never presented as a customisable greeting |
| **Caller speech is untrusted input** | Invariant I4 | Caller-supplied text is rendered as quoted, attributed content and never as interface chrome. No caller string ever becomes a button label |
| **p50 900 ms / p95 1500 ms** response latency | ADR-0011 | Live transcript is streamed and the thinking state is real. Nothing pre-renders an answer |
| **MSISDN identity, hardware-bound credential** | ADR-0010 | Account recovery is a device-trust flow, not a password flow. There is no password anywhere in this product |
| **India-resident data, four-condition consent gate** | ADR-0012, Invariant I2 | Consent is granular and per-purpose. No screen bundles consents |

---

## Status

| Aspect | State |
|---|---|
| Principles and invariants | **Defined** |
| Cross-surface conventions | **Defined** |
| Interaction language | **Defined** |
| Android — IA, navigation, screens, flows | **Defined** |
| Console — IA, navigation, screens, flows | **Defined** |
| Business Portal — IA, navigation, screens, flows | **Defined** |
| Visual design of any screen | **Not started.** Phase 4 |
| Implementation | **Not started.** Phase 4 |

**No screen is built from this document.** It says what must exist and how it
must behave. What it looks like is a Phase 4 decision constrained by
[`docs/design/`](../design/README.md).
