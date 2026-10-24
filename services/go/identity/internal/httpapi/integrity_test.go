package httpapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/routes"
)

// ADR-0031 over HTTP: the Integrity-Token header. The verifier is a test
// double; no verdict here is real.

// httpVerifier decodes "<kind>|<requestHash>": "good" is a genuine verdict
// bound to requestHash, "reject" a token Google rejects.
type httpVerifier struct{}

func (httpVerifier) Decode(_ context.Context, token string) (app.IntegrityVerdict, error) {
	kind, hash, _ := strings.Cut(token, "|")
	if kind != "good" {
		return app.IntegrityVerdict{}, app.ErrIntegrityTokenInvalid
	}
	return app.IntegrityVerdict{
		RequestPackageName: "com.example.callscreen", RequestHash: hash, Timestamp: time.Now().Add(-time.Second),
		AppRecognition: app.AppPlayRecognized, AppPackageName: "com.example.callscreen",
		CertificateDigests: []string{strings.Repeat("A", 43)},
		DeviceRecognition:  []string{app.DeviceMeetsIntegrity}, AppLicensing: app.AppLicensingLicensed,
	}, nil
}

func withIntegrity(t *testing.T, r *rig) *rig {
	t.Helper()
	err := r.svc.SetIntegrityVerifier(httpVerifier{}, app.IntegrityPolicy{
		PackageName: "com.example.callscreen", CertificateDigests: []string{strings.Repeat("A", 43)},
		MaxAge: 2 * time.Minute, FutureSkew: 30 * time.Second, DecodeTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) tierOf(t *testing.T, sessionID string) app.SessionIntegrityTier {
	t.Helper()
	s, err := r.repo.GetSession(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return s.IntegrityTier
}

// A repeated, empty or oversized Integrity-Token header is refused before
// anything is verified or spent: the same code and proof then enrol.
func TestHTTP_IntegrityTokenHeaderIsCheckedBeforeAnythingIsSpent(t *testing.T) {
	r := withIntegrity(t, newRig(t, true))
	k := deviceKey(t)
	reqID, code := r.requestCode(t)
	p := proof(t, k, k, "enrol-1", "POST", enrolURL, time.Now())
	for name, vals := range map[string][]string{
		"repeated":  {"good|x", "good|y"},
		"empty":     {""},
		"oversized": {strings.Repeat("a", app.MaxIntegrityTokenBytes+1)},
	} {
		got := r.do(t, "POST", routes.Enrollment, enrolBody(reqID, code), map[string][]string{"DPoP": {p}, IntegrityTokenHeader: vals})
		wantProblem(t, got, 400, "ERR_INVALID_INTEGRITY_TOKEN")
		noCredentials(t, got)
		if t.Failed() {
			t.Fatalf("%s", name)
		}
	}
	if n := r.repo.sessionCount(); n != 0 {
		t.Fatalf("%d sessions after refused headers", n)
	}
	token := "good|" + app.IntegrityRequestHash(p)
	got := r.do(t, "POST", routes.Enrollment, enrolBody(reqID, code), map[string][]string{"DPoP": {p}, IntegrityTokenHeader: {token}})
	if got.code != 200 {
		t.Fatalf("the unspent code and proof did not enrol: %d %s", got.code, got.raw)
	}
	if tier := r.tierOf(t, got.body["sessionId"].(string)); tier != app.SessionCertified {
		t.Fatalf("tier %q, want certified", tier)
	}
}

// Without the header, enrolment proceeds and the session is unverified.
func TestHTTP_EnrolmentWithoutAnIntegrityTokenIsUnverified(t *testing.T) {
	r := withIntegrity(t, newRig(t, true))
	sid, _ := r.enrol(t, deviceKey(t))
	if tier := r.tierOf(t, sid); tier != app.SessionUnverified {
		t.Fatalf("tier %q, want unverified", tier)
	}
}

// A token the verifier rejects refuses enrolment with 400 and no credential,
// creates no session, and is counted under its own outcome. The token never
// reaches a log or a response body.
func TestHTTP_ARejectedIntegrityTokenRefusesEnrolment(t *testing.T) {
	r := withIntegrity(t, newRig(t, true))
	k := deviceKey(t)
	reqID, code := r.requestCode(t)
	p := proof(t, k, k, "enrol-1", "POST", enrolURL, time.Now())
	token := "forged-token-" + strings.Repeat("z", 40)
	got := r.do(t, "POST", routes.Enrollment, enrolBody(reqID, code), map[string][]string{"DPoP": {p}, IntegrityTokenHeader: {token}})
	wantProblem(t, got, 400, "ERR_INVALID_INTEGRITY_TOKEN")
	noCredentials(t, got)
	if n := r.repo.sessionCount(); n != 0 {
		t.Fatalf("%d sessions after a rejected token", n)
	}
	if got := r.m.requests.Value(EndpointOTPValidate, "invalid_integrity_token"); got != 1 {
		t.Fatalf("invalid_integrity_token responses = %d, want 1", got)
	}
	for _, b := range append(r.bodies, r.logs.String()) {
		if strings.Contains(b, token) {
			t.Fatal("the integrity token was echoed or logged")
		}
	}
}

// At refresh, a rejected token is 400 and rotates nothing: the presented
// refresh token still works.
func TestHTTP_ARejectedIntegrityTokenAtRefreshRotatesNothing(t *testing.T) {
	r := withIntegrity(t, newRig(t, true))
	k := deviceKey(t)
	sid, rt := r.enrol(t, k)
	p := proof(t, k, k, "r1", "POST", refreshURL, time.Now())
	got := r.do(t, "POST", routes.Refresh, refreshBody(sid, rt), map[string][]string{"DPoP": {p}, IntegrityTokenHeader: {"reject|x"}})
	wantProblem(t, got, 400, "ERR_INVALID_INTEGRITY_TOKEN")
	noCredentials(t, got)
	p2 := proof(t, k, k, "r2", "POST", refreshURL, time.Now())
	token := "good|" + app.IntegrityRequestHash(p2)
	if got := r.do(t, "POST", routes.Refresh, refreshBody(sid, rt), map[string][]string{"DPoP": {p2}, IntegrityTokenHeader: {token}}); got.code != 200 {
		t.Fatalf("the refresh token was spent by the refused refresh: %d %s", got.code, got.raw)
	}
	if tier := r.tierOf(t, sid); tier != app.SessionCertified {
		t.Fatalf("tier %q after a certified refresh", tier)
	}
}

// The integrity audit events are counted under their own types, not "other",
// and an enrolment without a token is counted as not_presented.
func TestMetrics_IntegrityEventsAreCountedByType(t *testing.T) {
	r := withIntegrity(t, newRig(t, true))
	r.m.Instrument(r.svc)
	k := deviceKey(t)
	reqID, code := r.requestCode(t)
	p := proof(t, k, k, "enrol-1", "POST", enrolURL, time.Now())
	r.do(t, "POST", routes.Enrollment, enrolBody(reqID, code), map[string][]string{"DPoP": {p}, IntegrityTokenHeader: {"reject|x"}})
	if got := r.m.security.Value(string(app.EventIntegrityRejected)); got != 1 {
		t.Fatalf("rejected events = %d, want 1", got)
	}
	if got := r.m.security.Value(SecurityEventOther); got != 0 {
		t.Fatalf("an integrity event was counted as other (%d)", got)
	}
	r.enrol(t, deviceKey(t))
	if got := r.m.integrity.Value(string(app.IntegrityNotPresented)); got != 1 {
		t.Fatalf("not_presented = %d, want 1", got)
	}
	m := NewMetrics()
	_ = m.Record(app.SecurityEvent{Type: app.EventIntegrityRestricted})
	if m.security.Value(string(app.EventIntegrityRestricted)) != 1 {
		t.Fatal("a restricted event was not counted under its type")
	}
}
