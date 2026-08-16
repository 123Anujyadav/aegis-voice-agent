# 6 · Icons & Illustration

---

## 6.1 Icon system

### Specification

| Property | Value |
|---|---|
| **Grid** | 24 × 24dp |
| **Live area** | 20 × 20dp — 2dp keyline padding on all sides |
| **Stroke** | **2dp**, uniform |
| **Caps / joins** | Round |
| **Corner radius** | 2dp on rectangular forms |
| **Style** | Outlined by default, filled for selected |
| **Format** | `VectorDrawable`, single path where possible |
| **Colour** | `currentColor` — never baked in |

### Sizes

| Token | Size | Stroke | Use |
|---|---:|---:|---|
| `icon.xs` | 16dp | 1.5dp | Inline with `body.sm`, dense badges |
| `icon.sm` | 20dp | 1.5dp | Inline with `body.md`, list affordances |
| `icon.md` | **24dp** | **2dp** | **Default.** Buttons, nav, list leading |
| `icon.lg` | 32dp | 2dp | Empty states, feature callouts |
| `icon.xl` | 48dp | 2.5dp | Hero, onboarding |

Stroke weight is **redrawn per size, not scaled**. A 24dp icon scaled to 16dp has
a 1.33dp stroke that renders soft and inconsistent against text. Each size is a
distinct asset.

### Optical alignment

**Geometric centring is usually wrong.** Asymmetric glyphs need manual
correction:

| Glyph | Correction |
|---|---|
| Play triangle | +1dp horizontal — visual mass sits left |
| Chevron / arrow | +0.5dp in the direction of travel |
| Circular forms | Rendered 1dp larger than square forms to match perceived size |
| Text-adjacent icons | Aligned to the **cap height**, not the line box |

Optical alignment is the difference between an icon set that looks bought and one
that looks made.

### Source

**Material Symbols** (rounded, weight 400, grade 0, optical size 24) as the base
library, with custom glyphs for domain concepts it does not cover.

Material Symbols is chosen because it is comprehensive, actively maintained,
Apache-2.0, and — importantly — already familiar to Android users. An unfamiliar
icon language in a trust product costs comprehension for no benefit.

### Custom domain glyphs

These do not exist in any standard library and are drawn to the spec above:

| Glyph | Meaning | Notes |
|---|---|---|
| `ic_screening` | Call being screened | Phone + concentric arc. **Never a face.** |
| `ic_ai_agent` | The assistant | Abstract orb + arc. Non-anthropomorphic |
| `ic_fraud` | Fraud verdict | Shield with a break — not a skull, not an alarm |
| `ic_spam` | Unwanted caller | Crossed megaphone |
| `ic_transcript` | Call transcript | Lines with a quote mark |
| `ic_waveform` | Audio present | Five bars, varied height |
| `ic_forwarding` | Carrier forwarding active | Arrow through a phone |
| `ic_takeover` | Take the call | Hand + phone |
| `ic_premium` | Subscription tier | Faceted mark — not a crown, not a star |

**Rejected iconography**, and why:

| Rejected | Reason |
|---|---|
| Skull, biohazard, siren for fraud | Threat-dashboard aesthetic ([01 §1.5](01-foundations.md)) |
| Robot head, face, speech bubble with eyes for AI | Violates [Principle 2](01-foundations.md#principle-2--never-impersonate) |
| Crown, diamond for premium | Reads as a mobile game |
| Padlock for anything but literal encryption | Security theatre |

### Rules

1. **`currentColor` always.** An icon with a baked fill cannot theme.
2. **Never scale across size tokens.** Use the correct asset.
3. **Icons are never the sole carrier of meaning** — always paired with a label
   or an accessible name ([08](08-accessibility.md)).
4. **Decorative icons are hidden from screen readers**
   (`contentDescription = null`); meaningful icons are labelled.
5. **Filled = selected.** Outlined = unselected. No third style.

---

## 6.2 Illustration

### Style

**Geometric, restrained, two-tone.**

| Property | Value |
|---|---|
| **Construction** | Circles, rounded rectangles, arcs — the icon vocabulary at scale |
| **Palette** | `content.tertiary` + **one** semantic accent. Never more |
| **Stroke** | 2dp, matching icons |
| **Fill** | Flat, or a single 10% tint. **No gradients** |
| **Perspective** | Flat. No isometric, no 3D, no shadows |
| **People** | **None** — see below |
| **Format** | `VectorDrawable`, theme-aware |
| **Size** | 120 × 120dp typical; never more than 160dp |

### No people, no mascots

Two independent reasons:

**Principle 2.** Depicting the assistant as a character — even an abstract one —
undermines the caller announcement that says it is an AI. The interface must not
contradict the legal position ([ADR-0012](../adr/0012-privacy-dpdp-consent-retention.md)).

**Representation.** Any depicted person has a skin tone, a gender presentation, a
body. In a product serving all of India, every such choice excludes someone.
Geometric abstraction excludes nobody.

### Explicitly rejected

| Rejected | Reason |
|---|---|
| 3D / isometric renders | Heavy assets, dated within a year, at odds with the flat system |
| Purple-gradient AI abstractions | The current default AI aesthetic ([01 §1.5](01-foundations.md)) |
| Hand-drawn / sketchy | Reads as unfinished. Wrong for a trust product |
| Mascots | Principle 2 |
| Stock illustration | Immediately recognisable as bought |
| Photography | Cannot theme, heavy, representation problems |

### Where illustration appears

Deliberately narrow — see [07 §7.1](07-states.md) on why.

| Context | Allowed |
|---|---|
| First-run onboarding | ✅ One per screen, maximum three screens |
| First-time empty state | ✅ One, small |
| Recurring empty state | ❌ Icon only |
| Full-screen error | ✅ One, small |
| Inline error | ❌ Icon only |
| Success confirmation | ❌ Icon only |
| Decoration | ❌ Never |

---

## 6.3 Rules

1. **Icons and illustration share one vocabulary** — same stroke, same geometry,
   same corner treatment. They must look like one hand drew them.
2. **Two colours maximum** in any illustration.
3. **No gradients** outside the premium badge ([02 §2.6](02-color.md)).
4. **No people, no mascots, ever.**
5. **Everything is a vector and theme-aware.** A raster asset cannot respond to
   dark mode or high contrast.
6. **Optical alignment is checked at 1× on a real device**, not in the editor.
