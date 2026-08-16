package com.callscreen.core.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.CallMade
import androidx.compose.material.icons.automirrored.filled.CallReceived
import androidx.compose.material.icons.filled.Call

import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme

public enum class CallType {
    Incoming,
    Outgoing,
    ScreenedAI,
    BlockedSpam
}

@Composable
public fun AegisCallCard(
    callerName: String,
    phoneNumber: String,
    timestamp: String,
    callType: CallType,
    statusText: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier
) {
    Card(
        modifier = modifier
            .fillMaxWidth()
            .clickable(onClick = onClick),
        shape = RoundedCornerShape(16.dp),
        colors = CardDefaults.cardColors(
            containerColor = CallScreenTheme.colors.surface.default
        ),
        elevation = CardDefaults.cardElevation(defaultElevation = 2.dp)
    ) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(16.dp),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically
        ) {
            Row(
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(12.dp)
            ) {
                Box(
                    modifier = Modifier
                        .size(44.dp)
                        .clip(CircleShape)
                        .background(
                            when (callType) {
                                CallType.BlockedSpam -> CallScreenTheme.colors.status.spam.subtle
                                CallType.ScreenedAI -> CallScreenTheme.colors.status.ai.subtle
                                else -> CallScreenTheme.colors.surface.sunken
                            }
                        ),
                    contentAlignment = Alignment.Center
                ) {
                    Icon(
                        imageVector = when (callType) {
                            CallType.Outgoing -> Icons.AutoMirrored.Filled.CallMade
                            CallType.Incoming -> Icons.AutoMirrored.Filled.CallReceived
                            else -> Icons.Default.Call
                        },

                        contentDescription = "Call Icon",
                        tint = when (callType) {
                            CallType.BlockedSpam -> CallScreenTheme.colors.status.spam.text
                            CallType.ScreenedAI -> CallScreenTheme.colors.status.ai.text
                            else -> CallScreenTheme.colors.action.primary.fill
                        },
                        modifier = Modifier.size(20.dp)
                    )
                }

                Column {
                    Text(
                        text = callerName,
                        style = CallScreenTheme.typography.titleMedium,
                        fontWeight = FontWeight.Bold,
                        color = CallScreenTheme.colors.content.primary
                    )
                    Text(
                        text = phoneNumber,
                        style = CallScreenTheme.typography.bodyMedium,
                        color = CallScreenTheme.colors.content.secondary
                    )
                }
            }

            Column(
                horizontalAlignment = Alignment.End
            ) {
                Text(
                    text = timestamp,
                    style = CallScreenTheme.typography.bodySmall,
                    color = CallScreenTheme.colors.content.tertiary
                )
                AegisStatusChip(
                    text = statusText,
                    variant = when (callType) {
                        CallType.BlockedSpam -> StatusChipVariant.Spam
                        CallType.ScreenedAI -> StatusChipVariant.AIHandled
                        else -> StatusChipVariant.Verified
                    },
                    modifier = Modifier.padding(top = 4.dp)
                )
            }
        }
    }
}

@Preview
@Composable
private fun AegisCallCardPreview() {
    CallScreenTheme {
        AegisCallCard(
            callerName = "+1 (555) 234-5678",
            phoneNumber = "Unknown Caller",
            timestamp = "10:42 AM",
            callType = CallType.ScreenedAI,
            statusText = "AI Screened",
            onClick = {}
        )
    }
}
