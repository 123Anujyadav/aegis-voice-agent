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
    fun anEnglishReaderSeesEnglishNames() {
        assertEquals(
            listOf("English", "Hindi", "Tamil", "Telugu", "Malayalam", "Kannada"),
            AppLanguage.entries.map { it.displayName(Locale.ENGLISH) },
        )
    }
}
