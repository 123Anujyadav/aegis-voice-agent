package com.callscreen.feature.protection

import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.junit4.createComposeRule
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.telephony.prefilter.ListType
import com.callscreen.core.ui.components.ConfirmHaptic
import com.callscreen.core.ui.components.LocalConfirmHaptic
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** A32 "Add: row inserts, haptic.confirm" (decision packet §3a). */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class CallerListHapticTest {

    @get:Rule
    val compose = createComposeRule()

    private fun confirmsFor(type: ListType, message: ListMessage): Int {
        var count = 0
        compose.setContent {
            CompositionLocalProvider(LocalConfirmHaptic provides ConfirmHaptic { count++ }) {
                CallScreenTheme {
                    CallerListScreen(
                        state = CallerListState(type, loading = false, message = message),
                        notice = null,
                        onNoticeAction = {},
                        onQueryChange = {},
                        onAdd = {},
                        onRemove = {},
                        onSetLabel = { _, _ -> },
                        onMove = {},
                        onUndo = {},
                        onMessageShown = {},
                        onDiscardUnreadable = {},
                    )
                }
            }
        }
        compose.waitForIdle()
        return count
    }

    @Test
    fun anAddConfirmsOnce() {
        assertEquals(1, confirmsFor(ListType.BLOCK, ListMessage.ADDED))
    }

    @Test
    fun anAddThatMovesTheNumberHereConfirms() {
        assertEquals(1, confirmsFor(ListType.BLOCK, ListMessage.MOVED_TO_BLOCKED))
    }

    @Test
    fun aRemovalOrARefusalDoesNot() {
        assertEquals(0, confirmsFor(ListType.BLOCK, ListMessage.REMOVED))
    }

    @Test
    fun theRule() {
        assertTrue(gainedAnEntry(ListType.ALLOW, ListMessage.ADDED))
        assertTrue(gainedAnEntry(ListType.ALLOW, ListMessage.MOVED_TO_ALLOWED))
        assertFalse("a move out of this list, from the detail sheet", gainedAnEntry(ListType.BLOCK, ListMessage.MOVED_TO_ALLOWED))
        for (m in listOf(ListMessage.ALREADY_LISTED, ListMessage.NOT_A_PHONE_NUMBER, ListMessage.OWN_NUMBER, ListMessage.LIST_FULL,
            ListMessage.COULD_NOT_SAVE, ListMessage.LABEL_SAVED, ListMessage.INVALID_LABEL, ListMessage.UNREADABLE, ListMessage.REMOVED)) {
            assertFalse(m.name, gainedAnEntry(ListType.BLOCK, m))
        }
    }
}
