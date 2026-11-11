// AI-performed internal security assurance (2026-11-11); not an independent security review. docs/phase19/OD-12-REVISED-DIRECTION-2026-11-11.md §4.

package httpapi

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/routes"
)

// Threat T-h: account enumeration through sign-in. Whether a number already
// has an account must not be visible in the answer to an OTP request or to a
// wrong code: same status, same body shape and values, same headers.

// saRandomHeaders carry per-response values that differ on every request
// whatever the number; only their presence is compared.
var saRandomHeaders = map[string]bool{"Date": true, "Content-Length": true, "X-Correlation-Id": true}

// saHeaderNames returns the canonical header names except Date and
// Content-Length, which are ignored altogether.
func saHeaderNames(h http.Header) []string {
	out := make([]string, 0, len(h))
	for k := range h {
		if k == "Date" || k == "Content-Length" {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// saSameShape fails unless a and b are indistinguishable apart from the
// random request id and correlation id.
func saSameShape(t *testing.T, what string, a, b resp) {
	t.Helper()
	if a.code != b.code {
		t.Errorf("%s: status %d with an account, %d without", what, a.code, b.code)
	}
	if !eq(saHeaderNames(a.hdr), saHeaderNames(b.hdr)) {
		t.Errorf("%s: header names differ:\n%v\n%v", what, saHeaderNames(a.hdr), saHeaderNames(b.hdr))
	}
	for _, k := range saHeaderNames(a.hdr) {
		if !saRandomHeaders[k] && fmt.Sprint(a.hdr.Values(k)) != fmt.Sprint(b.hdr.Values(k)) {
			t.Errorf("%s: header %s differs: %q vs %q", what, k, a.hdr.Values(k), b.hdr.Values(k))
		}
	}
	if !eq(keys(a.body), keys(b.body)) {
		t.Errorf("%s: body keys differ: %v vs %v", what, keys(a.body), keys(b.body))
	}
	for k, av := range a.body {
		switch k {
		case "requestId":
			as, aok := av.(string)
			bs, bok := b.body[k].(string)
			if !aok || !bok || !requestIDPattern.MatchString(as) || !requestIDPattern.MatchString(bs) {
				t.Errorf("%s: requestId shapes differ: %v vs %v", what, av, b.body[k])
			}
		case "traceId", "timestamp":
			// The correlation id (random per response) and the problem's
			// time of writing: both differ on every response whatever the
			// number.
		default:
			if fmt.Sprint(av) != fmt.Sprint(b.body[k]) {
				t.Errorf("%s: %s differs by account existence: %v vs %v", what, k, av, b.body[k])
			}
		}
	}
	// The timestamp is RFC 3339 with trailing zeros trimmed, so its length
	// varies by chance; everything else must be the same length.
	am, bm := saProblemTime.ReplaceAllString(a.raw, `"timestamp":""`), saProblemTime.ReplaceAllString(b.raw, `"timestamp":""`)
	if len(am) != len(bm) {
		t.Errorf("%s: body lengths differ (%d vs %d):\n%s\n%s", what, len(am), len(bm), a.raw, b.raw)
	}
}

var saProblemTime = regexp.MustCompile(`"timestamp":"[^"]*"`)

// TestSecurityAssurance_SignInDoesNotRevealAccountExistence: msisdn has an
// enrolled account, otherNumber has never been seen. POST /v1/otp/request and
// a wrong code at POST /v1/otp/validate answer both identically.
func TestSecurityAssurance_SignInDoesNotRevealAccountExistence(t *testing.T) {
	r := newRig(t, true)
	k := deviceKey(t)
	r.enrol(t, k)

	with := r.post(t, routes.OTPRequest, numberBody(msisdn), "")
	without := r.post(t, routes.OTPRequest, numberBody(otherNumber), "")
	if with.code != http.StatusAccepted {
		t.Fatalf("OTP request for the enrolled number: %d %s", with.code, with.raw)
	}
	saSameShape(t, "POST "+routes.OTPRequest, with, without)

	reqWith, _ := with.body["requestId"].(string)
	reqWithout, _ := without.body["requestId"].(string)
	wrongWith := wrongFor(r.sender.code(msisdn))
	wrongWithout := wrongFor(r.sender.code(otherNumber))
	vWith := r.post(t, routes.Enrollment, enrolBody(reqWith, wrongWith), proof(t, k, k, "sa-enum-1", "POST", enrolURL, time.Now()))
	vWithout := r.post(t, routes.Enrollment, enrolBody(reqWithout, wrongWithout), proof(t, k, k, "sa-enum-2", "POST", enrolURL, time.Now()))
	wantProblem(t, vWith, 401, "ERR_INVALID_OTP")
	wantProblem(t, vWithout, 401, "ERR_INVALID_OTP")
	saSameShape(t, "POST "+routes.Enrollment+" with a wrong code", vWith, vWithout)
}
