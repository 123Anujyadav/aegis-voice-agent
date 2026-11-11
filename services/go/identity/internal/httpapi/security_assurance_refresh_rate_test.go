// AI-performed internal security assurance (2026-11-11); not an independent security review. docs/phase19/OD-12-REVISED-DIRECTION-2026-11-11.md §4.

package httpapi

import (
	"fmt"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/routes"
)

// Threat T-w: refresh flooding. A CHARACTERIZATION test, not a statement of
// desired behaviour: it records that POST /v1/token/refresh has NO rate
// limiter today. Every refresh needs the current refresh token AND a fresh
// DPoP proof by the enrolled device key, so only the key holder can drive it,
// but nothing bounds how often. This is a recorded gap (production readiness:
// per-source limiting, C13/E6). When a limiter is added, this test is
// expected to fail and should be replaced by one that pins the new limit.

// TestSecurityAssurance_DocumentsNoRefreshRateLimit: twenty back-to-back
// valid refreshes of one session all succeed, each rotating the token.
func TestSecurityAssurance_DocumentsNoRefreshRateLimit(t *testing.T) {
	const refreshes = 20
	r := newRig(t, true)
	k := deviceKey(t)
	sid, rt := r.enrol(t, k)
	seen := map[string]bool{rt: true}
	for i := 1; i <= refreshes; i++ {
		got := r.post(t, routes.Refresh, refreshBody(sid, rt), proof(t, k, k, fmt.Sprintf("sa-rate-%d", i), "POST", refreshURL, time.Now()))
		if got.code != 200 {
			t.Fatalf("refresh %d of %d: %d %s (a refresh limit now exists: replace this characterization test)", i, refreshes, got.code, got.raw)
		}
		next, ok := got.body["refreshToken"].(string)
		if !ok || seen[next] {
			t.Fatalf("refresh %d did not rotate to a fresh token: %s", i, got.raw)
		}
		seen[next] = true
		rt = next
	}
	if got := r.post(t, routes.Refresh, refreshBody(sid, rt), proof(t, k, k, "sa-rate-last", "POST", refreshURL, time.Now())); got.code != 200 {
		t.Errorf("the current token after %d refreshes: %d %s", refreshes, got.code, got.raw)
	}
}
