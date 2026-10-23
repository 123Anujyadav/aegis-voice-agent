package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// RefreshToken read the session, compared the presented token, then saved the
// rotated session as three separate repository calls. Two refreshes presenting
// the same valid token could both pass the comparison before either saved, so
// both were issued a new pair and the later save silently replaced the earlier
// one — leaving a client holding a token the store had never kept.

// barrierRepo holds every GetSession caller until `parties` of them have read,
// so the test controls the interleaving exactly: all reads, then all writes.
// No sleeps and no reliance on the scheduler.
type barrierRepo struct {
	*MemoryIdentityRepository
	arrived chan struct{}
	release chan struct{}
}

func (r *barrierRepo) GetSession(ctx context.Context, id string) (*UserSession, error) {
	s, err := r.MemoryIdentityRepository.GetSession(ctx, id)
	r.arrived <- struct{}{}
	<-r.release
	return s, err
}

type refreshResult struct {
	access, refresh string
	err             error
}

// raceTwoRefreshes runs two RefreshToken calls with the same presented token,
// both reading the session before either may write.
func raceTwoRefreshes(t *testing.T, repo *barrierRepo, svc *IdentityService, sessionID, token string) [2]refreshResult {
	t.Helper()
	var results [2]refreshResult
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, r, err := svc.RefreshToken(context.Background(), sessionID, token)
			results[i] = refreshResult{a, r, err}
		}()
	}
	for range results {
		select {
		case <-repo.arrived:
		case <-time.After(10 * time.Second):
			t.Fatal("deadlock guard: both refreshes never reached GetSession")
		}
	}
	close(repo.release) // both have read the same session; now let them write
	wg.Wait()
	return results
}

func seedRaceSession(ctx context.Context, t *testing.T, m *MemoryIdentityRepository) {
	t.Helper()
	if err := m.SaveSession(ctx, &UserSession{
		SessionID:        "sess_race",
		SubscriberID:     "usr_test",
		RefreshTokenHash: rth("rt_current"),
		Roles:            []string{"subscriber"},
		CreatedAt:        time.Now(),
		ExpiresAt:        time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// TestRefreshToken_ConcurrentRefreshesRotateExactlyOnce is the regression.
func TestRefreshToken_ConcurrentRefreshesRotateExactlyOnce(t *testing.T) {
	inner := NewMemoryIdentityRepository()
	seedRaceSession(context.Background(), t, inner)
	repo := &barrierRepo{
		MemoryIdentityRepository: inner,
		arrived:                  make(chan struct{}, 2),
		release:                  make(chan struct{}),
	}
	svc := NewIdentityService(repo, []byte("test_secret_key"))

	results := raceTwoRefreshes(t, repo, svc, "sess_race", "rt_current")

	var winner, loser *refreshResult
	for i := range results {
		if results[i].err == nil {
			if winner != nil {
				t.Fatalf("BOTH concurrent refreshes succeeded with the same token "+
					"(issued %q and %q): the rotation is not atomic",
					results[0].refresh[:8], results[1].refresh[:8])
			}
			winner = &results[i]
		} else {
			loser = &results[i]
		}
	}
	if winner == nil {
		t.Fatalf("neither refresh succeeded: %v / %v", results[0].err, results[1].err)
	}

	// The loser is told its token is no longer valid, and gets nothing.
	if !errors.Is(loser.err, ErrInvalidToken) {
		t.Errorf("losing refresh: err = %v, want ErrInvalidToken", loser.err)
	}
	if loser.access != "" || loser.refresh != "" {
		t.Error("the losing refresh was issued tokens")
	}

	// The winner's rotation is what the store holds, and it was not revoked.
	stored, err := inner.GetSession(context.Background(), "sess_race")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if stored.RefreshTokenHash != rth(winner.refresh) {
		t.Error("the stored refresh token is not the one issued to the winning request")
	}
	if stored.IsRevoked {
		t.Error("losing the race revoked the winner's session")
	}

	// And the winner's token keeps working through the ordinary flow.
	plain := NewIdentityService(inner, []byte("test_secret_key"))
	if _, _, err := plain.RefreshToken(context.Background(), "sess_race", winner.refresh); err != nil {
		t.Errorf("the winning refresh token was refused on its next use: %v", err)
	}
}

// revokeAfterReadRepo revokes the session immediately after RefreshToken has
// read it — the exact moment a concurrent reuse detection could land.
type revokeAfterReadRepo struct {
	*MemoryIdentityRepository
}

func (r *revokeAfterReadRepo) GetSession(ctx context.Context, id string) (*UserSession, error) {
	s, err := r.MemoryIdentityRepository.GetSession(ctx, id)
	if err == nil {
		_ = r.MemoryIdentityRepository.RevokeSession(ctx, id)
	}
	return s, err
}

// TestRefreshToken_RevocationBetweenReadAndWriteIsNotUndone proves a rotation
// cannot resurrect a session revoked after it was read. A blind SaveSession of
// the copy read earlier (IsRevoked=false) wrote the revocation away.
func TestRefreshToken_RevocationBetweenReadAndWriteIsNotUndone(t *testing.T) {
	inner := NewMemoryIdentityRepository()
	seedRaceSession(context.Background(), t, inner)
	svc := NewIdentityService(&revokeAfterReadRepo{inner}, []byte("test_secret_key"))

	access, refresh, err := svc.RefreshToken(context.Background(), "sess_race", "rt_current")

	if err == nil || access != "" || refresh != "" {
		t.Errorf("a session revoked mid-rotation was rotated: err=%v, tokens issued=%v",
			err, access != "" || refresh != "")
	}
	stored, gerr := inner.GetSession(context.Background(), "sess_race")
	if gerr != nil {
		t.Fatalf("GetSession: %v", gerr)
	}
	if !stored.IsRevoked {
		t.Error("the rotation un-revoked the session")
	}
	if stored.RefreshTokenHash != rth("rt_current") {
		t.Error("a revoked session's refresh token was replaced")
	}
}

// TestRepo_ReplaceSessionIsACompareAndSwap pins the repository contract that
// RefreshToken's single-use rotation rests on.
func TestRepo_ReplaceSessionIsACompareAndSwap(t *testing.T) {
	ctx := context.Background()
	next := func() *UserSession {
		return &UserSession{SessionID: "sess_race", RefreshTokenHash: rth("rt_next"), Roles: []string{"subscriber"}}
	}

	t.Run("matching token is replaced", func(t *testing.T) {
		m := NewMemoryIdentityRepository()
		seedRaceSession(ctx, t, m)
		s := next()
		if err := m.ReplaceSession(ctx, s, rth("rt_current")); err != nil {
			t.Fatalf("ReplaceSession: %v", err)
		}
		s.Roles[0] = "mutated after the call"
		got, _ := m.GetSession(ctx, "sess_race")
		if got.RefreshTokenHash != rth("rt_next") {
			t.Errorf("stored token hash = %q, want the hash of rt_next", got.RefreshTokenHash)
		}
		if got.Roles[0] != "subscriber" {
			t.Error("the stored session aliases the caller's Roles slice")
		}
	})

	for _, tc := range []struct {
		name  string
		setup func(*MemoryIdentityRepository)
	}{
		{"stale token", func(m *MemoryIdentityRepository) {}},
		{"revoked session", func(m *MemoryIdentityRepository) { _ = m.RevokeSession(ctx, "sess_race") }},
	} {
		t.Run(tc.name+" conflicts and stores nothing", func(t *testing.T) {
			m := NewMemoryIdentityRepository()
			seedRaceSession(ctx, t, m)
			tc.setup(m)
			before, _ := m.GetSession(ctx, "sess_race")

			expected := rth("rt_current")
			if tc.name == "stale token" {
				expected = rth("rt_superseded")
			}
			err := m.ReplaceSession(ctx, next(), expected)
			if !errors.Is(err, ErrSessionConflict) {
				t.Fatalf("err = %v, want ErrSessionConflict", err)
			}
			after, _ := m.GetSession(ctx, "sess_race")
			if after.RefreshTokenHash != before.RefreshTokenHash || after.IsRevoked != before.IsRevoked {
				t.Error("a conflicting ReplaceSession changed the stored session")
			}
		})
	}

	t.Run("missing session conflicts and creates nothing", func(t *testing.T) {
		m := NewMemoryIdentityRepository()
		if err := m.ReplaceSession(ctx, next(), rth("rt_current")); !errors.Is(err, ErrSessionConflict) {
			t.Fatalf("err = %v, want ErrSessionConflict", err)
		}
		if _, err := m.GetSession(ctx, "sess_race"); !errors.Is(err, ErrInvalidToken) {
			t.Error("a conflicting ReplaceSession created a session")
		}
	})
}

// TestRefreshToken_StoreFailureIsNotReportedAsAConflict keeps the two apart: a
// failing store is the store's error, never ErrInvalidToken, so a client is not
// told its credential is bad when the database is down.
func TestRefreshToken_StoreFailureIsNotReportedAsAConflict(t *testing.T) {
	repo := &injectableRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()}
	seedRaceSession(context.Background(), t, repo.MemoryIdentityRepository)
	repo.failSession = true
	svc := NewIdentityService(repo, []byte("test_secret_key"))

	_, _, err := svc.RefreshToken(context.Background(), "sess_race", "rt_current")
	if !errors.Is(err, errInternalStore) {
		t.Errorf("err = %v, want the store's own error", err)
	}
	if errors.Is(err, ErrInvalidToken) || errors.Is(err, ErrTokenReused) {
		t.Error("a store failure was reported as a credential rejection")
	}
}
