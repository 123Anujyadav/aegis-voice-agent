package com.callscreen.feature.calls

import java.io.File
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

@DisplayName("F14 role-loss banner copy")
class ScreeningRoleBannerCopyTest {

    @Test
    @DisplayName("the banner sentence is the UX spec's, word for word")
    fun matchesTheSpec() {
        // Unit tests run in the module directory (android/feature/calls).
        val spec = File("../../../docs/ux/android/08-user-flows.md")
        assertTrue(spec.isFile, "the UX spec is missing: ${spec.absolutePath}")
        // The spec wraps the sentence inside a diagram, so compare with whitespace collapsed.
        val text = spec.readText().replace(Regex("\\s+"), " ")
        assertTrue("\"${ScreeningRoleBannerCopy.LOST}\"" in text, "the A75 banner text")
    }
}
