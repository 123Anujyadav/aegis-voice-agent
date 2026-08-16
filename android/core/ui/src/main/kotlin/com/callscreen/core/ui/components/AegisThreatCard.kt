package com.callscreen.core.ui.components

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme

@Composable
public fun AegisThreatCard(
    threatTitle: String,
    threatScore: Int,
    description: String,
    modifier: Modifier = Modifier
) {
    val isHighRisk = threatScore >= 70
    val isMediumRisk = threatScore in 30..69

    val cardBg = when {
        isHighRisk -> CallScreenTheme.colors.status.fraud.subtle
        isMediumRisk -> CallScreenTheme.colors.status.spam.subtle
        else -> CallScreenTheme.colors.status.success.subtle
    }

    val textColor = when {
        isHighRisk -> CallScreenTheme.colors.status.fraud.text
        isMediumRisk -> CallScreenTheme.colors.status.spam.text
        else -> CallScreenTheme.colors.status.success.text
    }

    Card(
        modifier = modifier.fillMaxWidth(),
        shape = RoundedCornerShape(20.dp),
        colors = CardDefaults.cardColors(containerColor = cardBg)
    ) {
        Column(
            modifier = Modifier.padding(20.dp),
            verticalArrangement = Arrangement.spacedBy(8.dp)
        ) {
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically
            ) {
                Text(
                    text = threatTitle,
                    style = CallScreenTheme.typography.titleLarge,
                    fontWeight = FontWeight.Bold,
                    color = textColor
                )
                Text(
                    text = "$threatScore%",
                    style = CallScreenTheme.typography.headlineMedium,
                    fontWeight = FontWeight.Black,
                    color = textColor
                )
            }
            Text(
                text = description,
                style = CallScreenTheme.typography.bodyMedium,
                color = CallScreenTheme.colors.content.primary
            )
        }
    }
}

@Preview
@Composable
private fun AegisThreatCardPreview() {
    CallScreenTheme {
        AegisThreatCard(
            threatTitle = "Bank Impersonation Scam",
            threatScore = 92,
            description = "AI detected suspicious request for OTP code and spoofed caller origin."
        )
    }
}
