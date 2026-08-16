package speech

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// SpeechEventType classifies a speech event.
type SpeechEventType string

// The event types the speech runtime publishes.
const (
	EventSpeechSessionCreated SpeechEventType = "speech_session_created"
	EventSpeechSessionClosed  SpeechEventType = "speech_session_closed"
	EventSpeechStarted        SpeechEventType = "speech_started"
	EventSpeechPartial        SpeechEventType = "speech_partial"
	EventSpeechFinal          SpeechEventType = "speech_final"
	EventSpeechInterrupted    SpeechEventType = "speech_interrupted"
	EventSpeechCancelled      SpeechEventType = "speech_cancelled"
	EventSpeechFailed         SpeechEventType = "speech_failed"

	EventTTSStarted   SpeechEventType = "tts_started"
	EventTTSChunk     SpeechEventType = "tts_chunk"
	EventTTSCompleted SpeechEventType = "tts_completed"
	EventTTSCancelled SpeechEventType = "tts_cancelled"

	EventProviderChanged   SpeechEventType = "provider_changed"
	EventProviderFailed    SpeechEventType = "provider_failed"
	EventProviderRecovered SpeechEventType = "provider_recovered"
)

// AllSpeechEventTypes returns every published type, in declaration order.
func AllSpeechEventTypes() []SpeechEventType {
	return []SpeechEventType{
		EventSpeechSessionCreated, EventSpeechSessionClosed,
		EventSpeechStarted, EventSpeechPartial, EventSpeechFinal,
		EventSpeechInterrupted, EventSpeechCancelled, EventSpeechFailed,
		EventTTSStarted, EventTTSChunk, EventTTSCompleted, EventTTSCancelled,
		EventProviderChanged, EventProviderFailed, EventProviderRecovered,
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
// requires a dual-write migration across every consumer.
func (t SpeechEventType) Topic() string { return "speech.session." + string(t) + ".v1" }

// String implements fmt.Stringer.
func (t SpeechEventType) String() string { return string(t) }

// maxReasonLen bounds a reason code.
const maxReasonLen = 48

// boundedReason clamps a reason to something safe as a metric label.
//
// Reasons reach labels and topics. An unbounded one taken from provider output
// would let a vendor choose our metric cardinality; a long one would bloat every
// event on the busiest topic family in the platform.
func boundedReason(r string) string {
	if r == "" {
		return "unspecified"
	}
	out := make([]rune, 0, maxReasonLen)
	for _, c := range r {
		if len(out) == maxReasonLen {
			break
		}
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
			out = append(out, c)
		case c >= 'A' && c <= 'Z':
			out = append(out, c+('a'-'A'))
		case c == ' ', c == '-':
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "unspecified"
	}
	return string(out)
}

// SpeechEvent is one published speech event.
//
// # It carries identifiers, states and counts — never content
//
// A speech event stream is the record of every conversation the platform
// handled. Kafka cannot delete an individual record, so anything placed here is
// retained for as long as the topic is, regardless of what an erasure request
// later says.
//
// The test applied to this struct during design: if this topic were retained
// forever and could never be deleted, would that be a compliance failure? It
// must be no.
//
// There is therefore deliberately NO field capable of holding transcript text,
// audio, a payload, a sample or a credential. CharCount carries the LENGTH of
// what was said, which is enough to spot a provider emitting nothing or
// emitting nonsense, and tells nobody what the words were.
// TestSpeechEvent_CarriesNoContent enforces this by reflection, so a later
// field addition cannot quietly break it.
type SpeechEvent struct {
	// Type is what happened.
	Type SpeechEventType

	// Session, Turn and Segment identify the work.
	Session SessionID
	Turn    TurnID
	Segment SegmentID

	// Provider is the authored provider identifier.
	Provider ProviderID

	// Language is the language in play.
	Language Language

	// Role is who was speaking.
	Role Role

	// Reason is the bounded reason code.
	Reason string

	// CharCount is the LENGTH of the transcript, never the transcript.
	CharCount int

	// Confidence is the provider's confidence, or ConfidenceUnknown.
	Confidence float64

	// DurationMillis is elapsed time. Milliseconds as an integer rather than
	// time.Duration, so a consumer in another language does not have to know
	// Go's nanosecond representation.
	DurationMillis int64

	// Sequence orders events within one session, so a consumer can detect a gap
	// or a reordering after fanning out.
	Sequence int

	// At stamps the event.
	At time.Time
}

// PartitionKey returns the key that keeps a session's events ordered.
//
// The session identifier: every event for one session lands on one partition,
// so a consumer sees started before partial before final. Keying on anything
// coarser — the provider, say — puts a busy provider's sessions on one
// partition and makes it the bottleneck.
func (e SpeechEvent) PartitionKey() string { return string(e.Session) }

// Summary renders the event, content-free.
func (e SpeechEvent) Summary() string {
	s := fmt.Sprintf("%s session=%s", e.Type, e.Session)
	if e.Turn != "" {
		s += " turn=" + string(e.Turn)
	}
	if e.CharCount > 0 {
		s += fmt.Sprintf(" chars=%d", e.CharCount)
	}
	if e.Reason != "" {
		s += " (" + e.Reason + ")"
	}
	return s
}

// String implements fmt.Stringer, content-free.
func (e SpeechEvent) String() string { return e.Summary() }

// EventPublisher is the event port.
//
// NOTHING IN THIS PACKAGE IMPLEMENTS IT AGAINST KAFKA. A Kafka adapter lives in
// packages/go/eventbus and a service wires it in. The runtime depends on this
// interface, which is what lets the entire pipeline be tested with no broker.
//
// Implementations must be safe for concurrent use and must not block
// indefinitely. A publisher that blocks holds a speech turn, and a held speech
// turn is dead air on a live call.
type EventPublisher interface {
	// Publish emits an event. An error is returned to the caller but MUST NOT
	// fail the operation that produced it: a broker outage cannot be allowed to
	// stop calls from being answered.
	Publish(ctx context.Context, e SpeechEvent) error
}

// RecordingEventPublisher captures events in memory.
//
// For tests and for a deployment running without a broker. Bounded, because an
// unbounded recorder in a long-running process is a memory leak that presents
// as a slow crash three days later.
type RecordingEventPublisher struct {
	mu      sync.RWMutex
	events  []SpeechEvent
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
func (p *RecordingEventPublisher) Publish(_ context.Context, e SpeechEvent) error {
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
func (p *RecordingEventPublisher) Events() []SpeechEvent {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]SpeechEvent(nil), p.events...)
}

// OfType returns recorded events of one type.
func (p *RecordingEventPublisher) OfType(t SpeechEventType) []SpeechEvent {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []SpeechEvent
	for _, e := range p.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// ForSession returns recorded events for one session, in sequence order.
func (p *RecordingEventPublisher) ForSession(id SessionID) []SpeechEvent {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []SpeechEvent
	for _, e := range p.events {
		if e.Session == id {
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
// Named so that choosing it is visible in a configuration review.
type NopEventPublisher struct{}

// Publish discards.
func (NopEventPublisher) Publish(context.Context, SpeechEvent) error { return nil }
