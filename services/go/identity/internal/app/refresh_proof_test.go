package app

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
	"github.com/callscreen/callscreen-platform/packages/go/dpop"
)

// Proof-bound refresh. An enrolled session's refresh token rotates only with a
// fresh proof by the enrolled key for the refresh endpoint; every failure
// before the swap leaves the presented refresh token exactly as usable as it
// was.

// refreshHTU is test-only. No production refresh URL exists in this repository.
const refreshHTU = "https://identity.local.test/v1/token/refresh"

// withRefresh configures the rig's service for proof-bound refresh.
func (r *enrollRig) withRefresh(t *testing.T, store dpop.ReplayStore) *enrollRig {
	t.Helper()
	v, err := dpop.NewVerifier(dpop.Config{
		HTU: refreshHTU, MaxAge: time.Minute, FutureSkew: 5 * time.Second, Replay: store, Now: r.clock.now,
	})
	if err != nil {
		t.Fatalf("dpop.NewVerifier: %v", err)
	}
	r.svc.SetRefreshProofVerifier(v)
	return r
}

// enrolKey enrols k on a fresh challenge and returns the session.
func (r *enrollRig) enrolKey(ctx context.Context, t *testing.T, reqID string, k *ecdsa.PrivateKey) *UserSession {
	t.Helper()
	r.challenge(ctx, t, reqID)
	sess, _, err := r.svc.ValidateOtpWithDeviceProof(ctx, reqID, testOtpCode, "POST",
		enrollProof(t, k, k, "enrol-"+reqID, "POST", r.clock.now()))
	if err != nil {
		t.Fatalf("enrolment: %v", err)
	}
	return sess
}

func (r *enrollRig) refreshProof(t *testing.T, k *ecdsa.PrivateKey, jti string) string {
	t.Helper()
	return dpopProof(t, k, k, jti, "POST", refreshHTU, r.clock.now())
}

// assertUnrotated checks the stored session still holds want, unrevoked.
func (r *enrollRig) assertUnrotated(ctx context.Context, t *testing.T, sessionID, want string) {
	t.Helper()
	stored, err := r.repo.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RefreshTokenHash != rth(want) {
		t.Error("the refresh token was rotated")
	}
	if stored.IsRevoked {
		t.Error("the session was revoked")
	}
}

func newRefreshRig(ctx context.Context, t *testing.T) (*enrollRig, *ecdsa.PrivateKey, *UserSession) {
	t.Helper()
	r := newMemEnrollRig(t).withRefresh(t, dpop.NewMemoryReplayStore())
	k := deviceKey(t)
	sess := r.enrolKey(ctx, t, "req_a", k)
	return r, k, sess
}

// 1-3, 12: a correct proof rotates the refresh token and returns a verifiable
// token bound to the stored key; the rotated-away token is then reuse.
func TestRefreshProof_RotatesAndIssuesBoundToken(t *testing.T) {
	r, k, sess := newRefreshRig(context.Background(), t)
	ctx := context.Background()

	access, refresh, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", r.refreshProof(t, k, "r1"))
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refresh == "" || refresh == sess.RefreshToken {
		t.Fatal("no fresh refresh token")
	}
	stored, _ := r.repo.GetSession(ctx, sess.SessionID)
	if stored.RefreshTokenHash != rth(refresh) {
		t.Error("the store does not hold the issued refresh token")
	}
	claims, err := r.tokens.Verify(access)
	if err != nil {
		t.Fatalf("the access token does not verify: %v", err)
	}
	want, _ := accesstoken.ThumbprintP256(&k.PublicKey)
	if claims.Confirmation.JKT != stored.DeviceKeyThumbprint || claims.Confirmation.JKT != want {
		t.Errorf("cnf.jkt = %q, want the stored binding %q", claims.Confirmation.JKT, stored.DeviceKeyThumbprint)
	}
	if claims.Subject != stored.SubscriberID || claims.Expiry-claims.IssuedAt != int64(accesstoken.Lifetime/time.Second) {
		t.Errorf("sub/lifetime = %q/%ds", claims.Subject, claims.Expiry-claims.IssuedAt)
	}

	// The next rotation works the same way.
	if _, refresh2, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, refresh, "POST", r.refreshProof(t, k, "r2")); err != nil || refresh2 == refresh {
		t.Fatalf("second rotation: %v", err)
	}
	// The original token is now reuse, and reuse revokes.
	if _, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", r.refreshProof(t, k, "r3")); !errors.Is(err, ErrTokenReused) {
		t.Fatalf("reused token: err = %v, want ErrTokenReused", err)
	}
	if stored, _ := r.repo.GetSession(ctx, sess.SessionID); !stored.IsRevoked {
		t.Error("reuse did not revoke the session")
	}
}

// 4, 5, 8, 10: every rejected proof leaves the presented refresh token
// unrotated, the session unrevoked, and a correct proof still succeeds.
func TestRefreshProof_InvalidProofsDoNotRotate(t *testing.T) {
	for name, bad := range map[string]func(t *testing.T, r *enrollRig, k, other *ecdsa.PrivateKey) string{
		"different valid key": func(t *testing.T, r *enrollRig, _, other *ecdsa.PrivateKey) string {
			return r.refreshProof(t, other, "x")
		},
		"missing":   func(*testing.T, *enrollRig, *ecdsa.PrivateKey, *ecdsa.PrivateKey) string { return "" },
		"malformed": func(*testing.T, *enrollRig, *ecdsa.PrivateKey, *ecdsa.PrivateKey) string { return "a.b.c" },
		"stale": func(t *testing.T, r *enrollRig, k, _ *ecdsa.PrivateKey) string {
			return dpopProof(t, k, k, "x", "POST", refreshHTU, r.clock.now().Add(-2*time.Minute))
		},
		"invalid signature": func(t *testing.T, r *enrollRig, k, other *ecdsa.PrivateKey) string {
			return dpopProof(t, other, k, "x", "POST", refreshHTU, r.clock.now())
		},
		"wrong method": func(t *testing.T, r *enrollRig, k, _ *ecdsa.PrivateKey) string {
			return dpopProof(t, k, k, "x", "GET", refreshHTU, r.clock.now())
		},
		"enrolment URL": func(t *testing.T, r *enrollRig, k, _ *ecdsa.PrivateKey) string {
			return dpopProof(t, k, k, "x", "POST", enrollHTU, r.clock.now())
		},
		"other URL": func(t *testing.T, r *enrollRig, k, _ *ecdsa.PrivateKey) string {
			return dpopProof(t, k, k, "x", "POST", "https://identity.local.test/v1/token/refresh/", r.clock.now())
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, k, sess := newRefreshRig(context.Background(), t)
			ctx := context.Background()
			access, refresh, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", bad(t, r, k, deviceKey(t)))
			if !errors.Is(err, ErrInvalidDeviceProof) || access != "" || refresh != "" {
				t.Fatalf("(%d, %d bytes, %v), want ErrInvalidDeviceProof and nothing", len(access), len(refresh), err)
			}
			r.assertUnrotated(ctx, t, sess.SessionID, sess.RefreshToken)
			if _, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", r.refreshProof(t, k, "good")); err != nil {
				t.Errorf("the presented refresh token was not left usable: %v", err)
			}
		})
	}
}

// 6: a proof is single-use, including one that verified but named the wrong
// key: it was spent, and resending it is a replay.
func TestRefreshProof_ReplayIsRejected(t *testing.T) {
	r, k, sess := newRefreshRig(context.Background(), t)
	ctx := context.Background()
	proof := r.refreshProof(t, k, "once")
	_, refresh, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", proof)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, refresh, "POST", proof); !errors.Is(err, ErrInvalidDeviceProof) || !errors.Is(err, dpop.ErrReplayed) {
		t.Fatalf("replayed proof: err = %v, want ErrReplayed", err)
	}
	r.assertUnrotated(ctx, t, sess.SessionID, refresh)

	other := deviceKey(t)
	spent := r.refreshProof(t, other, "spent")
	if _, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, refresh, "POST", spent); !errors.Is(err, ErrInvalidDeviceProof) || errors.Is(err, dpop.ErrReplayed) {
		t.Fatalf("wrong key: err = %v", err)
	}
	if _, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, refresh, "POST", spent); !errors.Is(err, dpop.ErrReplayed) {
		t.Errorf("a verified wrong-key proof was not spent: err = %v", err)
	}
	r.assertUnrotated(ctx, t, sess.SessionID, refresh)
}

// 7, 9: an unreachable replay store, a missing verifier or a missing signer
// fails closed without rotating.
func TestRefreshProof_UnavailableDependenciesFailClosed(t *testing.T) {
	ctx := context.Background()

	t.Run("replay store unreachable", func(t *testing.T) {
		r := newMemEnrollRig(t).withRefresh(t, unreachableReplay{})
		k := deviceKey(t)
		sess := r.enrolKey(ctx, t, "req_a", k)
		_, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", r.refreshProof(t, k, "r1"))
		if !errors.Is(err, dpop.ErrReplayUnavailable) || errors.Is(err, ErrInvalidDeviceProof) {
			t.Errorf("err = %v, want ErrReplayUnavailable", err)
		}
		r.assertUnrotated(ctx, t, sess.SessionID, sess.RefreshToken)
	})

	t.Run("no refresh verifier", func(t *testing.T) {
		r := newMemEnrollRig(t)
		k := deviceKey(t)
		sess := r.enrolKey(ctx, t, "req_a", k)
		_, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", r.refreshProof(t, k, "r1"))
		if !errors.Is(err, ErrDeviceRefreshUnavailable) {
			t.Errorf("err = %v, want ErrDeviceRefreshUnavailable", err)
		}
		r.assertUnrotated(ctx, t, sess.SessionID, sess.RefreshToken)
	})

	t.Run("no signer", func(t *testing.T) {
		r, k, sess := newRefreshRig(ctx, t)
		signer := r.svc.accessTokens
		r.svc.SetAccessTokenSigner(nil)
		proof := r.refreshProof(t, k, "r1")
		if _, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", proof); !errors.Is(err, ErrTokenIssuanceUnavailable) {
			t.Fatalf("err = %v, want ErrTokenIssuanceUnavailable", err)
		}
		r.assertUnrotated(ctx, t, sess.SessionID, sess.RefreshToken)
		r.svc.SetAccessTokenSigner(signer)
		if _, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", proof); err != nil {
			t.Errorf("the proof was spent although nothing was verified: %v", err)
		}
	})
}

// 12, 13: reuse revokes whatever proof accompanies it; expired, revoked and
// unenrolled sessions are refused; the proof-less method still refuses an
// enrolled session.
func TestRefreshProof_SessionStateChecksPrecedeTheProof(t *testing.T) {
	ctx := context.Background()

	t.Run("reuse with a garbage proof still revokes", func(t *testing.T) {
		r, _, sess := newRefreshRig(ctx, t)
		markSuperseded(r.repo, sess.SessionID, "rt_stolen")
		if _, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, "rt_stolen", "POST", "garbage"); !errors.Is(err, ErrTokenReused) {
			t.Fatalf("err = %v, want ErrTokenReused", err)
		}
		if stored, _ := r.repo.GetSession(ctx, sess.SessionID); !stored.IsRevoked {
			t.Error("reuse did not revoke")
		}
	})

	t.Run("reuse revocation failure is reported", func(t *testing.T) {
		errRevoke := errors.New("store: revoke failed")
		repo := &revokeRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()}
		r := newEnrollRig(t, repo, repo.MemoryIdentityRepository, dpop.NewMemoryReplayStore()).withRefresh(t, dpop.NewMemoryReplayStore())
		sess := r.enrolKey(ctx, t, "req_a", deviceKey(t))
		markSuperseded(repo.MemoryIdentityRepository, sess.SessionID, "rt_stolen")
		repo.revokeErr = errRevoke
		_, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, "rt_stolen", "POST", "garbage")
		if !errors.Is(err, ErrTokenReused) || !errors.Is(err, errRevoke) {
			t.Errorf("err = %v, want ErrTokenReused carrying the store's error", err)
		}
	})

	t.Run("revoked", func(t *testing.T) {
		r, k, sess := newRefreshRig(ctx, t)
		_ = r.repo.RevokeSession(ctx, sess.SessionID)
		if _, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", r.refreshProof(t, k, "r1")); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("err = %v, want ErrInvalidToken", err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		r, k, sess := newRefreshRig(ctx, t)
		r.repo.mu.Lock()
		r.repo.sessions[sess.SessionID].ExpiresAt = time.Now().Add(-time.Minute)
		r.repo.mu.Unlock()
		if _, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", r.refreshProof(t, k, "r1")); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("err = %v, want ErrInvalidToken", err)
		}
		r.repo.mu.Lock()
		rt := r.repo.sessions[sess.SessionID].RefreshTokenHash
		r.repo.mu.Unlock()
		if rt != rth(sess.RefreshToken) {
			t.Error("an expired session was rotated")
		}
	})

	t.Run("missing session", func(t *testing.T) {
		r, k, _ := newRefreshRig(ctx, t)
		if _, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, "sess_missing", "rt", "POST", r.refreshProof(t, k, "r1")); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("err = %v, want ErrInvalidToken", err)
		}
	})

	t.Run("unenrolled session", func(t *testing.T) {
		r := newMemEnrollRig(t).withRefresh(t, dpop.NewMemoryReplayStore())
		r.challenge(ctx, t, "req_a")
		sess, _, err := r.svc.ValidateOtp(ctx, "req_a", testOtpCode)
		if err != nil {
			t.Fatal(err)
		}
		k := deviceKey(t)
		access, refresh, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", r.refreshProof(t, k, "r1"))
		if !errors.Is(err, ErrSessionNotDeviceBound) || access != "" || refresh != "" {
			t.Fatalf("(%d, %d bytes, %v), want ErrSessionNotDeviceBound", len(access), len(refresh), err)
		}
		r.assertUnrotated(ctx, t, sess.SessionID, sess.RefreshToken)
		if stored, _ := r.repo.GetSession(ctx, sess.SessionID); stored.DeviceKeyThumbprint != "" {
			t.Error("a refresh proof enrolled a key")
		}
	})

	t.Run("proof-less method cannot bypass", func(t *testing.T) {
		r, _, sess := newRefreshRig(ctx, t)
		if _, _, err := r.svc.RefreshToken(ctx, sess.SessionID, sess.RefreshToken); !errors.Is(err, ErrDeviceProofRequired) {
			t.Errorf("err = %v, want ErrDeviceProofRequired", err)
		}
		r.assertUnrotated(ctx, t, sess.SessionID, sess.RefreshToken)
	})
}

// swapBarrierRepo holds every ReplaceSession caller until `parties` have
// arrived: both refreshes have read, verified and signed before either swaps.
type swapBarrierRepo struct {
	*MemoryIdentityRepository
	armed   bool
	arrived chan struct{}
	release chan struct{}
}

func (r *swapBarrierRepo) ReplaceSession(ctx context.Context, s *UserSession, expected string) error {
	if r.armed {
		r.arrived <- struct{}{}
		<-r.release
	}
	return r.MemoryIdentityRepository.ReplaceSession(ctx, s, expected)
}

// 11: two concurrent refreshes with the same refresh token and two valid
// proofs: exactly one rotates; the loser gets nothing and revokes nothing.
func TestRefreshProof_ConcurrentRefreshesRotateOnce(t *testing.T) {
	repo := &swapBarrierRepo{
		MemoryIdentityRepository: NewMemoryIdentityRepository(),
		arrived:                  make(chan struct{}, 2),
		release:                  make(chan struct{}),
	}
	r := newEnrollRig(t, repo, repo.MemoryIdentityRepository, dpop.NewMemoryReplayStore()).withRefresh(t, dpop.NewMemoryReplayStore())
	k := deviceKey(t)
	sess := r.enrolKey(context.Background(), t, "req_a", k)
	repo.armed = true

	type result struct {
		access, refresh string
		err             error
	}
	var results [2]result
	proofs := [2]string{r.refreshProof(t, k, "c0"), r.refreshProof(t, k, "c1")}
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, rt, err := r.svc.RefreshTokenWithDeviceProof(context.Background(), sess.SessionID, sess.RefreshToken, "POST", proofs[i])
			results[i] = result{a, rt, err}
		}()
	}
	for range results {
		select {
		case <-repo.arrived:
		case <-time.After(10 * time.Second):
			t.Fatal("deadlock guard: both refreshes never reached the swap")
		}
	}
	close(repo.release)
	wg.Wait()

	var winner, loser *result
	for i := range results {
		if results[i].err == nil {
			if winner != nil {
				t.Fatal("BOTH concurrent refreshes succeeded")
			}
			winner = &results[i]
		} else {
			loser = &results[i]
		}
	}
	if winner == nil {
		t.Fatalf("neither succeeded: %v / %v", results[0].err, results[1].err)
	}
	if !errors.Is(loser.err, ErrInvalidToken) || loser.access != "" || loser.refresh != "" {
		t.Errorf("loser: (%d, %d bytes, %v), want ErrInvalidToken and nothing", len(loser.access), len(loser.refresh), loser.err)
	}
	stored, _ := repo.MemoryIdentityRepository.GetSession(context.Background(), sess.SessionID)
	if stored.RefreshTokenHash != rth(winner.refresh) || stored.IsRevoked {
		t.Error("the store does not hold the winner's live rotation")
	}
	if _, err := r.tokens.Verify(winner.access); err != nil {
		t.Errorf("the winner's access token: %v", err)
	}
}

// revokeAtSwapRepo revokes the session at the instant of the swap: after the
// refresh read, verified and signed, before its compare-and-swap.
type revokeAtSwapRepo struct {
	*MemoryIdentityRepository
	armed bool
}

func (r *revokeAtSwapRepo) ReplaceSession(ctx context.Context, s *UserSession, expected string) error {
	if r.armed {
		_ = r.MemoryIdentityRepository.RevokeSession(ctx, s.SessionID)
	}
	return r.MemoryIdentityRepository.ReplaceSession(ctx, s, expected)
}

// 14: a revocation landing between signing and the swap wins. The signed token
// is discarded, nothing is rotated and the revocation stands.
func TestRefreshProof_FailedSwapNeverUndoesRevocation(t *testing.T) {
	repo := &revokeAtSwapRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()}
	r := newEnrollRig(t, repo, repo.MemoryIdentityRepository, dpop.NewMemoryReplayStore()).withRefresh(t, dpop.NewMemoryReplayStore())
	k := deviceKey(t)
	sess := r.enrolKey(context.Background(), t, "req_a", k)
	repo.armed = true

	access, refresh, err := r.svc.RefreshTokenWithDeviceProof(context.Background(), sess.SessionID, sess.RefreshToken, "POST", r.refreshProof(t, k, "r1"))
	if !errors.Is(err, ErrInvalidToken) || access != "" || refresh != "" {
		t.Fatalf("(%d, %d bytes, %v), want ErrInvalidToken and nothing", len(access), len(refresh), err)
	}
	stored, _ := repo.MemoryIdentityRepository.GetSession(context.Background(), sess.SessionID)
	if !stored.IsRevoked {
		t.Error("the failed swap undid the revocation")
	}
	if stored.RefreshTokenHash != rth(sess.RefreshToken) {
		t.Error("a revoked session's refresh token was replaced")
	}
}

// 15: a signing failure rotates nothing and returns no token of any kind; the
// presented refresh token works once signing does.
func TestRefreshProof_SigningFailureRotatesNothing(t *testing.T) {
	r, k, sess := newRefreshRig(context.Background(), t)
	ctx := context.Background()
	good := r.svc.accessTokens
	failing, _ := testTokenKeys(t, r.clock, failingReader{})
	r.svc.SetAccessTokenSigner(failing)

	access, refresh, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", r.refreshProof(t, k, "r1"))
	if !errors.Is(err, ErrAccessTokenIssuance) || access != "" || refresh != "" {
		t.Fatalf("(%q, %d bytes, %v), want ErrAccessTokenIssuance and nothing", access, len(refresh), err)
	}
	r.assertUnrotated(ctx, t, sess.SessionID, sess.RefreshToken)

	r.svc.SetAccessTokenSigner(good)
	if _, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", r.refreshProof(t, k, "r2")); err != nil {
		t.Errorf("retry with the unrotated token and a fresh proof: %v", err)
	}
}
