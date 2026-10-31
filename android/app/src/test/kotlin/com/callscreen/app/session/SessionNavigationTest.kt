package com.callscreen.app.session

import android.app.Application
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.navigation.NavHostController
import androidx.navigation.compose.rememberNavController
import com.callscreen.app.navigation.AegisDestinations
import com.callscreen.app.navigation.AegisNavGraph
import com.callscreen.core.auth.SessionEndCause
import com.callscreen.core.auth.SessionState
import com.callscreen.core.designsystem.theme.CallScreenTheme
import java.io.IOException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/**
 * The navigation graph against a fake session: the cold-start route, leaving
 * onboarding, and the signed-out notice. Nothing here touches identity, the
 * session store or the device key.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34], application = Application::class)
class SessionNavigationTest {

    @get:Rule
    val compose = createComposeRule()

    private class FakeSession(private val stored: Boolean, initial: SessionState) : SessionSignals {
        val flow = MutableStateFlow(initial)
        override suspend fun state(): StateFlow<SessionState> = flow
        override suspend fun hasStoredSession(): Boolean = stored
    }

    /** A session store that cannot be read at all, as DataStore throws for a corrupt file. */
    private class UnreadableSession : SessionSignals {
        val flow = MutableStateFlow<SessionState>(SessionState.Unknown)
        override suspend fun state(): StateFlow<SessionState> = flow
        override suspend fun hasStoredSession(): Boolean = throw IOException("corrupt session file")
    }

    private fun graph(start: String, session: SessionSignals): NavHostController {
        lateinit var nav: NavHostController
        compose.setContent {
            nav = rememberNavController()
            CallScreenTheme {
                AegisNavGraph(navController = nav, startDestination = start, includeDesignPreviews = false, session = session)
            }
        }
        compose.waitForIdle()
        return nav
    }

    /** The routes on the back stack, oldest first, without the graph itself. */
    private fun NavHostController.routes(): List<String> =
        currentBackStack.value.mapNotNull { entry -> entry.destination.route?.takeIf { it != graph.route } }

    private fun afterSplash() {
        compose.mainClock.advanceTimeBy(2_500)
        compose.waitForIdle()
    }

    @Test
    fun coldStartWithAStoredSessionGoesToCallsAlone() {
        val nav = graph(AegisDestinations.SPLASH, FakeSession(stored = true, initial = SessionState.SignedIn))
        afterSplash()
        assertEquals(listOf(AegisDestinations.CALLS_FEED), nav.routes())
    }

    @Test
    fun coldStartWithoutOneGoesToWelcome() {
        val nav = graph(AegisDestinations.SPLASH, FakeSession(stored = false, initial = SessionState.SignedOut(SessionEndCause.NOT_SIGNED_IN)))
        afterSplash()
        assertEquals(listOf(AegisDestinations.WELCOME), nav.routes())
    }

    @Test
    fun anUnreadableSessionStoreGoesToWelcomeInsteadOfCrashing() {
        val session = UnreadableSession()
        val nav = graph(AegisDestinations.SPLASH, session)
        afterSplash()
        assertEquals(listOf(AegisDestinations.WELCOME), nav.routes())
        // Routing only: nothing claims the server ended the session.
        assertEquals(SessionState.Unknown, session.flow.value)
        compose.onNodeWithText(SignedOutCopy.TITLE).assertDoesNotExist()
    }

    @Test
    fun noSessionHandlingStartsAtWelcomeAsBefore() {
        val nav = graph(AegisDestinations.SPLASH, SessionSignals.None)
        afterSplash()
        assertEquals(listOf(AegisDestinations.WELCOME), nav.routes())
    }

    @Test
    fun finishingSetupLeavesOnlyCalls() {
        val nav = graph(AegisDestinations.WELCOME, FakeSession(stored = true, initial = SessionState.SignedIn))
        compose.runOnUiThread {
            nav.navigate(AegisDestinations.AI_SETUP)
            nav.navigate(AegisDestinations.SETUP_COMPLETE)
        }
        compose.waitForIdle()
        compose.onNodeWithText("Go to Calls Feed").performClick()
        compose.waitForIdle()
        assertEquals(listOf(AegisDestinations.CALLS_FEED), nav.routes())
    }

    @Test
    fun aSessionThatEndsShowsTheNoticeOnceAndAloneOnTheStack() {
        val session = FakeSession(stored = true, initial = SessionState.SignedIn)
        val nav = graph(AegisDestinations.CALLS_FEED, session)

        session.flow.value = SessionState.SignedOut(SessionEndCause.SESSION_ENDED)
        compose.waitForIdle()
        assertEquals(listOf(AegisDestinations.SIGNED_OUT), nav.routes())
        assertEquals(SessionEndCause.SESSION_ENDED.name, nav.currentBackStackEntry?.arguments?.getString("cause"))
        compose.onNodeWithText(SignedOutCopy.TITLE).assertExists()
        val notice = nav.currentBackStackEntry

        // Another signed-out value is not a new change: no second notice.
        session.flow.value = SessionState.SignedOut(SessionEndCause.NOT_SIGNED_IN)
        compose.waitForIdle()
        assertSame(notice, nav.currentBackStackEntry)
        assertEquals(listOf(AegisDestinations.SIGNED_OUT), nav.routes())

        compose.onNodeWithText(SignedOutCopy.SIGN_IN).performClick()
        compose.waitForIdle()
        assertEquals(listOf(AegisDestinations.WELCOME), nav.routes())
    }

    @Test
    fun aConfirmedDeletionDoesNotShowTheNotice() {
        val session = FakeSession(stored = true, initial = SessionState.SignedIn)
        val nav = graph(AegisDestinations.CALLS_FEED, session)
        session.flow.value = SessionState.SignedOut(SessionEndCause.ACCOUNT_DELETED)
        compose.waitForIdle()
        assertEquals(listOf(AegisDestinations.CALLS_FEED), nav.routes())
    }

    @Test
    fun startingSignedOutShowsNoNotice() {
        val session = FakeSession(stored = false, initial = SessionState.SignedOut(SessionEndCause.SESSION_ENDED))
        val nav = graph(AegisDestinations.CALLS_FEED, session)
        assertEquals(listOf(AegisDestinations.CALLS_FEED), nav.routes())
    }
}
