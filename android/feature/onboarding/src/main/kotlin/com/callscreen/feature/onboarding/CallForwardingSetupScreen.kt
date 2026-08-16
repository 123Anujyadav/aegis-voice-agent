package com.callscreen.feature.onboarding

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.PhoneForwarded
import androidx.compose.material.icons.filled.ContentCopy

import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisPrimaryButton
import com.callscreen.core.ui.components.AegisStatusChip
import com.callscreen.core.ui.components.StatusChipVariant

@Composable
public fun CallForwardingSetupScreen(
    onActivateForwardingClick: () -> Unit = {},
    modifier: Modifier = Modifier
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
            modifier = Modifier.padding(top = 24.dp)
        ) {
            Icon(
                imageVector = Icons.AutoMirrored.Filled.PhoneForwarded,
                contentDescription = "Forwarding Setup",
                tint = CallScreenTheme.colors.action.primary.fill,
                modifier = Modifier.size(64.dp)
            )

            Text(
                text = "Carrier Call Forwarding",
                style = CallScreenTheme.typography.headlineMedium,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary
            )
            Text(
                text = "Enable conditional call forwarding (*71 / *67 MMI code) so unanswered calls route to Aegis AI.",
                style = CallScreenTheme.typography.bodyLarge,
                color = CallScreenTheme.colors.content.secondary
            )

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
                            text = "Carrier Dial Code",
                            style = CallScreenTheme.typography.titleMedium,
                            fontWeight = FontWeight.Bold,
                            color = CallScreenTheme.colors.content.primary
                        )
                        AegisStatusChip(text = "Jio / Airtel", variant = StatusChipVariant.Verified)
                    }
                    Text(
                        text = "*71*+911140008899#",
                        style = CallScreenTheme.typography.headlineSmall,
                        fontWeight = FontWeight.Bold,
                        color = CallScreenTheme.colors.action.primary.fill
                    )
                    Row(
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.spacedBy(8.dp)
                    ) {
                        Icon(
                            imageVector = Icons.Default.ContentCopy,
                            contentDescription = "Copy Code",
                            tint = CallScreenTheme.colors.content.tertiary,
                            modifier = Modifier.size(16.dp)
                        )
                        Text(
                            text = "Tap button below to dial and activate automatically.",
                            style = CallScreenTheme.typography.bodySmall,
                            color = CallScreenTheme.colors.content.secondary
                        )
                    }
                }
            }
        }

        AegisPrimaryButton(
            text = "Activate Call Forwarding",
            onClick = onActivateForwardingClick,
            modifier = Modifier.padding(bottom = 16.dp)
        )
    }
}

@Preview
@Composable
private fun CallForwardingSetupScreenPreview() {
    CallScreenTheme {
        CallForwardingSetupScreen()
    }
}
