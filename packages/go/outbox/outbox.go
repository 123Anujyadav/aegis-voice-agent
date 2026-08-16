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
	ErrOutboxClosed  = errors.New("outbox: storage is closed")
	ErrInvalidMessage = errors.New("outbox: invalid payload or topic")
)

type Status string

const (
	StatusPending   Status = "PENDING"
	StatusPublished Status = "PUBLISHED"
	StatusFailed    Status = "FAILED"
)

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

type Store interface {
	Save(ctx context.Context, entry *OutboxEntry) error
	FetchPending(ctx context.Context, limit int) ([]*OutboxEntry, error)
	MarkPublished(ctx context.Context, id string) error
	MarkFailed(ctx context.Context, id string, errMessage string) error
}

type Publisher interface {
	Publish(ctx context.Context, topic eventbus.Topic, key string, payload []byte) error
}

type OutboxProcessor struct {
	store     Store
	publisher Publisher
}

func NewOutboxProcessor(store Store, publisher Publisher) *OutboxProcessor {
	return &OutboxProcessor{
		store:     store,
		publisher: publisher,
	}
}

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

func MarshalCloudEvent(event interface{}) ([]byte, error) {
	return json.Marshal(event)
}
