# 10 · Tokens & Naming

The token architecture, naming grammar, and build pipeline.

---

## 10.1 Why tokens exist

A design system without a single source of truth is a style guide, and style
guides drift. Within two quarters the Figma file, the Compose code and the
documentation each describe a different product.

**`design/tokens/*.json` is the only place a value is defined.** Everything else
is generated from it or references it. A hex value, a `dp` or an `sp` literal
appearing anywhere in application code is a defect, and CI treats it as one.

---

## 10.2 Three tiers

```
PRIMITIVE          neutral.700 = #414A5C
  ↓ referenced by
SEMANTIC           content.primary = {neutral.900 | neutral.50}   ← theme-aware
  ↓ referenced by
COMPONENT          button.primary.label = {content.inverse}
```

| Tier | Names describe | Theme-aware | Used by |
|---|---|---|---|
| **Primitive** | The value | No | Semantic tier only |
| **Semantic** | The role | **Yes** | Components |
| **Component** | The binding | Inherits | One component |

### Which tier to use

**Semantic, almost always.** A component reaching for a primitive cannot respond
to theme — this is the single most common way a design system breaks in dark
mode.

The component tier exists only where a component needs a value that no semantic
role expresses, and where hard-coding would be worse. It is deliberately small;
if it grows past a few dozen entries, semantic roles are missing.

```kotlin
Text(color = CallScreenTheme.colors.content.primary)   // ✅ semantic
Text(color = Primitives.neutral900)                    // ❌ primitive — breaks dark mode
Text(color = Color(0xFF1A2030))                        // ❌ literal — CI failure
```

---

## 10.3 Naming grammar

```
<category>.<concept>.<variant>.<state>
```

Read left to right, general to specific. Every segment is `lowerCamelCase`;
segments are separated by `.`.

| Segment | Required | Values |
|---|---|---|
| `category` | ✅ | `color` `space` `radius` `elevation` `type` `motion` `icon` `stroke` `shadow` `haptic` |
| `concept` | ✅ | The role — `content`, `surface`, `status`, `action` |
| `variant` | — | `primary`, `fraud`, `md` |
| `state` | — | `hover`, `pressed`, `disabled`, `focus` |

### Examples

```
color.content.primary
color.status.fraud.text
color.action.primary.pressed
space.lg
radius.md
type.body.lg
motion.duration.medium
motion.easing.emphasized
elevation.2
```

### Rules

1. **No abbreviations** except the established scale suffixes (`sm` `md` `lg`
   `xl`). `bg`, `fg`, `btn`, `txt` are rejected.
2. **No colour names in semantic tokens.** `color.status.fraud`, never
   `color.red.500`. The primitive tier is the only place a hue is named.
3. **Scale suffixes are ordinal, not absolute.** `space.lg` is 16dp today; the
   name survives if that changes.
4. **State is always last** and comes from the fixed set in
   [07 §7.5](07-states.md).
5. **Singular, not plural.** `color`, not `colors`.
6. **No numbers in semantic names** except elevation levels, which are genuinely
   ordinal.

### Platform transforms

One name, mechanically transformed. The transform is never hand-written.

| Platform | Form | Example |
|---|---|---|
| JSON | dot | `color.status.fraud.text` |
| Compose | nested object | `colors.status.fraud.text` |
| CSS | kebab custom property | `--cs-color-status-fraud-text` |
| Figma | slash | `color/status/fraud/text` |

---

## 10.4 Files

```
design/tokens/
  primitive.json        raw palette and raw scales — no theme
  semantic.json         role definitions, referencing primitives per theme
  component.json        per-component bindings
  motion.json           duration, easing, spring, haptic
  type.json             the type scale
```

### Reference syntax

Standard `{dot.path}` interpolation, resolved at build time.

```jsonc
// primitive.json
{ "color": { "neutral": { "900": { "value": "#1A2030" } } } }

// semantic.json — one entry, three themes
{
  "color": {
    "content": {
      "primary": {
        "light":    { "value": "{color.neutral.900}" },
        "dark":     { "value": "{color.neutral.50}"  },
        "contrast": { "value": "{color.neutral.1000}" }
      }
    }
  }
}
```

**Every semantic token defines all three themes.** A missing theme fails the
build rather than silently falling back — a silent fallback is how a token ends
up illegible in high contrast without anyone noticing.

---

## 10.5 Build pipeline

```
design/tokens/*.json
        │
        ├─→ validate      schema · references resolve · no orphans
        ├─→ contrast      every semantic pair, all 3 themes  ── FAILS BUILD
        │
        ├─→ Compose       android/core/designsystem/token/*.kt
        ├─→ CSS           web/admin-console/tokens.css
        └─→ Figma         design/figma/tokens.json  (Tokens Studio format)
```

Run by `task design:tokens`; verified by `task design:drift`, which regenerates
and fails if the committed output differs — the same discipline applied to
protobuf bindings in [ADR-0001](../adr/0001-monorepo-structure-and-tooling.md).

### The contrast gate

The most important step. For every semantic foreground/background pair, in all
three themes, the computed ratio must meet the threshold in
[08 §8.2](08-accessibility.md).

**A token change that drops any pair below threshold fails the build.** This is
why the `600`/`300` rule in [02 §2.1](02-color.md) exists — it makes the passing
combination the default one.

---

## 10.6 Compose surface

Tokens reach composables through `CompositionLocal`, so theme changes propagate
without threading parameters.

```kotlin
CallScreenTheme(
    mode = ThemeMode.System,          // System | Light | Dark
    highContrast = false,             // bound to the platform setting
    dynamicColor = false,             // opt-in, neutrals only — 02 §2.8
) {
    // CallScreenTheme.colors / .typography / .spacing / .shapes / .motion
}
```

| Accessor | Type |
|---|---|
| `CallScreenTheme.colors` | `CallScreenColors` |
| `CallScreenTheme.typography` | `CallScreenTypography` |
| `CallScreenTheme.spacing` | `Spacing` |
| `CallScreenTheme.shapes` | `Shapes` |
| `CallScreenTheme.elevation` | `Elevations` |
| `CallScreenTheme.motion` | `Motion` |

**`MaterialTheme` is not used directly.** Material 3's colour roles do not
express ten domain statuses, and mapping our semantics onto `primary` /
`secondary` / `tertiary` would lose meaning. We interoperate where a Material
component needs a scheme, but our tokens are the source.

---

## 10.7 Enforcement

| Rule | Enforced by | Blocking |
|---|---|---|
| No hex literal in app code | detekt custom rule | ✅ |
| No raw `dp`/`sp` in app code | detekt custom rule | ✅ |
| No primitive reference outside semantic tier | token validator | ✅ |
| All three themes defined | token validator | ✅ |
| Contrast thresholds | contrast gate | ✅ |
| Generated output matches source | `task design:drift` | ✅ |
| No orphan tokens | token validator | ⚠️ warn |

**Generated Compose files are committed** — for IDE navigation and reproducible
builds — and drift-checked, exactly as protobuf bindings are.

---

## 10.8 Changing a token

1. Edit the JSON. Never the generated output.
2. `task design:tokens` to regenerate.
3. `task design:drift` and the contrast gate must pass.
4. Review screenshot diffs — a token change touches many components at once, and
   that blast radius is the point of the review.
5. Commit source and generated output together.

**A primitive change is a breaking change.** It affects every semantic role that
references it, in every theme, across every component. Treat it accordingly.
