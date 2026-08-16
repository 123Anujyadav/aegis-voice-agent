# 5 · Motion

Duration, easing, transitions, voice animation and haptics.

---

## 5.1 The constraint

**60 Hz, mid-range GPU, 16.6 ms per frame.**

That is the target device, not the fallback. Every animation in this system is
designed to hold 60 fps on hardware that struggles — because a dropped frame
reads as *broken software*, and broken software in a trust product is
disproportionately damaging.

Three consequences, all enforced:

| Rule | Why |
|---|---|
| **Transform and opacity only** | The only properties that avoid layout and paint. Animating size, padding, colour, shadow or blur re-runs layout or raster every frame |
| **Never animate shadow or blur** | Shadow rendering is expensive; blur doubly so, and it is unavailable below API 31 ([04 §4.6](04-space-shape-elevation.md)) |
| **Never crossfade a large surface** | Full-screen alpha forces an offscreen buffer. Use a shared-axis slide instead |

**Motion must communicate.** Every animation either shows *state changing* or
*where something came from*. Delight that costs a frame is not delight.

---

## 5.2 Duration

| Token | ms | Use |
|---|---:|---|
| `duration.instant` | 0 | Reduced-motion resolution |
| `duration.micro` | **100** | State change — hover, press, checkbox |
| `duration.short` | **150** | Small element enter/exit — tooltip, badge |
| `duration.medium` | **250** | **Default.** Sheets, dialogs, expand |
| `duration.long` | **400** | Page transition, complex reveal |
| `duration.slow` | **600** | Onboarding, deliberate emphasis. Rare |
| `duration.pulse` | **1400** | Voice pulse cycle (§5.6) |
| `duration.shimmer` | **1200** | Skeleton sweep ([07](07-states.md)) |

### Scale with distance, not importance

A 4dp shift uses `micro`. A full-screen transition uses `long`. An element
travelling further needs longer to feel like it has *mass*; the same duration
across different distances makes short moves feel sluggish and long moves feel
snapped.

**Exits are faster than entrances** — typically one step down. The user has
already decided; waiting for a dismissal to finish is friction.

---

## 5.3 Easing

| Token | Curve | Use |
|---|---|---|
| `easing.standard` | `cubic-bezier(0.2, 0, 0, 1)` | **Default.** On-screen movement |
| `easing.decelerate` | `cubic-bezier(0, 0, 0, 1)` | Entering — fast in, soft settle |
| `easing.accelerate` | `cubic-bezier(0.3, 0, 1, 1)` | Exiting — leaves decisively |
| `easing.emphasized` | `cubic-bezier(0.2, 0, 0, 1)` + spring | Hero moments, sheets |
| `easing.linear` | `linear` | **Progress and audio only** |

**Linear easing is reserved.** It is correct only for things that genuinely
progress at a constant rate: determinate progress bars, waveform scroll, the
recording timer. Everything physical uses a curve, because nothing in the
physical world starts and stops instantaneously.

### Springs for interruptible motion

Anything the user can grab mid-flight — sheets, drag, swipe-to-dismiss — uses a
spring, not a duration curve. A spring can be interrupted and redirected without
a discontinuity; a duration curve snaps.

| Token | Damping | Stiffness | Use |
|---|---:|---:|---|
| `spring.gentle` | 0.9 | 300 | Sheets, large surfaces |
| `spring.default` | 0.8 | 500 | Standard interactive |
| `spring.snappy` | 0.7 | 900 | Small controls, toggles |

**No spring overshoots more than 2%.** Bouncy motion reads as playful; this
product is not playful ([01](01-foundations.md)).

---

## 5.4 Page transitions

| Transition | Motion | Duration |
|---|---|---|
| Forward (push) | Shared X axis — new enters from right 30dp + fade | `medium` / `standard` |
| Back (pop) | Reverse, faster | `short` / `accelerate` |
| Tab switch | Shared X axis, no depth change | `short` |
| Modal / sheet | Y axis from bottom + scrim fade | `medium` / `spring.gentle` |
| Dialog | Scale 0.96 → 1.0 + fade | `short` / `decelerate` |
| Full-screen takeover | Shared Z — scale + fade | `long` / `emphasized` |

**Distance is 30dp, not full width.** A full-width slide costs more to render and
takes longer to read. A short offset paired with a fade communicates direction
just as clearly at a fraction of the cost.

---

## 5.5 Micro-interactions

| Interaction | Motion | Duration |
|---|---|---|
| Press | Scale 1.0 → **0.97**, tonal darken | `micro` |
| Release | Scale back, spring | `spring.snappy` |
| Hover (pointer only) | Tonal lighten | `micro` |
| Focus | Ring expands 0 → 2dp | `micro` |
| Toggle | Thumb slides, track colour | `short` / `spring.snappy` |
| Checkbox | Path draw 0 → 1 | `short` / `decelerate` |
| Badge appear | Scale 0.8 → 1.0 + fade | `short` / `decelerate` |
| List item enter | Fade + 8dp rise, **20ms stagger** | `short` |
| Error shake | ±4dp, 3 cycles | `short` / `linear` |
| Snackbar | Rise 100% + fade | `medium` / `decelerate` |

**Press uses scale, not elevation.** Animating elevation animates a shadow, which
is prohibited (§5.1). A 3% scale reduction reads as depression more reliably
anyway, and costs nothing.

**Stagger is capped at 5 items.** Beyond that the last item's delay exceeds
100 ms and the list feels slow to appear rather than pleasingly sequenced.

---

## 5.6 Voice and call animation

The most product-specific motion in the system, and the place where
[Principle 2 — never overclaim](01-foundations.md#principle-2--never-impersonate)
becomes a technical constraint.

### The voice orb

A non-anthropomorphic circle. It is deliberately **not** a face, not a character,
not a "personality" — it is a state indicator.

| State | Motion | Colour |
|---|---|---|
| **Idle** | Static | `status.telephony.subtle` |
| **Listening** | Amplitude-reactive scale, 1.0–1.08, driven by real mic RMS | `status.voice.fill` |
| **Thinking** | Slow rotation of an inner arc, `duration.pulse`, `linear` | `status.ai.fill` |
| **Speaking** | Amplitude-reactive, driven by **TTS output** amplitude | `status.ai.fill` |
| **Error** | Single shake, settle to idle | `status.fraud.fill` |

### The honesty rule

> **The orb animates from real amplitude data or it does not animate.**

A synthetic "listening" pulse rendered while the microphone is closed is a lie
told by the interface, and it is the exact failure that
[Principle 2](01-foundations.md) exists to prevent. If amplitude data is
unavailable, the orb shows a **static** state — never a fake one.

The same applies to `thinking`: it animates only while a model request is
genuinely in flight.

### Waveform

Live audio visualisation for the transcript and live-screening surfaces.

| Property | Value |
|---|---|
| Bars | 48 for full width, 24 for compact |
| Bar width | 2dp, gap 2dp |
| Height | Min 2dp, max 32dp, mapped from normalised RMS |
| Update rate | **Sampled to 30 Hz**, interpolated to display refresh |
| Easing | `linear` — audio is linear |
| Colour | `status.voice.fill` live · `content.tertiary` historical |

**Implementation constraints, non-negotiable:**

- Drawn on a single `Canvas` — **not** 48 composables. Forty-eight recomposing
  nodes at 60 fps will drop frames on the target device.
- Amplitude buffer is **pre-allocated and reused**. Zero allocation per frame.
- Sampled at 30 Hz and interpolated. Audio callbacks arrive faster than the
  display can show, and rendering every sample is wasted work.

### Call state

| State | Motion |
|---|---|
| Incoming | Ring pulse, `duration.pulse`, `easing.standard`, indefinite |
| Screening | Voice orb active + live waveform |
| Recording | `status.recording` dot, 1 Hz opacity 1.0 → 0.4 |
| Connected (takeover) | Static, `numeric.lg` timer, `tnum` |
| Ended | Fade to summary, `medium` |

**The recording indicator never stops while recording is active.** It is not
subject to reduced-motion suppression (§5.7) — it is a legal disclosure
(ADR-0012), not decoration. Under reduced motion it becomes a **static** dot with
a text label rather than disappearing.

---

## 5.7 Reduced motion

Android's signal is **`Settings.Global.ANIMATOR_DURATION_SCALE == 0`** — set by
*Settings → Accessibility → Remove animations*. It is **not**
`prefers-reduced-motion`; that is a web media query and does not exist here.
Reading the wrong signal is the most common way this gets missed.

When reduced motion is active:

| Normally | Becomes |
|---|---|
| All transitions | `duration.instant` — instant, no crossfade |
| Press scale | Tonal change only |
| Voice orb pulse | **Static** with a state label |
| Waveform | **Static** bars showing the last frame |
| Skeleton shimmer | Static block |
| Stagger | Removed |
| **Recording indicator** | **Static dot + text label — never removed** |

**Nothing may become unreachable or ambiguous.** Any state communicated by motion
must have a non-motion equivalent. This is tested: the reduced-motion variant is
a screenshot-test case, not an afterthought.

---

## 5.8 Haptics

Android haptics are tiered by API level and `minSdk` is 26. Every call
degrades gracefully.

| Token | API 30+ | API 29 | API 26–28 |
|---|---|---|---|
| `haptic.tick` | `Composition PRIMITIVE_TICK` | `EFFECT_TICK` | `performHapticFeedback(KEYBOARD_TAP)` |
| `haptic.click` | `PRIMITIVE_CLICK` | `EFFECT_CLICK` | `VIRTUAL_KEY` |
| `haptic.confirm` | Click + tick | `EFFECT_CLICK` | `CONFIRM` |
| `haptic.reject` | Double tick | `EFFECT_DOUBLE_CLICK` | `REJECT` |
| `haptic.heavy` | `PRIMITIVE_THUD` | `EFFECT_HEAVY_CLICK` | `LONG_PRESS` |

### When to use

| Event | Haptic |
|---|---|
| Button press | `tick` |
| Toggle | `click` |
| Accept / allow call | `confirm` |
| Block / decline call | `reject` |
| **Fraud detected** | `heavy` |
| Takeover engaged | `heavy` |
| Sheet snap | `tick` |
| Error | `reject` |
| Scroll, hover, typing | **none** |

**Haptics are for consequential actions.** Vibrating on every scroll and tap
trains the user to ignore them, so that the fraud alert — the one that matters —
lands as noise.

**Respect the system setting.** If haptic feedback is disabled, we emit nothing.
No exceptions, including the fraud alert.

---

## 5.9 Rules

1. **Transform and opacity only.** No layout-affecting animation.
2. **Never animate shadow, blur or colour** on the hot path.
3. **Duration scales with distance**, not importance.
4. **Exits are faster than entrances.**
5. **Springs for interruptible motion**, curves for everything else.
6. **The orb animates from real data or it stays static.** No fake liveness.
7. **Waveform is one Canvas, zero per-frame allocation, sampled at 30 Hz.**
8. **Reduced motion is `ANIMATOR_DURATION_SCALE`**, not a web media query.
9. **The recording indicator never disappears** — it degrades to static.
10. **Haptics only for consequential actions**, and never against the system
    setting.
