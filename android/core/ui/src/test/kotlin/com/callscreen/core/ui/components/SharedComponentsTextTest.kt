package com.callscreen.core.ui.components

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsSelected
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.R
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

/**
 * The shared components' own words come from string resources
 * (res/values/strings.xml), are read once by screen readers, and still fit
 * with a large font: translated copy runs up to 40% longer than English
 * (UX_FREEZE.md §9 item 2).
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
// Real text measurement: Robolectric's default graphics give every glyph a
// fixed size and ignore the font scale.
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class SharedComponentsTextTest {

    @get:Rule
    val compose = createComposeRule()

    private fun s(id: Int): String = RuntimeEnvironment.getApplication().getString(id)

    private val tabs = listOf(
        AegisTab.Calls to R.string.core_ui_tab_calls,
        AegisTab.Protection to R.string.core_ui_tab_protection,
        AegisTab.Assistant to R.string.core_ui_tab_assistant,
        AegisTab.Settings to R.string.core_ui_tab_settings,
    )

    private fun show(fontScale: Float = 1f, width: Int = 360, content: @Composable () -> Unit) {
        compose.setContent {
            val density = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(density.density, fontScale = fontScale)) {
                CallScreenTheme { Box(Modifier.width(width.dp).testTag(CONTENT)) { content() } }
            }
        }
    }

    /** Is the text showing [text] clipped by a parent, or outside a [width] dp screen? */
    private fun cutOff(text: String, width: Int): Boolean {
        val node = compose.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode()
        val bounds = node.boundsInRoot
        val right = with(compose.density) { width.dp.toPx() }
        return bounds.height < node.size.height - 1 || bounds.width < node.size.width - 1 || bounds.right > right + 1
    }

    @Test
    fun eachTabIsATabNamedOnceAndTheCurrentOneIsSelected() {
        var selected: AegisTab? = null
        show { AegisBottomBar(currentTab = AegisTab.Protection, onTabSelected = { selected = it }) }
        for ((tab, label) in tabs) {
            val node = compose.onNode(hasText(s(label)) and hasClickAction())
            node.assert(SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.Tab))
            // The icon is decorative: the label is read once, not "Calls, Calls".
            compose.onNodeWithContentDescription(s(label), useUnmergedTree = true).assertDoesNotExist()
            node.performClick()
            assertEquals(tab, selected)
        }
        compose.onNode(hasText(s(R.string.core_ui_tab_protection)) and hasClickAction()).assertIsSelected()
    }

    @Test
    fun theTabLabelsFitAtTwiceTheFontScaleOnASmallScreen() {
        show(fontScale = 2f, width = 320) { AegisBottomBar(currentTab = AegisTab.Calls, onTabSelected = {}) }
        for ((_, label) in tabs) {
            compose.onNodeWithText(s(label), useUnmergedTree = true).assertIsDisplayed()
            assertFalse("${s(label)} is cut off", cutOff(s(label), width = 320))
        }
    }

    @Test
    fun theTopBarNamesOnlyWhatIsThere() {
        show { AegisTopAppBar(title = "Calls Feed") }
        compose.onNodeWithText("Calls Feed").assertIsDisplayed()
        compose.onNodeWithContentDescription(s(R.string.core_ui_logo)).assertExists()
        // The profile picture does nothing, so it is not announced as if it were a control.
        compose.onNodeWithContentDescription("User Profile").assertDoesNotExist()
    }

    /**
     * The status bar ([top] dp) and, in landscape, a navigation bar or cutout at either side ([left]
     * and [right] dp) lie over the bar (design review U1). Its title and logo start inside them, wrapped rather than cut,
     * and its colour still reaches the top edge.
     */
    private fun assertTopBarInside(width: Int, top: Int, left: Int = 0, right: Int = 0) {
        // About 40% longer than "Blocked numbers", as a translation can be, at twice the font scale.
        val title = "Numbers you blocked from calling"
        show(fontScale = 2f, width = width) {
            AegisTopAppBar(title = title, windowInsets = WindowInsets(left = left.dp, top = top.dp, right = right.dp))
        }
        with(compose.density) {
            // The screen really is that wide: a box wider than Robolectric's window is cut to it.
            assertEquals(width.dp.toPx(), compose.onNodeWithTag(CONTENT).fetchSemanticsNode().boundsInRoot.width, 1f)
            val safeTop = top.dp.toPx()
            val safeRight = (width - right).dp.toPx()
            for (bounds in listOf(
                compose.onNodeWithText(title, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot,
                compose.onNodeWithContentDescription(s(R.string.core_ui_logo)).fetchSemanticsNode().boundsInRoot,
            )) {
                assertTrue("$bounds starts above the $top dp status bar", bounds.top >= safeTop)
                assertTrue("$bounds reaches under the $left dp side bar", bounds.left >= left.dp.toPx())
                assertTrue("$bounds reaches under the $right dp side bar of a $width dp screen", bounds.right <= safeRight)
            }
            // The bar is the whole content here: it starts at the top edge, behind the status bar.
            val bar = compose.onNodeWithTag(CONTENT).fetchSemanticsNode().boundsInRoot
            assertTrue("the bar is ${bar.height} px high", bar.top == 0f && bar.height >= (top + 64).dp.toPx())
        }
        assertFalse("$title is cut off", cutOff(title, width = width - right))
    }

    @Test
    fun theTopBarStartsBelowTheStatusBarOnASmallScreenInPortrait() = assertTopBarInside(width = 320, top = 24)

    @Test
    @Config(qualifiers = "w640dp-h320dp-land")
    fun theTopBarStaysClearOfTheStatusAndNavigationBarsInLandscape() = assertTopBarInside(width = 640, top = 24, left = 32, right = 48)

    /** The tab labels stay above a [bottom] dp navigation bar, and clear of a [right] dp side bar. */
    private fun assertTabsInside(width: Int, bottom: Int, right: Int) {
        show(fontScale = 2f, width = width) {
            AegisBottomBar(currentTab = AegisTab.Calls, onTabSelected = {}, windowInsets = WindowInsets(right = right.dp, bottom = bottom.dp))
        }
        with(compose.density) {
            val bar = compose.onNodeWithTag(CONTENT).fetchSemanticsNode().boundsInRoot
            // The screen really is that wide: a box wider than Robolectric's window is cut to it.
            assertEquals(width.dp.toPx(), bar.width, 1f)
            for ((_, label) in tabs) {
                val bounds = compose.onNodeWithText(s(label), useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
                assertTrue("${s(label)} at $bounds reaches under the $bottom dp navigation bar", bounds.bottom <= bar.bottom - bottom.dp.toPx())
                assertTrue("${s(label)} at $bounds reaches under the $right dp side bar", bounds.right <= (width - right).dp.toPx())
                assertFalse("${s(label)} is cut off", cutOff(s(label), width = width - right))
            }
            // The tabs share the width the side bar leaves: the first and last sit as far from its
            // edges. A label is narrower than its tab, so this, not the label's edge, shows the bar
            // reaching under a side bar.
            val first = compose.onNodeWithText(s(tabs.first().second), useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
            val last = compose.onNodeWithText(s(tabs.last().second), useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
            val safeRight = (width - right).dp.toPx()
            assertEquals("tabs not centred in the $width dp bar less its $right dp side bar", first.center.x, safeRight - last.center.x, 1.dp.toPx())
            // The bar's own room is still there above the navigation bar.
            assertTrue("the bar is ${bar.height} px high", bar.height >= (80 + bottom).dp.toPx())
        }
    }

    @Test
    fun theTabsStayAboveTheNavigationBarOnASmallScreen() = assertTabsInside(width = 320, bottom = 48, right = 0)

    @Test
    @Config(qualifiers = "w640dp-h320dp-land")
    fun theTabsStayClearOfASideNavigationBarInLandscape() = assertTabsInside(width = 640, bottom = 0, right = 48)

    @Test
    fun aFullScreenColumnKeepsItsContentInsideTheSystemBars() {
        compose.setContent {
            CallScreenTheme {
                // Smaller than Robolectric's window, so this box stands for the screen.
                Box(Modifier.size(width = 320.dp, height = 360.dp).testTag(CONTENT)) {
                    AegisScreenColumn(windowInsets = WindowInsets(top = 24.dp, bottom = 48.dp)) {
                        Text("First")
                        Text("Last")
                    }
                }
            }
        }
        with(compose.density) {
            val first = compose.onNodeWithText("First").fetchSemanticsNode().boundsInRoot
            val last = compose.onNodeWithText("Last").fetchSemanticsNode().boundsInRoot
            // The status bar, then the column's own 24 dp; the same above the navigation bar.
            assertTrue("the first line starts at $first", first.top >= (24 + 24).dp.toPx() - 1)
            assertTrue("the last line ends at $last", last.bottom <= (360 - 48 - 24).dp.toPx() + 1)
        }
    }

    @Test
    fun theDialogsDefaultChoicesAreTheShippedWords() {
        show { AegisDialog(title = "Title", message = "Message", onConfirm = {}, onDismiss = {}) }
        compose.onNodeWithText(s(R.string.core_ui_confirm)).assertExists()
        compose.onNodeWithText(s(R.string.core_ui_cancel)).assertExists()
    }

    @Test
    fun theSearchBarSaysSearchUnlessTheScreenSaysMore() {
        show { AegisSearchBar(query = "", onQueryChange = {}) }
        compose.onNodeWithText(s(R.string.core_ui_search)).assertExists()
    }

    @Test
    fun aCallCardSaysWhichWayTheCallWent() {
        show {
            AegisCallCard(
                callerName = "Test caller",
                phoneNumber = "+91 00000 00000",
                timestamp = "10:00",
                callType = CallType.Incoming,
                statusText = "Answered",
                onClick = {},
            )
        }
        compose.onNodeWithContentDescription(s(R.string.core_ui_call_incoming), useUnmergedTree = true).assertExists()
    }

    private companion object {
        /** The box [show] puts the component in, as wide as the screen and as tall as the component. */
        const val CONTENT = "content"
    }
}
