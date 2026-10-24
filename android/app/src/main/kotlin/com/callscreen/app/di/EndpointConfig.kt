package com.callscreen.app.di

import okhttp3.HttpUrl.Companion.toHttpUrlOrNull

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

    /** [raw] as a Google Cloud project number (a positive decimal identifier), or null. */
    fun cloudProjectNumber(raw: String): Long? {
        if (raw.isEmpty() || raw.length > 20 || !raw.all(Char::isDigit)) return null
        return raw.toLongOrNull()?.takeIf { it > 0 }
    }
}
