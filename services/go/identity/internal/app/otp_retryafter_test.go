package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// P-ID-5: "lock the MSISDN for a stated window and surface a real countdown".
// The 429 carried no countdown at all; it now carries Retry-After, computed
// from the deadline the repository returned in the same step that refused.

func retryAtOf(t *testing.T, err error) time.Time {
	t.Helper()
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v, want a *RateLimitError", err)
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Fatal("a *RateLimitError no longer matches ErrRateLimited")
	}
	return rl.RetryAt
}

func postValidateOtp(t *testing.T, h *IdentityHandler, reqID, code string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"requestId": reqID, "code": code})
	rec := httptest.NewRecorder()
	h.HandleValidateOtp(rec, httptest.NewRequest(http.MethodPost, "/otp/validate", bytes.NewReader(body)))
	return rec
}

// retryAfterHeader asserts a 429 with a positive integer Retry-After and
// returns it.
func retryAfterHeader(t *testing.T, rec *httptest.ResponseRecorder) int64 {
	t.Helper()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	raw := rec.Header().Get("Retry-After")
	secs, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || secs < 1 {
		t.Fatalf("Retry-After = %q, want a positive integer number of seconds", raw)
	}
	return secs
}

// Rounding: up to the next whole second, and never below 1.
func TestRetryAfter_SecondsRoundUpAndAreAlwaysPositive(t *testing.T) {
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		d    time.Duration
		want int64
	}{
		{900 * time.Second, 900},
		{899*time.Second + 500*time.Millisecond, 900},
		{time.Second + time.Nanosecond, 2},
		{time.Second, 1},
		{time.Millisecond, 1},
		{time.Nanosecond, 1},
		{0, 1},
		{-5 * time.Second, 1},
	} {
		if got := retryAfterSeconds(now.Add(tc.d), now); got != tc.want {
			t.Errorf("retryAfterSeconds(%v) = %d, want %d", tc.d, got, tc.want)
		}
	}
}

// A full request window reports when its OLDEST request leaves the window; the
// header counts down as the clock moves; the request is accepted exactly when
// the window frees.
func TestRetryAfter_RequestWindowCountsDownToTheOldestRequest(t *testing.T) {
	svc, _, clock := newRateLimitedSvc(t)
	h := NewIdentityHandler(svc)
	t0 := clock.now()

	for i := 0; i < 3; i++ {
		if rec, _ := postRequestOtp(t, h, rlMSISDN); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d", i, rec.Code)
		}
		clock.advance(time.Minute)
	}
	// t0+3m. The oldest request (t0) frees its slot at t0+15m.
	_, err := svc.RequestOtp(context.Background(), rlMSISDN)
	if got, want := retryAtOf(t, err), t0.Add(15*time.Minute); !got.Equal(want) {
		t.Fatalf("RetryAt = %v, want %v (oldest request + window)", got, want)
	}

	rec, body := postRequestOtp(t, h, rlMSISDN)
	if got := retryAfterHeader(t, rec); got != 12*60 {
		t.Errorf("Retry-After at t0+3m = %d, want 720", got)
	}
	if body["code"] != "ERR_RATE_LIMIT_EXCEEDED" || strings.Contains(rec.Body.String(), rlMSISDN) {
		t.Errorf("unexpected 429 body: %s", rec.Body.String())
	}

	clock.advance(5 * time.Minute) // t0+8m
	rec, _ = postRequestOtp(t, h, rlMSISDN)
	if got := retryAfterHeader(t, rec); got != 7*60 {
		t.Errorf("Retry-After at t0+8m = %d, want 420", got)
	}

	clock.advance(7*time.Minute - time.Second) // t0+15m-1s
	rec, _ = postRequestOtp(t, h, rlMSISDN)
	if got := retryAfterHeader(t, rec); got != 1 {
		t.Errorf("Retry-After one second before the window frees = %d, want 1", got)
	}

	clock.advance(time.Second) // t0+15m exactly
	if rec, _ := postRequestOtp(t, h, rlMSISDN); rec.Code != http.StatusOK {
		t.Errorf("at the advertised moment the request was refused: status %d", rec.Code)
	}
}

// Fractional remaining time is rounded up in the header itself.
func TestRetryAfter_FractionalRemainderRoundsUp(t *testing.T) {
	svc, _, clock := newRateLimitedSvc(t)
	h := NewIdentityHandler(svc)
	for i := 0; i < 3; i++ {
		postRequestOtp(t, h, rlMSISDN)
	}
	clock.advance(100*time.Second + 250*time.Millisecond) // 799.75s remain
	rec, _ := postRequestOtp(t, h, rlMSISDN)
	if got := retryAfterHeader(t, rec); got != 800 {
		t.Errorf("Retry-After with 799.75s remaining = %d, want 800", got)
	}
}

// A failed-verification lockout reports ITS deadline, on both endpoints, even
// though the request window has already emptied.
func TestRetryAfter_LockoutReportsItsOwnDeadline(t *testing.T) {
	svc, repo, clock := newRateLimitedSvc(t)
	h := NewIdentityHandler(svc)
	ctx := context.Background()
	t0 := clock.now()

	// Three requests at t0 fill the request window until t0+15m.
	for i := 0; i < 3; i++ {
		if _, err := svc.RequestOtp(ctx, rlMSISDN); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	clock.advance(10 * time.Minute)
	live := time.Now().Add(time.Hour)
	seedChallengeFor(ctx, t, repo, svc, "req_a", live)
	seedChallengeFor(ctx, t, repo, svc, "req_b", live)
	for _, id := range []string{"req_a", "req_a", "req_a", "req_b", "req_b"} {
		_, _, _ = svc.ValidateOtp(ctx, id, "000000")
		clock.advance(time.Second)
	}
	// Fifth failure at t0+10m+4s: locked until t0+25m+4s.
	lockEnd := t0.Add(25*time.Minute + 4*time.Second)

	_, _, err := svc.ValidateOtp(ctx, "req_b", testOtpCode)
	if got := retryAtOf(t, err); !got.Equal(lockEnd) {
		t.Errorf("verification RetryAt = %v, want the lockout end %v", got, lockEnd)
	}

	// Move past the request window's expiry (t0+15m). Only the lockout remains.
	clock.advance(6 * time.Minute) // t0+16m+5s
	_, err = svc.RequestOtp(ctx, rlMSISDN)
	if got := retryAtOf(t, err); !got.Equal(lockEnd) {
		t.Errorf("request RetryAt after the request window expired = %v, want the lockout end %v", got, lockEnd)
	}
	wantSecs := int64(lockEnd.Sub(clock.now()) / time.Second) // 539s exactly
	if got := retryAfterHeader(t, postValidateOtp(t, h, "req_b", testOtpCode)); got != wantSecs {
		t.Errorf("validate Retry-After = %d, want %d", got, wantSecs)
	}
	reqRec, _ := postRequestOtp(t, h, rlMSISDN)
	if got := retryAfterHeader(t, reqRec); got != wantSecs {
		t.Errorf("request Retry-After = %d, want %d", got, wantSecs)
	}

	// Exactly at the lockout end both endpoints work again.
	clock.advance(lockEnd.Sub(clock.now()))
	if rec := postValidateOtp(t, h, "req_b", testOtpCode); rec.Code != http.StatusOK {
		t.Errorf("verification at the lockout end: status %d, want 200", rec.Code)
	}
}

// When a lockout and a full counted window both apply, the later deadline is
// reported, so retrying at it is past both. Exercised with a lockout shorter
// than the window, where the two differ.
func TestRetryAfter_ReportsTheLaterOfLockoutAndWindow(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	short := OtpRatePolicy{MaxRequests: 3, MaxFailures: 5, Window: 15 * time.Minute, Lockout: time.Minute}
	m := NewMemoryIdentityRepository()
	for i := 0; i < 5; i++ {
		_, _ = m.ReserveOtpVerification(ctx, mp(rlMSISDN), t0.Add(time.Duration(i)*time.Second), short)
	}
	// Locked until t0+4s+1m, but the five failures keep the window full until
	// the oldest (t0) leaves it at t0+15m: the later of the two.
	_, err := m.ReserveOtpVerification(ctx, mp(rlMSISDN), t0.Add(10*time.Second), short)
	if got, want := retryAtOf(t, err), t0.Add(15*time.Minute); !got.Equal(want) {
		t.Errorf("RetryAt = %v, want %v", got, want)
	}
	_, err = m.ReserveOtpVerification(ctx, mp(rlMSISDN), t0.Add(5*time.Minute), short)
	if got, want := retryAtOf(t, err), t0.Add(15*time.Minute); !got.Equal(want) {
		t.Errorf("RetryAt after the lock lapsed = %v, want %v", got, want)
	}
}

// Every racer refused at the boundary gets the same, correct deadline.
func TestRetryAfter_ConcurrentRefusalsCarryTheDeadline(t *testing.T) {
	svc, _, clock := newRateLimitedSvc(t)
	ctx := context.Background()
	t0 := clock.now()
	for i := 0; i < 2; i++ {
		if _, err := svc.RequestOtp(ctx, rlMSISDN); err != nil {
			t.Fatal(err)
		}
	}
	const racers = 16
	start := make(chan struct{})
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = svc.RequestOtp(ctx, rlMSISDN)
		}()
	}
	close(start)
	wg.Wait()

	accepted := 0
	for _, err := range errs {
		if err == nil {
			accepted++
			continue
		}
		if got := retryAtOf(t, err); !got.Equal(t0.Add(15 * time.Minute)) {
			t.Errorf("a refused racer got RetryAt %v, want %v", got, t0.Add(15*time.Minute))
		}
	}
	if accepted != 1 {
		t.Errorf("%d racers accepted for the last slot, want 1", accepted)
	}
}

// bareLimitRepo refuses with the plain sentinel: a limiter that knows it must
// refuse but not until when.
type bareLimitRepo struct{ *MemoryIdentityRepository }

func (r *bareLimitRepo) ReserveOtpRequest(context.Context, string, time.Time, OtpRatePolicy) error {
	return ErrRateLimited
}

// With no deadline available, the 429 is still sent but no Retry-After is
// invented.
func TestRetryAfter_NoDeadlineMeansNoHeader(t *testing.T) {
	svc := withFixedOtp(NewIdentityService(&bareLimitRepo{NewMemoryIdentityRepository()}, []byte("k")))
	rec, body := postRequestOtp(t, NewIdentityHandler(svc), rlMSISDN)
	if rec.Code != http.StatusTooManyRequests || body["code"] != "ERR_RATE_LIMIT_EXCEEDED" {
		t.Fatalf("status %d body %v, want the 429", rec.Code, body)
	}
	if v := rec.Header().Get("Retry-After"); v != "" {
		t.Errorf("Retry-After = %q, want no header when the deadline is unknown", v)
	}
}
