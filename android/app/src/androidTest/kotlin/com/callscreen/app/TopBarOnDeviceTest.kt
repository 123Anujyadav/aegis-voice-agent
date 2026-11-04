package com.callscreen.app

import android.content.Context
import android.content.Intent
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
 * bar lies over the top of every screen. The shared top bar must start its
 * title below it: before, "App language" and the status bar's clock were
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
    fun theTitleStartsBelowTheStatusBar() {
        val scenario = ActivityScenario.launch<MainActivity>(
            Intent(context, MainActivity::class.java).putExtra(MainActivity.START_DESTINATION_EXTRA, AegisDestinations.APP_LANGUAGE),
        )
        compose.waitUntil(10_000) { compose.onAllNodes(hasText("App language")).fetchSemanticsNodes().isNotEmpty() }
        var statusBar = -1
        scenario.onActivity {
            val insets = ViewCompat.getRootWindowInsets(it.window.decorView)
            statusBar = insets?.getInsets(WindowInsetsCompat.Type.statusBars())?.top ?: 0
        }
        val title = compose.onAllNodes(hasText("App language"))[0].fetchSemanticsNode().boundsInWindow
        assertTrue("the status bar is $statusBar px; the title starts at ${title.top}", statusBar > 0 && title.top >= statusBar)
        scenario.close()
    }
}
