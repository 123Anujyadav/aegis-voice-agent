package com.callscreen.core.ui.components

import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import kotlinx.coroutines.launch

/**
 * What re-selecting a tab does at its root (navigation spec 02 §2.1, F25): it
 * scrolls the tab's [list] to the top, and at the top nothing moves: never a
 * bounce. Pass it to [AegisBottomBar]'s `onReselect`.
 */
@Composable
public fun rememberScrollToTop(list: LazyListState): () -> Unit {
    val scope = rememberCoroutineScope()
    return remember(list, scope) {
        {
            if (list.firstVisibleItemIndex > 0 || list.firstVisibleItemScrollOffset > 0) {
                scope.launch { list.animateScrollToItem(0) }
            }
        }
    }
}
