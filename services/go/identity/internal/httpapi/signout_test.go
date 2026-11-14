package httpapi

import (
	"strings"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/routes"
)

// POST /v1/session/logout and POST /v1/session/revoke-all.

func TestHTTP_LogoutSignsOutThisDeviceOnly(t *testing.T) {
	r := newRig(t, true)
	k1, k2 := deviceKey(t), deviceKey(t)
	s1, rt1 := r.enrol(t, k1)
	s2, rt2 := r.enrol(t, k2)

	got := r.post(t, routes.Logout, refreshBody(s1, rt1), proof(t, k1, k1, "l1", "POST", logoutURL, time.Now()))
	if got.code != 204 || got.raw != "" {
		t.Fatalf("logout = %d %q, want 204 and no body", got.code, got.raw)
	}
	if cc := got.hdr.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	wantProblem(t, r.post(t, routes.Refresh, refreshBody(s1, rt1), proof(t, k1, k1, "r1", "POST", refreshURL, time.Now())), 401, "ERR_INVALID_TOKEN")
	if other := r.post(t, routes.Refresh, refreshBody(s2, rt2), proof(t, k2, k2, "r2", "POST", refreshURL, time.Now())); other.code != 200 {
		t.Errorf("the other device's refresh = %d %s", other.code, other.raw)
	}
	if again := r.post(t, routes.Logout, refreshBody(s1, rt1), proof(t, k1, k1, "l2", "POST", logoutURL, time.Now())); again.code != 204 {
		t.Errorf("retry = %d %s, want 204", again.code, again.raw)
	}
	if n := r.m.requests.Value(EndpointLogout, OutcomeSuccess); n != 2 {
		t.Errorf("%s success = %d, want 2", EndpointLogout, n)
	}
}

func TestHTTP_RevokeAllSignsOutEveryDeviceAndRetriesSafely(t *testing.T) {
	r := newRig(t, true)
	k1, k2, k3 := deviceKey(t), deviceKey(t), deviceKey(t)
	s1, rt1 := r.enrol(t, k1)
	s2, rt2 := r.enrol(t, k2)

	if got := r.post(t, routes.RevokeAll, refreshBody(s1, rt1), proof(t, k1, k1, "a1", "POST", revokeURL, time.Now())); got.code != 204 || got.raw != "" {
		t.Fatalf("revoke-all = %d %q, want 204 and no body", got.code, got.raw)
	}
	wantProblem(t, r.post(t, routes.Refresh, refreshBody(s2, rt2), proof(t, k2, k2, "r2", "POST", refreshURL, time.Now())), 401, "ERR_INVALID_TOKEN")

	// A device signed in since is untouched by a retry.
	s3, rt3 := r.enrol(t, k3)
	if again := r.post(t, routes.RevokeAll, refreshBody(s1, rt1), proof(t, k1, k1, "a2", "POST", revokeURL, time.Now())); again.code != 204 {
		t.Fatalf("retry = %d %s, want 204", again.code, again.raw)
	}
	if got := r.post(t, routes.Refresh, refreshBody(s3, rt3), proof(t, k3, k3, "r3", "POST", refreshURL, time.Now())); got.code != 200 {
		t.Errorf("a later device after a retry: %d %s, want 200", got.code, got.raw)
	}
	// A session revoked by another device's operation cannot revoke-all.
	wantProblem(t, r.post(t, routes.RevokeAll, refreshBody(s2, rt2), proof(t, k2, k2, "a3", "POST", revokeURL, time.Now())), 401, "ERR_INVALID_TOKEN")
	if n := r.m.requests.Value(EndpointRevokeAll, OutcomeSuccess); n != 2 {
		t.Errorf("%s success = %d, want 2", EndpointRevokeAll, n)
	}
}

// Every refusal is the documented problem, and none signs anything out: a
// superseded token with a refused proof revokes nothing.
func TestHTTP_SignOutRefusalsChangeNothing(t *testing.T) {
	for _, ep := range []struct{ path, url, other string }{
		{routes.Logout, logoutURL, revokeURL},
		{routes.RevokeAll, revokeURL, logoutURL},
	} {
		t.Run(ep.path, func(t *testing.T) {
			r := newRig(t, true)
			k, other := deviceKey(t), deviceKey(t)
			sid, stale := r.enrol(t, k)
			rot := r.post(t, routes.Refresh, refreshBody(sid, stale), proof(t, k, k, "rot", "POST", refreshURL, time.Now()))
			if rot.code != 200 {
				t.Fatalf("refresh: %d %s", rot.code, rot.raw)
			}
			rt := rot.body["refreshToken"].(string)
			first := len(r.bodies)

			wantProblem(t, r.post(t, ep.path, refreshBody("sess_x", rt), proof(t, k, k, "a", "POST", ep.url, time.Now())), 400, "ERR_INVALID_INPUT")
			wantProblem(t, r.post(t, ep.path, `{"sessionId":"`+sid+`"}`, proof(t, k, k, "b", "POST", ep.url, time.Now())), 400, "ERR_INVALID_INPUT")
			for name, p := range map[string]string{
				"missing":                "",
				"for the refresh URL":    proof(t, k, k, "c", "POST", refreshURL, time.Now()),
				"for the other sign-out": proof(t, k, k, "d", "POST", ep.other, time.Now()),
				"by another key":         proof(t, other, other, "e", "POST", ep.url, time.Now()),
			} {
				got := r.post(t, ep.path, refreshBody(sid, stale), p)
				if got.code != 400 || got.body["code"] != "ERR_INVALID_DPOP_PROOF" {
					t.Errorf("%s proof with a superseded token: %d %s", name, got.code, got.raw)
				}
			}
			wantProblem(t, r.post(t, ep.path, refreshBody(sid, strings.Repeat("ab", 32)), proof(t, k, k, "f", "POST", ep.url, time.Now())), 401, "ERR_INVALID_TOKEN")
			wantProblem(t, r.do(t, "GET", ep.path, "", nil), 405, "ERR_METHOD_NOT_ALLOWED")
			big := `{"sessionId":"` + sid + `","refreshToken":"` + strings.Repeat("a", 1100) + `"}`
			wantProblem(t, r.post(t, ep.path, big, proof(t, k, k, "g", "POST", ep.url, time.Now())), 413, "ERR_BODY_TOO_LARGE")

			// Nothing identifying in any sign-out response or any log line.
			for _, s := range append([]string{r.logs.String()}, r.bodies[first:]...) {
				if strings.Contains(s, sid) || strings.Contains(s, stale) || strings.Contains(s, rt) {
					t.Fatalf("a session id or refresh token leaked: %q", s)
				}
			}
			// Nothing was signed out: the current token still refreshes.
			if got := r.post(t, routes.Refresh, refreshBody(sid, rt), proof(t, k, k, "h", "POST", refreshURL, time.Now())); got.code != 200 {
				t.Errorf("refresh after the refusals = %d %s", got.code, got.raw)
			}
		})
	}
}

func TestHTTP_SignOutUnavailableIs503(t *testing.T) {
	r := newRig(t, true)
	k := deviceKey(t)
	sid, rt := r.enrol(t, k)
	r.svc.SetLogoutProofVerifier(nil)
	r.svc.SetRevokeAllProofVerifier(nil)
	wantProblem(t, r.post(t, routes.Logout, refreshBody(sid, rt), proof(t, k, k, "a", "POST", logoutURL, time.Now())), 503, "ERR_TEMPORARILY_UNAVAILABLE")
	wantProblem(t, r.post(t, routes.RevokeAll, refreshBody(sid, rt), proof(t, k, k, "b", "POST", revokeURL, time.Now())), 503, "ERR_TEMPORARILY_UNAVAILABLE")
}
