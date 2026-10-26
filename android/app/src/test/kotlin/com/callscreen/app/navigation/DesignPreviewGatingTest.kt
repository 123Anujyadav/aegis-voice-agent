package com.callscreen.app.navigation

import android.app.Application
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.navigation.NavHostController
import androidx.navigation.compose.rememberNavController
import com.callscreen.core.designsystem.theme.CallScreenTheme
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/**
 * The prototype screens exist only in debug builds: a release graph does not
 * register them, so nothing in a release build can open their sample data.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34], application = Application::class)
class DesignPreviewGatingTest {

    @get:Rule
    val compose = createComposeRule()

    private val productionRoutes = listOf(
        AegisDestinations.WELCOME, AegisDestinations.SIM_DETECTION, AegisDestinations.SCREENING_ROLE,
        AegisDestinations.CALL_FORWARDING, AegisDestinations.SETUP_COMPLETE, AegisDestinations.CALLS_FEED,
        AegisDestinations.PROTECTION_DASHBOARD, AegisDestinations.CALLER_LIST, AegisDestinations.ASSISTANT_HOME,
        AegisDestinations.VOICE_SETUP, AegisDestinations.VOICE_CONVERSATION, AegisDestinations.TASK_CENTER,
    )

    private fun graph(includePreviews: Boolean): NavHostController {
        lateinit var nav: NavHostController
        compose.setContent {
            nav = rememberNavController()
            CallScreenTheme { AegisNavGraph(navController = nav, includeDesignPreviews = includePreviews) }
        }
        compose.waitForIdle()
        return nav
    }

    @Test
    fun aReleaseGraphHasNoPrototypeScreens() {
        val nav = graph(includePreviews = false)
        for (route in AegisDestinations.DESIGN_PREVIEWS) assertNull(route, nav.graph.findNode(route))
        for (route in productionRoutes) assertNotNull(route, nav.graph.findNode(route))
    }

    @Test
    fun aDebugGraphHasThemFramedAsPreviews() {
        val nav = graph(includePreviews = true)
        for (route in AegisDestinations.DESIGN_PREVIEWS) assertNotNull(route, nav.graph.findNode(route))
        compose.runOnUiThread { nav.navigate(AegisDestinations.FAMILY_SHIELD) }
        compose.onNodeWithText(DESIGN_PREVIEW_LABEL).assertExists()
    }
}
