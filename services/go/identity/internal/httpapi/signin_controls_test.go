package httpapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/routes"
)

// The F-1 sign-in delivery controls through HTTP: a number off the tester
// list, and an allowlisted number once the hourly cap is spent, get exactly
// the answer a deployment with no delivery gives, and no response or log
// line carries a number or a code.

// comparableProblem is the problem body without the two fields that differ
// between any two responses: the trace id and the timestamp. Every other
// field must match exactly, and no field may be added.
func comparableProblem(got resp) string {
	fields := map[string]any{}
	for k, v := range got.body {
		if k != "traceId" && k != "timestamp" {
			fields[k] = v
		}
	}
	b, err := json.Marshal(fields) // map keys are sorted
	if err != nil {
		return "unencodable: " + err.Error()
	}
	return string(b)
}

func TestHTTP_SignInControls_RefusalsMatchDeliveryUnavailable(t *testing.T) {
	// The service's clock is the real one here: keep the test inside one
	// hour, so the cap cannot reset part-way.
	if left := time.Until(time.Now().Truncate(time.Hour).Add(time.Hour)); left < 10*time.Second {
		time.Sleep(left + time.Second)
	}

	// The reference: a deployment with no OTP delivery at all.
	none := newRig(t, false)
	reference := none.post(t, routes.OTPRequest, numberBody("+919000000099"), "")
	wantProblem(t, reference, 503, "ERR_OTP_DELIVERY_UNAVAILABLE")

	const hourlyCap = 3
	testers := []string{"+919000000001", "+919000000002", "+919000000003", "+919000000004", "+919000000005"}
	outsiders := []string{"+919876543210", "+918765432109", "+14155552671"}
	r := newRig(t, true)
	if err := r.svc.SetSignInDeliveryControls(testers, hourlyCap); err != nil {
		t.Fatal(err)
	}
	sweep := &saSweep{t: t}
	same := func(got resp, what string) {
		t.Helper()
		wantProblem(t, got, 503, "ERR_OTP_DELIVERY_UNAVAILABLE")
		if comparableProblem(got) != comparableProblem(reference) {
			t.Errorf("%s: body %s differs from no delivery's %s", what, got.raw, reference.raw)
		}
		if got.hdr.Get("Retry-After") != "" {
			t.Errorf("%s: a Retry-After header", what)
		}
	}

	// Off the list: refused before anything, any number of times.
	for _, n := range outsiders {
		for i := 0; i < 4; i++ {
			same(sweep.note(r.post(t, routes.OTPRequest, numberBody(n), "")), "a number off the list")
		}
	}
	if r.repo.saves != 0 || len(r.sender.codes) != 0 {
		t.Fatalf("a refused number created %d challenges and %d sends", r.repo.saves, len(r.sender.codes))
	}

	// On the list: delivered until the cap, then refused the same way.
	for i, n := range testers {
		got := sweep.note(r.post(t, routes.OTPRequest, numberBody(n), ""))
		if i < hourlyCap {
			if got.code != 202 || got.body["requestId"] == nil {
				t.Fatalf("tester %d: %d %s, want 202", i+1, got.code, got.raw)
			}
			continue
		}
		same(got, "a tester over the cap")
	}
	if len(r.sender.codes) != hourlyCap {
		t.Errorf("%d codes sent, want %d", len(r.sender.codes), hourlyCap)
	}
	if r.repo.saves != len(testers) || r.repo.deletes != len(testers)-hourlyCap {
		t.Errorf("%d challenges saved and %d withdrawn, want %d and %d", r.repo.saves, r.repo.deletes, len(testers), len(testers)-hourlyCap)
	}

	// Nothing carries a number or a code: not the responses, their headers,
	// or the log.
	for _, n := range append(append([]string{}, testers...), outsiders...) {
		sweep.secrets = append(sweep.secrets, n, n[3:])
	}
	for _, n := range testers[:hourlyCap] {
		if c := r.sender.code(n); c != "" {
			sweep.secrets = append(sweep.secrets, c)
		}
	}
	sweep.check(r.logs.String())
}
