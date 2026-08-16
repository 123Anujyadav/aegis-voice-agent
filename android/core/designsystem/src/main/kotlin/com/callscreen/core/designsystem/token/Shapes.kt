package com.callscreen.core.designsystem.token

import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.runtime.Immutable
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp

/**
 * Corner radius tokens and the shapes built from them.
 *
 * The geometry is rounded but not soft. [md] (12dp) is the default for
 * interactive surfaces; [lg] (16dp) for containers.
 *
 * ## Nested radius
 *
 * An inner radius inside an outer one must be **outer minus the padding**, or
 * the corners look wrong. Nesting equal radii makes the inner element appear to
 * bulge — the classic mistake:
 *
 * ```
 * outer lg (16) − sm padding (8) = inner sm (8)   ✓
 * outer lg (16) with inner lg (16)                ✗
 * ```
 *
 * ## [full] is semantic, not decorative
 *
 * Reserved for shapes that are *conceptually* circular: avatars, the voice orb,
 * status dots, and pills whose content is a single short label. Applying it to a
 * rectangle to look modern is a defect — it makes the shape language meaningless
 * and destroys the one signal that differentiates the premium chip from the warm
 * status hues.
 *
 * @see docs/design/04-space-shape-elevation.md
 * @see docs/design/02-color.md §2.3 rule R3
 */
@Immutable
public object Radius {

    /** 0dp. Full-bleed media, table cells, dividers. */
    public val none: Dp = 0.dp

    /** 4dp. Tiny chips and tags. */
    public val xs: Dp = 4.dp

    /** 8dp. Badges, inputs, small buttons. */
    public val sm: Dp = 8.dp

    /** 12dp. **The default.** Buttons, cards, list rows. */
    public val md: Dp = 12.dp

    /** 16dp. Containers and large cards. */
    public val lg: Dp = 16.dp

    /** 20dp. Bottom sheets and dialogs. */
    public val xl: Dp = 20.dp

    /** 28dp. Hero surfaces. Rare. */
    public val xxl: Dp = 28.dp

    /** Fully rounded. Conceptually circular shapes only — see the class doc. */
    public val full: Dp = 9999.dp
}

/**
 * Ready-made [RoundedCornerShape]s for each [Radius] step.
 *
 * Prefer these over constructing a shape inline, so that a radius change
 * propagates from one place.
 */
@Immutable
public object Shapes {

    /** Square corners. */
    public val none: RoundedCornerShape = RoundedCornerShape(Radius.none)

    /** 4dp corners. */
    public val xs: RoundedCornerShape = RoundedCornerShape(Radius.xs)

    /** 8dp corners. */
    public val sm: RoundedCornerShape = RoundedCornerShape(Radius.sm)

    /** 12dp corners. The default for interactive surfaces. */
    public val md: RoundedCornerShape = RoundedCornerShape(Radius.md)

    /** 16dp corners. Containers. */
    public val lg: RoundedCornerShape = RoundedCornerShape(Radius.lg)

    /** 20dp corners. Dialogs. */
    public val xl: RoundedCornerShape = RoundedCornerShape(Radius.xl)

    /** 28dp corners. Hero surfaces. */
    public val xxl: RoundedCornerShape = RoundedCornerShape(Radius.xxl)

    /** Fully rounded — pills, avatars, the voice orb. */
    public val full: RoundedCornerShape = RoundedCornerShape(Radius.full)

    /**
     * Top corners rounded at [Radius.xl], bottom square.
     *
     * The bottom-sheet shape. Its bottom corners sit off-screen, so rounding
     * them would be invisible and would cost a clip.
     */
    public val bottomSheet: RoundedCornerShape =
        RoundedCornerShape(topStart = Radius.xl, topEnd = Radius.xl)
}

/**
 * Stroke widths.
 *
 * Borders are preferred over shadows: cheaper to render, identical in both
 * themes without adaptation, and they read as more precise. Reach for a border
 * before reaching for elevation.
 */
@Immutable
public object Stroke {

    /**
     * 0.5dp. Renders as a true hairline only at ≥ 2× density; below that it
     * snaps to 1dp. Never rely on it for a load-bearing boundary.
     */
    public val hairline: Dp = 0.5.dp

    /** 1dp. **The default** — borders, dividers, input outlines. */
    public val thin: Dp = 1.dp

    /** 1.5dp. Selected states and emphasis. */
    public val medium: Dp = 1.5.dp

    /** 2dp. Focus rings and high-contrast borders. */
    public val thick: Dp = 2.dp

    /** 3dp. High-contrast focus rings. */
    public val heavy: Dp = 3.dp
}

/**
 * Icon sizes, each paired with the stroke weight it is drawn at.
 *
 * Stroke weight is **redrawn per size, never scaled**. A 24dp icon scaled to
 * 16dp has a 1.33dp stroke that renders soft and inconsistent against text, and
 * it is the difference between an icon set that looks bought and one that looks
 * made.
 *
 * @property size the rendered dimension, square
 * @property stroke the stroke weight the asset at this size is drawn with
 */
@Immutable
public enum class IconSize(public val size: Dp, public val stroke: Dp) {

    /** 16dp. Inline with `body.sm`, dense badges. */
    ExtraSmall(size = 16.dp, stroke = 1.5.dp),

    /** 20dp. Inline with `body.md`, list affordances. */
    Small(size = 20.dp, stroke = 1.5.dp),

    /** 24dp. **The default** — buttons, navigation, list leading icons. */
    Medium(size = 24.dp, stroke = 2.dp),

    /** 32dp. Empty states and feature callouts. */
    Large(size = 32.dp, stroke = 2.dp),

    /** 48dp. Hero and onboarding. */
    ExtraLarge(size = 48.dp, stroke = 2.5.dp),
}
