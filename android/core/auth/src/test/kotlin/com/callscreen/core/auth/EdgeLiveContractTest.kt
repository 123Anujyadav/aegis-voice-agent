package com.callscreen.core.auth

import com.callscreen.core.datastore.session.BlobStore
import com.callscreen.core.datastore.session.EncryptedSessionStore
import com.callscreen.core.datastore.session.SessionKey
import com.callscreen.core.network.identity.DeviceProofs
import com.callscreen.core.network.identity.IdentityClient
import com.callscreen.core.network.identity.IdentityResult
import com.callscreen.core.security.dpop.DpopProofFactory
import com.callscreen.core.security.dpop.ProofKey
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.OkHttpClient
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.condition.EnabledIfEnvironmentVariable
import java.io.File
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.Signature
import java.security.cert.CertificateFactory
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import java.util.Base64
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.net.ssl.SSLContext
import javax.net.ssl.TrustManagerFactory
import javax.net.ssl.X509TrustManager

/**
 * The app's sign-in and protected-API stack against the REAL identity and
 * edge-api services, not mocks: both Go services as processes, on one
 * PostgreSQL (identity's migrations create edge-api's shared DPoP replay
 * table), each behind a loopback TLS proxy, started by
 * tools/scripts/android-identity-contract.sh. It runs only when that script
 * sets AEGIS_EDGE_E2E_ORIGIN; CI then requires that it ran.
 *
 * The app side is real: IdentityAuthenticator over EncryptedSessionStore (an
 * in-memory key and blob store in place of Android Keystore and DataStore),
 * and EdgeApiClient with the app's own DpopProofFactory (a software P-256 key
 * in place of Keystore). The server's one test-only part is identity's OTP
 * sender, which writes codes to AEGIS_IDENTITY_E2E_OTP_FILE.
 */
@DisplayName("Edge-api client against the real identity and edge-api services")
@EnabledIfEnvironmentVariable(named = "AEGIS_EDGE_E2E_ORIGIN", matches = "https://127\\.0\\.0\\.1:[0-9]+")
class EdgeLiveContractTest {

    private val edgeOrigin: String = requireNotNull(System.getenv("AEGIS_EDGE_E2E_ORIGIN"))
    private val identityUrl: String = requireNotNull(System.getenv("AEGIS_IDENTITY_E2E_BASE_URL")) { "AEGIS_IDENTITY_E2E_BASE_URL is not set" }
    private val otpFile = File(requireNotNull(System.getenv("AEGIS_IDENTITY_E2E_OTP_FILE")) { "AEGIS_IDENTITY_E2E_OTP_FILE is not set" })
    private val http = trusting(File(requireNotNull(System.getenv("AEGIS_IDENTITY_E2E_CA_FILE")) { "AEGIS_IDENTITY_E2E_CA_FILE is not set" }))

    @Test
    @DisplayName("edge-api admits the identity-issued token with this device's proof, and refuses another key's")
    fun edgeAdmitsTheBoundTokenAndRefusesAnotherKey() = runBlocking {
        val device = DpopProofFactory(SoftwareProofKey())
        val identity = IdentityClient(identityUrl, DeviceProofs { method, htu -> device.proof(method, htu) }, httpClient = http)
        val auth = IdentityAuthenticator(identity, EncryptedSessionStore(MemoryKey(), MemoryBlobs()))

        // Sign in through the authenticator, as the app does.
        val before = otpCodes().size
        val challenge = when (val r = auth.requestCode(MSISDN)) {
            is IdentityResult.Success -> r.value
            is IdentityResult.Failure -> throw AssertionError("OTP request failed: ${r.error}")
        }
        assertEquals(EnrolOutcome.Enrolled, auth.enrol(challenge.requestId, otpFromServer(after = before)))
        val token = when (val state = auth.accessToken()) {
            is AuthState.Authenticated -> state.accessToken
            else -> throw AssertionError("not signed in after enrolment: $state")
        }

        // GET /v1/me with this device's proof: edge-api verifies identity's
        // token (EdDSA, issuer, audience, cnf.jkt) and the proof (htu, ath,
        // jkt), and answers with the token's subject and key binding.
        val edge = EdgeApiClient(edgeOrigin, auth, AccessTokenProofs { method, htu, t -> device.proof(method, htu, t) }, http)
        val expected = EdgeIdentity(claim(token, "sub"), device.jwk.thumbprint)
        assertEquals(EdgeResult.Success(expected), edge.me())
        // A second call carries a fresh proof (new jti) and is admitted too.
        assertEquals(EdgeResult.Success(expected), edge.me())

        // The same token with a proof from another key is refused as a proof
        // failure, not a token failure, so the client does not refresh.
        val other = DpopProofFactory(SoftwareProofKey())
        val stolen = EdgeApiClient(edgeOrigin, auth, AccessTokenProofs { method, htu, t -> other.proof(method, htu, t) }, http)
        assertEquals(EdgeResult.Failure(EdgeError.ProofRejected), stolen.me())
    }

    private fun otpCodes(): List<String> =
        otpFile.takeIf { it.isFile }?.readLines()?.filter { it.startsWith(OTP_PREFIX) }?.map { it.removePrefix(OTP_PREFIX).trim() }
            ?: emptyList()

    /** The first code written after the first [after] codes: this test's own. */
    private fun otpFromServer(after: Int): String {
        val deadline = System.nanoTime() + 30_000_000_000L
        while (System.nanoTime() < deadline) {
            otpCodes().getOrNull(after)?.let { return it }
            Thread.sleep(100)
        }
        error("the server wrote no OTP within 30 seconds")
    }

    private companion object {
        /** A syntactically valid E.164 number, distinct from the identity suite's; nothing is sent to it. */
        const val MSISDN = "+919000000002"
        const val OTP_PREFIX = "E2E-OTP "

        fun claim(jwt: String, name: String): String {
            val claims = String(Base64.getUrlDecoder().decode(jwt.split('.')[1]))
            return requireNotNull(Json.parseToJsonElement(claims).jsonObject[name]?.jsonPrimitive?.content) { "no $name claim" }
        }

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
    }

    private class MemoryBlobs : BlobStore {
        private var bytes: ByteArray? = null
        override suspend fun read(): ByteArray? = bytes
        override suspend fun write(bytes: ByteArray) {
            this.bytes = bytes.copyOf()
        }
        override suspend fun clear() {
            bytes = null
        }
    }

    private class MemoryKey : SessionKey {
        private var key: SecretKey? = null
        override fun getOrCreate(): SecretKey = key ?: KeyGenerator.getInstance("AES").apply { init(256) }.generateKey().also { key = it }
        override fun existing(): SecretKey? = key
        override fun delete() {
            key = null
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
