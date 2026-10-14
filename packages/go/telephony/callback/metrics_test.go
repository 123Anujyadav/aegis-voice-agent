package callback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
)

// Phase 19 P1-13 — callback metrics and diagnostics.
//
// Every request runs through the real handler and the real admission gate,
// each with its own fresh Metrics, so counts are exact and tests cannot see
// each other's series.

type observedRig struct {
	*handlerRig
	m   *Metrics
	log *bytes.Buffer
}

type rigOpts struct {
	store    Store
	recorder Recorder // overrides the Metrics as the recorder when set
	cfg      func(*HTTPConfig)
	norm     Normalizer
}

func newObservedRig(t *testing.T, o rigOpts) *observedRig {
	t.Helper()
	a := newAdmissionRig(t)
	if o.store != nil {
		in, err := NewIngestor(o.store, a.bindings, a.life)
		if err != nil {
			t.Fatal(err)
		}
		a.in = in
	}
	m := NewMetrics()
	var rec Recorder = m
	if o.recorder != nil {
		rec = o.recorder
	}
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(&lockedWriter{w: buf}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	rv := &recordingVerifier{inner: a.verifier}
	var norm Normalizer = a.norm
	if o.norm != nil {
		norm = o.norm
	}
	adm, err := NewAdmission(AdmissionConfig{
		MaxBodyBytes: 256,
		Adapters:     map[telephony.ProviderID]ProviderAdapter{provider: {Verifier: rv, Normalizer: norm}},
		Recorder:     rec,
		Logger:       logger,
	}, a.in)
	if err != nil {
		t.Fatal(err)
	}
	a.adm = adm
	cfg := httpCfg()
	cfg.Recorder = rec
	if o.cfg != nil {
		o.cfg(&cfg)
	}
	h, err := NewHTTPHandler(cfg, adm)
	if err != nil {
		t.Fatal(err)
	}
	return &observedRig{handlerRig: &handlerRig{admissionRig: a, rv: rv, h: h}, m: m, log: buf}
}

type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// only asserts that the admission counter holds exactly one observation, of
// the given outcome, and that latency was observed once.
func (o *observedRig) only(t *testing.T, label, outcome string) {
	t.Helper()
	if got := o.m.Admissions.Value(label, outcome); got != 1 {
		t.Errorf("admissions{%s,%s} = %d, want 1", label, outcome, got)
	}
	if got := o.m.Admissions.Total(); got != 1 {
		t.Errorf("admissions total = %d, want exactly 1", got)
	}
	if got := o.m.AdmissionLatency.Count(label); got != 1 {
		t.Errorf("admission latency observations = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// Admission outcomes
// ---------------------------------------------------------------------------

func TestMetrics_EveryAdmissionOutcomeIsCountedOnce(t *testing.T) {
	t.Parallel()
	classified := fmt.Errorf("adapter: %w", ErrStoreUnavailable)

	for _, tc := range []struct {
		outcome string
		opts    func() rigOpts
		setup   func(t *testing.T, o *observedRig) *http.Request
		status  int
	}{
		{OutcomeLabelApplied, nil, func(t *testing.T, o *observedRig) *http.Request {
			o.boundCall(t, "pc-1")
			return post("e-1|pc-1|ringing")
		}, 200},
		{OutcomeLabelAlreadySatisfied, nil, func(t *testing.T, o *observedRig) *http.Request {
			o.to(t, o.boundCall(t, "pc-1"), telephony.StateRinging)
			return post("e-1|pc-1|ringing")
		}, 200},
		{OutcomeLabelStale, nil, func(t *testing.T, o *observedRig) *http.Request {
			o.to(t, o.boundCall(t, "pc-1"), telephony.StateConnected)
			return post("e-1|pc-1|ringing")
		}, 200},
		{OutcomeLabelConflictRecorded, nil, func(t *testing.T, o *observedRig) *http.Request {
			o.to(t, o.boundCall(t, "pc-1"), telephony.StateConnected)
			return post("e-1|pc-1|busy")
		}, 200},
		{OutcomeLabelUnauthenticated, nil, func(t *testing.T, o *observedRig) *http.Request {
			o.boundCall(t, "pc-1")
			r := post("e-1|pc-1|ringing")
			r.Header.Set(testSecretHeader, "wrong")
			return r
		}, 401},
		{OutcomeLabelInvalidEvent, nil, func(t *testing.T, o *observedRig) *http.Request {
			return post("not-a-payload")
		}, 400},
		{OutcomeLabelProviderMismatch, func() rigOpts {
			return rigOpts{norm: &fakeNormalizer{provider: "other-carrier"}}
		}, func(t *testing.T, o *observedRig) *http.Request {
			return post("e-1|pc-1|ringing")
		}, 400},
		{OutcomeLabelUnknownCall, nil, func(t *testing.T, o *observedRig) *http.Request {
			return post("e-1|pc-unbound|ringing")
		}, 404},
		{OutcomeLabelInFlight, func() rigOpts {
			m, _ := NewMemoryStore(8)
			return rigOpts{store: inFlightStore{m}}
		}, func(t *testing.T, o *observedRig) *http.Request {
			o.boundCall(t, "pc-1")
			return post("e-1|pc-1|ringing")
		}, 503},
		{OutcomeLabelStoreUnavailable, func() rigOpts {
			m, _ := NewMemoryStore(8)
			return rigOpts{store: &outageStore{MemoryStore: m, reserveErr: classified}}
		}, func(t *testing.T, o *observedRig) *http.Request {
			o.boundCall(t, "pc-1")
			return post("e-1|pc-1|ringing")
		}, 503},
		{OutcomeLabelInternalError, func() rigOpts {
			m, _ := NewMemoryStore(8)
			return rigOpts{store: &outageStore{MemoryStore: m, reserveErr: errors.New("unclassified")}}
		}, func(t *testing.T, o *observedRig) *http.Request {
			o.boundCall(t, "pc-1")
			return post("e-1|pc-1|ringing")
		}, 500},
		{OutcomeLabelTimeout, func() rigOpts {
			return rigOpts{cfg: func(c *HTTPConfig) { c.Timeout = 50 * time.Millisecond }}
		}, func(t *testing.T, o *observedRig) *http.Request {
			o.rv.block = true
			return post("e-1|pc-1|ringing")
		}, 503},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			t.Parallel()
			var opts rigOpts
			if tc.opts != nil {
				opts = tc.opts()
			}
			o := newObservedRig(t, opts)
			w := o.serve(tc.setup(t, o))
			if w.Code != tc.status {
				t.Errorf("status = %d, want %d", w.Code, tc.status)
			}
			o.only(t, string(provider), tc.outcome)
			if got := o.m.Requests.Value(string(provider)); got != 1 {
				t.Errorf("http requests = %d, want 1", got)
			}
			if got := o.m.Rejections.Total(); got != 0 {
				t.Errorf("http rejections = %d; an admitted request is not a rejection", got)
			}
		})
	}
}

func TestMetrics_DuplicateAndReusedIDAreTheirOwnOutcomes(t *testing.T) {
	t.Parallel()
	o := newObservedRig(t, rigOpts{})
	o.boundCall(t, "pc-1")
	o.serve(post("e-1|pc-1|ringing"))
	o.serve(post("e-1|pc-1|ringing"))
	o.serve(post("e-1|pc-1|failed"))
	p := string(provider)
	for outcome, want := range map[string]uint64{
		OutcomeLabelApplied: 1, OutcomeLabelDuplicate: 1, OutcomeLabelEventIDReused: 1,
	} {
		if got := o.m.Admissions.Value(p, outcome); got != want {
			t.Errorf("%s = %d, want %d", outcome, got, want)
		}
	}
	if o.m.Admissions.Total() != 3 || o.m.Requests.Value(p) != 3 {
		t.Errorf("admissions %d, requests %d; want 3 and 3", o.m.Admissions.Total(), o.m.Requests.Value(p))
	}
}

func TestMetrics_AdmitOutsideHTTPIsLabelledByConfiguredProviderOnly(t *testing.T) {
	t.Parallel()
	o := newObservedRig(t, rigOpts{})
	// No verifier for this provider, and a nil request: both are admissions,
	// neither may create a label from request data.
	r := request("e-1|pc-1|ringing", true)
	r.Provider = "attacker-chosen-provider"
	if _, err := o.adm.Admit(context.Background(), r); !errors.Is(err, ErrNoVerifier) {
		t.Fatal(err)
	}
	if _, err := o.adm.Admit(context.Background(), nil); !errors.Is(err, ErrMalformedRequest) {
		t.Fatal(err)
	}
	if got := o.m.Admissions.Value(unconfiguredProvider, OutcomeLabelNoVerifier); got != 1 {
		t.Errorf("no_verifier = %d, want 1 under %q", got, unconfiguredProvider)
	}
	if got := o.m.Admissions.Value(unconfiguredProvider, OutcomeLabelMalformedRequest); got != 1 {
		t.Errorf("malformed_request = %d, want 1", got)
	}
	if got := o.m.Admissions.Value("attacker-chosen-provider", OutcomeLabelNoVerifier); got != 0 {
		t.Error("an unconfigured provider became a label")
	}
}

// ---------------------------------------------------------------------------
// HTTP rejections
// ---------------------------------------------------------------------------

func TestMetrics_ValidationRejectionsAreCountedByReason(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reason string
		req    func() *http.Request
	}{
		{RejectMethod, func() *http.Request { r := post("x"); r.Method = http.MethodGet; return r }},
		{RejectPath, func() *http.Request { r := post("x"); r.URL.Path = "/elsewhere"; return r }},
		{RejectHeader, func() *http.Request { r := post("x"); r.Header.Del(testSecretHeader); return r }},
		{RejectContentType, func() *http.Request { r := post("x"); r.Header.Set("Content-Type", "application/json"); return r }},
		{RejectBodySize, func() *http.Request { return post(strings.Repeat("x", 65)) }},
		{RejectEmptyBody, func() *http.Request { return post("") }},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			t.Parallel()
			o := newObservedRig(t, rigOpts{})
			o.serve(tc.req())
			p := string(provider)
			if got := o.m.Rejections.Value(p, tc.reason); got != 1 {
				t.Errorf("rejected{%s} = %d, want 1", tc.reason, got)
			}
			if got := o.m.Rejections.Total(); got != 1 {
				t.Errorf("rejections total = %d, want 1", got)
			}
			if got := o.m.Admissions.Total(); got != 0 {
				t.Errorf("admissions = %d; a rejected request never reaches admission", got)
			}
			if got := o.m.Requests.Value(p); got != 1 {
				t.Errorf("requests = %d, want 1", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Failures are failures
// ---------------------------------------------------------------------------

func TestMetrics_FailuresNeverCountAsSuccess(t *testing.T) {
	t.Parallel()
	success := []string{OutcomeLabelApplied, OutcomeLabelAlreadySatisfied, OutcomeLabelStale,
		OutcomeLabelConflictRecorded, OutcomeLabelDuplicate}
	m, _ := NewMemoryStore(8)
	o := newObservedRig(t, rigOpts{store: &outageStore{MemoryStore: m, settleErr: fmt.Errorf("x: %w", ErrStoreUnavailable)}})
	o.boundCall(t, "pc-1")
	w := o.serve(post("e-1|pc-1|ringing")) // applied, but not recorded
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", w.Code)
	}
	for _, s := range success {
		if got := o.m.Admissions.Value(string(provider), s); got != 0 {
			t.Errorf("a storage failure was counted as %s", s)
		}
	}
	o.only(t, string(provider), OutcomeLabelStoreUnavailable)
}

// ---------------------------------------------------------------------------
// Cardinality
// ---------------------------------------------------------------------------

func TestMetrics_RequestDataNeverBecomesALabel(t *testing.T) {
	t.Parallel()
	o := newObservedRig(t, rigOpts{})
	o.boundCall(t, "pc-1")
	for i := 0; i < 40; i++ {
		r := post(fmt.Sprintf("evt-%d|pc-%d-919876543210|ringing", i, i))
		r.URL.RawQuery = fmt.Sprintf("sig=s%d&n=%d", i, i)
		r.Header.Set("X-Caller", fmt.Sprintf("+91987654%04d", i))
		o.serve(r)
	}
	allowed := map[string]bool{string(provider): true, unconfiguredProvider: true}
	for _, v := range AdmissionOutcomeLabels() {
		allowed[v] = true
	}
	for _, v := range RejectionReasons() {
		allowed[v] = true
	}
	series := 0
	for _, s := range o.m.Registry().Snapshot() {
		for k, v := range s.Labels {
			if !allowed[v] {
				t.Errorf("%s{%s=%q}: a label value outside the fixed vocabulary", s.Name, k, v)
			}
		}
		series++
	}
	// 40 distinct events produce a handful of series, not 40.
	if series > 6 {
		t.Errorf("%d series after 40 distinct requests; labels are not bounded", series)
	}
}

// ---------------------------------------------------------------------------
// Diagnostics
// ---------------------------------------------------------------------------

func TestDiagnostics_LogsCarryNoSensitiveData(t *testing.T) {
	t.Parallel()
	const phone = "+919876543210"
	m, _ := NewMemoryStore(8)
	o := newObservedRig(t, rigOpts{store: &outageStore{MemoryStore: m, reserveErr: fmt.Errorf("redis at 10.1.2.3:6379 password=hunter2: %w", ErrStoreUnavailable)}})
	o.boundCall(t, "pc-1")

	r := post("evt-SECRET-42|pc-1|ringing")
	r.URL.RawQuery = "sig=topsecret-signature"
	r.Header.Set("Authorization", "Bearer secret-bearer")
	r.Header.Set("X-Caller", phone)
	o.serve(r)
	// And an internal error.
	o2 := newObservedRig(t, rigOpts{store: &outageStore{MemoryStore: m, reserveErr: errors.New("boom " + phone)}})
	o2.boundCall(t, "pc-1")
	o2.serve(post("evt-SECRET-43|pc-1|ringing"))

	logs := o.log.String() + o2.log.String()
	for _, want := range []string{`"outcome":"store_unavailable"`, `"outcome":"internal_error"`, `"stage":"admission"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %s:\n%s", want, logs)
		}
	}
	for _, secret := range []string{phone, "evt-SECRET", "topsecret", "secret-bearer", token,
		"hunter2", "10.1.2.3", "pc-1", "callbacks.example.invalid", "ringing"} {
		if strings.Contains(logs, secret) {
			t.Errorf("logs contain %q:\n%s", secret, logs)
		}
	}
}

func TestDiagnostics_RoutineOutcomesAreNotLogged(t *testing.T) {
	t.Parallel()
	o := newObservedRig(t, rigOpts{})
	o.boundCall(t, "pc-1")
	o.serve(post("e-1|pc-1|ringing"))
	o.serve(post("e-1|pc-1|ringing"))
	r := post("e-2|pc-1|ringing")
	r.Header.Set(testSecretHeader, "wrong")
	o.serve(r)
	if o.log.Len() != 0 {
		t.Errorf("routine outcomes were logged; counters are enough:\n%s", o.log.String())
	}
}

// ---------------------------------------------------------------------------
// Instrumentation cannot fail a callback
// ---------------------------------------------------------------------------

type panickingRecorder struct{}

func (panickingRecorder) HTTPRequest(string)                      { panic("metrics backend down") }
func (panickingRecorder) HTTPRejected(string, string)             { panic("metrics backend down") }
func (panickingRecorder) Admission(string, string, time.Duration) { panic("metrics backend down") }

func TestMetrics_ABrokenRecorderChangesNoResponse(t *testing.T) {
	t.Parallel()
	run := func(rec Recorder) []int {
		o := newObservedRig(t, rigOpts{recorder: rec})
		o.boundCall(t, "pc-1")
		var codes []int
		for _, r := range []*http.Request{
			post("e-1|pc-1|ringing"),       // applied
			post("e-1|pc-1|ringing"),       // duplicate
			post("e-2|pc-unbound|ringing"), // unknown call
			func() *http.Request { r := post("x"); r.Method = "GET"; return r }(), // rejected
			post(""), // empty body
		} {
			codes = append(codes, o.serve(r).Code)
		}
		if n := o.life.ops.Load(); n != 1 {
			t.Errorf("lifecycle ran %d times, want 1", n)
		}
		return codes
	}
	healthy := run(nil)
	broken := run(panickingRecorder{})
	if fmt.Sprint(healthy) != fmt.Sprint(broken) {
		t.Errorf("responses with a broken recorder %v differ from a healthy one %v", broken, healthy)
	}
}
