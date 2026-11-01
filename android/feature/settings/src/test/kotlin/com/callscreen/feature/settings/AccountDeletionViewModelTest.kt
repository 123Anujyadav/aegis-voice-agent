package com.callscreen.feature.settings

import com.callscreen.core.auth.AccountDeletionOutcome
import com.callscreen.core.network.identity.IdentityError
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.TestScope
import kotlinx.coroutines.test.advanceUntilIdle
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertNull
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

@OptIn(ExperimentalCoroutinesApi::class)
@DisplayName("Account deletion")
class AccountDeletionViewModelTest {

    private val dispatcher = StandardTestDispatcher()

    private class FakeService(
        override val isConfigured: Boolean = true,
        var signedIn: Boolean = true,
        var outcome: AccountDeletionOutcome = AccountDeletionOutcome.Deleted,
    ) : AccountDeletionService {
        var deletions = 0
        var gate: CompletableDeferred<Unit>? = null

        /** The stored session cannot be read, as with a corrupt session file. */
        var readFails = false

        override suspend fun isSignedIn(): Boolean {
            if (readFails) throw IllegalStateException("unreadable session file")
            return signedIn
        }

        override suspend fun deleteAccount(): AccountDeletionOutcome {
            deletions++
            gate?.await()
            if (readFails) throw IllegalStateException("unreadable session file")
            return outcome
        }
    }

    @BeforeEach
    fun setUp() = Dispatchers.setMain(dispatcher)

    @AfterEach
    fun tearDown() = Dispatchers.resetMain()

    private fun TestScope.ready(service: FakeService): AccountDeletionViewModel =
        AccountDeletionViewModel(service).also { advanceUntilIdle() }

    @Test
    @DisplayName("a signed-in phone is shown the consequences; nothing is sent until the final confirmation")
    fun confirmThenDelete() = runTest(dispatcher) {
        val service = FakeService()
        val vm = ready(service)
        assertEquals(DeletionStep.READY, vm.state.value.step)
        vm.requestDeletion()
        assertEquals(DeletionStep.CONFIRMING, vm.state.value.step)
        advanceUntilIdle()
        assertEquals(0, service.deletions, "opening the confirmation must not delete")

        vm.confirm()
        assertEquals(DeletionStep.DELETING, vm.state.value.step)
        advanceUntilIdle()
        assertEquals(DeletionStep.DELETED, vm.state.value.step)
        assertEquals(1, service.deletions)
    }

    @Test
    @DisplayName("a stored session that cannot be read shows not signed in, instead of crashing the app")
    fun unreadableSessionAtOpen() = runTest(dispatcher) {
        val vm = ready(FakeService().apply { readFails = true })
        assertEquals(DeletionStep.NOT_SIGNED_IN, vm.state.value.step)
        assertNull(vm.state.value.message)
    }

    @Test
    @DisplayName("a deletion that fails to read the stored session says so and can be retried, instead of crashing the app")
    fun unreadableSessionAtDeletion() = runTest(dispatcher) {
        val service = FakeService()
        val vm = ready(service)
        vm.requestDeletion()
        service.readFails = true
        vm.confirm()
        advanceUntilIdle()
        assertEquals(DeletionStep.READY, vm.state.value.step)
        assertEquals(DeletionMessage.UNEXPECTED, vm.state.value.message)

        service.readFails = false
        vm.requestDeletion()
        vm.confirm()
        advanceUntilIdle()
        assertEquals(DeletionStep.DELETED, vm.state.value.step)
        assertEquals(2, service.deletions)
    }

    @Test
    @DisplayName("cancelling the confirmation deletes nothing and returns to the explanation")
    fun cancel() = runTest(dispatcher) {
        val service = FakeService()
        val vm = ready(service)
        vm.requestDeletion()
        vm.cancel()
        advanceUntilIdle()
        assertEquals(DeletionStep.READY, vm.state.value.step)
        assertEquals(0, service.deletions)
        // Confirm without an open confirmation is ignored.
        vm.confirm()
        advanceUntilIdle()
        assertEquals(0, service.deletions)
    }

    @Test
    @DisplayName("a double tap on the final confirmation sends one deletion")
    fun singleFlight() = runTest(dispatcher) {
        val service = FakeService().apply { gate = CompletableDeferred() }
        val vm = ready(service)
        vm.requestDeletion()
        vm.confirm()
        vm.confirm()
        advanceUntilIdle()
        assertEquals(DeletionStep.DELETING, vm.state.value.step)
        service.gate!!.complete(Unit)
        advanceUntilIdle()
        assertEquals(1, service.deletions)
        assertEquals(DeletionStep.DELETED, vm.state.value.step)
    }

    @Test
    @DisplayName("no answer is never shown as success: the explanation returns with a retry message")
    fun networkFailure() = runTest(dispatcher) {
        val service = FakeService(outcome = AccountDeletionOutcome.Failed(IdentityError.Network))
        val vm = ready(service)
        vm.requestDeletion()
        vm.confirm()
        advanceUntilIdle()
        assertEquals(DeletionStep.READY, vm.state.value.step)
        assertEquals(DeletionMessage.NO_CONFIRMATION, vm.state.value.message)

        // The retry succeeds and clears the message.
        service.outcome = AccountDeletionOutcome.Deleted
        vm.requestDeletion()
        vm.confirm()
        advanceUntilIdle()
        assertEquals(DeletionStep.DELETED, vm.state.value.step)
        assertNull(vm.state.value.message)
        assertEquals(2, service.deletions)
    }

    @Test
    @DisplayName("each server refusal is reported, and none is shown as success")
    fun refusals() = runTest(dispatcher) {
        val cases = mapOf(
            IdentityError.TemporarilyUnavailable to DeletionMessage.SERVICE_UNAVAILABLE,
            IdentityError.RateLimited(30) to DeletionMessage.TOO_MANY_ATTEMPTS,
            IdentityError.InvalidDeviceProof to DeletionMessage.DEVICE_CHECK_FAILED,
            IdentityError.UnexpectedResponse(500) to DeletionMessage.NO_CONFIRMATION,
            IdentityError.InvalidInput to DeletionMessage.UNEXPECTED,
        )
        for ((error, message) in cases) {
            val vm = ready(FakeService(outcome = AccountDeletionOutcome.Failed(error)))
            vm.requestDeletion()
            vm.confirm()
            advanceUntilIdle()
            assertEquals(DeletionStep.READY, vm.state.value.step, "$error")
            assertEquals(message, vm.state.value.message, "$error")
        }
    }

    @Test
    @DisplayName("no failure message claims nothing was deleted: an earlier request whose answer was lost may have deleted it")
    fun failureMessagesClaimOnlyWhatIsKnown() {
        for (message in DeletionMessage.entries) {
            val text = AccountDeletionCopy.message(message).lowercase()
            assertFalse("nothing was deleted" in text || "was not deleted" in text, "$message: $text")
        }
    }

    @Test
    @DisplayName("a session the server already ended: signed out, told to sign in again, not told it was deleted")
    fun sessionEnded() = runTest(dispatcher) {
        val vm = ready(FakeService(outcome = AccountDeletionOutcome.SessionEnded))
        vm.requestDeletion()
        vm.confirm()
        advanceUntilIdle()
        assertEquals(DeletionStep.NOT_SIGNED_IN, vm.state.value.step)
        assertEquals(DeletionMessage.SESSION_ENDED, vm.state.value.message)
    }

    @Test
    @DisplayName("no session, or no identity server, offers nothing that would pretend to delete")
    fun nothingToDelete() = runTest(dispatcher) {
        for (service in listOf(FakeService(signedIn = false), FakeService(isConfigured = false))) {
            val vm = ready(service)
            val expected = if (service.isConfigured) DeletionStep.NOT_SIGNED_IN else DeletionStep.NOT_CONFIGURED
            assertEquals(expected, vm.state.value.step)
            vm.requestDeletion()
            vm.confirm()
            advanceUntilIdle()
            assertEquals(expected, vm.state.value.step)
            assertEquals(0, service.deletions)
        }
    }
}
