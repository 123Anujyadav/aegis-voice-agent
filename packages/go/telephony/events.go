package telephony

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// EventType classifies a telephony event.
type EventType string

// The event types the runtime publishes.
//
// The eight the brief enumerates, plus three the lifecycle produces and could
// not otherwise report: recovery started, recovery resumed, and recovery
// abandoned. A recovery that emitted nothing would leave the most operationally
// interesting thing the runtime does invisible to everything downstream.
const (
	EventCallCreated     EventType = "call_created"
	EventCallRinging     EventType = "call_ringing"
	EventCallAnswered    EventType = "call_answered"
	EventCallRejected    EventType = "call_rejected"
	EventCallConnected   EventType = "call_connected"
	EventCallTransferred EventType = "call_transferred"
	EventCallEnded       EventType = "call_ended"
	EventCallFailed      EventType = "call_failed"

	EventCallScreening EventType = "call_screening"
	EventCallEscalated EventType = "call_escalated"
	EventCallTimeout   EventType = "call_timeout"

	EventRecoveryStarted   EventType = "recovery_started"
	EventRecoveryResumed   EventType = "recovery_resumed"
	EventRecoveryAbandoned EventType = "recovery_abandoned"
)

// AllEventTypes returns every published type, in declaration order.
func AllEventTypes() []EventType {
	return []EventType{
		EventCallCreated, EventCallRinging, EventCallAnswered, EventCallRejected,
		EventCallConnected, EventCallTransferred, EventCallEnded, EventCallFailed,
		EventCallScreening, EventCallEscalated, EventCallTimeout,
		EventRecoveryStarted, EventRecoveryResumed, EventRecoveryAbandoned,
	}
}

// Topic returns the Kafka topic for an event type.
//
// The shape is fixed by packages/go/eventbus:
//
//	<domain>.<entity>.<event>.v<major>
//
// Lowercase, underscores inside a segment, hyphens prohibited because they
// collide with Prometheus metric-name normalisation. The version suffix is
// mandatory from the first topic — retrofitting versioning onto a live topic
// requires a dual-write migration across every consumer, and a telephony topic
// will have more consumers than any other.
func (t EventType) Topic() string { return "telephony.call." + string(t) + ".v1" }

// String implements fmt.Stringer.
func (t EventType) String() string { return string(t) }

// stateEvent maps an entered state to the event it publishes.
//
// A table rather than a switch at each call site, so "what does the platform
// see when a call is muted" is answered in one place. States with no entry
// publish nothing: Muted and Hold are deliberately silent, because a call that
// toggles hold twenty times would otherwise produce forty events describing
// nothing a downstream consumer acts on.
func stateEvent(s CallState) (EventType, bool) {
	switch s {
	case StateIncoming:
		return EventCallCreated, true
	case StateRinging:
		return EventCallRinging, true
	case StateScreening:
		return EventCallScreening, true
	case StateAccepted:
		return EventCallAnswered, true
	case StateRejected:
		return EventCallRejected, true
	case StateConnected:
		return EventCallConnected, true
	case StateTransferred:
		return EventCallTransferred, true
	case StateEscalated:
		return EventCallEscalated, true
	case StateTimeout:
		return EventCallTimeout, true
	case StateEnded:
		return EventCallEnded, true
	case StateFailed:
		return EventCallFailed, true
	default:
		return "", false
	}
}

// Event is one published telephony event.
//
// # It carries identifiers, states and codes — never content
//
// Frozen invariant I7, applied to the module with the most exposure to it. A
// telephony event stream is the record of every call the platform handled: who
// called whom, when, and for how long. Kafka cannot delete an individual
// record, so anything placed here is retained for as long as the topic is,
// regardless of what an erasure request later says.
//
// The test applied to this struct during design: if this topic were retained
// forever and could never be deleted, would that be a compliance failure? It
// must be no. There is deliberately no field capable of holding a phone number,
// a caller name, an audio reference or a transcript — which is also why
// [Endpoint] holds an opaque Ref rather than an E.164 number.
type Event struct {
	// Type is what happened.
	Type EventType

	// Call, Session and Correlation identify the call.
	Call        CallID
	Session     SessionID
	Correlation CorrelationID

	// From and To are the states this event describes. From is empty for events
	// that are not transitions.
	From CallState
	To   CallState

	// Reason is the bounded transition code.
	Reason string

	// Direction, Channel and Provider are the coarse call classification. All
	// three are enum-valued or authored identifiers, so all three are safe as
	// metric labels and none can carry content.
	Direction Direction
	Channel   Channel
	Provider  ProviderID

	// Tags are the call's coarse classification.
	Tags []string

	// DurationMillis and TalkMillis are the call's elapsed and media times.
	// Milliseconds as integers rather than time.Duration, so a consumer in
	// another language does not have to know Go's nanosecond representation.
	DurationMillis int64
	TalkMillis     int64

	// Sequence orders events within one call, so a consumer can detect a gap
	// or a reordering. Kafka orders within a partition and the partition key is
	// the call, but a consumer that fans out loses that guarantee and needs
	// this to recover it.
	Sequence int

	// At stamps the event.
	At time.Time
}

// PartitionKey returns the key that keeps a call's events ordered.
//
// The call identifier: every event for one call lands on one partition, so a
// consumer sees Created before Connected before Ended. Keying on anything
// coarser — the provider, say — puts a busy carrier's calls on one partition
// and makes it the bottleneck.
func (e Event) PartitionKey() string { return string(e.Call) }

// Summary renders the event.
func (e Event) Summary() string {
	s := fmt.Sprintf("%s call=%s", e.Type, e.Call)
	if e.From != "" {
		s += fmt.Sprintf(" %s->%s", e.From, e.To)
	}
	if e.Reason != "" {
		s += " (" + e.Reason + ")"
	}
	return s
}

// Publisher is the event port.
//
// NOTHING IN THIS PACKAGE IMPLEMENTS IT AGAINST KAFKA. A Kafka adapter lives in
// packages/go/eventbus and a service wires it in. The runtime depends on this
// interface, which is what lets the entire lifecycle be tested with no broker.
//
// Implementations must be safe for concurrent use and must not block
// indefinitely. A publisher that blocks holds a lifecycle transition, and a
// lifecycle transition that blocks holds a call.
type Publisher interface {
	// Publish emits an event. An error is returned to the caller but MUST NOT
	// fail the transition that produced it — see [CallLifecycle] on why a
	// broker outage cannot be allowed to stop calls from ending.
	Publish(ctx context.Context, e Event) error
}

// RecordingPublisher captures events in memory.
//
// For tests and for a deployment running without a broker. Bounded, because an
// unbounded recorder in a long-running process is a memory leak that presents
// as a slow crash three days later.
type RecordingPublisher struct {
	mu     sync.RWMutex
	events []Event
	max    int
	// dropped counts events discarded after the bound was reached, so a test
	// asserting on event counts cannot be silently misled by truncation.
	dropped int
}

// NewRecordingPublisher builds a recorder holding the most recent 4,096 events.
func NewRecordingPublisher() *RecordingPublisher {
	return &RecordingPublisher{max: 4096}
}

// NewBoundedRecordingPublisher builds a recorder with an explicit bound.
func NewBoundedRecordingPublisher(max int) *RecordingPublisher {
	if max <= 0 {
		max = 4096
	}
	return &RecordingPublisher{max: max}
}

// Publish records the event.
func (p *RecordingPublisher) Publish(_ context.Context, e Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.events) >= p.max {
		p.events = p.events[1:]
		p.dropped++
	}
	p.events = append(p.events, e)
	return nil
}

// Events returns a copy of the recorded events, oldest first.
func (p *RecordingPublisher) Events() []Event {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]Event(nil), p.events...)
}

// OfType returns recorded events of one type.
func (p *RecordingPublisher) OfType(t EventType) []Event {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []Event
	for _, e := range p.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// ForCall returns recorded events for one call, in sequence order.
func (p *RecordingPublisher) ForCall(id CallID) []Event {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []Event
	for _, e := range p.events {
		if e.Call == id {
			out = append(out, e)
		}
	}
	return out
}

// Len returns how many events are held.
func (p *RecordingPublisher) Len() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.events)
}

// Dropped returns how many events were discarded to stay within the bound.
func (p *RecordingPublisher) Dropped() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.dropped
}

// Reset clears the recorder.
func (p *RecordingPublisher) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = nil
	p.dropped = 0
}

// NopPublisher discards every event.
//
// Named so that choosing it is visible in a configuration review. A nil
// Publisher would be the same behaviour by accident, and the runtime refuses
// one for exactly that reason.
type NopPublisher struct{}

// Publish discards.
func (NopPublisher) Publish(context.Context, Event) error { return nil }

// FailingPublisher returns an error for every event.
//
// Exported because failure injection needs it and because the property it
// verifies is important: a broker outage must not stop calls from ending.
type FailingPublisher struct {
	Err error
	// Count records how many publishes were attempted, so a test can prove the
	// runtime kept trying rather than silently stopping.
	Count int
	mu    sync.Mutex
}

// Publish returns the configured error.
func (f *FailingPublisher) Publish(context.Context, Event) error {
	f.mu.Lock()
	f.Count++
	f.mu.Unlock()
	if f.Err != nil {
		return f.Err
	}
	return fmt.Errorf("telephony: publisher unavailable")
}

// Attempts returns how many publishes were attempted.
func (f *FailingPublisher) Attempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Count
}
