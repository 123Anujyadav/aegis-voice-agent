# 3 · Typography

Type scale, Indic script handling, and accessible text.

---

## 3.1 The constraint that decides everything

**Devanagari sets the metrics. Latin follows.**

Devanagari stacks matras above and below the base line; Bengali and Tamil use
tall conjuncts. Type set to comfortable Latin metrics **clips them**. Type set to
Devanagari-safe metrics is merely generous for Latin.

Every line-height and letter-spacing value below is derived from Devanagari first
and checked against Latin second. Doing it the other way round produces a system
that looks correct in review and breaks for the majority of our market.

Three consequences, all non-negotiable:

- **Every style carries at least 6sp of absolute leading** above its font size.
  See §3.3 for why this is a floor in `sp` and not a ratio — getting that
  distinction wrong is the most common mistake in this area.
- **Negative letter spacing is forbidden on Indic scripts.** It destroys
  conjuncts. Our tracking tokens are script-aware (§3.5).
- **Vertical rhythm is looser than a Latin-only system** would choose. That is
  the correct trade.

---

## 3.2 Font selection

### Decision: Roboto Flex, system-provided, variable

| | |
|---|---|
| **Text face** | **Roboto Flex** — variable, system-resident on Android 12+ |
| **Fallback** | Roboto (API 26–31), then platform default |
| **Indic** | System Noto Sans Devanagari / Bengali / Tamil / Telugu / Gujarati |
| **Numerals** | Roboto Flex with `tnum` (tabular figures) |
| **Bundled fonts** | **None** |

### Why not a bundled brand face

A distinctive display face is the obvious "premium" move, and it fails three of
our constraints simultaneously:

- **APK size.** The Phase 1 release gate fails a >2% APK delta. Covering
  Devanagari, Bengali, Tamil, Telugu and Latin in a single family is several
  megabytes — the entire size budget spent on typography.
- **Script coverage.** A bundled Latin display face means Indic text renders in a
  *different* family. Mixed-script UI with mismatched families looks broken, and
  Hinglish means mixed-script is the common case, not the exception.
- **First-run cost.** A downloadable font shows a flash of fallback text on
  first launch — on the trust-establishing screen.

### Why variable is the answer instead

Roboto Flex exposes axes that give a distinctive voice at zero size cost:

| Axis | Use |
|---|---|
| `wght` 100–1000 | The weight scale (§3.4) |
| `opsz` optical size | Automatic optical correction — display sizes get tighter apertures, small sizes get more open ones. This is what makes type feel *designed* rather than *scaled*. |
| `GRAD` grade | **Dark-mode compensation without metric shift.** Light text on dark appears heavier; a small negative grade corrects it *without changing advance widths*, so layout does not reflow between themes. |

`GRAD` is the detail that separates a considered system from a scaled one, and it
costs nothing.

> On API < 31 the variable axes are unavailable and Roboto static instances are
> used. Degradation is silent and acceptable — weights map to the nearest static
> instance.

---

## 3.3 Type scale

Modular, ratio ≈ 1.125–1.2, tuned rather than purely generated — a generated
scale produces sizes nobody needs and gaps where you want a step.

| Token | Size | Line height | Tracking | Weight | Use |
|---|---:|---:|---:|---:|---|
| `display.lg` | 40sp | 48sp (1.20) | −0.5% | 700 | Onboarding hero. Rare. |
| `display.md` | 32sp | 40sp (1.25) | −0.5% | 700 | Screen hero |
| `display.sm` | 28sp | 36sp (1.29) | −0.25% | 600 | Section hero |
| `headline.lg` | 24sp | 32sp (1.33) | 0 | 600 | Screen title |
| `headline.md` | 22sp | 30sp (1.36) | 0 | 600 | Card title, large |
| `headline.sm` | 20sp | 28sp (1.40) | 0 | 600 | Sheet title |
| `title.lg` | 18sp | 26sp (1.44) | 0 | 600 | List section |
| `title.md` | 16sp | 24sp (1.50) | 0 | 600 | Card title |
| `title.sm` | 14sp | 22sp (1.57) | +0.5% | 600 | Dense title |
| `body.lg` | 16sp | 26sp (1.63) | 0 | 400 | **Transcript, long reading** |
| `body.md` | 14sp | 22sp (1.57) | 0 | 400 | Default body |
| `body.sm` | 12sp | 20sp (1.67) | +1% | 400 | Supporting text |
| `label.lg` | 14sp | 20sp (1.43) | +1% | 500 | Button |
| `label.md` | 12sp | 18sp (1.50) | +1.5% | 500 | Badge, chip |
| `label.sm` | 11sp | 18sp (1.64) | +2% | 500 | Overline, dense meta |
| `numeric.lg` | 32sp | 40sp | 0 | 600 `tnum` | Call duration, live |
| `numeric.md` | 16sp | 24sp | 0 | 500 `tnum` | Timestamps, counts |
| `numeric.sm` | 12sp | 18sp | 0 | 500 `tnum` | Dense metrics |

### The invariant is absolute leading, not a ratio

A naive reading of §3.1 gives "1.5× everywhere", and that is **wrong twice
over**:

- **Large type needs proportionally *less* leading.** A 40sp display at 1.5×
  would be a 60sp line height, which looks broken. The ratios above correctly
  *decrease* as size increases — 1.67 at 12sp, 1.20 at 40sp.
- **WCAG 1.4.12's 1.5× applies to paragraph text**, not headings or labels.

What matras actually need is **absolute room**. So two rules hold, and
`TypographyTest` enforces both:

| Rule | Applies to |
|---|---|
| **≥ 6sp leading** above font size | **Every** style |
| **≥ 1.5× ratio** | `body.*` only — WCAG 1.4.12 |

This is why `label.md` is 12/18 rather than 12/16: four spare `sp` is enough for
Latin and not enough for a Devanagari matra stack.

### Display sizes are deliberately rare

`display.*` appears on onboarding and empty states. It does not appear on any
screen a user sees daily. Large type used routinely stops signalling importance
and starts signalling shouting — which violates
[Principle 3](01-foundations.md#principle-3--quiet-by-default-loud-when-it-matters).

### Numeric is a separate family for a reason

Call durations tick every second. Timestamps sit in scanning columns. With
proportional figures, `1` is narrower than `8` and the text **jitters** on every
tick — a small ugliness that reads as cheap.

`numeric.*` locks `tnum` (tabular figures) so digits share an advance width.
This is the single most noticeable "premium" typographic detail in the product,
and it costs one font feature flag.

**Rule: any number that changes over time, or that appears in a vertical column,
uses `numeric.*`. No exceptions.**

---

## 3.4 Weight

| Weight | `wght` | Use |
|---|---:|---|
| Regular | 400 | Body, transcripts |
| Medium | 500 | Labels, buttons, numerics |
| Semibold | 600 | Titles, headlines |
| Bold | 700 | Display only |

**Four weights, and no more.** Light and Thin are illegible at small sizes on
mid-range displays and unavailable in most Indic fallbacks. Black is shouting.

Weight is the **primary** hierarchy tool, ahead of size and far ahead of colour —
it works in greyscale, survives the colour-blindness test, and does not consume
the scarce colour budget.

---

## 3.5 Letter spacing is script-aware

Tracking tokens resolve differently by script. This is enforced in the Compose
layer, not left to the caller.

| Token | Latin | **Indic** | Reason |
|---|---:|---:|---|
| `tracking.tight` | −0.5% | **0** | Negative tracking breaks conjuncts |
| `tracking.snug` | −0.25% | **0** | Same |
| `tracking.normal` | 0 | 0 | — |
| `tracking.loose` | +1% | **+0.5%** | Indic needs less; matras already separate |
| `tracking.wide` | +2% | **+1%** | Same |

> **Never hard-code a negative `letterSpacing` in a composable.** Use the token.
> The resolver reads the active locale and returns the script-correct value.

---

## 3.6 Responsive typography

Android, not web — this responds to **user text size**, not viewport width.

### Dynamic type

All sizes are `sp` and scale with the system font-size setting. The system
supports up to **200%**. At that scale a 16sp body becomes 32sp.

**Layout must not break at 200%.** Rules:

- **Never fix the height of a text container.** Use `wrapContentHeight` or a
  minimum height.
- **Every text-bearing component has a `maxLines` and an overflow policy** —
  chosen deliberately per component, not defaulted.
- **Below 320dp effective width, single-column.** Two-column layouts collapse.
- **Icons scale with text** where they sit inline with it; standalone icons do
  not.

### The cap, and why there is one

Sizes above `headline.lg` are capped at **130%** scaling. Uncapped, a 40sp
display at 200% is 80sp and consumes an entire screen on a 5-inch device,
pushing the actual content out of view — which is worse for the low-vision user
the setting exists to serve.

Body, label and numeric text is **never capped**. The cap applies only to
decorative-scale type.

### Screen size

| Width | Behaviour |
|---|---|
| < 360dp | Single column, `space.md` margins, `body.md` default |
| 360–599dp | **Baseline.** `space.lg` margins |
| ≥ 600dp | Content max-width 600dp, centred. Type does **not** scale up |

Type size does not increase on tablets. Line lengths beyond ~75 characters hurt
readability; wider screens get more margin, not bigger text.

---

## 3.7 Accessibility

| Requirement | Rule |
|---|---|
| **Minimum size** | 12sp. `label.sm` (11sp) is permitted **only** for non-essential metadata that is duplicated elsewhere |
| **Line height** | ≥ 1.5× for all body text (WCAG 1.4.12) — already exceeded |
| **Paragraph spacing** | ≥ 2× font size |
| **Letter spacing** | User override to 0.12em must not clip |
| **Word spacing** | User override to 0.16em must not clip |
| **Contrast** | See [02 §2.5](02-color.md) and [08](08-accessibility.md) |
| **Justification** | **Never.** Justified text creates rivers and is a documented dyslexia barrier |
| **All-caps** | Only `label.sm` overlines, never sentences — screen readers spell out capitals |
| **Italic** | Not used. Poor Indic support, and a weak hierarchy signal |
| **Underline** | Links only, never emphasis |

### Transcripts are the accessibility surface

The call transcript is the product feature that makes this app usable by deaf and
hard-of-hearing users ([README §4](README.md)). It gets the most generous
typography in the system:

- `body.lg` at 16sp / 26sp — the largest body size
- Line length capped at **~66 characters**
- Speaker turns separated by `space.lg`, not by colour alone
- Full user text-scaling, uncapped
- Selectable, copyable, and screen-reader navigable by turn

---

## 3.8 Rules

1. **Use tokens.** A raw `fontSize` or `lineHeight` in a composable is a defect.
2. **Numbers that change or column-align use `numeric.*`.** Always.
3. **Never hard-code negative letter spacing.** Use `tracking.*`.
4. **Test at 200% text scale** before merge. It is a screenshot-test variant.
5. **Test with Devanagari and Tamil**, not only Latin. Both are fixtures.
6. **Weight before size, size before colour** for hierarchy.
7. **Four weights only.** Adding a fifth requires justification in review.
