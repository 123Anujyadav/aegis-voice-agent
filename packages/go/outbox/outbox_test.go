package outbox

import (
	"context"
	"errors"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/eventbus"
)

// fakeStore records the outcome calls ProcessBatch makes. Each fail* map makes
// that call fail for the given entry ID.
type fakeStore struct {
	pending       []*OutboxEntry
	fetchErr      error
	failMarkFail  map[string]error
	failMarkPub   map[string]error
	markedFailed  map[string]string
	markedPublish []string
}

func (s *fakeStore) Save(context.Context, *OutboxEntry) error { return nil }

func (s *fakeStore) FetchPending(context.Context, int) ([]*OutboxEntry, error) {
	return s.pending, s.fetchErr
}

func (s *fakeStore) MarkPublished(_ context.Context, id string) error {
	if err := s.failMarkPub[id]; err != nil {
		return err
	}
	s.markedPublish = append(s.markedPublish, id)
	return nil
}

func (s *fakeStore) MarkFailed(_ context.Context, id, msg string) error {
	if err := s.failMarkFail[id]; err != nil {
		return err
	}
	if s.markedFailed == nil {
		s.markedFailed = map[string]string{}
	}
	s.markedFailed[id] = msg
	return nil
}

// fakePublisher fails Publish for the correlation IDs in fail.
type fakePublisher struct {
	fail      map[string]error
	published []string
}

func (p *fakePublisher) Publish(_ context.Context, _ eventbus.Topic, key string, _ []byte) error {
	if err := p.fail[key]; err != nil {
		return err
	}
	p.published = append(p.published, key)
	return nil
}

func entries(ids ...string) []*OutboxEntry {
	out := make([]*OutboxEntry, 0, len(ids))
	for _, id := range ids {
		out = append(out, &OutboxEntry{ID: id, CorrelationID: id, Status: StatusPending})
	}
	return out
}

func TestProcessBatch_AllPublished(t *testing.T) {
	store := &fakeStore{pending: entries("a", "b")}
	n, err := NewOutboxProcessor(store, &fakePublisher{}).ProcessBatch(context.Background(), 10)
	if err != nil || n != 2 {
		t.Fatalf("ProcessBatch = (%d, %v), want (2, nil)", n, err)
	}
	if len(store.markedPublish) != 2 {
		t.Fatalf("marked published %v, want both", store.markedPublish)
	}
}

func TestProcessBatch_PublishFailureMarkedFailedIsNotAnError(t *testing.T) {
	pubErr := errors.New("broker unavailable")
	store := &fakeStore{pending: entries("a", "b")}
	pub := &fakePublisher{fail: map[string]error{"a": pubErr}}

	n, err := NewOutboxProcessor(store, pub).ProcessBatch(context.Background(), 10)
	if err != nil || n != 1 {
		t.Fatalf("ProcessBatch = (%d, %v), want (1, nil)", n, err)
	}
	if store.markedFailed["a"] != pubErr.Error() {
		t.Fatalf("entry a marked failed with %q, want the publish error", store.markedFailed["a"])
	}
}

// The case errcheck flagged: the entry could not be marked failed, so it stays
// pending. That used to be silent; it must now reach the caller, with both
// causes, while the rest of the batch is still processed.
func TestProcessBatch_MarkFailedFailureIsReportedWithItsCause(t *testing.T) {
	pubErr := errors.New("broker unavailable")
	markErr := errors.New("database connection lost")
	store := &fakeStore{pending: entries("a", "b", "c"), failMarkFail: map[string]error{"a": markErr}}
	pub := &fakePublisher{fail: map[string]error{"a": pubErr, "b": pubErr}}

	n, err := NewOutboxProcessor(store, pub).ProcessBatch(context.Background(), 10)
	if n != 1 {
		t.Fatalf("count = %d, want 1 (only c published)", n)
	}
	if err == nil {
		t.Fatal("a MarkFailed failure was not reported")
	}
	if !errors.Is(err, markErr) || !errors.Is(err, pubErr) {
		t.Fatalf("error %q must carry both the MarkFailed error and the publish error", err)
	}
	if _, marked := store.markedFailed["a"]; marked {
		t.Fatal("entry a should not be recorded as failed: its MarkFailed failed")
	}
	if store.markedFailed["b"] != pubErr.Error() {
		t.Fatal("entry b, whose MarkFailed succeeded, must still be marked failed")
	}
	if len(pub.published) != 1 || pub.published[0] != "c" {
		t.Fatalf("published %v, want [c]: processing must continue after a failure", pub.published)
	}
}

func TestProcessBatch_EveryMarkFailedFailureIsJoined(t *testing.T) {
	pubErr := errors.New("broker unavailable")
	errA, errB := errors.New("mark a"), errors.New("mark b")
	store := &fakeStore{pending: entries("a", "b"), failMarkFail: map[string]error{"a": errA, "b": errB}}
	pub := &fakePublisher{fail: map[string]error{"a": pubErr, "b": pubErr}}

	n, err := NewOutboxProcessor(store, pub).ProcessBatch(context.Background(), 10)
	if n != 0 || !errors.Is(err, errA) || !errors.Is(err, errB) {
		t.Fatalf("ProcessBatch = (%d, %v), want 0 and both MarkFailed errors", n, err)
	}
}

func TestProcessBatch_FetchFailureIsReturnedUnchanged(t *testing.T) {
	fetchErr := errors.New("query failed")
	n, err := NewOutboxProcessor(&fakeStore{fetchErr: fetchErr}, &fakePublisher{}).ProcessBatch(context.Background(), 10)
	if n != 0 || !errors.Is(err, fetchErr) {
		t.Fatalf("ProcessBatch = (%d, %v), want (0, wrapping the fetch error)", n, err)
	}
}

// Unchanged behaviour, pinned: a MarkPublished failure is neither counted nor
// reported.
func TestProcessBatch_MarkPublishedFailureIsNotCountedOrReported(t *testing.T) {
	store := &fakeStore{pending: entries("a", "b"), failMarkPub: map[string]error{"a": errors.New("update failed")}}
	n, err := NewOutboxProcessor(store, &fakePublisher{}).ProcessBatch(context.Background(), 10)
	if err != nil || n != 1 {
		t.Fatalf("ProcessBatch = (%d, %v), want (1, nil)", n, err)
	}
}
