package com.callscreen.core.ui.components

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
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
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.ui.R
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
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
                CallScreenTheme { Box(Modifier.width(width.dp)) { content() } }
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
}
