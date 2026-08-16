package com.callscreen.feature.protection

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Block
import androidx.compose.material.icons.filled.Psychology
import androidx.compose.material.icons.filled.Security
import androidx.compose.material.icons.filled.Shield
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisBottomBar
import com.callscreen.core.ui.components.AegisStatusChip
import com.callscreen.core.ui.components.AegisTab
import com.callscreen.core.ui.components.AegisThreatCard
import com.callscreen.core.ui.components.AegisTopAppBar
import com.callscreen.core.ui.components.StatusChipVariant

@Composable
public fun ProtectionDashboardScreen(
    onTabSelected: (AegisTab) -> Unit = {},
    modifier: Modifier = Modifier
) {
    Scaffold(
        topBar = { AegisTopAppBar(title = "Protection Dashboard") },
        bottomBar = { AegisBottomBar(currentTab = AegisTab.Protection, onTabSelected = onTabSelected) }
    ) { paddingValues ->
        LazyColumn(
            modifier = modifier
                .fillMaxSize()
                .background(CallScreenTheme.colors.surface.background)
                .padding(paddingValues)
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp)
        ) {
            item {
                Card(
                    modifier = Modifier.fillMaxWidth(),
                    shape = RoundedCornerShape(24.dp),
                    colors = CardDefaults.cardColors(containerColor = CallScreenTheme.colors.action.primary.fill)
                ) {
                    Column(
                        modifier = Modifier.padding(20.dp),
                        verticalArrangement = Arrangement.spacedBy(12.dp)
                    ) {
                        Row(
                            modifier = Modifier.fillMaxWidth(),
                            horizontalArrangement = Arrangement.SpaceBetween,
                            verticalAlignment = Alignment.CenterVertically
                        ) {
                            Row(
                                verticalAlignment = Alignment.CenterVertically,
                                horizontalArrangement = Arrangement.spacedBy(8.dp)
                            ) {
                                Icon(
                                    imageVector = Icons.Default.Shield,
                                    contentDescription = null,
                                    tint = CallScreenTheme.colors.status.success.text,
                                    modifier = Modifier.size(28.dp)
                                )
                                Text(
                                    text = "Aegis Score 98/100",
                                    style = CallScreenTheme.typography.titleLarge,
                                    fontWeight = FontWeight.Bold,
                                    color = CallScreenTheme.colors.content.inverse
                                )
                            }
                            AegisStatusChip(text = "Optimal", variant = StatusChipVariant.Verified)
                        }

                        Text(
                            text = "Active Protection Shield running on-device. Zero unverified scam breaches detected.",
                            style = CallScreenTheme.typography.bodyMedium,
                            color = CallScreenTheme.colors.content.inverse.copy(alpha = 0.8f)
                        )
                    }
                }
            }

            item {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.spacedBy(12.dp)
                ) {
                    StatMetricCard(
                        title = "Spam Blocked",
                        value = "142",
                        icon = Icons.Default.Block,
                        modifier = Modifier.weight(1f)
                    )
                    StatMetricCard(
                        title = "AI Screenings",
                        value = "89",
                        icon = Icons.Default.Psychology,
                        modifier = Modifier.weight(1f)
                    )
                }
            }

            item {
                Text(
                    text = "Live Threat Intelligence Radar",
                    style = CallScreenTheme.typography.titleMedium,
                    fontWeight = FontWeight.Bold,
                    color = CallScreenTheme.colors.content.primary
                )
            }

            item {
                AegisThreatCard(
                    threatTitle = "Active Bank OTP Phishing Campaign",
                    threatScore = 94,
                    description = "Widespread scam targeting India accounts via spoofed Mumbai numbers. 412 reports in last 24h."
                )
            }
        }
    }
}

@Composable
private fun StatMetricCard(
    title: String,
    value: String,
    icon: ImageVector,
    modifier: Modifier = Modifier
) {
    Card(
        modifier = modifier,
        shape = RoundedCornerShape(16.dp),
        colors = CardDefaults.cardColors(containerColor = CallScreenTheme.colors.surface.default)
    ) {
        Column(
            modifier = Modifier.padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(8.dp)
        ) {
            Box(
                modifier = Modifier
                    .size(36.dp)
                    .clip(CircleShape)
                    .background(CallScreenTheme.colors.action.secondary.fill),
                contentAlignment = Alignment.Center
            ) {
                Icon(
                    imageVector = icon,
                    contentDescription = title,
                    tint = CallScreenTheme.colors.action.primary.fill,
                    modifier = Modifier.size(20.dp)
                )
            }
            Text(
                text = value,
                style = CallScreenTheme.typography.headlineMedium,
                fontWeight = FontWeight.Black,
                color = CallScreenTheme.colors.content.primary
            )
            Text(
                text = title,
                style = CallScreenTheme.typography.bodySmall,
                color = CallScreenTheme.colors.content.secondary
            )
        }
    }
}

@Preview
@Composable
private fun ProtectionDashboardScreenPreview() {
    CallScreenTheme {
        ProtectionDashboardScreen()
    }
}
