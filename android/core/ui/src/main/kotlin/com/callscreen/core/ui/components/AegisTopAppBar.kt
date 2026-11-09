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
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Person
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
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
 *
 * [onBack], when there is somewhere to go back to, shows a Back arrow at the
 * start (owner decision D6): by default the app navigation's own Back
 * ([LocalAegisBack]), so the arrow does exactly what Android's Back does.
 */
@Composable
public fun AegisTopAppBar(
    title: String = stringResource(R.string.core_ui_app_title),
    modifier: Modifier = Modifier,
    onProfileClick: () -> Unit = {},
    windowInsets: WindowInsets = WindowInsets.aegisSafeArea.only(WindowInsetsSides.Horizontal + WindowInsetsSides.Top),
    onBack: (() -> Unit)? = LocalAegisBack.current(),
) {
    Row(
        modifier = modifier
            .fillMaxWidth()
            .background(CallScreenTheme.colors.surface.default)
            .windowInsetsPadding(windowInsets)
            // At least 64 dp, taller when a large font or a longer translation wraps the title.
            .heightIn(min = 64.dp)
            // The arrow's own 48 dp target already keeps it 12 dp from the edge.
            .padding(start = if (onBack != null) 4.dp else 16.dp, end = 16.dp, top = 8.dp, bottom = 8.dp),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
        verticalAlignment = Alignment.CenterVertically
    ) {
        if (onBack != null) {
            // IconButton's touch target is 48 dp. The arrow mirrors in a right-to-left language.
            IconButton(onClick = onBack) {
                Icon(
                    imageVector = Icons.AutoMirrored.Filled.ArrowBack,
                    contentDescription = stringResource(R.string.core_ui_back),
                    tint = CallScreenTheme.colors.content.primary,
                )
            }
        }
        Row(
            // The title takes the room the avatar leaves, and wraps within it.
            modifier = Modifier.weight(1f),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(10.dp)
        ) {
            LilyLogo(size = 36.dp, contentDescription = stringResource(R.string.core_ui_logo))
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
                .background(CallScreenTheme.colors.surface.sunken),
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
