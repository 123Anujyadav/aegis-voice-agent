package com.callscreen.app

import android.content.Context
import android.content.Intent
import android.content.pm.ActivityInfo
import android.content.res.Configuration
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.hasScrollToNodeAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isSelected
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.lifecycle.Lifecycle
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import androidx.test.espresso.Espresso
import androidx.test.ext.junit.runners.AndroidJUnit4
import com.callscreen.app.navigation.AegisDestinations
import dagger.hilt.android.testing.HiltAndroidRule
import dagger.hilt.android.testing.HiltAndroidTest
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/**
 * The app draws edge to edge (MainActivity.enableEdgeToEdge), so the status
 * bar lies over the top of every screen, and in landscape the navigation bar
 * or a cutout can lie over a side. The shared top bar must keep its title
 * clear of them: before, "App language" and the status bar's clock were
 * drawn on top of each other at a large font (design review U1).
 */
@HiltAndroidTest
@RunWith(AndroidJUnit4::class)
class TopBarOnDeviceTest {

    @get:Rule(order = 0)
    val hilt = HiltAndroidRule(this)

    @get:Rule(order = 1)
    val compose = createEmptyComposeRule()

    private val context: Context = ApplicationProvider.getApplicationContext()

    @Before
    fun inject() = hilt.inject()

    @Test
    fun theTitleStaysClearOfTheSystemBarsInPortraitAndLandscape() {
        val scenario = ActivityScenario.launch<MainActivity>(
            Intent(context, MainActivity::class.java).putExtra(MainActivity.START_DESTINATION_EXTRA, AegisDestinations.APP_LANGUAGE),
        )
        try {
            scenario.assertTitleClear("portrait")
            scenario.onActivity { it.requestedOrientation = ActivityInfo.SCREEN_ORIENTATION_LANDSCAPE }
            // The activity is re-created in landscape.
            compose.waitUntil(10_000) {
                var landscape = false
                scenario.onActivity { landscape = it.resources.configuration.orientation == Configuration.ORIENTATION_LANDSCAPE }
                landscape
            }
            scenario.assertTitleClear("landscape")
        } finally {
            scenario.onActivity { it.requestedOrientation = ActivityInfo.SCREEN_ORIENTATION_UNSPECIFIED }
            scenario.close()
        }
    }

    @Test
    fun theBackArrowReturnsWhereBackWouldAndIsAbsentWithNothingBehind() {
        // Owner decision D6, on a device: Settings first (nothing behind it, so no arrow), then
        // App language, whose arrow returns to Settings exactly as Android's Back does.
        val scenario = ActivityScenario.launch<MainActivity>(
            Intent(context, MainActivity::class.java).putExtra(MainActivity.START_DESTINATION_EXTRA, AegisDestinations.TASK_CENTER),
        )
        // Settings is shown when its tab is the selected one. With its bottom bar (F25) the list
        // is shorter, and "Change app language" is composed only once scrolled to.
        val settingsShown = { compose.onAllNodes(hasText("Settings") and hasClickAction() and isSelected()).fetchSemanticsNodes().isNotEmpty() }
        try {
            compose.waitUntil(10_000, settingsShown)
            compose.onAllNodes(hasContentDescription("Back")).assertCountEquals(0)
            compose.onNode(hasScrollToNodeAction()).performScrollToNode(hasText("Change app language"))
            compose.onNode(hasText("Change app language") and hasClickAction()).performClick()
            compose.waitUntil(10_000) { compose.onAllNodes(hasText("App language")).fetchSemanticsNodes().isNotEmpty() }
            compose.onNode(hasContentDescription("Back") and hasClickAction()).performClick()
            compose.waitUntil(10_000, settingsShown)
            compose.onAllNodes(hasText("Change app language")).assertCountEquals(1)
            compose.onAllNodes(hasContentDescription("Back")).assertCountEquals(0)
        } finally {
            scenario.close()
        }
    }

    @Test
    fun backOnATabRootLeavesTheAppInsteadOfReturningToTheLastTab() {
        // F25 (owner, 2026-11-05; 02 §2.4): Assistant, then Protection, then Back leaves the app.
        val scenario = ActivityScenario.launch<MainActivity>(
            Intent(context, MainActivity::class.java).putExtra(MainActivity.START_DESTINATION_EXTRA, AegisDestinations.ASSISTANT_HOME),
        )
        try {
            compose.waitUntil(10_000) { compose.onAllNodes(hasText("Protection") and hasClickAction()).fetchSemanticsNodes().isNotEmpty() }
            compose.onNode(hasText("Protection") and hasClickAction()).performClick()
            compose.waitUntil(10_000) { compose.onAllNodes(hasText("Protection Dashboard")).fetchSemanticsNodes().isNotEmpty() }
            // A tab root has nothing behind it in the app, so no arrow (D6 mirrors Back).
            compose.onAllNodes(hasContentDescription("Back")).assertCountEquals(0)
            Espresso.pressBackUnconditionally()
            // The app is left (finished, or sent to the background), not returned to Assistant.
            val deadline = System.currentTimeMillis() + 10_000
            while (scenario.state == Lifecycle.State.RESUMED && System.currentTimeMillis() < deadline) Thread.sleep(100)
            assertTrue("the app is still shown after Back on a tab root: ${scenario.state}", scenario.state != Lifecycle.State.RESUMED)
        } finally {
            scenario.close()
        }
    }

    private fun ActivityScenario<MainActivity>.assertTitleClear(orientation: String) {
        compose.waitUntil(10_000) { compose.onAllNodes(hasText("App language")).fetchSemanticsNodes().isNotEmpty() }
        compose.waitForIdle()
        var top = -1
        var right = -1
        var width = -1
        onActivity {
            val decor = it.window.decorView
            val insets = ViewCompat.getRootWindowInsets(decor)
                ?.getInsets(WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout())
            top = insets?.top ?: 0
            right = insets?.right ?: 0
            width = decor.width
        }
        val title = compose.onAllNodes(hasText("App language"))[0].fetchSemanticsNode().boundsInWindow
        assertTrue("$orientation: the status bar is $top px; the title starts at ${title.top}", top > 0 && title.top >= top)
        assertTrue("$orientation: the title ends at ${title.right} of $width px, $right px under a side bar", title.right <= width - right)
    }
}
