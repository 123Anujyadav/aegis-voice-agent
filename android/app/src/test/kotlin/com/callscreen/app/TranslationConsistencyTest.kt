package com.callscreen.app

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
