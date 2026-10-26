/**
 * The public endpoint configuration a RELEASE build must carry: the identity
 * and edge-api origins and their certificate pins (Gradle properties
 * callscreen.identityBaseUrl, callscreen.edgeApiBaseUrl,
 * callscreen.identityCertPins, callscreen.edgeApiCertPins; configuration, NOT
 * secrets).
 *
 * Every other build treats an empty value as "not configured": the feature
 * stays off and no host is assumed. A release cannot: with no identity origin
 * the app stops at phone verification ("Sign-in is not available in this
 * build") and nothing behind it is reachable, and an unpinned release breaks
 * ADR-0010's pinning rule (the current key's pin and a backup). The release
 * script therefore sets callscreen.requireReleaseEndpoints=true, and the build
 * fails, naming each property, unless all four are usable.
 *
 * The rules mirror the app's own parsing (EndpointConfig.httpsOrigin and
 * core:network CertificatePins.parse), so a value this accepts is one the app
 * will use rather than silently switch off; one rule is stricter (no IP
 * address as a release host). The edge-api origin must also equal
 * edge-api's CS_EDGE_API_PUBLIC_ORIGIN, which only the deployment can check.
 */
internal object ReleaseEndpoints {

    const val IDENTITY_BASE_URL: String = "callscreen.identityBaseUrl"
    const val EDGE_API_BASE_URL: String = "callscreen.edgeApiBaseUrl"
    const val IDENTITY_CERT_PINS: String = "callscreen.identityCertPins"
    const val EDGE_API_CERT_PINS: String = "callscreen.edgeApiCertPins"

    /** Set to "true" by the release script: the build then requires all four. */
    const val REQUIRED: String = "callscreen.requireReleaseEndpoints"

    // CertificatePins: a lower-case host name (pinning needs one), and
    // sha256/ followed by the base64 of a 32-byte SHA-256.
    private val HOST = Regex("^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$")
    private val PIN = Regex("^sha256/[A-Za-z0-9+/]{43}=$")

    /**
     * What is wrong with the release configuration [read] supplies (a property
     * name to its value, or null when unset), one message per property, or
     * nothing. A message names the property and the rule, never the value.
     */
    fun problems(read: (String) -> String?): List<String> = buildList {
        for ((url, pins) in listOf(IDENTITY_BASE_URL to IDENTITY_CERT_PINS, EDGE_API_BASE_URL to EDGE_API_CERT_PINS)) {
            originProblem(read(url).orEmpty())?.let { add("$url $it") }
            pinsProblem(read(pins).orEmpty())?.let { add("$pins $it") }
        }
    }

    private fun originProblem(raw: String): String? {
        if (raw.isEmpty()) return "is not set"
        val rest = raw.removePrefix("https://")
        if (rest == raw) return "must be an https origin"
        val authority = rest.trimEnd('/')
        if (authority.isEmpty() || authority.any { it == '/' || it == '?' || it == '#' || it == '@' }) {
            return "must be an origin only (scheme, host and optional port; no path, query or credentials)"
        }
        val host = authority.substringBefore(':')
        val port = authority.substringAfter(':', missingDelimiterValue = "")
        if (!HOST.matches(host)) return "must name a lower-case DNS host (certificate pinning needs one)"
        // Stricter than the app, which would accept it: a release names a DNS
        // host, never an IP address (a loopback or test address).
        if (host.split('.').all { label -> label.all(Char::isDigit) }) return "must name a DNS host, not an IP address"
        if (authority.contains(':') && (port.isEmpty() || port.length > 5 || !port.all(Char::isDigit) || port.toInt() !in 1..65535)) {
            return "has an invalid port"
        }
        return null
    }

    private fun pinsProblem(raw: String): String? {
        if (raw.isEmpty()) return "is not set (ADR-0010: the current key's pin and a backup)"
        val pins = raw.split(',').map { it.trim() }
        if (!pins.all { PIN.matches(it) }) return "must be comma-separated sha256/<base64 SHA-256> pins"
        if (pins.toSet().size != pins.size) return "must not repeat a pin"
        if (pins.size < 2) return "needs at least two pins: the current key's and a backup (ADR-0010)"
        return null
    }
}
