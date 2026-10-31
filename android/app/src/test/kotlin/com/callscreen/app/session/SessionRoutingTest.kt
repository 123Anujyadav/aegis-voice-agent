package com.callscreen.app.session

import com.callscreen.app.navigation.AegisDestinations
import com.callscreen.core.auth.SessionEndCause
import com.callscreen.core.auth.SessionState
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertNull
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

@DisplayName("Session routing")
class SessionRoutingTest {

    private val signedIn = SessionState.SignedIn
    private fun out(cause: SessionEndCause) = SessionState.SignedOut(cause)

    @Test
    @DisplayName("after the splash: Calls with a stored session, onboarding without one")
    fun afterSplash() {
        assertEquals(AegisDestinations.CALLS_FEED, SessionRouting.afterSplash(hasStoredSession = true))
        assertEquals(AegisDestinations.WELCOME, SessionRouting.afterSplash(hasStoredSession = false))
    }

    @Test
    @DisplayName("a stored session that goes away shows the notice with its cause")
    fun signedInToSignedOutShowsTheNotice() {
        for (cause in SessionEndCause.entries - SessionEndCause.ACCOUNT_DELETED) {
            assertEquals(cause, SessionRouting.signedOutNotice(signedIn, out(cause), AegisDestinations.CALLS_FEED), "$cause")
        }
    }

    @Test
    @DisplayName("no notice without a stored session going away: start-up, a repeat, or signing in")
    fun onlyAChangeFromSignedInCounts() {
        val calls = AegisDestinations.CALLS_FEED
        assertNull(SessionRouting.signedOutNotice(SessionState.Unknown, out(SessionEndCause.NOT_SIGNED_IN), calls))
        assertNull(SessionRouting.signedOutNotice(SessionState.Unknown, out(SessionEndCause.SESSION_ENDED), calls))
        assertNull(SessionRouting.signedOutNotice(out(SessionEndCause.SESSION_ENDED), out(SessionEndCause.NOT_SIGNED_IN), calls))
        assertNull(SessionRouting.signedOutNotice(out(SessionEndCause.NOT_SIGNED_IN), signedIn, calls))
        assertNull(SessionRouting.signedOutNotice(signedIn, signedIn, calls))
        assertNull(SessionRouting.signedOutNotice(SessionState.Unknown, signedIn, calls))
    }

    @Test
    @DisplayName("the deletion flow is left alone: it reports its own outcome and navigates itself")
    fun deletionIsNotInterrupted() {
        assertNull(SessionRouting.signedOutNotice(signedIn, out(SessionEndCause.ACCOUNT_DELETED), AegisDestinations.CALLS_FEED))
        assertNull(SessionRouting.signedOutNotice(signedIn, out(SessionEndCause.ACCOUNT_DELETED), AegisDestinations.ACCOUNT_DELETION))
        assertNull(SessionRouting.signedOutNotice(signedIn, out(SessionEndCause.SESSION_ENDED), AegisDestinations.ACCOUNT_DELETION))
    }

    @Test
    @DisplayName("the notice never stacks on itself")
    fun noticeNotRepeatedOnTheNotice() {
        assertNull(SessionRouting.signedOutNotice(signedIn, out(SessionEndCause.SESSION_ENDED), AegisDestinations.SIGNED_OUT))
    }

    @Test
    @DisplayName("A73's copy gives a reason only when the app knows it, and says on-device blocking continues")
    fun signedOutCopy() {
        assertEquals(listOf(SignedOutCopy.YOU_SIGNED_OUT), SignedOutCopy.lines(SessionEndCause.SIGNED_OUT_ON_THIS_DEVICE, false))
        assertEquals(listOf(SignedOutCopy.NOT_VERIFIED), SignedOutCopy.lines(SessionEndCause.STORED_SESSION_UNRECOVERABLE, false))
        for (cause in listOf(SessionEndCause.SESSION_ENDED, SessionEndCause.STORAGE_FAILED, SessionEndCause.NOT_SIGNED_IN, null)) {
            assertEquals(emptyList<String>(), SignedOutCopy.lines(cause, false), "$cause")
        }
        assertEquals(listOf(SignedOutCopy.BLOCKING_STILL_ON), SignedOutCopy.lines(SessionEndCause.SESSION_ENDED, true))
    }
}
