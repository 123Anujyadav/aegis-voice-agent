package com.callscreen.core.network.identity

/**
 * Certificate pins for one host, as ADR-0010 (accepted) requires: "Certificate
 * pinning in core/network against our own leaf and a backup pin. Pinning
 * without a backup pin is an outage waiting for a certificate rotation."
 *
 * So at least two distinct pins: the current key's and a backup. Each is
 * OkHttp's form, `sha256/` followed by the base64 SHA-256 of the certificate's
 * SubjectPublicKeyInfo. Pins are public values (hashes of public keys), not
 * secrets.
 */
public class CertificatePins private constructor(
    public val host: String,
    public val pins: List<String>,
) {
    override fun toString(): String = "CertificatePins(host=$host, pins=${pins.size})"

    public companion object {
        private val PIN = Regex("^sha256/[A-Za-z0-9+/]{43}=$")
        private val HOST = Regex("^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$")

        /**
         * Pins for [host] from [raw] (comma-separated), or null when [raw] is
         * empty: no pinning. A value that is set but unusable throws, so a
         * caller can refuse to connect rather than connect unpinned: fewer than
         * two pins, a malformed or repeated pin, or an invalid host.
         */
        public fun parse(host: String, raw: String): CertificatePins? {
            if (raw.isEmpty()) return null
            require(HOST.matches(host)) { "certificate pins need a lower-case host name" }
            val pins = raw.split(',').map { it.trim() }
            require(pins.all { PIN.matches(it) }) { "a certificate pin is not sha256/<base64 SHA-256>" }
            require(pins.toSet().size == pins.size) { "certificate pins must be distinct" }
            require(pins.size >= 2) { "certificate pinning needs at least two pins: the current key's and a backup (ADR-0010)" }
            return CertificatePins(host, pins)
        }
    }
}
