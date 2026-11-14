package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/dpop"
)

// Sign in with Google (google_signin.go) over the in-memory repository, with
// a stub token verifier: the order of checks, the M1 gate on every sign-in,
// the atomic creation budget, single use, the nonce binding to the proof's
// key, concurrency, and that nothing identifying is audited or returned.

const googleHTU = "https://identity.example/v1/auth/google"

// stubGoogle is a GoogleTokenVerifier: raw token -> identity, and the nonce it
// must be asked for.
type stubGoogle struct {
	mu          sync.Mutex
	identities  map[string]*GoogleIdentity
	nonces      map[string]string
	unavailable bool
}

func (s *stubGoogle) VerifyGoogleIDToken(_ context.Context, raw, expectedNonce string) (*GoogleIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable {
		return nil, ErrGoogleSignInUnavailable
	}
	id, ok := s.identities[raw]
	if !ok || s.nonces[raw] != expectedNonce {
		return nil, ErrGoogleSignInRefused
	}
	cp := *id
	return &cp, nil
}

type googleRig struct {
	*enrollRig
	audit *recordingSecurityAuditor
	stub  *stubGoogle
	dir   *GoogleAccountDirectory
	n     atomic.Int64
}

func newGoogleRig(t *testing.T, allowlist string, hourlyCap int) *googleRig {
	t.Helper()
	r := newMemEnrollRig(t)
	audit := &recordingSecurityAuditor{}
	r.svc.AddSecurityAuditor(audit)
	proofs, err := dpop.NewVerifier(dpop.Config{
		HTU: googleHTU, MaxAge: time.Minute, FutureSkew: 5 * time.Second, Replay: dpop.NewMemoryReplayStore(), Now: r.clock.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	g := &googleRig{enrollRig: r, audit: audit, stub: &stubGoogle{identities: map[string]*GoogleIdentity{}, nonces: map[string]string{}}}
	g.dir = newTestDirectory(t, r.repo, "k1 "+testKeyHex(0x11))
	list, err := ParseEmailAllowlist([]byte(allowlist))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.svc.SetGoogleSignIn(GoogleSignInConfig{
		Verifier: g.stub, ProofVerifier: proofs, Directory: g.dir, Allowlist: list, HourlyCap: hourlyCap,
	}); err != nil {
		t.Fatal(err)
	}
	return g
}

func newSeed(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// attempt is one sign-in: a fresh token for (sub, email, verified), bound to
// key's nonce, and a fresh proof by key.
type attempt struct {
	raw, seed, proof string
}

func (g *googleRig) mint(t *testing.T, key *ecdsa.PrivateKey, sub, email string, verified bool) attempt {
	t.Helper()
	seed := newSeed(t)
	raw := fmt.Sprintf("token-%d", g.n.Add(1))
	g.stub.mu.Lock()
	g.stub.identities[raw] = &GoogleIdentity{
		Issuer: GoogleIssuer, Subject: sub, Email: email, EmailVerified: verified, ExpiresAt: time.Now().Add(time.Hour),
	}
	g.stub.nonces[raw] = GoogleNonce(thumbprintOf(t, key), seed)
	g.stub.mu.Unlock()
	return attempt{raw: raw, seed: seed, proof: g.proof(t, key)}
}

func (g *googleRig) proof(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	return dpopProof(t, key, key, fmt.Sprintf("jti-%d", g.n.Add(1)), "POST", googleHTU, g.clock.now())
}

func (g *googleRig) signIn(a attempt) (*UserSession, string, error) {
	return g.svc.SignInWithGoogle(context.Background(), a.raw, a.seed, "POST", a.proof, "")
}

func (g *googleRig) creations() int {
	g.repo.mu.Lock()
	defer g.repo.mu.Unlock()
	if g.repo.accts == nil {
		return 0
	}
	total := 0
	for _, n := range g.repo.accts.creations {
		total += n
	}
	return total
}

func (g *googleRig) accountCount() int {
	g.repo.mu.Lock()
	defer g.repo.mu.Unlock()
	if g.repo.accts == nil {
		return 0
	}
	return len(g.repo.accts.accounts)
}

const googleTesters = "# M1 googleTesters\nTester@Example.com\n  second@example.com  \n"

func TestGoogleSignIn_SignsUpThenSignsIn(t *testing.T) {
	g := newGoogleRig(t, googleTesters, 5)
	key := deviceKey(t)
	session, token, err := g.signIn(g.mint(t, key, "sub-1", "tester@example.com", true))
	if err != nil {
		t.Fatalf("sign-up: %v", err)
	}
	claims, err := g.tokens.Verify(token)
	if err != nil || claims.Subject != session.SubscriberID || claims.Confirmation.JKT != thumbprintOf(t, key) {
		t.Fatalf("token: (%+v, %v)", claims, err)
	}
	if !validSubscriberID(session.SubscriberID) || g.accountCount() != 1 || g.creations() != 1 {
		t.Errorf("account %q, %d accounts, %d budget units", session.SubscriberID, g.accountCount(), g.creations())
	}
	// Again, on another device, with the email's case changed: the same
	// account, and no budget.
	again, _, err := g.signIn(g.mint(t, deviceKey(t), "sub-1", "  TESTER@example.COM ", true))
	if err != nil || again.SubscriberID != session.SubscriberID {
		t.Fatalf("sign-in: (%v, %v)", again, err)
	}
	// An email change at Google: the same subject is the same account.
	changed, _, err := g.signIn(g.mint(t, key, "sub-1", "second@example.com", true))
	if err != nil || changed.SubscriberID != session.SubscriberID {
		t.Errorf("after an email change: (%v, %v)", changed, err)
	}
	if g.accountCount() != 1 || g.creations() != 1 {
		t.Errorf("%d accounts and %d budget units: existing accounts spend nothing", g.accountCount(), g.creations())
	}
	types := map[SecurityEventType]int{}
	for _, e := range g.audit.snapshot() {
		types[e.Type]++
	}
	if types[EventAccountCreated] != 1 || types[EventGoogleSignedIn] != 3 || types[EventDeviceEnrolled] != 3 {
		t.Errorf("audit = %v", types)
	}
}

// The gate runs on every sign-in, BEFORE any account is created or any budget
// is spent; Gmail dots and +tags are not normalised away.
func TestGoogleSignIn_TheAllowlistGatesEverySignIn(t *testing.T) {
	g := newGoogleRig(t, googleTesters, 5)
	key := deviceKey(t)
	for name, a := range map[string]attempt{
		"not on the list":     g.mint(t, key, "sub-x", "stranger@example.com", true),
		"a +tag variant":      g.mint(t, key, "sub-x", "tester+x@example.com", true),
		"a dotted variant":    g.mint(t, key, "sub-x", "tes.ter@example.com", true),
		"email not verified":  g.mint(t, key, "sub-x", "tester@example.com", false),
		"no email":            g.mint(t, key, "sub-x", "", true),
		"a non-ASCII address": g.mint(t, key, "sub-x", "tëster@example.com", true),
	} {
		if _, _, err := g.signIn(a); !errors.Is(err, ErrGoogleSignInRefused) {
			t.Errorf("%s: err = %v, want refused", name, err)
		}
	}
	if g.accountCount() != 0 || g.creations() != 0 {
		t.Fatalf("%d accounts and %d budget units after refusals: the gate must come first", g.accountCount(), g.creations())
	}

	// An existing account removed from the list: its next Google sign-in is
	// refused, but its session is not revoked.
	session, _, err := g.signIn(g.mint(t, key, "sub-1", "tester@example.com", true))
	if err != nil {
		t.Fatal(err)
	}
	list, _ := ParseEmailAllowlist([]byte("second@example.com\n")) //nolint:errcheck // constant
	cfg := *g.svc.googleSignIn
	cfg.Allowlist = list
	g.svc.googleSignIn = &cfg
	if _, _, refused := g.signIn(g.mint(t, key, "sub-1", "tester@example.com", true)); !errors.Is(refused, ErrGoogleSignInRefused) {
		t.Errorf("a removed address signed in: %v", refused)
	}
	stored, err := g.repo.GetSession(context.Background(), session.SessionID)
	if err != nil || stored.IsRevoked {
		t.Errorf("the existing session was revoked: (%+v, %v)", stored, err)
	}
}

func TestGoogleSignIn_TheHourlyCapBoundsNewAccountsOnly(t *testing.T) {
	g := newGoogleRig(t, googleTesters, 2)
	key := deviceKey(t)
	first, _, err := g.signIn(g.mint(t, key, "sub-1", "tester@example.com", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, second := g.signIn(g.mint(t, key, "sub-2", "tester@example.com", true)); second != nil {
		t.Fatal(second)
	}
	if _, _, third := g.signIn(g.mint(t, key, "sub-3", "second@example.com", true)); !errors.Is(third, ErrGoogleSignInRefused) {
		t.Errorf("a third new account in the hour: %v, want refused", third)
	}
	again, _, err := g.signIn(g.mint(t, key, "sub-1", "tester@example.com", true))
	if err != nil || again.SubscriberID != first.SubscriberID {
		t.Errorf("an existing account with the hour spent: (%v, %v)", again, err)
	}
	if g.accountCount() != 2 || g.creations() != 2 {
		t.Errorf("%d accounts, %d units", g.accountCount(), g.creations())
	}
}

// Fifty concurrent first sign-ins of one identity: one account, one unit.
func TestGoogleSignIn_ConcurrentFirstSignInsCreateOneAccountAndSpendOneUnit(t *testing.T) {
	g := newGoogleRig(t, googleTesters, 5)
	attempts := make([]attempt, 50)
	for i := range attempts {
		attempts[i] = g.mint(t, deviceKey(t), "sub-1", "tester@example.com", true)
	}
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		ids = map[string]int{}
	)
	start := make(chan struct{})
	for _, a := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s, _, err := g.signIn(a)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("sign-in: %v", err)
				return
			}
			ids[s.SubscriberID]++
		}()
	}
	close(start)
	wg.Wait()
	if len(ids) != 1 || g.accountCount() != 1 || g.creations() != 1 {
		t.Errorf("%d accounts seen, %d stored, %d budget units; want 1, 1 and 1", len(ids), g.accountCount(), g.creations())
	}
}

func TestGoogleSignIn_SingleUseAndKeyBinding(t *testing.T) {
	g := newGoogleRig(t, googleTesters, 5)
	key := deviceKey(t)
	a := g.mint(t, key, "sub-1", "tester@example.com", true)
	if _, _, err := g.signIn(a); err != nil {
		t.Fatal(err)
	}
	replay := attempt{raw: a.raw, seed: a.seed, proof: g.proof(t, key)}
	if _, _, err := g.signIn(replay); !errors.Is(err, ErrGoogleSignInRefused) {
		t.Errorf("a replayed token: %v, want refused", err)
	}
	// A token whose nonce was made for another key cannot be redeemed with
	// this key's proof.
	stolen := g.mint(t, key, "sub-1", "tester@example.com", true)
	stolen.proof = g.proof(t, deviceKey(t))
	if _, _, err := g.signIn(stolen); !errors.Is(err, ErrGoogleSignInRefused) {
		t.Errorf("a token bound to another key: %v, want refused", err)
	}
	// The same key with another seed: refused too.
	wrongSeed := g.mint(t, key, "sub-1", "tester@example.com", true)
	wrongSeed.seed = newSeed(t)
	if _, _, err := g.signIn(wrongSeed); !errors.Is(err, ErrGoogleSignInRefused) {
		t.Errorf("another seed: %v, want refused", err)
	}
}

func TestGoogleSignIn_ProofAndShapeAreCheckedFirst(t *testing.T) {
	g := newGoogleRig(t, googleTesters, 5)
	key := deviceKey(t)
	a := g.mint(t, key, "sub-1", "tester@example.com", true)
	for name, seed := range map[string]string{"short": "abc", "padded": a.seed[:42] + "=", "empty": ""} {
		bad := a
		bad.seed = seed
		if _, _, err := g.signIn(bad); !errors.Is(err, ErrMalformedGoogleSignIn) {
			t.Errorf("%s seed: %v", name, err)
		}
	}
	// The malformed attempts did not spend the proof.
	if _, _, err := g.signIn(a); err != nil {
		t.Errorf("the proof was spent by a malformed request: %v", err)
	}
	bad := g.mint(t, key, "sub-1", "tester@example.com", true)
	bad.proof = "not-a-proof"
	if _, _, err := g.signIn(bad); !errors.Is(err, ErrInvalidDeviceProof) {
		t.Errorf("an invalid proof: %v", err)
	}
}

func TestGoogleSignIn_UnavailableAndUnconfigured(t *testing.T) {
	g := newGoogleRig(t, googleTesters, 5)
	g.stub.mu.Lock()
	g.stub.unavailable = true
	g.stub.mu.Unlock()
	if _, _, err := g.signIn(g.mint(t, deviceKey(t), "sub-1", "tester@example.com", true)); !errors.Is(err, ErrGoogleSignInUnavailable) {
		t.Errorf("keys unavailable: %v", err)
	}
	plain := newMemEnrollRig(t)
	if plain.svc.GoogleSignInEnabled() {
		t.Error("enabled without configuration")
	}
	if _, _, err := plain.svc.SignInWithGoogle(context.Background(), "t", newSeed(t), "POST", "p", ""); !errors.Is(err, ErrGoogleSignInUnavailable) {
		t.Errorf("unconfigured: %v", err)
	}
	for name, cfg := range map[string]GoogleSignInConfig{
		"no verifier": {ProofVerifier: &dpop.Verifier{}, Directory: g.dir, Allowlist: &EmailAllowlist{}, HourlyCap: 5},
		"cap zero":    {Verifier: g.stub, ProofVerifier: &dpop.Verifier{}, Directory: g.dir, Allowlist: &EmailAllowlist{}, HourlyCap: 0},
		"cap too big": {Verifier: g.stub, ProofVerifier: &dpop.Verifier{}, Directory: g.dir, Allowlist: &EmailAllowlist{}, HourlyCap: MaxAccountCreationHourlyCap + 1},
	} {
		if err := plain.svc.SetGoogleSignIn(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Nothing identifying reaches the audit log or an error.
func TestGoogleSignIn_NothingIdentifyingIsAuditedOrReturned(t *testing.T) {
	g := newGoogleRig(t, googleTesters, 1)
	key := deviceKey(t)
	ok := g.mint(t, key, "sub-secret-1", "tester@example.com", true)
	if _, _, err := g.signIn(ok); err != nil {
		t.Fatal(err)
	}
	var errs []error
	for _, a := range []attempt{
		g.mint(t, key, "sub-secret-2", "stranger@example.com", true),
		g.mint(t, key, "sub-secret-3", "second@example.com", true), // cap spent
		{raw: ok.raw, seed: ok.seed, proof: g.proof(t, key)},       // replay
	} {
		_, _, err := g.signIn(a)
		errs = append(errs, err)
	}
	forbidden := []string{"sub-secret", "tester@", "stranger@", "second@", ok.raw, ok.seed, GoogleNonce(thumbprintOf(t, key), ok.seed)}
	for _, err := range errs {
		if !errors.Is(err, ErrGoogleSignInRefused) || err.Error() != ErrGoogleSignInRefused.Error() {
			t.Errorf("refusal %v is not the one fixed error", err)
		}
	}
	for _, e := range g.audit.snapshot() {
		text := fmt.Sprintf("%+v", e)
		for _, f := range forbidden {
			if strings.Contains(text, f) {
				t.Errorf("audit event %s carries %q", e.Type, f)
			}
		}
		if e.RequestID != "" {
			t.Errorf("audit event %s carries a request id", e.Type)
		}
	}
}
