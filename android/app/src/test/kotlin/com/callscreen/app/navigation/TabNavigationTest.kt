package com.callscreen.app.navigation

import android.app.Application
import androidx.activity.ComponentActivity
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasScrollToNodeAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.navigation.NavHostController
import androidx.navigation.compose.rememberNavController
import com.callscreen.app.session.SessionSignals
import com.callscreen.core.designsystem.theme.CallScreenTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/**
 * The tabs through the app's real navigation graph, as the navigation
 * specification has them (`docs/ux/android/02-navigation-graph.md` §2.1, §2.4,
 * §2.6; F25, owner 2026-11-05): each tab keeps its own back stack, only one
 * tab is ever on the back stack, Back on a tab root leaves the app, and
 * choosing the tab already shown adds nothing.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34], application = Application::class)
class TabNavigationTest {

    @get:Rule
    val compose = createAndroidComposeRule<ComponentActivity>()

    private lateinit var nav: NavHostController

    private fun graph(start: String) {
        compose.setContent {
            nav = rememberNavController()
            CallScreenTheme {
                AegisNavGraph(navController = nav, startDestination = start, includeDesignPreviews = false, session = SessionSignals.None)
            }
        }
        compose.waitForIdle()
    }

    /** The routes on the back stack, oldest first, without the graph itself. */
    private fun routes(): List<String> =
        nav.currentBackStack.value.mapNotNull { entry -> entry.destination.route?.takeIf { it != nav.graph.route } }

    private fun currentId(): String = checkNotNull(nav.currentBackStackEntry).id

    private fun tab(label: String) {
        compose.onNode(hasText(label) and hasClickAction()).performClick()
        compose.waitForIdle()
    }

    /** Whether Back would be handled inside the app; false means it goes to Android, which leaves the app. */
    private fun backStaysInTheApp(): Boolean {
        var handled = false
        compose.runOnUiThread { handled = compose.activity.onBackPressedDispatcher.hasEnabledCallbacks() }
        return handled
    }

    @Test
    fun onlyTheChosenTabIsOnTheBackStackAndBackOnItsRootLeavesTheApp() {
        graph(AegisDestinations.ASSISTANT_HOME)
        assertFalse("Back on a tab root leaves the app", backStaysInTheApp())
        tab("Protection")
        // Not [Assistant, Protection]: Back never cycles through the tabs (02 §2.4).
        assertEquals(listOf(AegisDestinations.PROTECTION_DASHBOARD), routes())
        assertFalse("Back on a tab root leaves the app", backStaysInTheApp())
        tab("Calls")
        assertEquals(listOf(AegisDestinations.CALLS_FEED), routes())
        assertFalse(backStaysInTheApp())
    }

    @Test
    fun aTabLeftAndChosenAgainIsRestoredNotRecreated() {
        graph(AegisDestinations.ASSISTANT_HOME)
        val assistant = currentId()
        tab("Protection")
        tab("Assistant")
        // The same back-stack entry, its saved state restored (02 §2.1, §2.6), and still alone.
        assertEquals(listOf(AegisDestinations.ASSISTANT_HOME), routes())
        assertEquals(assistant, currentId())
    }

    @Test
    fun choosingTheTabAlreadyShownAddsNothing() {
        graph(AegisDestinations.ASSISTANT_HOME)
        val assistant = currentId()
        tab("Assistant")
        tab("Assistant")
        assertEquals(listOf(AegisDestinations.ASSISTANT_HOME), routes())
        assertEquals(assistant, currentId())
    }

    @Test
    fun settingsHasTheTabBarAndKeepsItsOwnStack() {
        graph(AegisDestinations.TASK_CENTER)
        val settings = currentId()
        // A detail screen above the tab root: Back stays in the app there.
        compose.onNode(hasScrollToNodeAction()).performScrollToNode(hasText("Change app language"))
        compose.onNode(hasText("Change app language") and hasClickAction()).performClick()
        compose.waitForIdle()
        assertTrue(backStaysInTheApp())
        compose.runOnUiThread { compose.activity.onBackPressedDispatcher.onBackPressed() }
        compose.waitForIdle()
        // Settings' own bar reaches the other tabs (02 §2.1: four destinations).
        tab("Calls")
        assertEquals(listOf(AegisDestinations.CALLS_FEED), routes())
        tab("Settings")
        assertEquals(listOf(AegisDestinations.TASK_CENTER), routes())
        assertEquals(settings, currentId())
    }

    @Test
    fun startingOverForgetsEveryTabsSavedStack() {
        // After a sign-out or an account deletion nothing from before may come back with a tab.
        graph(AegisDestinations.ASSISTANT_HOME)
        val before = currentId()
        tab("Protection")
        compose.runOnUiThread { nav.startOver(AegisDestinations.CALLS_FEED) }
        compose.waitForIdle()
        assertEquals(listOf(AegisDestinations.CALLS_FEED), routes())
        tab("Assistant")
        assertEquals(listOf(AegisDestinations.ASSISTANT_HOME), routes())
        assertNotEquals("the Assistant tab came back from before", before, currentId())
    }
}
