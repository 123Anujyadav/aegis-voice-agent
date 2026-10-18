// Package outbox implements the transactional-outbox pattern: entries are
// stored first, then published to the event bus by an OutboxProcessor and
// marked published or failed.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/eventbus"
)

var (
	// ErrOutboxClosed reports that the outbox storage has been closed.
	ErrOutboxClosed = errors.New("outbox: storage is closed")
	// ErrInvalidMessage reports an entry with an invalid payload or topic.
	ErrInvalidMessage = errors.New("outbox: invalid payload or topic")
)

// Status is an outbox entry's publication state.
type Status string

// The publication states an entry moves through.
const (
	StatusPending   Status = "PENDING"
	StatusPublished Status = "PUBLISHED"
	StatusFailed    Status = "FAILED"
)

// OutboxEntry is one event awaiting, or having completed, publication.
type OutboxEntry struct {
	ID            string         `json:"id"`
	Topic         eventbus.Topic `json:"topic"`
	CorrelationID string         `json:"correlationId"`
	Payload       []byte         `json:"payload"`
	Status        Status         `json:"status"`
	RetryCount    int            `json:"retryCount"`
	CreatedAt     time.Time      `json:"createdAt"`
	ProcessedAt   *time.Time     `json:"processedAt,omitempty"`
}

// Store persists outbox entries and records the outcome of publishing them.
type Store interface {
	Save(ctx context.Context, entry *OutboxEntry) error
	FetchPending(ctx context.Context, limit int) ([]*OutboxEntry, error)
	MarkPublished(ctx context.Context, id string) error
	MarkFailed(ctx context.Context, id string, errMessage string) error
}

// Publisher delivers an entry's payload to the event bus.
type Publisher interface {
	Publish(ctx context.Context, topic eventbus.Topic, key string, payload []byte) error
}

// OutboxProcessor moves pending entries from a Store to a Publisher.
type OutboxProcessor struct {
	store     Store
	publisher Publisher
}

// NewOutboxProcessor returns a processor that reads from store and publishes
// through publisher.
func NewOutboxProcessor(store Store, publisher Publisher) *OutboxProcessor {
	return &OutboxProcessor{
		store:     store,
		publisher: publisher,
	}
}

// ProcessBatch publishes up to batchSize pending entries, and returns how many
// were published and then marked published.
//
// It returns an error only when the pending entries cannot be fetched. When a
// publish fails, the entry is marked failed, any error from MarkFailed is
// discarded, and processing continues with the next entry. An entry whose
// MarkPublished call fails is not counted.
func (p *OutboxProcessor) ProcessBatch(ctx context.Context, batchSize int) (int, error) {
	entries, err := p.store.FetchPending(ctx, batchSize)
	if err != nil {
		return 0, fmt.Errorf("outbox fetch pending: %w", err)
	}

	successCount := 0
	for _, entry := range entries {
		err := p.publisher.Publish(ctx, entry.Topic, entry.CorrelationID, entry.Payload)
		if err != nil {
			_ = p.store.MarkFailed(ctx, entry.ID, err.Error())
			continue
		}
		if err := p.store.MarkPublished(ctx, entry.ID); err == nil {
			successCount++
		}
	}

	return successCount, nil
}

// MarshalCloudEvent encodes event as JSON. It adds no CloudEvents attributes;
// the caller supplies the complete event.
func MarshalCloudEvent(event interface{}) ([]byte, error) {
	return json.Marshal(event)
}
