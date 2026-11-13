package otpdelivery

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// MSG91's IPv4-only mode (MSG91Config.IPv4Only, restrictToIPv4): IPv4
// connections only, no IPv6 fallback, no proxy, and everything else as
// before. Every test runs against loopback stand-ins or makes no connection;
// nothing here contacts MSG91 (TestMain's dead proxy also stands guard).

func clientOf(t *testing.T, p Provider) *http.Client {
	t.Helper()
	switch m := p.(type) {
	case *msg91:
		return m.client
	case *msg91WithDeletion:
		return m.client
	case *twilio:
		return m.client
	case *twilioWithDeletion:
		return m.client
	}
	t.Fatalf("unexpected provider type %T", p)
	return nil
}

func ipv4Config(t *testing.T, srv *httptest.Server) MSG91Config {
	t.Helper()
	cfg := msg91Config(t, srv)
	cfg.IPv4Only = true
	return cfg
}

// Off (the default), the adapter's client is exactly as before: the supplied
// transport itself, or the production transport with no custom dialer.
func TestMSG91IPv4_OffByDefaultChangesNothing(t *testing.T) {
	_, srv := newCapture(t, http.StatusOK, `{"type":"success","message":"abc123"}`)
	p, err := NewMSG91(msg91Config(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	if clientOf(t, p).Transport != srv.Client().Transport {
		t.Error("with IPv4Only unset the supplied transport was replaced")
	}
	if sendErr := p.SendOtp(context.Background(), indiaNumber, adapterCode); sendErr != nil {
		t.Fatalf("SendOtp: %v", sendErr)
	}

	cfg := msg91Config(t, srv)
	cfg.BaseURL, cfg.HTTPClient = "", nil // the production client
	p, err = NewMSG91(cfg)
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := clientOf(t, p).Transport.(*http.Transport)
	if !ok || tr.DialContext != nil {
		t.Errorf("the production transport has a custom dialer with IPv4Only unset (%T)", clientOf(t, p).Transport)
	}
}

// "localhost" resolves to both families; the request still arrives over IPv4.
// TLS is verified against the stand-in's certificate as before.
func TestMSG91IPv4_DeliversOverIPv4(t *testing.T) {
	var remote atomic.Value
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remote.Store(r.RemoteAddr)
		_, _ = w.Write([]byte(`{"type":"success","message":"5e1e93cad6fc054d8e759a5b"}`)) //nolint:errcheck // test server
	}))
	t.Cleanup(srv.Close)
	tr, ok := srv.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("stand-in transport is %T", srv.Client().Transport)
	}
	tr = tr.Clone()
	tr.TLSClientConfig.ServerName = "127.0.0.1" // the stand-in's certificate names the IP, not "localhost"
	cfg := ipv4Config(t, srv)
	cfg.HTTPClient = &http.Client{Transport: tr}
	cfg.BaseURL = "https://localhost:" + srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	var ids []string
	cfg.Accepted = func(id string) { ids = append(ids, id) }
	p, err := NewMSG91(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if sendErr := p.SendOtp(context.Background(), indiaNumber, adapterCode); sendErr != nil {
		t.Fatalf("SendOtp: %v", sendErr)
	}
	addr, _ := remote.Load().(string)
	host, _, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host).To4() == nil {
		t.Errorf("the stand-in saw %q: want an IPv4 peer", addr)
	}
	if len(ids) != 1 || ids[0] != "5e1e93cad6fc054d8e759a5b" {
		t.Errorf("request id handling changed: %v", ids)
	}
}

// An IPv6 address is refused before any connection: there is no fallback.
func TestMSG91IPv4_NoIPv6Fallback(t *testing.T) {
	var conns atomic.Int64
	port := "443"
	if lis, err := net.Listen("tcp6", "[::1]:0"); err == nil {
		// When IPv6 loopback exists, a listener proves no connection arrives.
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		srv.Listener = lis
		srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
			if s == http.StateNew {
				conns.Add(1)
			}
		}
		srv.StartTLS()
		t.Cleanup(srv.Close)
		port = srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	}
	_, any4 := newCapture(t, http.StatusOK, `{"type":"success"}`)
	cfg := ipv4Config(t, any4)
	cfg.BaseURL = "https://[::1]:" + port
	p, err := NewMSG91(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SendOtp(context.Background(), indiaNumber, adapterCode); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("SendOtp to an IPv6 address = %v, want ErrProviderUnavailable", err)
	}
	if n := conns.Load(); n != 0 {
		t.Errorf("%d IPv6 connections were made", n)
	}

	// The dialer itself (at the IPv6 listener when there is one): tcp4 refuses
	// an IPv6 address.
	tr, ok := clientOf(t, p).Transport.(*http.Transport)
	if !ok || tr.DialContext == nil {
		t.Fatal("IPv4-only transport has no dialer")
	}
	if c, err := tr.DialContext(context.Background(), "tcp", "[::1]:"+port); err == nil {
		_ = c.Close() //nolint:errcheck // test cleanup
		t.Error("the IPv4-only dialer connected to an IPv6 address")
	}
}

// A proxy is refused: at construction when the provider's own address would
// be proxied, and for any request the environment would proxy.
func TestMSG91IPv4_RefusesAProxy(t *testing.T) {
	_, srv := newCapture(t, http.StatusOK, `{"type":"success"}`)
	proxyURL, _ := url.Parse("http://127.0.0.1:9") //nolint:errcheck // constant

	// The provider's address proxied: construction fails.
	tr := srv.Client().Transport.(*http.Transport).Clone() //nolint:forcetypeassert // httptest's client
	tr.Proxy = func(*http.Request) (*url.URL, error) { return proxyURL, nil }
	cfg := ipv4Config(t, srv)
	cfg.HTTPClient = &http.Client{Transport: tr}
	if _, err := NewMSG91(cfg); !errors.Is(err, ErrProviderConfig) {
		t.Errorf("a proxied provider address was accepted: %v", err)
	}

	// Another address proxied: construction succeeds, but such a request is
	// refused without any connection.
	tr = srv.Client().Transport.(*http.Transport).Clone() //nolint:forcetypeassert // httptest's client
	tr.Proxy = func(r *http.Request) (*url.URL, error) {
		if r.URL.Hostname() == "other.example" {
			return proxyURL, nil
		}
		return nil, nil
	}
	cfg.HTTPClient = &http.Client{Transport: tr}
	p, err := NewMSG91(cfg)
	if err != nil {
		t.Fatal(err)
	}
	restricted := clientOf(t, p).Transport.(*http.Transport)                          //nolint:forcetypeassert // set by restrictToIPv4
	req, _ := http.NewRequest(http.MethodPost, "https://other.example/", http.NoBody) //nolint:errcheck // constant
	if u, err := restricted.Proxy(req); u != nil || !errors.Is(err, errIPv4OnlyProxy) {
		t.Errorf("proxy decision = %v, %v: want errIPv4OnlyProxy", u, err)
	}

	// The real MSG91 host under this package's TestMain (a dead proxy in the
	// environment): construction refuses, so even IPv4-only mode cannot reach
	// MSG91 from a test.
	cfg = ipv4Config(t, srv)
	cfg.BaseURL, cfg.HTTPClient = "", nil
	if _, err := NewMSG91(cfg); !errors.Is(err, ErrProviderConfig) {
		t.Errorf("IPv4-only mode accepted a proxied real host: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A transport that cannot be restricted is a configuration error, never a
// silently unrestricted client.
func TestMSG91IPv4_FailsClosedWhenItCannotBeEnforced(t *testing.T) {
	_, srv := newCapture(t, http.StatusOK, `{"type":"success"}`)
	var nilTransport *http.Transport
	for name, rt := range map[string]http.RoundTripper{
		"a custom RoundTripper": roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("unused") }),
		"its own TLS dialer": &http.Transport{DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("unused")
		}},
		"a nil *http.Transport": nilTransport,
	} {
		cfg := ipv4Config(t, srv)
		cfg.HTTPClient = &http.Client{Transport: rt}
		if _, err := NewMSG91(cfg); !errors.Is(err, ErrProviderConfig) {
			t.Errorf("%s: err = %v, want ErrProviderConfig", name, err)
		}
	}
}

// Certificate verification is kept: a stand-in the client does not trust is
// refused and its handler is never reached.
func TestMSG91IPv4_TLSIsStillVerified(t *testing.T) {
	c, srv := newCapture(t, http.StatusOK, `{"type":"success"}`)
	cfg := ipv4Config(t, srv)
	cfg.HTTPClient = &http.Client{} // the default transport: system roots only
	p, err := NewMSG91(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if sendErr := p.SendOtp(context.Background(), indiaNumber, adapterCode); !errors.Is(sendErr, ErrProviderUnavailable) {
		t.Errorf("an untrusted certificate gave %v, want ErrProviderUnavailable", sendErr)
	}
	if n := c.count(); n != 0 {
		t.Errorf("%d requests reached a server with an untrusted certificate", n)
	}
	tr := clientOf(t, p).Transport.(*http.Transport) //nolint:forcetypeassert // set by restrictToIPv4
	if tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("certificate verification is off")
	}
	// The supplied stand-in client's TLS configuration is kept as-is.
	cfg = ipv4Config(t, srv)
	p, err = NewMSG91(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := clientOf(t, p).Transport.(*http.Transport).TLSClientConfig                        //nolint:forcetypeassert // set by restrictToIPv4
	want := srv.Client().Transport.(*http.Transport).TLSClientConfig                         //nolint:forcetypeassert // httptest's client
	if got == nil || want == nil || got.RootCAs == nil || !got.RootCAs.Equal(want.RootCAs) { //nolint:staticcheck // comparing the trusted roots
		t.Error("the TLS configuration was not carried over")
	}
}

// Redirects are still refused, and the redirect target receives nothing.
func TestMSG91IPv4_RedirectsAreStillRefused(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(target.Close)
	redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	p, err := NewMSG91(ipv4Config(t, redirector))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SendOtp(context.Background(), indiaNumber, adapterCode); err == nil {
		t.Error("a redirect was followed or ignored")
	}
	if hits.Load() != 0 {
		t.Errorf("the redirect target received %d requests", hits.Load())
	}
}

// The caller's deadline still bounds an attempt, with exactly one request.
func TestMSG91IPv4_TimeoutIsOneRequest(t *testing.T) {
	c, srv := newCapture(t, http.StatusOK, "")
	c.mu.Lock()
	c.hold = true
	c.mu.Unlock()
	p, err := NewMSG91(ipv4Config(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := p.SendOtp(ctx, indiaNumber, adapterCode); err == nil {
		t.Fatal("an unanswered send reported success")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("the attempt took %v: the deadline did not bound it", d)
	}
	if n := c.count(); n != 1 {
		t.Errorf("%d requests, want exactly 1", n)
	}
}

// The India-only rule and the fixed errors are unchanged in IPv4-only mode.
func TestMSG91IPv4_DomesticOnlyAndFixedErrors(t *testing.T) {
	c, srv := newCapture(t, http.StatusOK, `{"type":"error","message":"echo 919876543210 135790"}`)
	p, err := NewMSG91(ipv4Config(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	if sendErr := p.SendOtp(context.Background(), "+14155552671", adapterCode); !errors.Is(sendErr, ErrNotDomestic) {
		t.Errorf("a non-Indian number gave %v", sendErr)
	}
	if c.count() != 0 {
		t.Error("a non-Indian number was sent")
	}
	err = p.SendOtp(context.Background(), indiaNumber, adapterCode)
	if !errors.Is(err, ErrRejected) || strings.Contains(err.Error(), "9876543210") || strings.Contains(err.Error(), adapterCode) {
		t.Errorf("error = %v: want the fixed ErrRejected", err)
	}
}

// Twilio is not affected: it has no IPv4-only option and keeps the plain
// guarded client with no custom dialer.
func TestMSG91IPv4_TwilioIsUnaffected(t *testing.T) {
	_, srv := newCapture(t, http.StatusCreated, `{}`)
	cfg := twilioConfig(t, srv)
	cfg.BaseURL, cfg.HTTPClient = "", nil // the production client
	p, err := NewTwilio(cfg)
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := clientOf(t, p).Transport.(*http.Transport)
	if !ok || tr.DialContext != nil || tr.ForceAttemptHTTP2 {
		t.Error("the Twilio transport was changed")
	}
}
