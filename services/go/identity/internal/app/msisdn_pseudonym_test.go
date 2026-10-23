package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/dpop"
)

// MSISDNs at rest: everything stored is keyed by MSISDNPseudonym, and the raw
// number never leaves RequestOtp.

// mp is the pseudonym of raw under the tests' key; sid its subscriber id.
func mp(raw string) string {
	p, err := MSISDNPseudonym(testRefreshKey, raw)
	if err != nil {
		panic(fmt.Sprintf("test fixture %q is not canonical: %v", raw, err))
	}
	return p
}

func sid(raw string) string { return NewIdentityService(nil, testRefreshKey).subscriberID(raw) }

func TestMSISDNPseudonym_IsKeyedCanonicalAndDomainSeparated(t *testing.T) {
	const m = "+919876543210"
	p := mp(m)
	if !ValidMSISDNPseudonym(p) || len(p) != 64 || p != strings.ToLower(p) {
		t.Fatalf("pseudonym %q is not 64 lowercase hex", p)
	}
	if mp(m) != p {
		t.Error("not deterministic")
	}
	if other, _ := MSISDNPseudonym([]byte("another_key"), m); other == p {
		t.Error("the pseudonym does not depend on the key")
	}
	if mp("+919876543211") == p {
		t.Error("two numbers share a pseudonym")
	}
	svc := NewIdentityService(nil, testRefreshKey)
	unkeyed := sha256.Sum256([]byte(m))
	plain := hmac.New(sha256.New, testRefreshKey)
	plain.Write([]byte(m))
	for name, v := range map[string]string{
		"unkeyed SHA-256":                hex.EncodeToString(unkeyed[:]),
		"undomained HMAC (OTP codes)":    hex.EncodeToString(plain.Sum(nil)),
		"the code-hash function":         svc.hashString(m),
		"the refresh-token hash":         HashRefreshToken(testRefreshKey, m),
		"the subscriber id's HMAC input": strings.TrimPrefix(svc.subscriberID(m), "usr_"),
	} {
		if strings.HasPrefix(v, p[:32]) || v == p {
			t.Errorf("the pseudonym equals %s", name)
		}
	}
	if ValidMSISDNPseudonym(m) {
		t.Error("a raw number passes as a pseudonym")
	}

	// No alternate spelling earns an identifier of its own: every non-canonical
	// form is refused rather than pseudonymised.
	for _, bad := range []string{
		"919876543210", " +919876543210", "+919876543210 ", "+91 98765 43210", "+91-9876543210",
		"+0919876543210", "+91987654321O", "++919876543210", "", "+", "+123456",
	} {
		if got, err := MSISDNPseudonym(testRefreshKey, bad); !errors.Is(err, ErrInvalidMSISDN) || got != "" {
			t.Errorf("MSISDNPseudonym(%q) = (%q, %v), want ErrInvalidMSISDN", bad, got, err)
		}
	}
}

// fullSpy records every argument the service hands the repository.
type fullSpy struct {
	*MemoryIdentityRepository
	mu   sync.Mutex
	seen []string
}

func (s *fullSpy) note(v ...any) {
	s.mu.Lock()
	s.seen = append(s.seen, fmt.Sprintf("%+v", v))
	s.mu.Unlock()
}

func (s *fullSpy) SaveOtp(ctx context.Context, c *OtpChallenge) error {
	s.note(*c)
	return s.MemoryIdentityRepository.SaveOtp(ctx, c)
}

func (s *fullSpy) ReserveOtpRequest(ctx context.Context, k string, now time.Time, p OtpRatePolicy) error {
	s.note(k)
	return s.MemoryIdentityRepository.ReserveOtpRequest(ctx, k, now, p)
}

func (s *fullSpy) ReserveOtpVerification(ctx context.Context, k string, now time.Time, p OtpRatePolicy) (uint64, error) {
	s.note(k)
	return s.MemoryIdentityRepository.ReserveOtpVerification(ctx, k, now, p)
}

func (s *fullSpy) ReleaseOtpVerification(ctx context.Context, k string, id uint64) error {
	s.note(k)
	return s.MemoryIdentityRepository.ReleaseOtpVerification(ctx, k, id)
}

func (s *fullSpy) ConfirmOtpFailure(ctx context.Context, k string, id uint64) (time.Time, error) {
	s.note(k)
	return s.MemoryIdentityRepository.ConfirmOtpFailure(ctx, k, id)
}

func (s *fullSpy) ResetOtpFailures(ctx context.Context, k string) error {
	s.note(k)
	return s.MemoryIdentityRepository.ResetOtpFailures(ctx, k)
}

func (s *fullSpy) SaveSession(ctx context.Context, x *UserSession) error {
	s.note(*x)
	return s.MemoryIdentityRepository.SaveSession(ctx, x)
}

func (s *fullSpy) ReplaceSession(ctx context.Context, x *UserSession, h string) error {
	s.note(*x, h)
	return s.MemoryIdentityRepository.ReplaceSession(ctx, x, h)
}

// Through request, a wrong code, validation, refresh, enrolment and
// proof-bound refresh, no repository call and no stored state carries the
// number, with or without its '+'. Every rate key is the number's pseudonym.
func TestMSISDNPseudonym_RawNumbersNeverReachTheRepository(t *testing.T) {
	const m = "+919876543210"
	spy := &fullSpy{MemoryIdentityRepository: NewMemoryIdentityRepository()}
	svc := withFixedOtp(NewIdentityService(spy, testRefreshKey))
	ctx := context.Background()

	req, err := svc.RequestOtp(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, validateOtpErr := svc.ValidateOtp(ctx, req, "000000"); !errors.Is(validateOtpErr, ErrInvalidOtp) {
		t.Fatalf("wrong code: %v", validateOtpErr)
	}
	sess, _, err := svc.ValidateOtp(ctx, req, testOtpCode)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, refreshTokenErr := svc.RefreshToken(ctx, sess.SessionID, sess.RefreshToken); refreshTokenErr != nil {
		t.Fatal(refreshTokenErr)
	}
	rig := newEnrollRig(t, spy, spy.MemoryIdentityRepository, dpop.NewMemoryReplayStore()).withRefresh(t, dpop.NewMemoryReplayStore())
	req2, err := rig.svc.RequestOtp(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	k := deviceKey(t)
	esess, _, err := rig.svc.ValidateOtpWithDeviceProof(ctx, req2, testOtpCode, "POST", enrollProof(t, k, k, "e1", "POST", rig.clock.now()))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := rig.svc.RefreshTokenWithDeviceProof(ctx, esess.SessionID, esess.RefreshToken, "POST", rig.refreshProof(t, k, "r1")); err != nil {
		t.Fatal(err)
	}

	spy.mu.Lock()
	calls := strings.Join(spy.seen, "\n")
	spy.mu.Unlock()
	stored := fmt.Sprintf("%+v %+v", spy.MemoryIdentityRepository.otps, spy.MemoryIdentityRepository.sessions)
	for k := range spy.MemoryIdentityRepository.otpRates {
		stored += " " + k
	}
	for _, raw := range []string{m, strings.TrimPrefix(m, "+"), "9876543210"} {
		if strings.Contains(calls, raw) {
			t.Errorf("a repository call carried %q", raw)
		}
		if strings.Contains(stored, raw) {
			t.Errorf("repository state holds %q", raw)
		}
	}
	if !strings.Contains(calls, mp(m)) {
		t.Error("the number's pseudonym was not what the repository was given")
	}
	for key := range spy.MemoryIdentityRepository.otpRates {
		if key != mp(m) {
			t.Errorf("rate state keyed by %q, want only the pseudonym", key)
		}
	}
	if sess.SubscriberID != sid(m) || esess.SubscriberID != sid(m) {
		t.Error("the subscriber id changed with pseudonymisation")
	}
}

// The repository refuses a raw number on every MSISDN-keyed method, and keeps
// nothing from the attempt.
func TestMSISDNPseudonym_MemoryRepositoryRefusesRawNumbers(t *testing.T) {
	ctx := context.Background()
	r := NewMemoryIdentityRepository()
	for _, key := range []string{"+919876543210", "919876543210", "", strings.ToUpper(mp("+919876543210"))} {
		if err := r.SaveOtp(ctx, &OtpChallenge{RequestID: "req", MSISDNPseudonym: key, SubscriberID: "usr_x", ExpiresAt: time.Now()}); !errors.Is(err, ErrMSISDNNotPseudonymised) {
			t.Errorf("SaveOtp(%q): %v", key, err)
		}
		if err := r.ReserveOtpRequest(ctx, key, time.Now(), defaultOtpRatePolicy); !errors.Is(err, ErrMSISDNNotPseudonymised) {
			t.Errorf("ReserveOtpRequest(%q): %v", key, err)
		}
		if _, err := r.ReserveOtpVerification(ctx, key, time.Now(), defaultOtpRatePolicy); !errors.Is(err, ErrMSISDNNotPseudonymised) {
			t.Errorf("ReserveOtpVerification(%q): %v", key, err)
		}
		if err := r.ReleaseOtpVerification(ctx, key, 1); !errors.Is(err, ErrMSISDNNotPseudonymised) {
			t.Errorf("ReleaseOtpVerification(%q): %v", key, err)
		}
		if _, err := r.ConfirmOtpFailure(ctx, key, 1); !errors.Is(err, ErrMSISDNNotPseudonymised) {
			t.Errorf("ConfirmOtpFailure(%q): %v", key, err)
		}
		if err := r.ResetOtpFailures(ctx, key); !errors.Is(err, ErrMSISDNNotPseudonymised) {
			t.Errorf("ResetOtpFailures(%q): %v", key, err)
		}
	}
	if len(r.otps) != 0 || len(r.otpRates) != 0 {
		t.Error("a refused call stored state")
	}
}

// Two service instances with the same key share one number's allowance: the
// identifier they derive is the same, so the limit is not per instance.
func TestMSISDNPseudonym_InstancesShareRateState(t *testing.T) {
	shared := NewMemoryIdentityRepository()
	a := NewIdentityService(shared, testRefreshKey)
	b := NewIdentityService(shared, testRefreshKey)
	ctx := context.Background()
	for i, svc := range []*IdentityService{a, b, a} {
		if _, err := svc.RequestOtp(ctx, "+919876543210"); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if _, err := b.RequestOtp(ctx, "+919876543210"); !errors.Is(err, ErrRateLimited) {
		t.Errorf("fourth request across instances: err = %v, want ErrRateLimited", err)
	}
	if _, err := a.RequestOtp(ctx, "+919876543211"); err != nil {
		t.Errorf("another number: %v", err)
	}
}
