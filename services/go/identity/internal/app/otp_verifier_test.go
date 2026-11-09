package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// A code-generating provider (OtpVerifier, such as Twilio Verify): identity
// never sees the code, stores the provider's verification id, and keeps every
// other control its own. These tests use a fake provider; nothing leaves the
// process.

// Made-up identifiers in Twilio's documented shapes (fakeSID).
var (
	verifierRef = fakeSID("V", "E")
)

const (
	verifierNumber = "+919876543210"
	verifierCode   = "424242"
)

type fakeOtpVerifier struct {
	mu       sync.Mutex
	starts   []string // numbers
	checks   int
	startErr error
	ref      string
	checkErr error
}

func (f *fakeOtpVerifier) StartVerification(_ context.Context, msisdn string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, msisdn)
	if f.startErr != nil {
		return "", f.startErr
	}
	if f.ref != "" {
		return f.ref, nil
	}
	return verifierRef, nil
}

func (f *fakeOtpVerifier) CheckVerification(_ context.Context, ref, code string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	if f.checkErr != nil {
		return false, f.checkErr
	}
	return ref == verifierRef && code == verifierCode, nil
}

func (f *fakeOtpVerifier) checkCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks
}

func newVerifierService(t *testing.T) (*IdentityService, *MemoryIdentityRepository, *fakeOtpVerifier) {
	t.Helper()
	m := NewMemoryIdentityRepository()
	svc := NewIdentityService(m, testRefreshKey)
	svc.newCode = func() string {
		t.Error("identity minted a code although the provider generates them")
		return "000000"
	}
	v := &fakeOtpVerifier{}
	svc.SetOtpVerifier(v)
	return svc, m, v
}

func TestOtpVerifier_RequestStoresTheProviderRefAndNoCode(t *testing.T) {
	svc, m, v := newVerifierService(t)
	if !svc.CanDeliverOtp() {
		t.Fatal("CanDeliverOtp is false with a verifier set")
	}
	reqID, err := svc.RequestOtp(context.Background(), verifierNumber)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.starts) != 1 || v.starts[0] != verifierNumber {
		t.Fatalf("starts %v", v.starts)
	}
	ch, err := m.GetOtp(context.Background(), reqID)
	if err != nil {
		t.Fatal(err)
	}
	if ch.ProviderRef != verifierRef || ch.CodeHash != "" {
		t.Fatalf("challenge %+v: want the provider ref and no code hash", ch)
	}
	if !ValidMSISDNPseudonym(ch.MSISDNPseudonym) || ch.SubscriberID == "" {
		t.Errorf("challenge keys %+v", ch)
	}
	if got := time.Until(ch.ExpiresAt); got <= 0 || got > OtpLifetime {
		t.Errorf("expiry in %v, want identity's own lifetime %v", got, OtpLifetime)
	}
}

func TestOtpVerifier_ApprovedCodeSignsInOnce(t *testing.T) {
	svc, _, v := newVerifierService(t)
	reqID, _ := svc.RequestOtp(context.Background(), verifierNumber)
	session, _, err := svc.ValidateOtp(context.Background(), reqID, verifierCode)
	if err != nil || session.SessionID == "" {
		t.Fatalf("(%v, %v)", session, err)
	}
	// Reuse is refused by identity, without asking the provider again.
	if _, _, err := svc.ValidateOtp(context.Background(), reqID, verifierCode); !errors.Is(err, ErrInvalidOtp) {
		t.Fatalf("reuse: %v, want ErrInvalidOtp", err)
	}
	if v.checkCount() != 1 {
		t.Errorf("%d provider checks, want 1", v.checkCount())
	}
}

// Wrong codes count against identity's attempt limit; once exhausted, the
// provider is not asked again, even with the right code.
func TestOtpVerifier_WrongCodesExhaustTheChallenge(t *testing.T) {
	svc, m, v := newVerifierService(t)
	reqID, _ := svc.RequestOtp(context.Background(), verifierNumber)
	for i := 0; i < maxOtpAttempts; i++ {
		if _, _, err := svc.ValidateOtp(context.Background(), reqID, "111111"); !errors.Is(err, ErrInvalidOtp) {
			t.Fatalf("wrong %d: %v", i+1, err)
		}
	}
	if _, _, err := svc.ValidateOtp(context.Background(), reqID, verifierCode); !errors.Is(err, ErrInvalidOtp) {
		t.Fatalf("right code after exhaustion: %v, want ErrInvalidOtp", err)
	}
	if v.checkCount() != maxOtpAttempts {
		t.Errorf("%d provider checks, want %d", v.checkCount(), maxOtpAttempts)
	}
	if _, err := m.GetOtp(context.Background(), reqID); !errors.Is(err, ErrInvalidOtp) {
		t.Error("the exhausted challenge was kept")
	}
}

// Expiry is identity's: an expired challenge is refused before the provider
// is asked.
func TestOtpVerifier_ExpiredChallengeIsRefusedWithoutAskingTheProvider(t *testing.T) {
	svc, m, v := newVerifierService(t)
	reqID, _ := svc.RequestOtp(context.Background(), verifierNumber)
	ch, _ := m.GetOtp(context.Background(), reqID)
	ch.ExpiresAt = time.Now().Add(-time.Second)
	if err := m.SaveOtp(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ValidateOtp(context.Background(), reqID, verifierCode); !errors.Is(err, ErrExpiredOtp) {
		t.Fatalf("%v, want ErrExpiredOtp", err)
	}
	if v.checkCount() != 0 {
		t.Error("the provider was asked about an expired challenge")
	}
}

// A provider that cannot answer is not a wrong code: nothing is counted, the
// challenge stays, and the right code works once the provider recovers.
func TestOtpVerifier_ProviderFailureCountsNothing(t *testing.T) {
	svc, m, v := newVerifierService(t)
	reqID, _ := svc.RequestOtp(context.Background(), verifierNumber)
	v.checkErr = errors.New("twilio says +919876543210 is unreachable")
	for i := 0; i < maxOtpAttempts+2; i++ {
		_, _, err := svc.ValidateOtp(context.Background(), reqID, verifierCode)
		if !errors.Is(err, ErrOtpVerificationUnavailable) {
			t.Fatalf("attempt %d: %v, want ErrOtpVerificationUnavailable", i+1, err)
		}
		if err.Error() != ErrOtpVerificationUnavailable.Error() {
			t.Fatalf("the provider's text leaked: %v", err)
		}
	}
	ch, err := m.GetOtp(context.Background(), reqID)
	if err != nil || ch.Attempts != 0 {
		t.Fatalf("challenge (%+v, %v): provider failures were counted", ch, err)
	}
	v.checkErr = nil
	if _, _, err := svc.ValidateOtp(context.Background(), reqID, verifierCode); err != nil {
		t.Fatalf("after recovery: %v", err)
	}
}

func TestOtpVerifier_StartFailureLeavesNoChallengeAndStillCounts(t *testing.T) {
	svc, m, v := newVerifierService(t)
	v.startErr = errors.New("twilio: 60200 invalid To +919876543210")
	_, err := svc.RequestOtp(context.Background(), verifierNumber)
	if !errors.Is(err, ErrOtpDeliveryFailed) || err.Error() != ErrOtpDeliveryFailed.Error() {
		t.Fatalf("%v, want exactly ErrOtpDeliveryFailed", err)
	}
	if n := len(m.otps); n != 0 {
		t.Fatalf("%d challenges stored after a failed start", n)
	}
	// The failed request still counted: two more fill the allowance.
	v.startErr = nil
	for i := 0; i < DefaultOtpRatePolicy().MaxRequests-1; i++ {
		if _, err := svc.RequestOtp(context.Background(), verifierNumber); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.RequestOtp(context.Background(), verifierNumber); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("%v, want the rate limit", err)
	}
}

// identity's per-number request limit applies before the provider is asked:
// the fourth request in the window sends nothing.
func TestOtpVerifier_RateLimitBeforeTheProvider(t *testing.T) {
	svc, _, v := newVerifierService(t)
	p := DefaultOtpRatePolicy()
	for i := 0; i < p.MaxRequests; i++ {
		if _, err := svc.RequestOtp(context.Background(), verifierNumber); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.RequestOtp(context.Background(), verifierNumber); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("%v, want the rate limit", err)
	}
	if len(v.starts) != p.MaxRequests {
		t.Errorf("%d provider starts, want %d", len(v.starts), p.MaxRequests)
	}
	if _, err := svc.RequestOtp(context.Background(), "9876543210"); !errors.Is(err, ErrInvalidMSISDN) {
		t.Fatalf("a malformed number: %v", err)
	}
	if len(v.starts) != p.MaxRequests {
		t.Error("a malformed number reached the provider")
	}
}

// A provider answer that is not a usable id is a failed delivery.
func TestOtpVerifier_InvalidProviderRefIsRefused(t *testing.T) {
	for _, ref := range []string{"VE 123", "VE-123", "x", "VE" + string(make([]byte, 70))} {
		svc, m, v := newVerifierService(t)
		v.ref = ref
		if _, err := svc.RequestOtp(context.Background(), verifierNumber); !errors.Is(err, ErrOtpDeliveryFailed) {
			t.Errorf("ref %q: %v, want ErrOtpDeliveryFailed", ref, err)
		}
		if len(m.otps) != 0 {
			t.Errorf("ref %q was stored", ref)
		}
	}
}

// A provider challenge with no verifier configured (the provider was turned
// off) cannot be checked: unavailable, uncounted. A challenge with both or
// neither code source is refused, uncounted.
func TestOtpVerifier_InconsistentChallenges(t *testing.T) {
	m := NewMemoryIdentityRepository()
	svc := NewIdentityService(m, testRefreshKey)
	pseudonym, _ := MSISDNPseudonym(testRefreshKey, verifierNumber)
	save := func(id, hash, ref string) {
		t.Helper()
		if err := m.SaveOtp(context.Background(), &OtpChallenge{
			RequestID: id, MSISDNPseudonym: pseudonym, SubscriberID: "usr_x", CodeHash: hash, ProviderRef: ref,
			ExpiresAt: time.Now().Add(time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	save("req_provider", "", verifierRef)
	if _, _, err := svc.ValidateOtp(context.Background(), "req_provider", verifierCode); !errors.Is(err, ErrOtpVerificationUnavailable) {
		t.Errorf("no verifier: %v, want ErrOtpVerificationUnavailable", err)
	}
	save("req_both", svc.hashString(verifierCode), verifierRef)
	save("req_neither", "", "")
	for _, id := range []string{"req_both", "req_neither"} {
		if _, _, err := svc.ValidateOtp(context.Background(), id, verifierCode); !errors.Is(err, ErrInvalidOtp) {
			t.Errorf("%s: %v, want ErrInvalidOtp", id, err)
		}
		if ch, _ := m.GetOtp(context.Background(), id); ch == nil || ch.Attempts != 0 {
			t.Errorf("%s: counted as a wrong code", id)
		}
	}
	// None of these counted against the number.
	svc.SetOtpVerifier(&fakeOtpVerifier{})
	if _, _, err := svc.ValidateOtp(context.Background(), "req_provider", verifierCode); err != nil {
		t.Errorf("the provider challenge after the verifier returned: %v", err)
	}
}

// identity's own codes keep working with a verifier configured (a challenge
// issued before the switch), and the verifier is not asked about them.
func TestOtpVerifier_IdentityCodesStillCheckedLocally(t *testing.T) {
	m := NewMemoryIdentityRepository()
	svc := NewIdentityService(m, testRefreshKey)
	sender := &recordingSender{}
	svc.SetOtpSender(sender)
	reqID, err := svc.RequestOtp(context.Background(), verifierNumber)
	if err != nil {
		t.Fatal(err)
	}
	v := &fakeOtpVerifier{}
	svc.SetOtpVerifier(v)
	if _, _, err := svc.ValidateOtp(context.Background(), reqID, sender.code); err != nil {
		t.Fatal(err)
	}
	if v.checkCount() != 0 {
		t.Error("the verifier was asked about identity's own code")
	}
}

func TestValidOtpProviderRef(t *testing.T) {
	for ref, want := range map[string]bool{
		verifierRef: true, "ab": true, "a": false, "": false, "VE_1": false, "VE 1": false,
		string(make([]byte, 65)): false, "é1": false,
	} {
		if got := ValidOtpProviderRef(ref); got != want {
			t.Errorf("ValidOtpProviderRef(%q) = %v", ref, got)
		}
	}
}

// fakeSID builds a made-up Twilio identifier of the documented shape (two
// letters and 32 lower-case hex digits) at run time, so that no
// credential-shaped literal sits in the source. None is a real identifier.
func fakeSID(a, b string) string { return a + b + strings.Repeat("0123456789abcdef", 2) }
