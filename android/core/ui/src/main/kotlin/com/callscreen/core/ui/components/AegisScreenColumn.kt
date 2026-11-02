package com.callscreen.core.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
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
 */
@Composable
public fun AegisScreenColumn(
    modifier: Modifier = Modifier,
    horizontalAlignment: Alignment.Horizontal = Alignment.CenterHorizontally,
    verticalArrangement: Arrangement.Vertical = Arrangement.SpaceBetween,
    contentPadding: Dp = 24.dp,
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
                .padding(contentPadding),
            horizontalAlignment = horizontalAlignment,
            verticalArrangement = verticalArrangement,
            content = content,
        )
    }
}
