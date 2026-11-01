package com.callscreen.app.session

import android.content.Context
import com.callscreen.core.auth.EdgeError
import com.callscreen.core.auth.EdgeIdentity
import com.callscreen.core.auth.EdgeResult
import com.callscreen.core.auth.SignedOutReason
import kotlinx.coroutines.CancellationException

/** When the session was last checked with the server, kept across launches. */
internal interface CheckRecord {
    /** Epoch milliseconds of the last check, or null if none was ever recorded. */
    fun last(): Long?

    fun record(atMillis: Long)
}

/** [CheckRecord] in the app's own preferences. A timestamp only: nothing about the session. */
internal class PreferencesCheckRecord(context: Context) : CheckRecord {
    private val prefs = context.applicationContext.getSharedPreferences(FILE, Context.MODE_PRIVATE)

    override fun last(): Long? = if (prefs.contains(KEY)) prefs.getLong(KEY, 0) else null

    override fun record(atMillis: Long) {
        prefs.edit().putLong(KEY, atMillis).apply()
    }

    private companion object {
        const val FILE = "callscreen_session_check"
        const val KEY = "last_check_ms"
    }
}

/**
 * Confirms the stored session with the server through edge-api's GET /v1/me,
 * at most once per [intervalMillis], so a session identity has ended is
 * noticed while the app is in use and shows A73.
 *
 * edge-api verifies access tokens itself and identity does not revoke them
 * (docs/phase19/ACCOUNT-DELETION.md R1), so the ending is confirmed by
 * identity when the request needs the token refreshed. The authenticator then
 * clears the session; only identity's "session ended" does that. A network
 * failure, a timeout, a 5xx or any other refusal keeps the session.
 *
 * Never at every launch: the first time a session is seen only starts the
 * clock, and a check is due only [intervalMillis] after the previous one.
 */
internal class SessionCheck(
    private val me: suspend () -> EdgeResult<EdgeIdentity>,
    private val record: CheckRecord,
    private val nowMillis: () -> Long = System::currentTimeMillis,
    private val intervalMillis: Long = INTERVAL_MILLIS,
) {
    enum class Outcome {
        /** Checked less than an interval ago, or the first time: nothing sent. */
        NOT_DUE,

        /** No session is stored: nothing sent. */
        NO_SESSION,

        /** edge-api accepted the session. */
        CONFIRMED,

        /** The session is over; the authenticator has cleared it, and A73 follows. */
        ENDED,

        /** Not confirmed either way (offline, timeout, 5xx, a refused proof): the session is kept. */
        KEPT,
    }

    suspend fun runIfDue(): Outcome {
        val now = nowMillis()
        val last = record.last()
        if (last == null) {
            record.record(now)
            return Outcome.NOT_DUE
        }
        // A clock set back past the last check would otherwise stop checks until it caught up.
        if (now - last < intervalMillis && now >= last) return Outcome.NOT_DUE

        val result = try {
            me()
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            // An unreadable session store, or anything else unexpected: say nothing about the session.
            record.record(now)
            return Outcome.KEPT
        }
        val outcome = outcomeOf(result)
        if (outcome != Outcome.NO_SESSION) record.record(now)
        return outcome
    }

    internal companion object {
        /** Once an hour of use at most: four access-token lifetimes. */
        const val INTERVAL_MILLIS: Long = 60L * 60 * 1000

        fun outcomeOf(result: EdgeResult<EdgeIdentity>): Outcome = when (result) {
            is EdgeResult.Success -> Outcome.CONFIRMED
            is EdgeResult.Failure -> when (val error = result.error) {
                is EdgeError.SignedOut ->
                    if (error.reason == SignedOutReason.NOT_SIGNED_IN) Outcome.NO_SESSION else Outcome.ENDED
                // edge-api refusing a token identity just refreshed is not identity ending the session.
                is EdgeError.TokenUnavailable, EdgeError.TokenRejected, EdgeError.ProofRejected,
                EdgeError.TemporarilyUnavailable, EdgeError.Network, is EdgeError.UnexpectedResponse,
                -> Outcome.KEPT
            }
        }
    }
}
