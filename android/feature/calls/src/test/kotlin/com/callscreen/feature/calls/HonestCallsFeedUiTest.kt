package com.callscreen.feature.calls

import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.components.CallType
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config

/** The feed shows only calls it is given; nothing records calls yet, so it says so. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class HonestCallsFeedUiTest {

    @get:Rule
    val compose = createComposeRule()

    /** The shipped English resource: res/values/strings.xml. */
    private fun s(id: Int): String = RuntimeEnvironment.getApplication().getString(id)

    @Test
    fun withNoCallsItSaysSoAndShowsNoSampleRecords() {
        compose.setContent { CallScreenTheme { CallsFeedScreen() } }
        compose.onNodeWithText(s(CallsFeedCopy.EMPTY_TITLE)).assertExists()
        for (sample in listOf("Sarah", "National Bank Support", "Suspected Scam", "+91 22 4918 2000")) {
            compose.onNodeWithText(sample, substring = true).assertDoesNotExist()
        }
    }

    @Test
    fun givenCallsItShowsThemInsteadOfTheEmptyState() {
        val call = CallLogItem("c1", "Test caller", "+91 00000 00000", "10:00", CallType.ScreenedAI, "Screened")
        compose.setContent { CallScreenTheme { CallsFeedScreen(calls = listOf(call)) } }
        compose.onNodeWithText("Test caller").assertExists()
        compose.onNodeWithText(s(CallsFeedCopy.EMPTY_TITLE)).assertDoesNotExist()
    }

    @Test
    fun withTheRoleLostTheBannerSaysWhatStoppedAndOffersToSwitchBack() {
        var switched = 0
        compose.setContent { CallScreenTheme { CallsFeedScreen(roleLost = true, onSwitchBackClick = { switched++ }) } }
        compose.onNodeWithText(s(ScreeningRoleBannerCopy.LOST)).assertExists()
        compose.onNodeWithText(s(ScreeningRoleBannerCopy.SWITCH_BACK)).performClick()
        assertEquals(1, switched)
        compose.onNodeWithText(s(CallsFeedCopy.EMPTY_TITLE)).assertExists()
    }
}
