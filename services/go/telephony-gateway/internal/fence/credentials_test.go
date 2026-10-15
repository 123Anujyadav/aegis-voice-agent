package fence

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// P1-A1 — the provider-neutral credential seam (C1). Offline: a fake
// Credentials source and an injected connect step; no AWS, no database.

type fakeCreds struct {
	mu    sync.Mutex
	calls []string // host:port/user per call
	n     atomic.Int64
	err   error
	block bool
}

func (f *fakeCreds) Password(ctx context.Context, host string, port uint16, user string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fmt.Sprintf("%s:%d/%s", host, port, user))
	f.mu.Unlock()
	if f.block {
		<-ctx.Done()
		return "", errors.New("aws: AssumeRoleWithWebIdentity arn:aws:iam::123456789012:role/x TOKEN-MARKER")
	}
	if f.err != nil {
		return "", f.err
	}
	return fmt.Sprintf("token-%d-SECRETMARKER", f.n.Add(1)), nil
}

func testConnConfig(t *testing.T) *pgx.ConnConfig {
	t.Helper()
	c, err := ParseDSN("postgres://fence_user@writer.example.internal:5432/telephony?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// recordingConnect captures the password each connection attempt receives.
type recordingConnect struct {
	mu        sync.Mutex
	passwords []string
	err       error
}

func (r *recordingConnect) connect(_ context.Context, cfg *pgx.ConnConfig) (Session, error) {
	r.mu.Lock()
	r.passwords = append(r.passwords, cfg.Password)
	r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	return &fakeSession{}, nil
}

func TestCredentialDialer_AFreshPasswordForEveryConnection(t *testing.T) {
	t.Parallel()
	cfg := testConnConfig(t)
	creds := &fakeCreds{}
	rc := &recordingConnect{}
	dial := credentialDialer(cfg, creds, rc.connect)
	for range 3 {
		if _, err := dial(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(rc.passwords) != 3 || rc.passwords[0] == rc.passwords[1] || rc.passwords[1] == rc.passwords[2] {
		t.Errorf("passwords per dial = %v; want three distinct values", rc.passwords)
	}
	for _, c := range creds.calls {
		if c != "writer.example.internal:5432/fence_user" {
			t.Errorf("credentials requested for %q; want the DSN's own host:port/user", c)
		}
	}
	if cfg.Password != "" {
		t.Error("the shared config was mutated: it now holds a password")
	}
}

// Through the fence: acquisition, a forced loss, and re-acquisition each use a
// new credential.
func TestCredentialDialer_ReacquisitionAfterALossUsesANewCredential(t *testing.T) {
	t.Parallel()
	var fail atomic.Bool
	session := &fakeSession{holds: func(context.Context) (bool, error) {
		if fail.Load() {
			return false, errors.New("session lost")
		}
		return true, nil
	}}
	creds := &fakeCreds{}
	rc := &recordingConnect{}
	r := newRig(t, nil, session)
	r.f.dial = credentialDialer(testConnConfig(t), creds, func(ctx context.Context, cfg *pgx.ConnConfig) (Session, error) {
		_, _ = rc.connect(ctx, cfg)
		return session, nil
	})
	r.start(t)
	r.c.step(t)
	r.c.step(t)
	eventually(t, "ownership", r.f.Owned)
	fail.Store(true)
	r.c.step(t)
	eventually(t, "ownership to end", func() bool { return !r.f.Owned() })
	fail.Store(false)
	r.c.step(t) // retry interval, then a new dial
	eventually(t, "a second dial", func() bool { rc.mu.Lock(); defer rc.mu.Unlock(); return len(rc.passwords) >= 2 })
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.passwords[0] == rc.passwords[1] {
		t.Errorf("re-dial reused the previous credential %q", rc.passwords[0])
	}
}

// A credential failure: no connection attempt, a fixed error with none of the
// source's text, never owner.
func TestCredentialDialer_AFailingSourceFailsClosedWithoutLeaking(t *testing.T) {
	t.Parallel()
	creds := &fakeCreds{err: errors.New("sts: denied for arn:aws:iam::123456789012:role/gw TOKEN-MARKER")}
	rc := &recordingConnect{}
	dial := credentialDialer(testConnConfig(t), creds, rc.connect)
	_, err := dial(context.Background())
	if !errors.Is(err, ErrCredentialsUnavailable) {
		t.Fatalf("err = %v", err)
	}
	for _, leak := range []string{"arn:aws", "123456789012", "TOKEN-MARKER", "sts"} {
		if strings.Contains(err.Error(), leak) || strings.Contains(redact(err).Error(), leak) {
			t.Errorf("error leaks %q: %v", leak, err)
		}
	}
	if len(rc.passwords) != 0 {
		t.Error("a connection was attempted without a credential")
	}

	// Through the fence: never owner, readiness false, Run continues, and the
	// log carries nothing from the source.
	var logs strings.Builder
	var lmu sync.Mutex
	r := newRig(t, nil, &fakeSession{})
	r.f.log = newTestLogger(&logs, &lmu)
	r.f.dial = credentialDialer(testConnConfig(t), creds, rc.connect)
	r.start(t)
	r.c.step(t)
	r.c.step(t)
	if r.f.Owned() || r.f.Check(context.Background()) == nil {
		t.Error("owner, or ready, without credentials")
	}
	select {
	case err := <-r.ran:
		t.Fatalf("Run ended on a credential failure: %v", err)
	default:
	}
	lmu.Lock()
	out := logs.String()
	lmu.Unlock()
	if !strings.Contains(out, "cannot open a database session") {
		t.Errorf("no fail-closed log line:\n%s", out)
	}
	for _, leak := range []string{"arn:aws", "TOKEN-MARKER", "SECRETMARKER"} {
		if strings.Contains(out, leak) {
			t.Errorf("log leaks %q:\n%s", leak, out)
		}
	}
}

// A credential source that never answers is bounded by the dial's context
// (HeartbeatTimeout in the fence); the context error is reported, not the
// source's text.
func TestCredentialDialer_ABlockingSourceIsBoundedByTheDialContext(t *testing.T) {
	t.Parallel()
	creds := &fakeCreds{block: true}
	dial := credentialDialer(testConnConfig(t), creds, (&recordingConnect{}).connect)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond) // TEST value, not D10
	defer cancel()
	start := time.Now()
	_, err := dial(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if strings.Contains(err.Error(), "arn:aws") || strings.Contains(err.Error(), "TOKEN-MARKER") {
		t.Errorf("leak: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("not bounded by the context")
	}
}

func TestPgxCredentialDialer_RefusesMissingInputs(t *testing.T) {
	t.Parallel()
	if _, err := PgxCredentialDialer(nil, &fakeCreds{}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("nil config: %v", err)
	}
	if _, err := PgxCredentialDialer(testConnConfig(t), nil); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("nil credentials: %v", err)
	}
}

// ADR-0008 §14: no cloud SDK inside the fence package.
func TestFencePackage_ImportsNoAWSSDK(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			if strings.Contains(imp.Path.Value, "github.com/aws/") {
				t.Errorf("%s imports %s; AWS SDKs belong in an adapter package (ADR-0008 §14)", f, imp.Path.Value)
			}
		}
	}
}

func newTestLogger(b *strings.Builder, mu *sync.Mutex) *slog.Logger {
	return slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return b.Write(p)
	}), nil))
}

const secretText = "SECRETMARKER-reason"

type reasonErr struct{ reason, text string }

func (e reasonErr) Error() string             { return e.text }
func (e reasonErr) CredentialsReason() string { return e.reason }

// A vetted reason is kept; an unsafe "reason" (free text) is dropped; the
// source's error text never survives, and redact keeps the category.
func TestCredentialDialer_KeepsOnlyAVettedReason(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		reason string
		keep   bool
	}{
		"vetted":     {"sts_access_denied", true},
		"free text":  {"denied for arn:aws:iam::1:role/x", false},
		"upper case": {"STS_DENIED", false},
		"empty":      {"", false},
		"overlong":   {strings.Repeat("a", 41), false},
	} {
		src := &fakeCreds{err: reasonErr{reason: c.reason, text: "boom " + secretText}}
		dial := credentialDialer(&pgx.ConnConfig{}, src, func(context.Context, *pgx.ConnConfig) (Session, error) {
			t.Fatal("connected despite a credential failure")
			return nil, nil
		})
		_, err := dial(context.Background())
		if !errors.Is(err, ErrCredentialsUnavailable) || strings.Contains(err.Error(), secretText) {
			t.Errorf("%s: %v", name, err)
			continue
		}
		red := redact(fmt.Errorf("wrapped %s: %w", secretText, err)).Error()
		if got := strings.Contains(red, c.reason) && c.reason != ""; got != c.keep {
			t.Errorf("%s: redacted %q; reason kept = %v, want %v", name, red, got, c.keep)
		}
		if strings.Contains(red, secretText) || strings.Contains(red, "arn:aws") {
			t.Errorf("%s: redact leaks: %q", name, red)
		}
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "dial tcp 10.0.0.1:5432: i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestRedact_NamesNetworkFailuresWithoutDetails(t *testing.T) {
	t.Parallel()
	got := redact(fmt.Errorf("connect: %w", timeoutErr{}))
	if got != errNetwork || strings.Contains(got.Error(), "10.0.0.1") {
		t.Errorf("redact = %v", got)
	}
}
