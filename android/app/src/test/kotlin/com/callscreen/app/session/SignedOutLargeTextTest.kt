package com.callscreen.app.session

import android.app.Application
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsNode
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import com.callscreen.core.auth.SessionEndCause
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
 * The signed-out notice is rendered at twice the font scale in a 320 x 480 dp
 * window, with both of its lines: what it says, and Sign in, must be reachable.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34], application = Application::class)
// Real text measurement: Robolectric's default graphics give every glyph a
// fixed size and ignore the font scale.
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class SignedOutLargeTextTest {

    @get:Rule
    val compose = createComposeRule()

    private fun s(id: Int): String = RuntimeEnvironment.getApplication().getString(id)

    /**
     * [text] can be brought fully into view: scrolled to when it is inside a scrolling
     * container, then entirely inside the 320 x 480 dp window and clipped by no parent.
     */
    private fun reachable(text: String) {
        val node = compose.onNodeWithText(text)
        if (node.fetchSemanticsNode().inScrollable()) node.performScrollTo()
        node.assertIsDisplayed()
        val shown = node.fetchSemanticsNode()
        val window = with(compose.density) { Rect(0f, 0f, 320.dp.toPx(), 480.dp.toPx()) }
        val bounds = shown.boundsInRoot
        assertTrue(
            "\"$text\" is cut off: $bounds of ${shown.size}, window $window",
            bounds.height >= shown.size.height - 1 && bounds.width >= shown.size.width - 1 &&
                bounds.top >= window.top - 1 && bounds.bottom <= window.bottom + 1,
        )
    }

    private fun SemanticsNode.inScrollable(): Boolean =
        generateSequence(parent) { it.parent }.any { SemanticsActions.ScrollBy in it.config }

    @Test
    fun theNoticeAndSignInStayReachable() {
        compose.setContent {
            val density = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(density.density, fontScale = 2f)) {
                CallScreenTheme {
                    Box(Modifier.size(width = 320.dp, height = 480.dp)) {
                        SignedOutScreen(cause = SessionEndCause.STORED_SESSION_UNRECOVERABLE, blockingOn = true, onSignInClick = {})
                    }
                }
            }
        }
        reachable(s(SignedOutCopy.TITLE))
        reachable(s(SignedOutCopy.NOT_VERIFIED))
        reachable(s(SignedOutCopy.BLOCKING_STILL_ON))
        reachable(s(SignedOutCopy.SIGN_IN))
    }
}
