package com.callscreen.core.auth

import com.callscreen.core.datastore.session.BlobStore
import com.callscreen.core.datastore.session.EncryptedSessionStore
import com.callscreen.core.datastore.session.SessionKey
import com.callscreen.core.datastore.session.StoredSession
import com.callscreen.core.network.identity.DeviceProofs
import com.callscreen.core.network.identity.IdentityClient
import java.net.InetAddress
import java.util.concurrent.TimeUnit
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.runBlocking
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.assertThrows

/**
 * The edge-api client against local fakes of identity and edge-api, with the
 * real IdentityAuthenticator and EncryptedSessionStore. Nothing reaches a real
 * server.
 */
@DisplayName("Edge API client")
class EdgeApiClientTest {

    private class MemoryBlobs : BlobStore {
        var bytes: ByteArray? = null
        override suspend fun read() = bytes
        override suspend fun write(bytes: ByteArray) { this.bytes = bytes.copyOf() }
        override suspend fun clear() { bytes = null }
    }

    private class MemoryKey : SessionKey {
        var key: SecretKey? = null
        override fun getOrCreate(): SecretKey = key ?: KeyGenerator.getInstance("AES").apply { init(256) }.generateKey().also { key = it }
        override fun existing(): SecretKey? = key
        override fun delete() { key = null }
    }

    private lateinit var identity: MockWebServer
    private lateinit var edge: MockWebServer
    private val store = EncryptedSessionStore(MemoryKey(), MemoryBlobs())
    private val now = 1_760_000_000L
    private val sessionId = "sess_" + "b".repeat(64)
    private val signed = mutableListOf<Triple<String, String, String>>()

    @BeforeEach
    fun start() {
        identity = MockWebServer().apply { start(InetAddress.getByName("127.0.0.1"), 0) }
        edge = MockWebServer().apply { start(InetAddress.getByName("127.0.0.1"), 0) }
    }

    @AfterEach
    fun stop() {
        identity.shutdown()
        edge.shutdown()
    }

    private fun http() = IdentityClient.defaultHttpClient().newBuilder().readTimeout(500, TimeUnit.MILLISECONDS).build()

    private fun authenticator() = IdentityAuthenticator(
        IdentityClient(identity.url("/").toString(), DeviceProofs { _, _ -> "identity-proof" }, httpClient = http()),
        store,
        nowSeconds = { now },
        io = Dispatchers.IO,
    )

    private fun edgeOrigin() = edge.url("/").toString().trimEnd('/')

    private fun client(auth: IdentityAuthenticator = authenticator()) = EdgeApiClient(
        edgeOrigin(),
        auth,
        AccessTokenProofs { method, htu, token -> signed += Triple(method, htu, token); "edge-proof-${signed.size}" },
        http(),
    )

    private fun signedIn(access: String = "access-1") = runBlocking {
        store.write(StoredSession(sessionId, access, "1".repeat(64), now + 900))
    }

    private fun me() = MockResponse().setResponseCode(200).setHeader("Content-Type", "application/json")
        .setBody("""{"subject":"usr_1","deviceKeyThumbprint":"jkt_1"}""")

    private fun problem(status: Int, code: String) = MockResponse().setResponseCode(status)
        .setHeader("Content-Type", "application/problem+json").setHeader("WWW-Authenticate", "DPoP").setBody("""{"code":"$code"}""")

    private fun refreshed(access: String) = MockResponse().setResponseCode(200).setHeader("Content-Type", "application/json")
        .setBody("""{"accessToken":"$access","tokenType":"DPoP","expiresIn":900,"refreshToken":"${"2".repeat(64)}"}""")

    @Test
    @DisplayName("GET /v1/me carries DPoP <token> and a proof for GET, the configured origin + path, and that token")
    fun authenticatedRequest() = runBlocking {
        signedIn()
        edge.enqueue(me())
        assertEquals(EdgeResult.Success(EdgeIdentity("usr_1", "jkt_1")), client().me())
        val sent = edge.takeRequest()
        assertEquals("GET", sent.method)
        assertEquals("/v1/me", sent.path)
        assertEquals("DPoP access-1", sent.getHeader("Authorization"))
        assertEquals("edge-proof-1", sent.getHeader("DPoP"))
        assertEquals(listOf(Triple("GET", edgeOrigin() + "/v1/me", "access-1")), signed)
        assertEquals(0, identity.requestCount)
    }

    @Test
    @DisplayName("with no session, nothing is sent anywhere")
    fun signedOut() = runBlocking {
        assertEquals(EdgeResult.Failure(EdgeError.SignedOut(SignedOutReason.NOT_SIGNED_IN)), client().me())
        assertEquals(0, edge.requestCount)
        assertTrue(signed.isEmpty())
    }

    @Test
    @DisplayName("an invalid token is refreshed once and the request retried with a fresh proof")
    fun refreshOnceOnInvalidToken() = runBlocking {
        signedIn()
        edge.enqueue(problem(401, "ERR_INVALID_TOKEN"))
        identity.enqueue(refreshed("access-2"))
        edge.enqueue(me())
        assertEquals(EdgeResult.Success(EdgeIdentity("usr_1", "jkt_1")), client().me())
        assertEquals(1, identity.requestCount)
        assertEquals(2, edge.requestCount)
        edge.takeRequest()
        assertEquals("DPoP access-2", edge.takeRequest().getHeader("Authorization"))
        assertEquals(listOf("access-1", "access-2"), signed.map { it.third })
    }

    @Test
    @DisplayName("a token refused again after the refresh is TokenRejected: no loop")
    fun noRetryLoop() = runBlocking {
        signedIn()
        edge.enqueue(problem(401, "ERR_INVALID_TOKEN"))
        identity.enqueue(refreshed("access-2"))
        edge.enqueue(problem(401, "ERR_INVALID_TOKEN"))
        assertEquals(EdgeResult.Failure(EdgeError.TokenRejected), client().me())
        assertEquals(1, identity.requestCount)
        assertEquals(2, edge.requestCount)
    }

    @Test
    @DisplayName("a session the refresh finds ended is SignedOut")
    fun refreshEndsTheSession() = runBlocking {
        signedIn()
        edge.enqueue(problem(401, "ERR_INVALID_TOKEN"))
        identity.enqueue(problem(401, "ERR_INVALID_TOKEN"))
        assertEquals(EdgeResult.Failure(EdgeError.SignedOut(SignedOutReason.SESSION_ENDED)), client().me())
        assertEquals(1, edge.requestCount)
    }

    @Test
    @DisplayName("a refused proof is not retried and triggers no refresh")
    fun proofRejected() = runBlocking {
        signedIn()
        edge.enqueue(problem(401, "ERR_INVALID_DPOP_PROOF"))
        assertEquals(EdgeResult.Failure(EdgeError.ProofRejected), client().me())
        assertEquals(1, edge.requestCount)
        assertEquals(0, identity.requestCount)
    }

    @Test
    @DisplayName("503, a timeout, a malformed answer and a redirect fail safely")
    fun safeFailures() = runBlocking {
        signedIn()
        val elsewhere = MockWebServer().apply { start(InetAddress.getByName("127.0.0.1"), 0) }
        try {
            val cases = listOf(
                problem(503, "ERR_TEMPORARILY_UNAVAILABLE") to EdgeError.TemporarilyUnavailable,
                me().setBodyDelay(2, TimeUnit.SECONDS) to EdgeError.Network,
                MockResponse().setResponseCode(200).setHeader("Content-Type", "application/json").setBody("{broken") to EdgeError.UnexpectedResponse(200),
                MockResponse().setResponseCode(200).setHeader("Content-Type", "application/json").setBody("""{"subject":""}""") to EdgeError.UnexpectedResponse(200),
                MockResponse().setResponseCode(307).setHeader("Location", elsewhere.url("/v1/me").toString()) to EdgeError.UnexpectedResponse(307),
                problem(401, "ERR_UNAUTHENTICATED") to EdgeError.TokenRejected,
            )
            for ((response, want) in cases) {
                edge.enqueue(response)
                assertEquals(EdgeResult.Failure(want), client().me())
            }
            assertEquals(0, elsewhere.requestCount, "the redirect was followed with credentials")
            assertEquals(0, identity.requestCount)
        } finally {
            elsewhere.shutdown()
        }
    }

    @Test
    @DisplayName("refreshAfterRejection does not refresh a token another caller already rotated")
    fun rejectionOfAnAlreadyRotatedToken() = runBlocking {
        signedIn(access = "access-current")
        val state = authenticator().refreshAfterRejection("access-old")
        assertTrue(state is AuthState.Authenticated && state.accessToken == "access-current", "$state")
        assertEquals(0, identity.requestCount)
    }

    @Test
    @DisplayName("the origin must be an https origin (plain http only for loopback)")
    fun originRules() {
        val auth = authenticator()
        val proofs = AccessTokenProofs { _, _, _ -> "p" }
        assertThrows<IllegalArgumentException> { EdgeApiClient("http://edge.example.test", auth, proofs) }
        assertThrows<IllegalArgumentException> { EdgeApiClient("https://edge.example.test/api", auth, proofs) }
        EdgeApiClient("https://edge.example.test", auth, proofs)
    }
}
