package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/callscreen/callscreen-platform/services/go/edge-api/internal/auth"
	"github.com/callscreen/callscreen-platform/services/go/edge-api/internal/httpapi"
)

// deadlineStore is a replay store that records the context it is given and
// answers with err, after waiting for the context when wait is set.
type deadlineStore struct {
	mu       sync.Mutex
	deadline time.Time
	has      bool
	wait     bool
}

func (s *deadlineStore) Reserve(ctx context.Context, _, _ string, _, _ time.Time) error {
	s.mu.Lock()
	s.deadline, s.has = ctx.Deadline()
	s.mu.Unlock()
	if s.wait {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

// deadlineRig is the production composition (applicationHandler over the
// production routes, behind a real auth.Authenticator) with [store] as the
// replay store and a route wrapper that records the request's deadline.
func deadlineRig(t *testing.T, store dpop.ReplayStore) (h http.Handler, header func() http.Header, routeDeadline func() (time.Time, bool)) {
	t.Helper()
	priv := setMeEnv(t)
	ac, active, err := auth.LoadActivation()
	if err != nil || !active {
		t.Fatalf("(%v, %v)", active, err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := auth.New(ac, store, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	device, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	jkt, _ := accesstoken.ThumbprintP256(&device.PublicKey)
	signer := newSigner(t, priv, meIssuer, meAudience, meKID, time.Now)

	var mu sync.Mutex
	var seen time.Time
	var seenOK bool
	routes := protectedRoutes(log)
	prod := routes[httpapi.MePath]
	routes[httpapi.MePath] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen, seenOK = r.Context().Deadline()
		mu.Unlock()
		prod.ServeHTTP(w, r)
	})
	header = func() http.Header {
		tok, err := signer.Issue(meSubject, []string{"subscriber"}, jkt)
		if err != nil {
			t.Fatal(err)
		}
		return creds(tok, proof(t, device, "GET", meOrigin+httpapi.MePath, tok))
	}
	routeDeadline = func() (time.Time, bool) {
		mu.Lock()
		defer mu.Unlock()
		return seen, seenOK
	}
	return applicationHandler(a, routes, nil), header, routeDeadline
}

func serve(h http.Handler, hdr http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, meOrigin+httpapi.MePath, http.NoBody)
	req.Header = hdr
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDeadlines_EveryRequestIsBounded(t *testing.T) {
	store := &deadlineStore{}
	h, header, routeDeadline := deadlineRig(t, store)
	start := time.Now()
	if rec := serve(h, header()); rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %q", rec.Code, rec.Body.String())
	}
	for name, got := range map[string]func() (time.Time, bool){
		"replay store": func() (time.Time, bool) { return store.deadline, store.has },
		"route":        routeDeadline,
	} {
		d, ok := got()
		if !ok {
			t.Fatalf("%s: the request context has no deadline", name)
		}
		if limit := start.Add(requestHandlerTimeout); d.After(limit.Add(time.Second)) || d.Before(start) {
			t.Fatalf("%s: deadline %v is not within %v of the request", name, d.Sub(start), requestHandlerTimeout)
		}
	}
}

// A replay store that cannot answer in time fails closed: 503, never a hung
// request and never an admitted one. The store waits for the request's own
// deadline, so this test takes requestHandlerTimeout.
func TestDeadlines_AStalledReplayStoreFailsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the handler timeout")
	}
	store := &deadlineStore{wait: true}
	h, header, routeDeadline := deadlineRig(t, store)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- serve(h, header()) }()
	select {
	case rec := <-done:
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status %d, want 503; body %q", rec.Code, rec.Body.String())
		}
	case <-time.After(requestHandlerTimeout + 5*time.Second):
		t.Fatal("the request did not end after the handler timeout")
	}
	if _, reached := routeDeadline(); reached {
		t.Fatal("a request whose proof could not be replay-checked reached the route")
	}
}

func TestDeadlines_TheWrapperWorksOverARealConnection(t *testing.T) {
	// Over a real connection, where http.ResponseController can set the read
	// and write deadlines (a recorder cannot).
	var got time.Time
	var ok bool
	srv := httptest.NewServer(withDeadlines(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = r.Context().Deadline()
		w.WriteHeader(http.StatusNoContent)
	})))
	defer srv.Close()
	start := time.Now()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || !ok || got.Sub(start) > requestHandlerTimeout+time.Second {
		t.Fatalf("status %d, deadline %v (set %v)", resp.StatusCode, got.Sub(start), ok)
	}
}
