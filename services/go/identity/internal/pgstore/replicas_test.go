package pgstore

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Two service instances — two "replicas" — each with its own pool, repository
// and replay store, sharing one PostgreSQL database.

const (
	replicaEnrolURL   = "https://identity.example.test/v1/otp/validate"
	replicaRefreshURL = "https://identity.example.test/v1/token/refresh"
	replicaCode       = "975310"
	replicaMSISDN     = "+14155552671"
)

var replicaSecret = []byte("0123456789abcdef0123456789abcdef")

// knownCode stores every challenge with the hash of a code the test knows,
// since the service never reveals the code it generated.
type knownCode struct{ *Repository }

func (r knownCode) SaveOtp(ctx context.Context, c *app.OtpChallenge) error {
	h := hmac.New(sha256.New, replicaSecret)
	h.Write([]byte(replicaCode))
	cp := *c
	cp.CodeHash = hex.EncodeToString(h.Sum(nil))
	return r.Repository.SaveOtp(ctx, &cp)
}

func replica(t *testing.T, pool *pgxpool.Pool, key ed25519.PrivateKey, now func() time.Time) *app.IdentityService {
	t.Helper()
	svc := app.NewIdentityService(knownCode{NewRepository(pool)}, replicaSecret)
	signer, err := accesstoken.NewSigner(accesstoken.SignerConfig{
		Issuer: "urn:callscreen:identity:test", Audience: "urn:callscreen:edge-api:test", KeyID: "test-key-1", Key: key, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetAccessTokenSigner(signer)
	replay := NewReplayStore(pool)
	for htu, set := range map[string]func(*dpop.Verifier){replicaEnrolURL: svc.SetDeviceProofVerifier, replicaRefreshURL: svc.SetRefreshProofVerifier} {
		v, err := dpop.NewVerifier(dpop.Config{HTU: htu, MaxAge: time.Minute, FutureSkew: 5 * time.Second, Replay: replay, Now: now})
		if err != nil {
			t.Fatal(err)
		}
		set(v)
	}
	return svc
}

func replicaProof(t *testing.T, k *ecdsa.PrivateKey, jti, htu string, iat time.Time) string {
	t.Helper()
	pt, _ := k.PublicKey.Bytes()
	b64 := base64.RawURLEncoding.EncodeToString
	hdr, _ := json.Marshal(map[string]any{
		"typ": "dpop+jwt", "alg": "ES256",
		"jwk": map[string]any{"kty": "EC", "crv": "P-256", "x": b64(pt[1:33]), "y": b64(pt[33:65])},
	})
	cl, _ := json.Marshal(map[string]any{"jti": jti, "htm": "POST", "htu": htu, "iat": iat.Unix()})
	input := b64(hdr) + "." + b64(cl)
	d := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, k, d[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + b64(sig)
}

// Enrol on replica A; a replayed proof is refused by replica B; refresh on B;
// the token A issued is now reuse on A, which revokes on both.
func TestPostgres_TwoReplicasShareSecurityState(t *testing.T) {
	env := requirePostgres(t)
	clock := time.Now().Truncate(time.Second)
	now := func() time.Time { return clock }
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	a, b := replica(t, env.poolA, key, now), replica(t, env.poolB, key, now)
	verifier, _ := accesstoken.NewVerifier(accesstoken.VerifierConfig{
		Issuer: "urn:callscreen:identity:test", Audience: "urn:callscreen:edge-api:test",
		Keys: map[string]ed25519.PublicKey{"test-key-1": pub}, Now: now,
	})
	device, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	jkt, _ := accesstoken.ThumbprintP256(&device.PublicKey)

	req1, err := a.RequestOtp(ctx, replicaMSISDN)
	if err != nil {
		t.Fatal(err)
	}
	enrol := replicaProof(t, device, "e1", replicaEnrolURL, clock)
	sess, token, err := a.ValidateOtpWithDeviceProof(ctx, req1, replicaCode, "POST", enrol)
	if err != nil {
		t.Fatalf("enrol on A: %v", err)
	}
	if c, verifyErr := verifier.Verify(token); verifyErr != nil || c.Confirmation.JKT != jkt {
		t.Fatalf("A's token: (%+v, %v)", c, verifyErr)
	}

	req2, err := b.RequestOtp(ctx, replicaMSISDN)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, validateOtpWithDeviceProofErr := b.ValidateOtpWithDeviceProof(ctx, req2, replicaCode, "POST", enrol); !errors.Is(validateOtpWithDeviceProofErr, dpop.ErrReplayed) {
		t.Fatalf("the proof A accepted, replayed on B: err = %v, want ErrReplayed", validateOtpWithDeviceProofErr)
	}

	access, refresh, err := b.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST",
		replicaProof(t, device, "r1", replicaRefreshURL, clock))
	if err != nil {
		t.Fatalf("refresh on B of A's session: %v", err)
	}
	if c, err := verifier.Verify(access); err != nil || c.Confirmation.JKT != jkt {
		t.Fatalf("B's token: (%+v, %v)", c, err)
	}
	if _, _, err := a.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST",
		replicaProof(t, device, "r2", replicaRefreshURL, clock)); !errors.Is(err, app.ErrTokenReused) {
		t.Fatalf("A presented the token B rotated away: err = %v, want ErrTokenReused", err)
	}
	if _, _, err := b.RefreshTokenWithDeviceProof(ctx, sess.SessionID, refresh, "POST",
		replicaProof(t, device, "r3", replicaRefreshURL, clock)); !errors.Is(err, app.ErrInvalidToken) {
		t.Errorf("after A's reuse revoked the session, B refreshed it: err = %v", err)
	}
}
