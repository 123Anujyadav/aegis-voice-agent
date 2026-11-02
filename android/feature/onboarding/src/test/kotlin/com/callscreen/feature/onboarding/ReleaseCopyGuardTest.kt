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
        "AI will answer unknown calls on your behalf" to "assistant screening needs forwarding, not available",
        "AI Listening" to "the microphone is never used",
        "Audition & Save Voice" to "nothing auditions or saves a voice",
        "David's Senior Mode" to "a sample person in the Settings tab",
        "spam telemetry signatures" to "no fraud database is downloaded",
        "screening this call for Abhik" to "a real person's name in sample text",
    )

    @Test
    @DisplayName("no production source or string resource contains a removed invented value or claim")
    fun noneReturn() {
        // Unit tests run in the module directory: android/feature/onboarding.
        val android = File("../..").canonicalFile
        val main = android.walkTopDown()
            .onEnter { it.name !in setOf("build", ".gradle", "test", "androidTest", "build-logic") }
            .filter { it.isFile && "${File.separator}main${File.separator}" in it.path }
            .toList()
        val sources = main.filter { it.extension == "kt" }
        // The app's words live in string resources (res/values*/, every language), not only in Kotlin.
        val strings = main.filter { it.extension == "xml" && it.parentFile?.name?.startsWith("values") == true }
        assertTrue(sources.size > 50, "found only ${sources.size} sources under $android")
        assertTrue(strings.any { it.path.endsWith("feature${File.separator}onboarding${File.separator}src${File.separator}main${File.separator}res${File.separator}values${File.separator}strings.xml") }, "onboarding's strings.xml was not scanned")
        val found = (sources + strings).flatMap { f ->
            // As Android shows it: XML entities and resource escapes undone.
            val text = f.readText().replace("&amp;", "&").replace("&lt;", "<").replace("&gt;", ">").replace("\\'", "'").replace("\\\"", "\"")
            banned.filterKeys { it in text }.map { (value, why) -> "${f.relativeTo(android)}: \"$value\" ($why)" }
        }
        assertEquals(emptyList<String>(), found)
    }
}
