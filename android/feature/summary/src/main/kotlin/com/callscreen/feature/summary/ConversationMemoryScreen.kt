package com.callscreen.feature.summary

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
import com.callscreen.core.ui.components.AegisTopAppBar

public data class MemoryFact(
    val caller: String,
    val fact: String,
    val date: String
)

@Composable
public fun ConversationMemoryScreen(
    modifier: Modifier = Modifier
) {
    val facts = remember {
        listOf(
            MemoryFact("Mark (Courier)", "Prefers leaving packages with front desk security.", "Aug 6, 2026"),
            MemoryFact("Dr. Smith's Office", "Clinic hours are Mon-Fri 9 AM to 5 PM.", "Aug 4, 2026"),
            MemoryFact("Apt Maintenance", "Plumber appointment scheduled for Aug 10.", "Aug 1, 2026")
        )
    }

    Scaffold(
        topBar = { AegisTopAppBar(title = "AI Conversation Memory") }
    ) { paddingValues ->
        Column(
            modifier = modifier
                .fillMaxSize()
                .background(CallScreenTheme.colors.surface.background)
                .padding(paddingValues)
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp)
        ) {
            AegisSearchBar(query = "", onQueryChange = {}, placeholder = "Search caller memory facts...")

            Text(
                text = "Extracted Facts & Preferences",
                style = CallScreenTheme.typography.titleMedium,
                fontWeight = FontWeight.Bold,
                color = CallScreenTheme.colors.content.primary,
                modifier = Modifier.padding(top = 8.dp)
            )

            LazyColumn(
                verticalArrangement = Arrangement.spacedBy(10.dp)
            ) {
                items(facts) { item ->
                    Card(
                        modifier = Modifier.fillMaxWidth(),
                        shape = RoundedCornerShape(16.dp),
                        colors = CardDefaults.cardColors(containerColor = CallScreenTheme.colors.surface.default)
                    ) {
                        Column(
                            modifier = Modifier.padding(16.dp),
                            verticalArrangement = Arrangement.spacedBy(4.dp)
                        ) {
                            Row(
                                modifier = Modifier.fillMaxWidth(),
                                horizontalArrangement = Arrangement.SpaceBetween
                            ) {
                                Text(
                                    text = item.caller,
                                    style = CallScreenTheme.typography.titleSmall,
                                    fontWeight = FontWeight.Bold,
                                    color = CallScreenTheme.colors.content.primary
                                )
                                Text(
                                    text = item.date,
                                    style = CallScreenTheme.typography.bodySmall,
                                    color = CallScreenTheme.colors.content.tertiary
                                )
                            }
                            Text(
                                text = item.fact,
                                style = CallScreenTheme.typography.bodyMedium,
                                color = CallScreenTheme.colors.content.secondary
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
private fun ConversationMemoryScreenPreview() {
    CallScreenTheme {
        ConversationMemoryScreen()
    }
}
