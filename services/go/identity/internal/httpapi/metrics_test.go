package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/routes"
)

// Every response the API decides is counted once, under its endpoint and
// outcome; refusals included (they were logged nowhere before).
func TestMetrics_EveryResponseIsCountedOnceByEndpointAndOutcome(t *testing.T) {
	r := newRig(t, true)
	k := deviceKey(t)
	sid, rt := r.enrol(t, k) // otp_request success, otp_validate success
	got := r.post(t, routes.Refresh, refreshBody(sid, rt), proof(t, k, k, "m1", "POST", refreshURL, time.Now()))
	if got.code != 200 {
		t.Fatalf("refresh: %d %s", got.code, got.raw)
	}
	r.post(t, routes.Refresh, refreshBody(sid, rt), proof(t, k, k, "m2", "POST", refreshURL, time.Now())) // reuse: invalid_token
	r.post(t, routes.Enrollment, `{"requestId":"nope","code":"123456"}`, "")                              // invalid_input
	reqID, code := r.requestCode(t)                                                                       // otp_request success
	r.post(t, routes.Enrollment, enrolBody(reqID, code), "")                                              // no proof: invalid_dpop_proof
	r.do(t, "GET", routes.Refresh, "", nil)                                                               // method_not_allowed
	r.post(t, routes.OTPRequest+"/"+msisdn, `{"msisdn":"`+msisdn+`"}`, "")                                // not_found

	want := map[[2]string]uint64{
		{EndpointOTPRequest, OutcomeSuccess}:        2,
		{EndpointOTPValidate, OutcomeSuccess}:       1,
		{EndpointRefresh, OutcomeSuccess}:           1,
		{EndpointRefresh, "invalid_token"}:          1,
		{EndpointOTPValidate, "invalid_input"}:      1,
		{EndpointOTPValidate, "invalid_dpop_proof"}: 1,
		{EndpointRefresh, "method_not_allowed"}:     1,
		{EndpointUnknown, "not_found"}:              1,
	}
	var sum uint64
	for labels, n := range want {
		sum += n
		if got := r.m.requests.Value(labels[0], labels[1]); got != n {
			t.Errorf("%v = %d, want %d", labels, got, n)
		}
	}
	if total := r.m.requests.Total(); total != sum || total != uint64(len(r.bodies)) {
		t.Errorf("total = %d; want %d, one per response (%d responses)", total, sum, len(r.bodies))
	}
	// No series carries anything from the request: not the number, not a path.
	for _, s := range r.m.requests.Series() {
		if strings.Contains(s, "91") || strings.Contains(s, "/") || strings.Contains(s, "req_") || strings.Contains(s, "sess_") {
			t.Errorf("series %q carries request data", s)
		}
	}
}

// Without a delivery channel the refusal is counted under its own outcome.
func TestMetrics_DeliveryUnavailableIsCounted(t *testing.T) {
	r := newRig(t, false)
	r.post(t, routes.OTPRequest, `{"msisdn":"`+msisdn+`"}`, "")
	if got := r.m.requests.Value(EndpointOTPRequest, "otp_delivery_unavailable"); got != 1 || r.m.requests.Total() != 1 {
		t.Fatalf("otp_delivery_unavailable = %d of %d, want 1 of 1", got, r.m.requests.Total())
	}
}

// A panic is one internal outcome; a panic AFTER a response was decided is
// not counted a second time.
func TestMetrics_PanicsAreCountedOnce(t *testing.T) {
	m := NewMetrics()
	h := &handler{log: slog.New(slog.DiscardHandler), now: time.Now, m: m}
	serve := func(inner http.HandlerFunc) {
		withDecision(h.recoverer(inner)).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", routes.Refresh, http.NoBody))
	}
	serve(func(http.ResponseWriter, *http.Request) { panic("boom") })
	serve(func(w http.ResponseWriter, r *http.Request) {
		h.problem(w, r, http.StatusUnauthorized, "ERR_INVALID_TOKEN")
		panic("after the response")
	})
	if got := m.requests.Value(EndpointRefresh, "internal"); got != 1 {
		t.Errorf("internal = %d, want 1 (the first panic only)", got)
	}
	if got := m.requests.Value(EndpointRefresh, "invalid_token"); got != 1 {
		t.Errorf("invalid_token = %d, want 1", got)
	}
	if m.requests.Total() != 2 {
		t.Errorf("total = %d, want 2: one per request", m.requests.Total())
	}
}

// Concurrent requests are each counted exactly once (run under -race).
func TestMetrics_ConcurrentRequestsAreCountedExactly(t *testing.T) {
	r := newRig(t, true)
	const n = 64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", routes.Enrollment, strings.NewReader(`{"requestId":"x","code":"1"}`))
			req.Header.Set("Content-Type", "application/json")
			r.h.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	wg.Wait()
	if got := r.m.requests.Value(EndpointOTPValidate, "invalid_input"); got != n || r.m.requests.Total() != n {
		t.Fatalf("invalid_input = %d of %d, want %d of %d", got, r.m.requests.Total(), n, n)
	}
}

type fakeMount struct{ h http.Handler }

func (f *fakeMount) SetMetricsHandler(h http.Handler) { f.h = h }

// MountOn exposes the same registry the handler counts into, in the
// exposition format, with both labels.
func TestMetrics_MountOnExposesTheCounter(t *testing.T) {
	r := newRig(t, true)
	mount := &fakeMount{}
	r.m.MountOn(mount, "identity")
	if mount.h == nil {
		t.Fatal("nothing was mounted")
	}
	r.post(t, routes.Enrollment, `{"requestId":"nope","code":"123456"}`, "")
	rec := httptest.NewRecorder()
	mount.h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", http.NoBody))
	want := MetricRequests + `{endpoint="otp_validate",outcome="invalid_input"} 1`
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("scrape = %d:\n%s\nwant a line %q", rec.Code, rec.Body.String(), want)
	}
}

// The label vocabulary is closed: every problem code maps to a lower-case
// outcome without its prefix, every path outside the three routes is unknown,
// and a nil Metrics counts nothing (and does not panic).
func TestMetrics_VocabularyIsClosed(t *testing.T) {
	for code := range problems {
		o := outcomeFor(code)
		if o == "" || o == OutcomeSuccess || strings.ContainsAny(o, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") || strings.HasPrefix(o, "err_") {
			t.Errorf("%s -> %q", code, o)
		}
	}
	for path, want := range map[string]string{
		routes.OTPRequest: EndpointOTPRequest, routes.Enrollment: EndpointOTPValidate, routes.Refresh: EndpointRefresh,
		"/": EndpointUnknown, "/v1/otp/request/+919876543210": EndpointUnknown, "/metrics": EndpointUnknown,
	} {
		if got := endpointFor(path); got != want {
			t.Errorf("%s -> %s, want %s", path, got, want)
		}
	}
	var none *Metrics
	none.record(httptest.NewRequest("POST", routes.Refresh, http.NoBody), OutcomeSuccess)
}
