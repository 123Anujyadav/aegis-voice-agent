package com.callscreen.feature.calls

import androidx.annotation.StringRes
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
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
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisBottomBar
import com.callscreen.core.ui.components.AegisCallCard
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
        topBar = { AegisTopAppBar(title = stringResource(R.string.calls_feed_title)) },
        // No floating action button: the A20 spec has none, and the dial pad it showed did nothing (F19).
        bottomBar = { AegisBottomBar(currentTab = AegisTab.Calls, onTabSelected = onTabSelected) },
    ) { paddingValues ->
        // One list holds everything, banner included: with large text or longer
        // translations, a fixed header could leave no room for the feed (F17).
        LazyColumn(
            verticalArrangement = Arrangement.spacedBy(10.dp),
            modifier = modifier
                .fillMaxSize()
                .background(CallScreenTheme.colors.surface.background)
                .padding(paddingValues)
                .padding(horizontal = 16.dp),
        ) {
            if (roleLost) {
                item { ScreeningRoleBanner(onSwitchBack = onSwitchBackClick, modifier = Modifier.padding(top = 12.dp)) }
            }
            item {
                AegisSearchBar(
                    query = searchQuery,
                    onQueryChange = { searchQuery = it },
                    // It searches the feed's names and numbers, nothing else.
                    placeholder = stringResource(R.string.calls_search),
                    // The spacing the fixed header had: 12 dp above and below.
                    modifier = Modifier.padding(top = if (roleLost) 2.dp else 12.dp, bottom = 2.dp),
                )
            }
            item {
                LazyRow(
                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                    modifier = Modifier.padding(bottom = 2.dp),
                ) {
                    item { AegisStatusChip(text = stringResource(R.string.calls_filter_all), variant = StatusChipVariant.Verified) }
                    item { AegisStatusChip(text = stringResource(R.string.calls_filter_ai_screened), variant = StatusChipVariant.AIHandled) }
                    item { AegisStatusChip(text = stringResource(R.string.calls_filter_spam_blocked), variant = StatusChipVariant.Spam) }
                    item { AegisStatusChip(text = stringResource(R.string.calls_filter_fraud_risk), variant = StatusChipVariant.Fraud) }
                }
            }
            if (calls.isEmpty()) {
                item {
                    Column(verticalArrangement = Arrangement.spacedBy(4.dp), modifier = Modifier.padding(top = 24.dp)) {
                        Text(
                            text = stringResource(CallsFeedCopy.EMPTY_TITLE),
                            style = CallScreenTheme.typography.titleMedium,
                            color = CallScreenTheme.colors.content.primary,
                        )
                        Text(
                            text = stringResource(CallsFeedCopy.EMPTY),
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

@Preview
@Composable
private fun CallsFeedScreenPreview() {
    CallScreenTheme {
        CallsFeedScreen()
    }
}

/** A20 with no call records: nothing records calls on the device or the server yet. */
internal object CallsFeedCopy {
    @StringRes val EMPTY_TITLE: Int = R.string.calls_empty_title
    @StringRes val EMPTY: Int = R.string.calls_empty
}

