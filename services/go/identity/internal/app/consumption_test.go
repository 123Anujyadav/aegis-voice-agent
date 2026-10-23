package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A one-time code is only one-time if its consumption is confirmed.
//
// The successful-validation path used to be `_ = s.repo.DeleteOtp(...)`. When
// that delete failed the challenge stayed stored with Attempts unchanged —
// usually 0 — so the exhausted-challenge guard did not apply to it either, and
// the same correct code could be replayed until it expired.
//
// ValidateOtp now fails closed: if consumption cannot be confirmed, no session,
// no refresh token and no access token are produced.

// countingDeleteRepo lets a test fail DeleteOtp on demand and observe how often
// it was called. It embeds MemoryIdentityRepository, which copies on read and
// write, so nothing here depends on pointer aliasing.
type countingDeleteRepo struct {
	*MemoryIdentityRepository
	failDelete bool
	deletes    int
	saves      int
}

var errStoreUnavailable = errors.New("test: otp store unavailable")

func (r *countingDeleteRepo) DeleteOtp(ctx context.Context, requestID string) error {
	r.deletes++
	if r.failDelete {
		return errStoreUnavailable
	}
	return r.MemoryIdentityRepository.DeleteOtp(ctx, requestID)
}

// ConsumeOtp is the success path's delete now, so it is counted and fails with
// failDelete exactly as DeleteOtp does.
func (r *countingDeleteRepo) ConsumeOtp(ctx context.Context, requestID string) (bool, error) {
	r.deletes++
	if r.failDelete {
		return false, errStoreUnavailable
	}
	return r.MemoryIdentityRepository.ConsumeOtp(ctx, requestID)
}

func (r *countingDeleteRepo) SaveSession(ctx context.Context, session *UserSession) error {
	r.saves++
	return r.MemoryIdentityRepository.SaveSession(ctx, session)
}

func newConsumptionSvc(t *testing.T, failDelete bool) (*IdentityService, *countingDeleteRepo) {
	t.Helper()
	repo := &countingDeleteRepo{
		MemoryIdentityRepository: NewMemoryIdentityRepository(),
		failDelete:               failDelete,
	}
	return withFixedOtp(NewIdentityService(repo, []byte("test-secret-key"))), repo
}

// TestConsumption_SucceedsWhenDeleteSucceeds pins the unchanged happy path.
func TestConsumption_SucceedsWhenDeleteSucceeds(t *testing.T) {
	t.Parallel()

	svc, repo := newConsumptionSvc(t, false)

	reqID, err := svc.RequestOtp(context.Background(), "+919876543210")
	if err != nil {
		t.Fatalf("RequestOtp: %v", err)
	}

	sess, token, err := svc.ValidateOtp(context.Background(), reqID, testOtpCode)
	if err != nil {
		t.Fatalf("the correct code was rejected while the store was healthy: %v", err)
	}
	if sess == nil || token != "" {
		t.Fatal("validation succeeded but returned no session, or an unenrolled access token")
	}
	if repo.saves != 1 {
		t.Errorf("SaveSession called %d times, want 1", repo.saves)
	}
	if _, err := repo.GetOtp(context.Background(), reqID); err == nil {
		t.Error("the challenge survived a successful validation; it was not consumed")
	}
}

// TestConsumption_FailsClosedWhenDeleteFails is the core of the fix.
func TestConsumption_FailsClosedWhenDeleteFails(t *testing.T) {
	t.Parallel()

	svc, repo := newConsumptionSvc(t, true)

	reqID, err := svc.RequestOtp(context.Background(), "+919876543210")
	if err != nil {
		t.Fatalf("RequestOtp: %v", err)
	}

	sess, token, err := svc.ValidateOtp(context.Background(), reqID, testOtpCode)

	if err == nil {
		t.Fatal("authentication succeeded although one-time consumption could not " +
			"be confirmed; a correct code alone is not sufficient to authenticate")
	}
	if !errors.Is(err, errStoreUnavailable) {
		t.Errorf("err = %v, want the store failure to be propagated", err)
	}
	if sess != nil {
		t.Errorf("a session was returned despite the failure: %+v", sess)
	}
	if token != "" {
		t.Errorf("an access token was returned despite the failure: %q", token)
	}
	if repo.saves != 0 {
		t.Errorf("SaveSession was called %d times, want 0; the return must happen "+
			"before any identity is minted, not after", repo.saves)
	}
}

// TestConsumption_NoSessionIsPersistedWhenDeleteFails checks the stronger
// property: not merely that nothing was RETURNED, but that nothing was CREATED.
// A session written to the store and then discarded by the caller would still be
// usable through RefreshToken.
func TestConsumption_NoSessionIsPersistedWhenDeleteFails(t *testing.T) {
	t.Parallel()

	svc, repo := newConsumptionSvc(t, true)

	reqID, _ := svc.RequestOtp(context.Background(), "+919876543210")
	if _, _, err := svc.ValidateOtp(context.Background(), reqID, testOtpCode); err == nil {
		t.Fatal("validation succeeded despite a failed delete")
	}

	if repo.saves != 0 {
		t.Errorf("SaveSession was called %d times; a session was minted on a path "+
			"that must not authenticate, and RefreshToken could still use it",
			repo.saves)
	}
}

// TestConsumption_ChallengeRemainsStoredAfterFailedDeletion documents the state
// the fix has to survive: the challenge is genuinely still there.
func TestConsumption_ChallengeRemainsStoredAfterFailedDeletion(t *testing.T) {
	t.Parallel()

	svc, repo := newConsumptionSvc(t, true)

	reqID, _ := svc.RequestOtp(context.Background(), "+919876543210")
	_, _, _ = svc.ValidateOtp(context.Background(), reqID, testOtpCode)

	stored, err := repo.GetOtp(context.Background(), reqID)
	if err != nil {
		t.Fatalf("the challenge should still be stored after a failed delete: %v", err)
	}
	if stored.Attempts != 0 {
		t.Errorf("Attempts = %d, want 0; a CORRECT code must not consume attempt "+
			"budget, which is exactly why the entry guard cannot protect this path",
			stored.Attempts)
	}
	if repo.deletes == 0 {
		t.Error("DeleteOtp was never attempted on the success path")
	}
}

// TestConsumption_ReplayIsRefusedWhileConsumptionCannotBeConfirmed is the
// replay-safety test.
//
// The correct code is submitted repeatedly while the store cannot delete. Every
// attempt must fail. The danger the old code carried was the opposite: the first
// call would succeed, and because the challenge was never removed, so would
// every call after it.
func TestConsumption_ReplayIsRefusedWhileConsumptionCannotBeConfirmed(t *testing.T) {
	t.Parallel()

	svc, repo := newConsumptionSvc(t, true)

	reqID, _ := svc.RequestOtp(context.Background(), "+919876543210")

	for i := 1; i <= 5; i++ {
		sess, token, err := svc.ValidateOtp(context.Background(), reqID, testOtpCode)
		if err == nil {
			t.Fatalf("replay %d: the correct code authenticated while consumption "+
				"could not be confirmed", i)
		}
		if sess != nil || token != "" {
			t.Fatalf("replay %d: returned a session or token despite failing", i)
		}
	}

	if repo.saves != 0 {
		t.Errorf("SaveSession called %d times across five replays, want 0", repo.saves)
	}
}

// TestConsumption_RecoversOnceTheStoreRecovers shows the failure is transient
// rather than a permanent lockout: the same challenge validates once the store
// can delete again. Without this, "fail closed" could be satisfied by simply
// never authenticating.
func TestConsumption_RecoversOnceTheStoreRecovers(t *testing.T) {
	t.Parallel()

	svc, repo := newConsumptionSvc(t, true)

	reqID, _ := svc.RequestOtp(context.Background(), "+919876543210")
	if _, _, err := svc.ValidateOtp(context.Background(), reqID, testOtpCode); err == nil {
		t.Fatal("validation succeeded while the store was failing")
	}

	repo.failDelete = false

	sess, token, err := svc.ValidateOtp(context.Background(), reqID, testOtpCode)
	if err != nil {
		t.Fatalf("validation still failed after the store recovered: %v", err)
	}
	if sess == nil || token != "" {
		t.Fatal("recovery path returned no session, or an unenrolled access token")
	}
}

// TestConsumption_InvalidCodeBehaviourIsUnchanged confirms the change did not
// alter the wrong-code path, whose delete stays best-effort.
func TestConsumption_InvalidCodeBehaviourIsUnchanged(t *testing.T) {
	t.Parallel()

	svc, repo := newConsumptionSvc(t, true)

	reqID, _ := svc.RequestOtp(context.Background(), "+919876543210")

	_, _, err := svc.ValidateOtp(context.Background(), reqID, "000000")
	if !errors.Is(err, ErrInvalidOtp) {
		t.Fatalf("a wrong code returned %v, want ErrInvalidOtp; the wrong-code path "+
			"must not start surfacing store errors", err)
	}

	stored, err := repo.GetOtp(context.Background(), reqID)
	if err != nil {
		t.Fatalf("GetOtp: %v", err)
	}
	if stored.Attempts != 1 {
		t.Errorf("Attempts = %d after one wrong code, want 1; attempt counting "+
			"regressed", stored.Attempts)
	}
}

// TestConsumption_ExhaustedChallengeStillRejected confirms the entry guard is
// unaffected, including when the store cannot delete.
func TestConsumption_ExhaustedChallengeStillRejected(t *testing.T) {
	t.Parallel()

	svc, repo := newConsumptionSvc(t, true)

	if err := repo.SaveOtp(context.Background(), &OtpChallenge{
		RequestID:       "req_exhausted",
		MSISDNPseudonym: mp("+919876543210"), SubscriberID: sid("+919876543210"),
		CodeHash:  svc.hashString(testOtpCode),
		Attempts:  maxOtpAttempts,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("SaveOtp: %v", err)
	}

	_, _, err := svc.ValidateOtp(context.Background(), "req_exhausted", testOtpCode)
	if !errors.Is(err, ErrInvalidOtp) {
		t.Errorf("err = %v, want ErrInvalidOtp; an exhausted challenge must be "+
			"refused by the guard before the delete is ever reached", err)
	}
}
