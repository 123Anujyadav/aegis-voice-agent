package com.callscreen.core.designsystem.theme

import androidx.compose.ui.graphics.Color
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.assertAll
import kotlin.math.max
import kotlin.math.min
import kotlin.math.pow

/**
 * WCAG contrast verification for every semantic colour pair, in every theme.
 *
 * This test is the enforcement mechanism behind the claims in
 * `docs/design/02-color.md` and `docs/design/08-accessibility.md`. Contrast is
 * **computed, never eyeballed** — a token change that drops any pair below its
 * threshold fails the build here rather than shipping.
 *
 * It is also what makes the 600 / 300 rule safe to rely on: several `500` steps
 * deliberately fail as text and pass as fills, and this test proves the theme
 * never places one where it would fail.
 */
@DisplayName("Semantic colour contrast")
class ContrastTest {

    // --- WCAG 2.2 relative luminance and contrast ---------------------------

    /**
     * Converts an sRGB channel to its linear value per WCAG 2.2.
     *
     * @param channel the channel in the range 0..1
     * @return the linearised channel value
     */
    private fun linearise(channel: Float): Double {
        val c = channel.toDouble()
        return if (c <= SRGB_THRESHOLD) {
            c / SRGB_LOW_DIVISOR
        } else {
            ((c + SRGB_OFFSET) / SRGB_SCALE).pow(SRGB_EXPONENT)
        }
    }

    /**
     * Relative luminance of a colour, per WCAG 2.2.
     *
     * @param color the colour to measure
     * @return relative luminance in the range 0..1
     */
    private fun luminance(color: Color): Double =
        LUM_R * linearise(color.red) +
            LUM_G * linearise(color.green) +
            LUM_B * linearise(color.blue)

    /**
     * Contrast ratio between two colours, per WCAG 2.2.
     *
     * @param foreground the text or icon colour
     * @param background the surface it sits on
     * @return the ratio, from 1.0 (identical) to 21.0 (black on white)
     */
    private fun contrast(foreground: Color, background: Color): Double {
        val a = luminance(foreground)
        val b = luminance(background)
        return (max(a, b) + CONTRAST_OFFSET) / (min(a, b) + CONTRAST_OFFSET)
    }

    /**
     * Asserts a pair meets its minimum ratio, reporting the measured value.
     *
     * @param label human-readable identification of the pair
     * @param foreground the text or icon colour
     * @param background the surface it sits on
     * @param minimum the required ratio
     */
    private fun assertContrast(
        label: String,
        foreground: Color,
        background: Color,
        minimum: Double,
    ) {
        val ratio = contrast(foreground, background)
        assertTrue(ratio >= minimum) {
            "$label measured %.2f:1, requires %.1f:1".format(ratio, minimum)
        }
    }

    // --- Sanity ------------------------------------------------------------

    @Test
    @DisplayName("the contrast function agrees with the WCAG reference values")
    fun contrastFunctionIsCorrect() {
        // Black on white is the defined maximum, 21:1.
        val blackOnWhite = contrast(Color(0xFF000000), Color(0xFFFFFFFF))
        assertTrue(blackOnWhite in MAX_RATIO_LOW..MAX_RATIO_HIGH) {
            "black on white measured $blackOnWhite, expected ~21:1"
        }

        // A colour against itself is the defined minimum, 1:1.
        val identical = contrast(Color(0xFF1A2030), Color(0xFF1A2030))
        assertTrue(identical in MIN_RATIO_LOW..MIN_RATIO_HIGH) {
            "identical colours measured $identical, expected 1:1"
        }
    }

    // --- Content on surface ------------------------------------------------

    @Test
    @DisplayName("content roles meet their thresholds in light theme")
    fun lightContentContrast() {
        val c = lightColors()
        val bg = c.surface.default
        assertAll(
            { assertContrast("light content.primary", c.content.primary, bg, AAA_BODY) },
            { assertContrast("light content.secondary", c.content.secondary, bg, AA_BODY) },
            { assertContrast("light content.tertiary", c.content.tertiary, bg, AA_BODY) },
            { assertContrast("light content.disabled", c.content.disabled, bg, DISABLED_FLOOR) },
        )
    }

    @Test
    @DisplayName("content roles meet their thresholds in dark theme")
    fun darkContentContrast() {
        val c = darkColors()
        val bg = c.surface.default
        assertAll(
            { assertContrast("dark content.primary", c.content.primary, bg, AAA_BODY) },
            { assertContrast("dark content.secondary", c.content.secondary, bg, AA_BODY) },
            { assertContrast("dark content.tertiary", c.content.tertiary, bg, AA_BODY) },
            { assertContrast("dark content.disabled", c.content.disabled, bg, DISABLED_FLOOR) },
        )
    }

    @Test
    @DisplayName("high contrast themes reach AAA on body text")
    fun highContrastReachesAaa() {
        val light = highContrastLightColors()
        val dark = highContrastDarkColors()
        assertAll(
            {
                assertContrast(
                    "hc-light content.primary",
                    light.content.primary,
                    light.surface.default,
                    AAA_BODY,
                )
            },
            {
                assertContrast(
                    "hc-light content.secondary",
                    light.content.secondary,
                    light.surface.default,
                    AAA_BODY,
                )
            },
            {
                assertContrast(
                    "hc-dark content.primary",
                    dark.content.primary,
                    dark.surface.default,
                    AAA_BODY,
                )
            },
            {
                assertContrast(
                    "hc-dark content.secondary",
                    dark.content.secondary,
                    dark.surface.default,
                    AAA_BODY,
                )
            },
        )
    }

    // --- Status text — the 600 / 300 rule ----------------------------------

    @Test
    @DisplayName("every status text colour passes AA on the light surface")
    fun lightStatusTextContrast() {
        val c = lightColors()
        val bg = c.surface.default
        assertAll(
            statusPairs(c).map { (name, status) ->
                { assertContrast("light status.$name.text", status.text, bg, AA_BODY) }
            },
        )
    }

    @Test
    @DisplayName("every status text colour passes AA on the dark surface")
    fun darkStatusTextContrast() {
        val c = darkColors()
        val bg = c.surface.default
        assertAll(
            statusPairs(c).map { (name, status) ->
                { assertContrast("dark status.$name.text", status.text, bg, AA_BODY) }
            },
        )
    }

    @Test
    @DisplayName("status text passes against its own subtle tint, not just the surface")
    fun statusTextOnSubtleTint() {
        // A badge places status.text on status.subtle, not on the page surface.
        // Verifying only against the surface would miss the combination that
        // actually ships.
        val c = lightColors()
        assertAll(
            statusPairs(c).map { (name, status) ->
                { assertContrast("light status.$name.text on subtle", status.text, status.subtle, AA_BODY) }
            },
        )
    }

    // --- Action roles ------------------------------------------------------

    @Test
    @DisplayName("action content passes against its own fill in both themes")
    fun actionContrast() {
        val light = lightColors()
        val dark = darkColors()
        assertAll(
            {
                assertContrast(
                    "light action.primary",
                    light.action.primary.content,
                    light.action.primary.fill,
                    AA_BODY,
                )
            },
            {
                assertContrast(
                    "light action.danger",
                    light.action.danger.content,
                    light.action.danger.fill,
                    AA_BODY,
                )
            },
            {
                assertContrast(
                    "light action.disabled",
                    light.action.disabled.content,
                    light.action.disabled.fill,
                    DISABLED_FLOOR,
                )
            },
            {
                assertContrast(
                    "dark action.disabled",
                    dark.action.disabled.content,
                    dark.action.disabled.fill,
                    DISABLED_FLOOR,
                )
            },
            {
                assertContrast(
                    "dark action.primary",
                    dark.action.primary.content,
                    dark.action.primary.fill,
                    AA_BODY,
                )
            },
            {
                assertContrast(
                    "dark action.danger",
                    dark.action.danger.content,
                    dark.action.danger.fill,
                    AA_BODY,
                )
            },
        )
    }

    @Test
    @DisplayName("the focus ring is visible against every surface it can appear on")
    fun focusRingContrast() {
        val light = lightColors()
        val dark = darkColors()
        assertAll(
            { assertContrast("light focus on default", light.border.focus, light.surface.default, AA_NON_TEXT) },
            { assertContrast("light focus on sunken", light.border.focus, light.surface.sunken, AA_NON_TEXT) },
            { assertContrast("dark focus on default", dark.border.focus, dark.surface.default, AA_NON_TEXT) },
            { assertContrast("dark focus on sunken", dark.border.focus, dark.surface.sunken, AA_NON_TEXT) },
        )
    }

    // --- Regression guard for the 600 / 300 rule ---------------------------

    @Test
    @DisplayName("no status uses its fill colour as text — the fills would fail")
    fun fillsAreNotUsedAsText() {
        // warning.500, voice.500 and premium.500 measure below 4.5:1 on white.
        // They are legitimate fills and illegitimate text. If a future edit
        // wires a fill into a text role, this catches it before review does.
        val c = lightColors()
        assertAll(
            statusPairs(c).map { (name, status) ->
                {
                    assertTrue(status.text != status.fill) {
                        "status.$name.text must not reuse the fill colour — " +
                            "several fills fail WCAG AA as text"
                    }
                }
            },
        )
    }

    /**
     * Every status role paired with its name, for table-driven assertions.
     *
     * @param colors the scheme under test
     * @return name-to-role pairs covering all ten statuses
     */
    private fun statusPairs(colors: CallScreenColors): List<Pair<String, StatusColors>> =
        with(colors.status) {
            listOf(
                "success" to success,
                "warning" to warning,
                "spam" to spam,
                "fraud" to fraud,
                "emergency" to emergency,
                "ai" to ai,
                "voice" to voice,
                "telephony" to telephony,
                "recording" to recording,
                "premium" to premium,
            )
        }

    private companion object {
        // WCAG 2.2 sRGB linearisation constants.
        const val SRGB_THRESHOLD = 0.04045
        const val SRGB_LOW_DIVISOR = 12.92
        const val SRGB_OFFSET = 0.055
        const val SRGB_SCALE = 1.055
        const val SRGB_EXPONENT = 2.4

        // WCAG 2.2 luminance coefficients.
        const val LUM_R = 0.2126
        const val LUM_G = 0.7152
        const val LUM_B = 0.0722

        const val CONTRAST_OFFSET = 0.05

        /** WCAG AA for normal-size text. */
        const val AA_BODY = 4.5

        /** WCAG AAA for normal-size text; our target for primary content. */
        const val AAA_BODY = 7.0

        /** WCAG AA for non-text elements such as focus indicators. */
        const val AA_NON_TEXT = 3.0

        /**
         * Our floor for disabled content.
         *
         * WCAG **exempts** disabled controls from contrast entirely. We do not
         * take the exemption, because a control the user cannot read is one
         * whose purpose they cannot determine. But 4.5:1 is unreachable while
         * still *looking* disabled — at that ratio the control reads as
         * enabled, which is a worse failure. 3:1 is the honest compromise:
         * stricter than the standard requires, legible, and still visibly
         * inactive.
         */
        const val DISABLED_FLOOR = 3.0

        const val MAX_RATIO_LOW = 20.9
        const val MAX_RATIO_HIGH = 21.1
        const val MIN_RATIO_LOW = 0.99
        const val MIN_RATIO_HIGH = 1.01
    }
}
