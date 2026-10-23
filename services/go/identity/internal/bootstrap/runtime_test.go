package bootstrap

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/callscreen/callscreen-platform/packages/go/pgshared"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

// hmacKeyFile writes n random bytes and returns the path and the bytes.
func hmacKeyFile(t *testing.T, n int) (string, []byte) {
	t.Helper()
	k := make([]byte, n)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return writeFile(t, "hmac.key", k), k
}

func assertNoLeak(t *testing.T, err error, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if s != "" && strings.Contains(err.Error(), s) {
			t.Errorf("the error carries %q: %v", s, err)
		}
	}
}

// 1, 2, 4: the HMAC key file.
func TestReadHMACSecretKeyFile(t *testing.T) {
	t.Run("valid bytes are returned exactly, nothing trimmed", func(t *testing.T) {
		raw := append([]byte(" \t\x00"), make([]byte, 40)...)
		_, _ = rand.Read(raw[3:])
		raw = append(raw, "\r\n"...)
		got, err := ReadHMACSecretKeyFile(writeFile(t, "k", raw))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, raw) {
			t.Errorf("the key was altered: %d bytes back, %d written", len(got), len(raw))
		}
	})
	t.Run("exactly the minimum", func(t *testing.T) {
		p, k := hmacKeyFile(t, MinSecretKeyBytes)
		if got, err := ReadHMACSecretKeyFile(p); err != nil || !bytes.Equal(got, k) {
			t.Errorf("(%d bytes, %v)", len(got), err)
		}
	})

	short := make([]byte, MinSecretKeyBytes-1)
	_, _ = rand.Read(short)
	for name, tc := range map[string]struct {
		path    func(t *testing.T) string
		mention string
		secret  []byte
	}{
		"missing":       {func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent") }, "does not exist", nil},
		"directory":     {func(t *testing.T) string { return t.TempDir() }, "not a regular file", nil},
		"empty":         {func(t *testing.T) string { return writeFile(t, "k", nil) }, "at least 32 bytes", nil},
		"too short":     {func(t *testing.T) string { return writeFile(t, "k", short) }, "at least 32 bytes", short},
		"all zero":      {func(t *testing.T) string { return writeFile(t, "k", make([]byte, 64)) }, "single repeated byte", nil},
		"one character": {func(t *testing.T) string { return writeFile(t, "k", bytes.Repeat([]byte("a"), 64)) }, "single repeated byte", nil},
		"oversized":     {func(t *testing.T) string { return writeFile(t, "k", make([]byte, MaxHMACKeyFileBytes+1)) }, "larger than", nil},
	} {
		t.Run(name, func(t *testing.T) {
			path := tc.path(t)
			_, err := ReadHMACSecretKeyFile(path)
			wantInvalid(t, err, "CS_IDENTITY_HMAC_SECRET_KEY_FILE")
			if !strings.Contains(err.Error(), tc.mention) {
				t.Errorf("err = %q, want %q", err, tc.mention)
			}
			assertNoLeak(t, err, path, filepath.Base(filepath.Dir(path)), hex.EncodeToString(tc.secret), string(tc.secret))
		})
	}
}

// 3, 4: the DSN file.
func TestReadDSNFile(t *testing.T) {
	const dsn = "postgres://alice:hunter2-pw@db.internal.example:5432/identity?sslmode=require"
	t.Run("valid, trailing newline removed", func(t *testing.T) {
		got, err := ReadDSNFile(writeFile(t, "dsn", []byte(dsn+"\n")))
		if err != nil || got != dsn {
			t.Errorf("(%q, %v)", got, err)
		}
	})
	for name, tc := range map[string]struct {
		content []byte
		dir     bool
		missing bool
		mention string
	}{
		"missing":     {missing: true, mention: "does not exist"},
		"directory":   {dir: true, mention: "not a regular file"},
		"empty":       {content: nil, mention: "no connection string"},
		"whitespace":  {content: []byte(" \n\t\r\n "), mention: "no connection string"},
		"two lines":   {content: []byte(dsn + "\n" + dsn), mention: "more than one line"},
		"bad port":    {content: []byte("postgres://alice:hunter2-pw@db.internal.example:notaport/identity"), mention: "cannot be parsed"},
		"bad keyword": {content: []byte("host=db.internal.example password=hunter2-pw sslmode"), mention: "cannot be parsed"},
		"bad scheme":  {content: []byte("postgres://alice:hunter2-pw@[db.internal.example/identity"), mention: "cannot be parsed"},
		"oversized":   {content: bytes.Repeat([]byte("x"), MaxDSNFileBytes+1), mention: "larger than"},
	} {
		t.Run(name, func(t *testing.T) {
			var path string
			switch {
			case tc.missing:
				path = filepath.Join(t.TempDir(), "absent")
			case tc.dir:
				path = t.TempDir()
			default:
				path = writeFile(t, "dsn", tc.content)
			}
			_, err := ReadDSNFile(path)
			wantInvalid(t, err, "CS_IDENTITY_DATABASE_DSN_FILE")
			if !strings.Contains(err.Error(), tc.mention) {
				t.Errorf("err = %q, want %q", err, tc.mention)
			}
			assertNoLeak(t, err, "alice", "hunter2-pw", "db.internal.example", path, filepath.Base(filepath.Dir(path)))
		})
	}
}

// fullConfig writes every file a complete activation needs.
func fullConfig(t *testing.T, dsn string) (ActivationConfig, ed25519.PublicKey, []byte) {
	t.Helper()
	keyFile, pub, _ := signingKeyFile(t)
	hmacFile, hmacKey := hmacKeyFile(t, 48)
	return ActivationConfig{
		Security:          validConfig(keyFile),
		HMACSecretKeyFile: hmacFile,
		DatabaseDSNFile:   writeFile(t, "dsn", []byte(dsn+"\n")),
	}, pub, hmacKey
}

func setActivationEnv(t *testing.T, c ActivationConfig) {
	t.Helper()
	setEnv(t, c.Security)
	t.Setenv("CS_IDENTITY_HMAC_SECRET_KEY_FILE", c.HMACSecretKeyFile)
	t.Setenv("CS_IDENTITY_DATABASE_DSN_FILE", c.DatabaseDSNFile)
}

// 5, 6: none set is inactive; any one set requires them all.
func TestLoadActivation(t *testing.T) {
	clearAll := func(t *testing.T) {
		for _, n := range ActivationVariables {
			unset(t, n)
		}
	}
	t.Run("none set: inactive", func(t *testing.T) {
		clearAll(t)
		t.Setenv("CS_IDENTITY_LOG_LEVEL", "debug") // not an activation variable
		if _, active, err := LoadActivation(); active || err != nil {
			t.Errorf("(active %v, %v), want inactive", active, err)
		}
	})
	for _, only := range ActivationVariables {
		t.Run("only "+only, func(t *testing.T) {
			clearAll(t)
			t.Setenv(only, "x")
			_, active, err := LoadActivation()
			if !active {
				t.Error("a supplied identity variable did not activate identity")
			}
			wantInvalid(t, err, "partially configured")
			for _, other := range ActivationVariables {
				if other != only && !strings.Contains(err.Error(), other) {
					t.Errorf("the error does not name the missing %s", other)
				}
			}
		})
	}
	t.Run("set but empty still activates", func(t *testing.T) {
		clearAll(t)
		t.Setenv("CS_IDENTITY_DATABASE_DSN_FILE", "")
		if _, active, err := LoadActivation(); !active || err == nil {
			t.Errorf("(active %v, %v), want an activation error", active, err)
		}
	})
	t.Run("all set and valid", func(t *testing.T) {
		c, _, _ := fullConfig(t, "postgres://u@127.0.0.1:1/db")
		setActivationEnv(t, c)
		got, active, err := LoadActivation()
		if !active || err != nil || got != c {
			t.Errorf("(%+v, %v, %v)", got, active, err)
		}
	})
	t.Run("one missing of eight", func(t *testing.T) {
		c, _, _ := fullConfig(t, "postgres://u@127.0.0.1:1/db")
		setActivationEnv(t, c)
		unset(t, "CS_IDENTITY_HMAC_SECRET_KEY_FILE")
		_, active, err := LoadActivation()
		if !active {
			t.Error("not active")
		}
		wantInvalid(t, err, "CS_IDENTITY_HMAC_SECRET_KEY_FILE")
	})
}

// One file may not serve two purposes, and the HMAC key may not be, or
// contain, the signing key.
func TestOpen_SecretsMustBeDistinct(t *testing.T) {
	calls := 0
	orig := newPool
	newPool = func(string, pgshared.TLSPolicy) (*pgxpool.Pool, error) {
		calls++
		return orig("postgres://u@127.0.0.1:1/db", pgshared.AllowPlaintextForDevelopment)
	}
	t.Cleanup(func() { newPool = orig })

	c, _, _ := fullConfig(t, "postgres://u@127.0.0.1:1/db")
	same := c
	same.HMACSecretKeyFile = c.Security.AccessTokenPrivateKeyFile
	_, err := Open(context.Background(), same, nil)
	wantInvalid(t, err, "name the same file")

	same = c
	same.DatabaseDSNFile = c.HMACSecretKeyFile
	_, err = Open(context.Background(), same, nil)
	wantInvalid(t, err, "name the same file")

	pemBytes, _ := os.ReadFile(c.Security.AccessTokenPrivateKeyFile)
	copyOfPEM := c
	copyOfPEM.HMACSecretKeyFile = writeFile(t, "copy.pem", pemBytes)
	_, err = Open(context.Background(), copyOfPEM, nil)
	wantInvalid(t, err, "is the signing key file")

	key, _ := accesstoken.ParsePrivateKeyPEM(pemBytes)
	containing := c
	containing.HMACSecretKeyFile = writeFile(t, "seed.key", append(append([]byte("prefix-"), key.Seed()...), "-suffix"...))
	_, err = Open(context.Background(), containing, nil)
	wantInvalid(t, err, "contains the signing key")
	assertNoLeak(t, err, string(key.Seed()), hex.EncodeToString(key.Seed()))

	// A signer error (here an unprintable key id, which only NewSigner checks)
	// is also found before any connection is attempted.
	badKid := c
	badKid.Security.AccessTokenKeyID = "kid\x01"
	_, err = Open(context.Background(), badKid, nil)
	wantInvalid(t, err, "signer")

	if calls != 0 {
		t.Errorf("a pool was created %d times before local validation passed", calls)
	}
}

// 8, 4: an unreachable database fails closed, with no credentials in the
// error, and the pool that was created is closed.
func TestOpen_UnreachableDatabaseFailsClosed(t *testing.T) {
	var created *pgxpool.Pool
	orig := newPool
	// This test is about an unreachable database, not TLS: the pool is built
	// under the development policy so it gets as far as the connection.
	newPool = func(dsn string, _ pgshared.TLSPolicy) (*pgxpool.Pool, error) {
		p, err := orig(dsn, pgshared.AllowPlaintextForDevelopment)
		created = p
		return p, err
	}
	t.Cleanup(func() { newPool = orig })

	c, _, hmacKey := fullConfig(t, "postgres://alice:hunter2-pw@127.0.0.1:1/identity?connect_timeout=2")
	rt, err := Open(context.Background(), c, nil)
	if rt != nil || !errors.Is(err, pgstore.ErrConnect) {
		t.Fatalf("(%v, %v), want ErrConnect and no runtime", rt, err)
	}
	assertNoLeak(t, err, "alice", "hunter2-pw", "127.0.0.1", ":1", c.DatabaseDSNFile, hex.EncodeToString(hmacKey))
	if created == nil {
		t.Fatal("no pool was created")
	}
	if err := created.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "closed pool") {
		t.Errorf("the pool was not closed after the failure: ping = %v", err)
	}
}

// 10: Close is idempotent, and readiness answers with fixed text.
func TestRuntime_CloseAndCheck(t *testing.T) {
	pool, err := pgstore.NewPool("postgres://alice:hunter2-pw@127.0.0.1:1/identity?connect_timeout=1", pgshared.AllowPlaintextForDevelopment)
	if err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{pool: pool}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rt.CheckDatabase(ctx); err == nil || err.Error() != "identity database unreachable" {
		t.Errorf("CheckDatabase = %v, want the fixed message", err)
	}
	rt.Close()
	rt.Close()
	if err := pool.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "closed pool") {
		t.Errorf("Close did not close the pool: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Real PostgreSQL. Skipped unless AEGIS_IDENTITY_PG_DSN is set, and refused
// unless AEGIS_IDENTITY_PG_DISPOSABLE=1 declares that database disposable.
// ---------------------------------------------------------------------------

// pgSchemaDSN creates and migrates a fresh schema and returns a DSN bound to it.
func pgSchemaDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("AEGIS_IDENTITY_PG_DSN"))
	if dsn == "" {
		t.Skip("AEGIS_IDENTITY_PG_DSN is not set: real-PostgreSQL test SKIPPED (not passed)")
	}
	if os.Getenv("AEGIS_IDENTITY_PG_DISPOSABLE") != "1" {
		t.Fatal("AEGIS_IDENTITY_PG_DSN is set but AEGIS_IDENTITY_PG_DISPOSABLE is not 1: refusing")
	}
	ctx := context.Background()
	admin, err := pgstore.OpenPool(ctx, dsn, pgshared.AllowPlaintextForDevelopment)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "identity_test_" + hex.EncodeToString(b)
	if _, execErr := admin.Exec(ctx, `CREATE SCHEMA `+schema); execErr != nil {
		t.Fatal(execErr)
	}
	t.Cleanup(func() {
		if _, execErr := admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`); execErr != nil {
			t.Errorf("dropping %s: %v", schema, execErr)
		}
		admin.Close()
	})
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	bound := dsn + sep + "search_path=" + schema
	p, err := pgstore.OpenPool(ctx, bound, pgshared.AllowPlaintextForDevelopment)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := pgstore.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	return bound
}

func mustPseudonym(t *testing.T, key []byte, raw string) string {
	t.Helper()
	p, err := app.MSISDNPseudonym(key, raw)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// seedChallenge inserts a challenge for a code the test knows, hashed under
// the activation's HMAC key exactly as the service hashes codes.
func seedChallenge(t *testing.T, dsn string, key []byte, reqID, code string) {
	t.Helper()
	p, err := pgstore.OpenPool(context.Background(), dsn, pgshared.AllowPlaintextForDevelopment)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	h := hmac.New(sha256.New, key)
	h.Write([]byte(code))
	if err := pgstore.NewRepository(p).SaveOtp(context.Background(), &app.OtpChallenge{
		RequestID: reqID, MSISDNPseudonym: mustPseudonym(t, key, testMSISDN), SubscriberID: "usr_test", CodeHash: hex.EncodeToString(h.Sum(nil)), ExpiresAt: time.Now().Add(5 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
}

// 7, 9, 10: a complete activation builds the service on PostgreSQL; two
// runtimes share its repository and replay stores; enrolment then proof-bound
// refresh work; Close releases the pool.
func TestOpen_Postgres_EnrolAndRefreshAcrossRuntimes(t *testing.T) {
	dsn := pgSchemaDSN(t)
	clock := time.Now().Truncate(time.Second)
	now := func() time.Time { return clock }
	c, pub, hmacKey := fullConfig(t, dsn)
	c.dbTLS = pgshared.AllowPlaintextForDevelopment // the disposable test database is plaintext
	a, err := Open(context.Background(), c, now)
	if err != nil {
		t.Fatalf("Open A: %v", err)
	}
	defer a.Close()
	b, err := Open(context.Background(), c, now)
	if err != nil {
		t.Fatalf("Open B: %v", err)
	}
	if checkDatabaseErr := a.CheckDatabase(context.Background()); checkDatabaseErr != nil {
		t.Fatalf("readiness: %v", checkDatabaseErr)
	}
	verifier, _ := accesstoken.NewVerifier(accesstoken.VerifierConfig{
		Issuer: testIssuer, Audience: testAudience, Keys: map[string]ed25519.PublicKey{testKeyID: pub}, Now: now,
	})
	device, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	jkt, _ := accesstoken.ThumbprintP256(&device.PublicKey)
	ctx := context.Background()

	seedChallenge(t, dsn, hmacKey, "req_1", testCode)
	seedChallenge(t, dsn, hmacKey, "req_2", testCode)
	enrol := proof(t, device, "e1", testEnrolURL, clock)
	sess, token, err := a.Service.ValidateOtpWithDeviceProof(ctx, "req_1", testCode, "POST", enrol)
	if err != nil {
		t.Fatalf("enrol on A: %v", err)
	}
	if cl, verifyErr := verifier.Verify(token); verifyErr != nil || cl.Confirmation.JKT != jkt {
		t.Fatalf("A's token: (%+v, %v)", cl, verifyErr)
	}
	if _, _, validateOtpWithDeviceProofErr := b.Service.ValidateOtpWithDeviceProof(ctx, "req_2", testCode, "POST", enrol); !errors.Is(validateOtpWithDeviceProofErr, dpop.ErrReplayed) {
		t.Errorf("the proof replayed on B: err = %v, want ErrReplayed (shared PostgreSQL replay store)", validateOtpWithDeviceProofErr)
	}
	access, _, err := b.Service.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", proof(t, device, "r1", testRefresh, clock))
	if err != nil {
		t.Fatalf("refresh on B of A's session: %v", err)
	}
	if cl, err := verifier.Verify(access); err != nil || cl.Confirmation.JKT != jkt {
		t.Fatalf("B's token: (%+v, %v)", cl, err)
	}

	b.Close()
	if err := b.CheckDatabase(ctx); err == nil {
		t.Error("readiness still passes after Close")
	}
	if err := a.CheckDatabase(ctx); err != nil {
		t.Errorf("closing B affected A: %v", err)
	}
}
