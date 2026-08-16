package com.callscreen.core.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.callscreen.core.designsystem.theme.CallScreenTheme

public enum class StatusChipVariant {
    Verified,
    Spam,
    Fraud,
    Emergency,
    AIHandled
}

@Composable
public fun AegisStatusChip(
    text: String,
    variant: StatusChipVariant,
    modifier: Modifier = Modifier
) {
    val bgColor = when (variant) {
        StatusChipVariant.Verified -> CallScreenTheme.colors.status.success.subtle
        StatusChipVariant.Spam -> CallScreenTheme.colors.status.spam.subtle
        StatusChipVariant.Fraud -> CallScreenTheme.colors.status.fraud.subtle
        StatusChipVariant.Emergency -> CallScreenTheme.colors.status.emergency.subtle
        StatusChipVariant.AIHandled -> CallScreenTheme.colors.status.ai.subtle
    }

    val textColor = when (variant) {
        StatusChipVariant.Verified -> CallScreenTheme.colors.status.success.text
        StatusChipVariant.Spam -> CallScreenTheme.colors.status.spam.text
        StatusChipVariant.Fraud -> CallScreenTheme.colors.status.fraud.text
        StatusChipVariant.Emergency -> CallScreenTheme.colors.status.emergency.text
        StatusChipVariant.AIHandled -> CallScreenTheme.colors.status.ai.text
    }

    Box(
        modifier = modifier
            .clip(RoundedCornerShape(12.dp))
            .background(bgColor)
            .padding(horizontal = 10.dp, vertical = 4.dp)
    ) {
        Text(
            text = text.uppercase(),
            fontSize = 10.sp,
            fontWeight = FontWeight.Bold,
            color = textColor
        )
    }
}

@Preview
@Composable
private fun AegisStatusChipPreview() {
    CallScreenTheme {
        AegisStatusChip(text = "Protected", variant = StatusChipVariant.Verified)
    }
}
