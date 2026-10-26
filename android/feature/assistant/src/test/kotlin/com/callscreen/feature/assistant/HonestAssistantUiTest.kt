package com.callscreen.feature.assistant

import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.isToggleable
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import com.callscreen.core.designsystem.theme.CallScreenTheme
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** The Assistant tab claims nothing it cannot do: no screening switch, no saved voice, no listening. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class HonestAssistantUiTest {

    @get:Rule
    val compose = createComposeRule()

    @Test
    fun screeningIsNotAvailableAndHasNoSwitch() {
        compose.setContent { CallScreenTheme { AssistantHomeScreen() } }
        compose.onNodeWithText(AssistantCopy.SCREENING_UNAVAILABLE).assertExists()
        // A toggle that saves nothing and changes nothing would mislead.
        compose.onNode(isToggleable()).assertDoesNotExist()
        compose.onNodeWithText("on your behalf", substring = true).assertDoesNotExist()
    }

    @Test
    fun aVoiceCannotBeSavedAndNoneIsShownAsActive() {
        compose.setContent { CallScreenTheme { VoiceSetupScreen() } }
        compose.onNodeWithText(AssistantCopy.VOICES_INTRO).assertExists()
        compose.onNodeWithText(AssistantCopy.SAVE_UNAVAILABLE).assertIsNotEnabled()
        compose.onNodeWithText("Active", ignoreCase = true).assertDoesNotExist()
    }

    @Test
    fun theVoiceSessionSaysTheMicrophoneIsNotUsed() {
        compose.setContent { CallScreenTheme { VoiceConversationScreen() } }
        compose.onNodeWithText(AssistantCopy.NO_MICROPHONE, substring = true).assertExists()
        compose.onNodeWithText("Listening", substring = true, ignoreCase = true).assertDoesNotExist()
    }
}
