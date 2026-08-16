package com.callscreen.core.designsystem.token

import androidx.compose.ui.unit.TextUnit
import androidx.compose.ui.unit.isSpecified
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.assertAll

/**
 * Verifies the two typographic properties that break Indic text if wrong.
 *
 * Both are easy to get right in review and easy to regress in a refactor, which
 * is exactly what a test is for.
 */
@DisplayName("Type scale")
class TypographyTest {

    // --- Script detection ---------------------------------------------------

    @Test
    @DisplayName("Indic language tags resolve to the Indic script")
    fun indicLanguagesDetected() {
        val indicTags = listOf(
            "hi", "hi-IN", "bn", "bn-IN", "ta", "ta-IN",
            "te", "mr", "gu", "kn", "ml", "pa", "or", "as", "ne",
        )
        assertAll(
            indicTags.map { tag ->
                {
                    assertEquals(Script.Indic, Script.forLanguageTag(tag)) {
                        "$tag should resolve to Indic"
                    }
                }
            },
        )
    }

    @Test
    @DisplayName("non-Indic language tags resolve to Latin")
    fun latinLanguagesDetected() {
        val latinTags = listOf("en", "en-IN", "en-US", "fr", "de", "es", "")
        assertAll(
            latinTags.map { tag ->
                {
                    assertEquals(Script.Latin, Script.forLanguageTag(tag)) {
                        "'$tag' should resolve to Latin"
                    }
                }
            },
        )
    }

    @Test
    @DisplayName("script detection ignores region and is case-insensitive")
    fun scriptDetectionIsRobust() {
        assertAll(
            { assertEquals(Script.Indic, Script.forLanguageTag("HI-in")) },
            { assertEquals(Script.Indic, Script.forLanguageTag("Ta-IN")) },
            { assertEquals(Script.Latin, Script.forLanguageTag("EN-in")) },
        )
    }

    // --- The rule that matters ---------------------------------------------

    @Test
    @DisplayName("no tracking token is ever negative for Indic scripts")
    fun indicTrackingIsNeverNegative() {
        // Negative letter spacing collapses Devanagari and Bengali conjuncts
        // into an unreadable cluster. This is the single most damaging
        // typographic mistake available in this product's market.
        assertAll(
            Tracking.entries.map { tracking ->
                {
                    val value = tracking.resolve(Script.Indic).value
                    assertTrue(value >= 0f) {
                        "Tracking.$tracking resolves to $value em for Indic — must never be negative"
                    }
                }
            },
        )
    }

    @Test
    @DisplayName("Latin keeps its negative tracking on display sizes")
    fun latinKeepsNegativeTracking() {
        // The Indic rule must not have flattened Latin tracking as a side
        // effect — display type needs the tightening.
        assertAll(
            { assertTrue(Tracking.Tight.resolve(Script.Latin).value < 0f) },
            { assertTrue(Tracking.Snug.resolve(Script.Latin).value < 0f) },
        )
    }

    @Test
    @DisplayName("Indic tracking is never looser than Latin")
    fun indicTrackingIsNeverLooser() {
        // Matras already provide visual separation; matching Latin's positive
        // tracking would over-space Indic text.
        assertAll(
            Tracking.entries.map { tracking ->
                {
                    val latin = tracking.resolve(Script.Latin).value
                    val indic = tracking.resolve(Script.Indic).value
                    assertTrue(indic <= maxOf(latin, 0f)) {
                        "Tracking.$tracking is looser for Indic ($indic) than Latin ($latin)"
                    }
                }
            },
        )
    }

    // --- Line height --------------------------------------------------------

    @Test
    @DisplayName("every style has at least 6sp of absolute leading")
    fun lineHeightIsDevanagariSafe() {
        // Devanagari matras stack above and below the baseline, and what they
        // need is ABSOLUTE room, not a ratio. Large type needs proportionally
        // *less* leading — a 40sp display at 1.5x would be a 60sp line height,
        // which looks broken — so the invariant is a floor in sp, not a
        // multiplier. See docs/design/03-typography.md.
        val typography = CallScreenTypography(Script.Indic)
        val styles = listOf(
            "displayLarge" to typography.displayLarge,
            "displayMedium" to typography.displayMedium,
            "displaySmall" to typography.displaySmall,
            "headlineLarge" to typography.headlineLarge,
            "headlineMedium" to typography.headlineMedium,
            "headlineSmall" to typography.headlineSmall,
            "titleLarge" to typography.titleLarge,
            "titleMedium" to typography.titleMedium,
            "titleSmall" to typography.titleSmall,
            "bodyLarge" to typography.bodyLarge,
            "bodyMedium" to typography.bodyMedium,
            "bodySmall" to typography.bodySmall,
            "labelLarge" to typography.labelLarge,
            "labelMedium" to typography.labelMedium,
            "labelSmall" to typography.labelSmall,
            "numericLarge" to typography.numericLarge,
            "numericMedium" to typography.numericMedium,
            "numericSmall" to typography.numericSmall,
        )

        assertAll(
            styles.map { (name, style) ->
                {
                    val leading = style.lineHeight.valueOrZero() - style.fontSize.valueOrZero()
                    assertTrue(leading >= MIN_LEADING_SP) {
                        "$name has %.0fsp of leading, Devanagari-safe minimum is %.0fsp".format(
                            leading,
                            MIN_LEADING_SP,
                        )
                    }
                }
            },
        )
    }

    @Test
    @DisplayName("body styles additionally meet the WCAG 1.4.12 ratio of 1.5x")
    fun bodyStylesMeetWcagLineHeight() {
        // WCAG 1.4.12 applies to paragraph text, which is what the body styles
        // carry. Headings and labels are exempt and are covered by the absolute
        // leading floor above instead.
        val t = CallScreenTypography(Script.Indic)
        val bodyStyles = listOf(
            "bodyLarge" to t.bodyLarge,
            "bodyMedium" to t.bodyMedium,
            "bodySmall" to t.bodySmall,
        )

        assertAll(
            bodyStyles.map { (name, style) ->
                {
                    val ratio = style.lineHeight.valueOrZero() / style.fontSize.valueOrZero()
                    assertTrue(ratio >= WCAG_BODY_LINE_HEIGHT_RATIO) {
                        "$name has a line height ratio of %.2f, WCAG 1.4.12 requires %.2f".format(
                            ratio,
                            WCAG_BODY_LINE_HEIGHT_RATIO,
                        )
                    }
                }
            },
        )
    }

    @Test
    @DisplayName("line height ratio decreases as size increases")
    fun leadingRatioTightensWithSize() {
        // Correct typographic behaviour: larger type needs proportionally less
        // leading. A scale where the ratio grew with size would look loose at
        // the top and cramped at the bottom.
        val t = CallScreenTypography(Script.Latin)
        val bigRatio = t.displayLarge.lineHeight.valueOrZero() /
            t.displayLarge.fontSize.valueOrZero()
        val smallRatio = t.bodySmall.lineHeight.valueOrZero() /
            t.bodySmall.fontSize.valueOrZero()

        assertTrue(bigRatio < smallRatio) {
            "display ratio %.2f should be tighter than body-small ratio %.2f".format(
                bigRatio,
                smallRatio,
            )
        }
    }

    @Test
    @DisplayName("bodyLarge is the most generous body style")
    fun transcriptStyleIsMostGenerous() {
        // bodyLarge carries the transcript, which is the feature that makes this
        // product usable by deaf and hard-of-hearing users.
        val t = CallScreenTypography(Script.Latin)
        assertAll(
            { assertTrue(t.bodyLarge.fontSize.valueOrZero() > t.bodyMedium.fontSize.valueOrZero()) },
            { assertTrue(t.bodyLarge.lineHeight.valueOrZero() > t.bodyMedium.lineHeight.valueOrZero()) },
        )
    }

    /**
     * Reads a [TextUnit]'s numeric value, treating unspecified as zero.
     *
     * @return the value, or 0 when unspecified
     */
    private fun TextUnit.valueOrZero(): Float = if (isSpecified) value else 0f

    private companion object {
        /**
         * Devanagari-safe floor in sp. Latin tolerates 4; matras do not.
         *
         * Absolute, not a ratio — see the class doc on [CallScreenTypography].
         */
        const val MIN_LEADING_SP = 6f

        /** WCAG 1.4.12, which applies to paragraph text only. */
        const val WCAG_BODY_LINE_HEIGHT_RATIO = 1.5f
    }
}
