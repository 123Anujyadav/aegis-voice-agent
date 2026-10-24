package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/dpop"
)

// Refresh tokens at rest. The repository is handed, and holds, only
// HashRefreshToken values; the raw token exists on the session copy returned
// to the caller, and nowhere a repository can reach.

// testRefreshKey is the HMAC key every service in this package's tests uses.
var testRefreshKey = []byte("test_secret_key")

// rth is the stored form of raw under the tests' key.
func rth(raw string) string { return HashRefreshToken(testRefreshKey, raw) }

// markSuperseded records raw as a refresh token sessionID once held and has
// rotated away from, as a real rotation would: presenting it again is reuse.
func markSuperseded(m *MemoryIdentityRepository, sessionID, raw string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.superseded[sessionID] == nil {
		m.superseded[sessionID] = map[string]struct{}{}
	}
	m.superseded[sessionID][rth(raw)] = struct{}{}
}

func TestHashRefreshToken_IsKeyedCanonicalAndDomainSeparated(t *testing.T) {
	const raw = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"
	h := HashRefreshToken(testRefreshKey, raw)
	if !ValidRefreshTokenHash(h) || h != strings.ToLower(h) || len(h) != 64 {
		t.Fatalf("hash %q is not 64 lowercase hex", h)
	}
	if HashRefreshToken(testRefreshKey, raw) != h {
		t.Error("not deterministic")
	}
	if HashRefreshToken([]byte("another_key"), raw) == h {
		t.Error("the hash does not depend on the key")
	}
	if HashRefreshToken(testRefreshKey, raw+"0") == h {
		t.Error("the hash does not depend on the token")
	}
	unkeyed := sha256.Sum256([]byte(raw))
	if h == hex.EncodeToString(unkeyed[:]) {
		t.Error("the hash is an unkeyed SHA-256")
	}
	plain := hmac.New(sha256.New, testRefreshKey)
	plain.Write([]byte(raw))
	if h == hex.EncodeToString(plain.Sum(nil)) {
		t.Error("the hash is not domain-separated from the key's other uses (OTP codes, subscriber ids)")
	}
	if h == raw {
		t.Error("the hash is the token")
	}

	for name, v := range map[string]string{
		"empty": "", "short": h[:63], "long": h + "0", "upper": strings.ToUpper(h),
		"non-hex": strings.Repeat("g", 64), "prefixed": "rt_" + h[3:],
	} {
		if ValidRefreshTokenHash(v) {
			t.Errorf("%s accepted as a canonical hash", name)
		}
	}
}

// spyRepo records every session handed to the repository's write methods.
type spyRepo struct {
	*MemoryIdentityRepository
	mu       sync.Mutex
	writes   []UserSession
	expected []string
}

func (r *spyRepo) SaveSession(ctx context.Context, s *UserSession) error {
	r.mu.Lock()
	r.writes = append(r.writes, *s)
	r.mu.Unlock()
	return r.MemoryIdentityRepository.SaveSession(ctx, s)
}

func (r *spyRepo) ReplaceSession(ctx context.Context, s *UserSession, expected string) error {
	r.mu.Lock()
	r.writes = append(r.writes, *s)
	r.expected = append(r.expected, expected)
	r.mu.Unlock()
	return r.MemoryIdentityRepository.ReplaceSession(ctx, s, expected)
}

// Every value the repository is given, across creation, plain refresh,
// enrolment and proof-bound refresh, is a hash of a token the client holds —
// and no raw token appears anywhere in what it was given or what it stores.
func TestRefreshHash_RepositoryReceivesOnlyHashes(t *testing.T) {
	spy := &spyRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()}
	svc := withFixedOtp(NewIdentityService(spy, testRefreshKey))
	ctx := context.Background()
	var raws []string

	reqID, _ := svc.RequestOtp(ctx, "+919876543210")
	sess, _, err := svc.ValidateOtp(ctx, reqID, testOtpCode)
	if err != nil {
		t.Fatal(err)
	}
	raws = append(raws, sess.RefreshToken)
	_, r1, err := svc.RefreshToken(ctx, sess.SessionID, sess.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	raws = append(raws, r1)
	_, r2, err := svc.RefreshToken(ctx, sess.SessionID, r1)
	if err != nil {
		t.Fatal(err)
	}
	raws = append(raws, r2)

	// The enrolled path, through the rig with the spy as its repository.
	rig := newEnrollRig(t, spy, spy.MemoryIdentityRepository, dpop.NewMemoryReplayStore()).withRefresh(t, dpop.NewMemoryReplayStore())
	k := deviceKey(t)
	esess := rig.enrolKey(ctx, t, "req_e", k)
	raws = append(raws, esess.RefreshToken)
	_, r3, err := rig.svc.RefreshTokenWithDeviceProof(ctx, esess.SessionID, esess.RefreshToken, "POST", rig.refreshProof(t, k, "rp1"))
	if err != nil {
		t.Fatal(err)
	}
	raws = append(raws, r3)

	hashes := map[string]bool{}
	for _, raw := range raws {
		if raw == "" {
			t.Fatal("a flow returned no refresh token")
		}
		hashes[rth(raw)] = true
	}
	for i, w := range spy.writes {
		if w.RefreshToken != "" {
			t.Errorf("write %d handed the repository a raw refresh token", i)
		}
		if !hashes[w.RefreshTokenHash] {
			t.Errorf("write %d stored %q, which is not the hash of any token issued", i, w.RefreshTokenHash)
		}
	}
	for i, e := range spy.expected {
		if !hashes[e] {
			t.Errorf("ReplaceSession %d expected %q, not the hash of an issued token", i, e)
		}
	}
	spy.mu.Lock()
	dump := fmt.Sprintf("%+v %+v", spy.writes, spy.expected)
	spy.mu.Unlock()
	stored := fmt.Sprintf("%+v", spy.MemoryIdentityRepository.sessions)
	for _, raw := range raws {
		if strings.Contains(dump, raw) || strings.Contains(stored, raw) {
			t.Error("a raw refresh token reached the repository")
		}
	}
	if got, _ := spy.GetSession(ctx, sess.SessionID); got.RefreshToken != "" {
		t.Error("GetSession returned a raw refresh token")
	}
}

// Repositories refuse, before anything else, a session carrying a raw token
// or a non-canonical hash — and store nothing.
func TestRefreshHash_MemoryRepositoryRefusesUnhashedSessions(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryIdentityRepository()
	good := &UserSession{SessionID: "s", RefreshTokenHash: rth("rt_0")}
	if err := m.SaveSession(ctx, good); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]*UserSession{
		"raw token alongside a hash": {SessionID: "s", RefreshToken: "rt_0", RefreshTokenHash: rth("rt_0")},
		"raw token only":             {SessionID: "s", RefreshToken: "rt_0"},
		"no hash":                    {SessionID: "s"},
		"upper-case hash":            {SessionID: "s", RefreshTokenHash: strings.ToUpper(rth("rt_0"))},
		"raw token in the hash slot": {SessionID: "s", RefreshTokenHash: "rt_0"},
	} {
		if err := m.SaveSession(ctx, s); !errors.Is(err, ErrRefreshTokenNotHashed) {
			t.Errorf("SaveSession, %s: err = %v", name, err)
		}
		if err := m.ReplaceSession(ctx, s, rth("rt_0")); !errors.Is(err, ErrRefreshTokenNotHashed) {
			t.Errorf("ReplaceSession, %s: err = %v", name, err)
		}
	}
	if got, _ := m.GetSession(ctx, "s"); got.RefreshTokenHash != rth("rt_0") || got.RefreshToken != "" {
		t.Errorf("a refused write changed the stored session: %+v", got)
	}
}

// Wrong tokens, the stored hash presented as a token, and a token already
// rotated away are all refused — as reuse, which revokes, exactly as before.
func TestRefreshHash_OnlyTheCurrentRawTokenRefreshes(t *testing.T) {
	ctx := context.Background()
	newSvc := func(t *testing.T) (*IdentityService, *MemoryIdentityRepository, *UserSession) {
		t.Helper()
		m := NewMemoryIdentityRepository()
		svc := withFixedOtp(NewIdentityService(m, testRefreshKey))
		reqID, _ := svc.RequestOtp(ctx, "+919876543210")
		sess, _, err := svc.ValidateOtp(ctx, reqID, testOtpCode)
		if err != nil {
			t.Fatal(err)
		}
		return svc, m, sess
	}

	// A token the session never held is refused and revokes NOTHING: revoking
	// on any mismatch let anyone who knew a session id revoke it with any
	// string. The current token still refreshes afterwards.
	notHeld := func(t *testing.T, name string, token func(svc *IdentityService, m *MemoryIdentityRepository, sess *UserSession) string) {
		t.Run(name, func(t *testing.T) {
			svc, m, sess := newSvc(t)
			if _, _, err := svc.RefreshToken(ctx, sess.SessionID, token(svc, m, sess)); !errors.Is(err, ErrInvalidToken) || errors.Is(err, ErrTokenReused) {
				t.Fatalf("err = %v, want ErrInvalidToken and no reuse", err)
			}
			if s, _ := m.GetSession(ctx, sess.SessionID); s.IsRevoked {
				t.Fatal("a token the session never held revoked it")
			}
			if _, _, err := svc.RefreshToken(ctx, sess.SessionID, sess.RefreshToken); err != nil {
				t.Fatalf("the current token no longer refreshes: %v", err)
			}
		})
	}
	notHeld(t, "a wrong token", func(*IdentityService, *MemoryIdentityRepository, *UserSession) string { return "not-the-token" })
	notHeld(t, "the stored hash, as read from a leaked database", func(_ *IdentityService, m *MemoryIdentityRepository, sess *UserSession) string {
		stored, _ := m.GetSession(ctx, sess.SessionID)
		return stored.RefreshTokenHash
	})
	notHeld(t, "a hash computed under another key", func(_ *IdentityService, _ *MemoryIdentityRepository, sess *UserSession) string {
		other := NewIdentityService(NewMemoryIdentityRepository(), []byte("another_key"))
		return other.refreshTokenHash(sess.RefreshToken)
	})
	t.Run("rotation invalidates the previous token", func(t *testing.T) {
		svc, m, sess := newSvc(t)
		_, next, err := svc.RefreshToken(ctx, sess.SessionID, sess.RefreshToken)
		if err != nil {
			t.Fatal(err)
		}
		if s, _ := m.GetSession(ctx, sess.SessionID); s.RefreshTokenHash != rth(next) {
			t.Error("the store does not hold the hash of the new token")
		}
		if _, _, err := svc.RefreshToken(ctx, sess.SessionID, sess.RefreshToken); !errors.Is(err, ErrTokenReused) {
			t.Fatalf("the rotated-away token: err = %v, want ErrTokenReused", err)
		}
		if _, _, err := svc.RefreshToken(ctx, sess.SessionID, next); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("after reuse revoked the session, the current token: err = %v, want ErrInvalidToken", err)
		}
	})
	t.Run("concurrent refreshes: exactly one wins", func(t *testing.T) {
		svc, m, sess := newSvc(t)
		var mu sync.Mutex
		var wins []string
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, next, err := svc.RefreshToken(ctx, sess.SessionID, sess.RefreshToken)
				if err == nil {
					mu.Lock()
					wins = append(wins, next)
					mu.Unlock()
				}
			}()
		}
		close(start)
		wg.Wait()
		if len(wins) != 1 {
			t.Fatalf("%d concurrent refreshes won, want 1", len(wins))
		}
		if s, _ := m.GetSession(ctx, sess.SessionID); s.RefreshTokenHash != rth(wins[0]) {
			t.Error("the store does not hold the winner's hash")
		}
	})
}
