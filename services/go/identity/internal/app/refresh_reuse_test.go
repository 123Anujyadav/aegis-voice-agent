package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/dpop"
)

// Reuse detection needs a token the session actually held (RFC 9700
// §4.14.2); a token it never held is refused and revokes nothing
// (docs/phase19/REFRESH-TOKEN-THREAT-ANALYSIS.md §3).

// Through real rotations, with no test hook: the superseded token, presented
// again, is reuse and revokes the session, whatever the proof.
func TestRefreshReuse_ARealSupersededTokenRevokes(t *testing.T) {
	ctx := context.Background()
	r, k, sess := newRefreshRig(ctx, t)
	aud := &recordingSecurityAuditor{}
	r.svc.SetSecurityAuditor(aud)
	old := sess.RefreshToken
	if _, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, old, "POST", r.refreshProof(t, k, "r1")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, old, "POST", "garbage"); !errors.Is(err, ErrTokenReused) {
		t.Fatalf("err = %v, want ErrTokenReused", err)
	}
	if stored, _ := r.repo.GetSession(ctx, sess.SessionID); !stored.IsRevoked {
		t.Fatal("reuse of a superseded token did not revoke")
	}
	if len(reuseEvents(aud)) != 1 {
		t.Fatalf("reuse events = %d, want 1", len(reuseEvents(aud)))
	}
}

// A token the session never held revokes nothing, is not audited as reuse,
// spends no proof, and the holder of the current token refreshes normally.
func TestRefreshReuse_ATokenNeverHeldRevokesNothing(t *testing.T) {
	ctx := context.Background()
	r, k, sess := newRefreshRig(ctx, t)
	aud := &recordingSecurityAuditor{}
	r.svc.SetSecurityAuditor(aud)
	p := r.refreshProof(t, k, "r1")
	for _, guess := range []string{"rt_guessed", sess.SessionID, ""} {
		if _, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, guess, "POST", p); !errors.Is(err, ErrInvalidToken) || errors.Is(err, ErrTokenReused) {
			t.Fatalf("guess %q: err = %v, want ErrInvalidToken", guess, err)
		}
	}
	r.assertUnrotated(ctx, t, sess.SessionID, sess.RefreshToken)
	if len(reuseEvents(aud)) != 0 {
		t.Fatal("a guess was audited as reuse")
	}
	// The same proof is still unspent: nothing was verified for the guesses.
	if _, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", p); err != nil {
		t.Fatalf("the current token, with the unspent proof: %v", err)
	}
}

// supersededFailingRepo cannot answer the superseded-token lookup.
type supersededFailingRepo struct{ *MemoryIdentityRepository }

func (supersededFailingRepo) SupersededRefreshToken(context.Context, string, string) (bool, error) {
	return false, errors.New("store down")
}

// If the store cannot say whether the token was held, the mismatch is treated
// as reuse: revoking is the safe failure.
func TestRefreshReuse_AnUnanswerableLookupRevokes(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryIdentityRepository()
	r := newEnrollRig(t, supersededFailingRepo{m}, m, dpop.NewMemoryReplayStore()).withRefresh(t, dpop.NewMemoryReplayStore())
	sess := r.enrolKey(ctx, t, "req_a", deviceKey(t))
	if _, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, "rt_unknown", "POST", "garbage"); !errors.Is(err, ErrTokenReused) {
		t.Fatalf("err = %v, want ErrTokenReused", err)
	}
	if stored, _ := m.GetSession(ctx, sess.SessionID); !stored.IsRevoked {
		t.Fatal("an unanswerable lookup did not revoke")
	}
}

// Two refreshes of one token race: the loser is refused without revoking (it
// was not theft), and its token, now superseded by the winner's swap, is reuse
// if presented again.
func TestRefreshReuse_ARaceLoserIsNotRevokedButItsTokenIsSuperseded(t *testing.T) {
	ctx := context.Background()
	svc := withFixedOtp(NewIdentityService(NewMemoryIdentityRepository(), testRefreshKey))
	sess := validatedSession(ctx, t, svc)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, errs[i] = svc.RefreshToken(ctx, sess.SessionID, sess.RefreshToken)
		}(i)
	}
	wg.Wait()
	wins, reused := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ErrTokenReused):
			// A late racer that read after the swap sees the superseded token:
			// that is reuse, and revoking is correct.
			reused++
		case !errors.Is(err, ErrInvalidToken):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d refreshes won, want exactly 1", wins)
	}
	// Presented again after the race, the token is reuse. If a late racer
	// already detected that, the session is revoked and the answer is the
	// revoked session's ErrInvalidToken.
	_, _, err := svc.RefreshToken(ctx, sess.SessionID, sess.RefreshToken)
	if reused == 0 && !errors.Is(err, ErrTokenReused) {
		t.Fatalf("the superseded token presented after the race: err = %v, want ErrTokenReused", err)
	}
	if reused > 0 && !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("after a detected reuse: err = %v, want the revoked session's ErrInvalidToken", err)
	}
}
