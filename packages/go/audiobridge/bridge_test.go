package audiobridge

import (
	"context"
	"errors"
	"testing"
	"time"

	audiointel "github.com/callscreen/callscreen-platform/packages/go/audiointel"
	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
)

// ---------------------------------------------------------------------------
// A live Phase 11C session
// ---------------------------------------------------------------------------

// liveSpeechSession builds a real *speech.SpeechSession backed by Phase 11C's
// own fake providers.
//
// # A REAL session, not a mock, and that is the point of this module's tests
//
// The signature-level guarantee is already made at compile time by the
// assertion in bridge.go. What that cannot show is that the two engines agree
// about BEHAVIOUR — that Phase 11C accepts an interruption at the moment
// Phase 11D decides to send one, and refuses at the moments it should. Only
// driving the frozen implementation shows that.
func liveSpeechSession(t *testing.T) (*speech.SpeechSession, *speech.FakeTTSProvider, *rt.FakeClock) {
	t.Helper()

	clock := rt.NewFakeClock(time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC))

	runtime, err := speech.New(speech.TestConfig(), speech.WithClock(clock))
	if err != nil {
		t.Fatalf("building the Phase 11C runtime: %v", err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("starting the Phase 11C runtime: %v", err)
	}
	t.Cleanup(func() { _, _ = runtime.Stop(context.Background()) })

	stt := speech.NewFakeSTTProvider("asr",
		speech.ScriptedPartials([]string{"hello"}, "hello there."), clock)
	tts := speech.NewFakeTTSProvider("voice", 4, clock)

	if err := runtime.Router().RegisterSTT(stt, speech.TierPrimary); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Router().RegisterTTS(tts, speech.TierPrimary); err != nil {
		t.Fatal(err)
	}

	session, err := runtime.Open(context.Background(), speech.SessionContext{
		Correlation: "call-bridge-test",
		Language:    speech.LangEnglishIN,
		Format:      media.PCM16Mono8k(),
		Voice:       "default",
		Prosody:     speech.DefaultProsody(),
	})
	if err != nil {
		t.Fatalf("opening a Phase 11C session: %v", err)
	}

	return session, tts, clock
}

// bringToSpeaking drives a Phase 11C session to the state where an
// interruption is meaningful: the agent holding the floor.
//
// Phase 11C refuses an interruption unless the turn is responding or speaking,
// so a test that skipped this would be testing the refusal path while believing
// it tested the happy one.
func bringToSpeaking(
	t *testing.T, s *speech.SpeechSession, tts *speech.FakeTTSProvider, clock *rt.FakeClock,
) {
	t.Helper()

	ctx := context.Background()

	turn, err := s.Listen(ctx)
	if err != nil {
		t.Fatalf("opening a turn: %v", err)
	}

	frame := media.Frame{
		Format:  media.PCM16Mono8k(),
		Payload: make([]byte, 320),
		Arrival: clock.Now(),
	}
	if err := s.PushAudio(frame); err != nil {
		t.Fatalf("pushing audio: %v", err)
	}
	if err := s.EndOfSpeech(); err != nil {
		t.Fatalf("signalling end of speech: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for turn.State() != speech.TurnFinal {
		if time.Now().After(deadline) {
			t.Fatalf("the turn never reached %s; it is %s", speech.TurnFinal, turn.State())
		}
		time.Sleep(time.Millisecond)
	}

	// # The reply has to be long enough that synthesis is still in progress
	//
	// Phase 11C's TTSOrchestrator clears its speaking flag when the synthesis
	// pump finishes, and its outbound frame queue holds 100 frames. Nothing in
	// this test consumes that queue, so a reply short enough to fit inside it
	// is fully synthesised the instant Respond is called — and an interruption
	// then finds nothing to cancel and correctly does nothing.
	//
	// That is not a defect in either engine; it is an artefact of a fake
	// synthesiser that takes no time. Thirty sentences at four frames a chunk
	// overflows the queue, the pump blocks, and the agent is genuinely still
	// speaking when the caller talks over it — which is the situation the §8
	// scenario is about.
	//
	// An earlier version of this test used a three-sentence reply and reported
	// that Phase 11C had not cancelled. It had nothing to cancel.
	const longReply = "One. Two. Three. Four. Five. Six. Seven. Eight. Nine. " +
		"Ten. Eleven. Twelve. Thirteen. Fourteen. Fifteen. Sixteen. " +
		"Seventeen. Eighteen. Nineteen. Twenty. Twenty one. Twenty two. " +
		"Twenty three. Twenty four. Twenty five. Twenty six. Twenty seven. " +
		"Twenty eight. Twenty nine. Thirty."

	// Respond blocks until synthesis completes, so it runs on its own goroutine
	// and the interruption arrives while the agent still holds the floor.
	go func() { _ = s.Respond(ctx, longReply) }()

	// WAIT FOR THE PROVIDER STREAM, not just the turn state.
	//
	// Phase 11C moves the turn to responding BEFORE it opens the synthesiser,
	// so a test that waited only on the turn can interrupt in the window where
	// there is no stream to cancel — and then assert that cancellation did not
	// happen, which is true and tests nothing. An earlier version of this test
	// did exactly that.
	for tts.Opened() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the synthesiser never opened; the turn is %s", turn.State())
		}
		time.Sleep(time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// Contract
// ---------------------------------------------------------------------------

// TestBridge_SatisfiesBothContracts is the compile-time guarantee, restated
// where a reader looking for it will find it.
func TestBridge_SatisfiesBothContracts(t *testing.T) {
	t.Parallel()

	// The frozen Phase 11C session satisfies this module's interface with no
	// adapter on the speech side.
	var _ SpeechSession = (*speech.SpeechSession)(nil)

	// And this adapter satisfies the Phase 11D port.
	var _ audiointel.SpeechController = (*Adapter)(nil)
}

func TestBridge_RefusesANilSession(t *testing.T) {
	t.Parallel()

	if _, err := New(nil); err == nil {
		t.Error("a nil session was adapted; audiointel already counts an unset " +
			"controller as a configuration fault, and wrapping nothing hides that")
	}
}

// ---------------------------------------------------------------------------
// The §8 scenario, end to end
// ---------------------------------------------------------------------------

// TestBridge_BargeInCancelsRealPhase11CSynthesis is the phase brief's §8
// scenario, driven from synthetic audio through to the frozen speech pipeline.
//
//	AI is speaking
//	     ↓
//	caller starts speaking
//	     ↓
//	Audio Intelligence detects speech        ← packages/go/audiointel
//	     ↓
//	BargeIn event
//	     ↓
//	Phase 11C cancels TTS                    ← packages/go/speech, unmodified
//	     ↓
//	new speech turn begins
func TestBridge_BargeInCancelsRealPhase11CSynthesis(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// # The audio side is warmed up FIRST, and the ordering is not incidental
	//
	// audiointel refuses to assert anything until its noise floor converges,
	// which takes WarmupFrames of audio. Phase 11C's fake synthesiser, driven
	// by a fake clock, finishes a short reply almost immediately — so warming
	// up after the agent starts talking means the reply is over before the
	// caller's first frame is analysed, and the interruption then arrives with
	// nothing left to cancel.
	//
	// That is not a bug in either engine. It is an artefact of a synthesiser
	// that takes no time, and an earlier version of this test hit it: the
	// interruption was delivered, Phase 11C accepted it, and TTSOrchestrator.Cancel
	// returned immediately because it was no longer speaking.
	ctxState := audiointel.ConversationState{}

	cfg := audiointel.TestConfig(media.PCM16Mono8k())
	intel, err := audiointel.New(cfg, audiointel.WithClock(rt.SystemClock{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = intel.Stop(ctx) })

	audio, err := intel.Open(ctx, audiointel.SessionContext{
		Call:      "call-bridge-test",
		Direction: audiointel.DirectionInbound,
		Format:    cfg.Format,
	})
	if err != nil {
		t.Fatal(err)
	}

	gen := audiointel.NewSignalGenerator(cfg.Format, cfg.FrameInterval)
	gen.SetArrival(time.Now())

	session, tts, clock := liveSpeechSession(t)

	// A quiet line first, with nobody speaking. The floor converges here.
	for _, f := range audiointel.WarmupFrames(gen, cfg) {
		if _, err := audio.Analyze(ctx, f, ctxState, nil, nil); err != nil {
			t.Fatalf("warm-up: %v", err)
		}
	}

	// NOW the agent starts talking, and the caller talks over it.
	bringToSpeaking(t, session, tts, clock)
	closedBefore := tts.Closed()

	adapter, err := New(session)
	if err != nil {
		t.Fatal(err)
	}

	speaking := audiointel.ConversationState{AgentSpeaking: true}

	var delivered audiointel.BargeInDecision
	for _, f := range gen.NormalSpeech(20) {
		a, err := audio.Analyze(ctx, f, speaking, adapter, nil)
		if err != nil {
			t.Fatalf("analysing caller speech: %v", err)
		}
		if a.BargeIn.Detected {
			delivered = a.BargeIn
			break
		}
	}

	if !delivered.Detected {
		t.Fatal("the caller talked over the agent and no interruption was detected")
	}
	if delivered.Outcome != audiointel.BargeInDelivered {
		t.Fatalf("outcome = %s, want %s (err: %v)",
			delivered.Outcome, audiointel.BargeInDelivered, delivered.Err)
	}

	if adapter.Interrupts() != 1 {
		t.Errorf("the adapter delivered %d interruptions, want 1", adapter.Interrupts())
	}
	if adapter.Refusals() != 0 {
		t.Errorf("Phase 11C refused %d interruptions", adapter.Refusals())
	}

	// Phase 11C actually cancelled: its TTS stream closed.
	deadline := time.Now().Add(2 * time.Second)
	for tts.Closed() == closedBefore {
		if time.Now().After(deadline) {
			t.Fatalf("Phase 11C's TTS stream never closed after the interruption "+
				"(closed=%d before and after)", closedBefore)
		}
		time.Sleep(time.Millisecond)
	}

	// And a new turn was opened for the caller's incoming speech.
	turn, ok := session.Turns().Active()
	if !ok {
		t.Fatal("no live turn after the interruption")
	}
	if turn.Role != speech.RoleCaller {
		t.Errorf("the new turn's role is %s, want %s", turn.Role, speech.RoleCaller)
	}
	if session.Interrupts() != 1 {
		t.Errorf("Phase 11C recorded %d interruptions, want 1", session.Interrupts())
	}

	t.Logf("phase 11D measured %s from detection to the adapter returning; "+
		"phase 11C measured %d µs for its own cancellation "+
		"(ADR-0004 §12 budget: %s for the whole hop)",
		delivered.Latency, adapter.LastInterruptLatencyMicros(),
		audiointel.BargeInBudget)
}

// TestBridge_EndpointReachesPhase11C drives the other half of the contract.
//
// speech.SpeechSession.EndOfSpeech documents itself as "the VAD boundary, not a
// VAD" and points at exactly this layer. ADR-0005 C6 records that endpointing
// is ours and vendor endpointing is disabled or ignored — this is where the two
// statements meet.
func TestBridge_EndpointReachesPhase11C(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	session, _, clock := liveSpeechSession(t)

	turn, err := session.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}

	frame := media.Frame{
		Format:  media.PCM16Mono8k(),
		Payload: make([]byte, 320),
		Arrival: clock.Now(),
	}
	if err := session.PushAudio(frame); err != nil {
		t.Fatal(err)
	}

	adapter, err := New(session)
	if err != nil {
		t.Fatal(err)
	}

	if err := adapter.EndOfSpeech(ctx); err != nil {
		t.Fatalf("delivering the endpoint: %v", err)
	}
	if adapter.Endpoints() != 1 {
		t.Errorf("Endpoints = %d, want 1", adapter.Endpoints())
	}

	// Phase 11C moved the turn on.
	deadline := time.Now().Add(2 * time.Second)
	for {
		switch turn.State() {
		case speech.TurnFinalizing, speech.TurnFinal:
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the turn is %s after the endpoint; want finalizing or final",
				turn.State())
		}
		time.Sleep(time.Millisecond)
	}
}

// TestBridge_PropagatesAPhase11CRefusal covers the disagreement window.
//
// Phase 11D checks that the agent holds the floor before calling, and Phase 11C
// checks it again. They can still disagree in the window where the turn moved
// on between the detection and the call — and when that happens the refusal
// must reach audiointel unchanged, so it can be counted as BargeInRefused
// rather than mistaken for a delivery.
func TestBridge_PropagatesAPhase11CRefusal(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// A session with no live turn at all: Phase 11C has nothing to interrupt.
	session, _, _ := liveSpeechSession(t)

	adapter, err := New(session)
	if err != nil {
		t.Fatal(err)
	}

	err = adapter.Interrupt(ctx, "caller_spoke")
	if err == nil {
		t.Fatal("interrupting a session with nothing live was accepted")
	}
	if adapter.Refusals() != 1 {
		t.Errorf("Refusals = %d, want 1", adapter.Refusals())
	}
	if adapter.Interrupts() != 0 {
		t.Errorf("Interrupts = %d after a refusal, want 0", adapter.Interrupts())
	}

	// And audiointel must classify it, not swallow it.
	fake := &refusingSession{err: err}
	refusing, err := New(fake)
	if err != nil {
		t.Fatal(err)
	}

	cfg := audiointel.TestConfig(media.PCM16Mono8k())
	detector, err := audiointel.NewBargeInDetector(cfg, rt.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}

	decision := detector.Observe(ctx,
		audiointel.SignalView{},
		audiointel.VADDecision{OnsetConfirmed: true, State: audiointel.VADSpeech},
		true, refusing)

	if decision.Outcome != audiointel.BargeInRefused {
		t.Errorf("outcome = %s, want %s", decision.Outcome, audiointel.BargeInRefused)
	}
	if decision.Err == nil {
		t.Error("the refusal was swallowed rather than reported")
	}
}

// refusingSession always refuses, for the classification half of the test
// above.
type refusingSession struct{ err error }

func (r *refusingSession) Interrupt(string) (speech.InterruptResult, error) {
	return speech.InterruptResult{}, r.err
}

func (r *refusingSession) EndOfSpeech() error { return r.err }

func TestBridge_ReportsEndOfSpeechFailures(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("no recognition stream is open")
	adapter, err := New(&refusingSession{err: sentinel})
	if err != nil {
		t.Fatal(err)
	}

	if err := adapter.EndOfSpeech(context.Background()); !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want it to wrap the Phase 11C error", err)
	}
	if adapter.Endpoints() != 0 {
		t.Errorf("Endpoints = %d after a failure, want 0", adapter.Endpoints())
	}
}
