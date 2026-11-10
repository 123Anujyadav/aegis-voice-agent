package com.callscreen.feature.onboarding

import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import com.callscreen.core.designsystem.theme.CallScreenTheme
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** A09b in its two forms: first time, and once more after a decline (decision packet §3c). */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class ScreeningRoleAfterDeclineUiTest {

    @get:Rule
    val compose = createComposeRule()

    private fun show(previouslyDeclined: Boolean) = compose.setContent {
        CallScreenTheme { ScreeningRoleScreen(onContinueClick = {}, onNotNowClick = {}, previouslyDeclined = previouslyDeclined) }
    }

    @Test
    fun theFirstTimeSaysWhatHappensIfYouSayNo() {
        show(previouslyDeclined = false)
        compose.onNodeWithText("If you say no").assertExists()
        compose.onNodeWithText("What you're missing").assertDoesNotExist()
    }

    @Test
    fun afterADeclineItStatesTheLossAndKeepsBothActions() {
        show(previouslyDeclined = true)
        for (heading in listOf("What Android will ask", "What it enables", "What we'll never do", "What you're missing")) {
            compose.onNodeWithText(heading).assertExists()
        }
        compose.onNodeWithText("If you say no").assertDoesNotExist()
        compose.onNodeWithText("You're currently missing on-device blocking", substring = true).assertExists()
        compose.onNodeWithText("Continue").assertExists()
        compose.onNodeWithText("Not now").assertExists()
    }
}
