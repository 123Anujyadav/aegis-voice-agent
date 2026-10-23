package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
	"github.com/callscreen/callscreen-platform/packages/go/dpop"
)

// Device-key enrolment at OTP validation. A session is bound to a device key
// only through a VERIFIED proof of possession made for this request; a public
// key the client merely names enrols nothing.

const enrollHTU = "https://identity.local.test/v1/otp/validate"

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// enrollProof is an ES256 DPoP-style proof for the enrolment endpoint.
func enrollProof(t *testing.T, signer, named *ecdsa.PrivateKey, jti, method string, iat time.Time) string {
	t.Helper()
	return dpopProof(t, signer, named, jti, method, enrollHTU, iat)
}

// dpopProof is an ES256 proof for htu, signed by signer and naming named's
// public key.
func dpopProof(t *testing.T, signer, named *ecdsa.PrivateKey, jti, method, htu string, iat time.Time) string {
	t.Helper()
	pt, err := named.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	hdr, _ := json.Marshal(map[string]any{
		"typ": "dpop+jwt", "alg": "ES256",
		"jwk": map[string]any{"kty": "EC", "crv": "P-256", "x": b64u(pt[1:33]), "y": b64u(pt[33:65])},
	})
	cl, _ := json.Marshal(map[string]any{"jti": jti, "htm": method, "htu": htu, "iat": iat.Unix()})
	input := b64u(hdr) + "." + b64u(cl)
	d := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, signer, d[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + b64u(sig)
}

func deviceKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

type enrollRig struct {
	svc    *IdentityService
	repo   *MemoryIdentityRepository
	clock  *fakeClock
	tokens *accesstoken.Verifier // checks what the service signed
}

func newEnrollRig(t *testing.T, repo IdentityRepository, mem *MemoryIdentityRepository, store dpop.ReplayStore) *enrollRig {
	t.Helper()
	return newEnrollRigSigning(t, repo, mem, store, nil)
}

// newEnrollRigSigning is newEnrollRig with the signer's entropy source chosen
// by the test; nil is crypto/rand.
func newEnrollRigSigning(t *testing.T, repo IdentityRepository, mem *MemoryIdentityRepository, store dpop.ReplayStore, random io.Reader) *enrollRig {
	t.Helper()
	svc := withFixedOtp(NewIdentityService(repo, []byte("test_secret_key")))
	clock := &fakeClock{t: time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)}
	svc.now = clock.now
	v, err := dpop.NewVerifier(dpop.Config{
		HTU: enrollHTU, MaxAge: time.Minute, FutureSkew: 5 * time.Second, Replay: store, Now: clock.now,
	})
	if err != nil {
		t.Fatalf("dpop.NewVerifier: %v", err)
	}
	svc.SetDeviceProofVerifier(v)
	signer, verifier := testTokenKeys(t, clock, random)
	svc.SetAccessTokenSigner(signer)
	return &enrollRig{svc: svc, repo: mem, clock: clock, tokens: verifier}
}

// Test-only token configuration. These values exist nowhere outside tests; the
// key is generated per test and held only in memory.
const (
	testTokenIssuer   = "urn:callscreen:identity:local"
	testTokenAudience = "urn:callscreen:edge-api:local"
	testTokenKeyID    = "test-key-1"
)

func testTokenKeys(t *testing.T, clock *fakeClock, random io.Reader) (*accesstoken.Signer, *accesstoken.Verifier) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := accesstoken.NewSigner(accesstoken.SignerConfig{
		Issuer: testTokenIssuer, Audience: testTokenAudience, KeyID: testTokenKeyID,
		Key: priv, Now: clock.now, Random: random,
	})
	if err != nil {
		t.Fatalf("accesstoken.NewSigner: %v", err)
	}
	verifier, err := accesstoken.NewVerifier(accesstoken.VerifierConfig{
		Issuer: testTokenIssuer, Audience: testTokenAudience,
		Keys: map[string]ed25519.PublicKey{testTokenKeyID: pub}, Now: clock.now,
	})
	if err != nil {
		t.Fatalf("accesstoken.NewVerifier: %v", err)
	}
	return signer, verifier
}

func newMemEnrollRig(t *testing.T) *enrollRig {
	t.Helper()
	m := NewMemoryIdentityRepository()
	return newEnrollRig(t, m, m, dpop.NewMemoryReplayStore())
}

func (r *enrollRig) challenge(ctx context.Context, t *testing.T, id string) {
	t.Helper()
	seedChallengeFor(ctx, t, r.repo, r.svc, id, time.Now().Add(time.Hour))
}

// 13: a valid proof binds exactly the proven key's thumbprint, in the stored
// session; the unproven path binds nothing.
func TestEnroll_ValidProofBindsTheThumbprint(t *testing.T) {
	r := newMemEnrollRig(t)
	r.challenge(context.Background(), t, "req_a")
	k := deviceKey(t)
	want, _ := accesstoken.ThumbprintP256(&k.PublicKey)

	sess, _, err := r.svc.ValidateOtpWithDeviceProof(context.Background(), "req_a", testOtpCode, "POST",
		enrollProof(t, k, k, "j1", "POST", r.clock.now()))
	if err != nil {
		t.Fatalf("enrolment: %v", err)
	}
	if sess.DeviceKeyThumbprint != want {
		t.Errorf("session thumbprint = %q, want %q", sess.DeviceKeyThumbprint, want)
	}
	stored, err := r.repo.GetSession(context.Background(), sess.SessionID)
	if err != nil || stored.DeviceKeyThumbprint != want {
		t.Errorf("stored session thumbprint = %q (%v), want %q", stored.DeviceKeyThumbprint, err, want)
	}

	r.challenge(context.Background(), t, "req_b")
	plain, _, err := r.svc.ValidateOtp(context.Background(), "req_b", testOtpCode)
	if err != nil || plain.DeviceKeyThumbprint != "" {
		t.Errorf("plain ValidateOtp produced thumbprint %q (%v), want none", plain.DeviceKeyThumbprint, err)
	}
}

// 12: an invalid proof enrols nothing and touches no OTP state: no attempt, no
// counted failure, no consumed challenge.
func TestEnroll_InvalidProofCannotEnroll(t *testing.T) {
	k, other := deviceKey(t), deviceKey(t)
	for name, makeBad := range map[string]func(r *enrollRig) string{
		"wrong method":        func(r *enrollRig) string { return enrollProof(t, k, k, "j", "GET", r.clock.now()) },
		"signed by other key": func(r *enrollRig) string { return enrollProof(t, other, k, "j", "POST", r.clock.now()) },
		"stale":               func(r *enrollRig) string { return enrollProof(t, k, k, "j", "POST", r.clock.now().Add(-time.Hour)) },
		"garbage":             func(*enrollRig) string { return "not.a.proof" },
		"public key only":     func(*enrollRig) string { return `{"kty":"EC","crv":"P-256"}` },
	} {
		t.Run(name, func(t *testing.T) {
			r := newMemEnrollRig(t)
			r.challenge(context.Background(), t, "req_a")
			sess, token, err := r.svc.ValidateOtpWithDeviceProof(context.Background(), "req_a", testOtpCode, "POST", makeBad(r))
			if !errors.Is(err, ErrInvalidDeviceProof) || sess != nil || token != "" {
				t.Fatalf("(%v, %v), want ErrInvalidDeviceProof and no session", sess, err)
			}
			if len(r.repo.sessions) != 0 {
				t.Error("a session was created")
			}
			if ch, err := r.repo.GetOtp(context.Background(), "req_a"); err != nil || ch.Attempts != 0 {
				t.Errorf("the challenge was touched: %+v, %v", ch, err)
			}
			if st, ok := r.repo.otpRates[mp(rlMSISDN)]; ok && len(st.failures) != 0 {
				t.Error("a rate-limit failure was counted")
			}
		})
	}
}

// A replayed proof cannot enrol a second session, and the replay store's own
// failure fails closed without touching OTP state.
func TestEnroll_ReplayAndReplayStoreFailure(t *testing.T) {
	r := newMemEnrollRig(t)
	r.challenge(context.Background(), t, "req_a")
	r.challenge(context.Background(), t, "req_b")
	k := deviceKey(t)
	proof := enrollProof(t, k, k, "once", "POST", r.clock.now())
	if _, _, err := r.svc.ValidateOtpWithDeviceProof(context.Background(), "req_a", testOtpCode, "POST", proof); err != nil {
		t.Fatalf("first: %v", err)
	}
	_, _, err := r.svc.ValidateOtpWithDeviceProof(context.Background(), "req_b", testOtpCode, "POST", proof)
	if !errors.Is(err, ErrInvalidDeviceProof) || !errors.Is(err, dpop.ErrReplayed) {
		t.Errorf("replayed proof: err = %v, want ErrInvalidDeviceProof wrapping ErrReplayed", err)
	}
	if len(r.repo.sessions) != 1 {
		t.Errorf("%d sessions, want 1", len(r.repo.sessions))
	}

	m := NewMemoryIdentityRepository()
	f := newEnrollRig(t, m, m, unreachableReplay{})
	f.challenge(context.Background(), t, "req_a")
	sess, _, err := f.svc.ValidateOtpWithDeviceProof(context.Background(), "req_a", testOtpCode, "POST",
		enrollProof(t, k, k, "j", "POST", f.clock.now()))
	if sess != nil || !errors.Is(err, dpop.ErrReplayUnavailable) || errors.Is(err, ErrInvalidDeviceProof) {
		t.Errorf("failing replay store: (%v, %v), want ErrReplayUnavailable", sess, err)
	}
	if ch, err := m.GetOtp(context.Background(), "req_a"); err != nil || ch.Attempts != 0 || len(m.sessions) != 0 {
		t.Error("a failing replay store let OTP state change or a session appear")
	}
}

type unreachableReplay struct{}

func (unreachableReplay) Reserve(context.Context, string, string, time.Time, time.Time) error {
	return errors.New("replay store unreachable")
}

// A service with no verifier refuses enrolment rather than creating an
// unenrolled session the caller would believe bound.
func TestEnroll_UnconfiguredFailsClosed(t *testing.T) {
	m := NewMemoryIdentityRepository()
	svc := withFixedOtp(NewIdentityService(m, []byte("k")))
	seedChallengeFor(context.Background(), t, m, svc, "req_a", time.Now().Add(time.Hour))
	k := deviceKey(t)
	sess, token, err := svc.ValidateOtpWithDeviceProof(context.Background(), "req_a", testOtpCode, "POST",
		enrollProof(t, k, k, "j", "POST", time.Now()))
	if !errors.Is(err, ErrEnrollmentUnavailable) || sess != nil || token != "" || len(m.sessions) != 0 {
		t.Errorf("(%v, %v), want ErrEnrollmentUnavailable and no session", sess, err)
	}
	if ch, _ := m.GetOtp(context.Background(), "req_a"); ch == nil || ch.Attempts != 0 {
		t.Error("the challenge was touched")
	}
}

// A valid proof with a wrong code: the OTP rules apply exactly as before, no
// session exists, and the proof is spent — a retry needs a fresh proof.
func TestEnroll_WrongCodeWithValidProof(t *testing.T) {
	r := newMemEnrollRig(t)
	r.challenge(context.Background(), t, "req_a")
	k := deviceKey(t)
	proof := enrollProof(t, k, k, "j1", "POST", r.clock.now())
	if _, _, err := r.svc.ValidateOtpWithDeviceProof(context.Background(), "req_a", "000000", "POST", proof); !errors.Is(err, ErrInvalidOtp) {
		t.Fatalf("err = %v, want ErrInvalidOtp", err)
	}
	if len(r.repo.sessions) != 0 {
		t.Fatal("a session was created")
	}
	if _, _, err := r.svc.ValidateOtpWithDeviceProof(context.Background(), "req_a", testOtpCode, "POST", proof); !errors.Is(err, dpop.ErrReplayed) {
		t.Errorf("reusing the proof: err = %v, want ErrReplayed", err)
	}
	if _, _, err := r.svc.ValidateOtpWithDeviceProof(context.Background(), "req_a", testOtpCode, "POST",
		enrollProof(t, k, k, "j2", "POST", r.clock.now())); err != nil {
		t.Errorf("a fresh proof with the right code: %v", err)
	}
}

// 14: when the session write fails, no session — enrolled or partial — exists.
func TestEnroll_FailedPersistenceLeavesNoSession(t *testing.T) {
	inj := &injectableRepo{MemoryIdentityRepository: NewMemoryIdentityRepository(), failSession: true}
	r := newEnrollRig(t, inj, inj.MemoryIdentityRepository, dpop.NewMemoryReplayStore())
	r.challenge(context.Background(), t, "req_a")
	k := deviceKey(t)
	sess, token, err := r.svc.ValidateOtpWithDeviceProof(context.Background(), "req_a", testOtpCode, "POST",
		enrollProof(t, k, k, "j", "POST", r.clock.now()))
	if !errors.Is(err, errInternalStore) || sess != nil || token != "" {
		t.Fatalf("(%v, %v), want the store's error and no session", sess, err)
	}
	if len(r.repo.sessions) != 0 {
		t.Error("a session exists after its write failed")
	}
}

// 15: an enrolled key cannot be replaced — not by a second SaveSession, not by
// a rotation — and a refresh without a device proof neither rotates nor loses it.
func TestEnroll_EnrolledKeyCannotBeReplaced(t *testing.T) {
	r := newMemEnrollRig(t)
	r.challenge(context.Background(), t, "req_a")
	k, attacker := deviceKey(t), deviceKey(t)
	sess, _, err := r.svc.ValidateOtpWithDeviceProof(context.Background(), "req_a", testOtpCode, "POST",
		enrollProof(t, k, k, "j", "POST", r.clock.now()))
	if err != nil {
		t.Fatal(err)
	}
	original := sess.DeviceKeyThumbprint
	attackerJKT, _ := accesstoken.ThumbprintP256(&attacker.PublicKey)
	ctx := context.Background()

	stored, _ := r.repo.GetSession(ctx, sess.SessionID)
	over := *stored
	over.DeviceKeyThumbprint = attackerJKT
	if err := r.repo.SaveSession(ctx, &over); !errors.Is(err, ErrDeviceKeyAlreadyEnrolled) {
		t.Errorf("SaveSession replacing the key: err = %v, want ErrDeviceKeyAlreadyEnrolled", err)
	}
	cleared := *stored
	cleared.DeviceKeyThumbprint = ""
	if err := r.repo.SaveSession(ctx, &cleared); !errors.Is(err, ErrDeviceKeyAlreadyEnrolled) {
		t.Errorf("SaveSession clearing the key: err = %v, want ErrDeviceKeyAlreadyEnrolled", err)
	}
	rotated := *stored
	rotated.RefreshTokenHash = rth("rt_next")
	rotated.DeviceKeyThumbprint = attackerJKT
	if err := r.repo.ReplaceSession(ctx, &rotated, rth(sess.RefreshToken)); !errors.Is(err, ErrDeviceKeyAlreadyEnrolled) {
		t.Errorf("ReplaceSession replacing the key: err = %v, want ErrDeviceKeyAlreadyEnrolled", err)
	}
	if stored, _ := r.repo.GetSession(ctx, sess.SessionID); stored.DeviceKeyThumbprint != original || stored.RefreshTokenHash != rth(sess.RefreshToken) {
		t.Error("a refused write changed the stored session")
	}

	if _, _, err := r.svc.RefreshToken(ctx, sess.SessionID, sess.RefreshToken); !errors.Is(err, ErrDeviceProofRequired) {
		t.Fatalf("proof-less refresh of an enrolled session: err = %v, want ErrDeviceProofRequired", err)
	}
	if stored, _ := r.repo.GetSession(ctx, sess.SessionID); stored.DeviceKeyThumbprint != original {
		t.Error("a refused refresh lost the enrolled key")
	}
}

// 16: errors carry no proof, code or MSISDN.
func TestEnroll_ErrorsCarryNoSensitiveData(t *testing.T) {
	r := newMemEnrollRig(t)
	r.challenge(context.Background(), t, "req_a")
	k := deviceKey(t)
	proof := enrollProof(t, k, k, "secret-jti", "GET", r.clock.now())
	_, _, err := r.svc.ValidateOtpWithDeviceProof(context.Background(), "req_a", testOtpCode, "POST", proof)
	if err == nil {
		t.Fatal("expected a rejection")
	}
	for what, v := range map[string]string{
		"proof": proof[:40], "jti": "secret-jti", "code": testOtpCode,
		"MSISDN": rlMSISDN, "MSISDN digits": strings.TrimPrefix(rlMSISDN, "+"),
	} {
		if strings.Contains(err.Error(), v) {
			t.Errorf("the error carries the %s", what)
		}
	}
}
