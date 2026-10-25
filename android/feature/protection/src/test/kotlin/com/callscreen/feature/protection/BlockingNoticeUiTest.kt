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
import org.robolectric.annotation.Config

/** The blocklist's notice: a list that cannot block says so, with the one action that fixes it. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class BlockingNoticeUiTest {

    @get:Rule
    val compose = createComposeRule()

    private var actions = 0

    private fun show(state: BlockingNoticeState) {
        compose.setContent { CallScreenTheme { BlockingNotice(state, onAction = { actions++ }) } }
    }

    @Test
    fun androidNineAndEarlierSaysBlocksDoNotApplyAndOffersNothing() {
        show(BlockingNoticeState(BlockingStatus.UNSUPPORTED))
        compose.onNodeWithText(BlockingNoticeCopy.UNSUPPORTED).assertExists()
        compose.onNodeWithText(BlockingNoticeCopy.TURN_ON).assertDoesNotExist()
        compose.onNodeWithText(BlockingNoticeCopy.ALLOW_CONTACTS).assertDoesNotExist()
    }

    @Test
    fun roleDeclinedOrRevokedSaysBlockingIsOffAndOffersToTurnItOn() {
        show(BlockingNoticeState(BlockingStatus.ROLE_NOT_HELD))
        compose.onNodeWithText(BlockingNoticeCopy.ROLE_NOT_HELD).assertExists()
        compose.onNodeWithText(BlockingNoticeCopy.TURN_ON).performClick()
        assertEquals(1, actions)
    }

    @Test
    fun withoutContactsAccessItExplainsAndAsksOnlyOnRequest() {
        show(BlockingNoticeState(BlockingStatus.ACTIVE_EXCEPT_CONTACTS))
        compose.onNodeWithText(BlockingNoticeCopy.CONTACTS).assertExists()
        compose.onNodeWithText("doesn't read or upload your contacts", substring = true).assertExists()
        assertEquals(0, actions)
        compose.onNodeWithText(BlockingNoticeCopy.ALLOW_CONTACTS).performClick()
        assertEquals(1, actions)
    }

    @Test
    fun contactsAccessRefusedPointsToSettings() {
        show(BlockingNoticeState(BlockingStatus.ACTIVE_EXCEPT_CONTACTS, contactsDenied = true))
        compose.onNodeWithText(BlockingNoticeCopy.CONTACTS_DENIED, substring = true).assertExists()
        compose.onNodeWithText(BlockingNoticeCopy.ALLOW_CONTACTS).assertDoesNotExist()
        compose.onNodeWithText(BlockingNoticeCopy.OPEN_SETTINGS).performClick()
        assertEquals(1, actions)
    }

    @Test
    fun everyBlockAppliesSoThereIsNoNotice() {
        show(BlockingNoticeState(BlockingStatus.ACTIVE))
        compose.onNodeWithText(BlockingNoticeCopy.UNSUPPORTED).assertDoesNotExist()
        compose.onNodeWithText(BlockingNoticeCopy.ROLE_NOT_HELD).assertDoesNotExist()
        compose.onNodeWithText(BlockingNoticeCopy.CONTACTS, substring = true).assertDoesNotExist()
    }
}
