package com.callscreen.core.designsystem.token

import androidx.compose.ui.graphics.Color

/**
 * Tier 1 — raw colour primitives. Themeless.
 *
 * **These are never referenced by a component.** They exist solely to be mapped
 * to semantic roles by the theme, per light / dark / high-contrast. A component
 * that reaches for a primitive cannot respond to theme, which is the single most
 * common way a design system breaks in dark mode.
 *
 * Use [com.callscreen.core.designsystem.theme.CallScreenTheme.colors] instead.
 *
 * ## The 600 / 300 rule
 *
 * Ramps are tuned so one rule holds across all ten hues: on light surfaces text
 * uses the `600` step; fills use `500` with white content on top; on dark
 * surfaces text uses `300`.
 *
 * This is derived from measured contrast, not convention. `warning500`,
 * `voice500` and `premium500` all **fail** WCAG AA as text on white (3.8:1,
 * 3.8:1 and 4.3:1) while passing comfortably as fills. Following the rule makes
 * the failing combination unreachable.
 *
 * Generated from `design/tokens/primitive.json`. Do not hand-edit.
 *
 * @see docs/design/02-color.md
 */
@Suppress(
    // A KDoc block per colour step would be ~130 lines of "the 500 step of the
    // fraud ramp" restating the identifier. The class doc above carries the
    // meaning; per-property docs here would be noise that obscures it.
    "UndocumentedPublicProperty",
)
public object Primitives {

    // --- Neutral ------------------------------------------------------------
    // Slightly cool cast. A blue-tinted neutral reads technical and calm; a warm
    // neutral reads friendly and consumer. For a trust product, cool is right.

    public val neutral0: Color = Color(0xFFFFFFFF)
    public val neutral50: Color = Color(0xFFF7F8FA)
    public val neutral100: Color = Color(0xFFEEF0F4)
    public val neutral200: Color = Color(0xFFE1E4EA)
    public val neutral300: Color = Color(0xFFC9CED8)
    public val neutral400: Color = Color(0xFFA0A8B8)
    public val neutral500: Color = Color(0xFF7A8395)
    public val neutral600: Color = Color(0xFF5A6377)
    public val neutral700: Color = Color(0xFF414A5C)
    public val neutral800: Color = Color(0xFF2A3242)
    public val neutral900: Color = Color(0xFF1A2030)

    /**
     * Dark app background. Deliberately **not** pure black: `#000000` causes
     * OLED smearing during scroll and increases halation for astigmatic users.
     */
    public val neutral950: Color = Color(0xFF10141F)
    public val neutral1000: Color = Color(0xFF000000)

    // --- Brand / Telephony --------------------------------------------------
    // Anchored on #0B3D91, the launcher navy committed in Phase 2.

    public val brand50: Color = Color(0xFFEBF1FC)
    public val brand100: Color = Color(0xFFD2E1F9)
    public val brand200: Color = Color(0xFFA6C2F2)
    public val brand300: Color = Color(0xFF6E99E6)
    public val brand400: Color = Color(0xFF3D72D6)
    public val brand500: Color = Color(0xFF1E56C4)
    public val brand600: Color = Color(0xFF1444A6)
    public val brand700: Color = Color(0xFF0B3D91)
    public val brand800: Color = Color(0xFF082E6E)
    public val brand900: Color = Color(0xFF06214F)

    // --- Success ------------------------------------------------------------

    public val success50: Color = Color(0xFFE8F6EE)
    public val success100: Color = Color(0xFFC9EAD7)
    public val success200: Color = Color(0xFF96D5B2)
    public val success300: Color = Color(0xFF5BBA8A)
    public val success400: Color = Color(0xFF2E9E68)
    public val success500: Color = Color(0xFF128552)
    public val success600: Color = Color(0xFF0D6C43)
    public val success700: Color = Color(0xFF0A5636)
    public val success800: Color = Color(0xFF08442B)
    public val success900: Color = Color(0xFF063622)

    // --- Warning ------------------------------------------------------------
    // System attention. NOT caller state — that is `spam`.

    public val warning50: Color = Color(0xFFFDF4E5)
    public val warning100: Color = Color(0xFFFAE6C2)
    public val warning200: Color = Color(0xFFF3CC85)
    public val warning300: Color = Color(0xFFE8AC44)
    public val warning400: Color = Color(0xFFD48F14)
    public val warning500: Color = Color(0xFFB8730A)
    public val warning600: Color = Color(0xFF965C07)
    public val warning700: Color = Color(0xFF784A08)
    public val warning800: Color = Color(0xFF5E3B0A)
    public val warning900: Color = Color(0xFF4A2F0A)

    // --- Spam ---------------------------------------------------------------
    // Unwanted, not malicious. Deliberately distinct from fraud crimson.

    public val spam50: Color = Color(0xFFFDF0EA)
    public val spam100: Color = Color(0xFFFADCCE)
    public val spam200: Color = Color(0xFFF4B99E)
    public val spam300: Color = Color(0xFFE88F68)
    public val spam400: Color = Color(0xFFD66B3E)
    public val spam500: Color = Color(0xFFA8481F)
    public val spam600: Color = Color(0xFF8B3A19)
    public val spam700: Color = Color(0xFF70301A)
    public val spam800: Color = Color(0xFF5A2816)
    public val spam900: Color = Color(0xFF472013)

    // --- Fraud --------------------------------------------------------------
    // Active malicious intent. Deep crimson — serious, not shouty.

    public val fraud50: Color = Color(0xFFFCEDEF)
    public val fraud100: Color = Color(0xFFF8D5DA)
    public val fraud200: Color = Color(0xFFF0A9B4)
    public val fraud300: Color = Color(0xFFE2748A)
    public val fraud400: Color = Color(0xFFCE4763)
    public val fraud500: Color = Color(0xFFB22947)
    public val fraud600: Color = Color(0xFF931F3B)
    public val fraud700: Color = Color(0xFF761A31)
    public val fraud800: Color = Color(0xFF5C1628)
    public val fraud900: Color = Color(0xFF4A1321)

    // --- Emergency ----------------------------------------------------------
    // Urgent, must break through. Vermillion — distinct from fraud and recording.

    public val emergency50: Color = Color(0xFFFDEEE9)
    public val emergency100: Color = Color(0xFFFAD5C8)
    public val emergency200: Color = Color(0xFFF4AA92)
    public val emergency300: Color = Color(0xFFEC7A55)
    public val emergency400: Color = Color(0xFFE05226)
    public val emergency500: Color = Color(0xFFC43F14)
    public val emergency600: Color = Color(0xFFA23310)
    public val emergency700: Color = Color(0xFF82290E)
    public val emergency800: Color = Color(0xFF66210D)
    public val emergency900: Color = Color(0xFF521B0C)

    // --- AI -----------------------------------------------------------------
    // The assistant. Desaturated violet-indigo: legible convention beats novelty
    // in a trust product.

    public val ai50: Color = Color(0xFFF0EEFB)
    public val ai100: Color = Color(0xFFDFDAF7)
    public val ai200: Color = Color(0xFFC0B6EF)
    public val ai300: Color = Color(0xFF9C8DE3)
    public val ai400: Color = Color(0xFF7B67D4)
    public val ai500: Color = Color(0xFF6350C4)
    public val ai600: Color = Color(0xFF513FA6)
    public val ai700: Color = Color(0xFF413186)
    public val ai800: Color = Color(0xFF2F2463)
    public val ai900: Color = Color(0xFF1F1743)

    // --- Voice --------------------------------------------------------------
    // Live speech. Cyan-teal, deliberately distinct from the AI violet.

    public val voice50: Color = Color(0xFFE6F7F9)
    public val voice100: Color = Color(0xFFC2ECF1)
    public val voice200: Color = Color(0xFF8CDAE4)
    public val voice300: Color = Color(0xFF4FC3D2)
    public val voice400: Color = Color(0xFF16A9BD)
    public val voice500: Color = Color(0xFF0891A5)
    public val voice600: Color = Color(0xFF077588)
    public val voice700: Color = Color(0xFF075E6D)
    public val voice800: Color = Color(0xFF084B57)
    public val voice900: Color = Color(0xFF073D47)

    // --- Recording ----------------------------------------------------------
    // Universal hardware-indicator red. Must be unambiguous and distinct from
    // fraud crimson — one is a legal disclosure, the other is a judgement.

    public val recording50: Color = Color(0xFFFDEBEC)
    public val recording100: Color = Color(0xFFFACFD1)
    public val recording200: Color = Color(0xFFF49CA1)
    public val recording300: Color = Color(0xFFEE6870)
    public val recording400: Color = Color(0xFFF5333C)
    public val recording500: Color = Color(0xFFE01B24)
    public val recording600: Color = Color(0xFFBC1219)
    public val recording700: Color = Color(0xFF980E14)
    public val recording800: Color = Color(0xFF780C11)
    public val recording900: Color = Color(0xFF5E0A0E)

    // --- Premium ------------------------------------------------------------
    // Subscription tier. Gold-bronze. A decorative label, never a status that
    // requires action — see docs/design/02-color.md §2.3 rule R4.

    public val premium50: Color = Color(0xFFFAF4E4)
    public val premium100: Color = Color(0xFFF2E5BF)
    public val premium200: Color = Color(0xFFE4CB84)
    public val premium300: Color = Color(0xFFD2AC49)
    public val premium400: Color = Color(0xFFB98F26)
    public val premium500: Color = Color(0xFF9A7419)
    public val premium600: Color = Color(0xFF7D5D14)
    public val premium700: Color = Color(0xFF634A12)
    public val premium800: Color = Color(0xFF4E3B11)
    public val premium900: Color = Color(0xFF3E2F10)
}
