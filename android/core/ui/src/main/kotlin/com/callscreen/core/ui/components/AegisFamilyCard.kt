package com.callscreen.core.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Person
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
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

@Composable
public fun AegisFamilyCard(
    memberName: String,
    relation: String,
    statusMode: String,
    alertCount: Int,
    onClick: () -> Unit,
    modifier: Modifier = Modifier
) {
    Card(
        modifier = modifier
            .fillMaxWidth()
            .clickable(onClick = onClick),
        shape = RoundedCornerShape(16.dp),
        colors = CardDefaults.cardColors(containerColor = CallScreenTheme.colors.surface.default),
        elevation = CardDefaults.cardElevation(defaultElevation = 2.dp)
    ) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(16.dp),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically
        ) {
            Row(
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(12.dp)
            ) {
                Box(
                    modifier = Modifier
                        .size(48.dp)
                        .clip(CircleShape)
                        .background(CallScreenTheme.colors.action.secondary.fill),
                    contentAlignment = Alignment.Center
                ) {
                    Icon(
                        imageVector = Icons.Default.Person,
                        contentDescription = "Avatar",
                        tint = CallScreenTheme.colors.action.primary.fill,
                        modifier = Modifier.size(24.dp)
                    )
                }

                Column {
                    Text(
                        text = memberName,
                        style = CallScreenTheme.typography.titleMedium,
                        fontWeight = FontWeight.Bold,
                        color = CallScreenTheme.colors.content.primary
                    )
                    Text(
                        text = relation,
                        style = CallScreenTheme.typography.bodyMedium,
                        color = CallScreenTheme.colors.content.secondary
                    )
                }
            }

            Column(
                horizontalAlignment = Alignment.End,
                verticalArrangement = Arrangement.spacedBy(4.dp)
            ) {
                AegisStatusChip(
                    text = statusMode,
                    variant = StatusChipVariant.Verified
                )
                Text(
                    text = if (alertCount == 0) "0 Recent Alerts" else "$alertCount Recent Alert(s)",
                    style = CallScreenTheme.typography.labelSmall,
                    color = if (alertCount > 0) CallScreenTheme.colors.status.fraud.text else CallScreenTheme.colors.content.tertiary,
                    fontWeight = if (alertCount > 0) FontWeight.Bold else FontWeight.Normal
                )
            }
        }
    }
}

@Preview
@Composable
private fun AegisFamilyCardPreview() {
    CallScreenTheme {
        AegisFamilyCard(
            memberName = "David",
            relation = "Grandfather",
            statusMode = "Senior Mode",
            alertCount = 2,
            onClick = {}
        )
    }
}
