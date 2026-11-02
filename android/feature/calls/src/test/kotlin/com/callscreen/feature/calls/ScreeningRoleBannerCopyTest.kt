package com.callscreen.feature.calls

import java.io.File
import javax.xml.parsers.DocumentBuilderFactory
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test
import org.w3c.dom.Element

@DisplayName("F14 role-loss banner copy")
class ScreeningRoleBannerCopyTest {

    /**
     * The English strings as they ship (res/values/strings.xml, which every
     * translation starts from), read as Android reads them: escapes undone.
     * Unit tests run in the module directory (android/feature/calls).
     */
    private val strings: Map<String, String> = run {
        val file = File("src/main/res/values/strings.xml")
        val nodes = DocumentBuilderFactory.newInstance().newDocumentBuilder().parse(file).getElementsByTagName("string")
        (0 until nodes.length).associate { i ->
            val e = nodes.item(i) as Element
            e.getAttribute("name") to e.textContent.replace("\\'", "'").replace("\\\"", "\"")
        }
    }

    @Test
    @DisplayName("the banner sentence is the UX spec's, word for word")
    fun matchesTheSpec() {
        val spec = File("../../../docs/ux/android/08-user-flows.md")
        assertTrue(spec.isFile, "the UX spec is missing: ${spec.absolutePath}")
        // The spec wraps the sentence inside a diagram, so compare with whitespace collapsed.
        val text = spec.readText().replace(Regex("\\s+"), " ")
        assertTrue("\"${strings.getValue("calls_role_lost")}\"" in text, "the A75 banner text")
    }

    @Test
    @DisplayName("the banner shows those resources")
    fun theBannerUsesTheseStrings() {
        assertEquals(R.string.calls_role_lost, ScreeningRoleBannerCopy.LOST)
        assertEquals(R.string.calls_role_switch_back, ScreeningRoleBannerCopy.SWITCH_BACK)
    }
}
