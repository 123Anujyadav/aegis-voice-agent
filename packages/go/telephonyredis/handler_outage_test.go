package telephonyredis

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
	"github.com/callscreen/callscreen-platform/packages/go/telephony/callback"
)

// Phase 19 P1-12 — a Redis outage seen through the HTTP handler.
//
// The verifier and normalizer below are TEST-ONLY: a fixed header token and a
// "eventID|providerCallID|kind" body. They are not a carrier scheme.

type tokenVerifier struct{}

func (tokenVerifier) Verify(_ context.Context, r *callback.Request) (callback.Authentication, error) {
	if r.Header.Get("X-Test-Auth") != "test-token" {
		return callback.Authentication{Provider: r.Provider}, nil
	}
	return callback.Authentication{Provider: r.Provider, Verified: true, Mechanism: "test-only"}, nil
}

type pipeNormalizer struct{}

func (pipeNormalizer) Normalize(_ context.Context, r *callback.Request) (callback.Event, error) {
	p := strings.Split(string(r.Body), "|")
	if len(p) != 3 {
		return callback.Event{}, callback.ErrInvalidEvent
	}
	return callback.Event{Provider: r.Provider, EventID: p[0], ProviderCallID: p[1], Kind: callback.Kind(p[2]), ReceivedAt: r.ReceivedAt}, nil
}

func TestHandler_RedisOutageIsServiceUnavailableNeverSuccess(t *testing.T) {
	t.Parallel()

	// A Redis address with nothing listening.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	store, err := New(clientCfg(addr), storeCfg())
	if err != nil {
		t.Fatal(err)
	}

	h, err := telephony.NewHarness()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = h.Stop(context.Background()) })
	sess, err := h.BeginInbound(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bindings, _ := callback.NewBindings(callback.RegistryProviders(h.Runtime.Registry()), 8)
	if err := bindings.Bind(callback.Binding{Provider: h.Provider.ID(), ProviderCallID: "pc-1", Call: sess.ID()}); err != nil {
		t.Fatal(err)
	}
	in, _ := callback.NewIngestor(store, bindings, callback.RuntimeLifecycle(h.Runtime))
	adm, err := callback.NewAdmission(callback.AdmissionConfig{
		MaxBodyBytes: 256,
		Adapters:     map[telephony.ProviderID]callback.ProviderAdapter{h.Provider.ID(): {Verifier: tokenVerifier{}, Normalizer: pipeNormalizer{}}},
	}, in)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := callback.NewHTTPHandler(callback.HTTPConfig{
		Provider: h.Provider.ID(), Method: http.MethodPost,
		ExternalURL: "https://callbacks.example.invalid/cb", MaxBodyBytes: 256,
		Timeout: 2 * time.Second, RetryAfter: time.Second,
	}, adm)
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodPost, "/cb", strings.NewReader("e-1|pc-1|ringing"))
	r.Header.Set("X-Test-Auth", "test-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusServiceUnavailable || w.Body.String() != `{"code":"unavailable"}` {
		t.Fatalf("response = %d %s, want 503 unavailable", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), addr) {
		t.Error("response discloses the Redis address")
	}
	if s := sess.State(); s != telephony.StateIncoming {
		t.Errorf("state = %s; a Redis outage must not change the call", s)
	}
}
