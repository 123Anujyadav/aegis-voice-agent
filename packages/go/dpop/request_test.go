package dpop

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
)

// The resource-request profile: ath required and bound to the token, htu per
// request, and the proof key bound to the token's cnf.jkt before the jti is
// reserved.

const reqHTU = "https://edge.example.test/v1/calls"

// reqToken is an opaque stand-in for an access token: three base64url segments
// of {"alg":"EdDSA"}, {"sub":"usr_1"} and "signature". It is assembled here
// rather than written as one token-shaped literal, which secret scanners
// (correctly) treat as a possible credential.
var reqToken = b64.EncodeToString([]byte(`{"alg":"EdDSA"}`)) + "." +
	b64.EncodeToString([]byte(`{"sub":"usr_1"}`)) + "." + b64.EncodeToString([]byte("signature"))

func reqClaims(jti string, iat time.Time, ath any) map[string]any {
	c := map[string]any{"jti": jti, "htm": "GET", "htu": reqHTU, "iat": iat.Unix()}
	if ath != nil {
		c["ath"] = ath
	}
	return c
}

func newRequestVerifier(t *testing.T, store ReplayStore, now time.Time) *RequestVerifier {
	t.Helper()
	v, err := NewRequestVerifier(RequestConfig{MaxAge: time.Minute, FutureSkew: 5 * time.Second, Replay: store, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRequestProof_AccessTokenHashIsRFC9449(t *testing.T) {
	sum := sha256.Sum256([]byte(reqToken))
	if got, want := AccessTokenHash(reqToken), base64.RawURLEncoding.EncodeToString(sum[:]); got != want || strings.ContainsAny(got, "=+/") {
		t.Errorf("ath = %q, want %q (unpadded base64url)", got, want)
	}
	// RFC 9449 §7.1 example: token "Kz~8mXK1EalYznwH-LC-1fBAo.4Ljp~zsPE_NeO.gxU"
	// has ath "fUHyO2r2Z3DZ53EsNrWBb0xWXoaNy59IiKCAqksmQEo".
	if got := AccessTokenHash("Kz~8mXK1EalYznwH-LC-1fBAo.4Ljp~zsPE_NeO.gxU"); got != "fUHyO2r2Z3DZ53EsNrWBb0xWXoaNy59IiKCAqksmQEo" {
		t.Errorf("RFC 9449 example ath = %q", got)
	}
}

func TestRequestProof_ValidAndBound(t *testing.T) {
	k := newDeviceKey(t)
	jkt, _ := accesstoken.ThumbprintP256(&k.PublicKey)
	store := NewMemoryReplayStore()
	v := newRequestVerifier(t, store, t0)
	p := makeProof(t, k, goodHeader(t, k), reqClaims("r1", t0, AccessTokenHash(reqToken)))
	got, err := v.Verify(context.Background(), p, "GET", reqHTU, reqToken, jkt)
	if err != nil || got.JKT != jkt || got.JTI != "r1" {
		t.Fatalf("(%+v, %v)", got, err)
	}
	if _, err := v.Verify(context.Background(), p, "GET", reqHTU, reqToken, jkt); !errors.Is(err, ErrReplayed) {
		t.Errorf("second use: %v, want ErrReplayed", err)
	}
}

func TestRequestProof_Refusals(t *testing.T) {
	k, other := newDeviceKey(t), newDeviceKey(t)
	jkt, _ := accesstoken.ThumbprintP256(&k.PublicKey)
	ath := AccessTokenHash(reqToken)
	for name, tc := range map[string]struct {
		proof                     func(t *testing.T) string
		method, htu, token, bound string
	}{
		"missing ath": {func(t *testing.T) string { return makeProof(t, k, goodHeader(t, k), reqClaims("x", t0, nil)) }, "GET", reqHTU, reqToken, jkt},
		"ath of another token": {func(t *testing.T) string {
			return makeProof(t, k, goodHeader(t, k), reqClaims("x", t0, AccessTokenHash(reqToken+"x")))
		}, "GET", reqHTU, reqToken, jkt},
		"ath padded":           {func(t *testing.T) string { return makeProof(t, k, goodHeader(t, k), reqClaims("x", t0, ath+"=")) }, "GET", reqHTU, reqToken, jkt},
		"ath not a string":     {func(t *testing.T) string { return makeProof(t, k, goodHeader(t, k), reqClaims("x", t0, 42)) }, "GET", reqHTU, reqToken, jkt},
		"wrong method":         {func(t *testing.T) string { return makeProof(t, k, goodHeader(t, k), reqClaims("x", t0, ath)) }, "POST", reqHTU, reqToken, jkt},
		"wrong URL":            {func(t *testing.T) string { return makeProof(t, k, goodHeader(t, k), reqClaims("x", t0, ath)) }, "GET", reqHTU + "/x", reqToken, jkt},
		"another device's key": {func(t *testing.T) string { return makeProof(t, other, goodHeader(t, other), reqClaims("x", t0, ath)) }, "GET", reqHTU, reqToken, jkt},
		"stale": {func(t *testing.T) string {
			return makeProof(t, k, goodHeader(t, k), reqClaims("x", t0.Add(-2*time.Minute), ath))
		}, "GET", reqHTU, reqToken, jkt},
		"future": {func(t *testing.T) string {
			return makeProof(t, k, goodHeader(t, k), reqClaims("x", t0.Add(time.Minute), ath))
		}, "GET", reqHTU, reqToken, jkt},
		"empty token":   {func(t *testing.T) string { return makeProof(t, k, goodHeader(t, k), reqClaims("x", t0, ath)) }, "GET", reqHTU, "", jkt},
		"empty binding": {func(t *testing.T) string { return makeProof(t, k, goodHeader(t, k), reqClaims("x", t0, ath)) }, "GET", reqHTU, reqToken, ""},
		"empty URL":     {func(t *testing.T) string { return makeProof(t, k, goodHeader(t, k), reqClaims("x", t0, ath)) }, "GET", "", reqToken, jkt},
		"nonce": {func(t *testing.T) string {
			c := reqClaims("x", t0, ath)
			c["nonce"] = "n"
			return makeProof(t, k, goodHeader(t, k), c)
		}, "GET", reqHTU, reqToken, jkt},
	} {
		t.Run(name, func(t *testing.T) {
			store := NewMemoryReplayStore()
			v := newRequestVerifier(t, store, t0)
			got, err := v.Verify(context.Background(), tc.proof(t), tc.method, tc.htu, tc.token, tc.bound)
			if !errors.Is(err, ErrInvalidProof) || got != nil {
				t.Fatalf("(%v, %v), want ErrInvalidProof", got, err)
			}
			if store.Len() != 0 {
				t.Error("a refused proof reserved its jti")
			}
		})
	}
}

// The enrolment profile still refuses ath; the request profile still refuses a
// proof with no ath. Neither profile's proof passes the other.
func TestRequestProof_ProfilesDoNotCross(t *testing.T) {
	k := newDeviceKey(t)
	jkt, _ := accesstoken.ThumbprintP256(&k.PublicKey)
	enrol := newVerifier(t, NewMemoryReplayStore(), t0)
	withATH := goodClaims("a1", t0)
	withATH["ath"] = AccessTokenHash(reqToken)
	requireInvalid(t, enrol, makeProof(t, k, goodHeader(t, k), withATH), "POST", "an enrolment proof carrying ath")

	req := newRequestVerifier(t, NewMemoryReplayStore(), t0)
	plain := goodClaims("a2", t0) // a valid enrolment proof
	if _, err := req.Verify(context.Background(), makeProof(t, k, goodHeader(t, k), plain), "POST", testHTU, reqToken, jkt); !errors.Is(err, ErrInvalidProof) {
		t.Errorf("an enrolment proof accepted on a resource request: %v", err)
	}
}

func TestRequestProof_StoreFailureFailsClosed(t *testing.T) {
	k := newDeviceKey(t)
	jkt, _ := accesstoken.ThumbprintP256(&k.PublicKey)
	v := newRequestVerifier(t, failingStore{}, t0)
	_, err := v.Verify(context.Background(), makeProof(t, k, goodHeader(t, k), reqClaims("f", t0, AccessTokenHash(reqToken))), "GET", reqHTU, reqToken, jkt)
	if !errors.Is(err, ErrReplayUnavailable) || errors.Is(err, ErrInvalidProof) {
		t.Errorf("err = %v, want ErrReplayUnavailable", err)
	}
}

func TestRequestProof_ConfigFailsClosed(t *testing.T) {
	for name, c := range map[string]RequestConfig{
		"no store":     {MaxAge: time.Minute},
		"zero age":     {Replay: NewMemoryReplayStore()},
		"long age":     {MaxAge: 6 * time.Minute, Replay: NewMemoryReplayStore()},
		"long skew":    {MaxAge: time.Minute, FutureSkew: 2 * time.Minute, Replay: NewMemoryReplayStore()},
		"negative age": {MaxAge: -time.Second, Replay: NewMemoryReplayStore()},
	} {
		if _, err := NewRequestVerifier(c); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
