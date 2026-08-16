package com.callscreen.core.designsystem.token

import androidx.compose.runtime.Immutable
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp

/**
 * Elevation levels.
 *
 * Elevation communicates **layering**, never importance. Something is not more
 * important because it is raised; it is merely in front.
 *
 * ## Light and dark express depth differently
 *
 * This is the single most-missed detail in dark-mode design. A black shadow on a
 * near-black surface communicates nothing, so:
 *
 * - **Light mode:** depth is a **shadow**. Surface colour stays constant.
 * - **Dark mode:** depth is **tonal**. Surfaces get lighter as they rise, and
 *   shadows are suppressed entirely above [Level1].
 *
 * A component therefore requests an [Elevation] and never a shadow. The theme
 * resolves which mechanism applies — see
 * `com.callscreen.core.designsystem.theme.CallScreenColors.surfaceAt`.
 *
 * ## Shadows are never animated
 *
 * Shadow rendering is expensive and animating it drops frames on the target
 * mid-range device. Press states use a **scale** change instead.
 *
 * @property dp the nominal elevation, used for shadow rendering in light theme
 * @property shadow the shadow token applied in light theme
 * @property tonalStep how many surface steps to lighten by in dark theme
 *
 * @see docs/design/04-space-shape-elevation.md
 */
@Immutable
public enum class Elevation(
    public val dp: Dp,
    public val shadow: ShadowToken,
    public val tonalStep: Int,
) {

    /** Flat. The app background. */
    Level0(dp = 0.dp, shadow = ShadowToken.None, tonalStep = 0),

    /** Cards at rest. The only level where dark mode still draws a shadow. */
    Level1(dp = 1.dp, shadow = ShadowToken.ExtraSmall, tonalStep = 1),

    /** Raised cards, pressed states. */
    Level2(dp = 3.dp, shadow = ShadowToken.Small, tonalStep = 2),

    /** Sticky headers, floating action buttons. */
    Level3(dp = 6.dp, shadow = ShadowToken.Medium, tonalStep = 2),

    /** Bottom sheets, menus, snackbars. */
    Level4(dp = 8.dp, shadow = ShadowToken.Large, tonalStep = 2),

    /** Dialogs and modals. The top of the stack. */
    Level5(dp = 16.dp, shadow = ShadowToken.ExtraLarge, tonalStep = 2),
}

/**
 * Shadow definitions, applied in light theme only.
 *
 * Two deliberate properties:
 *
 * **Shadow colour is not black.** It is `neutral.900` at the stated opacity. A
 * cool-tinted shadow reads as light falling on a surface; pure black reads as a
 * hole punched through it.
 *
 * **Y-offset always exceeds zero.** Light comes from above. A symmetric shadow
 * is a glow, and glows belong to a different design language than this one.
 *
 * Negative [spread] on the larger tokens keeps the shadow tighter than its blur,
 * preventing the muddy halo that large soft shadows otherwise produce.
 *
 * @property offsetY vertical offset
 * @property blur blur radius
 * @property spread spread radius, negative on larger tokens
 * @property alpha opacity applied to the tinted shadow colour
 */
@Immutable
public enum class ShadowToken(
    public val offsetY: Dp,
    public val blur: Dp,
    public val spread: Dp,
    public val alpha: Float,
) {

    /** No shadow. */
    None(offsetY = 0.dp, blur = 0.dp, spread = 0.dp, alpha = 0f),

    /** Barely-there separation for resting cards. */
    ExtraSmall(offsetY = 1.dp, blur = 2.dp, spread = 0.dp, alpha = 0.04f),

    /** Light separation. */
    Small(offsetY = 2.dp, blur = 4.dp, spread = 0.dp, alpha = 0.06f),

    /** Clearly floating. */
    Medium(offsetY = 4.dp, blur = 8.dp, spread = (-1).dp, alpha = 0.08f),

    /** Overlay surfaces. */
    Large(offsetY = 8.dp, blur = 16.dp, spread = (-2).dp, alpha = 0.10f),

    /** Modal surfaces. */
    ExtraLarge(offsetY = 16.dp, blur = 32.dp, spread = (-4).dp, alpha = 0.12f),
}
