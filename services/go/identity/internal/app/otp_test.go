package app

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// RequestOtp used a hardcoded code:
//
//	code := "482910"
//
// The same six digits for every request and every phone number. Anyone who had
// ever seen one OTP could verify any MSISDN, and the three-attempt limit was
// irrelevant because one guess sufficed.
//
// These tests exercise the REAL generator. They construct the service with
// NewIdentityService rather than the package's withFixedOtp helper, so they see
// exactly what production sees.

var sixDigits = regexp.MustCompile(`^\d{6}$`)

// codeFor recovers the code a real service just minted, by searching the
// six-digit space for the one whose HMAC matches the stored challenge.
//
// This is only possible because the test holds the secret key and the space is
// 10^6. It is the same brute force an attacker would have to run — except an
// attacker is stopped by the three-attempt limit long before finishing, and does
// not hold the key needed to check candidates offline.
func codeFor(t *testing.T, svc *IdentityService, repo *MemoryIdentityRepository, reqID string) string {
	t.Helper()
	ch, err := repo.GetOtp(context.Background(), reqID)
	if err != nil {
		t.Fatalf("GetOtp: %v", err)
	}
	for i := 0; i < 1000000; i++ {
		c := fmt.Sprintf("%06d", i)
		if svc.hashString(c) == ch.CodeHash {
			return c
		}
	}
	t.Fatal("no six-digit code matched the stored hash; the generator is out of range")
	return ""
}

// TestOtp_IsNotAConstant is the direct regression test for the hardcoded code.
func TestOtp_IsNotAConstant(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	// Repeated requests for one number; the per-number limit is not what this tests.
	svc := withoutOtpRateLimit(NewIdentityService(repo, []byte("test-secret-key")))

	seen := map[string]struct{}{}
	for i := 0; i < 12; i++ {
		reqID, err := svc.RequestOtp(context.Background(), "+919876543210")
		if err != nil {
			t.Fatalf("RequestOtp: %v", err)
		}
		seen[codeFor(t, svc, repo, reqID)] = struct{}{}
	}

	if _, found := seen["482910"]; found && len(seen) == 1 {
		t.Fatal("every request minted the old hardcoded code 482910")
	}
	// 12 draws from 10^6 collide with probability ~0.007%, so 12 distinct values
	// is the overwhelmingly likely outcome. The threshold is set below that to
	// stay deterministic in practice while still failing hard for a constant or
	// near-constant generator, which collapses to 1-2 values.
	if len(seen) < 10 {
		t.Errorf("12 OTP requests produced only %d distinct codes; the generator "+
			"is not meaningfully random", len(seen))
	}
}

// TestOtp_HasTheExpectedFormat pins the shape the HTTP layer implies.
func TestOtp_HasTheExpectedFormat(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	// Repeated requests for one number; the per-number limit is not what this tests.
	svc := withoutOtpRateLimit(NewIdentityService(repo, []byte("test-secret-key")))

	// Six recoveries, not twenty-five: each one is a brute-force pass over the
	// whole six-digit space, and the property being checked is the same every
	// time. A leading-zero code (the case %06d formatting exists for) appears in
	// 10% of draws, so six is enough to exercise it without making the package
	// slow under -race.
	for i := 0; i < 6; i++ {
		reqID, _ := svc.RequestOtp(context.Background(), "+919876543210")
		code := codeFor(t, svc, repo, reqID)
		if !sixDigits.MatchString(code) {
			t.Fatalf("code %q is not six digits", code)
		}
	}
}

// TestOtp_IsNotDerivedFromTheMSISDN — the same number must not keep yielding
// the same code, or the OTP is a function of the caller's identity.
func TestOtp_IsNotDerivedFromTheMSISDN(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	// Repeated requests for one number; the per-number limit is not what this tests.
	svc := withoutOtpRateLimit(NewIdentityService(repo, []byte("test-secret-key")))

	const msisdn = "+919876543210"
	first, same := "", 0
	const draws = 6
	for i := 0; i < draws; i++ {
		reqID, _ := svc.RequestOtp(context.Background(), msisdn)
		c := codeFor(t, svc, repo, reqID)
		if i == 0 {
			first = c
			continue
		}
		if c == first {
			same++
		}
	}
	if same == draws-1 {
		t.Errorf("%d requests for %s all produced code %q; the OTP is derived "+
			"from the phone number", draws, msisdn, first)
	}
}

// TestOtp_ValidateAcceptsTheGeneratedCodeAndRejectsOthers covers both sides of
// verification against the real generator.
func TestOtp_ValidateAcceptsTheGeneratedCodeAndRejectsOthers(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	svc := NewIdentityService(repo, []byte("test-secret-key"))

	reqID, _ := svc.RequestOtp(context.Background(), "+919876543210")
	code := codeFor(t, svc, repo, reqID)

	wrong := "000001"
	if wrong == code {
		wrong = "000002"
	}
	if _, _, err := svc.ValidateOtp(context.Background(), reqID, wrong); err == nil {
		t.Fatal("ValidateOtp accepted a code that was never generated")
	}

	// A fresh challenge: the wrong attempt above consumed one of three.
	reqID2, _ := svc.RequestOtp(context.Background(), "+919876543210")
	code2 := codeFor(t, svc, repo, reqID2)
	sess, token, err := svc.ValidateOtp(context.Background(), reqID2, code2)
	if err != nil {
		t.Fatalf("ValidateOtp rejected the generated code: %v", err)
	}
	if sess == nil || token != "" {
		t.Fatal("validation succeeded but returned no session, or an unenrolled access token")
	}
}

// TestOtp_CodeIsBoundToItsRequest is the request-binding invariant: a code
// minted for one challenge must not validate another.
func TestOtp_CodeIsBoundToItsRequest(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	svc := NewIdentityService(repo, []byte("test-secret-key"))

	reqA, _ := svc.RequestOtp(context.Background(), "+919876543210")
	reqB, _ := svc.RequestOtp(context.Background(), "+919812345678")

	codeA := codeFor(t, svc, repo, reqA)
	codeB := codeFor(t, svc, repo, reqB)
	if codeA == codeB {
		t.Skip("both challenges drew the same code by chance; binding is untestable here")
	}

	if _, _, err := svc.ValidateOtp(context.Background(), reqB, codeA); err == nil {
		t.Fatal("a code minted for request A validated request B")
	}
}

// TestOtp_IsSingleUse pins the one-time semantics the implementation already
// provides by deleting the challenge on success.
func TestOtp_IsSingleUse(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	svc := NewIdentityService(repo, []byte("test-secret-key"))

	reqID, _ := svc.RequestOtp(context.Background(), "+919876543210")
	code := codeFor(t, svc, repo, reqID)

	if _, _, err := svc.ValidateOtp(context.Background(), reqID, code); err != nil {
		t.Fatalf("first validation failed: %v", err)
	}
	if _, _, err := svc.ValidateOtp(context.Background(), reqID, code); err == nil {
		t.Fatal("the same OTP validated twice; the challenge was not consumed")
	}
}

// copyingOtpRepo returns a COPY from GetOtp, the way any store that
// deserialises a record does. MemoryIdentityRepository returns its stored
// pointer, which silently made the attempt counter work by aliasing.
type copyingOtpRepo struct {
	*MemoryIdentityRepository
}

func (r *copyingOtpRepo) GetOtp(ctx context.Context, requestID string) (*OtpChallenge, error) {
	ch, err := r.MemoryIdentityRepository.GetOtp(ctx, requestID)
	if err != nil {
		return nil, err
	}
	cp := *ch
	return &cp, nil
}

// TestOtp_AttemptLimitIsEnforcedAndPersisted covers the limit AND the defect
// that made it depend on pointer aliasing.
//
// The increment is now written back with SaveOtp. Without that, a repository
// returning a fresh struct — which a SQL or Redis-backed one does — discards
// every increment, leaving Attempts permanently 0 and a six-digit code open to
// unlimited guessing.
func TestOtp_AttemptLimitIsEnforcedAndPersisted(t *testing.T) {
	t.Parallel()

	repo := &copyingOtpRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()}
	svc := NewIdentityService(repo, []byte("test-secret-key"))

	reqID, _ := svc.RequestOtp(context.Background(), "+919876543210")

	for i := 0; i < 3; i++ {
		if _, _, err := svc.ValidateOtp(context.Background(), reqID, "000000"); err == nil {
			t.Fatalf("attempt %d: a wrong code was accepted", i+1)
		}
	}

	if _, err := repo.GetOtp(context.Background(), reqID); err == nil {
		t.Error("the challenge survived three failed attempts; the attempt limit " +
			"is not persisted, so guesses against a six-digit code are unlimited")
	}
}

// TestOtp_ConcurrentGenerationIsIndependent guards against a shared or seeded
// generator producing the same code across simultaneous requests.
func TestOtp_ConcurrentGenerationIsIndependent(t *testing.T) {
	t.Parallel()

	const workers, each = 8, 12

	repo := NewMemoryIdentityRepository()
	// Repeated requests for one number; the per-number limit is not what this tests.
	svc := withoutOtpRateLimit(NewIdentityService(repo, []byte("test-secret-key")))

	var mu sync.Mutex
	counts := map[string]int{}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				reqID, err := svc.RequestOtp(context.Background(), "+919876543210")
				if err != nil {
					t.Errorf("RequestOtp: %v", err)
					return
				}
				ch, err := repo.GetOtp(context.Background(), reqID)
				if err != nil {
					t.Errorf("GetOtp: %v", err)
					return
				}
				mu.Lock()
				counts[ch.CodeHash]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	worst := 0
	for _, n := range counts {
		if n > worst {
			worst = n
		}
	}
	// A constant or clock-derived generator collapses to a handful of values.
	if worst > 3 {
		t.Errorf("%d concurrent OTPs: one code was minted %d times; the generator "+
			"is not independent per request", workers*each, worst)
	}
}

// TestOtp_IsNotReturnedToTheCaller is the disclosure invariant.
//
// SMS delivery does not exist, and the tempting shortcut is to return the code
// so the flow "works". That would publish the credential to every
// unauthenticated caller of this endpoint.
func TestOtp_IsNotReturnedToTheCaller(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	svc := NewIdentityService(repo, []byte("test-secret-key"))

	reqID, err := svc.RequestOtp(context.Background(), "+919876543210")
	if err != nil {
		t.Fatalf("RequestOtp: %v", err)
	}
	code := codeFor(t, svc, repo, reqID)

	if strings.Contains(reqID, code) {
		t.Fatalf("the request id %q contains the OTP %q", reqID, code)
	}
	if sixDigits.MatchString(reqID) {
		t.Fatalf("RequestOtp returned something shaped like an OTP: %q", reqID)
	}
}
