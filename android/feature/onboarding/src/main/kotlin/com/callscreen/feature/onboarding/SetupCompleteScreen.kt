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

@Composable
public fun SetupCompleteScreen(
    onGoToHomeClick: () -> Unit = {},
    modifier: Modifier = Modifier,
    /** Whether on-device blocking is on: this app holds the call screening role. */
    blockingOn: Boolean = false,
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
                text = SetupCompleteCopy.TITLE,
                style = CallScreenTheme.typography.headlineLarge,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary
            )

            for (line in SetupCompleteCopy.lines(blockingOn)) {
                Text(
                    text = line,
                    style = CallScreenTheme.typography.bodyLarge,
                    color = CallScreenTheme.colors.content.secondary
                )
            }
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

/** A14's words: what is actually on, never "protection active" when it is not. */
internal object SetupCompleteCopy {
    const val TITLE = "Setup finished"
    const val BLOCKING_ON = "On-device blocking is on for the numbers you block."
    const val BLOCKING_OFF = "On-device blocking is off. You can turn it on from Protection, Blocked numbers."
    const val SCREENING = "Screening unknown callers with the assistant needs call forwarding, which isn't available yet."

    fun lines(blockingOn: Boolean): List<String> = listOf(if (blockingOn) BLOCKING_ON else BLOCKING_OFF, SCREENING)
}

