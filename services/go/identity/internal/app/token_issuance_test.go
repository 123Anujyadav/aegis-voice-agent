package app

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
	"github.com/callscreen/callscreen-platform/packages/go/dpop"
)

// Signed, device-bound access tokens. Only an enrolled session is issued one,
// its cnf.jkt is the stored binding, and nothing falls back to the unsigned
// legacy format.

// enrolOK enrols a fresh device key on challenge reqID and returns the session,
// the token and the key's thumbprint.
func (r *enrollRig) enrolOK(ctx context.Context, t *testing.T, reqID, jti string) (*UserSession, string, string) {
	t.Helper()
	r.challenge(ctx, t, reqID)
	k := deviceKey(t)
	jkt, err := accesstoken.ThumbprintP256(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	sess, token, err := r.svc.ValidateOtpWithDeviceProof(ctx, reqID, testOtpCode, "POST",
		enrollProof(t, k, k, jti, "POST", r.clock.now()))
	if err != nil {
		t.Fatalf("enrolment: %v", err)
	}
	return sess, token, jkt
}

// payloadOf decodes a token's claim segment without verifying it.
func payloadOf(t *testing.T, token string) []byte {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments, want 3", len(parts))
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	return b
}

// 1-4: an enrolled session receives a signed token that verifies, is bound to
// the enrolled key, and has the configured issuer, audience and lifetime.
func TestToken_EnrolledSessionReceivesBoundSignedToken(t *testing.T) {
	r := newMemEnrollRig(t)
	sess, token, jkt := r.enrolOK(context.Background(), t, "req_a", "j1")
	if token == "" {
		t.Fatal("an enrolled session received no access token")
	}

	claims, err := r.tokens.Verify(token)
	if err != nil {
		t.Fatalf("the issued token does not verify: %v", err)
	}
	stored, err := r.repo.GetSession(context.Background(), sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Confirmation.JKT != stored.DeviceKeyThumbprint || claims.Confirmation.JKT != jkt {
		t.Errorf("cnf.jkt = %q, want the enrolled thumbprint %q (stored %q)",
			claims.Confirmation.JKT, jkt, stored.DeviceKeyThumbprint)
	}
	if claims.Issuer != testTokenIssuer || claims.Audience != testTokenAudience {
		t.Errorf("iss/aud = %q/%q, want %q/%q", claims.Issuer, claims.Audience, testTokenIssuer, testTokenAudience)
	}
	if claims.IssuedAt != r.clock.now().Unix() || claims.NotBefore != claims.IssuedAt {
		t.Errorf("iat/nbf = %d/%d, want both %d", claims.IssuedAt, claims.NotBefore, r.clock.now().Unix())
	}
	if got := time.Duration(claims.Expiry-claims.IssuedAt) * time.Second; got != 15*time.Minute {
		t.Errorf("lifetime = %v, want 15m", got)
	}
	if claims.Subject != stored.SubscriberID {
		t.Errorf("sub = %q, want the stored subscriber id %q", claims.Subject, stored.SubscriberID)
	}
	if !reflect.DeepEqual(claims.Roles, stored.Roles) {
		t.Errorf("roles = %v, want the stored roles %v", claims.Roles, stored.Roles)
	}

	// Past its lifetime (plus the verifier's skew) the same token is refused.
	r.clock.advance(15*time.Minute + accesstoken.DefaultClockSkew + time.Second)
	if _, err := r.tokens.Verify(token); !errors.Is(err, accesstoken.ErrInvalidToken) {
		t.Errorf("an expired token verified: %v", err)
	}
}

// 5: plain OTP validation creates an unbound session and no token, and the
// issuer refuses to sign for that session.
func TestToken_PlainValidationMintsNoToken(t *testing.T) {
	r := newMemEnrollRig(t)
	r.challenge(context.Background(), t, "req_a")
	sess, token, err := r.svc.ValidateOtp(context.Background(), "req_a", testOtpCode)
	if err != nil {
		t.Fatalf("ValidateOtp: %v", err)
	}
	if token != "" {
		t.Errorf("plain ValidateOtp returned an access token: %d bytes", len(token))
	}
	if got, issueBoundAccessTokenErr := r.svc.issueBoundAccessToken(context.Background(), sess.SessionID); got != "" || !errors.Is(issueBoundAccessTokenErr, ErrAccessTokenIssuance) {
		t.Errorf("signing for an unenrolled session: (%d bytes, %v), want ErrAccessTokenIssuance and nothing", len(got), issueBoundAccessTokenErr)
	}
	refreshedAccess, refreshed, err := r.svc.RefreshToken(context.Background(), sess.SessionID, sess.RefreshToken)
	if err != nil || refreshedAccess != "" || refreshed == "" {
		t.Errorf("unenrolled refresh: (access %d bytes, refresh %d bytes, %v), want rotation and no access token",
			len(refreshedAccess), len(refreshed), err)
	}
}

// 6: what is signed comes from the stored session. Changing the session object
// the caller holds changes nothing, and a revoked session gets no token.
func TestToken_IssuanceUsesOnlyTheStoredBinding(t *testing.T) {
	r := newMemEnrollRig(t)
	sess, _, jkt := r.enrolOK(context.Background(), t, "req_a", "j1")
	attacker := deviceKey(t)
	attackerJKT, _ := accesstoken.ThumbprintP256(&attacker.PublicKey)

	sess.DeviceKeyThumbprint = attackerJKT
	sess.SubscriberID = "usr_attacker"
	sess.Roles = append(sess.Roles, "admin")

	token, err := r.svc.issueBoundAccessToken(context.Background(), sess.SessionID)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := r.tokens.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Confirmation.JKT != jkt {
		t.Errorf("cnf.jkt = %q, want the stored binding %q", claims.Confirmation.JKT, jkt)
	}
	if claims.Subject == "usr_attacker" || !reflect.DeepEqual(claims.Roles, []string{"subscriber"}) {
		t.Errorf("caller-held fields reached the token: sub %q roles %v", claims.Subject, claims.Roles)
	}

	if err := r.repo.RevokeSession(context.Background(), sess.SessionID); err != nil {
		t.Fatal(err)
	}
	if got, err := r.svc.issueBoundAccessToken(context.Background(), sess.SessionID); got != "" || !errors.Is(err, ErrAccessTokenIssuance) {
		t.Errorf("signing for a revoked session: (%d bytes, %v), want refusal", len(got), err)
	}
}

// 7: no signer configured fails closed before anything is spent — the same
// proof and code still enrol once a signer is set.
func TestToken_MissingSignerFailsClosed(t *testing.T) {
	r := newMemEnrollRig(t)
	signer := r.svc.accessTokens
	r.svc.SetAccessTokenSigner(nil)
	r.challenge(context.Background(), t, "req_a")
	k := deviceKey(t)
	proof := enrollProof(t, k, k, "j1", "POST", r.clock.now())

	sess, token, err := r.svc.ValidateOtpWithDeviceProof(context.Background(), "req_a", testOtpCode, "POST", proof)
	if !errors.Is(err, ErrTokenIssuanceUnavailable) || sess != nil || token != "" {
		t.Fatalf("(%v, %d bytes, %v), want ErrTokenIssuanceUnavailable and nothing", sess, len(token), err)
	}
	if len(r.repo.sessions) != 0 {
		t.Error("a session was created without a signer")
	}
	if ch, err := r.repo.GetOtp(context.Background(), "req_a"); err != nil || ch.Attempts != 0 {
		t.Errorf("the challenge was touched: %+v, %v", ch, err)
	}

	r.svc.SetAccessTokenSigner(signer)
	if _, token, err := r.svc.ValidateOtpWithDeviceProof(context.Background(), "req_a", testOtpCode, "POST", proof); err != nil || token == "" {
		t.Errorf("the unspent proof and code after configuring the signer: (%d bytes, %v)", len(token), err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }

// 8: a signing failure after the session was created returns no session and no
// token — unsigned or otherwise — and revokes the session it could not serve.
func TestToken_SigningFailureHasNoFallback(t *testing.T) {
	t.Run("session revoked", func(t *testing.T) {
		m := NewMemoryIdentityRepository()
		r := newEnrollRigSigning(t, m, m, dpop.NewMemoryReplayStore(), failingReader{})
		r.challenge(context.Background(), t, "req_a")
		k := deviceKey(t)
		sess, token, err := r.svc.ValidateOtpWithDeviceProof(context.Background(), "req_a", testOtpCode, "POST",
			enrollProof(t, k, k, "j1", "POST", r.clock.now()))
		if !errors.Is(err, ErrAccessTokenIssuance) || sess != nil || token != "" {
			t.Fatalf("(%v, %q, %v), want ErrAccessTokenIssuance, no session, no token", sess, token, err)
		}
		if len(m.sessions) != 1 {
			t.Fatalf("%d sessions, want the one created before signing", len(m.sessions))
		}
		for id, s := range m.sessions {
			if !s.IsRevoked {
				t.Error("the session left without a token is still live")
			}
			if _, _, err := r.svc.RefreshToken(context.Background(), id, s.RefreshToken); !errors.Is(err, ErrInvalidToken) {
				t.Errorf("refreshing the revoked session: err = %v, want ErrInvalidToken", err)
			}
		}
		if _, err := m.GetOtp(context.Background(), "req_a"); err == nil {
			t.Error("the challenge survived; the OTP was expected to be spent")
		}
	})

	t.Run("revocation also fails", func(t *testing.T) {
		errRevoke := errors.New("store: revoke failed")
		repo := &revokeRepo{MemoryIdentityRepository: NewMemoryIdentityRepository(), revokeErr: errRevoke}
		r := newEnrollRigSigning(t, repo, repo.MemoryIdentityRepository, dpop.NewMemoryReplayStore(), failingReader{})
		r.challenge(context.Background(), t, "req_a")
		k := deviceKey(t)
		sess, token, err := r.svc.ValidateOtpWithDeviceProof(context.Background(), "req_a", testOtpCode, "POST",
			enrollProof(t, k, k, "j1", "POST", r.clock.now()))
		if !errors.Is(err, ErrAccessTokenIssuance) || !errors.Is(err, errRevoke) || sess != nil || token != "" {
			t.Fatalf("(%v, %q, %v), want both errors, no session, no token", sess, token, err)
		}
		if repo.revokeCalls != 1 {
			t.Errorf("RevokeSession called %d times, want 1", repo.revokeCalls)
		}
	})
}

// 9-10: an enrolled session is not refreshed on its refresh token alone. The
// refusal rotates nothing and revokes nothing; reuse, expiry and revocation are
// still enforced first.
func TestToken_EnrolledRefreshWithoutProofFailsClosed(t *testing.T) {
	ctx := context.Background()

	t.Run("current token refused, nothing changes", func(t *testing.T) {
		r := newMemEnrollRig(t)
		sess, _, _ := r.enrolOK(ctx, t, "req_a", "j1")
		for i := 0; i < 2; i++ {
			access, refresh, err := r.svc.RefreshToken(ctx, sess.SessionID, sess.RefreshToken)
			if !errors.Is(err, ErrDeviceProofRequired) || access != "" || refresh != "" {
				t.Fatalf("attempt %d: (%q, %q, %v), want ErrDeviceProofRequired and nothing", i, access, refresh, err)
			}
		}
		stored, _ := r.repo.GetSession(ctx, sess.SessionID)
		if stored.IsRevoked || stored.RefreshTokenHash != rth(sess.RefreshToken) {
			t.Error("a refused refresh rotated or revoked the session")
		}
	})

	t.Run("reuse still revokes", func(t *testing.T) {
		r := newMemEnrollRig(t)
		sess, _, _ := r.enrolOK(ctx, t, "req_a", "j1")
		if _, _, err := r.svc.RefreshToken(ctx, sess.SessionID, "rt_stolen"); !errors.Is(err, ErrTokenReused) {
			t.Fatalf("stale token on an enrolled session: err = %v, want ErrTokenReused", err)
		}
		if stored, _ := r.repo.GetSession(ctx, sess.SessionID); !stored.IsRevoked {
			t.Error("reuse did not revoke the enrolled session")
		}
		if _, _, err := r.svc.RefreshToken(ctx, sess.SessionID, sess.RefreshToken); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("after revocation: err = %v, want ErrInvalidToken", err)
		}
	})

	t.Run("expired is refused as before", func(t *testing.T) {
		r := newMemEnrollRig(t)
		sess, _, _ := r.enrolOK(ctx, t, "req_a", "j1")
		r.repo.mu.Lock()
		r.repo.sessions[sess.SessionID].ExpiresAt = time.Now().Add(-time.Minute)
		r.repo.mu.Unlock()
		if _, _, err := r.svc.RefreshToken(ctx, sess.SessionID, sess.RefreshToken); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("expired enrolled session: err = %v, want ErrInvalidToken", err)
		}
	})
}

// 11: the token carries exactly the profile's claims — no MSISDN, tenant, code,
// refresh token or session id.
func TestToken_CarriesNoSensitiveData(t *testing.T) {
	r := newMemEnrollRig(t)
	sess, token, _ := r.enrolOK(context.Background(), t, "req_a", "j1")
	payload := payloadOf(t, token)

	var claims map[string]json.RawMessage
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range claims {
		keys = append(keys, k)
	}
	want := map[string]bool{"iss": true, "aud": true, "sub": true, "iat": true, "nbf": true, "exp": true, "jti": true, "cnf": true, "roles": true}
	for _, k := range keys {
		if !want[k] {
			t.Errorf("unexpected claim %q", k)
		}
	}
	if len(keys) != len(want) {
		t.Errorf("claims = %v, want exactly %d", keys, len(want))
	}

	whole := token + string(payload)
	for what, v := range map[string]string{
		"MSISDN": rlMSISDN, "MSISDN digits": strings.TrimPrefix(rlMSISDN, "+"),
		"tenant": "ten_in_south", "tenant claim": "tenant", "msisdn claim": "msisdn",
		"OTP code": testOtpCode, "refresh token": sess.RefreshToken, "session id": sess.SessionID,
	} {
		if strings.Contains(whole, v) {
			t.Errorf("the token carries the %s", what)
		}
	}
}

// 12: the legacy unsigned format is not accepted by the verifier, and no
// issuance path returns it.
func TestToken_LegacyUnsignedTokensNeverAcceptedOrIssued(t *testing.T) {
	r := newMemEnrollRig(t)
	sess, token, _ := r.enrolOK(context.Background(), t, "req_a", "j1")

	// Exactly the shape the removed generator produced, for the same subject.
	legacyClaims, _ := json.Marshal(map[string]any{
		"sub": sess.SubscriberID, "msisdn": rlMSISDN, "tenantId": "ten_in_south",
		"roles": []string{"subscriber"}, "exp": r.clock.now().Add(time.Hour).Unix(),
	})
	legacy := "Bearer_JWT." + hex.EncodeToString(legacyClaims)
	for name, tok := range map[string]string{
		"legacy":                   legacy,
		"legacy, signed tail":      legacy + "." + strings.Split(token, ".")[2],
		"issued token, prefixed":   "Bearer_JWT." + token,
		"bearer-prefixed issued":   "Bearer " + token,
		"issued token, no signing": strings.Join(strings.Split(token, ".")[:2], ".") + ".",
	} {
		if _, err := r.tokens.Verify(tok); !errors.Is(err, accesstoken.ErrInvalidToken) {
			t.Errorf("%s: verified (err = %v)", name, err)
		}
	}

	if strings.HasPrefix(token, "Bearer_JWT.") || strings.Count(token, ".") != 2 {
		t.Errorf("the enrolment path issued a non-JWS token: %.20q", token)
	}
	r.challenge(context.Background(), t, "req_b")
	if _, tok, _ := r.svc.ValidateOtp(context.Background(), "req_b", testOtpCode); tok != "" {
		t.Errorf("ValidateOtp issued %.20q", tok)
	}
}
