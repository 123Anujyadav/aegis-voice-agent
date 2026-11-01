package com.callscreen.feature.settings

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.callscreen.core.auth.AccountDeletionOutcome
import com.callscreen.core.network.identity.IdentityError
import dagger.hilt.android.lifecycle.HiltViewModel
import javax.inject.Inject
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/**
 * What account deletion needs from the identity flow. The app binds it to
 * core:auth's IdentityAuthenticator. [isConfigured] is false in a build with
 * no identity base URL: there is then no account to delete, and nothing is
 * ever sent.
 */
public interface AccountDeletionService {
    public val isConfigured: Boolean
    public suspend fun isSignedIn(): Boolean
    public suspend fun deleteAccount(): AccountDeletionOutcome
}

public enum class DeletionStep {
    /** Reading whether this phone holds a session. */
    CHECKING,

    /** This build has no identity server. */
    NOT_CONFIGURED,

    /** No session on this phone: nothing to delete from here. */
    NOT_SIGNED_IN,

    /** The consequences are shown; nothing has been sent. */
    READY,

    /** The final confirmation is open; nothing has been sent. */
    CONFIRMING,

    /** The request is in flight. */
    DELETING,

    /** The server confirmed the deletion. */
    DELETED,
}

/** Why the last attempt did not delete the account. Never server text. */
public enum class DeletionMessage {
    /** No answer: the request may or may not have arrived. Trying again is safe. */
    NO_CONFIRMATION,
    SERVICE_UNAVAILABLE,
    TOO_MANY_ATTEMPTS,
    DEVICE_CHECK_FAILED,

    /**
     * The server no longer accepts this phone's session. This request deleted
     * nothing; an earlier one whose answer was lost may have.
     */
    SESSION_ENDED,
    UNEXPECTED,
}

public data class AccountDeletionState(
    val step: DeletionStep = DeletionStep.CHECKING,
    val message: DeletionMessage? = null,
    /** True once the screen has navigated away after DELETED, so it never navigates twice. */
    val deletedHandled: Boolean = false,
)

/**
 * Account deletion: explain, confirm, delete. Success is shown only when the
 * server confirmed it (AccountDeletionOutcome.Deleted); every other outcome
 * says what happened and deletes nothing locally except a session the server
 * has already ended.
 */
@HiltViewModel
public class AccountDeletionViewModel @Inject constructor(
    private val service: AccountDeletionService,
) : ViewModel() {

    private val mutable = MutableStateFlow(
        AccountDeletionState(step = if (service.isConfigured) DeletionStep.CHECKING else DeletionStep.NOT_CONFIGURED),
    )
    public val state: StateFlow<AccountDeletionState> = mutable.asStateFlow()

    init {
        if (service.isConfigured) {
            viewModelScope.launch {
                // A stored session that cannot be read (for example a corrupt
                // file) is no session this phone can delete with: shown as not
                // signed in, as the splash routes it. Nothing is cleared here.
                val signedIn = try {
                    service.isSignedIn()
                } catch (e: CancellationException) {
                    throw e
                } catch (e: Exception) {
                    false
                }
                mutable.update {
                    if (it.step != DeletionStep.CHECKING) it else it.copy(step = if (signedIn) DeletionStep.READY else DeletionStep.NOT_SIGNED_IN)
                }
            }
        }
    }

    /** Opens the final confirmation. Nothing is sent. */
    public fun requestDeletion() {
        mutable.update { if (it.step == DeletionStep.READY) it.copy(step = DeletionStep.CONFIRMING) else it }
    }

    /** Closes the confirmation without deleting anything. */
    public fun cancel() {
        mutable.update { if (it.step == DeletionStep.CONFIRMING) it.copy(step = DeletionStep.READY) else it }
    }

    /** The final confirmation: sends the deletion. Ignored unless the confirmation is open, so a double tap sends once. */
    public fun confirm() {
        if (mutable.value.step != DeletionStep.CONFIRMING) return
        mutable.update { it.copy(step = DeletionStep.DELETING, message = null) }
        viewModelScope.launch {
            // The failure expected here is reading the stored session, which
            // happens before anything is sent: report it and allow a retry,
            // instead of letting it crash the app.
            val outcome = try {
                service.deleteAccount()
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                null
            }
            mutable.update {
                when (outcome) {
                    null -> it.copy(step = DeletionStep.READY, message = DeletionMessage.UNEXPECTED)
                    AccountDeletionOutcome.Deleted -> it.copy(step = DeletionStep.DELETED)
                    AccountDeletionOutcome.NotSignedIn -> it.copy(step = DeletionStep.NOT_SIGNED_IN)
                    AccountDeletionOutcome.SessionEnded -> it.copy(step = DeletionStep.NOT_SIGNED_IN, message = DeletionMessage.SESSION_ENDED)
                    is AccountDeletionOutcome.Failed -> it.copy(step = DeletionStep.READY, message = messageFor(outcome.error))
                }
            }
        }
    }

    /** Records that the screen navigated away after [DeletionStep.DELETED]. */
    public fun onDeletedHandled() {
        mutable.update { it.copy(deletedHandled = true) }
    }

    private fun messageFor(error: IdentityError): DeletionMessage = when (error) {
        IdentityError.Network -> DeletionMessage.NO_CONFIRMATION
        IdentityError.TemporarilyUnavailable -> DeletionMessage.SERVICE_UNAVAILABLE
        is IdentityError.RateLimited -> DeletionMessage.TOO_MANY_ATTEMPTS
        IdentityError.InvalidDeviceProof -> DeletionMessage.DEVICE_CHECK_FAILED
        // A server answer outside the contract may still have followed a deletion: say so, as for no answer.
        is IdentityError.UnexpectedResponse -> DeletionMessage.NO_CONFIRMATION
        else -> DeletionMessage.UNEXPECTED
    }
}
