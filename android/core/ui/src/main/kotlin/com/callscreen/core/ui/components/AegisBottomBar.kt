package com.callscreen.core.ui.components

import androidx.compose.animation.animateColorAsState
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
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
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.callscreen.core.designsystem.theme.CallScreenTheme

public enum class AegisTab {
    Calls,
    Protection,
    Assistant,
    Settings
}

@Composable
public fun AegisBottomBar(
    currentTab: AegisTab,
    onTabSelected: (AegisTab) -> Unit,
    modifier: Modifier = Modifier
) {
    Row(
        modifier = modifier
            .fillMaxWidth()
            .height(80.dp)
            .background(CallScreenTheme.colors.surface.default)
            .padding(horizontal = 8.dp, vertical = 8.dp),
        horizontalArrangement = Arrangement.SpaceAround,
        verticalAlignment = Alignment.CenterVertically
    ) {
        AegisNavItem(
            tab = AegisTab.Calls,
            label = "Calls",
            icon = Icons.Default.Call,
            isSelected = currentTab == AegisTab.Calls,
            onClick = { onTabSelected(AegisTab.Calls) }
        )
        AegisNavItem(
            tab = AegisTab.Protection,
            label = "Protection",
            icon = Icons.Default.Security,
            isSelected = currentTab == AegisTab.Protection,
            onClick = { onTabSelected(AegisTab.Protection) }
        )
        AegisNavItem(
            tab = AegisTab.Assistant,
            label = "Assistant",
            icon = Icons.Default.SmartToy,
            isSelected = currentTab == AegisTab.Assistant,
            onClick = { onTabSelected(AegisTab.Assistant) }
        )
        AegisNavItem(
            tab = AegisTab.Settings,
            label = "Settings",
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
    onClick: () -> Unit
) {
    val containerColor by animateColorAsState(
        if (isSelected) CallScreenTheme.colors.action.secondary.fill else CallScreenTheme.colors.surface.default,
        label = "navContainerColor"
    )
    val contentColor by animateColorAsState(
        if (isSelected) CallScreenTheme.colors.action.primary.fill else CallScreenTheme.colors.content.secondary,
        label = "navContentColor"
    )

    Box(
        modifier = Modifier
            .clip(RoundedCornerShape(24.dp))
            .background(containerColor)
            .clickable(onClick = onClick)
            .padding(horizontal = 16.dp, vertical = 8.dp),
        contentAlignment = Alignment.Center
    ) {
        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.Center
        ) {
            Icon(
                imageVector = icon,
                contentDescription = label,
                tint = contentColor,
                modifier = Modifier.size(24.dp)
            )
            Text(
                text = label,
                fontSize = 11.sp,
                fontWeight = if (isSelected) FontWeight.Bold else FontWeight.Medium,
                color = contentColor,
                modifier = Modifier.padding(top = 2.dp)
            )
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
