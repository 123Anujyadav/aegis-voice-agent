package com.callscreen.feature.onboarding

import androidx.annotation.StringRes
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Language
import androidx.compose.material.icons.filled.RecordVoiceOver
import androidx.compose.material.icons.filled.Security
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisPrimaryButton
import com.callscreen.core.ui.components.AegisScreenColumn
import com.callscreen.core.ui.language.readInLanguages
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.shape.CircleShape
import com.callscreen.core.designsystem.token.Radius
import com.callscreen.core.ui.components.LilyLogo

@Composable
public fun WelcomeScreen(
    onGetStartedClick: () -> Unit = {},
    modifier: Modifier = Modifier,
    /** Opens A58l, so the app's language can be chosen before signing in. */
    onLanguageClick: () -> Unit = {},
) {
    AegisScreenColumn(modifier = modifier) {
        // A globe and "भाषा · Language", top end: found by someone who cannot yet read the
        // screen's language, before anything else is asked of them (owner decision D4).
        val language = WelcomeLanguageLabel.of(
            hindi = stringResource(R.string.onboarding_welcome_language_hi),
            english = stringResource(R.string.onboarding_welcome_language),
        )
        Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.End) {
            TextButton(onClick = onLanguageClick) {
                Icon(
                    imageVector = Icons.Default.Language,
                    contentDescription = null,
                    tint = CallScreenTheme.colors.content.secondary,
                )
                Text(
                    text = language.text,
                    color = CallScreenTheme.colors.content.secondary,
                    // Each part is read in its own language: Hindi, then English.
                    modifier = Modifier.padding(start = 8.dp).readInLanguages(language.text, language.parts),
                )
            }
        }

        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(16.dp)
        ) {
            LilyLogo(size = 88.dp, contentDescription = stringResource(R.string.onboarding_welcome_logo_description))
            Text(
                text = stringResource(R.string.onboarding_welcome_title),
                style = CallScreenTheme.typography.headlineLarge,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary
            )
            Text(
                text = stringResource(WelcomeCopy.TAGLINE),
                style = CallScreenTheme.typography.bodyLarge,
                color = CallScreenTheme.colors.content.secondary,
                modifier = Modifier.padding(horizontal = 8.dp)
            )
        }

        Column(
            verticalArrangement = Arrangement.spacedBy(12.dp)
        ) {
            WelcomeFeatureCard(
                icon = Icons.Default.RecordVoiceOver,
                title = stringResource(WelcomeCopy.SCREENING_TITLE),
                description = stringResource(WelcomeCopy.SCREENING),
            )
            WelcomeFeatureCard(
                icon = Icons.Default.Security,
                title = stringResource(WelcomeCopy.BLOCKING_TITLE),
                description = stringResource(WelcomeCopy.BLOCKING),
            )
        }

        AegisPrimaryButton(
            text = stringResource(R.string.onboarding_welcome_get_protected),
            onClick = onGetStartedClick,
            modifier = Modifier.padding(bottom = 16.dp)
        )
    }
}

@Composable
private fun WelcomeFeatureCard(
    icon: ImageVector,
    title: String,
    description: String
) {
    Card(
        modifier = Modifier.fillMaxWidth(),
        shape = RoundedCornerShape(Radius.xxl),
        colors = CardDefaults.cardColors(containerColor = CallScreenTheme.colors.surface.default),
        border = BorderStroke(1.dp, CallScreenTheme.colors.border.subtle),
        elevation = CardDefaults.cardElevation(defaultElevation = 0.dp)
    ) {
        Row(
            modifier = Modifier.padding(18.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(16.dp)
        ) {
            Box(
                modifier = Modifier
                    .size(48.dp)
                    .clip(CircleShape)
                    .background(CallScreenTheme.colors.status.ai.subtle),
                contentAlignment = Alignment.Center
            ) {
                Icon(
                    imageVector = icon,
                    contentDescription = title,
                    tint = CallScreenTheme.colors.status.ai.text,
                    modifier = Modifier.size(24.dp)
                )
            }
            Column {
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
        }
    }
}

@Preview
@Composable
private fun WelcomeScreenPreview() {
    CallScreenTheme {
        WelcomeScreen()
    }
}

/**
 * Welcome's claims, limited to what the app does (ADR-0002): numbers you block
 * are rejected on the phone (Stage 1); an assistant screens unknown callers
 * only through carrier forwarding after the phone has rung (Stage 2), which is
 * not available yet. No deepfake or speech analysis exists, so none is claimed.
 */
internal object WelcomeCopy {
    @StringRes val TAGLINE: Int = R.string.onboarding_welcome_tagline
    @StringRes val BLOCKING_TITLE: Int = R.string.onboarding_welcome_blocking_title
    @StringRes val BLOCKING: Int = R.string.onboarding_welcome_blocking
    @StringRes val SCREENING_TITLE: Int = R.string.onboarding_welcome_screening_title
    @StringRes val SCREENING: Int = R.string.onboarding_welcome_screening
}
