package com.callscreen.feature.settings

import com.callscreen.core.testing.language.TranslationLayout
import com.callscreen.core.telephony.screening.ScreeningRoleStatus
import com.callscreen.core.ui.language.AppLanguage
import androidx.compose.ui.test.junit4.createComposeRule
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.ParameterizedRobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

/**
 * Settings in each release language's configuration, with words half as long
 * again in that language's script (TranslationLayout, PseudoTranslation: a
 * stand-in, not a translation), at twice the font scale in 320 x 480 dp. Every
 * action and message must still be reachable. English runs as it ships.
 */
@RunWith(ParameterizedRobolectricTestRunner::class)
@Config(sdk = [34])
// Real text measurement, with each script's glyphs: Robolectric's default
// graphics give every glyph a fixed size and ignore the font scale.
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class SettingsTranslationLayoutTest(tag: String) {

    @get:Rule
    val compose = createComposeRule()

    private val layout = TranslationLayout(compose, tag)

    private fun s(id: Int): String = RuntimeEnvironment.getApplication().getString(id)

    private fun reachable(english: String) = layout.reachable(english)

    private fun deletion(state: AccountDeletionState) = layout.show {
        AccountDeletionScreen(state = state, onDeleteClick = {}, onConfirm = {}, onCancel = {}, onDone = {}, onBack = {})
    }

    @Test
    fun theChecklistTheLanguageAndTheDeleteEntryStayReachable() {
        layout.show { TaskCenterScreen(tasks = SetupTasks.of(ScreeningRoleStatus.NOT_HELD, contactsAccess = false), language = AppLanguage.TAMIL) }
        reachable(s(R.string.settings_task_blocking_to_do))
        reachable(s(R.string.settings_task_forwarding_unavailable))
        reachable(s(R.string.settings_language_not_translated))
        reachable(s(R.string.settings_language_change))
        reachable(s(AccountDeletionCopy.TITLE))
    }

    @Test
    fun theLanguageChoiceStaysReachable() {
        layout.show { AppLanguageScreen(selected = AppLanguage.KANNADA, onSelect = {}) }
        reachable(s(R.string.settings_language_intro))
        reachable(s(R.string.settings_language_phone_detail))
        for (language in AppLanguage.entries) reachable(language.nativeName())
    }

    @Test
    fun theDeletionExplanationEndsInItsActions() {
        deletion(AccountDeletionState(step = DeletionStep.READY, message = DeletionMessage.SESSION_ENDED))
        reachable(s(AccountDeletionCopy.DELETE_BUTTON))
        reachable(s(AccountDeletionCopy.BACK))
    }

    @Test
    fun theConfirmationKeepsBothChoices() {
        deletion(AccountDeletionState(step = DeletionStep.CONFIRMING))
        reachable(s(AccountDeletionCopy.CONFIRM))
        reachable(s(AccountDeletionCopy.CANCEL))
    }

    @Test
    fun theDeletedScreenCanBeDismissed() {
        deletion(AccountDeletionState(step = DeletionStep.DELETED))
        reachable(s(AccountDeletionCopy.DONE_BUTTON))
    }

    companion object {
        @JvmStatic
        @ParameterizedRobolectricTestRunner.Parameters(name = "{0}")
        fun tags(): List<Array<Any>> = TranslationLayout.TAGS.map { arrayOf<Any>(it) }
    }
}
