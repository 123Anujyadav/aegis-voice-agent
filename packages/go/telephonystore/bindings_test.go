package telephonystore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
	"github.com/callscreen/callscreen-platform/packages/go/telephony/callback"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Unit tests: the Go side of the adapter against a scripted querier. They
// prove argument passing, result interpretation and error mapping. They do NOT
// prove database atomicity — that is postgres_test.go's job, against a real
// server.

type recorded struct {
	sql  string
	args []any
}

type scriptedRow struct {
	val string
	err error
}

func (r scriptedRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*string)) = r.val
	return nil
}

type fakeQuerier struct {
	mu    sync.Mutex
	calls []recorded
	exec  func(ctx context.Context, sql string) (pgconn.CommandTag, error)
	row   func(ctx context.Context, sql string) pgx.Row
}

func (f *fakeQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.mu.Lock()
	f.calls = append(f.calls, recorded{sql, args})
	f.mu.Unlock()
	if f.exec == nil {
		return pgconn.NewCommandTag("INSERT 0 1"), nil
	}
	return f.exec(ctx, sql)
}

func (f *fakeQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	f.mu.Lock()
	f.calls = append(f.calls, recorded{sql, args})
	f.mu.Unlock()
	if f.row == nil {
		return scriptedRow{err: pgx.ErrNoRows}
	}
	return f.row(ctx, sql)
}

func (f *fakeQuerier) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeCalls map[telephony.CallID]telephony.ProviderID

func (f fakeCalls) ProviderOf(id telephony.CallID) (telephony.ProviderID, error) {
	p, ok := f[id]
	if !ok {
		return "", fmt.Errorf("%w: %s", telephony.ErrCallNotFound, id)
	}
	return p, nil
}

const (
	carrierA = telephony.ProviderID("carrier-a")
	carrierB = telephony.ProviderID("carrier-b")
	callA1   = telephony.CallID("call_a1")
	callA2   = telephony.CallID("call_a2")
)

var calls = fakeCalls{callA1: carrierA, callA2: carrierA}

func unit(t *testing.T, q *fakeQuerier) *Bindings {
	t.Helper()
	b, err := newBindings(q, calls, Config{OpTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func bindingA1() callback.Binding {
	return callback.Binding{Provider: carrierA, ProviderCallID: "pc-1", Call: callA1}
}

const leak = "password=hunter2 host=db.internal user=telephony"

// ---------------------------------------------------------------------------
// Bind
// ---------------------------------------------------------------------------

func TestBindCall_InsertsWithBindParameters(t *testing.T) {
	t.Parallel()
	q := &fakeQuerier{}
	if err := unit(t, q).BindCall(context.Background(), bindingA1()); err != nil {
		t.Fatalf("BindCall: %v", err)
	}
	if q.n() != 1 {
		t.Fatalf("%d statements, want 1 (the insert won)", q.n())
	}
	c := q.calls[0]
	if c.sql != sqlBind || !strings.Contains(c.sql, "ON CONFLICT DO NOTHING") {
		t.Errorf("statement = %q", c.sql)
	}
	if want := []any{"carrier-a", "pc-1", "call_a1"}; fmt.Sprint(c.args) != fmt.Sprint(want) {
		t.Errorf("args = %v, want %v", c.args, want)
	}
	if strings.Contains(c.sql, "pc-1") || strings.Contains(c.sql, "call_a1") {
		t.Error("a value was interpolated into the SQL")
	}
}

func TestBindCall_InterpretsAnExistingRow(t *testing.T) {
	t.Parallel()
	nothingInserted := func(context.Context, string) (pgconn.CommandTag, error) {
		return pgconn.NewCommandTag("INSERT 0 0"), nil
	}
	for _, tc := range []struct {
		name string
		row  scriptedRow
		want error
	}{
		{"same binding again", scriptedRow{val: "call_a1"}, nil},
		{"carrier call bound elsewhere", scriptedRow{val: "call_zz"}, callback.ErrBindingConflict},
		{"platform call bound elsewhere", scriptedRow{err: pgx.ErrNoRows}, callback.ErrBindingConflict},
		{"lookup fails", scriptedRow{err: errors.New(leak)}, ErrStoreUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeQuerier{exec: nothingInserted, row: func(context.Context, string) pgx.Row { return tc.row }}
			err := unit(t, q).BindCall(context.Background(), bindingA1())
			if !errors.Is(err, tc.want) || (tc.want == nil) != (err == nil) {
				t.Fatalf("BindCall = %v, want %v", err, tc.want)
			}
			if err != nil && strings.Contains(err.Error(), "hunter2") {
				t.Errorf("error leaks driver text: %v", err)
			}
			if q.n() != 2 || q.calls[1].sql != sqlLookup {
				t.Errorf("statements = %+v, want insert then lookup", q.calls)
			}
		})
	}
}

func TestBindCall_RefusesBeforeTouchingTheDatabase(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		b    callback.Binding
		want error
	}{
		{"invalid provider", callback.Binding{Provider: "Bad", ProviderCallID: "pc-1", Call: callA1}, callback.ErrInvalidBinding},
		{"empty carrier call ID", callback.Binding{Provider: carrierA, Call: callA1}, callback.ErrInvalidBinding},
		{"malformed carrier call ID", callback.Binding{Provider: carrierA, ProviderCallID: "pc 1", Call: callA1}, callback.ErrInvalidBinding},
		{"invalid platform call", callback.Binding{Provider: carrierA, ProviderCallID: "pc-1", Call: "x"}, callback.ErrInvalidBinding},
		{"unknown platform call", callback.Binding{Provider: carrierA, ProviderCallID: "pc-1", Call: "call_nope"}, telephony.ErrCallNotFound},
		{"provider does not carry the call", callback.Binding{Provider: carrierB, ProviderCallID: "pc-1", Call: callA1}, callback.ErrProviderMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeQuerier{}
			if err := unit(t, q).BindCall(context.Background(), tc.b); !errors.Is(err, tc.want) {
				t.Fatalf("BindCall = %v, want %v", err, tc.want)
			}
			if q.n() != 0 {
				t.Errorf("%d statements sent for a refused bind", q.n())
			}
		})
	}
}

func TestBindCall_MapsDatabaseFailuresSafely(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		err     error
		want    error
		wantMsg string
	}{
		{"check violation", &pgconn.PgError{Code: "23514", ConstraintName: "telephony_carrier_call_bindings_provider_format", Message: leak}, callback.ErrInvalidBinding, "provider_format"},
		{"server error", &pgconn.PgError{Code: "57P01", Message: leak, Detail: leak}, ErrStoreUnavailable, "57P01"},
		{"connection error", errors.New("failed to connect to " + leak), ErrStoreUnavailable, "details withheld"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeQuerier{exec: func(context.Context, string) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, tc.err }}
			err := unit(t, q).BindCall(context.Background(), bindingA1())
			if !errors.Is(err, tc.want) {
				t.Fatalf("BindCall = %v, want %v", err, tc.want)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q lacks %q", err, tc.wantMsg)
			}
			if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "db.internal") {
				t.Errorf("error leaks driver text: %v", err)
			}
			if errors.Is(err, callback.ErrBindingConflict) || errors.Is(err, callback.ErrUnknownCall) {
				t.Errorf("an outage was reported as a conflict or a missing binding: %v", err)
			}
		})
	}
}

func TestBindCall_IsBoundedByOpTimeout(t *testing.T) {
	t.Parallel()
	q := &fakeQuerier{exec: func(ctx context.Context, _ string) (pgconn.CommandTag, error) {
		<-ctx.Done()
		return pgconn.CommandTag{}, ctx.Err()
	}}
	b, err := newBindings(q, calls, Config{OpTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = b.BindCall(context.Background(), bindingA1())
	if !errors.Is(err, ErrStoreUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("BindCall = %v, want ErrStoreUnavailable wrapping DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("bind took %s; the timeout did not bound it", d)
	}
}

// ---------------------------------------------------------------------------
// Resolve and Remove
// ---------------------------------------------------------------------------

func TestResolve_DistinguishesMissingFromUnavailable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		row     scriptedRow
		want    error
		notWant error
		id      telephony.CallID
	}{
		{"bound", scriptedRow{val: "call_a1"}, nil, nil, callA1},
		{"unbound", scriptedRow{err: pgx.ErrNoRows}, callback.ErrUnknownCall, ErrStoreUnavailable, ""},
		{"database down", scriptedRow{err: errors.New(leak)}, ErrStoreUnavailable, callback.ErrUnknownCall, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeQuerier{row: func(context.Context, string) pgx.Row { return tc.row }}
			id, err := unit(t, q).Resolve(carrierA, "pc-1")
			if tc.want == nil {
				if err != nil || id != tc.id {
					t.Fatalf("Resolve = %s, %v; want %s", id, err, tc.id)
				}
				return
			}
			if !errors.Is(err, tc.want) || errors.Is(err, tc.notWant) {
				t.Fatalf("Resolve = %v; want %v and not %v", err, tc.want, tc.notWant)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Errorf("error leaks driver text: %v", err)
			}
		})
	}
}

func TestResolve_InvalidInputNeverQueries(t *testing.T) {
	t.Parallel()
	q := &fakeQuerier{}
	b := unit(t, q)
	for _, in := range []struct {
		p  telephony.ProviderID
		id string
	}{{"", "pc-1"}, {carrierA, ""}, {carrierA, "pc\n1"}, {carrierA, strings.Repeat("x", 129)}} {
		if _, err := b.Resolve(in.p, in.id); !errors.Is(err, callback.ErrUnknownCall) {
			t.Errorf("Resolve(%q, %q) = %v, want ErrUnknownCall", in.p, in.id, err)
		}
	}
	if q.n() != 0 {
		t.Errorf("%d queries for invalid input", q.n())
	}
}

func TestRemove_ReportsWhetherABindingExisted(t *testing.T) {
	t.Parallel()
	for tag, want := range map[string]bool{"DELETE 1": true, "DELETE 0": false} {
		q := &fakeQuerier{exec: func(context.Context, string) (pgconn.CommandTag, error) { return pgconn.NewCommandTag(tag), nil }}
		got, err := unit(t, q).Remove(context.Background(), carrierA, "pc-1")
		if err != nil || got != want {
			t.Errorf("%s: Remove = %v, %v; want %v", tag, got, err, want)
		}
		if q.calls[0].sql != sqlRemove {
			t.Errorf("statement = %q", q.calls[0].sql)
		}
	}
	q := &fakeQuerier{exec: func(context.Context, string) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, errors.New(leak) }}
	if _, err := unit(t, q).Remove(context.Background(), carrierA, "pc-1"); !errors.Is(err, ErrStoreUnavailable) {
		t.Errorf("Remove on failure = %v, want ErrStoreUnavailable", err)
	}
}

func TestNewBindings_RefusesIncompleteConfiguration(t *testing.T) {
	t.Parallel()
	if _, err := NewBindings(nil, calls, Config{OpTimeout: time.Second}); err == nil {
		t.Error("nil pool accepted")
	}
	if _, err := newBindings(&fakeQuerier{}, nil, Config{OpTimeout: time.Second}); err == nil {
		t.Error("nil CallProviders accepted")
	}
	if _, err := newBindings(&fakeQuerier{}, calls, Config{}); err == nil {
		t.Error("zero OpTimeout accepted")
	}
}

// ---------------------------------------------------------------------------
// Migrations
// ---------------------------------------------------------------------------

func TestMigrations_ChainIsContiguousAndEnforcesTheContract(t *testing.T) {
	t.Parallel()
	chain, err := Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	if len(chain) != 1 || chain[0].Version != 1 {
		t.Fatalf("chain = %+v, want exactly version 1", chain)
	}
	sql := chain[0].SQL
	for _, must := range []string{
		"PRIMARY KEY (provider, provider_call_id)",
		"UNIQUE (call_id)",
		"telephony_carrier_call_bindings_provider_format",
		"telephony_carrier_call_bindings_provider_call_id_format",
		"telephony_carrier_call_bindings_call_id_format",
	} {
		if !strings.Contains(sql, must) {
			t.Errorf("migration lacks %q", must)
		}
	}
	for _, mustNot := range []string{"DROP ", "TRUNCATE", "DELETE "} {
		if strings.Contains(strings.ToUpper(sql), mustNot) {
			t.Errorf("migration contains %q", mustNot)
		}
	}
}
