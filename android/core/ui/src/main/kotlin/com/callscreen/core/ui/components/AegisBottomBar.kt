package com.callscreen.core.ui.components

import androidx.compose.animation.animateColorAsState
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.WindowInsetsSides
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.only
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Call
import androidx.compose.material.icons.filled.Security
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.SmartToy
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.R

public enum class AegisTab {
    Calls,
    Protection,
    Assistant,
    Settings
}

/**
 * The tabs. The bar's colour reaches behind the navigation bar (the app draws
 * edge to edge); its tabs start above it and, in landscape, clear of a side
 * bar or cutout ([windowInsets]).
 */
@Composable
public fun AegisBottomBar(
    currentTab: AegisTab,
    onTabSelected: (AegisTab) -> Unit,
    modifier: Modifier = Modifier,
    windowInsets: WindowInsets = WindowInsets.aegisSafeArea.only(WindowInsetsSides.Horizontal + WindowInsetsSides.Bottom),
) {
    Row(
        modifier = modifier
            .fillMaxWidth()
            .background(CallScreenTheme.colors.surface.default)
            .windowInsetsPadding(windowInsets)
            // At least 80 dp, taller when a large font or a longer translation needs it.
            .heightIn(min = 80.dp)
            .padding(horizontal = 4.dp, vertical = 8.dp),
        horizontalArrangement = Arrangement.SpaceAround,
        verticalAlignment = Alignment.CenterVertically
    ) {
        AegisNavItem(
            // Each tab gets an equal share of the width, so a long label wraps instead of crowding the others.
            modifier = Modifier.weight(1f),
            tab = AegisTab.Calls,
            label = stringResource(R.string.core_ui_tab_calls),
            icon = Icons.Default.Call,
            isSelected = currentTab == AegisTab.Calls,
            onClick = { onTabSelected(AegisTab.Calls) }
        )
        AegisNavItem(
            // Each tab gets an equal share of the width, so a long label wraps instead of crowding the others.
            modifier = Modifier.weight(1f),
            tab = AegisTab.Protection,
            label = stringResource(R.string.core_ui_tab_protection),
            icon = Icons.Default.Security,
            isSelected = currentTab == AegisTab.Protection,
            onClick = { onTabSelected(AegisTab.Protection) }
        )
        AegisNavItem(
            // Each tab gets an equal share of the width, so a long label wraps instead of crowding the others.
            modifier = Modifier.weight(1f),
            tab = AegisTab.Assistant,
            label = stringResource(R.string.core_ui_tab_assistant),
            icon = Icons.Default.SmartToy,
            isSelected = currentTab == AegisTab.Assistant,
            onClick = { onTabSelected(AegisTab.Assistant) }
        )
        AegisNavItem(
            // Each tab gets an equal share of the width, so a long label wraps instead of crowding the others.
            modifier = Modifier.weight(1f),
            tab = AegisTab.Settings,
            label = stringResource(R.string.core_ui_tab_settings),
            icon = Icons.Default.Settings,
            isSelected = currentTab == AegisTab.Settings,
            onClick = { onTabSelected(AegisTab.Settings) }
        )
    }
}

@Composable
private fun AegisNavItem(
    tab: AegisTab,
    label: String,
    icon: ImageVector,
    isSelected: Boolean,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val containerColor by animateColorAsState(
        if (isSelected) CallScreenTheme.colors.action.secondary.fill else CallScreenTheme.colors.surface.default,
        label = "navContainerColor"
    )
    val contentColor by animateColorAsState(
        if (isSelected) CallScreenTheme.colors.action.primary.fill else CallScreenTheme.colors.content.secondary,
        label = "navContentColor"
    )

    // The tab's share of the bar; the highlighted pill inside fits its label.
    Box(modifier = modifier, contentAlignment = Alignment.Center) {
        Box(
            modifier = Modifier
                .clip(RoundedCornerShape(24.dp))
                .background(containerColor)
                // A tab, read with its selected state.
                .selectable(selected = isSelected, onClick = onClick, role = Role.Tab)
                .padding(horizontal = 12.dp, vertical = 8.dp),
            contentAlignment = Alignment.Center
        ) {
            Column(
                horizontalAlignment = Alignment.CenterHorizontally,
                verticalArrangement = Arrangement.Center
            ) {
                Icon(
                    imageVector = icon,
                    // Decorative: the label below names the tab once.
                    contentDescription = null,
                    tint = contentColor,
                    modifier = Modifier.size(24.dp)
                )
                Text(
                    text = label,
                    fontSize = 11.sp,
                    fontWeight = if (isSelected) FontWeight.Bold else FontWeight.Medium,
                    color = contentColor,
                    textAlign = TextAlign.Center,
                    modifier = Modifier.padding(top = 2.dp)
                )
            }
        }
    }
}

@Preview
@Composable
private fun AegisBottomBarPreview() {
    CallScreenTheme {
        AegisBottomBar(currentTab = AegisTab.Calls, onTabSelected = {})
    }
}
