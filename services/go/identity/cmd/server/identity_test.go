package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/pgshared"
	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/bootstrap"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/pgstore"
)

type fakeRunners struct {
	got     []platform.Runner
	metrics http.Handler
}

func (f *fakeRunners) Register(r platform.Runner)       { f.got = append(f.got, r) }
func (f *fakeRunners) SetMetricsHandler(h http.Handler) { f.metrics = h }

type registeredCheck struct {
	c        platform.Checker
	critical bool
}

type fakeHealth struct{ got []registeredCheck }

func (f *fakeHealth) Register(c platform.Checker, critical bool, _ time.Duration) {
	f.got = append(f.got, registeredCheck{c, critical})
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// testServiceConfig binds the application listener to an ephemeral port.
var testServiceConfig = platform.ServiceConfig{Name: serviceName, HTTPPort: 0, ReadHeaderTimeout: 5 * time.Second}

func clearIdentityEnv(t *testing.T) {
	t.Helper()
	for _, n := range bootstrap.ActivationVariables {
		t.Setenv(n, "")
		if err := os.Unsetenv(n); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// setFullIdentityEnv writes test-only key, HMAC and DSN files and sets all
// eight variables. Nothing here is, or may become, a production value.
func setFullIdentityEnv(t *testing.T, dsn string) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	hmacKey := make([]byte, 48)
	_, _ = rand.Read(hmacKey)
	for name, v := range map[string]string{
		"CS_IDENTITY_ACCESS_TOKEN_PRIVATE_KEY_FILE": writeFile(t, "signing.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"CS_IDENTITY_ACCESS_TOKEN_ISSUER":           "urn:callscreen:identity:test",
		"CS_IDENTITY_ACCESS_TOKEN_AUDIENCE":         "urn:callscreen:edge-api:test",
		"CS_IDENTITY_ACCESS_TOKEN_KID":              "test-key-1",
		"CS_IDENTITY_ENROLLMENT_CANONICAL_URL":      "https://identity.example.test/v1/otp/validate",
		"CS_IDENTITY_REFRESH_CANONICAL_URL":         "https://identity.example.test/v1/token/refresh",
		"CS_IDENTITY_HMAC_SECRET_KEY_FILE":          writeFile(t, "hmac.key", hmacKey),
		"CS_IDENTITY_DATABASE_DSN_FILE":             writeFile(t, "dsn", []byte(dsn+"\n")),
	} {
		t.Setenv(name, v)
	}
}

func TestDispatch_UnknownCommandIsRefused(t *testing.T) {
	for _, args := range [][]string{{"migrat"}, {"migrate", "extra"}, {"serve"}} {
		if err := dispatch(args); err == nil || !strings.Contains(err.Error(), "unknown command") {
			t.Errorf("dispatch(%q) = %v", args, err)
		}
	}
}

// 5: no identity variable keeps the skeleton: nothing constructed, no runner,
// readiness still 200 — reported "degraded" with an explicit, non-critical
// "identity inactive" check rather than implying identity is available.
func TestConfigureIdentity_InactivePreservesTheSkeleton(t *testing.T) {
	clearIdentityEnv(t)
	// Inactive must not reach construction at all: no pool, no housekeeper.
	opened := 0
	orig := openIdentity
	openIdentity = func(ctx context.Context, c bootstrap.ActivationConfig, now func() time.Time) (*bootstrap.Runtime, error) {
		opened++
		return orig(ctx, c, now)
	}
	t.Cleanup(func() { openIdentity = orig })
	runners, health := &fakeRunners{}, &fakeHealth{}
	if err := configureIdentity(context.Background(), testServiceConfig, runners, health, quiet); err != nil {
		t.Fatalf("inactive: %v", err)
	}
	if opened != 0 {
		t.Errorf("inactive identity opened a runtime %d times", opened)
	}
	if len(runners.got) != 0 {
		t.Error("a runner (identity or housekeeping) was registered for an inactive identity")
	}
	if runners.metrics != nil {
		t.Error("metrics were mounted for an inactive identity, which serves no API to count")
	}
	if len(health.got) != 1 || health.got[0].critical || health.got[0].c.Name() != "identity" {
		t.Fatalf("checks = %+v, want one non-critical identity check", health.got)
	}
	if err := health.got[0].c.Check(context.Background()); !errors.Is(err, errIdentityInactive) {
		t.Errorf("check = %v", err)
	}

	// Against the real readiness handler: the status code is the skeleton's.
	cfg := platform.ServiceConfig{Name: serviceName}
	healthReal := platform.NewHealth(cfg)
	healthReal.MarkReady()
	if err := configureIdentity(context.Background(), testServiceConfig, &fakeRunners{}, healthReal, quiet); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	healthReal.ReadinessHandler()(rec, httptest.NewRequest(http.MethodGet, "/readyz", http.NoBody))
	var body struct {
		Status string `json:"status"`
		Checks []struct {
			Name, Message string
		} `json:"checks"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusOK || body.Status != "degraded" || len(body.Checks) != 1 ||
		!strings.Contains(body.Checks[0].Message, "identity inactive") {
		t.Errorf("readiness = %d %s", rec.Code, rec.Body.String())
	}
}

// 6: any identity variable without the rest stops startup, registering nothing.
func TestConfigureIdentity_PartialConfigurationFailsClosed(t *testing.T) {
	for _, only := range bootstrap.ActivationVariables {
		t.Run(only, func(t *testing.T) {
			clearIdentityEnv(t)
			t.Setenv(only, "x")
			runners, health := &fakeRunners{}, &fakeHealth{}
			err := configureIdentity(context.Background(), testServiceConfig, runners, health, quiet)
			if !errors.Is(err, bootstrap.ErrInvalidConfig) {
				t.Fatalf("err = %v, want ErrInvalidConfig", err)
			}
			if len(runners.got)+len(health.got) != 0 {
				t.Error("something was registered for a partial configuration")
			}
		})
	}
}

// setEnvironment sets, or with value nil removes, this service's ENVIRONMENT
// variable for one test.
func setEnvironment(t *testing.T, value *string) {
	t.Helper()
	name := envPrefix + "_ENVIRONMENT"
	t.Setenv(name, "")
	if value == nil {
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Setenv(name, *value)
}

func ptr(s string) *string { return &s }

// ADR-0029 "Database TLS": outside an EXPLICIT development environment a DSN
// without verify-full TLS is refused before any connection is attempted, and
// the refusal names no credential or host.
func TestConfigureIdentity_PlaintextDatabaseIsRefusedOutsideDevelopment(t *testing.T) {
	for name, env := range map[string]*string{"unset": nil, "staging": ptr("staging"), "production": ptr("production")} {
		t.Run(name, func(t *testing.T) {
			clearIdentityEnv(t)
			setFullIdentityEnv(t, "postgres://alice:hunter2-pw@127.0.0.1:1/identity?connect_timeout=2")
			setEnvironment(t, env)
			runners, health := &fakeRunners{}, &fakeHealth{}
			err := configureIdentity(context.Background(), testServiceConfig, runners, health, quiet)
			if !errors.Is(err, pgshared.ErrInsecureDSN) {
				t.Fatalf("err = %v, want ErrInsecureDSN", err)
			}
			for _, s := range []string{"alice", "hunter2-pw", "127.0.0.1"} {
				if strings.Contains(err.Error(), s) {
					t.Errorf("the error carries %q: %v", s, err)
				}
			}
			if len(runners.got)+len(health.got) != 0 {
				t.Error("something was registered although construction failed")
			}
		})
	}
}

// 8, 4: complete configuration but an unreachable database stops startup,
// with no credential in the error.
func TestConfigureIdentity_UnreachableDatabaseFailsClosed(t *testing.T) {
	clearIdentityEnv(t)
	setFullIdentityEnv(t, "postgres://alice:hunter2-pw@127.0.0.1:1/identity?connect_timeout=2")
	// Explicit development: plaintext is allowed, so the failure is the
	// connection itself.
	setEnvironment(t, ptr("development"))
	runners, health := &fakeRunners{}, &fakeHealth{}
	err := configureIdentity(context.Background(), testServiceConfig, runners, health, quiet)
	if !errors.Is(err, pgstore.ErrConnect) {
		t.Fatalf("err = %v, want ErrConnect", err)
	}
	for _, s := range []string{"alice", "hunter2-pw", "127.0.0.1"} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("the error carries %q: %v", s, err)
		}
	}
	if len(runners.got)+len(health.got) != 0 {
		t.Error("something was registered although construction failed")
	}
}

func TestIdentityRunner_RunReturnsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- (&identityRunner{}).Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestRunMigrate_FailsClosedWithoutLeaking(t *testing.T) {
	clearIdentityEnv(t)
	var out bytes.Buffer
	if err := runMigrate(context.Background(), &out); err == nil || !strings.Contains(err.Error(), "CS_IDENTITY_DATABASE_DSN_FILE is required") {
		t.Errorf("unset: %v", err)
	}
	t.Setenv("CS_IDENTITY_DATABASE_DSN_FILE", "   ")
	if err := runMigrate(context.Background(), &out); err == nil || !strings.Contains(err.Error(), "CS_IDENTITY_DATABASE_DSN_FILE is required") {
		t.Errorf("blank path: %v", err)
	}
	for name, content := range map[string]string{
		"blank":       "  \n",
		"malformed":   "postgres://alice:hunter2-pw@127.0.0.1:notaport/identity",
		"unreachable": "postgres://alice:hunter2-pw@127.0.0.1:1/identity?connect_timeout=2",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeFile(t, "dsn", []byte(content))
			t.Setenv("CS_IDENTITY_DATABASE_DSN_FILE", path)
			out.Reset()
			err := runMigrate(context.Background(), &out)
			if err == nil {
				t.Fatal("migrate succeeded")
			}
			for _, s := range []string{"alice", "hunter2-pw", "127.0.0.1", path} {
				if strings.Contains(err.Error()+out.String(), s) {
					t.Errorf("output or error carries %q: %v", s, err)
				}
			}
		})
	}
}

// 11: the only HTTP surface is the identity API, created in exactly one place
// (the activated branch of configureIdentity) by exactly one HTTPRunner whose
// handler is httpapi.New. The main.go file mounts nothing, no file builds a mux or
// listener of its own, and the legacy proof-less handlers in internal/app
// (IdentityHandler, HandleValidateOtp, HandleRequestOtp) are never reachable.
func TestServer_OnlyTheIdentityAPIIsServed(t *testing.T) {
	forbidden := map[string]bool{
		"NewIdentityHandler": true, "IdentityHandler": true, "HandleValidateOtp": true, "HandleRequestOtp": true,
		"HandleFunc": true, "Handle": true, "NewServeMux": true, "ListenAndServe": true,
	}
	runners := map[string]int{}
	for _, file := range []string{"main.go", "identity.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p == "net/http" || strings.HasSuffix(p, "/middleware") || strings.HasSuffix(p, "/internal/app") {
				t.Errorf("%s imports %s", file, p)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && forbidden[sel.Sel.Name] {
				t.Errorf("%s references %s", file, sel.Sel.Name)
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "NewHTTPRunner" {
				runners[file]++
				h, ok := call.Args[2].(*ast.CallExpr)
				hs, ok2 := h.Fun.(*ast.SelectorExpr)
				if !ok || !ok2 || hs.Sel.Name != "New" || fmt.Sprint(hs.X) != "httpapi" {
					t.Errorf("%s: an HTTP runner serves something other than httpapi.New", file)
				}
			}
			return true
		})
	}
	if runners["main.go"] != 0 || runners["identity.go"] != 1 {
		t.Errorf("HTTP runners created: %v, want exactly one, in identity.go", runners)
	}
}

// ---------------------------------------------------------------------------
// Real PostgreSQL: skipped unless AEGIS_IDENTITY_PG_DSN is set, refused unless
// AEGIS_IDENTITY_PG_DISPOSABLE=1.
// ---------------------------------------------------------------------------

func freshSchemaDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("AEGIS_IDENTITY_PG_DSN"))
	if dsn == "" {
		t.Skip("AEGIS_IDENTITY_PG_DSN is not set: real-PostgreSQL test SKIPPED (not passed)")
	}
	if os.Getenv("AEGIS_IDENTITY_PG_DISPOSABLE") != "1" {
		t.Fatal("AEGIS_IDENTITY_PG_DSN is set but AEGIS_IDENTITY_PG_DISPOSABLE is not 1: refusing")
	}
	admin, err := pgstore.OpenPool(context.Background(), dsn, pgshared.AllowPlaintextForDevelopment)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "identity_test_" + hex.EncodeToString(b)
	if _, err := admin.Exec(context.Background(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("dropping %s: %v", schema, err)
		}
		admin.Close()
	})
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "search_path=" + schema
}

// The explicit migration command: applies the chain to an empty schema,
// reports names and version, is idempotent, and prints no connection detail.
func TestRunMigrate_Postgres(t *testing.T) {
	dsn := freshSchemaDSN(t)
	clearIdentityEnv(t)
	t.Setenv("CS_IDENTITY_DATABASE_DSN_FILE", writeFile(t, "dsn", []byte(dsn)))
	setEnvironment(t, ptr("development")) // the disposable test database is plaintext
	var out bytes.Buffer
	if err := runMigrate(context.Background(), &out); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !strings.Contains(out.String(), "applied: 0001_identity_security_state.sql") ||
		!strings.Contains(out.String(), "applied: 0002_refresh_token_hash.sql") ||
		!strings.Contains(out.String(), "applied: 0003_housekeeping_indexes.sql") ||
		!strings.Contains(out.String(), "applied: 0004_msisdn_pseudonyms.sql") || !strings.Contains(out.String(), "version 4") {
		t.Errorf("output = %q", out.String())
	}
	out.Reset()
	if err := runMigrate(context.Background(), &out); err != nil || !strings.Contains(out.String(), "already applied: 0001") {
		t.Errorf("re-run: (%q, %v)", out.String(), err)
	}
	if strings.Contains(out.String(), "identity_test") || strings.Contains(out.String(), "@") {
		t.Errorf("the output carries connection detail: %q", out.String())
	}
}

// 7, 10: complete configuration constructs the service on PostgreSQL,
// registers its pool owner and a critical database check, and shutting the
// runner down closes the pool.
func TestConfigureIdentity_Postgres(t *testing.T) {
	dsn := freshSchemaDSN(t)
	clearIdentityEnv(t)
	t.Setenv("CS_IDENTITY_DATABASE_DSN_FILE", writeFile(t, "dsn", []byte(dsn)))
	setEnvironment(t, ptr("development")) // the disposable test database is plaintext
	var out bytes.Buffer
	if err := runMigrate(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	setFullIdentityEnv(t, dsn)
	runners, health := &fakeRunners{}, &fakeHealth{}
	if err := configureIdentity(context.Background(), testServiceConfig, runners, health, quiet); err != nil {
		t.Fatalf("activation: %v", err)
	}
	if len(runners.got) != 3 || len(health.got) != 2 ||
		!health.got[0].critical || health.got[0].c.Name() != "identity-postgres" ||
		health.got[1].critical || health.got[1].c.Name() != "identity-housekeeping" {
		t.Fatalf("runners %d, checks %+v", len(runners.got), health.got)
	}
	// The pool owner first, housekeeping second: shutdown runs in reverse, so
	// housekeeping stops before the pool closes.
	r := runners.got[0].(*identityRunner)
	hk, ok := runners.got[1].(*bootstrap.HousekeepingRunner)
	if !ok {
		t.Fatalf("second runner is %T", runners.got[1])
	}
	// The HTTP listener is registered LAST, so it is the FIRST to stop.
	if web, ok := runners.got[2].(*platform.HTTPRunner); !ok || web.Name() != "identity-http" {
		t.Fatalf("third runner is %T, want the identity HTTP runner", runners.got[2])
	}
	// The API's outcome counter is mounted for GET /metrics on the health
	// listener, alongside the listener it counts.
	if runners.metrics == nil {
		t.Fatal("no metrics handler was mounted with the identity API")
	}
	scrape := httptest.NewRecorder()
	runners.metrics.ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
	if scrape.Code != http.StatusOK {
		t.Errorf("metrics scrape = %d", scrape.Code)
	}
	runCtx, cancelRun := context.WithCancel(context.Background())
	hkDone := make(chan error, 1)
	go func() { hkDone <- hk.Run(runCtx) }()
	if err := health.got[1].c.Check(context.Background()); err != nil {
		t.Errorf("housekeeping readiness: %v", err)
	}
	if err := hk.Shutdown(context.Background()); err != nil {
		t.Fatalf("housekeeping shutdown: %v", err)
	}
	cancelRun()
	if err := <-hkDone; err != nil {
		t.Errorf("housekeeping Run = %v", err)
	}
	if r.rt.Service == nil {
		t.Fatal("no service constructed")
	}
	if err := health.got[0].c.Check(context.Background()); err != nil {
		t.Fatalf("readiness: %v", err)
	}
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := health.got[0].c.Check(context.Background()); err == nil || err.Error() != "identity database unreachable" {
		t.Errorf("after shutdown: %v, want the pool closed and the fixed message", err)
	}
}

// The production tracer is the log-exporter one, never nil: identity's
// listener always emits edge spans (ADR-0026 step 2).
func TestIdentityTracer_ProductionDefaultIsReal(t *testing.T) {
	if identityTracer(quiet) == nil {
		t.Fatal("the production tracer is nil, so no edge span would be emitted")
	}
}
