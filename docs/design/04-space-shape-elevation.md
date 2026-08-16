# 4 · Space, Shape & Elevation

Spacing, corner radius, elevation, shadow, glass, blur and stroke.

---

## 4.1 Spacing scale

Base unit **4dp**. Every gap, pad and margin in the product is one of these ten
values.

| Token | Value | Use |
|---|---:|---|
| `space.none` | 0 | Explicit zero |
| `space.hairline` | 2dp | Icon-to-label in dense chips only |
| `space.xs` | **4dp** | Tight internal — icon ↔ text in a badge |
| `space.sm` | **8dp** | Related elements — label ↔ input |
| `space.md` | **12dp** | Internal card padding, dense |
| `space.lg` | **16dp** | **Default.** Card padding, screen margin |
| `space.xl` | **24dp** | Between groups |
| `space.2xl` | **32dp** | Between sections |
| `space.3xl` | **48dp** | Major separation |
| `space.4xl` | **64dp** | Screen-level, empty states |

### The 4/8 discipline

Values below `space.lg` step by 4; values above step by 8 or 16. Small spacing
needs fine control; large spacing does not, and offering `40dp` alongside `48dp`
invites decisions nobody should be making.

**`space.hairline` (2dp) is the only sub-4 value and it exists reluctantly** —
purely for optical icon-to-label alignment inside badges, where 4dp reads as
detached. Using it anywhere else is a defect.

### Grouping through space

Related things are **closer**; unrelated things are **further apart**. Proximity
does the grouping so borders do not have to
([01 §1.2](01-foundations.md#restraint-as-the-primary-signal)).

```
Caller name          ← title.md
                       space.xs   (4dp — tightly bound)
+91 98••• •••10      ← body.sm
                       space.lg   (16dp — separate idea)
"Asked about a delivery"
                       space.xl   (24dp — separate group)
[ Block ]  [ Allow ]
```

A card that needs a divider between two blocks of content usually needs more
space instead.

### Layout

| Context | Value |
|---|---|
| Screen horizontal margin | `space.lg` (16dp); `space.md` below 360dp |
| Card internal padding | `space.lg` |
| Card internal padding, dense list | `space.md` |
| List item vertical padding | `space.md` |
| Gap between cards | `space.md` |
| Gap between sections | `space.2xl` |
| Bottom sheet top padding | `space.xl` |
| Minimum touch target | **48 × 48dp** — non-negotiable |

---

## 4.2 Corner radius

| Token | Value | Use |
|---|---:|---|
| `radius.none` | 0 | Full-bleed media, table cells, dividers |
| `radius.xs` | 4dp | Tiny chips, tags |
| `radius.sm` | 8dp | Badges, inputs, small buttons |
| `radius.md` | **12dp** | **Default.** Buttons, cards, list rows |
| `radius.lg` | 16dp | Containers, large cards |
| `radius.xl` | 20dp | Bottom sheets, dialogs |
| `radius.2xl` | 28dp | Hero surfaces |
| `radius.full` | 9999dp | Pills, avatars, voice orb, FAB |

### Nested radius

An inner radius inside an outer one must be **outer minus the padding**, or the
corners look wrong — the classic mistake is nesting equal radii, which makes the
inner element appear to bulge.

```
outer radius.lg (16) − space.sm padding (8) = inner radius.sm (8) ✓
outer radius.lg (16) with inner radius.lg (16)                    ✗
```

### `radius.full` is semantic, not decorative

Reserved for shapes that are *conceptually* circular: avatars, the voice orb,
status dots, pills whose content is a single short label. **Applying it to a
rectangle to look modern is a defect** — it makes the shape language
meaningless and removes the one signal that differentiates the premium chip
([02 §2.3 R3](02-color.md)).

---

## 4.3 Elevation

Six levels. Elevation communicates **layering**, never importance.

| Token | dp | Light: shadow | Dark: surface | Use |
|---|---:|---|---|---|
| `elevation.0` | 0 | none | `neutral.950` | Background |
| `elevation.1` | 1 | `shadow.xs` | `neutral.900` | Cards at rest |
| `elevation.2` | 3 | `shadow.sm` | `neutral.800` | Raised cards, pressed |
| `elevation.3` | 6 | `shadow.md` | `neutral.800` | Sticky headers, FAB |
| `elevation.4` | 8 | `shadow.lg` | `neutral.800` + tint | Bottom sheets, menus |
| `elevation.5` | 16 | `shadow.xl` | `neutral.800` + tint | Dialogs, modals |

### Light and dark express depth differently

**This is the single most-missed detail in dark-mode design.**

Shadows are nearly invisible against a dark background — a black shadow on a
near-black surface communicates nothing. Material 3's answer is correct and we
follow it:

- **Light mode:** depth = **shadow**. Surface colour stays constant.
- **Dark mode:** depth = **tonal**. Surfaces get *lighter* as they rise. Shadows
  are suppressed entirely above `elevation.1`.

A component must therefore never hard-code a shadow. It requests an elevation
token and the theme resolves the mechanism.

---

## 4.4 Shadow

| Token | Y | Blur | Spread | Opacity (light) |
|---|---:|---:|---:|---:|
| `shadow.xs` | 1dp | 2dp | 0 | 4% |
| `shadow.sm` | 2dp | 4dp | 0 | 6% |
| `shadow.md` | 4dp | 8dp | −1dp | 8% |
| `shadow.lg` | 8dp | 16dp | −2dp | 10% |
| `shadow.xl` | 16dp | 32dp | −4dp | 12% |

**Shadow colour is not black.** It is `neutral.900` at the stated opacity — a
cool-tinted shadow reads as light falling on a surface; pure black reads as a
hole. Correct, and effectively free.

**Y-offset always exceeds zero.** Light comes from above. A symmetric shadow
(`y: 0`) is a glow, and glows belong to a different design language.

**Negative spread on larger shadows** keeps the shadow tighter than the blur,
preventing the muddy halo that large soft shadows otherwise produce.

**Shadows are never animated.** Shadow rendering is expensive and animating it
drops frames on a mid-range device ([05](05-motion.md)). Elevation changes on
press use a **tonal or scale** change instead.

---

## 4.5 Stroke

| Token | Width | Use |
|---|---:|---|
| `stroke.hairline` | 0.5dp | Dividers on high-density displays |
| `stroke.thin` | 1dp | **Default.** Borders, dividers, input outlines |
| `stroke.medium` | 1.5dp | Selected states, emphasis |
| `stroke.thick` | 2dp | Focus rings, high-contrast borders |
| `stroke.heavy` | 3dp | High-contrast focus rings |

**Borders before shadows.** A 1dp border is cheaper to render, works in both
themes without adaptation, and reads as more precise. Reach for `border.subtle`
before reaching for `elevation.1`.

`stroke.hairline` renders as a true hairline only at ≥ 2× density; below that it
snaps to 1dp. Never rely on it for a load-bearing boundary.

---

## 4.6 Glass and blur

**Blur is a progressive enhancement. It is never load-bearing.**

`RenderEffect.createBlurEffect` requires **API 31**. `minSdk` is **26**. Between
a quarter and a third of the Indian Android install base is below 31, and blur on
a mid-range GPU is expensive even where it is available.

### Where blur is permitted

Exactly two places ([01 §1.4](01-foundations.md)):

| Surface | Blur | Fallback (API < 31, or reduced-motion) |
|---|---|---|
| Modal scrim | 20dp, `neutral.900` @ 40% | `neutral.900` @ 60%, no blur |
| Live-screening sheet backdrop | 16dp, surface @ 70% | Surface @ 94%, no blur |

### Rules

1. **Never place text on a blurred backdrop** without an opaque plate behind it —
   contrast against a blur is unpredictable and cannot be verified.
2. **Never animate blur radius.** Fade the whole layer instead.
3. **The fallback must be tested first.** If the design only works with blur, it
   is not shippable.
4. **Glassmorphism as a surface style is rejected**
   ([01 §1.5](01-foundations.md)) — illegible in sunlight, expensive, and
   unavailable to a third of our users.

---

## 4.7 Grid and alignment

- **4dp baseline grid.** Every vertical dimension is a multiple of 4.
- **Text is optically aligned, not box-aligned.** Line-box padding differs from
  the visible glyph edge; components trim to the glyph.
- **Icons are optically centred**, which is rarely geometrically centred — a
  triangle "play" glyph needs a nudge right ([06](06-icons-illustration.md)).
- **Content max-width 600dp**, centred, on wide screens ([03 §3.6](03-typography.md)).

---

## 4.8 Rules

1. **Use tokens.** A raw `dp` in a composable is a defect.
2. **Never fix the height of a text container** — it breaks at 200% text scale.
3. **48dp minimum touch target.** Visual size may be smaller; the target may not.
4. **Nested radius = outer − padding.**
5. **`radius.full` only on conceptually round shapes.**
6. **Request elevation, never a shadow.** The theme decides the mechanism.
7. **Never animate shadow or blur.**
8. **The no-blur fallback is the design.** Blur is decoration on top of it.
