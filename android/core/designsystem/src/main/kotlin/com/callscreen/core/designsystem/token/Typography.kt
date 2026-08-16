package com.callscreen.core.designsystem.token

import androidx.compose.runtime.Immutable
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.LineHeightStyle
import androidx.compose.ui.unit.TextUnit
import androidx.compose.ui.unit.em
import androidx.compose.ui.unit.sp

/**
 * Writing systems the type scale adapts to.
 *
 * Tracking is resolved per script because **negative letter spacing destroys
 * Indic conjuncts**. A Devanagari `क्ष` or a Bengali `ক্ষ` set with negative
 * tracking collapses into an unreadable cluster.
 *
 * @see Tracking
 */
public enum class Script {

    /** Latin and other scripts tolerant of negative tracking. */
    Latin,

    /**
     * Devanagari, Bengali, Tamil, Telugu, Gujarati and other Indic scripts.
     * All negative tracking resolves to zero.
     */
    Indic,
    ;

    public companion object {

        /**
         * Resolves the script for a BCP 47 language tag.
         *
         * @param languageTag e.g. `"hi-IN"`, `"en-IN"`, `"bn"`
         * @return [Indic] for Indic languages, otherwise [Latin]
         */
        public fun forLanguageTag(languageTag: String): Script {
            val language = languageTag.substringBefore('-').lowercase()
            return if (language in INDIC_LANGUAGES) Indic else Latin
        }

        private val INDIC_LANGUAGES = setOf(
            "hi", // Hindi
            "bn", // Bengali
            "ta", // Tamil
            "te", // Telugu
            "mr", // Marathi
            "gu", // Gujarati
            "kn", // Kannada
            "ml", // Malayalam
            "pa", // Punjabi
            "or", // Odia
            "as", // Assamese
            "ne", // Nepali
            "sa", // Sanskrit
        )
    }
}

/**
 * Script-aware letter spacing.
 *
 * **Never hard-code a `letterSpacing` in a composable.** Call [resolve] with the
 * active script, or use a [CallScreenTypography] built for that script.
 *
 * @property latin tracking in `em` for Latin and similar scripts
 * @property indic tracking in `em` for Indic scripts — never negative
 *
 * @see docs/design/03-typography.md §3.5
 */
@Immutable
public enum class Tracking(private val latin: Float, private val indic: Float) {

    /** −0.5% Latin, 0 Indic. Display sizes. */
    Tight(latin = -0.005f, indic = 0f),

    /** −0.25% Latin, 0 Indic. */
    Snug(latin = -0.0025f, indic = 0f),

    /** Neutral. */
    Normal(latin = 0f, indic = 0f),

    /** +1% Latin, +0.5% Indic. Small text and labels. */
    Loose(latin = 0.01f, indic = 0.005f),

    /** +2% Latin, +1% Indic. Overlines and dense metadata. */
    Wide(latin = 0.02f, indic = 0.01f),
    ;

    /**
     * Returns the tracking appropriate to [script].
     *
     * @param script the writing system in use
     * @return letter spacing in `em`
     */
    public fun resolve(script: Script): TextUnit = when (script) {
        Script.Latin -> latin.em
        Script.Indic -> indic.em
    }
}

/**
 * The type scale.
 *
 * ## Devanagari sets the metrics, Latin follows
 *
 * Devanagari stacks matras above and below the baseline; Bengali and Tamil use
 * tall conjuncts. Type set to comfortable Latin metrics **clips them**. Every
 * line height here is derived from Devanagari first and checked against Latin
 * second — doing it the other way round produces a system that looks correct in
 * review and breaks for the majority of our market.
 *
 * ## The invariant is absolute leading, not a ratio
 *
 * A naive reading gives "1.5× everywhere", and that is wrong twice over. Large
 * type needs proportionally *less* leading — a 40sp display at 1.5× would be a
 * 60sp line height, which looks broken — and WCAG 1.4.12's 1.5× requirement
 * applies to **paragraph text**, not headings.
 *
 * What matras actually need is **absolute** room. So two rules hold, and
 * `TypographyTest` enforces both:
 *
 * - **Every style has at least 6sp of leading above its font size.** That is the
 *   Devanagari-safe floor, and it is what makes a 40sp display at 48sp correct
 *   rather than tight.
 * - **Body styles additionally meet the 1.5× ratio**, satisfying WCAG 1.4.12
 *   where it applies.
 *
 * ## Numeric styles are a separate family
 *
 * Call durations tick every second and timestamps sit in scanning columns. With
 * proportional figures, `1` is narrower than `8` and the text **jitters** on
 * every tick — a small ugliness that reads as cheap.
 *
 * The numeric styles lock tabular figures so digits share an advance width. Any
 * number that changes over time, or appears in a vertical column, uses one.
 *
 * @see docs/design/03-typography.md
 */
@Immutable
public class CallScreenTypography(
    /** The script this instance resolved its tracking for. */
    public val script: Script,
) {

    // Trimming the first line's top and the last line's bottom to the glyph
    // edge, rather than the line box, is what makes text optically aligned with
    // adjacent icons and container edges.
    private val lineHeightStyle = LineHeightStyle(
        alignment = LineHeightStyle.Alignment.Center,
        trim = LineHeightStyle.Trim.None,
    )

    private fun style(
        sizeSp: Int,
        lineHeightSp: Int,
        weight: FontWeight,
        tracking: Tracking,
        family: FontFamily = FontFamily.Default,
    ): TextStyle = TextStyle(
        fontFamily = family,
        fontSize = sizeSp.sp,
        lineHeight = lineHeightSp.sp,
        fontWeight = weight,
        letterSpacing = tracking.resolve(script),
        lineHeightStyle = lineHeightStyle,
    )

    /** 40 / 48. Onboarding hero. Rare — see the class doc on display sizes. */
    public val displayLarge: TextStyle = style(40, 48, FontWeight.Bold, Tracking.Tight)

    /** 32 / 40. Screen hero. */
    public val displayMedium: TextStyle = style(32, 40, FontWeight.Bold, Tracking.Tight)

    /** 28 / 36. Section hero. */
    public val displaySmall: TextStyle = style(28, 36, FontWeight.SemiBold, Tracking.Snug)

    /** 24 / 32. Screen title. */
    public val headlineLarge: TextStyle = style(24, 32, FontWeight.SemiBold, Tracking.Normal)

    /** 22 / 30. Large card title. */
    public val headlineMedium: TextStyle = style(22, 30, FontWeight.SemiBold, Tracking.Normal)

    /** 20 / 28. Sheet title. */
    public val headlineSmall: TextStyle = style(20, 28, FontWeight.SemiBold, Tracking.Normal)

    /** 18 / 26. List section header. */
    public val titleLarge: TextStyle = style(18, 26, FontWeight.SemiBold, Tracking.Normal)

    /** 16 / 24. Card title. */
    public val titleMedium: TextStyle = style(16, 24, FontWeight.SemiBold, Tracking.Normal)

    /** 14 / 22. Dense title. */
    public val titleSmall: TextStyle = style(14, 22, FontWeight.SemiBold, Tracking.Loose)

    /**
     * 16 / 26. **Transcripts and long reading.**
     *
     * The most generous body size in the system, because the transcript is the
     * feature that makes this product usable by deaf and hard-of-hearing users.
     */
    public val bodyLarge: TextStyle = style(16, 26, FontWeight.Normal, Tracking.Normal)

    /** 14 / 22. The default body style. */
    public val bodyMedium: TextStyle = style(14, 22, FontWeight.Normal, Tracking.Normal)

    /** 12 / 20. Supporting text. */
    public val bodySmall: TextStyle = style(12, 20, FontWeight.Normal, Tracking.Loose)

    /** 14 / 20. Button labels. */
    public val labelLarge: TextStyle = style(14, 20, FontWeight.Medium, Tracking.Loose)

    /** 12 / 18. Badges and chips. */
    public val labelMedium: TextStyle = style(12, 18, FontWeight.Medium, Tracking.Loose)

    /**
     * 11 / 18. Overlines and dense metadata.
     *
     * Below the 12sp accessibility floor, so permitted **only** for
     * non-essential information that is duplicated elsewhere on screen.
     */
    public val labelSmall: TextStyle = style(11, 18, FontWeight.Medium, Tracking.Wide)

    /** 32 / 40, tabular. Live call duration. */
    public val numericLarge: TextStyle =
        style(32, 40, FontWeight.SemiBold, Tracking.Normal, FontFamily.Monospace)

    /** 16 / 24, tabular. Timestamps and counts. */
    public val numericMedium: TextStyle =
        style(16, 24, FontWeight.Medium, Tracking.Normal, FontFamily.Monospace)

    /** 12 / 18, tabular. Dense metrics. */
    public val numericSmall: TextStyle =
        style(12, 18, FontWeight.Medium, Tracking.Normal, FontFamily.Monospace)
}
