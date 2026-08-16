package com.callscreen.core.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme

@Composable
public fun AegisTranscriptBubble(
    sender: String,
    message: String,
    timestamp: String,
    isAiAssistant: Boolean,
    modifier: Modifier = Modifier
) {
    val alignment = if (isAiAssistant) Alignment.End else Alignment.Start
    val bubbleBg = if (isAiAssistant) CallScreenTheme.colors.status.ai.subtle else CallScreenTheme.colors.surface.sunken
    val textColor = if (isAiAssistant) CallScreenTheme.colors.status.ai.text else CallScreenTheme.colors.content.primary

    Column(
        modifier = modifier
            .fillMaxWidth()
            .padding(vertical = 4.dp),
        horizontalAlignment = alignment
    ) {
        Text(
            text = sender,
            style = CallScreenTheme.typography.bodySmall,
            fontWeight = FontWeight.Bold,
            color = CallScreenTheme.colors.content.secondary,
            modifier = Modifier.padding(horizontal = 4.dp, vertical = 2.dp)
        )
        Box(
            modifier = Modifier
                .clip(
                    RoundedCornerShape(
                        topStart = 16.dp,
                        topEnd = 16.dp,
                        bottomStart = if (isAiAssistant) 16.dp else 4.dp,
                        bottomEnd = if (isAiAssistant) 4.dp else 16.dp
                    )
                )
                .background(bubbleBg)
                .padding(14.dp)
        ) {
            Column {
                Text(
                    text = message,
                    style = CallScreenTheme.typography.bodyMedium,
                    color = textColor
                )
                Text(
                    text = timestamp,
                    style = CallScreenTheme.typography.labelSmall,
                    color = CallScreenTheme.colors.content.tertiary,
                    modifier = Modifier.align(Alignment.End).padding(top = 4.dp)
                )
            }
        }
    }
}

@Preview
@Composable
private fun AegisTranscriptBubblePreview() {
    CallScreenTheme {
        AegisTranscriptBubble(
            sender = "Aegis AI Assistant",
            message = "Hello, I am screening this call for Abhik. Please state your name and purpose of calling.",
            timestamp = "10:43:12 AM",
            isAiAssistant = true
        )
    }
}
