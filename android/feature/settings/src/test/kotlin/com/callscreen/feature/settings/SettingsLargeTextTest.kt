package com.callscreen.feature.settings

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
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.telephony.screening.ScreeningRoleStatus
import com.callscreen.core.ui.language.AppLanguage
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
 * Each Settings screen is rendered at twice the font scale in a 320 x 480 dp
 * window, and its actions must still be reachable.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
// Real text measurement: Robolectric's default graphics give every glyph a
// fixed size and ignore the font scale.
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class SettingsLargeTextTest {

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
     *
     * Text taller than its list's viewport cannot be in view all at once (at twice the font
     * scale the Settings tab's bar, 02 §2.1, leaves the list 210 px for a 246 px paragraph).
     * For such text every line must still be readable: its first line and then its last line
     * are each scrolled fully into the viewport. (TranslationLayout counts such text as
     * reachable without this check.)
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
        val list = generateSequence(shown.parent) { it.parent }.firstOrNull { SemanticsActions.ScrollBy in it.config }
        if (list != null && shown.size.height > list.size.height) {
            assertTrue("\"$text\" is cut off sideways: $bounds of ${shown.size}", bounds.width >= shown.size.width - 1)
            val viewport = list.boundsInRoot
            val scroll = compose.onNode(scroller)
            // Its first line, then its last line, fully inside the list's viewport.
            scroll.performSemanticsAction(SemanticsActions.ScrollBy) { it(0f, node.fetchSemanticsNode().boundsInRoot.top - viewport.top) }
            assertTrue("\"$text\": its start cannot be scrolled into view", kotlin.math.abs(node.fetchSemanticsNode().boundsInRoot.top - viewport.top) <= 1)
            scroll.performSemanticsAction(SemanticsActions.ScrollBy) { it(0f, node.fetchSemanticsNode().boundsInRoot.bottom - viewport.bottom) }
            assertTrue("\"$text\": its end cannot be scrolled into view", kotlin.math.abs(node.fetchSemanticsNode().boundsInRoot.bottom - viewport.bottom) <= 1)
            assertTrue("\"$text\"'s list is outside the window", viewport.top >= window.top - 1 && viewport.bottom <= window.bottom + 1)
            return
        }
        assertTrue(
            "\"$text\" is cut off: $bounds of ${shown.size}, window $window",
            bounds.height >= shown.size.height - 1 && bounds.width >= shown.size.width - 1 &&
                bounds.top >= window.top - 1 && bounds.bottom <= window.bottom + 1,
        )
    }

    /** The screen's vertical list, for text it has not composed yet. */
    private val scroller: SemanticsMatcher =
        hasScrollToNodeAction() and SemanticsMatcher.keyIsDefined(SemanticsProperties.VerticalScrollAxisRange)

    private fun SemanticsNode.inScrollable(): Boolean =
        generateSequence(parent) { it.parent }.any { SemanticsActions.ScrollBy in it.config }

    private fun deletion(state: AccountDeletionState) = large {
        AccountDeletionScreen(state = state, onDeleteClick = {}, onConfirm = {}, onCancel = {}, onDone = {}, onBack = {})
    }

    @Test
    fun theChecklistAndTheDeleteEntryStayReachable() {
        large { TaskCenterScreen(tasks = SetupTasks.of(ScreeningRoleStatus.NOT_HELD, contactsAccess = false)) }
        reachable(s(R.string.settings_task_forwarding_unavailable))
        reachable(s(AccountDeletionCopy.TITLE))
    }

    @Test
    fun theLanguageEntryStaysReachable() {
        large { TaskCenterScreen(tasks = SetupTasks.of(ScreeningRoleStatus.NOT_HELD, contactsAccess = false), language = AppLanguage.MALAYALAM) }
        reachable(AppLanguage.MALAYALAM.nativeName())
        reachable(s(R.string.settings_language_change))
    }

    @Test
    fun everyLanguageCanBeFoundAndChosen() {
        large { AppLanguageScreen(selected = AppLanguage.TAMIL, onSelect = {}) }
        reachable(s(R.string.settings_language_intro))
        // "Phone's language (English)": found by its start, as the name in brackets follows the phone (D7).
        reachable(s(R.string.settings_language_phone).substringBefore(" ("), substring = true)
        for (language in AppLanguage.entries) reachable(language.nativeName())
        // The last row's English name, under its own.
        reachable("Kannada")
    }

    @Test
    fun theExplanationStillEndsInItsActions() {
        deletion(AccountDeletionState(step = DeletionStep.READY, message = DeletionMessage.SESSION_ENDED))
        reachable(s(AccountDeletionCopy.DELETE_BUTTON))
        reachable(s(AccountDeletionCopy.BACK))
    }

    @Test
    fun theConfirmationKeepsBothChoices() {
        deletion(AccountDeletionState(step = DeletionStep.CONFIRMING))
        compose.onNodeWithText(s(AccountDeletionCopy.CONFIRM)).assertIsDisplayed()
        compose.onNodeWithText(s(AccountDeletionCopy.CANCEL)).assertIsDisplayed()
    }

    @Test
    fun aPhoneThatIsNotSignedInCanGoBack() {
        deletion(AccountDeletionState(step = DeletionStep.NOT_SIGNED_IN, message = DeletionMessage.SESSION_ENDED))
        reachable(s(AccountDeletionCopy.BACK))
    }

    @Test
    fun theDeletedScreenCanBeDismissed() {
        deletion(AccountDeletionState(step = DeletionStep.DELETED))
        reachable(s(AccountDeletionCopy.DONE_BUTTON))
    }
}
