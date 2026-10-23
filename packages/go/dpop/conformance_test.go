package dpop

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
)

// ADR-0029 D11.3 conditions (b) and (c), and D11.4: RFC 9449's own examples
// verify under this profile, the strict-JSON and jti rules hold, and the
// parser never panics.

// RFC 9449 example key (§4.1, §6.1) and its RFC 7638 thumbprint; the §5
// example access token and its §7.1 ath; and the two complete example proofs,
// §4.1 (token endpoint, no ath) and §7.1 (resource request, with ath).
//
// The proofs are assembled from the RFC's header and claims JSON and its
// signatures rather than written as token-shaped literals, which secret
// scanners treat as possible credentials. The assembly is byte-exact or the
// ES256 signatures below would not verify.
const (
	rfcX         = "l8tFrhx-34tV3hRICRDY9zCkDlpBhF42UQUfWVAWBFs"
	rfcY         = "9VE4jf_Ok_o64zbTTlcuNJajHmt6v9TDVrU0CdvGRDA"
	rfcJKT       = "0ZcOCORZNYy-DWpqq30jZyJGHTN0d2HglBV3uiguA4I"
	rfcExampleAT = "Kz~8mXK1EalYznwH-LC-1fBAo.4Ljp~zsPE_NeO.gxU"
	rfcATH       = "fUHyO2r2Z3DZ53EsNrWBb0xWXoaNy59IiKCAqksmQEo"

	rfcProofHeader = `{"typ":"dpop+jwt","alg":"ES256","jwk":{"kty":"EC","x":"` + rfcX + `","y":"` + rfcY + `","crv":"P-256"}}`
	rfcClaimsPOST  = `{"jti":"-BwC3ESc6acc2lTc","htm":"POST","htu":"https://server.example.com/token","iat":1562262616}`
	rfcSigPOST     = "2-GxA6T8lP4vfrg8v-FdWP0A0zdrj8igiMLvqRMUvwnQg4PtFLbdLXiOSsX0x7NVY-FNyJK70nfbV37xRZT3Lg"
	rfcClaimsGET   = `{"jti":"e1j3V_bKic8-LAEB","htm":"GET","htu":"https://resource.example.org/protectedresource","iat":1562262618,"ath":"` + rfcATH + `"}`
	rfcSigGET      = "2oW9RP35yRqzhrtNP86L-Ey71EOptxRimPPToA1plemAgR6pxHF8y6-yqyVnmcw6Fy1dqd-jfxSYoMxhAJpLjA"
)

var (
	rfcProofPOST = b64.EncodeToString([]byte(rfcProofHeader)) + "." + b64.EncodeToString([]byte(rfcClaimsPOST)) + "." + rfcSigPOST
	rfcProofGET  = b64.EncodeToString([]byte(rfcProofHeader)) + "." + b64.EncodeToString([]byte(rfcClaimsGET)) + "." + rfcSigGET
)

func TestRFC9449_ThumbprintAndAccessTokenHashVectors(t *testing.T) {
	t.Parallel()
	x, err := b64.DecodeString(rfcX)
	if err != nil {
		t.Fatal(err)
	}
	y, err := b64.DecodeString(rfcY)
	if err != nil {
		t.Fatal(err)
	}
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
	if got, err := accesstoken.ThumbprintP256(pub); err != nil || got != rfcJKT {
		t.Errorf("RFC 7638 thumbprint = %q, %v; want %s (RFC 9449 §6.1)", got, err, rfcJKT)
	}
	if got := AccessTokenHash(rfcExampleAT); got != rfcATH {
		t.Errorf("ath = %s, want %s (RFC 9449 §7.1)", got, rfcATH)
	}
}

// RFC 9449 §4.1's proof is an enrolment-profile proof (jti, htm, htu, iat; no
// ath) and verifies unmodified at its own time, once.
func TestRFC9449_TokenEndpointProofVerifiesOnce(t *testing.T) {
	t.Parallel()
	v, err := NewVerifier(Config{
		HTU: "https://server.example.com/token", MaxAge: 2 * time.Minute, FutureSkew: time.Minute,
		Replay: NewMemoryReplayStore(), Now: func() time.Time { return time.Unix(1562262616, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Verify(context.Background(), rfcProofPOST, "POST")
	if err != nil {
		t.Fatalf("the RFC 9449 §4.1 proof is refused: %v", err)
	}
	if got.JKT != rfcJKT || got.JTI != "-BwC3ESc6acc2lTc" {
		t.Errorf("verified = %+v", got)
	}
	if _, err := v.Verify(context.Background(), rfcProofPOST, "POST"); err == nil {
		t.Error("the same proof was accepted twice")
	}
}

// RFC 9449 §7.1's proof is a resource-profile proof with ath, bound to the
// example token and key.
func TestRFC9449_ResourceProofVerifiesWithAthAndJKT(t *testing.T) {
	t.Parallel()
	v, err := NewRequestVerifier(RequestConfig{
		MaxAge: 2 * time.Minute, FutureSkew: time.Minute,
		Replay: NewMemoryReplayStore(), Now: func() time.Time { return time.Unix(1562262618, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	const htu = "https://resource.example.org/protectedresource"
	if _, err := v.Verify(context.Background(), rfcProofGET, "GET", htu, rfcExampleAT, rfcJKT); err != nil {
		t.Fatalf("the RFC 9449 §7.1 proof is refused: %v", err)
	}
	// The same proof, presented with any other token or key binding, fails.
	for name, c := range map[string][2]string{
		"another token": {rfcExampleAT + "x", rfcJKT},
		"another key":   {rfcExampleAT, strings.Repeat("A", 43)},
	} {
		v2, _ := NewRequestVerifier(RequestConfig{MaxAge: 2 * time.Minute, FutureSkew: time.Minute, Replay: NewMemoryReplayStore(), Now: func() time.Time { return time.Unix(1562262618, 0) }})
		if _, err := v2.Verify(context.Background(), rfcProofGET, "GET", htu, c[0], c[1]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Condition (a) for proofs: duplicate and case-variant members are refused
// even in a correctly signed proof.
func TestVerify_RefusesParserDifferentialsInSignedProofs(t *testing.T) {
	t.Parallel()
	k := newDeviceKey(t)
	now := time.Unix(1_800_000_000, 0)
	jwk := mustJSON(t, jwkOf(t, &k.PublicKey))
	good := `{"typ":"dpop+jwt","alg":"ES256","jwk":` + jwk + `}`
	claims := mustJSON(t, goodClaims("jti-ok", now))
	raw := func(header, claims string) string {
		in := enc([]byte(header)) + "." + enc([]byte(claims))
		return in + "." + enc(es256(t, k, in))
	}
	if _, err := newVerifier(t, NewMemoryReplayStore(), now).Verify(context.Background(), raw(good, claims), "POST"); err != nil {
		t.Fatalf("the control proof is refused: %v", err)
	}
	cases := map[string][2]string{
		"duplicate typ":        {`{"typ":"dpop+jwt","alg":"ES256","typ":"dpop+jwt","jwk":` + jwk + `}`, claims},
		"upper-case header":    {`{"TYP":"dpop+jwt","alg":"ES256","jwk":` + jwk + `}`, claims},
		"duplicate jwk member": {`{"typ":"dpop+jwt","alg":"ES256","jwk":` + strings.Replace(jwk, `"kty":"EC"`, `"kty":"EC","kty":"EC"`, 1) + `}`, claims},
		"duplicate htu":        {good, strings.Replace(claims, `"htu":`, `"htu":"https://evil.example/x","htu":`, 1)},
		"case-variant claim":   {good, strings.Replace(claims, `"htm":`, `"HTM":`, 1)},
	}
	for name, c := range cases {
		requireInvalid(t, newVerifier(t, NewMemoryReplayStore(), now), raw(c[0], c[1]), "POST", name)
	}
}

// D11.4: jti is printable, non-space ASCII.
func TestVerify_JTIMustBePrintableASCII(t *testing.T) {
	t.Parallel()
	k := newDeviceKey(t)
	now := time.Unix(1_800_000_000, 0)
	for name, jti := range map[string]string{
		"NUL": "abc\x00def", "space": "abc def", "tab": "abc\tdef", "non-ASCII": "abcé", "DEL": "abc\x7f",
		"too long": strings.Repeat("a", MaxJTIBytes+1), "empty": "",
	} {
		requireInvalid(t, newVerifier(t, NewMemoryReplayStore(), now), makeProof(t, k, goodHeader(t, k), goodClaims(jti, now)), "POST", "jti "+name)
	}
	for _, jti := range []string{"-BwC3ESc6acc2lTc", "~!#$%&'()*+,./:;<=>?@[]^_`{|}", strings.Repeat("z", MaxJTIBytes)} {
		if _, err := newVerifier(t, NewMemoryReplayStore(), now).Verify(context.Background(), makeProof(t, k, goodHeader(t, k), goodClaims(jti, now)), "POST"); err != nil {
			t.Errorf("printable jti %q refused: %v", jti, err)
		}
	}
}

// Condition (c): never panics; a refusal is always ErrInvalidProof or a
// replay/store error, never a nil result with a nil error.
func FuzzVerify(f *testing.F) {
	for _, seed := range []string{rfcProofPOST, rfcProofGET, "", "a.b.c", strings.Repeat("A", MaxProofBytes+1)} {
		f.Add(seed, "POST")
	}
	f.Fuzz(func(t *testing.T, proof, method string) {
		v, err := NewVerifier(Config{
			HTU: "https://server.example.com/token", MaxAge: 2 * time.Minute, FutureSkew: time.Minute,
			Replay: NewMemoryReplayStore(), Now: func() time.Time { return time.Unix(1562262616, 0) },
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := v.Verify(context.Background(), proof, method)
		if (got == nil) == (err == nil) {
			t.Fatalf("Verify = (%v, %v): exactly one must be set", got, err)
		}
		if err != nil && !errors.Is(err, ErrInvalidProof) && !errors.Is(err, ErrReplayed) && !errors.Is(err, ErrReplayUnavailable) {
			t.Fatalf("unexpected error class: %v", err)
		}
	})
}
