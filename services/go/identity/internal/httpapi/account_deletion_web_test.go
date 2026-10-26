package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/routes"
)

// Web account deletion over HTTP (app/web_erasure.go): the same 202 for every
// valid number, a code only to a number with an account, 204 on the right
// code and again on a retry, and the documented refusals.

const otherNumber = "+919876500088"

// deletionSender records deletion codes. The service sends after answering,
// so a test waits for the code.
type deletionSender struct {
	mu    sync.Mutex
	codes map[string]string
}

func (d *deletionSender) SendDeletionCode(_ context.Context, number, code string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.codes[number] = code
	return nil
}

func (d *deletionSender) await(t *testing.T, number string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		code, ok := d.codes[number]
		d.mu.Unlock()
		if ok {
			return code
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no deletion code reached %s", number)
	return ""
}

func (d *deletionSender) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.codes)
}

func (r *rig) withWebDeletion(budget int) *deletionSender {
	d := &deletionSender{codes: map[string]string{}}
	r.svc.SetDeletionCodeSender(d)
	r.svc.SetDeletionCodeBudget(budget)
	return d
}

func numberBody(n string) string { return fmt.Sprintf(`{"msisdn":%q}`, n) }

func TestHTTP_WebDeletionUnavailableWithoutConfiguration(t *testing.T) {
	r := newRig(t, true)
	for _, n := range []string{msisdn, otherNumber} {
		wantProblem(t, r.post(t, routes.AccountDeletionCode, numberBody(n), ""), 503, "ERR_OTP_DELIVERY_UNAVAILABLE")
	}
	wantProblem(t, r.post(t, routes.AccountDeletionConfirm, enrolBody("req_"+fmt.Sprintf("%064x", 1), "123456"), ""), 503, "ERR_OTP_DELIVERY_UNAVAILABLE")
}

// The 202 for a number with an account and one without differ only in the
// random request id: same status, same headers that matter, same body keys
// and value shapes. Only the number with an account receives a code.
func TestHTTP_WebDeletionAnswersTheSameWithAndWithoutAnAccount(t *testing.T) {
	r := newRig(t, true)
	r.enrol(t, deviceKey(t))
	d := r.withWebDeletion(100)

	with := r.post(t, routes.AccountDeletionCode, numberBody(msisdn), "")
	without := r.post(t, routes.AccountDeletionCode, numberBody(otherNumber), "")
	for name, got := range map[string]resp{"with an account": with, "without": without} {
		if got.code != http.StatusAccepted || !eq(keys(got.body), []string{"expiresInSeconds", "requestId"}) ||
			!requestIDPattern.MatchString(got.body["requestId"].(string)) || got.body["expiresInSeconds"] != float64(300) {
			t.Fatalf("%s: %d %s", name, got.code, got.raw)
		}
	}
	if len(with.raw) != len(without.raw) || with.hdr.Get("Content-Type") != without.hdr.Get("Content-Type") ||
		with.hdr.Get("Cache-Control") != without.hdr.Get("Cache-Control") {
		t.Errorf("the answers differ beyond the request id:\n%s\n%s", with.raw, without.raw)
	}
	d.await(t, msisdn)
	time.Sleep(50 * time.Millisecond) // a send to the other number, were there one, would have landed
	if d.count() != 1 {
		t.Errorf("%d numbers received a deletion code, want only the one with an account", d.count())
	}
	// The number without an account: every guess is the same refusal as a wrong code.
	wantProblem(t, r.post(t, routes.AccountDeletionConfirm, enrolBody(without.body["requestId"].(string), "123456"), ""), 401, "ERR_INVALID_OTP")
}

// The right code erases the account (its session can no longer refresh); a
// retry with the same request and code is 204 again; a wrong one is refused.
func TestHTTP_WebDeletionErasesAndAnswersRetries(t *testing.T) {
	r := newRig(t, true)
	k := deviceKey(t)
	sid, rt := r.enrol(t, k)
	d := r.withWebDeletion(100)

	start := r.post(t, routes.AccountDeletionCode, numberBody(msisdn), "")
	reqID := start.body["requestId"].(string)
	code := d.await(t, msisdn)
	wrong := map[bool]string{true: "000000", false: "111111"}[code != "000000"]
	wantProblem(t, r.post(t, routes.AccountDeletionConfirm, enrolBody(reqID, wrong), ""), 401, "ERR_INVALID_OTP")

	got := r.post(t, routes.AccountDeletionConfirm, enrolBody(reqID, code), "")
	if got.code != 204 || got.raw != "" || got.hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("confirm = %d %q", got.code, got.raw)
	}
	wantProblem(t, r.post(t, routes.Refresh, refreshBody(sid, rt), proof(t, k, k, "after-web", "POST", refreshURL, time.Now())), 401, "ERR_INVALID_TOKEN")
	if again := r.post(t, routes.AccountDeletionConfirm, enrolBody(reqID, code), ""); again.code != 204 {
		t.Errorf("retry = %d %s, want 204", again.code, again.raw)
	}
	wantProblem(t, r.post(t, routes.AccountDeletionConfirm, enrolBody(reqID, wrong), ""), 401, "ERR_INVALID_OTP")
}

func TestHTTP_WebDeletionRefusals(t *testing.T) {
	r := newRig(t, true)
	r.withWebDeletion(100)
	wantProblem(t, r.post(t, routes.AccountDeletionCode, numberBody("919876543210"), ""), 400, "ERR_INVALID_MSISDN")
	wantProblem(t, r.post(t, routes.AccountDeletionCode, `{}`, ""), 400, "ERR_INVALID_MSISDN")
	wantProblem(t, r.post(t, routes.AccountDeletionConfirm, enrolBody("req_x", "123456"), ""), 400, "ERR_INVALID_INPUT")
	wantProblem(t, r.post(t, routes.AccountDeletionConfirm, enrolBody("req_"+fmt.Sprintf("%064x", 2), "12345"), ""), 400, "ERR_INVALID_INPUT")
	wantProblem(t, r.do(t, "GET", routes.AccountDeletionCode, "", nil), 405, "ERR_METHOD_NOT_ALLOWED")

	// The number's request allowance is shared with sign-in: past it, 429.
	for range 3 {
		_ = r.post(t, routes.AccountDeletionCode, numberBody(otherNumber), "")
	}
	got := r.post(t, routes.AccountDeletionCode, numberBody(otherNumber), "")
	wantProblem(t, got, 429, "ERR_RATE_LIMITED")
	if got.hdr.Get("Retry-After") == "" {
		t.Error("429 without Retry-After")
	}
	wantProblem(t, r.post(t, routes.OTPRequest, numberBody(otherNumber), ""), 429, "ERR_RATE_LIMITED")

	// The hourly budget, spent, is 503 for every number.
	r2 := newRig(t, true)
	r2.withWebDeletion(1)
	_ = r2.post(t, routes.AccountDeletionCode, numberBody(otherNumber), "")
	wantProblem(t, r2.post(t, routes.AccountDeletionCode, numberBody(msisdn), ""), 503, "ERR_OTP_DELIVERY_UNAVAILABLE")
}
