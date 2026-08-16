package toolruntime

import (
	"sync"
	"sync/atomic"
	"time"
)

// EventType classifies an execution event.
type EventType string

// The eight event types the runtime publishes, exactly as the Phase 10D brief
// enumerates them.
const (
	EventStarted          EventType = "started"
	EventCompleted        EventType = "completed"
	EventFailed           EventType = "failed"
	EventCancelled        EventType = "cancelled"
	EventTimedOut         EventType = "timed_out"
	EventRetried          EventType = "retried"
	EventRolledBack       EventType = "rolled_back"
	EventPermissionDenied EventType = "permission_denied"
)

// Topic returns the Kafka topic for an event type.
//
// The shape is fixed by packages/go/eventbus and validated there:
//
//	<domain>.<entity>.<event>.v<major>
//
// Lowercase, underscores inside a segment, hyphens prohibited because they
// collide with Prometheus metric-name normalisation. The version suffix is
// mandatory from the first topic: retrofitting versioning onto a live topic
// requires a dual-write migration across every consumer.
func (t EventType) Topic() string { return "tool.execution." + string(t) + ".v1" }

// AllEventTypes returns every published type, in declaration order.
func AllEventTypes() []EventType {
	return []EventType{EventStarted, EventCompleted, EventFailed, EventCancelled,
		EventTimedOut, EventRetried, EventRolledBack, EventPermissionDenied}
}

// Event is one published execution event.
//
// IT CARRIES IDENTIFIERS AND FINGERPRINTS, NOT PAYLOADS.
//
// This is frozen invariant I7 applied to a second module, and the reasoning is
// unchanged: Kafka cannot delete an individual record, so an erasure right that
// depends on deleting from a topic is not an erasure right. There is
// deliberately no field here capable of holding a tool's arguments or results.
//
// The test applied to this struct during design: if this topic were retained
// forever and could never be deleted, would that be a compliance failure? It
// must be no.
//
// It is also the mechanism behind the Phase 10D architectural rule that the
// tool runtime never writes memory. Memory learns what happened by subscribing
// to these. A consumer needing the actual result asks the runtime for it,
// subject to the runtime's access rules — which a topic does not have.
type Event struct {
	// Type classifies the event.
	Type EventType

	// Execution, Step and Plan locate it.
	Execution ExecutionID
	Step      StepID
	Plan      PlanID

	// Intent is the originating intent.
	Intent IntentID

	// Correlation ties every event from one turn together.
	Correlation CorrelationID

	// Session identifies the conversation.
	Session SessionID

	// Actor is on whose behalf it ran.
	Actor ActorID

	// Descriptor is the tool and pinned version.
	Descriptor Descriptor

	// Capability is what was requested. Present as well as the descriptor,
	// because a consumer usually cares what was wanted rather than which
	// implementation served it.
	Capability CapabilityID

	// Effect classifies what the tool does, so a consumer can apply its own
	// handling without looking up a contract.
	Effect Effect

	// Attempt is 1-based.
	Attempt int

	// Phase names where the event occurred.
	Phase Phase

	// InputPrint and OutputPrint fingerprint arguments and result. A
	// fingerprint identifies content without revealing it — see ids.go.
	InputPrint  Fingerprint
	OutputPrint Fingerprint

	// Duration is the elapsed time, zero where meaningless.
	Duration time.Duration

	// Reason is a short machine-readable code. Never free text, never caller
	// content: it becomes a metric label.
	Reason string

	// Idempotent reports that this execution was served from the ledger rather
	// than by invoking the tool.
	Idempotent bool

	// OutputBytes is the result's estimated size. A size is not content: it
	// leaks nothing beyond magnitude, and magnitude is what a capacity
	// consumer needs.
	OutputBytes int

	// At is the event instant on the runtime clock.
	At time.Time

	// Sequence orders events from one runtime. Monotonic per runtime, which is
	// enough for a consumer to detect a gap without a global ordering service.
	Sequence uint64
}

// Publisher receives execution events.
//
// Narrow and synchronous by contract, so this module has no Kafka dependency:
// an adapter in a sibling module implements it over the real broker and the
// runtime keeps its zero-external-dependency property.
//
// Implementations must not block. The runtime publishes while holding no lock,
// but a publisher that blocks stalls the execution that produced the event.
type Publisher interface {
	// Publish delivers one event. A returned error is counted and discarded: a
	// tool execution must not fail because its notification could not be sent.
	Publish(Event) error
}

// NoopPublisher discards every event.
//
// The default, so the runtime runs with no broker configured. A runtime that
// required Kafka to start could not be unit-tested, and the whole suite would
// need infrastructure to prove that a plan orders its steps correctly.
type NoopPublisher struct{}

// Publish discards the event.
func (NoopPublisher) Publish(Event) error { return nil }

// RecordingPublisher captures events for assertion.
type RecordingPublisher struct {
	mu     sync.Mutex
	events []Event
	err    error
}

// NewRecordingPublisher builds a recording publisher.
func NewRecordingPublisher() *RecordingPublisher { return &RecordingPublisher{} }

// Publish records an event.
func (p *RecordingPublisher) Publish(e Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.events = append(p.events, e)
	return nil
}

// FailWith makes every subsequent Publish fail, for failure injection.
func (p *RecordingPublisher) FailWith(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

// Events returns a copy in publication order.
func (p *RecordingPublisher) Events() []Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Event(nil), p.events...)
}

// OfType returns events of one type.
func (p *RecordingPublisher) OfType(t EventType) []Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Event
	for _, e := range p.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// Count returns how many events of a type were published.
func (p *RecordingPublisher) Count(t EventType) int { return len(p.OfType(t)) }

// Len returns the total event count.
func (p *RecordingPublisher) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.events)
}

// Reset clears recorded events.
func (p *RecordingPublisher) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = nil
}

// EventDispatcher stamps, counts and publishes events.
//
// It owns the sequence number, which is why publication goes through it rather
// than through the publisher directly: a sequence assigned by each call site
// would be a sequence with gaps and duplicates, and its whole purpose is to let
// a consumer detect gaps.
type EventDispatcher struct {
	metrics   *Metrics
	publisher Publisher
	sequence  atomic.Uint64
	clock     func() time.Time
}

// NewEventDispatcher builds a dispatcher.
func NewEventDispatcher(m *Metrics, p Publisher, now func() time.Time) *EventDispatcher {
	if p == nil {
		p = NoopPublisher{}
	}
	return &EventDispatcher{metrics: m, publisher: p, clock: now}
}

// Dispatch stamps and publishes an event.
//
// A publisher failure is counted and swallowed. One broken subscriber must not
// fail tool executions for everybody else — but the loss IS visible, because a
// dropped event means a memory update that never happens, and a runtime that
// loses those silently produces an assistant that has quietly stopped learning.
func (d *EventDispatcher) Dispatch(e Event) {
	e.Sequence = d.sequence.Add(1)
	if e.At.IsZero() && d.clock != nil {
		e.At = d.clock()
	}
	if d.metrics != nil {
		d.metrics.EventsPublished.Inc(string(e.Type))
	}
	if err := d.publisher.Publish(e); err != nil && d.metrics != nil {
		d.metrics.EventsDropped.Inc(string(e.Type))
	}
}

// Sequence returns the last assigned sequence number.
func (d *EventDispatcher) Sequence() uint64 { return d.sequence.Load() }
