package callback

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
)

// Phase 19 P1-6 — callback admission.
//
// The verifier and normalizer here are TEST FAKES. The "X-Test-Auth" header is
// not a real scheme and is not offered as one; it exists only so the gate's
// fail-closed behaviour can be exercised deterministically.

const testSecretHeader = "X-Test-Auth"

// fakeVerifier accepts a request whose test header equals token, unless a
// verdict override is set.
type fakeVerifier struct {
	token    string
	override *Authentication
	err      error
	failNext atomic.Int64
	calls    atomic.Int64
}

func (v *fakeVerifier) Verify(_ context.Context, r *Request) (Authentication, error) {
	v.calls.Add(1)
	if v.failNext.Load() > 0 {
		v.failNext.Add(-1)
		return Authentication{}, errors.New("verifier unavailable; header was " + r.Header.Get(testSecretHeader))
	}
	if v.err != nil {
		return Authentication{}, v.err
	}
	if v.override != nil {
		return *v.override, nil
	}
	if r.Header.Get(testSecretHeader) != v.token {
		return Authentication{Provider: r.Provider}, nil // Verified: false
	}
	return Authentication{Provider: r.Provider, Verified: true, Mechanism: "test-header-v0"}, nil
}

// fakeNormalizer parses "eventID|providerCallID|kind".
type fakeNormalizer struct {
	provider telephony.ProviderID // when set, overrides the event's provider
	calls    atomic.Int64
}

func (n *fakeNormalizer) Normalize(_ context.Context, r *Request) (Event, error) {
	n.calls.Add(1)
	parts := strings.Split(string(r.Body), "|")
	if len(parts) != 3 {
		return Event{}, errors.New("unparseable test payload: " + string(r.Body))
	}
	p := r.Provider
	if n.provider != "" {
		p = n.provider
	}
	return Event{
		Provider: p, EventID: parts[0], ProviderCallID: parts[1], Kind: Kind(parts[2]),
		ReceivedAt: r.ReceivedAt,
	}, nil
}

type admissionRig struct {
	*rig
	bindings *Bindings
	verifier *fakeVerifier
	norm     *fakeNormalizer
	adm      *Admission
}

const token = "s3cr3t-test-token-value"

func newAdmissionRig(t *testing.T) *admissionRig {
	t.Helper()
	base := newRig(t, 64)
	bindings, err := NewBindings(RegistryProviders(base.h.Runtime.Registry()), 64)
	if err != nil {
		t.Fatal(err)
	}
	in, err := NewIngestor(base.store, bindings, base.life)
	if err != nil {
		t.Fatal(err)
	}
	base.in = in
	a := &admissionRig{rig: base, bindings: bindings, verifier: &fakeVerifier{token: token}, norm: &fakeNormalizer{}}
	a.adm, err = NewAdmission(AdmissionConfig{
		MaxBodyBytes: 256,
		Adapters:     map[telephony.ProviderID]ProviderAdapter{provider: {Verifier: a.verifier, Normalizer: a.norm}},
	}, in)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// boundCall admits an inbound call and binds it to providerCallID.
func (a *admissionRig) boundCall(t *testing.T, providerCallID string) telephony.CallID {
	t.Helper()
	sess, err := a.h.BeginInbound(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.bindings.Bind(Binding{Provider: provider, ProviderCallID: providerCallID, Call: sess.ID()}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	return sess.ID()
}

func request(body string, signed bool) *Request {
	r := &Request{
		Provider: provider, Method: "POST", URL: "https://callbacks.invalid/fake-carrier",
		Header: map[string][]string{}, Body: []byte(body), ReceivedAt: received,
	}
	if signed {
		r.Header.Set(testSecretHeader, token)
	}
	return r
}

// untouched asserts that nothing past the gate happened: no lifecycle
// operation, no reserved or settled key, and the call still where it was.
func (a *admissionRig) untouched(t *testing.T, id telephony.CallID, want telephony.CallState) {
	t.Helper()
	if n := a.life.ops.Load(); n != 0 {
		t.Errorf("%d lifecycle operations ran", n)
	}
	if remembered, _, _ := a.store.Stats(); remembered != 0 {
		t.Errorf("%d event keys remembered; a refused callback must reserve nothing", remembered)
	}
	if id != "" {
		if s := a.state(t, id); s != want {
			t.Errorf("state = %s, want %s", s, want)
		}
	}
}

func assertNoSecret(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), token) {
		t.Errorf("error discloses the credential: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Authentication
// ---------------------------------------------------------------------------

func TestAdmit_UnauthenticatedCallbackNeverReachesTheLifecycle(t *testing.T) {
	t.Parallel()
	a := newAdmissionRig(t)
	id := a.boundCall(t, "pc-1") // a KNOWN call ID: that is not authentication

	for _, tc := range []struct {
		name string
		req  *Request
	}{
		{"unsigned", request("e-1|pc-1|ringing", false)},
		{"wrong credential", func() *Request {
			r := request("e-1|pc-1|ringing", false)
			r.Header.Set(testSecretHeader, "guess")
			return r
		}()},
		{"mutual TLS alone", func() *Request {
			r := request("e-1|pc-1|ringing", false)
			r.PeerCertificates = [][]byte{[]byte("a verified client certificate")}
			return r
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := a.adm.Admit(context.Background(), tc.req)
			if !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("Admit = %v, want %v", err, ErrUnauthenticated)
			}
			assertNoSecret(t, err)
		})
	}
	if n := a.norm.calls.Load(); n != 0 {
		t.Errorf("the payload of an unauthenticated callback was parsed %d times", n)
	}
	a.untouched(t, id, telephony.StateIncoming)
}

func TestAdmit_InvalidAuthenticationResultsAreRefused(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		verdict *Authentication
		err     error
	}{
		{"verifier error", nil, errors.New("signature mismatch over " + token)},
		{"not verified", &Authentication{Provider: provider, Mechanism: "x"}, nil},
		{"verified as another provider", &Authentication{Provider: "other-carrier", Verified: true, Mechanism: "x"}, nil},
		{"verified with no provider", &Authentication{Verified: true, Mechanism: "x"}, nil},
		{"verified with no mechanism", &Authentication{Provider: provider, Verified: true}, nil},
		{"zero verdict", &Authentication{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newAdmissionRig(t)
			id := a.boundCall(t, "pc-1")
			a.verifier.override, a.verifier.err = tc.verdict, tc.err

			_, err := a.adm.Admit(context.Background(), request("e-1|pc-1|ringing", true))
			if !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("Admit = %v, want %v", err, ErrUnauthenticated)
			}
			assertNoSecret(t, err)
			a.untouched(t, id, telephony.StateIncoming)
		})
	}
}

func TestAdmit_ProviderWithoutAVerifierIsRefused(t *testing.T) {
	t.Parallel()
	a := newAdmissionRig(t)
	id := a.boundCall(t, "pc-1")

	r := request("e-1|pc-1|ringing", true)
	r.Provider = "unregistered-carrier"
	if _, err := a.adm.Admit(context.Background(), r); !errors.Is(err, ErrNoVerifier) {
		t.Fatalf("Admit = %v, want %v", err, ErrNoVerifier)
	}

	// Registered with a normalizer but no verifier: still refused.
	adm, err := NewAdmission(AdmissionConfig{
		MaxBodyBytes: 256,
		Adapters:     map[telephony.ProviderID]ProviderAdapter{provider: {Normalizer: a.norm}},
	}, a.in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adm.Admit(context.Background(), request("e-1|pc-1|ringing", true)); !errors.Is(err, ErrNoVerifier) {
		t.Fatalf("Admit = %v, want %v", err, ErrNoVerifier)
	}
	a.untouched(t, id, telephony.StateIncoming)
}

func TestAdmit_AuthenticatedCallbackIsApplied(t *testing.T) {
	t.Parallel()
	a := newAdmissionRig(t)
	id := a.boundCall(t, "pc-1")

	got, err := a.adm.Admit(context.Background(), request("e-1|pc-1|ringing", true))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if got.Outcome != OutcomeApplied || got.Call != id || got.To != telephony.StateRinging {
		t.Errorf("Result = %+v, want applied to %s", got, id)
	}
	if s := a.state(t, id); s != telephony.StateRinging {
		t.Errorf("state = %s, want ringing", s)
	}
}

// ---------------------------------------------------------------------------
// Malformed requests and events
// ---------------------------------------------------------------------------

func TestAdmit_MalformedRequestsAndEventsAreRefused(t *testing.T) {
	t.Parallel()
	a := newAdmissionRig(t)
	id := a.boundCall(t, "pc-1")
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		req  *Request
		want error
	}{
		{"nil request", nil, ErrMalformedRequest},
		{"no provider", func() *Request { r := request("e-1|pc-1|ringing", true); r.Provider = ""; return r }(), ErrMalformedRequest},
		{"invalid provider", func() *Request { r := request("e-1|pc-1|ringing", true); r.Provider = "Bad Carrier"; return r }(), ErrMalformedRequest},
		{"empty body", request("", true), ErrMalformedRequest},
		{"oversized body", request(strings.Repeat("x", 257), true), ErrMalformedRequest},
		{"unparseable payload", request("garbage", true), ErrInvalidEvent},
		{"empty event ID", request("|pc-1|ringing", true), ErrInvalidEvent},
		{"empty carrier call ID", request("e-1||ringing", true), ErrInvalidEvent},
		{"malformed carrier call ID", request("e-1|pc 1|ringing", true), ErrInvalidEvent},
		{"unknown kind", request("e-1|pc-1|picked_up", true), ErrInvalidEvent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := a.adm.Admit(ctx, tc.req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Admit = %v, want %v", err, tc.want)
			}
			if strings.Contains(fmt.Sprint(err), "garbage") {
				t.Errorf("error echoes the payload: %v", err)
			}
		})
	}
	a.untouched(t, id, telephony.StateIncoming)
}

func TestAdmit_EventNamingAnotherProviderIsRefused(t *testing.T) {
	t.Parallel()
	a := newAdmissionRig(t)
	id := a.boundCall(t, "pc-1")
	a.norm.provider = "other-carrier" // the payload claims a different carrier

	if _, err := a.adm.Admit(context.Background(), request("e-1|pc-1|ringing", true)); !errors.Is(err, ErrProviderMismatch) {
		t.Fatalf("Admit = %v, want %v", err, ErrProviderMismatch)
	}
	a.untouched(t, id, telephony.StateIncoming)
}

func TestAdmit_ProviderWithoutANormalizerIsRefused(t *testing.T) {
	t.Parallel()
	a := newAdmissionRig(t)
	id := a.boundCall(t, "pc-1")
	adm, err := NewAdmission(AdmissionConfig{
		MaxBodyBytes: 256,
		Adapters:     map[telephony.ProviderID]ProviderAdapter{provider: {Verifier: a.verifier}},
	}, a.in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adm.Admit(context.Background(), request("e-1|pc-1|ringing", true)); !errors.Is(err, ErrNoNormalizer) {
		t.Fatalf("Admit = %v, want %v", err, ErrNoNormalizer)
	}
	a.untouched(t, id, telephony.StateIncoming)
}

// ---------------------------------------------------------------------------
// Resolution, retries and idempotency
// ---------------------------------------------------------------------------

func TestAdmit_UnknownCarrierCallReservesNothingAndRetriesCleanly(t *testing.T) {
	t.Parallel()
	a := newAdmissionRig(t)
	sess, err := a.h.BeginInbound(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Authenticated, but the carrier call is not bound (yet).
	if _, err := a.adm.Admit(context.Background(), request("e-1|pc-1|ringing", true)); !errors.Is(err, ErrUnknownCall) {
		t.Fatalf("Admit = %v, want %v", err, ErrUnknownCall)
	}
	a.untouched(t, sess.ID(), telephony.StateIncoming)

	if err := a.bindings.Bind(Binding{Provider: provider, ProviderCallID: "pc-1", Call: sess.ID()}); err != nil {
		t.Fatal(err)
	}
	got, err := a.adm.Admit(context.Background(), request("e-1|pc-1|ringing", true))
	if err != nil || got.Outcome != OutcomeApplied {
		t.Errorf("after binding: %+v, %v; want applied", got, err)
	}
}

func TestAdmit_FailedAuthenticationThenValidRetryIsApplied(t *testing.T) {
	t.Parallel()
	a := newAdmissionRig(t)
	id := a.boundCall(t, "pc-1")
	a.verifier.failNext.Store(1)

	_, err := a.adm.Admit(context.Background(), request("e-1|pc-1|ringing", true))
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("first attempt = %v, want %v", err, ErrUnauthenticated)
	}
	assertNoSecret(t, err)
	a.untouched(t, id, telephony.StateIncoming)

	got, err := a.adm.Admit(context.Background(), request("e-1|pc-1|ringing", true))
	if err != nil || got.Outcome != OutcomeApplied {
		t.Fatalf("retry = %+v, %v; want applied", got, err)
	}
}

func TestAdmit_RedeliveryKeepsIngestorIdempotency(t *testing.T) {
	t.Parallel()
	a := newAdmissionRig(t)
	id := a.boundCall(t, "pc-1")

	first, err := a.adm.Admit(context.Background(), request("e-1|pc-1|ringing", true))
	if err != nil || first.Outcome != OutcomeApplied {
		t.Fatalf("first = %+v, %v", first, err)
	}
	for i := 0; i < 3; i++ {
		again, err := a.adm.Admit(context.Background(), request("e-1|pc-1|ringing", true))
		if err != nil || again.Outcome != OutcomeDuplicate || again.Prior != OutcomeApplied {
			t.Errorf("redelivery %d = %+v, %v; want duplicate", i+1, again, err)
		}
	}
	if n := a.life.ops.Load(); n != 1 {
		t.Errorf("lifecycle ran %d times, want 1", n)
	}
	if s := a.state(t, id); s != telephony.StateRinging {
		t.Errorf("state = %s", s)
	}
	// Every redelivery was authenticated again: idempotency never replaces it.
	if n := a.verifier.calls.Load(); n != 4 {
		t.Errorf("verifier ran %d times, want 4", n)
	}
}

func TestNewAdmission_RefusesBadConfiguration(t *testing.T) {
	t.Parallel()
	a := newAdmissionRig(t)
	for name, tc := range map[string]struct {
		cfg AdmissionConfig
		in  *Ingestor
	}{
		"zero body bound":  {AdmissionConfig{}, a.in},
		"no ingestor":      {AdmissionConfig{MaxBodyBytes: 1}, nil},
		"invalid provider": {AdmissionConfig{MaxBodyBytes: 1, Adapters: map[telephony.ProviderID]ProviderAdapter{"Bad Carrier": {}}}, a.in},
	} {
		if _, err := NewAdmission(tc.cfg, tc.in); err == nil {
			t.Errorf("%s: NewAdmission succeeded", name)
		}
	}
}
