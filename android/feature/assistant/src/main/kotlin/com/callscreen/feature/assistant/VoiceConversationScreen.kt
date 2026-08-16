package com.callscreen.feature.assistant

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisEmergencyButton
import com.callscreen.core.ui.components.AegisStatusChip
import com.callscreen.core.ui.components.AegisTopAppBar
import com.callscreen.core.ui.components.AegisVoiceOrb
import com.callscreen.core.ui.components.StatusChipVariant

@Composable
public fun VoiceConversationScreen(
    onEndSessionClick: () -> Unit = {},
    modifier: Modifier = Modifier
) {
    Scaffold(
        topBar = { AegisTopAppBar(title = "Interactive Voice Assistant") }
    ) { paddingValues ->
        Column(
            modifier = modifier
                .fillMaxSize()
                .background(CallScreenTheme.colors.surface.background)
                .padding(paddingValues)
                .padding(24.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.SpaceBetween
        ) {
            Column(
                horizontalAlignment = Alignment.CenterHorizontally,
                verticalArrangement = Arrangement.spacedBy(16.dp),
                modifier = Modifier.padding(top = 24.dp)
            ) {
                AegisStatusChip(text = "AI Listening...", variant = StatusChipVariant.AIHandled)
                Text(
                    text = "Say something to test Aegis response",
                    style = CallScreenTheme.typography.titleMedium,
                    fontWeight = FontWeight.Bold,
                    color = CallScreenTheme.colors.content.primary
                )
            }

            AegisVoiceOrb()

            AegisEmergencyButton(
                text = "End Voice Session",
                onClick = onEndSessionClick,
                modifier = Modifier.padding(bottom = 24.dp)
            )
        }
    }
}

@Preview
@Composable
private fun VoiceConversationScreenPreview() {
    CallScreenTheme {
        VoiceConversationScreen()
    }
}
