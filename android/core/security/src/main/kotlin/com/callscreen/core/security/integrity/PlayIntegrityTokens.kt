package com.callscreen.core.security.integrity

import com.callscreen.core.security.dpop.integrityRequestHash

/*
 * Play Integrity tokens for identity's enrolment and refresh (ADR-0031).
 *
 * A token is a STANDARD request bound to the request it accompanies:
 * requestHash = base64url(SHA-256(the DPoP proof sent with it)), D31.1. The
 * server recomputes the hash from the DPoP header and restricts a verdict that
 * does not match.
 *
 * Every failure returns null, and the request then carries no Integrity-Token.
 * That covers no provider, a timeout, a Google error, or a device without Play
 * services. ADR-0031 D31.3 records such a session as `unverified`, so
 * enrolment is never blocked here, and no token is ever invented.
 *
 * Nothing is logged: not the token, the proof or the hash.
 */

/** The platform's Play Integrity, behind a seam so the logic here is JVM-testable. */
public interface IntegritySource {
    /** Prepares a token provider (Google's warm-up), waiting at most [timeoutMillis]. */
    public fun prepare(timeoutMillis: Long): IntegrityProvider
}

public fun interface IntegrityProvider {
    /**
     * A token bound to [requestHash], waiting at most [timeoutMillis]. Throws
     * [IntegrityProviderInvalidException] when Google says the provider must
     * be prepared again.
     */
    public fun request(requestHash: String, timeoutMillis: Long): String
}

/** Google's INTEGRITY_TOKEN_PROVIDER_INVALID: prepare a new provider and retry. */
public class IntegrityProviderInvalidException : Exception("integrity token provider invalid")

/**
 * Tokens from [source], with the provider cached across requests.
 *
 * Preparation is throttled to one attempt per [minPrepareIntervalMillis].
 * Google allows five per minute per app instance, and an app offline or
 * without Play services must not spend them on every request.
 */
public class PlayIntegrityTokens(
    private val source: IntegritySource,
    private val nowMillis: () -> Long = System::currentTimeMillis,
    private val prepareTimeoutMillis: Long = 10_000,
    private val requestTimeoutMillis: Long = 10_000,
    private val minPrepareIntervalMillis: Long = 15_000,
) {
    private var provider: IntegrityProvider? = null
    private var lastPrepareAt: Long? = null

    /** A token bound to [proof], or null if none can be obtained now. Never throws. */
    @Synchronized
    public fun tokenFor(proof: String): String? {
        val hash = integrityRequestHash(proof)
        val current = provider ?: prepareOrNull() ?: return null
        return try {
            current.request(hash, requestTimeoutMillis).takeIf { it.isNotEmpty() }
        } catch (e: IntegrityProviderInvalidException) {
            provider = null
            val fresh = prepareOrNull() ?: return null
            runCatching { fresh.request(hash, requestTimeoutMillis) }.getOrNull()?.takeIf { it.isNotEmpty() }
        } catch (e: Exception) {
            null
        }
    }

    /** Prepares the provider ahead of the first request (for example at app start). */
    @Synchronized
    public fun warmUp() {
        if (provider == null) prepareOrNull()
    }

    private fun prepareOrNull(): IntegrityProvider? {
        val now = nowMillis()
        val last = lastPrepareAt
        if (last != null && now - last < minPrepareIntervalMillis) return null
        lastPrepareAt = now
        return try {
            source.prepare(prepareTimeoutMillis).also { provider = it }
        } catch (e: Exception) {
            null
        }
    }
}
