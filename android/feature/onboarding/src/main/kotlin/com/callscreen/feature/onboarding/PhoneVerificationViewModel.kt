package com.callscreen.feature.onboarding

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.callscreen.core.auth.EnrolOutcome
import com.callscreen.core.network.identity.IdentityError
import com.callscreen.core.network.identity.IdentityResult
import com.callscreen.core.network.identity.OtpChallenge
import dagger.hilt.android.lifecycle.HiltViewModel
import javax.inject.Inject
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/**
 * What phone verification needs from the identity flow. The app binds it to
 * core:auth's IdentityAuthenticator. [isConfigured] is false in a build with
 * no identity base URL: sign-in is then unavailable, honestly, instead of
 * calling a made-up server.
 */
public interface PhoneVerificationService {
    public val isConfigured: Boolean
    public suspend fun requestCode(msisdn: String): IdentityResult<OtpChallenge>
    public suspend fun enrol(requestId: String, code: String): EnrolOutcome
}

public enum class VerificationStep { ENTER_NUMBER, ENTER_CODE, VERIFIED }

/** A message for the user. It never contains a code, a token or server text. */
public enum class VerificationMessage {
    NOT_CONFIGURED,
    INVALID_NUMBER,
    INVALID_CODE,
    CODE_EXPIRED,
    TOO_MANY_ATTEMPTS,
    DELIVERY_UNAVAILABLE,
    OFFLINE,
    SERVICE_UNAVAILABLE,
    COULD_NOT_SAVE,
    UNEXPECTED,
}

/** The screen's state. The OTP challenge id stays inside the ViewModel. */
public data class PhoneVerificationState(
    val step: VerificationStep = VerificationStep.ENTER_NUMBER,
    val number: String = "",
    val code: String = "",
    val submitting: Boolean = false,
    val message: VerificationMessage? = null,
    /** Seconds to wait before retrying, when the server said so. */
    val retryAfterSeconds: Long? = null,
) {
    /** The code is never shown in logs or diagnostics. */
    override fun toString(): String =
        "PhoneVerificationState(step=$step, numberLength=${number.length}, codeLength=${code.length}, " +
            "submitting=$submitting, message=$message, retryAfterSeconds=$retryAfterSeconds)"
}

/**
 * Phone verification: request a code, enter it, enrol this device.
 *
 * Submissions are ignored while one is in flight, so a double tap sends one
 * request. Input is checked before anything is sent: an E.164 number (spaces
 * and dashes are removed) and a 6-digit code.
 */
@HiltViewModel
public class PhoneVerificationViewModel @Inject constructor(
    private val service: PhoneVerificationService,
) : ViewModel() {

    private val mutable = MutableStateFlow(
        PhoneVerificationState(message = if (service.isConfigured) null else VerificationMessage.NOT_CONFIGURED),
    )
    public val state: StateFlow<PhoneVerificationState> = mutable.asStateFlow()

    private var requestId: String? = null

    public fun onNumberChange(input: String) {
        mutable.update { it.copy(number = input.filter { c -> c.isDigit() || c == '+' || c == ' ' || c == '-' }.take(20), message = null) }
    }

    public fun onCodeChange(input: String) {
        mutable.update { it.copy(code = input.filter(Char::isDigit).take(CODE_LENGTH), message = null) }
    }

    /** Requests a code for the entered number. */
    public fun submitNumber() {
        val current = mutable.value
        if (current.submitting || !service.isConfigured) return
        val msisdn = normalised(current.number)
        if (msisdn == null) {
            mutable.update { it.copy(message = VerificationMessage.INVALID_NUMBER) }
            return
        }
        send(msisdn)
    }

    /** Requests a new code for the same number (after expiry or a failed delivery). */
    public fun resendCode() {
        val current = mutable.value
        if (current.submitting || !service.isConfigured) return
        normalised(current.number)?.let(::send)
    }

    /** Goes back to change the number. */
    public fun editNumber() {
        if (mutable.value.submitting) return
        requestId = null
        mutable.update { it.copy(step = VerificationStep.ENTER_NUMBER, code = "", message = null, retryAfterSeconds = null) }
    }

    /** Verifies the entered code. */
    public fun submitCode() {
        val current = mutable.value
        val id = requestId
        if (current.submitting || id == null || current.code.length != CODE_LENGTH) return
        mutable.update { it.copy(submitting = true, message = null, retryAfterSeconds = null) }
        viewModelScope.launch {
            val outcome = service.enrol(id, current.code)
            mutable.update {
                when (outcome) {
                    EnrolOutcome.Enrolled -> it.copy(step = VerificationStep.VERIFIED, submitting = false, code = "")
                    EnrolOutcome.StorageFailed -> {
                        requestId = null
                        it.copy(step = VerificationStep.ENTER_NUMBER, submitting = false, code = "", message = VerificationMessage.COULD_NOT_SAVE)
                    }
                    is EnrolOutcome.Failed -> failed(it.copy(submitting = false, code = ""), outcome.error)
                }
            }
        }
    }

    private fun send(msisdn: String) {
        mutable.update { it.copy(submitting = true, message = null, retryAfterSeconds = null) }
        viewModelScope.launch {
            val result = service.requestCode(msisdn)
            mutable.update {
                when (result) {
                    is IdentityResult.Success -> {
                        requestId = result.value.requestId
                        it.copy(step = VerificationStep.ENTER_CODE, submitting = false, code = "")
                    }
                    is IdentityResult.Failure -> failed(it.copy(submitting = false), result.error)
                }
            }
        }
    }

    private fun failed(state: PhoneVerificationState, error: IdentityError): PhoneVerificationState = when (error) {
        IdentityError.InvalidPhoneNumber -> state.copy(message = VerificationMessage.INVALID_NUMBER)
        IdentityError.InvalidOtp -> state.copy(message = VerificationMessage.INVALID_CODE)
        IdentityError.OtpExpired -> state.copy(message = VerificationMessage.CODE_EXPIRED)
        is IdentityError.RateLimited -> state.copy(message = VerificationMessage.TOO_MANY_ATTEMPTS, retryAfterSeconds = error.retryAfterSeconds)
        IdentityError.OtpDeliveryUnavailable, IdentityError.OtpDeliveryFailed -> state.copy(message = VerificationMessage.DELIVERY_UNAVAILABLE)
        IdentityError.Network -> state.copy(message = VerificationMessage.OFFLINE)
        IdentityError.TemporarilyUnavailable -> state.copy(message = VerificationMessage.SERVICE_UNAVAILABLE)
        else -> state.copy(message = VerificationMessage.UNEXPECTED)
    }

    internal companion object {
        const val CODE_LENGTH = 6
        private val E164 = Regex("^\\+[1-9][0-9]{6,14}$")

        /** The number in E.164 form, or null. Spaces and dashes are removed; nothing else is assumed. */
        fun normalised(input: String): String? = input.filterNot { it == ' ' || it == '-' }.takeIf(E164::matches)
    }
}
