package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/middleware"
	"github.com/callscreen/callscreen-platform/packages/go/pgshared"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// Offline: configuration, DSN file, and Open's fail-closed paths
// ---------------------------------------------------------------------------

func TestConfig_DSNFileIsRequiredAndDistinct(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	c := validConfig(t, pub)
	c.DatabaseDSNFile = ""
	wantConfigErr(t, c.Validate(), "CS_EDGE_API_DATABASE_DSN_FILE")
	c.DatabaseDSNFile = " " + validConfig(t, pub).DatabaseDSNFile
	wantConfigErr(t, c.Validate(), "CS_EDGE_API_DATABASE_DSN_FILE")
	c.DatabaseDSNFile = c.PublicKeyFile
	wantConfigErr(t, c.Validate(), "same file")
}

// recordPools replaces newPool for one test, recording every pool created.
func recordPools(t *testing.T) *[]*pgxpool.Pool {
	t.Helper()
	var made []*pgxpool.Pool
	orig := newPool
	newPool = func(dsn string, policy pgshared.TLSPolicy) (*pgxpool.Pool, error) {
		p, err := orig(dsn, policy)
		if p != nil {
			made = append(made, p)
		}
		return p, err
	}
	t.Cleanup(func() { newPool = orig })
	return &made
}

// closed reports whether p has been closed (Acquire refuses at once).
func closed(p *pgxpool.Pool) bool {
	c, err := p.Acquire(context.Background())
	if err == nil {
		c.Release()
		return false
	}
	return strings.Contains(err.Error(), "closed pool")
}

const secretish = "Sup3rS3cretPw"

func TestOpen_DSNFileFailuresOpenNothingAndEchoNothing(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	for name, file := range map[string]func(t *testing.T) string{
		"missing":   func(t *testing.T) string { return filepath.Join(dir, "absent-"+secretish) },
		"directory": func(t *testing.T) string { return t.TempDir() },
		"empty":     func(t *testing.T) string { return writeFile(t, "dsn", []byte(" \n\t")) },
		// Keyword form: the driver itself would accept this across lines.
		"two lines": func(t *testing.T) string {
			return writeFile(t, "dsn", []byte("host=127.0.0.1 user=u password="+secretish+"\ndbname=d"))
		},
		"unparseable": func(t *testing.T) string {
			return writeFile(t, "dsn", []byte("postgres://u:"+secretish+"@h:notaport/d"))
		},
		"too large": func(t *testing.T) string {
			return writeFile(t, "dsn", []byte("postgres://u:"+secretish+"@h/d?application_name="+strings.Repeat("a", MaxDSNFileBytes)))
		},
	} {
		t.Run(name, func(t *testing.T) {
			made := recordPools(t)
			c := validConfig(t, pub)
			c.DatabaseDSNFile = file(t)
			rt, err := Open(context.Background(), c, nil, nil)
			if rt != nil || !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "CS_EDGE_API_DATABASE_DSN_FILE") {
				t.Fatalf("(%v, %v)", rt, err)
			}
			if strings.Contains(err.Error(), secretish) || strings.Contains(err.Error(), dir) {
				t.Errorf("the error echoes the DSN or path: %v", err)
			}
			if len(*made) != 0 {
				t.Error("a pool was created for an invalid DSN file")
			}
		})
	}
}

// A local mistake — here, an unreadable key — is reported before any
// connection is attempted.
func TestOpen_LocalFailuresComeBeforeTheDatabase(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	made := recordPools(t)
	c := validConfig(t, pub)
	c.PublicKeyFile = filepath.Join(t.TempDir(), "absent.pem")
	if rt, err := Open(context.Background(), c, nil, nil); rt != nil || !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("(%v, %v)", rt, err)
	}
	c = validConfig(t, pub)
	c.PublicOrigin = "http://edge.example.test"
	if rt, err := Open(context.Background(), c, nil, nil); rt != nil || !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("(%v, %v)", rt, err)
	}
	if len(*made) != 0 {
		t.Error("a pool was created despite invalid local configuration")
	}
}

// An unreachable database is a startup failure: no runtime, the pool closed,
// no connection detail in the error.
func TestOpen_UnreachableDatabaseFailsClosed(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	made := recordPools(t)
	c := validConfig(t, pub)
	// Port 1 on loopback: refused at once, nothing outside the host is dialled.
	c.DatabaseDSNFile = writeFile(t, "dsn", []byte("postgres://edgeuser:"+secretish+"@127.0.0.1:1/edgedb?connect_timeout=2"))
	// Explicit development: plaintext is allowed, so the failure is the
	// connection itself (the TLS refusal is tested separately).
	c.dbTLS = pgshared.AllowPlaintextForDevelopment
	rt, err := Open(context.Background(), c, nil, nil)
	if rt != nil || !errors.Is(err, pgshared.ErrConnect) {
		t.Fatalf("(%v, %v)", rt, err)
	}
	for _, s := range []string{secretish, "edgeuser", "edgedb", "127.0.0.1"} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("the error names %q: %v", s, err)
		}
	}
	if len(*made) != 1 || !closed((*made)[0]) {
		t.Errorf("the pool was not closed after a failed start (%d pools)", len(*made))
	}
}

// ---------------------------------------------------------------------------
// Real PostgreSQL
//
// Gated exactly as identity's pgstore tests: AEGIS_IDENTITY_PG_DSN names a
// disposable database and AEGIS_IDENTITY_PG_DISPOSABLE=1 declares it so. Each
// test creates its own schema, builds the replay table in it from
// pgshared.ReplayTableDDL (identity's migration text), and drops it after.
// ---------------------------------------------------------------------------

const (
	dsnEnv        = "AEGIS_IDENTITY_PG_DSN"
	disposableEnv = "AEGIS_IDENTITY_PG_DISPOSABLE"
)

type pgEnv struct {
	admin   *pgxpool.Pool // in the test schema; for inspection only
	dsnFile string        // DSN with search_path set to the test schema
	schema  string
	scoped  string // that DSN; never logged
}

func requirePostgres(t *testing.T, withTable bool) *pgEnv {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(dsnEnv))
	if dsn == "" {
		t.Skipf("%s is not set: real-PostgreSQL test SKIPPED (not passed)", dsnEnv)
	}
	if os.Getenv(disposableEnv) != "1" {
		t.Fatalf("%s is set but %s is not 1: refusing to touch a database not declared disposable", dsnEnv, disposableEnv)
	}
	ctx := context.Background()
	var root *pgxpool.Pool
	var err error
	for attempt := 1; attempt <= 3; attempt++ { // harness setup only; see identity's pgstore tests
		if root, err = pgshared.OpenPool(ctx, dsn, pgshared.AllowPlaintextForDevelopment); err == nil {
			break
		}
		t.Logf("test harness connect attempt %d failed: %v", attempt, err)
		time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "edge_test_" + hex.EncodeToString(b)
	if _, execErr := root.Exec(ctx, `CREATE SCHEMA `+schema); execErr != nil {
		root.Close()
		t.Fatalf("creating test schema: %v", execErr)
	}
	var scoped string
	if strings.Contains(dsn, "://") {
		u, parseErr := url.Parse(dsn)
		if parseErr != nil {
			t.Fatal("parsing DSN (value not shown)")
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		scoped = u.String()
	} else {
		scoped = dsn + " search_path=" + schema
	}
	env := &pgEnv{dsnFile: writeFile(t, "dsn", []byte(scoped)), schema: schema, scoped: scoped}
	if env.admin, err = pgshared.OpenPool(ctx, scoped, pgshared.AllowPlaintextForDevelopment); err != nil {
		t.Fatalf("connecting to the test schema: %v", err)
	}
	t.Cleanup(func() {
		env.admin.Close()
		if _, err := root.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("dropping test schema %s: %v", schema, err)
		}
		root.Close()
	})
	if withTable {
		if _, err := env.admin.Exec(ctx, pgshared.ReplayTableDDL); err != nil {
			t.Fatalf("creating the replay table: %v", err)
		}
	}
	return env
}

// instances opens n INDEPENDENT authenticators — each its own auth.Open, its
// own pool — on one database, sharing only the issuer, the device and a
// clock. Every handler counts into the same counter.
type instances struct {
	rigs    []*rig
	rts     []*Runtime
	reached *atomic.Int32
}

func openInstances(t *testing.T, env *pgEnv, n int) *instances {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	device, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	c := &clock{t: now0}
	cfg := validConfig(t, pub)
	cfg.DatabaseDSNFile = env.dsnFile
	cfg.dbTLS = pgshared.AllowPlaintextForDevelopment // the disposable test database is plaintext
	in := &instances{reached: &atomic.Int32{}}
	for i := 0; i < n; i++ {
		logs := &syncBuf{}
		rt, err := Open(context.Background(), cfg, c.now, slog.New(slog.NewTextHandler(logs, nil)))
		if err != nil {
			t.Fatalf("instance %d: %v", i, err)
		}
		t.Cleanup(rt.Close)
		r := &rig{priv: priv, device: device, clock: c, logs: logs}
		r.signer = signerFor(t, priv, kid, issuer, audience, c)
		r.h = middleware.CorrelationIDMiddleware(rt.Authenticator().Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			in.reached.Add(1)
			r.reached.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("protected business logic ran"))
		})))
		in.rigs = append(in.rigs, r)
		in.rts = append(in.rts, rt)
	}
	return in
}

func rows(t *testing.T, env *pgEnv) int {
	t.Helper()
	var n int
	if err := env.admin.QueryRow(context.Background(), `SELECT count(*) FROM identity_dpop_replay`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A proof accepted by one instance is a replay at another.
func TestPostgres_ReplayIsRejectedAcrossInstances(t *testing.T) {
	env := requirePostgres(t, true)
	in := openInstances(t, env, 2)
	a, b := in.rigs[0], in.rigs[1]
	tok := a.token(t)
	p := a.goodProof(t, "GET", tok)
	if got := a.do(t, "GET", path, auth(tok, p)); got.code != 200 {
		t.Fatalf("instance A: %d %s", got.code, got.raw)
	}
	wantRefused(t, b, b.do(t, "GET", path, auth(tok, p)), 401, "ERR_INVALID_DPOP_PROOF", "invalid_dpop_proof")
	a.reached.Store(0)
	wantRefused(t, a, a.do(t, "GET", path, auth(tok, p)), 401, "ERR_INVALID_DPOP_PROOF", "invalid_dpop_proof")
	if in.reached.Load() != 1 || rows(t, env) != 1 {
		t.Errorf("handler ran %d times, %d reservations", in.reached.Load(), rows(t, env))
	}
	// A fresh proof is accepted by either instance.
	b.reached.Store(0)
	if got := b.do(t, "GET", path, auth(tok, b.goodProof(t, "GET", tok))); got.code != 200 {
		t.Fatalf("a fresh proof at instance B: %d %s", got.code, got.raw)
	}
	if got := a.do(t, "GET", path, auth(tok, a.goodProof(t, "GET", tok))); got.code != 200 {
		t.Fatalf("a fresh proof at instance A: %d %s", got.code, got.raw)
	}
	if rows(t, env) != 3 {
		t.Errorf("%d reservations, want 3", rows(t, env))
	}
}

// The same proof sent concurrently to two instances is accepted at most once.
func TestPostgres_ConcurrentReplayAcceptsAtMostOnce(t *testing.T) {
	env := requirePostgres(t, true)
	in := openInstances(t, env, 2)
	for round := 0; round < 5; round++ {
		in.reached.Store(0)
		tok := in.rigs[0].token(t)
		p := in.rigs[0].goodProof(t, "GET", tok)
		const n = 16
		codes := make([]int, n)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				r := in.rigs[i%2]
				req := httptest.NewRequest("GET", path, http.NoBody)
				for k, vs := range auth(tok, p) {
					for _, v := range vs {
						req.Header.Add(k, v)
					}
				}
				rec := newRecorder()
				r.h.ServeHTTP(rec, req)
				codes[i] = rec.code
			}(i)
		}
		close(start)
		wg.Wait()
		ok, refused := 0, 0
		for _, c := range codes {
			switch c {
			case 200:
				ok++
			case 401:
				refused++
			}
		}
		if ok != 1 || refused != n-1 || in.reached.Load() != 1 {
			t.Fatalf("round %d: %d accepted, %d refused, handler ran %d times (codes %v)", round, ok, refused, in.reached.Load(), codes)
		}
	}
}

// With its database gone, an instance refuses every request with 503 and never
// runs the handler; readiness reports the outage in fixed text.
func TestPostgres_StoreOutageFailsClosed(t *testing.T) {
	env := requirePostgres(t, true)
	in := openInstances(t, env, 2)
	a, b := in.rigs[0], in.rigs[1]
	in.rts[0].Close()
	tok := a.token(t)
	got := a.do(t, "GET", path, auth(tok, a.goodProof(t, "GET", tok)))
	wantRefused(t, a, got, 503, "ERR_TEMPORARILY_UNAVAILABLE", "")
	if got.hdr.Get("WWW-Authenticate") != "" {
		t.Errorf("a 503 carried WWW-Authenticate %q", got.hdr.Get("WWW-Authenticate"))
	}
	if err := in.rts[0].CheckDatabase(context.Background()); err == nil || err.Error() != "edge replay store unreachable" {
		t.Errorf("readiness = %v", err)
	}
	if strings.Contains(a.logs.String(), "closed pool") || strings.Contains(a.logs.String(), env.dsnFile) {
		t.Errorf("the store's error detail was logged: %s", a.logs.String())
	}
	// The other instance is unaffected.
	if got := b.do(t, "GET", path, auth(tok, b.goodProof(t, "GET", tok))); got.code != 200 {
		t.Fatalf("instance B: %d %s", got.code, got.raw)
	}
	if err := in.rts[1].CheckDatabase(context.Background()); err != nil {
		t.Errorf("instance B readiness: %v", err)
	}
	in.rts[0].Close() // idempotent
}

// A reservation outlives the proof's acceptance window, so cleanup inside the
// window removes nothing, and once cleanup does remove it the proof is already
// refused as stale.
func TestPostgres_ExpiryAndCleanupNeverPermitReplay(t *testing.T) {
	env := requirePostgres(t, true)
	in := openInstances(t, env, 2)
	a, b := in.rigs[0], in.rigs[1]
	store := pgshared.NewReplayStore(env.admin)
	ctx := context.Background()

	// The latest iat a proof may carry: FutureSkew ahead of the verifier.
	tok := a.token(t)
	claims := claimsFor(a, "GET", origin+path, tok)
	claims["iat"] = a.clock.now().Add(ProofFutureSkew).Unix()
	p := proofFor(t, a.device, a.device, claims)
	if got := a.do(t, "GET", path, auth(tok, p)); got.code != 200 {
		t.Fatalf("%d %s", got.code, got.raw)
	}
	iat := time.Unix(claims["iat"].(int64), 0)

	// Every instant the proof is still acceptable — the last one is
	// iat+ProofMaxAge — cleanup at that instant removes nothing, and the proof
	// is a replay at the other instance.
	for _, at := range []time.Time{now0, iat, iat.Add(ProofMaxAge)} {
		if n, err := store.Prune(ctx, at, 100); err != nil || n != 0 {
			t.Fatalf("prune at %v removed %d (%v)", at.Sub(now0), n, err)
		}
		a.clock.mu.Lock()
		a.clock.t = at
		a.clock.mu.Unlock()
		b.reached.Store(0)
		wantRefused(t, b, b.do(t, "GET", path, auth(tok, p)), 401, "ERR_INVALID_DPOP_PROOF", "invalid_dpop_proof")
	}
	if rows(t, env) != 1 {
		t.Fatalf("%d reservations inside the window, want 1", rows(t, env))
	}

	// After expiry cleanup removes the reservation, and the proof — now stale —
	// is still refused by both instances.
	after := iat.Add(ProofMaxAge + time.Second)
	a.clock.mu.Lock()
	a.clock.t = after
	a.clock.mu.Unlock()
	if n, err := store.Prune(ctx, after, 100); err != nil || n != 1 {
		t.Fatalf("prune after expiry removed %d (%v)", n, err)
	}
	for _, r := range in.rigs {
		r.reached.Store(0)
		wantRefused(t, r, r.do(t, "GET", path, auth(tok, p)), 401, "ERR_INVALID_DPOP_PROOF", "invalid_dpop_proof")
	}
	if rows(t, env) != 0 || in.reached.Load() != 1 {
		t.Errorf("%d reservations, handler ran %d times", rows(t, env), in.reached.Load())
	}
}

// A reachable database without the replay table — identity not migrated, or
// no privilege — is a startup failure, and the pool is closed.
func TestPostgres_MissingReplayTableFailsClosed(t *testing.T) {
	env := requirePostgres(t, false)
	made := recordPools(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	cfg := validConfig(t, pub)
	cfg.DatabaseDSNFile = env.dsnFile
	cfg.dbTLS = pgshared.AllowPlaintextForDevelopment // the disposable test database is plaintext
	rt, err := Open(context.Background(), cfg, nil, nil)
	if rt != nil || !errors.Is(err, pgshared.ErrReplaySchemaMissing) {
		t.Fatalf("(%v, %v)", rt, err)
	}
	if len(*made) != 1 || !closed((*made)[0]) {
		t.Error("the pool was not closed after a failed start")
	}
}

// recorder is a minimal concurrency-safe ResponseWriter (one per request).
type recorder struct {
	hdr  http.Header
	code int
}

func newRecorder() *recorder                    { return &recorder{hdr: http.Header{}, code: 200} }
func (r *recorder) Header() http.Header         { return r.hdr }
func (r *recorder) Write(b []byte) (int, error) { return len(b), nil }
func (r *recorder) WriteHeader(code int)        { r.code = code }
