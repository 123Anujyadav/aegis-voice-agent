package ollama

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// The endpoint policy (endpoint.go): a prompt never leaves this machine.

func endpointConfig(base string) Config {
	cfg := DefaultConfig()
	cfg.ID = rt.ProviderID("ollama-dev")
	cfg.Model = testModel
	cfg.BaseURL = base
	cfg.RequestTimeout = 5 * time.Second
	cfg.ProbeTimeout = 2 * time.Second
	return cfg
}

func TestEndpoint_OnlyLoopbackHostsAreAccepted(t *testing.T) {
	for _, base := range []string{
		"",
		"http://localhost:11434",
		"http://127.0.0.1:11434",
		"http://[::1]:11434",
		"https://localhost",
		"http://127.0.0.1:11434/prefix",
	} {
		if _, err := New(endpointConfig(base)); err != nil {
			t.Errorf("%q was refused: %v", base, err)
		}
	}
}

func TestEndpoint_EveryOtherHostIsRefusedAtConstruction(t *testing.T) {
	for _, base := range []string{
		"http://example.com:11434",
		"https://api.example.com",
		"http://10.0.0.5:11434",
		"http://192.168.1.10:11434",
		"http://0.0.0.0:11434",
		"http://127.0.0.2:11434",               // loopback, but not one of the three names
		"http://[::ffff:127.0.0.1]:11434",      // an IPv4-mapped spelling
		"http://localhost.example.com:11434",   // a name that merely starts with localhost
		"http://127.0.0.1.nip.io:11434",        // a public name that resolves to loopback
		"http://user:secret@127.0.0.1:11434",   // credentials in the URL
		"http://ollama.internal.example:11434", // a private name
	} {
		_, err := New(endpointConfig(base))
		if err == nil {
			t.Errorf("%q was accepted", base)
			continue
		}
		if !strings.Contains(err.Error(), "BaseURL") {
			t.Errorf("%q: the error does not name BaseURL: %v", base, err)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("%q: the error echoes the URL's credentials: %v", base, err)
		}
	}
}

func TestEndpoint_TheDialerRefusesAResolvedNonLoopbackAddress(t *testing.T) {
	// Control sees the address after resolution, so a hosts file or resolver
	// that maps "localhost" elsewhere is refused here. 192.0.2.1 is TEST-NET-1:
	// never routed, and never reached, because Control refuses before connect.
	d := loopbackDialer(time.Second)
	for _, addr := range []string{"192.0.2.1:11434", "[2001:db8::1]:11434"} {
		conn, err := d.DialContext(context.Background(), "tcp", addr)
		if err == nil {
			_ = conn.Close() //nolint:errcheck // the test has already failed
			t.Fatalf("dialled %s", addr)
		}
		if !errors.Is(err, ErrNonLoopbackEndpoint) {
			t.Errorf("%s: want ErrNonLoopbackEndpoint, got %v", addr, err)
		}
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }() //nolint:errcheck // test cleanup
	conn, err := d.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("a loopback address was refused: %v", err)
	}
	_ = conn.Close() //nolint:errcheck // test cleanup
}

func TestEndpoint_TheAdaptersOwnClientUsesNoProxy(t *testing.T) {
	tr, ok := defaultClient().Transport.(*http.Transport)
	if !ok {
		t.Fatal("the default client has no *http.Transport")
	}
	if tr.Proxy != nil {
		t.Error("the default transport has a proxy function; a proxy would carry the prompt off this machine")
	}
}

// redirecting returns a loopback daemon that redirects every request to a
// second server, and that second server's hit counter.
func redirecting(t *testing.T, status int) (from string, hits *atomic.Int64) {
	t.Helper()
	hits = new(atomic.Int64)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, status)
	}))
	t.Cleanup(src.Close)
	return src.URL, hits
}

func TestEndpoint_RedirectsAreNeverFollowed(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		for _, supplied := range []bool{false, true} {
			base, hits := redirecting(t, status)
			cfg := endpointConfig(base)
			if supplied {
				cfg.HTTPClient = &http.Client{} // follows redirects by default
			}
			p, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}

			if perr := p.Probe(context.Background()); perr == nil {
				t.Errorf("status %d, supplied client %v: Probe followed or ignored a redirect", status, supplied)
			}
			st, err := p.Generate(context.Background(), rt.GenerateRequest{
				Model:           testModel,
				Messages:        []rt.Message{{Role: rt.RoleUser, Content: "a transcript that must stay here"}},
				MaxOutputTokens: 8,
			})
			if err == nil {
				_ = st.Close() //nolint:errcheck // the test has already failed
				t.Errorf("status %d, supplied client %v: Generate followed or ignored a redirect", status, supplied)
			}
			if n := hits.Load(); n != 0 {
				t.Errorf("status %d, supplied client %v: the redirect target received %d requests", status, supplied, n)
			}
		}
	}
}

func TestEndpoint_ASuppliedClientIsCopiedNotMutated(t *testing.T) {
	supplied := &http.Client{}
	if _, err := New(Config{
		ID: "ollama-dev", Model: testModel, BaseURL: DefaultBaseURL, HTTPClient: supplied,
		MaxContextTokens: 1, MaxOutputTokens: 1, RequestTimeout: time.Second,
		ProbeTimeout: time.Second, MaxChunkBytes: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if supplied.CheckRedirect != nil {
		t.Error("New changed the caller's client")
	}
}
