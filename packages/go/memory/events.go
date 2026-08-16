package memory

import (
	"sync"
	"time"
)

// EventType classifies a memory event.
type EventType string

// The eight event types the engine publishes.
const (
	EventCreated  EventType = "created"
	EventUpdated  EventType = "updated"
	EventDeleted  EventType = "deleted"
	EventExpired  EventType = "expired"
	EventPromoted EventType = "promoted"
	EventDemoted  EventType = "demoted"
	EventMerged   EventType = "merged"
	EventArchived EventType = "archived"
)

// Topic returns the Kafka topic name for an event type.
//
// The shape is fixed by packages/go/eventbus and validated there:
//
//	<domain>.<entity>.<event>.v<major>
//
// Lowercase, underscores permitted inside a segment, hyphens prohibited
// (they collide with Prometheus metric-name normalisation). The version suffix
// is mandatory from the first topic, because retrofitting versioning onto a
// live topic requires a dual-write migration across every consumer.
func (t EventType) Topic() string { return "memory.record." + string(t) + ".v1" }

// Event is one published memory event.
//
// IT CARRIES IDENTIFIERS, NOT CONTENT.
//
// Frozen invariant I7: Kafka cannot delete an individual record, so erasure
// depends on events carrying references rather than payloads, and it cannot be
// retrofitted. There is deliberately no field here that could hold a memory's
// value — a consumer that needs content fetches it from this engine, subject to
// this engine's access rules.
//
// The test applied to this struct: if the topic were retained forever and could
// never be deleted, would that be a compliance failure? It must be no.
type Event struct {
	// Type classifies the event.
	Type EventType

	// Key identifies the record. Subject, kind and name — no payload.
	Key Key

	// Tier is the record's tier after the event.
	Tier Tier

	// PreviousTier is populated on promotion and demotion.
	PreviousTier Tier

	// Version is the record's version after the event.
	Version Version

	// Sensitivity classifies the record, so a consumer can apply its own
	// handling without fetching.
	Sensitivity Sensitivity

	// Retention names the record's deletion class.
	Retention Retention

	// Reason is a short machine-readable code. Never free text, never caller
	// content — it becomes a metric label.
	Reason string

	// SizeBytes is the payload size, for capacity accounting. A size is not
	// content: it leaks nothing beyond magnitude, and magnitude is what a
	// capacity consumer needs.
	SizeBytes int

	// At is the event instant on the engine's clock.
	At time.Time

	// Sequence orders events from one runtime. Monotonic per runtime, which is
	// enough for a consumer to detect a gap without a global ordering service.
	Sequence uint64
}

// Publisher receives memory events.
//
// The interface is narrow and synchronous by contract, so this engine has no
// Kafka dependency: an adapter in a sibling module implements it over the real
// broker and the engine keeps its zero-external-dependency property.
//
// Implementations must not block. The engine calls Publish while holding no
// lock, but a publisher that blocks stalls the operation that produced the
// event — and on the store path that is a stalled memory write.
type Publisher interface {
	// Publish delivers one event. A returned error is counted and discarded:
	// a memory write must not fail because its notification could not be sent.
	Publish(Event) error
}

// NoopPublisher discards every event.
//
// The default, so the engine runs with no broker configured. An engine that
// required Kafka to start could not be unit-tested, and the whole suite would
// need infrastructure to prove a map lookup works.
type NoopPublisher struct{}

// Publish discards the event.
func (NoopPublisher) Publish(Event) error { return nil }

// RecordingPublisher captures events for assertion. Exported because services
// embedding this engine need it to test their own reactions to memory events.
type RecordingPublisher struct {
	mu     sync.Mutex
	events []Event
	err    error
}

// NewRecordingPublisher returns a publisher that records what it receives.
func NewRecordingPublisher() *RecordingPublisher { return &RecordingPublisher{} }

// FailWith makes Publish return an error, for failure-injection tests.
func (p *RecordingPublisher) FailWith(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

// Publish records the event.
func (p *RecordingPublisher) Publish(e Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.events = append(p.events, e)
	return nil
}

// Events returns a copy of everything recorded.
func (p *RecordingPublisher) Events() []Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Event, len(p.events))
	copy(out, p.events)
	return out
}

// CountOf returns how many events of a type were recorded.
func (p *RecordingPublisher) CountOf(t EventType) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, e := range p.events {
		if e.Type == t {
			n++
		}
	}
	return n
}

// Reset clears recorded events.
func (p *RecordingPublisher) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = nil
}

// Dispatcher fans one event out to several publishers.
//
// This is the MemoryDispatcher of the brief. It exists so a deployment can send
// events to Kafka, to an in-process projection and to an audit sink without the
// store knowing there is more than one destination.
//
// A failing publisher is counted and skipped, never propagated. One broken
// subscriber must not be able to fail a memory write for everyone else — the
// same reasoning the Phase 10A dispatcher applies to a slow sink.
type Dispatcher struct {
	metrics *Metrics

	mu         sync.RWMutex
	publishers []Publisher
	sequence   uint64
}

// NewDispatcher constructs a dispatcher.
func NewDispatcher(metrics *Metrics, publishers ...Publisher) *Dispatcher {
	if metrics == nil {
		metrics = NewMetrics()
	}
	return &Dispatcher{metrics: metrics, publishers: publishers}
}

// Add registers a publisher.
func (d *Dispatcher) Add(p Publisher) {
	if p == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.publishers = append(d.publishers, p)
}

// Dispatch stamps and delivers an event.
func (d *Dispatcher) Dispatch(e Event) {
	d.mu.Lock()
	d.sequence++
	e.Sequence = d.sequence
	publishers := make([]Publisher, len(d.publishers))
	copy(publishers, d.publishers)
	d.mu.Unlock()

	if len(publishers) == 0 {
		return
	}
	for _, p := range publishers {
		if err := p.Publish(e); err != nil {
			d.metrics.EventsDropped.Inc("publisher_error")
			continue
		}
		d.metrics.EventsPublished.Inc(string(e.Type))
	}
}

// Sequence returns the last sequence number issued.
func (d *Dispatcher) Sequence() uint64 {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.sequence
}
