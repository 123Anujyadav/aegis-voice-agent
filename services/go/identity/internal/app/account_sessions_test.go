package app

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
)

// The device-bound session helper for non-SMS sign-in (account_sessions.go):
// the same session, token and audit as SMS sign-in, for a stored, active
// account only. It does not touch the SMS path's state.

type accountSessionRig struct {
	*enrollRig
	audit *recordingSecurityAuditor
	dir   *GoogleAccountDirectory
	jkt   string
}

func newAccountSessionRig(t *testing.T) *accountSessionRig {
	t.Helper()
	r := newMemEnrollRig(t)
	audit := &recordingSecurityAuditor{}
	r.svc.AddSecurityAuditor(audit)
	d := newTestDirectory(t, r.repo, "k1 "+testKeyHex(0x11))
	jkt := thumbprintOf(t, deviceKey(t))
	return &accountSessionRig{enrollRig: r, audit: audit, dir: d, jkt: jkt}
}

func TestAccountSession_IssuesADeviceBoundSessionForTheAccount(t *testing.T) {
	ctx := context.Background()
	r := newAccountSessionRig(t)
	id := mustCreateAccount(ctx, t, r.dir, "111")
	session, token, err := r.svc.createAccountSession(ctx, id, r.jkt, time.Now(), nil)
	if err != nil {
		t.Fatalf("createAccountSession: %v", err)
	}
	if session.SubscriberID != id || session.DeviceKeyThumbprint != r.jkt || session.RefreshToken == "" ||
		session.IntegrityTier != SessionUnverified {
		t.Errorf("session = %+v", session)
	}
	claims, err := r.tokens.Verify(token)
	if err != nil {
		t.Fatalf("the access token does not verify: %v", err)
	}
	if claims.Subject != id || claims.Confirmation.JKT != r.jkt {
		t.Errorf("token sub %q cnf.jkt %q, want %q and the device key", claims.Subject, claims.Confirmation.JKT, id)
	}
	stored, err := r.repo.GetSession(ctx, session.SessionID)
	if err != nil || stored.RefreshTokenHash == session.RefreshToken || stored.RefreshTokenHash == "" {
		t.Errorf("stored session: (%+v, %v): only the refresh token's hash is stored", stored, err)
	}
	events := r.audit.snapshot()
	if len(events) != 1 || events[0].Type != EventDeviceEnrolled || events[0].SessionID != session.SessionID {
		t.Errorf("audit = %+v, want one enrolment", events)
	}
	// A second device key for the same account is a re-enrolment.
	second := thumbprintOf(t, deviceKey(t))
	if _, _, err := r.svc.createAccountSession(ctx, id, second, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if events := r.audit.snapshot(); len(events) != 3 || events[2].Type != EventDeviceReenrolled {
		t.Errorf("audit after a second key = %+v, want a re-enrolment", events)
	}
}

func TestAccountSession_RefusesAccountsThatAreNotActive(t *testing.T) {
	ctx := context.Background()
	r := newAccountSessionRig(t)
	unknown := "usr_" + "00000000000000000000000000000001"
	if _, _, err := r.svc.createAccountSession(ctx, unknown, r.jkt, time.Now(), nil); !errors.Is(err, ErrAccountNotActive) {
		t.Errorf("unknown account: err = %v, want ErrAccountNotActive", err)
	}
	// A number-derived subscriber id has no account row: not an account.
	legacy := LegacySubscriberID([]byte("test_secret_key"), "+14155552671")
	if _, _, err := r.svc.createAccountSession(ctx, legacy, r.jkt, time.Now(), nil); !errors.Is(err, ErrAccountNotActive) {
		t.Errorf("a number-derived id: err = %v, want ErrAccountNotActive", err)
	}

	id := mustCreateAccount(ctx, t, r.dir, "111")
	session, _, err := r.svc.createAccountSession(ctx, id, r.jkt, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	if err := r.repo.EraseSubscriber(ctx, &SubscriberErasure{
		SubscriberID: id, RequestedAt: time.Now().Add(time.Millisecond), RequestingSessionID: session.SessionID,
		RequestingRefreshTokenHash: r.svc.refreshTokenHash(session.RefreshToken), DeviceKeyThumbprint: r.jkt,
		ReceiptExpiresAt: time.Now().Add(time.Hour), Exceptions: ErasureExceptions,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.svc.createAccountSession(ctx, id, r.jkt, began, nil); !errors.Is(err, ErrAccountNotActive) {
		t.Errorf("an erased account: err = %v, want ErrAccountNotActive", err)
	}
}

// The erasure fence: an erasure recorded after the sign-in began leaves no
// session, even while the account row still reads active (as in the race
// between a sign-in and a deletion).
func TestAccountSession_TheErasureFenceHolds(t *testing.T) {
	ctx := context.Background()
	r := newAccountSessionRig(t)
	id := mustCreateAccount(ctx, t, r.dir, "111")
	began := time.Now().Add(-time.Minute)
	r.repo.mu.Lock()
	r.repo.erasures = append(r.repo.erasures, SubscriberErasure{SubscriberID: id, RequestedAt: time.Now()})
	r.repo.mu.Unlock()
	if _, _, err := r.svc.createAccountSession(ctx, id, r.jkt, began, nil); !errors.Is(err, ErrSubscriberErased) {
		t.Errorf("err = %v, want ErrSubscriberErased", err)
	}
	r.repo.mu.RLock()
	defer r.repo.mu.RUnlock()
	for _, s := range r.repo.sessions {
		if s.SubscriberID == id {
			t.Error("a session was created for an account erased in flight")
		}
	}
}

func TestAccountSession_RefusesWhatItCannotIssueSafely(t *testing.T) {
	ctx := context.Background()
	r := newAccountSessionRig(t)
	id := mustCreateAccount(ctx, t, r.dir, "111")
	if _, _, err := r.svc.createAccountSession(ctx, id, "", time.Now(), nil); !errors.Is(err, ErrInvalidDeviceProof) {
		t.Errorf("no device key: err = %v", err)
	}
	if _, _, err := r.svc.createAccountSession(ctx, id, r.jkt, time.Time{}, nil); !errors.Is(err, ErrInvalidDeviceProof) {
		t.Errorf("no begin time: err = %v", err)
	}
	refused := errors.New("integrity refused")
	if _, _, err := r.svc.createAccountSession(ctx, id, r.jkt, time.Now(), func(context.Context) (SessionIntegrityTier, error) {
		return "", refused
	}); !errors.Is(err, refused) {
		t.Errorf("a refused integrity assessment: err = %v", err)
	}
	session, _, err := r.svc.createAccountSession(ctx, id, r.jkt, time.Now(), func(context.Context) (SessionIntegrityTier, error) {
		return SessionRestricted, nil
	})
	if err != nil || session.IntegrityTier != SessionRestricted {
		t.Errorf("the assessed tier is recorded: (%+v, %v)", session, err)
	}
	r.repo.mu.RLock()
	sessions := len(r.repo.sessions)
	otps := len(r.repo.otps)
	r.repo.mu.RUnlock()
	if sessions != 1 || otps != 0 {
		t.Errorf("%d sessions and %d SMS challenges, want 1 and 0: refused calls stored nothing, and the SMS path is untouched", sessions, otps)
	}

	noSigner := NewIdentityService(r.repo, []byte("test_secret_key"))
	if _, _, err := noSigner.createAccountSession(ctx, id, r.jkt, time.Now(), nil); !errors.Is(err, ErrTokenIssuanceUnavailable) {
		t.Errorf("no signer: err = %v", err)
	}
	noAccounts := NewIdentityService(withoutAccounts{r.repo}, []byte("test_secret_key"))
	noAccounts.SetAccessTokenSigner(r.svc.accessTokens)
	if _, _, err := noAccounts.createAccountSession(ctx, id, r.jkt, time.Now(), nil); !errors.Is(err, ErrAccountsUnavailable) {
		t.Errorf("a repository without accounts: err = %v", err)
	}
}

func thumbprintOf(t *testing.T, k *ecdsa.PrivateKey) string {
	t.Helper()
	jkt, err := accesstoken.ThumbprintP256(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return jkt
}

// withoutAccounts hides the account storage of a repository.
type withoutAccounts struct{ IdentityRepository }
