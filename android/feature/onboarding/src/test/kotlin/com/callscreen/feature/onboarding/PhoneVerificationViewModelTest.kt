package com.callscreen.feature.onboarding

import com.callscreen.core.auth.EnrolOutcome
import com.callscreen.core.network.identity.IdentityError
import com.callscreen.core.network.identity.IdentityResult
import com.callscreen.core.network.identity.OtpChallenge
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.TestScope
import kotlinx.coroutines.test.advanceUntilIdle
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertNull
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test

@OptIn(ExperimentalCoroutinesApi::class)
@DisplayName("Phone verification")
class PhoneVerificationViewModelTest {

    private val dispatcher = StandardTestDispatcher()
    private val requestId = "req_" + "a".repeat(64)

    private class FakeService(override val isConfigured: Boolean = true) : PhoneVerificationService {
        val numbers = mutableListOf<String>()
        val codes = mutableListOf<String>()
        var codeResult: IdentityResult<OtpChallenge> = IdentityResult.Success(OtpChallenge("req_" + "a".repeat(64), 300))
        var enrolResult: EnrolOutcome = EnrolOutcome.Enrolled
        var gate: CompletableDeferred<Unit>? = null

        override suspend fun requestCode(msisdn: String): IdentityResult<OtpChallenge> {
            numbers += msisdn
            gate?.await()
            return codeResult
        }

        override suspend fun enrol(requestId: String, code: String): EnrolOutcome {
            codes += code
            gate?.await()
            return enrolResult
        }
    }

    @BeforeEach
    fun setUp() = Dispatchers.setMain(dispatcher)

    @AfterEach
    fun tearDown() = Dispatchers.resetMain()

    private fun TestScope.atCodeStep(service: FakeService): PhoneVerificationViewModel {
        val vm = PhoneVerificationViewModel(service)
        vm.onNumberChange("+91 98765-43210")
        vm.submitNumber()
        advanceUntilIdle()
        return vm
    }

    @Test
    @DisplayName("a build with no identity server says so and sends nothing")
    fun notConfigured() = runTest(dispatcher) {
        val service = FakeService(isConfigured = false)
        val vm = PhoneVerificationViewModel(service)
        assertEquals(VerificationMessage.NOT_CONFIGURED, vm.state.value.message)
        vm.onNumberChange("+919876543210")
        vm.submitNumber()
        advanceUntilIdle()
        assertTrue(service.numbers.isEmpty())
    }

    @Test
    @DisplayName("an invalid number is refused locally; spaces and dashes are removed from a valid one")
    fun numberValidation() = runTest(dispatcher) {
        val service = FakeService()
        val vm = PhoneVerificationViewModel(service)
        vm.onNumberChange("98765 43210")
        vm.submitNumber()
        advanceUntilIdle()
        assertEquals(VerificationMessage.INVALID_NUMBER, vm.state.value.message)
        assertTrue(service.numbers.isEmpty())

        val at = atCodeStep(service)
        assertEquals(listOf("+919876543210"), service.numbers)
        assertEquals(VerificationStep.ENTER_CODE, at.state.value.step)
    }

    @Test
    @DisplayName("a double tap sends one request")
    fun noDuplicateSubmissions() = runTest(dispatcher) {
        val service = FakeService().apply { gate = CompletableDeferred() }
        val vm = PhoneVerificationViewModel(service)
        vm.onNumberChange("+919876543210")
        vm.submitNumber()
        advanceUntilIdle()
        assertTrue(vm.state.value.submitting)
        vm.submitNumber()
        vm.resendCode()
        advanceUntilIdle()
        assertEquals(1, service.numbers.size)
        service.gate!!.complete(Unit)
        advanceUntilIdle()
        assertFalse(vm.state.value.submitting)
    }

    @Test
    @DisplayName("a wrong code keeps the code step and clears the input; the right one verifies")
    fun codeFlow() = runTest(dispatcher) {
        val service = FakeService().apply { enrolResult = EnrolOutcome.Failed(IdentityError.InvalidOtp) }
        val vm = atCodeStep(service)
        vm.onCodeChange("12a34-56789")
        assertEquals("123456", vm.state.value.code)
        vm.submitCode()
        advanceUntilIdle()
        assertEquals(VerificationStep.ENTER_CODE, vm.state.value.step)
        assertEquals(VerificationMessage.INVALID_CODE, vm.state.value.message)
        assertEquals("", vm.state.value.code)

        service.enrolResult = EnrolOutcome.Enrolled
        vm.onCodeChange("654321")
        vm.submitCode()
        advanceUntilIdle()
        assertEquals(VerificationStep.VERIFIED, vm.state.value.step)
        assertEquals(listOf("123456", "654321"), service.codes)
    }

    @Test
    @DisplayName("an incomplete code is not sent")
    fun incompleteCode() = runTest(dispatcher) {
        val service = FakeService()
        val vm = atCodeStep(service)
        vm.onCodeChange("12345")
        vm.submitCode()
        advanceUntilIdle()
        assertTrue(service.codes.isEmpty())
    }

    @Test
    @DisplayName("server errors map to messages; rate limiting carries Retry-After")
    fun errorMessages() = runTest(dispatcher) {
        val cases = listOf(
            IdentityError.RateLimited(30) to VerificationMessage.TOO_MANY_ATTEMPTS,
            IdentityError.Network to VerificationMessage.OFFLINE,
            IdentityError.TemporarilyUnavailable to VerificationMessage.SERVICE_UNAVAILABLE,
            IdentityError.OtpDeliveryUnavailable to VerificationMessage.DELIVERY_UNAVAILABLE,
            IdentityError.OtpDeliveryFailed to VerificationMessage.DELIVERY_UNAVAILABLE,
            IdentityError.InvalidPhoneNumber to VerificationMessage.INVALID_NUMBER,
            IdentityError.UnexpectedResponse(500) to VerificationMessage.UNEXPECTED,
        )
        for ((error, message) in cases) {
            val service = FakeService().apply { codeResult = IdentityResult.Failure(error) }
            val vm = PhoneVerificationViewModel(service)
            vm.onNumberChange("+919876543210")
            vm.submitNumber()
            advanceUntilIdle()
            assertEquals(message, vm.state.value.message, "$error")
            assertEquals(VerificationStep.ENTER_NUMBER, vm.state.value.step)
            if (error is IdentityError.RateLimited) assertEquals(30L, vm.state.value.retryAfterSeconds)
        }
    }

    @Test
    @DisplayName("an expired code asks for a new one, and resending requests a new code for the same number")
    fun expiryAndResend() = runTest(dispatcher) {
        val service = FakeService().apply { enrolResult = EnrolOutcome.Failed(IdentityError.OtpExpired) }
        val vm = atCodeStep(service)
        vm.onCodeChange("123456")
        vm.submitCode()
        advanceUntilIdle()
        assertEquals(VerificationMessage.CODE_EXPIRED, vm.state.value.message)
        vm.resendCode()
        advanceUntilIdle()
        assertEquals(listOf("+919876543210", "+919876543210"), service.numbers)
        assertNull(vm.state.value.message)
    }

    @Test
    @DisplayName("a session that cannot be saved returns to the number step")
    fun storageFailed() = runTest(dispatcher) {
        val service = FakeService().apply { enrolResult = EnrolOutcome.StorageFailed }
        val vm = atCodeStep(service)
        vm.onCodeChange("123456")
        vm.submitCode()
        advanceUntilIdle()
        assertEquals(VerificationStep.ENTER_NUMBER, vm.state.value.step)
        assertEquals(VerificationMessage.COULD_NOT_SAVE, vm.state.value.message)
    }

    @Test
    @DisplayName("changing the number returns to the first step and forgets the challenge")
    fun editNumber() = runTest(dispatcher) {
        val service = FakeService()
        val vm = atCodeStep(service)
        vm.editNumber()
        assertEquals(VerificationStep.ENTER_NUMBER, vm.state.value.step)
        vm.onCodeChange("123456")
        vm.submitCode()
        advanceUntilIdle()
        assertTrue(service.codes.isEmpty(), "a code was sent without a challenge")
    }

    @Test
    @DisplayName("the state's toString never shows the code or the number")
    fun redaction() {
        val shown = PhoneVerificationState(number = "+919876543210", code = "123456").toString()
        assertFalse(shown.contains("123456") || shown.contains("9876543210"), shown)
        assertTrue(VerificationMessage.entries.none { describe(it, 30).contains(requestId) })
    }
}
