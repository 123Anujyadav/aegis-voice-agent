# 1 · Foundations

Design philosophy, visual language, and brand principles.

---

## 1.1 What this product is, emotionally

A stranger calls. You do not pick up. Something answers on your behalf, talks to
them, and tells you what happened.

That is a genuinely unusual thing to hand to software, and the interface has one
job before it has any other: **make that feel like a good decision.**

The emotional target is **a competent assistant, not a security system.**

| We are | We are not |
|---|---|
| Calm, prepared, present | Alarmed, urgent, vigilant-looking |
| Precise about what we know | Confident about what we guessed |
| Quietly capable | Impressively technical |
| On the user's side | Between the user and the world |

Every spam-blocker on the Play Store looks like a threat dashboard: red, orange,
hazard stripes, shield iconography, big counters of "blocked threats". That
aesthetic sells fear, and fear does not sustain a subscription. It also makes the
product exhausting to open.

**We look like the assistant, not the firewall.**

---

## 1.2 Visual language

### Restraint as the primary signal

The design language is built on a simple inversion: in most products, importance
is signalled by adding — more colour, more weight, more chrome. Here, importance
is signalled by **removing everything around it**.

A fraud verdict on a call card is not a red banner. It is the only saturated
element in an otherwise neutral composition. That reads as *serious*; a red
banner reads as *anxious*.

Practically:

- **Colour is scarce.** Most of any screen is neutral. Saturated colour appears
  only where it carries meaning, and never decoratively.
- **Chrome is minimal.** Borders before shadows, shadows before fills, fills
  before gradients. Gradients exist in exactly one place (§1.4).
- **Whitespace is structural.** Grouping is done with space, not with boxes.
  Cards are used when content is genuinely a separable object, not to create
  visual interest.

### Surfaces, not panels

The interface is composed of **surfaces at different depths**, not of panels with
borders. Depth is expressed tonally in dark mode and with a single soft shadow in
light mode (see [04](04-space-shape-elevation.md)).

There is no skeuomorphism, no glassmorphism as a default, no neumorphism. Blur
appears only where the platform supports it cheaply and only where it
communicates *layering* — never as decoration.

### Density

**Comfortable, not compact.** This is a consumer product used in glance moments,
frequently one-handed, sometimes in sunlight. Enterprise density (Linear, Stripe
Dashboard) is correct for a desktop tool operated for eight hours; it is wrong
for a phone screen read for two seconds.

The compromise: **generous vertical rhythm, disciplined horizontal margins.**
Lists breathe; the screen edge does not waste width.

### Geometry

Rounded, but not soft. `radius.md` (12dp) is the default for interactive
surfaces, `radius.lg` (16dp) for containers. Fully-rounded (`radius.full`) is
reserved for pills, avatars and the voice orb — shapes that are conceptually
circular, never as a style choice on rectangles.

Sharp corners (`radius.none`) exist and are used deliberately: full-bleed media,
table cells, and dividers.

---

## 1.3 Brand principles

Five statements. Each is testable — you can look at a screen and say whether it
holds.

### Principle 1 — Show the work

The user trusts the AI because they can check it. Transcripts are first-class,
not buried. Confidence is displayed, not hidden. A verdict always has an
accessible "why".

> **Test:** can the user get from any AI judgement to the evidence behind it in
> one tap?

### Principle 2 — Never impersonate

The agent is a machine and says so. The interface never uses a human avatar for
the AI, never a human name, never a photorealistic face. The voice orb is
deliberately abstract and non-anthropomorphic.

This is a brand principle *and* a compliance one — the caller announcement
(ADR-0012 §5.1) makes it a legal position, and the interface must not undermine
what the announcement says.

> **Test:** could any element be mistaken for a human being?

### Principle 3 — Quiet by default, loud when it matters

The default state of every surface is neutral. Escalation is earned. If
everything is highlighted, the genuinely dangerous call does not stand out —
which is the exact failure mode of every threat-dashboard competitor.

> **Test:** on a screen of ten ordinary calls, how many coloured elements are
> there? The answer should be zero or one.

### Principle 4 — Respect the moment

The user is often mid-something. Notifications, live-screening surfaces and
takeover controls are designed for interruption: large targets, one-handed
reachability, no confirmation dialogs on time-critical actions.

> **Test:** can the primary action be reached with a thumb, without looking, in
> under two seconds?

### Principle 5 — Premium is felt in the details, not the decoration

Premium is: correct optical alignment, tabular numerals that do not jitter,
motion that settles rather than stops, a shadow that matches the light source,
type that respects Devanagari. It is not: gradients, glass, glow, or gold.

> **Test:** remove every decorative element. Does it still feel expensive?

---

## 1.4 The two deliberate exceptions

A system with no exceptions is a system nobody follows. These two are named,
bounded, and everything else is prohibited.

**Gradient — Premium tier only.** A single, subtle gold-bronze gradient is
permitted on the premium tier badge and nowhere else in the product. It exists
because a flat gold reads as "warning amber" and the distinction has to be
instant. Documented in [02-color.md §2.6](02-color.md).

**Blur — layering only, API 31+.** Backdrop blur is permitted on modal scrims and
the live-screening sheet, purely to communicate that something is *above*
something else. `minSdk` is 26, so it is a progressive enhancement with a solid
fallback and is never load-bearing. Documented in
[04-space-shape-elevation.md §4.6](04-space-shape-elevation.md).

---

## 1.5 What this system explicitly rejects

Named so that "why don't we…" has a written answer.

| Rejected | Why |
|---|---|
| **Threat-dashboard aesthetic** — red banners, shields, hazard stripes, blocked-threat counters | Sells fear; exhausting to open; makes real danger invisible by making everything loud |
| **Purple-gradient "AI" styling** | The current default AI aesthetic. Instantly dated, and it signals *toy* in a product that must signal *trustworthy* |
| **Anthropomorphic AI** — faces, avatars, human names | Violates Principle 2 and undermines the caller announcement |
| **Glassmorphism as a default surface** | Expensive on mid-range GPUs; illegible in sunlight; unavailable below API 31 |
| **Dark mode as the only mode** | India-first means bright outdoor use is common. Light mode is the primary, not an afterthought |
| **Compact enterprise density** | Correct for a desktop tool, wrong for a two-second glance on a phone |
| **Decorative motion** | Every animation must communicate state or continuity. Delight that costs a frame is not delight |
| **Illustration-heavy empty states** | Charming once, obstructive on the fiftieth viewing. See [07-states.md](07-states.md) |

---

## 1.6 Voice and tone

Interface copy is part of the design system, because the wrong sentence undoes
the right layout.

| Attribute | Do | Don't |
|---|---|---|
| **Plain** | "Unknown caller — screening" | "Initiating AI-powered call analysis" |
| **Specific** | "Hung up after 4 seconds" | "Call ended" |
| **Honest about uncertainty** | "Likely a delivery service" | "This is a delivery service" |
| **Never alarming** | "Flagged as possible fraud" | "⚠️ DANGER: SCAM DETECTED" |
| **Never cute** | "No calls yet" | "It's quiet in here! 🦗" |

**Numbers are always specific.** "3 calls screened today", never "several calls".
Vagueness reads as evasion in a trust product.

**The AI speaks in the third person about itself**, never first: "The assistant
asked who was calling", not "I asked who was calling". First person implies a
persona; Principle 2 forbids one.
