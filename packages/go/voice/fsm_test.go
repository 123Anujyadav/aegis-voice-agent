package voice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func fsmClock() *rt.FakeClock {
	return rt.NewFakeClock(time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC))
}

// newFSM builds a machine with a fake clock and a recording publisher.
func newFSM(t *testing.T) (*SessionFSM, *RecordingEventPublisher, *rt.FakeClock) {
	t.Helper()

	pub := NewRecordingEventPublisher()
	clock := fsmClock()

	f, err := NewSessionFSM(FSMConfig{
		Session:   SessionID("ses-fsm-1"),
		Call:      CallID("call-1"),
		Clock:     clock,
		Publisher: pub,
	})
	if err != nil {
		t.Fatalf("NewSessionFSM: %v", err)
	}
	return f, pub, clock
}

// drive moves a machine along a path, failing the test on any refusal.
func drive(t *testing.T, f *SessionFSM, states ...SessionState) {
	t.Helper()

	for _, s := range states {
		if err := f.To(context.Background(), s, ReasonOK); err != nil {
			t.Fatalf("driving to %s: %v", s, err)
		}
	}
}

// ---------------------------------------------------------------------------
// The table itself
// ---------------------------------------------------------------------------

// TestFSM_TableIsExhaustivelyCorrect walks every ordered pair of states.
//
// §4 requires that every valid AND invalid transition be tested. Enumerating
// them by hand would test the ones somebody thought of; enumerating the product
// of the state set with itself tests all 121, including the ones nobody thought
// of — which are precisely the ones an implicit transition would hide in.
func TestFSM_TableIsExhaustivelyCorrect(t *testing.T) {
	t.Parallel()

	states := AllSessionStates()
	if len(states) != 11 {
		t.Fatalf("the state set has %d members; this test and §4 both assume 11",
			len(states))
	}

	// The expectation, written independently of the table it checks. Stating it
	// as data here rather than reading transitionTable is the point: a test that
	// consulted the table would pass for any table.
	expected := map[SessionState]map[SessionState]bool{
		StateCreated:          {StateListening: true},
		StateListening:        {StateSpeakingDetected: true},
		StateSpeakingDetected: {StateTranscribing: true, StateListening: true},
		StateTranscribing:     {StateThinking: true, StateListening: true},
		StateThinking:         {StateSynthesizing: true, StateListening: true},
		StateSynthesizing: {
			StateSpeaking: true, StateInterrupted: true, StateListening: true,
		},
		StateSpeaking:    {StateListening: true, StateInterrupted: true},
		StateInterrupted: {StateListening: true},
		StateCancelled:   {},
		StateFailed:      {},
		StateCompleted:   {},
	}

	// Every non-terminal state may end, three ways.
	for _, from := range terminalReachableFrom() {
		for _, end := range []SessionState{StateCompleted, StateCancelled, StateFailed} {
			expected[from][end] = true
		}
	}

	var permitted, refused int
	for _, from := range states {
		for _, to := range states {
			want := expected[from][to]
			got := CanTransition(from, to)

			if got != want {
				verb := "permits"
				if want {
					verb = "refuses"
				}
				t.Errorf("the table %s %s -> %s, which it must not", verb, from, to)
			}
			if want {
				permitted++
				// An edge nobody can justify should not be in the table.
				if TransitionRationale(from, to) == "" {
					t.Errorf("%s -> %s is permitted with no stated rationale", from, to)
				}
			} else {
				refused++
			}
		}
	}

	t.Logf("state table: %d permitted edges, %d refused, %d ordered pairs checked",
		permitted, refused, permitted+refused)

	// A terminal state is terminal: nothing at all follows it.
	for _, from := range []SessionState{StateCancelled, StateFailed, StateCompleted} {
		if got := TransitionsFrom(from); len(got) != 0 {
			t.Errorf("%s is terminal but leads to %v", from, got)
		}
	}
}

func TestFSM_NoStateIsUnreachableAndNoneIsADeadEnd(t *testing.T) {
	t.Parallel()

	// A declared state nothing can reach is a state that will never be
	// observed, and a non-terminal state nothing leaves is a session that
	// hangs. Either is a table bug rather than a behaviour bug, so it is
	// checked structurally.
	reachable := map[SessionState]bool{StateCreated: true}
	for _, from := range AllSessionStates() {
		for _, to := range TransitionsFrom(from) {
			reachable[to] = true
		}
	}

	for _, s := range AllSessionStates() {
		if !reachable[s] {
			t.Errorf("%s is declared but no transition reaches it", s)
		}
		if !s.Terminal() && len(TransitionsFrom(s)) == 0 {
			t.Errorf("%s is not terminal but nothing leaves it: a session there hangs", s)
		}
	}
}

func TestFSM_EveryDeclaredEdgeIsExecutable(t *testing.T) {
	t.Parallel()

	// The table permitting an edge and the machine performing it are different
	// claims. This drives every permitted edge through a real machine.
	for _, from := range AllSessionStates() {
		for _, to := range TransitionsFrom(from) {
			name := fmt.Sprintf("%s->%s", from, to)
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				f := fsmAt(t, from)
				if err := f.To(context.Background(), to, ReasonOK); err != nil {
					t.Fatalf("the table permits %s but the machine refused it: %v",
						name, err)
				}
				if got := f.State(); got != to {
					t.Errorf("after %s the machine is in %s", name, got)
				}
			})
		}
	}
}

// fsmAt returns a machine parked in the given state, reached legally.
func fsmAt(t *testing.T, target SessionState) *SessionFSM {
	t.Helper()

	f, _, _ := newFSM(t)
	if target == StateCreated {
		return f
	}

	// Breadth-first through the declared table: the test must not know a route
	// by hand, or it would encode a second copy of the graph.
	type node struct {
		state SessionState
		path  []SessionState
	}
	queue := []node{{StateCreated, nil}}
	seen := map[SessionState]bool{StateCreated: true}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		for _, next := range TransitionsFrom(cur.state) {
			if seen[next] {
				continue
			}
			path := append(append([]SessionState(nil), cur.path...), next)
			if next == target {
				drive(t, f, path...)
				return f
			}
			seen[next] = true
			queue = append(queue, node{next, path})
		}
	}

	t.Fatalf("no legal route reaches %s", target)
	return nil
}

// ---------------------------------------------------------------------------
// Refusals
// ---------------------------------------------------------------------------

func TestFSM_RefusesAnUndeclaredTransition(t *testing.T) {
	t.Parallel()

	f, pub, _ := newFSM(t)
	drive(t, f, StateListening)

	// Skipping recognition entirely: the agent would be answering something
	// nobody said.
	err := f.To(context.Background(), StateSpeaking, ReasonOK)
	if err == nil {
		t.Fatal("listening -> speaking was permitted")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("want ErrInvalidTransition, got %v", err)
	}

	var te *TransitionError
	if !errors.As(err, &te) {
		t.Fatalf("want a *TransitionError, got %T", err)
	}
	if te.From != StateListening || te.To != StateSpeaking {
		t.Errorf("the error describes %s -> %s", te.From, te.To)
	}
	// The message must say what WAS allowed, or the reader has to open the
	// source to find out.
	if !strings.Contains(err.Error(), string(StateSpeakingDetected)) {
		t.Errorf("the error should list the permitted targets, got %v", err)
	}

	// A refused transition changes nothing.
	if got := f.State(); got != StateListening {
		t.Errorf("a refused transition moved the session to %s", got)
	}
	if n := pub.Count(EventStateChanged); n != 1 {
		t.Errorf("a refused transition published an event: %d state changes for 1 "+
			"successful move", n)
	}
}

func TestFSM_RefusalIsCountedSeparatelyFromSuccess(t *testing.T) {
	t.Parallel()

	// An invalid transition is a bug in the caller, and it must be visible as
	// one rather than blending into the ordinary transition count.
	m := NewVoiceMetrics()
	f, err := NewSessionFSM(FSMConfig{
		Session: SessionID("ses-metrics"),
		Clock:   fsmClock(),
		Metrics: m,
	})
	if err != nil {
		t.Fatalf("NewSessionFSM: %v", err)
	}

	drive(t, f, StateListening)
	_ = f.To(context.Background(), StateSpeaking, ReasonOK) // refused

	if got := m.StateTransitions.Value(string(StateCreated), string(StateListening)); got != 1 {
		t.Errorf("the valid transition counted %d times, want 1", got)
	}
	if got := m.InvalidTransitions.Value(string(StateListening), string(StateSpeaking)); got != 1 {
		t.Errorf("the invalid transition counted %d times, want 1", got)
	}
	if got := m.StateTransitions.Value(string(StateListening), string(StateSpeaking)); got != 0 {
		t.Errorf("a refused transition was counted as a real one (%d)", got)
	}
}

func TestFSM_NothingFollowsATerminalState(t *testing.T) {
	t.Parallel()

	for _, end := range []SessionState{StateCompleted, StateCancelled, StateFailed} {
		t.Run(string(end), func(t *testing.T) {
			t.Parallel()

			f, _, _ := newFSM(t)
			drive(t, f, StateListening)
			if err := f.To(context.Background(), end, ReasonCallEnded); err != nil {
				t.Fatalf("ending as %s: %v", end, err)
			}

			for _, to := range AllSessionStates() {
				if to == end {
					continue // the idempotent case, covered separately
				}
				if err := f.To(context.Background(), to, ReasonOK); err == nil {
					t.Errorf("%s -> %s was permitted after the session ended", end, to)
				}
			}
			if got := f.State(); got != end {
				t.Errorf("the session left its terminal state for %s", got)
			}
		})
	}
}

func TestFSM_EndingTwiceIsANoOpButChangingHowItEndedIsNot(t *testing.T) {
	t.Parallel()

	// A supervisor cancelling and a caller hanging up can both decide to end
	// the same session, from different goroutines. Erroring on whichever
	// arrived second turns an ordinary race into a logged fault.
	f, pub, _ := newFSM(t)
	drive(t, f, StateListening)

	if err := f.To(context.Background(), StateCancelled, ReasonRequested); err != nil {
		t.Fatalf("cancelling: %v", err)
	}
	if err := f.To(context.Background(), StateCancelled, ReasonRequested); err != nil {
		t.Errorf("ending twice must be a no-op, got %v", err)
	}

	// ...and it must not publish a second event or count a second transition:
	// one session ended once.
	changes := pub.OfType(EventStateChanged)
	if got := changes[len(changes)-1].To; got != StateCancelled {
		t.Errorf("the last event is %s", got)
	}
	if n := len(changes); n != 2 { // created->listening, listening->cancelled
		t.Errorf("got %d state-change events, want 2", n)
	}

	// But "was it cancelled or did it fail" has one true answer.
	if err := f.To(context.Background(), StateFailed, ReasonCrashed); err == nil {
		t.Error("a cancelled session was allowed to become failed")
	}
}

func TestFSM_RefusesAnUndeclaredState(t *testing.T) {
	t.Parallel()

	f, _, _ := newFSM(t)

	err := f.To(context.Background(), SessionState("daydreaming"), ReasonOK)
	if err == nil {
		t.Fatal("an undeclared state was accepted")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("want ErrInvalidTransition, got %v", err)
	}
}

func TestFSM_RefusesAReasonOutsideTheDeclaredVocabulary(t *testing.T) {
	t.Parallel()

	// The reason reaches an event that leaves this process. If a caller could
	// pass an arbitrary string, a recogniser's output or a model's output could
	// become one — which is exactly what classifications.go exists to prevent.
	f, _, _ := newFSM(t)

	err := f.To(context.Background(), StateListening,
		"the caller said their account number is 4111 1111 1111 1111")
	if err == nil {
		t.Fatal("an arbitrary reason string was accepted")
	}
	if !strings.Contains(err.Error(), "classifications.go") {
		t.Errorf("the error should point at the vocabulary, got %v", err)
	}
	if got := f.State(); got != StateCreated {
		t.Errorf("a refused reason still moved the session to %s", got)
	}

	// Every declared code is accepted.
	for _, reason := range allReasonCodes() {
		fresh, _, _ := newFSM(t)
		if err := fresh.To(context.Background(), StateListening, reason); err != nil {
			t.Errorf("the declared reason %q was refused: %v", reason, err)
		}
	}
}

// ---------------------------------------------------------------------------
// What a transition records
// ---------------------------------------------------------------------------

func TestFSM_PublishesOneEventPerTransition(t *testing.T) {
	t.Parallel()

	f, pub, clock := newFSM(t)

	drive(t, f, StateListening, StateSpeakingDetected)
	clock.Advance(250 * time.Millisecond)
	if err := f.To(context.Background(), StateTranscribing, ReasonOK); err != nil {
		t.Fatalf("To: %v", err)
	}

	events := pub.OfType(EventStateChanged)
	if len(events) != 3 {
		t.Fatalf("got %d events for 3 transitions", len(events))
	}

	last := events[2]
	if last.From != StateSpeakingDetected || last.To != StateTranscribing {
		t.Errorf("the event describes %s -> %s", last.From, last.To)
	}
	if last.Session != SessionID("ses-fsm-1") || last.Call != CallID("call-1") {
		t.Errorf("the event lost its identifiers: %+v", last)
	}
	if last.DurationMillis != 250 {
		t.Errorf("time in the previous state is %dms, want 250", last.DurationMillis)
	}

	// Sequence orders a session's events so a consumer can detect a gap after
	// fanning out.
	for i, e := range events {
		if e.Sequence != i+1 {
			t.Errorf("event %d has sequence %d", i, e.Sequence)
		}
	}
}

func TestFSM_EventsCarryTheTurnWithoutItBeingThreadedThrough(t *testing.T) {
	t.Parallel()

	f, pub, _ := newFSM(t)
	drive(t, f, StateListening, StateSpeakingDetected)

	f.SetTurn(TurnID("turn-7"))
	if err := f.To(context.Background(), StateTranscribing, ReasonOK); err != nil {
		t.Fatalf("To: %v", err)
	}

	events := pub.OfType(EventStateChanged)
	if got := events[len(events)-1].Turn; got != TurnID("turn-7") {
		t.Errorf("the event names turn %q, want turn-7", got)
	}
	if got := f.Turn(); got != TurnID("turn-7") {
		t.Errorf("Turn is %q", got)
	}
}

func TestFSM_HistoryIsRecordedAndBounded(t *testing.T) {
	t.Parallel()

	// An unbounded slice per session is a memory leak that presents as a slow
	// crash days later, and a thirty-minute call makes thousands of these.
	f, err := NewSessionFSM(FSMConfig{
		Session:    SessionID("ses-history"),
		Clock:      fsmClock(),
		MaxHistory: 4,
	})
	if err != nil {
		t.Fatalf("NewSessionFSM: %v", err)
	}

	drive(t, f, StateListening)
	for i := 0; i < 10; i++ {
		drive(t, f, StateSpeakingDetected, StateListening)
	}

	history := f.History()
	if len(history) != 4 {
		t.Fatalf("history holds %d entries against a bound of 4", len(history))
	}

	// The transitions that explain how a call ended are the ones at the end.
	if last := history[len(history)-1]; last.To != StateListening {
		t.Errorf("the newest entry is %s, want the most recent transition", last)
	}
	if got := f.Sequence(); got != 21 {
		t.Errorf("Sequence is %d; history is bounded but the count is not", got)
	}

	// A caller holding the returned slice cannot edit the session's record.
	history[0].Reason = "tampered"
	if f.History()[0].Reason == "tampered" {
		t.Error("History returns the machine's own slice")
	}
}

func TestFSM_TimeInStateComesFromTheInjectedClock(t *testing.T) {
	t.Parallel()

	f, _, clock := newFSM(t)
	drive(t, f, StateListening)

	clock.Advance(3 * time.Second)
	if got := f.TimeInState(); got != 3*time.Second {
		t.Errorf("TimeInState is %s, want 3s", got)
	}

	drive(t, f, StateSpeakingDetected)
	if got := f.TimeInState(); got != 0 {
		t.Errorf("entering a state did not reset the timer: %s", got)
	}
}

// ---------------------------------------------------------------------------
// The states' own predicates
// ---------------------------------------------------------------------------

func TestSessionState_AgentHoldsFloorMatchesTheBargeInEdges(t *testing.T) {
	t.Parallel()

	// Barge-in is only meaningful where the agent holds the floor, and it must
	// be possible from exactly those states. Two independent declarations —
	// the predicate and the table — must agree, or a barge-in arrives in a
	// state that cannot act on it.
	for _, s := range AllSessionStates() {
		holdsFloor := s.AgentHoldsFloor()
		canInterrupt := CanTransition(s, StateInterrupted)

		if holdsFloor != canInterrupt {
			t.Errorf("%s: AgentHoldsFloor=%v but the table %s an interruption",
				s, holdsFloor,
				map[bool]string{true: "permits", false: "refuses"}[canInterrupt])
		}
	}
}

func TestSessionState_TerminalStatesAcceptNoAudio(t *testing.T) {
	t.Parallel()

	// Inbound audio keeps flowing while the agent speaks — that is how a
	// barge-in is heard at all — so the only states that stop are the ended
	// ones.
	for _, s := range AllSessionStates() {
		if s.AcceptsAudio() == s.Terminal() {
			t.Errorf("%s: AcceptsAudio=%v and Terminal=%v; a live session must "+
				"accept audio and an ended one must not", s, s.AcceptsAudio(), s.Terminal())
		}
	}
}

// ---------------------------------------------------------------------------
// Construction and concurrency
// ---------------------------------------------------------------------------

func TestFSM_RefusesAnInvalidConfiguration(t *testing.T) {
	t.Parallel()

	if _, err := NewSessionFSM(FSMConfig{Session: SessionID("")}); err == nil {
		t.Error("a machine with no session identifier was built")
	}
	if _, err := NewSessionFSM(FSMConfig{
		Session: SessionID("ses-1"), MaxHistory: -1,
	}); err == nil {
		t.Error("a negative history bound was accepted")
	}

	// A caller that does not care about metrics or events must not have to
	// build either.
	f, err := NewSessionFSM(FSMConfig{Session: SessionID("ses-minimal")})
	if err != nil {
		t.Fatalf("a minimal configuration was refused: %v", err)
	}
	if err := f.To(context.Background(), StateListening, ReasonOK); err != nil {
		t.Errorf("a machine with no publisher cannot transition: %v", err)
	}
	if got := f.State(); got != StateListening {
		t.Errorf("state is %s", got)
	}
}

func TestFSM_StartsInCreated(t *testing.T) {
	t.Parallel()

	f, _, _ := newFSM(t)
	if got := f.State(); got != StateCreated {
		t.Errorf("a new session is in %s, want created", got)
	}
	if f.Terminal() {
		t.Error("a new session reports itself terminal")
	}
	if got := f.Sequence(); got != 0 {
		t.Errorf("a new session has made %d transitions", got)
	}
}

func TestFSM_IsSafeUnderConcurrentTransitions(t *testing.T) {
	t.Parallel()

	// The audio path, the barge-in detector and a supervisor all move a
	// session and do not coordinate with each other. Exactly one of them must
	// win each transition, and the loser must be told it lost.
	f, pub, _ := newFSM(t)
	drive(t, f, StateListening, StateSpeakingDetected, StateTranscribing,
		StateThinking, StateSynthesizing, StateSpeaking)

	const racers = 16
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
	)

	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			// Every racer attempts the same barge-in.
			if err := f.To(context.Background(), StateInterrupted, ReasonBargeIn); err == nil {
				mu.Lock()
				succeeded++
				mu.Unlock()
			}
		}()
	}

	close(start)
	wg.Wait()

	if succeeded != 1 {
		t.Errorf("%d of %d racing transitions succeeded; exactly one must win",
			succeeded, racers)
	}
	if got := f.State(); got != StateInterrupted {
		t.Errorf("the session is in %s", got)
	}

	// One transition, one event. A second would tell a consumer the caller
	// interrupted twice.
	interrupts := 0
	for _, e := range pub.OfType(EventStateChanged) {
		if e.To == StateInterrupted {
			interrupts++
		}
	}
	if interrupts != 1 {
		t.Errorf("%d interruption events published for one interruption", interrupts)
	}
}

func TestFSM_ReadsAreSafeWhileTransitionsHappen(t *testing.T) {
	t.Parallel()

	f, _, _ := newFSM(t)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = f.State()
				_ = f.History()
				_ = f.Sequence()
				_ = f.TimeInState()
				_ = f.Terminal()
				_ = f.Turn()
			}
		}
	}()

	for i := 0; i < 200; i++ {
		_ = f.To(context.Background(), StateListening, ReasonOK)
		_ = f.To(context.Background(), StateSpeakingDetected, ReasonOK)
	}

	close(stop)
	wg.Wait()
}

// ---------------------------------------------------------------------------
// A whole turn, and a barge-in
// ---------------------------------------------------------------------------

func TestFSM_RunsACompleteTurnAndReturnsToListening(t *testing.T) {
	t.Parallel()

	f, pub, _ := newFSM(t)

	drive(t, f,
		StateListening, StateSpeakingDetected, StateTranscribing,
		StateThinking, StateSynthesizing, StateSpeaking, StateListening)

	if got := f.State(); got != StateListening {
		t.Errorf("after a full turn the session is in %s", got)
	}
	if got := f.Sequence(); got != 7 {
		t.Errorf("a full turn made %d transitions, want 7", got)
	}

	// The turn is repeatable: a call is many turns, and a machine that could
	// only run one would end the call after the first answer.
	drive(t, f,
		StateSpeakingDetected, StateTranscribing, StateThinking,
		StateSynthesizing, StateSpeaking, StateListening)

	if n := pub.Count(EventStateChanged); n != 13 {
		t.Errorf("two turns published %d events, want 13", n)
	}
}

func TestFSM_BargeInInterruptsSpeechAndReturnsTheFloor(t *testing.T) {
	t.Parallel()

	f, _, _ := newFSM(t)
	drive(t, f,
		StateListening, StateSpeakingDetected, StateTranscribing,
		StateThinking, StateSynthesizing, StateSpeaking)

	if err := f.To(context.Background(), StateInterrupted, ReasonBargeIn); err != nil {
		t.Fatalf("barge-in: %v", err)
	}

	// The floor returns to the caller, and a new turn can start from there.
	if err := f.To(context.Background(), StateListening, ReasonBargeIn); err != nil {
		t.Fatalf("returning the floor: %v", err)
	}
	if err := f.To(context.Background(), StateSpeakingDetected, ReasonOK); err != nil {
		t.Fatalf("a new turn after a barge-in: %v", err)
	}
}

func TestFSM_ATurnMayEndWithNothingToSay(t *testing.T) {
	t.Parallel()

	// Silence, unrecognisable audio and a governance refusal all produce a turn
	// with no response. None is a failure, and routing any of them through
	// StateFailed would make an ordinary silence look like an outage.
	cases := []struct {
		name   string
		from   SessionState
		reason string
	}{
		{"a false onset", StateSpeakingDetected, ReasonEmptyTranscript},
		{"nothing recognised", StateTranscribing, ReasonEmptyTranscript},
		{"governance refused", StateThinking, ReasonGovernanceDeny},
		{"no audio synthesised", StateSynthesizing, ReasonInvalidOutput},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := fsmAt(t, tc.from)
			if err := f.To(context.Background(), StateListening, tc.reason); err != nil {
				t.Fatalf("%s -> listening: %v", tc.from, err)
			}
			if f.State() != StateListening {
				t.Errorf("the session is in %s", f.State())
			}
			if f.Terminal() {
				t.Error("a turn with nothing to say ended the session")
			}
		})
	}
}

func TestFSM_ACallCanEndFromAnyLiveState(t *testing.T) {
	t.Parallel()

	// A caller hangs up mid-sentence. Forcing the session through a fake
	// intermediate state to reach an end would be inventing a transition that
	// did not happen.
	for _, from := range terminalReachableFrom() {
		t.Run(string(from), func(t *testing.T) {
			t.Parallel()

			f := fsmAt(t, from)
			if err := f.To(context.Background(), StateCompleted, ReasonCallEnded); err != nil {
				t.Fatalf("a call ending in %s: %v", from, err)
			}
			if !f.Terminal() {
				t.Error("the session did not end")
			}
		})
	}
}
