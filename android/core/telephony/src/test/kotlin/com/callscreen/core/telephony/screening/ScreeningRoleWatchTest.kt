package com.callscreen.core.telephony.screening

import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

@DisplayName("Losing the screening role (F14)")
class ScreeningRoleWatchTest {

    private class MemoryFlag : Flag {
        var value = false

        override fun get(): Boolean = value

        override fun set() {
            value = true
        }
    }

    @Test
    @DisplayName("never held: nothing is lost, whatever the status")
    fun neverHeld() {
        val watch = ScreeningRoleWatch(MemoryFlag())
        assertFalse(watch.lost(ScreeningRoleStatus.NOT_HELD))
        assertFalse(watch.lost(ScreeningRoleStatus.UNAVAILABLE))
    }

    @Test
    @DisplayName("held, then taken by another app: lost until it is held again; remembered across restarts")
    fun lostAndRegained() {
        val flag = MemoryFlag()
        val watch = ScreeningRoleWatch(flag)
        assertFalse(watch.lost(ScreeningRoleStatus.HELD))
        assertTrue(watch.lost(ScreeningRoleStatus.NOT_HELD))
        assertTrue(ScreeningRoleWatch(flag).lost(ScreeningRoleStatus.NOT_HELD), "after a restart")
        assertFalse(watch.lost(ScreeningRoleStatus.HELD), "switched back: the banner clears")
    }
}
