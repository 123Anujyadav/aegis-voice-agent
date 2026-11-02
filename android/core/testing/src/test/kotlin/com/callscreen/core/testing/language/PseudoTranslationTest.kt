package com.callscreen.core.testing.language

import androidx.compose.material3.Text
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config

/** The stand-in is longer, in the language's script, under its configuration, and English stays as it ships. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class PseudoTranslationTest {

    @get:Rule
    val compose = createComposeRule()

    @Test
    fun theStandInIsHalfAsLongAgainAndStartsWithTheEnglish() {
        val long = "Numbers you block are rejected on this phone."
        val expanded = PseudoTranslation.expand(long, "தமிழ்")
        assertTrue(expanded, expanded.startsWith(long) && expanded.length >= long.length * 3 / 2)
        assertTrue("a short label grows by at least four", PseudoTranslation.expand("Done", "x").length >= 8)
        assertTrue(PseudoTranslation.expand("Done", "தமிழ்").contains("தமிழ்"))
    }

    @Test
    fun theFillerIsTheLanguagesOwnName() {
        assertEquals("English", PseudoTranslation.filler("en"))
        assertEquals(listOf("en", "hi", "ta", "te", "ml", "kn"), TranslationLayout.TAGS)
        for (tag in TranslationLayout.TAGS - "en") {
            val filler = PseudoTranslation.filler(tag)
            assertTrue("$tag: $filler", filler.isNotBlank() && filler.none { it in 'a'..'z' })
        }
    }

    @Test
    fun aScreenReadsTheStandInUnderTheLanguagesConfiguration() {
        var locale = ""
        compose.setContent {
            InPseudoTranslation("ml") {
                locale = LocalConfiguration.current.locales[0].language
                Text(stringResource(android.R.string.cancel))
            }
        }
        assertEquals("ml", locale)
        val filler = PseudoTranslation.filler("ml")
        val shown = compose.onNodeWithText(filler, substring = true).fetchSemanticsNode()
            .config[androidx.compose.ui.semantics.SemanticsProperties.Text].single().text
        // Android's own Malayalam "Cancel", lengthened with Malayalam.
        assertTrue(shown, shown.endsWith(filler) && !shown.startsWith("Cancel"))
    }

    @Test
    fun englishIsShownAsItShips() {
        compose.setContent { InPseudoTranslation("en") { Text(stringResource(android.R.string.cancel)) } }
        compose.onNodeWithText(RuntimeEnvironment.getApplication().getString(android.R.string.cancel)).assertExists()
    }
}
