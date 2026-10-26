package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/otpdelivery"
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

// The security-event counter counts each audit event under its type only;
// an unknown type is "other"; a nil Metrics counts nothing and never fails.
func TestMetrics_SecurityEventsAreCountedByTypeOnly(t *testing.T) {
	m := NewMetrics()
	for _, e := range []app.SecurityEvent{
		{Type: app.EventOtpVerificationFailed, RequestID: "req_secret"},
		{Type: app.EventOtpVerificationFailed},
		{Type: app.EventOtpLockout},
		{Type: app.EventRefreshTokenReused, SessionID: "sess_secret"},
		{Type: "identity.something.new"},
	} {
		if err := m.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	for typ, n := range map[string]uint64{
		string(app.EventOtpVerificationFailed): 2, string(app.EventOtpLockout): 1,
		string(app.EventRefreshTokenReused): 1, SecurityEventOther: 1,
	} {
		if got := m.security.Value(typ); got != n {
			t.Errorf("%s = %d, want %d", typ, got, n)
		}
	}
	for _, s := range m.security.Series() {
		if strings.Contains(s, "secret") {
			t.Errorf("series %q carries event data", s)
		}
	}
	var none *Metrics
	if err := none.Record(app.SecurityEvent{Type: app.EventOtpLockout}); err != nil {
		t.Fatal(err)
	}
}

// Through the API: once Instrument makes Metrics the service's auditor, a
// refresh-token reuse over HTTP is counted as a security event, alongside the
// response counter's invalid_token.
func TestMetrics_InstrumentCountsReuseOverHTTP(t *testing.T) {
	r := newRig(t, true)
	r.m.Instrument(r.svc)
	k := deviceKey(t)
	sid, rt := r.enrol(t, k)
	if got := r.post(t, routes.Refresh, refreshBody(sid, rt), proof(t, k, k, "s1", "POST", refreshURL, time.Now())); got.code != 200 {
		t.Fatalf("refresh: %d %s", got.code, got.raw)
	}
	r.post(t, routes.Refresh, refreshBody(sid, rt), proof(t, k, k, "s2", "POST", refreshURL, time.Now()))
	if got := r.m.security.Value(string(app.EventRefreshTokenReused)); got != 1 {
		t.Fatalf("reuse events = %d, want 1", got)
	}
	if got := r.m.requests.Value(EndpointRefresh, "invalid_token"); got != 1 {
		t.Fatalf("invalid_token responses = %d, want 1", got)
	}
	if r.svc.AuditFailures() != 0 {
		t.Fatalf("audit failures = %d", r.svc.AuditFailures())
	}
}

// Instrument makes Play Integrity outcomes observable: with no verifier
// configured (production today) every call is "unavailable".
func TestMetrics_IntegrityOutcomesAreCounted(t *testing.T) {
	r := newRig(t, true)
	r.m.Instrument(r.svc)
	jkt := strings.Repeat("k", 43)
	for i := 0; i < 2; i++ {
		_, _ = r.svc.VerifyPlayIntegrity(context.Background(), "tok", app.IntegrityBinding{DeviceKeyThumbprint: jkt, RequestHash: "h"})
	}
	if got := r.m.integrity.Value(string(app.IntegrityUnavailable)); got != 2 {
		t.Fatalf("unavailable = %d, want 2", got)
	}
	r.m.ObserveIntegrity(app.IntegrityRestricted, []app.IntegrityReason{app.ReasonUnlicensed, app.ReasonDeviceIntegrity})
	if r.m.integrity.Value(string(app.IntegrityRestricted)) != 1 ||
		r.m.restrictions.Value(string(app.ReasonUnlicensed)) != 1 || r.m.restrictions.Value(string(app.ReasonDeviceIntegrity)) != 1 {
		t.Fatal("a restricted outcome and its reasons were not counted")
	}
	var none *Metrics
	none.ObserveIntegrity(app.IntegrityCertified, nil)
}

type okProvider struct{ name string }

func (p okProvider) Name() string                                { return p.name }
func (okProvider) SendOtp(context.Context, string, string) error { return nil }

type downProvider struct{ name string }

func (p downProvider) Name() string { return p.name }
func (downProvider) SendOtp(context.Context, string, string) error {
	return errors.New("down")
}

// A Sender configured with Observe: m.ObserveOtpDelivery reports every
// attempt by provider and outcome, and the scrape shows it. Invalid provider
// labels can never reach the exposition.
func TestMetrics_OtpDeliveriesAreCountedByProvider(t *testing.T) {
	m := NewMetrics()
	s, err := otpdelivery.New(otpdelivery.Config{
		Providers:      []otpdelivery.Provider{downProvider{"primary-sms"}, okProvider{"secondary-sms"}},
		AttemptTimeout: otpdelivery.MinAttemptTimeout,
		Observe:        m.ObserveOtpDelivery,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SendOtp(context.Background(), msisdn, "123456"); err != nil {
		t.Fatal(err)
	}
	if m.deliveries.Value("primary-sms", string(otpdelivery.OutcomeFailed)) != 1 ||
		m.deliveries.Value("secondary-sms", string(otpdelivery.OutcomeDelivered)) != 1 {
		t.Fatalf("series %v", m.deliveries.Series())
	}
	m.ObserveOtpDelivery("+919876543210", otpdelivery.OutcomeFailed)
	if m.deliveries.Value(ProviderOther, string(otpdelivery.OutcomeFailed)) != 1 {
		t.Fatal("an invalid provider label was not mapped to other")
	}
	mount := &fakeMount{}
	m.MountOn(mount, "identity")
	rec := httptest.NewRecorder()
	mount.h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", http.NoBody))
	body := rec.Body.String()
	if !strings.Contains(body, MetricOtpDeliveries+`{outcome="delivered",provider="secondary-sms"} 1`) || strings.Contains(body, "+91") {
		t.Fatalf("scrape:\n%s", body)
	}
	var none *Metrics
	none.ObserveOtpDelivery("primary-sms", otpdelivery.OutcomeDelivered)
}

// deletionProvider is a fake with a deletion template.
type deletionProvider struct{ okProvider }

func (deletionProvider) SendDeletionCode(context.Context, string, string) error { return nil }

// Deletion code attempts are counted apart from sign-in codes, by provider
// and outcome, and only on their own series.
func TestMetrics_DeletionCodeDeliveriesAreCountedApart(t *testing.T) {
	m := NewMetrics()
	s, err := otpdelivery.New(otpdelivery.Config{
		Providers:       []otpdelivery.Provider{deletionProvider{okProvider{"primary-sms"}}},
		AttemptTimeout:  otpdelivery.MinAttemptTimeout,
		Observe:         m.ObserveOtpDelivery,
		ObserveDeletion: m.ObserveDeletionCodeDelivery,
	})
	if err != nil {
		t.Fatal(err)
	}
	d, ok := s.DeletionCodes()
	if !ok {
		t.Fatal("no deletion sender")
	}
	if err := d.SendDeletionCode(context.Background(), msisdn, "123456"); err != nil {
		t.Fatal(err)
	}
	if m.deletionSent.Value("primary-sms", string(otpdelivery.OutcomeDelivered)) != 1 ||
		m.deliveries.Value("primary-sms", string(otpdelivery.OutcomeDelivered)) != 0 {
		t.Fatalf("deletion %v, sign-in %v", m.deletionSent.Series(), m.deliveries.Series())
	}
	m.ObserveDeletionCodeDelivery("+919876543210", otpdelivery.OutcomeFailed)
	if m.deletionSent.Value(ProviderOther, string(otpdelivery.OutcomeFailed)) != 1 {
		t.Fatal("an invalid provider label was not mapped to other")
	}
	mount := &fakeMount{}
	m.MountOn(mount, "identity")
	rec := httptest.NewRecorder()
	mount.h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", http.NoBody))
	body := rec.Body.String()
	if !strings.Contains(body, MetricDeletionCodeDeliveries+`{outcome="delivered",provider="primary-sms"} 1`) || strings.Contains(body, "+91") {
		t.Fatalf("scrape:\n%s", body)
	}
	var none *Metrics
	none.ObserveDeletionCodeDelivery("primary-sms", otpdelivery.OutcomeDelivered)
}

// refusingAuditor is a durable audit log that is down.
type refusingAuditor struct{}

func (refusingAuditor) Record(app.SecurityEvent) error { return errors.New("audit store down") }

// Through the API: when the durable audit log refuses an event, Instrument
// makes the loss visible as identity_security_audit_failures_total under the
// event's type, while the event is still counted as a security event and the
// response is unchanged.
func TestMetrics_AuditFailuresAreCountedByType(t *testing.T) {
	r := newRig(t, true)
	r.svc.AddSecurityAuditor(refusingAuditor{}) // as bootstrap.Open installs the durable log
	r.m.Instrument(r.svc)
	k := deviceKey(t)
	sid, rt := r.enrol(t, k)
	if got := r.post(t, routes.Refresh, refreshBody(sid, rt), proof(t, k, k, "s1", "POST", refreshURL, time.Now())); got.code != 200 {
		t.Fatalf("refresh: %d %s", got.code, got.raw)
	}
	if got := r.post(t, routes.Refresh, refreshBody(sid, rt), proof(t, k, k, "s2", "POST", refreshURL, time.Now())); got.code != 401 {
		t.Fatalf("reuse with the audit log down: %d %s", got.code, got.raw)
	}
	reuse := string(app.EventRefreshTokenReused)
	if got := r.m.auditFails.Value(reuse); got != 1 {
		t.Fatalf("audit failures for %s = %d, want 1", reuse, got)
	}
	if got := r.m.auditFails.Value(string(app.EventDeviceEnrolled)); got != 1 {
		t.Fatalf("audit failures for the enrolment = %d, want 1", got)
	}
	if got := r.m.security.Value(reuse); got != 1 {
		t.Fatalf("reuse events = %d, want 1", got)
	}
	if want := uint64(2); r.svc.AuditFailures() != want {
		t.Fatalf("service audit failures = %d, want %d", r.svc.AuditFailures(), want)
	}

	m := NewMetrics()
	m.ObserveAuditFailure("identity.something.new")
	if m.auditFails.Value(SecurityEventOther) != 1 {
		t.Fatal("an unknown type was not counted as other")
	}
	var none *Metrics
	none.ObserveAuditFailure(app.EventOtpLockout)
}
