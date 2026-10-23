package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/services/go/edge-api/internal/auth"
	"github.com/callscreen/callscreen-platform/services/go/edge-api/internal/httpapi"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeRunners struct{ got []platform.Runner }

func (f *fakeRunners) Register(r platform.Runner) { f.got = append(f.got, r) }

type fakeChecks struct{ got []string }

func (f *fakeChecks) Register(c platform.Checker, critical bool, _ time.Duration) {
	f.got = append(f.got, fmt.Sprintf("%s critical=%v", c.Name(), critical))
}

// fakeRuntime is an auth runtime on an in-memory replay store, recording
// Close; it stands in for auth.Open, whose PostgreSQL paths are tested in the
// auth package.
type fakeRuntime struct {
	a      *auth.Authenticator
	closed int
}

func (f *fakeRuntime) Authenticator() *auth.Authenticator  { return f.a }
func (f *fakeRuntime) CheckDatabase(context.Context) error { return nil }
func (f *fakeRuntime) Close()                              { f.closed++ }

// fakeOpen replaces openAuth for one test with the real authenticator
// constructor on an in-memory store, and returns the runtimes it made.
func fakeOpen(t *testing.T) *[]*fakeRuntime {
	t.Helper()
	var made []*fakeRuntime
	orig := openAuth
	openAuth = func(_ context.Context, cfg auth.Config, log *slog.Logger) (authRuntime, error) {
		a, err := auth.New(cfg, dpop.NewMemoryReplayStore(), nil, log)
		if err != nil {
			return nil, err
		}
		rt := &fakeRuntime{a: a}
		made = append(made, rt)
		return rt, nil
	}
	t.Cleanup(func() { openAuth = orig })
	return &made
}

func clearAuthEnv(t *testing.T) {
	t.Helper()
	for _, n := range auth.ActivationVariables {
		t.Setenv(n, "")
		if err := os.Unsetenv(n); err != nil {
			t.Fatal(err)
		}
	}
}

// setAuthEnv writes a generated (test-only) public key and a credential-free
// DSN file naming a closed loopback port, and sets all six variables.
func setAuthEnv(t *testing.T) {
	t.Helper()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(pub)
	p := filepath.Join(t.TempDir(), "issuer.pub.pem")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	dsn := filepath.Join(t.TempDir(), "dsn")
	if err := os.WriteFile(dsn, []byte("postgres://edge@127.0.0.1:1/edge?connect_timeout=2"), 0o600); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"CS_EDGE_API_ACCESS_TOKEN_PUBLIC_KEY_FILE": p,
		"CS_EDGE_API_ACCESS_TOKEN_KID":             "test-key-1",
		"CS_EDGE_API_ACCESS_TOKEN_ISSUER":          "urn:callscreen:identity:test",
		"CS_EDGE_API_ACCESS_TOKEN_AUDIENCE":        "urn:callscreen:edge-api:test",
		"CS_EDGE_API_PUBLIC_ORIGIN":                "https://edge.example.test",
		"CS_EDGE_API_DATABASE_DSN_FILE":            dsn,
	} {
		t.Setenv(k, v)
	}
}

var ctx = context.Background()

var testCfg = platform.ServiceConfig{Name: serviceName, HTTPPort: 0, ReadHeaderTimeout: 5 * time.Second}

func someRoutes(reached *int) map[string]http.Handler {
	return map[string]http.Handler{"/v1/protected": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*reached++
		w.WriteHeader(http.StatusOK)
	})}
}

// configureAuth itself, with an empty table and no authentication: nothing
// registered. (The production table is not empty; see the next test.)
func TestConfigureAuth_InactiveWithoutRoutesRegistersNothing(t *testing.T) {
	clearAuthEnv(t)
	runners, checks := &fakeRunners{}, &fakeChecks{}
	if err := configureAuth(ctx, testCfg, runners, checks, nil, quiet); err != nil || len(runners.got) != 0 {
		t.Fatalf("(%v, %d runners)", err, len(runners.got))
	}
}

// The production table is exactly GET /v1/me, and without authentication
// edge-api refuses to start rather than serve it open.
func TestConfigureAuth_ProductionRoutesRequireAuthentication(t *testing.T) {
	routes := protectedRoutes(quiet)
	if len(routes) != 1 || routes[httpapi.MePath] == nil {
		t.Fatalf("production routes = %v; update the report and these tests deliberately", routes)
	}
	clearAuthEnv(t)
	runners, checks := &fakeRunners{}, &fakeChecks{}
	if err := configureAuth(ctx, testCfg, runners, checks, routes, quiet); !errors.Is(err, errRoutesWithoutAuth) || len(runners.got) != 0 || len(checks.got) != 0 {
		t.Fatalf("(%v, %d runners)", err, len(runners.got))
	}
}

// Routes with no authentication to put in front of them are refused, never
// served open.
func TestConfigureAuth_RoutesWithoutAuthenticationAreRefused(t *testing.T) {
	clearAuthEnv(t)
	n := 0
	runners, checks := &fakeRunners{}, &fakeChecks{}
	if err := configureAuth(ctx, testCfg, runners, checks, someRoutes(&n), quiet); !errors.Is(err, errRoutesWithoutAuth) || len(runners.got) != 0 {
		t.Fatalf("(%v, %d runners)", err, len(runners.got))
	}
}

func TestConfigureAuth_PartialOrInvalidFailsClosed(t *testing.T) {
	for _, only := range auth.ActivationVariables {
		t.Run(only, func(t *testing.T) {
			clearAuthEnv(t)
			t.Setenv(only, "x")
			runners, checks := &fakeRunners{}, &fakeChecks{}
			if err := configureAuth(ctx, testCfg, runners, checks, nil, quiet); !errors.Is(err, auth.ErrInvalidConfig) || len(runners.got) != 0 {
				t.Fatalf("(%v, %d runners)", err, len(runners.got))
			}
		})
	}
	t.Run("missing key file", func(t *testing.T) {
		setAuthEnv(t)
		t.Setenv("CS_EDGE_API_ACCESS_TOKEN_PUBLIC_KEY_FILE", filepath.Join(t.TempDir(), "absent.pem"))
		if err := configureAuth(ctx, testCfg, &fakeRunners{}, &fakeChecks{}, nil, quiet); !errors.Is(err, auth.ErrInvalidConfig) {
			t.Fatalf("err = %v", err)
		}
	})
}

// Configured but with no routes: the authenticator is built and validated,
// and no application listener is started.
func TestConfigureAuth_ConfiguredWithoutRoutesStartsNoListener(t *testing.T) {
	setAuthEnv(t)
	made := fakeOpen(t)
	runners, checks := &fakeRunners{}, &fakeChecks{}
	if err := configureAuth(ctx, testCfg, runners, checks, nil, quiet); err != nil || len(runners.got) != 0 || len(checks.got) != 0 {
		t.Fatalf("(%v, %d runners, %v)", err, len(runners.got), checks.got)
	}
	// Verified, then released: nothing would use the pool.
	if len(*made) != 1 || (*made)[0].closed != 1 {
		t.Errorf("runtime opened %d times, closed %v", len(*made), *made)
	}
}

// The real auth.Open against an unreachable database: a startup error, and
// nothing registered — with or without routes.
func TestConfigureAuth_UnreachableReplayStoreFailsClosed(t *testing.T) {
	setAuthEnv(t)
	for _, routes := range []map[string]http.Handler{nil, protectedRoutes(quiet)} {
		runners, checks := &fakeRunners{}, &fakeChecks{}
		err := configureAuth(ctx, testCfg, runners, checks, routes, quiet)
		if err == nil || len(runners.got) != 0 || len(checks.got) != 0 {
			t.Fatalf("(%v, %d runners, %v)", err, len(runners.got), checks.got)
		}
		if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "edge@") {
			t.Errorf("the error names the database: %v", err)
		}
	}
}

// With routes, exactly one listener, and its handler admits nothing without a
// token and proof.
func TestConfigureAuth_RoutesAreServedOnlyBehindAuthentication(t *testing.T) {
	setAuthEnv(t)
	made := fakeOpen(t)
	n := 0
	runners, checks := &fakeRunners{}, &fakeChecks{}
	if err := configureAuth(ctx, testCfg, runners, checks, someRoutes(&n), quiet); err != nil || len(runners.got) != 2 {
		t.Fatalf("(%v, %d runners)", err, len(runners.got))
	}
	// The pool owner first and the listener last: shutdown runs in reverse,
	// so requests drain before the pool closes.
	owner, ok := runners.got[0].(*authRunner)
	if !ok || owner.rt != (*made)[0] {
		t.Fatalf("first runner is %T", runners.got[0])
	}
	if web, ok := runners.got[1].(*platform.HTTPRunner); !ok || web.Name() != "edge-api-http" {
		t.Fatalf("last runner is %T", runners.got[1])
	}
	if len(checks.got) != 1 || checks.got[0] != "edge-replay-store critical=true" {
		t.Errorf("checks = %v", checks.got)
	}
	if (*made)[0].closed != 0 {
		t.Fatal("the runtime was closed while in use")
	}
	if err := owner.Shutdown(ctx); err != nil || (*made)[0].closed != 1 {
		t.Errorf("shutdown: (%v, closed %d)", err, (*made)[0].closed)
	}

	ac, _, err := auth.LoadActivation()
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(ac, dpop.NewMemoryReplayStore(), nil, quiet)
	if err != nil {
		t.Fatal(err)
	}
	h := applicationHandler(a, someRoutes(&n), nil)
	for _, hdr := range []map[string]string{nil, {"Authorization": "Bearer x"}, {"Authorization": "DPoP x", "DPoP": "y"}} {
		req := httptest.NewRequest("GET", "/v1/protected", http.NoBody)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("X-Correlation-ID") == "" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%v -> %d %v", hdr, rec.Code, rec.Header())
		}
	}
	if n != 0 {
		t.Error("a protected route ran without authentication")
	}
}

// The health listener stays public: probes need no token, and with the
// skeleton's empty route table no application port is bound.
func TestLifecycle_HealthIsPublicAndNoApplicationPortIsBound(t *testing.T) {
	setAuthEnv(t)
	fakeOpen(t)
	pick := func() int {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	}
	cfg := platform.ServiceConfig{Name: serviceName, Version: "test", HTTPPort: pick(), HealthPort: pick(), ShutdownTimeout: time.Second, ReadHeaderTimeout: 5 * time.Second}
	health := platform.NewHealth(cfg)
	svc := platform.NewService(cfg, quiet, health)
	if err := configureAuth(ctx, cfg, svc, health, nil, quiet); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- svc.Run(runCtx) }()
	var code int
	for i := 0; i < 100; i++ {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", cfg.HealthPort))
		if err == nil {
			code = resp.StatusCode
			resp.Body.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code != http.StatusOK {
		t.Errorf("/healthz without credentials = %d, want 200", code)
	}
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.HTTPPort), 300*time.Millisecond); err == nil {
		c.Close()
		t.Error("an application port was bound with no routes to serve")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("service did not stop")
	}
}
