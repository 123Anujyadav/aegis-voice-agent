package com.callscreen.feature.protection

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.telephony.callerlist.CallerListEntry
import com.callscreen.core.telephony.callerlist.ListSource
import com.callscreen.core.telephony.prefilter.E164Number
import com.callscreen.core.telephony.prefilter.ListType
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/**
 * The list's messages reach the screen: a removal offers Undo, and a message is
 * marked shown only after its snackbar has gone. Marking it shown first changed
 * the effect's key and dismissed the snackbar at once (found on a device).
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class CallerListSnackbarUiTest {

    @get:Rule
    val compose = createComposeRule()

    private val entry = CallerListEntry(requireNotNull(E164Number.of("+919876500001")), ListType.BLOCK, ListSource.MANUAL, 1)

    @Test
    fun removalOffersUndoAndTheMessageIsClearedOnlyAfterwards() {
        var state by mutableStateOf(
            CallerListState(ListType.BLOCK, loading = false, message = ListMessage.REMOVED, undoable = entry),
        )
        var shown = 0
        var undone = 0
        compose.setContent {
            CallScreenTheme {
                CallerListScreen(
                    state = state,
                    notice = null,
                    onNoticeAction = {},
                    onQueryChange = {},
                    onAdd = {},
                    onRemove = {},
                    onSetLabel = { _, _ -> },
                    onMove = {},
                    onUndo = { undone++ },
                    onMessageShown = {
                        shown++
                        state = state.copy(message = null)
                    },
                    onDiscardUnreadable = {},
                )
            }
        }
        compose.onNodeWithText("Removed.").assertExists()
        compose.onNodeWithText("Undo").assertExists()
        assertEquals("the message must stay until its snackbar is done", 0, shown)

        compose.onNodeWithText("Undo").performClick()
        compose.waitForIdle()
        assertEquals(1, undone)
        assertEquals(1, shown)
    }

    @Test
    fun aMoveIsStatedInTheDirectionItWent() {
        compose.setContent {
            CallScreenTheme {
                CallerListScreen(
                    state = CallerListState(ListType.BLOCK, loading = false, message = ListMessage.MOVED_TO_ALLOWED),
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
        compose.onNodeWithText("Moved from your blocked numbers to your allowed numbers.").assertExists()
    }

    @Test
    fun aPopulatedAllowlistStillSaysTheCarrierMayForward() {
        val allowed = entry.copy(type = ListType.ALLOW)
        compose.setContent {
            CallScreenTheme {
                CallerListScreen(
                    state = CallerListState(ListType.ALLOW, loading = false, entries = listOf(allowed)),
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
        compose.onNodeWithText(CallerListCopy.FORWARDING_CAVEAT).assertExists()
    }
}
