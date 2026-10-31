package com.callscreen.app.session

import com.callscreen.core.auth.IdentityAuthenticator
import com.callscreen.core.auth.SessionEndCause
import com.callscreen.core.auth.SessionState
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

/**
 * What the navigation graph needs to know about the session: whether one is
 * stored on this phone, and when that changes. Local state only: neither
 * member sends anything to a server.
 */
public interface SessionSignals {
    /** The session as the authenticator last saw its store. */
    public val state: StateFlow<SessionState>

    /** Whether a session is stored on this phone and can be opened. */
    public suspend fun hasStoredSession(): Boolean

    /**
     * No session handling: a build with no identity server, or a test that
     * hosts the graph on its own. Nothing is ever stored, so the app starts
     * at onboarding exactly as before, and no notice is ever shown.
     */
    public object None : SessionSignals {
        override val state: StateFlow<SessionState> =
            MutableStateFlow<SessionState>(SessionState.SignedOut(SessionEndCause.NOT_SIGNED_IN)).asStateFlow()

        override suspend fun hasStoredSession(): Boolean = false
    }

    public companion object {
        /** The app's session over its one authenticator, or [None] when sign-in is unavailable. */
        public fun of(authenticator: IdentityAuthenticator?): SessionSignals =
            if (authenticator == null) {
                None
            } else {
                object : SessionSignals {
                    override val state: StateFlow<SessionState> = authenticator.sessionState
                    override suspend fun hasStoredSession(): Boolean = authenticator.isSignedIn()
                }
            }
    }
}
