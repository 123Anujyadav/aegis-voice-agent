package voice

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// VoiceEventType classifies a voice runtime event.
type VoiceEventType string

// The event types the voice runtime publishes.
const (
	EventSessionCreated VoiceEventType = "voice_session_created"
	EventSessionClosed  VoiceEventType = "voice_session_closed"

	EventTurnStarted   VoiceEventType = "voice_turn_started"
	EventTurnCompleted VoiceEventType = "voice_turn_completed"
	EventTurnFailed    VoiceEventType = "voice_turn_failed"
	EventTurnCancelled VoiceEventType = "voice_turn_cancelled"

	EventStateChanged VoiceEventType = "voice_state_changed"

	EventTranscriptPartial VoiceEventType = "voice_transcript_partial"
	EventTranscriptFinal   VoiceEventType = "voice_transcript_final"

	EventResponseStarted   VoiceEventType = "voice_response_started"
	EventResponseCompleted VoiceEventType = "voice_response_completed"

	EventBargeIn VoiceEventType = "voice_barge_in"

	EventGovernanceDenied VoiceEventType = "voice_governance_denied"

	EventProviderSwitched VoiceEventType = "voice_provider_switched"
	EventProviderFailed   VoiceEventType = "voice_provider_failed"
	EventProviderRestored VoiceEventType = "voice_provider_restored"
	EventProcessRestarted VoiceEventType = "voice_process_restarted"
)

// AllVoiceEventTypes returns every published type, in declaration order.
func AllVoiceEventTypes() []VoiceEventType {
	return []VoiceEventType{
		EventSessionCreated, EventSessionClosed,
		EventTurnStarted, EventTurnCompleted, EventTurnFailed, EventTurnCancelled,
		EventStateChanged,
		EventTranscriptPartial, EventTranscriptFinal,
		EventResponseStarted, EventResponseCompleted,
		EventBargeIn, EventGovernanceDenied,
		EventProviderSwitched, EventProviderFailed, EventProviderRestored,
		EventProcessRestarted,
	}
}

// Topic returns the Kafka topic for an event type.
//
// The shape is fixed by packages/go/eventbus: <domain>.<entity>.<event>.v<major>.
// Lowercase, underscores inside a segment, hyphens prohibited because they
// collide with Prometheus metric-name normalisation.
func (t VoiceEventType) Topic() string { return "voice.session." + string(t) + ".v1" }

// String implements fmt.Stringer.
func (t VoiceEventType) String() string { return string(t) }

// Valid reports whether the type is declared.
func (t VoiceEventType) Valid() bool {
	for _, known := range AllVoiceEventTypes() {
		if t == known {
			return true
		}
	}
	return false
}

// VoiceEvent is one published voice runtime event.
//
// # It carries identifiers, states and counts — never content
//
// A voice event stream is the record of every call the platform handled. Kafka
// cannot delete an individual record, so anything placed here is retained for
// as long as the topic is, regardless of what an erasure request later says.
//
// The test applied during design, the same one Phases 11B, 11C and 11D applied:
// if this topic were retained forever and could never be deleted, would that be
// a compliance failure? It must be no.
//
// So there is deliberately NO field capable of holding a transcript, a response,
// audio, a prompt, a phone number or a credential. CharCount carries the LENGTH
// of what was said, which is enough to spot a recogniser emitting nothing or a
// model emitting a wall of text, and tells nobody what the words were.
// TestVoiceEvent_CarriesNoContent enforces that by reflection.
type VoiceEvent struct {
	// Type is what happened.
	Type VoiceEventType

	// Session, Turn and Call identify the work.
	Session SessionID
	Turn    TurnID
	Call    CallID

	// Provider is the authored provider identifier, where one is involved.
	Provider ProviderID

	// From and To are the states this event describes. Empty for events that
	// are not transitions.
	From SessionState
	To   SessionState

	// Reason is the bounded reason code.
	Reason string

	// CharCount is the LENGTH of a transcript or response, never the text.
	CharCount int

	// Confidence is a recogniser's confidence, or zero.
	Confidence float64

	// DurationMillis is elapsed time, as an integer so a consumer in another
	// language need not know Go's nanosecond representation.
	DurationMillis int64

	// LatencyMicros is a measured latency — first partial, first audio, a
	// cancellation.
	LatencyMicros int64

	// Sequence orders events within one session so a consumer can detect a gap
	// after fanning out.
	Sequence int

	// At stamps the event on the injected clock.
	At time.Time
}

// PartitionKey returns the key that keeps a session's events ordered.
func (e VoiceEvent) PartitionKey() string { return string(e.Session) }

// Summary renders the event, content-free.
func (e VoiceEvent) Summary() string {
	s := fmt.Sprintf("%s session=%s", e.Type, e.Session)
	if e.Turn != "" {
		s += " turn=" + string(e.Turn)
	}
	if e.From != "" {
		s += fmt.Sprintf(" %s->%s", e.From, e.To)
	}
	if e.Provider != "" {
		s += " provider=" + string(e.Provider)
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
func (e VoiceEvent) String() string { return e.Summary() }

// EventPublisher is the event port.
//
// NOTHING IN THIS PACKAGE IMPLEMENTS IT AGAINST KAFKA. A broker adapter lives in
// packages/go/eventbus and a service wires it in.
//
// Implementations must be safe for concurrent use and must not block: this is
// called from the turn path, and a publisher that blocks holds a conversation.
type EventPublisher interface {
	// Publish emits an event. An error is returned but MUST NOT fail the
	// operation that produced it: a broker outage cannot stop calls being
	// answered.
	Publish(ctx context.Context, e VoiceEvent) error
}

// RecordingEventPublisher captures events in memory.
//
// Bounded, because an unbounded recorder in a long-running process is a memory
// leak that presents as a slow crash three days later.
type RecordingEventPublisher struct {
	mu      sync.RWMutex
	events  []VoiceEvent
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
func (p *RecordingEventPublisher) Publish(_ context.Context, e VoiceEvent) error {
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
func (p *RecordingEventPublisher) Events() []VoiceEvent {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]VoiceEvent(nil), p.events...)
}

// OfType returns recorded events of one type, in order.
func (p *RecordingEventPublisher) OfType(t VoiceEventType) []VoiceEvent {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []VoiceEvent
	for _, e := range p.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// Count returns how many events of one type were recorded.
func (p *RecordingEventPublisher) Count(t VoiceEventType) int {
	return len(p.OfType(t))
}

// ForSession returns recorded events for one session.
func (p *RecordingEventPublisher) ForSession(id SessionID) []VoiceEvent {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []VoiceEvent
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
func (NopEventPublisher) Publish(context.Context, VoiceEvent) error { return nil }
