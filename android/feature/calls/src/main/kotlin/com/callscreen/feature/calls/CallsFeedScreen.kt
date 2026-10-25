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
import androidx.compose.material3.Text
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
    /** The calls to show. Nothing records calls yet, so the app passes none and the feed says so. */
    calls: List<CallLogItem> = emptyList(),
    onTabSelected: (AegisTab) -> Unit = {},
    modifier: Modifier = Modifier,
    /** F14: another app has taken the screening role; the banner (A75) offers to switch back. */
    roleLost: Boolean = false,
    onSwitchBackClick: () -> Unit = {},
) {
    var searchQuery by remember { mutableStateOf("") }

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
            if (roleLost) {
                ScreeningRoleBanner(onSwitchBack = onSwitchBackClick, modifier = Modifier.padding(top = 12.dp))
            }

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
                if (calls.isEmpty()) {
                    item {
                        Column(verticalArrangement = Arrangement.spacedBy(4.dp), modifier = Modifier.padding(top = 24.dp)) {
                            Text(
                                text = CallsFeedCopy.EMPTY_TITLE,
                                style = CallScreenTheme.typography.titleMedium,
                                color = CallScreenTheme.colors.content.primary,
                            )
                            Text(
                                text = CallsFeedCopy.EMPTY,
                                style = CallScreenTheme.typography.bodyMedium,
                                color = CallScreenTheme.colors.content.secondary,
                            )
                        }
                    }
                }
                items(calls.filter { searchQuery.isBlank() || searchQuery in it.callerName || searchQuery in it.phoneNumber }) { call ->
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

/** A20 with no call records: nothing records calls on the device or the server yet. */
internal object CallsFeedCopy {
    const val EMPTY_TITLE = "No call data yet"
    const val EMPTY = "Calls the app blocks or screens will appear here."
}

