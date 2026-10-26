package com.callscreen.feature.settings

import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assertHasClickAction
import androidx.compose.ui.test.hasScrollAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performScrollToNode
import com.callscreen.core.auth.AccountDeletionOutcome
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.network.identity.IdentityError
import com.callscreen.core.telephony.screening.ScreeningRoleStatus
import kotlinx.coroutines.CompletableDeferred
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/**
 * The deletion screen, driven by the real ViewModel over a fake service:
 * the entry point, the explanation, the final confirmation, cancel, failure
 * and success. Success is shown only after the service confirmed.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class AccountDeletionUiTest {

    @get:Rule
    val compose = createComposeRule()

    private class FakeService(var outcome: AccountDeletionOutcome = AccountDeletionOutcome.Deleted, val signedIn: Boolean = true) :
        AccountDeletionService {
        override val isConfigured: Boolean = true
        var deletions = 0
        var gate: CompletableDeferred<Unit>? = null
        override suspend fun isSignedIn(): Boolean = signedIn
        override suspend fun deleteAccount(): AccountDeletionOutcome {
            deletions++
            gate?.await()
            return outcome
        }
    }

    private var deletedNavigations = 0

    private fun show(service: FakeService) {
        val vm = AccountDeletionViewModel(service)
        compose.setContent {
            CallScreenTheme {
                AccountDeletionRoute(onDeleted = { deletedNavigations++ }, onBack = {}, viewModel = vm)
            }
        }
        compose.waitForIdle()
    }

    private fun tap(text: String) = compose.onNodeWithText(text).performScrollTo().performClick()

    @Test
    fun settingsHasAnAccessibleDeleteAccountEntry() {
        var opened = 0
        compose.setContent {
            CallScreenTheme {
                TaskCenterScreen(tasks = SetupTasks.of(ScreeningRoleStatus.HELD, true), onDeleteAccountClick = { opened++ })
            }
        }
        // The section is the list's last item: scroll the list to it.
        compose.onNode(hasScrollAction()).performScrollToNode(hasText(AccountDeletionCopy.TITLE))
        compose.onNode(SemanticsMatcher.expectValue(SemanticsProperties.Heading, Unit).and(hasText("Account"))).assertExists()
        compose.onNodeWithText(AccountDeletionCopy.TITLE).assertHasClickAction().performClick()
        assertEquals(1, opened)
    }

    @Test
    fun theConsequencesAreShownBeforeAnything() {
        show(FakeService())
        for (line in AccountDeletionCopy.DELETED + AccountDeletionCopy.KEPT + AccountDeletionCopy.PERMANENT) {
            compose.onNodeWithText(line, substring = true).performScrollTo().assertExists()
        }
    }

    @Test
    fun cancellingTheConfirmationDeletesNothing() {
        val service = FakeService()
        show(service)
        tap(AccountDeletionCopy.DELETE_BUTTON)
        compose.onNodeWithText(AccountDeletionCopy.CONFIRM_TITLE).assertExists()
        compose.onNodeWithText(AccountDeletionCopy.CANCEL).performClick()
        compose.waitForIdle()
        compose.onNodeWithText(AccountDeletionCopy.CONFIRM_TITLE).assertDoesNotExist()
        compose.onNodeWithText(AccountDeletionCopy.DELETE_BUTTON).assertExists()
        assertEquals(0, service.deletions)
        assertEquals(0, deletedNavigations)
    }

    @Test
    fun confirmedDeletionShowsLoadingThenSuccessThenNavigatesOnce() {
        val service = FakeService().apply { gate = CompletableDeferred() }
        show(service)
        tap(AccountDeletionCopy.DELETE_BUTTON)
        compose.onNodeWithText(AccountDeletionCopy.CONFIRM).performClick()
        compose.waitForIdle()
        compose.onNodeWithText(AccountDeletionCopy.DELETING).assertExists()
        compose.onNodeWithText(AccountDeletionCopy.DONE_TITLE).assertDoesNotExist()

        service.gate!!.complete(Unit)
        compose.waitForIdle()
        compose.onNodeWithText(AccountDeletionCopy.DONE_TITLE).assertExists()
        assertEquals(0, deletedNavigations)
        tap(AccountDeletionCopy.DONE_BUTTON)
        tap(AccountDeletionCopy.DONE_BUTTON)
        assertEquals(1, deletedNavigations)
        assertEquals(1, service.deletions)
    }

    @Test
    fun aNetworkFailureIsNotShownAsSuccess() {
        val service = FakeService(outcome = AccountDeletionOutcome.Failed(IdentityError.Network))
        show(service)
        tap(AccountDeletionCopy.DELETE_BUTTON)
        compose.onNodeWithText(AccountDeletionCopy.CONFIRM).performClick()
        compose.waitForIdle()
        compose.onNodeWithText(AccountDeletionCopy.message(DeletionMessage.NO_CONFIRMATION)).assertExists()
        compose.onNodeWithText(AccountDeletionCopy.DONE_TITLE).assertDoesNotExist()
        // The retry is available.
        compose.onNodeWithText(AccountDeletionCopy.DELETE_BUTTON).performScrollTo().assertHasClickAction()
        assertEquals(0, deletedNavigations)
    }

    @Test
    fun withoutASessionNothingOffersToDelete() {
        show(FakeService(signedIn = false))
        compose.onNodeWithText(AccountDeletionCopy.NOT_SIGNED_IN).assertExists()
        compose.onNodeWithText(AccountDeletionCopy.DELETE_BUTTON).assertDoesNotExist()
    }
}
