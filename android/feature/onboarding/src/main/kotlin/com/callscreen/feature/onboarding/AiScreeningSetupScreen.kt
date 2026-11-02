package com.callscreen.feature.onboarding

import androidx.annotation.StringRes
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
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
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisPrimaryButton
import com.callscreen.core.ui.components.AegisScreenColumn
import com.callscreen.core.ui.components.AegisStatusChip
import com.callscreen.core.ui.components.StatusChipVariant

@Composable
public fun AiScreeningSetupScreen(
    onContinueClick: () -> Unit = {},
    modifier: Modifier = Modifier
) {
    var selectedPersona by remember { mutableStateOf(Persona.STRICT) }

    AegisScreenColumn(modifier = modifier) {
        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(16.dp),
            modifier = Modifier.padding(top = 24.dp)
        ) {
            Icon(
                imageVector = Icons.Default.Psychology,
                contentDescription = stringResource(R.string.onboarding_ai_icon_description),
                tint = CallScreenTheme.colors.status.ai.text,
                modifier = Modifier.size(64.dp)
            )
            Text(
                text = stringResource(R.string.onboarding_ai_title),
                style = CallScreenTheme.typography.headlineMedium,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary
            )
            Text(
                text = stringResource(R.string.onboarding_ai_intro),
                style = CallScreenTheme.typography.bodyLarge,
                color = CallScreenTheme.colors.content.secondary
            )

            Column(
                verticalArrangement = Arrangement.spacedBy(12.dp),
                modifier = Modifier.padding(top = 12.dp)
            ) {
                for (persona in Persona.entries) {
                    PersonaCard(
                        title = stringResource(persona.title),
                        description = stringResource(persona.description),
                        isSelected = selectedPersona == persona,
                        onSelect = { selectedPersona = persona },
                    )
                }
            }
        }

        AegisPrimaryButton(
            text = stringResource(R.string.onboarding_ai_save),
            onClick = onContinueClick,
            modifier = Modifier.padding(bottom = 16.dp)
        )
    }
}

/** The assistant's personas, in the order shown. Not saved anywhere yet. */
private enum class Persona(@StringRes val title: Int, @StringRes val description: Int) {
    STRICT(R.string.onboarding_ai_strict_title, R.string.onboarding_ai_strict),
    BALANCED(R.string.onboarding_ai_balanced_title, R.string.onboarding_ai_balanced),
    SILENT(R.string.onboarding_ai_silent_title, R.string.onboarding_ai_silent),
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
                AegisStatusChip(text = stringResource(R.string.onboarding_ai_selected), variant = StatusChipVariant.AIHandled)
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
