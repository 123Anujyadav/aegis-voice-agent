package com.callscreen.app.security

import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import com.callscreen.core.auth.AuthState
import com.callscreen.core.auth.EnrolOutcome
import com.callscreen.core.auth.IdentityAuthenticator
import com.callscreen.core.auth.SignedOutReason
import com.callscreen.core.datastore.session.DataStoreBlobStore
import com.callscreen.core.datastore.session.EncryptedSessionStore
import com.callscreen.core.datastore.session.KeystoreSessionKey
import com.callscreen.core.network.identity.DeviceProofs
import com.callscreen.core.network.identity.IdentityClient
import com.callscreen.core.network.identity.IdentityError
import com.callscreen.core.network.identity.IdentityRoutes
import com.callscreen.core.security.dpop.DeviceProofKey
import com.callscreen.core.security.dpop.KeystoreProofKeys
import java.math.BigInteger
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.Signature
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import java.security.spec.ECPoint
import java.security.spec.ECPublicKeySpec
import java.util.Base64
import kotlinx.coroutines.runBlocking
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import okhttp3.tls.HandshakeCertificates
import okhttp3.tls.HeldCertificate
import org.json.JSONObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith

/**
 * Sign-in and refresh on a real Android runtime, against a local HTTPS fake of
 * identity. The JVM tests (SessionWiringTest, IdentityAuthenticatorTest) use a
 * software key and a javax.crypto session key; here the proofs are signed by
 * a real Android Keystore key and the session is sealed by a real Keystore AES
 * key into the app's DataStore file:
 * - every proof verifies as ES256 under the jwk it carries, and names the
 *   method and URL it was made for;
 * - enrolment and refresh are signed by the same device key;
 * - the stored session survives a new authenticator (a process restart) and
 *   is read back without the network;
 * - a 503 keeps the stored session, and identity's ERR_INVALID_TOKEN clears it.
 *
 * Test key aliases are used, never the app's. The session file is the app's
 * own (DataStoreBlobStore has no other), so it is cleared before and after.
 */
@RunWith(AndroidJUnit4::class)
class SessionOnDeviceTest {

    private val context = ApplicationProvider.getApplicationContext<android.content.Context>()
    private val proofKeys = KeystoreProofKeys(PROOF_ALIAS)
    private val sessionKey = KeystoreSessionKey(SESSION_ALIAS)
    private val blobs = DataStoreBlobStore(context)
    private val server = MockWebServer()
    private var now = 1_800_000_000L

    private val serverCertificate = HeldCertificate.Builder()
        .addSubjectAlternativeName("localhost")
        .addSubjectAlternativeName("127.0.0.1")
        .build()
    private val trustTheServer = HandshakeCertificates.Builder()
        .addTrustedCertificate(serverCertificate.certificate)
        .build()

    @Before
    fun setUp() {
        runBlocking { blobs.clear() }
        server.useHttps(HandshakeCertificates.Builder().heldCertificate(serverCertificate).build().sslSocketFactory(), false)
        server.start()
    }

    @After
    fun tearDown() {
        server.shutdown()
        runBlocking { blobs.clear() }
        sessionKey.delete()
        proofKeys.delete()
    }

    /** A new authenticator over the same Keystore keys and session file: what a restarted process builds. */
    private fun authenticator(): IdentityAuthenticator {
        val device = DeviceProofKey(proofKeys)
        val http = IdentityClient.defaultHttpClient().newBuilder()
            .sslSocketFactory(trustTheServer.sslSocketFactory(), trustTheServer.trustManager)
            .build()
        val client = IdentityClient(
            server.url("/").toString(),
            DeviceProofs { method, htu -> device.proofs().proof(method, htu) },
            httpClient = http,
        )
        return IdentityAuthenticator(client, EncryptedSessionStore(sessionKey, blobs), nowSeconds = { now })
    }

    private fun tokens(access: String, refresh: String, sessionId: String? = SESSION_ID): MockResponse {
        val body = JSONObject()
            .put("accessToken", access)
            .put("tokenType", "DPoP")
            .put("expiresIn", 900)
            .put("refreshToken", refresh)
        if (sessionId != null) body.put("sessionId", sessionId)
        return MockResponse().setResponseCode(200).setHeader("Content-Type", "application/json").setBody(body.toString())
    }

    private fun problem(status: Int, code: String) = MockResponse().setResponseCode(status)
        .setHeader("Content-Type", "application/problem+json").setBody("""{"code":"$code"}""")

    private fun enrol(auth: IdentityAuthenticator) {
        server.enqueue(tokens("access-1", "refresh-1"))
        assertEquals(EnrolOutcome.Enrolled, runBlocking { auth.enrol(REQUEST_ID, "123456") })
    }

    @Test
    fun keystoreSignedEnrolmentAndRefreshSurviveARestart() = runBlocking {
        val auth = authenticator()
        enrol(auth)
        val enrolProof = verifiedProof(server.takeRequest(), IdentityRoutes.ENROLMENT)

        // Within the refresh margin of the expiry identity reported: one refresh.
        now += 900 - IdentityAuthenticator.REFRESH_MARGIN_SECONDS
        server.enqueue(tokens("access-2", "refresh-2", sessionId = null))
        val refreshed = auth.accessToken()
        assertTrue("$refreshed", refreshed is AuthState.Authenticated && refreshed.accessToken == "access-2")
        val refreshRequest = server.takeRequest()
        val refreshProof = verifiedProof(refreshRequest, IdentityRoutes.REFRESH)
        assertEquals("enrolment and refresh are signed by the same device key", enrolProof.thumbprint, refreshProof.thumbprint)
        assertTrue("the rotated-away token was presented", refreshRequest.body.readUtf8().contains("\"refresh-1\""))

        // A restarted process reads the sealed record back, with no request.
        val requests = server.requestCount
        val restarted = authenticator().accessToken()
        assertTrue("$restarted", restarted is AuthState.Authenticated && restarted.accessToken == "access-2")
        assertEquals("the stored session was read without the network", requests, server.requestCount)
    }

    @Test
    fun aTemporaryFailureKeepsTheStoredSessionAndAnEndedSessionClearsIt() = runBlocking {
        enrol(authenticator())
        server.takeRequest()
        now += 900

        server.enqueue(problem(503, "ERR_TEMPORARILY_UNAVAILABLE"))
        val unavailable = authenticator().accessToken()
        assertTrue("$unavailable", unavailable is AuthState.Unavailable)
        assertNotNull("a 503 kept the sealed record", blobs.read())
        assertTrue("and a restart still finds the session", authenticator().isSignedIn())

        server.enqueue(problem(401, "ERR_INVALID_TOKEN"))
        assertEquals(AuthState.SignedOut(SignedOutReason.SESSION_ENDED), authenticator().accessToken())
        assertEquals("identity ended the session: the record is gone", null, blobs.read())
        assertEquals(AuthState.SignedOut(SignedOutReason.NOT_SIGNED_IN), authenticator().accessToken())
        // Both failures reached identity; nothing else did.
        assertEquals(3, server.requestCount)
        assertEquals(IdentityError.TemporarilyUnavailable, (unavailable as AuthState.Unavailable).error)
    }

    private class Proof(val thumbprint: String)

    /**
     * Verifies [request]'s DPoP proof as identity would: ES256 over the
     * signing input, under the jwk in its header, for this request's method
     * and URL. Returns the key's identity (x and y).
     */
    private fun verifiedProof(request: RecordedRequest, route: String): Proof {
        val compact = assertNotNullValue(request.getHeader("DPoP"), "no DPoP header on $route")
        val parts = compact.split('.')
        assertEquals("a compact JWS", 3, parts.size)
        val header = json(parts[0])
        val claims = json(parts[1])
        assertEquals("dpop+jwt", header.string("typ"))
        assertEquals("ES256", header.string("alg"))
        assertEquals("POST", claims.string("htm"))
        assertEquals(server.url(route).toString(), claims.string("htu"))
        assertTrue("iat is the phone's time", kotlin.math.abs(claims.getLong("iat") - System.currentTimeMillis() / 1000) < 300)

        val jwk = header.getJSONObject("jwk")
        val x = BigInteger(1, b64u(jwk.string("x")))
        val y = BigInteger(1, b64u(jwk.string("y")))
        val key = KeyFactory.getInstance("EC").generatePublic(ECPublicKeySpec(ECPoint(x, y), P256)) as ECPublicKey
        val raw = b64u(parts[2])
        assertEquals("a raw 64-byte R||S signature", 64, raw.size)
        val valid = Signature.getInstance("SHA256withECDSA").run {
            initVerify(key)
            update("${parts[0]}.${parts[1]}".toByteArray(Charsets.US_ASCII))
            verify(rawToDer(raw))
        }
        assertTrue("the Keystore-signed proof verifies under its jwk", valid)
        return Proof(jwk.string("x") + "." + jwk.string("y"))
    }

    private fun <T> assertNotNullValue(value: T?, message: String): T {
        assertNotNull(message, value)
        return value!!
    }

    private fun b64u(s: String): ByteArray = Base64.getUrlDecoder().decode(s)

    private fun json(segment: String): JSONObject = JSONObject(String(b64u(segment), Charsets.UTF_8))

    private fun JSONObject.string(name: String): String = getString(name)

    /** R||S (32 bytes each) to the DER SEQUENCE of two INTEGERs that java.security verifies. */
    private fun rawToDer(raw: ByteArray): ByteArray {
        fun integer(half: ByteArray): ByteArray {
            val positive = BigInteger(1, half).toByteArray() // minimal, with a 0x00 when the top bit is set
            return byteArrayOf(0x02, positive.size.toByte()) + positive
        }
        val body = integer(raw.copyOfRange(0, 32)) + integer(raw.copyOfRange(32, 64))
        return byteArrayOf(0x30, body.size.toByte()) + body
    }

    private companion object {
        const val PROOF_ALIAS = "callscreen.test.session-device-key"
        const val SESSION_ALIAS = "callscreen.test.session-key"
        val REQUEST_ID = "req_" + "a".repeat(64)
        val SESSION_ID = "sess_" + "b".repeat(64)

        /** P-256's domain parameters, from a software key (no Keystore involved). */
        val P256 = (KeyPairGenerator.getInstance("EC").apply { initialize(ECGenParameterSpec("secp256r1")) }
            .generateKeyPair().public as ECPublicKey).params
    }
}
