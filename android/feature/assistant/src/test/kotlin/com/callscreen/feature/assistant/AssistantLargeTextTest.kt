package com.callscreen.feature.assistant

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsNode
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
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
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

/**
 * Longer words and larger text: translated copy runs up to 40% longer than
 * English (UX_FREEZE.md §9 item 2), and a subscriber may set a large font.
 * Each Assistant screen is rendered at twice the font scale in a 320 x 480 dp
 * window, and what it says, and its way back, must still be reachable.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
// Real text measurement: Robolectric's default graphics give every glyph a
// fixed size and ignore the font scale.
@GraphicsMode(GraphicsMode.Mode.NATIVE)
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

    /**
     * [text] can be brought fully into view: scrolled to when it is inside a scrolling
     * container, then entirely inside the 320 x 480 dp window and clipped by no parent.
     */
    private fun reachable(text: String, substring: Boolean = false) {
        if (compose.onAllNodesWithText(text, substring = substring).fetchSemanticsNodes().isEmpty()) {
            compose.onNode(scroller).performScrollToNode(hasText(text, substring = substring))
        }
        val node = compose.onNodeWithText(text, substring = substring)
        if (node.fetchSemanticsNode().inScrollable()) node.performScrollTo()
        node.assertIsDisplayed()
        val shown = node.fetchSemanticsNode()
        val window = with(compose.density) { Rect(0f, 0f, 320.dp.toPx(), 480.dp.toPx()) }
        val bounds = shown.boundsInRoot
        // Text taller than its scrolling list can never be seen whole; it is read by scrolling through it.
        val viewport = shown.scrollableParent()?.boundsInRoot
        if (viewport != null && shown.size.height > viewport.height) return
        assertTrue(
            "\"$text\" is cut off: $bounds of ${shown.size}, window $window",
            bounds.height >= shown.size.height - 1 && bounds.width >= shown.size.width - 1 &&
                bounds.top >= window.top - 1 && bounds.bottom <= window.bottom + 1,
        )
    }

    /** The screen's vertical list, for text it has not composed yet. */
    private val scroller: SemanticsMatcher =
        hasScrollToNodeAction() and SemanticsMatcher.keyIsDefined(SemanticsProperties.VerticalScrollAxisRange)

    private fun SemanticsNode.scrollableParent(): SemanticsNode? =
        generateSequence(parent) { it.parent }.firstOrNull { SemanticsActions.ScrollBy in it.config }

    private fun SemanticsNode.inScrollable(): Boolean = scrollableParent() != null

    @Test
    fun theHomeCardsStayReachable() {
        large { AssistantHomeScreen() }
        reachable(s(R.string.assistant_screening_unavailable))
        // The status beside long words was pushed out of the card.
        reachable(s(R.string.assistant_not_available).uppercase()) // the chip shows it in capitals
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
