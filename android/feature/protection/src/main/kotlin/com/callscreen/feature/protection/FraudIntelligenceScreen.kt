package com.callscreen.feature.protection

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisSearchBar
import com.callscreen.core.ui.components.AegisStatusChip
import com.callscreen.core.ui.components.AegisTopAppBar
import com.callscreen.core.ui.components.StatusChipVariant

public data class ThreatEntry(
    val number: String,
    val threatType: String,
    val reports: Int,
    val riskLevel: String
)

@Composable
public fun FraudIntelligenceScreen(
    modifier: Modifier = Modifier
) {
    val threatEntries = remember {
        listOf(
            ThreatEntry("+91 11 4056 9900", "Customs / Courier Arrest Scam", 1240, "High Risk"),
            ThreatEntry("+91 22 9876 5432", "KYC Suspension SMS Phishing", 890, "High Risk"),
            ThreatEntry("+1 (800) 555-0199", "AI Voice Clone Impersonation", 310, "Critical"),
            ThreatEntry("+91 80 4123 0000", "Electricity Bill Disconnect Scam", 650, "Medium Risk")
        )
    }

    Scaffold(
        topBar = { AegisTopAppBar(title = "Fraud Intelligence Center") }
    ) { paddingValues ->
        Column(
            modifier = modifier
                .fillMaxSize()
                .background(CallScreenTheme.colors.surface.background)
                .padding(paddingValues)
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp)
        ) {
            AegisSearchBar(query = "", onQueryChange = {}, placeholder = "Search threat database by phone number...")

            Text(
                text = "Community & AI Flagged Threat Database",
                style = CallScreenTheme.typography.titleMedium,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary,
                modifier = Modifier.padding(top = 8.dp)
            )

            LazyColumn(
                verticalArrangement = Arrangement.spacedBy(10.dp)
            ) {
                items(threatEntries) { item ->
                    Card(
                        modifier = Modifier.fillMaxWidth(),
                        shape = RoundedCornerShape(16.dp),
                        colors = CardDefaults.cardColors(containerColor = CallScreenTheme.colors.surface.default)
                    ) {
                        Column(
                            modifier = Modifier.padding(16.dp),
                            verticalArrangement = Arrangement.spacedBy(6.dp)
                        ) {
                            Row(
                                modifier = Modifier.fillMaxWidth(),
                                horizontalArrangement = Arrangement.SpaceBetween
                            ) {
                                Text(
                                    text = item.number,
                                    style = CallScreenTheme.typography.titleMedium,
                                    fontWeight = FontWeight.Bold,
                                    color = CallScreenTheme.colors.content.primary
                                )
                                AegisStatusChip(
                                    text = item.riskLevel,
                                    variant = if (item.riskLevel.contains("High") || item.riskLevel.contains("Critical")) StatusChipVariant.Fraud else StatusChipVariant.Spam
                                )
                            }
                            Text(
                                text = item.threatType,
                                style = CallScreenTheme.typography.bodyMedium,
                                color = CallScreenTheme.colors.content.secondary
                            )
                            Text(
                                text = "Reported by ${item.reports} Aegis users across India.",
                                style = CallScreenTheme.typography.bodySmall,
                                color = CallScreenTheme.colors.content.tertiary
                            )
                        }
                    }
                }
            }
        }
    }
}

@Preview
@Composable
private fun FraudIntelligenceScreenPreview() {
    CallScreenTheme {
        FraudIntelligenceScreen()
    }
}
