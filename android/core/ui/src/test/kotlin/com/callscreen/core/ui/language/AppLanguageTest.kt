package com.callscreen.core.ui.language

import java.util.Locale
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertNull
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.Test

/** The six languages, how a locale maps to one, and the names a reader looks for. */
class AppLanguageTest {

    @Test
    fun theOwnersSixInTheOwnersOrderEnglishFirst() {
        assertEquals(listOf("en", "hi", "ta", "te", "ml", "kn"), AppLanguage.entries.map { it.tag })
    }

    @Test
    fun aChoiceAppliesTheLanguageInIndiaAndPlainEnglish() {
        assertEquals(listOf("en", "hi-IN", "ta-IN", "te-IN", "ml-IN", "kn-IN"), AppLanguage.entries.map { it.locale.toLanguageTag() })
        for (language in AppLanguage.entries) assertEquals(language, AppLanguage.of(language.locale))
    }

    @Test
    fun onlyEnglishHasItsScreensInItUntilAReviewedTranslationArrives() {
        // A language is marked translated in the same change that adds its reviewed
        // translation; TranslationConsistencyTest (app) then requires every string.
        assertEquals(listOf(AppLanguage.ENGLISH), AppLanguage.entries.filter { it.translated })
    }

    @Test
    fun aLocaleMatchesOnItsLanguageAndAnythingElseIsThePhonesLanguage() {
        assertEquals(AppLanguage.HINDI, AppLanguage.of(Locale.forLanguageTag("hi-IN")))
        assertEquals(AppLanguage.KANNADA, AppLanguage.ofTag("kn"))
        assertEquals(AppLanguage.MALAYALAM, AppLanguage.ofTag("ml-Mlym-IN"))
        assertEquals(AppLanguage.ENGLISH, AppLanguage.ofTag("en-GB"))
        for (other in listOf(null, "", " ", "bn", "mr", "fr", "und", "zz-ZZ", "not a tag!")) {
            assertNull(AppLanguage.ofTag(other), "$other")
        }
        assertNull(AppLanguage.of(null))
        assertNull(AppLanguage.of(Locale.ROOT))
    }

    @Test
    fun eachLanguageIsNamedInItsOwnScript() {
        val scripts = mapOf(
            AppLanguage.HINDI to Character.UnicodeBlock.DEVANAGARI,
            AppLanguage.TAMIL to Character.UnicodeBlock.TAMIL,
            AppLanguage.TELUGU to Character.UnicodeBlock.TELUGU,
            AppLanguage.MALAYALAM to Character.UnicodeBlock.MALAYALAM,
            AppLanguage.KANNADA to Character.UnicodeBlock.KANNADA,
        )
        assertEquals("English", AppLanguage.ENGLISH.nativeName())
        for ((language, block) in scripts) {
            val name = language.nativeName()
            val letters = name.filter { it.isLetter() }
            assertTrue(letters.isNotEmpty() && letters.all { Character.UnicodeBlock.of(it) == block }, "$language: \"$name\" is not in $block")
        }
    }

    @Test
    fun thePhonesLanguageIsNamedAsThePhoneHasItInTheScreensLanguage() {
        // Owner decision D7: "Phone's language (English)", the name following the phone, never fixed.
        assertEquals("English", AppLanguage.phoneLanguageName(Locale.forLanguageTag("en-IN"), Locale.ENGLISH))
        assertEquals("Tamil", AppLanguage.phoneLanguageName(Locale.forLanguageTag("ta-IN"), Locale.ENGLISH))
        assertEquals("Kannada", AppLanguage.phoneLanguageName(Locale.forLanguageTag("kn"), Locale.ENGLISH))
        // A phone language that is not one of the six is still named, not replaced by English.
        assertEquals("Marathi", AppLanguage.phoneLanguageName(Locale.forLanguageTag("mr-IN"), Locale.ENGLISH))
        assertEquals("French", AppLanguage.phoneLanguageName(Locale.FRANCE, Locale.ENGLISH))
        // Once the screens are in Hindi, the name is too.
        assertEquals(AppLanguage.TAMIL.displayName(AppLanguage.HINDI.locale), AppLanguage.phoneLanguageName(Locale.forLanguageTag("ta-IN"), AppLanguage.HINDI.locale))
    }

    @Test
    fun theScreensAreInAConfiguredLanguageOnlyOnceItIsTranslated() {
        assertEquals(Locale.ENGLISH, AppLanguage.screensLocale(Locale.forLanguageTag("en-IN")))
        // Tamil chosen but not translated: the screens' words, and so the names among them, are English.
        assertEquals(Locale.ENGLISH, AppLanguage.screensLocale(AppLanguage.TAMIL.locale))
        assertEquals(Locale.ENGLISH, AppLanguage.screensLocale(Locale.FRANCE))
        assertEquals(Locale.ENGLISH, AppLanguage.screensLocale(null))
    }

    @Test
    fun anEnglishReaderSeesEnglishNames() {
        assertEquals(
            listOf("English", "Hindi", "Tamil", "Telugu", "Malayalam", "Kannada"),
            AppLanguage.entries.map { it.displayName(Locale.ENGLISH) },
        )
    }
}
