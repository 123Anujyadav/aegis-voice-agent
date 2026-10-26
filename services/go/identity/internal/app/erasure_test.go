package app

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
	"github.com/callscreen/callscreen-platform/packages/go/dpop"
)

// Account deletion (erasure.go): who may erase, what is erased, what is left,
// and what a repeated or replayed request gets.

// deletionHTU is test-only, like refreshHTU.
const deletionHTU = "https://identity.local.test/v1/account/delete"

const otherMSISDN = "+919876500002"

func (r *enrollRig) withDeletion(t *testing.T, store dpop.ReplayStore) *enrollRig {
	t.Helper()
	v, err := dpop.NewVerifier(dpop.Config{
		HTU: deletionHTU, MaxAge: time.Minute, FutureSkew: 5 * time.Second, Replay: store, Now: r.clock.now,
	})
	if err != nil {
		t.Fatalf("dpop.NewVerifier: %v", err)
	}
	r.svc.SetAccountDeletionProofVerifier(v)
	return r
}

func (r *enrollRig) deletionProof(t *testing.T, k *ecdsa.PrivateKey, jti string) string {
	t.Helper()
	return dpopProof(t, k, k, jti, "POST", deletionHTU, r.clock.now())
}

// enrolOther enrols k for a second subscriber (otherMSISDN).
func (r *enrollRig) enrolOther(ctx context.Context, t *testing.T, reqID string, k *ecdsa.PrivateKey) *UserSession {
	t.Helper()
	if err := r.repo.SaveOtp(ctx, &OtpChallenge{
		RequestID: reqID, MSISDNPseudonym: mp(otherMSISDN), SubscriberID: sid(otherMSISDN),
		CodeHash: r.svc.hashString(testOtpCode), ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	sess, _, err := r.svc.ValidateOtpWithDeviceProof(ctx, reqID, testOtpCode, "POST", enrollProof(t, k, k, "enrol-"+reqID, "POST", r.clock.now()))
	if err != nil {
		t.Fatalf("enrolment: %v", err)
	}
	return sess
}

func thumbprint(t *testing.T, k *ecdsa.PrivateKey) string {
	t.Helper()
	jkt, err := accesstoken.ThumbprintP256(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return jkt
}

type erasureRig struct {
	*enrollRig
	audit                 *recordingSecurityAuditor
	kA1, kA2, kB          *ecdsa.PrivateKey
	sessA1, sessA2, sessB *UserSession
}

// newErasureRig: subscriber A on two devices, subscriber B on one, each with
// a pending code challenge, a device attestation and guardian delegations.
func newErasureRig(ctx context.Context, t *testing.T) *erasureRig {
	t.Helper()
	r := newMemEnrollRig(t).withRefresh(t, dpop.NewMemoryReplayStore()).withDeletion(t, dpop.NewMemoryReplayStore())
	e := &erasureRig{enrollRig: r, audit: &recordingSecurityAuditor{}, kA1: deviceKey(t), kA2: deviceKey(t), kB: deviceKey(t)}
	r.svc.AddSecurityAuditor(e.audit)
	e.sessA1 = r.enrolKey(ctx, t, "req_a1", e.kA1)
	e.sessA2 = r.enrolKey(ctx, t, "req_a2", e.kA2)
	e.sessB = r.enrolOther(ctx, t, "req_b", e.kB)
	r.challenge(ctx, t, "req_pending_a")
	if err := r.repo.SaveOtp(ctx, &OtpChallenge{
		RequestID: "req_pending_b", MSISDNPseudonym: mp(otherMSISDN), SubscriberID: sid(otherMSISDN),
		CodeHash: r.svc.hashString(testOtpCode), ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []*ecdsa.PrivateKey{e.kA1, e.kB} {
		if err := r.repo.SaveDevice(ctx, &DeviceAttestation{DeviceID: "dev_" + thumbprint(t, k), AttestedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	a, b := sid(rlMSISDN), sid(otherMSISDN)
	r.repo.delegations = map[string]*GuardianDelegation{
		"g:" + a: {GuardianID: "g", SubscriberID: a},
		a + ":x": {GuardianID: a, SubscriberID: "x"},
		"g:" + b: {GuardianID: "g", SubscriberID: b},
	}
	e.audit.events = nil
	return e
}

func (e *erasureRig) sessionExists(ctx context.Context, t *testing.T, id string) bool {
	t.Helper()
	_, err := e.repo.GetSession(ctx, id)
	return err == nil
}

// The subscriber's every session (both devices), pending challenge, device
// attestation and delegations are erased; the other subscriber is untouched;
// the erasure is recorded with its exceptions and audited.
func TestDeleteAccount_ErasesTheSubscriberEverywhereAndNobodyElse(t *testing.T) {
	ctx := context.Background()
	e := newErasureRig(ctx, t)

	if err := e.svc.DeleteAccountWithDeviceProof(ctx, e.sessA1.SessionID, e.sessA1.RefreshToken, "POST", e.deletionProof(t, e.kA1, "d1")); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if e.sessionExists(ctx, t, e.sessA1.SessionID) || e.sessionExists(ctx, t, e.sessA2.SessionID) {
		t.Error("a session of the erased subscriber survived (both devices must go)")
	}
	if !e.sessionExists(ctx, t, e.sessB.SessionID) {
		t.Error("another subscriber's session was erased")
	}
	if _, ok := e.repo.otps["req_pending_a"]; ok {
		t.Error("the erased subscriber's pending challenge survived")
	}
	if _, ok := e.repo.otps["req_pending_b"]; !ok {
		t.Error("another subscriber's challenge was erased")
	}
	if _, ok := e.repo.devices["dev_"+thumbprint(t, e.kA1)]; ok {
		t.Error("the erased subscriber's device attestation survived")
	}
	if _, ok := e.repo.devices["dev_"+thumbprint(t, e.kB)]; !ok {
		t.Error("another subscriber's device attestation was erased")
	}
	if got := len(e.repo.delegations); got != 1 {
		t.Errorf("%d delegations left, want only the one not involving the erased subscriber", got)
	}
	if _, ok := e.repo.delegations["g:"+sid(otherMSISDN)]; !ok {
		t.Error("a delegation not involving the erased subscriber was erased")
	}

	erasures := e.repo.Erasures()
	if len(erasures) != 1 || erasures[0].SubscriberID != sid(rlMSISDN) || !reflect.DeepEqual(erasures[0].Exceptions, ErasureExceptions) {
		t.Fatalf("erasure record = %+v", erasures)
	}
	if len(e.audit.events) != 1 || e.audit.events[0].Type != EventAccountDeleted || e.audit.events[0].SessionID != e.sessA1.SessionID {
		t.Errorf("audit = %+v, want one %s naming the requesting session", e.audit.events, EventAccountDeleted)
	}

	// The other device can no longer refresh: its session is gone.
	if _, _, err := e.svc.RefreshTokenWithDeviceProof(ctx, e.sessA2.SessionID, e.sessA2.RefreshToken, "POST", e.refreshProof(t, e.kA2, "r-after")); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("refresh on the other device after erasure = %v, want ErrInvalidToken", err)
	}
}

// Every authentication failure erases nothing.
func TestDeleteAccount_RefusalsEraseNothing(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		run  func(e *erasureRig) error
		want error
	}{
		{"a refresh token the session never held", func(e *erasureRig) error {
			return e.svc.DeleteAccountWithDeviceProof(ctx, e.sessA1.SessionID, e.sessB.RefreshToken, "POST", e.deletionProof(t, e.kA1, "x"))
		}, ErrInvalidToken},
		{"a proof by another key (another subscriber's device)", func(e *erasureRig) error {
			return e.svc.DeleteAccountWithDeviceProof(ctx, e.sessA1.SessionID, e.sessA1.RefreshToken, "POST", e.deletionProof(t, e.kB, "x"))
		}, ErrInvalidDeviceProof},
		{"a proof for the refresh endpoint", func(e *erasureRig) error {
			return e.svc.DeleteAccountWithDeviceProof(ctx, e.sessA1.SessionID, e.sessA1.RefreshToken, "POST", e.refreshProof(t, e.kA1, "x"))
		}, ErrInvalidDeviceProof},
		{"no proof", func(e *erasureRig) error {
			return e.svc.DeleteAccountWithDeviceProof(ctx, e.sessA1.SessionID, e.sessA1.RefreshToken, "POST", "")
		}, ErrInvalidDeviceProof},
		{"another subscriber's session with this subscriber's key", func(e *erasureRig) error {
			return e.svc.DeleteAccountWithDeviceProof(ctx, e.sessB.SessionID, e.sessB.RefreshToken, "POST", e.deletionProof(t, e.kA1, "x"))
		}, ErrInvalidDeviceProof},
		{"an unknown session", func(e *erasureRig) error {
			return e.svc.DeleteAccountWithDeviceProof(ctx, "sess_unknown", e.sessA1.RefreshToken, "POST", e.deletionProof(t, e.kA1, "x"))
		}, ErrInvalidToken},
		{"a revoked session", func(e *erasureRig) error {
			if err := e.repo.RevokeSession(ctx, e.sessA1.SessionID); err != nil {
				return err
			}
			return e.svc.DeleteAccountWithDeviceProof(ctx, e.sessA1.SessionID, e.sessA1.RefreshToken, "POST", e.deletionProof(t, e.kA1, "x"))
		}, ErrInvalidToken},
		{"an expired session", func(e *erasureRig) error {
			e.repo.sessions[e.sessA1.SessionID].ExpiresAt = time.Now().Add(-time.Second)
			return e.svc.DeleteAccountWithDeviceProof(ctx, e.sessA1.SessionID, e.sessA1.RefreshToken, "POST", e.deletionProof(t, e.kA1, "x"))
		}, ErrInvalidToken},
		{"deletion not configured", func(e *erasureRig) error {
			e.svc.SetAccountDeletionProofVerifier(nil)
			return e.svc.DeleteAccountWithDeviceProof(ctx, e.sessA1.SessionID, e.sessA1.RefreshToken, "POST", e.deletionProof(t, e.kA1, "x"))
		}, ErrAccountDeletionUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newErasureRig(ctx, t)
			if err := c.run(e); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if len(e.repo.Erasures()) != 0 {
				t.Error("an erasure was recorded")
			}
			if !e.sessionExists(ctx, t, e.sessA2.SessionID) || !e.sessionExists(ctx, t, e.sessB.SessionID) {
				t.Error("a session was erased")
			}
			if _, ok := e.repo.otps["req_pending_a"]; !ok {
				t.Error("a challenge was erased")
			}
		})
	}
}

// A refresh token the session rotated away from is reuse, as on refresh: the
// session is revoked (theft signal) and nothing is erased.
func TestDeleteAccount_SupersededRefreshTokenIsReuse(t *testing.T) {
	ctx := context.Background()
	e := newErasureRig(ctx, t)
	if _, _, err := e.svc.RefreshTokenWithDeviceProof(ctx, e.sessA1.SessionID, e.sessA1.RefreshToken, "POST", e.refreshProof(t, e.kA1, "r1")); err != nil {
		t.Fatal(err)
	}
	err := e.svc.DeleteAccountWithDeviceProof(ctx, e.sessA1.SessionID, e.sessA1.RefreshToken, "POST", e.deletionProof(t, e.kA1, "d1"))
	if !errors.Is(err, ErrTokenReused) {
		t.Fatalf("err = %v, want ErrTokenReused", err)
	}
	stored, _ := e.repo.GetSession(ctx, e.sessA1.SessionID)
	if stored == nil || !stored.IsRevoked {
		t.Error("the session was not revoked")
	}
	if len(e.repo.Erasures()) != 0 {
		t.Error("an erasure was recorded")
	}
}

// A client that lost the answer retries: the same session and key are told
// "done" again. Another key, a replayed proof, or an expired receipt is not.
func TestDeleteAccount_RepeatIsIdempotentForTheSameKeyOnly(t *testing.T) {
	ctx := context.Background()
	e := newErasureRig(ctx, t)
	first := e.deletionProof(t, e.kA1, "d1")
	if err := e.svc.DeleteAccountWithDeviceProof(ctx, e.sessA1.SessionID, e.sessA1.RefreshToken, "POST", first); err != nil {
		t.Fatal(err)
	}

	if err := e.svc.DeleteAccountWithDeviceProof(ctx, e.sessA1.SessionID, e.sessA1.RefreshToken, "POST", e.deletionProof(t, e.kA1, "d2")); err != nil {
		t.Errorf("repeat with the same key = %v, want success", err)
	}
	if err := e.svc.DeleteAccountWithDeviceProof(ctx, e.sessA1.SessionID, e.sessA1.RefreshToken, "POST", e.deletionProof(t, e.kB, "d3")); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("repeat with another key = %v, want ErrInvalidToken", err)
	}
	if err := e.svc.DeleteAccountWithDeviceProof(ctx, e.sessA1.SessionID, e.sessA1.RefreshToken, "POST", first); !errors.Is(err, ErrInvalidDeviceProof) {
		t.Errorf("a replayed proof = %v, want ErrInvalidDeviceProof", err)
	}
	if got := len(e.repo.Erasures()); got != 1 {
		t.Errorf("%d erasures recorded, want 1 (a repeat records nothing)", got)
	}
	if got := len(e.audit.events); got != 1 {
		t.Errorf("%d audit events, want 1 (a repeat audits nothing)", got)
	}

	// An expired receipt answers nothing.
	if err := e.repo.EraseSubscriber(ctx, &SubscriberErasure{
		SubscriberID: sid(otherMSISDN), RequestedAt: time.Now(), RequestingSessionID: "sess_old",
		DeviceKeyThumbprint: thumbprint(t, e.kB), ReceiptExpiresAt: time.Now().Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := e.repo.ErasureReceipt(ctx, "sess_old"); found {
		t.Error("an expired receipt was found")
	}
}

// Erasure does not block the number: signing in again creates a new account
// with nothing of the old one. (Whether an erased number may return is an
// owner decision: docs/phase19/ACCOUNT-DELETION.md.)
func TestDeleteAccount_TheNumberCanSignUpAgain(t *testing.T) {
	ctx := context.Background()
	e := newErasureRig(ctx, t)
	if err := e.svc.DeleteAccountWithDeviceProof(ctx, e.sessA1.SessionID, e.sessA1.RefreshToken, "POST", e.deletionProof(t, e.kA1, "d1")); err != nil {
		t.Fatal(err)
	}
	again := e.enrolKey(ctx, t, "req_again", deviceKey(t))
	if again.SessionID == e.sessA1.SessionID || again.SubscriberID != sid(rlMSISDN) {
		t.Errorf("new session %+v", again)
	}
}
