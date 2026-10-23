package accesstoken

import (
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
	"strings"
	"testing"
	"time"
)

// These tests cover the token core only. A verified token's cnf.jkt NAMES a
// device key; nothing here proves the presenter holds that key — that is the
// proof-of-possession step ADR-0010 requires and this package does not do.

const (
	testIss = "urn:callscreen:identity:local"
	testAud = "urn:callscreen:edge-api:local"
)

var t0 = time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)

func fixed(at time.Time) func() time.Time { return func() time.Time { return at } }

func newEdKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func deviceJKT(t *testing.T) string {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jkt, err := ThumbprintP256(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return jkt
}

func newSigner(t *testing.T, kid string, priv ed25519.PrivateKey, now time.Time) *Signer {
	t.Helper()
	s, err := NewSigner(SignerConfig{Issuer: testIss, Audience: testAud, KeyID: kid, Key: priv, Now: fixed(now)})
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s
}

func newVerifier(t *testing.T, keys map[string]ed25519.PublicKey, now time.Time) *Verifier {
	t.Helper()
	v, err := NewVerifier(VerifierConfig{Issuer: testIss, Audience: testAud, Keys: keys, Now: fixed(now)})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

func enc(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// forge builds a token from raw header and payload JSON, signed by sign.
func forge(header, payload string, sign func([]byte) []byte) string {
	input := enc(header) + "." + enc(payload)
	return input + "." + base64.RawURLEncoding.EncodeToString(sign([]byte(input)))
}

func edSign(priv ed25519.PrivateKey) func([]byte) []byte {
	return func(in []byte) []byte { return ed25519.Sign(priv, in) }
}

// validPayload is a correct claim set at time at, for forging variants of it.
func validPayload(t *testing.T, at time.Time, jkt string) map[string]any {
	t.Helper()
	iat := at.Unix()
	return map[string]any{
		"iss": testIss, "aud": testAud, "sub": "usr_test", "iat": iat, "nbf": iat,
		"exp": iat + 900, "jti": "AAAAAAAAAAAAAAAAAAAAAA", "cnf": map[string]any{"jkt": jkt},
		"roles": []string{"subscriber"},
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const goodHeader = `{"alg":"EdDSA","typ":"at+jwt","kid":"k1"}`

func requireInvalid(t *testing.T, v *Verifier, token, why string) {
	t.Helper()
	c, err := v.Verify(token)
	if !errors.Is(err, ErrInvalidToken) {
		t.Errorf("%s: err = %v, want ErrInvalidToken", why, err)
	}
	if c != nil {
		t.Errorf("%s: claims were returned for an invalid token", why)
	}
}

// 1, 2, 3: a valid token round-trips; its lifetime is exactly 15 minutes; its
// claim set is exactly the profile's, with no MSISDN or tenant.
func TestToken_ValidRoundTripAndShape(t *testing.T) {
	pub, priv := newEdKey(t)
	jkt := deviceJKT(t)
	tok, err := newSigner(t, "k1", priv, t0).Issue("usr_abc", []string{"subscriber"}, jkt)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	parts := strings.Split(tok, ".")
	if len(parts) != 3 || strings.ContainsAny(tok, "=+/") {
		t.Fatalf("token is not three unpadded base64url segments: %q", tok)
	}
	rawHeader, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if string(rawHeader) != goodHeader {
		t.Errorf("header = %s, want %s", rawHeader, goodHeader)
	}
	rawClaims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var members map[string]json.RawMessage
	if err = json.Unmarshal(rawClaims, &members); err != nil {
		t.Fatal(err)
	}
	want := []string{"aud", "cnf", "exp", "iat", "iss", "jti", "nbf", "roles", "sub"}
	if len(members) != len(want) {
		t.Errorf("claims = %s, want exactly %v", rawClaims, want)
	}
	for _, k := range want {
		if _, ok := members[k]; !ok {
			t.Errorf("claim %q missing", k)
		}
	}
	for _, banned := range []string{"msisdn", "tenant_id", "ten_in_south"} {
		if strings.Contains(string(rawClaims), banned) {
			t.Errorf("claims carry %q", banned)
		}
	}

	c, err := newVerifier(t, map[string]ed25519.PublicKey{"k1": pub}, t0).Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if c.Issuer != testIss || c.Audience != testAud || c.Subject != "usr_abc" ||
		c.Confirmation.JKT != jkt || len(c.Roles) != 1 || c.Roles[0] != "subscriber" {
		t.Errorf("claims = %+v", c)
	}
	if c.IssuedAt != t0.Unix() || c.NotBefore != c.IssuedAt || c.Expiry-c.IssuedAt != 15*60 {
		t.Errorf("iat=%d nbf=%d exp=%d; want nbf=iat and exp=iat+900", c.IssuedAt, c.NotBefore, c.Expiry)
	}
	if jti, err := base64.RawURLEncoding.DecodeString(c.ID); err != nil || len(jti) != 16 {
		t.Errorf("jti %q is not 16 random bytes", c.ID)
	}
}

func TestToken_JTIIsUniqueAndRandomnessFailureIssuesNothing(t *testing.T) {
	_, priv := newEdKey(t)
	s := newSigner(t, "k1", priv, t0)
	jkt := deviceJKT(t)
	a, _ := s.Issue("usr_a", nil, jkt)
	b, _ := s.Issue("usr_a", nil, jkt)
	if a == b {
		t.Error("two tokens at the same instant are identical; jti is not random")
	}

	bad, _ := NewSigner(SignerConfig{
		Issuer: testIss, Audience: testAud, KeyID: "k1", Key: priv,
		Now: fixed(t0), Random: strings.NewReader("short"),
	})
	if tok, err := bad.Issue("usr_a", nil, jkt); err == nil || tok != "" {
		t.Errorf("a failing randomness source still issued %q (err %v)", tok, err)
	}
}

// 4: the binding is mandatory and well-formed, at issue and at verification.
func TestToken_ConfirmationIsRequired(t *testing.T) {
	pub, priv := newEdKey(t)
	s := newSigner(t, "k1", priv, t0)
	v := newVerifier(t, map[string]ed25519.PublicKey{"k1": pub}, t0)
	good := deviceJKT(t)
	digest := sha256.Sum256([]byte("x"))

	for name, jkt := range map[string]string{
		"empty":           "",
		"too short":       "abc",
		"padded":          good + "=",
		"standard base64": base64.StdEncoding.EncodeToString(digest[:]),
		"wrong length":    base64.RawURLEncoding.EncodeToString(digest[:31]),
	} {
		if _, err := s.Issue("usr_a", nil, jkt); !errors.Is(err, ErrInvalidClaims) {
			t.Errorf("Issue with %s jkt: err = %v, want ErrInvalidClaims", name, err)
		}
	}

	// Correctly signed tokens whose binding is absent or malformed.
	for name, mutate := range map[string]func(map[string]any){
		"cnf missing":     func(p map[string]any) { delete(p, "cnf") },
		"jkt missing":     func(p map[string]any) { p["cnf"] = map[string]any{} },
		"jkt malformed":   func(p map[string]any) { p["cnf"] = map[string]any{"jkt": "not-a-thumbprint"} },
		"cnf not object":  func(p map[string]any) { p["cnf"] = good },
		"cnf extra field": func(p map[string]any) { p["cnf"] = map[string]any{"jkt": good, "jwk": "x"} },
	} {
		p := validPayload(t, t0, good)
		mutate(p)
		requireInvalid(t, v, forge(goodHeader, mustJSON(t, p), edSign(priv)), name)
	}
}

// 5, 6: tampering, the wrong key, and an unknown key id all fail.
func TestToken_TamperingAndWrongKeys(t *testing.T) {
	pub, priv := newEdKey(t)
	_, otherPriv := newEdKey(t)
	v := newVerifier(t, map[string]ed25519.PublicKey{"k1": pub}, t0)
	jkt := deviceJKT(t)
	tok, _ := newSigner(t, "k1", priv, t0).Issue("usr_a", []string{"subscriber"}, jkt)
	parts := strings.Split(tok, ".")

	// Re-encoded header naming another type, payload granting a role, flipped
	// signature byte: each keeps the original signature.
	requireInvalid(t, v, enc(`{"alg":"EdDSA","typ":"at+jwt","kid":"k2"}`)+"."+parts[1]+"."+parts[2], "tampered header")
	p := validPayload(t, t0, jkt)
	p["roles"] = []string{"admin"}
	requireInvalid(t, v, parts[0]+"."+enc(mustJSON(t, p))+"."+parts[2], "tampered payload")
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sig[0] ^= 0x01
	requireInvalid(t, v, parts[0]+"."+parts[1]+"."+base64.RawURLEncoding.EncodeToString(sig), "tampered signature")

	other, _ := newSigner(t, "k1", otherPriv, t0).Issue("usr_a", nil, jkt)
	requireInvalid(t, v, other, "signed by a key the verifier does not hold under that kid")
	unknown, _ := newSigner(t, "k9", priv, t0).Issue("usr_a", nil, jkt)
	requireInvalid(t, v, unknown, "unknown kid")
}

// 7: no algorithm but EdDSA is ever accepted, and the token cannot choose one.
func TestToken_OnlyEdDSAIsAccepted(t *testing.T) {
	pub, priv := newEdKey(t)
	v := newVerifier(t, map[string]ed25519.PublicKey{"k1": pub}, t0)
	payload := mustJSON(t, validPayload(t, t0, deviceJKT(t)))

	requireInvalid(t, v, enc(`{"alg":"none","typ":"at+jwt","kid":"k1"}`)+"."+enc(payload)+".", "alg none, empty signature")
	requireInvalid(t, v, forge(`{"alg":"none","typ":"at+jwt","kid":"k1"}`, payload, edSign(priv)), "alg none, real signature")

	// HS256 using the PUBLIC key as the HMAC secret: the classic confusion.
	requireInvalid(t, v, forge(`{"alg":"HS256","typ":"at+jwt","kid":"k1"}`, payload, func(in []byte) []byte {
		m := hmac.New(sha256.New, pub)
		m.Write(in)
		return m.Sum(nil)
	}), "HS256 keyed with the public key")

	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	requireInvalid(t, v, forge(`{"alg":"ES256","typ":"at+jwt","kid":"k1"}`, payload, func(in []byte) []byte {
		d := sha256.Sum256(in)
		s, _ := ecdsa.SignASN1(rand.Reader, ecKey, d[:])
		return s
	}), "ES256")

	for _, alg := range []string{"eddsa", "Ed25519", "EdDSA ", "RS256", ""} {
		requireInvalid(t, v, forge(`{"alg":"`+alg+`","typ":"at+jwt","kid":"k1"}`, payload, edSign(priv)), "alg "+alg)
	}
	requireInvalid(t, v, forge(`{"alg":"EdDSA","typ":"JWT","kid":"k1"}`, payload, edSign(priv)), "typ JWT")

	// Token-supplied key locations are refused outright, even correctly signed.
	for _, extra := range []string{`"jku":"https://evil.example/jwks"`, `"x5u":"https://evil.example/c"`, `"jwk":{"kty":"OKP"}`, `"crit":["exp"]`} {
		requireInvalid(t, v, forge(`{"alg":"EdDSA","typ":"at+jwt","kid":"k1",`+extra+`}`, payload, edSign(priv)), "header "+extra)
	}
}

// 8: expiry, not-before, issued-at, issuer and audience, at their boundaries.
func TestToken_TimeIssuerAndAudience(t *testing.T) {
	pub, priv := newEdKey(t)
	jkt := deviceJKT(t)
	keys := map[string]ed25519.PublicKey{"k1": pub}
	tok, _ := newSigner(t, "k1", priv, t0).Issue("usr_a", nil, jkt)
	exp := t0.Add(15 * time.Minute)

	if _, err := newVerifier(t, keys, exp.Add(59*time.Second)).Verify(tok); err != nil {
		t.Errorf("within the 60s skew after exp: %v", err)
	}
	requireInvalid(t, newVerifier(t, keys, exp.Add(60*time.Second)), tok, "expired beyond the skew")

	early := t0.Add(-59 * time.Second)
	if _, err := newVerifier(t, keys, early).Verify(tok); err != nil {
		t.Errorf("within the 60s skew before nbf/iat: %v", err)
	}
	requireInvalid(t, newVerifier(t, keys, t0.Add(-61*time.Second)), tok, "nbf and iat beyond the skew in the future")

	// Correctly signed, but not this profile's time shape.
	v := newVerifier(t, keys, t0)
	for name, mutate := range map[string]func(map[string]any){
		"one-hour lifetime": func(p map[string]any) { p["exp"] = p["iat"].(int64) + 3600 },
		"nbf after iat":     func(p map[string]any) { p["nbf"] = p["iat"].(int64) + 10 },
		"iat zero":          func(p map[string]any) { p["iat"], p["nbf"], p["exp"] = int64(0), int64(0), int64(900) },
		"exp missing":       func(p map[string]any) { delete(p, "exp") },
	} {
		p := validPayload(t, t0, jkt)
		mutate(p)
		requireInvalid(t, v, forge(goodHeader, mustJSON(t, p), edSign(priv)), name)
	}

	for name, mutate := range map[string]func(map[string]any){
		"wrong issuer":      func(p map[string]any) { p["iss"] = "urn:callscreen:identity:other" },
		"wrong audience":    func(p map[string]any) { p["aud"] = "urn:callscreen:other:local" },
		"audience as array": func(p map[string]any) { p["aud"] = []string{testAud} },
		"missing subject":   func(p map[string]any) { p["sub"] = "" },
		"missing jti":       func(p map[string]any) { p["jti"] = "" },
		"unknown claim":     func(p map[string]any) { p["msisdn"] = "+14155552671" },
	} {
		p := validPayload(t, t0, jkt)
		mutate(p)
		requireInvalid(t, v, forge(goodHeader, mustJSON(t, p), edSign(priv)), name)
	}
}

// 9: malformed encodings, sizes and the legacy unsigned format.
func TestToken_MalformedAndLegacy(t *testing.T) {
	pub, priv := newEdKey(t)
	v := newVerifier(t, map[string]ed25519.PublicKey{"k1": pub}, t0)
	jkt := deviceJKT(t)
	tok, _ := newSigner(t, "k1", priv, t0).Issue("usr_a", nil, jkt)
	parts := strings.Split(tok, ".")

	cases := map[string]string{
		"empty":               "",
		"two segments":        parts[0] + "." + parts[1],
		"four segments":       tok + ".x",
		"empty signature":     parts[0] + "." + parts[1] + ".",
		"padded segment":      parts[0] + "=." + parts[1] + "." + parts[2],
		"standard base64":     strings.ReplaceAll(strings.ReplaceAll(tok, "-", "+"), "_", "/") + "+/",
		"whitespace":          " " + tok,
		"oversized":           tok + strings.Repeat("A", MaxTokenBytes),
		"payload not JSON":    forge(goodHeader, "not json", edSign(priv)),
		"payload two objects": forge(goodHeader, mustJSON(t, validPayload(t, t0, jkt))+"{}", edSign(priv)),
		"header not JSON":     forge("{", mustJSON(t, validPayload(t, t0, jkt)), edSign(priv)),
	}
	for name, token := range cases {
		requireInvalid(t, v, token, name)
	}

	// The format identity issued before this package: hex JSON, no signature.
	legacy := "Bearer_JWT." + hex.EncodeToString([]byte(`{"sub":"usr_a","roles":["admin"],"exp":9999999999}`))
	requireInvalid(t, v, legacy, "legacy unsigned Bearer_JWT")
	if _, err := v.Verify(legacy); err == nil || !strings.Contains(err.Error(), "legacy") {
		t.Errorf("legacy token error = %v, want it named as the legacy format", err)
	}
}

// 10: rotation overlap, and a removed key failing closed.
func TestToken_KeyRotation(t *testing.T) {
	pub1, priv1 := newEdKey(t)
	pub2, priv2 := newEdKey(t)
	jkt := deviceJKT(t)
	oldTok, _ := newSigner(t, "k1", priv1, t0).Issue("usr_a", nil, jkt)
	newTok, _ := newSigner(t, "k2", priv2, t0).Issue("usr_a", nil, jkt)

	keys := map[string]ed25519.PublicKey{"k1": pub1, "k2": pub2}
	overlap := newVerifier(t, keys, t0)
	for name, tok := range map[string]string{"previous key": oldTok, "active key": newTok} {
		if _, err := overlap.Verify(tok); err != nil {
			t.Errorf("during the overlap, a token signed with the %s failed: %v", name, err)
		}
	}
	// The verifier copied its key set: changing the caller's map adds nothing.
	_, priv3 := newEdKey(t)
	keys["k3"] = priv3.Public().(ed25519.PublicKey)
	t3, _ := newSigner(t, "k3", priv3, t0).Issue("usr_a", nil, jkt)
	requireInvalid(t, overlap, t3, "key added to the caller's map after construction")

	after := newVerifier(t, map[string]ed25519.PublicKey{"k2": pub2}, t0)
	requireInvalid(t, after, oldTok, "token signed by a removed key")
	if _, err := after.Verify(newTok); err != nil {
		t.Errorf("active-key token after the overlap: %v", err)
	}
}

// 11: empty configuration and bad key material fail closed.
func TestToken_ConfigurationFailsClosed(t *testing.T) {
	pub, priv := newEdKey(t)
	inconsistent := append(ed25519.PrivateKey(nil), priv...)
	inconsistent[ed25519.SeedSize] ^= 0xff // public half no longer matches the seed

	for name, cfg := range map[string]SignerConfig{
		"no issuer":         {Audience: testAud, KeyID: "k1", Key: priv},
		"no audience":       {Issuer: testIss, KeyID: "k1", Key: priv},
		"padded issuer":     {Issuer: " " + testIss, Audience: testAud, KeyID: "k1", Key: priv},
		"no key id":         {Issuer: testIss, Audience: testAud, Key: priv},
		"key id with quote": {Issuer: testIss, Audience: testAud, KeyID: `k"1`, Key: priv},
		"no key":            {Issuer: testIss, Audience: testAud, KeyID: "k1"},
		"short key":         {Issuer: testIss, Audience: testAud, KeyID: "k1", Key: priv[:32]},
		"inconsistent key":  {Issuer: testIss, Audience: testAud, KeyID: "k1", Key: inconsistent},
	} {
		if s, err := NewSigner(cfg); !errors.Is(err, ErrInvalidConfig) || s != nil {
			t.Errorf("NewSigner(%s): (%v, %v), want ErrInvalidConfig", name, s, err)
		}
	}

	good := map[string]ed25519.PublicKey{"k1": pub}
	for name, cfg := range map[string]VerifierConfig{
		"no issuer":     {Audience: testAud, Keys: good},
		"no audience":   {Issuer: testIss, Keys: good},
		"no keys":       {Issuer: testIss, Audience: testAud},
		"short key":     {Issuer: testIss, Audience: testAud, Keys: map[string]ed25519.PublicKey{"k1": pub[:16]}},
		"empty kid":     {Issuer: testIss, Audience: testAud, Keys: map[string]ed25519.PublicKey{"": pub}},
		"negative skew": {Issuer: testIss, Audience: testAud, Keys: good, ClockSkew: -time.Second},
	} {
		if v, err := NewVerifier(cfg); !errors.Is(err, ErrInvalidConfig) || v != nil {
			t.Errorf("NewVerifier(%s): (%v, %v), want ErrInvalidConfig", name, v, err)
		}
	}

	v, err := NewVerifier(VerifierConfig{Issuer: testIss, Audience: testAud, Keys: good})
	if err != nil || v.skew != DefaultClockSkew {
		t.Errorf("zero skew: skew=%v err=%v, want the 60s default", v.skew, err)
	}
	if DefaultClockSkew != 60*time.Second || Lifetime != 15*time.Minute {
		t.Error("the profile constants changed")
	}
}

func TestToken_ParsePrivateKeyPEM(t *testing.T) {
	_, priv := newEdKey(t)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	good := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	got, err := ParsePrivateKeyPEM(good)
	if err != nil || !got.Equal(priv) {
		t.Fatalf("round trip: %v", err)
	}

	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecDER, _ := x509.MarshalPKCS8PrivateKey(ecKey)
	secret := base64.StdEncoding.EncodeToString(der)
	for name, data := range map[string][]byte{
		"empty":            nil,
		"garbage":          []byte("not a key"),
		"wrong PEM type":   pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}),
		"P-256 PKCS#8 key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER}),
		"trailing data":    append(append([]byte{}, good...), []byte("extra")...),
		"corrupt body":     pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der[:10]}),
	} {
		k, err := ParsePrivateKeyPEM(data)
		if !errors.Is(err, ErrInvalidConfig) || k != nil {
			t.Errorf("%s: (%v, %v), want ErrInvalidConfig", name, k != nil, err)
		}
		if err != nil && strings.Contains(err.Error(), secret[:16]) {
			t.Errorf("%s: the error echoes key material", name)
		}
	}
}

// The RFC 7638 thumbprint matches the published example: the DPoP proof key in
// RFC 9449 and the jkt that document gives for it.
func TestThumbprintP256_KnownAnswer(t *testing.T) {
	x, _ := base64.RawURLEncoding.DecodeString("l8tFrhx-34tV3hRICRDY9zCkDlpBhF42UQUfWVAWBFs")
	y, _ := base64.RawURLEncoding.DecodeString("9VE4jf_Ok_o64zbTTlcuNJajHmt6v9TDVrU0CdvGRDA")
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{0x04}, x...), y...))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got, err := ThumbprintP256(pub)
	if err != nil {
		t.Fatal(err)
	}
	if want := "0ZcOCORZNYy-DWpqq30jZyJGHTN0d2HglBV3uiguA4I"; got != want {
		t.Errorf("thumbprint = %s, want %s", got, want)
	}

	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if _, err := ThumbprintP256(&p384.PublicKey); err == nil {
		t.Error("a P-384 key was accepted")
	}
	if _, err := ThumbprintP256(nil); err == nil {
		t.Error("a nil key was accepted")
	}
}
