package com.callscreen.feature.onboarding

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
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
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import androidx.hilt.navigation.compose.hiltViewModel
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisPrimaryButton

/** Phone verification wired to the identity flow. [onVerified] runs once, after enrolment. */
@Composable
public fun PhoneVerificationRoute(
    onVerified: () -> Unit,
    modifier: Modifier = Modifier,
    viewModel: PhoneVerificationViewModel = hiltViewModel(),
) {
    val state by viewModel.state.collectAsStateWithLifecycle()
    LaunchedEffect(state.step) {
        if (state.step == VerificationStep.VERIFIED) onVerified()
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
    Column(
        modifier = modifier
            .fillMaxSize()
            .background(CallScreenTheme.colors.surface.background)
            .padding(24.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.SpaceBetween,
    ) {
        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(16.dp),
            modifier = Modifier.padding(top = 32.dp),
        ) {
            Icon(
                imageVector = Icons.Default.PhonelinkRing,
                contentDescription = "Phone Verification",
                tint = CallScreenTheme.colors.action.primary.fill,
                modifier = Modifier.size(72.dp),
            )
            Text(
                text = if (!enteringCode) "Enter Phone Number" else "Enter Verification Code",
                style = CallScreenTheme.typography.headlineMedium,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary,
            )
            Text(
                text = if (!enteringCode) {
                    "Aegis AI binds your mobile identity securely on-device."
                } else {
                    "We sent a 6-digit code to ${state.number}"
                },
                style = CallScreenTheme.typography.bodyLarge,
                color = CallScreenTheme.colors.content.secondary,
            )

            OutlinedTextField(
                value = if (!enteringCode) state.number else state.code,
                onValueChange = if (!enteringCode) onNumberChange else onCodeChange,
                label = { Text(if (!enteringCode) "Mobile number, with country code" else "6-digit code") },
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
                    text = describe(message, state.retryAfterSeconds),
                    style = CallScreenTheme.typography.bodyMedium,
                    color = CallScreenTheme.colors.content.secondary,
                )
            }

            if (enteringCode) {
                TextButton(onClick = onResendCode, enabled = !state.submitting) { Text("Send a new code") }
                TextButton(onClick = onEditNumber, enabled = !state.submitting) { Text("Change number") }
            }
        }

        AegisPrimaryButton(
            text = if (!enteringCode) "Send Verification Code" else "Verify & Continue",
            onClick = if (!enteringCode) onSubmitNumber else onSubmitCode,
            enabled = state.message != VerificationMessage.NOT_CONFIGURED &&
                (!enteringCode || state.code.length == PhoneVerificationViewModel.CODE_LENGTH),
            isLoading = state.submitting,
            modifier = Modifier.padding(bottom = 16.dp),
        )
    }
}

/** User-facing text for [message]. Never includes a code, a token or server text. */
internal fun describe(message: VerificationMessage, retryAfterSeconds: Long?): String = when (message) {
    VerificationMessage.NOT_CONFIGURED -> "Sign-in is not available in this build."
    VerificationMessage.INVALID_NUMBER -> "Enter your number with its country code, for example +91 98765 43210."
    VerificationMessage.INVALID_CODE -> "That code is not right. Check it and try again."
    VerificationMessage.CODE_EXPIRED -> "That code has expired. Send a new code."
    VerificationMessage.TOO_MANY_ATTEMPTS ->
        if (retryAfterSeconds != null) "Too many attempts. Try again in $retryAfterSeconds seconds." else "Too many attempts. Try again later."
    VerificationMessage.DELIVERY_UNAVAILABLE -> "We could not send a code right now. Try again later."
    VerificationMessage.OFFLINE -> "No connection. Check your network and try again."
    VerificationMessage.SERVICE_UNAVAILABLE -> "The service is busy. Try again in a moment."
    VerificationMessage.COULD_NOT_SAVE -> "Your sign-in could not be saved on this device. Request a new code."
    VerificationMessage.UNEXPECTED -> "Something went wrong. Try again."
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
