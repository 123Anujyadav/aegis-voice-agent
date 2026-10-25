package com.callscreen.core.network.identity

import okhttp3.CertificatePinner
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.tls.HandshakeCertificates
import okhttp3.tls.HeldCertificate
import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertNull
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.assertThrows

/**
 * ADR-0010's certificate pinning, against a real TLS server (MockWebServer
 * with certificates made here). Nothing reaches the network.
 */
@DisplayName("Certificate pinning")
class CertificatePinningTest {

    private val root = HeldCertificate.Builder().certificateAuthority(0).commonName("test root").build()
    private val leaf = HeldCertificate.Builder().signedBy(root).addSubjectAlternativeName("localhost").build()
    private val otherKey = HeldCertificate.Builder().commonName("not the server").build()
    private val spare = HeldCertificate.Builder().commonName("backup key").build()
    private lateinit var server: MockWebServer

    @BeforeEach
    fun start() {
        server = MockWebServer()
        server.useHttps(HandshakeCertificates.Builder().heldCertificate(leaf, root.certificate).build().sslSocketFactory(), false)
        server.start()
    }

    @AfterEach
    fun stop() = server.shutdown()

    private fun pin(c: HeldCertificate) = CertificatePinner.pin(c.certificate)

    /** The production client's settings and pins, trusting the test root as a device trusts public CAs. */
    private fun client(pins: CertificatePins?): OkHttpClient {
        val trust = HandshakeCertificates.Builder().addTrustedCertificate(root.certificate).build()
        return IdentityClient.defaultHttpClient(pins).newBuilder().sslSocketFactory(trust.sslSocketFactory(), trust.trustManager).build()
    }

    private fun requestOtp(pins: CertificatePins?): IdentityResult<OtpChallenge> {
        server.enqueue(
            // identity answers an OTP request with 202 Accepted (the client accepts nothing else).
            MockResponse().setResponseCode(202).setHeader("Content-Type", "application/json")
                .setBody("""{"requestId":"req_${"a".repeat(64)}","expiresInSeconds":300}"""),
        )
        // The certificate is for "localhost"; the server's own URL may carry the
        // machine's host name instead, so the origin is built explicitly.
        val origin = "https://localhost:${server.port}"
        return IdentityClient(origin, { _, _ -> "proof" }, httpClient = client(pins)).requestOtp("+919876543210")
    }

    @Test
    @DisplayName("a connection presenting the pinned key succeeds, with a backup pin alongside")
    fun pinnedKeyConnects() {
        val pins = CertificatePins.parse("localhost", "${pin(leaf)},${pin(spare)}")
        val result = requestOtp(pins)
        assertTrue(result is IdentityResult.Success, "got $result")
    }

    @Test
    @DisplayName("a connection to a key outside the pin set is refused, even with a CA-valid certificate")
    fun otherKeyIsRefused() {
        val pins = CertificatePins.parse("localhost", "${pin(otherKey)},${pin(spare)}")
        assertEquals(IdentityResult.Failure(IdentityError.Network), requestOtp(pins))
        assertEquals(0, server.requestCount, "nothing may be sent to an unpinned server")
    }

    @Test
    @DisplayName("without pins the client behaves as before (no pinning)")
    fun noPinsIsTodaysBehaviour() {
        val result = requestOtp(null)
        assertTrue(result is IdentityResult.Success, "got $result")
        // OkHttp wraps the default pinner, so compare its pins, not its identity.
        assertTrue(IdentityClient.defaultHttpClient().certificatePinner.pins.isEmpty())
    }

    @Test
    @DisplayName("pins are refused unless there are at least two distinct, well-formed ones for a host (ADR-0010)")
    fun parseRules() {
        val a = pin(leaf)
        val b = pin(spare)
        assertNull(CertificatePins.parse("api.example.test", ""), "empty means no pinning")
        assertEquals(listOf(a, b), CertificatePins.parse("api.example.test", "$a, $b")!!.pins)
        for (bad in listOf(a, "$a,$a", "$a,sha1/AAAA", "$a,sha256/short=", "$a,not a pin")) {
            assertThrows<IllegalArgumentException>(bad) { CertificatePins.parse("api.example.test", bad) }
        }
        for (badHost in listOf("", "API.example.test", "https://api.example.test", "api.example.test:443")) {
            assertThrows<IllegalArgumentException>(badHost) { CertificatePins.parse(badHost, "$a,$b") }
        }
    }
}
