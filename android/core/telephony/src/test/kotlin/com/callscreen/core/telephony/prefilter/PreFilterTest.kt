package com.callscreen.core.telephony.prefilter

import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertNull
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

@DisplayName("On-device pre-filter (ADR-0002 Stage 1)")
class PreFilterTest {

    private val blocked = "+919876500001"
    private val allowed = "+919876500002"
    private val contact = "+919876500003"
    private val stranger = "+919876500004"

    private val lists = CallerListLookup {
        when (it.value) {
            blocked -> ListType.BLOCK
            allowed -> ListType.ALLOW
            else -> null
        }
    }
    private val contacts = ContactLookup { it.value == contact }
    private val filter = PreFilter(lists, contacts)

    @Test
    @DisplayName("Indian and international forms normalise to E.164; uncertain forms give null")
    fun normalisation() {
        val expected = mapOf(
            "+91 98765 43210" to "+919876543210",
            "9876543210" to "+919876543210",
            "098765-43210" to "+919876543210",
            "919876543210" to "+919876543210",
            "0091 98765 43210" to "+919876543210",
            "(011) 2345 6789" to "+911123456789",
            "+1 (415) 555-0100" to "+14155550100",
            "+44 20 7946 0000" to "+442079460000",
        )
        for ((raw, e164) in expected) assertEquals(e164, PhoneNumbers.normalise(raw)?.value, raw)
        // Short codes (emergency included), local landlines without an area
        // code, a 10-digit number that is not a mobile, malformed and empty.
        val uncertain = listOf(
            "112", "100", "1800", "23456789", "1234567890", "+0123456789", "+12",
            "98765O3210", "tel:9876543210", "", "+", "00",
        )
        for (raw in uncertain) {
            assertNull(PhoneNumbers.normalise(raw), raw)
        }
    }

    @Test
    @DisplayName("allowlist rings through, blocklist rejects, contact rings through, anything else is screened")
    fun decisions() {
        assertEquals(PreFilterDecision.RING_THROUGH, filter.decide(allowed))
        assertEquals(PreFilterDecision.REJECT, filter.decide(blocked))
        assertEquals(PreFilterDecision.RING_THROUGH, filter.decide(contact))
        assertEquals(PreFilterDecision.SCREEN, filter.decide(stranger))
    }

    @Test
    @DisplayName("a number matches its list in any form the handset presents it")
    fun matchesOnTheNormalisedForm() {
        for (raw in listOf("98765 00001", "098765-00001", "919876500001", "0091 9876500001")) {
            assertEquals(PreFilterDecision.REJECT, filter.decide(raw), raw)
        }
    }

    @Test
    @DisplayName("an explicit block wins over a contact; an explicit allow wins over everything")
    fun explicitInstructionWins() {
        val blockedContact = PreFilter(lists) { true }
        assertEquals(PreFilterDecision.REJECT, blockedContact.decide(blocked))
        assertEquals(PreFilterDecision.RING_THROUGH, blockedContact.decide(allowed))
    }

    @Test
    @DisplayName("a number that cannot be normalised is screened without consulting the lists, never rejected")
    fun unknownFormIsScreened() {
        val everythingBlocked = PreFilter({ ListType.BLOCK }, { false })
        for (raw in listOf(null, "", "112", "private", "23456789")) {
            assertEquals(PreFilterDecision.SCREEN, everythingBlocked.decide(raw), raw.toString())
        }
    }

    @Test
    @DisplayName("a failing lookup degrades to screening, never to rejection or a crash")
    fun failingLookupsDegradeToScreen() {
        val brokenLists = PreFilter({ error("database unavailable") }, contacts)
        assertEquals(PreFilterDecision.SCREEN, brokenLists.decide(blocked))
        assertEquals(PreFilterDecision.RING_THROUGH, brokenLists.decide(contact))
        val brokenBoth = PreFilter({ error("database unavailable") }, { error("provider unavailable") })
        assertEquals(PreFilterDecision.SCREEN, brokenBoth.decide(contact))
    }

    @Test
    @DisplayName("SILENCE is never produced: its use is reserved for a reputation mapping the owner sets")
    fun noSilenceYet() {
        val decisions = listOf(blocked, allowed, contact, stranger, null, "112").map(filter::decide)
        assertFalse(PreFilterDecision.SILENCE in decisions)
    }

    @Test
    @DisplayName("each decision maps to the platform response that keeps a blocked call off the forwarding path")
    fun platformResponses() {
        val allow = ScreeningResponse(
            disallowCall = false,
            rejectCall = false,
            silenceCall = false,
            skipNotification = false,
        )
        assertEquals(allow, ScreeningResponse.of(PreFilterDecision.RING_THROUGH))
        assertEquals(allow, ScreeningResponse.of(PreFilterDecision.SCREEN))
        assertEquals(
            ScreeningResponse(disallowCall = true, rejectCall = true, silenceCall = false, skipNotification = true),
            ScreeningResponse.of(PreFilterDecision.REJECT),
        )
        assertEquals(allow.copy(silenceCall = true), ScreeningResponse.of(PreFilterDecision.SILENCE))
        // The platform's own rules: reject and skip-notification only with
        // disallow; silence only without it.
        for (d in PreFilterDecision.entries) {
            val r = ScreeningResponse.of(d)
            assertFalse((r.rejectCall || r.skipNotification) && !r.disallowCall, d.name)
            assertFalse(r.silenceCall && r.disallowCall, d.name)
        }
    }
}
