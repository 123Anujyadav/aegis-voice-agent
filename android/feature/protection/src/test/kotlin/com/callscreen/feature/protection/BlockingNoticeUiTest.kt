package com.callscreen.feature.protection

import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.telephony.screening.BlockingStatus
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config

/** The blocklist's notice: a list that cannot block says so, with the one action that fixes it. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class BlockingNoticeUiTest {

    @get:Rule
    val compose = createComposeRule()

    /** The English text of a string resource, as the screen shows it. */
    private fun s(id: Int, vararg args: Any): String = RuntimeEnvironment.getApplication().getString(id, *args)

    private var actions = 0

    private fun show(state: BlockingNoticeState) {
        compose.setContent { CallScreenTheme { BlockingNotice(state, onAction = { actions++ }) } }
    }

    @Test
    fun androidNineAndEarlierSaysBlocksDoNotApplyAndOffersNothing() {
        show(BlockingNoticeState(BlockingStatus.UNSUPPORTED))
        compose.onNodeWithText(s(BlockingNoticeCopy.UNSUPPORTED)).assertExists()
        compose.onNodeWithText(s(BlockingNoticeCopy.TURN_ON)).assertDoesNotExist()
        compose.onNodeWithText(s(BlockingNoticeCopy.ALLOW_CONTACTS)).assertDoesNotExist()
    }

    @Test
    fun roleDeclinedOrRevokedSaysBlockingIsOffAndOffersToTurnItOn() {
        show(BlockingNoticeState(BlockingStatus.ROLE_NOT_HELD))
        compose.onNodeWithText(s(BlockingNoticeCopy.ROLE_NOT_HELD)).assertExists()
        compose.onNodeWithText(s(BlockingNoticeCopy.TURN_ON)).performClick()
        assertEquals(1, actions)
    }

    @Test
    fun withoutContactsAccessItExplainsAndAsksOnlyOnRequest() {
        show(BlockingNoticeState(BlockingStatus.ACTIVE_EXCEPT_CONTACTS))
        compose.onNodeWithText(s(BlockingNoticeCopy.CONTACTS)).assertExists()
        compose.onNodeWithText("doesn't read or upload your contacts", substring = true).assertExists()
        assertEquals(0, actions)
        compose.onNodeWithText(s(BlockingNoticeCopy.ALLOW_CONTACTS)).performClick()
        assertEquals(1, actions)
    }

    @Test
    fun contactsAccessRefusedPointsToSettings() {
        show(BlockingNoticeState(BlockingStatus.ACTIVE_EXCEPT_CONTACTS, contactsDenied = true))
        compose.onNodeWithText(s(BlockingNoticeCopy.CONTACTS_DENIED), substring = true).assertExists()
        compose.onNodeWithText(s(BlockingNoticeCopy.ALLOW_CONTACTS)).assertDoesNotExist()
        compose.onNodeWithText(s(BlockingNoticeCopy.OPEN_SETTINGS)).performClick()
        assertEquals(1, actions)
    }

    @Test
    fun everyBlockAppliesSoThereIsNoNotice() {
        show(BlockingNoticeState(BlockingStatus.ACTIVE))
        compose.onNodeWithText(s(BlockingNoticeCopy.UNSUPPORTED)).assertDoesNotExist()
        compose.onNodeWithText(s(BlockingNoticeCopy.ROLE_NOT_HELD)).assertDoesNotExist()
        compose.onNodeWithText(s(BlockingNoticeCopy.CONTACTS), substring = true).assertDoesNotExist()
    }
}
