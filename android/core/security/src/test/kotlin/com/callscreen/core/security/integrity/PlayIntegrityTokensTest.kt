package com.callscreen.core.security.integrity

import com.callscreen.core.security.dpop.integrityRequestHash
import java.util.concurrent.TimeoutException
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertNull
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

/** The token logic over a fake source. Nothing here reaches Google. */
@DisplayName("Play Integrity tokens")
class PlayIntegrityTokensTest {

    private class FakeSource : IntegritySource {
        var prepares = 0
        var prepareFails = false
        val hashes = mutableListOf<String>()
        /** Per request, in order: a token, or an exception to throw. */
        val answers = ArrayDeque<Any>()

        override fun prepare(timeoutMillis: Long): IntegrityProvider {
            prepares++
            if (prepareFails) throw TimeoutException("warm-up timed out")
            return IntegrityProvider { hash, _ ->
                hashes += hash
                when (val next = answers.removeFirstOrNull() ?: "token") {
                    is Exception -> throw next
                    else -> next as String
                }
            }
        }
    }

    private var now = 1_000_000L

    private fun tokens(source: FakeSource) = PlayIntegrityTokens(source, nowMillis = { now })

    @Test
    @DisplayName("the token is requested for base64url(SHA-256(proof)), the server's binding (ADR-0031 D31.1)")
    fun bindsToTheProof() {
        val source = FakeSource()
        assertEquals("token", tokens(source).tokenFor("the.dpop.proof"))
        assertEquals(listOf(integrityRequestHash("the.dpop.proof")), source.hashes)
    }

    @Test
    @DisplayName("the provider is prepared once and reused")
    fun cachesTheProvider() {
        val source = FakeSource()
        val t = tokens(source)
        t.tokenFor("p1")
        t.tokenFor("p2")
        assertEquals(1, source.prepares)
        assertEquals(listOf(integrityRequestHash("p1"), integrityRequestHash("p2")), source.hashes)
    }

    @Test
    @DisplayName("an invalid provider is prepared again once, and the request retried")
    fun reprepareOnProviderInvalid() {
        val source = FakeSource().apply { answers += IntegrityProviderInvalidException() }
        now += 0
        val t = PlayIntegrityTokens(source, nowMillis = { now }, minPrepareIntervalMillis = 0)
        assertEquals("token", t.tokenFor("p"))
        assertEquals(2, source.prepares)
        assertEquals(2, source.hashes.size)
    }

    @Test
    @DisplayName("any failure is null (no Integrity-Token, so the session is unverified), never an exception")
    fun failuresAreNull() {
        val failing = FakeSource().apply { answers += IllegalStateException("google error") }
        assertNull(tokens(failing).tokenFor("p"))
        val emptyToken = FakeSource().apply { answers += "" }
        assertNull(tokens(emptyToken).tokenFor("p"))
        val noProvider = FakeSource().apply { prepareFails = true }
        assertNull(tokens(noProvider).tokenFor("p"))
    }

    @Test
    @DisplayName("preparation is throttled: a device without Play services does not retry on every request")
    fun throttlesPreparation() {
        val source = FakeSource().apply { prepareFails = true }
        val t = tokens(source)
        repeat(5) { assertNull(t.tokenFor("p")) }
        assertEquals(1, source.prepares, "prepare was retried inside the throttle window")
        now += 15_000
        source.prepareFails = false
        assertEquals("token", t.tokenFor("p"))
        assertEquals(2, source.prepares)
    }

    @Test
    @DisplayName("warmUp prepares ahead of the first request")
    fun warmUp() {
        val source = FakeSource()
        val t = tokens(source)
        t.warmUp()
        t.warmUp()
        assertEquals(1, source.prepares)
        t.tokenFor("p")
        assertEquals(1, source.prepares)
    }
}
