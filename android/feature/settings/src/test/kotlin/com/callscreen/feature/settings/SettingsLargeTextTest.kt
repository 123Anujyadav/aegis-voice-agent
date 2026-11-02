package com.callscreen.feature.settings

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.hasScrollToNodeAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.telephony.screening.ScreeningRoleStatus
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config

/**
 * Longer words and larger text: translated copy runs up to 40% longer than
 * English (UX_FREEZE.md §9 item 2), and a subscriber may set a large font.
 * Each Settings screen is rendered at twice the font scale in a 320 x 480 dp
 * window, and its actions must still be reachable.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class SettingsLargeTextTest {

    @get:Rule
    val compose = createComposeRule()

    private fun s(id: Int): String = RuntimeEnvironment.getApplication().getString(id)

    private fun large(content: @Composable () -> Unit) {
        compose.setContent {
            val density = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(density.density, fontScale = 2f)) {
                CallScreenTheme { Box(Modifier.size(width = 320.dp, height = 480.dp)) { content() } }
            }
        }
    }

    /** Scrolls to [text] (in a lazy list, it may not be composed yet) and requires it on screen. */
    private fun reachable(text: String) {
        if (compose.onAllNodesWithText(text).fetchSemanticsNodes().isEmpty()) {
            compose.onNode(hasScrollToNodeAction()).performScrollToNode(hasText(text))
        }
        compose.onNodeWithText(text).performScrollTo().assertIsDisplayed()
    }

    private fun deletion(state: AccountDeletionState) = large {
        AccountDeletionScreen(state = state, onDeleteClick = {}, onConfirm = {}, onCancel = {}, onDone = {}, onBack = {})
    }

    @Test
    fun theChecklistAndTheDeleteEntryStayReachable() {
        large { TaskCenterScreen(tasks = SetupTasks.of(ScreeningRoleStatus.NOT_HELD, contactsAccess = false)) }
        reachable(s(R.string.settings_task_forwarding_unavailable))
        reachable(s(AccountDeletionCopy.TITLE))
    }

    @Test
    fun theExplanationStillEndsInItsActions() {
        deletion(AccountDeletionState(step = DeletionStep.READY, message = DeletionMessage.SESSION_ENDED))
        reachable(s(AccountDeletionCopy.DELETE_BUTTON))
        reachable(s(AccountDeletionCopy.BACK))
    }

    @Test
    fun theConfirmationKeepsBothChoices() {
        deletion(AccountDeletionState(step = DeletionStep.CONFIRMING))
        compose.onNodeWithText(s(AccountDeletionCopy.CONFIRM)).assertIsDisplayed()
        compose.onNodeWithText(s(AccountDeletionCopy.CANCEL)).assertIsDisplayed()
    }

    @Test
    fun aPhoneThatIsNotSignedInCanGoBack() {
        deletion(AccountDeletionState(step = DeletionStep.NOT_SIGNED_IN, message = DeletionMessage.SESSION_ENDED))
        reachable(s(AccountDeletionCopy.BACK))
    }

    @Test
    fun theDeletedScreenCanBeDismissed() {
        deletion(AccountDeletionState(step = DeletionStep.DELETED))
        reachable(s(AccountDeletionCopy.DONE_BUTTON))
    }
}
