package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/pgshared"
	"github.com/jackc/pgx/v5/pgconn"
)

// The database role edge-api runs as needs exactly what the deployment
// documentation (infra/k8s/base/services/edge-api.yaml) grants it:
//
//	GRANT USAGE ON SCHEMA <identity schema> TO <edge role>;
//	GRANT SELECT, INSERT, UPDATE ON identity_dpop_replay TO <edge role>;
//
// SELECT for Open's table check (and the conflict predicate), INSERT and
// UPDATE for Reserve's single INSERT ... ON CONFLICT DO UPDATE. No DELETE:
// edge-api never prunes; identity's housekeeping does. This test creates a
// throwaway role per case in the disposable database, runs the REAL Open and
// middleware as that role, and shows that each grant is necessary: without
// it, startup or every request fails closed.

// roleDSN is env's scoped DSN with the given credentials. It is never logged.
func roleDSN(t *testing.T, env *pgEnv, user, password string) string {
	t.Helper()
	if !strings.Contains(env.scoped, "://") {
		return env.scoped + " user=" + user + " password=" + password
	}
	u, err := url.Parse(env.scoped)
	if err != nil {
		t.Fatal("parsing DSN (value not shown)")
	}
	u.User = url.UserPassword(user, password)
	return u.String()
}

// grantRole creates a LOGIN role holding exactly grants (SQL fragments run
// with %s as the role name), dropped at cleanup, and returns a DSN file for it.
func grantRole(t *testing.T, env *pgEnv, grants ...string) string {
	t.Helper()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	role := "edge_grant_test_" + hex.EncodeToString(b)
	pw := make([]byte, 18)
	_, _ = rand.Read(pw)
	password := hex.EncodeToString(pw)
	ctx := context.Background()
	if _, err := env.admin.Exec(ctx, `CREATE ROLE `+role+` LOGIN PASSWORD '`+password+`'`); err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "42501" {
			t.Skip("the disposable database's role cannot CREATE ROLE: grant test SKIPPED (not passed)")
		}
		t.Fatalf("creating role: %v", pgshared.Redact(err))
	}
	t.Cleanup(func() {
		_, _ = env.admin.Exec(ctx, `DROP OWNED BY `+role)
		if _, err := env.admin.Exec(ctx, `DROP ROLE `+role); err != nil {
			t.Errorf("dropping role: %v", err)
		}
	})
	for _, g := range grants {
		if _, err := env.admin.Exec(ctx, strings.ReplaceAll(g, "%s", role)); err != nil {
			t.Fatalf("granting: %v", err)
		}
	}
	return writeFile(t, "role.dsn", []byte(roleDSN(t, env, role, password)))
}

func TestPostgres_DocumentedGrantsAreSufficientAndNecessary(t *testing.T) {
	env := requirePostgres(t, true)
	usage := `GRANT USAGE ON SCHEMA ` + env.schema + ` TO %s`
	table := func(privs string) string { return `GRANT ` + privs + ` ON identity_dpop_replay TO %s` }

	t.Run("documented grants: starts, accepts once, refuses the replay", func(t *testing.T) {
		in := openInstances(t, &pgEnv{admin: env.admin, dsnFile: grantRole(t, env, usage, table("SELECT, INSERT, UPDATE"))}, 2)
		a, b := in.rigs[0], in.rigs[1]
		tok := a.token(t)
		p := a.goodProof(t, "GET", tok)
		if got := a.do(t, "GET", path, auth(tok, p)); got.code != 200 {
			t.Fatalf("%d %s", got.code, got.raw)
		}
		wantRefused(t, b, b.do(t, "GET", path, auth(tok, p)), 401, "ERR_INVALID_DPOP_PROOF", "invalid_dpop_proof")
		if err := in.rts[0].CheckDatabase(context.Background()); err != nil {
			t.Errorf("readiness as the edge role: %v", err)
		}
	})

	for name, grants := range map[string][]string{
		"without SELECT":          {usage, table("INSERT, UPDATE")},
		"without USAGE on schema": {table("SELECT, INSERT, UPDATE")},
	} {
		t.Run(name+": startup refused", func(t *testing.T) {
			pub, _, _ := ed25519.GenerateKey(rand.Reader)
			cfg := validConfig(t, pub)
			cfg.DatabaseDSNFile = grantRole(t, env, grants...)
			cfg.dbTLS = pgshared.AllowPlaintextForDevelopment // the disposable test database is plaintext
			rt, err := Open(context.Background(), cfg, nil, nil)
			if rt != nil || !errors.Is(err, pgshared.ErrReplaySchemaMissing) {
				t.Fatalf("(%v, %v); want ErrReplaySchemaMissing", rt, err)
			}
		})
	}

	for name, privs := range map[string]string{"without INSERT": "SELECT, UPDATE", "without UPDATE": "SELECT, INSERT"} {
		t.Run(name+": every request fails closed", func(t *testing.T) {
			in := openInstances(t, &pgEnv{admin: env.admin, dsnFile: grantRole(t, env, usage, table(privs))}, 1)
			r := in.rigs[0]
			tok := r.token(t)
			wantRefused(t, r, r.do(t, "GET", path, auth(tok, r.goodProof(t, "GET", tok))), 503, "ERR_TEMPORARILY_UNAVAILABLE", "")
		})
	}
}
