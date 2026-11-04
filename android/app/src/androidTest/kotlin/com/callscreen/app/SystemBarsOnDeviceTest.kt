package com.callscreen.app

import android.content.Context
import android.content.Intent
import android.content.pm.ActivityInfo
import android.content.res.Configuration
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.test.hasScrollAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.test.performScrollToNode
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
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
 * The app draws edge to edge (MainActivity.enableEdgeToEdge): the status bar,
 * the navigation bar and any cutout lie over its edges. Each kind of screen
 * keeps its words and controls inside them: the full-screen onboarding column
 * (Welcome), the tab bar (Protection), and a screen inside a Scaffold, which
 * must not make room for the status bar twice (App language). See
 * TopBarOnDeviceTest for the top bar.
 */
@HiltAndroidTest
@RunWith(AndroidJUnit4::class)
class SystemBarsOnDeviceTest {

    @get:Rule(order = 0)
    val hilt = HiltAndroidRule(this)

    @get:Rule(order = 1)
    val compose = createEmptyComposeRule()

    private val context: Context = ApplicationProvider.getApplicationContext()

    @Before
    fun inject() = hilt.inject()

    /** The system bars and cutout over the window's edges, and the window's size, in pixels. */
    private class Bars(val left: Int, val top: Int, val right: Int, val bottom: Int, val width: Int, val height: Int) {
        fun assertInside(what: String, bounds: Rect) {
            assertTrue(
                "$what at $bounds is under a system bar: bars l=$left t=$top r=$right b=$bottom, window ${width}x$height",
                bounds.left >= left - 1 && bounds.top >= top - 1 && bounds.right <= width - right + 1 && bounds.bottom <= height - bottom + 1,
            )
        }
    }

    private fun launch(destination: String): ActivityScenario<MainActivity> =
        ActivityScenario.launch(Intent(context, MainActivity::class.java).putExtra(MainActivity.START_DESTINATION_EXTRA, destination))

    private fun ActivityScenario<MainActivity>.bars(): Bars {
        compose.waitForIdle()
        var bars: Bars? = null
        onActivity {
            val decor = it.window.decorView
            val insets = ViewCompat.getRootWindowInsets(decor)
                ?.getInsets(WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout())
            bars = Bars(insets?.left ?: 0, insets?.top ?: 0, insets?.right ?: 0, insets?.bottom ?: 0, decor.width, decor.height)
        }
        return checkNotNull(bars)
    }

    private fun ActivityScenario<MainActivity>.rotate(orientation: Int, expected: Int) {
        onActivity { it.requestedOrientation = orientation }
        compose.waitUntil(10_000) {
            var now = -1
            onActivity { now = it.resources.configuration.orientation }
            now == expected
        }
    }

    private fun await(text: String) =
        compose.waitUntil(10_000) { compose.onAllNodes(hasText(text)).fetchSemanticsNodes().isNotEmpty() }

    /** The node showing [text], brought into view when the screen scrolls. */
    private fun shown(text: String): Rect {
        await(text)
        compose.onAllNodes(hasScrollAction()).fetchSemanticsNodes().takeIf { it.isNotEmpty() }?.let {
            compose.onAllNodes(hasScrollAction())[0].performScrollToNode(hasText(text))
        }
        compose.waitForIdle()
        return compose.onAllNodes(hasText(text), useUnmergedTree = true)[0].fetchSemanticsNode().boundsInWindow
    }

    @Test
    fun welcomeKeepsItsLanguageControlAndActionClearOfTheSystemBarsInPortraitAndLandscape() {
        val scenario = launch(AegisDestinations.WELCOME)
        try {
            for ((name, orientation, expected) in listOf(
                Triple("portrait", ActivityInfo.SCREEN_ORIENTATION_PORTRAIT, Configuration.ORIENTATION_PORTRAIT),
                Triple("landscape", ActivityInfo.SCREEN_ORIENTATION_LANDSCAPE, Configuration.ORIENTATION_LANDSCAPE),
            )) {
                scenario.rotate(orientation, expected)
                await("Language")
                val bars = scenario.bars()
                bars.assertInside("$name: Language", shown("Language"))
                bars.assertInside("$name: Get Protected", shown("Get Protected"))
            }
        } finally {
            scenario.onActivity { it.requestedOrientation = ActivityInfo.SCREEN_ORIENTATION_UNSPECIFIED }
            scenario.close()
        }
    }

    @Test
    fun theTabsStayAboveTheNavigationBar() {
        val scenario = launch(AegisDestinations.PROTECTION_DASHBOARD)
        try {
            await("Protection Dashboard")
            val bars = scenario.bars()
            for (tab in listOf("Calls", "Protection", "Assistant")) {
                // The tab's label: the lowest node with that text, below any card that shares it.
                val label = compose.onAllNodes(hasText(tab), useUnmergedTree = true).fetchSemanticsNodes().maxBy { it.boundsInWindow.bottom }
                bars.assertInside("the $tab tab", label.boundsInWindow)
            }
        } finally {
            scenario.close()
        }
    }

    @Test
    fun aScreenInsideAScaffoldMakesRoomForTheStatusBarOnce() {
        val scenario = launch(AegisDestinations.APP_LANGUAGE)
        try {
            await("App language")
            val bars = scenario.bars()
            val intro = compose.onAllNodes(hasText("Choose the language of this app's screens", substring = true), useUnmergedTree = true)
                .fetchSemanticsNodes().first().boundsInWindow
            // Below the status bar, the 64 dp top bar and the column's 16 dp padding, and not a second status bar lower.
            val once = bars.top + (64 + 16) * context.resources.displayMetrics.density
            assertTrue("the intro starts at ${intro.top}, expected about $once (status bar ${bars.top} px)", intro.top >= once - 1 && intro.top < once + bars.top - 1)
        } finally {
            scenario.close()
        }
    }
}
