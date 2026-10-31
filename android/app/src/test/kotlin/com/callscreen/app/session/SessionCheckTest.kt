package com.callscreen.app.session

import com.callscreen.app.session.SessionCheck.Outcome
import com.callscreen.core.auth.EdgeError
import com.callscreen.core.auth.EdgeIdentity
import com.callscreen.core.auth.EdgeResult
import com.callscreen.core.auth.SignedOutReason
import com.callscreen.core.network.identity.IdentityError
import java.io.IOException
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.runBlocking
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertThrows
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

@DisplayName("Session check")
class SessionCheckTest {

    private class MemoryRecord(var at: Long? = null) : CheckRecord {
        override fun last(): Long? = at
        override fun record(atMillis: Long) { at = atMillis }
    }

    private val hour = SessionCheck.INTERVAL_MILLIS
    private var now = 1_760_000_000_000L
    private var calls = 0

    private fun check(record: CheckRecord, answer: suspend () -> EdgeResult<EdgeIdentity>) =
        SessionCheck(me = { calls++; answer() }, record = record, nowMillis = { now })

    private val ok: EdgeResult<EdgeIdentity> = EdgeResult.Success(EdgeIdentity("sub_1", "thumb"))
    private fun failure(error: EdgeError): EdgeResult<EdgeIdentity> = EdgeResult.Failure(error)

    @Test
    @DisplayName("the first session seen only starts the clock: nothing is sent at that launch")
    fun firstSightStartsTheClock() = runBlocking {
        val record = MemoryRecord()
        assertEquals(Outcome.NOT_DUE, check(record) { ok }.runIfDue())
        assertEquals(0, calls)
        assertEquals(now, record.at)
    }

    @Test
    @DisplayName("at most one check per interval, so not at every launch")
    fun atMostOncePerInterval() = runBlocking {
        val record = MemoryRecord(now)
        val check = check(record) { ok }
        now += hour - 1
        assertEquals(Outcome.NOT_DUE, check.runIfDue())
        assertEquals(0, calls)
        now += 1
        assertEquals(Outcome.CONFIRMED, check.runIfDue())
        assertEquals(Outcome.NOT_DUE, check.runIfDue())
        assertEquals(1, calls)
    }

    @Test
    @DisplayName("a clock set back past the last check does not stop checks")
    fun clockSetBack() = runBlocking {
        val record = MemoryRecord(now)
        now -= 3 * hour
        assertEquals(Outcome.CONFIRMED, check(record) { ok }.runIfDue())
        assertEquals(now, record.at)
    }

    @Test
    @DisplayName("only a session that is over ends it; every temporary or unclear failure keeps it")
    fun outcomes() {
        assertEquals(Outcome.CONFIRMED, SessionCheck.outcomeOf(ok))
        for (reason in SignedOutReason.entries - SignedOutReason.NOT_SIGNED_IN) {
            assertEquals(Outcome.ENDED, SessionCheck.outcomeOf(failure(EdgeError.SignedOut(reason))), "$reason")
        }
        assertEquals(Outcome.NO_SESSION, SessionCheck.outcomeOf(failure(EdgeError.SignedOut(SignedOutReason.NOT_SIGNED_IN))))
        val kept = listOf(
            EdgeError.Network,
            EdgeError.TemporarilyUnavailable,
            EdgeError.UnexpectedResponse(500),
            EdgeError.UnexpectedResponse(502),
            EdgeError.UnexpectedResponse(403),
            EdgeError.TokenRejected,
            EdgeError.ProofRejected,
            EdgeError.TokenUnavailable(IdentityError.Network),
            EdgeError.TokenUnavailable(IdentityError.TemporarilyUnavailable),
            EdgeError.TokenUnavailable(IdentityError.UnexpectedResponse(500)),
            EdgeError.TokenUnavailable(IdentityError.RateLimited(30)),
        )
        for (error in kept) assertEquals(Outcome.KEPT, SessionCheck.outcomeOf(failure(error)), "$error")
    }

    @Test
    @DisplayName("no stored session sends nothing and leaves the clock alone")
    fun noSession() = runBlocking {
        val record = MemoryRecord(now - 2 * hour)
        val before = record.at
        assertEquals(Outcome.NO_SESSION, check(record) { failure(EdgeError.SignedOut(SignedOutReason.NOT_SIGNED_IN)) }.runIfDue())
        assertEquals(before, record.at)
    }

    @Test
    @DisplayName("a failed check waits a full interval before the next, so an outage is not hammered")
    fun failureWaitsAnInterval() = runBlocking {
        val record = MemoryRecord(now - hour)
        val check = check(record) { failure(EdgeError.Network) }
        assertEquals(Outcome.KEPT, check.runIfDue())
        assertEquals(Outcome.NOT_DUE, check.runIfDue())
        assertEquals(1, calls)
    }

    @Test
    @DisplayName("an exception (an unreadable session store) keeps the session and never escapes; cancellation does")
    fun exceptions() = runBlocking {
        assertEquals(Outcome.KEPT, check(MemoryRecord(now - hour)) { throw IOException("corrupt session file") }.runIfDue())
        assertThrows(CancellationException::class.java) {
            runBlocking { check(MemoryRecord(now - hour)) { throw CancellationException("left") }.runIfDue() }
        }
        Unit
    }

    @Test
    @DisplayName("the app's confirmIfDue never throws, even when the check cannot be built")
    fun confirmIfDueIsBestEffort() = runBlocking {
        SessionSignals.None.confirmIfDue()
        SessionSignals.of { null }.confirmIfDue()
        SessionSignals.of(check = { error("edge graph failed") }) { null }.confirmIfDue()
        val record = MemoryRecord(now - hour)
        SessionSignals.of(check = { check(record) { throw IOException("unreadable") } }) { null }.confirmIfDue()
        assertEquals(1, calls)
    }
}
