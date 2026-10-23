package dpop

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
)

const testHTU = "https://identity.local.test/v1/otp/validate"

var t0 = time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)

func enc(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func newDeviceKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func jwkOf(t *testing.T, pub *ecdsa.PublicKey) map[string]any {
	t.Helper()
	b, err := pub.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"kty": "EC", "crv": "P-256", "x": enc(b[1:33]), "y": enc(b[33:65])}
}

func goodClaims(jti string, iat time.Time) map[string]any {
	return map[string]any{"jti": jti, "htm": "POST", "htu": testHTU, "iat": iat.Unix()}
}

func goodHeader(t *testing.T, k *ecdsa.PrivateKey) map[string]any {
	return map[string]any{"typ": Type, "alg": Algorithm, "jwk": jwkOf(t, &k.PublicKey)}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// es256 signs input as ES256 with the 64-byte R||S encoding.
func es256(t *testing.T, k *ecdsa.PrivateKey, input string) []byte {
	t.Helper()
	d := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, k, d[:])
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, 64)
	r.FillBytes(out[:32])
	s.FillBytes(out[32:])
	return out
}

func makeProof(t *testing.T, signer *ecdsa.PrivateKey, header, claims any) string {
	t.Helper()
	input := enc([]byte(mustJSON(t, header))) + "." + enc([]byte(mustJSON(t, claims)))
	return input + "." + enc(es256(t, signer, input))
}

func newVerifier(t *testing.T, store ReplayStore, now time.Time) *Verifier {
	t.Helper()
	v, err := NewVerifier(Config{
		HTU: testHTU, MaxAge: time.Minute, FutureSkew: 5 * time.Second,
		Replay: store, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

func requireInvalid(t *testing.T, v *Verifier, proof, method, why string) {
	t.Helper()
	got, err := v.Verify(context.Background(), proof, method)
	if !errors.Is(err, ErrInvalidProof) || got != nil {
		t.Errorf("%s: (%v, %v), want ErrInvalidProof", why, got, err)
	}
}

// 1: a valid proof verifies, and names the key it proved by the same thumbprint
// the access-token core computes.
func TestProof_Valid(t *testing.T) {
	k := newDeviceKey(t)
	store := NewMemoryReplayStore()
	got, err := newVerifier(t, store, t0).Verify(context.Background(),
		makeProof(t, k, goodHeader(t, k), goodClaims("jti-1", t0)), "POST")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	want, _ := accesstoken.ThumbprintP256(&k.PublicKey)
	if got.JKT != want || got.JTI != "jti-1" || !got.IssuedAt.Equal(t0) {
		t.Errorf("verified = %+v, want jkt %s", got, want)
	}
	if store.Len() != 1 {
		t.Errorf("replay store holds %d reservations, want 1", store.Len())
	}
}

// 2: the proof is for one method and one URL.
func TestProof_MethodAndURL(t *testing.T) {
	k := newDeviceKey(t)
	v := newVerifier(t, NewMemoryReplayStore(), t0)
	requireInvalid(t, v, makeProof(t, k, goodHeader(t, k), goodClaims("j", t0)), "GET", "request method GET")
	requireInvalid(t, v, makeProof(t, k, goodHeader(t, k), goodClaims("j", t0)), "post", "method case")
	for name, htu := range map[string]string{
		"other path":     "https://identity.local.test/v1/otp/request",
		"trailing slash": testHTU + "/",
		"query":          testHTU + "?a=1",
		"fragment":       testHTU + "#x",
		"other scheme":   "http://identity.local.test/v1/otp/validate",
		"other host":     "https://evil.test/v1/otp/validate",
		"upper-case":     strings.ToUpper(testHTU),
	} {
		c := goodClaims("j-"+name, t0)
		c["htu"] = htu
		requireInvalid(t, v, makeProof(t, k, goodHeader(t, k), c), "POST", "htu "+name)
	}
	c := goodClaims("j-htm", t0)
	c["htm"] = "GET"
	requireInvalid(t, v, makeProof(t, k, goodHeader(t, k), c), "POST", "htm GET for a POST")
}

// 3: signatures.
func TestProof_Signatures(t *testing.T) {
	k, other := newDeviceKey(t), newDeviceKey(t)
	v := newVerifier(t, NewMemoryReplayStore(), t0)

	requireInvalid(t, v, makeProof(t, other, goodHeader(t, k), goodClaims("j", t0)), "POST", "signed by a key other than the embedded one")

	proof := makeProof(t, k, goodHeader(t, k), goodClaims("j", t0))
	parts := strings.Split(proof, ".")
	tampered := goodClaims("j", t0)
	tampered["htu"] = "https://identity.local.test/v1/admin"
	requireInvalid(t, v, parts[0]+"."+enc([]byte(mustJSON(t, tampered)))+"."+parts[2], "POST", "tampered payload")
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sig[5] ^= 0x01
	requireInvalid(t, v, parts[0]+"."+parts[1]+"."+enc(sig), "POST", "flipped signature byte")

	d := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	der, _ := ecdsa.SignASN1(rand.Reader, k, d[:])
	requireInvalid(t, v, parts[0]+"."+parts[1]+"."+enc(der), "POST", "DER-encoded signature")
}

// 4 and 5: the jwk and the header, exactly.
func TestProof_HeaderAndKey(t *testing.T) {
	k := newDeviceKey(t)
	v := newVerifier(t, NewMemoryReplayStore(), t0)
	claims := goodClaims("j", t0)
	jwk := jwkOf(t, &k.PublicKey)

	for name, mutate := range map[string]func(h map[string]any){
		"jwk missing":           func(h map[string]any) { delete(h, "jwk") },
		"jwk not an object":     func(h map[string]any) { h["jwk"] = "x" },
		"x missing":             func(h map[string]any) { j := clone(jwk); delete(j, "x"); h["jwk"] = j },
		"x not base64url":       func(h map[string]any) { j := clone(jwk); j["x"] = "!!!"; h["jwk"] = j },
		"x 31 bytes":            func(h map[string]any) { j := clone(jwk); j["x"] = enc(make([]byte, 31)); h["jwk"] = j },
		"x padded":              func(h map[string]any) { j := clone(jwk); j["x"] = jwk["x"].(string) + "="; h["jwk"] = j },
		"point not on curve":    func(h map[string]any) { j := clone(jwk); j["y"] = enc(make([]byte, 32)); h["jwk"] = j },
		"curve P-384":           func(h map[string]any) { j := clone(jwk); j["crv"] = "P-384"; h["jwk"] = j },
		"kty RSA":               func(h map[string]any) { j := clone(jwk); j["kty"] = "RSA"; h["jwk"] = j },
		"private member d":      func(h map[string]any) { j := clone(jwk); j["d"] = enc(k.D.Bytes()); h["jwk"] = j },
		"extra jwk member use":  func(h map[string]any) { j := clone(jwk); j["use"] = "sig"; h["jwk"] = j },
		"alg RS256":             func(h map[string]any) { h["alg"] = "RS256" },
		"alg EdDSA":             func(h map[string]any) { h["alg"] = "EdDSA" },
		"alg none":              func(h map[string]any) { h["alg"] = "none" },
		"typ JWT":               func(h map[string]any) { h["typ"] = "JWT" },
		"extra header kid":      func(h map[string]any) { h["kid"] = "k1" },
		"extra header jku":      func(h map[string]any) { h["jku"] = "https://evil.test/jwks" },
		"typ missing":           func(h map[string]any) { delete(h, "typ") },
		"alg missing":           func(h map[string]any) { delete(h, "alg") },
		"private d at the root": func(h map[string]any) { h["d"] = "x" },
	} {
		h := goodHeader(t, k)
		mutate(h)
		requireInvalid(t, v, makeProof(t, k, h, claims), "POST", name)
	}

	got, err := v.Verify(context.Background(), makeProof(t, k, func() map[string]any {
		h := goodHeader(t, k)
		j := clone(jwk)
		j["d"] = enc(k.D.Bytes())
		h["jwk"] = j
		return h
	}(), goodClaims("jd", t0)), "POST")
	if got != nil || err == nil || !strings.Contains(err.Error(), "private key material") {
		t.Errorf("a jwk carrying d: (%v, %v), want it refused as private key material", got, err)
	}
}

func clone(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

// 6 and 7: freshness, jti and the claim set.
func TestProof_ClaimsAndFreshness(t *testing.T) {
	k := newDeviceKey(t)
	v := newVerifier(t, NewMemoryReplayStore(), t0)
	ok := func(name string, c map[string]any) {
		t.Helper()
		if _, err := v.Verify(context.Background(), makeProof(t, k, goodHeader(t, k), c), "POST"); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	ok("iat exactly MaxAge old", goodClaims("a1", t0.Add(-time.Minute)))
	ok("iat at the future-skew limit", goodClaims("a2", t0.Add(5*time.Second)))
	ok("jti of 128 bytes", goodClaims(strings.Repeat("j", 128), t0))

	for name, c := range map[string]map[string]any{
		"stale by a second":          goodClaims("s1", t0.Add(-time.Minute-time.Second)),
		"future beyond skew":         goodClaims("s2", t0.Add(6*time.Second)),
		"jti of 129 bytes":           goodClaims(strings.Repeat("j", 129), t0),
		"jti empty":                  goodClaims("", t0),
		"iat missing":                func() map[string]any { c := goodClaims("s3", t0); delete(c, "iat"); return c }(),
		"iat a string":               func() map[string]any { c := goodClaims("s4", t0); c["iat"] = "now"; return c }(),
		"jti missing":                func() map[string]any { c := goodClaims("", t0); delete(c, "jti"); return c }(),
		"ath present (no token yet)": func() map[string]any { c := goodClaims("s5", t0); c["ath"] = "x"; return c }(),
		"nonce present":              func() map[string]any { c := goodClaims("s6", t0); c["nonce"] = "x"; return c }(),
		"htm missing":                func() map[string]any { c := goodClaims("s7", t0); delete(c, "htm"); return c }(),
	} {
		requireInvalid(t, v, makeProof(t, k, goodHeader(t, k), c), "POST", name)
	}

	proof := makeProof(t, k, goodHeader(t, k), goodClaims("m", t0))
	parts := strings.Split(proof, ".")
	for name, p := range map[string]string{
		"two segments":  parts[0] + "." + parts[1],
		"four segments": proof + ".x",
		"padded":        parts[0] + "=." + parts[1] + "." + parts[2],
		"oversized":     proof + strings.Repeat("A", MaxProofBytes),
		"empty":         "",
	} {
		requireInvalid(t, v, p, "POST", name)
	}
}

// 8: a proof is single-use; the reservation is scoped to the key; an invalid
// proof reserves nothing; and an expired reservation does not block for ever.
func TestProof_Replay(t *testing.T) {
	k, other := newDeviceKey(t), newDeviceKey(t)
	store := NewMemoryReplayStore()
	v := newVerifier(t, store, t0)
	proof := makeProof(t, k, goodHeader(t, k), goodClaims("once", t0))

	if _, err := v.Verify(context.Background(), proof, "POST"); err != nil {
		t.Fatalf("first presentation: %v", err)
	}
	_, err := v.Verify(context.Background(), proof, "POST")
	if !errors.Is(err, ErrReplayed) || !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("second presentation: err = %v, want ErrReplayed (an ErrInvalidProof)", err)
	}
	if _, err := v.Verify(context.Background(), makeProof(t, other, goodHeader(t, other), goodClaims("once", t0)), "POST"); err != nil {
		t.Errorf("the same jti under a different key was refused: %v", err)
	}

	before := store.Len()
	requireInvalid(t, v, makeProof(t, k, goodHeader(t, k), goodClaims("bad", t0)), "GET", "invalid proof")
	if store.Len() != before {
		t.Error("an invalid proof consumed a replay reservation")
	}

	// Store-level expiry: a reservation lapses at expiresAt and is pruned.
	s := NewMemoryReplayStore()
	exp := t0.Add(time.Minute)
	if err := s.Reserve(context.Background(), "jkt", "j", exp, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.Reserve(context.Background(), "jkt", "j", exp, exp.Add(-time.Nanosecond)); !errors.Is(err, ErrReplayed) {
		t.Errorf("before expiry: err = %v, want ErrReplayed", err)
	}
	if err := s.Reserve(context.Background(), "jkt", "j", exp.Add(time.Minute), exp); err != nil {
		t.Errorf("at expiry the reservation still blocked: %v", err)
	}
	_ = s.Reserve(context.Background(), "jkt2", "k", exp.Add(time.Hour), exp.Add(2*time.Minute))
	if s.Len() != 1 {
		t.Errorf("expired reservations were not pruned: %d held, want 1", s.Len())
	}
}

// 9: concurrent presentations of one proof: exactly one succeeds.
func TestProof_ConcurrentReplay(t *testing.T) {
	k := newDeviceKey(t)
	v := newVerifier(t, NewMemoryReplayStore(), t0)
	proof := makeProof(t, k, goodHeader(t, k), goodClaims("race", t0))

	const n = 32
	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = v.Verify(context.Background(), proof, "POST")
		}()
	}
	close(start)
	wg.Wait()
	ok, replayed := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrReplayed):
			replayed++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || replayed != n-1 {
		t.Errorf("%d concurrent presentations: %d accepted, %d replayed; want 1 and %d", n, ok, replayed, n-1)
	}
}

type failingStore struct{}

func (failingStore) Reserve(context.Context, string, string, time.Time, time.Time) error {
	return errors.New("replay store unreachable")
}

// 10: a store that cannot answer fails closed, and is not reported as a bad
// proof.
func TestProof_ReplayStoreFailureFailsClosed(t *testing.T) {
	k := newDeviceKey(t)
	got, err := newVerifier(t, failingStore{}, t0).Verify(context.Background(),
		makeProof(t, k, goodHeader(t, k), goodClaims("j", t0)), "POST")
	if got != nil || !errors.Is(err, ErrReplayUnavailable) || errors.Is(err, ErrInvalidProof) {
		t.Errorf("(%v, %v), want ErrReplayUnavailable and nothing verified", got, err)
	}
}

// 11: the jwk path yields the RFC 7638 thumbprint of the RFC 9449 example key.
func TestProof_ThumbprintKnownAnswer(t *testing.T) {
	j := &jwk{
		Kty: "EC", Crv: "P-256",
		X: "l8tFrhx-34tV3hRICRDY9zCkDlpBhF42UQUfWVAWBFs", Y: "9VE4jf_Ok_o64zbTTlcuNJajHmt6v9TDVrU0CdvGRDA",
	}
	pub, err := j.publicKey()
	if err != nil {
		t.Fatal(err)
	}
	got, err := accesstoken.ThumbprintP256(pub)
	if err != nil {
		t.Fatal(err)
	}
	if want := "0ZcOCORZNYy-DWpqq30jZyJGHTN0d2HglBV3uiguA4I"; got != want {
		t.Errorf("thumbprint = %s, want %s", got, want)
	}
}

// 16: no error carries proof content.
func TestProof_ErrorsCarryNoProofContent(t *testing.T) {
	k := newDeviceKey(t)
	v := newVerifier(t, NewMemoryReplayStore(), t0)
	jwk := jwkOf(t, &k.PublicKey)
	c := goodClaims("secret-jti-value", t0.Add(-time.Hour))
	proof := makeProof(t, k, goodHeader(t, k), c)
	_, err := v.Verify(context.Background(), proof, "GET")
	if err == nil {
		t.Fatal("expected a rejection")
	}
	for what, val := range map[string]string{
		"jwk x": jwk["x"].(string), "jwk y": jwk["y"].(string), "jti": "secret-jti-value",
		"htu": testHTU, "proof": proof[:40],
	} {
		if strings.Contains(err.Error(), val) {
			t.Errorf("the error carries the %s", what)
		}
	}
}

func TestProof_ConfigFailsClosed(t *testing.T) {
	store := NewMemoryReplayStore()
	for name, cfg := range map[string]Config{
		"no HTU":            {MaxAge: time.Minute, Replay: store},
		"relative HTU":      {HTU: "/v1/otp/validate", MaxAge: time.Minute, Replay: store},
		"HTU with query":    {HTU: testHTU + "?x=1", MaxAge: time.Minute, Replay: store},
		"HTU fragment":      {HTU: testHTU + "#f", MaxAge: time.Minute, Replay: store},
		"HTU userinfo":      {HTU: "https://u:p@identity.local.test/x", MaxAge: time.Minute, Replay: store},
		"HTU ftp":           {HTU: "ftp://identity.local.test/x", MaxAge: time.Minute, Replay: store},
		"zero MaxAge":       {HTU: testHTU, Replay: store},
		"MaxAge 6m":         {HTU: testHTU, MaxAge: 6 * time.Minute, Replay: store},
		"negative skew":     {HTU: testHTU, MaxAge: time.Minute, FutureSkew: -time.Second, Replay: store},
		"skew 2m":           {HTU: testHTU, MaxAge: time.Minute, FutureSkew: 2 * time.Minute, Replay: store},
		"no replay store":   {HTU: testHTU, MaxAge: time.Minute},
		"non-canonical HTU": {HTU: "HTTPS://identity.local.test/v1/otp/validate", MaxAge: time.Minute, Replay: store},
	} {
		if v, err := NewVerifier(cfg); !errors.Is(err, ErrInvalidConfig) || v != nil {
			t.Errorf("%s: (%v, %v), want ErrInvalidConfig", name, v, err)
		}
	}
}
