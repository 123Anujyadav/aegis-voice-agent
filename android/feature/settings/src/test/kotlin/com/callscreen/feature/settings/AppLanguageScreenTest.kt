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
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.text.intl.LocaleList
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.telephony.screening.ScreeningRoleStatus
import com.callscreen.core.ui.language.AppLanguage
import java.util.Locale
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
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

    /** The phone's own language in these tests: English, as on the CI emulators. */
    private val englishPhone: Locale = Locale.forLanguageTag("en-IN")

    /** "Phone's language (English)" for [phone]. */
    private fun phoneRow(phone: Locale = englishPhone): String =
        RuntimeEnvironment.getApplication().getString(R.string.settings_language_phone, AppLanguage.phoneLanguageName(phone, Locale.ENGLISH))

    private fun show(selected: AppLanguage?, phone: Locale = englishPhone) = compose.setContent {
        CallScreenTheme { AppLanguageScreen(selected = selected, onSelect = { chosen = it; choices++ }, phoneLocale = phone) }
    }

    @Test
    fun thePhonesLanguageAndTheSixAreOneChoice() {
        show(selected = null)
        compose.onAllNodes(radio).assertCountEquals(1 + AppLanguage.entries.size)
        compose.onNode(radio and hasText(phoneRow())).assertIsSelected()
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
        compose.onNode(radio and hasText(phoneRow())).assertIsNotSelected().performClick()
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
        compose.onNodeWithText(phoneRow()).assertExists()
    }

    @Test
    fun thePhonesLanguageIsNamedAfterThePhone() {
        // Owner decision D7: "Phone's language (English)" on an English phone, following the phone.
        show(selected = null)
        compose.onNode(radio and hasText("Phone's language (English)")).assertIsSelected()
    }

    @Test
    fun aTamilPhoneIsNamedTamilWhileItsScreensAreStillInEnglish() {
        show(selected = AppLanguage.KANNADA, phone = Locale.forLanguageTag("ta-IN"))
        // The phone's language, whatever the app has chosen, named in the screens' language.
        compose.onNode(radio and hasText("Phone's language (Tamil)")).assertIsNotSelected()
        // And the row still says what a choice of it shows.
        compose.onNodeWithText(s(R.string.settings_language_phone_detail)).assertExists()
    }

    @Test
    fun aPhoneLanguageOutsideTheSixIsStillNamed() {
        show(selected = null, phone = Locale.forLanguageTag("mr-IN"))
        compose.onNode(radio and hasText("Phone's language (Marathi)")).assertIsSelected()
    }

    @Test
    fun theNoteSaysTheScreensStayInEnglishUntilAReviewedTranslation() {
        // Owner decision D3.
        assertEquals("Not translated yet. Screens stay in English until a reviewed translation is available.", s(R.string.settings_language_not_translated))
    }

    @Test
    fun everyLanguageCanBeChosenByEveryoneWithNothingToUnlock() {
        // Owner decision D8: the screen language is free. Every row is enabled, none asks for
        // anything first, and each choice is reported straight away. (The app has no premium
        // entitlement at all today; this keeps the language rows from ever depending on one.)
        show(selected = null)
        val rows = compose.onAllNodes(radio).fetchSemanticsNodes()
        assertEquals(1 + AppLanguage.entries.size, rows.size)
        for (row in rows) assertTrue(row.config.getOrNull(SemanticsProperties.Disabled) == null)
        for ((index, language) in AppLanguage.entries.withIndex()) {
            compose.onNode(radio and hasText(language.nativeName())).performScrollTo().performClick()
            assertEquals(language, chosen)
            assertEquals(index + 1, choices)
        }
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
        compose.setContent { CallScreenTheme { TaskCenterScreen(tasks = emptyList(), language = null, phoneLocale = Locale.forLanguageTag("te-IN")) } }
        compose.onNode(hasScrollToNodeAction()).performScrollToNode(hasText(s(R.string.settings_language_change)))
        // D7: the phone's actual language, Telugu here.
        compose.onNodeWithText(phoneRow(Locale.forLanguageTag("te-IN"))).assertExists()
        compose.onNodeWithText("Phone's language (Telugu)").assertExists()
        compose.onNodeWithText(s(R.string.settings_language_not_translated)).assertDoesNotExist()
    }
}
