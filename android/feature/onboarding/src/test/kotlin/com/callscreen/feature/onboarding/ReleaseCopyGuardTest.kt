package com.callscreen.feature.onboarding

import java.io.File
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

/**
 * Invented values and unsupported claims that were removed from the app
 * (docs/phase19/ANDROID-SCREENING-DEVICE-VALIDATION.md §4.1) must not come
 * back. Each entry is something the app has no source for, or cannot do.
 */
@DisplayName("Release copy guard")
class ReleaseCopyGuardTest {

    private val banned = mapOf(
        "Aegis Score" to "no protection score exists",
        "Jio 5G India" to "carrier shown without the phone reporting it",
        "Call Forwarding Supported" to "forwarding support is only known once set up and checked",
        "*71*" to "not the ADR-0002 forwarding code; a US code",
        "+911140008899" to "an invented forwarding number",
        "dial and activate automatically" to "the button dials nothing",
        "before your phone rings" to "the assistant answers only after the carrier's ring delay",
        "speech vectors" to "no such analysis exists",
        "Protection Active" to "claimed active when forwarding is not available",
        "Unknown calls will automatically be screened" to "forwarding is not available",
        "412 reports" to "no threat feed exists",
        "Sarah (Wife)" to "sample call records",
        "without speaking" to "every caller hears the announcement (ADR-0012)",
    )

    @Test
    @DisplayName("no production source contains a removed invented value or claim")
    fun noneReturn() {
        // Unit tests run in the module directory: android/feature/onboarding.
        val android = File("../..").canonicalFile
        val sources = android.walkTopDown()
            .onEnter { it.name !in setOf("build", ".gradle", "test", "androidTest", "build-logic") }
            .filter { it.isFile && it.extension == "kt" }
            .filter { "${File.separator}main${File.separator}" in it.path }
            .toList()
        assertTrue(sources.size > 50, "found only ${sources.size} sources under $android")
        val found = sources.flatMap { f ->
            val text = f.readText()
            banned.filterKeys { it in text }.map { (value, why) -> "${f.relativeTo(android)}: \"$value\" ($why)" }
        }
        assertEquals(emptyList<String>(), found)
    }
}
