package com.callscreen.feature.assistant

import com.callscreen.core.testing.language.TranslationLayout
import androidx.compose.ui.test.junit4.createComposeRule
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.ParameterizedRobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

/**
 * The Assistant tab in each release language's configuration, with words half as long
 * again in that language's script (TranslationLayout, PseudoTranslation: a
 * stand-in, not a translation), at twice the font scale in 320 x 480 dp. Every
 * action and message must still be reachable. English runs as it ships.
 */
@RunWith(ParameterizedRobolectricTestRunner::class)
@Config(sdk = [34])
// Real text measurement, with each script's glyphs: Robolectric's default
// graphics give every glyph a fixed size and ignore the font scale.
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class AssistantTranslationLayoutTest(tag: String) {

    @get:Rule
    val compose = createComposeRule()

    private val layout = TranslationLayout(compose, tag)

    private fun s(id: Int): String = RuntimeEnvironment.getApplication().getString(id)

    private fun reachable(english: String) = layout.reachable(english)

    @Test
    fun theHomeCardsStayReachable() {
        layout.show { AssistantHomeScreen() }
        reachable(s(R.string.assistant_screening_unavailable))
        reachable(s(R.string.assistant_not_available).uppercase()) // the chip shows it in capitals
        reachable(s(R.string.assistant_session_card))
    }

    @Test
    fun everyPlannedVoiceAndTheDisabledSaveStayReachable() {
        layout.show { VoiceSetupScreen() }
        reachable(s(R.string.assistant_voice_setup_title)) // the top bar's title, wrapped whole
        reachable(s(R.string.assistant_voice_cartesia_accent))
        reachable(s(R.string.assistant_save_unavailable))
    }

    @Test
    fun theVoiceSessionStillSaysTheMicrophoneIsUnusedAndCanGoBack() {
        layout.show { VoiceConversationScreen() }
        reachable(s(R.string.assistant_session_screen_title))
        reachable(s(R.string.assistant_no_microphone))
        reachable(s(R.string.assistant_back))
    }

    companion object {
        @JvmStatic
        @ParameterizedRobolectricTestRunner.Parameters(name = "{0}")
        fun tags(): List<Array<Any>> = TranslationLayout.TAGS.map { arrayOf<Any>(it) }
    }
}
