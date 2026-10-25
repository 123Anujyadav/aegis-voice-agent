package com.callscreen.app.di

import com.callscreen.core.network.identity.CertificatePins
import com.callscreen.core.network.identity.IdentityClient
import com.callscreen.core.network.identity.IntegrityTokens
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.OkHttpClient

/**
 * The app's non-secret endpoint configuration, parsed strictly. An empty or
 * invalid value means "not configured": the feature it enables stays off, and
 * no host or project is ever assumed.
 */
internal object EndpointConfig {

    /** [raw] as an https origin (scheme, host, optional port; nothing else), or null. */
    fun httpsOrigin(raw: String): String? {
        if (raw.isEmpty()) return null
        val url = raw.trimEnd('/').toHttpUrlOrNull() ?: return null
        if (!url.isHttps || url.encodedPath != "/" || url.query != null || url.fragment != null ||
            url.username.isNotEmpty() || url.password.isNotEmpty()
        ) {
            return null
        }
        return url.toString().trimEnd('/')
    }

    /**
     * The integrity tokens for a configured project, or none. With no valid
     * project number, every request carries no Integrity-Token and identity
     * records the session as unverified. [create] is never called then, so no
     * Play Integrity work happens at all.
     */
    fun integrityTokens(rawProjectNumber: String, create: (Long) -> (String) -> String?): IntegrityTokens {
        val project = cloudProjectNumber(rawProjectNumber) ?: return IntegrityTokens { null }
        val tokenFor = create(project)
        return IntegrityTokens { proof -> tokenFor(proof) }
    }

    /**
     * The HTTP client for [origin] (an https origin from [httpsOrigin]),
     * pinned to [rawPins] when set (ADR-0010: the current key's pin and a
     * backup, comma-separated). Empty pins give the unpinned default client.
     * Pins that are set but unusable give null, so the feature stays off: a
     * misconfigured pin set never falls back to an unpinned connection.
     */
    fun httpClient(origin: String, rawPins: String): OkHttpClient? {
        val host = origin.toHttpUrlOrNull()?.host ?: return null
        val pins = try {
            CertificatePins.parse(host, rawPins)
        } catch (e: IllegalArgumentException) {
            return null
        }
        return IdentityClient.defaultHttpClient(pins)
    }

    /** [raw] as a Google Cloud project number (a positive decimal identifier), or null. */
    fun cloudProjectNumber(raw: String): Long? {
        if (raw.isEmpty() || raw.length > 20 || !raw.all(Char::isDigit)) return null
        return raw.toLongOrNull()?.takeIf { it > 0 }
    }
}
