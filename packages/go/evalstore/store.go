// Package evalstore is a PostgreSQL backend for [evaluation.Repository].
//
// It exists because approved goldens encode human decisions that no process can
// reproduce, and until now they lived only in memory — a restart lost them.
// That is production blocker B2.
//
// It is an ADAPTER, not a layer. Every behavioural rule lives in the port and
// is reproduced here; nothing in this package decides anything the port has not
// already decided. The engines depend on [evaluation.Repository] and do not know
// this package exists.
//
// Configuration comes from the environment. No credential, password or DSN
// containing one appears in this repository. See ADR-0015.
package evalstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	ev "github.com/callscreen/callscreen-platform/packages/go/evaluation"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DSNEnv is the environment variable holding the connection string.
//
// A variable rather than a constant in configuration, and never a literal in
// source: a DSN carries a password, and a password in git is a password that has
// leaked. Falling back to pgx's own PG* handling means a developer who already
// has PGHOST/PGUSER set needs no extra setup.
const DSNEnv = "AEGIS_EVALSTORE_DSN"

// DSNFromEnv returns the configured DSN, or "" when none is set.
//
// Empty is not an error here. It is the signal an integration test uses to skip
// rather than to invent a credential.
func DSNFromEnv() string { return strings.TrimSpace(os.Getenv(DSNEnv)) }

// Store is a PostgreSQL-backed [evaluation.Repository].
type Store struct {
	pool  *pgxpool.Pool
	clock rt.Clock

	mu     sync.Mutex
	closed bool
	nextID uint64
}

// Compile-time proof that this satisfies the port. If the frozen interface gains
// a method, this fails here rather than at a call site.
var _ ev.Repository = (*Store)(nil)

// Open connects and returns a store.
//
// It does NOT migrate. Applying DDL is a deliberate, separately-invoked step —
// a process that migrates on connect will migrate from whichever replica starts
// first, which is how two concurrent deploys race each other's schema.
func Open(ctx context.Context, dsn string, clock rt.Clock) (*Store, error) {
	if clock == nil {
		return nil, errors.New("evalstore: a clock is required")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// The error from ParseConfig can echo the DSN, which may carry a
		// password. Reported without it.
		return nil, fmt.Errorf("evalstore: invalid connection configuration (value not shown)")
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("evalstore: creating connection pool: %w", err)
	}
	// NewWithConfig is lazy, so a bad host would otherwise surface at the first
	// query rather than at Open, which is the wrong place to discover it.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("evalstore: connecting: %w", err)
	}
	return &Store{pool: pool, clock: clock}, nil
}

// Pool exposes the underlying pool, for migrations and for tests.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Close releases the pool. Safe to call more than once, as the port requires.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.pool.Close()
	return nil
}

// checkOpen reports ErrClosed after Close.
func (s *Store) checkOpen() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ev.ErrClosed
	}
	return nil
}

// guard is the preamble every method shares: honour the caller's cancellation
// before touching the pool, and refuse a closed store.
func (s *Store) guard(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.checkOpen()
}

// ---------------------------------------------------------------------------
// Writes
// ---------------------------------------------------------------------------

// Put stores or replaces a record.
func (s *Store) Put(ctx context.Context, rec ev.Record) error {
	if err := s.guard(ctx); err != nil {
		return err
	}
	if err := validateForWrite(rec); err != nil {
		return err
	}
	rec = s.defaults(rec)

	if _, err := s.pool.Exec(ctx, upsertSQL, upsertArgs(rec)...); err != nil {
		return wrap("storing "+rec.Key().String(), err)
	}
	return nil
}

// PutBatch stores many records atomically.
//
// The port does not promise atomicity — "a backend that cannot provide it
// should not be excluded and should not lie about it" — but PostgreSQL can, so
// this one does. Every record is validated before anything is written, so a
// rejected record cannot leave earlier ones committed.
func (s *Store) PutBatch(ctx context.Context, recs []ev.Record) error {
	if err := s.guard(ctx); err != nil {
		return err
	}
	for _, rec := range recs {
		if err := validateForWrite(rec); err != nil {
			return err
		}
	}
	if len(recs) == 0 {
		return nil
	}

	return s.inTx(ctx, func(tx pgx.Tx) error {
		for _, rec := range recs {
			r := s.defaults(rec)
			if _, err := tx.Exec(ctx, upsertSQL, upsertArgs(r)...); err != nil {
				return wrap("storing "+r.Key().String(), err)
			}
		}
		return nil
	})
}

// Delete removes one record, refusing a legal hold, and audits the removal.
//
// Both in ONE transaction: the port requires that a deletion and its audit
// entry are atomic where the backend can make them so. A deletion whose audit
// entry was lost is indistinguishable from data that was never there.
func (s *Store) Delete(ctx context.Context, key ev.RecordKey) error {
	if err := s.guard(ctx); err != nil {
		return err
	}

	return s.inTx(ctx, func(tx pgx.Tx) error {
		var hold bool
		err := tx.QueryRow(ctx,
			`SELECT legal_hold FROM eval_records WHERE kind=$1 AND id=$2 FOR UPDATE`,
			string(key.Kind), key.ID).Scan(&hold)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %s", ev.ErrRecordNotFound, key)
		}
		if err != nil {
			return wrap("locking "+key.String(), err)
		}
		if hold {
			return fmt.Errorf("%w: %s", ev.ErrLegalHold, key)
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM eval_records WHERE kind=$1 AND id=$2`,
			string(key.Kind), key.ID); err != nil {
			return wrap("deleting "+key.String(), err)
		}
		return s.auditTx(ctx, tx, ev.AuditDeleted, []ev.RecordKey{key}, 1, "", "", "")
	})
}

// SetLegalHold places or lifts a hold, and audits either way.
func (s *Store) SetLegalHold(ctx context.Context, key ev.RecordKey,
	hold bool, by, reason string,
) error {
	if err := s.guard(ctx); err != nil {
		return err
	}
	var problems []string
	if by == "" {
		problems = append(problems, "legal hold: By is required; an unattributed "+
			"hold cannot be defended and cannot be lifted with confidence")
	}
	if reason == "" {
		problems = append(problems, "legal hold: Reason is required")
	}
	if len(problems) > 0 {
		return &ev.ConfigError{Problems: problems}
	}

	return s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE eval_records SET legal_hold=$3 WHERE kind=$1 AND id=$2`,
			string(key.Kind), key.ID, hold)
		if err != nil {
			return wrap("setting legal hold on "+key.String(), err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("%w: %s", ev.ErrRecordNotFound, key)
		}
		action := ev.AuditHoldLifted
		if hold {
			action = ev.AuditHoldPlaced
		}
		return s.auditTx(ctx, tx, action, []ev.RecordKey{key}, 1, "", by, reason)
	})
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

// Get retrieves one record.
func (s *Store) Get(ctx context.Context, key ev.RecordKey) (ev.Record, error) {
	if err := s.guard(ctx); err != nil {
		return ev.Record{}, err
	}
	row := s.pool.QueryRow(ctx, selectColumns+
		` FROM eval_records WHERE kind=$1 AND id=$2`, string(key.Kind), key.ID)

	rec, err := scanRecord(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ev.Record{}, fmt.Errorf("%w: %s", ev.ErrRecordNotFound, key)
	}
	if err != nil {
		return ev.Record{}, wrap("reading "+key.String(), err)
	}
	return rec, nil
}

// List retrieves matching records, newest first.
func (s *Store) List(ctx context.Context, q ev.Query) ([]ev.Record, error) {
	if err := s.guard(ctx); err != nil {
		return nil, err
	}
	sql, args := buildQuery(q, false)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, wrap("listing records", err)
	}
	defer rows.Close()

	var out []ev.Record
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, wrap("scanning record", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("listing records", err)
	}
	return out, nil
}

// Count returns how many records match, without materialising them.
func (s *Store) Count(ctx context.Context, q ev.Query) (int, error) {
	if err := s.guard(ctx); err != nil {
		return 0, err
	}
	sql, args := buildQuery(q, true)

	var n int
	if err := s.pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		return 0, wrap("counting records", err)
	}
	return n, nil
}

// Schema reports the lowest and highest record schema versions present.
func (s *Store) Schema(ctx context.Context) (low, high ev.SchemaVersion, err error) {
	if err := s.guard(ctx); err != nil {
		return 0, 0, err
	}
	var lo, hi *int
	if err := s.pool.QueryRow(ctx,
		`SELECT MIN(schema_ver), MAX(schema_ver) FROM eval_records`).Scan(&lo, &hi); err != nil {
		return 0, 0, wrap("reading schema range", err)
	}
	// An empty store reports the current version rather than zero, matching the
	// reference implementation: "nothing stored" must not look like "everything
	// is ancient and needs migrating".
	if lo == nil || hi == nil {
		return ev.CurrentSchema, ev.CurrentSchema, nil
	}
	return ev.SchemaVersion(*lo), ev.SchemaVersion(*hi), nil
}

// ---------------------------------------------------------------------------
// Retention
// ---------------------------------------------------------------------------

// Sweep deletes everything expired at `now` and reports what it removed.
func (s *Store) Sweep(ctx context.Context, now time.Time) (ev.SweepReport, error) {
	if err := s.guard(ctx); err != nil {
		return ev.SweepReport{}, err
	}
	started := time.Now()

	report := ev.SweepReport{
		At:      now,
		Deleted: make(map[ev.RecordKind]int),
		Held:    make(map[ev.RecordKind]int),
	}

	err := s.inTx(ctx, func(tx pgx.Tx) error {
		// Held records are counted, never deleted. Reported separately because
		// "a sweep that silently retained a thousand held records looks
		// identical to a sweep with nothing to do".
		heldRows, err := tx.Query(ctx, `
			SELECT kind, count(*) FROM eval_records
			WHERE kind <> $1 AND legal_hold = TRUE
			  AND expires_at IS NOT NULL AND expires_at <= $2
			GROUP BY kind`, string(ev.RecordAudit), now)
		if err != nil {
			return wrap("counting held records", err)
		}
		for heldRows.Next() {
			var kind string
			var n int
			if err := heldRows.Scan(&kind, &n); err != nil {
				heldRows.Close()
				return wrap("scanning held count", err)
			}
			report.Held[ev.RecordKind(kind)] = n
		}
		heldRows.Close()
		if err := heldRows.Err(); err != nil {
			return wrap("counting held records", err)
		}

		// Audit records are exempt: the record of WHAT WAS DELETED is the
		// evidence the retention bound was honoured.
		delRows, err := tx.Query(ctx, `
			DELETE FROM eval_records
			WHERE kind <> $1 AND legal_hold = FALSE
			  AND expires_at IS NOT NULL AND expires_at <= $2
			RETURNING kind, id`, string(ev.RecordAudit), now)
		if err != nil {
			return wrap("sweeping expired records", err)
		}
		for delRows.Next() {
			var kind, id string
			if err := delRows.Scan(&kind, &id); err != nil {
				delRows.Close()
				return wrap("scanning swept record", err)
			}
			report.Deleted[ev.RecordKind(kind)]++
			if len(report.Keys) < maxAuditKeys {
				report.Keys = append(report.Keys, ev.RecordKey{Kind: ev.RecordKind(kind), ID: id})
			}
		}
		delRows.Close()
		if err := delRows.Err(); err != nil {
			return wrap("sweeping expired records", err)
		}

		if report.Total() > 0 || report.TotalHeld() > 0 {
			return s.auditTx(ctx, tx, ev.AuditSwept, report.Keys, report.Total(), "", "", "")
		}
		return nil
	})
	if err != nil {
		return ev.SweepReport{}, err
	}

	report.TookNanos = time.Since(started)
	return report, nil
}

// ---------------------------------------------------------------------------
// Record-schema migration
// ---------------------------------------------------------------------------

// Migrate upgrades every record below CurrentSchema.
//
// DISTINCT FROM the DDL chain in migrate.go. This walks stored payloads and
// re-encodes them; that one changes table definitions. They share a word and
// nothing else.
func (s *Store) Migrate(ctx context.Context, m ev.Migrator) (ev.MigrationReport, error) {
	started := time.Now()

	low, high, err := s.Schema(ctx)
	if err != nil {
		return ev.MigrationReport{}, err
	}
	report := ev.MigrationReport{From: low, To: ev.CurrentSchema}
	if high > ev.CurrentSchema {
		return report, fmt.Errorf("%w: store holds v%d", ev.ErrSchemaTooNew, high)
	}

	// Everything, including records this build cannot decode — migrating them
	// is the entire point.
	all, err := s.List(ctx, ev.Query{IncludeUnreadable: true})
	if err != nil {
		return report, err
	}

	steps := map[string]bool{}
	for _, rec := range all {
		if rec.Schema >= ev.CurrentSchema {
			report.Skipped++
			continue
		}
		path, perr := m.Path(rec.Schema)
		if perr != nil {
			report.Failed = append(report.Failed, ev.MigrationFailure{
				Key: rec.Key(), Schema: rec.Schema, Reason: perr.Error(),
			})
			continue
		}

		migrated := rec
		failed := false
		for _, step := range path {
			next, aerr := step.Apply(migrated)
			if aerr != nil {
				// One malformed payload must not strand every other record at
				// the old version.
				report.Failed = append(report.Failed, ev.MigrationFailure{
					Key: rec.Key(), Schema: rec.Schema, Reason: aerr.Error(),
				})
				failed = true
				break
			}
			migrated = next
			// Same label the reference implementation records, so a report from
			// either backend reads identically.
			steps[fmt.Sprintf("v%d→v%d (%s)", step.From, step.To, step.Description)] = true
		}
		if failed {
			continue
		}
		if err := s.Put(ctx, migrated); err != nil {
			report.Failed = append(report.Failed, ev.MigrationFailure{
				Key: rec.Key(), Schema: rec.Schema, Reason: err.Error(),
			})
			continue
		}
		report.Migrated++
	}

	for name := range steps {
		report.Steps = append(report.Steps, name)
	}
	sortStrings(report.Steps)
	report.TookNanos = time.Since(started)
	return report, nil
}

// ---------------------------------------------------------------------------
// Snapshot and restore
// ---------------------------------------------------------------------------

// Snapshot captures the whole store.
func (s *Store) Snapshot(ctx context.Context) (ev.StoreSnapshot, error) {
	if err := s.guard(ctx); err != nil {
		return ev.StoreSnapshot{}, err
	}
	recs, err := s.List(ctx, ev.Query{IncludeUnreadable: true})
	if err != nil {
		return ev.StoreSnapshot{}, err
	}
	return ev.StoreSnapshot{
		Schema:  ev.CurrentSchema,
		TakenAt: s.clock.Now(),
		Records: recs,
	}, nil
}

// Restore replaces the store's contents with a snapshot.
//
// One transaction. A restore that truncated and then failed partway would leave
// the store holding neither the old contents nor the new, which is the one
// outcome worse than either.
func (s *Store) Restore(ctx context.Context, snap ev.StoreSnapshot) error {
	if err := s.guard(ctx); err != nil {
		return err
	}
	for _, rec := range snap.Records {
		if err := validateForWrite(rec); err != nil {
			return err
		}
	}

	return s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM eval_records`); err != nil {
			return wrap("clearing store for restore", err)
		}
		for _, rec := range snap.Records {
			r := s.defaults(rec)
			// Restore must preserve holds exactly as captured, so it writes the
			// snapshot's value rather than going through Put's carry-forward.
			if _, err := tx.Exec(ctx, upsertSQL, upsertArgs(r)...); err != nil {
				return wrap("restoring "+r.Key().String(), err)
			}
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Internals
// ---------------------------------------------------------------------------

// maxAuditKeys bounds how many keys one audit entry lists, matching the
// reference implementation.
const maxAuditKeys = 64

// validateForWrite refuses a record this build must not store.
func validateForWrite(rec ev.Record) error {
	if rec.Schema > ev.CurrentSchema {
		// Decoding a payload under the wrong schema produces a WRONG baseline,
		// which fails silently and in the reassuring direction.
		return fmt.Errorf("%w: record %s is v%d, this build writes v%d",
			ev.ErrSchemaTooNew, rec.Key(), rec.Schema, ev.CurrentSchema)
	}
	return nil
}

// defaults fills the fields the port lets a caller omit.
func (s *Store) defaults(rec ev.Record) ev.Record {
	if rec.Schema == 0 {
		rec.Schema = ev.CurrentSchema
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = s.clock.Now()
	}
	return rec
}

// upsertSQL stores or replaces a record.
//
// The legal_hold clause is the subtle one: a replacement must NOT silently drop
// a hold placed on the existing row. Re-storing a run to correct its label would
// otherwise lift a legal hold as a side effect, which is a compliance failure
// caused by an unrelated edit.
const upsertSQL = `
INSERT INTO eval_records
	(kind, id, schema_ver, scenario, subject, suite, label,
	 created_at, expires_at, legal_hold, payload)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (kind, id) DO UPDATE SET
	schema_ver = EXCLUDED.schema_ver,
	scenario   = EXCLUDED.scenario,
	subject    = EXCLUDED.subject,
	suite      = EXCLUDED.suite,
	label      = EXCLUDED.label,
	created_at = EXCLUDED.created_at,
	expires_at = EXCLUDED.expires_at,
	legal_hold = eval_records.legal_hold OR EXCLUDED.legal_hold,
	payload    = EXCLUDED.payload`

func upsertArgs(rec ev.Record) []any {
	return []any{
		string(rec.Kind), rec.ID, int(rec.Schema),
		string(rec.Scenario), string(rec.Subject), string(rec.Suite), rec.Label,
		rec.CreatedAt, nullableTime(rec.ExpiresAt), rec.LegalHold, rec.Payload,
	}
}

const selectColumns = `SELECT kind, id, schema_ver, scenario, subject, suite,
	label, created_at, expires_at, legal_hold, payload`

// scanner is satisfied by both pgx.Row and pgx.Rows.
type scanner interface{ Scan(dest ...any) error }

func scanRecord(sc scanner) (ev.Record, error) {
	var (
		rec       ev.Record
		kind      string
		scenario  string
		subject   string
		suite     string
		schemaVer int
		expires   *time.Time
	)
	if err := sc.Scan(&kind, &rec.ID, &schemaVer, &scenario, &subject, &suite,
		&rec.Label, &rec.CreatedAt, &expires, &rec.LegalHold, &rec.Payload); err != nil {
		return ev.Record{}, err
	}
	rec.Kind = ev.RecordKind(kind)
	rec.Scenario = ev.ScenarioID(scenario)
	rec.Subject = ev.SubjectName(subject)
	rec.Suite = ev.SuiteID(suite)
	rec.Schema = ev.SchemaVersion(schemaVer)
	if expires != nil {
		rec.ExpiresAt = *expires
	}
	return rec, nil
}

// nullableTime maps Go's zero time to SQL NULL.
//
// "Never expires" must not be storable as a timestamp, or an expiry comparison
// could one day match it.
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// buildQuery renders a Query as SQL.
//
// Parameterised throughout. The Query type is small on purpose — "a rich query
// language in a storage port becomes a second, worse SQL" — and this function
// is correspondingly small.
func buildQuery(q ev.Query, count bool) (string, []any) {
	var (
		sb    strings.Builder
		args  []any
		where []string
	)
	if count {
		sb.WriteString(`SELECT count(*) FROM eval_records`)
	} else {
		sb.WriteString(selectColumns)
		sb.WriteString(` FROM eval_records`)
	}

	add := func(clause string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}

	if len(q.Kinds) > 0 {
		kinds := make([]string, 0, len(q.Kinds))
		for _, k := range q.Kinds {
			kinds = append(kinds, string(k))
		}
		add(`kind = ANY($%d)`, kinds)
	}
	if q.Scenario != "" {
		add(`scenario = $%d`, string(q.Scenario))
	}
	if q.Subject != "" {
		add(`subject = $%d`, string(q.Subject))
	}
	if q.Suite != "" {
		add(`suite = $%d`, string(q.Suite))
	}
	if !q.Since.IsZero() {
		add(`created_at >= $%d`, q.Since)
	}
	if !q.Until.IsZero() {
		// Until is exclusive, matching Query.matches: !CreatedAt.Before(Until).
		add(`created_at < $%d`, q.Until)
	}
	if !q.IncludeUnreadable {
		args = append(args, int(ev.MinReadableSchema), int(ev.CurrentSchema))
		where = append(where, fmt.Sprintf(`schema_ver BETWEEN $%d AND $%d`,
			len(args)-1, len(args)))
	}

	if len(where) > 0 {
		sb.WriteString(" WHERE ")
		sb.WriteString(strings.Join(where, " AND "))
	}
	if !count {
		// Newest first, then a total order so two reads of an unchanged store
		// return an identical sequence.
		sb.WriteString(` ORDER BY created_at DESC, kind ASC, id ASC`)
		if q.Limit > 0 {
			args = append(args, q.Limit)
			sb.WriteString(fmt.Sprintf(` LIMIT $%d`, len(args)))
		}
	}
	return sb.String(), args
}

// inTx runs fn inside a transaction, rolling back on any error.
func (s *Store) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return wrap("beginning transaction", err)
	}
	// Safe on every path: Rollback after a successful Commit is a no-op.
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return wrap("committing transaction", err)
	}
	return nil
}

// auditTx writes an audit record inside the caller's transaction.
//
// The encoding matches the reference implementation's byte-for-byte, so an
// audit entry written here decodes with the same reader.
func (s *Store) auditTx(ctx context.Context, tx pgx.Tx, action ev.AuditAction,
	keys []ev.RecordKey, count int, policy, by, reason string,
) error {
	s.mu.Lock()
	s.nextID++
	id := fmt.Sprintf("aud_%d", s.nextID)
	s.mu.Unlock()

	var b strings.Builder
	fmt.Fprintf(&b, "action=%s\ncount=%d\n", action, count)
	if policy != "" {
		fmt.Fprintf(&b, "policy=%s\n", policy)
	}
	if by != "" {
		fmt.Fprintf(&b, "by=%s\n", by)
	}
	if reason != "" {
		fmt.Fprintf(&b, "reason=%s\n", strings.ReplaceAll(reason, "\n", " "))
	}
	for _, k := range keys {
		fmt.Fprintf(&b, "key=%s\n", k)
	}

	rec := ev.Record{
		Schema:    ev.CurrentSchema,
		Kind:      ev.RecordAudit,
		ID:        id,
		CreatedAt: s.clock.Now(),
		// No expiry: the audit schedule is indefinite, and stamping one here
		// would let a schedule change expire the evidence.
		Payload: []byte(b.String()),
	}
	if _, err := tx.Exec(ctx, upsertSQL, upsertArgs(rec)...); err != nil {
		return wrap("writing audit entry", err)
	}
	return nil
}

// wrap adds context to a driver error without inventing a typed one.
//
// A driver error is NOT translated into a port error: reporting a connection
// failure as ErrRecordNotFound would make an outage look like an empty store,
// which is the single most dangerous confusion this layer could introduce.
func wrap(what string, err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return fmt.Errorf("evalstore: %s: %s (SQLSTATE %s): %w",
			what, pgErr.Message, pgErr.Code, err)
	}
	return fmt.Errorf("evalstore: %s: %w", what, err)
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
