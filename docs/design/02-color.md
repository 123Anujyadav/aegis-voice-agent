# 2 · Color

Palettes, semantic roles, and the three themes.

---

## 2.1 How colour works here

Three tiers. Application code touches **only tier 2 and 3** — a component that
references `neutral.700` directly is a defect, because it cannot respond to
theme.

```
Tier 1 · PRIMITIVE     brand.700 = #0B3D91          raw, themeless, never used directly
   ↓
Tier 2 · SEMANTIC      color.fraud.text             role-based, theme-aware
   ↓
Tier 3 · COMPONENT     badge.fraud.background       component-specific binding
```

### The rule that makes contrast work

Every ramp is tuned so that a single rule holds across all ten semantic hues:

> **On light surfaces, text and icons use the `600` step. Fills use `500` with
> white content on top. On dark surfaces, text uses `300`.**

This is not a convention — it is derived from measured contrast. The `500` step
of several hues (warning, voice, premium) **fails** WCAG AA as text on white
while passing comfortably as a fill. Following the rule means you cannot
accidentally ship a failing combination.

---

## 2.2 Primitive ramps

### Neutral — slightly cool

A blue-cast neutral reads as technical and calm; a warm neutral reads as
friendly and consumer. For a trust product the cool cast is correct.

| Step | Hex | Use |
|---|---|---|
| `neutral.0` | `#FFFFFF` | Light surface base |
| `neutral.50` | `#F7F8FA` | Light app background |
| `neutral.100` | `#EEF0F4` | Subtle fill, hover |
| `neutral.200` | `#E1E4EA` | Divider, border |
| `neutral.300` | `#C9CED8` | Disabled border |
| `neutral.400` | `#A0A8B8` | Placeholder, disabled text |
| `neutral.500` | `#7A8395` | Tertiary text |
| `neutral.600` | `#5A6377` | Secondary text |
| `neutral.700` | `#414A5C` | Body text (light) |
| `neutral.800` | `#2A3242` | Dark surface raised |
| `neutral.900` | `#1A2030` | Dark surface base |
| `neutral.950` | `#10141F` | Dark app background |
| `neutral.1000` | `#000000` | High-contrast dark base |

### Brand / Telephony — deep navy

Anchored on `#0B3D91`, the launcher navy already committed in Phase 2. Navy is
the telephony heritage colour and reads institutional without reading corporate.

| Step | Hex | | Step | Hex |
|---|---|---|---|---|
| `brand.50` | `#EBF1FC` | | `brand.500` | `#1E56C4` |
| `brand.100` | `#D2E1F9` | | `brand.600` | `#1444A6` |
| `brand.200` | `#A6C2F2` | | `brand.700` | `#0B3D91` |
| `brand.300` | `#6E99E6` | | `brand.800` | `#082E6E` |
| `brand.400` | `#3D72D6` | | `brand.900` | `#06214F` |

### Semantic hue ramps

Abbreviated to the steps in active use. Full ramps in
`design/tokens/primitive.json`.

| Hue | `300` (dark text) | `500` (fill) | `600` (light text) | Meaning |
|---|---|---|---|---|
| **success** | `#5BBA8A` | `#128552` | `#0D6C43` | Completed, safe, allowed |
| **warning** | `#E8AC44` | `#B8730A` | `#965C07` | System attention — not caller state |
| **spam** | `#E88F68` | `#A8481F` | `#8B3A19` | Unwanted, not malicious |
| **fraud** | `#E2748A` | `#B22947` | `#931F3B` | Active malicious intent |
| **emergency** | `#EC7A55` | `#C43F14` | `#A23310` | Urgent, must break through |
| **ai** | `#9C8DE3` | `#6350C4` | `#513FA6` | The agent itself |
| **voice** | `#4FC3D2` | `#0891A5` | `#077588` | Live speech, listening |
| **telephony** | `#6E99E6` | `#1E56C4` | `#1444A6` | Call, carrier, connection |
| **recording** | `#F5333C` | `#E01B24` | `#BC1219` | Recording in progress |
| **premium** | `#D2AC49` | `#9A7419` | `#7D5D14` | Subscription tier |

---

## 2.3 The warm-hue collision — and how it is resolved

**This is the most dangerous part of the palette and it must be understood.**

`warning` (amber), `spam` (orange), `premium` (gold) and `emergency`
(vermillion) occupy adjacent regions of hue space. At a two-second glance, on a
sunlit mid-range display, a user cannot reliably distinguish amber from gold.

Four rules, all mandatory:

**R1 · They never co-occur in the same context.**
`premium` appears only on the tier badge. `warning` appears only on system
status. `spam` appears only as a caller classification. `emergency` appears only
on a live call surface. A screen showing two of them simultaneously is a design
review failure.

**R2 · Colour is never the sole carrier.**
WCAG 1.4.1. Every one of these states carries a distinct icon *and* a text
label. Remove all colour and the meaning must survive — this is the
colour-blindness test in [08-accessibility.md](08-accessibility.md), and it is
run on every component.

**R3 · Shape differentiates.**
`premium` is the only chip with a gradient fill and the only one using
`radius.full`. `warning`, `spam` and `emergency` use `radius.sm` rectangles.
Shape is legible before hue at a glance.

**R4 · `premium` is decorative; the others are status.**
`premium` never conveys a state that requires action. If gold is on screen it is
a label, never an alert. This is the semantic distinction users learn in one
exposure.

> **Reviewer's question:** if you desaturate this screen to greyscale, can you
> still tell the difference? If not, it is not shippable.

---

## 2.4 Semantic roles

Tier 2. This is what components reference.

### Surface and content

| Role | Light | Dark | Purpose |
|---|---|---|---|
| `surface.background` | `neutral.50` | `neutral.950` | App canvas |
| `surface.default` | `neutral.0` | `neutral.900` | Cards, sheets |
| `surface.raised` | `neutral.0` | `neutral.800` | Elevated over default |
| `surface.sunken` | `neutral.100` | `neutral.1000` | Wells, inputs |
| `surface.inverse` | `neutral.900` | `neutral.100` | Tooltips, snackbars |
| `content.primary` | `neutral.900` | `neutral.50` | Body text |
| `content.secondary` | `neutral.700` | `neutral.300` | Supporting text |
| `content.tertiary` | `neutral.600` | `neutral.400` | Timestamps, metadata |
| `content.disabled` | `neutral.500` | `neutral.500` | Disabled |

> **These sit darker (light) and lighter (dark) than the ramp position suggests.**
> `neutral.500` on white measures **3.81 : 1** — a WCAG AA failure as body text.
> The values above are the ones that pass, and `ContrastTest` enforces them.
| `content.inverse` | `neutral.0` | `neutral.900` | On inverse surfaces |
| `border.subtle` | `neutral.200` | `neutral.800` | Dividers |
| `border.default` | `neutral.300` | `neutral.700` | Input borders |
| `border.strong` | `neutral.400` | `neutral.600` | Emphasis |
| `border.focus` | `brand.500` | `brand.300` | Focus ring |

### Interactive

| Role | Light | Dark |
|---|---|---|
| `action.primary.fill` | `brand.500` | `brand.300` |
| `action.primary.content` | `neutral.0` | `neutral.950` |
| `action.primary.hover` | `brand.600` | `brand.200` |
| `action.primary.pressed` | `brand.700` | `brand.100` |
| `action.secondary.fill` | `neutral.100` | `neutral.800` |
| `action.secondary.content` | `neutral.900` | `neutral.100` |
| `action.danger.fill` | `fraud.500` | `fraud.300` |
| `action.danger.content` | `neutral.0` | `neutral.950` |
| `action.disabled.fill` | `neutral.100` | `neutral.800` |
| `action.disabled.content` | `neutral.500` | `neutral.400` |

> **Dark action fills are two steps lighter than the light-theme mirror.** In
> dark theme the fill carries *dark* content, so it must be light enough to
> support it: `brand.400` against `neutral.950` measures **3.98 : 1** and fails.
> `brand.300` measures 6.5 : 1.

### Status — the ten semantic states

Each has four sub-roles. `subtle` is a tinted background; `text` and `icon`
follow the 600/300 rule from §2.1.

| Role | `.subtle` (L / D) | `.fill` (L / D) | `.text` (L / D) |
|---|---|---|---|
| `status.success` | `success.50` / `success.900` | `success.500` / `success.400` | `success.600` / `success.300` |
| `status.warning` | `warning.50` / `warning.900` | `warning.500` / `warning.400` | `warning.600` / `warning.300` |
| `status.spam` | `spam.50` / `spam.900` | `spam.500` / `spam.400` | `spam.600` / `spam.300` |
| `status.fraud` | `fraud.50` / `fraud.900` | `fraud.500` / `fraud.400` | `fraud.600` / `fraud.300` |
| `status.emergency` | `emergency.50` / `emergency.900` | `emergency.500` / `emergency.400` | `emergency.600` / `emergency.300` |
| `status.ai` | `ai.50` / `ai.900` | `ai.500` / `ai.400` | `ai.600` / `ai.300` |
| `status.voice` | `voice.50` / `voice.900` | `voice.500` / `voice.400` | `voice.600` / `voice.300` |
| `status.telephony` | `brand.50` / `brand.900` | `brand.500` / `brand.400` | `brand.600` / `brand.300` |
| `status.recording` | `recording.50` / `recording.900` | `recording.500` / `recording.400` | `recording.600` / `recording.300` |
| `status.premium` | `premium.50` / `premium.900` | `premium.500` / `premium.400` | `premium.600` / `premium.300` |

---

## 2.5 Measured contrast

Verified, not assumed. Ratios against the surface each role actually sits on.

All values below are **produced by `ContrastTest`**, not estimated. The test
fails the build if any drifts.

| Pair | Ratio | WCAG |
|---|---:|---|
| `content.primary` on `surface.default` (light) | **15.3 : 1** | AAA |
| `content.secondary` on `surface.default` (light) | **8.9 : 1** | AAA |
| `content.tertiary` on `surface.default` (light) | **6.0 : 1** | AA |
| `content.disabled` on `surface.default` (light) | **3.8 : 1** | see below |
| `brand.700` on white | **10.0 : 1** | AAA |
| White on `action.primary.fill` (`brand.500`) | **6.6 : 1** | AA |
| `status.fraud.text` on white | **8.1 : 1** | AAA |
| `status.spam.text` on white | **7.1 : 1** | AAA |
| `status.warning.text` on white | **5.5 : 1** | AA |
| `status.success.text` on white | **6.5 : 1** | AA |
| `status.ai.text` on white | **7.6 : 1** | AAA |
| `status.voice.text` on white | **5.4 : 1** | AA |
| `status.premium.text` on white | **6.1 : 1** | AA |
| `status.fraud.text` on dark surface | **5.5 : 1** | AA |

**Why the `500` step is a fill and not text:** `warning.500` on white measures
**3.8 : 1** — a failure. `voice.500` measures **3.8 : 1**. `premium.500`
measures **4.3 : 1**. All three pass comfortably as fills with white content and
fail as text. The 600/300 rule exists precisely to make this impossible to get
wrong.

### Disabled content: 3 : 1, and why not 4.5

WCAG **exempts** disabled controls from contrast entirely. We decline the
exemption — a control the user cannot read is one whose purpose they cannot
determine.

But 4.5 : 1 is unreachable while still *looking* disabled: at that ratio the
control reads as enabled, which is a worse failure than a slightly dim one. **3 : 1
is the honest floor** — stricter than the standard requires, legible, and still
visibly inactive. `ContrastTest` enforces it as `DISABLED_FLOOR`.

Contrast is **verified in CI**, not by eye. See
[08-accessibility.md](08-accessibility.md).

---

## 2.6 The premium gradient

The one permitted gradient in the entire product ([01 §1.4](01-foundations.md)).

```
linear-gradient(135°, premium.300 #D2AC49 → premium.500 #9A7419)
```

**Permitted:** the premium tier badge, and nowhere else.
**Why it exists:** flat gold at badge scale is indistinguishable from warning
amber. The gradient makes it read as *material* rather than *state*, which is
the semantic distinction in rule R4 above.
**Constraints:** two stops only, never animated, and it must still pass contrast
against its darkest stop.

Any other gradient proposal is rejected by default.

---

## 2.7 The three themes

### Light — primary

The default, and the one designed first. India-first means outdoor daylight use
is routine; light mode is not an afterthought.

### Dark

**Dark mode is not inverted light mode.** Two rules:

- **Elevation is tonal, not shadowed.** Shadows are near-invisible on dark
  surfaces. Depth is expressed by lighter surface steps
  (`neutral.950 → 900 → 800`). See
  [04-space-shape-elevation.md](04-space-shape-elevation.md).
- **Saturated hues are lightened, not just darkened backgrounds.** A `500` step
  that works on white vibrates against `neutral.950`. Dark mode uses `300`/`400`
  steps for content and `900` steps for tinted backgrounds.

Pure black (`#000000`) is **not** the dark background. `neutral.950` avoids
OLED smearing during scroll and reduces halation for astigmatic users.

### High contrast

Not a stylistic variant — an accessibility mode bound to the platform's
high-contrast setting.

| Change | Value |
|---|---|
| Text contrast | ≥ **7 : 1** everywhere (AAA) |
| Borders | `border.default` → `border.strong`, **1dp → 2dp** |
| Tinted `subtle` backgrounds | Removed; replaced by border + icon |
| Focus ring | 2dp → **3dp** |
| Backgrounds | Light: pure `neutral.0` · Dark: pure `neutral.1000` |
| Disabled content | Lightened to remain legible at 4.5:1 |

Tinted status backgrounds are *removed* rather than darkened, because a
low-contrast tint behind high-contrast text is the exact pattern high-contrast
mode exists to eliminate.

---

## 2.8 Dynamic colour

Android 12+ offers Material You wallpaper-derived palettes.

**Decision: dynamic colour is not applied to semantic or status roles. It is
permitted only on neutral surfaces, and only when the user opts in.**

Reasoning: `fraud` must be the same colour on every device. If a user's wallpaper
recolours the fraud badge to a friendly teal, the design system has actively
caused harm. Semantic colour in a trust product is a **safety property**, not
personalisation.

| Layer | Dynamic? |
|---|---|
| `surface.*`, `border.*` | ✅ Permitted, opt-in |
| `action.primary.*` | ⚠️ Permitted, opt-in — contrast re-verified at runtime |
| `status.*` (all ten) | ❌ **Never** |
| `premium.*` | ❌ Never — brand-owned |

---

## 2.9 Rules

1. **Never reference a primitive in a component.** Semantic roles only.
2. **Never introduce a hex value outside `design/tokens/`.** CI fails the build.
3. **The `600`/`300` rule is not optional** — it is what keeps contrast passing.
4. **Colour is never the only signal** (WCAG 1.4.1). Icon + label always.
5. **No new semantic hue without an ADR-style justification.** Ten is already at
   the limit of what a user can learn.
6. **Status colour appears at most once per screen region.** If two things are
   coloured, neither is important.
