package com.callscreen.feature.onboarding

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.PhonelinkRing
import androidx.compose.material3.Icon
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisPrimaryButton

@Composable
public fun PhoneVerificationScreen(
    onSendOtpClick: (String) -> Unit = {},
    modifier: Modifier = Modifier
) {
    var phoneNumber by remember { mutableStateOf("+91 98765 43210") }
    var otpCode by remember { mutableStateOf("") }
    var isOtpSent by remember { mutableStateOf(false) }

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
            modifier = Modifier.padding(top = 32.dp)
        ) {
            Icon(
                imageVector = Icons.Default.PhonelinkRing,
                contentDescription = "Phone Verification",
                tint = CallScreenTheme.colors.action.primary.fill,
                modifier = Modifier.size(72.dp)
            )
            Text(
                text = if (!isOtpSent) "Enter Phone Number" else "Enter Verification Code",
                style = CallScreenTheme.typography.headlineMedium,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary
            )
            Text(
                text = if (!isOtpSent) "Aegis AI binds your mobile identity securely on-device." else "We sent a 6-digit OTP code to $phoneNumber",
                style = CallScreenTheme.typography.bodyLarge,
                color = CallScreenTheme.colors.content.secondary
            )

            if (!isOtpSent) {
                OutlinedTextField(
                    value = phoneNumber,
                    onValueChange = { phoneNumber = it },
                    label = { Text("Mobile Number") },
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(top = 16.dp),
                    shape = RoundedCornerShape(16.dp),
                    colors = OutlinedTextFieldDefaults.colors(
                        focusedBorderColor = CallScreenTheme.colors.action.primary.fill,
                        unfocusedBorderColor = CallScreenTheme.colors.border.default
                    )
                )
            } else {
                OutlinedTextField(
                    value = otpCode,
                    onValueChange = { otpCode = it },
                    label = { Text("6-Digit OTP") },
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(top = 16.dp),
                    shape = RoundedCornerShape(16.dp),
                    colors = OutlinedTextFieldDefaults.colors(
                        focusedBorderColor = CallScreenTheme.colors.action.primary.fill,
                        unfocusedBorderColor = CallScreenTheme.colors.border.default
                    )
                )
            }
        }

        AegisPrimaryButton(
            text = if (!isOtpSent) "Send Verification OTP" else "Verify & Continue",
            onClick = {
                if (!isOtpSent) {
                    isOtpSent = true
                } else {
                    onSendOtpClick(phoneNumber)
                }
            },
            modifier = Modifier.padding(bottom = 16.dp)
        )
    }
}

@Preview
@Composable
private fun PhoneVerificationScreenPreview() {
    CallScreenTheme {
        PhoneVerificationScreen()
    }
}
