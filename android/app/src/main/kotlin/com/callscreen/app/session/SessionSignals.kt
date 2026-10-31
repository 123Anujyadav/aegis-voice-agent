package com.callscreen.app.session

import com.callscreen.core.auth.IdentityAuthenticator
import com.callscreen.core.auth.SessionEndCause
import com.callscreen.core.auth.SessionState
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.withContext

/**
 * What the navigation graph needs to know about the session: whether one is
 * stored on this phone, and when that changes. Local state only: neither
 * member sends anything to a server.
 */
public interface SessionSignals {
    /**
     * The session as the authenticator last saw its store. It suspends
     * because finding the authenticator may build the identity flow, which
     * is done off the main thread.
     */
    public suspend fun state(): StateFlow<SessionState>

    /**
     * Whether a session is stored on this phone and can be opened. It throws
     * when the store cannot be read at all (a corrupt or unreadable file):
     * the splash routes through [SessionRouting.afterSplash], which treats
     * that as no session.
     */
    public suspend fun hasStoredSession(): Boolean

    /**
     * No session handling: a build with no identity server, or a test that
     * hosts the graph on its own. Nothing is ever stored, so the app starts
     * at onboarding exactly as before, and no notice is ever shown.
     */
    public object None : SessionSignals {
        private val signedOut: StateFlow<SessionState> =
            MutableStateFlow<SessionState>(SessionState.SignedOut(SessionEndCause.NOT_SIGNED_IN)).asStateFlow()

        override suspend fun state(): StateFlow<SessionState> = signedOut

        override suspend fun hasStoredSession(): Boolean = false
    }

    public companion object {
        /**
         * The app's session over its one authenticator, found by
         * [authenticator] on [io] the first time either member is used, so
         * building the identity flow (its HTTP client and session store)
         * never runs on the main thread at launch. Behaves as [None] when
         * [authenticator] returns null (sign-in unavailable).
         */
        public fun of(io: CoroutineDispatcher = Dispatchers.IO, authenticator: () -> IdentityAuthenticator?): SessionSignals =
            FromAuthenticator(io, authenticator)
    }
}

private class FromAuthenticator(
    private val io: CoroutineDispatcher,
    find: () -> IdentityAuthenticator?,
) : SessionSignals {
    // Synchronised: the splash and the notice may ask at the same time.
    private val found by lazy(find)

    private suspend fun authenticator(): IdentityAuthenticator? = withContext(io) { found }

    override suspend fun state(): StateFlow<SessionState> = authenticator()?.sessionState ?: SessionSignals.None.state()

    override suspend fun hasStoredSession(): Boolean = authenticator()?.isSignedIn() ?: false
}
