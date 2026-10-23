// Package persistence provides PostgreSQL-backed implementations of the
// unit-of-work, transactional-outbox and inbox-deduplication contracts.
package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/outbox"
)

// Errors returned by this package.
var (
	// ErrTxAlreadyEnded reports use of a transaction after it was committed or
	// rolled back.
	ErrTxAlreadyEnded = errors.New("persistence: transaction already committed or rolled back")
	// ErrDuplicateKey reports a unique-constraint violation.
	ErrDuplicateKey = errors.New("persistence: duplicate key violation")
)

// UnitOfWork runs a function inside a single database transaction.
type UnitOfWork interface {
	Execute(ctx context.Context, fn func(tx *sql.Tx) error) error
}

// SQLUnitOfWork is a UnitOfWork over a database/sql connection pool, using
// read-committed isolation.
type SQLUnitOfWork struct {
	db *sql.DB
}

// NewSQLUnitOfWork returns a SQLUnitOfWork that opens transactions on db.
func NewSQLUnitOfWork(db *sql.DB) *SQLUnitOfWork {
	return &SQLUnitOfWork{db: db}
}

// Execute runs fn in a new transaction and commits it if fn returns nil. If fn
// returns an error the transaction is rolled back and the error returned,
// joined with any rollback failure. If fn panics the transaction is rolled
// back and the panic re-raised.
func (u *SQLUnitOfWork) Execute(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := u.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback() //nolint:errcheck // the re-raised panic is the failure being reported; a rollback error has no channel to the caller
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return errors.Join(err, fmt.Errorf("rollback transaction: %w", rbErr))
		}
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// PostgresOutboxStore stores outbox entries in the outbox_events table.
type PostgresOutboxStore struct {
	db *sql.DB
}

// NewPostgresOutboxStore returns a PostgresOutboxStore backed by db.
func NewPostgresOutboxStore(db *sql.DB) *PostgresOutboxStore {
	return &PostgresOutboxStore{db: db}
}

// Save inserts entry into the outbox.
func (p *PostgresOutboxStore) Save(ctx context.Context, entry *outbox.OutboxEntry) error {
	query := `INSERT INTO outbox_events (id, topic, correlation_id, payload, status, created_at)
	          VALUES ($1, $2, $3, $4, $5, $6)`
	_, err := p.db.ExecContext(ctx, query, entry.ID, entry.Topic.String(), entry.CorrelationID, entry.Payload, entry.Status, entry.CreatedAt)
	return err
}

// FetchPending returns up to limit PENDING entries, oldest first. The topic
// column is read but not mapped back onto the returned entries.
func (p *PostgresOutboxStore) FetchPending(ctx context.Context, limit int) (entries []*outbox.OutboxEntry, err error) {
	query := `SELECT id, topic, correlation_id, payload, status, retry_count, created_at
	          FROM outbox_events WHERE status = 'PENDING' ORDER BY created_at ASC LIMIT $1`
	rows, err := p.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			entries, err = nil, cerr
		}
	}()

	for rows.Next() {
		var e outbox.OutboxEntry
		var topicStr string
		if scanErr := rows.Scan(&e.ID, &topicStr, &e.CorrelationID, &e.Payload, &e.Status, &e.RetryCount, &e.CreatedAt); scanErr != nil {
			return nil, scanErr
		}
		entries = append(entries, &e)
	}
	// Next returns false on a failed read as well as at the end of the result
	// set; only Err tells them apart.
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	return entries, nil
}

// MarkPublished marks the entry id as published, recording the time.
func (p *PostgresOutboxStore) MarkPublished(ctx context.Context, id string) error {
	now := time.Now().UTC()
	query := `UPDATE outbox_events SET status = 'PUBLISHED', processed_at = $1 WHERE id = $2`
	_, err := p.db.ExecContext(ctx, query, now, id)
	return err
}

// MarkFailed marks the entry id as failed, incrementing its retry count and
// recording errMessage.
func (p *PostgresOutboxStore) MarkFailed(ctx context.Context, id, errMessage string) error {
	query := `UPDATE outbox_events SET status = 'FAILED', retry_count = retry_count + 1, error_message = $1 WHERE id = $2`
	_, err := p.db.ExecContext(ctx, query, errMessage, id)
	return err
}

// InboxDeduplicator records which messages each consumer group has processed,
// in the inbox_messages table.
type InboxDeduplicator struct {
	db *sql.DB
}

// NewInboxDeduplicator returns an InboxDeduplicator backed by db.
func NewInboxDeduplicator(db *sql.DB) *InboxDeduplicator {
	return &InboxDeduplicator{db: db}
}

// HasProcessed reports whether consumerGroup has recorded messageID.
func (i *InboxDeduplicator) HasProcessed(ctx context.Context, messageID, consumerGroup string) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM inbox_messages WHERE message_id = $1 AND consumer_group = $2)`
	var exists bool
	err := i.db.QueryRowContext(ctx, query, messageID, consumerGroup).Scan(&exists)
	return exists, err
}

// MarkProcessed records messageID as processed by consumerGroup. Recording an
// already-recorded message is not an error.
func (i *InboxDeduplicator) MarkProcessed(ctx context.Context, messageID, consumerGroup string) error {
	query := `INSERT INTO inbox_messages (message_id, consumer_group) VALUES ($1, $2) ON CONFLICT DO NOTHING`
	_, err := i.db.ExecContext(ctx, query, messageID, consumerGroup)
	return err
}
