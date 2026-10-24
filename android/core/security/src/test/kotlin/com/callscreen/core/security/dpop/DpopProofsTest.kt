package com.callscreen.core.security.dpop

import java.math.BigInteger
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.PrivateKey
import java.security.Signature
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import java.security.spec.ECPoint
import java.security.spec.ECPublicKeySpec
import java.util.Base64
import org.junit.jupiter.api.Assertions.assertArrayEquals
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.assertThrows

/**
 * The proof profile identity and edge-api accept (packages/go/dpop). The
 * vectors are the ones the Go server's own tests use, so a pass here and
 * there means the client and server agree on them.
 */
@DisplayName("DPoP proofs")
class DpopProofsTest {

    private val decoder = Base64.getUrlDecoder()

    /** A java.security P-256 key standing in for the Keystore key. */
    private class JvmKey : ProofKey {
        private val pair = KeyPairGenerator.getInstance("EC").apply { initialize(ECGenParameterSpec("secp256r1")) }.generateKeyPair()
        val private: PrivateKey = pair.private
        override val publicKey: ECPublicKey = pair.public as ECPublicKey
        override fun signDer(data: ByteArray): ByteArray =
            Signature.getInstance("SHA256withECDSA").run { initSign(private); update(data); sign() }
    }

    private fun json(segment: String) = String(decoder.decode(segment), Charsets.UTF_8)

    @Test
    @DisplayName("the thumbprint is RFC 7638 over the RFC 9449 example key (the server's vector)")
    fun thumbprintVector() {
        val jwk = P256Jwk(
            x = "l8tFrhx-34tV3hRICRDY9zCkDlpBhF42UQUfWVAWBFs",
            y = "9VE4jf_Ok_o64zbTTlcuNJajHmt6v9TDVrU0CdvGRDA",
        )
        assertEquals("0ZcOCORZNYy-DWpqq30jZyJGHTN0d2HglBV3uiguA4I", jwk.thumbprint)
    }

    @Test
    @DisplayName("a public key's jwk round-trips through its coordinates")
    fun jwkOfKey() {
        val x = BigInteger(1, decoder.decode("l8tFrhx-34tV3hRICRDY9zCkDlpBhF42UQUfWVAWBFs"))
        val y = BigInteger(1, decoder.decode("9VE4jf_Ok_o64zbTTlcuNJajHmt6v9TDVrU0CdvGRDA"))
        val params = (JvmKey().publicKey).params
        val key = KeyFactory.getInstance("EC").generatePublic(ECPublicKeySpec(ECPoint(x, y), params)) as ECPublicKey
        assertEquals("0ZcOCORZNYy-DWpqq30jZyJGHTN0d2HglBV3uiguA4I", P256Jwk.of(key).thumbprint)
    }

    @Test
    @DisplayName("a proof has exactly the server's header and claims, and a raw R||S signature that verifies")
    fun proofShapeAndSignature() {
        val key = JvmKey()
        val factory = DpopProofFactory(key, nowSeconds = { 1_760_000_000L }, jti = { "jti-1" })
        val proof = factory.proof("POST", "https://identity.example.test/v1/token/refresh")
        val parts = proof.split('.')
        assertEquals(3, parts.size)
        parts.forEach { assertFalse(it.contains('=') || it.contains('+') || it.contains('/'), "segment is not unpadded base64url: $it") }

        val jwk = factory.jwk
        assertEquals("""{"typ":"dpop+jwt","alg":"ES256","jwk":{"kty":"EC","crv":"P-256","x":"${jwk.x}","y":"${jwk.y}"}}""", json(parts[0]))
        assertEquals("""{"jti":"jti-1","htm":"POST","htu":"https://identity.example.test/v1/token/refresh","iat":1760000000}""", json(parts[1]))

        val raw = decoder.decode(parts[2])
        assertEquals(64, raw.size, "ES256 signatures are 64 raw bytes, never DER")
        val verifier = Signature.getInstance("SHA256withECDSAinP1363Format").apply {
            initVerify(key.publicKey)
            update((parts[0] + "." + parts[1]).toByteArray(Charsets.US_ASCII))
        }
        assertTrue(verifier.verify(raw), "the raw signature does not verify")
    }

    @Test
    @DisplayName("ath is base64url(SHA-256(access token)), present only when a token is presented")
    fun athOnlyWithAnAccessToken() {
        val factory = DpopProofFactory(JvmKey(), nowSeconds = { 1L }, jti = { "j" })
        val withToken = json(factory.proof("GET", "https://edge.example.test/v1/me", accessToken = "abc").split('.')[1])
        assertTrue(withToken.endsWith(""","ath":"ungWv48Bz-pBQUDeXa4iI7ADYaOWF3qctBD_YfIAFa0"}"""), withToken)
        val without = json(factory.proof("GET", "https://edge.example.test/v1/me").split('.')[1])
        assertFalse(without.contains("ath"), without)
    }

    @Test
    @DisplayName("the integrity requestHash is base64url(SHA-256(proof)) (ADR-0031, the server's vector)")
    fun integrityRequestHashVector() {
        assertEquals("ungWv48Bz-pBQUDeXa4iI7ADYaOWF3qctBD_YfIAFa0", integrityRequestHash("abc"))
    }

    @Test
    @DisplayName("every proof gets a fresh random jti by default")
    fun jtiIsFresh() {
        val factory = DpopProofFactory(JvmKey())
        val a = json(factory.proof("POST", "https://identity.example.test/x").split('.')[1])
        val b = json(factory.proof("POST", "https://identity.example.test/x").split('.')[1])
        assertFalse(a == b, "two proofs shared a jti")
        assertEquals(43, randomJti().length)
    }

    @Test
    @DisplayName("requests the server would refuse are refused before signing")
    fun refusesWhatTheServerRefuses() {
        val factory = DpopProofFactory(JvmKey(), jti = { "j" })
        assertThrows<IllegalArgumentException> { factory.proof("post", "https://identity.example.test/x") }
        assertThrows<IllegalArgumentException> { factory.proof("POST", "http://identity.example.test/x") }
        assertThrows<IllegalArgumentException> { factory.proof("POST", "https://identity.example.test/a b") }
        assertThrows<IllegalArgumentException> { factory.proof("POST", "https://identity.example.test/x", accessToken = "") }
        val badJti = DpopProofFactory(JvmKey(), jti = { "has space" })
        assertThrows<IllegalArgumentException> { badJti.proof("POST", "https://identity.example.test/x") }
        val longJti = DpopProofFactory(JvmKey(), jti = { "a".repeat(129) })
        assertThrows<IllegalArgumentException> { longJti.proof("POST", "https://identity.example.test/x") }
    }

    @Test
    @DisplayName("DER signatures convert to raw R||S: padded, short and full-width integers")
    fun derToRaw() {
        // r has a leading 0x00 (high bit set), s is short (31 bytes): both normalise to 32.
        val r = ByteArray(32) { 0x80.toByte() }
        val s = ByteArray(31) { 0x11 }
        val der = byteArrayOf(0x30, (2 + 33 + 2 + 31).toByte(), 0x02, 33, 0x00) + r + byteArrayOf(0x02, 31) + s
        assertArrayEquals(r + (byteArrayOf(0x00) + s), Es256.derToRaw(der))
    }

    @Test
    @DisplayName("malformed DER is refused, never guessed at")
    fun derToRawRefusesMalformed() {
        val ok = byteArrayOf(0x30, 6, 0x02, 1, 0x01, 0x02, 1, 0x01)
        assertEquals(64, Es256.derToRaw(ok).size)
        listOf(
            byteArrayOf(0x31, 6, 0x02, 1, 0x01, 0x02, 1, 0x01), // not a SEQUENCE
            byteArrayOf(0x30, 7, 0x02, 1, 0x01, 0x02, 1, 0x01), // length mismatch
            ok + byteArrayOf(0x00), // trailing bytes
            byteArrayOf(0x30, 6, 0x02, 1, 0x81.toByte(), 0x02, 1, 0x01), // negative r
            byteArrayOf(0x30, 7, 0x02, 2, 0x00, 0x01, 0x02, 1, 0x01), // non-minimal r
            byteArrayOf(0x30, 6, 0x04, 1, 0x01, 0x02, 1, 0x01), // not an INTEGER
            byteArrayOf(0x30, 3, 0x02, 1, 0x01), // s missing
        ).forEachIndexed { i, der ->
            assertThrows<IllegalArgumentException>("case $i") { Es256.derToRaw(der) }
        }
    }

    @Test
    @DisplayName("real Keystore-style DER signatures from java.security always convert and verify")
    fun derFromJavaSecurityConverts() {
        val key = JvmKey()
        repeat(64) { n ->
            val data = "message $n".toByteArray()
            val raw = Es256.derToRaw(key.signDer(data))
            val ok = Signature.getInstance("SHA256withECDSAinP1363Format").run {
                initVerify(key.publicKey); update(data); verify(raw)
            }
            assertTrue(ok, "signature $n did not verify after conversion")
        }
    }
}
