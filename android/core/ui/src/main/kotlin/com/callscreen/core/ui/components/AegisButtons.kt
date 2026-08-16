package com.callscreen.core.ui.components

import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme

@Composable
public fun AegisPrimaryButton(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    isLoading: Boolean = false
) {
    Button(
        onClick = onClick,
        modifier = modifier
            .fillMaxWidth()
            .height(54.dp),
        enabled = enabled && !isLoading,
        shape = RoundedCornerShape(28.dp),
        colors = ButtonDefaults.buttonColors(
            containerColor = CallScreenTheme.colors.action.primary.fill,
            contentColor = CallScreenTheme.colors.action.primary.content,
            disabledContainerColor = CallScreenTheme.colors.action.disabled.fill,
            disabledContentColor = CallScreenTheme.colors.action.disabled.content
        )
    ) {
        if (isLoading) {
            CircularProgressIndicator(
                color = CallScreenTheme.colors.action.primary.content,
                modifier = Modifier.padding(4.dp)
            )
        } else {
            Text(
                text = text,
                style = CallScreenTheme.typography.titleMedium,
                fontWeight = FontWeight.Bold
            )
        }
    }
}

@Composable
public fun AegisEmergencyButton(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true
) {
    Button(
        onClick = onClick,
        modifier = modifier
            .fillMaxWidth()
            .height(54.dp),
        enabled = enabled,
        shape = RoundedCornerShape(28.dp),
        colors = ButtonDefaults.buttonColors(
            containerColor = CallScreenTheme.colors.action.danger.fill,
            contentColor = CallScreenTheme.colors.action.danger.content,
            disabledContainerColor = CallScreenTheme.colors.action.disabled.fill,
            disabledContentColor = CallScreenTheme.colors.action.disabled.content
        )
    ) {
        Text(
            text = text,
            style = CallScreenTheme.typography.titleMedium,
            fontWeight = FontWeight.Bold
        )
    }
}

@Preview
@Composable
private fun AegisButtonsPreview() {
    CallScreenTheme {
        AegisPrimaryButton(text = "Get Started", onClick = {})
    }
}
