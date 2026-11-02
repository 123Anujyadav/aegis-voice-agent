package com.callscreen.app

import com.callscreen.BuildConfig
import com.callscreen.core.ui.language.AppLanguage
import java.io.File
import javax.xml.parsers.DocumentBuilderFactory
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test
import org.w3c.dom.Element

/**
 * A translation (res/values-xx/strings.xml in any module) can only replace
 * the English words, never change what the app does with them: every name
 * it translates exists in English and is translatable, its placeholders are
 * English's, its plurals keep "other", and nothing is left empty. A missing
 * translation falls back to English, so a partial translation is allowed.
 */
@DisplayName("Translations")
class TranslationConsistencyTest {

    private class Resources(
        val strings: Map<String, String>,
        val untranslatable: Set<String>,
        val plurals: Map<String, Map<String, String>>,
    )

    private fun read(file: File): Resources {
        val doc = DocumentBuilderFactory.newInstance().newDocumentBuilder().parse(file)
        val strings = doc.getElementsByTagName("string")
        val plurals = doc.getElementsByTagName("plurals")
        val elements = (0 until strings.length).map { strings.item(it) as Element }
        return Resources(
            strings = elements.associate { it.getAttribute("name") to it.textContent },
            untranslatable = elements.filter { it.getAttribute("translatable") == "false" }.map { it.getAttribute("name") }.toSet(),
            plurals = (0 until plurals.length).associate { i ->
                val p = plurals.item(i) as Element
                val items = p.getElementsByTagName("item")
                p.getAttribute("name") to (0 until items.length).associate { j ->
                    val item = items.item(j) as Element
                    item.getAttribute("quantity") to item.textContent
                }
            },
        )
    }

    /** %1$s, %2$d, …: what the code fills in. "%%" is a literal percent sign. */
    private fun placeholders(text: String): List<String> =
        Regex("""%(\d+\$)?[-#+ 0,(]*\d*(\.\d+)?[sdfxXoc]""").findAll(text.replace("%%", "")).map { it.value }.sorted().toList()

    /** Every module's resource folder: values (English) and its translations. */
    private fun resourceDirs(): List<File> {
        // Unit tests run in the module directory: android/app.
        val android = File("..").canonicalFile
        return android.walkTopDown()
            .onEnter { it.name !in setOf("build", ".gradle", "test", "androidTest", "build-logic") }
            .filter { it.isDirectory && it.name == "res" && it.parentFile?.name == "main" }
            .toList()
    }

    /** The problems with each translation in [res], against its English. */
    private fun problems(res: File): List<String> {
        val englishFile = File(res, "values/strings.xml")
        if (!englishFile.isFile) return emptyList()
        val english = read(englishFile)
        val translations = res.listFiles { f -> f.isDirectory && f.name.startsWith("values-") }.orEmpty()
            .map { File(it, "strings.xml") }
            .filter { it.isFile }
        return translations.flatMap { file ->
            val where = "${res.parentFile?.parentFile?.parentFile?.name}/${file.parentFile?.name}"
            val t = read(file)
            t.strings.flatMap { (name, text) ->
                listOfNotNull(
                    "$where: $name is not in English".takeIf { name !in english.strings },
                    "$where: $name is not translatable".takeIf { name in english.untranslatable },
                    "$where: $name is empty".takeIf { text.isBlank() },
                    "$where: $name placeholders ${placeholders(text)} are not English's ${placeholders(english.strings[name].orEmpty())}"
                        .takeIf { name in english.strings && placeholders(text) != placeholders(english.strings.getValue(name)) },
                )
            } + t.plurals.flatMap { (name, items) ->
                val englishItems = english.plurals[name]
                val englishPlaceholders = englishItems?.get("other")?.let(::placeholders)
                listOfNotNull(
                    "$where: plurals $name is not in English".takeIf { englishItems == null },
                    "$where: plurals $name has no \"other\"".takeIf { "other" !in items },
                ) + items.filter { (_, text) -> englishPlaceholders != null && placeholders(text) != englishPlaceholders }
                    .map { (q, text) -> "$where: plurals $name $q placeholders ${placeholders(text)} are not English's $englishPlaceholders" }
            }
        }
    }

    @Test
    @DisplayName("every translation keeps English's names, placeholders and plurals")
    fun translationsMatchEnglish() {
        val dirs = resourceDirs()
        assertTrue(dirs.any { File(it, "values/strings.xml").isFile }, "no English strings.xml found from ${File("..").canonicalPath}")
        assertEquals(emptyList<String>(), dirs.flatMap(::problems))
    }

    /** The language of a values-* folder ("values-hi" is "hi", "values-b+kn+Knda" is "kn"), or null for night, v27 and the like. */
    private fun language(folder: String): String? {
        val first = folder.removePrefix("values-").split('-').first()
        return when {
            first.startsWith("b+") -> first.removePrefix("b+").substringBefore('+')
            Regex("^[a-z]{2,3}$").matches(first) -> first
            else -> null
        }
    }

    @Test
    @DisplayName("every translation is in a language the release packages")
    fun translationsArePackaged() {
        // localeFilters strips every other language from the APK, so a translation outside
        // app/release-locales.txt would be dropped from the release without any error.
        val packaged = BuildConfig.PACKAGED_LOCALES.split(",").toSet()
        assertEquals(setOf("en", "hi", "ta", "te", "ml", "kn"), packaged)
        val unpackaged = resourceDirs().flatMap { res ->
            res.listFiles { f -> f.isDirectory && f.name.startsWith("values-") && File(f, "strings.xml").isFile }.orEmpty()
                .filter { language(it.name)?.let { code -> code !in packaged } ?: false }
                .map { "${res.parentFile?.parentFile?.parentFile?.name}/${it.name}" }
        }
        assertEquals(emptyList<String>(), unpackaged)
        assertEquals(listOf("hi", "kn", null, null), listOf("values-hi", "values-b+kn+Knda", "values-night", "values-v27").map(::language))
    }

    /** What [language]'s translation in [res] lacks of English: names of strings and plurals. */
    private fun missing(res: File, language: String): List<String> {
        val englishFile = File(res, "values/strings.xml")
        if (!englishFile.isFile) return emptyList()
        val english = read(englishFile)
        val file = File(res, "values-$language/strings.xml")
        val translated = if (file.isFile) read(file) else Resources(emptyMap(), emptySet(), emptyMap())
        val where = "${res.parentFile?.parentFile?.parentFile?.name}/values-$language"
        return (english.strings.keys - english.untranslatable - translated.strings.keys).map { "$where: $it" } +
            (english.plurals.keys - translated.plurals.keys).map { "$where: plurals $it" }
    }

    @Test
    @DisplayName("a language marked translated has every release string, so no screen mixes it with English")
    fun translatedLanguagesAreComplete() {
        // AppLanguage.translated is what the language picker tells people. A language
        // still awaiting its translation may be partial: the picker says so.
        val dirs = resourceDirs()
        val incomplete = AppLanguage.entries.filter { it.translated && it != AppLanguage.ENGLISH }
            .flatMap { language -> dirs.flatMap { missing(it, language.tag) } }
        assertEquals(emptyList<String>(), incomplete)
    }

    @Test
    @DisplayName("every module with words has a file ready for each language to translate into")
    fun everyLanguageHasItsFiles() {
        // A translator fills these (tools/i18n/translation_kit.py); a module added later gets them too.
        val languages = BuildConfig.PACKAGED_LOCALES.split(",") - "en"
        val missing = resourceDirs().filter { File(it, "values/strings.xml").isFile }.flatMap { res ->
            languages.filterNot { File(res, "values-$it/strings.xml").isFile }
                .map { "${res.parentFile?.parentFile?.parentFile?.name}/values-$it" }
        }
        assertEquals(emptyList<String>(), missing)
    }

    @Test
    @DisplayName("the check catches a translation that changes a placeholder, a name or a plural")
    fun theCheckWorks(@org.junit.jupiter.api.io.TempDir tmp: File) {
        File(tmp, "x/src/main/res/values").apply { mkdirs() }.resolve("strings.xml").writeText(
            """
            <resources>
                <string name="greeting">Remove %1${'$'}s</string>
                <string name="brand" translatable="false">Aegis</string>
                <plurals name="seconds"><item quantity="one">%1${'$'}d second</item><item quantity="other">%1${'$'}d seconds</item></plurals>
            </resources>
            """.trimIndent(),
        )
        File(tmp, "x/src/main/res/values-hi").apply { mkdirs() }.resolve("strings.xml").writeText(
            """
            <resources>
                <string name="greeting">हटाएँ %1${'$'}d</string>
                <string name="brand">एजिस</string>
                <string name="unknown">?</string>
                <plurals name="seconds"><item quantity="one">%1${'$'}d सेकंड</item></plurals>
            </resources>
            """.trimIndent(),
        )
        val found = problems(File(tmp, "x/src/main/res"))
        assertEquals(4, found.size, found.joinToString("\n"))
    }
}
