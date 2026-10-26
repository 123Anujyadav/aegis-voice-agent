package com.callscreen.feature.onboarding

import java.io.File
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

/**
 * A release build writes nothing to logcat (docs/phase19/ANDROID-DATA-INVENTORY.md):
 * it plants no Timber tree, and no production code logs directly. Phone
 * numbers, codes and tokens pass through this app, and logcat is readable by
 * anyone with a debugging connection, so a direct log call is a leak waiting
 * to happen. Timber calls are safe only because release plants no tree.
 */
@DisplayName("Release logging guard")
class ReleaseLoggingGuardTest {

    private val android = File("../..").canonicalFile // unit tests run in android/feature/onboarding

    private val sources = android.walkTopDown()
        .onEnter { it.name !in setOf("build", ".gradle", "test", "androidTest", "build-logic") }
        .filter { it.isFile && it.extension in setOf("kt", "java") }
        .filter { "${File.separator}main${File.separator}" in it.path }
        .toList()

    @Test
    @DisplayName("no production code logs directly (android.util.Log, println, printStackTrace)")
    fun noDirectLogging() {
        assertTrue(sources.size > 50, "found only ${sources.size} sources under $android")
        val direct = listOf("android.util.Log", "Log.v(", "Log.d(", "Log.i(", "Log.w(", "Log.e(", "Log.wtf(", "println(", ".printStackTrace(")
        val found = sources.flatMap { f ->
            val text = f.readText()
            direct.filter { it in text }.map { "${f.relativeTo(android)}: $it" }
        }
        assertEquals(emptyList<String>(), found)
    }

    @Test
    @DisplayName("a Timber tree is planted only in debug builds, and only in one place")
    fun timberTreeIsDebugOnly() {
        val planting = sources.filter { "Timber.plant(" in it.readText() }
        assertEquals(listOf("CallScreenApplication.kt"), planting.map { it.name })
        val text = planting.single().readText()
        val plant = text.indexOf("Timber.plant(")
        val guard = text.lastIndexOf("if (BuildConfig.DEBUG)", plant)
        assertTrue(guard in 0 until plant && plant - guard < 120, "Timber.plant must sit inside if (BuildConfig.DEBUG)")
    }
}
