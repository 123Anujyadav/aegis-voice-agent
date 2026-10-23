package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/callscreen/callscreen-platform/packages/go/middleware"
	"github.com/callscreen/callscreen-platform/packages/go/pgshared"
)

// ADR-0029 "Keys and rotation": edge-api verifies with a key set (the active
// key and, during a rotation, the previous one) so identity can switch signing
// keys without rejecting tokens it already issued. These tests run the exact
// procedure the ADR prescribes, through the real middleware.

const kidOld, kidNew = "signing-2026-a", "signing-2026-b"

// rotationRig is an authenticator under cfg with one device and one clock, so
// tokens from any signer can be presented with a valid proof.
type rotationRig struct {
	h      http.Handler
	device *ecdsa.PrivateKey
	clock  *clock
}

func newRotationRig(t *testing.T, cfg Config, c *clock, device *ecdsa.PrivateKey) *rotationRig {
	t.Helper()
	a, err := New(cfg, dpop.NewMemoryReplayStore(), c.now, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return &rotationRig{h: middleware.CorrelationIDMiddleware(a.Middleware(ok)), device: device, clock: c}
}

// present sends token with a fresh valid proof and returns the status.
func (r *rotationRig) present(t *testing.T, token string) int {
	t.Helper()
	claims := map[string]any{
		"jti": "rot-" + strconv.FormatInt(jtiN.Add(1), 10), "htm": http.MethodGet, "htu": origin + path,
		"iat": r.clock.now().Unix(), "ath": dpop.AccessTokenHash(token),
	}
	rr := (&rig{h: r.h, clock: r.clock}).do(t, http.MethodGet, origin+path, auth(token, proofFor(t, r.device, r.device, claims)))
	return rr.code
}

func issue(t *testing.T, s *accesstoken.Signer, device *ecdsa.PrivateKey) string {
	t.Helper()
	jkt, err := accesstoken.ThumbprintP256(&device.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := s.Issue("usr_rotation", []string{"subscriber"}, jkt)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestRotation_OverlapProcedureNeverRejectsAValidToken(t *testing.T) {
	oldPub, oldPriv, _ := ed25519.GenerateKey(rand.Reader)
	newPub, newPriv, _ := ed25519.GenerateKey(rand.Reader)
	device, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	c := &clock{t: now0}
	oldSigner := signerFor(t, oldPriv, kidOld, issuer, audience, c)
	newSigner := signerFor(t, newPriv, kidNew, issuer, audience, c)

	base := validConfig(t, oldPub)
	base.KeyID = kidOld

	// Before: only the old key is trusted, and identity signs with it.
	before := newRotationRig(t, base, c, device)
	oldTok := issue(t, oldSigner, device)
	if got := before.present(t, oldTok); got != http.StatusOK {
		t.Fatalf("before rotation, an old-key token = %d", got)
	}
	if got := before.present(t, issue(t, newSigner, device)); got != http.StatusUnauthorized {
		t.Fatalf("before rotation, a new-key token was accepted (%d): the new key is not trusted yet", got)
	}

	// Step 1: edge-api trusts the new key as active and the old as previous.
	overlap := base
	overlap.PublicKeyFile, overlap.KeyID = publicKeyFile(t, newPub), kidNew
	overlap.PreviousPublicKeyFile, overlap.PreviousKeyID = publicKeyFile(t, oldPub), kidOld
	during := newRotationRig(t, overlap, c, device)
	if got := during.present(t, oldTok); got != http.StatusOK {
		t.Fatalf("during the overlap, a token identity issued before switching = %d", got)
	}

	// Step 2: identity switches to the new key. Both kinds of token verify.
	newTok := issue(t, newSigner, device)
	if got := during.present(t, newTok); got != http.StatusOK {
		t.Fatalf("during the overlap, a new-key token = %d", got)
	}
	c.add(accesstoken.Lifetime - time.Minute)
	if got := during.present(t, oldTok); got != http.StatusOK {
		t.Fatalf("an old-key token still within its lifetime = %d", got)
	}

	// Step 3: wait the token lifetime plus skew; the last old-key token has
	// expired by itself.
	c.add(time.Minute + accesstoken.DefaultClockSkew + time.Second)
	if got := during.present(t, oldTok); got != http.StatusUnauthorized {
		t.Fatalf("an expired old-key token = %d", got)
	}

	// Step 4: the previous key is removed. New-key tokens keep working; an
	// old-key token, even a fresh one, is refused.
	after := overlap
	after.PreviousPublicKeyFile, after.PreviousKeyID = "", ""
	done := newRotationRig(t, after, c, device)
	if got := done.present(t, issue(t, newSigner, device)); got != http.StatusOK {
		t.Fatalf("after rotation, a new-key token = %d", got)
	}
	if got := done.present(t, issue(t, oldSigner, device)); got != http.StatusUnauthorized {
		t.Fatalf("after rotation, an old-key token was accepted (%d)", got)
	}
}

func TestRotation_PreviousKeyConfigurationIsValidated(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	prevPub, _, _ := ed25519.GenerateKey(rand.Reader)
	ok := validConfig(t, pub)
	prevFile := publicKeyFile(t, prevPub)
	cases := map[string]struct {
		mutate  func(*Config)
		mention string
	}{
		"file without kid": {func(c *Config) { c.PreviousPublicKeyFile = prevFile }, "set together"},
		"kid without file": {func(c *Config) { c.PreviousKeyID = "prev" }, "set together"},
		"same kid":         {func(c *Config) { c.PreviousPublicKeyFile, c.PreviousKeyID = prevFile, c.KeyID }, "must differ"},
		"same file":        {func(c *Config) { c.PreviousPublicKeyFile, c.PreviousKeyID = c.PublicKeyFile, "prev" }, "own file"},
		"padded kid":       {func(c *Config) { c.PreviousPublicKeyFile, c.PreviousKeyID = prevFile, " prev" }, "whitespace"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := ok
			tc.mutate(&c)
			wantConfigErr(t, c.Validate(), tc.mention)
		})
	}
	// The previous key file holding the ACTIVE key is a misconfiguration.
	c := ok
	c.PreviousPublicKeyFile, c.PreviousKeyID = publicKeyFile(t, pub), "prev"
	if _, err := New(c, dpop.NewMemoryReplayStore(), nil, nil); !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "holds the active key") {
		t.Errorf("a previous key equal to the active key: %v", err)
	}
	// A valid overlap builds.
	c = ok
	c.PreviousPublicKeyFile, c.PreviousKeyID = prevFile, "prev"
	if _, err := New(c, dpop.NewMemoryReplayStore(), nil, nil); err != nil {
		t.Errorf("a valid overlap was refused: %v", err)
	}
}

// ADR-0029 "Database TLS": LoadActivation derives the policy from an EXPLICIT
// ENVIRONMENT, and Open refuses a plaintext DSN under the verified policy
// before connecting, naming nothing from the DSN.
func TestDatabaseTLS_PolicyAndRefusal(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	c := validConfig(t, pub)
	setAll := func(t *testing.T) {
		t.Helper()
		clearAuthEnv(t)
		for name, v := range map[string]string{
			"CS_EDGE_API_ACCESS_TOKEN_PUBLIC_KEY_FILE": c.PublicKeyFile, "CS_EDGE_API_ACCESS_TOKEN_KID": c.KeyID,
			"CS_EDGE_API_ACCESS_TOKEN_ISSUER": c.Issuer, "CS_EDGE_API_ACCESS_TOKEN_AUDIENCE": c.Audience,
			"CS_EDGE_API_PUBLIC_ORIGIN": c.PublicOrigin, "CS_EDGE_API_DATABASE_DSN_FILE": c.DatabaseDSNFile,
		} {
			t.Setenv(name, v)
		}
	}
	for name, env := range map[string]*string{"unset": nil, "staging": strPtr("staging"), "production": strPtr("production"), "development": strPtr("development")} {
		t.Run(name, func(t *testing.T) {
			setAll(t)
			t.Setenv(EnvPrefix+"_ENVIRONMENT", "")
			if env == nil {
				if err := os.Unsetenv(EnvPrefix + "_ENVIRONMENT"); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Setenv(EnvPrefix+"_ENVIRONMENT", *env)
			}
			got, _, err := LoadActivation()
			if err != nil {
				t.Fatal(err)
			}
			want := pgshared.RequireVerifiedTLS
			if env != nil && *env == "development" {
				want = pgshared.AllowPlaintextForDevelopment
			}
			if got.dbTLS != want {
				t.Fatalf("dbTLS = %v, want %v", got.dbTLS, want)
			}
			if want != pgshared.RequireVerifiedTLS {
				return
			}
			got.DatabaseDSNFile = writeFile(t, "dsn", []byte("postgres://edgeuser:"+secretish+"@127.0.0.1:1/edgedb?connect_timeout=2"))
			rt, err := Open(context.Background(), got, nil, nil)
			if rt != nil || !errors.Is(err, pgshared.ErrInsecureDSN) {
				t.Fatalf("Open = (%v, %v), want ErrInsecureDSN", rt, err)
			}
			for _, s := range []string{secretish, "edgeuser", "edgedb", "127.0.0.1"} {
				if strings.Contains(err.Error(), s) {
					t.Errorf("the error names %q: %v", s, err)
				}
			}
		})
	}
}

func strPtr(s string) *string { return &s }
