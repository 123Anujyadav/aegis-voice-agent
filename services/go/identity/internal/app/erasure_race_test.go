package app

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// Account deletion under races and storage failure: the checks the service
// makes on the session as read are made again by the repository inside the
// erasure transaction (erasure.go), and a failed erasure changes nothing.

// seamRepo wraps the memory repository: afterRead runs once after the next
// GetSession (a race injected between the service's checks and the erasure),
// and eraseErr makes EraseSubscriber fail without changing anything.
type seamRepo struct {
	*MemoryIdentityRepository
	mu        sync.Mutex
	afterRead func()
	eraseErr  error
	// afterConsume runs once inside sign-in, after the code is consumed and
	// before the session is saved (ResetOtpFailures sits between the two).
	afterConsume func()
}

func (r *seamRepo) ResetOtpFailures(ctx context.Context, pseudonym string) error {
	r.mu.Lock()
	hook := r.afterConsume
	r.afterConsume = nil
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	return r.MemoryIdentityRepository.ResetOtpFailures(ctx, pseudonym)
}

func (r *seamRepo) GetSession(ctx context.Context, id string) (*UserSession, error) {
	s, err := r.MemoryIdentityRepository.GetSession(ctx, id)
	r.mu.Lock()
	hook := r.afterRead
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	return s, err
}

func (r *seamRepo) EraseSubscriber(ctx context.Context, e *SubscriberErasure) error {
	if r.eraseErr != nil {
		return r.eraseErr
	}
	return r.MemoryIdentityRepository.EraseSubscriber(ctx, e)
}

func (e *erasureRig) withSeam() *seamRepo {
	seam := &seamRepo{MemoryIdentityRepository: e.repo}
	e.svc.repo = seam
	return seam
}

func (e *erasureRig) deletionsAudited() int {
	n := 0
	for _, ev := range e.audit.events {
		if ev.Type == EventAccountDeleted {
			n++
		}
	}
	return n
}

// Two deletions of one session, each with its own fresh proof by the enrolled
// key, both pass the service's checks before either erases. Both succeed, and
// exactly one erasure is recorded and audited: the second finds the session
// gone inside its transaction and is answered from the first's receipt.
func TestDeleteAccount_ConcurrentDuplicatesRecordOneErasure(t *testing.T) {
	ctx := context.Background()
	e := newErasureRig(ctx, t)
	seam := e.withSeam()
	var bothRead sync.WaitGroup
	bothRead.Add(2)
	seam.afterRead = func() { bothRead.Done(); bothRead.Wait() }

	errs := make([]error, 2)
	proofs := []string{e.deletionProof(t, e.kA1, "dup-1"), e.deletionProof(t, e.kA1, "dup-2")}
	var done sync.WaitGroup
	for i := range 2 {
		done.Add(1)
		go func() {
			defer done.Done()
			errs[i] = e.svc.DeleteAccountWithDeviceProof(ctx, e.sessA1.SessionID, e.sessA1.RefreshToken, "POST", proofs[i])
		}()
	}
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("deletion %d: %v, want success (the duplicate is answered from the receipt)", i, err)
		}
	}
	if n := len(e.repo.Erasures()); n != 1 {
		t.Errorf("%d erasure records, want 1", n)
	}
	if n := e.deletionsAudited(); n != 1 {
		t.Errorf("%d deletion audit events, want 1", n)
	}
}

// The session is rotated (a refresh) after the deletion read it but before it
// erased: the deletion presented a token that is no longer current, so it
// erases nothing and is refused.
func TestDeleteAccount_ARotationBetweenReadAndEraseErasesNothing(t *testing.T) {
	ctx := context.Background()
	e := newErasureRig(ctx, t)
	seam := e.withSeam()
	seam.afterRead = func() {
		seam.mu.Lock()
		seam.afterRead = nil
		seam.mu.Unlock()
		if _, _, err := e.svc.RefreshTokenWithDeviceProof(ctx, e.sessA1.SessionID, e.sessA1.RefreshToken, "POST", e.refreshProof(t, e.kA1, "rotate")); err != nil {
			t.Errorf("rotation: %v", err)
		}
	}

	err := e.svc.DeleteAccountWithDeviceProof(ctx, e.sessA1.SessionID, e.sessA1.RefreshToken, "POST", e.deletionProof(t, e.kA1, "stale"))
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken", err)
	}
	if !e.sessionExists(ctx, t, e.sessA1.SessionID) || !e.sessionExists(ctx, t, e.sessA2.SessionID) {
		t.Error("a session was erased by a deletion that lost the race")
	}
	if n := len(e.repo.Erasures()); n != 0 || e.deletionsAudited() != 0 {
		t.Errorf("%d erasures and %d audit events, want none", n, e.deletionsAudited())
	}
}

// A storage failure during the erasure is returned, nothing is erased, and no
// deletion is audited (the client is told nothing was confirmed and retries).
func TestDeleteAccount_AStorageFailureErasesAndAuditsNothing(t *testing.T) {
	ctx := context.Background()
	e := newErasureRig(ctx, t)
	seam := e.withSeam()
	failure := errors.New("database unavailable")
	seam.eraseErr = failure

	err := e.svc.DeleteAccountWithDeviceProof(ctx, e.sessA1.SessionID, e.sessA1.RefreshToken, "POST", e.deletionProof(t, e.kA1, "fails"))
	if !errors.Is(err, failure) {
		t.Fatalf("err = %v, want the storage failure", err)
	}
	if !e.sessionExists(ctx, t, e.sessA1.SessionID) || !e.sessionExists(ctx, t, e.sessA2.SessionID) {
		t.Error("a session was erased by a failed erasure")
	}
	if e.deletionsAudited() != 0 {
		t.Error("a failed erasure was audited as a deletion")
	}

	// Once storage recovers, the same session deletes with a fresh proof.
	seam.eraseErr = nil
	if err := e.svc.DeleteAccountWithDeviceProof(ctx, e.sessA1.SessionID, e.sessA1.RefreshToken, "POST", e.deletionProof(t, e.kA1, "recovered")); err != nil {
		t.Fatalf("retry after recovery: %v", err)
	}
}

// A sign-in already past its code check when the account is erased must not
// re-create a session for the erased account. The erasure runs exactly
// between the code's consumption and the session's save; the sign-in is
// refused as a spent code, and no session of the subscriber survives. A code
// requested after the erasure then signs in normally (D1 is unchanged).
func TestDeleteAccount_ASignInInFlightCannotOutliveTheErasure(t *testing.T) {
	ctx := context.Background()
	e := newErasureRig(ctx, t)
	seam := e.withSeam()
	seam.afterConsume = func() {
		if err := e.svc.DeleteAccountWithDeviceProof(ctx, e.sessA1.SessionID, e.sessA1.RefreshToken, "POST", e.deletionProof(t, e.kA1, "mid-sign-in")); err != nil {
			t.Errorf("deletion during the sign-in: %v", err)
		}
	}

	newKey := deviceKey(t)
	_, _, err := e.svc.ValidateOtpWithDeviceProof(ctx, "req_pending_a", testOtpCode, "POST",
		enrollProof(t, newKey, newKey, "in-flight", "POST", e.clock.now()))
	if !errors.Is(err, ErrInvalidOtp) || !errors.Is(err, ErrSubscriberErased) {
		t.Fatalf("in-flight sign-in: err = %v, want a spent code (ErrInvalidOtp, ErrSubscriberErased)", err)
	}
	if n := e.sessionsOf(sid(rlMSISDN)); n != 0 {
		t.Fatalf("%d sessions of the erased subscriber exist after the erasure", n)
	}
	if n := len(e.repo.Erasures()); n != 1 {
		t.Fatalf("%d erasures, want 1", n)
	}

	afterTheClockTicks()
	again := e.enrolKey(ctx, t, "req_after", deviceKey(t))
	if again.SubscriberID != sid(rlMSISDN) {
		t.Errorf("a code requested after the erasure did not sign in: %+v", again)
	}
}

func (e *erasureRig) sessionsOf(subscriber string) int {
	e.repo.mu.RLock()
	defer e.repo.mu.RUnlock()
	n := 0
	for _, s := range e.repo.sessions {
		if s.SubscriberID == subscriber {
			n++
		}
	}
	return n
}
