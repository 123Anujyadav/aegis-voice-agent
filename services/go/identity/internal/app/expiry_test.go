package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// docs/domain/10-identity.md, VerificationChallenge validation rules:
//
//	"Expiry and wrong-code are distinct outcomes — collapsing them makes an
//	 expired code look like user error."
//
// Both used to return ErrInvalidOtp, whose message even read "invalid or
// expired OTP code" — the conflation was written into the error text. A user who
// simply waited too long was told their code was wrong, so the useful action
// (request a new code) was indistinguishable from the useless one (retype it).

// storeChallenge writes a challenge with a chosen expiry and attempt count.
func storeChallenge(t *testing.T, repo IdentityRepository, svc *IdentityService, reqID string, attempts int, expiresAt time.Time) {
	t.Helper()
	if err := repo.SaveOtp(context.Background(), &OtpChallenge{
		RequestID:       reqID,
		MSISDNPseudonym: mp("+919876543210"), SubscriberID: sid("+919876543210"),
		CodeHash:  svc.hashString(testOtpCode),
		Attempts:  attempts,
		ExpiresAt: expiresAt,
	}); err != nil {
		t.Fatalf("SaveOtp: %v", err)
	}
}

func expiredAt() time.Time  { return time.Now().Add(-time.Second) }
func validUntil() time.Time { return time.Now().Add(5 * time.Minute) }

// TestExpiry_ExpiredChallengeWithWrongCodeReportsExpiry — requirement 1.
func TestExpiry_ExpiredChallengeWithWrongCodeReportsExpiry(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	svc := withFixedOtp(NewIdentityService(repo, []byte("test-secret-key")))
	storeChallenge(t, repo, svc, "req_1", 0, expiredAt())

	_, _, err := svc.ValidateOtp(context.Background(), "req_1", "000000")
	if !errors.Is(err, ErrExpiredOtp) {
		t.Fatalf("err = %v, want ErrExpiredOtp", err)
	}
	if errors.Is(err, ErrInvalidOtp) {
		t.Error("expiry reported as a wrong code")
	}
}

// TestExpiry_ExpiredChallengeWithCorrectCodeReportsExpiry — requirement 2.
//
// The submitted code being correct must not change the outcome: expiry is
// decided before the comparison, so a correct code cannot revive a dead
// challenge.
func TestExpiry_ExpiredChallengeWithCorrectCodeReportsExpiry(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	svc := withFixedOtp(NewIdentityService(repo, []byte("test-secret-key")))
	storeChallenge(t, repo, svc, "req_1", 0, expiredAt())

	sess, token, err := svc.ValidateOtp(context.Background(), "req_1", testOtpCode)
	if !errors.Is(err, ErrExpiredOtp) {
		t.Fatalf("err = %v, want ErrExpiredOtp", err)
	}
	if sess != nil || token != "" {
		t.Fatal("an expired challenge produced a session or token")
	}
}

// TestExpiry_WrongCodeOnLiveChallengeStillReportsInvalid — requirement 3.
func TestExpiry_WrongCodeOnLiveChallengeStillReportsInvalid(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	svc := withFixedOtp(NewIdentityService(repo, []byte("test-secret-key")))
	storeChallenge(t, repo, svc, "req_1", 0, validUntil())

	_, _, err := svc.ValidateOtp(context.Background(), "req_1", "000000")
	if !errors.Is(err, ErrInvalidOtp) {
		t.Fatalf("err = %v, want ErrInvalidOtp", err)
	}
	if errors.Is(err, ErrExpiredOtp) {
		t.Error("a wrong code on a live challenge reported expiry")
	}
}

// TestExpiry_CorrectCodeOnLiveChallengeStillSucceeds — requirement 4.
func TestExpiry_CorrectCodeOnLiveChallengeStillSucceeds(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	svc := withFixedOtp(NewIdentityService(repo, []byte("test-secret-key")))
	storeChallenge(t, repo, svc, "req_1", 0, validUntil())

	sess, token, err := svc.ValidateOtp(context.Background(), "req_1", testOtpCode)
	if err != nil {
		t.Fatalf("a correct code on a live challenge was rejected: %v", err)
	}
	if sess == nil || token != "" {
		t.Fatal("success returned no session, or an unenrolled access token")
	}
}

// TestExpiry_ExpiredChallengeDoesNotConsumeAnAttempt — requirement 5.
//
// The challenge is deleted on the expiry path, so the counter is observed
// through a repository whose DeleteOtp fails and therefore keeps the record.
func TestExpiry_ExpiredChallengeDoesNotConsumeAnAttempt(t *testing.T) {
	t.Parallel()

	repo := &failingDeleteRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()}
	svc := withFixedOtp(NewIdentityService(repo, []byte("test-secret-key")))
	storeChallenge(t, repo, svc, "req_1", 1, expiredAt())

	for i := 0; i < 3; i++ {
		if _, _, err := svc.ValidateOtp(context.Background(), "req_1", "000000"); !errors.Is(err, ErrExpiredOtp) {
			t.Fatalf("call %d: err = %v, want ErrExpiredOtp", i, err)
		}
	}

	stored, err := repo.GetOtp(context.Background(), "req_1")
	if err != nil {
		t.Fatalf("GetOtp: %v", err)
	}
	if stored.Attempts != 1 {
		t.Errorf("Attempts = %d after three calls against an EXPIRED challenge, "+
			"want 1; expiry must be decided before the code is compared, so it "+
			"cannot burn attempt budget", stored.Attempts)
	}
}

// TestExpiry_WrongCodeStillIncrementsAttempts — requirement 6.
func TestExpiry_WrongCodeStillIncrementsAttempts(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	svc := withFixedOtp(NewIdentityService(repo, []byte("test-secret-key")))
	storeChallenge(t, repo, svc, "req_1", 0, validUntil())

	for i := 1; i <= 2; i++ {
		if _, _, err := svc.ValidateOtp(context.Background(), "req_1", "000000"); !errors.Is(err, ErrInvalidOtp) {
			t.Fatalf("attempt %d: err = %v, want ErrInvalidOtp", i, err)
		}
		stored, err := repo.GetOtp(context.Background(), "req_1")
		if err != nil {
			t.Fatalf("GetOtp: %v", err)
		}
		if stored.Attempts != i {
			t.Fatalf("Attempts = %d after %d wrong codes, want %d", stored.Attempts, i, i)
		}
	}
}

// TestExpiry_ExhaustedTakesPrecedenceOverExpiry — requirement 7.
//
// A challenge that is BOTH exhausted and expired must be refused by the attempt
// guard, which runs first. This pins the ordering: exhausted -> expired -> code.
func TestExpiry_ExhaustedTakesPrecedenceOverExpiry(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	svc := withFixedOtp(NewIdentityService(repo, []byte("test-secret-key")))
	storeChallenge(t, repo, svc, "req_1", maxOtpAttempts, expiredAt())

	_, _, err := svc.ValidateOtp(context.Background(), "req_1", testOtpCode)
	if !errors.Is(err, ErrInvalidOtp) {
		t.Errorf("err = %v, want ErrInvalidOtp; the exhausted guard must run "+
			"before the expiry check", err)
	}
}

// TestExpiry_MissingChallengeBehaviourIsUnchanged — requirement 8.
//
// A challenge that does not exist still reports ErrInvalidOtp, NOT expiry.
// Distinguishing "no such request" would tell an unauthenticated caller which
// request ids exist.
func TestExpiry_MissingChallengeBehaviourIsUnchanged(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	svc := withFixedOtp(NewIdentityService(repo, []byte("test-secret-key")))

	_, _, err := svc.ValidateOtp(context.Background(), "req_does_not_exist", testOtpCode)
	if !errors.Is(err, ErrInvalidOtp) {
		t.Errorf("err = %v, want ErrInvalidOtp", err)
	}
	if errors.Is(err, ErrExpiredOtp) {
		t.Error("a missing challenge reported expiry, disclosing that the id is unknown")
	}
}

// ---------------------------------------------------------------------------
// Requirement 9 — API level.
//
// The package had NO handler tests. These are the first, so the distinction is
// pinned where a client actually observes it: the problem-details body.
// ---------------------------------------------------------------------------

func postValidate(t *testing.T, h *IdentityHandler, reqID, code string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"requestId": reqID, "code": code})
	req := httptest.NewRequest(http.MethodPost, "/otp/validate", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.HandleValidateOtp(rec, req)

	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	return rec, decoded
}

func TestExpiryAPI_ExpiredAndWrongCodeAreDistinguishable(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	svc := withFixedOtp(NewIdentityService(repo, []byte("test-secret-key")))
	h := &IdentityHandler{service: svc}

	storeChallenge(t, repo, svc, "req_expired", 0, expiredAt())
	storeChallenge(t, repo, svc, "req_live", 0, validUntil())

	expRec, expBody := postValidate(t, h, "req_expired", testOtpCode)
	wrongRec, wrongBody := postValidate(t, h, "req_live", "000000")

	// Status: this package uses 400/401/500 only, and both are rejected
	// credentials, so both stay 401. The machine-readable code is the
	// discriminator.
	if expRec.Code != http.StatusUnauthorized {
		t.Errorf("expired: status = %d, want 401", expRec.Code)
	}
	if wrongRec.Code != http.StatusUnauthorized {
		t.Errorf("wrong code: status = %d, want 401", wrongRec.Code)
	}

	if got := expBody["code"]; got != "ERR_OTP_EXPIRED" {
		t.Errorf("expired: code = %v, want ERR_OTP_EXPIRED", got)
	}
	if got := wrongBody["code"]; got != "ERR_UNAUTHORIZED" {
		t.Errorf("wrong code: code = %v, want ERR_UNAUTHORIZED", got)
	}
	if expBody["code"] == wrongBody["code"] {
		t.Error("a client cannot tell an expired code from a wrong one")
	}

	// The problem `type` URI is derived from the code, so it must differ too.
	if expBody["type"] == wrongBody["type"] {
		t.Errorf("both outcomes share the problem type %v", expBody["type"])
	}
}

func TestExpiryAPI_ExpiredDetailIsActionableAndLeaksNothing(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	svc := withFixedOtp(NewIdentityService(repo, []byte("test-secret-key")))
	h := &IdentityHandler{service: svc}
	storeChallenge(t, repo, svc, "req_expired", 0, expiredAt())

	_, body := postValidate(t, h, "req_expired", testOtpCode)

	detail, _ := body["detail"].(string)
	if detail == "" {
		t.Fatal("no detail returned for an expired challenge")
	}
	// A fixed, actionable string — not the Go error text, and nothing internal.
	for _, leak := range []string{"identity:", "req_expired", "+919876543210", testOtpCode} {
		if bytes.Contains([]byte(detail), []byte(leak)) {
			t.Errorf("detail %q leaks %q", detail, leak)
		}
	}
}

func TestExpiryAPI_SuccessResponseUnchanged(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	svc := withFixedOtp(NewIdentityService(repo, []byte("test-secret-key")))
	h := &IdentityHandler{service: svc}
	storeChallenge(t, repo, svc, "req_live", 0, validUntil())

	rec, body := postValidate(t, h, "req_live", testOtpCode)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	for _, k := range []string{"refreshToken", "sessionId", "subscriberId"} {
		if v, ok := body[k].(string); !ok || v == "" {
			t.Errorf("success response is missing %q", k)
		}
	}
	if _, ok := body["accessToken"]; ok {
		t.Error("an unenrolled validation returned an accessToken field")
	}
}
