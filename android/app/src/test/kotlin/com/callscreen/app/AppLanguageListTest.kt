package com.callscreen.app

import com.callscreen.BuildConfig
import com.callscreen.core.ui.language.AppLanguage
import java.io.File
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

/**
 * The languages the picker offers are the languages the release packages,
 * and Android's own App languages page lists only translated ones.
 */
@DisplayName("App languages")
class AppLanguageListTest {

    @Test
    @DisplayName("the picker offers exactly what app/release-locales.txt packages")
    fun theListIsThePackagedLanguages() {
        assertEquals(BuildConfig.PACKAGED_LOCALES.split(","), AppLanguage.entries.map { it.tag })
    }

    @Test
    @DisplayName("Android's App languages page lists a language only once it is translated")
    fun androidsListHasOnlyTranslatedLanguages() {
        // Unit tests run in the module directory: android/app.
        val manifest = File("src/main/AndroidManifest.xml").readText()
        val config = File("src/main/res/xml/locales_config.xml")
        val translated = AppLanguage.entries.filter { it.translated }.map { it.tag }
        if (translated.size < 2) {
            // Only English: the system page would offer one choice, or list languages whose
            // screens are English. The in-app picker (A58l) says which are not translated yet.
            assertFalse("android:localeConfig" in manifest, "localeConfig declared while only English is translated")
            assertFalse(config.exists(), "$config exists while only English is translated")
        } else {
            assertTrue("android:localeConfig=\"@xml/locales_config\"" in manifest, "the manifest does not declare res/xml/locales_config.xml")
            val listed = Regex("""<locale\s+android:name="([^"]+)"""").findAll(config.readText()).map { it.groupValues[1] }.toList()
            assertEquals(translated, listed)
        }
    }
}
