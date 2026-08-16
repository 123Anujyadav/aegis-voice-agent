# CallScreen Design System

The visual and interaction language every screen in this product will be built
from.

> **This is not a style guide.** It is a set of enforced constraints with a
> single machine-readable source of truth (`design/tokens/`) that compiles to
> Compose, CSS and Figma. A screen that uses a raw hex value instead of a token
> is a defect, and CI treats it as one.

---

## Reading order

| # | Document | Answers |
|---|---|---|
| 1 | [Foundations](01-foundations.md) | What does this product feel like, and why? |
| 2 | [Color](02-color.md) | Palettes, semantic roles, light / dark / high contrast |
| 3 | [Typography](03-typography.md) | Type scale, Indic script handling, accessibility |
| 4 | [Space, Shape & Elevation](04-space-shape-elevation.md) | Spacing, radius, shadow, glass, stroke |
| 5 | [Motion](05-motion.md) | Duration, easing, voice animation, haptics |
| 6 | [Icons & Illustration](06-icons-illustration.md) | Icon grid, illustration style |
| 7 | [States](07-states.md) | Empty, error, loading, skeleton, interaction states |
| 8 | [Accessibility](08-accessibility.md) | WCAG, colour-blindness, screen reader, voice |
| 9 | [Components](09-components.md) | The catalogue and every component's contract |
| 10 | [Tokens & Naming](10-tokens-and-naming.md) | Token architecture, naming, build pipeline |
| 11 | [Contributing](11-contributing.md) | How to add or change anything here |

---

## The four constraints that shaped every decision

Most design systems are written for a US-first, flagship-device, English-only
product. Ours is not, and pretending otherwise would produce a system that looks
excellent in a Figma file and fails on the actual handset in the actual hand.

**1 · India-first, mid-range Android.** 60 Hz displays, modest GPUs, `minSdk 26`.
Motion must be cheap, blur is a progressive enhancement and never load-bearing,
and the APK-size gate (2% delta, Phase 1 §17) rules out bundling five Indic font
families.

**2 · Indic scripts set the metrics, not Latin.** Devanagari matras and Bengali
conjuncts need more vertical room than Latin and break under negative tracking.
Every line-height and letter-spacing token in this system is derived from
Devanagari first and checked against Latin second — not the other way round.

**3 · This is a glance product.** The subscriber is notified mid-screening and
has roughly two seconds, one-handed, often in sunlight. Hierarchy must survive
that. Anything that needs a second read has failed.

**4 · Accessibility is the product, not compliance.** A call screener that
transcribes calls is the difference between "can't use the phone" and "can" for
deaf and hard-of-hearing users. They are not an edge case to accommodate; they
are among the highest-value users of this specific product.

---

## Design principles

Ordered. When two conflict, the earlier one wins.

### 1 · Trust is built with restraint

The product answers your phone on your behalf. That is a significant delegation,
and the interface has to earn it. Every competitor in this category shouts —
red banners, hazard stripes, alarm iconography — and shouting reads as
insecurity.

We express confidence by *not* raising our voice. High-severity states are
communicated through hierarchy, weight and placement before colour. The loudest
thing on any screen should be the thing the user came for, not the thing we most
want them to notice.

### 2 · Never overclaim

The AI is confident, the AI is wrong sometimes, and the interface must be honest
about which is which. A fraud verdict is a *judgement*, displayed with its
confidence. A transcript is *what we heard*, not what was said. Recording state
is shown when and only when recording is happening.

Concretely: no state may imply the agent is listening when it is not, and no
certainty may be implied that the model did not express.

### 3 · Legible in two seconds

Every surface answers *who, what, and what do I do* in that order, at a glance,
one-handed. If a screen needs a legend, the screen is wrong.

### 4 · Fast beats beautiful

A 60 Hz mid-range device is the target, not the fallback. Animation that drops a
frame is worse than no animation. Transform and opacity only; blur, shadow
animation and large-surface crossfades are prohibited on the hot path.

### 5 · One source of truth

Tokens in `design/tokens/*.json` compile to every platform. A value that exists
only in Compose, or only in Figma, is drift — and drift is how design systems die.

---

## Structure

```
design/tokens/            JSON — the single source of truth
  primitive.json          raw palette, raw scale
  semantic.json           roles: fraud, spam, ai, voice, recording…
  component.json          per-component bindings
  theme.light.json        light-mode role → primitive mapping
  theme.dark.json         dark-mode mapping
  theme.contrast.json     high-contrast mapping

android/core/designsystem/   Compose implementation
  token/                     generated + hand-written token objects
  theme/                     CallScreenTheme, colour schemes, providers
  component/                 the component library
  foundation/                primitives components are built from
```

---

## Status

| Aspect | State |
|---|---|
| Foundations, tokens, motion, accessibility | **Defined** |
| Component contracts | **Defined** — see [09-components.md](09-components.md) |
| Compose implementation | **Phase 3** — tokens and foundation components |
| Screens | **Not started.** Phase 4. |

**No application screens exist and none should be built from this document.**
The design system is the vocabulary; screens are sentences, and they come later.
