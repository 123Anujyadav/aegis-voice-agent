package app

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/dpop"
)

// ADR-0031: Play Integrity at issuance and refresh. The verifier is a test
// double; nothing here talks to Google, and no verdict is real.

// boundVerifier decodes test tokens of the form "<kind>|<requestHash>":
// "good" is a genuine verdict bound to requestHash, "unlicensed" a verdict
// that fails licensing, "reject" a token Google rejects, "down" an outage.
type boundVerifier struct {
	now func() time.Time

	mu    sync.Mutex
	calls int
}

func (b *boundVerifier) Decode(_ context.Context, token string) (IntegrityVerdict, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	kind, hash, _ := strings.Cut(token, "|")
	v := goodVerdict()
	v.RequestHash, v.Timestamp = hash, b.now().Add(-5*time.Second)
	switch kind {
	case "good":
		return v, nil
	case "unlicensed":
		v.AppLicensing = "UNLICENSED"
		return v, nil
	case "reject":
		return IntegrityVerdict{}, ErrIntegrityTokenInvalid
	default:
		return IntegrityVerdict{}, errors.New("google: 503")
	}
}

func (b *boundVerifier) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// integrityRig is an enrolment rig with a Play Integrity verifier, refresh
// configured, and a recording auditor.
func integrityRig(t *testing.T, withVerifier bool) (*enrollRig, *boundVerifier, *recordingSecurityAuditor) {
	t.Helper()
	r := newMemEnrollRig(t).withRefresh(t, dpop.NewMemoryReplayStore())
	v := &boundVerifier{now: r.clock.now}
	if withVerifier {
		if err := r.svc.SetIntegrityVerifier(v, goodPolicy()); err != nil {
			t.Fatal(err)
		}
	}
	aud := &recordingSecurityAuditor{}
	r.svc.SetSecurityAuditor(aud)
	return r, v, aud
}

// enrolWith enrols k on a fresh challenge, presenting an integrity token
// built by token from the enrolment proof (or none, if token is nil).
func (r *enrollRig) enrolWith(ctx context.Context, t *testing.T, reqID string, k *ecdsa.PrivateKey, token func(proof string) string) (*UserSession, error) {
	t.Helper()
	r.challenge(ctx, t, reqID)
	proof := enrollProof(t, k, k, "enrol-"+reqID, "POST", r.clock.now())
	tok := ""
	if token != nil {
		tok = token(proof)
	}
	sess, _, err := r.svc.ValidateOtpWithDeviceProofAndIntegrity(ctx, reqID, testOtpCode, "POST", proof, tok)
	return sess, err
}

func bound(kind string) func(string) string {
	return func(proof string) string { return kind + "|" + IntegrityRequestHash(proof) }
}

func (r *enrollRig) storedTier(ctx context.Context, t *testing.T, sessionID string) SessionIntegrityTier {
	t.Helper()
	s, err := r.repo.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return s.IntegrityTier
}

func (r *enrollRig) sessionCount() int {
	r.repo.mu.RLock()
	defer r.repo.mu.RUnlock()
	return len(r.repo.sessions)
}

// D31.1: the request hash is base64url(SHA-256(proof)), unpadded.
func TestIntegrityRequestHash_IsTheProofDigest(t *testing.T) {
	// SHA-256("abc"), base64url without padding (RFC 4648 §5).
	if got, want := IntegrityRequestHash("abc"), "ungWv48Bz-pBQUDeXa4iI7ADYaOWF3qctBD_YfIAFa0"; got != want {
		t.Fatalf("IntegrityRequestHash = %s, want %s", got, want)
	}
}

// D31.3, every row that issues: the session records the tier, issuance
// proceeds, and only a restricted verdict is audited.
func TestIntegrityAtIssuance_TheOutcomeTable(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name     string
		verifier bool
		token    func(string) string
		want     SessionIntegrityTier
		audited  bool
	}{
		{"certified", true, bound("good"), SessionCertified, false},
		{"restricted", true, bound("unlicensed"), SessionRestricted, true},
		{"bound to another request", true, func(string) string { return "good|" + IntegrityRequestHash("another proof") }, SessionRestricted, true},
		{"no token", true, nil, SessionUnverified, false},
		{"no verifier", false, bound("good"), SessionUnverified, false},
		{"google outage", true, bound("down"), SessionUnverified, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, _, aud := integrityRig(t, c.verifier)
			sess, err := r.enrolWith(ctx, t, "req_a", deviceKey(t), c.token)
			if err != nil {
				t.Fatalf("enrolment refused: %v", err)
			}
			if got := r.storedTier(ctx, t, sess.SessionID); got != c.want {
				t.Fatalf("stored tier %q, want %q", got, c.want)
			}
			restricted := eventsOf(aud, EventIntegrityRestricted)
			if c.audited != (len(restricted) == 1) || len(eventsOf(aud, EventIntegrityRejected)) != 0 {
				t.Fatalf("restricted events %+v; want audited=%v", restricted, c.audited)
			}
			if c.audited && (restricted[0].RequestID != "req_a" || restricted[0].SessionID != "") {
				t.Fatalf("the restricted event names %+v, want the OTP request only", restricted[0])
			}
		})
	}
}

// D31.3: a token the verifier rejects refuses enrolment. No session exists,
// the rejection is audited, and the code is spent (a retry is refused).
func TestIntegrityAtIssuance_ARejectedTokenRefusesAndLeavesNoSession(t *testing.T) {
	ctx := context.Background()
	r, _, aud := integrityRig(t, true)
	k := deviceKey(t)
	if _, err := r.enrolWith(ctx, t, "req_a", k, bound("reject")); !errors.Is(err, ErrPlayIntegrityFailed) {
		t.Fatalf("err = %v, want ErrPlayIntegrityFailed", err)
	}
	if n := r.sessionCount(); n != 0 {
		t.Fatalf("%d sessions exist after a rejected token", n)
	}
	if got := eventsOf(aud, EventIntegrityRejected); len(got) != 1 || got[0].RequestID != "req_a" {
		t.Fatalf("rejected events %+v, want one naming req_a", got)
	}
	if len(eventsOf(aud, EventDeviceEnrolled)) != 0 {
		t.Fatal("an enrolment was audited for a refused request")
	}
	proof := enrollProof(t, k, k, "enrol-retry", "POST", r.clock.now())
	if _, _, err := r.svc.ValidateOtpWithDeviceProofAndIntegrity(ctx, "req_a", testOtpCode, "POST", proof, ""); !errors.Is(err, ErrInvalidOtp) {
		t.Fatalf("the spent code was accepted again: %v", err)
	}
}

// D31.3's order: integrity is assessed only after the code and the proof are
// verified, so a wrong code or a bad proof spends no Google quota.
func TestIntegrityAtIssuance_NoQuotaIsSpentBeforeTheCodeAndProofVerify(t *testing.T) {
	ctx := context.Background()
	r, v, _ := integrityRig(t, true)
	k := deviceKey(t)
	r.challenge(ctx, t, "req_a")
	proof := enrollProof(t, k, k, "p1", "POST", r.clock.now())
	if _, _, err := r.svc.ValidateOtpWithDeviceProofAndIntegrity(ctx, "req_a", "000000", "POST", proof, bound("good")(proof)); !errors.Is(err, ErrInvalidOtp) {
		t.Fatalf("wrong code: %v", err)
	}
	bad := enrollProof(t, k, deviceKey(t), "p2", "POST", r.clock.now()) // signed by another key
	if _, _, err := r.svc.ValidateOtpWithDeviceProofAndIntegrity(ctx, "req_a", testOtpCode, "POST", bad, bound("good")(bad)); !errors.Is(err, ErrInvalidDeviceProof) {
		t.Fatalf("bad proof: %v", err)
	}
	if v.callCount() != 0 {
		t.Fatalf("the verifier was called %d times before the code and proof verified", v.callCount())
	}
}

// D31.4: refresh with a token re-assesses and records the tier in the same
// swap; without a token the tier is kept; a rejected token rotates nothing
// and keeps the tier.
func TestIntegrityAtRefresh_RecordsKeepsAndRefuses(t *testing.T) {
	ctx := context.Background()
	r, _, aud := integrityRig(t, true)
	k := deviceKey(t)
	sess, err := r.enrolWith(ctx, t, "req_a", k, nil)
	if err != nil || r.storedTier(ctx, t, sess.SessionID) != SessionUnverified {
		t.Fatalf("enrolment: %v", err)
	}
	rt := sess.RefreshToken

	refresh := func(jti string, token func(string) string) (string, error) {
		proof := r.refreshProof(t, k, jti)
		tok := ""
		if token != nil {
			tok = token(proof)
		}
		_, next, rerr := r.svc.RefreshTokenWithDeviceProofAndIntegrity(ctx, sess.SessionID, rt, "POST", proof, tok)
		return next, rerr
	}

	if rt, err = refresh("r1", bound("good")); err != nil {
		t.Fatal(err)
	}
	if got := r.storedTier(ctx, t, sess.SessionID); got != SessionCertified {
		t.Fatalf("after a certified refresh: %q", got)
	}
	if rt, err = refresh("r2", nil); err != nil {
		t.Fatal(err)
	}
	if got := r.storedTier(ctx, t, sess.SessionID); got != SessionCertified {
		t.Fatalf("a refresh without a token changed the tier to %q", got)
	}
	if _, rerr := refresh("r3", bound("reject")); !errors.Is(rerr, ErrPlayIntegrityFailed) {
		t.Fatalf("rejected token at refresh: %v", rerr)
	}
	r.assertUnrotated(ctx, t, sess.SessionID, rt)
	if got := r.storedTier(ctx, t, sess.SessionID); got != SessionCertified {
		t.Fatalf("a refused refresh changed the tier to %q", got)
	}
	if got := eventsOf(aud, EventIntegrityRejected); len(got) != 1 || got[0].SessionID != sess.SessionID {
		t.Fatalf("rejected events at refresh %+v", got)
	}
	if _, err = refresh("r4", bound("unlicensed")); err != nil {
		t.Fatal(err)
	}
	if got := r.storedTier(ctx, t, sess.SessionID); got != SessionRestricted {
		t.Fatalf("a restricted verdict at refresh recorded %q", got)
	}
}

// The tier is recorded, not enforced (D31.5): a restricted session still
// enrols and refreshes, and its access token carries nothing about the tier.
func TestIntegrity_TheTierAuthorisesNothingYet(t *testing.T) {
	ctx := context.Background()
	r, _, _ := integrityRig(t, true)
	k := deviceKey(t)
	r.challenge(ctx, t, "req_a")
	proof := enrollProof(t, k, k, "enrol", "POST", r.clock.now())
	sess, token, err := r.svc.ValidateOtpWithDeviceProofAndIntegrity(ctx, "req_a", testOtpCode, "POST", proof, bound("unlicensed")(proof))
	if err != nil || token == "" {
		t.Fatalf("a restricted enrolment was refused: %v", err)
	}
	claims, err := r.tokens.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(claims.Roles, []string{"subscriber"}) {
		t.Fatalf("the access token's roles changed with the tier: %v", claims.Roles)
	}
	if r.storedTier(ctx, t, sess.SessionID) != SessionRestricted {
		t.Fatal("the tier was not recorded")
	}
}

// Repositories store only known tiers, and the zero value as unverified.
func TestIntegrityTier_RepositoriesStoreOnlyKnownTiers(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryIdentityRepository()
	s := &UserSession{SessionID: "sess_x", RefreshTokenHash: HashRefreshToken([]byte("k"), "t"), IntegrityTier: "trusted"}
	if err := repo.SaveSession(ctx, s); !errors.Is(err, ErrInvalidIntegrityTier) {
		t.Fatalf("an unknown tier was stored: %v", err)
	}
	s.IntegrityTier = ""
	if err := repo.SaveSession(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetSession(ctx, "sess_x")
	if got.IntegrityTier != SessionUnverified {
		t.Fatalf("the zero tier was stored as %q, want unverified", got.IntegrityTier)
	}
}
