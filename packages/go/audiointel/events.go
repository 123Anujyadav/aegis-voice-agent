package audiointel

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// AudioEventType classifies an audio intelligence event.
type AudioEventType string

// The sixteen event types the audio intelligence runtime publishes.
//
// Grouped by the question each answers: is somebody talking, has the turn
// ended, is somebody being talked over, and is the audio any good.
const (
	// Speech activity.
	EventSpeechStarted  AudioEventType = "speech_started"
	EventSpeechDetected AudioEventType = "speech_detected"
	EventSpeechStopped  AudioEventType = "speech_stopped"

	// Endpointing.
	EventEndpointCandidate AudioEventType = "endpoint_candidate"
	EventEndpointConfirmed AudioEventType = "endpoint_confirmed"

	// Silence.
	EventSilenceStarted  AudioEventType = "silence_started"
	EventSilenceExtended AudioEventType = "silence_extended"

	// Interruption.
	EventBargeInDetected AudioEventType = "barge_in_detected"
	EventOverlapDetected AudioEventType = "overlap_detected"
	EventOverlapResolved AudioEventType = "overlap_resolved"

	// Signal conditions.
	EventNoiseChanged   AudioEventType = "noise_changed"
	EventQualityChanged AudioEventType = "quality_changed"
	EventAudioDegraded  AudioEventType = "audio_degraded"
	EventAudioRecovered AudioEventType = "audio_recovered"

	// Transport continuity.
	EventFrameGapDetected        AudioEventType = "frame_gap_detected"
	EventFrameContinuityRestored AudioEventType = "frame_continuity_restored"
)

// AllAudioEventTypes returns every published type, in declaration order.
func AllAudioEventTypes() []AudioEventType {
	return []AudioEventType{
		EventSpeechStarted, EventSpeechDetected, EventSpeechStopped,
		EventEndpointCandidate, EventEndpointConfirmed,
		EventSilenceStarted, EventSilenceExtended,
		EventBargeInDetected, EventOverlapDetected, EventOverlapResolved,
		EventNoiseChanged, EventQualityChanged, EventAudioDegraded,
		EventAudioRecovered,
		EventFrameGapDetected, EventFrameContinuityRestored,
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
// second per session this sits alongside media as the highest-volume topic
// family in the platform.
func (t AudioEventType) Topic() string { return "audio.intel." + string(t) + ".v1" }

// String implements fmt.Stringer.
func (t AudioEventType) String() string { return string(t) }

// Valid reports whether the type is one of the declared sixteen.
func (t AudioEventType) Valid() bool {
	for _, known := range AllAudioEventTypes() {
		if t == known {
			return true
		}
	}
	return false
}

// Classification is the bounded state or class an event reports.
//
// Always an authored enum value — a VAD state, a silence class, a quality
// class, an overlap state. Never derived from input, never free text, so it is
// safe as a metric label.
type Classification string

// EventDetail is the bounded metadata an event carries.
//
// # A fixed struct, deliberately, and never a map
//
// A map[string]any on an event is a hole in every review this document
// describes: nobody can tell by reading the type what a producer might put in
// it, the reflection test below cannot prove anything about its contents, and
// the first time somebody needs "just a bit more context" the transcript ends
// up in it.
//
// Every field here is a NUMBER, and every number is a measurement about the
// audio rather than any part of it. A level in dBFS says how loud the caller
// was; it does not say a single thing about what they said, and it cannot be
// reassembled into anything that does.
type EventDetail struct {
	// DurationMillis is how long the reported condition lasted.
	//
	// Milliseconds as an integer rather than time.Duration, so a consumer in
	// another language does not have to know Go's nanosecond representation.
	DurationMillis int64

	// LevelDBFS is the signal level, in decibels relative to full scale.
	// Negative for anything below clipping.
	LevelDBFS float64

	// NoiseFloorDBFS is the adaptive floor estimate at the moment of the event.
	NoiseFloorDBFS float64

	// SNRDB is the estimated signal-to-noise ratio.
	SNRDB float64

	// FrameCount is how many frames the condition spans.
	FrameCount int

	// LatencyMicros is the measured detection or reaction latency.
	//
	// Microseconds because the numbers that matter here — barge-in against a
	// 20 ms budget — are smaller than a millisecond can express usefully.
	LatencyMicros int64
}

// AudioEvent is one published audio intelligence event.
//
// # It carries identifiers, classifications and measurements — never audio
//
// An audio intelligence event stream is a record of every call the platform
// listened to. Kafka cannot delete an individual record, so anything placed
// here is retained for as long as the topic is, regardless of what an erasure
// request later says.
//
// The test applied to this struct during design, the same one Phases 11B and
// 11C applied to theirs: if this topic were retained forever and could never be
// deleted, would that be a compliance failure? It must be no.
//
// There is therefore deliberately NO field capable of holding a sample, a
// payload, a frame, a transcript, a phone number or a credential.
// TestAudioEvent_CarriesNoAudio enforces this by reflection — recursively,
// including [EventDetail] — so a later field addition cannot quietly break it.
type AudioEvent struct {
	// Type is what happened.
	Type AudioEventType

	// Session, Call and Turn identify the work.
	//
	// Call and Turn are opaque and supplied by the caller; a signal detected
	// between turns legitimately has no turn, and a session analysing audio
	// before a call record exists legitimately has no call.
	Session SessionID
	Call    CallID
	Turn    TurnID

	// Language is the tag Phase 11C associated with this audio, carried through
	// unmodified. Empty when none was supplied. See [Language] — this engine
	// never interprets it.
	Language Language

	// Classification is the bounded state or class reported.
	Classification Classification

	// Confidence is how sure the detector is, in [0,1].
	//
	// ALWAYS MEANINGFUL, never a placeholder. A detector that cannot estimate
	// its confidence reports the value its documentation specifies for that
	// case, and OVERLAP_DETECTION.md in particular explains why overlap
	// confidence is the number to read rather than the state.
	Confidence float64

	// Reason is the bounded reason code.
	Reason string

	// Detail is the bounded numeric metadata.
	Detail EventDetail

	// MediaTime is the position on the stream's media timeline this event
	// describes.
	//
	// SEPARATE FROM At, AND BOTH ARE NEEDED. Media time says where in the audio
	// the thing happened; wall time says when we noticed. The gap between them
	// is the detection latency, and collapsing the two makes that gap
	// unmeasurable — which is exactly the number ADR-0011 budgets.
	MediaTime time.Duration

	// Sequence orders events within one session, so a consumer can detect a gap
	// or a reordering after fanning out.
	Sequence int

	// At stamps the event on the injected clock.
	At time.Time
}

// PartitionKey returns the key that keeps a session's events ordered.
//
// The session identifier: every event for one session lands on one partition,
// so a consumer sees speech_started before speech_stopped before
// endpoint_confirmed. Keying on anything coarser puts a busy deployment's
// sessions on one partition and makes it the bottleneck.
func (e AudioEvent) PartitionKey() string { return string(e.Session) }

// Summary renders the event, content-free.
func (e AudioEvent) Summary() string {
	s := fmt.Sprintf("%s session=%s", e.Type, e.Session)
	if e.Classification != "" {
		s += " class=" + string(e.Classification)
	}
	if e.Confidence > 0 {
		s += fmt.Sprintf(" conf=%.2f", e.Confidence)
	}
	if e.Detail.DurationMillis > 0 {
		s += fmt.Sprintf(" dur=%dms", e.Detail.DurationMillis)
	}
	if e.Reason != "" {
		s += " (" + e.Reason + ")"
	}
	return s
}

// String implements fmt.Stringer, content-free.
func (e AudioEvent) String() string { return e.Summary() }

// EventPublisher is the event port.
//
// NOTHING IN THIS PACKAGE IMPLEMENTS IT AGAINST KAFKA. A Kafka adapter lives in
// packages/go/eventbus and a service wires it in. The runtime depends on this
// interface, which is what lets the entire detection pipeline be tested with no
// broker.
//
// Implementations must be safe for concurrent use and must not block. THIS PORT
// IS CALLED FROM THE FRAME PATH: a publisher that blocks holds a frame, and at
// fifty frames a second a held frame is the barge-in budget spent on a broker.
type EventPublisher interface {
	// Publish emits an event. An error is returned to the caller but MUST NOT
	// fail the detection that produced it: a broker outage cannot be allowed to
	// stop the engine noticing that somebody is talking.
	Publish(ctx context.Context, e AudioEvent) error
}

// RecordingEventPublisher captures events in memory.
//
// For tests and for a deployment running without a broker. Bounded, because an
// unbounded recorder in a long-running process is a memory leak that presents
// as a slow crash three days later.
type RecordingEventPublisher struct {
	mu      sync.RWMutex
	events  []AudioEvent
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
func (p *RecordingEventPublisher) Publish(_ context.Context, e AudioEvent) error {
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
func (p *RecordingEventPublisher) Events() []AudioEvent {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]AudioEvent(nil), p.events...)
}

// OfType returns recorded events of one type, in order.
func (p *RecordingEventPublisher) OfType(t AudioEventType) []AudioEvent {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []AudioEvent
	for _, e := range p.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// ForSession returns recorded events for one session, in sequence order.
func (p *RecordingEventPublisher) ForSession(id SessionID) []AudioEvent {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []AudioEvent
	for _, e := range p.events {
		if e.Session == id {
			out = append(out, e)
		}
	}
	return out
}

// Count returns how many events of one type were recorded.
func (p *RecordingEventPublisher) Count(t AudioEventType) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var n int
	for _, e := range p.events {
		if e.Type == t {
			n++
		}
	}
	return n
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
// Named so that choosing it is visible in a configuration review. A nil
// publisher would be the same behaviour by accident.
type NopEventPublisher struct{}

// Publish discards.
func (NopEventPublisher) Publish(context.Context, AudioEvent) error { return nil }
