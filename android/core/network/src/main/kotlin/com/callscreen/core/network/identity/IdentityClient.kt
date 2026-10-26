package com.callscreen.core.network.identity

import java.io.IOException
import java.util.concurrent.TimeUnit
import kotlinx.serialization.Serializable
import kotlinx.serialization.SerializationException
import kotlinx.serialization.json.Json
import okhttp3.CertificatePinner
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response

/**
 * The identity HTTP client. Blocking: call it off the main thread.
 *
 * - **Nothing sensitive is logged.** No logging interceptor is added, and
 *   [httpClient] must not have a body-logging one: requests carry codes,
 *   proofs and refresh tokens, and responses carry tokens.
 * - **Redirects are never followed** by [defaultHttpClient], so a proof or a
 *   token goes only to [baseUrl].
 * - **[baseUrl] must be https**; plain http is accepted only for a loopback
 *   host, which only tests use.
 * - **The proof's htu** is [baseUrl] plus the route. In production that is
 *   identity's configured canonical URL (`CS_IDENTITY_ENROLLMENT_CANONICAL_URL`,
 *   `CS_IDENTITY_REFRESH_CANONICAL_URL`), which the app must be configured to
 *   match exactly. Deletion's is the refresh URL's origin plus its route.
 */
public class IdentityClient(
    baseUrl: String,
    private val proofs: DeviceProofs,
    private val integrity: IntegrityTokens = IntegrityTokens { null },
    private val httpClient: OkHttpClient = defaultHttpClient(),
    private val refreshIntegrityIntervalSeconds: Long = REFRESH_INTEGRITY_INTERVAL_SECONDS,
    private val nowSeconds: () -> Long = { System.currentTimeMillis() / 1000 },
) {
    /**
     * When this client last asked for an integrity token (epoch seconds), or
     * null if never in this process. ADR-0031 D31.4: a token is attached at
     * enrolment, and on refresh at most once per [refreshIntegrityIntervalSeconds],
     * because Google's default quota (10,000 requests per app per day) would
     * otherwise run out at about 100 subscribers refreshing every 15 minutes.
     * A refresh without a token keeps the session's tier on the server.
     */
    @Volatile
    private var lastIntegrityAttempt: Long? = null

    private val base: HttpUrl = requireNotNull(baseUrl.trimEnd('/').toHttpUrlOrNull()) { "baseUrl is not a URL" }.also {
        require(it.isHttps || it.host in LOOPBACK) { "baseUrl must be https" }
        require(it.encodedPath == "/" && it.query == null && it.username.isEmpty()) { "baseUrl must be an origin" }
    }
    private val origin = base.toString().trimEnd('/')

    /** Requests an OTP for [msisdn] (E.164). */
    public fun requestOtp(msisdn: String): IdentityResult<OtpChallenge> {
        if (!IdentityInput.msisdn(msisdn)) return IdentityResult.Failure(IdentityError.InvalidInput)
        return call(IdentityRoutes.OTP_REQUEST, json.encodeToString(OtpRequestBody.serializer(), OtpRequestBody(msisdn)), proof = null) { status, body ->
            if (status != 202) return@call null
            val r = json.decodeFromString(OtpChallengeBody.serializer(), body)
            if (!IdentityInput.requestId(r.requestId) || r.expiresInSeconds <= 0) null else OtpChallenge(r.requestId, r.expiresInSeconds)
        }
    }

    /** Validates [code] for [requestId], enrolling this device's key. */
    public fun enrol(requestId: String, code: String): IdentityResult<IssuedTokens> {
        if (!IdentityInput.requestId(requestId) || !IdentityInput.code(code)) return IdentityResult.Failure(IdentityError.InvalidInput)
        val proof = proofs.proof(METHOD, origin + IdentityRoutes.ENROLMENT)
        return call(IdentityRoutes.ENROLMENT, json.encodeToString(EnrolBody.serializer(), EnrolBody(requestId, code)), proof) { status, body ->
            if (status != 200) return@call null
            val r = json.decodeFromString(EnrolResponse.serializer(), body)
            tokens(r.accessToken, r.tokenType, r.expiresIn, r.refreshToken)?.takeIf { IdentityInput.sessionId(r.sessionId) }
                ?.let { IssuedTokens(it.accessToken, it.refreshToken, it.expiresInSeconds, r.sessionId) }
        }
    }

    /** Rotates [refreshToken] for [sessionId], returning a new access and refresh token. */
    public fun refresh(sessionId: String, refreshToken: String): IdentityResult<IssuedTokens> {
        if (!IdentityInput.sessionId(sessionId) || !IdentityInput.refreshToken(refreshToken)) return IdentityResult.Failure(IdentityError.InvalidInput)
        val proof = proofs.proof(METHOD, origin + IdentityRoutes.REFRESH)
        return call(IdentityRoutes.REFRESH, json.encodeToString(RefreshBody.serializer(), RefreshBody(sessionId, refreshToken)), proof) { status, body ->
            if (status != 200) return@call null
            val r = json.decodeFromString(RefreshResponse.serializer(), body)
            tokens(r.accessToken, r.tokenType, r.expiresIn, r.refreshToken)
        }
    }

    /**
     * Deletes the account of [sessionId]'s subscriber, on every device
     * (docs/phase19/ACCOUNT-DELETION.md). Authenticated as a refresh is: the
     * current [refreshToken] and a proof by the enrolled key. Success only on
     * the server's 204. A retry after a lost answer is answered 204 again.
     * No integrity token is sent: deletion does not use one.
     */
    public fun deleteAccount(sessionId: String, refreshToken: String): IdentityResult<Unit> {
        if (!IdentityInput.sessionId(sessionId) || !IdentityInput.refreshToken(refreshToken)) return IdentityResult.Failure(IdentityError.InvalidInput)
        val proof = proofs.proof(METHOD, origin + IdentityRoutes.ACCOUNT_DELETION)
        return call(IdentityRoutes.ACCOUNT_DELETION, json.encodeToString(RefreshBody.serializer(), RefreshBody(sessionId, refreshToken)), proof) { status, _ ->
            if (status == 204) Unit else null
        }
    }

    private fun tokens(access: String, type: String, expiresIn: Long, refresh: String): IssuedTokens? =
        if (access.isEmpty() || type != "DPoP" || expiresIn <= 0 || !IdentityInput.refreshToken(refresh)) {
            null
        } else {
            IssuedTokens(access, refresh, expiresIn, sessionId = null)
        }

    /**
     * One POST. [success] maps a non-error response, returning null for one
     * outside the contract. Problem responses map to [IdentityError]; nothing
     * from a response body ever reaches an error.
     */
    /**
     * Whether this request should carry an integrity token: always at
     * enrolment; on refresh only if none was requested for at least the
     * interval (or ever, in this process). An attempt counts whether or not a
     * token was obtained: a device that cannot get one does not ask Google
     * every 15 minutes. A clock that went backwards counts as due.
     */
    private fun integrityDue(route: String): Boolean {
        if (route == IdentityRoutes.ENROLMENT) return true
        if (route == IdentityRoutes.ACCOUNT_DELETION) return false
        val last = lastIntegrityAttempt ?: return true
        val now = nowSeconds()
        return now < last || now - last >= refreshIntegrityIntervalSeconds
    }

    private fun <T> call(route: String, body: String, proof: String?, success: (Int, String) -> T?): IdentityResult<T> {
        val request = Request.Builder()
            .url(origin + route)
            .post(body.toRequestBody(JSON))
            .header("Accept", "application/json, application/problem+json")
            .apply {
                if (proof != null) {
                    header("DPoP", proof)
                    if (integrityDue(route)) {
                        lastIntegrityAttempt = nowSeconds()
                        integrity.tokenFor(proof)?.takeIf { it.isNotEmpty() }?.let { header(INTEGRITY_HEADER, it) }
                    }
                }
            }
            .build()
        return try {
            httpClient.newCall(request).execute().use { response -> handle(response, success) }
        } catch (e: IOException) {
            IdentityResult.Failure(IdentityError.Network)
        }
    }

    private fun <T> handle(response: Response, success: (Int, String) -> T?): IdentityResult<T> {
        val text = readBounded(response) ?: return IdentityResult.Failure(IdentityError.UnexpectedResponse(response.code))
        val type = response.header("Content-Type").orEmpty().substringBefore(';').trim().lowercase()
        if (response.code >= 400) {
            if (type != "application/problem+json") return IdentityResult.Failure(IdentityError.UnexpectedResponse(response.code))
            val problem = try {
                json.decodeFromString(ProblemBody.serializer(), text)
            } catch (e: SerializationException) {
                return IdentityResult.Failure(IdentityError.UnexpectedResponse(response.code))
            } catch (e: IllegalArgumentException) {
                return IdentityResult.Failure(IdentityError.UnexpectedResponse(response.code))
            }
            val retryAfter = response.header("Retry-After")?.trim()?.toLongOrNull()?.takeIf { it >= 0 }
            return IdentityResult.Failure(IdentityError.fromProblem(problem.code, response.code, retryAfter))
        }
        // 204 has no body and no content type; only deletion expects it.
        if (response.code == 204) return success(204, "")?.let { IdentityResult.Success(it) } ?: IdentityResult.Failure(IdentityError.UnexpectedResponse(204))
        if (type != "application/json") return IdentityResult.Failure(IdentityError.UnexpectedResponse(response.code))
        val value = try {
            success(response.code, text)
        } catch (e: SerializationException) {
            null
        } catch (e: IllegalArgumentException) {
            null
        }
        return if (value == null) IdentityResult.Failure(IdentityError.UnexpectedResponse(response.code)) else IdentityResult.Success(value)
    }

    /** The body as text, or null if it exceeds [MAX_RESPONSE_BYTES]. */
    private fun readBounded(response: Response): String? {
        val source = response.body?.source() ?: return ""
        source.request(MAX_RESPONSE_BYTES + 1)
        if (source.buffer.size > MAX_RESPONSE_BYTES) return null
        return source.buffer.readUtf8()
    }

    public companion object {
        private const val METHOD = "POST"
        private const val INTEGRITY_HEADER = "Integrity-Token"

        /** ADR-0031 D31.4: on refresh, an integrity token at most once every 6 hours. */
        public const val REFRESH_INTEGRITY_INTERVAL_SECONDS: Long = 6L * 60 * 60
        private val JSON = "application/json".toMediaType()
        private val LOOPBACK = setOf("localhost", "127.0.0.1", "::1")

        /** A response is a few hundred bytes; anything far larger is refused unread. */
        internal const val MAX_RESPONSE_BYTES: Long = 16L * 1024

        private val json = Json { ignoreUnknownKeys = true }

        /**
         * A client with bounded timeouts that never follows redirects. Add no
         * body-logging interceptor to it. With [pins], a connection to that
         * host must present one of the pinned keys (ADR-0010); a mismatch is a
         * connection failure, reported as [IdentityError.Network].
         */
        public fun defaultHttpClient(pins: CertificatePins? = null): OkHttpClient = OkHttpClient.Builder()
            .connectTimeout(10, TimeUnit.SECONDS)
            .readTimeout(15, TimeUnit.SECONDS)
            .writeTimeout(15, TimeUnit.SECONDS)
            .callTimeout(30, TimeUnit.SECONDS)
            .followRedirects(false)
            .followSslRedirects(false)
            .apply {
                if (pins != null) certificatePinner(CertificatePinner.Builder().add(pins.host, *pins.pins.toTypedArray()).build())
            }
            .build()
    }
}

@Serializable
private data class OtpRequestBody(val msisdn: String)

@Serializable
private data class OtpChallengeBody(val requestId: String, val expiresInSeconds: Long)

@Serializable
private data class EnrolBody(val requestId: String, val code: String)

@Serializable
private data class RefreshBody(val sessionId: String, val refreshToken: String)

@Serializable
private data class EnrolResponse(
    val accessToken: String,
    val tokenType: String,
    val expiresIn: Long,
    val refreshToken: String,
    val sessionId: String,
)

@Serializable
private data class RefreshResponse(
    val accessToken: String,
    val tokenType: String,
    val expiresIn: Long,
    val refreshToken: String,
)

@Serializable
private data class ProblemBody(val code: String? = null)
