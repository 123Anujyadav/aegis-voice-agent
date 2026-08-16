package com.callscreen.feature.assistant

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
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
import androidx.compose.material.icons.filled.Mic
import androidx.compose.material.icons.filled.RecordVoiceOver
import androidx.compose.material.icons.filled.SmartToy
import androidx.compose.material.icons.filled.Tune
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Switch
import androidx.compose.material3.SwitchDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
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
import com.callscreen.core.ui.components.AegisTopAppBar
import com.callscreen.core.ui.components.StatusChipVariant

@Composable
public fun AssistantHomeScreen(
    onVoiceSetupClick: () -> Unit = {},
    onStartVoiceChatClick: () -> Unit = {},
    onTabSelected: (AegisTab) -> Unit = {},
    modifier: Modifier = Modifier
) {
    var isReceptionistEnabled by remember { mutableStateOf(true) }

    Scaffold(
        topBar = { AegisTopAppBar(title = "AI Receptionist Settings") },
        bottomBar = { AegisBottomBar(currentTab = AegisTab.Assistant, onTabSelected = onTabSelected) }
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
                    shape = RoundedCornerShape(20.dp),
                    colors = CardDefaults.cardColors(containerColor = CallScreenTheme.colors.surface.default)
                ) {
                    Row(
                        modifier = Modifier
                            .fillMaxWidth()
                            .padding(20.dp),
                        horizontalArrangement = Arrangement.SpaceBetween,
                        verticalAlignment = Alignment.CenterVertically
                    ) {
                        Column {
                            Text(
                                text = "Autonomous Screening",
                                style = CallScreenTheme.typography.titleMedium,
                                fontWeight = FontWeight.Bold,
                                color = CallScreenTheme.colors.content.primary
                            )
                            Text(
                                text = "AI will answer unknown calls on your behalf",
                                style = CallScreenTheme.typography.bodyMedium,
                                color = CallScreenTheme.colors.content.secondary
                            )
                        }
                        Switch(
                            checked = isReceptionistEnabled,
                            onCheckedChange = { isReceptionistEnabled = it },
                            colors = SwitchDefaults.colors(
                                checkedThumbColor = CallScreenTheme.colors.action.primary.content,
                                checkedTrackColor = CallScreenTheme.colors.action.primary.fill
                            )
                        )
                    }
                }
            }

            item {
                Text(
                    text = "Customization & Voice Controls",
                    style = CallScreenTheme.typography.titleMedium,
                    fontWeight = FontWeight.Bold,
                    color = CallScreenTheme.colors.content.primary
                )
            }

            item {
                AssistantSettingCard(
                    icon = Icons.Default.RecordVoiceOver,
                    title = "AI Voice & Pitch Setup",
                    description = "Choose voice model (ElevenLabs / Cartesia) and tone.",
                    onClick = onVoiceSetupClick
                )
            }

            item {
                AssistantSettingCard(
                    icon = Icons.Default.Mic,
                    title = "Interactive Voice Session",
                    description = "Test screening prompts in real-time voice chat mode.",
                    onClick = onStartVoiceChatClick
                )
            }
        }
    }
}

@Composable
private fun AssistantSettingCard(
    icon: ImageVector,
    title: String,
    description: String,
    onClick: () -> Unit
) {
    Card(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onClick),
        shape = RoundedCornerShape(16.dp),
        colors = CardDefaults.cardColors(containerColor = CallScreenTheme.colors.surface.default)
    ) {
        Row(
            modifier = Modifier.padding(16.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(16.dp)
        ) {
            Box(
                modifier = Modifier
                    .size(44.dp)
                    .clip(CircleShape)
                    .background(CallScreenTheme.colors.status.ai.subtle),
                contentAlignment = Alignment.Center
            ) {
                Icon(
                    imageVector = icon,
                    contentDescription = title,
                    tint = CallScreenTheme.colors.status.ai.text,
                    modifier = Modifier.size(24.dp)
                )
            }
            Column {
                Text(
                    text = title,
                    style = CallScreenTheme.typography.titleMedium,
                    fontWeight = FontWeight.Bold,
                    color = CallScreenTheme.colors.content.primary
                )
                Text(
                    text = description,
                    style = CallScreenTheme.typography.bodyMedium,
                    color = CallScreenTheme.colors.content.secondary
                )
            }
        }
    }
}

@Preview
@Composable
private fun AssistantHomeScreenPreview() {
    CallScreenTheme {
        AssistantHomeScreen()
    }
}
