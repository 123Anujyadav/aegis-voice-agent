package com.callscreen.core.ui.components

import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.displayCutout
import androidx.compose.foundation.layout.systemBars
import androidx.compose.foundation.layout.union
import androidx.compose.runtime.Composable
import androidx.compose.runtime.NonRestartableComposable

/**
 * What a screen keeps its words and controls out of. The app draws edge to
 * edge (MainActivity.enableEdgeToEdge), so the status bar, the navigation
 * bar and any camera cutout lie over its edges; screens paint their colour
 * behind them and start their content inside this area.
 *
 * Not the keyboard: a screen with a text field keeps its keyboard behaviour.
 */
public val WindowInsets.Companion.aegisSafeArea: WindowInsets
    @Composable
    @NonRestartableComposable
    get() = WindowInsets.systemBars.union(WindowInsets.displayCutout)
