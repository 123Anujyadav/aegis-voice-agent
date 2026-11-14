package com.callscreen.core.security.dpop

import java.security.KeyPairGenerator
import java.security.SecureRandom
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import java.util.Base64
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertNotEquals
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.assertThrows

/**
 * The Google sign-in nonce (ADR-0035 D35.4). The vectors were produced by the
 * server's own GoogleNonce (services/go/identity/internal/app/google_signin.go)
 * on main 538fa2b, so a pass here means the client computes the nonce the
 * server recomputes.
 */
@DisplayName("Google sign-in nonce")
class GoogleNonceTest {

    private val rfc9449Jkt = "0ZcOCORZNYy-DWpqq30jZyJGHTN0d2HglBV3uiguA4I"
    private val zeroJkt = "A".repeat(43)
    private val zeroSeed = "A".repeat(43) // 32 zero bytes
    private val sequenceSeed = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8" // bytes 0..31
    private val ffSeed = "_".repeat(42) + "8" // 32 bytes of 0xff

    @Test
    @DisplayName("matches the server's GoogleNonce on every vector")
    fun serverVectors() {
        val vectors = listOf(
            Triple(rfc9449Jkt, zeroSeed, "eGV54uqA1FAjglxnVbLsR791b84nDanQGQh3GYVxB4s"),
            Triple(rfc9449Jkt, sequenceSeed, "lD64hcTj3hUk04WnL5nwi1T-BlIysgJe-bchC_sxd7I"),
            Triple(rfc9449Jkt, ffSeed, "1Fdi2RiJ8aJspLLh_PO25x9egB0YkZwZF0Xcxj389Zg"),
            Triple(zeroJkt, zeroSeed, "LKtw_-RQjcOmznoZ4XRZo72T7qSPOTGdVFpG5EPySao"),
            Triple(zeroJkt, sequenceSeed, "7VOx42jlqCjDrhy26tx5aKN85af1f4BQKmjHMBSMCmg"),
            Triple(zeroJkt, ffSeed, "HTclI0P5oc7T0MCT7APdDtHV004240BqrGxBS3qtxoI"),
        )
        for ((jkt, seed, want) in vectors) {
            assertEquals(want, GoogleNonce.of(jkt, seed), "jkt=$jkt seed=$seed")
        }
    }

    @Test
    @DisplayName("the nonce is unpadded base64url of 32 bytes, and depends on both the key and the seed")
    fun encodingAndBinding() {
        val nonce = GoogleNonce.of(rfc9449Jkt, sequenceSeed)
        assertEquals(43, nonce.length)
        assertTrue(nonce.matches(Regex("^[A-Za-z0-9_-]{43}$")), nonce)
        assertEquals(32, Base64.getUrlDecoder().decode(nonce).size)
        assertNotEquals(nonce, GoogleNonce.of(zeroJkt, sequenceSeed), "another key must give another nonce")
        assertNotEquals(nonce, GoogleNonce.of(rfc9449Jkt, zeroSeed), "another seed must give another nonce")
    }

    @Test
    @DisplayName("a seed is 32 random bytes as 43 base64url characters, fresh every time")
    fun seeds() {
        val seeds = (1..200).map { GoogleNonce.newSeed() }
        for (seed in seeds) {
            assertEquals(GoogleNonce.SEED_LENGTH, seed.length)
            assertTrue(GoogleNonce.isValidSeed(seed), seed)
            assertEquals(32, Base64.getUrlDecoder().decode(seed).size)
        }
        assertEquals(seeds.size, seeds.toSet().size, "seeds repeated")

        // The source is the injected SecureRandom: a fixed stream gives a fixed seed.
        val zeros = object : SecureRandom() {
            override fun nextBytes(bytes: ByteArray) = bytes.fill(0)
        }
        assertEquals(zeroSeed, GoogleNonce.newSeed(zeros))
    }

    @Test
    @DisplayName("malformed inputs are refused, never hashed")
    fun invalidInputs() {
        val badSeeds = listOf("", "A".repeat(42), "A".repeat(44), "A".repeat(42) + "=", "A".repeat(42) + "+", "A".repeat(42) + "/", "A".repeat(42) + " ")
        for (seed in badSeeds) {
            assertFalse(GoogleNonce.isValidSeed(seed), "seed '$seed'")
            assertThrows<IllegalArgumentException>("seed '$seed'") { GoogleNonce.of(rfc9449Jkt, seed) }
        }
        val badJkts = listOf("", rfc9449Jkt.dropLast(1), rfc9449Jkt + "A", rfc9449Jkt.dropLast(1) + "=", rfc9449Jkt.replace('-', '+'))
        for (jkt in badJkts) {
            assertThrows<IllegalArgumentException>("jkt '$jkt'") { GoogleNonce.of(jkt, zeroSeed) }
        }
    }

    @Test
    @DisplayName("the nonce for a proof factory uses that factory's own key thumbprint")
    fun boundToTheSigningKey() {
        val key = object : ProofKey {
            private val pair = KeyPairGenerator.getInstance("EC").apply { initialize(ECGenParameterSpec("secp256r1")) }.generateKeyPair()
            override val publicKey: ECPublicKey = pair.public as ECPublicKey
            override fun signDer(data: ByteArray): ByteArray = throw UnsupportedOperationException("not used")
        }
        val factory = DpopProofFactory(key)
        assertEquals(GoogleNonce.of(P256Jwk.of(key.publicKey).thumbprint, sequenceSeed), GoogleNonce.of(factory, sequenceSeed))
    }
}
