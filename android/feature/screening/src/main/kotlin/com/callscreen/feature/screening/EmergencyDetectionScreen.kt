package com.callscreen.feature.screening

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.LocalHospital
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
import com.callscreen.core.ui.components.AegisPrimaryButton
import com.callscreen.core.ui.components.AegisStatusChip
import com.callscreen.core.ui.components.StatusChipVariant

@Composable
public fun EmergencyDetectionScreen(
    callerName: String = "Dr. Smith (City Hospital)",
    onConnectImmediatelyClick: () -> Unit = {},
    modifier: Modifier = Modifier
) {
    Column(
        modifier = modifier
            .fillMaxSize()
            .background(CallScreenTheme.colors.status.emergency.subtle)
            .padding(24.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.SpaceBetween
    ) {
        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(16.dp),
            modifier = Modifier.padding(top = 40.dp)
        ) {
            Box(
                modifier = Modifier
                    .size(96.dp)
                    .clip(CircleShape)
                    .background(CallScreenTheme.colors.status.emergency.fill),
                contentAlignment = Alignment.Center
            ) {
                Icon(
                    imageVector = Icons.Default.LocalHospital,
                    contentDescription = "Emergency",
                    tint = CallScreenTheme.colors.status.emergency.subtle,
                    modifier = Modifier.size(56.dp)
                )
            }

            AegisStatusChip(text = "EMERGENCY DETECTED", variant = StatusChipVariant.Emergency)

            Text(
                text = "Urgent Medical / Family Call",
                style = CallScreenTheme.typography.headlineMedium,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.status.emergency.text
            )

            Text(
                text = "Caller: $callerName",
                style = CallScreenTheme.typography.titleMedium,
                color = CallScreenTheme.colors.content.primary
            )

            Text(
                text = "AI recognized authentic emergency intent in transcript. Silent mode bypassed.",
                style = CallScreenTheme.typography.bodyLarge,
                color = CallScreenTheme.colors.content.secondary
            )
        }

        AegisPrimaryButton(
            text = "Connect Call Immediately",
            onClick = onConnectImmediatelyClick,
            modifier = Modifier.padding(bottom = 24.dp)
        )
    }
}

@Preview
@Composable
private fun EmergencyDetectionScreenPreview() {
    CallScreenTheme {
        EmergencyDetectionScreen()
    }
}
