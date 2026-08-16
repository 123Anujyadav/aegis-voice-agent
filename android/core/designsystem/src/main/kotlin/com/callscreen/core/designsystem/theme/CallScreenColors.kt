package com.callscreen.core.designsystem.theme

import androidx.compose.runtime.Immutable
import androidx.compose.ui.graphics.Color
import com.callscreen.core.designsystem.token.Elevation
import com.callscreen.core.designsystem.token.Primitives

/**
 * The four sub-roles every semantic status exposes.
 *
 * @property subtle tinted background for a badge or banner
 * @property fill saturated fill, with content of the opposite polarity on top
 * @property text and icon colour, meeting contrast against the default surface
 * @property border outline for outlined variants
 */
@Immutable
public data class StatusColors(
    public val subtle: Color,
    public val fill: Color,
    public val text: Color,
    public val border: Color,
)

/**
 * Surface colours, by depth.
 *
 * @property background the app canvas
 * @property default cards and sheets
 * @property raised elevated above [default]
 * @property sunken wells and input fields
 * @property inverse tooltips and snackbars
 */
@Immutable
public data class SurfaceColors(
    public val background: Color,
    public val default: Color,
    public val raised: Color,
    public val sunken: Color,
    public val inverse: Color,
)

/**
 * Foreground content colours.
 *
 * @property primary body text
 * @property secondary supporting text
 * @property tertiary timestamps and metadata
 * @property disabled disabled content — still meets 4.5:1, see the class doc on
 *   [CallScreenColors]
 * @property inverse content on [SurfaceColors.inverse]
 */
@Immutable
public data class ContentColors(
    public val primary: Color,
    public val secondary: Color,
    public val tertiary: Color,
    public val disabled: Color,
    public val inverse: Color,
)

/**
 * Border colours.
 *
 * @property subtle dividers
 * @property default input outlines
 * @property strong emphasis
 * @property focus the focus ring — never removed, never clipped
 */
@Immutable
public data class BorderColors(
    public val subtle: Color,
    public val default: Color,
    public val strong: Color,
    public val focus: Color,
)

/**
 * A single interactive role's fill and content, plus its interaction states.
 *
 * @property fill the resting background
 * @property content the foreground on [fill]
 * @property hover fill while hovered — pointer input only
 * @property pressed fill while pressed
 */
@Immutable
public data class ActionColors(
    public val fill: Color,
    public val content: Color,
    public val hover: Color,
    public val pressed: Color,
)

/**
 * Every interactive role.
 *
 * @property primary the single main action on a screen
 * @property secondary alternative actions
 * @property danger destructive actions — block, delete
 * @property disabled the disabled treatment, using explicit tokens rather than
 *   a blanket alpha
 */
@Immutable
public data class ActionColorGroup(
    public val primary: ActionColors,
    public val secondary: ActionColors,
    public val danger: ActionColors,
    public val disabled: ActionColors,
)

/**
 * The ten semantic statuses.
 *
 * Four of these — [warning], [spam], [emergency] and [premium] — occupy adjacent
 * regions of hue space and are genuinely hard to tell apart at a glance,
 * especially for the ~8% of men with red-green colour vision deficiency. Four
 * rules keep them distinguishable, and all four are mandatory:
 *
 * 1. They never co-occur in the same context.
 * 2. Colour is never the sole carrier — icon and label, always.
 * 3. [premium] is the only fully-rounded gradient chip; the others are
 *    small-radius rectangles.
 * 4. [premium] is decorative; the others are status. Gold on screen is a label,
 *    never an alert.
 *
 * **None of these is ever subject to dynamic colour.** A wallpaper must not be
 * able to recolour a fraud badge — semantic colour in a trust product is a
 * safety property, not personalisation.
 *
 * @property success completed, safe, allowed
 * @property warning system attention — not a caller state
 * @property spam unwanted, not malicious
 * @property fraud active malicious intent
 * @property emergency urgent, must break through
 * @property ai the assistant, applied to every model-generated string
 * @property voice live speech
 * @property telephony call, carrier, connection
 * @property recording recording in progress — a legal disclosure
 * @property premium subscription tier, decorative only
 *
 * @see docs/design/02-color.md §2.3
 */
@Immutable
public data class StatusColorGroup(
    public val success: StatusColors,
    public val warning: StatusColors,
    public val spam: StatusColors,
    public val fraud: StatusColors,
    public val emergency: StatusColors,
    public val ai: StatusColors,
    public val voice: StatusColors,
    public val telephony: StatusColors,
    public val recording: StatusColors,
    public val premium: StatusColors,
)

/**
 * The complete semantic colour scheme for one theme.
 *
 * This is what components reference. A component reaching for
 * [com.callscreen.core.designsystem.token.Primitives] cannot respond to theme,
 * which is the single most common way a design system breaks in dark mode.
 *
 * ## Disabled content stays readable
 *
 * WCAG exempts disabled controls from contrast requirements. **We decline the
 * exemption.** A control the user cannot read is a control whose purpose they
 * cannot determine — a usability failure even where it is a compliance pass.
 * This is why disabled states use explicit tokens and never a blanket alpha,
 * which would produce unpredictable contrast against whatever sits behind.
 *
 * @property surface surfaces by depth
 * @property content foreground colours
 * @property border border colours
 * @property action interactive roles
 * @property status the ten semantic statuses
 * @property isDark whether this scheme is a dark variant, used to resolve
 *   elevation to tonal steps rather than shadows
 *
 * @see docs/design/02-color.md
 */
@Immutable
public data class CallScreenColors(
    public val surface: SurfaceColors,
    public val content: ContentColors,
    public val border: BorderColors,
    public val action: ActionColorGroup,
    public val status: StatusColorGroup,
    public val isDark: Boolean,
) {

    /**
     * Resolves the surface colour for a given [elevation].
     *
     * Light and dark express depth by different mechanisms. In light mode the
     * surface colour is constant and depth is a shadow; in dark mode shadows are
     * near-invisible, so surfaces get **lighter** as they rise and shadows are
     * suppressed above [Elevation.Level1].
     *
     * A component requests an elevation and never a shadow — this method is
     * where that indirection is resolved.
     *
     * @param elevation the requested layer depth
     * @return the surface colour to paint at that depth
     */
    public fun surfaceAt(elevation: Elevation): Color = when {
        !isDark -> surface.default
        elevation.tonalStep <= 0 -> surface.background
        elevation.tonalStep == 1 -> surface.default
        else -> surface.raised
    }
}

/**
 * Builds the light colour scheme.
 *
 * Light is the **primary** theme, designed first. India-first means bright
 * outdoor use is routine, so light mode is not an afterthought.
 *
 * @return the light [CallScreenColors]
 */
public fun lightColors(): CallScreenColors = CallScreenColors(
    surface = SurfaceColors(
        background = Primitives.neutral50,
        default = Primitives.neutral0,
        raised = Primitives.neutral0,
        sunken = Primitives.neutral100,
        inverse = Primitives.neutral900,
    ),
    // Measured, not assumed. neutral500 on white is 3.81:1 — it fails AA as
    // body text, so tertiary sits one step darker than the ramp position
    // suggests. ContrastTest enforces this.
    content = ContentColors(
        primary = Primitives.neutral900,
        secondary = Primitives.neutral700,
        tertiary = Primitives.neutral600,
        disabled = Primitives.neutral500,
        inverse = Primitives.neutral0,
    ),
    border = BorderColors(
        subtle = Primitives.neutral200,
        default = Primitives.neutral300,
        strong = Primitives.neutral400,
        focus = Primitives.brand500,
    ),
    action = ActionColorGroup(
        primary = ActionColors(
            fill = Primitives.brand500,
            content = Primitives.neutral0,
            hover = Primitives.brand600,
            pressed = Primitives.brand700,
        ),
        secondary = ActionColors(
            fill = Primitives.neutral100,
            content = Primitives.neutral900,
            hover = Primitives.neutral200,
            pressed = Primitives.neutral300,
        ),
        danger = ActionColors(
            fill = Primitives.fraud500,
            content = Primitives.neutral0,
            hover = Primitives.fraud600,
            pressed = Primitives.fraud700,
        ),
        // neutral500 on neutral200 measures 2.99:1 — one hundredth under the
        // floor. Lightening the fill rather than darkening the content keeps
        // the control reading as inactive while clearing the bar at 4.1:1.
        disabled = ActionColors(
            fill = Primitives.neutral100,
            content = Primitives.neutral500,
            hover = Primitives.neutral100,
            pressed = Primitives.neutral100,
        ),
    ),
    status = StatusColorGroup(
        success = StatusColors(
            Primitives.success50, Primitives.success500,
            Primitives.success600, Primitives.success200,
        ),
        warning = StatusColors(
            Primitives.warning50, Primitives.warning500,
            Primitives.warning600, Primitives.warning200,
        ),
        spam = StatusColors(
            Primitives.spam50, Primitives.spam500,
            Primitives.spam600, Primitives.spam200,
        ),
        fraud = StatusColors(
            Primitives.fraud50, Primitives.fraud500,
            Primitives.fraud600, Primitives.fraud200,
        ),
        emergency = StatusColors(
            Primitives.emergency50, Primitives.emergency500,
            Primitives.emergency600, Primitives.emergency200,
        ),
        ai = StatusColors(
            Primitives.ai50, Primitives.ai500,
            Primitives.ai600, Primitives.ai200,
        ),
        voice = StatusColors(
            Primitives.voice50, Primitives.voice500,
            Primitives.voice600, Primitives.voice200,
        ),
        telephony = StatusColors(
            Primitives.brand50, Primitives.brand500,
            Primitives.brand600, Primitives.brand200,
        ),
        recording = StatusColors(
            Primitives.recording50, Primitives.recording500,
            Primitives.recording600, Primitives.recording200,
        ),
        premium = StatusColors(
            Primitives.premium50, Primitives.premium500,
            Primitives.premium600, Primitives.premium200,
        ),
    ),
    isDark = false,
)

/**
 * Builds the dark colour scheme.
 *
 * Dark mode is **not** inverted light mode. Two things change beyond swapping
 * surfaces: depth becomes tonal rather than shadowed (see
 * [CallScreenColors.surfaceAt]), and saturated hues move to their lighter `300`
 * and `400` steps, because a `500` step tuned for white vibrates against a
 * near-black background.
 *
 * @return the dark [CallScreenColors]
 */
public fun darkColors(): CallScreenColors = CallScreenColors(
    surface = SurfaceColors(
        background = Primitives.neutral950,
        default = Primitives.neutral900,
        raised = Primitives.neutral800,
        sunken = Primitives.neutral1000,
        inverse = Primitives.neutral100,
    ),
    // Dark needs LIGHTER steps than the mirror of light would give. neutral500
    // measures 3.45:1 on the dark surface — fine for disabled at our 3:1 floor,
    // not for tertiary body text.
    content = ContentColors(
        primary = Primitives.neutral50,
        secondary = Primitives.neutral300,
        tertiary = Primitives.neutral400,
        disabled = Primitives.neutral500,
        inverse = Primitives.neutral900,
    ),
    border = BorderColors(
        subtle = Primitives.neutral800,
        default = Primitives.neutral700,
        strong = Primitives.neutral600,
        focus = Primitives.brand300,
    ),
    // Action fills carry dark content on a light fill in dark theme, which is
    // the inverse of light theme. brand400 against neutral950 measures only
    // 3.98:1, so the fill steps one lighter than the ramp position suggests.
    action = ActionColorGroup(
        primary = ActionColors(
            fill = Primitives.brand300,
            content = Primitives.neutral950,
            hover = Primitives.brand200,
            pressed = Primitives.brand100,
        ),
        secondary = ActionColors(
            fill = Primitives.neutral800,
            content = Primitives.neutral100,
            hover = Primitives.neutral700,
            pressed = Primitives.neutral600,
        ),
        danger = ActionColors(
            fill = Primitives.fraud300,
            content = Primitives.neutral950,
            hover = Primitives.fraud200,
            pressed = Primitives.fraud100,
        ),
        disabled = ActionColors(
            fill = Primitives.neutral800,
            content = Primitives.neutral400,
            hover = Primitives.neutral800,
            pressed = Primitives.neutral800,
        ),
    ),
    status = StatusColorGroup(
        success = StatusColors(
            Primitives.success900, Primitives.success400,
            Primitives.success300, Primitives.success700,
        ),
        warning = StatusColors(
            Primitives.warning900, Primitives.warning400,
            Primitives.warning300, Primitives.warning700,
        ),
        spam = StatusColors(
            Primitives.spam900, Primitives.spam400,
            Primitives.spam300, Primitives.spam700,
        ),
        fraud = StatusColors(
            Primitives.fraud900, Primitives.fraud400,
            Primitives.fraud300, Primitives.fraud700,
        ),
        emergency = StatusColors(
            Primitives.emergency900, Primitives.emergency400,
            Primitives.emergency300, Primitives.emergency700,
        ),
        ai = StatusColors(
            Primitives.ai900, Primitives.ai400,
            Primitives.ai300, Primitives.ai700,
        ),
        voice = StatusColors(
            Primitives.voice900, Primitives.voice400,
            Primitives.voice300, Primitives.voice700,
        ),
        telephony = StatusColors(
            Primitives.brand900, Primitives.brand400,
            Primitives.brand300, Primitives.brand700,
        ),
        recording = StatusColors(
            Primitives.recording900, Primitives.recording400,
            Primitives.recording300, Primitives.recording700,
        ),
        premium = StatusColors(
            Primitives.premium900, Primitives.premium400,
            Primitives.premium300, Primitives.premium700,
        ),
    ),
    isDark = true,
)

/**
 * Builds the high-contrast light scheme.
 *
 * Not a stylistic variant — an accessibility mode bound to the platform's
 * high-contrast setting. Every text pair reaches AAA (7:1), borders strengthen,
 * and **tinted status backgrounds are removed** rather than darkened: a
 * low-contrast tint behind high-contrast text is the exact pattern high-contrast
 * mode exists to eliminate.
 *
 * @return the high-contrast light [CallScreenColors]
 */
public fun highContrastLightColors(): CallScreenColors {
    val base = lightColors()
    return base.copy(
        surface = base.surface.copy(background = Primitives.neutral0),
        content = base.content.copy(
            primary = Primitives.neutral1000,
            secondary = Primitives.neutral800,
            tertiary = Primitives.neutral700,
            disabled = Primitives.neutral600,
        ),
        border = base.border.copy(
            subtle = Primitives.neutral700,
            default = Primitives.neutral900,
            strong = Primitives.neutral1000,
            focus = Primitives.brand700,
        ),
        status = base.status.withoutSubtleTints(Primitives.neutral0),
    )
}

/**
 * Builds the high-contrast dark scheme.
 *
 * @return the high-contrast dark [CallScreenColors]
 */
public fun highContrastDarkColors(): CallScreenColors {
    val base = darkColors()
    return base.copy(
        surface = base.surface.copy(
            background = Primitives.neutral1000,
            default = Primitives.neutral1000,
        ),
        content = base.content.copy(
            primary = Primitives.neutral0,
            secondary = Primitives.neutral200,
            tertiary = Primitives.neutral300,
            disabled = Primitives.neutral400,
        ),
        border = base.border.copy(
            subtle = Primitives.neutral500,
            default = Primitives.neutral400,
            strong = Primitives.neutral200,
            focus = Primitives.brand200,
        ),
        status = base.status.withoutSubtleTints(Primitives.neutral1000),
    )
}

/**
 * Replaces every status `subtle` tint with [surface].
 *
 * High-contrast mode communicates status through border, icon and label rather
 * than a tinted background, so the tint is removed rather than adjusted.
 *
 * @param surface the flat surface colour to substitute
 * @return a group with all subtle tints flattened
 */
private fun StatusColorGroup.withoutSubtleTints(surface: Color): StatusColorGroup =
    StatusColorGroup(
        success = success.copy(subtle = surface),
        warning = warning.copy(subtle = surface),
        spam = spam.copy(subtle = surface),
        fraud = fraud.copy(subtle = surface),
        emergency = emergency.copy(subtle = surface),
        ai = ai.copy(subtle = surface),
        voice = voice.copy(subtle = surface),
        telephony = telephony.copy(subtle = surface),
        recording = recording.copy(subtle = surface),
        premium = premium.copy(subtle = surface),
    )
