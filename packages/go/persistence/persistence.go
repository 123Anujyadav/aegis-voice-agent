package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/outbox"
)

var (
	ErrTxAlreadyEnded = errors.New("persistence: transaction already committed or rolled back")
	ErrDuplicateKey   = errors.New("persistence: duplicate key violation")
)

type UnitOfWork interface {
	Execute(ctx context.Context, fn func(tx *sql.Tx) error) error
}

type SqlUnitOfWork struct {
	db *sql.DB
}

func NewSqlUnitOfWork(db *sql.DB) *SqlUnitOfWork {
	return &SqlUnitOfWork{db: db}
}

func (u *SqlUnitOfWork) Execute(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := u.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

type PostgresOutboxStore struct {
	db *sql.DB
}

func NewPostgresOutboxStore(db *sql.DB) *PostgresOutboxStore {
	return &PostgresOutboxStore{db: db}
}

func (p *PostgresOutboxStore) Save(ctx context.Context, entry *outbox.OutboxEntry) error {
	query := `INSERT INTO outbox_events (id, topic, correlation_id, payload, status, created_at)
	          VALUES ($1, $2, $3, $4, $5, $6)`
	_, err := p.db.ExecContext(ctx, query, entry.ID, entry.Topic.String(), entry.CorrelationID, entry.Payload, entry.Status, entry.CreatedAt)
	return err
}

func (p *PostgresOutboxStore) FetchPending(ctx context.Context, limit int) ([]*outbox.OutboxEntry, error) {
	query := `SELECT id, topic, correlation_id, payload, status, retry_count, created_at
	          FROM outbox_events WHERE status = 'PENDING' ORDER BY created_at ASC LIMIT $1`
	rows, err := p.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*outbox.OutboxEntry
	for rows.Next() {
		var e outbox.OutboxEntry
		var topicStr string
		if err := rows.Scan(&e.ID, &topicStr, &e.CorrelationID, &e.Payload, &e.Status, &e.RetryCount, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, &e)
	}
	return entries, nil
}

func (p *PostgresOutboxStore) MarkPublished(ctx context.Context, id string) error {
	now := time.Now().UTC()
	query := `UPDATE outbox_events SET status = 'PUBLISHED', processed_at = $1 WHERE id = $2`
	_, err := p.db.ExecContext(ctx, query, now, id)
	return err
}

func (p *PostgresOutboxStore) MarkFailed(ctx context.Context, id string, errMessage string) error {
	query := `UPDATE outbox_events SET status = 'FAILED', retry_count = retry_count + 1, error_message = $1 WHERE id = $2`
	_, err := p.db.ExecContext(ctx, query, errMessage, id)
	return err
}

type InboxDeduplicator struct {
	db *sql.DB
}

func NewInboxDeduplicator(db *sql.DB) *InboxDeduplicator {
	return &InboxDeduplicator{db: db}
}

func (i *InboxDeduplicator) HasProcessed(ctx context.Context, messageID, consumerGroup string) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM inbox_messages WHERE message_id = $1 AND consumer_group = $2)`
	var exists bool
	err := i.db.QueryRowContext(ctx, query, messageID, consumerGroup).Scan(&exists)
	return exists, err
}

func (i *InboxDeduplicator) MarkProcessed(ctx context.Context, messageID, consumerGroup string) error {
	query := `INSERT INTO inbox_messages (message_id, consumer_group) VALUES ($1, $2) ON CONFLICT DO NOTHING`
	_, err := i.db.ExecContext(ctx, query, messageID, consumerGroup)
	return err
}
