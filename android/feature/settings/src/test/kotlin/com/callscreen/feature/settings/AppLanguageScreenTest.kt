package com.callscreen.feature.settings

import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsNotSelected
import androidx.compose.ui.test.assertIsSelected
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasScrollToNodeAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.text.intl.LocaleList
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.telephony.screening.ScreeningRoleStatus
import com.callscreen.core.ui.language.AppLanguage
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config

/**
 * A58l: the phone's language and the six, each findable by its own name,
 * one chosen at a time, and honest about which are not translated yet.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class AppLanguageScreenTest {

    @get:Rule
    val compose = createComposeRule()

    private fun s(id: Int): String = RuntimeEnvironment.getApplication().getString(id)

    private val radio = SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.RadioButton)

    private var chosen: AppLanguage? = AppLanguage.ENGLISH
    private var choices = 0

    private fun show(selected: AppLanguage?) = compose.setContent {
        CallScreenTheme { AppLanguageScreen(selected = selected, onSelect = { chosen = it; choices++ }) }
    }

    @Test
    fun thePhonesLanguageAndTheSixAreOneChoice() {
        show(selected = null)
        compose.onAllNodes(radio).assertCountEquals(1 + AppLanguage.entries.size)
        compose.onNode(radio and hasText(s(R.string.settings_language_phone))).assertIsSelected()
        for (language in AppLanguage.entries) {
            compose.onNode(radio and hasText(language.nativeName())).assertIsNotSelected()
        }
    }

    @Test
    fun choosingALanguageReportsIt() {
        show(selected = null)
        compose.onNode(radio and hasText(AppLanguage.TAMIL.nativeName())).performClick()
        assertEquals(AppLanguage.TAMIL, chosen)
        assertEquals(1, choices)
    }

    @Test
    fun theChosenLanguageIsMarkedAndThePhonesLanguageCanBeChosenAgain() {
        show(selected = AppLanguage.KANNADA)
        compose.onNode(radio and hasText(AppLanguage.KANNADA.nativeName())).assertIsSelected()
        compose.onNode(radio and hasText(s(R.string.settings_language_phone))).assertIsNotSelected().performClick()
        assertNull(chosen)
    }

    @Test
    fun eachLanguageNotYetTranslatedSaysSo() {
        show(selected = null)
        val note = s(R.string.settings_language_not_translated)
        compose.onAllNodes(radio and hasText(note)).assertCountEquals(AppLanguage.entries.count { !it.translated })
        for (language in AppLanguage.entries) {
            val row = compose.onNode(radio and hasText(language.nativeName()))
            if (language.translated) row.assert(!hasText(note)) else row.assert(hasText(note))
        }
        // The screen says it is about the screens, not the assistant's language on calls.
        compose.onNodeWithText(s(R.string.settings_language_intro)).assertExists()
    }

    @Test
    fun eachNameIsMarkedAsItsLanguageForScreenReaders() {
        show(selected = null)
        for (language in AppLanguage.entries) {
            val text = compose.onNodeWithText(language.nativeName(), useUnmergedTree = true)
                .fetchSemanticsNode().config.getOrNull(SemanticsProperties.Text).orEmpty().single()
            assertEquals(language.name, listOf(LocaleList(language.tag)), text.spanStyles.map { it.item.localeList })
        }
    }

    @Test
    @Config(qualifiers = "hi")
    fun aLanguageAwaitingTranslationDoesNotMixItsNamesIntoEnglishScreens() {
        // Hindi chosen but not translated: the words are English, so the names under each are too.
        show(selected = AppLanguage.HINDI)
        compose.onNode(radio and hasText(AppLanguage.TAMIL.nativeName()) and hasText("Tamil")).assertExists()
        compose.onNodeWithText(s(R.string.settings_language_phone)).assertExists()
    }

    @Test
    fun settingsShowsTheLanguageAndOpensTheChoice() {
        var opened = 0
        compose.setContent {
            CallScreenTheme {
                TaskCenterScreen(
                    tasks = SetupTasks.of(ScreeningRoleStatus.NOT_HELD, false),
                    language = AppLanguage.HINDI,
                    onLanguageClick = { opened++ },
                )
            }
        }
        val change = s(R.string.settings_language_change)
        compose.onNode(hasScrollToNodeAction()).performScrollToNode(hasText(change))
        compose.onNodeWithText(AppLanguage.HINDI.nativeName()).assertExists()
        compose.onNodeWithText(s(R.string.settings_language_not_translated)).assertExists()
        compose.onNode(hasText(change) and hasClickAction()).performClick()
        assertEquals(1, opened)
    }

    @Test
    fun settingsShowsThePhonesLanguageWhenNothingIsChosen() {
        compose.setContent { CallScreenTheme { TaskCenterScreen(tasks = emptyList(), language = null) } }
        compose.onNode(hasScrollToNodeAction()).performScrollToNode(hasText(s(R.string.settings_language_change)))
        compose.onNodeWithText(s(R.string.settings_language_phone)).assertExists()
        compose.onNodeWithText(s(R.string.settings_language_not_translated)).assertDoesNotExist()
    }
}
