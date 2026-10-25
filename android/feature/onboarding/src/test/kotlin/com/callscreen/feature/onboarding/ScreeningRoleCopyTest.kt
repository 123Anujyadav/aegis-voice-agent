package com.callscreen.feature.onboarding

import java.io.File
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

@DisplayName("A09b screening role rationale copy")
class ScreeningRoleCopyTest {

    /** Unit tests run in the module directory (android/feature/onboarding). */
    private val spec = File("../../../docs/ux/android/06-screens-onboarding-auth-permissions.md")

    @Test
    @DisplayName("the enables, never and if-declined lines are the UX spec's words exactly")
    fun matchesTheSpec() {
        assertTrue(spec.isFile, "the UX spec is missing: ${spec.absolutePath}")
        val lines = spec.readLines()
        for ((block, specRow) in listOf(1 to "Enables", 2 to "Never", 3 to "If declined")) {
            val line = ScreeningRoleCopy.blocks[block].second
            val cells = lines.single { it.startsWith("| $specRow |") }.split("|").map { it.trim() }
            // Columns: (empty) | row name | A09a | A09b | A09c | (empty)
            assertEquals("\"$line\"", cells[3], "A09b '$specRow'")
        }
        assertTrue(spec.readText().contains("\"${ScreeningRoleCopy.NOT_NOW}\""), "the secondary action is \"Not now\"")
    }

    @Test
    @DisplayName("four blocks in the contract's order, and nothing about the microphone or the call log")
    fun contractOrder() {
        assertEquals(
            listOf("What Android will ask", "What it enables", "What we'll never do", "If you say no"),
            ScreeningRoleCopy.blocks.map { it.first },
        )
        val text = ScreeningRoleCopy.blocks.joinToString(" ") { it.second }.lowercase()
        for (word in listOf("microphone", "record", "call log", "contacts permission")) {
            assertFalse(word in text, word)
        }
    }
}
