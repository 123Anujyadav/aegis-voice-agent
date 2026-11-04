package com.callscreen.core.ui.components

import androidx.compose.runtime.Composable
import androidx.compose.runtime.ProvidableCompositionLocal
import androidx.compose.runtime.staticCompositionLocalOf

/**
 * The way back from the screen being composed, for the shared top bar's Back
 * arrow (owner decision D6, 2026-11-04: a Back arrow across the app, keeping
 * navigation as it is). The app's navigation provides it: it returns the
 * action Android's Back would take, or null on a screen with nothing in the
 * app to go back to, such as the first screen after sign-in. Outside the
 * app's navigation (previews, tests of one screen) there is no arrow.
 *
 * It is resolved per screen, from the screen's own place in the back stack,
 * so a screen leaving or arriving during a transition shows its own arrow.
 */
public val LocalAegisBack: ProvidableCompositionLocal<@Composable () -> (() -> Unit)?> =
    staticCompositionLocalOf { { null } }
