package fence

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Per-phase timing for one acquisition attempt and for each heartbeat
// (Phase 19 P1-41 §8; the evidence P1-41 §5 E1–E5 needs to choose D10 values).
//
// It only MEASURES. It adds no timeout, threshold, metric or default, and it
// changes neither ownership nor readiness: durations come from time.Now's
// monotonic reading, never from the fence's ownership clock (Fence.now).
//
// Log fields are a fixed set of names holding durations only — no hosts,
// users, addresses, values or error text. Phases that were not reached are
// omitted.
//
// What can be measured separately (pgx v5.10.0 hooks):
//
//	login_prepare       Credentials.Password, end to end (the dial's view):
//	                    preparing the login secret for this connection
//	login_prepare_sts   reported by the source: obtaining the temporary AWS
//	                    credentials (cache lookup, plus the STS exchange when
//	                    this call refreshed) — a COMPONENT of login_prepare
//	login_prepare_sign  reported by the source: signing the RDS token with
//	                    those credentials — a COMPONENT of login_prepare
//	dns            pgconn LookupFunc (also called for an IP-literal host)
//	tcp            pgconn DialFunc
//	tls            an explicit TLS handshake in AfterNetConnect (see below)
//	auth           startup message → authentication complete (ValidateConnect
//	               start, or the end of the connect when there is none)
//	session_check  ValidateConnect (target_session_attrs=read-write)
//	connect        the whole pgx connect (dns … session_check)
//	primary        the writable-primary query
//	lock           pg_try_advisory_lock
//	heartbeat      one ownership confirmation (pg_locks)
//
// TLS: pgconn returns a tls.Conn whose handshake would otherwise run lazily
// inside the first write (the startup message), folding it into "auth". The
// handshake is therefore completed explicitly in AfterNetConnect, with the
// same tls.Config (same verification) and bounded by the same dial context.
// Multi-host fallbacks (password mode only; refused in IAM mode) reuse the
// hooks: each phase then reports the LAST attempt.
//
// Durations NEST; do not add them up. connect contains dns, tcp, tls, auth and
// session_check; login_prepare contains login_prepare_sts and
// login_prepare_sign (plus a little hand-off overhead). Only phases at the same level are
// sequential and disjoint.
//
// sts_refresh (a string, inside the same group) says whether THIS call
// refreshed the STS credentials or reused cached ones — see
// STSCredentialsOutcome.
//
// Field names must survive the gateway's real logger (platform.NewLogger):
// its RedactingHandler replaces any key CONTAINING a deny-listed substring
// ("credential", "token", "secret", "password", "pin", "otp", …) with
// "[REDACTED]" and pseudonymises keys containing "ip", "contact", …, at any
// group depth. The names below avoid all of them (P1-43, F-8; asserted by
// TestGatewayLogger_TimingFieldsSurviveRedaction). The Go identifiers keep
// their earlier names; only the logged names changed.

// Phase names (log field names inside the "phase" group).
const (
	PhaseCredentials = "login_prepare"
	// PhaseCredentialsSTS and PhaseCredentialsSign are reported by a
	// Credentials source (RecordCredentialPhase) and nest inside
	// PhaseCredentials.
	PhaseCredentialsSTS  = "login_prepare_sts" //nolint:gosec // G101: a log field name, not a credential
	PhaseCredentialsSign = "login_prepare_sign"
	PhaseDNS             = "dns"
	PhaseTCP             = "tcp"
	PhaseTLS             = "tls"
	PhaseAuth            = "auth"
	PhaseSessionCheck    = "session_check"
	PhaseConnect         = "connect"
	PhasePrimary         = "primary"
	PhaseLock            = "lock"
	PhaseHeartbeat       = "heartbeat"
)

// FieldSTSOutcome is the log field (inside the "phase" group) holding the
// STSCredentialsOutcome string.
const FieldSTSOutcome = "sts_refresh"

// phaseOrder fixes the field order (and the complete set of names).
var phaseOrder = []string{
	PhaseCredentials, PhaseCredentialsSTS, PhaseCredentialsSign, PhaseDNS, PhaseTCP, PhaseTLS, PhaseAuth,
	PhaseSessionCheck, PhaseConnect, PhasePrimary, PhaseLock, PhaseHeartbeat,
}

// STSCredentialsOutcome says how ONE credential preparation obtained its
// temporary AWS credentials. A finite set; anything else is never logged.
type STSCredentialsOutcome string

const (
	// STSCredentialsReused means valid credentials were returned and this
	// call did not run the STS exchange itself (cached; or it joined an exchange
	// another caller had started — the fence has one dialer, so in practice
	// cached).
	STSCredentialsReused STSCredentialsOutcome = "reused"
	// STSCredentialsRefreshed means this call ran the STS exchange and it
	// returned credentials that passed validation. Recorded only after that.
	STSCredentialsRefreshed STSCredentialsOutcome = "refreshed"
	// STSCredentialsRefreshFailed means this call ran the STS exchange and it
	// failed (error, panic or unusable credentials).
	STSCredentialsRefreshFailed STSCredentialsOutcome = "refresh_failed"
	// STSCredentialsRefreshIncomplete means this call started the STS
	// exchange but stopped waiting (its context ended) before it finished.
	STSCredentialsRefreshIncomplete STSCredentialsOutcome = "refresh_incomplete"
	// STSCredentialsUnavailable means no credentials, and this call did not
	// run the exchange (for example it was cancelled while another refresh
	// ran).
	STSCredentialsUnavailable STSCredentialsOutcome = "unavailable"
)

func (o STSCredentialsOutcome) valid() bool {
	switch o {
	case STSCredentialsReused, STSCredentialsRefreshed, STSCredentialsRefreshFailed,
		STSCredentialsRefreshIncomplete, STSCredentialsUnavailable:
		return true
	}
	return false
}

// Timings collects phase durations for one attempt. Safe for concurrent use.
// The fence creates one per acquisition attempt and per heartbeat.
type Timings struct {
	mu  sync.Mutex
	d   map[string]time.Duration
	sts STSCredentialsOutcome
}

// RecordCredentialPhase lets a Credentials source report a sub-phase of its
// own work into the dial's collector. Only PhaseCredentialsSTS and
// PhaseCredentialsSign are accepted (anything else is ignored); a context
// without a collector makes it a no-op.
func RecordCredentialPhase(ctx context.Context, phase string, d time.Duration) {
	if phase == PhaseCredentialsSTS || phase == PhaseCredentialsSign {
		timingsFrom(ctx).set(phase, d)
	}
}

// RecordSTSCredentials records how this call obtained its STS credentials.
// Values outside STSCredentialsOutcome's set are ignored.
func RecordSTSCredentials(ctx context.Context, o STSCredentialsOutcome) {
	t := timingsFrom(ctx)
	if t == nil || !o.valid() {
		return
	}
	t.mu.Lock()
	t.sts = o
	t.mu.Unlock()
}

// Durations returns a copy of the recorded durations by phase name.
func (t *Timings) Durations() map[string]time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]time.Duration, len(t.d))
	for k, v := range t.d {
		out[k] = v
	}
	return out
}

// STSCredentials returns the recorded outcome, or "" if none was recorded.
func (t *Timings) STSCredentials() STSCredentialsOutcome {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sts
}

// NewTimings returns an empty collector for one acquisition attempt or one
// heartbeat.
func NewTimings() *Timings { return &Timings{d: make(map[string]time.Duration, len(phaseOrder))} }

func (t *Timings) set(phase string, d time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.d[phase] = d
	t.mu.Unlock()
}

func (t *Timings) has(phase string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.d[phase]
	return ok
}

// time runs fn and records its duration under phase.
func (t *Timings) time(phase string, fn func()) {
	start := time.Now()
	fn()
	t.set(phase, time.Since(start))
}

// attr is the "phase" log group: one Duration per phase reached, plus
// sts_refresh when a source recorded it.
func (t *Timings) attr() slog.Attr {
	t.mu.Lock()
	defer t.mu.Unlock()
	attrs := make([]any, 0, len(t.d)+1)
	for _, p := range phaseOrder {
		if d, ok := t.d[p]; ok {
			attrs = append(attrs, slog.Duration(p, d))
		}
	}
	if t.sts != "" {
		attrs = append(attrs, slog.String(FieldSTSOutcome, string(t.sts)))
	}
	return slog.Group("phase", attrs...)
}

type timingsKey struct{}

// WithTimings returns ctx carrying t, so the dial's hooks and a Credentials
// source record into it.
func WithTimings(ctx context.Context, t *Timings) context.Context {
	return context.WithValue(ctx, timingsKey{}, t)
}

// timingsFrom returns the attempt's collector, or nil (recording is a no-op)
// for a context that carries none.
func timingsFrom(ctx context.Context) *Timings {
	t, ok := ctx.Value(timingsKey{}).(*Timings)
	if !ok {
		return nil
	}
	return t
}

// instrumentedConnect connects with pgx, recording the dns, tcp, tls, auth,
// session_check and connect phases into ctx's collector. The config c must be
// a per-dial copy: its hooks are replaced by timing wrappers around the
// originals.
func instrumentedConnect(ctx context.Context, c *pgx.ConnConfig) (*pgx.Conn, error) {
	t := timingsFrom(ctx)
	if t == nil {
		return pgx.ConnectConfig(ctx, c)
	}
	var mu sync.Mutex
	var authStart time.Time
	markAuthStart := func() { mu.Lock(); authStart = time.Now(); mu.Unlock() }
	authSince := func() (time.Duration, bool) {
		mu.Lock()
		defer mu.Unlock()
		if authStart.IsZero() {
			return 0, false
		}
		return time.Since(authStart), true
	}

	if lookup := c.LookupFunc; lookup != nil {
		c.LookupFunc = func(ctx context.Context, host string) (addrs []string, err error) {
			t.time(PhaseDNS, func() { addrs, err = lookup(ctx, host) })
			return addrs, err
		}
	}
	if dial := c.DialFunc; dial != nil {
		c.DialFunc = func(ctx context.Context, network, addr string) (conn net.Conn, err error) {
			t.time(PhaseTCP, func() { conn, err = dial(ctx, network, addr) })
			return conn, err
		}
	}
	after := c.AfterNetConnect
	// On error the returned conn must still be closable: pgconn (v5.10.0,
	// pgconn.go:366-368) stores it BEFORE calling Close, so returning nil
	// would panic on, for example, an untrusted server certificate.
	c.AfterNetConnect = func(ctx context.Context, cfg *pgconn.Config, conn net.Conn) (net.Conn, error) {
		if tc, ok := conn.(*tls.Conn); ok {
			var err error
			t.time(PhaseTLS, func() { err = tc.HandshakeContext(ctx) })
			if err != nil {
				return conn, err
			}
		}
		if after != nil {
			next, err := after(ctx, cfg, conn)
			if err != nil {
				if next == nil {
					next = conn
				}
				return next, err
			}
			conn = next
		}
		markAuthStart()
		return conn, nil
	}
	if validate := c.ValidateConnect; validate != nil {
		c.ValidateConnect = func(ctx context.Context, pc *pgconn.PgConn) (err error) {
			if d, ok := authSince(); ok {
				t.set(PhaseAuth, d)
			}
			t.time(PhaseSessionCheck, func() { err = validate(ctx, pc) })
			return err
		}
	}

	var conn *pgx.Conn
	var err error
	t.time(PhaseConnect, func() { conn, err = pgx.ConnectConfig(ctx, c) })
	if !t.has(PhaseAuth) { // no ValidateConnect, or authentication failed
		if d, ok := authSince(); ok {
			t.set(PhaseAuth, d)
		}
	}
	return conn, err
}
