package telephonystore

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

// The migration chain, following the evalstore convention (ADR-0015):
// forward-only, numbered, embedded, each step in its own transaction, recorded
// in a ledger. This package never applies DDL on its own initiative: [Migrate]
// is called by whoever owns the deployment's migration step, and nothing in
// this phase calls it outside tests that are given a disposable database.

//go:embed migrations/*.sql
var migrationFS embed.FS

// ledgerDDL creates this package's own ledger, separate from any other
// adapter's that may share the cluster.
const ledgerDDL = `
CREATE TABLE IF NOT EXISTS telephonystore_schema_migrations (
	version     INTEGER     PRIMARY KEY,
	name        TEXT        NOT NULL,
	applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
)`

// Migration is one forward step.
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

// ErrDirtySchema is returned when the database is at a version this build does
// not know.
var ErrDirtySchema = errors.New("telephonystore: database schema is newer than this build")

// Migrations returns the embedded chain, ordered and validated: versions must
// run 1..n with no gaps or duplicates.
func Migrations() ([]Migration, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("telephonystore: reading embedded migrations: %w", err)
	}
	var chain []Migration
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			return nil, fmt.Errorf("telephonystore: migration %q has no version prefix", name)
		}
		v, err := strconv.Atoi(prefix)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("telephonystore: migration %q has an invalid version", name)
		}
		body, err := migrationFS.ReadFile(path.Join("migrations", name))
		if err != nil {
			return nil, fmt.Errorf("telephonystore: reading migration %q: %w", name, err)
		}
		chain = append(chain, Migration{Version: v, Name: name, SQL: string(body)})
	}
	sort.Slice(chain, func(i, j int) bool { return chain[i].Version < chain[j].Version })
	for i, m := range chain {
		if m.Version != i+1 {
			return nil, fmt.Errorf("telephonystore: migration chain is not contiguous at %q (want version %d)", m.Name, i+1)
		}
	}
	return chain, nil
}

// Migrate applies every migration not yet recorded. Re-running it is a no-op.
func Migrate(ctx context.Context, pool *pgxpool.Pool) (MigrationResult, error) {
	var res MigrationResult
	chain, err := Migrations()
	if err != nil {
		return res, err
	}
	if _, err := pool.Exec(ctx, ledgerDDL); err != nil {
		return res, fmt.Errorf("telephonystore: creating migration ledger: %w", redact(err))
	}
	applied, highest, err := appliedVersions(ctx, pool)
	if err != nil {
		return res, err
	}
	if highest > len(chain) {
		return res, fmt.Errorf("%w: database at v%d, this build knows v%d", ErrDirtySchema, highest, len(chain))
	}
	for _, m := range chain {
		if applied[m.Version] {
			res.AlreadyApplied = append(res.AlreadyApplied, m.Name)
			continue
		}
		if err := applyOne(ctx, pool, m); err != nil {
			return res, err
		}
		res.Applied = append(res.Applied, m.Name)
	}
	_, res.Version, err = appliedVersions(ctx, pool)
	return res, err
}

func applyOne(ctx context.Context, pool *pgxpool.Pool, m Migration) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("telephonystore: begin migration %s: %w", m.Name, redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }() // a no-op after Commit

	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return fmt.Errorf("telephonystore: applying migration %s: %w", m.Name, redact(err))
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO telephonystore_schema_migrations (version, name) VALUES ($1, $2)`,
		m.Version, m.Name); err != nil {
		return fmt.Errorf("telephonystore: recording migration %s: %w", m.Name, redact(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("telephonystore: committing migration %s: %w", m.Name, redact(err))
	}
	return nil
}

func appliedVersions(ctx context.Context, pool *pgxpool.Pool) (map[int]bool, int, error) {
	rows, err := pool.Query(ctx, `SELECT version FROM telephonystore_schema_migrations`)
	if err != nil {
		return nil, 0, fmt.Errorf("telephonystore: reading migration ledger: %w", redact(err))
	}
	defer rows.Close()
	applied := map[int]bool{}
	var highest int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, 0, fmt.Errorf("telephonystore: scanning migration ledger: %w", redact(err))
		}
		applied[v] = true
		if v > highest {
			highest = v
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("telephonystore: reading migration ledger: %w", redact(err))
	}
	return applied, highest, nil
}
