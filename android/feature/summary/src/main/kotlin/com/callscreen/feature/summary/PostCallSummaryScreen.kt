package com.callscreen.feature.summary

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisStatusChip
import com.callscreen.core.ui.components.AegisThreatCard
import com.callscreen.core.ui.components.AegisTopAppBar
import com.callscreen.core.ui.components.StatusChipVariant

@Composable
public fun PostCallSummaryScreen(
    callerName: String = "Unknown +1 (555) 234-5678",
    onSaveContactClick: () -> Unit = {},
    onBlockReportClick: () -> Unit = {},
    modifier: Modifier = Modifier
) {
    Scaffold(
        topBar = { AegisTopAppBar(title = "Post-Call AI Summary") }
    ) { paddingValues ->
        Column(
            modifier = modifier
                .fillMaxSize()
                .background(CallScreenTheme.colors.surface.background)
                .padding(paddingValues)
                .padding(16.dp),
            verticalArrangement = Arrangement.SpaceBetween
        ) {
            Column(
                verticalArrangement = Arrangement.spacedBy(16.dp)
            ) {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween
                ) {
                    Column {
                        Text(
                            text = callerName,
                            style = CallScreenTheme.typography.titleLarge,
                            fontWeight = FontWeight.Bold,
                            color = CallScreenTheme.colors.content.primary
                        )
                        Text(
                            text = "Call Duration: 42 seconds",
                            style = CallScreenTheme.typography.bodyMedium,
                            color = CallScreenTheme.colors.content.secondary
                        )
                    }
                    AegisStatusChip(text = "AI Handled", variant = StatusChipVariant.AIHandled)
                }

                AegisThreatCard(
                    threatTitle = "Low Risk / Informational",
                    threatScore = 12,
                    description = "Caller identified as courier delivery inquiry for parcel arrival."
                )

                Card(
                    modifier = Modifier.fillMaxWidth(),
                    shape = RoundedCornerShape(16.dp),
                    colors = CardDefaults.cardColors(containerColor = CallScreenTheme.colors.surface.default)
                ) {
                    Column(
                        modifier = Modifier.padding(16.dp),
                        verticalArrangement = Arrangement.spacedBy(8.dp)
                    ) {
                        Text(
                            text = "Extracted AI Summary",
                            style = CallScreenTheme.typography.titleMedium,
                            fontWeight = FontWeight.Bold,
                            color = CallScreenTheme.colors.content.primary
                        )
                        Text(
                            text = "• Caller: Mark from BlueDart Express\n• Reason: Confirming gate code for package delivery\n• Action taken: AI informed courier to leave parcel with security guard.",
                            style = CallScreenTheme.typography.bodyMedium,
                            color = CallScreenTheme.colors.content.secondary
                        )
                    }
                }
            }

            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.spacedBy(12.dp)
            ) {
                Button(
                    onClick = onSaveContactClick,
                    modifier = Modifier.weight(1f),
                    shape = RoundedCornerShape(24.dp),
                    colors = ButtonDefaults.buttonColors(containerColor = CallScreenTheme.colors.action.primary.fill)
                ) {
                    Text("Save Contact", fontWeight = FontWeight.Bold)
                }

                Button(
                    onClick = onBlockReportClick,
                    modifier = Modifier.weight(1f),
                    shape = RoundedCornerShape(24.dp),
                    colors = ButtonDefaults.buttonColors(containerColor = CallScreenTheme.colors.action.danger.fill)
                ) {
                    Text("Block & Report", fontWeight = FontWeight.Bold)
                }
            }
        }
    }
}

@Preview
@Composable
private fun PostCallSummaryScreenPreview() {
    CallScreenTheme {
        PostCallSummaryScreen()
    }
}
