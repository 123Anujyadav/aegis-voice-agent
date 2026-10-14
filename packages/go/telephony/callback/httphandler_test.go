package callback

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
)

// Phase 19 P1-12 — the callback HTTP handler.
//
// The verifier and normalizer are the P1-6 TEST FAKES (admission_test.go).
// Nothing here is a real carrier scheme, and no test authenticates by any
// path other than the admission gate.

const externalURL = "https://callbacks.example.invalid/carrier/fake-carrier"

// recordingVerifier wraps the fake verifier and keeps the request it judged.
type recordingVerifier struct {
	inner Verifier
	mu    sync.Mutex
	last  *Request
	calls int
	block bool // wait for the context to end instead of judging
}

func (v *recordingVerifier) Verify(ctx context.Context, r *Request) (Authentication, error) {
	v.mu.Lock()
	cp := *r
	cp.Body = append([]byte(nil), r.Body...)
	v.last, v.calls = &cp, v.calls+1
	block := v.block
	v.mu.Unlock()
	if block {
		<-ctx.Done()
		return Authentication{}, ctx.Err()
	}
	return v.inner.Verify(ctx, r)
}

func (v *recordingVerifier) seen() (*Request, int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.last, v.calls
}

type handlerRig struct {
	*admissionRig
	rv *recordingVerifier
	h  http.Handler
}

func httpCfg() HTTPConfig {
	return HTTPConfig{
		Provider: provider, Method: http.MethodPost, ExternalURL: externalURL,
		MaxBodyBytes: 64, Timeout: 2 * time.Second,
		RequiredHeaders: []string{testSecretHeader},
		ContentTypes:    []string{"text/plain"},
		RetryAfter:      3 * time.Second,
	}
}

// newHandlerRig builds the handler over an admission gate whose ingestor uses
// store (nil means the rig's MemoryStore).
func newHandlerRig(t *testing.T, store Store, mutate ...func(*HTTPConfig)) *handlerRig {
	t.Helper()
	a := newAdmissionRig(t)
	if store != nil {
		in, err := NewIngestor(store, a.bindings, a.life)
		if err != nil {
			t.Fatal(err)
		}
		a.in = in
	}
	rv := &recordingVerifier{inner: a.verifier}
	adm, err := NewAdmission(AdmissionConfig{
		MaxBodyBytes: 256,
		Adapters:     map[telephony.ProviderID]ProviderAdapter{provider: {Verifier: rv, Normalizer: a.norm}},
	}, a.in)
	if err != nil {
		t.Fatal(err)
	}
	a.adm = adm
	cfg := httpCfg()
	for _, m := range mutate {
		m(&cfg)
	}
	h, err := NewHTTPHandler(cfg, adm)
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	return &handlerRig{admissionRig: a, rv: rv, h: h}
}

func post(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/carrier/fake-carrier", strings.NewReader(body))
	r.Header.Set(testSecretHeader, token)
	r.Header.Set("Content-Type", "text/plain; charset=utf-8")
	return r
}

func (hr *handlerRig) serve(r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	hr.h.ServeHTTP(w, r)
	return w
}

// expect checks status and the fixed JSON body, and that the response never
// echoes the credential or the payload.
func expect(t *testing.T, w *httptest.ResponseRecorder, status int, code string, payload string) {
	t.Helper()
	if w.Code != status {
		t.Errorf("status = %d, want %d (body %s)", w.Code, status, w.Body.String())
	}
	if want := `{"code":"` + code + `"}`; w.Body.String() != want {
		t.Errorf("body = %s, want %s", w.Body.String(), want)
	}
	if strings.Contains(w.Body.String(), token) || (payload != "" && strings.Contains(w.Body.String(), payload)) {
		t.Errorf("response discloses the credential or payload: %s", w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
}

// ---------------------------------------------------------------------------
// Delegation
// ---------------------------------------------------------------------------

func TestHTTP_DelegatesTheExactBodyAndTrustedIdentity(t *testing.T) {
	t.Parallel()
	hr := newHandlerRig(t, nil)
	id := hr.boundCall(t, "pc-1")

	body := "e-1|pc-1|ringing"
	r := post(body)
	r.URL.RawQuery = "sig=abc&n=1"
	expect(t, hr.serve(r), http.StatusOK, CodeAccepted, "")

	got, calls := hr.rv.seen()
	if calls != 1 {
		t.Fatalf("verifier ran %d times", calls)
	}
	if string(got.Body) != body || got.Provider != provider || got.Method != http.MethodPost {
		t.Errorf("admitted request = provider %s, method %s, body %q", got.Provider, got.Method, got.Body)
	}
	if got.URL != externalURL+"?sig=abc&n=1" {
		t.Errorf("verifier URL = %q, want the configured URL plus the received query", got.URL)
	}
	if got.Header.Get(testSecretHeader) != token {
		t.Error("headers were not passed to the verifier")
	}
	if s := hr.state(t, id); s != telephony.StateRinging {
		t.Errorf("state = %s, want ringing", s)
	}
}

func TestHTTP_ForwardingHeadersChangeNothing(t *testing.T) {
	t.Parallel()
	hr := newHandlerRig(t, nil)
	hr.boundCall(t, "pc-1")

	r := post("e-1|pc-1|ringing")
	r.Host = "attacker.invalid"
	for k, v := range map[string]string{
		"X-Forwarded-Host":  "attacker.invalid",
		"X-Forwarded-Proto": "http",
		"X-Forwarded-For":   "203.0.113.9",
		"Forwarded":         "for=203.0.113.9;host=attacker.invalid;proto=http",
		"X-Real-IP":         "203.0.113.9",
		"X-Provider":        "other-carrier",
	} {
		r.Header.Set(k, v)
	}
	expect(t, hr.serve(r), http.StatusOK, CodeAccepted, "")
	got, _ := hr.rv.seen()
	if got.URL != externalURL || got.Provider != provider {
		t.Errorf("URL %q / provider %s were influenced by request headers", got.URL, got.Provider)
	}
}

// ---------------------------------------------------------------------------
// Refusals before admission
// ---------------------------------------------------------------------------

func TestHTTP_RefusesBadRequestsBeforeAdmission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		req    func() *http.Request
		status int
		code   string
	}{
		{"GET", func() *http.Request { r := post("x"); r.Method = http.MethodGet; return r }, 405, CodeMethod},
		{"PUT", func() *http.Request { r := post("x"); r.Method = http.MethodPut; return r }, 405, CodeMethod},
		{"wrong path", func() *http.Request { r := post("e-1|pc-1|ringing"); r.URL.Path = "/carrier/other"; return r }, 404, CodeNotFound},
		{"missing required header", func() *http.Request { r := post("e-1|pc-1|ringing"); r.Header.Del(testSecretHeader); return r }, 400, CodeMalformed},
		{"blank required header", func() *http.Request { r := post("e-1|pc-1|ringing"); r.Header.Set(testSecretHeader, "  "); return r }, 400, CodeMalformed},
		{"repeated required header", func() *http.Request { r := post("e-1|pc-1|ringing"); r.Header.Add(testSecretHeader, token); return r }, 400, CodeMalformed},
		{"oversized required header", func() *http.Request {
			r := post("e-1|pc-1|ringing")
			r.Header.Set(testSecretHeader, strings.Repeat("a", maxHeaderValueBytes+1))
			return r
		}, 400, CodeMalformed},
		{"no content type", func() *http.Request { r := post("e-1|pc-1|ringing"); r.Header.Del("Content-Type"); return r }, 415, CodeUnsupportedType},
		{"wrong content type", func() *http.Request {
			r := post("e-1|pc-1|ringing")
			r.Header.Set("Content-Type", "application/json")
			return r
		}, 415, CodeUnsupportedType},
		{"empty body", func() *http.Request { return post("") }, 400, CodeMalformed},
		{"declared oversized body", func() *http.Request { return post(strings.Repeat("x", 65)) }, 413, CodeTooLarge},
		{"undeclared oversized body", func() *http.Request {
			r := post("")
			r.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", 10_000)))
			r.ContentLength = -1
			return r
		}, 413, CodeTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hr := newHandlerRig(t, nil)
			id := hr.boundCall(t, "pc-1")
			w := hr.serve(tc.req())
			expect(t, w, tc.status, tc.code, "")
			if tc.status == 405 && w.Header().Get("Allow") != http.MethodPost {
				t.Errorf("Allow = %q", w.Header().Get("Allow"))
			}
			if _, calls := hr.rv.seen(); calls != 0 {
				t.Errorf("the verifier ran %d times for a refused request", calls)
			}
			hr.untouched(t, id, telephony.StateIncoming)
		})
	}
}

// ---------------------------------------------------------------------------
// Admission outcomes
// ---------------------------------------------------------------------------

func TestHTTP_AuthenticationFailureIsRefusedAndClosed(t *testing.T) {
	t.Parallel()
	hr := newHandlerRig(t, nil)
	id := hr.boundCall(t, "pc-1")

	r := post("e-1|pc-1|ringing")
	r.Header.Set(testSecretHeader, "guess-"+token)
	expect(t, hr.serve(r), http.StatusUnauthorized, CodeUnauthenticated, "e-1|pc-1")
	hr.untouched(t, id, telephony.StateIncoming)

	// A route whose provider has no verifier refuses everything the same way.
	none := newHandlerRig(t, nil, func(c *HTTPConfig) { c.Provider = "unregistered-carrier" })
	expect(t, none.serve(post("e-1|pc-1|ringing")), http.StatusUnauthorized, CodeUnauthenticated, "")
}

func TestHTTP_InvalidEventsAreMalformed(t *testing.T) {
	t.Parallel()
	hr := newHandlerRig(t, nil)
	id := hr.boundCall(t, "pc-1")
	for _, body := range []string{"garbage-payload", "e-1|pc-1|picked_up", "|pc-1|ringing"} {
		expect(t, hr.serve(post(body)), http.StatusBadRequest, CodeMalformed, body)
	}
	hr.untouched(t, id, telephony.StateIncoming)
}

func TestHTTP_DuplicateIsAcceptedAndAppliedOnce(t *testing.T) {
	t.Parallel()
	hr := newHandlerRig(t, nil)
	hr.boundCall(t, "pc-1")
	for i := 0; i < 3; i++ {
		expect(t, hr.serve(post("e-1|pc-1|ringing")), http.StatusOK, CodeAccepted, "")
	}
	if n := hr.life.ops.Load(); n != 1 {
		t.Errorf("lifecycle ran %d times, want 1", n)
	}
}

func TestHTTP_ReusedEventIDAndUnknownCall(t *testing.T) {
	t.Parallel()
	hr := newHandlerRig(t, nil)
	hr.boundCall(t, "pc-1")
	expect(t, hr.serve(post("e-1|pc-1|ringing")), http.StatusOK, CodeAccepted, "")
	expect(t, hr.serve(post("e-1|pc-1|failed")), http.StatusConflict, CodeEventIDReused, "")
	expect(t, hr.serve(post("e-2|pc-unbound|ringing")), http.StatusNotFound, CodeUnknownCall, "")
}

// inFlightStore answers every Reserve as already held elsewhere.
type inFlightStore struct{ *MemoryStore }

func (inFlightStore) Reserve(context.Context, Key, Fingerprint) (Reservation, error) {
	return Reservation{Status: StatusInFlight}, nil
}

func TestHTTP_InFlightAsksForARetry(t *testing.T) {
	t.Parallel()
	m, _ := NewMemoryStore(8)
	hr := newHandlerRig(t, inFlightStore{m})
	id := hr.boundCall(t, "pc-1")
	w := hr.serve(post("e-1|pc-1|ringing"))
	expect(t, w, http.StatusServiceUnavailable, CodeInFlight, "")
	if w.Header().Get("Retry-After") != "3" {
		t.Errorf("Retry-After = %q, want 3", w.Header().Get("Retry-After"))
	}
	hr.untouched(t, id, telephony.StateIncoming)
}

// outageStore fails Reserve or Settle with err.
type outageStore struct {
	*MemoryStore
	reserveErr, settleErr error
}

func (o *outageStore) Reserve(ctx context.Context, k Key, fp Fingerprint) (Reservation, error) {
	if o.reserveErr != nil {
		return Reservation{}, o.reserveErr
	}
	return o.MemoryStore.Reserve(ctx, k, fp)
}

func (o *outageStore) Settle(ctx context.Context, k Key, token string, r Result) error {
	if o.settleErr != nil {
		return o.settleErr
	}
	return o.MemoryStore.Settle(ctx, k, token, r)
}

func TestHTTP_StorageFailuresAreNeverSuccess(t *testing.T) {
	t.Parallel()
	classified := fmt.Errorf("adapter: %w", ErrStoreUnavailable)
	unclassified := errors.New("something broke")
	for _, tc := range []struct {
		name   string
		store  func(*MemoryStore) Store
		status int
		code   string
		moved  bool
	}{
		{"reserve outage", func(m *MemoryStore) Store { return &outageStore{MemoryStore: m, reserveErr: classified} }, 503, CodeUnavailable, false},
		{"reserve unclassified failure", func(m *MemoryStore) Store { return &outageStore{MemoryStore: m, reserveErr: unclassified} }, 500, CodeInternal, false},
		// The transition happened but was not recorded: the carrier is told to
		// retry, and the redelivery is re-judged (P1-11), never re-applied.
		{"settle outage", func(m *MemoryStore) Store { return &outageStore{MemoryStore: m, settleErr: classified} }, 503, CodeUnavailable, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := NewMemoryStore(8)
			hr := newHandlerRig(t, tc.store(m))
			id := hr.boundCall(t, "pc-1")
			w := hr.serve(post("e-1|pc-1|ringing"))
			expect(t, w, tc.status, tc.code, "")
			if w.Code == http.StatusOK {
				t.Fatal("a storage failure was reported as success")
			}
			if moved := hr.state(t, id) != telephony.StateIncoming; moved != tc.moved {
				t.Errorf("lifecycle moved = %v, want %v", moved, tc.moved)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Timeouts and cancellation
// ---------------------------------------------------------------------------

func TestHTTP_TimeoutAndCancellationAreUnavailableNotUnauthenticated(t *testing.T) {
	t.Parallel()
	hr := newHandlerRig(t, nil, func(c *HTTPConfig) { c.Timeout = 50 * time.Millisecond })
	id := hr.boundCall(t, "pc-1")
	hr.rv.block = true

	start := time.Now()
	w := hr.serve(post("e-1|pc-1|ringing"))
	expect(t, w, http.StatusServiceUnavailable, CodeTimeout, "")
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("handler took %s; the timeout did not bound it", d)
	}

	ctx, cancel := context.WithCancel(context.Background())
	r := post("e-1|pc-1|ringing").WithContext(ctx)
	time.AfterFunc(20*time.Millisecond, cancel)
	expect(t, hr.serve(r), http.StatusServiceUnavailable, CodeTimeout, "")
	hr.untouched(t, id, telephony.StateIncoming)
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

func TestNewHTTPHandler_RefusesIncompleteConfiguration(t *testing.T) {
	t.Parallel()
	a := newAdmissionRig(t)
	for name, mutate := range map[string]func(*HTTPConfig){
		"invalid provider":       func(c *HTTPConfig) { c.Provider = "Bad" },
		"no method":              func(c *HTTPConfig) { c.Method = "" },
		"DELETE":                 func(c *HTTPConfig) { c.Method = http.MethodDelete },
		"http URL":               func(c *HTTPConfig) { c.ExternalURL = "http://callbacks.example.invalid/x" },
		"relative URL":           func(c *HTTPConfig) { c.ExternalURL = "/x" },
		"URL with query":         func(c *HTTPConfig) { c.ExternalURL = externalURL + "?a=1" },
		"URL with credentials":   func(c *HTTPConfig) { c.ExternalURL = "https://u:p@callbacks.example.invalid/x" },
		"URL without path":       func(c *HTTPConfig) { c.ExternalURL = "https://callbacks.example.invalid" },
		"no body limit":          func(c *HTTPConfig) { c.MaxBodyBytes = 0 },
		"no timeout":             func(c *HTTPConfig) { c.Timeout = 0 },
		"sub-second retry-after": func(c *HTTPConfig) { c.RetryAfter = 500 * time.Millisecond },
		"empty header name":      func(c *HTTPConfig) { c.RequiredHeaders = []string{" "} },
	} {
		cfg := httpCfg()
		mutate(&cfg)
		if _, err := NewHTTPHandler(cfg, a.adm); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := NewHTTPHandler(httpCfg(), nil); err == nil {
		t.Error("nil admission accepted")
	}
}
