package dpop

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
)

// RFC 8725 §3.12: different kinds of JWT must have mutually exclusive
// validation rules, so one can never be accepted as the other. The platform
// has two: access tokens (typ at+jwt, EdDSA, issuer key) and DPoP proofs (typ
// dpop+jwt, ES256, the device's own embedded key). These tests present a
// GENUINE token of each kind to the other's verifier.

func TestTypeConfusion_AnAccessTokenIsNeverADPoPProof(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	k := newDeviceKey(t)
	jkt, _ := accesstoken.ThumbprintP256(&k.PublicKey)
	signer, err := accesstoken.NewSigner(accesstoken.SignerConfig{
		Issuer: "https://identity.example", Audience: "edge", KeyID: "k1", Key: priv,
		Now: func() time.Time { return t0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := signer.Issue("usr_1", []string{"subscriber"}, jkt)
	if err != nil {
		t.Fatal(err)
	}
	requireInvalid(t, newVerifier(t, NewMemoryReplayStore(), t0), token, "POST", "an access token as an enrolment/refresh proof")
	got, err := newRequestVerifier(t, NewMemoryReplayStore(), t0).Verify(context.Background(), token, "GET", testHTU, token, jkt)
	if !errors.Is(err, ErrInvalidProof) || got != nil {
		t.Fatalf("an access token as a request proof: (%v, %v), want ErrInvalidProof", got, err)
	}
}

func TestTypeConfusion_ADPoPProofIsNeverAnAccessToken(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	v, err := accesstoken.NewVerifier(accesstoken.VerifierConfig{
		Issuer: "https://identity.example", Audience: "edge",
		Keys: map[string]ed25519.PublicKey{"k1": pub}, Now: func() time.Time { return t0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	k := newDeviceKey(t)
	proof := makeProof(t, k, goodHeader(t, k), goodClaims("jti-x", t0))
	if c, err := v.Verify(proof); !errors.Is(err, accesstoken.ErrInvalidToken) || c != nil {
		t.Fatalf("a DPoP proof as an access token: (%+v, %v), want ErrInvalidToken", c, err)
	}
	// Even a proof that claims to be an access token, with the access token's
	// typ and a kid, is refused: the algorithm and the key are not the issuer's.
	forged := makeProof(t, k, map[string]any{"typ": "at+jwt", "alg": "ES256", "kid": "k1"}, goodClaims("jti-y", t0))
	if c, err := v.Verify(forged); !errors.Is(err, accesstoken.ErrInvalidToken) || c != nil {
		t.Fatalf("a device-signed token typed at+jwt: (%+v, %v), want ErrInvalidToken", c, err)
	}
}

// A proof typed as an access token is not a proof.
func TestTypeConfusion_AProofMustBeTypedDpopJWT(t *testing.T) {
	k := newDeviceKey(t)
	h := goodHeader(t, k)
	h["typ"] = "at+jwt"
	requireInvalid(t, newVerifier(t, NewMemoryReplayStore(), t0), makeProof(t, k, h, goodClaims("jti-z", t0)), "POST", "a proof typed at+jwt")
}
