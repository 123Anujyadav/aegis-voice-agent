package speech

import (
	"testing"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

func testClock() *rt.FakeClock {
	return rt.NewFakeClock(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
}

func seg(turn TurnID, sess SessionID, n uint64, text string, final bool) TranscriptSegment {
	return TranscriptSegment{
		Session: sess, Turn: turn, Segment: NewSegmentID(),
		Sequence: n, Text: text, IsFinal: final,
		Confidence: 0.9, Language: LangEnglishIN, Role: RoleCaller,
		StartTime: 0, EndTime: time.Duration(n) * 100 * time.Millisecond,
	}
}

// ---------------------------------------------------------------------------
// Transcript assembly — mandatory cases 1-4
// ---------------------------------------------------------------------------

// Mandatory case 1: partial -> partial -> final.
func TestAssembler_PartialPartialFinal(t *testing.T) {
	t.Parallel()
	sess := NewSessionID()
	turn := NewTurnID()
	a := NewTranscriptAssembler(sess, testClock())

	for i, text := range []string{"hello", "hello there", "hello there friend"} {
		res, err := a.Apply(seg(turn, sess, uint64(i+1), text, false))
		if err != nil {
			t.Fatalf("partial %d: %v", i+1, err)
		}
		if !res.Applied {
			t.Fatalf("partial %d was not applied: %s", i+1, res.Reason)
		}
	}
	got, ok := a.Partial(turn)
	if !ok || got.Text != "hello there friend" {
		t.Fatalf("partial = %q (ok=%v), want the newest", got.Text, ok)
	}

	res, err := a.Apply(seg(turn, sess, 4, "hello there friend.", true))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied {
		t.Fatalf("final was not applied: %s", res.Reason)
	}
	fin, ok := a.Final(turn)
	if !ok || fin.Text != "hello there friend." {
		t.Fatalf("final = %q (ok=%v)", fin.Text, ok)
	}
	// The live partial is cleared by finalisation: a consumer rendering the
	// partial alongside the final would show the caller's words twice.
	if _, ok := a.Partial(turn); ok {
		t.Error("a partial survived finalisation")
	}
}

// Mandatory case 2: a stale partial after the final must not rewrite it.
func TestAssembler_StalePartialAfterFinalIsRejected(t *testing.T) {
	t.Parallel()
	sess := NewSessionID()
	turn := NewTurnID()
	a := NewTranscriptAssembler(sess, testClock())

	if _, err := a.Apply(seg(turn, sess, 2, "final text", true)); err != nil {
		t.Fatal(err)
	}
	res, err := a.Apply(seg(turn, sess, 1, "stale partial", false))
	if err != nil {
		t.Fatalf("a stale partial must be reported, not errored: %v", err)
	}
	if res.Applied {
		t.Fatal("a stale partial rewrote a finalised turn")
	}
	if res.Reason != ReasonAfterFinal {
		t.Errorf("reason = %s, want after_final", res.Reason)
	}
	fin, _ := a.Final(turn)
	if fin.Text != "final text" {
		t.Errorf("final was rewritten to %q", fin.Text)
	}
}

// Mandatory case 3: a second final for the same turn must be refused.
func TestAssembler_DuplicateFinalIsRefused(t *testing.T) {
	t.Parallel()
	sess := NewSessionID()
	turn := NewTurnID()
	a := NewTranscriptAssembler(sess, testClock())

	if _, err := a.Apply(seg(turn, sess, 1, "first final", true)); err != nil {
		t.Fatal(err)
	}
	res, err := a.Apply(seg(turn, sess, 2, "second final", true))
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied {
		t.Fatal("a turn was finalised twice")
	}
	if res.Reason != ReasonDoubleFinal {
		t.Errorf("reason = %s, want double_final", res.Reason)
	}
	fin, _ := a.Final(turn)
	if fin.Text != "first final" {
		t.Errorf("the first final was overwritten: %q", fin.Text)
	}
}

// Mandatory case 4: a partial behind the committed sequence is out of order.
func TestAssembler_OutOfOrderPartialIsRejected(t *testing.T) {
	t.Parallel()
	sess := NewSessionID()
	turn := NewTurnID()
	a := NewTranscriptAssembler(sess, testClock())

	if _, err := a.Apply(seg(turn, sess, 5, "fifth", false)); err != nil {
		t.Fatal(err)
	}
	res, err := a.Apply(seg(turn, sess, 3, "third", false))
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied {
		t.Fatal("an out-of-order partial was applied")
	}
	if res.Reason != ReasonOutOfOrder {
		t.Errorf("reason = %s, want out_of_order", res.Reason)
	}
	got, _ := a.Partial(turn)
	if got.Text != "fifth" {
		t.Errorf("partial regressed to %q", got.Text)
	}
}

func TestAssembler_DuplicateSequenceIsDeduplicated(t *testing.T) {
	t.Parallel()
	sess := NewSessionID()
	turn := NewTurnID()
	a := NewTranscriptAssembler(sess, testClock())

	if _, err := a.Apply(seg(turn, sess, 1, "once", false)); err != nil {
		t.Fatal(err)
	}
	res, _ := a.Apply(seg(turn, sess, 1, "once", false))
	if res.Applied {
		t.Fatal("a duplicate sequence was applied twice")
	}
	if res.Reason != ReasonDuplicate {
		t.Errorf("reason = %s, want duplicate", res.Reason)
	}
}

// Mandatory case 25 (part): cross-session contamination must be impossible.
func TestAssembler_RefusesForeignSession(t *testing.T) {
	t.Parallel()
	a := NewTranscriptAssembler(NewSessionID(), testClock())
	other := seg(NewTurnID(), NewSessionID(), 1, "not mine", false)

	if _, err := a.Apply(other); err == nil {
		t.Fatal("a segment from another session was accepted")
	}
}

func TestAssembler_RejectsInvalidSegment(t *testing.T) {
	t.Parallel()
	sess := NewSessionID()
	a := NewTranscriptAssembler(sess, testClock())

	bad := seg(NewTurnID(), sess, 1, "x", false)
	bad.Confidence = 5 // outside 0..1 and not ConfidenceUnknown
	if _, err := a.Apply(bad); err == nil {
		t.Fatal("an invalid segment was accepted")
	}
}

func TestAssembler_SegmentsPreserveTurnOrder(t *testing.T) {
	t.Parallel()
	sess := NewSessionID()
	a := NewTranscriptAssembler(sess, testClock())

	var turns []TurnID
	for i := 0; i < 3; i++ {
		turn := NewTurnID()
		turns = append(turns, turn)
		if _, err := a.Apply(seg(turn, sess, 1, "text", true)); err != nil {
			t.Fatal(err)
		}
	}
	got := a.Segments()
	if len(got) != 3 {
		t.Fatalf("got %d finalised segments, want 3", len(got))
	}
	for i, s := range got {
		if s.Turn != turns[i] {
			t.Errorf("segment %d is turn %s, want %s", i, s.Turn, turns[i])
		}
	}
}

// Transcript text must never appear in the redacted rendering, which is what
// logs and events use.
func TestTranscriptSegment_RedactedOmitsText(t *testing.T) {
	t.Parallel()
	s := seg(NewTurnID(), NewSessionID(), 1, "my account number is 12345", false)
	if got := s.Redacted(); contains(got, "account") || contains(got, "12345") {
		t.Errorf("Redacted() leaked transcript content: %q", got)
	}
	// String() must be redacted too: the commonest leak is %v while debugging.
	if got := s.String(); contains(got, "account") {
		t.Errorf("String() leaked transcript content: %q", got)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}()
}

// ---------------------------------------------------------------------------
// Speech turn state machine
// ---------------------------------------------------------------------------

func TestTurn_NineStatesExist(t *testing.T) {
	t.Parallel()
	if got := len(AllTurnStates()); got != 9 {
		t.Fatalf("%d states declared, want 9", got)
	}
	for _, s := range AllTurnStates() {
		if !s.Valid() {
			t.Errorf("%s is listed but not valid", s)
		}
	}
}

func TestTurn_NoImplicitTransitions(t *testing.T) {
	t.Parallel()
	// Listening cannot jump straight to Final: a turn that never passed through
	// Finalizing was never endpointed, and a transcript nobody endpointed is a
	// transcript nobody can be sure is complete.
	if CanTurnTransition(TurnListening, TurnFinal) {
		t.Error("listening -> final must not be declared")
	}
	// The three terminal states go nowhere.
	for _, term := range []SpeechTurnState{TurnCancelled, TurnInterrupted, TurnFailed} {
		for _, s := range AllTurnStates() {
			if CanTurnTransition(term, s) {
				t.Errorf("%s -> %s must not be declared; %s is terminal", term, s, term)
			}
		}
	}
}

func TestTurn_HappyPath(t *testing.T) {
	t.Parallel()
	m := NewSpeechTurnManager(testClock())
	turn, err := m.Begin(RoleCaller)
	if err != nil {
		t.Fatal(err)
	}
	if turn.State() != TurnListening {
		t.Fatalf("a new turn starts %s, want listening", turn.State())
	}
	for _, to := range []SpeechTurnState{
		TurnPartial, TurnFinalizing, TurnFinal, TurnResponding, TurnSpeaking,
	} {
		if err := m.Transition(turn.ID, to, "test"); err != nil {
			t.Fatalf("-> %s: %v", to, err)
		}
	}
}

func TestTurn_UndeclaredTransitionIsRefused(t *testing.T) {
	t.Parallel()
	m := NewSpeechTurnManager(testClock())
	turn, err := m.Begin(RoleCaller)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Transition(turn.ID, TurnFinal, "skip"); err == nil {
		t.Fatal("an undeclared transition was accepted")
	}
	if turn.State() != TurnListening {
		t.Errorf("a refused transition still moved the turn to %s", turn.State())
	}
}

func TestTurn_OnlyOneActiveTurn(t *testing.T) {
	t.Parallel()
	m := NewSpeechTurnManager(testClock())
	if _, err := m.Begin(RoleCaller); err != nil {
		t.Fatal(err)
	}
	// A second Begin while one is live would produce duplicate turns, which is
	// exactly what the brief forbids.
	if _, err := m.Begin(RoleCaller); err == nil {
		t.Fatal("a duplicate concurrent turn was created")
	}
}

func TestTurn_InterruptedStartsANewTurn(t *testing.T) {
	t.Parallel()
	m := NewSpeechTurnManager(testClock())
	first, err := m.Begin(RoleCaller)
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []SpeechTurnState{
		TurnPartial, TurnFinalizing, TurnFinal, TurnResponding, TurnSpeaking,
	} {
		if err := m.Transition(first.ID, to, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Transition(first.ID, TurnInterrupted, "barge_in"); err != nil {
		t.Fatal(err)
	}
	second, err := m.Begin(RoleCaller)
	if err != nil {
		t.Fatalf("an interrupted turn blocked the next one: %v", err)
	}
	if second.ID == first.ID {
		t.Fatal("the interrupted turn was reused instead of a new one being created")
	}
}

func TestTurn_HistoryIsBounded(t *testing.T) {
	t.Parallel()
	m := NewSpeechTurnManager(testClock())
	turn, err := m.Begin(RoleCaller)
	if err != nil {
		t.Fatal(err)
	}
	// A long utterance revises its interim transcript dozens of times. That
	// must NOT grow the turn history: it is one state change followed by many
	// no-ops, not a hundred transitions.
	for i := 0; i < maxTurnHistory*3; i++ {
		if err := m.NotePartial(turn.ID); err != nil {
			t.Fatal(err)
		}
	}
	if turn.State() != TurnPartial {
		t.Errorf("state = %s after partials, want partial", turn.State())
	}
	if got := len(turn.History()); got != 1 {
		t.Errorf("history holds %d entries after %d partials, want 1",
			got, maxTurnHistory*3)
	}
}

func TestTurn_PartialAfterFinalizingIsRefused(t *testing.T) {
	t.Parallel()
	m := NewSpeechTurnManager(testClock())
	turn, err := m.Begin(RoleCaller)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Transition(turn.ID, TurnFinalizing, "endpoint"); err != nil {
		t.Fatal(err)
	}
	// A provider emitting a partial after we endpointed is stale by definition.
	if err := m.NotePartial(turn.ID); err == nil {
		t.Fatal("a partial was accepted for a turn that was already finalizing")
	}
}

func TestTurn_UnknownTurnIsRefused(t *testing.T) {
	t.Parallel()
	m := NewSpeechTurnManager(testClock())
	if err := m.Transition(NewTurnID(), TurnPartial, "ghost"); err == nil {
		t.Fatal("a transition on an unknown turn was accepted")
	}
}
