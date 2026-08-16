package conversation

import (
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Party is a participant in the conversation.
type Party int

const (
	// PartyNone means nobody holds the floor.
	PartyNone Party = iota
	// PartyCaller is the human on the other end.
	PartyCaller
	// PartyAgent is the AI.
	PartyAgent
	// PartySystem is the platform itself — announcements, error notices.
	PartySystem
)

// String renders the party for logs and metric labels.
func (p Party) String() string {
	switch p {
	case PartyCaller:
		return "caller"
	case PartyAgent:
		return "agent"
	case PartySystem:
		return "system"
	default:
		return "none"
	}
}

// TurnID identifies one turn within a conversation. Monotonic per conversation,
// which is safe because it never leaves the conversation and therefore leaks
// nothing about platform volume.
type TurnID uint64

// Turn is one party's contiguous occupation of the floor.
type Turn struct {
	// ID is the turn's ordinal.
	ID TurnID
	// Owner is the party holding the floor.
	Owner Party
	// StartedAt is when the floor was acquired.
	StartedAt time.Time
	// EndedAt is when it was released. Zero while open.
	EndedAt time.Time
	// Yielded reports whether the turn ended by yielding rather than by
	// completing. A yielded turn was cut short and its content is incomplete.
	Yielded bool
	// InterruptedBy names the interruption that ended the turn, if any.
	InterruptedBy InterruptionKind
	// Expectation is the answer shape this turn established, if any. Set when
	// the agent asks a constrained question.
	Expectation Expectation
}

// Duration returns the turn's length, or its length so far if still open.
func (t Turn) Duration(now time.Time) time.Duration {
	if t.EndedAt.IsZero() {
		return now.Sub(t.StartedAt)
	}
	return t.EndedAt.Sub(t.StartedAt)
}

// Expectation is the answer shape a constrained turn established.
type Expectation int

const (
	// ExpectNothing is an unconstrained turn.
	ExpectNothing Expectation = iota
	// ExpectDisambiguation follows a clarifying question.
	ExpectDisambiguation
	// ExpectYesNo follows a confirmation.
	ExpectYesNo
	// ExpectSlotValue follows a question targeting a specific slot.
	ExpectSlotValue
)

// String renders the expectation for logs and metric labels.
func (e Expectation) String() string {
	switch e {
	case ExpectDisambiguation:
		return "disambiguation"
	case ExpectYesNo:
		return "yes_no"
	case ExpectSlotValue:
		return "slot_value"
	default:
		return "none"
	}
}

// TurnConfig tunes floor control.
type TurnConfig struct {
	// MaxTurnDuration bounds one party's occupation of the floor. An agent
	// turn that runs long is a monologue; a caller turn that runs long is
	// usually a voicemail greeting or a hold recording.
	MaxTurnDuration time.Duration

	// OverlapGrace is how long simultaneous speech is tolerated before
	// arbitration runs.
	//
	// Non-zero deliberately. Real conversations overlap constantly —
	// backchannels ("mm-hm", "right") are simultaneous speech that is NOT an
	// interruption. Arbitrating instantly on any overlap makes the agent stop
	// every time the caller agrees with it.
	OverlapGrace time.Duration

	// BackchannelMaxDuration is the longest overlap treated as a backchannel
	// rather than a barge-in.
	BackchannelMaxDuration time.Duration

	// SilenceThreshold is how long silence must persist before the engine
	// treats the floor as released.
	SilenceThreshold time.Duration
}

// DefaultTurnConfig returns the configuration used unless overridden.
//
// OverlapGrace and BackchannelMaxDuration are derived from conversation
// analysis rather than from round numbers: a backchannel is typically under
// 500 ms, and 250 ms of overlap is below the threshold at which a human would
// perceive being interrupted.
func DefaultTurnConfig() TurnConfig {
	return TurnConfig{
		MaxTurnDuration:        45 * time.Second,
		OverlapGrace:           250 * time.Millisecond,
		BackchannelMaxDuration: 600 * time.Millisecond,
		SilenceThreshold:       700 * time.Millisecond,
	}
}

func (c TurnConfig) validate() []string {
	var p []string
	if c.MaxTurnDuration <= 0 {
		p = append(p, "turn: MaxTurnDuration must be positive")
	}
	if c.OverlapGrace < 0 {
		p = append(p, "turn: OverlapGrace cannot be negative")
	}
	if c.BackchannelMaxDuration < c.OverlapGrace {
		p = append(p, "turn: BackchannelMaxDuration must be at least OverlapGrace, "+
			"or no overlap can ever be classified as a backchannel")
	}
	if c.SilenceThreshold <= 0 {
		p = append(p, "turn: SilenceThreshold must be positive")
	}
	return p
}

// FloorDecision is the outcome of arbitration.
type FloorDecision int

const (
	// FloorGranted means the requester took the floor.
	FloorGranted FloorDecision = iota
	// FloorDenied means the current holder keeps it.
	FloorDenied
	// FloorBackchannel means the overlap was classified as a backchannel and
	// the floor did not move.
	FloorBackchannel
	// FloorQueued means the request was recorded and will apply when the
	// current non-yielding turn completes.
	FloorQueued
)

// String renders the decision for logs and metric labels.
func (d FloorDecision) String() string {
	switch d {
	case FloorGranted:
		return "granted"
	case FloorDenied:
		return "denied"
	case FloorBackchannel:
		return "backchannel"
	case FloorQueued:
		return "queued"
	default:
		return "unknown"
	}
}

// TurnManager owns floor control: who may speak, when, and what happens when
// two parties speak at once.
//
// HALF-DUPLEX BY CONSTRUCTION. At most one party holds the floor at any instant.
// Simultaneous audio is a physical fact of a phone line; simultaneous
// FLOOR OWNERSHIP is a modelling error, and this type makes it unrepresentable.
type TurnManager struct {
	cfg     TurnConfig
	clock   rt.Clock
	metrics *Metrics

	mu      sync.RWMutex
	current *Turn
	history []Turn
	nextID  TurnID

	// overlapStart is when the non-holder began speaking over the holder. Zero
	// when there is no overlap.
	overlapStart time.Time

	// queued records a floor request deferred because the current turn is
	// non-yielding. It exists so a barge-in during the greeting is honoured
	// after the greeting rather than discarded — INV-CV-2.
	queued *floorRequest

	// nonYielding marks the current turn as one the caller cannot take.
	nonYielding bool
}

type floorRequest struct {
	party Party
	at    time.Time
	kind  InterruptionKind
}

// NewTurnManager constructs a turn manager.
func NewTurnManager(cfg TurnConfig, clock rt.Clock, metrics *Metrics) (*TurnManager, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	if clock == nil {
		clock = rt.SystemClock{}
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	return &TurnManager{cfg: cfg, clock: clock, metrics: metrics}, nil
}

// Current returns a copy of the open turn, and whether one is open.
//
// A copy: handing out the live pointer would let a caller mutate floor state
// without synchronisation, which is the usual way a "thread-safe" type turns
// out not to be.
func (t *TurnManager) Current() (Turn, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.current == nil {
		return Turn{}, false
	}
	return *t.current, true
}

// Holder returns the party currently holding the floor.
func (t *TurnManager) Holder() Party {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.current == nil {
		return PartyNone
	}
	return t.current.Owner
}

// Acquire attempts to take the floor for a party.
//
// nonYielding marks a turn the caller may not take. It is true for exactly one
// thing — the opening announcement — and [INV-CV-2] depends on it.
func (t *TurnManager) Acquire(p Party, nonYielding bool) (Turn, FloorDecision) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.clock.Now()

	if t.current == nil {
		return t.openLocked(p, nonYielding, now), FloorGranted
	}
	if t.current.Owner == p {
		// Already held. Not an error: a party continuing to speak is normal.
		return *t.current, FloorGranted
	}

	// The floor is held by someone else. This is contention, and how it
	// resolves is [arbitrateLocked]'s job.
	decision := t.arbitrateLocked(p, now, InterruptionUser)
	switch decision {
	case FloorGranted:
		t.closeLocked(now, true, InterruptionUser)
		return t.openLocked(p, false, now), FloorGranted
	default:
		return *t.current, decision
	}
}

// openLocked starts a turn. Caller holds t.mu.
func (t *TurnManager) openLocked(p Party, nonYielding bool, now time.Time) Turn {
	t.nextID++
	t.current = &Turn{ID: t.nextID, Owner: p, StartedAt: now}
	t.nonYielding = nonYielding
	t.overlapStart = time.Time{}
	t.metrics.TurnsStarted.Inc(p.String())
	return *t.current
}

// closeLocked ends the open turn. Caller holds t.mu.
func (t *TurnManager) closeLocked(now time.Time, yielded bool, by InterruptionKind) {
	if t.current == nil {
		return
	}
	t.current.EndedAt = now
	t.current.Yielded = yielded
	if yielded {
		t.current.InterruptedBy = by
	}
	t.metrics.TurnDuration.Observe(t.current.Duration(now).Seconds(), t.current.Owner.String())
	if yielded {
		t.metrics.TurnsYielded.Inc(t.current.Owner.String(), by.String())
	}
	t.history = append(t.history, *t.current)
	t.current = nil
	t.nonYielding = false
	t.overlapStart = time.Time{}
}

// Release ends the open turn normally.
func (t *TurnManager) Release(p Party, expectation Expectation) (Turn, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.current == nil || t.current.Owner != p {
		return Turn{}, false
	}
	t.current.Expectation = expectation
	closed := *t.current
	closed.EndedAt = t.clock.Now()
	closed.Expectation = expectation

	t.closeLocked(t.clock.Now(), false, InterruptionNone)
	// closeLocked appended a copy without the expectation; correct the record
	// so the trace shows what the turn established.
	if n := len(t.history); n > 0 {
		t.history[n-1].Expectation = expectation
	}

	// A deferred request now applies. This is the greeting barge-in case:
	// the caller tried to take the floor during a non-yielding turn, and their
	// request was queued rather than discarded.
	if t.queued != nil {
		req := t.queued
		t.queued = nil
		t.openLocked(req.party, false, t.clock.Now())
		t.metrics.FloorDecisions.Inc(req.party.String(), FloorGranted.String())
	}
	return closed, true
}

// arbitrateLocked decides contention. Caller holds t.mu.
//
// The policy, and why:
//
//  1. A non-yielding turn is never taken. The request is QUEUED, not denied,
//     because the caller's intent to speak is real and discarding it would make
//     the system feel deaf. This is what lets INV-CV-2 hold without the caller
//     having to repeat themselves.
//  2. Brief overlap is a backchannel, not an interruption. Below
//     BackchannelMaxDuration the floor does not move.
//  3. Otherwise the caller wins. Always. An agent that competes with a human
//     for the floor on a phone call is intolerable, and no product requirement
//     outweighs that.
func (t *TurnManager) arbitrateLocked(requester Party, now time.Time, kind InterruptionKind) FloorDecision {
	// Emergency and transfer preempt everything, including a non-yielding turn.
	// U7 outranks I1's non-yielding greeting: an emergency mid-announcement
	// must still reach a human.
	if kind == InterruptionEmergency || kind == InterruptionTransfer {
		t.metrics.FloorDecisions.Inc(requester.String(), FloorGranted.String())
		return FloorGranted
	}

	if t.nonYielding {
		if t.queued == nil {
			t.queued = &floorRequest{party: requester, at: now, kind: kind}
		}
		t.metrics.FloorDecisions.Inc(requester.String(), FloorQueued.String())
		return FloorQueued
	}

	if t.overlapStart.IsZero() {
		t.overlapStart = now
	}
	overlap := now.Sub(t.overlapStart)

	if overlap < t.cfg.OverlapGrace {
		t.metrics.FloorDecisions.Inc(requester.String(), FloorBackchannel.String())
		return FloorBackchannel
	}
	if overlap <= t.cfg.BackchannelMaxDuration && requester == PartyCaller &&
		t.current.Owner == PartyAgent {
		// Still plausibly a backchannel. The agent keeps the floor.
		t.metrics.FloorDecisions.Inc(requester.String(), FloorBackchannel.String())
		return FloorBackchannel
	}

	// Sustained overlap. The caller wins; the agent does not.
	if requester == PartyCaller {
		t.metrics.FloorDecisions.Inc(requester.String(), FloorGranted.String())
		return FloorGranted
	}
	t.metrics.FloorDecisions.Inc(requester.String(), FloorDenied.String())
	return FloorDenied
}

// NoteOverlap records that a non-holder has begun producing audio.
//
// Called by the audio path on every frame of detected speech from the party
// that does not hold the floor. It is what makes backchannel classification
// possible: without a start time, every overlap looks instantaneous.
func (t *TurnManager) NoteOverlap(p Party) FloorDecision {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.current == nil || t.current.Owner == p {
		return FloorDenied
	}
	return t.arbitrateLocked(p, t.clock.Now(), InterruptionUser)
}

// ClearOverlap records that simultaneous speech has stopped without the floor
// changing hands — a completed backchannel.
func (t *TurnManager) ClearOverlap() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.overlapStart.IsZero() {
		t.metrics.Backchannels.Inc()
	}
	t.overlapStart = time.Time{}
}

// ForceYield takes the floor unconditionally for a party, for interruption
// kinds that do not negotiate.
func (t *TurnManager) ForceYield(p Party, kind InterruptionKind) Turn {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.clock.Now()
	if t.current != nil {
		t.closeLocked(now, true, kind)
	}
	t.queued = nil
	return t.openLocked(p, false, now)
}

// Overrunning reports whether the open turn has exceeded MaxTurnDuration.
func (t *TurnManager) Overrunning() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.current == nil {
		return false
	}
	return t.current.Duration(t.clock.Now()) > t.cfg.MaxTurnDuration
}

// History returns a copy of every completed turn.
func (t *TurnManager) History() []Turn {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]Turn, len(t.history))
	copy(out, t.history)
	return out
}

// Count returns the number of completed turns.
func (t *TurnManager) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.history)
}

// LastExpectation returns the expectation established by the most recent
// completed turn, which is how a constrained answer is interpreted.
func (t *TurnManager) LastExpectation() Expectation {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for i := len(t.history) - 1; i >= 0; i-- {
		if t.history[i].Owner == PartyAgent {
			return t.history[i].Expectation
		}
	}
	return ExpectNothing
}
