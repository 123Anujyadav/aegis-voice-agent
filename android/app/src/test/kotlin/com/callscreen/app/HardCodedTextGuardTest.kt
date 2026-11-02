package com.callscreen.app

import java.io.File
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

/**
 * A module whose words have moved to string resources (res/values/strings.xml,
 * the English source of every translation) keeps them there: text written
 * into its Kotlin would ship untranslated in every language.
 *
 * Each migrated module is listed here as it is migrated. Screens that exist
 * only as debug-build design previews (AegisNavGraph.includeDesignPreviews)
 * are listed apart, with their reason: their copy is "intent and examples, not
 * final strings" (UX_FREEZE.md §9 item 1) until the feature is built for
 * release, and they move to resources then.
 */
@DisplayName("Hard-coded user-visible text")
class HardCodedTextGuardTest {

    private val migrated = listOf(
        "core/ui",
        "feature/assistant",
        "feature/calls",
        "feature/onboarding",
        "feature/protection",
        "feature/settings",
    )

    /** Debug-only design previews inside a migrated module, never in a release build. */
    private val designPreviews = setOf(
        "FraudIntelligenceScreen.kt",
        "ConsentVaultScreen.kt",
        "IncomingCallScreen.kt",
    )

    /**
     * Text a user would see, written as a literal: a capitalised phrase passed
     * to Text(), or as text, title, description, label or contentDescription,
     * or held in a copy constant.
     */
    private val userText = Regex(
        """(Text\(|\b(text|title|description|contentDescription|label|actionLabel|message) = )"[A-Z][a-z]""" +
            """|const val [A-Z_]+ = "[A-Z][a-z]""",
    )

    /**
     * The lines of [file] that can reach a user: not comments, and not in an
     * @Preview function (sample data for Android Studio's preview, never part
     * of the app). A top-level preview function ends at a "}" in column 0.
     */
    private fun shippedLines(file: File): List<IndexedValue<String>> {
        var inPreview = false
        return file.readLines().withIndex().filter { (_, line) ->
            val code = line.trimStart()
            if (code.startsWith("@Preview")) inPreview = true
            val shipped = !inPreview && !code.startsWith("*") && !code.startsWith("//")
            if (inPreview && line == "}") inPreview = false
            shipped
        }
    }

    @Test
    @DisplayName("a migrated module's sources write no user-visible text")
    fun migratedModulesUseResources() {
        // Unit tests run in the module directory: android/app.
        val android = File("..").canonicalFile
        val found = migrated.flatMap { module ->
            val sources = File(android, "$module/src/main").walkTopDown()
                .filter { it.isFile && it.extension == "kt" && it.name !in designPreviews }
                .toList()
            assertTrue(sources.isNotEmpty(), "no sources under $module")
            assertTrue(File(android, "$module/src/main/res/values/strings.xml").isFile, "$module has no strings.xml")
            sources.flatMap { file ->
                shippedLines(file)
                    .filter { (_, line) -> userText.containsMatchIn(line) }
                    .map { (i, line) -> "${file.relativeTo(android)}:${i + 1}: ${line.trim()}" }
            }
        }
        assertEquals(emptyList<String>(), found)
    }
}
