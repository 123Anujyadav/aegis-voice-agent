package com.callscreen.feature.screening

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisStatusChip
import com.callscreen.core.ui.components.AegisTranscriptBubble
import com.callscreen.core.ui.components.AegisVoiceOrb
import com.callscreen.core.ui.components.StatusChipVariant

public data class TranscriptItem(
    val sender: String,
    val text: String,
    val timestamp: String,
    val isAi: Boolean
)

@Composable
public fun LiveAiScreeningScreen(
    callerName: String = "Unknown Caller (+1 555-234-5678)",
    onTakeOverClick: () -> Unit = {},
    onEndCallClick: () -> Unit = {},
    modifier: Modifier = Modifier
) {
    val sampleTranscript = remember {
        listOf(
            TranscriptItem("Aegis AI Assistant", "Hello, I am screening this call for Abhik. Please state your name and purpose of calling.", "10:42:01", true),
            TranscriptItem("Caller", "Hi, I am calling from National Bank regarding an urgent security update on your account.", "10:42:05", false),
            TranscriptItem("Aegis AI Assistant", "Please verify your caller ID or department reference code.", "10:42:10", true)
        )
    }

    Column(
        modifier = modifier
            .fillMaxSize()
            .background(CallScreenTheme.colors.surface.background)
            .padding(16.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.SpaceBetween
    ) {
        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(8.dp)
        ) {
            AegisStatusChip(text = "Live AI Screening Active", variant = StatusChipVariant.AIHandled)
            Text(
                text = callerName,
                style = CallScreenTheme.typography.titleLarge,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary
            )
        }

        Card(
            modifier = Modifier
                .fillMaxWidth()
                .weight(1f)
                .padding(vertical = 12.dp),
            shape = RoundedCornerShape(20.dp),
            colors = CardDefaults.cardColors(containerColor = CallScreenTheme.colors.surface.default)
        ) {
            LazyColumn(
                modifier = Modifier
                    .fillMaxSize()
                    .padding(16.dp),
                verticalArrangement = Arrangement.spacedBy(8.dp)
            ) {
                items(sampleTranscript) { item ->
                    AegisTranscriptBubble(
                        sender = item.sender,
                        message = item.text,
                        timestamp = item.timestamp,
                        isAiAssistant = item.isAi
                    )
                }
            }
        }

        AegisVoiceOrb(modifier = Modifier.padding(vertical = 8.dp))

        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(bottom = 12.dp),
            horizontalArrangement = Arrangement.spacedBy(12.dp)
        ) {
            Button(
                onClick = onTakeOverClick,
                modifier = Modifier
                    .weight(1f)
                    .padding(vertical = 4.dp),
                shape = RoundedCornerShape(24.dp),
                colors = ButtonDefaults.buttonColors(containerColor = CallScreenTheme.colors.action.primary.fill)
            ) {
                Text("Take Over Call", fontWeight = FontWeight.Bold)
            }

            Button(
                onClick = onEndCallClick,
                modifier = Modifier
                    .weight(1f)
                    .padding(vertical = 4.dp),
                shape = RoundedCornerShape(24.dp),
                colors = ButtonDefaults.buttonColors(containerColor = CallScreenTheme.colors.action.danger.fill)
            ) {
                Text("End & Block", fontWeight = FontWeight.Bold)
            }
        }
    }
}

@Preview
@Composable
private fun LiveAiScreeningScreenPreview() {
    CallScreenTheme {
        LiveAiScreeningScreen()
    }
}
