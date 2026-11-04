package com.callscreen.feature.assistant

import androidx.annotation.StringRes
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.WindowInsets
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
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisPrimaryButton
import com.callscreen.core.ui.components.AegisScreenColumn
import com.callscreen.core.ui.components.AegisTopAppBar

/**
 * A voice planned for the assistant (ADR-0005, ADR-0007): its product name,
 * which is not translated, its style and its accent. None can be chosen or
 * saved yet.
 */
internal enum class PlannedVoice(
    @StringRes val product: Int,
    @StringRes val style: Int,
    @StringRes val accent: Int,
) {
    ELEVENLABS(R.string.assistant_voice_elevenlabs, R.string.assistant_voice_elevenlabs_style, R.string.assistant_voice_elevenlabs_accent),
    SARVAM(R.string.assistant_voice_sarvam, R.string.assistant_voice_sarvam_style, R.string.assistant_voice_sarvam_accent),
    CARTESIA(R.string.assistant_voice_cartesia, R.string.assistant_voice_cartesia_style, R.string.assistant_voice_cartesia_accent),
}

@Composable
public fun VoiceSetupScreen(
    onSaveVoiceClick: () -> Unit = {},
    modifier: Modifier = Modifier
) {
    var selectedVoice by remember { mutableStateOf(PlannedVoice.ELEVENLABS) }

    Scaffold(
        topBar = { AegisTopAppBar(title = stringResource(R.string.assistant_voice_setup_title)) }
    ) { paddingValues ->
        // Scrolls when the voices do not fit: a large font, or a longer translation (F18).
        AegisScreenColumn(
            modifier = modifier.padding(paddingValues),
            // The Scaffold has made room for the system bars (AegisScreenColumn).
            windowInsets = WindowInsets(0, 0, 0, 0),
            horizontalAlignment = Alignment.Start,
            contentPadding = 16.dp,
        ) {
            Column(
                verticalArrangement = Arrangement.spacedBy(12.dp)
            ) {
                Text(
                    text = stringResource(R.string.assistant_voice_select),
                    style = CallScreenTheme.typography.titleLarge,
                    fontWeight = FontWeight.Bold,
                    color = CallScreenTheme.colors.content.primary
                )
                Text(
                    text = stringResource(AssistantCopy.VOICES_INTRO),
                    style = CallScreenTheme.typography.bodyMedium,
                    color = CallScreenTheme.colors.content.secondary
                )

                for (voice in PlannedVoice.entries) {
                    VoiceOptionCard(
                        name = stringResource(R.string.assistant_voice_option, stringResource(voice.product), stringResource(voice.style)),
                        accent = stringResource(voice.accent),
                        isSelected = selectedVoice == voice,
                        onSelect = { selectedVoice = voice }
                    )
                }
            }

            AegisPrimaryButton(
                text = stringResource(AssistantCopy.SAVE_UNAVAILABLE),
                onClick = onSaveVoiceClick,
                enabled = false,
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
