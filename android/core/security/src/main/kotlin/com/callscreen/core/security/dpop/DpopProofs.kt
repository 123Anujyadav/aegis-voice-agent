package com.callscreen.core.security.dpop

import java.math.BigInteger
import java.security.MessageDigest
import java.security.SecureRandom
import java.security.interfaces.ECPublicKey
import java.util.Base64

/*
 * Device-key proofs (DPoP, RFC 9449) in exactly the profile identity and
 * edge-api accept (packages/go/dpop, ADR-0029 D11.4):
 *
 *   header  exactly {"typ":"dpop+jwt","alg":"ES256","jwk":{"kty","crv","x","y"}}
 *   claims  exactly {"jti","htm","htu","iat"} at enrolment and refresh,
 *           plus "ath" on a request that presents an access token
 *   signature  ES256 in the raw 64-byte R||S form (RFC 7518 §3.4). The server
 *           REFUSES the DER form, which is what Android Keystore produces, so
 *           every signature is converted (Es256.derToRaw).
 *
 * The private key never reaches this code: a ProofKey signs. On a device that
 * is a non-exportable Android Keystore key (KeystoreProofKey).
 */

/** A P-256 key that signs proofs. Its private half never leaves the implementation. */
public interface ProofKey {
    /** The public key embedded in every proof as its jwk. */
    public val publicKey: ECPublicKey

    /** SHA256withECDSA over [data], in the DER form java.security and Android Keystore produce. */
    public fun signDer(data: ByteArray): ByteArray
}

/** Unpadded base64url, the only encoding the server accepts. */
internal fun b64u(bytes: ByteArray): String = Base64.getUrlEncoder().withoutPadding().encodeToString(bytes)

internal fun sha256(bytes: ByteArray): ByteArray = MessageDigest.getInstance("SHA-256").digest(bytes)

/** The jwk of a P-256 public key, and its RFC 7638 thumbprint. */
public data class P256Jwk(val x: String, val y: String) {
    /** The RFC 7638 SHA-256 thumbprint: the session's cnf.jkt. */
    public val thumbprint: String
        get() = b64u(sha256("""{"crv":"P-256","kty":"EC","x":"$x","y":"$y"}""".toByteArray(Charsets.US_ASCII)))

    internal fun json(): String = """{"kty":"EC","crv":"P-256","x":"$x","y":"$y"}"""

    public companion object {
        /** The jwk of [key], which must be a P-256 key. */
        public fun of(key: ECPublicKey): P256Jwk {
            // The field size alone would admit other 256-bit curves
            // (secp256k1); the group order identifies P-256 exactly.
            require(key.params.order == P256_ORDER && key.params.cofactor == 1) { "the device key must be P-256" }
            return P256Jwk(b64u(fixed32(key.w.affineX)), b64u(fixed32(key.w.affineY)))
        }

        /** The order n of the P-256 group (FIPS 186-4 D.1.2.3). */
        private val P256_ORDER = BigInteger("ffffffff00000000ffffffffffffffffbce6faada7179e84f3b9cac2fc632551", 16)
    }
}

/** An unsigned integer as exactly 32 big-endian bytes. */
internal fun fixed32(n: BigInteger): ByteArray {
    val raw = n.toByteArray()
    return when {
        raw.size == 32 -> raw
        raw.size == 33 && raw[0] == 0.toByte() -> raw.copyOfRange(1, 33)
        raw.size < 32 -> ByteArray(32 - raw.size) + raw
        else -> throw IllegalArgumentException("value does not fit in 32 bytes")
    }
}

/** ES256 signature encodings. */
public object Es256 {
    /**
     * The raw 64-byte R||S form of a DER ECDSA-Sig-Value
     * (SEQUENCE { INTEGER r, INTEGER s }). Strict: anything that is not exactly
     * that, with r and s positive and at most 32 bytes, is refused rather than
     * guessed at.
     */
    public fun derToRaw(der: ByteArray): ByteArray {
        var i = 0
        fun byte(): Int {
            require(i < der.size) { "truncated DER signature" }
            return der[i++].toInt() and 0xff
        }
        fun length(): Int {
            val first = byte()
            if (first < 0x80) return first
            require(first == 0x81) { "unsupported DER length" }
            val n = byte()
            require(n >= 0x80) { "non-minimal DER length" }
            return n
        }
        fun integer(): ByteArray {
            require(byte() == 0x02) { "DER signature: expected INTEGER" }
            val len = length()
            require(len in 1..33 && i + len <= der.size) { "DER signature: bad INTEGER length" }
            val v = der.copyOfRange(i, i + len)
            i += len
            require((v[0].toInt() and 0x80) == 0) { "DER signature: negative INTEGER" }
            require(!(v.size > 1 && v[0] == 0.toByte() && (v[1].toInt() and 0x80) == 0)) { "DER signature: non-minimal INTEGER" }
            return fixed32(BigInteger(1, v))
        }
        require(byte() == 0x30) { "DER signature: expected SEQUENCE" }
        val seqLen = length()
        require(i + seqLen == der.size) { "DER signature: length mismatch or trailing bytes" }
        val r = integer()
        val s = integer()
        require(i == der.size) { "DER signature: trailing bytes" }
        return r + s
    }
}

/**
 * Builds proofs for one device key. [nowSeconds] and [jti] are injectable for
 * tests; the defaults are the system clock and 32 random bytes.
 */
public class DpopProofFactory(
    private val key: ProofKey,
    private val nowSeconds: () -> Long = { System.currentTimeMillis() / 1000 },
    private val jti: () -> String = { randomJti() },
) {
    /** The key's jwk and thumbprint: what enrolment binds the session to. */
    public val jwk: P256Jwk = P256Jwk.of(key.publicKey)

    /**
     * A proof for one request. [htu] must be the server's canonical https URL
     * for the endpoint, byte for byte (the server does no normalisation).
     * [accessToken] is given only on a request that presents one, and adds
     * ath = base64url(SHA-256(token)).
     */
    public fun proof(method: String, htu: String, accessToken: String? = null): String {
        require(METHOD.matches(method)) { "method must be an upper-case HTTP method" }
        require(htu.startsWith("https://") && htu.none { it.code < 0x21 || it.code > 0x7e || it == '"' || it == '\\' }) {
            "htu must be a canonical https URL"
        }
        val id = jti()
        require(id.length in 1..MAX_JTI && id.all { it.code in 0x21..0x7e && it != '"' && it != '\\' }) {
            "jti must be 1..$MAX_JTI printable ASCII characters"
        }
        val header = """{"typ":"dpop+jwt","alg":"ES256","jwk":${jwk.json()}}"""
        val claims = buildString {
            append("""{"jti":"$id","htm":"$method","htu":"$htu","iat":${nowSeconds()}""")
            if (accessToken != null) {
                require(accessToken.isNotEmpty()) { "an access token must not be empty" }
                append(""","ath":"${b64u(sha256(accessToken.toByteArray(Charsets.US_ASCII)))}"""")
            }
            append('}')
        }
        val input = b64u(header.toByteArray(Charsets.UTF_8)) + "." + b64u(claims.toByteArray(Charsets.UTF_8))
        val signature = Es256.derToRaw(key.signDer(input.toByteArray(Charsets.US_ASCII)))
        return input + "." + b64u(signature)
    }

    private companion object {
        val METHOD = Regex("^[A-Z]{3,7}$")
        const val MAX_JTI = 128
    }
}

/** 32 random bytes, base64url: 43 printable characters. */
internal fun randomJti(): String = ByteArray(32).also { SecureRandom().nextBytes(it) }.let(::b64u)

/**
 * The Play Integrity requestHash for a request carrying [proof] (ADR-0031
 * D31.1): base64url(SHA-256(the DPoP proof exactly as sent)).
 */
public fun integrityRequestHash(proof: String): String = b64u(sha256(proof.toByteArray(Charsets.US_ASCII)))
