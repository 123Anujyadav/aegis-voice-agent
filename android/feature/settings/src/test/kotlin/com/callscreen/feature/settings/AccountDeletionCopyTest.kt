package com.callscreen.feature.settings

import java.io.File
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

/**
 * The deletion screen's English words, as they ship in res/values/strings.xml,
 * are the ones docs/phase19/ACCOUNT-DELETION.md specifies for each state.
 */
@DisplayName("Account deletion copy")
class AccountDeletionCopyTest {

    /** Unit tests run in the module directory (android/feature/settings). */
    private val spec = File("../../../docs/phase19/ACCOUNT-DELETION.md")
    private val english = ShippedStrings.byFolder.getValue("values")

    private fun text(id: Int): String = english.getValue(ShippedStrings.name(id))

    /** The spec's table row for [state]: | State | What the user sees |. */
    private fun row(state: String): String {
        assertTrue(spec.isFile, "the spec is missing: ${spec.absolutePath}")
        return spec.readLines().single { it.startsWith("| $state |") }
    }

    @Test
    @DisplayName("each state's quoted words and button names are the spec's")
    fun matchesTheSpec() {
        val expected = listOf(
            "Checking" to "\"${text(AccountDeletionCopy.CHECKING)}\"",
            "Ready" to "\"${text(AccountDeletionCopy.PERMANENT)}\"",
            "Ready" to "**${text(AccountDeletionCopy.DELETE_BUTTON)}**",
            "Confirming" to "\"${text(AccountDeletionCopy.CONFIRM_TITLE)}\"",
            "Confirming" to "**${text(AccountDeletionCopy.CONFIRM)}**",
            "Confirming" to "**${text(AccountDeletionCopy.CANCEL)}**",
            "Deleting" to "\"${text(AccountDeletionCopy.DELETING)}\"",
            "Deleted" to "\"${text(AccountDeletionCopy.DONE_TITLE)}\"",
            "Deleted" to "**${text(AccountDeletionCopy.DONE_BUTTON)}**",
        )
        for ((state, words) in expected) {
            assertTrue(words in row(state), "$state: $words")
        }
    }

    @Test
    @DisplayName("the no-answer failure keeps the spec's opening and its retry promise")
    fun noAnswerMatchesTheSpec() {
        val message = text(AccountDeletionCopy.message(DeletionMessage.NO_CONFIRMATION))
        val line = spec.readLines().single { it.startsWith("- **No answer, or an answer outside the contract:**") }
        // The spec elides the middle: "We could not confirm the deletion… If it already went through, …"
        val quoted = line.substringAfter("\"").substringBefore("\"")
        val (start, end) = quoted.split("…").map { it.trim() }
        assertTrue(message.startsWith(start), message)
        assertTrue(message.endsWith(end), message)
    }

    @Test
    @DisplayName("every line of the explanation has English text")
    fun everyLineHasText() {
        for (id in AccountDeletionCopy.DELETED + AccountDeletionCopy.KEPT) {
            assertTrue(text(id).isNotBlank(), ShippedStrings.name(id))
        }
    }
}
