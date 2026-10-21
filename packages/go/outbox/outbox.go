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
// When the pending entries cannot be fetched, it returns that error and
// publishes nothing. Otherwise every entry is attempted:
//   - When a publish fails, the entry is marked failed and processing
//     continues. If marking it failed ALSO fails, the entry stays pending (so
//     a later batch publishes it again), and that failure is reported in the
//     returned error, joined with the publish error that caused it. Several
//     such entries are joined into one error, so errors.Is finds each cause.
//   - An entry whose MarkPublished call fails is not counted and is not
//     reported; it stays pending and a later batch publishes it again.
//
// A non-nil error can therefore accompany a non-zero count. An entry that was
// marked failed is not retried by this processor: Store.FetchPending returns
// pending entries only, and nothing here moves a failed entry back.
func (p *OutboxProcessor) ProcessBatch(ctx context.Context, batchSize int) (int, error) {
	entries, err := p.store.FetchPending(ctx, batchSize)
	if err != nil {
		return 0, fmt.Errorf("outbox fetch pending: %w", err)
	}

	successCount := 0
	var unmarked []error
	for _, entry := range entries {
		err := p.publisher.Publish(ctx, entry.Topic, entry.CorrelationID, entry.Payload)
		if err != nil {
			if markErr := p.store.MarkFailed(ctx, entry.ID, err.Error()); markErr != nil {
				unmarked = append(unmarked, fmt.Errorf("outbox entry %s: publish failed and could not be marked failed: %w",
					entry.ID, errors.Join(err, markErr)))
			}
			continue
		}
		if err := p.store.MarkPublished(ctx, entry.ID); err == nil {
			successCount++
		}
	}

	return successCount, errors.Join(unmarked...)
}

// MarshalCloudEvent encodes event as JSON. It adds no CloudEvents attributes;
// the caller supplies the complete event.
func MarshalCloudEvent(event interface{}) ([]byte, error) {
	return json.Marshal(event)
}
