// AI-performed internal security assurance (2026-11-11); not an independent security review. docs/phase19/OD-12-REVISED-DIRECTION-2026-11-11.md §4.

package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/routes"
)

// Threat T-l: a code that is not exactly six ASCII digits reaching the code
// comparison, where it would spend an attempt (or a DPoP proof) or exercise
// Unicode digit folding. Both code-taking endpoints must refuse it at the
// edge: 400 ERR_INVALID_INPUT, attempt counter unchanged, proof unspent.

// saMalformedCodes are rejected by codePattern (^\d{6}$, where \d is ASCII
// only in Go's RE2 and $ matches only at the very end of the text).
var saMalformedCodes = map[string]string{
	"seven digits":            "1234567",
	"Arabic-Indic digits":     "١٣٥٧٩٠",
	"fullwidth digits":        "１２３４５６",
	"leading space":           " 123456",
	"five digits and newline": "12345\n",
	"six digits and newline":  "123456\n",
	"inner space":             "123 456",
	"empty":                   "",
}

// saCodeBody is the {"requestId","code"} body, JSON-encoded so that every
// code above travels exactly as written.
func saCodeBody(t *testing.T, reqID, code string) string {
	t.Helper()
	b, err := json.Marshal(struct {
		RequestID string `json:"requestId"`
		Code      string `json:"code"`
	}{reqID, code})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (r *rig) saAttempts(t *testing.T, reqID string) int {
	t.Helper()
	ch, err := r.repo.GetOtp(context.Background(), reqID)
	if err != nil {
		t.Fatalf("challenge %s: %v", reqID, err)
	}
	return ch.Attempts
}

// TestSecurityAssurance_ValidateRefusesNonASCIISixDigitCodes covers
// POST /v1/otp/validate. One DPoP proof is presented with every malformed
// code and is still accepted afterwards with the right one.
func TestSecurityAssurance_ValidateRefusesNonASCIISixDigitCodes(t *testing.T) {
	r := newRig(t, true)
	reqID, code := r.requestCode(t)
	k := deviceKey(t)
	p := proof(t, k, k, "sa-input", "POST", enrolURL, time.Now())

	for name, bad := range saMalformedCodes {
		wantProblem(t, r.post(t, routes.Enrollment, saCodeBody(t, reqID, bad), p), 400, "ERR_INVALID_INPUT")
		if n := r.saAttempts(t, reqID); n != 0 {
			t.Fatalf("%s: the challenge's attempt counter is %d, want 0", name, n)
		}
	}
	if r.repo.sessionCount() != 0 {
		t.Fatal("a malformed code created a session")
	}
	if got := r.post(t, routes.Enrollment, saCodeBody(t, reqID, code), p); got.code != 200 {
		t.Errorf("after the malformed codes, the right code with the same proof: %d %s", got.code, got.raw)
	}
}

// TestSecurityAssurance_DeletionConfirmRefusesNonASCIISixDigitCodes covers
// POST /v1/account/deletion/confirm (no proof on this endpoint): the
// account-deletion challenge's attempt counter is untouched, and the right
// code still erases.
func TestSecurityAssurance_DeletionConfirmRefusesNonASCIISixDigitCodes(t *testing.T) {
	r := newRig(t, true)
	r.enrol(t, deviceKey(t))
	d := r.withWebDeletion(100)
	start := r.post(t, routes.AccountDeletionCode, numberBody(msisdn), "")
	if start.code != 202 {
		t.Fatalf("deletion code: %d %s", start.code, start.raw)
	}
	reqID := start.body["requestId"].(string)
	code := d.await(t, msisdn)

	for name, bad := range saMalformedCodes {
		wantProblem(t, r.post(t, routes.AccountDeletionConfirm, saCodeBody(t, reqID, bad), ""), 400, "ERR_INVALID_INPUT")
		if n := r.saAttempts(t, reqID); n != 0 {
			t.Fatalf("%s: the deletion challenge's attempt counter is %d, want 0", name, n)
		}
	}
	if got := r.post(t, routes.AccountDeletionConfirm, saCodeBody(t, reqID, code), ""); got.code != 204 {
		t.Errorf("after the malformed codes, the right code: %d %s", got.code, got.raw)
	}
}
