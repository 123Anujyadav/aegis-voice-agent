package com.callscreen.feature.onboarding

import java.io.File
import javax.xml.parsers.DocumentBuilderFactory
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test
import org.w3c.dom.Element

/**
 * The A09b rationale as it ships: the English strings in
 * res/values/strings.xml, which every translation starts from, read as
 * Android reads them (with its escapes undone).
 */
@DisplayName("A09b screening role rationale copy")
class ScreeningRoleCopyTest {

    /** Unit tests run in the module directory (android/feature/onboarding). */
    private val spec = File("../../../docs/ux/android/06-screens-onboarding-auth-permissions.md")
    private val strings: Map<String, String> = run {
        val file = File("src/main/res/values/strings.xml")
        val nodes = DocumentBuilderFactory.newInstance().newDocumentBuilder().parse(file).getElementsByTagName("string")
        (0 until nodes.length).associate { i ->
            val e = nodes.item(i) as Element
            e.getAttribute("name") to e.textContent.replace("\\'", "'").replace("\\\"", "\"")
        }
    }

    /** The four blocks, in the order ScreeningRoleCopy.blocks shows them. */
    private val blocks = listOf("ask", "enables", "never", "declined")

    @Test
    @DisplayName("the enables, never and if-declined lines are the UX spec's words exactly")
    fun matchesTheSpec() {
        assertTrue(spec.isFile, "the UX spec is missing: ${spec.absolutePath}")
        val lines = spec.readLines()
        for ((block, specRow) in listOf("enables" to "Enables", "never" to "Never", "declined" to "If declined")) {
            val line = strings.getValue("onboarding_role_$block")
            val cells = lines.single { it.startsWith("| $specRow |") }.split("|").map { it.trim() }
            // Columns: (empty) | row name | A09a | A09b | A09c | (empty)
            assertEquals("\"$line\"", cells[3], "A09b '$specRow'")
        }
        assertTrue(spec.readText().contains("\"${strings.getValue("onboarding_role_not_now")}\""), "the secondary action is \"Not now\"")
    }

    @Test
    @DisplayName("four blocks in the contract's order, and nothing about the microphone or the call log")
    fun contractOrder() {
        assertEquals(
            listOf("What Android will ask", "What it enables", "What we'll never do", "If you say no"),
            blocks.map { strings.getValue("onboarding_role_${it}_label") },
        )
        val text = blocks.joinToString(" ") { strings.getValue("onboarding_role_$it") }.lowercase()
        for (word in listOf("microphone", "record", "call log", "contacts permission")) {
            assertFalse(word in text, word)
        }
    }

    @Test
    @DisplayName("the screen shows those four blocks, in that order")
    fun theScreenUsesTheseStrings() {
        assertEquals(
            listOf(
                R.string.onboarding_role_ask_label to R.string.onboarding_role_ask,
                R.string.onboarding_role_enables_label to R.string.onboarding_role_enables,
                R.string.onboarding_role_never_label to R.string.onboarding_role_never,
                R.string.onboarding_role_declined_label to R.string.onboarding_role_declined,
            ),
            ScreeningRoleCopy.blocks,
        )
    }

    @Test
    @DisplayName("after a decline: the same first three blocks, then the loss in the past tense, and nothing about forwarding")
    fun afterADecline() {
        assertEquals(ScreeningRoleCopy.blocks.dropLast(1), ScreeningRoleCopy.blocksAfterDecline.dropLast(1))
        assertEquals(
            R.string.onboarding_role_missing_label to R.string.onboarding_role_missing,
            ScreeningRoleCopy.blocksAfterDecline.last(),
        )
        assertEquals("What you're missing", strings.getValue("onboarding_role_missing_label"))
        val missing = strings.getValue("onboarding_role_missing")
        // The spec's past-tense opening (06-screens, A09b edge cases: "You're currently missing…").
        assertTrue(missing.startsWith("You're currently missing"), missing)
        // Only what is true today: call forwarding is not set up, so it is not mentioned.
        for (word in listOf("forward", "carrier", "charge", "microphone", "record")) {
            assertFalse(word in missing.lowercase(), word)
        }
    }

    @Test
    @DisplayName("the first-time if-declined line promises no forwarding (none exists before M3) and names the way back")
    fun declinedLineIsTrueToday() {
        val line = strings.getValue("onboarding_role_declined").lowercase()
        for (word in listOf("forward", "carrier", "charge", "screened too")) {
            assertFalse(word in line, word)
        }
        assertTrue("protection" in line, line)
    }
}
