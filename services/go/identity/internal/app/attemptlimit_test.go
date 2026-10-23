package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The OTP attempt limit was enforced ONLY by deleting the challenge once the
// counter reached the limit — and every DeleteOtp call discards its error with
// `_ =`. A delete that failed left the challenge in place with Attempts at or
// above the limit, and nothing read Attempts on entry, so the next call
// evaluated the supplied code as though the budget were untouched.
//
// The control was therefore not independent: it worked only while cleanup
// happened to succeed.
//
// These tests drive the service through a repository whose DeleteOtp always
// fails, which is the condition the old code could not survive. The repository
// double does not alias: it embeds MemoryIdentityRepository, which now copies on
// read and on write.

// failingDeleteRepo is a repository whose DeleteOtp never succeeds.
type failingDeleteRepo struct {
	*MemoryIdentityRepository
	deletes int
}

var errDeleteUnavailable = errors.New("test: otp store unavailable for delete")

func (r *failingDeleteRepo) DeleteOtp(ctx context.Context, requestID string) error {
	r.deletes++
	return errDeleteUnavailable
}

// ConsumeOtp fails the same way: this double models a store that cannot delete.
func (r *failingDeleteRepo) ConsumeOtp(ctx context.Context, requestID string) (bool, error) {
	r.deletes++
	return false, errDeleteUnavailable
}

// seedChallenge stores a challenge with a chosen attempt count, so a test can
// start from any point in the budget without burning real attempts.
func seedChallenge(t *testing.T, repo IdentityRepository, svc *IdentityService, reqID string, attempts int) {
	t.Helper()
	if err := repo.SaveOtp(context.Background(), &OtpChallenge{
		RequestID:       reqID,
		MSISDNPseudonym: mp("+919876543210"), SubscriberID: sid("+919876543210"),
		CodeHash:  svc.hashString(testOtpCode),
		Attempts:  attempts,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("SaveOtp: %v", err)
	}
}

// TestAttemptLimit_EligibilityAcrossTheBudget walks the counter through every
// value that matters and pins what the existing contract does at each one.
//
// The limit counts WRONG codes: three are allowed, and the third exhausts the
// challenge. A correct code on the third attempt still succeeds, because the
// guard runs before this attempt is counted.
func TestAttemptLimit_EligibilityAcrossTheBudget(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		attempts    int
		wantAccept  bool
		description string
	}{
		{"fresh", 0, true, "an untouched challenge validates"},
		{"one wrong attempt used", 1, true, "still within budget"},
		{"two wrong attempts used", 2, true, "final allowed attempt still validates"},
		{"budget exhausted", 3, false, "rejected before the code is compared"},
		{"counter beyond the limit", 4, false, "also rejected"},
		{"counter far beyond the limit", 99, false, "also rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := NewMemoryIdentityRepository()
			svc := withFixedOtp(NewIdentityService(repo, []byte("test-secret-key")))

			reqID := "req_" + tc.name
			seedChallenge(t, repo, svc, reqID, tc.attempts)

			sess, token, err := svc.ValidateOtp(context.Background(), reqID, testOtpCode)

			if tc.wantAccept {
				if err != nil {
					t.Fatalf("Attempts=%d: the CORRECT code was rejected (%v); %s",
						tc.attempts, err, tc.description)
				}
				if sess == nil || token != "" {
					t.Fatalf("Attempts=%d: accepted but returned no session, or an unenrolled access token", tc.attempts)
				}
				return
			}

			if err == nil {
				t.Fatalf("Attempts=%d: an exhausted challenge accepted the correct "+
					"code; %s", tc.attempts, tc.description)
			}
			if !errors.Is(err, ErrInvalidOtp) {
				t.Errorf("Attempts=%d: err = %v, want ErrInvalidOtp — the contract "+
					"must not disclose that a challenge is exhausted rather than wrong",
					tc.attempts, err)
			}
		})
	}
}

// TestAttemptLimit_ExhaustedChallengeIsRejectedEvenWithTheCorrectCode is the
// core security property, stated on its own because it is the one that matters:
// running out of attempts must beat knowing the code.
func TestAttemptLimit_ExhaustedChallengeIsRejectedEvenWithTheCorrectCode(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	svc := withFixedOtp(NewIdentityService(repo, []byte("test-secret-key")))

	seedChallenge(t, repo, svc, "req_1", maxOtpAttempts)

	if _, _, err := svc.ValidateOtp(context.Background(), "req_1", testOtpCode); err == nil {
		t.Fatal("an exhausted challenge validated the correct code")
	}
}

// TestAttemptLimit_SurvivesDeleteFailure is the regression test for the defect.
//
// With DeleteOtp permanently failing, the challenge is never removed. The old
// implementation would keep evaluating codes against it forever; the guard must
// refuse it on every call after the budget is spent.
func TestAttemptLimit_SurvivesDeleteFailure(t *testing.T) {
	t.Parallel()

	repo := &failingDeleteRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()}
	svc := withFixedOtp(NewIdentityService(repo, []byte("test-secret-key")))

	reqID, err := svc.RequestOtp(context.Background(), "+919876543210")
	if err != nil {
		t.Fatalf("RequestOtp: %v", err)
	}

	// Spend the budget with wrong codes.
	for i := 1; i <= maxOtpAttempts; i++ {
		if _, _, validateOtpErr := svc.ValidateOtp(context.Background(), reqID, "000000"); validateOtpErr == nil {
			t.Fatalf("attempt %d: a wrong code was accepted", i)
		}
	}

	// The challenge is still there, because every delete failed.
	stored, err := repo.GetOtp(context.Background(), reqID)
	if err != nil {
		t.Fatalf("the challenge should have survived the failed deletes: %v", err)
	}
	if stored.Attempts < maxOtpAttempts {
		t.Fatalf("Attempts = %d after %d wrong codes, want >= %d",
			stored.Attempts, maxOtpAttempts, maxOtpAttempts)
	}

	// The correct code must still be refused, and further wrong guesses must not
	// be evaluated either.
	if _, _, err := svc.ValidateOtp(context.Background(), reqID, testOtpCode); err == nil {
		t.Fatal("after the budget was spent and every delete failed, the correct " +
			"code was accepted; the attempt limit depends on DeleteOtp succeeding")
	}
	for i := 0; i < 5; i++ {
		if _, _, err := svc.ValidateOtp(context.Background(), reqID, "111111"); err == nil {
			t.Fatal("an exhausted challenge accepted a further guess")
		}
	}
}

// TestAttemptLimit_ExhaustedChallengeDoesNotConsumeMoreBudget checks that the
// guard returns before the increment, so the counter does not run away and,
// more importantly, that no comparison is performed.
func TestAttemptLimit_ExhaustedChallengeDoesNotConsumeMoreBudget(t *testing.T) {
	t.Parallel()

	repo := &failingDeleteRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()}
	svc := withFixedOtp(NewIdentityService(repo, []byte("test-secret-key")))

	seedChallenge(t, repo, svc, "req_1", maxOtpAttempts)

	for i := 0; i < 4; i++ {
		if _, _, err := svc.ValidateOtp(context.Background(), "req_1", "000000"); err == nil {
			t.Fatal("an exhausted challenge accepted a code")
		}
	}

	stored, err := repo.GetOtp(context.Background(), "req_1")
	if err != nil {
		t.Fatalf("GetOtp: %v", err)
	}
	if stored.Attempts != maxOtpAttempts {
		t.Errorf("Attempts = %d after four rejected calls, want %d; the guard is "+
			"running after the increment rather than before it",
			stored.Attempts, maxOtpAttempts)
	}
}

// TestAttemptLimit_NormalConsumptionIsUnchanged pins that the guard did not
// break the ordinary path: a correct code on a fresh challenge still produces a
// session and still consumes the challenge exactly once.
func TestAttemptLimit_NormalConsumptionIsUnchanged(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	svc := withFixedOtp(NewIdentityService(repo, []byte("test-secret-key")))

	reqID, err := svc.RequestOtp(context.Background(), "+919876543210")
	if err != nil {
		t.Fatalf("RequestOtp: %v", err)
	}

	sess, token, err := svc.ValidateOtp(context.Background(), reqID, testOtpCode)
	if err != nil {
		t.Fatalf("the correct code on a fresh challenge was rejected: %v", err)
	}
	if sess == nil || token != "" {
		t.Fatal("validation succeeded but returned no session, or an unenrolled access token")
	}

	if _, _, err := svc.ValidateOtp(context.Background(), reqID, testOtpCode); err == nil {
		t.Error("the same challenge validated twice; one-time consumption broke")
	}
}

// TestAttemptLimit_ExpiryStillTakesPrecedenceForAFreshChallenge confirms the
// guard did not displace the expiry check for challenges still within budget.
func TestAttemptLimit_ExpiryStillTakesPrecedenceForAFreshChallenge(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	svc := withFixedOtp(NewIdentityService(repo, []byte("test-secret-key")))

	if err := repo.SaveOtp(context.Background(), &OtpChallenge{
		RequestID:       "req_expired",
		MSISDNPseudonym: mp("+919876543210"), SubscriberID: sid("+919876543210"),
		CodeHash:  svc.hashString(testOtpCode),
		Attempts:  0,
		ExpiresAt: time.Now().Add(-time.Second),
	}); err != nil {
		t.Fatalf("SaveOtp: %v", err)
	}

	_, _, err := svc.ValidateOtp(context.Background(), "req_expired", testOtpCode)
	if err == nil {
		t.Fatal("an expired challenge validated")
	}
	// This assertion used to be `errors.Is(err, ErrInvalidOtp)`, which encoded
	// the very conflation docs/domain/10-identity.md forbids. Expiry now has its
	// own error.
	if !errors.Is(err, ErrExpiredOtp) {
		t.Errorf("err = %v, want ErrExpiredOtp", err)
	}
	if errors.Is(err, ErrInvalidOtp) {
		t.Error("an expired challenge reported ErrInvalidOtp; expiry and wrong-code " +
			"must be distinguishable")
	}
}
