package pgstore

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationFS carries the DDL chain inside the binary, so the schema travels
// with the code that expects it (the evalstore convention).
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// ledgerDDL creates the applied-version ledger. It is this service's own
// ledger; no other chain records here.
const ledgerDDL = `
CREATE TABLE IF NOT EXISTS identity_schema_migrations (
	version     INTEGER     PRIMARY KEY,
	name        TEXT        NOT NULL,
	applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
)`

// migrationLockKey is the advisory lock held for a whole Migrate call, so two
// processes migrating at once apply each step once rather than racing. The
// value is arbitrary but fixed: ASCII "CSIDMIGR".
const migrationLockKey int64 = 0x435349444d494752

// Migration is one forward step in the chain.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// MigrationResult reports what a Migrate call did.
type MigrationResult struct {
	Applied        []string
	AlreadyApplied []string
	Version        int
}

// ErrSchemaFromTheFuture is a database migrated by a newer build. Running
// against it would read and write a schema this build does not describe.
var ErrSchemaFromTheFuture = errors.New("pgstore: database schema is newer than this build")

// Migrations returns the embedded chain, ordered and validated: contiguous
// versions from 1, each file named NNNN_name.sql.
func Migrations() ([]Migration, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("pgstore: reading embedded migrations: %w", err)
	}
	var out []Migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		name := e.Name()
		num, _, ok := strings.Cut(strings.TrimSuffix(name, ".sql"), "_")
		if !ok {
			return nil, fmt.Errorf("pgstore: migration %q must be named NNNN_name.sql", name)
		}
		v, err := strconv.Atoi(num)
		if err != nil {
			return nil, fmt.Errorf("pgstore: migration %q has a non-numeric version", name)
		}
		body, err := migrationFS.ReadFile(path.Join("migrations", name))
		if err != nil {
			return nil, fmt.Errorf("pgstore: reading %q: %w", name, err)
		}
		out = append(out, Migration{Version: v, Name: name, SQL: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i, m := range out {
		if m.Version != i+1 {
			return nil, fmt.Errorf("pgstore: migration chain must be contiguous from 1; found %d at position %d", m.Version, i+1)
		}
	}
	return out, nil
}

// Migrate applies every outstanding migration, in order, and is idempotent.
//
// It is never called implicitly: Open does not migrate, and nothing at startup
// calls this. A process that migrates on connect migrates from whichever
// replica starts first.
//
// FORWARD ONLY. Each step runs in its own transaction together with its ledger
// entry, so a failing step leaves the previous version intact. The whole call
// holds an advisory lock, so concurrent callers serialise. The migrations use
// plain CREATE, not IF NOT EXISTS: a database that already has identity_ tables
// this ledger did not create is refused, not silently adopted.
func Migrate(ctx context.Context, pool *pgxpool.Pool) (MigrationResult, error) {
	var res MigrationResult
	chain, err := Migrations()
	if err != nil {
		return res, err
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return res, fmt.Errorf("pgstore: acquiring a connection to migrate: %w", err)
	}
	defer conn.Release()
	if _, execErr := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); execErr != nil {
		return res, fmt.Errorf("pgstore: taking the migration lock: %w", execErr)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLockKey) //nolint:errcheck // an unlock fails only on a broken connection, which ends the session and its lock
	}()

	if _, execErr := conn.Exec(ctx, ledgerDDL); execErr != nil {
		return res, fmt.Errorf("pgstore: creating the migration ledger: %w", execErr)
	}
	applied, highest, err := readLedger(ctx, conn)
	if err != nil {
		return res, err
	}
	if highest > len(chain) {
		return res, fmt.Errorf("%w: database at v%d, this build knows v%d", ErrSchemaFromTheFuture, highest, len(chain))
	}

	for _, m := range chain {
		if applied[m.Version] {
			res.AlreadyApplied = append(res.AlreadyApplied, m.Name)
			continue
		}
		if err := applyMigration(ctx, conn, m); err != nil {
			return res, err
		}
		res.Applied = append(res.Applied, m.Name)
		highest = m.Version
	}
	res.Version = highest
	return res, nil
}

// readLedger returns the versions the ledger records and the highest of them.
func readLedger(ctx context.Context, conn *pgxpool.Conn) (map[int]bool, int, error) {
	applied := map[int]bool{}
	highest := 0
	rows, err := conn.Query(ctx, `SELECT version FROM identity_schema_migrations`)
	if err != nil {
		return nil, 0, fmt.Errorf("pgstore: reading the migration ledger: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		if scanErr := rows.Scan(&v); scanErr != nil {
			return nil, 0, fmt.Errorf("pgstore: reading the migration ledger: %w", scanErr)
		}
		applied[v] = true
		highest = max(highest, v)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("pgstore: reading the migration ledger: %w", err)
	}
	return applied, highest, nil
}

// applyMigration applies m and records it in the ledger, in one transaction.
func applyMigration(ctx context.Context, conn *pgxpool.Conn, m Migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgstore: beginning migration %s: %w", m.Name, err)
	}
	if _, execErr := tx.Exec(ctx, m.SQL); execErr != nil {
		_ = tx.Rollback(ctx) //nolint:errcheck // the Exec error is the one returned; a failed rollback is the same broken transaction
		return fmt.Errorf("pgstore: applying migration %s: %w", m.Name, execErr)
	}
	if _, execErr := tx.Exec(ctx, `INSERT INTO identity_schema_migrations (version, name) VALUES ($1, $2)`, m.Version, m.Name); execErr != nil {
		_ = tx.Rollback(ctx) //nolint:errcheck // the Exec error is the one returned; a failed rollback is the same broken transaction
		return fmt.Errorf("pgstore: recording migration %s: %w", m.Name, execErr)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pgstore: committing migration %s: %w", m.Name, err)
	}
	return nil
}
