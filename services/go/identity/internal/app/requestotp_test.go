package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// HandleRequestOtp already returned 500 / ERR_INTERNAL for infrastructure
// failures — the status and code were right. The body was not: it passed
// err.Error() straight through.
//
// RequestOtp propagates exactly one error, SaveOtp's, so everything reaching
// that branch is the repository's own text. This endpoint is unauthenticated and
// takes only an MSISDN, so anyone able to reach it could read whatever the store
// says when it fails.

func postRequestOtp(t *testing.T, h *IdentityHandler, msisdn string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"msisdn": msisdn})
	req := httptest.NewRequest(http.MethodPost, "/otp/request", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.HandleRequestOtp(rec, req)

	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	return rec, decoded
}

// TestRequestOtpAPI_SuccessResponseUnchanged pins the happy path.
func TestRequestOtpAPI_SuccessResponseUnchanged(t *testing.T) {
	t.Parallel()

	repo := &injectableRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()}
	h := newHandlerWith(repo)

	rec, body := postRequestOtp(t, h, "+919876543210")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if id, ok := body["requestId"].(string); !ok || !strings.HasPrefix(id, "req_") {
		t.Errorf("requestId = %v, want a req_-prefixed id", body["requestId"])
	}
	if body["expiresIn"] != float64(300) {
		t.Errorf("expiresIn = %v, want 300", body["expiresIn"])
	}
	if body["status"] != "PENDING_DELIVERY" {
		t.Errorf("status = %v, want PENDING_DELIVERY", body["status"])
	}
}

// TestRequestOtpAPI_SuccessDoesNotReturnTheCode re-pins the disclosure rule that
// matters most on this endpoint: the OTP itself must never be in the response.
func TestRequestOtpAPI_SuccessDoesNotReturnTheCode(t *testing.T) {
	t.Parallel()

	repo := &injectableRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()}
	h := newHandlerWith(repo)

	rec, body := postRequestOtp(t, h, "+919876543210")

	// The service is configured with a fixed test code, so its presence anywhere
	// in the body would be unambiguous.
	if strings.Contains(rec.Body.String(), testOtpCode) {
		t.Fatalf("the response contains the OTP: %s", rec.Body.String())
	}
	if _, present := body["code"]; present {
		t.Error("the response carries a \"code\" field")
	}
}

// TestRequestOtpAPI_SaveOtpFailureIsFiveHundred — status and code unchanged.
func TestRequestOtpAPI_SaveOtpFailureIsFiveHundred(t *testing.T) {
	t.Parallel()

	repo := &injectableRepo{MemoryIdentityRepository: NewMemoryIdentityRepository(), failSaveOtp: true}
	h := newHandlerWith(repo)

	rec, body := postRequestOtp(t, h, "+919876543210")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if got := body["code"]; got != "ERR_INTERNAL" {
		t.Errorf("code = %v, want ERR_INTERNAL", got)
	}
}

// TestRequestOtpAPI_FailureLeaksNoInternalText is the defect's regression test.
//
// The errInternalStore value is shaped like a real driver message and carries a host name,
// a database user and an authentication failure — the kind of detail that
// identifies infrastructure to someone probing an unauthenticated endpoint.
func TestRequestOtpAPI_FailureLeaksNoInternalText(t *testing.T) {
	t.Parallel()

	repo := &injectableRepo{MemoryIdentityRepository: NewMemoryIdentityRepository(), failSaveOtp: true}
	h := newHandlerWith(repo)

	rec, body := postRequestOtp(t, h, "+919876543210")
	whole := rec.Body.String()

	for _, leak := range []string{
		errInternalStore.Error(),
		"pq:", "db-prod-1", "password authentication", "identity\"",
	} {
		if strings.Contains(whole, leak) {
			t.Errorf("response leaks %q\n  body: %s", leak, whole)
		}
	}

	detail, _ := body["detail"].(string)
	if detail != "The request could not be processed. Please try again." {
		t.Errorf("detail = %q, want the fixed generic internal-error message", detail)
	}
}

// TestRequestOtpAPI_FailureDetailMatchesValidateOtp keeps the two endpoints
// describing the same condition identically. Divergent wording for one
// infrastructure failure is how a client ends up parsing prose.
func TestRequestOtpAPI_FailureDetailMatchesValidateOtp(t *testing.T) {
	t.Parallel()

	requestRepo := &injectableRepo{MemoryIdentityRepository: NewMemoryIdentityRepository(), failSaveOtp: true}
	_, requestBody := postRequestOtp(t, newHandlerWith(requestRepo), "+919876543210")

	validateRepo := &injectableRepo{MemoryIdentityRepository: NewMemoryIdentityRepository(), failGet: true}
	_, validateBody := postValidate(t, newHandlerWith(validateRepo), "req_1", "000000")

	if requestBody["detail"] != validateBody["detail"] {
		t.Errorf("the two endpoints describe the same infrastructure failure "+
			"differently:\n  request:  %v\n  validate: %v",
			requestBody["detail"], validateBody["detail"])
	}
	if requestBody["code"] != validateBody["code"] {
		t.Errorf("codes differ: %v vs %v", requestBody["code"], validateBody["code"])
	}
}

// TestRequestOtpAPI_BadInputStillFourHundred confirms the input-validation
// branch was not disturbed.
func TestRequestOtpAPI_BadInputStillFourHundred(t *testing.T) {
	t.Parallel()

	h := newHandlerWith(&injectableRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()})

	rec, body := postRequestOtp(t, h, "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if got := body["code"]; got != "ERR_INVALID_INPUT" {
		t.Errorf("code = %v, want ERR_INVALID_INPUT", got)
	}
}
