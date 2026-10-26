package httpapi

import (
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/routes"
)

// POST /v1/account/delete: 204 once the account is erased, and again for a
// retry by the same device; every refusal is the documented problem.
func TestHTTP_AccountDeletionErasesAndAnswersRetries(t *testing.T) {
	r := newRig(t, true)
	k := deviceKey(t)
	sid, rt := r.enrol(t, k)

	got := r.post(t, routes.AccountDeletion, refreshBody(sid, rt), proof(t, k, k, "d1", "POST", deleteURL, time.Now()))
	if got.code != 204 || got.raw != "" {
		t.Fatalf("delete = %d %q, want 204 and no body", got.code, got.raw)
	}
	if cc := got.hdr.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	// The session is gone: refreshing it is refused.
	wantProblem(t, r.post(t, routes.Refresh, refreshBody(sid, rt), proof(t, k, k, "r1", "POST", refreshURL, time.Now())), 401, "ERR_INVALID_TOKEN")
	// A retry by the same device is told "done"; any other key is not.
	if again := r.post(t, routes.AccountDeletion, refreshBody(sid, rt), proof(t, k, k, "d2", "POST", deleteURL, time.Now())); again.code != 204 {
		t.Errorf("retry = %d %s, want 204", again.code, again.raw)
	}
	other := deviceKey(t)
	wantProblem(t, r.post(t, routes.AccountDeletion, refreshBody(sid, rt), proof(t, other, other, "d3", "POST", deleteURL, time.Now())), 401, "ERR_INVALID_TOKEN")
}

func TestHTTP_AccountDeletionRefusals(t *testing.T) {
	r := newRig(t, true)
	k := deviceKey(t)
	sid, rt := r.enrol(t, k)
	other := deviceKey(t)

	wantProblem(t, r.post(t, routes.AccountDeletion, refreshBody("sess_x", rt), proof(t, k, k, "a", "POST", deleteURL, time.Now())), 400, "ERR_INVALID_INPUT")
	wantProblem(t, r.post(t, routes.AccountDeletion, refreshBody(sid, rt), ""), 400, "ERR_INVALID_DPOP_PROOF")
	wantProblem(t, r.post(t, routes.AccountDeletion, refreshBody(sid, rt), proof(t, k, k, "b", "POST", refreshURL, time.Now())), 400, "ERR_INVALID_DPOP_PROOF")
	wantProblem(t, r.post(t, routes.AccountDeletion, refreshBody(sid, rt), proof(t, other, other, "c", "POST", deleteURL, time.Now())), 400, "ERR_INVALID_DPOP_PROOF")
	wrong := rt[:len(rt)-1] + map[bool]string{true: "0", false: "1"}[rt[len(rt)-1] != '0']
	wantProblem(t, r.post(t, routes.AccountDeletion, refreshBody(sid, wrong), proof(t, k, k, "d", "POST", deleteURL, time.Now())), 401, "ERR_INVALID_TOKEN")
	wantProblem(t, r.do(t, "GET", routes.AccountDeletion, "", nil), 405, "ERR_METHOD_NOT_ALLOWED")

	// After every refusal the account is intact: a correct request still works.
	if got := r.post(t, routes.AccountDeletion, refreshBody(sid, rt), proof(t, k, k, "e", "POST", deleteURL, time.Now())); got.code != 204 {
		t.Fatalf("delete after refusals = %d %s", got.code, got.raw)
	}

	r.svc.SetAccountDeletionProofVerifier(nil)
	// Re-enrolling the erased number: a sign-in that begins at the erasure's
	// own clock reading counts as in flight (app.UserSession.ErasureFence),
	// and a coarse clock (Windows) can give both one reading.
	for start := time.Now(); !time.Now().After(start); {
		time.Sleep(time.Millisecond)
	}
	sid2, rt2 := r.enrol(t, k)
	wantProblem(t, r.post(t, routes.AccountDeletion, refreshBody(sid2, rt2), proof(t, k, k, "f", "POST", deleteURL, time.Now())), 503, "ERR_TEMPORARILY_UNAVAILABLE")
}
