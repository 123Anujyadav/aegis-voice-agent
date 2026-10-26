package bootstrap

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

// Test-only values. None of them is, or may become, a production default.
const (
	testIssuer   = "urn:callscreen:identity:test"
	testAudience = "urn:callscreen:edge-api:test"
	testKeyID    = "test-key-1"
	testEnrolURL = "https://identity.example.test/v1/otp/validate"
	testRefresh  = "https://identity.example.test/v1/token/refresh"
	testMSISDN   = "+14155552671"
	testCode     = "246810"
)

var envNames = []string{
	"CS_IDENTITY_ACCESS_TOKEN_PRIVATE_KEY_FILE",
	"CS_IDENTITY_ACCESS_TOKEN_ISSUER",
	"CS_IDENTITY_ACCESS_TOKEN_AUDIENCE",
	"CS_IDENTITY_ACCESS_TOKEN_KID",
	"CS_IDENTITY_ENROLLMENT_CANONICAL_URL",
	"CS_IDENTITY_REFRESH_CANONICAL_URL",
}

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func pkcs8PEM(t *testing.T, key any) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// signingKeyFile writes a generated Ed25519 key to a temporary file.
func signingKeyFile(t *testing.T) (string, ed25519.PublicKey, []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	data := pkcs8PEM(t, priv)
	return writeFile(t, "signing.pem", data), pub, data
}

func validConfig(keyFile string) SecurityConfig {
	return SecurityConfig{
		AccessTokenPrivateKeyFile: keyFile,
		AccessTokenIssuer:         testIssuer,
		AccessTokenAudience:       testAudience,
		AccessTokenKeyID:          testKeyID,
		EnrollmentCanonicalURL:    testEnrolURL,
		RefreshCanonicalURL:       testRefresh,
	}
}

func setEnv(t *testing.T, c SecurityConfig) {
	t.Helper()
	for i, v := range []string{
		c.AccessTokenPrivateKeyFile, c.AccessTokenIssuer, c.AccessTokenAudience,
		c.AccessTokenKeyID, c.EnrollmentCanonicalURL, c.RefreshCanonicalURL,
	} {
		t.Setenv(envNames[i], v)
	}
}

func unset(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "") // registers restoration
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
}

func validDeps() Dependencies {
	return Dependencies{
		Repository:       app.NewMemoryIdentityRepository(),
		SecretKey:        []byte("0123456789abcdef0123456789abcdef"),
		EnrollmentReplay: dpop.NewMemoryReplayStore(),
		RefreshReplay:    dpop.NewMemoryReplayStore(),
	}
}

func wantInvalid(t *testing.T, err error, mention string) {
	t.Helper()
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}
	if mention != "" && !strings.Contains(err.Error(), mention) {
		t.Errorf("err = %q, want it to name %q", err, mention)
	}
}

// 1: each variable missing on its own is refused, by name; all missing reports
// all six at once.
func TestLoad_EachVariableMissing(t *testing.T) {
	path, _, _ := signingKeyFile(t)
	for _, name := range envNames {
		t.Run(name, func(t *testing.T) {
			setEnv(t, validConfig(path))
			unset(t, name)
			_, err := LoadSecurityConfig()
			wantInvalid(t, err, name)
		})
	}
	t.Run("all", func(t *testing.T) {
		for _, n := range envNames {
			unset(t, n)
		}
		_, err := LoadSecurityConfig()
		for _, n := range envNames {
			wantInvalid(t, err, n)
		}
	})
	t.Run("present and valid", func(t *testing.T) {
		setEnv(t, validConfig(path))
		c, err := LoadSecurityConfig()
		if err != nil || c != validConfig(path) {
			t.Fatalf("(%+v, %v)", c, err)
		}
	})
}

// 2: empty, whitespace-only and padded values are refused.
func TestLoad_BlankAndPaddedValues(t *testing.T) {
	path, _, _ := signingKeyFile(t)
	for _, name := range envNames {
		for label, v := range map[string]string{"empty": "", "spaces": "   ", "tab": "\t", "padded": " x "} {
			t.Run(name+"/"+label, func(t *testing.T) {
				setEnv(t, validConfig(path))
				t.Setenv(name, v)
				_, err := LoadSecurityConfig()
				wantInvalid(t, err, name)
			})
		}
	}
}

// 2: a config built in code, not loaded, is held to the same rule: each empty
// field is refused by name before anything is constructed from it.
func TestBuild_EmptyFieldsAreNamed(t *testing.T) {
	path, _, _ := signingKeyFile(t)
	for i, clear := range []func(*SecurityConfig){
		func(c *SecurityConfig) { c.AccessTokenPrivateKeyFile = "" },
		func(c *SecurityConfig) { c.AccessTokenIssuer = "" },
		func(c *SecurityConfig) { c.AccessTokenAudience = "" },
		func(c *SecurityConfig) { c.AccessTokenKeyID = "" },
		func(c *SecurityConfig) { c.EnrollmentCanonicalURL = "" },
		func(c *SecurityConfig) { c.RefreshCanonicalURL = "" },
	} {
		t.Run(envNames[i], func(t *testing.T) {
			c := validConfig(path)
			clear(&c)
			_, err := Build(c, validDeps())
			wantInvalid(t, err, envNames[i]+" is blank")
		})
	}
}

// 3: malformed or non-canonical URLs, and identical endpoint URLs, are refused
// for both endpoints.
func TestBuild_InvalidURLs(t *testing.T) {
	path, _, _ := signingKeyFile(t)
	bad := map[string]string{
		"not a URL":    "not a url",
		"relative":     "/v1/otp/validate",
		"ftp":          "ftp://identity.example.test/x",
		"no host":      "https:///x",
		"query":        "https://identity.example.test/x?a=1",
		"empty query":  "https://identity.example.test/x?",
		"fragment":     "https://identity.example.test/x#f",
		"userinfo":     "https://u:p@identity.example.test/x",
		"upper scheme": "HTTPS://identity.example.test/x",
		"control char": "https://identity.example.test/\x7f",
	}
	for label, u := range bad {
		t.Run("enrolment/"+label, func(t *testing.T) {
			c := validConfig(path)
			c.EnrollmentCanonicalURL = u
			_, err := Build(c, validDeps())
			wantInvalid(t, err, "ENROLLMENT_CANONICAL_URL")
		})
		t.Run("refresh/"+label, func(t *testing.T) {
			c := validConfig(path)
			c.RefreshCanonicalURL = u
			_, err := Build(c, validDeps())
			wantInvalid(t, err, "REFRESH_CANONICAL_URL")
		})
	}
	t.Run("identical", func(t *testing.T) {
		c := validConfig(path)
		c.RefreshCanonicalURL = c.EnrollmentCanonicalURL
		_, err := Build(c, validDeps())
		wantInvalid(t, err, "must differ")
	})
	// Each canonical URL must name the path the API serves for it, or no proof
	// for that endpoint could ever verify.
	for name, mutate := range map[string]func(*SecurityConfig){
		"enrolment on another path": func(c *SecurityConfig) { c.EnrollmentCanonicalURL = "https://identity.example.test/v1/enrol" },
		"enrolment with a prefix": func(c *SecurityConfig) {
			c.EnrollmentCanonicalURL = "https://identity.example.test/identity/v1/otp/validate"
		},
		"refresh on another path":     func(c *SecurityConfig) { c.RefreshCanonicalURL = "https://identity.example.test/v1/refresh" },
		"refresh with trailing slash": func(c *SecurityConfig) { c.RefreshCanonicalURL = "https://identity.example.test/v1/token/refresh/" },
		"paths swapped": func(c *SecurityConfig) {
			c.EnrollmentCanonicalURL, c.RefreshCanonicalURL = c.RefreshCanonicalURL, c.EnrollmentCanonicalURL
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := validConfig(path)
			mutate(&c)
			_, err := Build(c, validDeps())
			wantInvalid(t, err, "must name the served path")
		})
	}
	// The app signs every proof for https, with one identity origin: either
	// rule broken would refuse every client at runtime, so startup refuses.
	for name, mutate := range map[string]func(*SecurityConfig){
		"enrolment over http": func(c *SecurityConfig) { c.EnrollmentCanonicalURL = "http://identity.example.test/v1/otp/validate" },
		"refresh over http":   func(c *SecurityConfig) { c.RefreshCanonicalURL = "http://identity.example.test/v1/token/refresh" },
	} {
		t.Run(name, func(t *testing.T) {
			c := validConfig(path)
			mutate(&c)
			_, err := Build(c, validDeps())
			wantInvalid(t, err, "must be https")
		})
	}
	for name, mutate := range map[string]func(*SecurityConfig){
		"another host": func(c *SecurityConfig) { c.RefreshCanonicalURL = "https://auth.example.test/v1/token/refresh" },
		"another port": func(c *SecurityConfig) {
			c.EnrollmentCanonicalURL = "https://identity.example.test:8443/v1/otp/validate"
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := validConfig(path)
			mutate(&c)
			_, err := Build(c, validDeps())
			wantInvalid(t, err, "must share one origin")
		})
	}
	t.Run("invalid key id", func(t *testing.T) {
		c := validConfig(path)
		c.AccessTokenKeyID = "kid\x01"
		_, err := Build(c, validDeps())
		wantInvalid(t, err, "signer")
	})
}

// 4, 5, 9: unusable key files are refused, and no error carries key material.
func TestBuild_KeyFileFailures(t *testing.T) {
	_, _, goodPEM := signingKeyFile(t)
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecDER, _ := x509.MarshalECPrivateKey(ecKey)
	garbage := make([]byte, 48)
	_, _ = rand.Read(garbage)

	cases := map[string]struct {
		path    func(t *testing.T) string
		mention string
	}{
		"missing":   {func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent.pem") }, "does not exist"},
		"directory": {func(t *testing.T) string { return t.TempDir() }, "not a regular file"},
		"oversized": {func(t *testing.T) string {
			return writeFile(t, "big.pem", append(goodPEM, make([]byte, MaxKeyFileBytes)...))
		}, "larger than"},
		"empty":   {func(t *testing.T) string { return writeFile(t, "empty.pem", nil) }, "no PEM block"},
		"not PEM": {func(t *testing.T) string { return writeFile(t, "x.pem", []byte("hello, not a key")) }, "no PEM block"},
		"SEC1 EC PEM": {func(t *testing.T) string {
			return writeFile(t, "ec.pem", pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ecDER}))
		}, "PEM type"},
		"PKCS#8 P-256": {func(t *testing.T) string { return writeFile(t, "p256.pem", pkcs8PEM(t, ecKey)) }, "want Ed25519"},
		"malformed DER": {func(t *testing.T) string {
			return writeFile(t, "bad.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: garbage}))
		}, "not a PKCS#8"},
		"trailing bytes": {func(t *testing.T) string {
			return writeFile(t, "trail.pem", append(append([]byte{}, goodPEM...), "trailing"...))
		}, "trailing data"},
		"two keys": {func(t *testing.T) string {
			return writeFile(t, "two.pem", append(append([]byte{}, goodPEM...), goodPEM...))
		}, "trailing data"},
	}
	block, _ := pem.Decode(goodPEM)
	secrets := []string{
		base64.StdEncoding.EncodeToString(block.Bytes),
		base64.StdEncoding.EncodeToString(block.Bytes)[:16],
		hex.EncodeToString(block.Bytes),
		"BEGIN PRIVATE KEY",
		base64.StdEncoding.EncodeToString(garbage)[:16],
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			_, err := Build(validConfig(tc.path(t)), validDeps())
			wantInvalid(t, err, "CS_IDENTITY_ACCESS_TOKEN_PRIVATE_KEY_FILE")
			if !strings.Contains(err.Error(), tc.mention) {
				t.Errorf("err = %q, want %q", err, tc.mention)
			}
			for _, s := range secrets {
				if strings.Contains(err.Error(), s) {
					t.Errorf("the error carries key material: %q", err)
				}
			}
		})
	}
}

// 8: every dependency is required.
func TestBuild_MissingDependenciesFailClosed(t *testing.T) {
	path, _, _ := signingKeyFile(t)
	for label, mutate := range map[string]func(*Dependencies){
		"repository":       func(d *Dependencies) { d.Repository = nil },
		"enrolment replay": func(d *Dependencies) { d.EnrollmentReplay = nil },
		"refresh replay":   func(d *Dependencies) { d.RefreshReplay = nil },
		"secret key":       func(d *Dependencies) { d.SecretKey = nil },
		"short secret key": func(d *Dependencies) { d.SecretKey = make([]byte, MinSecretKeyBytes-1) },
		"invalid config":   nil,
	} {
		t.Run(label, func(t *testing.T) {
			deps := validDeps()
			cfg := validConfig(path)
			if mutate != nil {
				mutate(&deps)
			} else {
				cfg.AccessTokenIssuer = ""
			}
			svc, err := Build(cfg, deps)
			if svc != nil {
				t.Error("a service was returned")
			}
			wantInvalid(t, err, "")
		})
	}
}

// knownCodeRepo is a test double: it stores every challenge with the hash of a
// code the test knows, since the service never reveals the code it generated.
// The hash is the service's scheme, HMAC-SHA256 under the secret key, in hex.
type knownCodeRepo struct {
	*app.MemoryIdentityRepository
	secret []byte
}

func (r *knownCodeRepo) SaveOtp(ctx context.Context, c *app.OtpChallenge) error {
	h := hmac.New(sha256.New, r.secret)
	h.Write([]byte(testCode))
	cp := *c
	cp.CodeHash = hex.EncodeToString(h.Sum(nil))
	return r.MemoryIdentityRepository.SaveOtp(ctx, &cp)
}

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func proof(t *testing.T, k *ecdsa.PrivateKey, jti, htu string, iat time.Time) string {
	t.Helper()
	pt, err := k.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	hdr, _ := json.Marshal(map[string]any{
		"typ": "dpop+jwt", "alg": "ES256",
		"jwk": map[string]any{"kty": "EC", "crv": "P-256", "x": b64u(pt[1:33]), "y": b64u(pt[33:65])},
	})
	cl, _ := json.Marshal(map[string]any{"jti": jti, "htm": "POST", "htu": htu, "iat": iat.Unix()})
	input := b64u(hdr) + "." + b64u(cl)
	d := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, k, d[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + b64u(sig)
}

// 6, 7, 10, 11, 13: a complete test configuration builds a service that enrols
// a device and refreshes on proof, each endpoint accepting only proofs for its
// own canonical URL, and every token verifies and is bound to the device.
func TestBuild_EnrolThenRefreshEndToEnd(t *testing.T) {
	path, pub, _ := signingKeyFile(t)
	clock := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	deps := validDeps()
	repo := &knownCodeRepo{MemoryIdentityRepository: app.NewMemoryIdentityRepository(), secret: deps.SecretKey}
	deps.Repository = repo
	deps.Now = now

	setEnv(t, validConfig(path))
	cfg, err := LoadSecurityConfig()
	if err != nil {
		t.Fatalf("LoadSecurityConfig: %v", err)
	}
	svc, err := Build(cfg, deps)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	verifier, err := accesstoken.NewVerifier(accesstoken.VerifierConfig{
		Issuer: testIssuer, Audience: testAudience, Keys: map[string]ed25519.PublicKey{testKeyID: pub}, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	device, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	jkt, _ := accesstoken.ThumbprintP256(&device.PublicKey)
	ctx := context.Background()

	reqID, err := svc.RequestOtp(ctx, testMSISDN)
	if err != nil {
		t.Fatalf("RequestOtp: %v", err)
	}
	// A proof for the refresh URL does not enrol.
	if _, _, validateOtpWithDeviceProofErr := svc.ValidateOtpWithDeviceProof(ctx, reqID, testCode, "POST", proof(t, device, "e0", testRefresh, clock)); !errors.Is(validateOtpWithDeviceProofErr, app.ErrInvalidDeviceProof) {
		t.Fatalf("enrolment with a refresh-URL proof: err = %v, want ErrInvalidDeviceProof", validateOtpWithDeviceProofErr)
	}
	sess, token, err := svc.ValidateOtpWithDeviceProof(ctx, reqID, testCode, "POST", proof(t, device, "e1", testEnrolURL, clock))
	if err != nil {
		t.Fatalf("enrolment: %v", err)
	}
	claims, err := verifier.Verify(token)
	if err != nil {
		t.Fatalf("enrolment token: %v", err)
	}
	if claims.Confirmation.JKT != jkt || sess.DeviceKeyThumbprint != jkt {
		t.Errorf("cnf.jkt = %q, session %q, want the device's %q", claims.Confirmation.JKT, sess.DeviceKeyThumbprint, jkt)
	}
	if claims.Issuer != testIssuer || claims.Audience != testAudience || claims.Expiry-claims.IssuedAt != int64(accesstoken.Lifetime/time.Second) {
		t.Errorf("iss/aud/lifetime = %q/%q/%ds", claims.Issuer, claims.Audience, claims.Expiry-claims.IssuedAt)
	}

	// A proof for the enrolment URL does not refresh, and rotates nothing.
	if _, _, refreshTokenWithDeviceProofErr := svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", proof(t, device, "r0", testEnrolURL, clock)); !errors.Is(refreshTokenWithDeviceProofErr, app.ErrInvalidDeviceProof) {
		t.Fatalf("refresh with an enrolment-URL proof: err = %v, want ErrInvalidDeviceProof", refreshTokenWithDeviceProofErr)
	}
	access, refresh, err := svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", proof(t, device, "r1", testRefresh, clock))
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refresh == "" || refresh == sess.RefreshToken {
		t.Error("the refresh token was not rotated")
	}
	claims, err = verifier.Verify(access)
	if err != nil || claims.Confirmation.JKT != jkt {
		t.Fatalf("refreshed token: (%+v, %v), want cnf.jkt %q", claims, err, jkt)
	}

	// The unbound path issues no token, and the legacy format does not verify.
	reqID2, err := svc.RequestOtp(ctx, testMSISDN)
	if err != nil {
		t.Fatal(err)
	}
	if _, tok, err := svc.ValidateOtp(ctx, reqID2, testCode); err != nil || tok != "" {
		t.Errorf("plain ValidateOtp: (%d bytes, %v), want no token", len(tok), err)
	}
	if _, err := verifier.Verify("Bearer_JWT." + hex.EncodeToString([]byte(`{"sub":"x"}`))); !errors.Is(err, accesstoken.ErrInvalidToken) {
		t.Errorf("a legacy token verified: %v", err)
	}
}

// 13: this package constructs and serves nothing: it imports no HTTP or
// middleware package, so it cannot mount a route.
func TestBootstrap_MountsNoRoutes(t *testing.T) {
	for _, file := range []string{"bootstrap.go", "runtime.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p == "net/http" || strings.HasSuffix(p, "/middleware") {
				t.Errorf("%s imports %s", file, p)
			}
		}
	}
}
