package com.callscreen.core.designsystem.theme

import androidx.compose.ui.graphics.Color
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test
import kotlin.math.max
import kotlin.math.min
import kotlin.math.pow

/**
 * The Lily AI brand accent is used for text and icons (the selected tab, the
 * logo title), so it must read in every theme: WCAG AA (4.5:1) in light and
 * dark, AAA (7:1) in the high-contrast themes, on the default surface and on
 * the AI tint the selected tab sits on.
 */
@DisplayName("Brand accent contrast")
class BrandAccentContrastTest {

    private fun linearise(c: Float): Double =
        if (c <= 0.04045f) c / 12.92 else ((c + 0.055) / 1.055).pow(2.4)

    private fun luminance(c: Color): Double =
        0.2126 * linearise(c.red) + 0.7152 * linearise(c.green) + 0.0722 * linearise(c.blue)

    private fun contrast(a: Color, b: Color): Double {
        val la = luminance(a)
        val lb = luminance(b)
        return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
    }

    private fun check(name: String, colors: CallScreenColors, minimum: Double) {
        for ((bgName, bg) in listOf("surface.default" to colors.surface.default, "status.ai.subtle" to colors.status.ai.subtle)) {
            val ratio = contrast(colors.brandAccent, bg)
            assertTrue(ratio >= minimum, "$name: brandAccent on $bgName is ${"%.2f".format(ratio)}:1, need $minimum:1")
        }
    }

    @Test
    @DisplayName("light: AA")
    fun light() = check("light", lightColors(), 4.5)

    @Test
    @DisplayName("dark: AA")
    fun dark() = check("dark", darkColors(), 4.5)

    @Test
    @DisplayName("high-contrast light: AAA")
    fun highContrastLight() = check("high-contrast light", highContrastLightColors(), 7.0)

    @Test
    @DisplayName("high-contrast dark: AAA")
    fun highContrastDark() = check("high-contrast dark", highContrastDarkColors(), 7.0)

    @Test
    @DisplayName("light uses the designed violet; dark does not (it would fail)")
    fun theDesignedVioletIsUsedWhereItReads() {
        assertTrue(lightColors().brandAccent == LilyAiTokens.brandViolet)
        assertTrue(contrast(LilyAiTokens.brandViolet, darkColors().surface.default) < 4.5, "the designed violet reads on dark: simplify")
    }
}
