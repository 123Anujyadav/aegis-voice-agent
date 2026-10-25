package com.callscreen.app

import com.callscreen.app.navigation.AegisDestinations
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

@DisplayName("Start destination")
class StartDestinationTest {

    @Test
    @DisplayName("a release build ignores a requested screen: nothing skips onboarding and sign-in")
    fun releaseIgnoresTheExtra() {
        for (requested in listOf(AegisDestinations.PROTECTION_DASHBOARD, AegisDestinations.CALLS_FEED, "caller_list/block", "", null)) {
            assertEquals(AegisDestinations.SPLASH, MainActivity.startDestinationFor(requested, debuggable = false), requested.toString())
        }
    }

    @Test
    @DisplayName("a debug build honours it for manual testing, and defaults to the splash screen")
    fun debugHonoursTheExtra() {
        assertEquals(AegisDestinations.PROTECTION_DASHBOARD, MainActivity.startDestinationFor(AegisDestinations.PROTECTION_DASHBOARD, true))
        assertEquals(AegisDestinations.SPLASH, MainActivity.startDestinationFor(null, true))
        assertEquals(AegisDestinations.SPLASH, MainActivity.startDestinationFor("", true))
    }
}
