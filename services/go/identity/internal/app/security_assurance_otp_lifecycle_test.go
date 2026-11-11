// AI-performed internal security assurance (2026-11-11); not an independent security review. docs/phase19/OD-12-REVISED-DIRECTION-2026-11-11.md §4.

package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Threat T-c: a code that outlives its advertised lifetime. Threat T-k: a
// second request for one number either multiplying the guessing budget or
// changing the first challenge in a way nobody decided.

// TestSecurityAssurance_ChallengeExpiresAtNowPlusOtpLifetime: the stored
// challenge expires OtpLifetime (five minutes) after it was requested, within
// two seconds, and the lifetime is still the five minutes the HTTP layer
// advertises as expiresInSeconds: 300.
func TestSecurityAssurance_ChallengeExpiresAtNowPlusOtpLifetime(t *testing.T) {
	t.Parallel()
	if OtpLifetime != 5*time.Minute {
		t.Fatalf("OtpLifetime = %v, want 5m (the HTTP API advertises 300 seconds)", OtpLifetime)
	}
	repo := NewMemoryIdentityRepository()
	svc := withFixedOtp(NewIdentityService(repo, []byte("test-secret-key")))
	ctx := context.Background()

	before := time.Now()
	reqID, err := svc.RequestOtp(ctx, "+14155552671")
	after := time.Now()
	if err != nil {
		t.Fatal(err)
	}
	ch, err := repo.GetOtp(ctx, reqID)
	if err != nil {
		t.Fatal(err)
	}
	lo, hi := before.Add(OtpLifetime).Add(-2*time.Second), after.Add(OtpLifetime).Add(2*time.Second)
	if ch.ExpiresAt.Before(lo) || ch.ExpiresAt.After(hi) {
		t.Errorf("ExpiresAt = %v, want within 2s of now+%v (between %v and %v)", ch.ExpiresAt, OtpLifetime, lo, hi)
	}
	if ch.Attempts != 0 {
		t.Errorf("a new challenge starts with %d attempts", ch.Attempts)
	}
}

// saTwoChallenges requests two sign-in challenges for rlMSISDN, minting
// distinct codes, and returns their request ids and codes.
func saTwoChallenges(ctx context.Context, t *testing.T, svc *IdentityService) (idA, codeA, idB, codeB string) {
	t.Helper()
	codes := []string{"111111", "222222"}
	n := 0
	svc.newCode = func() string { c := codes[n]; n++; return c }
	var err error
	if idA, err = svc.RequestOtp(ctx, rlMSISDN); err != nil {
		t.Fatalf("first request: %v", err)
	}
	if idB, err = svc.RequestOtp(ctx, rlMSISDN); err != nil {
		t.Fatalf("second request: %v", err)
	}
	if idA == idB {
		t.Fatal("two requests share a request id")
	}
	return idA, codes[0], idB, codes[1]
}

// TestSecurityAssurance_SecondRequestDoesNotInvalidateTheFirst pins TODAY'S
// behaviour: a second request for the same number leaves the first challenge
// live, and each challenge accepts only its own code. Invalidating earlier
// challenges on a new request is ADR-0033 Q4, an OWNER decision; if that
// decision changes the behaviour, this test is expected to change with it.
// The guessing budget is not multiplied by it: see the lockout test below.
func TestSecurityAssurance_SecondRequestDoesNotInvalidateTheFirst(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("the first challenge validates with its own code", func(t *testing.T) {
		t.Parallel()
		svc, _, _ := newRateLimitedSvc(t)
		idA, codeA, idB, codeB := saTwoChallenges(ctx, t, svc)
		if _, _, err := svc.ValidateOtp(ctx, idA, codeA); err != nil {
			t.Fatalf("the first challenge, after a second request: %v", err)
		}
		if _, _, err := svc.ValidateOtp(ctx, idB, codeB); err != nil {
			t.Fatalf("the second challenge: %v", err)
		}
	})

	t.Run("a challenge does not accept the other challenge's code", func(t *testing.T) {
		t.Parallel()
		svc, repo, _ := newRateLimitedSvc(t)
		idA, codeA, idB, codeB := saTwoChallenges(ctx, t, svc)
		if _, _, err := svc.ValidateOtp(ctx, idA, codeB); !errors.Is(err, ErrInvalidOtp) {
			t.Fatalf("first challenge with the second code: err = %v, want ErrInvalidOtp", err)
		}
		if _, _, err := svc.ValidateOtp(ctx, idB, codeA); !errors.Is(err, ErrInvalidOtp) {
			t.Fatalf("second challenge with the first code: err = %v, want ErrInvalidOtp", err)
		}
		if len(repo.sessions) != 0 {
			t.Error("a cross-challenge code created a session")
		}
	})
}

// TestSecurityAssurance_ChallengesShareTheNumbersFailureBudget: wrong codes on
// either of two live challenges for one number draw on ONE per-number budget
// of five. After five (interleaved) failures, the right code on a challenge
// that still has per-challenge attempts left is refused as rate-limited and no
// session exists. A second request therefore does not buy extra guesses.
func TestSecurityAssurance_ChallengesShareTheNumbersFailureBudget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, repo, clock := newRateLimitedSvc(t)
	idA, codeA, idB, codeB := saTwoChallenges(ctx, t, svc)

	// A, B, A, B, A: no challenge reaches its own limit of three before the
	// fifth failure except A, on the fifth itself.
	for i, id := range []string{idA, idB, idA, idB, idA} {
		if _, _, err := svc.ValidateOtp(ctx, id, "000000"); !errors.Is(err, ErrInvalidOtp) {
			t.Fatalf("failure %d: err = %v, want ErrInvalidOtp", i+1, err)
		}
		clock.advance(time.Second)
	}
	if ch, err := repo.GetOtp(ctx, idB); err != nil || ch.Attempts != 2 {
		t.Fatalf("challenge B after two failures: %+v, %v", ch, err)
	}
	if _, _, err := svc.ValidateOtp(ctx, idB, codeB); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("right code on B after five failures across both challenges: err = %v, want ErrRateLimited", err)
	}
	if _, _, err := svc.ValidateOtp(ctx, idA, codeA); err == nil {
		t.Fatal("the exhausted first challenge validated during the lockout")
	}
	if len(repo.sessions) != 0 {
		t.Errorf("%d session(s) created during the shared lockout", len(repo.sessions))
	}
}
