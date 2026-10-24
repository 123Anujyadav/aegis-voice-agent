package com.callscreen.core.auth

import com.callscreen.core.network.identity.IdentityClient
import com.callscreen.core.network.identity.IdentityError
import java.io.IOException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.Serializable
import kotlinx.serialization.SerializationException
import kotlinx.serialization.json.Json
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response

/*
 * Requests to edge-api's protected routes, as services/go/edge-api/internal/auth
 * and internal/httpapi define them:
 *
 *   Authorization: DPoP <access token>     (the DPoP scheme only)
 *   DPoP: <proof>                          ES256, exactly {jti, htm, htu, iat, ath},
 *                                          htu = the configured public origin + the
 *                                          escaped path (no query), ath = SHA-256(token)
 *
 *   GET /v1/me -> 200 {"subject", "deviceKeyThumbprint"}; the only protected route today.
 *   401 ERR_UNAUTHENTICATED | ERR_INVALID_TOKEN | ERR_INVALID_DPOP_PROOF, 503 ERR_TEMPORARILY_UNAVAILABLE.
 */

/** Signs a resource-server proof: htu plus ath for [accessToken] (core:security's DpopProofFactory). */
public fun interface AccessTokenProofs {
    public fun proof(method: String, htu: String, accessToken: String): String
}

/** The caller as edge-api verified it. */
public data class EdgeIdentity(val subject: String, val deviceKeyThumbprint: String)

/** Why a protected request did not succeed. No variant carries response text or a token. */
public sealed interface EdgeError {
    /** There is no usable session: sign in. */
    public data class SignedOut(val reason: SignedOutReason) : EdgeError

    /** A token could not be obtained now (offline, identity unavailable); the session is kept. */
    public data class TokenUnavailable(val error: IdentityError) : EdgeError

    /** edge-api refused the token even after one refresh. */
    public data object TokenRejected : EdgeError

    /** edge-api refused the device proof (for example a clock far off). Not retried. */
    public data object ProofRejected : EdgeError
    public data object TemporarilyUnavailable : EdgeError
    public data object Network : EdgeError
    public data class UnexpectedResponse(val status: Int) : EdgeError
}

public sealed interface EdgeResult<out T> {
    public data class Success<T>(val value: T) : EdgeResult<T>
    public data class Failure(val error: EdgeError) : EdgeResult<Nothing>
}

/**
 * The protected edge-api client.
 *
 * - **Credentials go only to [publicOrigin].** URLs are built from it and a
 *   fixed path; no caller-supplied URL exists, so a token or proof can never
 *   reach another host. [publicOrigin] must be an https origin (loopback http
 *   only for tests), and it must equal edge-api's CS_EDGE_API_PUBLIC_ORIGIN,
 *   because that is what the proof's htu is checked against.
 * - **Redirects are never followed** (IdentityClient.defaultHttpClient).
 * - **One retry, only for an invalid token.** On ERR_INVALID_TOKEN the token
 *   is refreshed through [IdentityAuthenticator.refreshAfterRejection], which
 *   is single-flight, and the request is retried once with a fresh proof. A
 *   refused proof is never retried, and nothing loops.
 * - **Nothing is logged**, and errors carry no response text.
 */
public class EdgeApiClient(
    publicOrigin: String,
    private val authenticator: IdentityAuthenticator,
    private val proofs: AccessTokenProofs,
    private val httpClient: OkHttpClient = IdentityClient.defaultHttpClient(),
) {
    private val origin: String = run {
        val url: HttpUrl = requireNotNull(publicOrigin.trimEnd('/').toHttpUrlOrNull()) { "publicOrigin is not a URL" }
        require(url.isHttps || url.host in LOOPBACK) { "publicOrigin must be https" }
        require(url.encodedPath == "/" && url.query == null && url.username.isEmpty()) { "publicOrigin must be an origin" }
        url.toString().trimEnd('/')
    }

    /** GET /v1/me: who edge-api says the caller is. */
    public suspend fun me(): EdgeResult<EdgeIdentity> = get(ME_PATH) { body ->
        val r = json.decodeFromString(MeBody.serializer(), body)
        if (r.subject.isEmpty() || r.deviceKeyThumbprint.isEmpty()) null else EdgeIdentity(r.subject, r.deviceKeyThumbprint)
    }

    private suspend fun <T> get(path: String, parse: (String) -> T?): EdgeResult<T> {
        val first = when (val state = authenticator.accessToken()) {
            is AuthState.Authenticated -> state.accessToken
            is AuthState.SignedOut -> return EdgeResult.Failure(EdgeError.SignedOut(state.reason))
            is AuthState.Unavailable -> return EdgeResult.Failure(EdgeError.TokenUnavailable(state.error))
        }
        val outcome = send(path, first, parse)
        if (outcome !is Attempt.InvalidToken) return outcome.result()

        val second = when (val state = authenticator.refreshAfterRejection(first)) {
            is AuthState.Authenticated -> state.accessToken
            is AuthState.SignedOut -> return EdgeResult.Failure(EdgeError.SignedOut(state.reason))
            is AuthState.Unavailable -> return EdgeResult.Failure(EdgeError.TokenUnavailable(state.error))
        }
        return when (val retried = send(path, second, parse)) {
            is Attempt.InvalidToken -> EdgeResult.Failure(EdgeError.TokenRejected)
            else -> retried.result()
        }
    }

    private sealed interface Attempt<out T> {
        data class Done<T>(val result: EdgeResult<T>) : Attempt<T>
        data object InvalidToken : Attempt<Nothing>

        fun result(): EdgeResult<T> = when (this) {
            is Done -> result
            InvalidToken -> EdgeResult.Failure(EdgeError.TokenRejected)
        }
    }

    private suspend fun <T> send(path: String, token: String, parse: (String) -> T?): Attempt<T> = withContext(Dispatchers.IO) {
        val url = origin + path
        val request = Request.Builder()
            .url(url)
            .get()
            .header("Authorization", "DPoP $token")
            .header("DPoP", proofs.proof("GET", url, token))
            .header("Accept", "application/json, application/problem+json")
            .build()
        try {
            httpClient.newCall(request).execute().use { handle(it, parse) }
        } catch (e: IOException) {
            Attempt.Done(EdgeResult.Failure(EdgeError.Network))
        }
    }

    private fun <T> handle(response: Response, parse: (String) -> T?): Attempt<T> {
        val text = readBounded(response) ?: return Attempt.Done(EdgeResult.Failure(EdgeError.UnexpectedResponse(response.code)))
        val type = response.header("Content-Type").orEmpty().substringBefore(';').trim().lowercase()
        if (response.code >= 400) {
            val code = if (type == "application/problem+json") {
                try {
                    json.decodeFromString(ProblemBody.serializer(), text).code
                } catch (e: SerializationException) {
                    null
                } catch (e: IllegalArgumentException) {
                    null
                }
            } else {
                null
            }
            return when {
                response.code == 401 && code == "ERR_INVALID_TOKEN" -> Attempt.InvalidToken
                response.code == 401 && code == "ERR_INVALID_DPOP_PROOF" -> Attempt.Done(EdgeResult.Failure(EdgeError.ProofRejected))
                response.code == 401 -> Attempt.Done(EdgeResult.Failure(EdgeError.TokenRejected))
                response.code == 503 && code == "ERR_TEMPORARILY_UNAVAILABLE" -> Attempt.Done(EdgeResult.Failure(EdgeError.TemporarilyUnavailable))
                else -> Attempt.Done(EdgeResult.Failure(EdgeError.UnexpectedResponse(response.code)))
            }
        }
        if (response.code != 200 || type != "application/json") return Attempt.Done(EdgeResult.Failure(EdgeError.UnexpectedResponse(response.code)))
        val value = try {
            parse(text)
        } catch (e: SerializationException) {
            null
        } catch (e: IllegalArgumentException) {
            null
        }
        return Attempt.Done(if (value == null) EdgeResult.Failure(EdgeError.UnexpectedResponse(200)) else EdgeResult.Success(value))
    }

    private fun readBounded(response: Response): String? {
        val source = response.body?.source() ?: return ""
        source.request(MAX_RESPONSE_BYTES + 1)
        if (source.buffer.size > MAX_RESPONSE_BYTES) return null
        return source.buffer.readUtf8()
    }

    public companion object {
        /** The authenticated caller's identity endpoint (edge-api httpapi.MePath). */
        public const val ME_PATH: String = "/v1/me"
        internal const val MAX_RESPONSE_BYTES: Long = 16L * 1024
        private val LOOPBACK = setOf("localhost", "127.0.0.1", "::1")
        private val json = Json { ignoreUnknownKeys = true }
    }
}

@Serializable
private data class MeBody(val subject: String, val deviceKeyThumbprint: String)

@Serializable
private data class ProblemBody(val code: String? = null)
