package com.callscreen.app.session

import com.callscreen.app.navigation.AegisDestinations
import com.callscreen.core.auth.SessionEndCause
import com.callscreen.core.auth.SessionState
import kotlinx.coroutines.CancellationException

/**
 * Where the session sends the app (docs/ux/android/02-navigation-graph.md
 * §2.2): a cold start with an account goes to Calls; without one, to
 * onboarding; a session that ends while the app is in use shows A73 once.
 */
internal object SessionRouting {

    /**
     * Where the splash screen goes. Decided from the session stored on this
     * phone only: nothing is sent, so an offline start still reaches Calls.
     * A session the server has since ended is discovered the next time
     * anything asks identity for a token, and then shows the notice.
     */
    fun afterSplash(hasStoredSession: Boolean): String =
        if (hasStoredSession) AegisDestinations.CALLS_FEED else AegisDestinations.WELCOME

    /**
     * Where the splash screen goes for [session]. A session store that cannot
     * be read at all (a corrupt or unreadable file) routes as no session, to
     * onboarding, instead of crashing every launch. That concerns this phone
     * only: the session state is left as it was, nothing is sent, and nothing
     * claims the server ended the session. Cancellation is never swallowed.
     */
    suspend fun afterSplash(session: SessionSignals): String {
        val stored = try {
            session.hasStoredSession()
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            false
        }
        return afterSplash(stored)
    }

    /**
     * The cause to show on the signed-out notice for a change from [previous]
     * to [current] while [currentRoute] is showing, or null for no notice.
     *
     * Only a session that was stored and then went away shows it, once: the
     * state stays signed out afterwards, so the same change is never seen
     * twice. Account deletion is excluded: its screen reports the outcome
     * itself and navigates on its own.
     */
    fun signedOutNotice(previous: SessionState, current: SessionState, currentRoute: String?): SessionEndCause? {
        if (previous != SessionState.SignedIn) return null
        val cause = (current as? SessionState.SignedOut)?.cause ?: return null
        if (cause == SessionEndCause.ACCOUNT_DELETED) return null
        if (currentRoute == AegisDestinations.ACCOUNT_DELETION) return null
        if (currentRoute == AegisDestinations.SIGNED_OUT) return null
        return cause
    }
}
