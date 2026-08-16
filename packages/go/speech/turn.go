package speech

import (
	"fmt"
	"sort"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// SpeechTurnState is one of the nine states a speech turn may occupy.
//
// A string rather than an integer. These values reach metric labels, event
// payloads and log lines; an integer enum renumbers itself the moment somebody
// inserts a constant in the middle.
type SpeechTurnState string

// The nine speech turn states, in lifecycle order.
const (
	// TurnListening is receiving caller audio. The initial state.
	TurnListening SpeechTurnState = "listening"

	// TurnPartial has received at least one interim transcript.
	TurnPartial SpeechTurnState = "partial"

	// TurnFinalizing has been endpointed and is awaiting the final transcript.
	//
	// Distinct from Partial because this is where the ADR-0005 budget applies —
	// 120 ms p50 / 250 ms p95 from end-of-speech to final — and a state that
	// conflated "still talking" with "waiting for the final" would make that
	// measurement meaningless.
	TurnFinalizing SpeechTurnState = "finalizing"

	// TurnFinal holds an immutable transcript, ready to hand off.
	TurnFinal SpeechTurnState = "final"

	// TurnResponding is generating response text.
	TurnResponding SpeechTurnState = "responding"

	// TurnSpeaking is playing synthesised audio to the caller.
	TurnSpeaking SpeechTurnState = "speaking"

	// TurnInterrupted was cut off by the caller speaking. Terminal.
	//
	// Terminal FOR THIS TURN on purpose: barge-in creates a NEW turn rather
	// than resuming this one. A resumed turn would have two beginnings and no
	// single point at which the caller took the floor, which makes the
	// conversation record ambiguous about who said what when.
	TurnInterrupted SpeechTurnState = "interrupted"

	// TurnCancelled was abandoned by the platform — session close, shutdown, or
	// an upstream cancellation. Terminal.
	TurnCancelled SpeechTurnState = "cancelled"

	// TurnFailed hit an unrecoverable error. Terminal.
	TurnFailed SpeechTurnState = "failed"
)

// AllTurnStates returns every state in lifecycle order.
func AllTurnStates() []SpeechTurnState {
	return []SpeechTurnState{
		TurnListening, TurnPartial, TurnFinalizing, TurnFinal,
		TurnResponding, TurnSpeaking, TurnInterrupted, TurnCancelled, TurnFailed,
	}
}

// String implements fmt.Stringer.
func (s SpeechTurnState) String() string { return string(s) }

// Valid reports whether the state is one of the declared nine.
func (s SpeechTurnState) Valid() bool {
	for _, known := range AllTurnStates() {
		if s == known {
			return true
		}
	}
	return false
}

// Terminal reports whether nothing may follow.
func (s SpeechTurnState) Terminal() bool {
	return s == TurnInterrupted || s == TurnCancelled || s == TurnFailed
}

// AcceptsAudio reports whether inbound caller audio belongs to this turn.
//
// Listening and Partial only. Once endpointed, further audio belongs to the
// NEXT turn — which is what makes barge-in during Speaking produce a new turn
// rather than appending to the one being interrupted.
func (s SpeechTurnState) AcceptsAudio() bool {
	return s == TurnListening || s == TurnPartial
}

// turnTransitions is THE declaration of every legal move.
//
// The single source of truth for the speech turn model. Nothing else decides
// whether a transition is allowed, and no code path assigns a state directly —
// runtime.FSM refuses anything absent here.
//
//	listening → partial → finalizing → final → responding → speaking
//	     ↓         ↓           ↓         ↓          ↓           ↓
//	  cancelled/failed at any point; interrupted from responding or speaking
func turnTransitions() map[SpeechTurnState][]SpeechTurnState {
	return map[SpeechTurnState][]SpeechTurnState{
		// A very short utterance is endpointed without ever producing a partial,
		// so listening reaches finalizing directly. It cannot reach final: that
		// would be a transcript nobody endpointed.
		TurnListening: {TurnPartial, TurnFinalizing, TurnCancelled, TurnFailed},

		// NOT a self-transition. A long utterance revises its interim transcript
		// dozens of times, but that is an EVENT WITHIN the Partial state, not a
		// state change — runtime.FSM refuses self-transitions for exactly this
		// reason, and it is right to. Use [SpeechTurnManager.NotePartial], which
		// transitions on the first partial and is a no-op thereafter.
		TurnPartial: {TurnFinalizing, TurnCancelled, TurnFailed},

		TurnFinalizing: {TurnFinal, TurnCancelled, TurnFailed},

		TurnFinal: {TurnResponding, TurnCancelled, TurnFailed},

		// Interruption becomes possible here: the agent has started producing a
		// reply, so there is something for the caller to talk over.
		TurnResponding: {TurnSpeaking, TurnInterrupted, TurnCancelled, TurnFailed},

		TurnSpeaking: {TurnInterrupted, TurnCancelled, TurnFailed},

		TurnInterrupted: nil,
		TurnCancelled:   nil,
		TurnFailed:      nil,
	}
}

// CanTurnTransition reports whether from → to is declared.
//
// Exported so a caller can ask before attempting. That matters on the
// provider-facing path: a late callback for a turn that has already been
// interrupted should be discarded quietly rather than logged as an error.
func CanTurnTransition(from, to SpeechTurnState) bool {
	for _, allowed := range turnTransitions()[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// TurnTransitionsFrom returns the states reachable from one state, sorted.
func TurnTransitionsFrom(s SpeechTurnState) []SpeechTurnState {
	out := append([]SpeechTurnState(nil), turnTransitions()[s]...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// maxTurnHistory bounds a turn's retained transitions.
//
// Partial → Partial repeats for the length of an utterance, so an unbounded
// history grows with how long somebody talks.
const maxTurnHistory = 32

// TurnTransition is one entry in a turn's history.
type TurnTransition struct {
	From   SpeechTurnState
	To     SpeechTurnState
	Reason string
	At     time.Time
}

// String renders the transition.
func (t TurnTransition) String() string {
	return fmt.Sprintf("%s->%s (%s)", t.From, t.To, t.Reason)
}

// SpeechTurn is one utterance's audio lifecycle.
type SpeechTurn struct {
	ID   TurnID
	Role Role

	fsm   *rt.FSM[SpeechTurnState]
	clock rt.Clock

	mu        sync.RWMutex
	startedAt time.Time
	history   []TurnTransition
}

// State returns the current state.
func (t *SpeechTurn) State() SpeechTurnState { return t.fsm.State() }

// Terminal reports whether the turn has concluded.
func (t *SpeechTurn) Terminal() bool { return t.State().Terminal() }

// StartedAt returns when the turn began.
func (t *SpeechTurn) StartedAt() time.Time {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.startedAt
}

// Age returns how long the turn has been live.
func (t *SpeechTurn) Age() time.Duration { return t.clock.Since(t.StartedAt()) }

// History returns a copy of the turn's transitions, oldest first.
func (t *SpeechTurn) History() []TurnTransition {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]TurnTransition(nil), t.history...)
}

// SpeechTurnManager sequences speech turns within one session.
//
// # It is not conversation.TurnManager, and the distinction matters
//
// packages/go/conversation owns the DIALOGUE FLOOR: who is expected to speak,
// what response is expected, and what happens when an interruption is resumed
// (Party, Expectation, FloorDecision, ResumePolicy, Checkpoint). This type owns
// the AUDIO LIFECYCLE of one utterance: is audio arriving, has it been
// endpointed, is a transcript final, is synthesis playing.
//
// They are different layers answering different questions, and this package
// does not import that one — see the package doc. A service composes them: this
// manager emits the signals (final transcript, interruption) that a dialogue
// floor decides what to do about.
//
// # One live turn at a time
//
// Begin refuses while a non-terminal turn exists. That single rule is what
// prevents duplicate turns, out-of-order results being attributed to the wrong
// turn, and two provider streams writing into one transcript.
type SpeechTurnManager struct {
	clock rt.Clock

	mu      sync.RWMutex
	turns   map[TurnID]*SpeechTurn
	order   []TurnID
	current TurnID
}

// NewSpeechTurnManager builds a turn manager.
func NewSpeechTurnManager(clock rt.Clock) *SpeechTurnManager {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	return &SpeechTurnManager{clock: clock, turns: make(map[TurnID]*SpeechTurn)}
}

// Begin starts a new turn in TurnListening.
//
// Refuses while a non-terminal turn is live.
func (m *SpeechTurnManager) Begin(role Role) (*SpeechTurn, error) {
	if !role.Valid() {
		return nil, fmt.Errorf("%w: role %q is not declared", ErrInvalidTranscript, role)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if cur := m.turns[m.current]; cur != nil && !cur.Terminal() {
		return nil, fmt.Errorf(
			"%w: turn %s is still %s; conclude it before beginning another",
			ErrInternalFailure, cur.ID, cur.State())
	}

	fsm, err := rt.NewFSM(rt.FSMSpec[SpeechTurnState]{
		Initial:     TurnListening,
		Transitions: turnTransitions(),
		Terminal:    []SpeechTurnState{TurnInterrupted, TurnCancelled, TurnFailed},
	}, m.clock)
	if err != nil {
		return nil, fmt.Errorf("%w: turn fsm: %v", ErrInternalFailure, err)
	}

	turn := &SpeechTurn{
		ID: NewTurnID(), Role: role, fsm: fsm, clock: m.clock,
		startedAt: m.clock.Now(),
	}
	m.turns[turn.ID] = turn
	m.order = append(m.order, turn.ID)
	m.current = turn.ID
	m.evictLocked()
	return turn, nil
}

// evictLocked bounds retained turns. Caller holds the lock.
func (m *SpeechTurnManager) evictLocked() {
	const maxTurns = 256
	for len(m.order) > maxTurns {
		oldest := m.order[0]
		m.order = m.order[1:]
		if oldest != m.current {
			delete(m.turns, oldest)
		}
	}
}

// Transition moves a turn, recording the reason.
//
// THE ONLY WAY A TURN CHANGES STATE. There is no setter, and the FSM refuses
// anything the transition table does not declare.
func (m *SpeechTurnManager) Transition(id TurnID, to SpeechTurnState, reason string) error {
	m.mu.RLock()
	turn := m.turns[id]
	m.mu.RUnlock()

	if turn == nil {
		return fmt.Errorf("%w: no turn %s", ErrInternalFailure, id)
	}

	from := turn.State()
	if _, err := turn.fsm.To(to); err != nil {
		return fmt.Errorf("%w: turn %s: %s -> %s: %v",
			ErrInternalFailure, id, from, to, err)
	}

	turn.mu.Lock()
	turn.history = append(turn.history, TurnTransition{
		From: from, To: to, Reason: reason, At: m.clock.Now(),
	})
	if len(turn.history) > maxTurnHistory {
		turn.history = turn.history[len(turn.history)-maxTurnHistory:]
	}
	turn.mu.Unlock()
	return nil
}

// NotePartial records that an interim transcript arrived for a turn.
//
// Transitions Listening → Partial on the first one and does nothing on every
// subsequent one. A repeated partial is not a state change: the turn is still
// receiving audio and still revising the same interim text, and treating each
// revision as a transition would both be refused by the FSM and grow the turn
// history with the length of the utterance.
func (m *SpeechTurnManager) NotePartial(id TurnID) error {
	m.mu.RLock()
	turn := m.turns[id]
	m.mu.RUnlock()

	if turn == nil {
		return fmt.Errorf("%w: no turn %s", ErrInternalFailure, id)
	}
	switch turn.State() {
	case TurnPartial:
		return nil // already there; nothing to record
	case TurnListening:
		return m.Transition(id, TurnPartial, "partial_received")
	default:
		return fmt.Errorf("%w: turn %s is %s and cannot accept a partial",
			ErrTranscriptOutOfOrder, id, turn.State())
	}
}

// Turn returns a turn by identifier.
func (m *SpeechTurnManager) Turn(id TurnID) (*SpeechTurn, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.turns[id]
	return t, ok
}

// Active returns the live turn, if there is one.
func (m *SpeechTurnManager) Active() (*SpeechTurn, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t := m.turns[m.current]
	if t == nil || t.Terminal() {
		return nil, false
	}
	return t, true
}

// Len returns how many turns are retained.
func (m *SpeechTurnManager) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.turns)
}
