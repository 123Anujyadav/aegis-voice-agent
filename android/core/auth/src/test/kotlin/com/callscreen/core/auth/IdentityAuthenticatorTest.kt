package com.callscreen.core.auth

import com.callscreen.core.datastore.session.BlobStore
import com.callscreen.core.datastore.session.EncryptedSessionStore
import com.callscreen.core.datastore.session.SessionKey
import com.callscreen.core.datastore.session.StoredSession
import com.callscreen.core.datastore.session.StoredSessionResult
import com.callscreen.core.network.identity.DeviceProofs
import com.callscreen.core.network.identity.IdentityClient
import com.callscreen.core.network.identity.IdentityError
import java.net.InetAddress
import java.util.concurrent.TimeUnit
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.runBlocking
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

/**
 * The real IdentityClient against a local fake of identity, and the real
 * EncryptedSessionStore over an in-memory key and byte store. Nothing here
 * reaches a real server.
 */
@DisplayName("Identity authenticator")
class IdentityAuthenticatorTest {

    private class MemoryBlobs : BlobStore {
        var bytes: ByteArray? = null
        var failWrites = false
        override suspend fun read() = bytes
        override suspend fun write(bytes: ByteArray) {
            check(!failWrites) { "disk full" }
            this.bytes = bytes.copyOf()
        }
        override suspend fun clear() { bytes = null }
    }

    private class MemoryKey : SessionKey {
        var key: SecretKey? = null
        override fun getOrCreate(): SecretKey = key ?: KeyGenerator.getInstance("AES").apply { init(256) }.generateKey().also { key = it }
        override fun existing(): SecretKey? = key
        override fun delete() { key = null }
    }

    private lateinit var server: MockWebServer
    private val blobs = MemoryBlobs()
    private val store = EncryptedSessionStore(MemoryKey(), blobs)
    private var now = 1_760_000_000L

    private val requestId = "req_" + "a".repeat(64)
    private val sessionId = "sess_" + "b".repeat(64)
    private val rt1 = "1".repeat(64)
    private val rt2 = "2".repeat(64)

    @BeforeEach
    fun start() {
        server = MockWebServer().apply { start(InetAddress.getByName("127.0.0.1"), 0) }
    }

    @AfterEach
    fun stop() = server.shutdown()

    private fun authenticator(): IdentityAuthenticator {
        val http = IdentityClient.defaultHttpClient().newBuilder().readTimeout(500, TimeUnit.MILLISECONDS).build()
        val client = IdentityClient(server.url("/").toString(), DeviceProofs { _, _ -> "proof" }, httpClient = http)
        return IdentityAuthenticator(client, store, nowSeconds = { now }, io = Dispatchers.IO)
    }

    private fun json(status: Int, body: String) =
        MockResponse().setResponseCode(status).setHeader("Content-Type", "application/json").setBody(body)

    private fun problem(status: Int, code: String) =
        MockResponse().setResponseCode(status).setHeader("Content-Type", "application/problem+json").setBody("""{"code":"$code"}""")

    private fun enrolled(access: String = "access-1", refresh: String = rt1) =
        json(200, """{"accessToken":"$access","tokenType":"DPoP","expiresIn":900,"refreshToken":"$refresh","sessionId":"$sessionId"}""")

    private fun refreshed(access: String = "access-2", refresh: String = rt2) =
        json(200, """{"accessToken":"$access","tokenType":"DPoP","expiresIn":900,"refreshToken":"$refresh"}""")

    private suspend fun stored() = store.read()

    @Test
    @DisplayName("enrolment stores the session, and a fresh token is served without a network call")
    fun enrolThenToken() = runBlocking {
        server.enqueue(enrolled())
        val auth = authenticator()
        assertEquals(EnrolOutcome.Enrolled, auth.enrol(requestId, "123456"))
        assertEquals(StoredSessionResult.Present(StoredSession(sessionId, "access-1", rt1, now + 900)), stored())
        val state = auth.accessToken()
        assertTrue(state is AuthState.Authenticated && state.accessToken == "access-1", "$state")
        assertEquals(1, server.requestCount, "a fresh token caused a network call")
    }

    @Test
    @DisplayName("a wrong code, a server rejection, a network failure or a malformed answer stores nothing")
    fun enrolFailures() = runBlocking {
        val auth = authenticator()
        server.enqueue(problem(401, "ERR_INVALID_OTP"))
        assertEquals(EnrolOutcome.Failed(IdentityError.InvalidOtp), auth.enrol(requestId, "123456"))
        server.enqueue(problem(429, "ERR_RATE_LIMITED"))
        assertEquals(EnrolOutcome.Failed(IdentityError.RateLimited(null)), auth.enrol(requestId, "123456"))
        server.enqueue(json(200, """{"accessToken":"x"}"""))
        assertEquals(EnrolOutcome.Failed(IdentityError.UnexpectedResponse(200)), auth.enrol(requestId, "123456"))
        server.enqueue(enrolled().setBodyDelay(2, TimeUnit.SECONDS))
        assertEquals(EnrolOutcome.Failed(IdentityError.Network), auth.enrol(requestId, "123456"))
        assertEquals(StoredSessionResult.Absent, stored())
    }

    @Test
    @DisplayName("tokens that cannot be stored are StorageFailed, and nothing is kept")
    fun enrolStorageFailure() = runBlocking {
        server.enqueue(enrolled())
        blobs.failWrites = true
        assertEquals(EnrolOutcome.StorageFailed, authenticator().enrol(requestId, "123456"))
        blobs.failWrites = false
        assertEquals(StoredSessionResult.Absent, stored())
    }

    @Test
    @DisplayName("an expiring token is refreshed with the stored session, and the rotated tokens are stored")
    fun refreshesNearExpiry() = runBlocking {
        server.enqueue(enrolled())
        val auth = authenticator()
        auth.enrol(requestId, "123456")
        now += 900 - IdentityAuthenticator.REFRESH_MARGIN_SECONDS
        server.enqueue(refreshed())
        val state = auth.accessToken()
        assertTrue(state is AuthState.Authenticated && state.accessToken == "access-2", "$state")
        server.takeRequest()
        val sent = server.takeRequest()
        assertEquals("/v1/token/refresh", sent.path)
        assertEquals("""{"sessionId":"$sessionId","refreshToken":"$rt1"}""", sent.body.readUtf8())
        assertEquals(StoredSessionResult.Present(StoredSession(sessionId, "access-2", rt2, now + 900)), stored())
    }

    @Test
    @DisplayName("concurrent callers share ONE refresh: a second would present a superseded token, which is reuse")
    fun refreshIsSingleFlight() = runBlocking {
        server.enqueue(enrolled())
        val auth = authenticator()
        auth.enrol(requestId, "123456")
        now += 10_000
        server.enqueue(refreshed())
        val states = (1..8).map { async(Dispatchers.Default) { auth.accessToken() } }.awaitAll()
        assertTrue(states.all { it is AuthState.Authenticated && it.accessToken == "access-2" }, "$states")
        assertEquals(2, server.requestCount, "expected one enrolment and exactly one refresh")
    }

    @Test
    @DisplayName("a session the server ended is cleared: SignedOut, sign in again")
    fun sessionEnded() = runBlocking {
        for (code in listOf("ERR_INVALID_TOKEN", "ERR_INVALID_DPOP_PROOF")) {
            server.enqueue(enrolled())
            val auth = authenticator()
            auth.enrol(requestId, "123456")
            now += 10_000
            server.enqueue(problem(if (code == "ERR_INVALID_TOKEN") 401 else 400, code))
            assertEquals(AuthState.SignedOut(SignedOutReason.SESSION_ENDED), auth.accessToken(), code)
            assertEquals(StoredSessionResult.Absent, stored(), code)
        }
    }

    @Test
    @DisplayName("a transient refresh failure keeps the session: the refresh token was not spent")
    fun transientRefreshFailureKeepsTheSession() = runBlocking {
        server.enqueue(enrolled())
        val auth = authenticator()
        auth.enrol(requestId, "123456")
        now += 10_000
        val transient = listOf(
            refreshed().setBodyDelay(2, TimeUnit.SECONDS) to IdentityError.Network,
            problem(503, "ERR_TEMPORARILY_UNAVAILABLE") to IdentityError.TemporarilyUnavailable,
            json(200, "{broken") to IdentityError.UnexpectedResponse(200),
        )
        for ((response, error) in transient) {
            server.enqueue(response)
            assertEquals(AuthState.Unavailable(error), auth.accessToken())
            assertTrue(stored() is StoredSessionResult.Present, "a transient failure cleared the session")
        }
        server.enqueue(refreshed())
        assertTrue(auth.accessToken() is AuthState.Authenticated, "the kept session did not refresh later")
    }

    @Test
    @DisplayName("rotated tokens that cannot be stored end the session rather than keep a superseded token")
    fun refreshStorageFailure() = runBlocking {
        server.enqueue(enrolled())
        val auth = authenticator()
        auth.enrol(requestId, "123456")
        now += 10_000
        server.enqueue(refreshed())
        blobs.failWrites = true
        assertEquals(AuthState.SignedOut(SignedOutReason.STORAGE_FAILED), auth.accessToken())
        blobs.failWrites = false
        assertEquals(StoredSessionResult.Absent, stored())
    }

    @Test
    @DisplayName("no session or an unrecoverable one is SignedOut with no network call")
    fun signedOutWithoutNetwork() = runBlocking {
        val auth = authenticator()
        assertEquals(AuthState.SignedOut(SignedOutReason.NOT_SIGNED_IN), auth.accessToken())
        server.enqueue(enrolled())
        auth.enrol(requestId, "123456")
        blobs.bytes!!.let { it[it.size - 1] = (it[it.size - 1].toInt() xor 0x01).toByte() }
        assertEquals(AuthState.SignedOut(SignedOutReason.STORED_SESSION_UNRECOVERABLE), auth.accessToken())
        assertEquals(1, server.requestCount)
    }

    @Test
    @DisplayName("signing out removes the session; tokens never appear in toString")
    fun signOutAndRedaction() = runBlocking {
        server.enqueue(enrolled(access = "secret-access"))
        val auth = authenticator()
        auth.enrol(requestId, "123456")
        val state = auth.accessToken()
        assertFalse(state.toString().contains("secret-access"), state.toString())
        auth.signOut()
        assertEquals(AuthState.SignedOut(SignedOutReason.NOT_SIGNED_IN), auth.accessToken())
    }
}
