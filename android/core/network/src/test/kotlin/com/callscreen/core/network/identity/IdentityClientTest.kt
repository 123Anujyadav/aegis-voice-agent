package com.callscreen.core.network.identity

import java.net.InetAddress
import java.util.concurrent.TimeUnit
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertNull
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.assertThrows

/**
 * The client against a local fake of identity's HTTP API. Nothing here
 * reaches a real server; the request and response shapes are the ones
 * services/go/identity/internal/httpapi documents.
 */
@DisplayName("Identity HTTP client")
class IdentityClientTest {

    private lateinit var server: MockWebServer
    private val proofs = mutableListOf<Pair<String, String>>()
    private val proofSource = DeviceProofs { method, htu -> proofs += method to htu; "proof-${proofs.size}" }

    private val requestId = "req_" + "a".repeat(64)
    private val sessionId = "sess_" + "b".repeat(64)
    private val refreshToken = "c".repeat(64)

    @BeforeEach
    fun start() {
        server = MockWebServer()
        server.start(InetAddress.getByName("127.0.0.1"), 0)
    }

    @AfterEach
    fun stop() = server.shutdown()

    private fun origin() = server.url("/").toString().trimEnd('/')

    private fun client(integrity: IntegrityTokens = IntegrityTokens { null }, http: OkHttpClient = IdentityClient.defaultHttpClient()) =
        IdentityClient(origin(), proofSource, integrity, http)

    private fun json(status: Int, body: String) =
        MockResponse().setResponseCode(status).setHeader("Content-Type", "application/json").setBody(body)

    private fun problem(status: Int, code: String) =
        MockResponse().setResponseCode(status).setHeader("Content-Type", "application/problem+json")
            .setBody("""{"type":"https://api.aegisai.in/errors/$code","title":"t","status":$status,"detail":"d","code":"$code"}""")

    private val enrolled =
        """{"accessToken":"eyJ.access.token","tokenType":"DPoP","expiresIn":900,"refreshToken":"$refreshToken","sessionId":"$sessionId"}"""

    @Test
    @DisplayName("requestOtp sends the documented body, no proof, and maps the 202")
    fun requestOtp() {
        server.enqueue(json(202, """{"requestId":"$requestId","expiresInSeconds":300}"""))
        val result = client().requestOtp("+919876543210")
        assertEquals(IdentityResult.Success(OtpChallenge(requestId, 300)), result)
        val sent = server.takeRequest()
        assertEquals("POST", sent.method)
        assertEquals("/v1/otp/request", sent.path)
        assertEquals("""{"msisdn":"+919876543210"}""", sent.body.readUtf8())
        assertTrue(sent.getHeader("Content-Type")!!.startsWith("application/json"))
        assertNull(sent.getHeader("DPoP"))
        assertTrue(proofs.isEmpty())
    }

    @Test
    @DisplayName("enrol signs a proof for POST and the route's URL, and sends it with the integrity token bound to it")
    fun enrolWithIntegrity() {
        server.enqueue(json(200, enrolled))
        var boundTo: String? = null
        val result = client(integrity = { proof -> boundTo = proof; "integrity-token" }).enrol(requestId, "123456")
        val tokens = (result as IdentityResult.Success).value
        assertEquals("eyJ.access.token", tokens.accessToken)
        assertEquals(refreshToken, tokens.refreshToken)
        assertEquals(900L, tokens.expiresInSeconds)
        assertEquals(sessionId, tokens.sessionId)

        val sent = server.takeRequest()
        assertEquals("/v1/otp/validate", sent.path)
        assertEquals(listOf("POST" to origin() + "/v1/otp/validate"), proofs)
        assertEquals("proof-1", sent.getHeader("DPoP"))
        assertEquals("proof-1", boundTo, "the integrity token must be bound to the proof sent")
        assertEquals("integrity-token", sent.getHeader("Integrity-Token"))
        assertEquals("""{"requestId":"$requestId","code":"123456"}""", sent.body.readUtf8())
    }

    @Test
    @DisplayName("without an integrity token, no Integrity-Token header is sent")
    fun enrolWithoutIntegrity() {
        server.enqueue(json(200, enrolled))
        client().enrol(requestId, "123456")
        assertNull(server.takeRequest().getHeader("Integrity-Token"))
    }

    private val refreshed = """{"accessToken":"a.b.c","tokenType":"DPoP","expiresIn":900,"refreshToken":"${"d".repeat(64)}"}"""

    /** One refresh through [client]; the Integrity-Token header it sent, if any. */
    private fun refreshSends(client: IdentityClient): String? {
        server.enqueue(json(200, refreshed))
        client.refresh(sessionId, refreshToken)
        return server.takeRequest().getHeader("Integrity-Token")
    }

    @Test
    @DisplayName("on refresh an integrity token is requested at most once every 6 hours; at enrolment always (ADR-0031 D31.4)")
    fun refreshIntegrityInterval() {
        var now = 1_760_000_000L
        var asked = 0
        val client = IdentityClient(origin(), proofSource, { asked++; "integrity-token" }, nowSeconds = { now })

        assertEquals("integrity-token", refreshSends(client), "the first refresh in a process is due")
        now += 15 * 60
        assertNull(refreshSends(client), "15 minutes later: not due")
        now += IdentityClient.REFRESH_INTEGRITY_INTERVAL_SECONDS - 15 * 60 - 1
        assertNull(refreshSends(client), "one second short of 6 hours: not due")
        now += 1
        assertEquals("integrity-token", refreshSends(client), "6 hours after the last request: due")
        assertEquals(2, asked)

        // Enrolment always asks, and restarts the interval.
        now += 60
        server.enqueue(json(200, enrolled))
        client.enrol(requestId, "123456")
        assertEquals("integrity-token", server.takeRequest().getHeader("Integrity-Token"))
        now += 60
        assertNull(refreshSends(client), "a minute after enrolment: not due")
        assertEquals(3, asked)
    }

    @Test
    @DisplayName("a failed acquisition still counts, so a device without tokens does not ask Google on every refresh")
    fun failedIntegrityAttemptCounts() {
        var now = 1_760_000_000L
        var asked = 0
        val client = IdentityClient(origin(), proofSource, { asked++; null }, nowSeconds = { now })
        assertNull(refreshSends(client))
        now += 15 * 60
        assertNull(refreshSends(client))
        assertEquals(1, asked, "Google was asked again within the interval")
    }

    @Test
    @DisplayName("a clock that went backwards makes the token due rather than suppressing it")
    fun clockGoingBackwardsIsDue() {
        var now = 1_760_000_000L
        val client = IdentityClient(origin(), proofSource, { "integrity-token" }, nowSeconds = { now })
        assertEquals("integrity-token", refreshSends(client))
        now -= 60
        assertEquals("integrity-token", refreshSends(client))
    }

    @Test
    @DisplayName("refresh sends the session and refresh token with a proof for the refresh URL")
    fun refresh() {
        server.enqueue(json(200, """{"accessToken":"a.b.c","tokenType":"DPoP","expiresIn":900,"refreshToken":"${"d".repeat(64)}"}"""))
        val result = client().refresh(sessionId, refreshToken)
        val tokens = (result as IdentityResult.Success).value
        assertEquals("d".repeat(64), tokens.refreshToken)
        assertNull(tokens.sessionId)
        val sent = server.takeRequest()
        assertEquals("/v1/token/refresh", sent.path)
        assertEquals(listOf("POST" to origin() + "/v1/token/refresh"), proofs)
        assertEquals("""{"sessionId":"$sessionId","refreshToken":"$refreshToken"}""", sent.body.readUtf8())
    }

    @Test
    @DisplayName("every server problem code maps to its error; Retry-After is read")
    fun problemCodes() {
        val cases = listOf(
            problem(400, "ERR_INVALID_MSISDN") to IdentityError.InvalidPhoneNumber,
            problem(429, "ERR_RATE_LIMITED").setHeader("Retry-After", "42") to IdentityError.RateLimited(42),
            problem(503, "ERR_OTP_DELIVERY_UNAVAILABLE") to IdentityError.OtpDeliveryUnavailable,
            problem(503, "ERR_OTP_DELIVERY_FAILED") to IdentityError.OtpDeliveryFailed,
            problem(401, "ERR_INVALID_OTP") to IdentityError.InvalidOtp,
            problem(401, "ERR_OTP_EXPIRED") to IdentityError.OtpExpired,
            problem(401, "ERR_INVALID_TOKEN") to IdentityError.SessionEnded,
            problem(400, "ERR_INVALID_DPOP_PROOF") to IdentityError.InvalidDeviceProof,
            problem(400, "ERR_INVALID_INTEGRITY_TOKEN") to IdentityError.InvalidIntegrityToken,
            problem(503, "ERR_TEMPORARILY_UNAVAILABLE") to IdentityError.TemporarilyUnavailable,
            problem(400, "ERR_INVALID_INPUT") to IdentityError.InvalidInput,
            problem(500, "ERR_INTERNAL") to IdentityError.UnexpectedResponse(500),
            problem(418, "ERR_SOMETHING_NEW") to IdentityError.UnexpectedResponse(418),
        )
        for ((response, want) in cases) {
            server.enqueue(response)
            assertEquals(IdentityResult.Failure(want), client().enrol(requestId, "123456"))
        }
    }

    @Test
    @DisplayName("responses outside the contract are UnexpectedResponse, never a success")
    fun outsideTheContract() {
        val cases = listOf(
            json(200, "{not json"),
            json(200, """{"accessToken":"a"}"""), // missing fields
            json(200, enrolled.replace("\"DPoP\"", "\"Bearer\"")), // wrong token type
            json(200, enrolled.replace(sessionId, "sess_short")), // malformed session id
            json(200, enrolled.replace("\"expiresIn\":900", "\"expiresIn\":0")),
            json(201, enrolled), // an unexpected success status
            MockResponse().setResponseCode(200).setHeader("Content-Type", "text/html").setBody(enrolled),
            MockResponse().setResponseCode(502).setHeader("Content-Type", "text/html").setBody("<html>gateway</html>"),
            problem(400, "x").setBody("{broken"),
            json(200, "{\"pad\":\"" + "x".repeat(IdentityClient.MAX_RESPONSE_BYTES.toInt()) + "\"}"), // oversized
        )
        cases.forEachIndexed { i, response ->
            server.enqueue(response)
            val result = client().enrol(requestId, "123456")
            assertTrue(result is IdentityResult.Failure && result.error is IdentityError.UnexpectedResponse, "case $i: $result")
        }
    }

    @Test
    @DisplayName("a timeout is a Network failure")
    fun timeout() {
        server.enqueue(json(200, enrolled).setBodyDelay(2, TimeUnit.SECONDS))
        val impatient = IdentityClient.defaultHttpClient().newBuilder().readTimeout(200, TimeUnit.MILLISECONDS).build()
        assertEquals(IdentityResult.Failure(IdentityError.Network), client(http = impatient).enrol(requestId, "123456"))
    }

    @Test
    @DisplayName("a refused connection is a Network failure")
    fun connectionRefused() {
        val gone = MockWebServer().apply { start(InetAddress.getByName("127.0.0.1"), 0) }
        val url = gone.url("/").toString()
        gone.shutdown()
        val result = IdentityClient(url, proofSource).requestOtp("+919876543210")
        assertEquals(IdentityResult.Failure(IdentityError.Network), result)
    }

    @Test
    @DisplayName("redirects are never followed: the proof goes nowhere else")
    fun noRedirects() {
        val elsewhere = MockWebServer().apply { start(InetAddress.getByName("127.0.0.1"), 0) }
        try {
            server.enqueue(MockResponse().setResponseCode(307).setHeader("Location", elsewhere.url("/v1/otp/validate").toString()))
            val result = client().enrol(requestId, "123456")
            assertTrue(result is IdentityResult.Failure, "$result")
            assertEquals(0, elsewhere.requestCount, "the redirect was followed")
        } finally {
            elsewhere.shutdown()
        }
    }

    @Test
    @DisplayName("input the server would refuse is refused locally, and nothing is sent or signed")
    fun refusesInvalidInput() {
        val c = client()
        val refused = listOf(
            c.requestOtp("9876543210"),
            c.requestOtp("+0123456789"),
            c.enrol("req_short", "123456"),
            c.enrol(requestId, "12345"),
            c.refresh("sess_short", refreshToken),
            c.refresh(sessionId, "not-hex"),
        )
        refused.forEach { assertEquals(IdentityResult.Failure(IdentityError.InvalidInput), it) }
        assertEquals(0, server.requestCount)
        assertTrue(proofs.isEmpty(), "a proof was signed for a refused request")
    }

    @Test
    @DisplayName("deleteAccount sends the session and refresh token with a proof for the deletion URL, no integrity token, and succeeds only on 204")
    fun deleteAccount() {
        server.enqueue(MockResponse().setResponseCode(204))
        var integrityAsked = false
        val result = client(integrity = { integrityAsked = true; "integrity-token" }).deleteAccount(sessionId, refreshToken)
        assertEquals(IdentityResult.Success(Unit), result)

        val sent = server.takeRequest()
        assertEquals("POST", sent.method)
        assertEquals("/v1/account/delete", sent.path)
        assertEquals(listOf("POST" to origin() + "/v1/account/delete"), proofs)
        assertEquals("proof-1", sent.getHeader("DPoP"))
        assertNull(sent.getHeader("Integrity-Token"))
        assertFalse(integrityAsked, "deletion must not spend integrity quota")
        assertEquals("""{"sessionId":"$sessionId","refreshToken":"$refreshToken"}""", sent.body.readUtf8())
    }

    @Test
    @DisplayName("deleteAccount: anything but 204 is not a deletion; refusals map to their errors")
    fun deleteAccountRefusals() {
        val cases = listOf(
            json(200, "{}") to IdentityError.UnexpectedResponse(200),
            problem(401, "ERR_INVALID_TOKEN") to IdentityError.SessionEnded,
            problem(400, "ERR_INVALID_DPOP_PROOF") to IdentityError.InvalidDeviceProof,
            problem(503, "ERR_TEMPORARILY_UNAVAILABLE") to IdentityError.TemporarilyUnavailable,
            MockResponse().setResponseCode(302).setHeader("Location", "https://elsewhere.example.test/") to IdentityError.UnexpectedResponse(302),
        )
        for ((response, error) in cases) {
            server.enqueue(response)
            assertEquals(IdentityResult.Failure(error), client().deleteAccount(sessionId, refreshToken))
        }
        assertEquals(cases.size, server.requestCount, "a redirect must not be followed")
    }

    @Test
    @DisplayName("deleteAccount refuses malformed input without sending, and reports no answer as Network")
    fun deleteAccountInputAndNetwork() {
        assertEquals(IdentityResult.Failure(IdentityError.InvalidInput), client().deleteAccount("sess_x", refreshToken))
        assertEquals(IdentityResult.Failure(IdentityError.InvalidInput), client().deleteAccount(sessionId, "short"))
        assertEquals(0, server.requestCount)
        assertTrue(proofs.isEmpty())

        val gone = MockWebServer().apply { start(InetAddress.getByName("127.0.0.1"), 0) }
        val url = gone.url("/").toString()
        gone.shutdown()
        assertEquals(IdentityResult.Failure(IdentityError.Network), IdentityClient(url, proofSource).deleteAccount(sessionId, refreshToken))
    }

    @Test
    @DisplayName("a 204 is not success for the other endpoints")
    fun noContentElsewhere() {
        server.enqueue(MockResponse().setResponseCode(204))
        assertEquals(IdentityResult.Failure(IdentityError.UnexpectedResponse(204)), client().refresh(sessionId, refreshToken))
    }

    @Test
    @DisplayName("the base URL must be an https origin (plain http only for loopback)")
    fun baseUrlRules() {
        assertThrows<IllegalArgumentException> { IdentityClient("http://identity.example.test", proofSource) }
        assertThrows<IllegalArgumentException> { IdentityClient("https://identity.example.test/api", proofSource) }
        assertThrows<IllegalArgumentException> { IdentityClient("not a url", proofSource) }
        IdentityClient("https://identity.example.test/", proofSource) // a trailing slash is fine
    }

    @Test
    @DisplayName("issued tokens never appear in toString, and errors carry no response text")
    fun nothingSensitiveLeaks() {
        server.enqueue(json(200, enrolled))
        val tokens = (client().enrol(requestId, "123456") as IdentityResult.Success).value
        val shown = tokens.toString()
        assertFalse(shown.contains("eyJ.access.token") || shown.contains(refreshToken) || shown.contains(sessionId), shown)

        server.enqueue(problem(401, "ERR_INVALID_OTP").setBody("""{"code":"ERR_INVALID_OTP","detail":"SECRET-ECHO 123456"}"""))
        val failure = client().enrol(requestId, "123456").toString()
        assertFalse(failure.contains("SECRET-ECHO") || failure.contains("123456"), failure)
    }
}
