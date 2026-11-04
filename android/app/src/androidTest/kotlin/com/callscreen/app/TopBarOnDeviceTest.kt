package com.callscreen.app

import android.content.Context
import android.content.Intent
import android.content.pm.ActivityInfo
import android.content.res.Configuration
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createEmptyComposeRule
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
