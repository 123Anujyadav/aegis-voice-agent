package com.callscreen.core.security.dpop

import java.security.SecureRandom

/**
 * The nonce a Google sign-in requests from Google (ADR-0035 D35.4), exactly as
 * identity recomputes it (services/go/identity/internal/app/google_signin.go,
 * GoogleNonce):
 *
 *   nonce = base64url_nopad(SHA-256("lily.identity.google-nonce.v1" || 0x00 ||
 *                                   jkt || 0x00 || seed))
 *
 * [jkt] is the RFC 7638 thumbprint of the device key that signs the sign-in's
 * DPoP proof ([P256Jwk.thumbprint], [DpopProofFactory.jwk]), and the seed is
 * 32 fresh random bytes, unpadded base64url ([newSeed]); the request sends
 * the seed as `nonceSeed`. Binding the nonce to the key means an ID token
 * Google issued for this sign-in is useless with any other device key.
 *
 * Neither input is a secret, but neither is logged: the seed is single-use
 * per sign-in attempt, and nothing here keeps it.
 */
public object GoogleNonce {
    /** The length of a seed: 32 bytes as unpadded base64url. */
    public const val SEED_LENGTH: Int = 43

    private const val DOMAIN = "lily.identity.google-nonce.v1\u0000"
    private const val SEED_BYTES = 32

    /** The seed form identity accepts (app.ValidNonceSeed). */
    private val SEED = Regex("^[A-Za-z0-9_-]{43}$")

    /** A SHA-256 thumbprint as unpadded base64url. */
    private val JKT = Regex("^[A-Za-z0-9_-]{43}$")

    /** A fresh seed: 32 bytes from [random], unpadded base64url (43 characters). */
    public fun newSeed(random: SecureRandom = SecureRandom()): String = ByteArray(SEED_BYTES).also { random.nextBytes(it) }.let(::b64u)

    /** Whether [seed] has the form identity accepts as `nonceSeed`. */
    public fun isValidSeed(seed: String): Boolean = SEED.matches(seed)

    /**
     * The nonce for a sign-in whose proof is signed by the key with thumbprint
     * [jkt], with [seed]. Both must be well formed (unpadded base64url, 43
     * characters); anything else is refused rather than hashed.
     */
    public fun of(jkt: String, seed: String): String {
        require(JKT.matches(jkt)) { "jkt must be a base64url SHA-256 thumbprint" }
        require(SEED.matches(seed)) { "seed must be 32 bytes as unpadded base64url" }
        val input = DOMAIN.toByteArray(Charsets.US_ASCII) + jkt.toByteArray(Charsets.US_ASCII) +
            byteArrayOf(0) + seed.toByteArray(Charsets.US_ASCII)
        return b64u(sha256(input))
    }

    /** The nonce for a sign-in whose proofs [factory] signs. */
    public fun of(factory: DpopProofFactory, seed: String): String = of(factory.jwk.thumbprint, seed)
}
