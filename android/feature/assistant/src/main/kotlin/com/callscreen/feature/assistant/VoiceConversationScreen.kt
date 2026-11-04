package com.callscreen.feature.assistant

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.consumeWindowInsets
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisEmergencyButton
import com.callscreen.core.ui.components.AegisScreenColumn
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
        topBar = { AegisTopAppBar(title = stringResource(R.string.assistant_session_screen_title)) }
    ) { paddingValues ->
        // Scrolls when the words do not fit: a large font, or a longer translation.
        AegisScreenColumn(modifier = modifier.padding(paddingValues).consumeWindowInsets(paddingValues)) {
            Column(
                horizontalAlignment = Alignment.CenterHorizontally,
                verticalArrangement = Arrangement.spacedBy(16.dp),
                modifier = Modifier.padding(top = 24.dp)
            ) {
                AegisStatusChip(text = stringResource(AssistantCopy.NOT_AVAILABLE), variant = StatusChipVariant.Fraud)
                Text(
                    text = stringResource(AssistantCopy.SESSION_UNAVAILABLE) + " " + stringResource(AssistantCopy.NO_MICROPHONE),
                    style = CallScreenTheme.typography.titleMedium,
                    fontWeight = FontWeight.Bold,
                    color = CallScreenTheme.colors.content.primary
                )
            }

            // Not listening: the microphone is never used here.
            AegisVoiceOrb(isListening = false)

            AegisEmergencyButton(
                text = stringResource(R.string.assistant_back),
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
