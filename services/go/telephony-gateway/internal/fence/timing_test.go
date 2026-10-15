package fence

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// P1-41 §8 — per-phase timing: fields present on success and failure paths,
// a fixed field set, durations only, nothing secret, and no change to
// ownership or readiness. Offline (fakes, loopback TLS); the real-PG variant
// is in timing_postgres_test.go.

// jsonLog captures JSON log records at Debug level.
type jsonLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *jsonLog) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(writerFunc(func(p []byte) (int, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.buf.Write(p)
	}), &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (l *jsonLog) raw() string { l.mu.Lock(); defer l.mu.Unlock(); return l.buf.String() }

// records returns every record whose message contains msg.
func (l *jsonLog) records(t *testing.T, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(l.raw()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %v", err)
		}
		if s, _ := m["msg"].(string); strings.Contains(s, msg) {
			out = append(out, m)
		}
	}
	return out
}

// phases returns a record's "phase" group, checking that every key is a
// known phase and every value a non-negative duration.
func phases(t *testing.T, rec map[string]any) map[string]time.Duration {
	t.Helper()
	known := map[string]bool{}
	for _, p := range phaseOrder {
		known[p] = true
	}
	g, ok := rec["phase"].(map[string]any)
	if !ok {
		t.Fatalf("record %q has no phase group", rec["msg"])
	}
	out := map[string]time.Duration{}
	for k, v := range g {
		if k == FieldSTSOutcome {
			if o, _ := v.(string); !STSCredentialsOutcome(o).valid() {
				t.Errorf("%s = %v, not in the fixed set", FieldSTSOutcome, v)
			}
			continue
		}
		n, isNum := v.(float64) // slog's JSON handler writes a Duration as integer nanoseconds
		if !known[k] || !isNum || n < 0 {
			t.Errorf("unexpected phase field %q = %v", k, v)
			continue
		}
		out[k] = time.Duration(n)
	}
	return out
}

func wantPhases(t *testing.T, what string, got map[string]time.Duration, present, absent []string) {
	t.Helper()
	for _, p := range present {
		if _, ok := got[p]; !ok {
			t.Errorf("%s: phase %q missing (have %v)", what, p, got)
		}
	}
	for _, p := range absent {
		if _, ok := got[p]; ok {
			t.Errorf("%s: phase %q present but was not reached", what, p)
		}
	}
}

func noLeaks(t *testing.T, logs string, forbidden ...string) {
	t.Helper()
	for _, s := range append(forbidden, secretText, "SECRETMARKER", "TOKEN-MARKER", "arn:aws") {
		if s != "" && strings.Contains(logs, s) {
			t.Errorf("logs contain %q", s)
		}
	}
}

// timedFence is a fence on the controllable clock whose dial goes through the
// credential dialer (so the credentials phase is real) into sess.
func timedFence(t *testing.T, creds Credentials, sess Session) (*Fence, *clock, *jsonLog) {
	t.Helper()
	l := &jsonLog{}
	dial := credentialDialer(&pgx.ConnConfig{}, creds, func(context.Context, *pgx.ConnConfig) (Session, error) {
		return sess, nil
	})
	f, err := New(cfg, dial, l.logger())
	if err != nil {
		t.Fatal(err)
	}
	c := newClock()
	f.now, f.sleep = c.now, c.sleep
	return f, c, l
}

// Success path: acquisition, heartbeats and loss each carry their phases, and
// ownership follows exactly the same clock rules as before (grace, then
// owned, then lost on a failed confirmation).
func TestTiming_AcquisitionHeartbeatsAndLossCarryPhases(t *testing.T) {
	t.Parallel()
	var fail atomic.Bool
	sess := &fakeSession{holds: func(context.Context) (bool, error) {
		if fail.Load() {
			return false, errors.New("conn 10.0.0.9:5432 reset " + secretText)
		}
		return true, nil
	}}
	f, c, l := timedFence(t, &fakeCreds{}, sess)
	go func() { _ = f.Run(context.Background()) }()
	t.Cleanup(func() { _ = f.Shutdown(context.Background()) })

	if f.Owned() {
		t.Fatal("owned before the grace period")
	}
	for i := 0; i < 3; i++ {
		c.step(t)
	}
	eventually(t, "ownership after the grace period", f.Owned)

	acq := l.records(t, "lock acquired")
	if len(acq) != 1 {
		t.Fatalf("acquisition records = %d", len(acq))
	}
	wantPhases(t, "acquired", phases(t, acq[0]), []string{PhaseCredentials, PhasePrimary, PhaseLock}, []string{PhaseHeartbeat})
	eventually(t, "heartbeat records", func() bool { return len(l.records(t, "ownership confirmed")) >= 2 })
	for _, r := range l.records(t, "ownership confirmed") {
		wantPhases(t, "confirmed", phases(t, r), []string{PhaseHeartbeat}, []string{PhaseLock, PhaseCredentials})
	}

	fail.Store(true)
	c.step(t)
	eventually(t, "loss", func() bool { return !f.Owned() && len(l.records(t, "ownership LOST")) == 1 })
	lost := l.records(t, "ownership LOST")[0]
	wantPhases(t, "lost", phases(t, lost), []string{PhaseHeartbeat}, nil)
	noLeaks(t, l.raw(), "10.0.0.9", "reset")
}

// Failure paths: each carries the phases reached, and only a sanitized error.
func TestTiming_FailurePathsCarryPhasesAndSanitizedErrors(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		creds   Credentials
		sess    *fakeSession
		msg     string
		present []string
		absent  []string
		errText string
	}{
		"credentials fail": {
			creds: &fakeCreds{err: reasonErr{reason: "sts_access_denied", text: "AccessDenied arn:aws:iam::1:role/x " + secretText}},
			sess:  &fakeSession{}, msg: "cannot open a database session",
			present: []string{PhaseCredentials}, absent: []string{PhasePrimary, PhaseLock},
			errText: "sts_access_denied",
		},
		"not a primary": {
			creds: &fakeCreds{}, sess: &fakeSession{primary: func(context.Context) (bool, error) { return false, nil }},
			msg: "not a writable primary", present: []string{PhaseCredentials, PhasePrimary}, absent: []string{PhaseLock},
		},
		"lock query fails": {
			creds: &fakeCreds{}, sess: &fakeSession{tryLock: func(context.Context) (bool, error) {
				return false, errors.New("write tcp 10.0.0.9:5432: broken pipe " + secretText)
			}},
			msg: "lock attempt failed", present: []string{PhaseCredentials, PhasePrimary, PhaseLock},
			errText: "details withheld",
		},
		"lock held elsewhere (debug)": {
			creds: &fakeCreds{}, sess: &fakeSession{tryLock: func(context.Context) (bool, error) { return false, nil }},
			msg: "lock held elsewhere", present: []string{PhaseCredentials, PhasePrimary, PhaseLock},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, _, l := timedFence(t, c.creds, c.sess)
			go func() { _ = f.Run(context.Background()) }()
			t.Cleanup(func() { _ = f.Shutdown(context.Background()) })
			eventually(t, "the record", func() bool { return len(l.records(t, c.msg)) > 0 })
			rec := l.records(t, c.msg)[0]
			wantPhases(t, name, phases(t, rec), c.present, c.absent)
			if e, _ := rec["error"].(string); c.errText != "" && !strings.Contains(e, c.errText) {
				t.Errorf("error = %q, want it to contain %q", e, c.errText)
			}
			if f.Owned() {
				t.Error("owner on a failure path")
			}
			noLeaks(t, l.raw(), "10.0.0.9", "broken pipe", "AccessDenied")
		})
	}
}

// fakeAuthPostgres answers SSLRequest, completes TLS with cert, asks for a
// cleartext password and refuses the login (SQLSTATE 28P01).
func fakeAuthPostgres(t *testing.T, cert tls.Certificate) (port int, handshakes *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	handshakes = &atomic.Int64{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				if _, err := io.ReadFull(c, make([]byte, 8)); err != nil {
					return
				}
				if _, err := c.Write([]byte("S")); err != nil {
					return
				}
				srv := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
				if srv.Handshake() != nil {
					return
				}
				handshakes.Add(1)
				r := bufio.NewReader(srv)
				var n uint32
				if binary.Read(r, binary.BigEndian, &n) != nil || n < 8 || n > 10000 {
					return
				}
				if _, err := io.ReadFull(r, make([]byte, n-4)); err != nil {
					return
				}
				_, _ = srv.Write([]byte{'R', 0, 0, 0, 8, 0, 0, 0, 3})
				if typ, err := r.ReadByte(); err != nil || typ != 'p' {
					return
				}
				if binary.Read(r, binary.BigEndian, &n) != nil || n < 5 || n > 10000 {
					return
				}
				if _, err := io.ReadFull(r, make([]byte, n-4)); err != nil {
					return
				}
				msg := "SFATAL\x00VFATAL\x00C28P01\x00Mpassword authentication failed\x00\x00"
				e := []byte{'E', 0, 0, 0, 0}
				binary.BigEndian.PutUint32(e[1:], uint32(4+len(msg)))
				_, _ = srv.Write(append(e, msg...))
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, handshakes
}

// On the wire: dns, tcp, tls and auth are measured separately; a refused
// login carries its SQLSTATE only; a bad certificate stops at tls (no auth)
// and is reported as the TLS category.
func TestTiming_WirePhasesOverVerifyFullTLS(t *testing.T) {
	t.Parallel()
	trusted := newTestCA(t, "trusted-ca")
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, trusted.pem, 0o600); err != nil {
		t.Fatal(err)
	}
	dsn := func(host string, port int) string {
		return "postgres://fence_iam@" + host + ":" + strconv.Itoa(port) +
			"/telephony?sslmode=verify-full&sslrootcert=" + url.QueryEscape(caFile) + "&target_session_attrs=read-write"
	}
	dialOnce := func(t *testing.T, d string) (map[string]time.Duration, error) {
		t.Helper()
		cfg, err := ParseDSN(d)
		if err != nil {
			t.Fatal(err)
		}
		dial, err := PgxCredentialDialer(cfg, &fakeCreds{})
		if err != nil {
			t.Fatal(err)
		}
		tm := NewTimings()
		ctx, cancel := context.WithTimeout(WithTimings(context.Background(), tm), 5*time.Second) // TEST bound, not D10
		defer cancel()
		_, err = dial(ctx)
		return tm.d, err
	}

	good, handshakes := fakeAuthPostgres(t, trusted.leaf(t, "localhost", net.ParseIP("127.0.0.1")))
	got, err := dialOnce(t, dsn("localhost", good))
	if err == nil {
		t.Fatal("login succeeded against a server that refuses it")
	}
	if r := redact(err).Error(); r != "postgres error 28P01" {
		t.Errorf("redacted = %q, want the SQLSTATE only", r)
	}
	wantPhases(t, "refused login", got, []string{PhaseCredentials, PhaseDNS, PhaseTCP, PhaseTLS, PhaseAuth, PhaseConnect}, []string{PhaseSessionCheck})
	if handshakes.Load() < 1 {
		t.Error("no TLS handshake completed with the trusted certificate")
	}

	bad, badHandshakes := fakeAuthPostgres(t, newTestCA(t, "untrusted-ca").leaf(t, "localhost", net.ParseIP("127.0.0.1")))
	got, err = dialOnce(t, dsn("127.0.0.1", bad))
	if err == nil || !sameError(redact(err), errTLSVerification) {
		t.Fatalf("untrusted certificate: err %v (redacted %v)", err, redact(err))
	}
	wantPhases(t, "bad certificate", got, []string{PhaseCredentials, PhaseDNS, PhaseTCP, PhaseTLS, PhaseConnect}, []string{PhaseAuth, PhaseSessionCheck})
	time.Sleep(50 * time.Millisecond)
	if badHandshakes.Load() != 0 {
		t.Error("the client completed a handshake with an untrusted certificate")
	}
}

// The log attribute is the fixed "phase" group: known names, durations only,
// in a fixed order, and nothing for phases not reached.
func TestTiming_AttrIsAFixedDurationOnlyGroup(t *testing.T) {
	t.Parallel()
	tm := NewTimings()
	tm.set(PhaseLock, 3*time.Millisecond)
	tm.set(PhaseCredentials, time.Millisecond)
	var b bytes.Buffer
	slog.New(slog.NewTextHandler(&b, nil)).Info("x", tm.attr())
	if got := b.String(); !strings.Contains(got, "phase.login_prepare=1ms phase.lock=3ms") || strings.Count(got, "phase.") != 2 {
		t.Errorf("attr rendered %q", got)
	}
	var nilT *Timings
	nilT.set(PhaseTCP, time.Second) // a context without a collector records nothing
	if timingsFrom(context.Background()) != nil {
		t.Error("collector found in a bare context")
	}
	if len(phaseOrder) != 12 {
		t.Errorf("phase set changed: %v", phaseOrder)
	}
}

// Regression (P1-41 §8): a running fence — which always installs a timing
// collector — dialling a server with an untrusted certificate must fail
// closed, not crash (pgconn closes the conn the hook returns), and log the
// TLS category with the phases reached.
func TestTiming_AnUntrustedCertificateFailsClosedInARunningFence(t *testing.T) {
	t.Parallel()
	trusted := newTestCA(t, "trusted-ca")
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, trusted.pem, 0o600); err != nil {
		t.Fatal(err)
	}
	port, handshakes := fakeAuthPostgres(t, newTestCA(t, "untrusted-ca").leaf(t, "x", net.ParseIP("127.0.0.1")))
	pc, err := ParseDSN("postgres://fence_iam@127.0.0.1:" + strconv.Itoa(port) +
		"/telephony?sslmode=verify-full&sslrootcert=" + url.QueryEscape(caFile))
	if err != nil {
		t.Fatal(err)
	}
	dial, _ := PgxCredentialDialer(pc, &fakeCreds{})
	l := &jsonLog{}
	f, err := New(cfg, dial, l.logger())
	if err != nil {
		t.Fatal(err)
	}
	c := newClock()
	f.now, f.sleep = c.now, c.sleep
	go func() { _ = f.Run(context.Background()) }()
	t.Cleanup(func() { _ = f.Shutdown(context.Background()) })

	eventually(t, "the dial failure record", func() bool { return len(l.records(t, "cannot open a database session")) > 0 })
	rec := l.records(t, "cannot open a database session")[0]
	if rec["error"] != errTLSVerification.Error() {
		t.Errorf("error = %v, want the TLS category", rec["error"])
	}
	wantPhases(t, "untrusted certificate", phases(t, rec), []string{PhaseCredentials, PhaseTCP, PhaseTLS, PhaseConnect}, []string{PhaseAuth, PhasePrimary, PhaseLock})
	if f.Owned() || handshakes.Load() != 0 {
		t.Errorf("owned=%v handshakes=%d", f.Owned(), handshakes.Load())
	}
	noLeaks(t, l.raw(), "127.0.0.1", ":"+strconv.Itoa(port), "fence_iam", "x509")
}

// Only the two credential sub-phases and the fixed outcomes can be recorded
// by a source; anything else is ignored.
func TestTiming_SourceRecordingIsRestrictedToTheFixedSet(t *testing.T) {
	t.Parallel()
	tm := NewTimings()
	ctx := WithTimings(context.Background(), tm)
	RecordCredentialPhase(ctx, PhaseCredentialsSTS, time.Millisecond)
	RecordCredentialPhase(ctx, PhaseCredentialsSign, time.Millisecond)
	RecordCredentialPhase(ctx, PhaseTCP, time.Hour)                      // not a source phase
	RecordCredentialPhase(ctx, "arn:aws:iam::1:role/x", time.Hour)       // free text
	RecordSTSCredentials(ctx, STSCredentialsOutcome("arn:aws:iam::1:x")) // not in the set
	if d := tm.Durations(); len(d) != 2 || tm.STSCredentials() != "" {
		t.Errorf("recorded %v, outcome %q", d, tm.STSCredentials())
	}
	RecordSTSCredentials(ctx, STSCredentialsReused)
	var b bytes.Buffer
	slog.New(slog.NewTextHandler(&b, nil)).Info("x", tm.attr())
	if got := b.String(); !strings.Contains(got, "phase.login_prepare_sts=1ms phase.login_prepare_sign=1ms phase.sts_refresh=reused") || strings.Contains(got, "arn:aws") {
		t.Errorf("attr rendered %q", got)
	}
	// No collector: recording is a no-op, not a panic.
	RecordCredentialPhase(context.Background(), PhaseCredentialsSTS, time.Second)
	RecordSTSCredentials(context.Background(), STSCredentialsRefreshed)
}
