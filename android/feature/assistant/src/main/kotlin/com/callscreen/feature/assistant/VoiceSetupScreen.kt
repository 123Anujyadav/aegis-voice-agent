package com.callscreen.feature.assistant

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisPrimaryButton
import com.callscreen.core.ui.components.AegisStatusChip
import com.callscreen.core.ui.components.AegisTopAppBar
import com.callscreen.core.ui.components.StatusChipVariant

@Composable
public fun VoiceSetupScreen(
    onSaveVoiceClick: () -> Unit = {},
    modifier: Modifier = Modifier
) {
    var selectedVoice by remember { mutableStateOf("ElevenLabs Flash - Calm Professional") }

    Scaffold(
        topBar = { AegisTopAppBar(title = "AI Voice Customization") }
    ) { paddingValues ->
        Column(
            modifier = modifier
                .fillMaxSize()
                .background(CallScreenTheme.colors.surface.background)
                .padding(paddingValues)
                .padding(16.dp),
            verticalArrangement = Arrangement.SpaceBetween
        ) {
            Column(
                verticalArrangement = Arrangement.spacedBy(12.dp)
            ) {
                Text(
                    text = "Select Voice Model",
                    style = CallScreenTheme.typography.titleLarge,
                    fontWeight = FontWeight.Bold,
                    color = CallScreenTheme.colors.content.primary
                )
                Text(
                    text = "Voice responses are generated using low-latency streaming TTS.",
                    style = CallScreenTheme.typography.bodyMedium,
                    color = CallScreenTheme.colors.content.secondary
                )

                VoiceOptionCard(
                    name = "ElevenLabs Flash - Calm Professional",
                    accent = "English & Hindi (Neutral Tone)",
                    isSelected = selectedVoice.contains("ElevenLabs"),
                    onSelect = { selectedVoice = "ElevenLabs Flash - Calm Professional" }
                )
                VoiceOptionCard(
                    name = "Sarvam Indic Voice - Authoritative",
                    accent = "Hindi Devanagari Native Accent",
                    isSelected = selectedVoice.contains("Sarvam"),
                    onSelect = { selectedVoice = "Sarvam Indic Voice - Authoritative" }
                )
                VoiceOptionCard(
                    name = "Cartesia Sonic - Friendly Receptionist",
                    accent = "Clear Indian English Accent",
                    isSelected = selectedVoice.contains("Cartesia"),
                    onSelect = { selectedVoice = "Cartesia Sonic - Friendly Receptionist" }
                )
            }

            AegisPrimaryButton(
                text = "Audition & Save Voice",
                onClick = onSaveVoiceClick,
                modifier = Modifier.padding(bottom = 16.dp)
            )
        }
    }
}

@Composable
private fun VoiceOptionCard(
    name: String,
    accent: String,
    isSelected: Boolean,
    onSelect: () -> Unit
) {
    Card(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onSelect),
        shape = RoundedCornerShape(16.dp),
        colors = CardDefaults.cardColors(
            containerColor = if (isSelected) CallScreenTheme.colors.status.ai.subtle else CallScreenTheme.colors.surface.default
        )
    ) {
        Row(
            modifier = Modifier.padding(16.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.SpaceBetween
        ) {
            Column(modifier = Modifier.weight(1f)) {
                Text(
                    text = name,
                    style = CallScreenTheme.typography.titleMedium,
                    fontWeight = FontWeight.Bold,
                    color = CallScreenTheme.colors.content.primary
                )
                Text(
                    text = accent,
                    style = CallScreenTheme.typography.bodyMedium,
                    color = CallScreenTheme.colors.content.secondary
                )
            }
            if (isSelected) {
                AegisStatusChip(text = "Active", variant = StatusChipVariant.AIHandled)
            }
        }
    }
}

@Preview
@Composable
private fun VoiceSetupScreenPreview() {
    CallScreenTheme {
        VoiceSetupScreen()
    }
}
