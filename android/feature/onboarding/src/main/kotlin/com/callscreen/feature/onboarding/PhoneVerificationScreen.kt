package com.callscreen.feature.onboarding

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.PhonelinkRing
import androidx.compose.material3.Icon
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import androidx.hilt.navigation.compose.hiltViewModel
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisPrimaryButton
import com.callscreen.core.ui.components.AegisScreenColumn

/** Phone verification wired to the identity flow. [onVerified] runs once, after enrolment. */
@Composable
public fun PhoneVerificationRoute(
    onVerified: () -> Unit,
    modifier: Modifier = Modifier,
    viewModel: PhoneVerificationViewModel = hiltViewModel(),
) {
    val state by viewModel.state.collectAsStateWithLifecycle()
    LaunchedEffect(state.step, state.verifiedHandled) {
        if (state.step == VerificationStep.VERIFIED && !state.verifiedHandled) {
            viewModel.onVerifiedHandled()
            onVerified()
        }
    }
    PhoneVerificationScreen(
        state = state,
        onNumberChange = viewModel::onNumberChange,
        onCodeChange = viewModel::onCodeChange,
        onSubmitNumber = viewModel::submitNumber,
        onSubmitCode = viewModel::submitCode,
        onResendCode = viewModel::resendCode,
        onEditNumber = viewModel::editNumber,
        modifier = modifier,
    )
}

/** The stateless screen. */
@Composable
public fun PhoneVerificationScreen(
    state: PhoneVerificationState,
    onNumberChange: (String) -> Unit,
    onCodeChange: (String) -> Unit,
    onSubmitNumber: () -> Unit,
    onSubmitCode: () -> Unit,
    onResendCode: () -> Unit,
    onEditNumber: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val enteringCode = state.step != VerificationStep.ENTER_NUMBER
    AegisScreenColumn(modifier = modifier) {
        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(16.dp),
            modifier = Modifier.padding(top = 32.dp),
        ) {
            Icon(
                imageVector = Icons.Default.PhonelinkRing,
                contentDescription = stringResource(R.string.onboarding_phone_icon_description),
                tint = CallScreenTheme.colors.action.primary.fill,
                modifier = Modifier.size(72.dp),
            )
            Text(
                text = if (!enteringCode) stringResource(R.string.onboarding_phone_title_number) else stringResource(R.string.onboarding_phone_title_code),
                style = CallScreenTheme.typography.headlineMedium,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary,
            )
            Text(
                text = if (!enteringCode) {
                    stringResource(R.string.onboarding_phone_subtitle_number)
                } else {
                    stringResource(R.string.onboarding_phone_subtitle_code, state.number)
                },
                style = CallScreenTheme.typography.bodyLarge,
                color = CallScreenTheme.colors.content.secondary,
            )

            OutlinedTextField(
                value = if (!enteringCode) state.number else state.code,
                onValueChange = if (!enteringCode) onNumberChange else onCodeChange,
                label = { Text(if (!enteringCode) stringResource(R.string.onboarding_phone_label_number) else stringResource(R.string.onboarding_phone_label_code)) },
                enabled = !state.submitting,
                singleLine = true,
                keyboardOptions = KeyboardOptions(keyboardType = if (!enteringCode) KeyboardType.Phone else KeyboardType.NumberPassword),
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(top = 16.dp),
                shape = RoundedCornerShape(16.dp),
                colors = OutlinedTextFieldDefaults.colors(
                    focusedBorderColor = CallScreenTheme.colors.action.primary.fill,
                    unfocusedBorderColor = CallScreenTheme.colors.border.default,
                ),
            )

            state.message?.let { message ->
                Text(
                    text = describe(message, state.retryAfterSeconds).text(),
                    style = CallScreenTheme.typography.bodyMedium,
                    color = CallScreenTheme.colors.content.secondary,
                )
            }

            if (enteringCode) {
                TextButton(onClick = onResendCode, enabled = !state.submitting) { Text(stringResource(R.string.onboarding_phone_resend)) }
                TextButton(onClick = onEditNumber, enabled = !state.submitting) { Text(stringResource(R.string.onboarding_phone_change_number)) }
            }
        }

        AegisPrimaryButton(
            text = if (!enteringCode) stringResource(R.string.onboarding_phone_send) else stringResource(R.string.onboarding_phone_verify),
            onClick = if (!enteringCode) onSubmitNumber else onSubmitCode,
            enabled = state.message != VerificationMessage.NOT_CONFIGURED &&
                (!enteringCode || state.code.length == PhoneVerificationViewModel.CODE_LENGTH),
            isLoading = state.submitting,
            modifier = Modifier.padding(bottom = 16.dp),
        )
    }
}

/**
 * User-facing text for a message: a string resource, or a plural with a
 * number of seconds. It has no text argument, so it can never carry a code, a
 * token or server text.
 */
internal data class MessageText(val id: Int, val seconds: Long? = null)

internal fun describe(message: VerificationMessage, retryAfterSeconds: Long?): MessageText = when (message) {
    VerificationMessage.NOT_CONFIGURED -> MessageText(R.string.onboarding_phone_message_not_configured)
    VerificationMessage.INVALID_NUMBER -> MessageText(R.string.onboarding_phone_message_invalid_number)
    VerificationMessage.INVALID_CODE -> MessageText(R.string.onboarding_phone_message_invalid_code)
    VerificationMessage.CODE_EXPIRED -> MessageText(R.string.onboarding_phone_message_code_expired)
    VerificationMessage.TOO_MANY_ATTEMPTS ->
        if (retryAfterSeconds != null) {
            MessageText(R.plurals.onboarding_phone_message_retry_in, retryAfterSeconds)
        } else {
            MessageText(R.string.onboarding_phone_message_too_many_attempts)
        }
    VerificationMessage.DELIVERY_UNAVAILABLE -> MessageText(R.string.onboarding_phone_message_delivery_unavailable)
    VerificationMessage.OFFLINE -> MessageText(R.string.onboarding_phone_message_offline)
    VerificationMessage.SERVICE_UNAVAILABLE -> MessageText(R.string.onboarding_phone_message_service_unavailable)
    VerificationMessage.COULD_NOT_SAVE -> MessageText(R.string.onboarding_phone_message_could_not_save)
    VerificationMessage.UNEXPECTED -> MessageText(R.string.onboarding_phone_message_unexpected)
}

@Composable
internal fun MessageText.text(): String = when (val n = seconds) {
    null -> stringResource(id)
    else -> pluralStringResource(id, n.coerceAtMost(Int.MAX_VALUE.toLong()).toInt(), n)
}

@Preview
@Composable
private fun PhoneVerificationScreenPreview() {
    CallScreenTheme {
        PhoneVerificationScreen(
            state = PhoneVerificationState(),
            onNumberChange = {},
            onCodeChange = {},
            onSubmitNumber = {},
            onSubmitCode = {},
            onResendCode = {},
            onEditNumber = {},
        )
    }
}
