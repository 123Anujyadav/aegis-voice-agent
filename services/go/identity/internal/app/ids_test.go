package app

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// Identifier generation in this service was derived from time.Now().UnixNano()
// and, for SubscriberID, from the MSISDN itself. Both were measured before being
// changed:
//
//   - 1600 concurrent UnixNano ids produced 2 distinct values, one reused 1599
//     times. UnixNano reports the platform clock's resolution, not nanoseconds.
//   - "usr_" + hex(MSISDN)[:12] is the first six bytes of the phone number.
//     Three distinct test MSISDNs produced one id, and hex-decoding it returns
//     "+91987".
//
// SubscriberID is the `sub` claim (generateAccessToken), so the second defect
// meant different people sharing a subject identity while leaking a personal
// identifier into every token and log line that carried it.

// testOtpCode is the code a test-configured service always mints.
//
// It is NOT "000000": identity_test.go uses that as its example of a WRONG
// code, and a fixed code equal to it would make that assertion pass for the
// wrong reason.
const testOtpCode = "135790"

// withFixedOtp makes a service mint a known code so a test can complete the
// OTP flow.
//
// This seam exists only inside the package. The newCode field is unexported and has no
// setter, and RequestOtp neither returns the code nor puts it in the HTTP
// response, so no production caller can learn or fix the generated value.
func withFixedOtp(s *IdentityService) *IdentityService {
	s.newCode = func() string { return testOtpCode }
	return s
}

func newSvc() *IdentityService {
	// Identifier tests request many OTPs for one number; the per-number limit
	// is not what they test.
	return withoutOtpRateLimit(withFixedOtp(NewIdentityService(NewMemoryIdentityRepository(), []byte("test-secret-key"))))
}

// TestRequestID_IsUniqueUnderConcurrency covers the OTP challenge key. A
// collision means one caller's challenge overwrites another's, or validates
// against it.
func TestRequestID_IsUniqueUnderConcurrency(t *testing.T) {
	t.Parallel()

	const workers, each = 16, 50
	svc := newSvc()

	var mu sync.Mutex
	seen := make(map[string]struct{}, workers*each)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				id, err := svc.RequestOtp(context.Background(), "+919876543210")
				if err != nil {
					t.Errorf("RequestOtp: %v", err)
					return
				}
				mu.Lock()
				seen[id] = struct{}{}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(seen) != workers*each {
		t.Errorf("%d concurrent OTP requests produced %d distinct request ids; "+
			"challenge keys collide, so one caller's challenge overwrites another's",
			workers*each, len(seen))
	}
}

// TestRequestID_IsNotDerivedFromTheClock asserts unpredictability, not just
// uniqueness: a monotonic-looking id can be unique and still guessable.
func TestRequestID_IsNotDerivedFromTheClock(t *testing.T) {
	t.Parallel()

	svc := newSvc()
	a, _ := svc.RequestOtp(context.Background(), "+919876543210")
	b, _ := svc.RequestOtp(context.Background(), "+919876543210")

	if !strings.HasPrefix(a, "req_") || !strings.HasPrefix(b, "req_") {
		t.Fatalf("request id lost its req_ prefix: %q %q", a, b)
	}

	bodyA, bodyB := strings.TrimPrefix(a, "req_"), strings.TrimPrefix(b, "req_")
	if len(bodyA) < 32 {
		t.Errorf("request id body %q is %d chars — too short to carry meaningful "+
			"entropy", bodyA, len(bodyA))
	}
	// A clock-derived id increments. Two random ids differ in most positions.
	if diff := hamming(bodyA, bodyB); diff < len(bodyA)/4 {
		t.Errorf("consecutive request ids differ in only %d of %d characters, "+
			"which is what a counter or clock looks like, not randomness",
			diff, len(bodyA))
	}
}

// TestSessionID_IsUniqueUnderConcurrency covers the identifier of an
// authenticated session.
func TestSessionID_IsUniqueUnderConcurrency(t *testing.T) {
	t.Parallel()

	const workers, each = 8, 25

	var mu sync.Mutex
	seen := make(map[string]struct{}, workers*each)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc := newSvc()
			for i := 0; i < each; i++ {
				reqID, err := svc.RequestOtp(context.Background(), "+919876543210")
				if err != nil {
					t.Errorf("RequestOtp: %v", err)
					return
				}
				sess, _, err := svc.ValidateOtp(context.Background(), reqID, testOtpCode)
				if err != nil {
					t.Errorf("ValidateOtp: %v", err)
					return
				}
				mu.Lock()
				seen[sess.SessionID] = struct{}{}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(seen) != workers*each {
		t.Errorf("%d concurrent logins produced %d distinct session ids; two "+
			"authenticated sessions sharing an id is a session-fixation defect",
			workers*each, len(seen))
	}
}

// TestSubscriberID_DoesNotCollideAcrossMSISDNs uses real-shaped Indian numbers
// sharing a prefix — exactly what defeated the old derivation.
func TestSubscriberID_DoesNotCollideAcrossMSISDNs(t *testing.T) {
	t.Parallel()

	msisdns := []string{
		"+919876543210", "+919876543211", "+919876500000",
		"+919812345678", "+918876543210",
	}

	// A larger sweep of realistic consecutive numbers as well. Five MSISDNs is
	// enough to catch a prefix-derived id, but NOT enough to catch a hash that
	// is merely truncated too short: a mutation shortening the id to one hex
	// character escaped this test until the sweep and the entropy floor below
	// were added.
	//
	// The generated range is deliberately disjoint from the literals above:
	// "+9198765%05d" with i=0 is "+919876500000", which is already in the list,
	// and the same MSISDN legitimately yields the same subscriber id. Feeding a
	// duplicate in would make this test report a stability guarantee as a
	// collision.
	for i := 1; i < 301; i++ {
		msisdns = append(msisdns, fmt.Sprintf("+9198761%05d", i))
	}

	svc := newSvc()
	ids := map[string]string{}
	for _, m := range msisdns {
		reqID, err := svc.RequestOtp(context.Background(), m)
		if err != nil {
			t.Fatalf("RequestOtp(%s): %v", m, err)
		}
		sess, _, err := svc.ValidateOtp(context.Background(), reqID, testOtpCode)
		if err != nil {
			t.Fatalf("ValidateOtp(%s): %v", m, err)
		}
		if prev, dup := ids[sess.SubscriberID]; dup {
			t.Fatalf("subscriber id %q is shared by %s and %s; distinct people "+
				"would carry the same `sub` claim", sess.SubscriberID, prev, m)
		}
		ids[sess.SubscriberID] = m
	}
}

// TestSubscriberID_CarriesEnoughEntropy is the direct guard on truncation.
//
// Uniqueness across a sample proves the derivation is not prefix-based, but it
// does not bound collision probability at real subscriber volumes. A birthday
// collision needs only ~2^(n/2) subscribers for an n-bit id, so a short
// truncation is a defect long before any test sample shows a duplicate.
func TestSubscriberID_CarriesEnoughEntropy(t *testing.T) {
	t.Parallel()

	svc := newSvc()
	reqID, _ := svc.RequestOtp(context.Background(), "+919876543210")
	sess, _, err := svc.ValidateOtp(context.Background(), reqID, testOtpCode)
	if err != nil {
		t.Fatalf("ValidateOtp: %v", err)
	}

	body := strings.TrimPrefix(sess.SubscriberID, "usr_")

	// 32 hex characters is 128 bits, the width the implementation takes from
	// the HMAC. Anything materially shorter is a truncation regression.
	const minHexChars = 32
	if len(body) < minHexChars {
		t.Errorf("subscriber id body %q is %d hex chars (%d bits); below %d chars "+
			"the birthday bound puts a collision within reach of a real subscriber "+
			"base", body, len(body), len(body)*4, minHexChars)
	}
}

// TestSubscriberID_DependsOnTheSecretKey is what makes the pseudonym a
// pseudonym rather than a lookup.
//
// SECURITY.md specifies a KEYED HMAC. The key is the whole control: an unkeyed
// digest of an MSISDN is reversible by brute force, because the input space is
// tiny — Indian mobile numbers are on the order of 10^10 values, which is a few
// minutes of hashing. Anyone holding a subscriber id could recover the phone
// number it stands for.
//
// A mutation replacing s.secretKey with nil escaped every other test here: the
// ids were still unique, still stable, still long, still not hex-decodable to
// the number. Only comparing two differently-keyed services detects it.
func TestSubscriberID_DependsOnTheSecretKey(t *testing.T) {
	t.Parallel()

	const msisdn = "+919876543210"

	idFor := func(key string) string {
		svc := withFixedOtp(NewIdentityService(NewMemoryIdentityRepository(), []byte(key)))
		reqID, _ := svc.RequestOtp(context.Background(), msisdn)
		sess, _, err := svc.ValidateOtp(context.Background(), reqID, testOtpCode)
		if err != nil {
			t.Fatalf("ValidateOtp with key %q: %v", key, err)
		}
		return sess.SubscriberID
	}

	a, b := idFor("secret-key-one"), idFor("secret-key-two")
	if a == b {
		t.Fatalf("two services with different secret keys produced the same "+
			"subscriber id (%q) for the same MSISDN. The derivation ignores the "+
			"key, so it is an unkeyed digest of the phone number and is "+
			"reversible by brute force over a ~10^10 input space.", a)
	}
}

// TestSubscriberID_IsStableForTheSameMSISDN is the property that forbids simply
// using randomness here: the same person signing in twice must be the same
// subscriber.
func TestSubscriberID_IsStableForTheSameMSISDN(t *testing.T) {
	t.Parallel()

	svc := newSvc()
	const msisdn = "+919876543210"

	var first string
	for i := 0; i < 3; i++ {
		reqID, _ := svc.RequestOtp(context.Background(), msisdn)
		sess, _, err := svc.ValidateOtp(context.Background(), reqID, testOtpCode)
		if err != nil {
			t.Fatalf("login %d: %v", i, err)
		}
		if i == 0 {
			first = sess.SubscriberID
			continue
		}
		if sess.SubscriberID != first {
			t.Fatalf("login %d produced subscriber id %q, first login produced %q; "+
				"the same person must not become a new subscriber on each sign-in",
				i, sess.SubscriberID, first)
		}
	}
}

// TestSubscriberID_DoesNotDiscloseTheMSISDN is the privacy invariant. SECURITY.md
// requires personal identifiers to be "replaced with a keyed HMAC so records
// remain correlatable without exposing the value".
func TestSubscriberID_DoesNotDiscloseTheMSISDN(t *testing.T) {
	t.Parallel()

	svc := newSvc()
	const msisdn = "+919876543210"

	reqID, _ := svc.RequestOtp(context.Background(), msisdn)
	sess, _, err := svc.ValidateOtp(context.Background(), reqID, testOtpCode)
	if err != nil {
		t.Fatalf("ValidateOtp: %v", err)
	}

	id := sess.SubscriberID
	body := strings.TrimPrefix(id, "usr_")

	if strings.Contains(id, msisdn) {
		t.Fatalf("subscriber id %q contains the MSISDN verbatim", id)
	}
	// The old scheme was hex of the raw MSISDN, so decoding recovered it.
	if raw, err := hex.DecodeString(body); err == nil {
		if s := string(raw); strings.Contains(msisdn, s) && len(s) >= 4 {
			t.Fatalf("subscriber id %q hex-decodes to %q, a prefix of the MSISDN; "+
				"the identifier is reversible", id, s)
		}
	}
	// A different number must not share a prefix with this one's id.
	reqID2, _ := svc.RequestOtp(context.Background(), "+919876543211")
	sess2, _, _ := svc.ValidateOtp(context.Background(), reqID2, testOtpCode)
	if commonPrefix(body, strings.TrimPrefix(sess2.SubscriberID, "usr_")) > 8 {
		t.Errorf("subscriber ids for two similar MSISDNs share a long prefix "+
			"(%q vs %q), which leaks number similarity", id, sess2.SubscriberID)
	}
}

func hamming(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	d := 0
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			d++
		}
	}
	return d
}

func commonPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}
