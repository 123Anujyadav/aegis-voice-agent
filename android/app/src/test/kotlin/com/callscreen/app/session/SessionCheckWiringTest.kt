package com.callscreen.app.session

import com.callscreen.app.session.SessionCheck.Outcome
import com.callscreen.core.auth.AccessTokenProofs
import com.callscreen.core.auth.EdgeApiClient
import com.callscreen.core.auth.EnrolOutcome
import com.callscreen.core.auth.IdentityAuthenticator
import com.callscreen.core.auth.SessionEndCause
import com.callscreen.core.auth.SessionState
import com.callscreen.core.datastore.session.BlobStore
import com.callscreen.core.datastore.session.EncryptedSessionStore
import com.callscreen.core.datastore.session.SessionKey
import com.callscreen.core.datastore.session.StoredSessionResult
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

/**
 * The session check over the real wiring: the real IdentityAuthenticator and
 * EdgeApiClient, their real HTTP clients against local fakes of identity and
 * edge-api, and the real EncryptedSessionStore over an in-memory key and byte
 * store. Nothing here reaches a real server.
 */
@DisplayName("Session check against identity and edge-api")
class SessionCheckWiringTest {

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

    private class MemoryRecord(var at: Long?) : CheckRecord {
        override fun last(): Long? = at
        override fun record(atMillis: Long) { at = atMillis }
    }

    private lateinit var identity: MockWebServer
    private lateinit var edge: MockWebServer
    private val store = EncryptedSessionStore(MemoryKey(), MemoryBlobs())
    private var nowSeconds = 1_760_000_000L
    private lateinit var auth: IdentityAuthenticator
    private lateinit var check: SessionCheck

    private val rt = "1".repeat(64)

    @BeforeEach
    fun start() {
        identity = MockWebServer().apply { start(InetAddress.getByName("127.0.0.1"), 0) }
        edge = MockWebServer().apply { start(InetAddress.getByName("127.0.0.1"), 0) }
        val http = IdentityClient.defaultHttpClient().newBuilder().readTimeout(500, TimeUnit.MILLISECONDS).build()
        val client = IdentityClient(identity.url("/").toString(), DeviceProofs { _, _ -> "proof" }, httpClient = http)
        auth = IdentityAuthenticator(client, store, nowSeconds = { nowSeconds }, io = Dispatchers.IO)
        val edgeClient = EdgeApiClient(edge.url("/").toString().trimEnd('/'), auth, AccessTokenProofs { _, _, _ -> "proof" }, http)
        // Due: the last check was more than an interval ago.
        check = SessionCheck(edgeClient::me, MemoryRecord(0L), nowMillis = { nowSeconds * 1000 })
        identity.enqueue(json(200, """{"accessToken":"access-1","tokenType":"DPoP","expiresIn":900,"refreshToken":"$rt","sessionId":"sess_${"b".repeat(64)}"}"""))
        assertEquals(EnrolOutcome.Enrolled, runBlocking { auth.enrol("req_" + "a".repeat(64), "123456") })
    }

    @AfterEach
    fun stop() {
        identity.shutdown()
        edge.shutdown()
    }

    private fun json(status: Int, body: String) =
        MockResponse().setResponseCode(status).setHeader("Content-Type", "application/json").setBody(body)

    private fun problem(status: Int, code: String) =
        MockResponse().setResponseCode(status).setHeader("Content-Type", "application/problem+json").setBody("""{"code":"$code"}""")

    private val me = json(200, """{"subject":"sub_1","deviceKeyThumbprint":"thumb"}""")
    private fun refreshed() = json(200, """{"accessToken":"access-2","tokenType":"DPoP","expiresIn":900,"refreshToken":"${"2".repeat(64)}"}""")
    private fun stored() = runBlocking { store.read() }

    private fun assertKept(outcome: Outcome) {
        assertEquals(Outcome.KEPT, outcome)
        assertTrue(stored() is StoredSessionResult.Present, "a temporary failure cleared the session")
        assertEquals(SessionState.SignedIn, auth.sessionState.value)
    }

    @Test
    @DisplayName("a live session is confirmed by edge-api")
    fun confirmed() = runBlocking {
        edge.enqueue(me)
        assertEquals(Outcome.CONFIRMED, check.runIfDue())
        assertEquals("/v1/me", edge.takeRequest().path)
        assertEquals(1, identity.requestCount, "a fresh token was refreshed")
        assertEquals(SessionState.SignedIn, auth.sessionState.value)
    }

    @Test
    @DisplayName("an expired token refreshed with identity, then confirmed")
    fun refreshedThenConfirmed() = runBlocking {
        nowSeconds += 3_600
        identity.enqueue(refreshed())
        edge.enqueue(me)
        assertEquals(Outcome.CONFIRMED, check.runIfDue())
        identity.takeRequest() // the enrolment
        assertEquals("/v1/token/refresh", identity.takeRequest().path)
    }

    @Test
    @DisplayName("identity says the session ended: it is cleared and the app is signed out")
    fun endedByIdentity() = runBlocking {
        nowSeconds += 3_600
        identity.enqueue(problem(401, "ERR_INVALID_TOKEN"))
        assertEquals(Outcome.ENDED, check.runIfDue())
        assertEquals(StoredSessionResult.Absent, stored())
        assertEquals(SessionState.SignedOut(SessionEndCause.SESSION_ENDED), auth.sessionState.value)
        assertEquals(0, edge.requestCount)
    }

    @Test
    @DisplayName("edge-api refuses the token, and identity then says the session ended")
    fun rejectedByEdgeThenEndedByIdentity() = runBlocking {
        edge.enqueue(problem(401, "ERR_INVALID_TOKEN"))
        identity.enqueue(problem(401, "ERR_INVALID_TOKEN"))
        assertEquals(Outcome.ENDED, check.runIfDue())
        assertEquals(SessionState.SignedOut(SessionEndCause.SESSION_ENDED), auth.sessionState.value)
    }

    @Test
    @DisplayName("edge-api refuses the token, identity refreshes it, and edge-api accepts the new one")
    fun rejectedByEdgeThenRefreshed() = runBlocking {
        edge.enqueue(problem(401, "ERR_INVALID_TOKEN"))
        identity.enqueue(refreshed())
        edge.enqueue(me)
        assertEquals(Outcome.CONFIRMED, check.runIfDue())
        assertEquals(SessionState.SignedIn, auth.sessionState.value)
    }

    @Test
    @DisplayName("edge-api failures never sign out: 503, 500, 502, a timeout, a refused proof, a refused refreshed token")
    fun edgeFailuresKeepTheSession() {
        val responses = listOf(
            problem(503, "ERR_TEMPORARILY_UNAVAILABLE"),
            MockResponse().setResponseCode(500),
            MockResponse().setResponseCode(502).setBody("<html>bad gateway</html>"),
            json(200, """{"subject":"sub_1","deviceKeyThumbprint":"thumb"}""").setBodyDelay(2, TimeUnit.SECONDS),
            problem(401, "ERR_INVALID_DPOP_PROOF"),
        )
        for (response in responses) {
            edge.enqueue(response)
            assertKept(runBlocking { due().runIfDue() })
        }
        // A token identity has just refreshed, refused again by edge-api, is not identity ending the session.
        edge.enqueue(problem(401, "ERR_INVALID_TOKEN"))
        identity.enqueue(refreshed())
        edge.enqueue(problem(401, "ERR_INVALID_TOKEN"))
        assertKept(runBlocking { due().runIfDue() })
        assertEquals(2, identity.requestCount, "expected the enrolment and exactly one refresh")
    }

    @Test
    @DisplayName("identity failures during the refresh never sign out: 503, 500, a timeout")
    fun identityFailuresKeepTheSession() {
        val responses = listOf(
            problem(503, "ERR_TEMPORARILY_UNAVAILABLE"),
            MockResponse().setResponseCode(500),
            json(200, "{}").setBodyDelay(2, TimeUnit.SECONDS),
        )
        nowSeconds += 3_600
        for (response in responses) {
            identity.enqueue(response)
            assertKept(runBlocking { due().runIfDue() })
        }
        assertEquals(0, edge.requestCount)
    }

    /** A check that is due now: the last one was long ago. */
    private fun due() = SessionCheck({ edgeClient().me() }, MemoryRecord(0L), nowMillis = { nowSeconds * 1000 })

    private fun edgeClient(): EdgeApiClient {
        val http = IdentityClient.defaultHttpClient().newBuilder().readTimeout(500, TimeUnit.MILLISECONDS).build()
        return EdgeApiClient(edge.url("/").toString().trimEnd('/'), auth, AccessTokenProofs { _, _, _ -> "proof" }, http)
    }
}
