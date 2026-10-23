package accesstoken

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// ADR-0029 D11.3 conditions (a)-(c): RFC conformance vectors, strict JSON and
// fuzzing for the standard-library token profile.

// RFC 8037 Appendix A.1, A.2 and A.4: the Ed25519 key pair and the JWS
// "Example of Ed25519 signing". Ed25519 is deterministic, so the signature is
// a fixed vector.
const (
	rfc8037D            = "nWGxne_9WmC6hEr0kuwsxERJxWl7MmkZcDusAxyuf2A"
	rfc8037X            = "11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo"
	rfc8037SigningInput = "eyJhbGciOiJFZERTQSJ9.RXhhbXBsZSBvZiBFZDI1NTE5IHNpZ25pbmc"
	rfc8037Signature    = "hgyY0il_MGCjP0JzlnLWG1PPOt7-09PGcvMg3AIbQR6dWbhijcNR4ki4iylGjg5BhVsPt9g7sVvpAr_MuM0KAg"
)

// The signature layer this package uses (signing input = header.payload, raw
// unpadded base64url, Ed25519) reproduces the RFC's vector exactly.
func TestRFC8037_Ed25519JWSVector(t *testing.T) {
	t.Parallel()
	seed, err := b64.DecodeString(rfc8037D)
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("seed: %v (%d bytes)", err, len(seed))
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("not an Ed25519 public key")
	}
	if got := b64.EncodeToString(pub); got != rfc8037X {
		t.Fatalf("public key x = %s, want %s", got, rfc8037X)
	}
	if got := b64.EncodeToString(ed25519.Sign(priv, []byte(rfc8037SigningInput))); got != rfc8037Signature {
		t.Fatalf("signature = %s, want the RFC 8037 A.4 vector", got)
	}
	sig, err := b64.DecodeString(rfc8037Signature)
	if err != nil || !ed25519.Verify(pub, []byte(rfc8037SigningInput), sig) {
		t.Fatalf("the RFC vector does not verify: %v", err)
	}

	// The RFC token's header is {"alg":"EdDSA"} only: this profile is
	// narrower (typ and kid are required), so the verifier refuses it even
	// though its signature is valid for a configured key.
	v := newVerifier(t, map[string]ed25519.PublicKey{"k1": pub}, time.Now())
	requireInvalid(t, v, rfc8037SigningInput+"."+rfc8037Signature, "an RFC 8037 JWS without typ and kid")
}

// Condition (a): JSON the profile reads must say exactly one thing.
func TestDecodeStrictJSON_RefusesDuplicateAndCaseVariantMembers(t *testing.T) {
	t.Parallel()
	type inner struct {
		JKT string `json:"jkt"`
	}
	type doc struct {
		Alg   string   `json:"alg"`
		Cnf   inner    `json:"cnf"`
		Roles []string `json:"roles"`
	}
	ok := []string{
		`{"alg":"EdDSA","cnf":{"jkt":"x"},"roles":["a","b"]}`,
		`{"alg":"EdDSA","cnf":{},"roles":[]}`,
		` {"alg" : "EdDSA"} `,
	}
	for _, in := range ok {
		var d doc
		if err := DecodeStrictJSON([]byte(in), &d); err != nil {
			t.Errorf("%s refused: %v", in, err)
		}
	}
	bad := map[string]string{
		"duplicate top-level member": `{"alg":"EdDSA","alg":"none"}`,
		"duplicate nested member":    `{"cnf":{"jkt":"a","jkt":"b"}}`,
		"upper-case member":          `{"ALG":"EdDSA"}`,
		"mixed-case member":          `{"Alg":"EdDSA"}`,
		"nested case variant":        `{"cnf":{"JKT":"a"}}`,
		"unknown member":             `{"alg":"EdDSA","x5u":"https://evil"}`,
		"trailing data":              `{"alg":"EdDSA"}{"alg":"none"}`,
		"unterminated":               `{"alg":"EdDSA"`,
		"empty member name":          `{"":"x"}`,
		"digit in member name":       `{"alg2":"x"}`,
		"not an object":              `["alg"]`,
	}
	for name, in := range bad {
		var d doc
		if err := DecodeStrictJSON([]byte(in), &d); err == nil {
			t.Errorf("%s: %s accepted", name, in)
		}
	}
}

// Condition (a), end to end: a token whose header or payload differs only in
// member-name spelling or duplication is refused, even when correctly signed.
func TestVerify_RefusesParserDifferentialsInSignedTokens(t *testing.T) {
	t.Parallel()
	pub, priv := newEdKey(t)
	now := time.Unix(1_800_000_000, 0)
	v := newVerifier(t, map[string]ed25519.PublicKey{"k1": pub}, now)
	jkt := deviceJKT(t)
	payload := mustJSON(t, validPayload(t, now, jkt))

	if _, err := v.Verify(forge(goodHeader, payload, edSign(priv))); err != nil {
		t.Fatalf("the control token is refused: %v", err)
	}
	cases := map[string]struct{ header, payload string }{
		"duplicate alg":         {`{"alg":"EdDSA","typ":"at+jwt","kid":"k1","alg":"EdDSA"}`, payload},
		"upper-case header key": {`{"ALG":"EdDSA","typ":"at+jwt","kid":"k1"}`, payload},
		"duplicate sub":         {goodHeader, strings.Replace(payload, `"sub":"usr_test"`, `"sub":"usr_test","sub":"usr_admin"`, 1)},
		"case-variant claim":    {goodHeader, strings.Replace(payload, `"sub":`, `"Sub":`, 1)},
		"duplicate cnf.jkt":     {goodHeader, strings.Replace(payload, `"jkt":"`+jkt+`"`, `"jkt":"`+jkt+`","jkt":"`+jkt+`"`, 1)},
	}
	for name, c := range cases {
		requireInvalid(t, v, forge(c.header, c.payload, edSign(priv)), name)
	}
}

// Condition (c): the verifier never panics, and anything it accepts verifies
// again identically.
func FuzzVerify(f *testing.F) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		f.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	s, err := NewSigner(SignerConfig{Issuer: testIss, Audience: testAud, KeyID: "k1", Key: priv, Now: fixed(now)})
	if err != nil {
		f.Fatal(err)
	}
	jkt := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	good, err := s.Issue("usr_fuzz", []string{"subscriber"}, jkt)
	if err != nil {
		f.Fatal(err)
	}
	v, err := NewVerifier(VerifierConfig{Issuer: testIss, Audience: testAud, Keys: map[string]ed25519.PublicKey{"k1": pub}, Now: fixed(now)})
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range []string{good, "", "a.b.c", "Bearer_JWT.00", strings.Repeat("A", MaxTokenBytes+1), rfc8037SigningInput + "." + rfc8037Signature} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, token string) {
		c, err := v.Verify(token)
		if (c == nil) == (err == nil) {
			t.Fatalf("Verify(%q) = (%v, %v): exactly one must be set", token, c, err)
		}
		if err == nil {
			again, err2 := v.Verify(token)
			if err2 != nil || again.Subject != c.Subject || again.ID != c.ID {
				t.Fatalf("accepted token does not verify identically twice: %v", err2)
			}
		}
	})
}
