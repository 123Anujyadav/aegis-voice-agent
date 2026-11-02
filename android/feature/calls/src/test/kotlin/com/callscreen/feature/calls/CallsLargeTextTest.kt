package com.callscreen.feature.calls

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
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
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config

/**
 * Longer words and larger text: translated copy runs up to 40% longer than
 * English (UX_FREEZE.md §9 item 2), and a subscriber may set a large font.
 * The Calls feed is rendered at twice the font scale in a 320 x 480 dp
 * window, and what it says must still be reachable.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class CallsLargeTextTest {

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

    /** The feed's vertical list: the filter chips scroll too, sideways. */
    private val feed = hasScrollToNodeAction() and SemanticsMatcher.keyIsDefined(SemanticsProperties.VerticalScrollAxisRange)

    /** Scrolls to [text] (in a lazy list, it may not be composed yet) and requires it on screen. */
    private fun reachable(text: String) {
        if (compose.onAllNodesWithText(text).fetchSemanticsNodes().isEmpty()) {
            compose.onNode(feed).performScrollToNode(hasText(text))
        }
        compose.onNodeWithText(text).performScrollTo().assertIsDisplayed()
    }

    @Test
    fun theEmptyFeedStillSaysWhy() {
        large { CallsFeedScreen() }
        reachable(s(R.string.calls_empty_title))
        reachable(s(R.string.calls_empty))
    }

    @Test
    fun withTheRoleLostTheBannerActionAndTheEmptyStateStayReachable() {
        large { CallsFeedScreen(roleLost = true) }
        reachable(s(R.string.calls_role_switch_back))
        reachable(s(R.string.calls_empty))
    }
}
