package com.callscreen.feature.assistant

import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.isToggleable
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import com.callscreen.core.designsystem.theme.CallScreenTheme
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config

/** The Assistant tab claims nothing it cannot do: no screening switch, no saved voice, no listening. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class HonestAssistantUiTest {

    @get:Rule
    val compose = createComposeRule()

    /** The shipped English resource: res/values/strings.xml. */
    private fun s(id: Int): String = RuntimeEnvironment.getApplication().getString(id)

    @Test
    fun screeningIsNotAvailableAndHasNoSwitch() {
        compose.setContent { CallScreenTheme { AssistantHomeScreen() } }
        compose.onNodeWithText(s(AssistantCopy.SCREENING_UNAVAILABLE)).assertExists()
        // A toggle that saves nothing and changes nothing would mislead.
        compose.onNode(isToggleable()).assertDoesNotExist()
        compose.onNodeWithText("on your behalf", substring = true).assertDoesNotExist()
    }

    @Test
    fun aVoiceCannotBeSavedAndNoneIsShownAsActive() {
        compose.setContent { CallScreenTheme { VoiceSetupScreen() } }
        compose.onNodeWithText(s(AssistantCopy.VOICES_INTRO)).assertExists()
        compose.onNodeWithText(s(AssistantCopy.SAVE_UNAVAILABLE)).assertIsNotEnabled()
        compose.onNodeWithText("Active", ignoreCase = true).assertDoesNotExist()
    }

    @Test
    fun theVoiceSessionSaysTheMicrophoneIsNotUsed() {
        compose.setContent { CallScreenTheme { VoiceConversationScreen() } }
        compose.onNodeWithText(s(AssistantCopy.NO_MICROPHONE), substring = true).assertExists()
        compose.onNodeWithText("Listening", substring = true, ignoreCase = true).assertDoesNotExist()
    }

    @Test
    fun eachPlannedVoiceIsShownWithItsProductNameAndOnlyOneIsHighlighted() {
        compose.setContent { CallScreenTheme { VoiceSetupScreen() } }
        for (voice in PlannedVoice.entries) {
            compose.onNodeWithText(s(voice.product), substring = true).assertExists()
            compose.onNodeWithText(s(voice.accent)).assertExists()
        }
        // Product names are not translated: they name a vendor's voice.
        assertEquals("ElevenLabs Flash - Calm Professional", RuntimeEnvironment.getApplication().getString(
            R.string.assistant_voice_option, s(PlannedVoice.ELEVENLABS.product), s(PlannedVoice.ELEVENLABS.style),
        ))
    }

    @Test
    fun theCardIconsAreNotReadAsASecondTitle() {
        compose.setContent { CallScreenTheme { AssistantHomeScreen() } }
        compose.onNodeWithContentDescription(s(R.string.assistant_voice_title)).assertDoesNotExist()
        compose.onNodeWithText(s(R.string.assistant_voice_title)).assertExists()
    }
}
