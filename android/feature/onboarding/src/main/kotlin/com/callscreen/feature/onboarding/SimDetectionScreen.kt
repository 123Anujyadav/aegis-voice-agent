package com.callscreen.feature.onboarding

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.SimCard
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import android.telephony.TelephonyManager
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisPrimaryButton
import com.callscreen.core.ui.components.AegisStatusChip
import com.callscreen.core.ui.components.StatusChipVariant

/** A05 with the SIM as this phone reports it (no permission needed). */
@Composable
public fun SimDetectionRoute(onContinueClick: () -> Unit) {
    val context = LocalContext.current
    val sim = remember { SimSummary.of(context.getSystemService(TelephonyManager::class.java)) }
    SimDetectionScreen(onContinueClick = onContinueClick, sim = sim)
}

@Composable
public fun SimDetectionScreen(
    onContinueClick: () -> Unit = {},
    modifier: Modifier = Modifier,
    sim: SimSummary = SimSummary.Unknown,
) {
    Column(
        modifier = modifier
            .fillMaxSize()
            .background(CallScreenTheme.colors.surface.background)
            .padding(24.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.SpaceBetween
    ) {
        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(16.dp),
            modifier = Modifier.padding(top = 32.dp)
        ) {
            Icon(
                imageVector = Icons.Default.SimCard,
                contentDescription = "SIM Card",
                tint = CallScreenTheme.colors.action.primary.fill,
                modifier = Modifier.size(72.dp)
            )
            Text(
                text = "Your SIM",
                style = CallScreenTheme.typography.headlineMedium,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary
            )
            Text(
                text = "This is what your phone reports about its SIM.",
                style = CallScreenTheme.typography.bodyLarge,
                color = CallScreenTheme.colors.content.secondary
            )

            Spacer(modifier = Modifier.height(16.dp))

            Card(
                modifier = Modifier.fillMaxWidth(),
                shape = RoundedCornerShape(20.dp),
                colors = CardDefaults.cardColors(containerColor = CallScreenTheme.colors.surface.default)
            ) {
                Column(
                    modifier = Modifier.padding(20.dp),
                    verticalArrangement = Arrangement.spacedBy(12.dp)
                ) {
                    Row(
                        modifier = Modifier.fillMaxWidth(),
                        horizontalArrangement = Arrangement.SpaceBetween,
                        verticalAlignment = Alignment.CenterVertically
                    ) {
                        Text(
                            text = "SIM",
                            style = CallScreenTheme.typography.titleMedium,
                            fontWeight = FontWeight.Bold,
                            color = CallScreenTheme.colors.content.primary
                        )
                        AegisStatusChip(text = sim.status, variant = if (sim.ready) StatusChipVariant.Verified else StatusChipVariant.Fraud)
                    }
                    Text(
                        text = sim.carrierLine,
                        style = CallScreenTheme.typography.bodyMedium,
                        color = CallScreenTheme.colors.content.secondary
                    )
                    // Whether the carrier supports forwarding is only known once it is set up and checked (A08).
                    Text(
                        text = SimSummary.FORWARDING_UNKNOWN,
                        style = CallScreenTheme.typography.bodySmall,
                        color = CallScreenTheme.colors.content.secondary
                    )
                }
            }
        }

        AegisPrimaryButton(
            text = "Verify Phone Number",
            onClick = onContinueClick,
            modifier = Modifier.padding(bottom = 16.dp)
        )
    }
}

@Preview
@Composable
private fun SimDetectionScreenPreview() {
    CallScreenTheme {
        SimDetectionScreen()
    }
}
