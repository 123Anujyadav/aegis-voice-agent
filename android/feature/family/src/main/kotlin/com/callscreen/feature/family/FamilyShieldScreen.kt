package com.callscreen.feature.family

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisFamilyCard
import com.callscreen.core.ui.components.AegisTopAppBar

@Composable
public fun FamilyShieldScreen(
    onMemberClick: (String) -> Unit = {},
    modifier: Modifier = Modifier
) {
    Scaffold(
        topBar = { AegisTopAppBar(title = "Family Shield Hub") }
    ) { paddingValues ->
        LazyColumn(
            modifier = modifier
                .fillMaxSize()
                .background(CallScreenTheme.colors.surface.background)
                .padding(paddingValues)
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp)
        ) {
            item {
                Column {
                    Text(
                        text = "Family Protection Network",
                        style = CallScreenTheme.typography.titleLarge,
                        fontWeight = FontWeight.Bold,
                        color = CallScreenTheme.colors.content.primary
                    )
                    Text(
                        text = "Monitoring and safeguarding connected family members.",
                        style = CallScreenTheme.typography.bodyMedium,
                        color = CallScreenTheme.colors.content.secondary
                    )
                }
            }

            item {
                AegisFamilyCard(
                    memberName = "Sarah",
                    relation = "Wife",
                    statusMode = "Protected",
                    alertCount = 0,
                    onClick = { onMemberClick("Sarah") }
                )
            }

            item {
                AegisFamilyCard(
                    memberName = "David",
                    relation = "Grandfather",
                    statusMode = "Senior Mode",
                    alertCount = 2,
                    onClick = { onMemberClick("David") }
                )
            }

            item {
                AegisFamilyCard(
                    memberName = "Emma",
                    relation = "Daughter",
                    statusMode = "Child Mode",
                    alertCount = 0,
                    onClick = { onMemberClick("Emma") }
                )
            }
        }
    }
}

@Preview
@Composable
private fun FamilyShieldScreenPreview() {
    CallScreenTheme {
        FamilyShieldScreen()
    }
}
