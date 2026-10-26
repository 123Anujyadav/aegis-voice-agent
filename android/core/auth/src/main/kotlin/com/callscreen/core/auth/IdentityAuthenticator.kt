package com.callscreen.core.auth

import com.callscreen.core.datastore.session.EncryptedSessionStore
import com.callscreen.core.datastore.session.StoredSession
import com.callscreen.core.datastore.session.StoredSessionResult
import com.callscreen.core.network.identity.IdentityClient
import com.callscreen.core.network.identity.IdentityError
import com.callscreen.core.network.identity.IdentityResult
import com.callscreen.core.network.identity.OtpChallenge
import kotlin.coroutines.cancellation.CancellationException
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext

/** Whether a valid access token is available. toString never shows a token. */
public sealed interface AuthState {
    public class Authenticated(public val accessToken: String) : AuthState {
        override fun toString(): String = "Authenticated(accessToken=<redacted>)"
    }

    /** No usable session: the user must sign in. */
    public data class SignedOut(val reason: SignedOutReason) : AuthState

    /**
     * The session is kept, but a fresh token could not be obtained now
     * (offline, the server unavailable, rate limited). Retry later.
     */
    public data class Unavailable(val error: IdentityError) : AuthState
}

public enum class SignedOutReason {
    /** Nothing was ever stored on this device. */
    NOT_SIGNED_IN,

    /** The server ended the session (expired, revoked, or the device key no longer matches). */
    SESSION_ENDED,

    /** The stored session could not be opened safely and was cleared. */
    STORED_SESSION_UNRECOVERABLE,

    /** New tokens were issued but could not be stored, so the session cannot continue. */
    STORAGE_FAILED,
}

/** The outcome of entering a code. */
public sealed interface EnrolOutcome {
    public data object Enrolled : EnrolOutcome
    public data class Failed(val error: IdentityError) : EnrolOutcome

    /** The server enrolled the device but the session could not be stored: request a new code. */
    public data object StorageFailed : EnrolOutcome
}

/** The outcome of deleting the account (docs/phase19/ACCOUNT-DELETION.md). */
public sealed interface AccountDeletionOutcome {
    /** The server confirmed the erasure (204), and the session was removed from this phone. */
    public data object Deleted : AccountDeletionOutcome

    /** No usable session on this phone: nothing was sent. */
    public data object NotSignedIn : AccountDeletionOutcome

    /**
     * The server refused the session (ended or revoked): nothing was erased by
     * this request. The session was removed from this phone; signing in again
     * is needed to delete the account.
     */
    public data object SessionEnded : AccountDeletionOutcome

    /**
     * No confirmation. The session is kept. After [IdentityError.Network] the
     * request may have reached the server: trying again with the same session
     * is answered as a success if it did.
     */
    public data class Failed(val error: IdentityError) : AccountDeletionOutcome
}

/**
 * The identity flow over the HTTP client and the encrypted session store.
 *
 * - **Refresh is single-flight.** Every token read and refresh runs under one
 *   lock, and the store is re-read after the lock is acquired. Two concurrent
 *   refreshes would present one refresh token twice, and identity treats the
 *   second presentation of a superseded token as reuse and revokes the
 *   session (#135).
 * - **Fail closed.** A session the server ended, a stored session that cannot
 *   be opened, or tokens that cannot be stored all lead to SignedOut, and the
 *   store is cleared. A transient failure (offline, server unavailable, an
 *   unexpected response) keeps the stored session: the refresh token was not
 *   spent.
 * - **No policy is invented.** The only timing rule is refreshing an access
 *   token within [REFRESH_MARGIN_SECONDS] of the expiry the server reported,
 *   which matches the server's 60 s clock-skew allowance. Session lifetime and
 *   backoff remain owner decisions.
 * - **Nothing is logged.**
 */
public class IdentityAuthenticator(
    private val client: IdentityClient,
    private val store: EncryptedSessionStore,
    private val nowSeconds: () -> Long = { System.currentTimeMillis() / 1000 },
    private val io: CoroutineDispatcher = Dispatchers.IO,
    /**
     * Runs once the server has CONFIRMED a deletion (204), after the session
     * is cleared, under the same lock, on [io]. The app destroys the device
     * key here (core:security DeviceProofKey.discard). It never runs on any
     * other outcome: the key is what a retry of an unconfirmed deletion needs.
     * A failure in it does not change the outcome, which the server decided.
     */
    private val onAccountDeleted: () -> Unit = {},
) {
    private val lock = Mutex()

    /** Requests a code for [msisdn]. */
    public suspend fun requestCode(msisdn: String): IdentityResult<OtpChallenge> =
        withContext(io) { client.requestOtp(msisdn) }

    /** Enters [code] for [requestId]: enrols this device and stores the session. */
    public suspend fun enrol(requestId: String, code: String): EnrolOutcome = lock.withLock {
        withContext(io) {
            when (val result = client.enrol(requestId, code)) {
                is IdentityResult.Failure -> EnrolOutcome.Failed(result.error)
                is IdentityResult.Success -> {
                    val tokens = result.value
                    val sessionId = tokens.sessionId ?: return@withContext EnrolOutcome.Failed(IdentityError.UnexpectedResponse(200))
                    val stored = StoredSession(sessionId, tokens.accessToken, tokens.refreshToken, nowSeconds() + tokens.expiresInSeconds)
                    if (persist(stored)) EnrolOutcome.Enrolled else EnrolOutcome.StorageFailed
                }
            }
        }
    }

    /** A valid access token, refreshing it first if it is about to expire. */
    public suspend fun accessToken(): AuthState = lock.withLock {
        withContext(io) {
            when (val read = store.read()) {
                StoredSessionResult.Absent -> AuthState.SignedOut(SignedOutReason.NOT_SIGNED_IN)
                StoredSessionResult.Unrecoverable -> AuthState.SignedOut(SignedOutReason.STORED_SESSION_UNRECOVERABLE)
                is StoredSessionResult.Present ->
                    if (nowSeconds() < read.session.accessTokenExpiresAtEpochSeconds - REFRESH_MARGIN_SECONDS) {
                        AuthState.Authenticated(read.session.accessToken)
                    } else {
                        refresh(read.session)
                    }
            }
        }
    }

    /**
     * The access token after a resource server refused [rejectedAccessToken]
     * as invalid. Under the same lock as [accessToken]:
     * - if the stored token is still the rejected one, it is refreshed now,
     *   whatever its reported expiry;
     * - if another caller already rotated it, the current token is returned
     *   without refreshing again.
     * A caller retries its request at most once with the result.
     */
    public suspend fun refreshAfterRejection(rejectedAccessToken: String): AuthState = lock.withLock {
        withContext(io) {
            when (val read = store.read()) {
                StoredSessionResult.Absent -> AuthState.SignedOut(SignedOutReason.NOT_SIGNED_IN)
                StoredSessionResult.Unrecoverable -> AuthState.SignedOut(SignedOutReason.STORED_SESSION_UNRECOVERABLE)
                is StoredSessionResult.Present ->
                    if (read.session.accessToken == rejectedAccessToken) refresh(read.session) else AuthState.Authenticated(read.session.accessToken)
            }
        }
    }

    /** Whether a session is stored on this phone and can be opened. Nothing is sent. */
    public suspend fun isSignedIn(): Boolean = lock.withLock {
        withContext(io) { store.read() is StoredSessionResult.Present }
    }

    /**
     * Deletes the account, on every device, with the stored session. Under the
     * same lock as refresh, so the refresh token presented is the current one.
     * The session is removed from this phone only after the server confirms
     * (204), or when the server says it has ended; on any other failure it is
     * kept, so the request can be retried.
     */
    public suspend fun deleteAccount(): AccountDeletionOutcome = lock.withLock {
        withContext(io) {
            val session = (store.read() as? StoredSessionResult.Present)?.session
                ?: return@withContext AccountDeletionOutcome.NotSignedIn
            when (val result = client.deleteAccount(session.sessionId, session.refreshToken)) {
                is IdentityResult.Success -> {
                    // Erased on the server: a session left behind only fails its next refresh.
                    runCatching { store.clear() }
                    runCatching { onAccountDeleted() }
                    AccountDeletionOutcome.Deleted
                }
                is IdentityResult.Failure -> if (result.error == IdentityError.SessionEnded) {
                    runCatching { store.clear() }
                    AccountDeletionOutcome.SessionEnded
                } else {
                    AccountDeletionOutcome.Failed(result.error)
                }
            }
        }
    }

    /** Signs out on this device: the stored session is removed. (Identity has no sign-out endpoint.) */
    public suspend fun signOut() {
        lock.withLock { withContext(io) { store.clear() } }
    }

    private suspend fun refresh(session: StoredSession): AuthState =
        when (val result = client.refresh(session.sessionId, session.refreshToken)) {
            is IdentityResult.Success -> {
                val t = result.value
                val next = StoredSession(session.sessionId, t.accessToken, t.refreshToken, nowSeconds() + t.expiresInSeconds)
                if (persist(next)) AuthState.Authenticated(t.accessToken) else AuthState.SignedOut(SignedOutReason.STORAGE_FAILED)
            }
            is IdentityResult.Failure -> when (result.error) {
                IdentityError.SessionEnded, IdentityError.InvalidDeviceProof -> signedOut(SignedOutReason.SESSION_ENDED)
                IdentityError.InvalidInput -> signedOut(SignedOutReason.STORED_SESSION_UNRECOVERABLE)
                else -> AuthState.Unavailable(result.error)
            }
        }

    /**
     * Stores [session]. If that fails, the previous record is cleared too: its
     * refresh token may already be superseded, and presenting it again would
     * be reuse.
     */
    private suspend fun persist(session: StoredSession): Boolean = try {
        store.write(session)
        true
    } catch (e: CancellationException) {
        throw e
    } catch (e: Exception) {
        runCatching { store.clear() }
        false
    }

    private suspend fun signedOut(reason: SignedOutReason): AuthState {
        runCatching { store.clear() }
        return AuthState.SignedOut(reason)
    }

    public companion object {
        /** Refresh this long before the reported expiry: the server's clock-skew allowance. */
        public const val REFRESH_MARGIN_SECONDS: Long = 60
    }
}
