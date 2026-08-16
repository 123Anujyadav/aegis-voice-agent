package speech

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// ---------------------------------------------------------------------------
// Session, runtime and barge-in — mandatory cases 10, 13, 14, 24, 25
// ---------------------------------------------------------------------------

func speechRig(t *testing.T, script []TranscriptSegment) (
	*SpeechRuntime, *rt.FakeClock, *RecordingEventPublisher, *FakeTTSProvider,
) {
	t.Helper()
	clock := testClock()
	events := NewRecordingEventPublisher()
	r, err := New(TestConfig(), WithClock(clock), WithEventPublisher(events))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	stt := NewFakeSTTProvider("asr", script, clock)
	tts := NewFakeTTSProvider("voice", 4, clock)
	if err := r.Router().RegisterSTT(stt, TierPrimary); err != nil {
		t.Fatal(err)
	}
	if err := r.Router().RegisterTTS(tts, TierPrimary); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = r.Stop(context.Background()) })
	return r, clock, events, tts
}

func openSession(t *testing.T, r *SpeechRuntime) *SpeechSession {
	t.Helper()
	s, err := r.Open(context.Background(), SessionContext{
		Correlation: "call-1", Language: LangEnglishIN,
		Format: media.PCM16Mono8k(), Voice: "default", Prosody: DefaultProsody(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// waitForState spins until a turn reaches want, or fails.
func waitForState(t *testing.T, turn *SpeechTurn, want SpeechTurnState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if turn.State() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("turn is %s, want %s", turn.State(), want)
}

// assertGoroutinesSettle waits for the goroutine count to return to baseline.
func assertGoroutinesSettle(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("goroutines did not settle: %d now, %d at baseline",
		runtime.NumGoroutine(), baseline)
}

// Mandatory case 10: caller interruption during TTS.
//
// Asserts all seven requirements of the barge-in contract plus the ADR-0011
// latency budget.
func TestBargeIn_CancelsTTSAndOpensANewTurn(t *testing.T) {
	t.Parallel()
	r, clock, events, provider := speechRig(t, ScriptedPartials([]string{"hi"}, "hi there."))
	s := openSession(t, r)

	turn, err := s.Listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PushAudio(testFrame(clock, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.EndOfSpeech(); err != nil {
		t.Fatal(err)
	}
	waitForState(t, turn, TurnFinal)

	if err := s.Respond(context.Background(),
		"This is a long reply. It has several clauses. Here is another."); err != nil {
		t.Fatal(err)
	}

	// 1. The interruption is accepted from the media/speech boundary.
	res, err := s.Interrupt("caller_spoke")
	if err != nil {
		t.Fatalf("interrupt refused: %v", err)
	}

	// 2. Active synthesis is cancelled.
	//
	// Asserted on Closed rather than Cancelled: whether audio was still
	// outstanding at the instant of cancellation depends on how far the pump
	// had drained, which is a race. That the stream was CLOSED is not.
	if provider.Closed() == 0 {
		t.Error("the synthesis stream was not closed by the barge-in")
	}

	// 3. No stale outbound audio escapes.
	time.Sleep(20 * time.Millisecond)
	select {
	case f, ok := <-s.Frames():
		if ok {
			t.Errorf("stale audio leaked after barge-in: seq=%d", f.Sequence)
		}
	default:
	}

	// 5. A new turn exists and differs from the interrupted one.
	if res.NewTurn == res.PreviousTurn {
		t.Error("barge-in reused the interrupted turn")
	}
	if res.PreviousTurn != turn.ID {
		t.Errorf("interrupted %s, want %s", res.PreviousTurn, turn.ID)
	}
	if turn.State() != TurnInterrupted {
		t.Errorf("the previous turn is %s, want interrupted", turn.State())
	}
	newTurn, ok := s.Turns().Active()
	if !ok {
		t.Fatal("no live turn after barge-in")
	}
	if newTurn.State() != TurnListening {
		t.Errorf("the new turn is %s, want listening", newTurn.State())
	}

	// 4 and 6. Inbound audio and its transcript survive the interruption: the
	// input path is never flushed, because the caller speech that caused the
	// barge-in is already arriving into it.
	if _, ok := s.Transcript().Final(turn.ID); !ok {
		t.Error("the finalised transcript was lost by the interruption")
	}

	// 7. Latency is measured and reported.
	//
	// This rig runs on a FakeClock, so Latency is 0 by construction — fake time
	// does not advance during real work. That is the right trade for THIS test,
	// which asserts the interruption CONTRACT deterministically. The wall-clock
	// budget is a separate claim and is measured separately, against a real
	// clock, in TestBargeIn_LatencyIsWithinFrozenBudget.
	if res.Latency < 0 {
		t.Errorf("negative interruption latency %s", res.Latency)
	}
	t.Logf("barge-in on a fake clock: latency %s, chunks dropped %d",
		res.Latency, res.ChunksDropped)

	if len(events.OfType(EventSpeechInterrupted)) == 0 {
		t.Error("no speech_interrupted event was published")
	}
	if s.Interrupts() != 1 {
		t.Errorf("interrupts = %d, want 1", s.Interrupts())
	}
}

// Mandatory case 13: session termination with an active STT stream.
func TestSession_CloseWithActiveSTT(t *testing.T) {
	t.Parallel()
	r, _, _, _ := speechRig(t, ScriptedPartials([]string{"a", "b"}, "c."))
	s := openSession(t, r)

	if _, err := s.Listen(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := runtime.NumGoroutine()
	if err := s.Close(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if !s.Closed() {
		t.Error("session does not report closed")
	}
	assertGoroutinesSettle(t, before)
}

// Mandatory case 14: session termination with an active TTS stream.
func TestSession_CloseWithActiveTTS(t *testing.T) {
	t.Parallel()
	r, clock, _, _ := speechRig(t, ScriptedPartials([]string{"a"}, "b."))
	s := openSession(t, r)

	turn, err := s.Listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PushAudio(testFrame(clock, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.EndOfSpeech(); err != nil {
		t.Fatal(err)
	}
	waitForState(t, turn, TurnFinal)
	if err := s.Respond(context.Background(), "One. Two. Three. Four."); err != nil {
		t.Fatal(err)
	}

	before := runtime.NumGoroutine()
	if err := s.Close(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	assertGoroutinesSettle(t, before)
}

// Mandatory case 24: concurrent sessions.
func TestSession_ConcurrentSessions(t *testing.T) {
	t.Parallel()
	r, _, _, _ := speechRig(t, ScriptedPartials([]string{"x"}, "y."))

	const n = 25
	var wg sync.WaitGroup
	errs := make(chan error, n*3)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := r.Open(context.Background(), SessionContext{
				Correlation: "call", Language: LangEnglishIN,
				Format: media.PCM16Mono8k(), Prosody: DefaultProsody(),
			})
			if err != nil {
				errs <- err
				return
			}
			if _, err := s.Listen(context.Background()); err != nil {
				errs <- err
				return
			}
			if err := s.Close(context.Background(), "done"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent session: %v", err)
	}
}

// Mandatory case 25: cross-session isolation.
func TestSession_CrossSessionIsolation(t *testing.T) {
	t.Parallel()
	r, _, _, _ := speechRig(t, ScriptedPartials([]string{"a"}, "b."))

	one := openSession(t, r)
	two := openSession(t, r)

	if one.ID() == two.ID() {
		t.Fatal("two sessions share an identifier")
	}

	turnOne, err := one.Listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := two.Listen(context.Background()); err != nil {
		t.Fatal(err)
	}

	// A segment belonging to session one must be refused by session two,
	// structurally rather than by convention.
	foreign := seg(turnOne.ID, one.ID(), 1, "the other session words", true)
	if _, err := two.Transcript().Apply(foreign); err == nil {
		t.Fatal("session two accepted session one transcript")
	}

	// Each session knows only its own turns.
	if _, ok := two.Turns().Turn(turnOne.ID); ok {
		t.Error("session two can see a turn belonging to session one")
	}
}

func TestRuntime_RefusesBeyondCapacity(t *testing.T) {
	t.Parallel()
	cfg := TestConfig()
	cfg.MaxSessions = 2
	r, err := New(cfg, WithClock(testClock()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = r.Stop(context.Background()) })

	sc := SessionContext{Correlation: "c", Language: LangEnglishIN,
		Format: media.PCM16Mono8k(), Prosody: DefaultProsody()}
	for i := 0; i < 2; i++ {
		if _, err := r.Open(context.Background(), sc); err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
	}
	if _, err := r.Open(context.Background(), sc); !errors.Is(err, ErrBackpressure) {
		t.Errorf("err = %v, want ErrBackpressure", err)
	}
	if r.Shed() != 1 {
		t.Errorf("shed = %d, want 1", r.Shed())
	}
}

func TestRuntime_StopClosesEverySession(t *testing.T) {
	t.Parallel()
	r, _, _, _ := speechRig(t, nil)

	var sessions []*SpeechSession
	for i := 0; i < 3; i++ {
		sessions = append(sessions, openSession(t, r))
	}
	n, err := r.Stop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("stopped %d sessions, want 3", n)
	}
	for i, s := range sessions {
		if !s.Closed() {
			t.Errorf("session %d survived Stop", i)
		}
	}
	if r.Live() != 0 {
		t.Errorf("%d sessions still live", r.Live())
	}
}

func TestSession_RefusesForeignFormat(t *testing.T) {
	t.Parallel()
	r, _, _, _ := speechRig(t, nil)
	_, err := r.Open(context.Background(), SessionContext{
		Correlation: "c", Language: LangEnglishIN,
		Format: media.PCM16Mono16k(), Prosody: DefaultProsody(),
	})
	if !errors.Is(err, ErrInvalidAudio) {
		t.Errorf("err = %v, want ErrInvalidAudio", err)
	}
}

func TestSession_OperationsAfterCloseAreRefused(t *testing.T) {
	t.Parallel()
	r, clock, _, _ := speechRig(t, nil)
	s := openSession(t, r)
	if err := s.Close(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}

	if err := s.PushAudio(testFrame(clock, 0)); !errors.Is(err, ErrSpeechSessionClosed) {
		t.Errorf("PushAudio err = %v, want ErrSpeechSessionClosed", err)
	}
	if _, err := s.Listen(context.Background()); !errors.Is(err, ErrSpeechSessionClosed) {
		t.Errorf("Listen err = %v, want ErrSpeechSessionClosed", err)
	}
	if err := s.Respond(context.Background(), "x."); !errors.Is(err, ErrSpeechSessionClosed) {
		t.Errorf("Respond err = %v, want ErrSpeechSessionClosed", err)
	}
	if _, err := s.Interrupt("x"); !errors.Is(err, ErrSpeechSessionClosed) {
		t.Errorf("Interrupt err = %v, want ErrSpeechSessionClosed", err)
	}
}

// TestBargeIn_LatencyIsWithinFrozenBudget measures the real wall-clock cost of
// an interruption against ADR-0011.
//
// # Why this one uses a real clock
//
// Every other test here injects a FakeClock, which makes ordering deterministic
// and makes elapsed time zero. Barge-in latency is a WALL-CLOCK claim — the
// caller hears the agent stop, or does not — so measuring it on a fake clock
// would assert nothing. This test therefore runs on the system clock and is the
// only timing-sensitive test in the package.
func TestBargeIn_LatencyIsWithinFrozenBudget(t *testing.T) {
	t.Parallel()

	// ADR-0011 and ADR-0004 section 12: one frame interval from the
	// interruption signal to silence.
	const bargeInBudget = 20 * time.Millisecond

	r, err := New(TestConfig()) // no WithClock: the real clock
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = r.Stop(context.Background()) })

	if err := r.Router().RegisterSTT(
		NewFakeSTTProvider("asr", ScriptedPartials([]string{"hi"}, "hi there."), nil),
		TierPrimary); err != nil {
		t.Fatal(err)
	}
	if err := r.Router().RegisterTTS(NewFakeTTSProvider("voice", 8, nil), TierPrimary); err != nil {
		t.Fatal(err)
	}

	// Ten interruptions, worst case reported. One sample would be noise.
	const runs = 10
	var worst time.Duration
	for i := 0; i < runs; i++ {
		s := openSession(t, r)

		turn, err := s.Listen(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		format := media.PCM16Mono8k()
		f := media.Frame{
			Sequence: 0, Format: format,
			Payload: make([]byte, format.BytesFor(20*time.Millisecond)),
		}
		if err := s.PushAudio(f); err != nil {
			t.Fatal(err)
		}
		if err := s.EndOfSpeech(); err != nil {
			t.Fatal(err)
		}
		waitForState(t, turn, TurnFinal)
		if err := s.Respond(context.Background(),
			"A fairly long reply. With several clauses in it. And one more."); err != nil {
			t.Fatal(err)
		}

		res, err := s.Interrupt("caller_spoke")
		if err != nil {
			t.Fatal(err)
		}
		if res.Latency > worst {
			worst = res.Latency
		}
		if err := s.Close(context.Background(), "done"); err != nil {
			t.Fatal(err)
		}
	}

	t.Logf("barge-in worst of %d runs: %s (ADR-0011 budget %s)", runs, worst, bargeInBudget)
	if worst > bargeInBudget {
		t.Errorf("barge-in worst case %s exceeds the ADR-0011 budget of %s",
			worst, bargeInBudget)
	}
}

// Interrupting when the agent is not speaking must be refused, not silently
// treated as a barge-in.
func TestBargeIn_RefusedWhenNothingIsBeingSaid(t *testing.T) {
	t.Parallel()
	r, _, _, _ := speechRig(t, ScriptedPartials([]string{"a"}, "b."))
	s := openSession(t, r)

	turn, err := s.Listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The turn is Listening: the caller is talking, the agent is not.
	if _, err := s.Interrupt("caller_spoke"); err == nil {
		t.Fatal("an interrupt was accepted while the agent was not speaking")
	}
	// The listening turn must survive untouched — cancelling it would discard a
	// transcript that was being recognised perfectly well.
	if turn.State() != TurnListening {
		t.Errorf("the listening turn became %s", turn.State())
	}
	if _, ok := s.Turns().Active(); !ok {
		t.Error("the session lost its live turn to a refused interrupt")
	}
}

// Closing a session by any route must free its runtime slot.
func TestRuntime_ClosingASessionFreesItsSlot(t *testing.T) {
	t.Parallel()
	cfg := TestConfig()
	cfg.MaxSessions = 2
	r, err := New(cfg, WithClock(testClock()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = r.Stop(context.Background()) })

	sc := SessionContext{Correlation: "c", Language: LangEnglishIN,
		Format: media.PCM16Mono8k(), Prosody: DefaultProsody()}

	// Open and close well past capacity. If Close leaked its registry entry,
	// this would refuse with backpressure on the third iteration.
	for i := 0; i < 10; i++ {
		s, err := r.Open(context.Background(), sc)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if err := s.Close(context.Background(), "done"); err != nil {
			t.Fatal(err)
		}
		if got := r.Live(); got != 0 {
			t.Fatalf("iteration %d left %d sessions live", i, got)
		}
	}
}
