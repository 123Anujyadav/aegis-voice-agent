// AI-performed internal security assurance (2026-11-11); not an independent security review. docs/phase19/OD-12-REVISED-DIRECTION-2026-11-11.md §4.

package httpapi

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/routes"
)

// Threat T-x: the phone number or a one-time code leaking through the web
// account-deletion endpoints' responses or logs, on any path: success, wrong
// code, malformed input, rate limit (429), budget spent (503) and a failing
// deletion-code provider. TestHTTP_NothingSensitiveLeaksIntoErrorsOrLogs
// covers sign-in and refresh; this extends the sweep to
// POST /v1/account/deletion/code and POST /v1/account/deletion/confirm.

// saRequestIDs are the random request ids a 202 legitimately carries. They
// are masked before scanning, so a code's six digits occurring by chance
// inside 64 random hex characters cannot be mistaken for a leak.
var saRequestIDs = regexp.MustCompile(`req_[0-9a-f]{64}`)

// failingDeletionSender is a provider that refuses, quoting the number as a
// careless provider error might.
type failingDeletionSender struct{ calls chan string }

func (f *failingDeletionSender) SendDeletionCode(_ context.Context, number, code string) error {
	f.calls <- code
	return errors.New("provider refused " + number + " code " + code)
}

// saForget drops the recorded code for number, so the next await returns the
// next code sent rather than the previous one.
func (d *deletionSender) saForget(number string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.codes, number)
}

type saSweep struct {
	t       *testing.T
	secrets []string
	seen    []resp
}

func (s *saSweep) note(got resp) resp {
	s.seen = append(s.seen, got)
	return got
}

func (s *saSweep) check(logs string) {
	s.t.Helper()
	for i, got := range s.seen {
		body := saRequestIDs.ReplaceAllString(got.raw, "req_<masked>")
		var hdr strings.Builder
		for k, vs := range got.hdr {
			fmt.Fprintf(&hdr, "%s: %s\n", k, strings.Join(vs, ","))
		}
		for _, sec := range s.secrets {
			if strings.Contains(body, sec) {
				s.t.Errorf("response %d (%d) body carries %q: %s", i, got.code, sec, got.raw)
			}
			if strings.Contains(hdr.String(), sec) {
				s.t.Errorf("response %d (%d) headers carry %q", i, got.code, sec)
			}
		}
	}
	for _, sec := range s.secrets {
		if strings.Contains(logs, sec) {
			s.t.Errorf("the log carries %q", sec)
		}
	}
}

// saAwaitWithdrawn waits until the undelivered challenge reqID is gone: the
// service withdraws it after the failing send returns, asynchronously.
func saAwaitWithdrawn(t *testing.T, r *rig, reqID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := r.repo.GetOtp(context.Background(), reqID); err != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the undelivered deletion challenge was not withdrawn")
}

func saNumberSecrets(number string) []string {
	return []string{number, strings.TrimPrefix(number, "+"), number[len(number)-10:]}
}

// TestSecurityAssurance_DeletionEndpointsLeakNoNumberOrCode drives every
// reachable outcome of the two web-deletion endpoints for an enrolled number
// and scans every response (body and headers) and the whole log for the
// number (with and without "+", and its last ten digits) and every code sent.
func TestSecurityAssurance_DeletionEndpointsLeakNoNumberOrCode(t *testing.T) {
	r := newRig(t, true)
	r.enrol(t, deviceKey(t)) // OTP request 1 of 3 for msisdn
	signInCode := r.sender.code(msisdn)
	d := r.withWebDeletion(100)
	s := &saSweep{t: t}
	reqIDOf := func(got resp) string {
		t.Helper()
		id, ok := got.body["requestId"].(string)
		if got.code != 202 || !ok {
			t.Fatalf("deletion code: %d %s", got.code, got.raw)
		}
		return id
	}

	// Challenge A: three wrong codes exhaust it (failures 1-3).
	reqA := reqIDOf(s.note(r.post(t, routes.AccountDeletionCode, numberBody(msisdn), ""))) // request 2 of 3
	codeA := d.await(t, msisdn)
	d.saForget(msisdn)
	for range 3 {
		wantProblem(t, s.note(r.post(t, routes.AccountDeletionConfirm, enrolBody(reqA, wrongFor(codeA)), "")), 401, "ERR_INVALID_OTP")
	}

	// Malformed input carrying the number: refused without echo.
	wantProblem(t, s.note(r.post(t, routes.AccountDeletionCode, numberBody(strings.TrimPrefix(msisdn, "+")), "")), 400, "ERR_INVALID_MSISDN")
	wantProblem(t, s.note(r.post(t, routes.AccountDeletionConfirm, enrolBody(reqA, strings.TrimPrefix(msisdn, "+")), "")), 400, "ERR_INVALID_INPUT")
	wantProblem(t, s.note(r.post(t, routes.AccountDeletionConfirm, enrolBody("req_"+msisdn, codeA), "")), 400, "ERR_INVALID_INPUT")
	// An unknown request id with a well-formed code.
	wantProblem(t, s.note(r.post(t, routes.AccountDeletionConfirm, enrolBody("req_"+strings.Repeat("0", 64), codeA), "")), 401, "ERR_INVALID_OTP")

	// Challenge B: two wrong codes are failures 4 and 5, locking the number.
	reqB := reqIDOf(s.note(r.post(t, routes.AccountDeletionCode, numberBody(msisdn), ""))) // request 3 of 3
	codeB := d.await(t, msisdn)
	for range 2 {
		wantProblem(t, s.note(r.post(t, routes.AccountDeletionConfirm, enrolBody(reqB, wrongFor(codeB)), "")), 401, "ERR_INVALID_OTP")
	}
	// 429 on both endpoints: the right code during the lockout, and a fourth request.
	wantProblem(t, s.note(r.post(t, routes.AccountDeletionConfirm, enrolBody(reqB, codeB), "")), 429, "ERR_RATE_LIMITED")
	wantProblem(t, s.note(r.post(t, routes.AccountDeletionCode, numberBody(msisdn), "")), 429, "ERR_RATE_LIMITED")

	// 503: the hourly budget is spent (a second rig, budget 1).
	r2 := newRig(t, true)
	r2.enrol(t, deviceKey(t))
	r2.withWebDeletion(1)
	s.note(r2.post(t, routes.AccountDeletionCode, numberBody(otherNumber), ""))
	wantProblem(t, s.note(r2.post(t, routes.AccountDeletionCode, numberBody(msisdn), "")), 503, "ERR_OTP_DELIVERY_UNAVAILABLE")
	wantProblem(t, s.note(r2.post(t, routes.AccountDeletionConfirm, enrolBody("req_"+strings.Repeat("1", 64), "123456"), "")), 401, "ERR_INVALID_OTP")

	// A failing provider (a third rig): the request is still 202, the
	// undelivered challenge is withdrawn, and the provider's error text, which
	// quotes the number and the code, goes nowhere.
	r3 := newRig(t, true)
	r3.enrol(t, deviceKey(t))
	failing := &failingDeletionSender{calls: make(chan string, 1)}
	r3.svc.SetDeletionCodeSender(failing)
	r3.svc.SetDeletionCodeBudget(100)
	reqC := reqIDOf(s.note(r3.post(t, routes.AccountDeletionCode, numberBody(msisdn), "")))
	codeC := <-failing.calls
	saAwaitWithdrawn(t, r3, reqC)
	wantProblem(t, s.note(r3.post(t, routes.AccountDeletionConfirm, enrolBody(reqC, codeC), "")), 401, "ERR_INVALID_OTP")

	s.secrets = append(saNumberSecrets(msisdn), codeA, codeB, codeC, signInCode, r2.sender.code(msisdn), r3.sender.code(msisdn))
	s.secrets = append(s.secrets, saNumberSecrets(otherNumber)...)
	s.check(r.logs.String() + r2.logs.String() + r3.logs.String())
	if r2.sender.code(msisdn) == "" || r3.sender.code(msisdn) == "" {
		t.Fatal("the enrolments did not run as expected")
	}
}
