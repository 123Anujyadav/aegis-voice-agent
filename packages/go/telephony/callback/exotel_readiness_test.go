package callback

import (
	"context"
	"errors"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
)

// Phase 19 P1-7 — Exotel readiness.
//
// No Exotel Verifier or Normalizer exists: the payload and authentication
// contract is not in the repository (docs/telephony/EXOTEL_CONTRACT_CHECKLIST.md).
// These tests pin what that means: every Exotel callback is refused before it
// is parsed, and nothing reaches the lifecycle.
//
// The payload below is SYNTHETIC and deliberately not shaped like any real
// Exotel callback. It must never be read as a documented format.

const exotel = telephony.ProviderID("exotel")

const syntheticExotelBody = "synthetic-event|pc-1|ringing"

// exotelRig is an admission rig whose configuration names no Exotel adapter,
// as the repository's real state does.
func exotelRig(t *testing.T, adapters map[telephony.ProviderID]ProviderAdapter) *admissionRig {
	t.Helper()
	a := newAdmissionRig(t)
	adm, err := NewAdmission(AdmissionConfig{MaxBodyBytes: 256, Adapters: adapters}, a.in)
	if err != nil {
		t.Fatal(err)
	}
	a.adm = adm
	return a
}

func exotelRequest() *Request {
	r := request(syntheticExotelBody, true)
	r.Provider = exotel
	return r
}

func TestExotel_EveryCallbackIsRefusedWithoutAVerifier(t *testing.T) {
	t.Parallel()
	a := exotelRig(t, nil)
	id := a.boundCall(t, "pc-1")

	for name, r := range map[string]*Request{
		"plain":            exotelRequest(),
		"over mutual TLS":  func() *Request { r := exotelRequest(); r.PeerCertificates = [][]byte{[]byte("cert")}; return r }(),
		"with credentials": func() *Request { r := exotelRequest(); r.Header.Set("Authorization", "Basic synthetic"); return r }(),
	} {
		if _, err := a.adm.Admit(context.Background(), r); !errors.Is(err, ErrNoVerifier) {
			t.Errorf("%s: Admit = %v, want %v", name, err, ErrNoVerifier)
		}
	}
	a.untouched(t, id, telephony.StateIncoming)
}

// TestExotel_NormalizationNeverBypassesAuthentication: a normalizer registered
// for Exotel without a verifier is never called.
func TestExotel_NormalizationNeverBypassesAuthentication(t *testing.T) {
	t.Parallel()
	norm := &fakeNormalizer{}
	a := exotelRig(t, map[telephony.ProviderID]ProviderAdapter{exotel: {Normalizer: norm}})
	id := a.boundCall(t, "pc-1")

	if _, err := a.adm.Admit(context.Background(), exotelRequest()); !errors.Is(err, ErrNoVerifier) {
		t.Fatalf("Admit = %v, want %v", err, ErrNoVerifier)
	}
	if n := norm.calls.Load(); n != 0 {
		t.Errorf("an unauthenticated Exotel payload was parsed %d times", n)
	}
	a.untouched(t, id, telephony.StateIncoming)
}

// TestExotel_ProviderIdentityComesFromTheRouteNotThePayload: even with an
// authenticating verifier, a payload claiming to be from another provider is
// refused. Provider identity is the adapter's configuration, not the payload's.
func TestExotel_ProviderIdentityComesFromTheRouteNotThePayload(t *testing.T) {
	t.Parallel()
	v := &fakeVerifier{token: token}
	norm := &fakeNormalizer{provider: "fake-carrier"} // the payload names another carrier
	a := exotelRig(t, map[telephony.ProviderID]ProviderAdapter{exotel: {Verifier: v, Normalizer: norm}})
	id := a.boundCall(t, "pc-1")

	if _, err := a.adm.Admit(context.Background(), exotelRequest()); !errors.Is(err, ErrProviderMismatch) {
		t.Fatalf("Admit = %v, want %v", err, ErrProviderMismatch)
	}
	a.untouched(t, id, telephony.StateIncoming)
}
