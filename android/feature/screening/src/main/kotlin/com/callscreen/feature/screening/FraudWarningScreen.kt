package com.callscreen.feature.screening

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Warning
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
import com.callscreen.core.ui.components.AegisEmergencyButton
import com.callscreen.core.ui.components.AegisThreatCard

@Composable
public fun FraudWarningScreen(
    callerName: String = "Unknown +1 (555) 019-2834",
    onTerminateAndBlockClick: () -> Unit = {},
    modifier: Modifier = Modifier
) {
    Column(
        modifier = modifier
            .fillMaxSize()
            .background(CallScreenTheme.colors.status.fraud.subtle)
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
                    .background(CallScreenTheme.colors.status.fraud.fill),
                contentAlignment = Alignment.Center
            ) {
                Icon(
                    imageVector = Icons.Default.Warning,
                    contentDescription = "Fraud Alert",
                    tint = CallScreenTheme.colors.status.fraud.subtle,
                    modifier = Modifier.size(56.dp)
                )
            }

            Text(
                text = "HIGH RISK FRAUD DETECTED",
                style = CallScreenTheme.typography.headlineSmall,
                fontWeight = FontWeight.Black,
                color = CallScreenTheme.colors.status.fraud.text
            )

            Text(
                text = callerName,
                style = CallScreenTheme.typography.titleLarge,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary
            )

            AegisThreatCard(
                threatTitle = "Bank OTP / Deepfake Scam",
                threatScore = 98,
                description = "AI voice engine matched suspicious audio vectors asking for banking PIN code."
            )
        }

        AegisEmergencyButton(
            text = "Terminate & Report Fraud",
            onClick = onTerminateAndBlockClick,
            modifier = Modifier.padding(bottom = 24.dp)
        )
    }
}

@Preview
@Composable
private fun FraudWarningScreenPreview() {
    CallScreenTheme {
        FraudWarningScreen()
    }
}
