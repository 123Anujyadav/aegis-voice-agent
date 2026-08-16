# 11 · Contributing to the Design System

---

## 11.1 Before you add anything

Four questions, in order. A "no" at any point means stop.

1. **Does an existing component do this?** Most requests are a variant, not a
   component. A variant is cheaper to maintain and cheaper for users to learn.
2. **Will this be used in at least three places?** A component used once is a
   screen, and belongs in the feature module.
3. **Can it be composed from existing components?** If so, compose it in the
   feature and promote it later if the pattern repeats.
4. **Does it need a new token?** Almost always no. Adding a token is the most
   expensive change in this system ([10 §10.8](10-tokens-and-naming.md)).

> The most valuable thing a design system reviewer does is say no. Every
> component added is a permanent maintenance cost and one more thing an engineer
> has to know before they can build a screen.

---

## 11.2 The component checklist

A component is not done when it renders. It is done when every line below is
true.

### Contract

- [ ] Public API documented with KDoc on **every** public symbol — detekt blocks
      the build otherwise
- [ ] All parameters have sensible defaults except the genuinely required ones
- [ ] `modifier: Modifier = Modifier` is the first optional parameter
- [ ] No parameter takes a raw `Color`, `Dp` or `TextUnit` — tokens only
- [ ] Slot APIs (`@Composable () -> Unit`) rather than boolean flags where the
      caller may need to substitute content

### Tokens

- [ ] Zero hex literals, zero raw `dp`, zero raw `sp`
- [ ] Semantic tokens only — no primitive references
      ([10 §10.2](10-tokens-and-naming.md))
- [ ] Elevation requested as a token; the theme resolves shadow vs tonal
      ([04 §4.3](04-space-shape-elevation.md))

### States

- [ ] Every applicable state from [07 §7.5](07-states.md)
- [ ] Disabled uses tokens, **never** blanket alpha
- [ ] Focus ring drawn outside bounds, unclippable
- [ ] Loading preserves layout — no width jump

### Accessibility

- [ ] Accessible name resolves and describes the **action**
- [ ] Decorative children hidden (`contentDescription = null`, explicit)
- [ ] `mergeDescendants` where the component is one logical unit
- [ ] Touch target ≥ 48dp
- [ ] State announced via semantics, not baked into the name
- [ ] Colour is never the only carrier ([08 §8.3](08-accessibility.md))

### Motion

- [ ] Transform and opacity only ([05 §5.1](05-motion.md))
- [ ] Reduced-motion variant defined and correct
- [ ] No animated shadow, blur or colour on the hot path

### Tests

Every one of these is a screenshot variant, not a manual check:

- [ ] Light · Dark · High contrast
- [ ] 200% text scale
- [ ] Colour-blind ×4 (deuteranopia, protanopia, tritanopia, achromatopsia)
- [ ] Reduced motion
- [ ] RTL
- [ ] **Devanagari and Tamil** text fixtures, not only Latin
- [ ] Unit tests for state logic
- [ ] Coverage ≥ 85% on new lines (Phase 1 §17)

---

## 11.3 Changing a token

See [10 §10.8](10-tokens-and-naming.md). The short version:

1. Edit `design/tokens/*.json`. **Never** the generated output.
2. `task design:tokens`
3. `task design:drift` and the contrast gate must pass
4. Review the screenshot diff — a token change touches many components at once,
   and that blast radius **is** the review
5. Commit source and generated output together

**A primitive change is a breaking change.** It propagates through every semantic
role, every theme, every component.

---

## 11.4 Deprecating

Nothing is deleted immediately.

1. Mark `@Deprecated` with a `ReplaceWith` that actually compiles
2. Update every call site in the same release
3. Remove **one release later**

A deprecation without a working replacement is a bug report addressed to a future
engineer.

---

## 11.5 Review

Design system changes require **both** `@callscreen/android` and
`@callscreen/design` approval (CODEOWNERS).

The reviewer asks:

| Question | Why |
|---|---|
| Could this be a variant of something existing? | Fewer components is better |
| Does it hold in greyscale? | [08 §8.3](08-accessibility.md) |
| Does it hold at 200% text? | [03 §3.6](03-typography.md) |
| Does it hold in Devanagari? | [03 §3.1](03-typography.md) |
| Is anything animated that shouldn't be? | [05 §5.1](05-motion.md) |
| How many coloured elements? | Should be zero or one ([01 P3](01-foundations.md)) |
| Does it overclaim? | [01 P2](01-foundations.md) — confidence, AI attribution |

---

## 11.6 The five things that will break this system

Named, so they can be caught in review.

1. **A hex value in a screen.** One is tolerated, then twenty, then the theme is
   decorative.
2. **A component that takes a `Color` parameter.** It moves the decision to the
   caller and every caller decides differently.
3. **A blanket `alpha` for disabled.** Invisible contrast failure.
4. **A "temporary" magic number.** It is never temporary.
5. **A component built for one screen.** It grows six boolean flags and becomes
   unmaintainable.

If you find yourself doing any of these, the design system is missing something.
**Say so** — a gap in the system is a system problem, not a licence to work
around it.
