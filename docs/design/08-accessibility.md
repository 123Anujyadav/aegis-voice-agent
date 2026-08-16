# 8 · Accessibility

---

## 8.1 Why this section is not compliance

A call screener that transcribes what a caller said is, for a deaf or
hard-of-hearing user, **the difference between being unable to use a phone and
being able to**. For a blind user, an AI that identifies and filters unknown
callers removes a daily hostile-interaction surface.

These users are not an edge case to accommodate. For this specific product they
are among the **highest-value users**, and the transcript — which exists for
product reasons — is the accessibility feature.

Target: **WCAG 2.2 Level AA minimum, AAA for body text and all status colour.**

---

## 8.2 Contrast

### Requirements

| Content | Minimum | Our target |
|---|---:|---:|
| Body text | 4.5 : 1 | **7 : 1** |
| Large text (≥ 18sp, or 14sp bold) | 3 : 1 | **4.5 : 1** |
| Icons, essential graphics | 3 : 1 | **4.5 : 1** |
| Focus indicators | 3 : 1 | **4.5 : 1** |
| Borders on interactive elements | 3 : 1 | 3 : 1 |
| **Disabled text** | *exempt in WCAG* | **4.5 : 1 — we do not take the exemption** |

**We do not take the disabled-text exemption.** A control the user cannot read is
a control whose purpose they cannot determine, which is a usability failure even
where it is a compliance pass. This is why disabled states use explicit tokens
rather than blanket opacity ([07 §7.5](07-states.md)).

### Verified, not assumed

Contrast is computed in CI from `design/tokens/*.json` for **every** semantic
pair across **all three themes**. A token change that drops any pair below its
threshold **fails the build**.

This is why the `600`/`300` rule in [02 §2.1](02-color.md) exists — several `500`
steps fail as text and pass as fills, and a rule is more reliable than vigilance.

---

## 8.3 Colour independence

> **Remove all colour. Does every state remain distinguishable?**

WCAG 1.4.1. In this product it is load-bearing, because the ten semantic states
in [02](02-color.md) include four warm hues that are genuinely hard to
distinguish — for anyone, and especially for the ~8% of men with red-green colour
vision deficiency.

**Every status carries three signals:**

| Status | Colour | Icon | Label |
|---|---|---|---|
| Success | `status.success` | check | "Allowed" |
| Warning | `status.warning` | triangle | "Attention" |
| Spam | `status.spam` | crossed megaphone | "Spam" |
| Fraud | `status.fraud` | broken shield | "Possible fraud" |
| Emergency | `status.emergency` | filled circle | "Urgent" |
| AI | `status.ai` | orb + arc | "Assistant" |
| Voice | `status.voice` | waveform | "Live" |
| Recording | `status.recording` | filled dot | **"Recording"** |
| Premium | `status.premium` | faceted mark | "Premium" |

Plus **shape**: premium is the only `radius.full` gradient chip
([02 §2.3](02-color.md)).

### Tested per type

| Type | Prevalence | Risk in our palette |
|---|---|---|
| Deuteranopia / -anomaly | ~6% of men | **Fraud ↔ success confusion** |
| Protanopia / -anomaly | ~2% of men | Fraud appears darker, may read as neutral |
| Tritanopia | Rare | Voice ↔ telephony confusion |
| Achromatopsia | Very rare | All ten states rely on icon + label |

Simulation for all four is a **screenshot-test variant** on every status
component.

---

## 8.4 Touch targets and motor accessibility

| Requirement | Value |
|---|---:|
| Minimum touch target | **48 × 48dp** |
| Minimum spacing between targets | 8dp |
| Primary action reachable one-handed | Bottom 60% of screen |
| Destructive action requires confirmation | Yes — except time-critical |

**Visual size may be smaller than the target.** A 24dp icon button has a 48dp
touch area. This is invisible and essential.

**Time-critical actions do not get confirmation dialogs.** "Take this call" is
time-critical; a confirmation step means the call is missed. Instead: a large,
unambiguous target and an undo path where one is possible
([01 Principle 4](01-foundations.md)).

**No action requires a gesture as the only means of access.** Swipe-to-dismiss is
always paired with a button. Long-press always has a visible alternative.

---

## 8.5 Screen readers

TalkBack is the target. Rules that apply everywhere:

| Rule | Detail |
|---|---|
| Every interactive element has an accessible name | Never the raw icon name |
| Decorative elements are hidden | `contentDescription = null` |
| Names describe the **action**, not the appearance | "Block caller", not "red button" |
| State is announced | `Modifier.semantics { selected = … }`, not baked into the name |
| Related content is merged | `mergeDescendants` on cards — 11 separate nodes per row is unusable |
| Reading order follows visual order | Verified, not assumed |
| Live regions used sparingly | See below |
| Headings are marked | `heading()` semantics for navigation |

### Live regions

The transcript updates continuously during a screened call. Announcing every
partial would be unusable.

| Content | Politeness |
|---|---|
| Transcript turn **completed** | `polite` |
| Transcript partial / interim | **not announced** |
| Voice state change | `polite` |
| Recording started / stopped | **`assertive`** |
| Fraud detected | **`assertive`** |
| Call ended | `assertive` |

**Only two things interrupt: recording state and danger.** Everything else waits.

### The caller card

The single most-read component. Its accessible output is composed deliberately,
not derived from its children:

```
"Unknown caller, +91 98 triple 7 space 1 2 3 4 5.
 Screened 2 minutes ago.
 Flagged as possible fraud, high confidence.
 Double tap to open transcript."
```

Phone numbers are read **digit by digit with grouping**, never as a single large
number ("nine one nine eight seven seven seven…" is unparseable).

---

## 8.6 Text scaling

Covered in [03 §3.6](03-typography.md). The accessibility requirements:

- **All text scales to 200%** without loss of content or function (WCAG 1.4.4)
- **No fixed-height text containers**
- **Reflow to single column** below 320dp effective width (WCAG 1.4.10)
- **User overrides honoured**: letter spacing 0.12em, word spacing 0.16em, line
  height 1.5× (WCAG 1.4.12) — our defaults already exceed the line-height
  requirement
- **200% is a screenshot-test variant** on every component

---

## 8.7 Voice accessibility

This product is *about* voice, which creates obligations in both directions.

### For users who cannot hear

- **The transcript is the primary surface**, not a secondary one. Every screened
  call has a full transcript, available without playing audio.
- **Every audio cue has a visual equivalent.** Ring, screening progress,
  recording state, call end.
- **No information exists only in audio.** If the assistant says it, the
  transcript shows it.
- Transcripts get the most generous typography in the system
  ([03 §3.7](03-typography.md)).

### For users who cannot see

- **Every visual state has a text equivalent** ([07 §7.6](07-states.md)) —
  the voice orb, the waveform, the fraud badge.
- **The waveform is decorative and is hidden from screen readers.** It carries no
  information the transcript does not.
- **Voice-state changes are announced** politely; recording assertively.

### For users of voice control

- Every interactive element has a **speakable, unique** accessible name.
- No name collides within a screen — "Block" and "Block caller" on the same
  screen is ambiguous to Voice Access.
- Names avoid characters that are hard to speak: no "+", no "•", no emoji.

---

## 8.8 Reduced motion and cognitive load

- Reduced motion is `ANIMATOR_DURATION_SCALE == 0` ([05 §5.7](05-motion.md))
- **No content is conveyed by motion alone**
- **Nothing auto-advances.** No carousels, no timed dismissal of anything the
  user must read
- **No flashing above 3 Hz** (WCAG 2.3.1). The recording pulse is 1 Hz
- **Session timeouts warn** at 20 seconds with an extend option (WCAG 2.2.1)
- **Copy is plain.** Reading level is a design constraint
  ([01 §1.6](01-foundations.md))

---

## 8.9 Testing

Accessibility is **gated in CI**, not reviewed at the end.

| Check | Where | Blocking |
|---|---|---|
| Token contrast, all pairs, all 3 themes | CI, from JSON | ✅ |
| Touch target ≥ 48dp | Compose lint | ✅ |
| Missing `contentDescription` | Android Lint | ✅ |
| Screenshot at 200% text scale | Roborazzi variant | ✅ |
| Screenshot in high-contrast theme | Roborazzi variant | ✅ |
| Colour-blind simulation, 4 types | Roborazzi variant | ✅ |
| Reduced-motion variant | Roborazzi variant | ✅ |
| TalkBack traversal | Manual, per release | ✅ |
| Switch Access | Manual, per release | ✅ |

**Every component ships with these variants or it does not ship.** This is
enforced by the component checklist in [11-contributing.md](11-contributing.md).

---

## 8.10 Rules

1. **AA is the floor. AAA for body text and status colour.**
2. **Contrast is computed in CI**, never eyeballed.
3. **Colour + icon + label. Always all three.**
4. **48dp targets**, regardless of visual size.
5. **Disabled text stays readable** — we decline the WCAG exemption.
6. **Only recording and danger interrupt** a screen-reader user.
7. **Phone numbers are read digit by digit.**
8. **No information exists only in audio, or only in motion.**
9. **200%, high-contrast, colour-blind and reduced-motion are test variants**,
   not manual checks.
