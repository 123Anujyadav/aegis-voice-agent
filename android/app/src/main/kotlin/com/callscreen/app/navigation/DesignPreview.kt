package com.callscreen.app.navigation

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme

/** The label over every prototype screen in a debug build. */
public const val DESIGN_PREVIEW_LABEL: String = "Design preview: sample data, not a working feature. Not in release builds."

/**
 * A prototype screen, framed so that nobody mistakes its sample data for the
 * app's: a fixed label above it. Only debug builds show these screens.
 */
@Composable
internal fun DesignPreview(content: @Composable () -> Unit) {
    Column(modifier = Modifier.fillMaxSize()) {
        Text(
            text = DESIGN_PREVIEW_LABEL,
            style = CallScreenTheme.typography.bodySmall,
            fontWeight = FontWeight.Bold,
            color = CallScreenTheme.colors.status.warning.text,
            modifier = Modifier
                .fillMaxWidth()
                .background(CallScreenTheme.colors.status.warning.subtle)
                .padding(horizontal = 16.dp, vertical = 8.dp),
        )
        Box(modifier = Modifier.weight(1f)) { content() }
    }
}
