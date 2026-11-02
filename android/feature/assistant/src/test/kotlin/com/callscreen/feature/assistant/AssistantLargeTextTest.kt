package com.callscreen.feature.assistant

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.hasScrollToNodeAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config

/**
 * Longer words and larger text: translated copy runs up to 40% longer than
 * English (UX_FREEZE.md §9 item 2), and a subscriber may set a large font.
 * Each Assistant screen is rendered at twice the font scale in a 320 x 480 dp
 * window, and what it says, and its way back, must still be reachable.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class AssistantLargeTextTest {

    @get:Rule
    val compose = createComposeRule()

    private fun s(id: Int): String = RuntimeEnvironment.getApplication().getString(id)

    private fun large(content: @Composable () -> Unit) {
        compose.setContent {
            val density = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(density.density, fontScale = 2f)) {
                CallScreenTheme { Box(Modifier.size(width = 320.dp, height = 480.dp)) { content() } }
            }
        }
    }

    /** Scrolls to [text] (in a lazy list, it may not be composed yet) and requires it on screen. */
    private fun reachable(text: String, substring: Boolean = false) {
        if (compose.onAllNodesWithText(text, substring = substring).fetchSemanticsNodes().isEmpty()) {
            compose.onNode(hasScrollToNodeAction()).performScrollToNode(hasText(text, substring = substring))
        }
        compose.onNodeWithText(text, substring = substring).performScrollTo().assertIsDisplayed()
    }

    @Test
    fun theHomeCardsStayReachable() {
        large { AssistantHomeScreen() }
        reachable(s(R.string.assistant_screening_unavailable))
        reachable(s(R.string.assistant_session_card))
    }

    @Test
    fun everyPlannedVoiceAndTheDisabledSaveStayReachable() {
        large { VoiceSetupScreen() }
        reachable(s(R.string.assistant_voice_cartesia_accent))
        reachable(s(R.string.assistant_save_unavailable))
    }

    @Test
    fun theVoiceSessionStillSaysTheMicrophoneIsUnusedAndCanGoBack() {
        large { VoiceConversationScreen() }
        reachable(s(R.string.assistant_no_microphone), substring = true)
        reachable(s(R.string.assistant_back))
    }
}
