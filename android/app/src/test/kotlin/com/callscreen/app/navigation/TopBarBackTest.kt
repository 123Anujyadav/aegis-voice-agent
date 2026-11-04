package com.callscreen.app.navigation

import android.app.Application
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasScrollToNodeAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.navigation.NavHostController
import androidx.navigation.compose.rememberNavController
import com.callscreen.app.session.SessionSignals
import com.callscreen.core.designsystem.theme.CallScreenTheme
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/**
 * The top bar's Back arrow through the app's real navigation graph (owner
 * decision D6): shown when the app has a screen behind, absent on a screen
 * with nothing behind it, and doing exactly what Android's Back does.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34], application = Application::class)
class TopBarBackTest {

    @get:Rule
    val compose = createComposeRule()

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

    private fun arrow() = compose.onNodeWithContentDescription("Back")

    @Test
    fun aScreenOpenedFromSettingsHasTheArrowAndItReturnsToSettings() {
        graph(AegisDestinations.TASK_CENTER)
        // Settings is the first screen here: nothing behind it, so no arrow.
        arrow().assertDoesNotExist()
        compose.onNode(hasScrollToNodeAction()).performScrollToNode(hasText("Change app language"))
        compose.onNode(hasText("Change app language") and hasClickAction()).performClick()
        compose.waitForIdle()
        assertEquals(listOf(AegisDestinations.TASK_CENTER, AegisDestinations.APP_LANGUAGE), routes())

        arrow().performClick()
        compose.waitForIdle()
        // Exactly where Android's Back goes: Settings, with the language screen gone.
        assertEquals(listOf(AegisDestinations.TASK_CENTER), routes())
        compose.onNode(hasScrollToNodeAction()).performScrollToNode(hasText("Change app language"))
        arrow().assertDoesNotExist()
    }

    @Test
    fun theArrowFollowsTheBackStackAsBuilt() {
        // As built, switching tabs adds to the back stack, and Back returns to the previous tab
        // (production-readiness F25). The arrow does the same, no more and no less.
        graph(AegisDestinations.ASSISTANT_HOME)
        arrow().assertDoesNotExist()
        compose.onNode(hasText("Protection") and hasClickAction()).performClick()
        compose.waitForIdle()
        assertEquals(listOf(AegisDestinations.ASSISTANT_HOME, AegisDestinations.PROTECTION_DASHBOARD), routes())
        arrow().performClick()
        compose.waitForIdle()
        assertEquals(listOf(AegisDestinations.ASSISTANT_HOME), routes())
        arrow().assertDoesNotExist()
    }
}
