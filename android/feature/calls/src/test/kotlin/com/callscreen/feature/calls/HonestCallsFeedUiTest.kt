package com.callscreen.feature.calls

import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.CallType
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** The feed shows only calls it is given; nothing records calls yet, so it says so. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class HonestCallsFeedUiTest {

    @get:Rule
    val compose = createComposeRule()

    @Test
    fun withNoCallsItSaysSoAndShowsNoSampleRecords() {
        compose.setContent { CallScreenTheme { CallsFeedScreen() } }
        compose.onNodeWithText(CallsFeedCopy.EMPTY_TITLE).assertExists()
        for (sample in listOf("Sarah", "National Bank Support", "Suspected Scam", "+91 22 4918 2000")) {
            compose.onNodeWithText(sample, substring = true).assertDoesNotExist()
        }
    }

    @Test
    fun givenCallsItShowsThemInsteadOfTheEmptyState() {
        val call = CallLogItem("c1", "Test caller", "+91 00000 00000", "10:00", CallType.ScreenedAI, "Screened")
        compose.setContent { CallScreenTheme { CallsFeedScreen(calls = listOf(call)) } }
        compose.onNodeWithText("Test caller").assertExists()
        compose.onNodeWithText(CallsFeedCopy.EMPTY_TITLE).assertDoesNotExist()
    }
}
