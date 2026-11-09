package com.callscreen.core.designsystem.theme

import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color

/**
 * The Lily AI brand colours and gradients from the approved redesign.
 *
 * They decorate (logo plates, gradients, the voice orb). Text and icons in
 * the brand colour use [CallScreenColors.brandAccent], which each theme sets
 * to a variant that keeps its contrast (ContrastTest).
 */
public object LilyAiTokens {
    public val brandViolet: Color = Color(0xFF7C3AED)
    public val brandIndigo: Color = Color(0xFF6366F1)
    public val brandPurple: Color = Color(0xFF8B5CF6)
    public val brandNavy: Color = Color(0xFF001A48)
    public val brandNavyContainer: Color = Color(0xFF002D72)
    public val secondaryIndigo: Color = Color(0xFF4355B9)
    public val indigoContainer: Color = Color(0xFF8596FF)
    public val canvasLight: Color = Color(0xFFFAF8FF)
    public val canvasDark: Color = Color(0xFF10141F)

    /** The brand violet for dark surfaces, where the designed shade is too dark to read. */
    public val brandVioletOnDark: Color = Color(0xFFC4B5FD)

    /** Deeper violet for the high-contrast light theme (AAA). */
    public val brandVioletHighContrast: Color = Color(0xFF5B21B6)

    /** Paler violet for the high-contrast dark theme (AAA). */
    public val brandVioletHighContrastOnDark: Color = Color(0xFFDDD6FE)

    /** The plate behind the logo, in every theme: the logo's navy wordmark needs a light ground. */
    public val logoPlate: Color = Color(0xFFFFFFFF)

    public val primaryGradient: Brush = Brush.linearGradient(listOf(brandViolet, brandIndigo, brandPurple))

    public val navyGradient: Brush = Brush.verticalGradient(listOf(brandNavy, brandNavyContainer))

    public val orbGradient: Brush = Brush.radialGradient(listOf(brandViolet, brandIndigo, secondaryIndigo))
}
