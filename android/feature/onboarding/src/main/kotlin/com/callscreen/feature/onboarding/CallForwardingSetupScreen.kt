package com.callscreen.feature.onboarding

import androidx.annotation.StringRes
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.PhoneForwarded
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
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
public fun CallForwardingSetupScreen(
    onActivateForwardingClick: () -> Unit = {},
    modifier: Modifier = Modifier
) {
    AegisScreenColumn(modifier = modifier) {
        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(16.dp),
            modifier = Modifier.padding(top = 24.dp)
        ) {
            Icon(
                imageVector = Icons.AutoMirrored.Filled.PhoneForwarded,
                contentDescription = stringResource(R.string.onboarding_forwarding_icon_description),
                tint = CallScreenTheme.colors.action.primary.fill,
                modifier = Modifier.size(64.dp)
            )

            Text(
                text = stringResource(ForwardingCopy.TITLE),
                style = CallScreenTheme.typography.headlineMedium,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary
            )
            Text(
                text = stringResource(ForwardingCopy.HOW),
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
                            text = stringResource(ForwardingCopy.STATUS_TITLE),
                            style = CallScreenTheme.typography.titleMedium,
                            fontWeight = FontWeight.Bold,
                            color = CallScreenTheme.colors.content.primary
                        )
                        AegisStatusChip(text = stringResource(ForwardingCopy.NOT_AVAILABLE), variant = StatusChipVariant.Fraud)
                    }
                    Text(
                        text = stringResource(ForwardingCopy.WHY_NOT),
                        style = CallScreenTheme.typography.bodyMedium,
                        color = CallScreenTheme.colors.content.secondary
                    )
                }
            }
        }

        AegisPrimaryButton(
            text = stringResource(ForwardingCopy.CONTINUE),
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

/**
 * A07 until forwarding exists. ADR-0002 §5 provisions it with the standard
 * no-reply code (`**61*<DID>**<seconds>#`) to a platform number, dialled only
 * with the subscriber's consent and verified afterwards. No platform number is
 * allocated yet, so no code is shown and nothing is dialled.
 */
internal object ForwardingCopy {
    @StringRes val TITLE: Int = R.string.onboarding_forwarding_title
    @StringRes val HOW: Int = R.string.onboarding_forwarding_how
    @StringRes val STATUS_TITLE: Int = R.string.onboarding_forwarding_status_title
    @StringRes val NOT_AVAILABLE: Int = R.string.onboarding_forwarding_not_available
    @StringRes val WHY_NOT: Int = R.string.onboarding_forwarding_why_not
    @StringRes val CONTINUE: Int = R.string.onboarding_forwarding_continue
}

