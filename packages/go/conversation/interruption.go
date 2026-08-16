package conversation

import (
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// InterruptionKind classifies why a turn was cut short.
type InterruptionKind int

const (
	// InterruptionNone is the absence of an interruption.
	InterruptionNone InterruptionKind = iota

	// InterruptionUser is the caller barging in. The ordinary case, and the
	// one the whole floor-control design is built around.
	InterruptionUser

	// InterruptionAI is the agent stopping itself — a safety verdict arriving
	// mid-utterance, or a plan superseded by new information.
	InterruptionAI

	// InterruptionProvider is an upstream failure: the model stream died, the
	// speech pipeline stalled.
	InterruptionProvider

	// InterruptionEmergency is emergency intent. It preempts everything,
	// including a non-yielding greeting, and it never resumes.
	InterruptionEmergency

	// InterruptionTransfer is the call being routed elsewhere.
	InterruptionTransfer
)

// String renders the kind for logs and metric labels.
func (k InterruptionKind) String() string {
	switch k {
	case InterruptionUser:
		return "user"
	case InterruptionAI:
		return "ai"
	case InterruptionProvider:
		return "provider"
	case InterruptionEmergency:
		return "emergency"
	case InterruptionTransfer:
		return "transfer"
	default:
		return "none"
	}
}

// Priority orders interruptions. Higher wins when two arrive together.
//
// Emergency is highest and is the only kind that outranks a non-yielding turn.
// That ordering is U7 expressed as a number: an emergency during the opening
// announcement must still reach a human, even though the announcement is
// otherwise uninterruptible under I1.
func (k InterruptionKind) Priority() int {
	switch k {
	case InterruptionEmergency:
		return 5
	case InterruptionTransfer:
		return 4
	case InterruptionProvider:
		return 3
	case InterruptionAI:
		return 2
	case InterruptionUser:
		return 1
	default:
		return 0
	}
}

// Preemptive reports whether the kind takes the floor without negotiating.
func (k InterruptionKind) Preemptive() bool {
	switch k {
	case InterruptionEmergency, InterruptionTransfer, InterruptionProvider, InterruptionAI:
		return true
	}
	return false
}

// ResumePolicy says what happens to the interrupted work.
type ResumePolicy int

const (
	// ResumeAbandon discards the interrupted utterance entirely.
	//
	// The correct policy for a user barge-in: a caller who interrupts does not
	// want the rest of the sentence when they finish. Replaying it is the most
	// irritating behaviour a voice system can have.
	ResumeAbandon ResumePolicy = iota

	// ResumeFromCheckpoint continues from the last committed point. Used for a
	// provider failure, where the caller heard part of a legitimate answer and
	// the remainder is still wanted.
	ResumeFromCheckpoint

	// ResumeRestart replays the interrupted turn from its beginning. Used
	// sparingly — when the caller almost certainly missed it, such as a
	// provider failure within the first moments of speech.
	ResumeRestart

	// ResumeNever is terminal: the conversation does not return here.
	ResumeNever
)

// String renders the policy for logs and metric labels.
func (p ResumePolicy) String() string {
	switch p {
	case ResumeFromCheckpoint:
		return "checkpoint"
	case ResumeRestart:
		return "restart"
	case ResumeNever:
		return "never"
	default:
		return "abandon"
	}
}

// Interruption is one interruption event.
type Interruption struct {
	// Kind classifies it.
	Kind InterruptionKind
	// By names the party responsible.
	By Party
	// At is when it was raised, on the engine's clock.
	At time.Time
	// Reason is a short machine-readable code. Never caller content.
	Reason string
	// InterruptedTurn is the turn that was cut short, if any.
	InterruptedTurn TurnID
	// Resume is the policy applied.
	Resume ResumePolicy
}

// Checkpoint is a resumable position within an agent turn.
//
// The engine records position, never content: what was said is the transcript's
// business, and duplicating it here would create a second copy of SENSITIVE
// data with weaker handling.
type Checkpoint struct {
	// TurnID identifies the interrupted turn.
	TurnID TurnID
	// Offset is how far into the planned output the turn reached, in
	// implementation-defined units the layer above assigns.
	Offset int
	// PlanRef identifies the plan being executed.
	PlanRef string
	// At is when the checkpoint was taken.
	At time.Time
}

// InterruptionEngine records interruptions, decides resume policy, and holds
// resumable checkpoints.
//
// It does not move the floor — that is [TurnManager]'s job — and it does not
// change state, which is [Conversation]'s. Separating "what kind of
// interruption is this and what should happen to the interrupted work" from
// "who holds the floor now" keeps both testable in isolation.
type InterruptionEngine struct {
	clock   rt.Clock
	metrics *Metrics

	mu          sync.RWMutex
	history     []Interruption
	checkpoint  *Checkpoint
	emergencyAt time.Time
}

// NewInterruptionEngine constructs an interruption engine.
func NewInterruptionEngine(clock rt.Clock, metrics *Metrics) *InterruptionEngine {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	return &InterruptionEngine{clock: clock, metrics: metrics}
}

// Checkpoint records a resumable position. Called by the layer above as output
// is committed.
func (e *InterruptionEngine) Checkpoint(cp Checkpoint) {
	e.mu.Lock()
	defer e.mu.Unlock()
	cp.At = e.clock.Now()
	e.checkpoint = &cp
}

// LastCheckpoint returns the most recent checkpoint, if any.
func (e *InterruptionEngine) LastCheckpoint() (Checkpoint, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.checkpoint == nil {
		return Checkpoint{}, false
	}
	return *e.checkpoint, true
}

// Raise records an interruption and returns it with its resume policy applied.
func (e *InterruptionEngine) Raise(kind InterruptionKind, by Party, reason string, turn TurnID) Interruption {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := e.clock.Now()
	i := Interruption{
		Kind:            kind,
		By:              by,
		At:              now,
		Reason:          reason,
		InterruptedTurn: turn,
		Resume:          resumePolicyFor(kind, e.checkpoint, now),
	}
	if kind == InterruptionEmergency {
		e.emergencyAt = now
	}

	e.history = append(e.history, i)
	e.metrics.Interruptions.Inc(kind.String())
	e.metrics.ResumeOutcomes.Inc(i.Resume.String())
	return i
}

// resumePolicyFor decides what happens to the interrupted work.
//
// The reasoning per kind:
//
//	user      — abandon. A caller who interrupts has changed the subject; the
//	            rest of the sentence is unwanted noise when they stop.
//	ai        — abandon. The agent stopped itself because what it was saying was
//	            wrong or unsafe. Resuming it would defeat the interruption.
//	provider  — resume. The caller heard a legitimate partial answer and the
//	            remainder is still wanted. Restart instead when almost nothing
//	            was delivered, since replaying two words is not annoying and
//	            resuming from two words in is incoherent.
//	emergency — never. The conversation is over; a human has it now.
//	transfer  — never. Same.
func resumePolicyFor(kind InterruptionKind, cp *Checkpoint, now time.Time) ResumePolicy {
	switch kind {
	case InterruptionEmergency, InterruptionTransfer:
		return ResumeNever
	case InterruptionUser, InterruptionAI:
		return ResumeAbandon
	case InterruptionProvider:
		if cp == nil {
			return ResumeRestart
		}
		// A checkpoint taken within the last 400 ms means very little was
		// delivered; restarting reads better than resuming mid-clause.
		if now.Sub(cp.At) < 400*time.Millisecond {
			return ResumeRestart
		}
		return ResumeFromCheckpoint
	default:
		return ResumeAbandon
	}
}

// EmergencyRaised reports whether an emergency interruption has occurred.
//
// Once true it stays true. An emergency is irreversible for the conversation
// (U7), and a flag that could be cleared would permit resuming a call that must
// not resume.
func (e *InterruptionEngine) EmergencyRaised() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return !e.emergencyAt.IsZero()
}

// History returns a copy of every recorded interruption.
func (e *InterruptionEngine) History() []Interruption {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Interruption, len(e.history))
	copy(out, e.history)
	return out
}

// Count returns the number of interruptions of a kind. Passing
// InterruptionNone returns the total.
func (e *InterruptionEngine) Count(kind InterruptionKind) int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if kind == InterruptionNone {
		return len(e.history)
	}
	n := 0
	for _, i := range e.history {
		if i.Kind == kind {
			n++
		}
	}
	return n
}

// stateAfterInterruption returns the state a conversation should occupy once an
// interruption has been arbitrated.
//
// Kept here rather than in the state machine because the mapping is a property
// of the interruption, and putting it in the transition table would mean
// encoding five near-identical paths.
func stateAfterInterruption(kind InterruptionKind) State {
	switch kind {
	case InterruptionEmergency:
		return StateEscalated
	case InterruptionTransfer:
		return StateTransferred
	case InterruptionProvider:
		return StateError
	default:
		// User and AI interruptions hand the floor to the caller.
		return StateListening
	}
}
