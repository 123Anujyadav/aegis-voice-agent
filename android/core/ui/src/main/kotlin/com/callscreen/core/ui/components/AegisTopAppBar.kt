package com.callscreen.core.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.WindowInsetsSides
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.only
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Person
import androidx.compose.material.icons.filled.Shield
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.R

/**
 * The app draws edge to edge (MainActivity.enableEdgeToEdge), so the status
 * bar lies over the top of every screen, and in landscape the navigation bar
 * or a camera cutout can lie over a side. The bar's colour reaches behind
 * them; its title and avatar start inside [windowInsets] (design review U1).
 */
@Composable
public fun AegisTopAppBar(
    title: String = stringResource(R.string.core_ui_app_title),
    modifier: Modifier = Modifier,
    onProfileClick: () -> Unit = {},
    windowInsets: WindowInsets = WindowInsets.aegisSafeArea.only(WindowInsetsSides.Horizontal + WindowInsetsSides.Top),
) {
    Row(
        modifier = modifier
            .fillMaxWidth()
            .background(CallScreenTheme.colors.surface.sunken)
            .windowInsetsPadding(windowInsets)
            // At least 64 dp, taller when a large font or a longer translation wraps the title.
            .heightIn(min = 64.dp)
            .padding(horizontal = 16.dp, vertical = 8.dp),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
        verticalAlignment = Alignment.CenterVertically
    ) {
        Row(
            // The title takes the room the avatar leaves, and wraps within it.
            modifier = Modifier.weight(1f),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(8.dp)
        ) {
            Icon(
                imageVector = Icons.Default.Shield,
                contentDescription = stringResource(R.string.core_ui_logo),
                tint = CallScreenTheme.colors.action.primary.fill,
                modifier = Modifier.size(28.dp)
            )
            Text(
                text = title,
                style = CallScreenTheme.typography.titleLarge,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary
            )
        }

        Box(
            modifier = Modifier
                .size(36.dp)
                .clip(CircleShape)
                .background(CallScreenTheme.colors.surface.raised),
            contentAlignment = Alignment.Center
        ) {
            Icon(
                imageVector = Icons.Default.Person,
                // Decorative: it opens nothing, so it is not announced as a control.
                contentDescription = null,
                tint = CallScreenTheme.colors.content.secondary,
                modifier = Modifier.size(20.dp)
            )
        }
    }
}

@Preview
@Composable
private fun AegisTopAppBarPreview() {
    CallScreenTheme {
        AegisTopAppBar()
    }
}
