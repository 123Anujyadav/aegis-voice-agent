package media

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MediaEventType classifies a media streaming event.
type MediaEventType string

// The event types the media runtime publishes.
//
// Eight lifecycle events plus three recovery outcomes. Recovery is the most
// operationally interesting thing this engine does, and a recovery that emitted
// nothing would leave it invisible to everything downstream.
const (
	EventStreamCreated  MediaEventType = "stream_created"
	EventStreamStarted  MediaEventType = "stream_started"
	EventStreamPaused   MediaEventType = "stream_paused"
	EventStreamResumed  MediaEventType = "stream_resumed"
	EventStreamDraining MediaEventType = "stream_draining"
	EventStreamClosed   MediaEventType = "stream_closed"
	EventStreamFailed   MediaEventType = "stream_failed"
	EventStreamTimeout  MediaEventType = "stream_timeout"

	EventRecoveryStarted   MediaEventType = "recovery_started"
	EventRecoveryResumed   MediaEventType = "recovery_resumed"
	EventRecoveryAbandoned MediaEventType = "recovery_abandoned"
)

// AllMediaEventTypes returns every published type, in declaration order.
func AllMediaEventTypes() []MediaEventType {
	return []MediaEventType{
		EventStreamCreated, EventStreamStarted, EventStreamPaused,
		EventStreamResumed, EventStreamDraining, EventStreamClosed,
		EventStreamFailed, EventStreamTimeout,
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
// requires a dual-write migration across every consumer, and at fifty frames a
// second per stream this is the highest-volume topic family in the platform.
func (t MediaEventType) Topic() string { return "media.stream." + string(t) + ".v1" }

// String implements fmt.Stringer.
func (t MediaEventType) String() string { return string(t) }

// streamStateEvent maps an entered state to the event it publishes.
//
// A table rather than a switch at each call site, so "what does the platform see
// when a stream pauses" is answered in one place.
//
// Idle and Opening publish nothing: a stream that has not yet carried a frame is
// not something any consumer acts on, and emitting for it would double the
// volume of the busiest topic family in the platform to describe a stream that
// may never exist. Their absence is deliberate, not an omission.
func streamStateEvent(s StreamState) (MediaEventType, bool) {
	switch s {
	case StateActive:
		return EventStreamStarted, true
	case StatePaused:
		return EventStreamPaused, true
	case StateRecovering:
		return EventRecoveryStarted, true
	case StateClosing:
		return EventStreamDraining, true
	case StateClosed:
		return EventStreamClosed, true
	case StateFailed:
		return EventStreamFailed, true
	case StateTimeout:
		return EventStreamTimeout, true
	default:
		return "", false
	}
}

// MediaEvent is one published media streaming event.
//
// # It carries identifiers, states and counts — never audio
//
// MEDIA-PCM-1, applied at its sharpest point. A media event stream is the record
// of every stream the platform carried. Kafka cannot delete an individual
// record, so anything placed here is retained for as long as the topic is,
// regardless of what an erasure request later says.
//
// The test applied to this struct during design: if this topic were retained
// forever and could never be deleted, would that be a compliance failure? It
// must be no. There is deliberately no field capable of holding a sample, a
// payload, a frame or a reference to stored audio — and TestMediaEvent_CarriesNoAudio
// enforces that by reflection, so a later field addition cannot quietly break it.
type MediaEvent struct {
	// Type is what happened.
	Type MediaEventType

	// Stream, Session and Correlation identify the stream.
	Stream      StreamID
	Session     SessionID
	Correlation CorrelationID

	// From and To are the states this event describes. From is empty for events
	// that are not transitions.
	From StreamState
	To   StreamState

	// Reason is the bounded transition code.
	Reason string

	// Direction and Source are the coarse classification. Both are enum-valued
	// or authored identifiers, so both are safe as metric labels and neither can
	// carry content.
	Direction StreamDirection
	Source    SourceID

	// DurationMillis is the stream's elapsed time. Milliseconds as an integer
	// rather than time.Duration, so a consumer in another language does not have
	// to know Go's nanosecond representation.
	DurationMillis int64

	// Delivered and Discarded are counts, not content. They are what makes a
	// quality regression visible without anyone listening to audio.
	Delivered uint64
	Discarded uint64

	// ResumeCount is how many times this stream has recovered.
	ResumeCount int

	// Sequence orders events within one stream, so a consumer can detect a gap or
	// a reordering. Kafka orders within a partition and the partition key is the
	// stream, but a consumer that fans out loses that guarantee and needs this to
	// recover it.
	Sequence int

	// At stamps the event.
	At time.Time
}

// PartitionKey returns the key that keeps a stream's events ordered.
//
// The stream identifier: every event for one stream lands on one partition, so a
// consumer sees Created before Started before Closed. Keying on anything coarser
// — the source, say — puts a busy carrier's streams on one partition and makes
// it the bottleneck.
func (e MediaEvent) PartitionKey() string { return string(e.Stream) }

// Summary renders the event.
func (e MediaEvent) Summary() string {
	s := fmt.Sprintf("%s stream=%s", e.Type, e.Stream)
	if e.From != "" {
		s += fmt.Sprintf(" %s->%s", e.From, e.To)
	}
	if e.Reason != "" {
		s += " (" + e.Reason + ")"
	}
	return s
}

// EventPublisher is the event port.
//
// NOTHING IN THIS PACKAGE IMPLEMENTS IT AGAINST KAFKA. A Kafka adapter lives in
// packages/go/eventbus and a service wires it in. The runtime depends on this
// interface, which is what lets the entire lifecycle be tested with no broker.
//
// Implementations must be safe for concurrent use and must not block
// indefinitely. A publisher that blocks holds a stream transition, and in a media
// path a held transition is audible.
type EventPublisher interface {
	// Publish emits an event. An error is returned to the caller but MUST NOT
	// fail the transition that produced it: a broker outage cannot be allowed to
	// stop streams from closing, or a Kafka incident becomes a media incident.
	Publish(ctx context.Context, e MediaEvent) error
}

// RecordingEventPublisher captures events in memory.
//
// For tests and for a deployment running without a broker. Bounded, because an
// unbounded recorder in a long-running process is a memory leak that presents as
// a slow crash three days later.
type RecordingEventPublisher struct {
	mu      sync.RWMutex
	events  []MediaEvent
	max     int
	dropped int
}

// NewRecordingEventPublisher builds a recorder holding the most recent 4,096
// events.
func NewRecordingEventPublisher() *RecordingEventPublisher {
	return &RecordingEventPublisher{max: 4096}
}

// NewBoundedRecordingEventPublisher builds a recorder with an explicit bound.
func NewBoundedRecordingEventPublisher(max int) *RecordingEventPublisher {
	if max <= 0 {
		max = 4096
	}
	return &RecordingEventPublisher{max: max}
}

// Publish records the event.
func (p *RecordingEventPublisher) Publish(_ context.Context, e MediaEvent) error {
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
func (p *RecordingEventPublisher) Events() []MediaEvent {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]MediaEvent(nil), p.events...)
}

// OfType returns recorded events of one type.
func (p *RecordingEventPublisher) OfType(t MediaEventType) []MediaEvent {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []MediaEvent
	for _, e := range p.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// ForStream returns recorded events for one stream, in sequence order.
func (p *RecordingEventPublisher) ForStream(id StreamID) []MediaEvent {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []MediaEvent
	for _, e := range p.events {
		if e.Stream == id {
			out = append(out, e)
		}
	}
	return out
}

// Len returns how many events are held.
func (p *RecordingEventPublisher) Len() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.events)
}

// Dropped returns how many events were discarded to stay within the bound.
func (p *RecordingEventPublisher) Dropped() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.dropped
}

// Reset clears the recorder.
func (p *RecordingEventPublisher) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = nil
	p.dropped = 0
}

// NopEventPublisher discards every event.
//
// Named so that choosing it is visible in a configuration review. A nil publisher
// would be the same behaviour by accident.
type NopEventPublisher struct{}

// Publish discards.
func (NopEventPublisher) Publish(context.Context, MediaEvent) error { return nil }
