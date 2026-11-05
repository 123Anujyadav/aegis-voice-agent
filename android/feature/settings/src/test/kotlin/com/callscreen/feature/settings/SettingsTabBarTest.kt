package com.callscreen.feature.settings

import androidx.compose.ui.test.assertIsSelected
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.performClick
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.AegisTab
import java.util.Locale
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/**
 * Settings is the fourth tab, with the bar like the other three
 * (`docs/ux/android/02-navigation-graph.md` §2.1; F25, owner 2026-11-05): its
 * Back leaves the app, so the bar is its way to the other tabs.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class SettingsTabBarTest {

    @get:Rule
    val compose = createComposeRule()

    @Test
    fun settingsShowsTheTabBarWithSettingsSelectedAndReachesTheOtherTabs() {
        val chosen = mutableListOf<AegisTab>()
        compose.setContent {
            CallScreenTheme {
                TaskCenterScreen(tasks = emptyList(), phoneLocale = Locale.ENGLISH, onTabSelected = { chosen += it })
            }
        }
        compose.onNode(hasText("Settings") and hasClickAction()).assertIsSelected()
        compose.onNode(hasText("Calls") and hasClickAction()).performClick()
        // Settings itself is re-selected, which never navigates.
        compose.onNode(hasText("Settings") and hasClickAction()).performClick()
        assertEquals(listOf(AegisTab.Calls), chosen)
    }
}
