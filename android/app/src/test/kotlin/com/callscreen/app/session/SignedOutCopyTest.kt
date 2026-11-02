package com.callscreen.app.session

import com.callscreen.R
import com.callscreen.core.auth.SessionEndCause
import java.io.File
import javax.xml.parsers.DocumentBuilderFactory
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test
import org.w3c.dom.Element

/**
 * A73's English words as they ship (res/values/strings.xml, which every
 * translation starts from, read as Android reads them) are the ones
 * docs/ux/android/06-screens-onboarding-auth-permissions.md specifies.
 */
@DisplayName("A73 signed-out copy")
class SignedOutCopyTest {

    /** Unit tests run in the module directory (android/app). */
    private val spec = File("../../docs/ux/android/06-screens-onboarding-auth-permissions.md")
    private val strings: Map<String, String> = run {
        val nodes = DocumentBuilderFactory.newInstance().newDocumentBuilder()
            .parse(File("src/main/res/values/strings.xml")).getElementsByTagName("string")
        (0 until nodes.length).associate { i ->
            val e = nodes.item(i) as Element
            e.getAttribute("name") to e.textContent.replace("\\'", "'").replace("\\\"", "\"")
        }
    }

    @Test
    @DisplayName("each line is quoted in the spec, word for word")
    fun matchesTheSpec() {
        assertTrue(spec.isFile, "the UX spec is missing: ${spec.absolutePath}")
        // The spec wraps its lines, so compare with whitespace collapsed.
        val text = spec.readText().replace(Regex("\\s+"), " ")
        for (name in listOf("title", "you_signed_out", "not_verified", "blocking_still_on")) {
            val line = strings.getValue("app_signed_out_$name")
            assertTrue("\"$line\"" in text, "$name: \"$line\"")
        }
        assertTrue("**${strings.getValue("app_signed_out_sign_in")}**" in text, "the one action, Sign in")
    }

    @Test
    @DisplayName("the screen shows those resources, a reason only when the app knows it")
    fun theScreenUsesTheseStrings() {
        assertEquals(R.string.app_signed_out_title, SignedOutCopy.TITLE)
        assertEquals(R.string.app_signed_out_sign_in, SignedOutCopy.SIGN_IN)
        assertEquals(listOf(R.string.app_signed_out_you_signed_out), SignedOutCopy.lines(SessionEndCause.SIGNED_OUT_ON_THIS_DEVICE, false))
        assertEquals(listOf(R.string.app_signed_out_not_verified), SignedOutCopy.lines(SessionEndCause.STORED_SESSION_UNRECOVERABLE, false))
        assertEquals(listOf(R.string.app_signed_out_blocking_still_on), SignedOutCopy.lines(SessionEndCause.SESSION_ENDED, true))
    }
}
