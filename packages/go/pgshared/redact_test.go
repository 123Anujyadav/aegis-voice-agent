package pgshared

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Redact keeps connection detail (user, password, database, host, port, and
// the server's own message, which names the user) out of errors that reach a
// log, a probe body or a terminal. These tests produce REAL pgconn errors,
// without a database, and check what survives.

// redactSecrets appear in the DSNs below; none may survive Redact.
var redactSecrets = []string{"alice", "hunter2-pw", "identitydb", "127.0.0.1"}

func dsnFor(port int) string {
	return fmt.Sprintf("postgres://alice:hunter2-pw@127.0.0.1:%d/identitydb?sslmode=disable&connect_timeout=5", port)
}

// connectErr connects with pgconn and requires a *pgconn.ConnectError that
// carries the connection detail Redact must remove, so each test proves it
// started from a leaking error.
func connectErr(ctx context.Context, t *testing.T, port int) error {
	t.Helper()
	conn, err := pgconn.Connect(ctx, dsnFor(port))
	if err == nil {
		_ = conn.Close(ctx)
		t.Fatal("connected unexpectedly")
	}
	var ce *pgconn.ConnectError
	if !errors.As(err, &ce) {
		t.Fatalf("got %T (%v), want a *pgconn.ConnectError", err, err)
	}
	if !strings.Contains(err.Error(), "127.0.0.1") && !strings.Contains(err.Error(), "alice") {
		t.Fatalf("the unredacted error names no connection detail, so the test proves nothing: %v", err)
	}
	return err
}

func assertRedacted(t *testing.T, got error, wantSuffix string) {
	t.Helper()
	if !errors.Is(got, ErrConnect) {
		t.Fatalf("Redact = %v, want ErrConnect", got)
	}
	for _, s := range redactSecrets {
		if strings.Contains(got.Error(), s) {
			t.Errorf("the redacted error carries %q: %v", s, got)
		}
	}
	if want := ErrConnect.Error() + wantSuffix; got.Error() != want {
		t.Errorf("Redact = %q, want %q", got.Error(), want)
	}
}

// closedPort returns a local port with nothing listening on it.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// fakeServer accepts connections on a local port and hands each to serve.
func fakeServer(t *testing.T, serve func(net.Conn)) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestRedact_LeavesOtherErrorsUnchanged(t *testing.T) {
	t.Parallel()
	if Redact(nil) != nil {
		t.Error("Redact(nil) is not nil")
	}
	plain := errors.New("constraint violated")
	wrapped := fmt.Errorf("reserve: %w", plain)
	for _, err := range []error{plain, wrapped, ErrInvalidDSN, ErrReplaySchemaMissing, context.Canceled} {
		if got := Redact(err); got != err { //nolint:errorlint // identity is the point: the same error, not a copy
			t.Errorf("Redact(%v) = %v, want the same error", err, got)
		}
	}
}

// A refused connection names the user, host and port; redacted, it is the
// fixed ErrConnect.
func TestRedact_ARefusedConnectionNamesNothing(t *testing.T) {
	t.Parallel()
	err := connectErr(context.Background(), t, closedPort(t))
	assertRedacted(t, Redact(err), "")
}

// A server that never answers: the attempt times out, and only "timed out"
// survives.
func TestRedact_ATimedOutAttemptSaysOnlyThat(t *testing.T) {
	t.Parallel()
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	port := fakeServer(t, func(c net.Conn) {
		<-hold
		_ = c.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := connectErr(ctx, t, port)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want a deadline-exceeded connect error", err)
	}
	assertRedacted(t, Redact(err), ": timed out")
}

// A cancelled attempt says only "cancelled".
func TestRedact_ACancelledAttemptSaysOnlyThat(t *testing.T) {
	t.Parallel()
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	port := fakeServer(t, func(c net.Conn) {
		<-hold
		_ = c.Close()
	})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	err := connectErr(ctx, t, port)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want a cancelled connect error", err)
	}
	assertRedacted(t, Redact(err), ": cancelled")
}

// The server refuses the login with SQLSTATE 28P01 and a message naming the
// user. Redacted, only the SQLSTATE survives.
func TestRedact_AServerRefusalKeepsOnlyTheSQLSTATE(t *testing.T) {
	t.Parallel()
	port := fakeServer(t, func(c net.Conn) {
		defer c.Close()
		// The startup message: an int32 length that includes itself, then the body.
		var n uint32
		if binary.Read(c, binary.BigEndian, &n) != nil || n < 8 || n > 10_000 {
			return
		}
		if _, err := io.CopyN(io.Discard, c, int64(n-4)); err != nil {
			return
		}
		// An ErrorResponse: 'E', an int32 length, then code-tagged fields.
		var body []byte
		for _, f := range []struct {
			code  byte
			value string
		}{
			{'S', "FATAL"},
			{'V', "FATAL"},
			{'C', "28P01"},
			{'M', `password authentication failed for user "alice"`},
		} {
			body = append(body, f.code)
			body = append(body, f.value...)
			body = append(body, 0)
		}
		body = append(body, 0)
		msg := []byte{'E'}
		msg = binary.BigEndian.AppendUint32(msg, uint32(len(body)+4))
		_, _ = c.Write(append(msg, body...))
	})
	err := connectErr(context.Background(), t, port)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "28P01" || !strings.Contains(err.Error(), "alice") {
		t.Fatalf("got %v, want a 28P01 refusal naming the user", err)
	}
	assertRedacted(t, Redact(err), ": SQLSTATE 28P01")
}

// Redact survives further wrapping by its callers: the bootstrap code wraps
// it with a variable name, and the redacted text is all that remains.
func TestRedact_TheCallersWrappingCarriesNoDetail(t *testing.T) {
	t.Parallel()
	err := connectErr(context.Background(), t, closedPort(t))
	wrapped := fmt.Errorf("invalid configuration: CS_IDENTITY_DATABASE_DSN_FILE: %w", Redact(err))
	if !errors.Is(wrapped, ErrConnect) {
		t.Fatalf("wrapped = %v", wrapped)
	}
	for _, s := range redactSecrets {
		if strings.Contains(wrapped.Error(), s) {
			t.Errorf("the wrapped error carries %q: %v", s, wrapped)
		}
	}
}
