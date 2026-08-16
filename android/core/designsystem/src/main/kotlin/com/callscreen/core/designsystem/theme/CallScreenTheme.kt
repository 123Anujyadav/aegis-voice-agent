package com.callscreen.core.designsystem.theme

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.ProvidableCompositionLocal
import androidx.compose.runtime.ReadOnlyComposable
import androidx.compose.runtime.remember
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.platform.LocalConfiguration
import com.callscreen.core.designsystem.token.CallScreenTypography
import com.callscreen.core.designsystem.token.Script

/**
 * Which colour scheme the theme should resolve.
 */
public enum class ThemeMode {

    /** Follow the platform's dark-theme setting. The default. */
    System,

    /** Force light regardless of the platform setting. */
    Light,

    /** Force dark regardless of the platform setting. */
    Dark,
}

/**
 * The CallScreen theme.
 *
 * Provides colours, typography and motion to the composition. Access them
 * through the [CallScreenTheme] object rather than threading parameters:
 *
 * ```kotlin
 * Text(
 *     text = "Unknown caller",
 *     style = CallScreenTheme.typography.titleMedium,
 *     color = CallScreenTheme.colors.content.primary,
 * )
 * ```
 *
 * ## `MaterialTheme` is deliberately not used
 *
 * Material 3's colour roles cannot express ten domain statuses. Mapping `fraud`,
 * `spam`, `recording` and the rest onto `primary` / `secondary` / `tertiary`
 * would discard exactly the meaning this design system exists to carry. We
 * interoperate where a Material component needs a scheme, but these tokens are
 * the source of truth.
 *
 * ## Dynamic colour is not applied to status
 *
 * [dynamicColor] affects neutral surfaces only, and only on opt-in. Semantic
 * status colours are **never** wallpaper-derived: `fraud` must be the same
 * colour on every device. If a user's wallpaper recoloured the fraud badge to a
 * friendly teal, the design system would have actively caused harm. Semantic
 * colour in a trust product is a safety property, not personalisation.
 *
 * @param mode which scheme to resolve
 * @param highContrast whether to use the high-contrast variants — bind this to
 *   the platform accessibility setting rather than exposing it as a preference
 * @param dynamicColor whether to derive neutral surfaces from the wallpaper.
 *   Reserved for a future opt-in; status colours are unaffected either way
 * @param content the composition to theme
 *
 * @see docs/design/02-color.md
 * @see docs/design/10-tokens-and-naming.md
 */
@Composable
public fun CallScreenTheme(
    mode: ThemeMode = ThemeMode.System,
    highContrast: Boolean = false,
    @Suppress(
        // Accepted now so that call sites are stable when dynamic neutrals ship.
        // Wiring it before the contrast re-verification described in
        // docs/design/02-color.md §2.8 exists would let a wallpaper produce an
        // unverified surface, which is worse than not offering the feature.
        "UNUSED_PARAMETER",
    )
    dynamicColor: Boolean = false,
    content: @Composable () -> Unit,
) {
    val isDark = when (mode) {
        ThemeMode.System -> isSystemInDarkTheme()
        ThemeMode.Light -> false
        ThemeMode.Dark -> true
    }

    val colors = remember(isDark, highContrast) {
        when {
            highContrast && isDark -> highContrastDarkColors()
            highContrast -> highContrastLightColors()
            isDark -> darkColors()
            else -> lightColors()
        }
    }

    // Tracking is resolved per script because negative letter spacing destroys
    // Indic conjuncts. Reading the locale here means no call site has to think
    // about it — see docs/design/03-typography.md §3.5.
    val configuration = LocalConfiguration.current
    val script = remember(configuration) {
        val tag = configuration.locales[0]?.toLanguageTag().orEmpty()
        Script.forLanguageTag(tag)
    }
    val typography = remember(script) { CallScreenTypography(script) }

    CompositionLocalProvider(
        LocalCallScreenColors provides colors,
        LocalCallScreenTypography provides typography,
        content = content,
    )
}

/**
 * Accessors for the current theme's tokens.
 *
 * Deliberately an object with `@Composable` properties rather than free
 * functions, so that usage reads as `CallScreenTheme.colors.status.fraud.text` —
 * mirroring the token path under `design/tokens` exactly.
 *
 * Note: a glob such as `tokens` followed by a slash and an asterisk cannot be
 * written literally in a KDoc block — Kotlin supports nested block comments, so
 * that sequence opens a comment that is never closed.
 */
public object CallScreenTheme {

    /** The active colour scheme. */
    public val colors: CallScreenColors
        @Composable
        @ReadOnlyComposable
        get() = LocalCallScreenColors.current

    /** The active type scale, with tracking resolved for the current script. */
    public val typography: CallScreenTypography
        @Composable
        @ReadOnlyComposable
        get() = LocalCallScreenTypography.current
}

/**
 * The current colour scheme.
 *
 * `staticCompositionLocalOf` is correct here rather than `compositionLocalOf`:
 * the scheme changes rarely — only on a theme or contrast switch — and a static
 * local skips the invalidation bookkeeping that a frequently-changing value
 * would need.
 *
 * Reading this outside a [CallScreenTheme] is a programming error and throws,
 * rather than silently supplying a default that would render untested colours.
 */
public val LocalCallScreenColors: ProvidableCompositionLocal<CallScreenColors> =
    staticCompositionLocalOf {
        error("CallScreenColors was read outside a CallScreenTheme. Wrap the composition in CallScreenTheme.")
    }

/**
 * The current type scale.
 *
 * Reading this outside a [CallScreenTheme] is a programming error and throws,
 * for the same reason as [LocalCallScreenColors].
 */
public val LocalCallScreenTypography: ProvidableCompositionLocal<CallScreenTypography> =
    staticCompositionLocalOf {
        error("CallScreenTypography was read outside a CallScreenTheme. Wrap the composition in CallScreenTheme.")
    }
