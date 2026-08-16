package com.callscreen.core.ui.components

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ErrorOutline
import androidx.compose.material.icons.filled.Inbox
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme

@Composable
public fun AegisLoading(
    modifier: Modifier = Modifier,
    message: String = "Protecting your calls..."
) {
    Box(
        modifier = modifier.fillMaxSize(),
        contentAlignment = Alignment.Center
    ) {
        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(16.dp)
        ) {
            CircularProgressIndicator(
                color = CallScreenTheme.colors.action.primary.fill,
                strokeWidth = 4.dp,
                modifier = Modifier.size(48.dp)
            )
            Text(
                text = message,
                style = CallScreenTheme.typography.bodyMedium,
                color = CallScreenTheme.colors.content.secondary
            )
        }
    }
}

@Composable
public fun AegisEmptyState(
    title: String = "No Recent Calls",
    description: String = "Your calls feed will show screened and incoming calls here.",
    icon: ImageVector = Icons.Default.Inbox,
    modifier: Modifier = Modifier
) {
    Box(
        modifier = modifier
            .fillMaxSize()
            .padding(32.dp),
        contentAlignment = Alignment.Center
    ) {
        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(12.dp)
        ) {
            Icon(
                imageVector = icon,
                contentDescription = null,
                tint = CallScreenTheme.colors.content.tertiary,
                modifier = Modifier.size(64.dp)
            )
            Text(
                text = title,
                style = CallScreenTheme.typography.titleLarge,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary,
                textAlign = TextAlign.Center
            )
            Text(
                text = description,
                style = CallScreenTheme.typography.bodyMedium,
                color = CallScreenTheme.colors.content.secondary,
                textAlign = TextAlign.Center
            )
        }
    }
}

@Composable
public fun AegisErrorState(
    title: String = "Connection Error",
    errorMessage: String = "Unable to sync threat database. Please retry.",
    onRetry: () -> Unit,
    modifier: Modifier = Modifier
) {
    Box(
        modifier = modifier
            .fillMaxSize()
            .padding(32.dp),
        contentAlignment = Alignment.Center
    ) {
        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(16.dp)
        ) {
            Icon(
                imageVector = Icons.Default.ErrorOutline,
                contentDescription = null,
                tint = CallScreenTheme.colors.status.fraud.text,
                modifier = Modifier.size(64.dp)
            )
            Text(
                text = title,
                style = CallScreenTheme.typography.titleLarge,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary,
                textAlign = TextAlign.Center
            )
            Text(
                text = errorMessage,
                style = CallScreenTheme.typography.bodyMedium,
                color = CallScreenTheme.colors.content.secondary,
                textAlign = TextAlign.Center
            )
            AegisPrimaryButton(
                text = "Retry",
                onClick = onRetry,
                modifier = Modifier.fillMaxWidth(0.6f)
            )
        }
    }
}

@Composable
public fun AegisDialog(
    title: String,
    message: String,
    confirmText: String = "Confirm",
    dismissText: String = "Cancel",
    onConfirm: () -> Unit,
    onDismiss: () -> Unit
) {
    AlertDialog(
        onDismissRequest = onDismiss,
        title = {
            Text(
                text = title,
                style = CallScreenTheme.typography.titleLarge,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary
            )
        },
        text = {
            Text(
                text = message,
                style = CallScreenTheme.typography.bodyMedium,
                color = CallScreenTheme.colors.content.secondary
            )
        },
        confirmButton = {
            TextButton(onClick = onConfirm) {
                Text(
                    text = confirmText,
                    fontWeight = FontWeight.Bold,
                    color = CallScreenTheme.colors.action.primary.fill
                )
            }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) {
                Text(
                    text = dismissText,
                    color = CallScreenTheme.colors.content.secondary
                )
            }
        },
        shape = RoundedCornerShape(24.dp),
        containerColor = CallScreenTheme.colors.surface.default
    )
}

@Preview
@Composable
private fun FeedbackStatesPreview() {
    CallScreenTheme {
        AegisEmptyState()
    }
}
