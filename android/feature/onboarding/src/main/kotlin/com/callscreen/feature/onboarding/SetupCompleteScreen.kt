package com.callscreen.feature.onboarding

import androidx.annotation.StringRes
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
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
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisPrimaryButton
import com.callscreen.core.ui.components.AegisScreenColumn

@Composable
public fun SetupCompleteScreen(
    onGoToHomeClick: () -> Unit = {},
    modifier: Modifier = Modifier,
    /** Whether on-device blocking is on: this app holds the call screening role. */
    blockingOn: Boolean = false,
) {
    AegisScreenColumn(modifier = modifier) {
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
                    contentDescription = stringResource(R.string.onboarding_complete_icon_description),
                    tint = CallScreenTheme.colors.status.success.text,
                    modifier = Modifier.size(56.dp)
                )
            }

            Text(
                text = stringResource(SetupCompleteCopy.TITLE),
                style = CallScreenTheme.typography.headlineLarge,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary
            )

            for (line in SetupCompleteCopy.lines(blockingOn)) {
                Text(
                    text = stringResource(line),
                    style = CallScreenTheme.typography.bodyLarge,
                    color = CallScreenTheme.colors.content.secondary
                )
            }
        }

        AegisPrimaryButton(
            text = stringResource(R.string.onboarding_complete_go_to_calls),
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
    @StringRes val TITLE: Int = R.string.onboarding_complete_title
    @StringRes val BLOCKING_ON: Int = R.string.onboarding_complete_blocking_on
    @StringRes val BLOCKING_OFF: Int = R.string.onboarding_complete_blocking_off
    @StringRes val SCREENING: Int = R.string.onboarding_complete_screening

    fun lines(blockingOn: Boolean): List<Int> = listOf(if (blockingOn) BLOCKING_ON else BLOCKING_OFF, SCREENING)
}

