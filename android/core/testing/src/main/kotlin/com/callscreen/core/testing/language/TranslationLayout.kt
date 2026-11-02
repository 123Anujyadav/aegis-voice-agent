package com.callscreen.core.testing.language

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
import androidx.compose.ui.test.junit4.ComposeContentTestRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.language.AppLanguage
import org.junit.Assert.assertTrue

/**
 * A screen in one of the release languages, with longer words and a large
 * font, on a small phone: does every action and message still fit?
 *
 * [tag]'s strings are [PseudoTranslation]'s stand-ins (English as it ships),
 * at twice the font scale, in a 320 x 480 dp window by default. Run with
 * Robolectric's native graphics: its default graphics give every glyph a fixed
 * size and ignore the font scale, so they cannot show whether text fits.
 */
public class TranslationLayout(
    private val compose: ComposeContentTestRule,
    public val tag: String,
    private val width: Dp = 320.dp,
    private val height: Dp = 480.dp,
    private val fontScale: Float = 2f,
) {

    /** Shows [content] in [tag], themed, at the font scale and in the window. */
    public fun show(content: @Composable () -> Unit) {
        compose.setContent {
            val density = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(density.density, fontScale = fontScale)) {
                InPseudoTranslation(tag) {
                    CallScreenTheme { Box(Modifier.size(width = width, height = height)) { content() } }
                }
            }
        }
    }

    /**
     * The text that starts with [english] can be brought fully into view:
     * composed (a lazy list is scrolled to it), scrolled to when it is inside
     * a scrolling container, then entirely inside the window and clipped by
     * no parent. Text taller than its scrolling viewport counts as reachable,
     * because scrolling shows all of it.
     */
    public fun reachable(english: String) {
        if (compose.onAllNodesWithText(english, substring = true).fetchSemanticsNodes().isEmpty()) {
            compose.onNode(scroller).performScrollToNode(hasText(english, substring = true))
        }
        val node = compose.onAllNodesWithText(english, substring = true)[0]
        if (node.fetchSemanticsNode().inScrollable()) node.performScrollTo()
        node.assertIsDisplayed()
        val shown = node.fetchSemanticsNode()
        val window = with(compose.density) { Rect(0f, 0f, width.toPx(), height.toPx()) }
        val bounds = shown.boundsInRoot
        val viewport = shown.scrollViewportHeight()
        val fitsWindow = bounds.top >= window.top - 1 && bounds.bottom <= window.bottom + 1
        val tallerThanItsViewport = viewport != null && shown.size.height > viewport
        assertTrue(
            "[$tag] \"$english\" is cut off: $bounds of ${shown.size}, window $window",
            bounds.width >= shown.size.width - 1 &&
                (tallerThanItsViewport || (bounds.height >= shown.size.height - 1 && fitsWindow)),
        )
    }

    /** The screen's vertical list, for text it has not composed yet. */
    private val scroller: SemanticsMatcher =
        hasScrollToNodeAction() and SemanticsMatcher.keyIsDefined(SemanticsProperties.VerticalScrollAxisRange)

    private fun SemanticsNode.inScrollable(): Boolean =
        generateSequence(parent) { it.parent }.any { SemanticsActions.ScrollBy in it.config }

    private fun SemanticsNode.scrollViewportHeight(): Int? =
        generateSequence(parent) { it.parent }.firstOrNull { SemanticsActions.ScrollBy in it.config }?.size?.height

    public companion object {
        /** The release languages' tags, English first: one layout run each. */
        public val TAGS: List<String> = AppLanguage.entries.map { it.tag }
    }
}
