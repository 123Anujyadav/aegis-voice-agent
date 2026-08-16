package com.callscreen.feature.onboarding

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Check
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
public fun SetupCompleteScreen(
    onGoToHomeClick: () -> Unit = {},
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
            verticalArrangement = Arrangement.spacedBy(20.dp),
            modifier = Modifier.padding(top = 48.dp)
        ) {
            Box(
                modifier = Modifier
                    .size(96.dp)
                    .clip(CircleShape)
                    .background(CallScreenTheme.colors.status.success.subtle),
                contentAlignment = Alignment.Center
            ) {
                Icon(
                    imageVector = Icons.Default.Check,
                    contentDescription = "Success",
                    tint = CallScreenTheme.colors.status.success.text,
                    modifier = Modifier.size(56.dp)
                )
            }

            Text(
                text = "Setup Complete!",
                style = CallScreenTheme.typography.headlineLarge,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary
            )

            Text(
                text = "Aegis AI Call Protection is now active. Unknown calls will automatically be screened.",
                style = CallScreenTheme.typography.bodyLarge,
                color = CallScreenTheme.colors.content.secondary
            )

            AegisStatusChip(
                text = "Protection Active",
                variant = StatusChipVariant.Verified,
                modifier = Modifier.padding(top = 8.dp)
            )
        }

        AegisPrimaryButton(
            text = "Go to Calls Feed",
            onClick = onGoToHomeClick,
            modifier = Modifier.padding(bottom = 16.dp)
        )
    }
}

@Preview
@Composable
private fun SetupCompleteScreenPreview() {
    CallScreenTheme {
        SetupCompleteScreen()
    }
}
