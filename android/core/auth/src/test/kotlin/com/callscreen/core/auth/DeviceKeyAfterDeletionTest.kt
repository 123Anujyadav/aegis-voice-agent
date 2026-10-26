package com.callscreen.core.auth

import com.callscreen.core.datastore.session.BlobStore
import com.callscreen.core.datastore.session.EncryptedSessionStore
import com.callscreen.core.datastore.session.SessionKey
import com.callscreen.core.network.identity.DeviceProofs
import com.callscreen.core.network.identity.IdentityClient
import com.callscreen.core.network.identity.IdentityError
import com.callscreen.core.security.dpop.DeviceProofKey
import com.callscreen.core.security.dpop.ProofKey
import com.callscreen.core.security.dpop.ProofKeyStore
import java.net.InetAddress
import java.security.KeyPairGenerator
import java.security.Signature
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import java.util.concurrent.TimeUnit
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.runBlocking
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertNotEquals
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

/**
 * The device key's life across account deletion, through the real client,
 * authenticator and DeviceProofKey (a software key store stands in for
 * Android Keystore): it is destroyed only after the server CONFIRMS the
 * deletion, every unconfirmed outcome keeps it for the retry, and the next
 * sign-in from this phone enrols a new key.
 */
@DisplayName("Device key after account deletion")
class DeviceKeyAfterDeletionTest {

    private class SoftwareKeys : ProofKeyStore {
        var key: ProofKey? = null
        var deleted = 0
        override fun loadOrCreate(): ProofKey = key ?: software().also { key = it }
        override fun delete() {
            key = null
            deleted++
        }
        private fun software(): ProofKey {
            val pair = KeyPairGenerator.getInstance("EC").apply { initialize(ECGenParameterSpec("secp256r1")) }.generateKeyPair()
            return object : ProofKey {
                override val publicKey = pair.public as ECPublicKey
                override fun signDer(data: ByteArray): ByteArray =
                    Signature.getInstance("SHA256withECDSA").run { initSign(pair.private); update(data); sign() }
            }
        }
    }

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

    private lateinit var server: MockWebServer
    private val keys = SoftwareKeys()
    private val device = DeviceProofKey(keys)
    private val sessionId = "sess_" + "b".repeat(64)

    @BeforeEach
    fun start() {
        server = MockWebServer().apply { start(InetAddress.getByName("127.0.0.1"), 0) }
    }

    @AfterEach
    fun stop() = server.shutdown()

    /** The authenticator as the app wires it: proofs from [device], discarded on a confirmed deletion. */
    private fun authenticator(): IdentityAuthenticator {
        val http = IdentityClient.defaultHttpClient().newBuilder().readTimeout(500, TimeUnit.MILLISECONDS).build()
        // The proof carries its key's thumbprint, so each request shows which key signed it.
        val proofs = DeviceProofs { _, _ -> "proof-by-" + device.proofs().jwk.thumbprint }
        val client = IdentityClient(server.url("/").toString(), proofs, httpClient = http)
        val store = EncryptedSessionStore(MemoryKey(), MemoryBlobs())
        return IdentityAuthenticator(client, store, io = Dispatchers.IO, onAccountDeleted = device::discard)
    }

    private fun enrolled() = MockResponse().setResponseCode(200).setHeader("Content-Type", "application/json")
        .setBody("""{"accessToken":"a","tokenType":"DPoP","expiresIn":900,"refreshToken":"${"1".repeat(64)}","sessionId":"$sessionId"}""")

    private fun problem(status: Int, code: String) = MockResponse().setResponseCode(status)
        .setHeader("Content-Type", "application/problem+json").setBody("""{"code":"$code"}""")

    private fun signerOfNextRequest(): String = server.takeRequest().getHeader("DPoP")!!.removePrefix("proof-by-")

    private suspend fun signIn(auth: IdentityAuthenticator): String {
        server.enqueue(enrolled())
        assertEquals(EnrolOutcome.Enrolled, auth.enrol("req_" + "a".repeat(64), "123456"))
        return signerOfNextRequest()
    }

    @Test
    @DisplayName("no unconfirmed outcome destroys the key; every retry is signed by the enrolled key; only the 204 destroys it")
    fun keptUntilConfirmed() = runBlocking {
        val auth = authenticator()
        val enrolledKey = signIn(auth)
        val unconfirmed = listOf(
            MockResponse().setResponseCode(204).setHeadersDelay(2, TimeUnit.SECONDS) to IdentityError.Network,
            problem(503, "ERR_TEMPORARILY_UNAVAILABLE") to IdentityError.TemporarilyUnavailable,
            problem(400, "ERR_INVALID_DPOP_PROOF") to IdentityError.InvalidDeviceProof,
            MockResponse().setResponseCode(500) to IdentityError.UnexpectedResponse(500),
        )
        for ((response, error) in unconfirmed) {
            server.enqueue(response)
            assertEquals(AccountDeletionOutcome.Failed(error), auth.deleteAccount(), "$error")
            assertEquals(enrolledKey, signerOfNextRequest(), "$error: the retry must be signed by the enrolled key")
            assertEquals(0, keys.deleted, "$error destroyed the key")
        }

        server.enqueue(MockResponse().setResponseCode(204))
        assertEquals(AccountDeletionOutcome.Deleted, auth.deleteAccount())
        assertEquals(enrolledKey, signerOfNextRequest())
        assertEquals(1, keys.deleted, "a confirmed deletion must destroy the key")
    }

    @Test
    @DisplayName("a session the server ended (401) keeps the key: nothing confirmed a deletion")
    fun sessionEndedKeepsTheKey() = runBlocking {
        val auth = authenticator()
        signIn(auth)
        server.enqueue(problem(401, "ERR_INVALID_TOKEN"))
        assertEquals(AccountDeletionOutcome.SessionEnded, auth.deleteAccount())
        assertEquals(0, keys.deleted)
        assertEquals(AccountDeletionOutcome.NotSignedIn, auth.deleteAccount())
        assertEquals(0, keys.deleted)
    }

    @Test
    @DisplayName("signing up again after a confirmed deletion enrols a NEW key, unlinkable to the erased account's")
    fun reRegistrationUsesANewKey() = runBlocking {
        val auth = authenticator()
        val erasedKey = signIn(auth)
        server.enqueue(MockResponse().setResponseCode(204))
        assertEquals(AccountDeletionOutcome.Deleted, auth.deleteAccount())
        signerOfNextRequest()

        val newKey = signIn(auth)
        assertNotEquals(erasedKey, newKey, "re-registration from this phone reused the erased account's key")
    }
}
