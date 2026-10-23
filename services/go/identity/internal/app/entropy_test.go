package app

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"
)

// generateSecureToken discarded its entropy error (`_, _ = rand.Read(b)`) while
// all 28 other crypto/rand.Read call sites in this repository check it. It now
// checks it too, panicking with the same wording secureOtpCode and
// packages/go/runtime/ids.go use.
//
// # WHY THE FAILURE BRANCH IS NOT TESTED HERE
//
// It cannot be reached. On this toolchain crypto/rand.Read documents:
//
//	"Read fills b with cryptographically secure random bytes. It never returns
//	 an error, and always fills b entirely. Read calls io.ReadFull on Reader and
//	 crashes the program irrecoverably if an error is returned."
//
// The implementation calls fatal() — not panic — so it is not recoverable, and
// the returned error is always nil. Replacing crypto/rand.Reader with a failing
// reader does not produce an error return; it kills the test binary. That was
// verified directly: the attempt printed
//
//	fatal error: crypto/rand: failed to read random data (see
//	https://go.dev/issue/66821): entropy source unavailable (simulated)
//
// So the corrected line is DEFENSIVE, not a closed exploit: the old code could
// not have produced a predictable token on Go >= 1.24 either. Its value is
// consistency with the 28 sibling call sites and with linters that reject a
// discarded error, not a security fix.
//
// What IS tested below is the property that actually holds: the token is
// well-formed, unpredictable, and never the all-zero value.

// theZeroToken is what a zero-filled 32-byte buffer hex-encodes to — the value
// the old code would have returned if crypto/rand.Read could fail quietly.
const theZeroToken = "0000000000000000000000000000000000000000000000000000000000000000"

func TestSecureToken_IsWellFormed(t *testing.T) {
	t.Parallel()

	svc := newSvc()
	for i := 0; i < 50; i++ {
		tok := svc.generateSecureToken()

		if len(tok) != 64 {
			t.Fatalf("token length = %d, want 64 (32 bytes hex encoded)", len(tok))
		}
		if _, err := hex.DecodeString(tok); err != nil {
			t.Fatalf("token %q is not valid hex: %v", tok, err)
		}
		if tok == theZeroToken {
			t.Fatal("token is the all-zero value — the buffer was never filled")
		}
	}
}

func TestSecureToken_TokensAreDistinct(t *testing.T) {
	t.Parallel()

	svc := newSvc()
	const n = 500
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		tok := svc.generateSecureToken()
		if _, dup := seen[tok]; dup {
			t.Fatalf("token %q was produced twice in %d draws from a 256-bit space; "+
				"the generator is not random", tok, i+1)
		}
		seen[tok] = struct{}{}
	}
}

// TestSecureToken_IsNotDerivedFromCallOrder guards against a counter or a
// seeded PRNG replacing the crypto source: successive tokens must differ in
// most positions, not increment.
func TestSecureToken_IsNotDerivedFromCallOrder(t *testing.T) {
	t.Parallel()

	svc := newSvc()
	a, b := svc.generateSecureToken(), svc.generateSecureToken()

	if diff := hamming(a, b); diff < len(a)/4 {
		t.Errorf("consecutive tokens differ in only %d of %d characters, which is "+
			"what a counter looks like, not 256 bits of randomness", diff, len(a))
	}
}

// TestSecureToken_AllFourCallSitesProduceWellFormedCredentials walks every
// production caller: the OTP challenge id, the session id, the initial refresh
// token, and the rotated refresh token.
func TestSecureToken_AllFourCallSitesProduceWellFormedCredentials(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	svc := withFixedOtp(NewIdentityService(repo, []byte("test-secret-key")))

	// 1. requestID
	reqID, err := svc.RequestOtp(context.Background(), "+919876543210")
	if err != nil {
		t.Fatalf("RequestOtp: %v", err)
	}
	assertTokenBody(t, "requestId", reqID, "req_")

	// 2. sessionID and 3. refresh token
	sess, token, err := svc.ValidateOtp(context.Background(), reqID, testOtpCode)
	if err != nil {
		t.Fatalf("ValidateOtp: %v", err)
	}
	assertTokenBody(t, "sessionId", sess.SessionID, "sess_")
	assertTokenBody(t, "refreshToken", sess.RefreshToken, "")
	if token != "" {
		t.Error("an unenrolled session was issued an access token")
	}

	// 4. rotated refresh token
	newAccess, newRefresh, err := svc.RefreshToken(context.Background(), sess.SessionID, sess.RefreshToken)
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	assertTokenBody(t, "rotated refreshToken", newRefresh, "")
	if newRefresh == sess.RefreshToken {
		t.Error("rotation returned the same refresh token")
	}
	if newAccess != "" {
		t.Error("rotating an unenrolled session issued an access token")
	}
}

func assertTokenBody(t *testing.T, name, value, prefix string) {
	t.Helper()
	if prefix != "" && !strings.HasPrefix(value, prefix) {
		t.Errorf("%s = %q, want the %q prefix", name, value, prefix)
	}
	body := strings.TrimPrefix(value, prefix)
	if len(body) != 64 {
		t.Errorf("%s body is %d chars, want 64", name, len(body))
	}
	if _, err := hex.DecodeString(body); err != nil {
		t.Errorf("%s body %q is not valid hex", name, body)
	}
	if body == theZeroToken {
		t.Errorf("%s is the all-zero value", name)
	}
}
