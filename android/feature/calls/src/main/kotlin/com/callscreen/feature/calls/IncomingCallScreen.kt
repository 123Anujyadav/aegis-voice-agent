package com.callscreen.feature.calls

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Call
import androidx.compose.material.icons.filled.CallEnd
import androidx.compose.material.icons.filled.Psychology
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
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
import com.callscreen.core.ui.components.AegisStatusChip
import com.callscreen.core.ui.components.StatusChipVariant

@Composable
public fun IncomingCallScreen(
    callerName: String = "Unknown Caller",
    phoneNumber: String = "+1 (555) 234-5678",
    onScreenWithAiClick: () -> Unit = {},
    onAnswerClick: () -> Unit = {},
    onDeclineClick: () -> Unit = {},
    modifier: Modifier = Modifier
) {
    Column(
        modifier = modifier
            .fillMaxSize()
            .background(CallScreenTheme.colors.action.primary.fill)
            .padding(24.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.SpaceBetween
    ) {
        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(16.dp),
            modifier = Modifier.padding(top = 48.dp)
        ) {
            Box(
                modifier = Modifier
                    .size(96.dp)
                    .clip(CircleShape)
                    .background(CallScreenTheme.colors.surface.raised),
                contentAlignment = Alignment.Center
            ) {
                Icon(
                    imageVector = Icons.Default.Call,
                    contentDescription = "Incoming Call",
                    tint = CallScreenTheme.colors.action.primary.fill,
                    modifier = Modifier.size(48.dp)
                )
            }
            AegisStatusChip(text = "Incoming Call", variant = StatusChipVariant.AIHandled)
            Text(
                text = callerName,
                style = CallScreenTheme.typography.headlineLarge,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.inverse
            )
            Text(
                text = phoneNumber,
                style = CallScreenTheme.typography.titleMedium,
                color = CallScreenTheme.colors.content.inverse.copy(alpha = 0.8f)
            )
        }

        Column(
            verticalArrangement = Arrangement.spacedBy(16.dp),
            modifier = Modifier.padding(bottom = 32.dp)
        ) {
            Button(
                onClick = onScreenWithAiClick,
                modifier = Modifier
                    .fillMaxWidth()
                    .height(56.dp),
                shape = RoundedCornerShape(28.dp),
                colors = ButtonDefaults.buttonColors(
                    containerColor = CallScreenTheme.colors.status.ai.fill,
                    contentColor = CallScreenTheme.colors.status.ai.text
                )
            ) {
                Row(
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(8.dp)
                ) {
                    Icon(
                        imageVector = Icons.Default.Psychology,
                        contentDescription = "Screen",
                        modifier = Modifier.size(24.dp)
                    )
                    Text(
                        text = "Screen with Aegis AI",
                        style = CallScreenTheme.typography.titleMedium,
                        fontWeight = FontWeight.Bold
                    )
                }
            }

            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceEvenly
            ) {
                Button(
                    onClick = onDeclineClick,
                    modifier = Modifier.size(64.dp),
                    shape = CircleShape,
                    colors = ButtonDefaults.buttonColors(
                        containerColor = CallScreenTheme.colors.action.danger.fill
                    )
                ) {
                    Icon(
                        imageVector = Icons.Default.CallEnd,
                        contentDescription = "Decline",
                        tint = CallScreenTheme.colors.action.danger.content
                    )
                }

                Button(
                    onClick = onAnswerClick,
                    modifier = Modifier.size(64.dp),
                    shape = CircleShape,
                    colors = ButtonDefaults.buttonColors(
                        containerColor = CallScreenTheme.colors.status.success.fill
                    )
                ) {
                    Icon(
                        imageVector = Icons.Default.Call,
                        contentDescription = "Answer",
                        tint = CallScreenTheme.colors.status.success.text
                    )
                }
            }
        }
    }
}

@Preview
@Composable
private fun IncomingCallScreenPreview() {
    CallScreenTheme {
        IncomingCallScreen()
    }
}
