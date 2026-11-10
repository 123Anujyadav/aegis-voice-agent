package com.callscreen.feature.calls

import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

@DisplayName("F14: granted → banner clears, haptic.confirm (decision packet §3a)")
class RoleRegainedTest {
    @Test
    @DisplayName("only the banner clearing confirms")
    fun onlyTheBannerClearingConfirms() {
        assertTrue(roleRegained(wasLost = true, nowLost = false))
        assertFalse(roleRegained(wasLost = true, nowLost = true), "still lost")
        assertFalse(roleRegained(wasLost = false, nowLost = false), "never lost")
        assertFalse(roleRegained(wasLost = false, nowLost = true), "just lost")
    }
}
