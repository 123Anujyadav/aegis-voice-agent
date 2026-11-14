package app

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/dpop"
)

// Sign-out (session_signout.go): logout and revoke-all. The proof is checked
// before anything can change; a revoke-all retry never reaches a session
// signed in since; another account is never touched.

// Test-only canonical URLs, like refreshHTU.
const (
	logoutHTU    = "https://identity.local.test/v1/session/logout"
	revokeAllHTU = "https://identity.local.test/v1/session/revoke-all"
)

type signOutRig struct {
	*enrollRig
	audit *recordingSecurityAuditor
	n     int
}

// newSignOutRig configures refresh and both sign-out endpoints, sharing one
// replay store as production does.
func newSignOutRig(t *testing.T) *signOutRig {
	t.Helper()
	m := NewMemoryIdentityRepository()
	return newSignOutRigOn(t, m, m)
}

// newSignOutRigOn is newSignOutRig over repo, which wraps mem.
func newSignOutRigOn(t *testing.T, repo IdentityRepository, mem *MemoryIdentityRepository) *signOutRig {
	t.Helper()
	store := dpop.NewMemoryReplayStore()
	r := newEnrollRig(t, repo, mem, dpop.NewMemoryReplayStore()).withRefresh(t, store)
	for htu, set := range map[string]func(*dpop.Verifier){
		logoutHTU: r.svc.SetLogoutProofVerifier, revokeAllHTU: r.svc.SetRevokeAllProofVerifier,
	} {
		v, err := dpop.NewVerifier(dpop.Config{
			HTU: htu, MaxAge: time.Minute, FutureSkew: 5 * time.Second, Replay: store, Now: r.clock.now,
		})
		if err != nil {
			t.Fatal(err)
		}
		set(v)
	}
	g := &signOutRig{enrollRig: r, audit: &recordingSecurityAuditor{}}
	r.svc.AddSecurityAuditor(g.audit)
	return g
}

// proof is a fresh proof by k for htu.
func (g *signOutRig) proof(t *testing.T, k *ecdsa.PrivateKey, htu string) string {
	t.Helper()
	g.n++
	return dpopProof(t, k, k, fmt.Sprintf("so-%d", g.n), "POST", htu, g.clock.now())
}

type signOutEndpoint struct {
	name, htu, otherHTU string
	call                func(*IdentityService, context.Context, string, string, string, string) error
}

var signOutEndpoints = []signOutEndpoint{
	{"logout", logoutHTU, revokeAllHTU, (*IdentityService).LogoutWithDeviceProof},
	{"revoke-all", revokeAllHTU, logoutHTU, (*IdentityService).RevokeAllSessionsWithDeviceProof},
}

func (g *signOutRig) revoked(ctx context.Context, t *testing.T, sessionID string) bool {
	t.Helper()
	s, err := g.repo.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("session %s: %v", sessionID, err)
	}
	return s.IsRevoked
}

// signOutEvents returns the sign-out and reuse events, in order.
func (g *signOutRig) signOutEvents() []SecurityEvent {
	var out []SecurityEvent
	for _, e := range g.audit.snapshot() {
		switch e.Type {
		case EventSessionLoggedOut, EventSessionsRevokedAll, EventRefreshTokenReused:
			out = append(out, e)
		}
	}
	return out
}

// rotate refreshes s once, so its old refresh token is superseded.
func (g *signOutRig) rotate(ctx context.Context, t *testing.T, s *UserSession, k *ecdsa.PrivateKey) string {
	t.Helper()
	_, next, err := g.svc.RefreshTokenWithDeviceProof(ctx, s.SessionID, s.RefreshToken, "POST", g.proof(t, k, refreshHTU))
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	return next
}

// A superseded refresh token with any refused proof revokes nothing, on
// either endpoint: the proof comes first. Only with a valid proof by the
// session's key is it reuse, and then it revokes that session alone.
func TestSignOut_RefusedProofsRevokeNothingEvenWithASupersededToken(t *testing.T) {
	for _, ep := range signOutEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			ctx := context.Background()
			g := newSignOutRig(t)
			k, k2, other := deviceKey(t), deviceKey(t), deviceKey(t)
			s1 := g.enrolKey(ctx, t, "req_s1", k)
			s2 := g.enrolKey(ctx, t, "req_s2", k2)
			current := g.rotate(ctx, t, s1, k)
			stale := s1.RefreshToken
			neverHeld := strings.Repeat("ab", 32)

			spent := g.proof(t, k, ep.htu)
			if err := ep.call(g.svc, ctx, s1.SessionID, neverHeld, "POST", spent); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("a token never held: %v, want ErrInvalidToken", err)
			}
			for name, proof := range map[string]string{
				"missing":                "",
				"malformed":              "not.a.proof",
				"by another key":         g.proof(t, other, ep.htu),
				"for the refresh URL":    g.proof(t, k, refreshHTU),
				"for the other sign-out": g.proof(t, k, ep.otherHTU),
				"replayed":               spent,
				"stale":                  dpopProof(t, k, k, "so-stale", "POST", ep.htu, g.clock.now().Add(-10*time.Minute)),
			} {
				if err := ep.call(g.svc, ctx, s1.SessionID, stale, "POST", proof); !errors.Is(err, ErrInvalidDeviceProof) {
					t.Errorf("%s proof: %v, want ErrInvalidDeviceProof", name, err)
				}
			}
			if g.revoked(ctx, t, s1.SessionID) || g.revoked(ctx, t, s2.SessionID) {
				t.Fatal("a refused proof revoked a session")
			}
			if ev := g.signOutEvents(); len(ev) != 0 {
				t.Fatalf("events after refused proofs: %v", ev)
			}

			// A valid proof by the session's key: the superseded token is
			// reuse, and revokes this session only.
			if err := ep.call(g.svc, ctx, s1.SessionID, stale, "POST", g.proof(t, k, ep.htu)); !errors.Is(err, ErrTokenReused) {
				t.Fatalf("superseded token with a valid proof: %v, want ErrTokenReused", err)
			}
			if !g.revoked(ctx, t, s1.SessionID) || g.revoked(ctx, t, s2.SessionID) {
				t.Errorf("reuse: s1 revoked %v, s2 revoked %v; want s1 only", g.revoked(ctx, t, s1.SessionID), g.revoked(ctx, t, s2.SessionID))
			}
			if ev := g.signOutEvents(); len(ev) != 1 || ev[0].Type != EventRefreshTokenReused || ev[0].SessionID != s1.SessionID {
				t.Errorf("events = %v, want one reuse event for s1", ev)
			}
			if _, _, err := g.svc.RefreshTokenWithDeviceProof(ctx, s1.SessionID, current, "POST", g.proof(t, k, refreshHTU)); !errors.Is(err, ErrInvalidToken) {
				t.Errorf("refresh after reuse: %v", err)
			}
		})
	}
}

// Missing, expired and unbound sessions are refused before any proof, and
// nothing is revoked.
func TestSignOut_MissingExpiredAndUnboundSessionsAreRefused(t *testing.T) {
	for _, ep := range signOutEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			ctx := context.Background()
			g := newSignOutRig(t)
			k := deviceKey(t)
			s := g.enrolKey(ctx, t, "req_s", k)
			stale := s.RefreshToken
			g.rotate(ctx, t, s, k)

			if err := ep.call(g.svc, ctx, "sess_"+strings.Repeat("0", 64), stale, "POST", g.proof(t, k, ep.htu)); !errors.Is(err, ErrInvalidToken) {
				t.Errorf("missing: %v", err)
			}

			g.repo.mu.Lock()
			g.repo.sessions[s.SessionID].ExpiresAt = time.Now().Add(-time.Minute)
			g.repo.mu.Unlock()
			if err := ep.call(g.svc, ctx, s.SessionID, stale, "POST", g.proof(t, k, ep.htu)); !errors.Is(err, ErrInvalidToken) {
				t.Errorf("expired: %v", err)
			}
			if g.revoked(ctx, t, s.SessionID) {
				t.Error("an expired session was revoked")
			}

			unbound := &UserSession{
				SessionID: "sess_" + strings.Repeat("1", 64), SubscriberID: sid(rlMSISDN), RefreshTokenHash: g.svc.refreshTokenHash("rt_unbound"),
				Roles: []string{"subscriber"}, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
			}
			if err := g.repo.SaveSession(ctx, unbound); err != nil {
				t.Fatal(err)
			}
			if err := ep.call(g.svc, ctx, unbound.SessionID, "rt_unbound", "POST", g.proof(t, k, ep.htu)); !errors.Is(err, ErrSessionNotDeviceBound) {
				t.Errorf("unbound: %v", err)
			}
			if g.revoked(ctx, t, unbound.SessionID) {
				t.Error("an unbound session was revoked")
			}
			if ev := g.signOutEvents(); len(ev) != 0 {
				t.Errorf("events = %v", ev)
			}
		})
	}
}

// Logout revokes its own session only; another device keeps refreshing. A
// retry with a fresh proof by the same key is answered as done.
func TestSignOut_LogoutRevokesOnlyItsSessionAndAnswersRetries(t *testing.T) {
	ctx := context.Background()
	g := newSignOutRig(t)
	kA1, kA2, kB, other := deviceKey(t), deviceKey(t), deviceKey(t), deviceKey(t)
	a1 := g.enrolKey(ctx, t, "req_a1", kA1)
	a2 := g.enrolKey(ctx, t, "req_a2", kA2)
	b := g.enrolOther(ctx, t, "req_b", kB)

	first := g.proof(t, kA1, logoutHTU)
	if err := g.svc.LogoutWithDeviceProof(ctx, a1.SessionID, a1.RefreshToken, "POST", first); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if !g.revoked(ctx, t, a1.SessionID) || g.revoked(ctx, t, a2.SessionID) || g.revoked(ctx, t, b.SessionID) {
		t.Fatal("logout revoked the wrong sessions")
	}
	if _, _, err := g.svc.RefreshTokenWithDeviceProof(ctx, a1.SessionID, a1.RefreshToken, "POST", g.proof(t, kA1, refreshHTU)); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("refresh after logout: %v", err)
	}
	if _, _, err := g.svc.RefreshTokenWithDeviceProof(ctx, a2.SessionID, a2.RefreshToken, "POST", g.proof(t, kA2, refreshHTU)); err != nil {
		t.Errorf("the other device: %v", err)
	}

	if err := g.svc.LogoutWithDeviceProof(ctx, a1.SessionID, a1.RefreshToken, "POST", g.proof(t, kA1, logoutHTU)); err != nil {
		t.Errorf("retry: %v, want done", err)
	}
	if err := g.svc.LogoutWithDeviceProof(ctx, a1.SessionID, a1.RefreshToken, "POST", first); !errors.Is(err, ErrInvalidDeviceProof) {
		t.Errorf("retry with the spent proof: %v", err)
	}
	if err := g.svc.LogoutWithDeviceProof(ctx, a1.SessionID, a1.RefreshToken, "POST", g.proof(t, other, logoutHTU)); !errors.Is(err, ErrInvalidDeviceProof) {
		t.Errorf("retry by another key: %v", err)
	}
	if err := g.svc.LogoutWithDeviceProof(ctx, a1.SessionID, strings.Repeat("cd", 32), "POST", g.proof(t, kA1, logoutHTU)); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("retry with another token: %v", err)
	}
	ev := g.signOutEvents()
	if len(ev) != 1 || ev[0] != (SecurityEvent{Type: EventSessionLoggedOut, At: ev[0].At, SessionID: a1.SessionID}) {
		t.Errorf("events = %+v, want one logout event naming a1 only", ev)
	}
}

// Revoke-all revokes every session of the account, the one that asked
// included, and nothing of any other account (a phone account and a Google
// account), and leaves sign-in methods alone.
func TestSignOut_RevokeAllRevokesTheAccountOnly(t *testing.T) {
	ctx := context.Background()
	g := newSignOutRig(t)
	kA1, kA2, kB, kG := deviceKey(t), deviceKey(t), deviceKey(t), deviceKey(t)
	a1 := g.enrolKey(ctx, t, "req_a1", kA1)
	a2 := g.enrolKey(ctx, t, "req_a2", kA2)
	b := g.enrolOther(ctx, t, "req_b", kB)

	dir := newTestDirectory(t, g.repo, "k1 "+testKeyHex(0x11))
	account, _, err := dir.Create(ctx, GoogleIssuer, "sub-g")
	if err != nil {
		t.Fatal(err)
	}
	gs := &UserSession{
		SessionID: "sess_" + strings.Repeat("9", 64), SubscriberID: account, RefreshTokenHash: g.svc.refreshTokenHash("rt_g"),
		Roles: []string{"subscriber"}, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), DeviceKeyThumbprint: thumbprint(t, kG),
	}
	if err := g.repo.SaveSession(ctx, gs); err != nil {
		t.Fatal(err)
	}

	if err := g.svc.RevokeAllSessionsWithDeviceProof(ctx, a1.SessionID, a1.RefreshToken, "POST", g.proof(t, kA1, revokeAllHTU)); err != nil {
		t.Fatalf("revoke-all: %v", err)
	}
	if !g.revoked(ctx, t, a1.SessionID) || !g.revoked(ctx, t, a2.SessionID) {
		t.Error("a session of the account survived")
	}
	if g.revoked(ctx, t, b.SessionID) || g.revoked(ctx, t, gs.SessionID) {
		t.Error("another account's session was revoked")
	}

	if err := g.svc.RevokeAllSessionsWithDeviceProof(ctx, gs.SessionID, "rt_g", "POST", g.proof(t, kG, revokeAllHTU)); err != nil {
		t.Fatalf("revoke-all of the Google account: %v", err)
	}
	if !g.revoked(ctx, t, gs.SessionID) || g.revoked(ctx, t, b.SessionID) {
		t.Error("the Google account's revoke-all reached the wrong sessions")
	}
	if got, found, err := dir.Lookup(ctx, GoogleIssuer, "sub-g"); err != nil || !found || got != account {
		t.Errorf("the Google identity after revoke-all: (%q, %v, %v), want its account", got, found, err)
	}

	ev := g.signOutEvents()
	if len(ev) != 2 ||
		ev[0] != (SecurityEvent{Type: EventSessionsRevokedAll, At: ev[0].At, SessionID: a1.SessionID}) ||
		ev[1] != (SecurityEvent{Type: EventSessionsRevokedAll, At: ev[1].At, SessionID: gs.SessionID}) {
		t.Errorf("events = %+v, want one revoke-all event per operation, naming only the session that asked", ev)
	}
}

// A retry of a completed revoke-all is answered as done and revokes nothing
// more: a session signed in since stays live. A new operation from that live
// session revokes it. A session revoked some other way cannot use revoke-all.
func TestSignOut_RevokeAllRetriesNeverReachALaterSession(t *testing.T) {
	ctx := context.Background()
	g := newSignOutRig(t)
	kA1, kA2, kA3, kB := deviceKey(t), deviceKey(t), deviceKey(t), deviceKey(t)
	a1 := g.enrolKey(ctx, t, "req_a1", kA1)
	a2 := g.enrolKey(ctx, t, "req_a2", kA2)
	b := g.enrolOther(ctx, t, "req_b", kB)

	if err := g.svc.RevokeAllSessionsWithDeviceProof(ctx, a1.SessionID, a1.RefreshToken, "POST", g.proof(t, kA1, revokeAllHTU)); err != nil {
		t.Fatal(err)
	}
	a3 := g.enrolKey(ctx, t, "req_a3", kA3)
	for i := 0; i < 2; i++ {
		if err := g.svc.RevokeAllSessionsWithDeviceProof(ctx, a1.SessionID, a1.RefreshToken, "POST", g.proof(t, kA1, revokeAllHTU)); err != nil {
			t.Fatalf("retry %d: %v, want done", i, err)
		}
	}
	if g.revoked(ctx, t, a3.SessionID) {
		t.Fatal("a retry revoked a session signed in after the operation")
	}
	if err := g.svc.RevokeAllSessionsWithDeviceProof(ctx, a1.SessionID, strings.Repeat("cd", 32), "POST", g.proof(t, kA1, revokeAllHTU)); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("retry with another token: %v", err)
	}
	if err := g.svc.RevokeAllSessionsWithDeviceProof(ctx, a2.SessionID, a2.RefreshToken, "POST", g.proof(t, kA2, revokeAllHTU)); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("revoke-all from a session another operation revoked: %v", err)
	}
	if g.revoked(ctx, t, a3.SessionID) {
		t.Fatal("a refused revoke-all revoked a later session")
	}

	if err := g.svc.RevokeAllSessionsWithDeviceProof(ctx, a3.SessionID, a3.RefreshToken, "POST", g.proof(t, kA3, revokeAllHTU)); err != nil {
		t.Fatalf("a new operation: %v", err)
	}
	if !g.revoked(ctx, t, a3.SessionID) {
		t.Error("a new operation left a live session")
	}

	// Logout, then revoke-all from the same session: refused, revoking nothing.
	kB2 := deviceKey(t)
	b2 := g.enrolOther(ctx, t, "req_b2", kB2)
	if err := g.svc.LogoutWithDeviceProof(ctx, b.SessionID, b.RefreshToken, "POST", g.proof(t, kB, logoutHTU)); err != nil {
		t.Fatal(err)
	}
	if err := g.svc.RevokeAllSessionsWithDeviceProof(ctx, b.SessionID, b.RefreshToken, "POST", g.proof(t, kB, revokeAllHTU)); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("revoke-all after logout: %v", err)
	}
	if g.revoked(ctx, t, b2.SessionID) {
		t.Error("revoke-all from a logged-out session revoked another")
	}
	if n := countType(g.audit.snapshot(), EventSessionsRevokedAll); n != 2 {
		t.Errorf("%d revoke-all events, want 2 (retries are not audited again)", n)
	}
}

// Concurrent requests of one revoke-all: every one is answered as done, and
// the operation is recorded and audited once.
func TestSignOut_ConcurrentRevokeAllOfOneSessionIsOneOperation(t *testing.T) {
	ctx := context.Background()
	g := newSignOutRig(t)
	k, k2 := deviceKey(t), deviceKey(t)
	s := g.enrolKey(ctx, t, "req_s", k)
	s2 := g.enrolKey(ctx, t, "req_s2", k2)
	proofs := make([]string, 10)
	for i := range proofs {
		proofs[i] = g.proof(t, k, revokeAllHTU)
	}
	var wg sync.WaitGroup
	errs := make([]error, len(proofs))
	for i := range proofs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = g.svc.RevokeAllSessionsWithDeviceProof(ctx, s.SessionID, s.RefreshToken, "POST", proofs[i])
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("request %d: %v", i, err)
		}
	}
	if !g.revoked(ctx, t, s2.SessionID) {
		t.Error("the other session survived")
	}
	if n := countType(g.audit.snapshot(), EventSessionsRevokedAll); n != 1 {
		t.Errorf("%d revoke-all events, want 1", n)
	}
}

// Unconfigured endpoints, and revoke-all over a store without its storage,
// are refused before anything is read.
func TestSignOut_Unavailable(t *testing.T) {
	ctx := context.Background()
	r := newMemEnrollRig(t)
	k := deviceKey(t)
	s := r.enrolKey(ctx, t, "req_s", k)
	if err := r.svc.LogoutWithDeviceProof(ctx, s.SessionID, s.RefreshToken, "POST", "x"); !errors.Is(err, ErrSignOutUnavailable) {
		t.Errorf("logout: %v", err)
	}
	if err := r.svc.RevokeAllSessionsWithDeviceProof(ctx, s.SessionID, s.RefreshToken, "POST", "x"); !errors.Is(err, ErrSignOutUnavailable) {
		t.Errorf("revoke-all: %v", err)
	}

	plain := NewIdentityService(struct{ IdentityRepository }{r.repo}, []byte("test_secret_key"))
	v, err := dpop.NewVerifier(dpop.Config{HTU: revokeAllHTU, MaxAge: time.Minute, FutureSkew: 5 * time.Second, Replay: dpop.NewMemoryReplayStore()})
	if err != nil {
		t.Fatal(err)
	}
	plain.SetRevokeAllProofVerifier(v)
	if err := plain.RevokeAllSessionsWithDeviceProof(ctx, s.SessionID, s.RefreshToken, "POST", "x"); !errors.Is(err, ErrSignOutUnavailable) {
		t.Errorf("revoke-all without its storage: %v", err)
	}
	if got, _ := r.repo.GetSession(ctx, s.SessionID); got.IsRevoked {
		t.Error("an unavailable sign-out revoked the session")
	}
}

// eraseAfterRead erases the account once, right after the next session read:
// an account deletion landing between logout's read and its revocation.
type eraseAfterRead struct {
	*MemoryIdentityRepository
	mu  sync.Mutex
	arm string // the subscriber to erase after the next read; "" is none
}

func (r *eraseAfterRead) GetSession(ctx context.Context, id string) (*UserSession, error) {
	s, err := r.MemoryIdentityRepository.GetSession(ctx, id)
	r.mu.Lock()
	sub := r.arm
	r.arm = ""
	r.mu.Unlock()
	if sub != "" {
		r.MemoryIdentityRepository.mu.Lock()
		r.eraseLocked(&SubscriberErasure{SubscriberID: sub, RequestedAt: time.Now()})
		r.MemoryIdentityRepository.mu.Unlock()
	}
	return s, err
}

// L1: a session erased after logout read it is refused, as a logout after
// the erasure would be, and no logout is audited for it.
func TestSignOut_LogoutOfASessionErasedMeanwhileIsRefusedAndNotAudited(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryIdentityRepository()
	wrapped := &eraseAfterRead{MemoryIdentityRepository: m}
	g := newSignOutRigOn(t, wrapped, m)
	k := deviceKey(t)
	s := g.enrolKey(ctx, t, "req_s", k)

	wrapped.mu.Lock()
	wrapped.arm = s.SubscriberID
	wrapped.mu.Unlock()
	if err := g.svc.LogoutWithDeviceProof(ctx, s.SessionID, s.RefreshToken, "POST", g.proof(t, k, logoutHTU)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("logout of a session erased meanwhile: %v, want ErrInvalidToken", err)
	}
	if _, err := m.GetSession(ctx, s.SessionID); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("the session was not erased: %v", err)
	}
	if n := countType(g.audit.snapshot(), EventSessionLoggedOut); n != 0 {
		t.Errorf("%d logout events for an erased session, want 0", n)
	}
}

// L2: concurrent logouts of one session all succeed, and exactly one is
// audited: only the call that revoked it.
func TestSignOut_ConcurrentLogoutsAreAuditedOnce(t *testing.T) {
	ctx := context.Background()
	g := newSignOutRig(t)
	k, k2 := deviceKey(t), deviceKey(t)
	s := g.enrolKey(ctx, t, "req_s", k)
	other := g.enrolKey(ctx, t, "req_o", k2)
	proofs := make([]string, 10)
	for i := range proofs {
		proofs[i] = g.proof(t, k, logoutHTU)
	}
	var wg sync.WaitGroup
	errs := make([]error, len(proofs))
	for i := range proofs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = g.svc.LogoutWithDeviceProof(ctx, s.SessionID, s.RefreshToken, "POST", proofs[i])
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("logout %d: %v", i, err)
		}
	}
	if !g.revoked(ctx, t, s.SessionID) || g.revoked(ctx, t, other.SessionID) {
		t.Error("concurrent logouts revoked the wrong sessions")
	}
	if n := countType(g.audit.snapshot(), EventSessionLoggedOut); n != 1 {
		t.Errorf("%d logout events, want exactly 1", n)
	}
}

// Logout needs the sign-out storage, as revoke-all does.
func TestSignOut_LogoutWithoutItsStorageIsUnavailable(t *testing.T) {
	ctx := context.Background()
	r := newMemEnrollRig(t)
	k := deviceKey(t)
	s := r.enrolKey(ctx, t, "req_s", k)
	plain := NewIdentityService(struct{ IdentityRepository }{r.repo}, []byte("test_secret_key"))
	v, err := dpop.NewVerifier(dpop.Config{HTU: logoutHTU, MaxAge: time.Minute, FutureSkew: 5 * time.Second, Replay: dpop.NewMemoryReplayStore()})
	if err != nil {
		t.Fatal(err)
	}
	plain.SetLogoutProofVerifier(v)
	if err := plain.LogoutWithDeviceProof(ctx, s.SessionID, s.RefreshToken, "POST", "x"); !errors.Is(err, ErrSignOutUnavailable) {
		t.Errorf("logout without its storage: %v", err)
	}
	if got, _ := r.repo.GetSession(ctx, s.SessionID); got.IsRevoked {
		t.Error("an unavailable logout revoked the session")
	}
}
