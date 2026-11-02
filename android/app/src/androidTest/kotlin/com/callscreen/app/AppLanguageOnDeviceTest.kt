package com.callscreen.app

import android.app.LocaleManager
import android.content.Context
import android.content.Intent
import android.content.res.Resources
import android.os.Build
import android.os.LocaleList
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assertIsSelected
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasScrollToNodeAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import androidx.test.espresso.Espresso
import androidx.test.ext.junit.runners.AndroidJUnit4
import com.callscreen.app.navigation.AegisDestinations
import com.callscreen.core.datastore.session.DataStoreBlobStore
import com.callscreen.core.ui.language.AppLanguage
import com.callscreen.core.ui.language.AppLanguageSetting
import dagger.hilt.android.testing.HiltAndroidRule
import dagger.hilt.android.testing.HiltAndroidTest
import java.io.File
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/**
 * Changing the app's language on a real Android runtime, through the screen
 * (A58l) a subscriber uses: Android 13 and later through Android's per-app
 * language, Android 8 to 12 through the app's own setting.
 *
 * - the activity is re-created in the chosen language, on the same screen,
 *   and Back still returns to Settings;
 * - the app's words fall back to English (no translation is in the app yet),
 *   while the platform's own strings show the language was applied;
 * - the choice is where the next process will read it, and a new activity
 *   starts in it;
 * - the stored session record is untouched.
 *
 * The language is reset before and after, so no other test runs in it.
 */
@HiltAndroidTest
@RunWith(AndroidJUnit4::class)
class AppLanguageOnDeviceTest {

    @get:Rule(order = 0)
    val hilt = HiltAndroidRule(this)

    @get:Rule(order = 1)
    val compose = createEmptyComposeRule()

    private val context: Context = ApplicationProvider.getApplicationContext()
    private val blobs = DataStoreBlobStore(context)
    private val sessionRecord = byteArrayOf(1, 2, 3, 4, 5, 6, 7, 8)

    private val radio = SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.RadioButton)

    @Before
    fun phonesLanguage() {
        hilt.inject()
        AppLanguageSetting.select(context, null)
        // An opaque record in the app's session file. This build has no identity server,
        // so the app never opens it; the test only checks a language change leaves it alone.
        runBlocking { blobs.write(sessionRecord) }
    }

    @After
    fun reset() {
        AppLanguageSetting.select(context, null)
        runBlocking { blobs.clear() }
    }

    private fun launch(start: String): ActivityScenario<MainActivity> =
        ActivityScenario.launch(
            Intent(context, MainActivity::class.java).putExtra(MainActivity.START_DESTINATION_EXTRA, start),
        )

    private fun language(scenario: ActivityScenario<MainActivity>): String {
        var tag = ""
        scenario.onActivity { tag = it.resources.configuration.locales[0].language }
        return tag
    }

    private fun awaitLanguage(scenario: ActivityScenario<MainActivity>, tag: String) {
        val deadline = System.currentTimeMillis() + 10_000
        while (language(scenario) != tag) {
            assertTrue("the activity is still in ${language(scenario)}, not $tag", System.currentTimeMillis() < deadline)
            Thread.sleep(100)
        }
        compose.waitForIdle()
    }

    private fun awaitNode(matcher: SemanticsMatcher) = compose.waitUntil(10_000) {
        compose.onAllNodes(matcher).fetchSemanticsNodes().isNotEmpty()
    }

    private fun string(scenario: ActivityScenario<MainActivity>, id: Int): String {
        var text = ""
        scenario.onActivity { text = it.getString(id) }
        return text
    }

    @Test
    fun aChosenLanguageReCreatesTheScreensInItAndKeepsThePlaceTheChoiceAndTheSession() {
        val scenario = launch(AegisDestinations.TASK_CENTER)
        val change = "Change app language"
        compose.onNode(hasScrollToNodeAction()).performScrollToNode(hasText(change))
        compose.onNode(hasText(change) and hasClickAction()).performClick()
        awaitNode(radio and hasText(AppLanguage.TAMIL.nativeName()))
        val englishCancel = string(scenario, android.R.string.cancel)

        compose.onNode(radio and hasText(AppLanguage.TAMIL.nativeName())).performClick()
        awaitLanguage(scenario, "ta")

        // The same screen, re-created in Tamil: the app's words are English (no translation
        // yet), and the platform's own Tamil strings show the language really applies.
        awaitNode(radio and hasText(AppLanguage.TAMIL.nativeName()))
        compose.onNode(radio and hasText(AppLanguage.TAMIL.nativeName())).assertIsSelected()
        compose.onNode(hasText("App language")).assertExists()
        assertNotEquals(englishCancel, string(scenario, android.R.string.cancel))

        // Where the next process reads it.
        assertEquals(AppLanguage.TAMIL, AppLanguageSetting.selected(context))
        if (Build.VERSION.SDK_INT >= 33) {
            assertEquals(LocaleList.forLanguageTags("ta"), context.getSystemService(LocaleManager::class.java).applicationLocales)
        } else {
            val file = File(context.applicationInfo.dataDir, "shared_prefs/callscreen_app_language.xml")
            assertTrue("the choice is not on disk", file.isFile && file.readText().contains(">ta<"))
        }

        // The back stack survived the re-creation: Back returns to Settings, which shows the choice.
        Espresso.pressBack()
        awaitNode(hasText(change))
        compose.onNode(hasText(AppLanguage.TAMIL.nativeName())).assertExists()
        compose.onNode(hasText("Not translated yet. Screens stay in English.")).assertExists()

        assertArrayEquals("the stored session record changed", sessionRecord, runBlocking { blobs.read() })
        scenario.close()

        // A new activity starts in the chosen language.
        val again = launch(AegisDestinations.APP_LANGUAGE)
        awaitLanguage(again, "ta")
        awaitNode(radio and hasText(AppLanguage.TAMIL.nativeName()))

        // And the phone's language takes it back.
        compose.onNode(radio and hasText("Phone's language")).performClick()
        awaitLanguage(again, Resources.getSystem().configuration.locales[0].language)
        assertEquals(null, AppLanguageSetting.selected(context))
        again.close()
    }

    @Test
    fun eachLanguageIsNamedInItsOwnScriptOnThisAndroid() {
        // The names come from the platform's locale data (ICU), which differs by Android version.
        val blocks = mapOf(
            AppLanguage.HINDI to Character.UnicodeBlock.DEVANAGARI,
            AppLanguage.TAMIL to Character.UnicodeBlock.TAMIL,
            AppLanguage.TELUGU to Character.UnicodeBlock.TELUGU,
            AppLanguage.MALAYALAM to Character.UnicodeBlock.MALAYALAM,
            AppLanguage.KANNADA to Character.UnicodeBlock.KANNADA,
        )
        for ((language, block) in blocks) {
            val letters = language.nativeName().filter { it.isLetter() }
            assertTrue("$language: \"${language.nativeName()}\"", letters.isNotEmpty() && letters.all { Character.UnicodeBlock.of(it) == block })
        }
    }
}
