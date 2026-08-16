package telephony

import (
	"fmt"
	"sort"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// TransitionRecord is one entry in a call's history.
//
// Carries states, a bounded reason code and a timestamp — never content. The
// history is the evidence a post-incident review reads, and it must be safe to
// retain for as long as the incident process takes.
type TransitionRecord struct {
	From CallState
	To   CallState
	// Reason is a bounded code such as "screening_rejected" or
	// "provider_timeout". See [checkReasonCode].
	Reason string
	At     time.Time
	// Seq is the position in the history, so two transitions inside one clock
	// tick are still ordered. A FakeClock that does not advance would otherwise
	// produce a history no reader can sequence.
	Seq int
}

// String renders the record.
func (t TransitionRecord) String() string {
	return fmt.Sprintf("#%d %s->%s (%s) at %s", t.Seq, t.From, t.To, t.Reason,
		t.At.UTC().Format(time.RFC3339Nano))
}

// maxHistory bounds a session's retained transitions.
//
// A call that flaps between hold and connected for an hour would otherwise grow
// without limit, and the session is held in memory for the life of the call.
// The oldest records are dropped; the first transition is always kept, because
// "how did this call begin" is the question a review asks first.
const maxHistory = 128

// reasonCodeMax bounds a transition reason.
const reasonCodeMax = 64

// checkReasonCode refuses free text on a path that reaches Kafka.
//
// The same control Phase 10E added after its audit found reason codes were
// unbounded free text on a path into an event stream. Here the risk is worse:
// the obvious thing for an adapter author to put in a hangup reason is whatever
// the carrier returned, and carriers return strings containing numbers.
func checkReasonCode(reason string) error {
	if reason == "" {
		return invariant("INV-TEL-3", "a transition reason is required")
	}
	if len(reason) > reasonCodeMax {
		return invariant("INV-TEL-3",
			"reason code is %d characters, cap is %d — a reason is a code, not a "+
				"carrier message", len(reason), reasonCodeMax)
	}
	for _, r := range reason {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '.':
		default:
			return invariant("INV-TEL-3",
				"reason code %q must be lowercase alphanumerics, underscore or dot; "+
					"free text on this path reaches a durable event stream", truncate(reason))
		}
	}
	return nil
}

// CallSession is one call's mutable state.
//
// # Ownership
//
// A session is owned by exactly one [CallRegistry] and carries its own lock.
// Two sessions share nothing, so a thousand concurrent calls contend only when
// they land on the same registry shard.
//
// # The lock covers the mutable fields, not the FSM
//
// [runtime.FSM] has its own lock and is safe for concurrent use. The session
// lock covers history, timestamps and metadata. Transition therefore takes the
// FSM lock and the session lock in that order and never the reverse — a rule
// worth stating because the natural way to write the reverse is to hold the
// session lock while asking the FSM whether a move is legal.
type CallSession struct {
	// Immutable identity, safe to read without the lock.
	id          CallID
	sessionID   SessionID
	correlation CorrelationID
	ctx         CallContext
	createdAt   time.Time

	fsm   *rt.FSM[CallState]
	clock rt.Clock

	mu        sync.RWMutex
	history   []TransitionRecord
	seq       int
	updatedAt time.Time
	// answeredAt and endedAt bound the billable and the observable call.
	// Separate because a call that rang for 30 seconds and talked for 5 has two
	// durations and conflating them makes both wrong.
	answeredAt time.Time
	endedAt    time.Time
	legs       []LegID
	// resumeCount records how many times this call has been recovered. The
	// number a recovery incident asks for, and the reason SessionID is distinct
	// from CallID.
	resumeCount int
	// attrs is small mutable per-call state, bounded like the context metadata.
	attrs map[string]string
	// endReason is the bounded code the call concluded with.
	endReason string
}

// newSession builds a session. Callers go through [CallRegistry.Create].
func newSession(id CallID, sess SessionID, corr CorrelationID, cc CallContext,
	initial CallState, clock rt.Clock) (*CallSession, error) {
	if err := cc.Validate(); err != nil {
		return nil, err
	}
	fsm, err := newCallFSM(initial, clock)
	if err != nil {
		return nil, err
	}

	now := clock.Now()
	return &CallSession{
		id: id, sessionID: sess, correlation: corr,
		ctx: cc.Clone(), createdAt: now, updatedAt: now,
		fsm: fsm, clock: clock,
		attrs: make(map[string]string),
	}, nil
}

// ID returns the call identifier. Immutable, no lock.
func (s *CallSession) ID() CallID { return s.id }

// SessionID returns this session's identifier. Immutable, no lock.
func (s *CallSession) SessionID() SessionID { return s.sessionID }

// Correlation returns the correlation identifier. Immutable, no lock.
func (s *CallSession) Correlation() CorrelationID { return s.correlation }

// Context returns a copy of the call context.
//
// A copy, not the value, because [CallContext] holds a map and a slice and
// handing out the originals would let a caller mutate a live call's provider
// capabilities.
func (s *CallSession) Context() CallContext { return s.ctx.Clone() }

// CreatedAt returns when the session began. Immutable, no lock.
func (s *CallSession) CreatedAt() time.Time { return s.createdAt }

// State returns the current state.
func (s *CallSession) State() CallState { return s.fsm.State() }

// Is reports whether the call is in the given state.
func (s *CallSession) Is(state CallState) bool { return s.fsm.Is(state) }

// Terminal reports whether the call has concluded.
func (s *CallSession) Terminal() bool { return s.fsm.IsTerminal() }

// CanTransition reports whether a move is currently legal.
func (s *CallSession) CanTransition(to CallState) bool { return s.fsm.CanGo(to) }

// Transition moves the call, recording the reason.
//
// THE ONLY WAY A CALL CHANGES STATE. There is no setter, and the FSM refuses
// anything the transition table does not declare — so "no implicit transitions"
// is enforced by construction rather than by review.
func (s *CallSession) Transition(to CallState, reason string) error {
	if err := checkReasonCode(reason); err != nil {
		return err
	}

	from, err := s.fsm.To(to)
	if err != nil {
		return &TransitionError{Call: s.id, From: from, To: to, Reason: reason}
	}

	now := s.clock.Now()

	s.mu.Lock()
	s.seq++
	rec := TransitionRecord{From: from, To: to, Reason: reason, At: now, Seq: s.seq}
	s.appendHistoryLocked(rec)
	s.updatedAt = now

	// Timestamps that only a transition can establish. Recorded here rather
	// than by the caller, because a caller that forgot would leave a connected
	// call with no answer time and a duration of zero.
	if to.Connected() && s.answeredAt.IsZero() {
		s.answeredAt = now
	}
	if to.Terminal() {
		s.endedAt = now
		s.endReason = reason
	}
	s.mu.Unlock()

	return nil
}

// appendHistoryLocked adds a record, preserving the first and dropping the
// oldest of the rest. The caller holds the write lock.
func (s *CallSession) appendHistoryLocked(rec TransitionRecord) {
	if len(s.history) < maxHistory {
		s.history = append(s.history, rec)
		return
	}
	// Keep index 0 — how the call began — and slide the rest. A flapping call
	// loses its middle, which is the least interesting part.
	copy(s.history[1:], s.history[2:])
	s.history[len(s.history)-1] = rec
}

// History returns a copy of the transition history, oldest first.
//
// COPIES. Use [CallSession.HistoryLen] when only the count is needed — this
// allocates the whole history, which at the 128-record cap is over 12 KB.
func (s *CallSession) History() []TransitionRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]TransitionRecord(nil), s.history...)
}

// HistoryLen returns how many transitions are retained. O(1), no allocation.
//
// Exists because the event sequencer needs the count on every published event
// and the first version obtained it as len(History()) — deep-copying 128
// records, 12.8 KB, per event. The same shape of defect Phase 10F found in its
// pending-golden gauge: a cheap-looking accessor that copies a collection,
// called on a hot path. See ENGINEERING_AUDIT F4.
func (s *CallSession) HistoryLen() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.history)
}

// UpdatedAt returns when the session last changed.
func (s *CallSession) UpdatedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updatedAt
}

// AnsweredAt returns when media was first established, or the zero time.
func (s *CallSession) AnsweredAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.answeredAt
}

// EndedAt returns when the call concluded, or the zero time.
func (s *CallSession) EndedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.endedAt
}

// EndReason returns the code the call concluded with.
func (s *CallSession) EndReason() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.endReason
}

// Duration returns how long the call has existed.
func (s *CallSession) Duration() time.Duration {
	s.mu.RLock()
	end := s.endedAt
	s.mu.RUnlock()
	if end.IsZero() {
		return s.clock.Now().Sub(s.createdAt)
	}
	return end.Sub(s.createdAt)
}

// TalkDuration returns how long media was established.
//
// DISTINCT FROM [CallSession.Duration], and the distinction is the one a
// billing system and a capacity model disagree about. A call that rang for
// thirty seconds and talked for five occupied a channel for thirty-five and is
// billable for five. Reporting one number for both makes whichever consumer you
// did not think about wrong.
func (s *CallSession) TalkDuration() time.Duration {
	s.mu.RLock()
	answered, end := s.answeredAt, s.endedAt
	s.mu.RUnlock()

	if answered.IsZero() {
		return 0
	}
	if end.IsZero() {
		return s.clock.Now().Sub(answered)
	}
	return end.Sub(answered)
}

// ResumeCount returns how many times this call has been recovered.
func (s *CallSession) ResumeCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.resumeCount
}

// Legs returns the call's legs, in creation order.
func (s *CallSession) Legs() []LegID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]LegID(nil), s.legs...)
}

// AddLeg records a new leg, as a transfer produces.
func (s *CallSession) AddLeg(leg LegID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.legs = append(s.legs, leg)
	s.updatedAt = s.clock.Now()
}

// SetAttr records small mutable per-call state.
//
// Bounded like the context metadata, and for the same reason: this reaches the
// snapshot, and a snapshot reaches durable storage.
func (s *CallSession) SetAttr(key, value string) error {
	if len(key) > maxMetadataKeyLen || len(value) > maxMetadataValLen {
		return invariant("INV-TEL-4",
			"attribute %q exceeds the size cap", truncate(key))
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, replacing := s.attrs[key]; !replacing && len(s.attrs) >= maxMetadataEntries {
		return invariant("INV-TEL-4",
			"session already holds %d attributes, cap is %d",
			len(s.attrs), maxMetadataEntries)
	}
	s.attrs[key] = value
	s.updatedAt = s.clock.Now()
	return nil
}

// Attr returns an attribute.
func (s *CallSession) Attr(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.attrs[key]
	return v, ok
}

// Attrs returns a copy of the attributes.
func (s *CallSession) Attrs() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.attrs))
	for k, v := range s.attrs {
		out[k] = v
	}
	return out
}

// ---------------------------------------------------------------------------
// Snapshot and restore
// ---------------------------------------------------------------------------

// Snapshot is a session captured for durable storage.
//
// # It is the recovery contract
//
// Everything needed to reconstruct a call after a process restart, and nothing
// more. Snapshot is a plain struct with exported fields and no methods that
// mutate: a [SessionStore] serialises it however its backend prefers, and this
// package takes no position on the encoding.
//
// # It carries no content
//
// The same rule as the event stream. A snapshot is written to Redis or Aurora
// and lives as long as the retention policy says — which for a crashed call is
// longer than the call. There is no field here capable of holding a number, a
// name, or anything said.
type Snapshot struct {
	// SchemaVersion is stamped on every snapshot so a rolling deploy can read
	// what the previous version wrote. Phase 10.5 established this pattern for
	// the evaluation repository; a call snapshot has the same problem and a
	// worse consequence, since a snapshot that decodes wrong resumes a call
	// into the wrong state.
	SchemaVersion int

	Call        CallID
	Session     SessionID
	Correlation CorrelationID

	State   CallState
	Context CallContext

	CreatedAt  time.Time
	UpdatedAt  time.Time
	AnsweredAt time.Time
	EndedAt    time.Time
	EndReason  string

	History     []TransitionRecord
	Legs        []LegID
	Attrs       map[string]string
	ResumeCount int
}

// SnapshotSchemaVersion is the version this build writes.
const SnapshotSchemaVersion = 1

// Snapshot captures the session.
func (s *CallSession) Snapshot() Snapshot {
	state := s.fsm.State()

	s.mu.RLock()
	defer s.mu.RUnlock()

	attrs := make(map[string]string, len(s.attrs))
	for k, v := range s.attrs {
		attrs[k] = v
	}

	return Snapshot{
		SchemaVersion: SnapshotSchemaVersion,
		Call:          s.id,
		Session:       s.sessionID,
		Correlation:   s.correlation,
		State:         state,
		Context:       s.ctx.Clone(),
		CreatedAt:     s.createdAt,
		UpdatedAt:     s.updatedAt,
		AnsweredAt:    s.answeredAt,
		EndedAt:       s.endedAt,
		EndReason:     s.endReason,
		History:       append([]TransitionRecord(nil), s.history...),
		Legs:          append([]LegID(nil), s.legs...),
		Attrs:         attrs,
		ResumeCount:   s.resumeCount,
	}
}

// Recoverable reports whether this snapshot can be resumed.
//
// A terminal call cannot: it is already over, and resuming it would create a
// second session for a call that concluded. An unreadable schema cannot, and
// refusing is the right answer — a snapshot decoded under the wrong schema
// resumes a call into a state it was never in, which is worse than losing it.
func (s Snapshot) Recoverable() bool {
	if s.SchemaVersion != SnapshotSchemaVersion {
		return false
	}
	if !s.State.Valid() || s.State.Terminal() {
		return false
	}
	return s.Call.Valid()
}

// Restore reconstructs a session from a snapshot.
//
// # The restored session starts in StateRecovery, not its snapshotted state
//
// This is the central recovery decision and it is worth being explicit about.
// The obvious implementation puts the session back into the state it was in —
// and it is wrong. The snapshot says the call was Connected; the process that
// believed that is gone, and nothing has verified with the provider that the
// call is still up. A session restored directly into Connected would report a
// live call that may have hung up minutes ago, and would emit metrics and
// events for it.
//
// StateRecovery says exactly what is true: this call existed, and we do not yet
// know whether it still does. [CallLifecycle.Resume] moves it to Connected only
// after the provider confirms, and to Ended otherwise.
func Restore(snap Snapshot, clock rt.Clock) (*CallSession, error) {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	if !snap.Recoverable() {
		return nil, fmt.Errorf("%w: call %s in state %s at schema v%d",
			ErrNotRecoverable, snap.Call, snap.State, snap.SchemaVersion)
	}
	if err := snap.Context.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotRecoverable, err)
	}

	fsm, err := newCallFSM(StateRecovery, clock)
	if err != nil {
		return nil, err
	}

	attrs := make(map[string]string, len(snap.Attrs))
	for k, v := range snap.Attrs {
		attrs[k] = v
	}

	sess := &CallSession{
		id: snap.Call,
		// A NEW session identifier for the same call. The call is the same; the
		// session is not, and conflating them makes "how many times did we
		// recover this" unanswerable.
		sessionID:   NewSessionID(),
		correlation: snap.Correlation,
		ctx:         snap.Context.Clone(),
		createdAt:   snap.CreatedAt,
		fsm:         fsm,
		clock:       clock,
		history:     append([]TransitionRecord(nil), snap.History...),
		updatedAt:   clock.Now(),
		answeredAt:  snap.AnsweredAt,
		legs:        append([]LegID(nil), snap.Legs...),
		attrs:       attrs,
		resumeCount: snap.ResumeCount + 1,
	}
	if n := len(sess.history); n > 0 {
		sess.seq = sess.history[n-1].Seq
	}
	return sess, nil
}

// Summary renders the session for an operator.
func (s *CallSession) Summary() string {
	s.mu.RLock()
	resumes := s.resumeCount
	s.mu.RUnlock()

	out := fmt.Sprintf("call %s session %s state=%s %s dur=%s",
		s.id, s.sessionID, s.State(), s.ctx, s.Duration().Round(time.Millisecond))
	if talk := s.TalkDuration(); talk > 0 {
		out += fmt.Sprintf(" talk=%s", talk.Round(time.Millisecond))
	}
	if resumes > 0 {
		out += fmt.Sprintf(" resumed=%d", resumes)
	}
	return out
}

// sortSnapshots orders snapshots oldest first, for a deterministic recovery
// sweep. Recovery order must not depend on map iteration: two runs of the same
// recovery should produce the same event sequence.
func sortSnapshots(snaps []Snapshot) {
	sort.Slice(snaps, func(i, j int) bool {
		if !snaps[i].CreatedAt.Equal(snaps[j].CreatedAt) {
			return snaps[i].CreatedAt.Before(snaps[j].CreatedAt)
		}
		return snaps[i].Call < snaps[j].Call
	})
}
