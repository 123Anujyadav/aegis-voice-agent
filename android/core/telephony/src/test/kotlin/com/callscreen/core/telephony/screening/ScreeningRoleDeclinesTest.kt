package com.callscreen.core.telephony.screening

import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

@DisplayName("A09b after a decline (previously and permanently denied)")
class ScreeningRoleDeclinesTest {

    private class MemoryFlag : Flag {
        var value = false

        override fun get(): Boolean = value

        override fun set() {
            value = true
        }
    }

    private val once = MemoryFlag()
    private val twice = MemoryFlag()
    private fun declines() = ScreeningRoleDeclines(once, twice)

    @Test
    @DisplayName("never declined: offered, in its first form")
    fun neverDeclined() {
        assertTrue(declines().shouldOffer())
        assertFalse(declines().previouslyDeclined())
    }

    @Test
    @DisplayName("declined once: offered again, in the past-tense form; remembered across restarts")
    fun declinedOnce() {
        declines().recordOutcome(ScreeningRoleStatus.NOT_HELD)
        assertTrue(declines().shouldOffer())
        assertTrue(declines().previouslyDeclined())
    }

    @Test
    @DisplayName("declined twice: not offered again (Protection's notice remains the way back)")
    fun declinedTwice() {
        declines().recordOutcome(ScreeningRoleStatus.NOT_HELD)
        declines().recordOutcome(ScreeningRoleStatus.NOT_HELD)
        assertFalse(declines().shouldOffer())
    }

    @Test
    @DisplayName("granting records nothing; an unavailable role counts as not granted")
    fun grantingRecordsNothing() {
        declines().recordOutcome(ScreeningRoleStatus.HELD)
        assertFalse(declines().previouslyDeclined())
        assertTrue(declines().shouldOffer())
        declines().recordOutcome(ScreeningRoleStatus.UNAVAILABLE)
        assertTrue(declines().previouslyDeclined())
    }
}
