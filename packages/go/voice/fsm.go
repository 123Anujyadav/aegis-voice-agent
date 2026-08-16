package voice

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// ---------------------------------------------------------------------------
// The session state machine
// ---------------------------------------------------------------------------
//
// # Every edge is declared, and nothing else is reachable
//
// [sessionTransitions] below is the whole table. A state change that is not an
// entry in it fails, is counted, and leaves the session where it was. There is
// no default branch, no "if it looks close enough", and no path that moves a
// session by assignment — the field is unexported and [SessionFSM.To] is the
// only thing that writes it.
//
// The reason for that strictness is what an implicit transition costs here. A
// session that slipped from speaking to listening without the interruption
// being orchestrated would leave a synthesiser still producing audio for a turn
// the caller has already talked over, and the symptom is the agent talking over
// the caller — the single most damaging thing a voice agent can do. It would
// also be invisible: no error, no event, just a session in a state nobody moved
// it to.
//
// TestFSM_TableIsExhaustivelyCorrect walks all 121 ordered pairs and asserts
// each is permitted exactly when the table says so, so "every valid and invalid
// transition is tested" is a property of the test rather than a claim about it.

// transition is one declared edge, with the reason it exists.
//
// The `why` is not documentation decoration: it is the justification an edge
// had when it was added, and an edge nobody can justify is an edge that should
// not be in the table.
type transition struct {
	from SessionState
	to   SessionState
	why  string
}

// sessionTransitions is the complete set of permitted state changes.
//
// Read it as the session's whole life. The steady-state cycle first, then the
// departures from it, then the ends.
var sessionTransitions = []transition{
	// --- The steady-state cycle -------------------------------------------
	{StateCreated, StateListening,
		"the session begins listening; the only way out of created"},
	{StateListening, StateSpeakingDetected,
		"Phase 11D confirmed a speech onset"},
	{StateSpeakingDetected, StateTranscribing,
		"a recognition stream is open and receiving audio"},
	{StateTranscribing, StateThinking,
		"a final transcript exists; planning, governance and generation follow"},
	{StateThinking, StateSynthesizing,
		"a response is being generated and its first chunk has gone to the voice"},
	{StateSynthesizing, StateSpeaking,
		"the first synthesised frame is on its way to the caller"},
	{StateSpeaking, StateListening,
		"the turn finished speaking; the floor returns to the caller"},

	// --- Turns that end early, without a fault ----------------------------
	//
	// Each of these is a turn producing nothing to say. None is a failure, and
	// routing them through failed would make an ordinary silence look like an
	// outage.
	{StateSpeakingDetected, StateListening,
		"the onset did not become speech worth recognising; a false onset is " +
			"noise, not a fault"},
	{StateTranscribing, StateListening,
		"recognition produced nothing — silence, or audio that was not speech"},
	{StateThinking, StateListening,
		"the turn concluded with nothing to say, including when governance " +
			"refused the action"},
	{StateSynthesizing, StateListening,
		"the synthesiser produced no audio for the response"},

	// --- Barge-in ---------------------------------------------------------
	//
	// From both states where the agent holds the floor. Synthesizing counts
	// because the turn is committed even if the first frame has not left, and a
	// caller talking then is interrupting.
	{StateSynthesizing, StateInterrupted,
		"the caller spoke over a turn that had been committed but not yet heard"},
	{StateSpeaking, StateInterrupted,
		"the caller spoke over audio already playing"},
	{StateInterrupted, StateListening,
		"the interruption has been orchestrated: synthesis stopped and stale " +
			"audio blocked; the floor is the caller's"},
}

// terminalReachableFrom is every non-terminal state, each of which may end.
//
// # A call can end at any moment, and the table must say so rather than imply it
//
// A caller hangs up mid-sentence, a supervisor cancels, a provider dies. Each
// of those can arrive in any state, and forcing a session through a fake
// intermediate state to reach an end would be inventing a transition that did
// not happen — the same dishonesty as an implicit one, just spelled differently.
//
// Which END it is carries the meaning: completed is a call that finished,
// cancelled was asked to stop, failed hit a fault it could not recover from.
func terminalReachableFrom() []SessionState {
	return []SessionState{
		StateCreated, StateListening, StateSpeakingDetected, StateTranscribing,
		StateThinking, StateSynthesizing, StateSpeaking, StateInterrupted,
	}
}

// transitionTable is the compiled lookup, built once.
var transitionTable = buildTransitionTable()

func buildTransitionTable() map[SessionState]map[SessionState]string {
	table := make(map[SessionState]map[SessionState]string, len(AllSessionStates()))

	add := func(from, to SessionState, why string) {
		if table[from] == nil {
			table[from] = make(map[SessionState]string)
		}
		table[from][to] = why
	}

	for _, t := range sessionTransitions {
		add(t.from, t.to, t.why)
	}
	for _, from := range terminalReachableFrom() {
		add(from, StateCompleted, "the call ended")
		add(from, StateCancelled, "the session was asked to stop")
		add(from, StateFailed, "an unrecoverable fault ended the session")
	}
	return table
}

// CanTransition reports whether the table permits a state change.
//
// Exported because the pipeline needs to ask before acting, and because a
// caller that has to attempt a transition to discover it is illegal will
// produce an InvalidTransitions count for an ordinary question.
func CanTransition(from, to SessionState) bool {
	_, ok := transitionTable[from][to]
	return ok
}

// TransitionsFrom returns the states reachable from one state, sorted.
func TransitionsFrom(from SessionState) []SessionState {
	out := make([]SessionState, 0, len(transitionTable[from]))
	for to := range transitionTable[from] {
		out = append(out, to)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// TransitionRationale returns why an edge exists, or "" if it does not.
func TransitionRationale(from, to SessionState) string {
	return transitionTable[from][to]
}

// ---------------------------------------------------------------------------

// FSMConfig configures one session's state machine.
type FSMConfig struct {
	// Session identifies the session. Required.
	Session SessionID

	// Call is the call being served, carried for correlation. Optional:
	// Phase 11A owns call identity and a session may exist before one is known.
	Call CallID

	// Clock stamps transitions. Nil means the system clock.
	Clock rt.Clock

	// Metrics counts transitions. Nil means a fresh set, so a caller that does
	// not care about metrics need not build one.
	Metrics *VoiceMetrics

	// Publisher receives state-change events. Nil means none are published.
	Publisher EventPublisher

	// MaxHistory bounds the retained transition history.
	//
	// BOUNDED, BECAUSE A CALL IS LONG. A session that recorded every transition
	// of a thirty-minute call would hold thousands, and an unbounded slice per
	// session is a memory leak that presents as a slow crash days later.
	// Zero means [DefaultMaxHistory].
	MaxHistory int
}

// DefaultMaxHistory is how many recent transitions a session keeps.
//
// Enough to see the last several turns, which is what a support engineer reads
// when asked why a call went wrong, and small enough that ten thousand
// concurrent sessions cost megabytes rather than gigabytes.
const DefaultMaxHistory = 64

// StateChange is one recorded transition.
type StateChange struct {
	From     SessionState
	To       SessionState
	Reason   string
	At       time.Time
	Sequence int

	// InPrevious is how long the session held the state it just left.
	InPrevious time.Duration
}

// String renders the change, content-free.
func (c StateChange) String() string {
	return fmt.Sprintf("%s->%s (%s) after %s", c.From, c.To, c.Reason, c.InPrevious)
}

// SessionFSM is one voice session's lifecycle state.
//
// Safe for concurrent use: the audio path, the barge-in detector and a
// supervisor all move a session, and they do not coordinate with each other.
type SessionFSM struct {
	session SessionID
	call    CallID
	clock   rt.Clock
	metrics *VoiceMetrics
	pub     EventPublisher
	maxHist int

	mu        sync.RWMutex
	state     SessionState
	turn      TurnID
	enteredAt time.Time
	seq       int
	history   []StateChange
}

// NewSessionFSM builds a state machine in [StateCreated].
func NewSessionFSM(cfg FSMConfig) (*SessionFSM, error) {
	var problems []string

	if !cfg.Session.Valid() {
		problems = append(problems, fmt.Sprintf(
			"session identifier %q is not a valid label", cfg.Session))
	}
	if cfg.MaxHistory < 0 {
		problems = append(problems, "MaxHistory must not be negative")
	}
	if len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}

	clock := cfg.Clock
	if clock == nil {
		clock = rt.SystemClock{}
	}
	metrics := cfg.Metrics
	if metrics == nil {
		metrics = NewVoiceMetrics()
	}
	maxHist := cfg.MaxHistory
	if maxHist == 0 {
		maxHist = DefaultMaxHistory
	}

	return &SessionFSM{
		session:   cfg.Session,
		call:      cfg.Call,
		clock:     clock,
		metrics:   metrics,
		pub:       cfg.Publisher,
		maxHist:   maxHist,
		state:     StateCreated,
		enteredAt: clock.Now(),
	}, nil
}

// Session returns the identifier.
func (f *SessionFSM) Session() SessionID { return f.session }

// State returns the current state.
func (f *SessionFSM) State() SessionState {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.state
}

// Turn returns the current turn identifier, which is empty outside a turn.
func (f *SessionFSM) Turn() TurnID {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.turn
}

// SetTurn records which turn the session is serving.
//
// Carried by the FSM because every state-change event names a turn, and a
// caller threading it through each To call would eventually pass the wrong one.
func (f *SessionFSM) SetTurn(id TurnID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.turn = id
}

// Terminal reports whether the session has ended.
func (f *SessionFSM) Terminal() bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.state.Terminal()
}

// TimeInState returns how long the current state has been held.
func (f *SessionFSM) TimeInState() time.Duration {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.clock.Since(f.enteredAt)
}

// Sequence returns how many transitions have been made.
func (f *SessionFSM) Sequence() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.seq
}

// History returns the retained transitions, oldest first.
func (f *SessionFSM) History() []StateChange {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return append([]StateChange(nil), f.history...)
}

// To moves the session, refusing any change the table does not declare.
//
// # Ending twice is not an error
//
// A supervisor cancelling and a caller hanging up can both decide to end the
// same session, from different goroutines, with neither aware of the other.
// Returning an error to whichever arrived second would turn an ordinary race
// into a logged fault, so a transition into the terminal state the session is
// already in is a no-op — and only that. Moving from one terminal state to a
// different one still fails, because "was it cancelled or did it fail" is a
// question with one true answer.
//
// # The reason must come from the declared vocabulary
//
// It reaches an event that leaves this process. Every code is a constant in
// classifications.go, so nothing a recogniser or a model produced can become
// one.
func (f *SessionFSM) To(ctx context.Context, to SessionState, reason string) error {
	f.mu.Lock()

	from := f.state

	// The idempotent end, taken before validation: it is not a transition.
	if from == to && to.Terminal() {
		f.mu.Unlock()
		return nil
	}

	if err := f.checkLocked(to, reason); err != nil {
		f.mu.Unlock()
		f.metrics.InvalidTransitions.Inc(string(from), string(to))
		return err
	}

	now := f.clock.Now()
	held := now.Sub(f.enteredAt)

	f.state = to
	f.enteredAt = now
	f.seq++

	change := StateChange{
		From: from, To: to, Reason: reason,
		At: now, Sequence: f.seq, InPrevious: held,
	}
	f.history = append(f.history, change)
	if len(f.history) > f.maxHist {
		// Drop the oldest. Keeping the newest is right: the transitions that
		// explain how a call ended are the ones at the end of it.
		f.history = f.history[len(f.history)-f.maxHist:]
	}

	turn, seq := f.turn, f.seq
	f.mu.Unlock()

	// OUTSIDE THE LOCK. Publishing is somebody else's code and may be slow;
	// holding the session's lock across it would let a broker's latency block
	// the audio path.
	f.metrics.StateTransitions.Inc(string(from), string(to))
	f.publish(ctx, VoiceEvent{
		Type:           EventStateChanged,
		Session:        f.session,
		Turn:           turn,
		Call:           f.call,
		From:           from,
		To:             to,
		Reason:         reason,
		DurationMillis: held.Milliseconds(),
		Sequence:       seq,
		At:             now,
	})
	return nil
}

// checkLocked validates a proposed transition. Caller holds the write lock.
func (f *SessionFSM) checkLocked(to SessionState, reason string) error {
	from := f.state

	switch {
	case !to.Valid():
		return &TransitionError{Session: f.session, From: from, To: to,
			Reason: "the target is not one of the declared states"}

	case from.Terminal():
		return &TransitionError{Session: f.session, From: from, To: to,
			Reason: "the session has already ended; nothing follows a terminal state"}

	case !validReason(reason):
		return &TransitionError{Session: f.session, From: from, To: to,
			Reason: fmt.Sprintf("reason %q is not a declared code; a reason "+
				"reaches an event that leaves this process, so it comes from the "+
				"vocabulary in classifications.go and never from provider output",
				reason)}

	case !CanTransition(from, to):
		return &TransitionError{Session: f.session, From: from, To: to,
			Reason: "no such edge is declared; the permitted targets are " +
				formatStates(TransitionsFrom(from))}
	}
	return nil
}

// publish emits an event, never failing the transition that produced it.
//
// A broker outage cannot be allowed to stop calls being handled — the event
// port says so explicitly — so the error is deliberately dropped here rather
// than returned.
func (f *SessionFSM) publish(ctx context.Context, e VoiceEvent) {
	if f.pub == nil {
		return
	}
	_ = f.pub.Publish(ctx, e)
}

// validReason reports whether the code is in the declared vocabulary.
func validReason(reason string) bool {
	for _, known := range allReasonCodes() {
		if reason == known {
			return true
		}
	}
	return false
}

// formatStates renders a state list for an error message.
func formatStates(states []SessionState) string {
	if len(states) == 0 {
		return "(none)"
	}
	out := make([]string, 0, len(states))
	for _, s := range states {
		out = append(out, string(s))
	}
	return "[" + joinStrings(out, " ") + "]"
}

func joinStrings(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	s := parts[0]
	for _, p := range parts[1:] {
		s += sep + p
	}
	return s
}
