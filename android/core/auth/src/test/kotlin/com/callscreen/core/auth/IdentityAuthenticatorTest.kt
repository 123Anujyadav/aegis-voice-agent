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
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
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

    private suspend fun signedIn(): IdentityAuthenticator {
        server.enqueue(enrolled())
        return authenticator().also { it.enrol(requestId, "123456"); server.takeRequest() }
    }

    @Test
    @DisplayName("account deletion presents the stored session and clears it only after the server's 204")
    fun deleteAccount() = runBlocking {
        val auth = signedIn()
        assertTrue(auth.isSignedIn())
        server.enqueue(MockResponse().setResponseCode(204))
        assertEquals(AccountDeletionOutcome.Deleted, auth.deleteAccount())
        val sent = server.takeRequest()
        assertEquals("/v1/account/delete", sent.path)
        assertEquals("""{"sessionId":"$sessionId","refreshToken":"$rt1"}""", sent.body.readUtf8())
        assertEquals(StoredSessionResult.Absent, stored())
        assertFalse(auth.isSignedIn())
        assertEquals(AuthState.SignedOut(SignedOutReason.NOT_SIGNED_IN), auth.accessToken())
    }

    @Test
    @DisplayName("account deletion without a confirmation keeps the session, so a retry is possible")
    fun deleteAccountFailureKeepsTheSession() = runBlocking {
        val auth = signedIn()
        val failures = listOf(
            MockResponse().setResponseCode(204).setHeadersDelay(2, TimeUnit.SECONDS) to IdentityError.Network,
            problem(503, "ERR_TEMPORARILY_UNAVAILABLE") to IdentityError.TemporarilyUnavailable,
            problem(400, "ERR_INVALID_DPOP_PROOF") to IdentityError.InvalidDeviceProof,
            json(200, "{}") to IdentityError.UnexpectedResponse(200),
        )
        for ((response, error) in failures) {
            server.enqueue(response)
            assertEquals(AccountDeletionOutcome.Failed(error), auth.deleteAccount(), "$error")
            assertTrue(stored() is StoredSessionResult.Present, "$error cleared the session")
        }
        // The retry presents the same, still current, refresh token.
        server.enqueue(MockResponse().setResponseCode(204))
        assertEquals(AccountDeletionOutcome.Deleted, auth.deleteAccount())
        repeat(failures.size) { server.takeRequest() }
        assertEquals("""{"sessionId":"$sessionId","refreshToken":"$rt1"}""", server.takeRequest().body.readUtf8())
    }

    @Test
    @DisplayName("a session the server refuses is cleared: SessionEnded; with no session nothing is sent")
    fun deleteAccountSessionEndedAndSignedOut() = runBlocking {
        val auth = signedIn()
        server.enqueue(problem(401, "ERR_INVALID_TOKEN"))
        assertEquals(AccountDeletionOutcome.SessionEnded, auth.deleteAccount())
        assertEquals(StoredSessionResult.Absent, stored())

        val before = server.requestCount
        assertEquals(AccountDeletionOutcome.NotSignedIn, auth.deleteAccount())
        assertFalse(auth.isSignedIn())
        assertEquals(before, server.requestCount)
    }

    @Test
    @DisplayName("deletion and refresh share the lock: deletion never presents a token a concurrent refresh spent")
    fun deletionIsSerialisedWithRefresh() = runBlocking {
        val auth = signedIn()
        now += 10_000
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse =
                if (request.path == "/v1/token/refresh") refreshed() else MockResponse().setResponseCode(204)
        }
        val refresh = async(Dispatchers.Default) { auth.accessToken() }
        val deletion = async(Dispatchers.Default) { auth.deleteAccount() }
        refresh.await()
        assertEquals(AccountDeletionOutcome.Deleted, deletion.await())
        val sent = (1 until server.requestCount).map { server.takeRequest() }
        val presented = sent.single { it.path == "/v1/account/delete" }.body.readUtf8()
        // Refresh first: deletion presents the rotated token. Deletion first: refresh finds no session and sends nothing.
        val expected = if (sent.any { it.path == "/v1/token/refresh" }) rt2 else rt1
        assertTrue(presented.contains(expected), presented)
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

    private fun out(cause: SessionEndCause) = SessionState.SignedOut(cause)

    @Test
    @DisplayName("session state is Unknown until the store is read, then follows every read, write and clear")
    fun sessionStateFollowsTheStore() = runBlocking {
        val auth = authenticator()
        assertEquals(SessionState.Unknown, auth.sessionState.value)
        assertFalse(auth.isSignedIn())
        assertEquals(out(SessionEndCause.NOT_SIGNED_IN), auth.sessionState.value)
        server.enqueue(enrolled())
        auth.enrol(requestId, "123456")
        assertEquals(SessionState.SignedIn, auth.sessionState.value)
        assertTrue(auth.isSignedIn())
        assertEquals(SessionState.SignedIn, auth.sessionState.value)
        auth.signOut()
        assertEquals(out(SessionEndCause.SIGNED_OUT_ON_THIS_DEVICE), auth.sessionState.value)
        assertEquals(1, server.requestCount, "observing the session sent something")
    }

    @Test
    @DisplayName("a wrong code, or a transient refresh failure, leaves the session state as it was")
    fun sessionStateUnchangedByFailuresThatKeepTheStore() = runBlocking {
        val auth = authenticator()
        server.enqueue(problem(401, "ERR_INVALID_OTP"))
        auth.enrol(requestId, "123456")
        assertEquals(SessionState.Unknown, auth.sessionState.value)
        server.enqueue(enrolled())
        auth.enrol(requestId, "123456")
        now += 10_000
        server.enqueue(problem(503, "ERR_TEMPORARILY_UNAVAILABLE"))
        assertEquals(AuthState.Unavailable(IdentityError.TemporarilyUnavailable), auth.accessToken())
        assertEquals(SessionState.SignedIn, auth.sessionState.value)
    }

    @Test
    @DisplayName("an ended, unrecoverable or unstorable session is SignedOut with its cause")
    fun sessionStateSignedOutCauses() = runBlocking {
        val ended = authenticator()
        server.enqueue(enrolled())
        ended.enrol(requestId, "123456")
        now += 10_000
        server.enqueue(problem(401, "ERR_INVALID_TOKEN"))
        ended.accessToken()
        assertEquals(out(SessionEndCause.SESSION_ENDED), ended.sessionState.value)

        val corrupted = authenticator()
        server.enqueue(enrolled())
        corrupted.enrol(requestId, "123456")
        blobs.bytes!!.let { it[it.size - 1] = (it[it.size - 1].toInt() xor 0x01).toByte() }
        assertFalse(corrupted.isSignedIn())
        assertEquals(out(SessionEndCause.STORED_SESSION_UNRECOVERABLE), corrupted.sessionState.value)

        val unstorable = authenticator()
        server.enqueue(enrolled())
        blobs.failWrites = true
        assertEquals(EnrolOutcome.StorageFailed, unstorable.enrol(requestId, "123456"))
        assertEquals(out(SessionEndCause.STORAGE_FAILED), unstorable.sessionState.value)
        blobs.failWrites = false

        val unstorableRefresh = authenticator()
        server.enqueue(enrolled())
        unstorableRefresh.enrol(requestId, "123456")
        now += 10_000
        server.enqueue(refreshed())
        blobs.failWrites = true
        unstorableRefresh.accessToken()
        blobs.failWrites = false
        assertEquals(out(SessionEndCause.STORAGE_FAILED), unstorableRefresh.sessionState.value)
    }

    @Test
    @DisplayName("deletion: confirmed is ACCOUNT_DELETED, refused is SESSION_ENDED, unconfirmed stays SignedIn")
    fun sessionStateAfterDeletion() = runBlocking {
        val auth = signedIn()
        server.enqueue(problem(503, "ERR_TEMPORARILY_UNAVAILABLE"))
        assertEquals(AccountDeletionOutcome.Failed(IdentityError.TemporarilyUnavailable), auth.deleteAccount())
        assertEquals(SessionState.SignedIn, auth.sessionState.value)
        server.enqueue(MockResponse().setResponseCode(204))
        assertEquals(AccountDeletionOutcome.Deleted, auth.deleteAccount())
        assertEquals(out(SessionEndCause.ACCOUNT_DELETED), auth.sessionState.value)

        val refused = signedIn()
        server.enqueue(problem(401, "ERR_INVALID_TOKEN"))
        assertEquals(AccountDeletionOutcome.SessionEnded, refused.deleteAccount())
        assertEquals(out(SessionEndCause.SESSION_ENDED), refused.sessionState.value)
    }
}
