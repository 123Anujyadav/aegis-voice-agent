package com.callscreen.feature.calls

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.Scaffold
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisBottomBar
import com.callscreen.core.ui.components.AegisCallCard
import com.callscreen.core.ui.components.AegisFAB
import com.callscreen.core.ui.components.AegisSearchBar
import com.callscreen.core.ui.components.AegisStatusChip
import com.callscreen.core.ui.components.AegisTab
import com.callscreen.core.ui.components.AegisTopAppBar
import com.callscreen.core.ui.components.CallType
import com.callscreen.core.ui.components.StatusChipVariant

public data class CallLogItem(
    val id: String,
    val callerName: String,
    val phoneNumber: String,
    val timestamp: String,
    val callType: CallType,
    val statusText: String
)

@Composable
public fun CallsFeedScreen(
    onCallClick: (String) -> Unit = {},
    onTabSelected: (AegisTab) -> Unit = {},
    modifier: Modifier = Modifier
) {
    var searchQuery by remember { mutableStateOf("") }

    val sampleCalls = remember {
        listOf(
            CallLogItem("1", "Unknown Caller", "+1 (555) 234-5678", "10:42 AM", CallType.ScreenedAI, "AI Handled"),
            CallLogItem("2", "National Bank Support", "+91 22 4918 2000", "09:15 AM", CallType.BlockedSpam, "Blocked Spam"),
            CallLogItem("3", "Sarah (Wife)", "+91 98200 12345", "Yesterday", CallType.Incoming, "Verified"),
            CallLogItem("4", "David (Grandfather)", "+91 98211 67890", "Yesterday", CallType.Outgoing, "Verified"),
            CallLogItem("5", "Suspected Scam", "+91 11 4056 9900", "Aug 5", CallType.BlockedSpam, "High Risk")
        )
    }

    Scaffold(
        topBar = { AegisTopAppBar(title = "Calls Feed") },
        bottomBar = { AegisBottomBar(currentTab = AegisTab.Calls, onTabSelected = onTabSelected) },
        floatingActionButton = { AegisFAB(onClick = {}) }
    ) { paddingValues ->
        Column(
            modifier = modifier
                .fillMaxSize()
                .background(CallScreenTheme.colors.surface.background)
                .padding(paddingValues)
                .padding(horizontal = 16.dp)
        ) {
            AegisSearchBar(
                query = searchQuery,
                onQueryChange = { searchQuery = it },
                modifier = Modifier.padding(vertical = 12.dp)
            )

            LazyRow(
                horizontalArrangement = Arrangement.spacedBy(8.dp),
                modifier = Modifier.padding(bottom = 12.dp)
            ) {
                item { AegisStatusChip(text = "All Calls", variant = StatusChipVariant.Verified) }
                item { AegisStatusChip(text = "AI Screened", variant = StatusChipVariant.AIHandled) }
                item { AegisStatusChip(text = "Spam Blocked", variant = StatusChipVariant.Spam) }
                item { AegisStatusChip(text = "Fraud Risk", variant = StatusChipVariant.Fraud) }
            }

            LazyColumn(
                verticalArrangement = Arrangement.spacedBy(10.dp),
                modifier = Modifier.fillMaxWidth()
            ) {
                items(sampleCalls) { call ->
                    AegisCallCard(
                        callerName = call.callerName,
                        phoneNumber = call.phoneNumber,
                        timestamp = call.timestamp,
                        callType = call.callType,
                        statusText = call.statusText,
                        onClick = { onCallClick(call.id) }
                    )
                }
            }
        }
    }
}

@Preview
@Composable
private fun CallsFeedScreenPreview() {
    CallScreenTheme {
        CallsFeedScreen()
    }
}
