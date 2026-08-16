# 7 · States

Empty, error, loading, skeleton, and the interaction state matrix.

---

## 7.1 Empty states

### The rule most systems get wrong

**An empty state is seen more than once.** The charming illustration that
delights on first view is an obstruction on the fiftieth — it pushes the useful
content down and says nothing new.

So empty states are **tiered by how often they occur**:

| Tier | When | Treatment |
|---|---|---|
| **First-run** | User has never had data here | Illustration + heading + body + primary action |
| **Recurring** | Genuinely empty right now | Icon + one line + action if any |
| **Filtered** | Search or filter matched nothing | One line + clear-filter action. **No illustration** |
| **Cleared** | User emptied it themselves | One line. No action — they meant to do it |

### Anatomy

```
        [ illustration or icon ]      ← tier-dependent
                space.lg
        Heading                        ← title.md, content.primary
                space.xs
        One sentence explaining        ← body.md, content.secondary
        what will appear here          ← max 2 lines, ≤ 100 chars
                space.xl
        [ Primary action ]             ← only if there is a real next step
```

Vertically centred in the container, horizontally centred, max width 320dp.

### Copy

| Do | Don't |
|---|---|
| "No screened calls yet" | "It's quiet in here! 🦗" |
| "Screened calls will appear here once someone unknown calls you." | "Nothing to see!" |
| "No results for 'delivery'" | "Oops! We couldn't find anything." |

Never apologise for an empty state that is working correctly. Never use humour —
[01 §1.6](01-foundations.md) — and never an emoji.

---

## 7.2 Error states

### Tiers

Severity determines placement, and placement is a design decision, not a
developer one.

| Tier | Use | Placement |
|---|---|---|
| **Inline** | Field-level validation | Below the field, `status.fraud.text`, icon + message |
| **Contextual** | One section failed | Banner within the section, `status.warning.subtle` |
| **Blocking** | Screen cannot function | Full-screen: icon, heading, body, retry |
| **Transient** | Failed but recoverable, non-blocking | Snackbar, 5s, with action |
| **Destructive** | Data loss risk | Dialog, explicit confirmation |

### Every error answers three questions

1. **What happened** — in plain language, never a code
2. **Why** — if we know, and if it helps
3. **What now** — a specific action, not "try again later"

```
✗  Error 503: upstream unavailable
✓  Couldn't reach the screening service
   Your calls are ringing through normally right now.
   [ Retry ]
```

The second version does the thing that matters in a telephony product: it tells
the user **their phone still works**. The failure mode of our backend is "the
phone rings normally" ([ADR-0002](../adr/0002-telephony-architecture.md)) — and
the error state is where the user learns that.

### Error copy rules

| Rule | Example |
|---|---|
| Never expose an error code in primary copy | Trace ID goes in a collapsible "Details" for support |
| Never blame the user | "That number isn't valid" not "You entered an invalid number" |
| Never say "unexpected error" | If we cannot say what happened, say what the user can do |
| Never use "Oops", "Uh oh", "Whoops" | Cute copy in a failure state reads as unserious |
| State the impact on screening | The user's real question is always "is my phone still working" |

### Errors are not always red

`status.fraud` (red) is for **failures**. `status.warning` (amber) is for
**degradations**. Using red for a recoverable, non-blocking condition trains the
user to ignore red, which matters enormously in a product where red also means
*fraud*.

---

## 7.3 Loading states

Chosen by **expected duration**, measured — not guessed.

| Duration | Treatment | Why |
|---|---|---|
| **< 100 ms** | **Nothing** | Below perception. A spinner that flashes is worse than no spinner |
| **100–300 ms** | Optimistic UI, or nothing | Show the expected result; reconcile on arrival |
| **300 ms – 2 s** | **Skeleton** | Preserves layout, communicates shape |
| **2–10 s** | Skeleton + progress text | The user needs to know it is still working |
| **> 10 s** | Determinate progress + cancel | Indeterminate at this length reads as hung |
| **Unknown** | Indeterminate + descriptive text | "Connecting to your carrier…" |

### The 100 ms floor

**Never show a loading indicator for something that usually completes in under
100 ms.** The flash of a spinner appearing and vanishing is more disruptive than
a brief pause, and it makes fast software feel slow.

Implementation: delay the indicator by 100 ms. If the data arrives first, it was
never shown.

### Spinners are a last resort

A spinner communicates "something is happening" and nothing else. A skeleton
communicates *what* is coming and *where it will be*, so the user can begin
parsing layout before content arrives. Prefer skeletons everywhere the shape is
known.

Spinners are correct only for: indeterminate work of unknown shape, inline button
loading, and pull-to-refresh.

---

## 7.4 Skeleton system

### Rules

1. **Skeletons match the real layout exactly.** Same dimensions, same spacing,
   same radius. A skeleton that reflows on load is worse than a spinner — it
   makes the arrival of content feel like a glitch.
2. **Skeleton text blocks are 60–90% width, varied.** Uniform full-width bars
   look like a barcode, not like text.
3. **Never skeleton more than one screenful.** Below the fold, nothing.
4. **Never skeleton indefinitely.** After the timeout, an error state.

### Anatomy

| Element | Skeleton |
|---|---|
| Text line | `radius.xs`, height = line height × 0.6, width 60–90% varied |
| Title | `radius.xs`, height = line height × 0.7, width 40–60% |
| Avatar | `radius.full`, exact size |
| Icon | `radius.xs`, exact size |
| Card | `radius.md`, exact size, containing child skeletons |
| Button | `radius.md`, exact size |

### Shimmer

| Property | Value |
|---|---|
| Base | `surface.sunken` |
| Highlight | `neutral.0` @ 40% (light) · `neutral.0` @ 6% (dark) |
| Motion | Linear gradient sweep, −100% → 200% |
| Duration | `duration.shimmer` (1200 ms), `easing.linear`, looping |
| Angle | 20° from vertical |

**One shimmer animation drives every skeleton on screen**, from a single shared
clock. Per-element animations desynchronise into visual noise and multiply the
frame cost.

**Under reduced motion the shimmer is removed** and skeletons render as static
`surface.sunken` blocks ([05 §5.7](05-motion.md)).

---

## 7.5 Interaction state matrix

Every interactive component implements all of these. A component missing a state
is incomplete.

| State | Visual | Motion |
|---|---|---|
| **Default** | Base tokens | — |
| **Hover** *(pointer only)* | Surface +4% tonal | `micro` |
| **Pressed** | Surface +8% tonal, **scale 0.97** | `micro` in, `spring.snappy` out |
| **Focused** | 2dp `border.focus` ring, 2dp offset | `micro` |
| **Selected** | `status.*.subtle` fill, `stroke.medium` border | `short` |
| **Disabled** | `action.disabled.*`, **no** opacity multiplier | none |
| **Loading** | Content → inline spinner, width preserved | `short` |
| **Error** | `status.fraud` border + message | `short` + shake |
| **Read-only** | `surface.sunken`, no border, normal content colour | none |

### Disabled uses tokens, not opacity

**Never `alpha = 0.38` on a whole component.** Blanket opacity produces
unpredictable contrast against whatever is behind it, and it fails WCAG in a way
that is invisible in review.

Disabled uses explicit `action.disabled.fill` and `action.disabled.content`
tokens with **verified** contrast. Disabled text must still reach 4.5:1 — a
disabled control the user cannot read is a control they cannot understand the
purpose of.

### Focus is never removed

The focus ring is required for keyboard and switch-access users. It is drawn
**outside** the component bounds with a 2dp offset so it is never clipped by a
parent, and it uses `border.focus` which is verified against every surface it can
appear on.

---

## 7.6 Voice-specific states

Unique to this product. Each has a **non-motion, non-colour** equivalent
([05 §5.7](05-motion.md), [08](08-accessibility.md)).

| State | Visual | Text equivalent | Announced |
|---|---|---|---|
| **Idle** | Static orb, `status.telephony.subtle` | "Ready" | polite |
| **Listening** | Amplitude scale, `status.voice` | "Listening" | polite |
| **Thinking** | Rotating arc, `status.ai` | "Thinking" | polite |
| **Speaking** | Amplitude scale, `status.ai` | "Assistant speaking" | polite |
| **Recording** | Pulsing dot, `status.recording` | **"Recording"** | **assertive** |
| **Muted** | Struck-through mic, `content.tertiary` | "Muted" | assertive |
| **Error** | Shake → idle, `status.fraud` | "Screening failed" | assertive |

**Recording is announced assertively and is never suppressed** — not by reduced
motion, not by a quiet-mode setting, not by battery saver. It is a legal
disclosure ([ADR-0012](../adr/0012-privacy-dpdp-consent-retention.md)), and the
design system treats it as one.

---

## 7.7 Rules

1. **Empty states are tiered by frequency.** Illustration on first-run only.
2. **Every error says what the user can do**, and whether their phone still works.
3. **Red for failure, amber for degradation.** Never red for recoverable.
4. **No loading indicator under 100 ms.**
5. **Skeletons match final layout exactly.** No reflow on load.
6. **One shimmer clock per screen.**
7. **Disabled uses tokens, never blanket opacity.**
8. **Focus rings are never removed** and never clipped.
9. **Every voice state has a text equivalent.**
10. **Recording state is never suppressed.**
