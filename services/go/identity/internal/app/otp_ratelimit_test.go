package app

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

// Per-MSISDN OTP rate limiting. Before it, the only bound was three wrong codes
// per CHALLENGE, and a caller could request another challenge, and another,
// for the same number without limit, so there was no bound per number at all.

// withoutOtpRateLimit gives a test of an UNRELATED property (code randomness,
// identifier uniqueness under load) an allowance large enough that the
// per-number limit cannot interfere. It is a test-only configuration of this
// service value; production construction always uses defaultOtpRatePolicy.
func withoutOtpRateLimit(s *IdentityService) *IdentityService {
	s.ratePolicy = OtpRatePolicy{MaxRequests: 1 << 30, MaxFailures: 1 << 30, Window: time.Minute, Lockout: time.Minute}
	return s
}

// fakeClock is a controllable clock for window tests: no sleeping.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

const rlMSISDN = "+14155552671"

func newRateLimitedSvc(t *testing.T) (*IdentityService, *MemoryIdentityRepository, *fakeClock) {
	t.Helper()
	repo := NewMemoryIdentityRepository()
	svc := withFixedOtp(NewIdentityService(repo, []byte("test_secret_key")))
	clock := &fakeClock{t: time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)}
	svc.now = clock.now
	return svc, repo, clock
}

func TestOtpRateLimit_DefaultPolicyIsTheSelectedOne(t *testing.T) {
	got := NewIdentityService(NewMemoryIdentityRepository(), []byte("k")).ratePolicy
	want := OtpRatePolicy{MaxRequests: 3, MaxFailures: 5, Window: 15 * time.Minute, Lockout: 15 * time.Minute}
	if got != want {
		t.Errorf("default policy = %+v, want %+v", got, want)
	}
}

// Three requests per rolling 15 minutes; a fourth is refused until the oldest
// request leaves the window.
func TestOtpRateLimit_RequestsPerWindow(t *testing.T) {
	svc, repo, clock := newRateLimitedSvc(t)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		if _, err := svc.RequestOtp(ctx, rlMSISDN); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		clock.advance(time.Minute)
	}
	codes := 0
	svc.newCode = func() string { codes++; return testOtpCode }
	before := len(repo.otps)
	if _, err := svc.RequestOtp(ctx, rlMSISDN); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("fourth request in the window: err = %v, want ErrRateLimited", err)
	}
	if codes != 0 || len(repo.otps) != before {
		t.Error("a refused request minted a code or stored a challenge")
	}

	// Another number is unaffected.
	if _, err := svc.RequestOtp(ctx, "+442071838750"); err != nil {
		t.Errorf("a different number was limited: %v", err)
	}

	// The clock is at t0+3m. The first request (t0) leaves the window at t0+15m.
	clock.advance(11*time.Minute + 59*time.Second)
	if _, err := svc.RequestOtp(ctx, rlMSISDN); !errors.Is(err, ErrRateLimited) {
		t.Errorf("one second before the oldest request expires: err = %v, want ErrRateLimited", err)
	}
	clock.advance(time.Second)
	if _, err := svc.RequestOtp(ctx, rlMSISDN); err != nil {
		t.Errorf("after the oldest request left the window: %v", err)
	}
}

// A malformed MSISDN is refused before the limiter and consumes nothing.
func TestOtpRateLimit_InvalidMSISDNConsumesNoAllowance(t *testing.T) {
	svc, repo, _ := newRateLimitedSvc(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if _, err := svc.RequestOtp(ctx, "14155552671"); !errors.Is(err, ErrInvalidMSISDN) {
			t.Fatalf("malformed: err = %v, want ErrInvalidMSISDN", err)
		}
	}
	if len(repo.otpRates) != 0 {
		t.Errorf("malformed requests created rate-limit state for %d key(s)", len(repo.otpRates))
	}
	for i := 1; i <= 3; i++ {
		if _, err := svc.RequestOtp(ctx, rlMSISDN); err != nil {
			t.Fatalf("canonical request %d after malformed ones: %v", i, err)
		}
	}
}

// seedChallengeFor writes a live challenge for rlMSISDN directly, so failure
// tests are not throttled by the request limit they are not testing.
func seedChallengeFor(ctx context.Context, t *testing.T, repo *MemoryIdentityRepository, svc *IdentityService, reqID string, expires time.Time) {
	t.Helper()
	if err := repo.SaveOtp(ctx, &OtpChallenge{
		RequestID: reqID, MSISDNPseudonym: mp(rlMSISDN), SubscriberID: sid(rlMSISDN), CodeHash: svc.hashString(testOtpCode), ExpiresAt: expires,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// Five wrong codes across challenges lock the number. During the lockout both
// requests and verifications, even with the right code, are refused, and the
// lockout ends after 15 minutes.
func TestOtpRateLimit_FiveFailuresLockTheNumber(t *testing.T) {
	svc, repo, clock := newRateLimitedSvc(t)
	ctx := context.Background()
	live := time.Now().Add(time.Hour)

	// Two challenges, so the per-challenge limit of three is not what stops it.
	seedChallengeFor(ctx, t, repo, svc, "req_a", live)
	seedChallengeFor(ctx, t, repo, svc, "req_b", live)
	for _, id := range []string{"req_a", "req_a", "req_a", "req_b", "req_b"} {
		if _, _, err := svc.ValidateOtp(ctx, id, "000000"); !errors.Is(err, ErrInvalidOtp) {
			t.Fatalf("wrong code on %s: err = %v, want ErrInvalidOtp", id, err)
		}
		clock.advance(time.Second)
	}

	// req_b has one per-challenge attempt left and this is the right code, but
	// the number is locked.
	if _, _, err := svc.ValidateOtp(ctx, "req_b", testOtpCode); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("verification during lockout: err = %v, want ErrRateLimited", err)
	}
	if len(repo.sessions) != 0 {
		t.Fatal("a session was created during lockout")
	}
	if _, err := svc.RequestOtp(ctx, rlMSISDN); !errors.Is(err, ErrRateLimited) {
		t.Errorf("OTP request during lockout: err = %v, want ErrRateLimited", err)
	}
	if ch, err := repo.GetOtp(ctx, "req_b"); err != nil || ch.Attempts != 2 {
		t.Errorf("a refused verification touched the challenge: %+v, %v", ch, err)
	}

	// The fifth failure was at t0+4s, so the lockout ends at t0+4s+15m. The
	// clock is now t0+5s.
	clock.advance(15*time.Minute - 2*time.Second) // t0+15m+3s
	if _, _, err := svc.ValidateOtp(ctx, "req_b", testOtpCode); !errors.Is(err, ErrRateLimited) {
		t.Errorf("one second before the lockout ends: err = %v, want ErrRateLimited", err)
	}
	clock.advance(time.Second) // t0+15m+4s
	if _, _, err := svc.ValidateOtp(ctx, "req_b", testOtpCode); err != nil {
		t.Errorf("after the lockout ended, the correct code was refused: %v", err)
	}
}

// A correct verification before the threshold succeeds and resets the
// number's failures, giving a fresh allowance.
func TestOtpRateLimit_SuccessResetsFailures(t *testing.T) {
	svc, repo, clock := newRateLimitedSvc(t)
	ctx := context.Background()
	live := time.Now().Add(time.Hour)
	seedChallengeFor(ctx, t, repo, svc, "req_a", live)
	seedChallengeFor(ctx, t, repo, svc, "req_b", live)
	seedChallengeFor(ctx, t, repo, svc, "req_c", live)

	for _, id := range []string{"req_a", "req_a", "req_a", "req_b"} { // four failures
		if _, _, err := svc.ValidateOtp(ctx, id, "000000"); !errors.Is(err, ErrInvalidOtp) {
			t.Fatalf("wrong code: %v", err)
		}
		clock.advance(time.Second)
	}
	if _, _, err := svc.ValidateOtp(ctx, "req_b", testOtpCode); err != nil {
		t.Fatalf("correct code before the threshold: %v", err)
	}
	if st := repo.otpRates[mp(rlMSISDN)]; len(st.failures) != 0 || !st.lockedUntil.IsZero() {
		t.Errorf("success did not reset the failures: %d left, locked until %v", len(st.failures), st.lockedUntil)
	}
	// Without the reset, these two would be failures five and six.
	for i := 0; i < 2; i++ {
		if _, _, err := svc.ValidateOtp(ctx, "req_c", "000000"); !errors.Is(err, ErrInvalidOtp) {
			t.Fatalf("wrong code after reset: %v", err)
		}
	}
	if _, _, err := svc.ValidateOtp(ctx, "req_c", testOtpCode); err != nil {
		t.Errorf("after the reset, two failures locked the number: %v", err)
	}
}

// Missing and expired challenges are not counted as failures.
func TestOtpRateLimit_MissingAndExpiredAreNotFailures(t *testing.T) {
	svc, repo, _ := newRateLimitedSvc(t)
	ctx := context.Background()
	seedChallengeFor(ctx, t, repo, svc, "req_old", time.Now().Add(-time.Minute))
	for i := 0; i < 10; i++ {
		if _, _, err := svc.ValidateOtp(ctx, "req_missing", "000000"); !errors.Is(err, ErrInvalidOtp) {
			t.Fatalf("missing: err = %v", err)
		}
	}
	if _, _, err := svc.ValidateOtp(ctx, "req_old", "000000"); !errors.Is(err, ErrExpiredOtp) {
		t.Fatalf("expired: err = %v, want ErrExpiredOtp", err)
	}
	if st, ok := repo.otpRates[mp(rlMSISDN)]; ok && len(st.failures) != 0 {
		t.Errorf("%d failures counted for missing or expired challenges", len(st.failures))
	}
}

// Concurrent requests at the boundary: many callers race for the last slot and
// exactly one wins.
func TestOtpRateLimit_ConcurrentRequestsCannotExceedTheLimit(t *testing.T) {
	svc, _, _ := newRateLimitedSvc(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := svc.RequestOtp(ctx, rlMSISDN); err != nil {
			t.Fatalf("setup request %d: %v", i, err)
		}
	}
	const racers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted, limited := 0, 0
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.RequestOtp(ctx, rlMSISDN)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				accepted++
			case errors.Is(err, ErrRateLimited):
				limited++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if accepted != 1 || limited != racers-1 {
		t.Errorf("%d racers for the last slot: %d accepted, %d limited; want 1 and %d", racers, accepted, limited, racers-1)
	}
}

// Concurrent wrong guesses across challenges can never exceed five evaluated
// codes, even when every request has read its challenge before any wrote.
func TestOtpRateLimit_ConcurrentWrongGuessesCannotExceedFive(t *testing.T) {
	names := []string{"g1", "g2", "g3", "g4", "g5", "g6", "g7"}
	repo := newOrderedOtpRepo(names...)
	svc := withFixedOtp(NewIdentityService(repo, []byte("test_secret_key")))
	clock := &fakeClock{t: time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)}
	svc.now = clock.now
	live := time.Now().Add(time.Hour)
	for _, id := range []string{"req_1", "req_2", "req_3", "req_4"} {
		seedChallengeFor(context.Background(), t, repo.MemoryIdentityRepository, svc, id, live)
	}
	ids := []string{"req_1", "req_1", "req_2", "req_2", "req_3", "req_3", "req_4"}

	results := make([]validateResult, len(ids))
	done := make([]<-chan struct{}, len(ids))
	for i, id := range ids {
		done[i] = goValidate(asCaller(context.Background(), names[i]), svc, id, "000000", &results[i])
	}
	repo.waitAllRead(t, len(ids))
	for _, n := range names {
		repo.release(n)
	}
	invalid, limited := 0, 0
	for i := range ids {
		await(t, done[i], names[i])
		switch {
		case errors.Is(results[i].err, ErrInvalidOtp):
			invalid++
		case errors.Is(results[i].err, ErrRateLimited):
			limited++
		default:
			t.Errorf("guess %d: unexpected err %v", i, results[i].err)
		}
	}
	if invalid != 5 || limited != 2 {
		t.Errorf("7 concurrent wrong guesses: %d evaluated, %d limited; want 5 and 2", invalid, limited)
	}
	repo.MemoryIdentityRepository.mu.RLock()
	locked := repo.MemoryIdentityRepository.otpRates[mp(rlMSISDN)].lockedUntil.After(clock.now())
	repo.MemoryIdentityRepository.mu.RUnlock()
	if !locked {
		t.Error("five failures did not lock the number")
	}
}

// failingRateRepo fails the rate-limit operations on demand.
type failingRateRepo struct {
	*MemoryIdentityRepository
	failRequest, failVerify bool
}

func (r *failingRateRepo) ReserveOtpRequest(ctx context.Context, m string, now time.Time, p OtpRatePolicy) error {
	if r.failRequest {
		return errInternalStore
	}
	return r.MemoryIdentityRepository.ReserveOtpRequest(ctx, m, now, p)
}

func (r *failingRateRepo) ReserveOtpVerification(ctx context.Context, m string, now time.Time, p OtpRatePolicy) (uint64, error) {
	if r.failVerify {
		return 0, errInternalStore
	}
	return r.MemoryIdentityRepository.ReserveOtpVerification(ctx, m, now, p)
}

// A rate-limit store failure is surfaced, never treated as "allowed", and is
// reported as a 500 rather than a 429 or a 401.
func TestOtpRateLimit_StoreFailuresAreSurfacedNotAllowed(t *testing.T) {
	repo := &failingRateRepo{MemoryIdentityRepository: NewMemoryIdentityRepository(), failRequest: true, failVerify: true}
	svc := withFixedOtp(NewIdentityService(repo, []byte("test_secret_key")))
	ctx := context.Background()

	if _, err := svc.RequestOtp(ctx, rlMSISDN); !errors.Is(err, errInternalStore) {
		t.Errorf("RequestOtp with a failing limiter: err = %v, want the store's error", err)
	}
	if len(repo.otps) != 0 {
		t.Error("a challenge was stored although the limiter failed")
	}
	if rec, _ := postRequestOtp(t, NewIdentityHandler(svc), rlMSISDN); rec.Code != http.StatusInternalServerError {
		t.Errorf("HTTP status = %d, want 500", rec.Code)
	}

	seedChallengeFor(ctx, t, repo.MemoryIdentityRepository, svc, "req_a", time.Now().Add(time.Hour))
	if _, _, err := svc.ValidateOtp(ctx, "req_a", testOtpCode); !errors.Is(err, errInternalStore) {
		t.Errorf("ValidateOtp with a failing limiter: err = %v, want the store's error", err)
	}
	if len(repo.sessions) != 0 {
		t.Error("a session was created although the limiter failed")
	}
	if _, err := repo.GetOtp(ctx, "req_a"); err != nil {
		t.Error("the challenge was consumed although the limiter failed")
	}
}

// The HTTP contract: the platform's existing 429.
func TestOtpRateLimitAPI_Uses429(t *testing.T) {
	svc, _, _ := newRateLimitedSvc(t)
	h := NewIdentityHandler(svc)
	for i := 0; i < 3; i++ {
		if rec, _ := postRequestOtp(t, h, rlMSISDN); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d", i, rec.Code)
		}
	}
	rec, body := postRequestOtp(t, h, rlMSISDN)
	if rec.Code != http.StatusTooManyRequests || body["code"] != "ERR_RATE_LIMIT_EXCEEDED" || body["detail"] != "Rate limit exceeded" {
		t.Errorf("fourth request: status %d, body %v; want 429 ERR_RATE_LIMIT_EXCEEDED", rec.Code, body)
	}
}

// TestRepo_OtpRateLimitOperations pins the repository contract directly,
// including a policy whose lockout is SHORTER than its window. Under the
// default policy the lockout (15m) covers the whole window (15m), so the lock
// alone stops a sixth failure; with a shorter lockout, the failure count must
// still hold once the lock has lapsed.
func TestRepo_OtpRateLimitOperations(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	short := OtpRatePolicy{MaxRequests: 3, MaxFailures: 5, Window: 15 * time.Minute, Lockout: time.Minute}

	m := NewMemoryIdentityRepository()
	for i := 0; i < 5; i++ {
		if _, err := m.ReserveOtpVerification(ctx, mp(rlMSISDN), t0.Add(time.Duration(i)*time.Second), short); err != nil {
			t.Fatalf("reservation %d: %v", i+1, err)
		}
	}
	if _, err := m.ReserveOtpVerification(ctx, mp(rlMSISDN), t0.Add(30*time.Second), short); !errors.Is(err, ErrRateLimited) {
		t.Errorf("during the lockout: err = %v, want ErrRateLimited", err)
	}
	// The lock (t0+4s+1m) has lapsed, but all five failures are still inside
	// the 15-minute window.
	if _, err := m.ReserveOtpVerification(ctx, mp(rlMSISDN), t0.Add(2*time.Minute), short); !errors.Is(err, ErrRateLimited) {
		t.Errorf("after a short lockout, inside the window: err = %v, want ErrRateLimited", err)
	}
	if _, err := m.ReserveOtpVerification(ctx, mp(rlMSISDN), t0.Add(15*time.Minute+5*time.Second), short); err != nil {
		t.Errorf("after every failure left the window: %v", err)
	}

	// Releasing the reservation that triggered a lockout withdraws the lock.
	m = NewMemoryIdentityRepository()
	var lockingID uint64
	for i := 0; i < 5; i++ {
		lockingID, _ = m.ReserveOtpVerification(ctx, mp(rlMSISDN), t0.Add(time.Duration(i)*time.Second), defaultOtpRatePolicy)
	}
	if err := m.ReleaseOtpVerification(ctx, mp(rlMSISDN), lockingID); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := m.ReserveOtpRequest(ctx, mp(rlMSISDN), t0.Add(5*time.Second), defaultOtpRatePolicy); err != nil {
		t.Errorf("a released reservation left its lockout in place: %v", err)
	}

	// Reset clears failures and lockout.
	for i := 4; i < 9; i++ {
		_, _ = m.ReserveOtpVerification(ctx, mp(rlMSISDN), t0.Add(time.Duration(i)*time.Second), defaultOtpRatePolicy)
	}
	if err := m.ResetOtpFailures(ctx, mp(rlMSISDN)); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := m.ReserveOtpVerification(ctx, mp(rlMSISDN), t0.Add(10*time.Second), defaultOtpRatePolicy); err != nil {
		t.Errorf("after reset: %v", err)
	}
}
