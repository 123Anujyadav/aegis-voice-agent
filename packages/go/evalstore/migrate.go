package evalstore

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationFS carries the DDL chain inside the binary.
//
// Embedded rather than read from disk so the schema travels with the code that
// expects it. A deploy artefact that has to locate a migrations directory at
// runtime is a deploy artefact that can be started against the wrong one.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// ledgerDDL creates the applied-version ledger.
//
// Separate from the chain and applied first, because the chain cannot record
// its own progress until the ledger exists. IF NOT EXISTS so that this is
// itself idempotent.
const ledgerDDL = `
CREATE TABLE IF NOT EXISTS eval_schema_migrations (
	version     INTEGER     PRIMARY KEY,
	name        TEXT        NOT NULL,
	applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
)`

// Migration is one forward step in the DDL chain.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// MigrationResult reports what a Migrate call did.
type MigrationResult struct {
	// Applied lists migrations run by this call, in order.
	Applied []string
	// AlreadyApplied lists migrations that were already recorded.
	AlreadyApplied []string
	// Version is the highest version present after the call.
	Version int
}

// ErrDirtySchema is returned when the database is at a version this build does
// not know about — it was migrated by a newer binary.
//
// Refusing to run is deliberate. A build that silently operates against a
// schema from the future will read columns it does not understand and write
// rows the newer build cannot use.
var ErrDirtySchema = errors.New("evalstore: database schema is newer than this build")

// Migrations returns the embedded chain, ordered and validated.
//
// Validated at load rather than mid-run: a chain with a gap or a duplicate
// version is a packaging error, and discovering it halfway through leaves a
// database at a version no build describes.
func Migrations() ([]Migration, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("evalstore: reading embedded migrations: %w", err)
	}

	var out []Migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		name := e.Name()
		numPart, _, found := strings.Cut(strings.TrimSuffix(name, ".sql"), "_")
		if !found {
			return nil, fmt.Errorf("evalstore: migration %q must be named NNNN_name.sql", name)
		}
		v, err := strconv.Atoi(numPart)
		if err != nil {
			return nil, fmt.Errorf("evalstore: migration %q has a non-numeric version: %w", name, err)
		}
		body, err := migrationFS.ReadFile(path.Join("migrations", name))
		if err != nil {
			return nil, fmt.Errorf("evalstore: reading %q: %w", name, err)
		}
		out = append(out, Migration{Version: v, Name: name, SQL: string(body)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })

	for i, m := range out {
		if m.Version != i+1 {
			return nil, fmt.Errorf(
				"evalstore: migration chain must be contiguous from 1; found %d at position %d",
				m.Version, i+1)
		}
	}
	return out, nil
}

// Migrate applies every outstanding migration, in order.
//
// FORWARD ONLY, and idempotent: re-running applies nothing and reports what was
// already there. There are no down-migrations, because a down-migration that
// drops the records table is a data-loss tool wearing a safety feature's name.
// Rollback is a Snapshot taken before the migration — see ADR-0015.
//
// Each migration runs inside its OWN transaction, so a failing step leaves the
// database at the previous version rather than half-applied, and the ledger
// entry commits with the DDL it describes rather than after it.
func Migrate(ctx context.Context, pool *pgxpool.Pool) (MigrationResult, error) {
	var res MigrationResult

	chain, err := Migrations()
	if err != nil {
		return res, err
	}

	if _, err := pool.Exec(ctx, ledgerDDL); err != nil {
		return res, fmt.Errorf("evalstore: creating migration ledger: %w", err)
	}

	applied, highest, err := appliedVersions(ctx, pool)
	if err != nil {
		return res, err
	}
	if highest > len(chain) {
		return res, fmt.Errorf("%w: database at v%d, this build knows v%d",
			ErrDirtySchema, highest, len(chain))
	}

	for _, m := range chain {
		if applied[m.Version] {
			res.AlreadyApplied = append(res.AlreadyApplied, m.Name)
			continue
		}
		if err := applyOne(ctx, pool, m); err != nil {
			// res carries what succeeded before the failure, so a caller can
			// see how far the chain got rather than only that it stopped.
			return res, err
		}
		res.Applied = append(res.Applied, m.Name)
	}

	_, res.Version, err = appliedVersions(ctx, pool)
	if err != nil {
		return res, err
	}
	return res, nil
}

// applyOne runs one migration and records it, atomically.
func applyOne(ctx context.Context, pool *pgxpool.Pool, m Migration) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("evalstore: begin migration %s: %w", m.Name, err)
	}
	// Rollback after a successful Commit is a no-op, so this is safe on every
	// path and removes the chance of an early return leaking the transaction.
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return fmt.Errorf("evalstore: applying migration %s: %w", m.Name, err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO eval_schema_migrations (version, name) VALUES ($1, $2)`,
		m.Version, m.Name); err != nil {
		return fmt.Errorf("evalstore: recording migration %s: %w", m.Name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("evalstore: committing migration %s: %w", m.Name, err)
	}
	return nil
}

// appliedVersions reads the ledger.
func appliedVersions(ctx context.Context, pool *pgxpool.Pool) (map[int]bool, int, error) {
	rows, err := pool.Query(ctx, `SELECT version FROM eval_schema_migrations`)
	if err != nil {
		return nil, 0, fmt.Errorf("evalstore: reading migration ledger: %w", err)
	}
	defer rows.Close()

	applied := map[int]bool{}
	var highest int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, 0, fmt.Errorf("evalstore: scanning migration ledger: %w", err)
		}
		applied[v] = true
		if v > highest {
			highest = v
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("evalstore: reading migration ledger: %w", err)
	}
	return applied, highest, nil
}

// compile-time assurance that pgx.Tx stays the type applyOne expects.
var _ func(context.Context) (pgx.Tx, error) = (*pgxpool.Pool)(nil).Begin
