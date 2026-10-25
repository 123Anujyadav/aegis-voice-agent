package com.callscreen.feature.protection

import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextReplacement
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
 * A32 rows and the A32d detail sheet, rendered on the JVM (Robolectric).
 * Robolectric 4.14 runs up to SDK 35, below this app's targetSdk 36, hence the pin.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class CallerListUiTest {

    @get:Rule
    val compose = createComposeRule()

    private val number = requireNotNull(E164Number.of("+919876500001"))
    private val spoken = "+ 9 1 9 8 7 6 5 0 0 0 0 1"
    private val blocked = CallerListEntry(number, ListType.BLOCK, ListSource.FROM_CALL, 1_700_000_000, label = "Courier")
    private val allowed = blocked.copy(type = ListType.ALLOW, source = ListSource.MANUAL, label = null)

    @Test
    fun blockedDetailShowsTheStoredFactsAndItsActions() {
        var saved: String? = null
        var moves = 0
        var removes = 0
        compose.setContent {
            CallScreenTheme {
                EntryDetail(blocked, onSaveLabel = { saved = it }, onMove = { moves++ }, onRemove = { removes++ })
            }
        }
        // The chip shows the list as a word (upper-cased by the chip), not only as a colour.
        compose.onNodeWithText("Blocked", ignoreCase = true).assertExists()
        compose.onNodeWithContentDescription(spoken).assertExists()
        compose.onNodeWithText("From a call", substring = true).assertExists()
        compose.onNodeWithText(CallerListCopy.FORWARDING_CAVEAT).assertDoesNotExist()

        compose.onNodeWithText(CallerListCopy.SAVE_LABEL).assertIsNotEnabled()
        compose.onNodeWithText("Courier").performTextReplacement("Courier company")
        compose.onNodeWithText(CallerListCopy.SAVE_LABEL).assertIsEnabled().performClick()
        assertEquals("Courier company", saved)

        compose.onNodeWithText("Move to allowed numbers").performClick()
        compose.onNodeWithText(CallerListCopy.REMOVE).performClick()
        assertEquals(1, moves)
        assertEquals(1, removes)
    }

    @Test
    fun allowedDetailSaysTheCarrierMayStillForward() {
        compose.setContent {
            CallScreenTheme { EntryDetail(allowed, onSaveLabel = {}, onMove = {}, onRemove = {}) }
        }
        compose.onNodeWithText("Allowed", ignoreCase = true).assertExists()
        compose.onNodeWithText(CallerListCopy.ALLOWED_NEVER_BLOCKED).assertExists()
        compose.onNodeWithText(CallerListCopy.FORWARDING_CAVEAT).assertExists()
        compose.onNodeWithText("Move to blocked numbers").assertExists()
        compose.onNodeWithText("rejected", substring = true).assertDoesNotExist()
    }

    @Test
    fun rowIsOneNodeWithLabelDigitsAndSourceAndOpensItsDetail() {
        var opened: CallerListEntry? = null
        var removed: CallerListEntry? = null
        compose.setContent {
            CallScreenTheme { EntryRow(blocked, onOpen = { opened = it }, onRemove = { removed = it }) }
        }
        compose.onNode(hasContentDescription("Courier, $spoken, From a call", substring = true)).performClick()
        assertEquals(blocked, opened)
        compose.onNodeWithContentDescription("${CallerListCopy.REMOVE} $spoken").performClick()
        assertEquals(blocked, removed)
    }
}
