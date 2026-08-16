package com.callscreen.feature.onboarding

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Psychology
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
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
public fun AiScreeningSetupScreen(
    onContinueClick: () -> Unit = {},
    modifier: Modifier = Modifier
) {
    var selectedPersona by remember { mutableStateOf("Strict Protection") }

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
                imageVector = Icons.Default.Psychology,
                contentDescription = "AI Persona",
                tint = CallScreenTheme.colors.status.ai.text,
                modifier = Modifier.size(64.dp)
            )
            Text(
                text = "AI Screening Persona",
                style = CallScreenTheme.typography.headlineMedium,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary
            )
            Text(
                text = "Configure how Aegis AI interacts with unknown callers when screening.",
                style = CallScreenTheme.typography.bodyLarge,
                color = CallScreenTheme.colors.content.secondary
            )

            Column(
                verticalArrangement = Arrangement.spacedBy(12.dp),
                modifier = Modifier.padding(top = 12.dp)
            ) {
                PersonaCard(
                    title = "Strict Protection",
                    description = "Screens all unknown numbers immediately. High fraud sensitivity.",
                    isSelected = selectedPersona == "Strict Protection",
                    onSelect = { selectedPersona = "Strict Protection" }
                )
                PersonaCard(
                    title = "Balanced Receptionist",
                    description = "Politely asks purpose of call and takes messages for non-contacts.",
                    isSelected = selectedPersona == "Balanced Receptionist",
                    onSelect = { selectedPersona = "Balanced Receptionist" }
                )
                PersonaCard(
                    title = "Silent Assistant",
                    description = "Transcribes live calls without speaking unless scam patterns trigger.",
                    isSelected = selectedPersona == "Silent Assistant",
                    onSelect = { selectedPersona = "Silent Assistant" }
                )
            }
        }

        AegisPrimaryButton(
            text = "Save & Continue",
            onClick = onContinueClick,
            modifier = Modifier.padding(bottom = 16.dp)
        )
    }
}

@Composable
private fun PersonaCard(
    title: String,
    description: String,
    isSelected: Boolean,
    onSelect: () -> Unit
) {
    Card(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onSelect),
        shape = RoundedCornerShape(16.dp),
        colors = CardDefaults.cardColors(
            containerColor = if (isSelected) CallScreenTheme.colors.status.ai.subtle else CallScreenTheme.colors.surface.default
        )
    ) {
        Row(
            modifier = Modifier.padding(16.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.SpaceBetween
        ) {
            Column(modifier = Modifier.weight(1f)) {
                Text(
                    text = title,
                    style = CallScreenTheme.typography.titleMedium,
                    fontWeight = FontWeight.Bold,
                    color = CallScreenTheme.colors.content.primary
                )
                Text(
                    text = description,
                    style = CallScreenTheme.typography.bodyMedium,
                    color = CallScreenTheme.colors.content.secondary
                )
            }
            if (isSelected) {
                AegisStatusChip(text = "Selected", variant = StatusChipVariant.AIHandled)
            }
        }
    }
}

@Preview
@Composable
private fun AiScreeningSetupScreenPreview() {
    CallScreenTheme {
        AiScreeningSetupScreen()
    }
}
