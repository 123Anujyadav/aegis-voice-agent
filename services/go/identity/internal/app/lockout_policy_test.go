package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The OTP lockout is deliberately FIXED: 15 minutes after 5 wrong codes in 15
// minutes, never escalating (see defaultOtpRatePolicy and the ADR-0029
// ratification brief §3a). These tests pin the properties that choice relies
// on, so an escalating or extendable lockout cannot creep in unnoticed.

// lockNumber makes five wrong guesses for number across two challenges,
// returning the lockout's end (the fifth failure's time plus the lockout).
func lockNumber(ctx context.Context, t *testing.T, svc *IdentityService, repo *MemoryIdentityRepository, clock *fakeClock, number, prefix string) time.Time {
	t.Helper()
	live := time.Now().Add(time.Hour)
	for _, id := range []string{prefix + "_a", prefix + "_b"} {
		if err := repo.SaveOtp(ctx, &OtpChallenge{
			RequestID: id, MSISDNPseudonym: mp(number), SubscriberID: sid(number),
			CodeHash: svc.hashString(testOtpCode), ExpiresAt: live,
		}); err != nil {
			t.Fatal(err)
		}
	}
	var fifth time.Time
	for _, id := range []string{prefix + "_a", prefix + "_a", prefix + "_a", prefix + "_b", prefix + "_b"} {
		fifth = clock.now()
		if _, _, err := svc.ValidateOtp(ctx, id, "000000"); !errors.Is(err, ErrInvalidOtp) {
			t.Fatalf("wrong code on %s: %v", id, err)
		}
		clock.advance(time.Second)
	}
	return fifth.Add(DefaultOtpRatePolicy().Lockout)
}

func retryAt(t *testing.T, err error) time.Time {
	t.Helper()
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v, want a *RateLimitError", err)
	}
	return rl.RetryAt
}

// Refusals during a lockout name its exact end, and guessing during it
// neither counts as a failure nor pushes the end back.
func TestLockoutPolicy_GuessingDuringALockoutCannotExtendIt(t *testing.T) {
	svc, repo, clock := newRateLimitedSvc(t)
	ctx := context.Background()
	end := lockNumber(ctx, t, svc, repo, clock, rlMSISDN, "req")
	failuresAtLock := len(repo.otpRates[mp(rlMSISDN)].failures)

	for i := 0; i < 50; i++ {
		_, _, err := svc.ValidateOtp(ctx, "req_b", "000000")
		if got := retryAt(t, err); !got.Equal(end) {
			t.Fatalf("attempt %d during the lockout: RetryAt %v, want the fixed end %v", i, got, end)
		}
		clock.advance(10 * time.Second)
	}
	if got := len(repo.otpRates[mp(rlMSISDN)].failures); got != failuresAtLock {
		t.Fatalf("refused guesses were counted: %d failures, want %d", got, failuresAtLock)
	}
	if _, err := svc.RequestOtp(ctx, rlMSISDN); !retryAt(t, err).Equal(end) {
		t.Fatalf("an OTP request during the lockout names a different end")
	}
}

// A second lockout is exactly as long as the first: the policy does not
// escalate.
func TestLockoutPolicy_RepeatedLockoutsDoNotEscalate(t *testing.T) {
	svc, repo, clock := newRateLimitedSvc(t)
	ctx := context.Background()
	lockout := DefaultOtpRatePolicy().Lockout

	end1 := lockNumber(ctx, t, svc, repo, clock, rlMSISDN, "first")
	clock.t = end1 // the first lockout has just ended
	start2 := clock.now()
	end2 := lockNumber(ctx, t, svc, repo, clock, rlMSISDN, "second")
	if got, want := end2.Sub(start2), lockout+4*time.Second; got != want {
		// lockNumber's fifth guess is 4s after its first.
		t.Fatalf("second lockout ends %v after it began, want %v: the lockout escalated", got, want)
	}
	clock.t = end2.Add(-time.Nanosecond)
	if _, _, err := svc.ValidateOtp(ctx, "second_b", testOtpCode); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("just before the second lockout ends: %v", err)
	}
	clock.t = end2
	if _, _, err := svc.ValidateOtp(ctx, "second_b", testOtpCode); err != nil {
		t.Fatalf("the second lockout outlasted its fixed length: %v", err)
	}
}

// One number's lockout never affects another number: a lockout cannot be
// spread by locking a neighbour.
func TestLockoutPolicy_LockoutsAreIsolatedPerNumber(t *testing.T) {
	svc, repo, clock := newRateLimitedSvc(t)
	ctx := context.Background()
	lockNumber(ctx, t, svc, repo, clock, rlMSISDN, "victim")

	other := "+14155550199"
	if err := repo.SaveOtp(ctx, &OtpChallenge{
		RequestID: "other_a", MSISDNPseudonym: mp(other), SubscriberID: sid(other),
		CodeHash: svc.hashString(testOtpCode), ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ValidateOtp(ctx, "other_a", testOtpCode); err != nil {
		t.Fatalf("another number was affected by the lockout: %v", err)
	}
	if _, err := svc.RequestOtp(ctx, other); err != nil {
		t.Fatalf("another number cannot request a code: %v", err)
	}
}

// The selected policy itself: five failures, a 15-minute window, a fixed
// 15-minute lockout.
func TestLockoutPolicy_IsTheFixedSelectedPolicy(t *testing.T) {
	p := DefaultOtpRatePolicy()
	if p.MaxFailures != 5 || p.Window != 15*time.Minute || p.Lockout != 15*time.Minute {
		t.Fatalf("policy = %+v; changing it is a recorded decision (brief §3a), not a refactor", p)
	}
}
