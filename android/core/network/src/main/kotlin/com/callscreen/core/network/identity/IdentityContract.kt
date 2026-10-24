package com.callscreen.core.network.identity

/*
 * The identity service's HTTP contract, as served by
 * services/go/identity/internal/httpapi (the package doc there is the source
 * of truth). Plain Kotlin: nothing here depends on Android, so the same
 * contract and error model can back a future non-Android client.
 *
 *   POST /v1/otp/request    {"msisdn"}                     -> 202 {"requestId","expiresInSeconds"}
 *   POST /v1/otp/validate   DPoP [+ Integrity-Token] {"requestId","code"}
 *                                                          -> 200 {"accessToken","tokenType","expiresIn","refreshToken","sessionId"}
 *   POST /v1/token/refresh  DPoP [+ Integrity-Token] {"sessionId","refreshToken"}
 *                                                          -> 200 {"accessToken","tokenType","expiresIn","refreshToken"}
 *
 * Errors are application/problem+json with a fixed "code" (IdentityError).
 */

/** The three endpoint paths (services/go/identity/internal/routes). */
public object IdentityRoutes {
    public const val OTP_REQUEST: String = "/v1/otp/request"
    public const val ENROLMENT: String = "/v1/otp/validate"
    public const val REFRESH: String = "/v1/token/refresh"
}

/** The server's input rules, applied before anything is sent. */
internal object IdentityInput {
    private val msisdn = Regex("^\\+[1-9][0-9]{6,14}$")
    private val requestId = Regex("^req_[0-9a-f]{64}$")
    private val sessionId = Regex("^sess_[0-9a-f]{64}$")
    private val refreshToken = Regex("^[0-9a-f]{64}$")
    private val code = Regex("^[0-9]{6}$")

    fun msisdn(s: String): Boolean = msisdn.matches(s)
    fun requestId(s: String): Boolean = requestId.matches(s)
    fun sessionId(s: String): Boolean = sessionId.matches(s)
    fun refreshToken(s: String): Boolean = refreshToken.matches(s)
    fun code(s: String): Boolean = code.matches(s)
}

/** An OTP challenge the server created. */
public data class OtpChallenge(val requestId: String, val expiresInSeconds: Long)

/**
 * The credentials a successful enrolment or refresh returns. toString never
 * shows a token, so logging one of these cannot leak it.
 */
public class IssuedTokens(
    public val accessToken: String,
    public val refreshToken: String,
    public val expiresInSeconds: Long,
    /** Present after enrolment; a refresh keeps the same session. */
    public val sessionId: String?,
) {
    override fun toString(): String =
        "IssuedTokens(accessToken=<redacted>, refreshToken=<redacted>, expiresInSeconds=$expiresInSeconds, " +
            "sessionId=${if (sessionId == null) "null" else "<redacted>"})"
}

/**
 * Every way a call can fail, from the server's fixed problem codes. No
 * variant carries response text, a token, a proof, a code or a phone number.
 */
public sealed interface IdentityError {
    /** The input was refused before anything was sent. */
    public data object InvalidInput : IdentityError
    public data object InvalidPhoneNumber : IdentityError
    public data class RateLimited(val retryAfterSeconds: Long?) : IdentityError
    public data object OtpDeliveryUnavailable : IdentityError
    public data object OtpDeliveryFailed : IdentityError
    public data object InvalidOtp : IdentityError
    public data object OtpExpired : IdentityError

    /** The session cannot be refreshed: sign in again. */
    public data object SessionEnded : IdentityError
    public data object InvalidDeviceProof : IdentityError
    public data object InvalidIntegrityToken : IdentityError
    public data object TemporarilyUnavailable : IdentityError

    /** The server answered with something outside the contract. */
    public data class UnexpectedResponse(val status: Int) : IdentityError

    /** No answer: connection failure, timeout, or the device is offline. */
    public data object Network : IdentityError

    public companion object {
        /** The error for a problem [code] with HTTP [status]; unknown codes are UnexpectedResponse. */
        public fun fromProblem(code: String?, status: Int, retryAfterSeconds: Long?): IdentityError = when (code) {
            "ERR_INVALID_MSISDN" -> InvalidPhoneNumber
            "ERR_RATE_LIMITED" -> RateLimited(retryAfterSeconds)
            "ERR_OTP_DELIVERY_UNAVAILABLE" -> OtpDeliveryUnavailable
            "ERR_OTP_DELIVERY_FAILED" -> OtpDeliveryFailed
            "ERR_INVALID_OTP" -> InvalidOtp
            "ERR_OTP_EXPIRED" -> OtpExpired
            "ERR_INVALID_TOKEN" -> SessionEnded
            "ERR_INVALID_DPOP_PROOF" -> InvalidDeviceProof
            "ERR_INVALID_INTEGRITY_TOKEN" -> InvalidIntegrityToken
            "ERR_TEMPORARILY_UNAVAILABLE" -> TemporarilyUnavailable
            "ERR_INVALID_INPUT", "ERR_MALFORMED_BODY", "ERR_UNSUPPORTED_MEDIA_TYPE", "ERR_BODY_TOO_LARGE" -> InvalidInput
            else -> UnexpectedResponse(status)
        }
    }
}

/** A call's outcome. */
public sealed interface IdentityResult<out T> {
    public data class Success<T>(val value: T) : IdentityResult<T>
    public data class Failure(val error: IdentityError) : IdentityResult<Nothing>
}

/** Signs the DPoP proof for one request (core:security's DpopProofFactory on Android). */
public fun interface DeviceProofs {
    public fun proof(method: String, htu: String): String
}

/**
 * Supplies a Play Integrity token bound to [proof] (ADR-0031), or null when
 * none is available; the request then carries no Integrity-Token and the
 * session is recorded as unverified. On Android, core:security's
 * PlayIntegrityTokens implements it (#143); the app uses it only when the
 * Google Cloud project number is configured (#146).
 */
public fun interface IntegrityTokens {
    public fun tokenFor(proof: String): String?
}
