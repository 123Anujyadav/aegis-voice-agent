package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// HandleValidateOtp used to render EVERY error from ValidateOtp as 401 with
// err.Error() in the body. ValidateOtp propagates raw repository errors from
// four places — GetOtp, SaveOtp, DeleteOtp and SaveSession — so an unavailable
// store was reported to the client as a rejected credential, and the store's own
// error text was handed to an unauthenticated caller.
//
// A 401 tells a client its credential was refused, so it stops. The correct code
// had in fact been accepted and the STORE had failed, which is retryable and
// will now never be retried.

// injectableRepo fails a chosen repository call with a recognisable internal
// error. It embeds MemoryIdentityRepository, so every call it does not override
// behaves normally.
//
// One double covering all four call sites, rather than four doubles: the
// classification under test is "is this error one of our sentinels", and the
// call it came from is irrelevant to that decision.
type injectableRepo struct {
	*MemoryIdentityRepository
	failGet     bool
	failSaveOtp bool
	failDelete  bool
	failSession bool
}

// The text deliberately looks like something a real driver would say, so a test
// asserting it does NOT appear in the body is meaningful.
var errInternalStore = errors.New("pq: connection to server at \"db-prod-1\" failed: FATAL password authentication failed for user \"identity\"")

func (r *injectableRepo) GetOtp(ctx context.Context, id string) (*OtpChallenge, error) {
	if r.failGet {
		return nil, errInternalStore
	}
	return r.MemoryIdentityRepository.GetOtp(ctx, id)
}

func (r *injectableRepo) SaveOtp(ctx context.Context, c *OtpChallenge) error {
	if r.failSaveOtp {
		return errInternalStore
	}
	return r.MemoryIdentityRepository.SaveOtp(ctx, c)
}

func (r *injectableRepo) DeleteOtp(ctx context.Context, id string) error {
	if r.failDelete {
		return errInternalStore
	}
	return r.MemoryIdentityRepository.DeleteOtp(ctx, id)
}

// ConsumeOtp and RecordFailedOtpAttempt are where ValidateOtp now deletes on a
// correct code and persists a wrong guess, so failDelete and failSaveOtp reach
// them too.
func (r *injectableRepo) ConsumeOtp(ctx context.Context, id string) (bool, error) {
	if r.failDelete {
		return false, errInternalStore
	}
	return r.MemoryIdentityRepository.ConsumeOtp(ctx, id)
}

func (r *injectableRepo) RecordFailedOtpAttempt(ctx context.Context, id string) (int, error) {
	if r.failSaveOtp {
		return 0, errInternalStore
	}
	return r.MemoryIdentityRepository.RecordFailedOtpAttempt(ctx, id)
}

func (r *injectableRepo) SaveSession(ctx context.Context, s *UserSession) error {
	if r.failSession {
		return errInternalStore
	}
	return r.MemoryIdentityRepository.SaveSession(ctx, s)
}

// ReplaceSession honours failSession too. RefreshToken persists a rotation
// through it rather than SaveSession, and the failed-save and expiry tests
// inject their store failure with this flag.
func (r *injectableRepo) ReplaceSession(ctx context.Context, s *UserSession, expected string) error {
	if r.failSession {
		return errInternalStore
	}
	return r.MemoryIdentityRepository.ReplaceSession(ctx, s, expected)
}

// seedFor writes a live challenge directly, bypassing RequestOtp so that a
// SaveOtp failure can be injected without breaking the setup itself.
func seedFor(t *testing.T, r *injectableRepo, svc *IdentityService, reqID string, attempts int, expires time.Time) {
	t.Helper()
	if err := r.MemoryIdentityRepository.SaveOtp(context.Background(), &OtpChallenge{
		RequestID:       reqID,
		MSISDNPseudonym: mp("+919876543210"), SubscriberID: sid("+919876543210"),
		CodeHash:  svc.hashString(testOtpCode),
		Attempts:  attempts,
		ExpiresAt: expires,
	}); err != nil {
		t.Fatalf("seed SaveOtp: %v", err)
	}
}

func newHandlerWith(repo IdentityRepository) *IdentityHandler {
	return &IdentityHandler{service: withFixedOtp(NewIdentityService(repo, []byte("test-secret-key")))}
}

// ---------------------------------------------------------------------------
// Credential rejection stays 401
// ---------------------------------------------------------------------------

func TestErrorMapping_WrongCodeIs401(t *testing.T) {
	t.Parallel()

	repo := &injectableRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()}
	h := newHandlerWith(repo)
	seedFor(t, repo, h.service, "req_1", 0, time.Now().Add(5*time.Minute))

	rec, body := postValidate(t, h, "req_1", "000000")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if got := body["code"]; got != "ERR_UNAUTHORIZED" {
		t.Errorf("code = %v, want ERR_UNAUTHORIZED", got)
	}
}

func TestErrorMapping_ExpiredIs401(t *testing.T) {
	t.Parallel()

	repo := &injectableRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()}
	h := newHandlerWith(repo)
	seedFor(t, repo, h.service, "req_1", 0, time.Now().Add(-time.Second))

	rec, body := postValidate(t, h, "req_1", testOtpCode)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if got := body["code"]; got != "ERR_OTP_EXPIRED" {
		t.Errorf("code = %v, want ERR_OTP_EXPIRED", got)
	}
}

// TestErrorMapping_MissingChallengeIs401 covers the ErrInvalidOtp path that the
// repository itself produces for an unknown request id.
func TestErrorMapping_MissingChallengeIs401(t *testing.T) {
	t.Parallel()

	repo := &injectableRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()}
	h := newHandlerWith(repo)

	rec, body := postValidate(t, h, "req_unknown", testOtpCode)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if got := body["code"]; got != "ERR_UNAUTHORIZED" {
		t.Errorf("code = %v, want ERR_UNAUTHORIZED", got)
	}
}

// TestErrorMapping_ErrInvalidTokenIsClassifiedAsCredentialRejection covers the
// third sentinel directly. ValidateOtp does not currently return it, so the
// classifier is exercised rather than the handler path.
func TestErrorMapping_ErrInvalidTokenIsClassifiedAsCredentialRejection(t *testing.T) {
	t.Parallel()

	for _, err := range []error{ErrInvalidOtp, ErrExpiredOtp, ErrInvalidToken} {
		if !isCredentialRejection(err) {
			t.Errorf("%v is not classified as credential rejection", err)
		}
	}
	for _, err := range []error{errInternalStore, errors.New("anything else")} {
		if isCredentialRejection(err) {
			t.Errorf("%v was classified as credential rejection; unknown errors "+
				"come from the repository and are infrastructure", err)
		}
	}
}

// ---------------------------------------------------------------------------
// Infrastructure failures become 500
// ---------------------------------------------------------------------------

func TestErrorMapping_InfrastructureFailuresAre500(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		set  func(*injectableRepo)
		code string
		// seedAttempts drives which repository call ValidateOtp reaches.
		seedAttempts int
	}{
		{"GetOtp fails", func(r *injectableRepo) { r.failGet = true }, "000000", 0},
		{"SaveOtp fails on the wrong-code path", func(r *injectableRepo) { r.failSaveOtp = true }, "000000", 0},
		{"DeleteOtp fails on the success path", func(r *injectableRepo) { r.failDelete = true }, testOtpCode, 0},
		{"SaveSession fails after consumption", func(r *injectableRepo) { r.failSession = true }, testOtpCode, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := &injectableRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()}
			h := newHandlerWith(repo)
			seedFor(t, repo, h.service, "req_1", tc.seedAttempts, time.Now().Add(5*time.Minute))
			tc.set(repo)

			rec, body := postValidate(t, h, "req_1", tc.code)

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500; a store failure is not a rejected "+
					"credential, and a 401 tells the client never to retry", rec.Code)
			}
			if got := body["code"]; got != "ERR_INTERNAL" {
				t.Errorf("code = %v, want ERR_INTERNAL", got)
			}
		})
	}
}

// TestErrorMapping_InfrastructureResponseLeaksNoInternalText is the disclosure
// invariant. The injected error is shaped like a real driver message.
func TestErrorMapping_InfrastructureResponseLeaksNoInternalText(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		set  func(*injectableRepo)
		code string
	}{
		{"GetOtp", func(r *injectableRepo) { r.failGet = true }, "000000"},
		{"SaveOtp", func(r *injectableRepo) { r.failSaveOtp = true }, "000000"},
		{"DeleteOtp", func(r *injectableRepo) { r.failDelete = true }, testOtpCode},
		{"SaveSession", func(r *injectableRepo) { r.failSession = true }, testOtpCode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := &injectableRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()}
			h := newHandlerWith(repo)
			seedFor(t, repo, h.service, "req_1", 0, time.Now().Add(5*time.Minute))
			tc.set(repo)

			rec, body := postValidate(t, h, "req_1", tc.code)
			_ = rec

			detail, _ := body["detail"].(string)
			whole := rec.Body.String()

			for _, leak := range []string{
				"pq:", "db-prod-1", "password authentication", "identity\"",
				errInternalStore.Error(),
			} {
				if strings.Contains(whole, leak) {
					t.Errorf("response leaks %q\n  detail: %s", leak, detail)
				}
			}
			if detail == "" {
				t.Error("no detail returned; the client should learn it may retry")
			}
		})
	}
}

// TestErrorMapping_SuccessResponseUnchanged pins that the classification did not
// disturb the happy path.
func TestErrorMapping_SuccessResponseUnchanged(t *testing.T) {
	t.Parallel()

	repo := &injectableRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()}
	h := newHandlerWith(repo)
	seedFor(t, repo, h.service, "req_1", 0, time.Now().Add(5*time.Minute))

	rec, body := postValidate(t, h, "req_1", testOtpCode)
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
