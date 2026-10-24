package com.callscreen.core.network.identity

import com.callscreen.core.security.dpop.DpopProofFactory
import com.callscreen.core.security.dpop.ProofKey
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.OkHttpClient
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertNotEquals
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.condition.EnabledIfEnvironmentVariable
import java.io.File
import java.security.KeyStore
import java.security.KeyPairGenerator
import java.security.Signature
import java.security.cert.CertificateFactory
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import java.util.Base64
import javax.net.ssl.SSLContext
import javax.net.ssl.TrustManagerFactory
import javax.net.ssl.X509TrustManager

/**
 * This client against the REAL identity service, not a mock: the Go binary,
 * its real HTTP API, DPoP verifier and PostgreSQL store, started by
 * tools/scripts/android-identity-contract.sh. It runs only when that script
 * sets AEGIS_IDENTITY_E2E_BASE_URL; CI then requires that it ran.
 *
 * The one test-only part of the server is its OTP sender, which writes each
 * code to AEGIS_IDENTITY_E2E_OTP_FILE instead of an SMS (production has none).
 * identity is reached over TLS through a loopback proxy, as behind a load
 * balancer; the client trusts only that run's CA (AEGIS_IDENTITY_E2E_CA_FILE).
 * The device key is a software P-256 key in place of Android Keystore; the
 * proofs are the app's own (core:security).
 */
@DisplayName("Identity client against the real identity service")
@EnabledIfEnvironmentVariable(named = "AEGIS_IDENTITY_E2E_BASE_URL", matches = "https://127\\.0\\.0\\.1:[0-9]+")
class IdentityLiveContractTest {

    private val baseUrl: String = requireNotNull(System.getenv("AEGIS_IDENTITY_E2E_BASE_URL"))
    private val otpFile = File(requireNotNull(System.getenv("AEGIS_IDENTITY_E2E_OTP_FILE")) { "AEGIS_IDENTITY_E2E_OTP_FILE is not set" })
    private val http = trusting(File(requireNotNull(System.getenv("AEGIS_IDENTITY_E2E_CA_FILE")) { "AEGIS_IDENTITY_E2E_CA_FILE is not set" }))

    @Test
    @DisplayName("enrol, refresh, device binding and refresh-token reuse, as the server enforces them")
    fun theFullSessionLifecycle() {
        val device = DpopProofFactory(SoftwareProofKey())
        val client = IdentityClient(baseUrl, DeviceProofs { method, htu -> device.proof(method, htu) }, httpClient = http)

        val before = otpCodes().size
        val challenge = success(client.requestOtp(MSISDN))
        val code = otpFromServer(after = before)

        // A wrong code is refused without spending the challenge (3 attempts).
        assertEquals(IdentityResult.Failure(IdentityError.InvalidOtp), client.enrol(challenge.requestId, wrong(code)))

        val enrolled = success(client.enrol(challenge.requestId, code))
        val sessionId = requireNotNull(enrolled.sessionId) { "enrolment returned no session" }
        assertEquals(device.jwk.thumbprint, jkt(enrolled.accessToken), "the access token must be bound to this device key")

        // Another key cannot refresh this session, and the refusal does not
        // rotate: the genuine device's token stays valid.
        val other = DpopProofFactory(SoftwareProofKey())
        val thief = IdentityClient(baseUrl, DeviceProofs { method, htu -> other.proof(method, htu) }, httpClient = http)
        assertEquals(IdentityResult.Failure(IdentityError.InvalidDeviceProof), thief.refresh(sessionId, enrolled.refreshToken))

        val refreshed = success(client.refresh(sessionId, enrolled.refreshToken))
        assertNotEquals(enrolled.refreshToken, refreshed.refreshToken, "refresh must rotate the refresh token")
        assertEquals(device.jwk.thumbprint, jkt(refreshed.accessToken))

        // Presenting the rotated-away token is reuse: refused, and the whole
        // session family is revoked, so the newest token is dead too.
        assertEquals(IdentityResult.Failure(IdentityError.SessionEnded), client.refresh(sessionId, enrolled.refreshToken))
        assertEquals(IdentityResult.Failure(IdentityError.SessionEnded), client.refresh(sessionId, refreshed.refreshToken))
    }

    /** Every code the server's test OTP sender has written so far, in order. */
    private fun otpCodes(): List<String> =
        otpFile.takeIf { it.isFile }?.readLines()?.filter { it.startsWith(OTP_PREFIX) }?.map { it.removePrefix(OTP_PREFIX).trim() }
            ?: emptyList()

    /**
     * The first code written after the first [after] codes, which is this
     * test's own (other suites share the file), waiting up to 30 seconds.
     */
    private fun otpFromServer(after: Int): String {
        val deadline = System.nanoTime() + 30_000_000_000L
        while (System.nanoTime() < deadline) {
            otpCodes().getOrNull(after)?.let { return it }
            Thread.sleep(100)
        }
        error("the server wrote no OTP within 30 seconds")
    }

    private fun <T> success(result: IdentityResult<T>): T = when (result) {
        is IdentityResult.Success -> result.value
        is IdentityResult.Failure -> throw AssertionError("expected success, got ${result.error}")
    }

    private companion object {
        /** A syntactically valid E.164 number; nothing is sent to it. */
        const val MSISDN = "+919000000001"
        const val OTP_PREFIX = "E2E-OTP "

        fun wrong(code: String): String = code.map { if (it == '9') '0' else it + 1 }.joinToString("")

        /** The production client's settings, trusting only [ca] (this run's test CA). */
        fun trusting(ca: File): OkHttpClient {
            val certificate = ca.inputStream().use { CertificateFactory.getInstance("X.509").generateCertificate(it) }
            val store = KeyStore.getInstance(KeyStore.getDefaultType()).apply {
                load(null, null)
                setCertificateEntry("aegis-android-contract-ca", certificate)
            }
            val trust = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm()).apply { init(store) }
                .trustManagers.single() as X509TrustManager
            val tls = SSLContext.getInstance("TLS").apply { init(null, arrayOf(trust), null) }
            return IdentityClient.defaultHttpClient().newBuilder().sslSocketFactory(tls.socketFactory, trust).build()
        }

        /** cnf.jkt from an access token's claims (the signature is the server's concern). */
        fun jkt(accessToken: String): String? {
            val claims = String(Base64.getUrlDecoder().decode(accessToken.split('.')[1]))
            return Json.parseToJsonElement(claims).jsonObject["cnf"]?.jsonObject?.get("jkt")?.jsonPrimitive?.content
        }
    }
}

/** A software P-256 device key, standing in for Android Keystore on the JVM. */
private class SoftwareProofKey : ProofKey {
    private val pair = KeyPairGenerator.getInstance("EC").apply { initialize(ECGenParameterSpec("secp256r1")) }.generateKeyPair()
    override val publicKey: ECPublicKey = pair.public as ECPublicKey
    override fun signDer(data: ByteArray): ByteArray =
        Signature.getInstance("SHA256withECDSA").run {
            initSign(pair.private)
            update(data)
            sign()
        }
}
