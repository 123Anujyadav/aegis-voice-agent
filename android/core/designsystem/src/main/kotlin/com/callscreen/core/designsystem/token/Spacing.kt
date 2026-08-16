package com.callscreen.core.designsystem.token

import androidx.compose.runtime.Immutable
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp

/**
 * The spacing scale. Base unit 4dp.
 *
 * Every gap, pad and margin in the product is one of these ten values. A raw
 * `dp` literal in a composable is a defect — it is a decision made in one place
 * that cannot be changed anywhere.
 *
 * ## The 4 / 8 discipline
 *
 * Values below [lg] step by 4; values above step by 8 or 16. Small spacing needs
 * fine control, large spacing does not, and offering `40.dp` alongside `48.dp`
 * invites decisions nobody should be making.
 *
 * ## Grouping through space
 *
 * Related things are closer, unrelated things further apart. Proximity does the
 * grouping so borders do not have to. A card that needs a divider between two
 * blocks of content usually needs more space instead.
 *
 * @see docs/design/04-space-shape-elevation.md
 */
@Immutable
public object Spacing {

    /** Explicit zero. Use in preference to omitting a parameter, for clarity. */
    public val none: Dp = 0.dp

    /**
     * 2dp. The only sub-4 value, and it exists reluctantly.
     *
     * Permitted **only** for optical icon-to-label alignment inside dense chips,
     * where 4dp reads as detached. Using it anywhere else is a defect.
     */
    public val hairline: Dp = 2.dp

    /** 4dp. Tight internal spacing — icon to text within a badge. */
    public val xs: Dp = 4.dp

    /** 8dp. Related elements — a label and its input. */
    public val sm: Dp = 8.dp

    /** 12dp. Dense internal card padding, list-item vertical padding. */
    public val md: Dp = 12.dp

    /** 16dp. **The default.** Card padding and screen horizontal margin. */
    public val lg: Dp = 16.dp

    /** 24dp. Between groups within a section. */
    public val xl: Dp = 24.dp

    /** 32dp. Between sections. */
    public val xxl: Dp = 32.dp

    /** 48dp. Major separation. Also the minimum touch target dimension. */
    public val xxxl: Dp = 48.dp

    /** 64dp. Screen-level separation, empty-state padding. */
    public val huge: Dp = 64.dp

    /**
     * The minimum interactive target, 48 × 48dp.
     *
     * Visual size may be smaller — a 24dp icon button still has a 48dp touch
     * area. This is invisible to sighted users and essential for motor
     * accessibility, and it is non-negotiable.
     *
     * @see docs/design/08-accessibility.md
     */
    public val minTouchTarget: Dp = 48.dp
}
