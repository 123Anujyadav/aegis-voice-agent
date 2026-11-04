package com.callscreen.core.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.union
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme

/**
 * A full screen's root column: the theme's background, [contentPadding], and
 * [verticalArrangement] (by default the content at the top and the action at
 * the bottom) whenever the content fits.
 *
 * When it does not fit, on a small screen, in landscape or with a large font
 * scale, it scrolls. A column that only fills the screen clips what does not
 * fit, and the primary action at the bottom becomes unreachable: on a 640 x
 * 320 dp landscape emulator, the sign-in screen's message and button were off
 * screen with no way to reach them (the minified-build functional tests,
 * android/test-minified).
 *
 * The content is at least as tall as the screen, so the arrangement still
 * spreads it when there is room.
 *
 * The background reaches behind the system bars (the app draws edge to edge).
 * On each side the content is padded by [contentPadding] or by the system bar
 * there ([windowInsets]), whichever is wider: it rests clear of the bars,
 * scrolls behind them as an edge-to-edge screen's content does, and loses no
 * room where its own padding already covered the bar (a 24 dp status bar or
 * gesture bar). Adding the two made Welcome's Get Protected scroll out of
 * view on a 320 x 640 dp screen.
 *
 * Inside a Scaffold, which has already made room for its bars, pass its
 * padding as `Modifier.padding(padding)` and `windowInsets =
 * WindowInsets(0, 0, 0, 0)`, so that room is not made twice. Consuming the
 * Scaffold's padding instead (`consumeWindowInsets`) would also take it out
 * of [contentPadding], since a consumed inset is subtracted from the whole
 * padding: App language's intro then sat flush under the top bar
 * (SystemBarsOnDeviceTest).
 */
@Composable
public fun AegisScreenColumn(
    modifier: Modifier = Modifier,
    horizontalAlignment: Alignment.Horizontal = Alignment.CenterHorizontally,
    verticalArrangement: Arrangement.Vertical = Arrangement.SpaceBetween,
    contentPadding: Dp = 24.dp,
    windowInsets: WindowInsets = WindowInsets.aegisSafeArea,
    content: @Composable ColumnScope.() -> Unit,
) {
    BoxWithConstraints(
        modifier = modifier
            .fillMaxSize()
            .background(CallScreenTheme.colors.surface.background),
    ) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .verticalScroll(rememberScrollState())
                .heightIn(min = maxHeight)
                .windowInsetsPadding(windowInsets.union(WindowInsets(contentPadding, contentPadding, contentPadding, contentPadding))),
            horizontalAlignment = horizontalAlignment,
            verticalArrangement = verticalArrangement,
            content = content,
        )
    }
}
