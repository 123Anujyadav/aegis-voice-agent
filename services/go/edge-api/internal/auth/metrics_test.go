package auth

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/callscreen/callscreen-platform/packages/go/middleware"
)

// Every decision Middleware makes is counted under its outcome, refusals
// included: a 401 used to leave no trace at all (ADR-0029 §6).
func TestMetrics_EveryDecisionIsCountedByOutcome(t *testing.T) {
	r := newRig(t)
	tok := r.token(t)
	replayed := r.goodProof(t, "GET", tok)

	r.do(t, "GET", path, nil)                                   // no Authorization header
	r.do(t, "GET", path, auth(tok, ""))                         // an empty proof
	r.do(t, "GET", path, auth("x.y.z", "proof"))                // a token that does not verify
	r.do(t, "GET", path, auth(tok, r.goodProof(t, "GET", tok))) // admitted
	r.do(t, "GET", path, auth(tok, replayed))                   // admitted: first use of this proof
	r.do(t, "GET", path, auth(tok, replayed))                   // the same proof again: a replay

	want := map[string]uint64{
		OutcomeAdmitted:        2,
		OutcomeUnauthenticated: 1,
		OutcomeInvalidToken:    1,
		OutcomeInvalidProof:    2,
		OutcomeUnavailable:     0,
	}
	for outcome, n := range want {
		if got := r.a.outcomes.Value(outcome); got != n {
			t.Errorf("%s = %d, want %d", outcome, got, n)
		}
	}
	if got := r.reached.Load(); got != 2 {
		t.Errorf("the protected handler ran %d times; the admitted count says 2", got)
	}
	if total := r.a.outcomes.Total(); total != 6 {
		t.Errorf("total = %d, want one count per request (6)", total)
	}
}

// A replay store that cannot answer is counted as unavailable.
func TestMetrics_StoreFailureIsCountedAsUnavailable(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	device, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	c := &clock{t: now0}
	a, err := New(validConfig(t, pub), failingReplay{}, c.now, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{device: device, clock: c, logs: &syncBuf{}, replay: dpop.NewMemoryReplayStore()}
	r.signer = signerFor(t, priv, kid, issuer, audience, c)
	r.h = middleware.CorrelationIDMiddleware(a.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
	tok := r.token(t)
	r.do(t, "GET", path, auth(tok, r.goodProof(t, "GET", tok)))
	if got := a.outcomes.Value(OutcomeUnavailable); got != 1 || a.outcomes.Total() != 1 {
		t.Fatalf("unavailable = %d of %d, want 1 of 1", got, a.outcomes.Total())
	}
}

// The label vocabulary is closed and carries nothing from the request: after
// hostile input, every series is one of the documented outcomes.
func TestMetrics_LabelVocabularyIsClosed(t *testing.T) {
	r := newRig(t)
	for _, hdr := range []map[string][]string{
		nil,
		{"Authorization": {"Bearer usr_attacker"}},
		{"Authorization": {"DPoP " + strings.Repeat("A", 64)}, "DPoP": {"msisdn=+911234567890"}},
		{"Authorization": {"DPoP a", "DPoP b"}},
	} {
		r.do(t, "GET", path+"?sid=sess_leak", hdr)
	}
	closed := []string{OutcomeAdmitted, OutcomeUnauthenticated, OutcomeInvalidToken, OutcomeInvalidProof, OutcomeUnavailable}
	for _, s := range r.a.outcomes.Series() {
		if !slices.Contains(closed, s) {
			t.Errorf("series %q is not a documented outcome", s)
		}
	}
	if r.a.outcomes.Total() != 4 {
		t.Errorf("total = %d, want 4", r.a.outcomes.Total())
	}
}

// Every problem code refuse is given maps to its own outcome; an unknown code
// fails closed to unavailable.
func TestMetrics_OutcomeForCode(t *testing.T) {
	for code, want := range map[string]string{
		"ERR_UNAUTHENTICATED":         OutcomeUnauthenticated,
		"ERR_INVALID_TOKEN":           OutcomeInvalidToken,
		"ERR_INVALID_DPOP_PROOF":      OutcomeInvalidProof,
		"ERR_TEMPORARILY_UNAVAILABLE": OutcomeUnavailable,
		"ERR_SOMETHING_NEW":           OutcomeUnavailable,
	} {
		if got := outcomeForCode(code); got != want {
			t.Errorf("%s -> %s, want %s", code, got, want)
		}
	}
	// The table of problem texts and the outcome mapping must not drift apart.
	for code := range problems {
		if code != "ERR_TEMPORARILY_UNAVAILABLE" && outcomeForCode(code) == OutcomeUnavailable {
			t.Errorf("problem code %s has no outcome of its own", code)
		}
	}
}
