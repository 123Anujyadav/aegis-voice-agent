# 9 · Component Catalogue

Every component, its contract, and its constraints.

> **Contracts, not implementations.** This document defines what each component
> must accept, render and announce. It does not define screens — a screen is a
> composition of these, and composition is Phase 4.

---

## 9.0 Layers

```
DOMAIN      CallerCard · FraudBadge · AiBadge · RiskIndicator · PremiumBadge
            Waveform · VoiceOrb · Transcript · CallTimeline
                              ↑ built from
CORE        Button · Card · TextField · SearchField · Dropdown · Sheet
            Dialog · ListItem · DataTable · Chart · Nav · Snackbar
                              ↑ built from
FOUNDATION  Surface · Text · Icon · Divider · Skeleton · Ripple · FocusRing
                              ↑ built from
TOKEN       colour · type · space · radius · elevation · motion
```

**A domain component may not reach past core into tokens for anything a core
component already provides.** That is how a design system becomes a pile of
one-off widgets.

### Every component ships with

Non-negotiable. Enforced by the checklist in [11](11-contributing.md).

- All applicable states from [07 §7.5](07-states.md)
- Light, dark and high-contrast rendering
- Screenshot variants: default, 200% text, high contrast, colour-blind ×4,
  reduced motion, RTL
- Devanagari and Tamil text fixtures, not only Latin
- A resolved accessible name and announced state
- 48dp minimum touch target where interactive
- KDoc on every public symbol — enforced by detekt

---

## 9.1 Foundation

### `Surface`

The base of everything. Owns background, shape, elevation, border and click
handling so no component re-implements them.

```kotlin
Surface(
    modifier: Modifier,
    shape: Shape = Shapes.md,
    color: Color = colors.surface.default,
    contentColor: Color = colors.content.primary,
    border: BorderStroke? = null,
    elevation: Elevation = Elevation.Level0,
    onClick: (() -> Unit)? = null,
    content: @Composable () -> Unit,
)
```

**Resolves the light/dark elevation mechanism** ([04 §4.3](04-space-shape-elevation.md)):
shadow in light, tonal surface step in dark. A component never chooses.

### `Text`, `Icon`, `Divider`, `Skeleton`, `FocusRing`

| Component | Contract |
|---|---|
| `Text` | Takes a `TypeStyle` token. Resolves script-aware tracking ([03 §3.5](03-typography.md)). Raw `fontSize` is not exposed |
| `Icon` | Takes a `IconSize` token. Enforces `currentColor`. `contentDescription` is a required parameter — `null` must be explicit |
| `Divider` | `stroke.thin`, `border.subtle`. Horizontal and vertical |
| `Skeleton` | Shape-matching placeholder driven by one shared shimmer clock ([07 §7.4](07-states.md)) |
| `FocusRing` | 2dp `border.focus`, 2dp offset, drawn outside bounds so it cannot be clipped |

---

## 9.2 Buttons

### Variants

| Variant | Fill | Use | Per screen |
|---|---|---|---|
| `Primary` | `action.primary.fill` | The one main action | **Max 1** |
| `Secondary` | `action.secondary.fill` | Alternative actions | Several |
| `Tertiary` | Transparent, no border | Low-emphasis, inline | Several |
| `Danger` | `action.danger.fill` | Block, delete, destructive | Max 1 |
| `Ghost` | Transparent, icon-forward | Toolbars, dense rows | Several |

### Sizes

| Size | Height | Padding H | Type | Icon |
|---|---:|---:|---|---|
| `sm` | 32dp | `space.md` | `label.md` | `icon.xs` |
| `md` | **40dp** | `space.lg` | `label.lg` | `icon.sm` |
| `lg` | 48dp | `space.xl` | `label.lg` | `icon.md` |

**Touch target is 48dp regardless of visual height.** `sm` and `md` extend their
target beyond their bounds.

```kotlin
Button(
    onClick: () -> Unit,
    label: String,
    modifier: Modifier = Modifier,
    variant: ButtonVariant = ButtonVariant.Primary,
    size: ButtonSize = ButtonSize.Medium,
    leadingIcon: ImageVector? = null,
    trailingIcon: ImageVector? = null,
    enabled: Boolean = true,
    loading: Boolean = false,
    haptic: HapticToken? = HapticToken.Tick,
)
```

**Loading preserves width.** The label is replaced by a spinner at the same
measured width, so the layout does not jump — a jumping button on tap is the
cheapest-looking interaction in mobile UI.

**One primary per screen.** Two primaries means neither is primary.

---

## 9.3 Cards

```kotlin
Card(
    modifier: Modifier = Modifier,
    variant: CardVariant = CardVariant.Outlined,
    onClick: (() -> Unit)? = null,
    content: @Composable ColumnScope.() -> Unit,
)
```

| Variant | Treatment | Use |
|---|---|---|
| `Outlined` | **Default.** `border.subtle`, `elevation.0` | Lists, most content |
| `Elevated` | `elevation.1`, no border | Content that floats above |
| `Filled` | `surface.sunken`, no border | Grouped, de-emphasised |

**Outlined is the default** because borders are cheaper than shadows, work
identically in both themes, and read as more precise
([04 §4.5](04-space-shape-elevation.md)).

**A card is a separable object, not a decoration.** Content that is simply a
group uses spacing, not a card ([04 §4.1](04-space-shape-elevation.md)).

---

## 9.4 Inputs

### `TextField`

```kotlin
TextField(
    value: String,
    onValueChange: (String) -> Unit,
    label: String,
    modifier: Modifier = Modifier,
    placeholder: String? = null,
    helperText: String? = null,
    errorText: String? = null,
    leadingIcon: ImageVector? = null,
    trailingContent: (@Composable () -> Unit)? = null,
    enabled: Boolean = true,
    readOnly: Boolean = false,
    singleLine: Boolean = true,
    keyboardType: KeyboardType = KeyboardType.Text,
)
```

| Property | Value |
|---|---|
| Height | 48dp single line; grows for multiline |
| Shape | `radius.sm` |
| Border | `stroke.thin` `border.default` → `border.focus` 2dp on focus |
| Label | Always visible above. **Never a placeholder-as-label** |
| Error | `errorText` replaces `helperText`, border → `status.fraud` |

**Placeholder is never the label.** A placeholder vanishes on input, leaving the
user unable to recall what the field is — a well-documented usability failure and
an accessibility one.

**Error text replaces helper text** rather than appearing below it, so the field
does not grow and reflow the form on validation.

### `PhoneField`

Domain-specific and worth its own component because phone numbers are the
identity primitive in this product ([ADR-0010](../adr/0010-authentication-and-device-trust.md)).

- India-first: `+91` default, formats as `+91 98765 43210`
- **`numeric.md` with `tnum`** so digits do not jitter while typing
- Accessible name reads **digit by digit with grouping**
  ([08 §8.5](08-accessibility.md))

### `SearchField`

- `radius.full`, `surface.sunken`, leading search icon
- Clear button appears only when non-empty
- Debounce is the caller's concern, not the component's
- Empty result state is [07 §7.1](07-states.md) tier "Filtered" — no illustration

### `Dropdown` / `Select`

- Trigger renders as a `TextField` with a trailing chevron
- Menu is `elevation.4`, `radius.md`, max height 320dp then scrolls
- Selected item shows a leading check, **not** colour alone
- Keyboard: type-ahead, arrow navigation, Escape to close
- Below 6 items, prefer inline radio; above 12, prefer a searchable sheet

---

## 9.5 Overlays

### `BottomSheet`

The primary overlay on mobile — reachable one-handed, unlike a centred dialog.

```kotlin
BottomSheet(
    onDismissRequest: () -> Unit,
    modifier: Modifier = Modifier,
    dragHandle: Boolean = true,
    scrimBlur: Boolean = true,
    content: @Composable ColumnScope.() -> Unit,
)
```

- `radius.xl` top corners, `elevation.4`
- Drag handle: 32 × 4dp, `radius.full`, `border.strong`
- Motion: `spring.gentle` — interruptible, because the user can grab it
- Scrim: `neutral.900` @ 40% with 20dp blur where available, **@ 60% solid
  below API 31** ([04 §4.6](04-space-shape-elevation.md))
- Snap points: content-height, half, full. Haptic `tick` on snap

### `Dialog`

For decisions that must not be dismissed accidentally.

- `radius.xl`, `elevation.5`, max width 320dp
- **Destructive actions are `Danger` variant and are never the default focus**
- Motion: scale 0.96 → 1.0 + fade, `duration.short`
- **Never used for time-critical actions** ([08 §8.4](08-accessibility.md))

### `Snackbar`

- `surface.inverse`, `radius.md`, `elevation.4`
- 5 s default; **never auto-dismisses when it carries an action**
- One at a time; a new one replaces the current
- Above the bottom nav, never covering the primary action

---

## 9.6 Navigation

| Component | Contract |
|---|---|
| `TopAppBar` | 56dp, `surface.default`, `elevation.0` at rest → `elevation.2` on scroll. Title `headline.sm`. Max 2 trailing actions |
| `BottomNav` | 64dp + insets, 3–5 destinations. **Filled icon = selected** ([06 §6.1](06-icons-illustration.md)). Label always visible — icon-only navigation fails Voice Access |
| `TabRow` | Scrollable when > 3. Indicator 2dp, `radius.full`, `status.telephony` |
| `Breadcrumb` | Rare on mobile. Sheet hierarchies only |

**Labels are always visible in bottom navigation.** Icon-only tabs are
unlabelled for Voice Access users and ambiguous for everyone at a glance.

---

## 9.7 Lists and data

### `ListItem`

The most-used component in the product.

```kotlin
ListItem(
    headline: String,
    modifier: Modifier = Modifier,
    supporting: String? = null,
    overline: String? = null,
    leading: (@Composable () -> Unit)? = null,
    trailing: (@Composable () -> Unit)? = null,
    onClick: (() -> Unit)? = null,
)
```

- Min height 56dp (one line), 72dp (two), 88dp (three)
- Padding `space.lg` horizontal, `space.md` vertical
- **`mergeDescendants` for screen readers** — a caller row must announce as one
  node, not eleven ([08 §8.5](08-accessibility.md))

### `DataTable`

Rare on mobile; exists for the operator console and dense settings.

- Below 600dp, **collapses to a card list**. Horizontal scrolling tables are a
  mobile antipattern
- Header row `label.md`, sticky
- Numeric columns **right-aligned with `numeric.*`** and `tnum`
- Zebra striping via `surface.sunken` at 50% — never a border grid

### `Chart`

Minimal set: sparkline, bar, donut. Charts are supporting, not hero.

| Rule | Detail |
|---|---|
| **Never colour-only** | Series carry a label or a pattern |
| Max 5 series | Beyond that, comprehension collapses |
| Axis labels `label.sm`, `content.tertiary` | |
| No 3D, no gradients, no drop shadows | |
| Accessible alternative required | A data table equivalent is not optional |
| Animation | Draw-in on first render only, `duration.medium`. Never on update |

---

## 9.8 Domain components

Where this design system earns its keep.

### `CallerCard`

The most-read surface in the product. It is what the user opens the app to see.

```kotlin
CallerCard(
    callerName: String?,          // null = unknown
    phoneNumber: String,
    screenedAt: Instant,
    outcome: ScreeningOutcome,    // Allowed | Declined | Blocked | Missed
    risk: RiskLevel?,             // null = not assessed
    summary: String?,             // one-line AI summary
    isPremiumOnly: Boolean = false,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
)
```

**Hierarchy is fixed** — this is the two-second-glance surface
([01 §1.2](01-foundations.md)):

```
1. Who       callerName ?: "Unknown caller"      title.md, content.primary
2. What      summary                              body.md, content.secondary
3. Risk      RiskIndicator                        only if risk != null
4. When      screenedAt, relative                 numeric.sm, content.tertiary
5. Number    formatted                            body.sm, content.tertiary
```

**At most one coloured element.** If risk is present, nothing else on the card is
coloured ([01 Principle 3](01-foundations.md)).

Accessible output is composed deliberately, not derived from children
([08 §8.5](08-accessibility.md)).

### `RiskIndicator`

The product's core judgement, and the component where
[Principle 2 — never overclaim](01-foundations.md#principle-2--never-overclaim)
is most at stake.

```kotlin
RiskIndicator(
    level: RiskLevel,            // Safe | Unknown | Spam | Fraud | Emergency
    confidence: Confidence,      // Low | Medium | High
    modifier: Modifier = Modifier,
    variant: RiskVariant = RiskVariant.Badge,   // Badge | Inline | Detailed
)
```

| Level | Colour | Icon | Label |
|---|---|---|---|
| `Safe` | `status.success` | check | "Looks fine" |
| `Unknown` | `content.tertiary` | question | "Not assessed" |
| `Spam` | `status.spam` | crossed megaphone | "Likely spam" |
| `Fraud` | `status.fraud` | broken shield | "Possible fraud" |
| `Emergency` | `status.emergency` | filled circle | "Urgent" |

**Confidence is always rendered**, never hidden:

| Confidence | Rendering |
|---|---|
| `High` | Filled badge, label as above |
| `Medium` | Outlined badge, label prefixed "Likely" |
| `Low` | Outlined, `content.tertiary`, label prefixed **"Possibly"** |

> **A low-confidence fraud verdict must never look like a high-confidence one.**
> The model is sometimes wrong, and the interface says so. Hiding uncertainty to
> look decisive is the single most damaging thing this component could do.

### `FraudBadge`

`RiskIndicator` specialised to `Fraud`, with an evidence affordance.

- Always tappable → opens the transcript at the flagged turn
  ([01 Principle 1 — show the work](01-foundations.md))
- Haptic `heavy` on first appearance ([05 §5.8](05-motion.md))
- Announced **assertively** ([08 §8.5](08-accessibility.md))

### `AiBadge`

Marks content generated by the assistant. Applied to summaries, verdicts and
suggested actions.

- `status.ai.subtle` background, `status.ai.text`, `icon.xs` orb glyph
- Label: **"Assistant"** — never "AI" alone, never a product name, never a
  persona ([01 Principle 2](01-foundations.md))
- Present on **every** model-generated string in the UI without exception. The
  user must always be able to tell what a machine wrote

### `PremiumBadge`

The one gradient in the product ([02 §2.6](02-color.md)).

- `radius.full`, gradient `premium.300 → premium.500`, faceted glyph
- **Never conveys a state requiring action** — it is a label, not an alert
  ([02 §2.3 R4](02-color.md))
- Never animated

### `VoiceOrb`

```kotlin
VoiceOrb(
    state: VoiceState,           // Idle | Listening | Thinking | Speaking | Error
    amplitude: Float?,           // 0f..1f, null = no data available
    size: Dp = 96.dp,
    modifier: Modifier = Modifier,
)
```

**The honesty contract** ([05 §5.6](05-motion.md)):

> If `amplitude` is `null`, the orb renders **static**. It never synthesises
> motion to imply liveness that is not happening.

Non-anthropomorphic by construction: a circle and an arc, no features, ever.

### `Waveform`

```kotlin
Waveform(
    amplitudes: FloatArray,      // pre-allocated, reused by the caller
    modifier: Modifier = Modifier,
    live: Boolean = false,
    barCount: Int = 48,
)
```

Implementation constraints are part of the contract, not an optimisation
([05 §5.6](05-motion.md)):

- **One `Canvas`.** Not `barCount` composables
- **Zero allocation per frame.** The caller owns and reuses the array
- **Sampled to 30 Hz**, interpolated to display refresh
- **Hidden from screen readers** — decorative; the transcript carries the
  information

### `Transcript`

The accessibility surface of the product ([08 §8.7](08-accessibility.md)).

```kotlin
Transcript(
    turns: List<TranscriptTurn>,
    modifier: Modifier = Modifier,
    highlightTurnId: String? = null,   // deep link from FraudBadge
    isLive: Boolean = false,
)
```

| Aspect | Treatment |
|---|---|
| Type | `body.lg` — the most generous in the system |
| Line length | Capped ~66 characters |
| Speaker separation | `space.lg` + a text label. **Never colour alone** |
| Caller turns | `surface.sunken`, left-aligned |
| Assistant turns | `surface.default` + `AiBadge`, left-aligned |
| Interim text | `content.tertiary`, italic-free, **not announced** |
| Selection | Fully selectable and copyable |
| Live region | `polite` on completed turns only |

**Assistant and caller turns are not left/right aligned like a chat app.**
Chat alignment implies a conversation between peers; this is a record of a
screening, and both parties read left-aligned in reading order.

### `CallTimeline`

Chronological events within one call: answered, announcement, turns, verdict,
outcome.

- Vertical rail, 2dp, `border.subtle`
- Nodes: 8dp `radius.full`, semantic colour + icon
- Timestamps `numeric.sm`, relative to call start
- Collapsible; collapsed by default beyond 10 events

---

## 9.9 Voice controls

| Component | Contract |
|---|---|
| `TakeoverButton` | `lg` size, `radius.full`, `status.telephony` fill. **No confirmation** — time-critical ([08 §8.4](08-accessibility.md)). Haptic `heavy` |
| `MuteToggle` | Icon toggle. Struck-through mic when muted. Announced **assertively** |
| `RecordingIndicator` | Pulsing dot + **"Recording"** label. **Never suppressible** — legal disclosure ([07 §7.6](07-states.md)) |
| `EndCallButton` | `Danger` variant, `radius.full`. Haptic `reject` |
| `LiveListenToggle` | Ghost button; shows connection state; degrades to transcript-only if WebRTC fails ([ADR-0004](../adr/0004-media-transport.md)) |

---

## 9.10 Rules

1. **Layers only go downward.** Domain → core → foundation → tokens.
2. **One primary action per screen. At most one coloured element per card.**
3. **Every model-generated string carries an `AiBadge`.**
4. **Confidence is always shown.** Low-confidence never renders as high.
5. **The orb animates from real data or stays static.**
6. **The waveform is one Canvas with zero per-frame allocation.**
7. **The recording indicator is never suppressible.**
8. **Loading preserves layout.** No width jumps, no reflow.
9. **Every component ships its full test-variant matrix** ([9.0](#every-component-ships-with)).
