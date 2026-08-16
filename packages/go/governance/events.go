package governance

import (
	"sync"
	"sync/atomic"
	"time"
)

// EventType classifies a governance event.
type EventType string

// The event types the engine publishes.
const (
	EventDecided              EventType = "decided"
	EventDeniedE              EventType = "denied"
	EventEscalated            EventType = "escalated"
	EventEscalationResolved   EventType = "escalation_resolved"
	EventConsentGranted       EventType = "consent_granted"
	EventConsentRevoked       EventType = "consent_revoked"
	EventConsentExpired       EventType = "consent_expired"
	EventEmergencyActivated   EventType = "emergency_activated"
	EventEmergencyDeactivated EventType = "emergency_deactivated"
	EventRiskThresholdCrossed EventType = "risk_threshold_crossed"
	EventPolicyChanged        EventType = "policy_changed"
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
func (t EventType) Topic() string { return "governance.decision." + string(t) + ".v1" }

// AllEventTypes returns every published type, in declaration order.
func AllEventTypes() []EventType {
	return []EventType{EventDecided, EventDeniedE, EventEscalated,
		EventEscalationResolved, EventConsentGranted, EventConsentRevoked,
		EventConsentExpired, EventEmergencyActivated, EventEmergencyDeactivated,
		EventRiskThresholdCrossed, EventPolicyChanged}
}

// Event is one published governance event.
//
// IT CARRIES IDENTIFIERS, CODES AND FINGERPRINTS — NEVER CONTENT.
//
// Frozen invariant I7, applied to a fifth module, and here the stakes are
// highest: a governance event stream is the record of every decision the
// platform made about every person it spoke to. Kafka cannot delete an
// individual record, so an erasure right that depends on deleting from a topic
// is not an erasure right.
//
// The test applied to this struct during design: if this topic were retained
// forever and could never be deleted, would that be a compliance failure? It
// must be no. There is deliberately no field capable of holding an action's
// resource, an attribute value, or anything a person said.
type Event struct {
	// Type classifies it.
	Type EventType

	// Decision, Correlation and Session locate it.
	Decision    DecisionID
	Correlation CorrelationID
	Session     SessionID

	// Actor and Subject are who asked and about whom. Identifiers, which are
	// pseudonymous references the platform can resolve and a topic reader
	// cannot.
	Actor   ActorID
	Subject SubjectID

	// Outcome is what was decided.
	Outcome Outcome

	// Reason is a short machine-readable code. Never free text.
	Reason string

	// Policy and Scope name what decided.
	Policy PolicyID
	Scope  Scope

	// ActionLabel is the bounded metric form — kind and operation only. NEVER
	// the resource, which is frequently a business or memory identifier.
	ActionLabel string

	// Risk is the aggregate the decision saw.
	Risk RiskLevel

	// PolicyVersion is the snapshot the decision was made against.
	PolicyVersion uint64

	// RequestPrint fingerprints the inputs, so a consumer can correlate two
	// decisions about the same question without either event holding it.
	RequestPrint Fingerprint

	// Obligations lists the obligation kinds imposed, without their targets.
	// A consumer learns that consent was required; it does not learn which
	// basis, because a basis name in a permanent topic is a statement about a
	// person.
	Obligations []ObligationKind

	// Details carries bounded structured extras. Values must be codes.
	Details map[string]string

	// At is the event instant on the engine's clock.
	At time.Time

	// Sequence orders events from one engine. Monotonic per engine, enough for
	// a consumer to detect a gap without a global ordering service.
	Sequence uint64
}

// Publisher receives governance events.
//
// Narrow and synchronous by contract, so this module has no Kafka dependency:
// an adapter in a sibling module implements it over the real broker and the
// engine keeps its zero-external-dependency property.
type Publisher interface {
	// Publish delivers one event. A returned error is counted and discarded: a
	// governance decision must not fail because its notification could not be
	// sent.
	Publish(Event) error
}

// NoopPublisher discards every event.
//
// The default, so the engine runs with no broker configured. An engine that
// required Kafka to start could not be unit-tested, and the whole suite would
// need infrastructure to prove that a policy comparison works.
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
// fail governance decisions for everybody else — but the loss IS visible,
// because a dropped decision event means a downstream that has stopped learning
// what the platform decided.
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

// eventFor builds the decision event.
//
// Obligation KINDS travel; obligation TARGETS do not. A consumer learns that
// consent was required, not which basis — because a basis name in a permanent
// topic is a statement about a person that cannot later be withdrawn.
func eventFor(t EventType, d Decision) Event {
	kinds := make([]ObligationKind, 0, len(d.Obligations))
	seen := make(map[ObligationKind]bool, len(d.Obligations))
	for _, o := range d.Obligations {
		if !seen[o.Kind] {
			seen[o.Kind] = true
			kinds = append(kinds, o.Kind)
		}
	}

	e := Event{
		Type: t, Decision: d.ID, Correlation: d.Correlation, Session: d.Session,
		Actor: d.Actor, Subject: d.Subject, Outcome: d.Outcome, Reason: d.Reason,
		Policy: d.DecidedBy, Scope: d.Scope, ActionLabel: d.ActionLabel,
		Risk: d.Risk.Level, PolicyVersion: d.PolicyVersion,
		RequestPrint: d.RequestPrint, Obligations: kinds, At: d.DecidedAt,
	}
	if d.Emergency != "" {
		e.Details = map[string]string{"emergency": d.Emergency}
	}
	return e
}
