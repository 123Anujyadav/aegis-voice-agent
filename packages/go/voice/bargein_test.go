package voice

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	audiobridge "github.com/callscreen/callscreen-platform/packages/go/audiobridge"
	audiointel "github.com/callscreen/callscreen-platform/packages/go/audiointel"
	media "github.com/callscreen/callscreen-platform/packages/go/media"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
)

// ---------------------------------------------------------------------------
// Driving a real detection
// ---------------------------------------------------------------------------
//
// The interruption path under test starts inside Phase 11D: audiointel calls
// the controller from within Analyze, and this pipeline's orchestration runs
// there. These tests therefore drive the pipeline through its ordinary audio
// input and let the analyser decide — nothing here calls Interrupt directly
// except where a test is explicitly about the controller in isolation.
//
// bargeIntel is a stand-in ANALYSER, not a stand-in detector: it decides when a
// detection happens and then performs the same call audiointel performs, so the
// orchestration being measured is the real one. Replacing audiointel's acoustic
// judgement with a script is what makes these tests deterministic; replacing
// the CALL would make them test nothing.

type bargeIntel struct {
	mu sync.Mutex

	frames  int
	onsetAt int
	endAt   int

	// bargeAt is the frame on which the agent is deemed to have been talked
	// over. Zero means never.
	bargeAt int

	// repeat fires a further detection on every frame after bargeAt, which is
	// how a caller who keeps talking is modelled.
	repeat bool

	// delivered records the outcomes the controller returned.
	outcomes []error

	// latencies records how long each controller call took, measured where
	// Phase 11D measures it: around the call, from before to after.
	latencies []time.Duration
}

func (s *bargeIntel) Analyze(
	ctx context.Context, _ media.Frame, state audiointel.ConversationState,
	controller audiointel.SpeechController, _ audiointel.OutboundEnvelope,
) (audiointel.Analysis, error) {
	s.mu.Lock()
	s.frames++
	n := s.frames
	barge := s.bargeAt > 0 && (n == s.bargeAt || (s.repeat && n > s.bargeAt))
	s.mu.Unlock()

	var a audiointel.Analysis
	if n == s.onsetAt {
		a.VAD.OnsetConfirmed = true
	}
	if n == s.endAt {
		a.Endpoint.Confirmed = true
	}

	if barge {
		a.VAD.OnsetConfirmed = true
		a.BargeIn.Detected = true
		a.BargeIn.Reason = audiointel.ReasonCallerSpoke

		// Phase 11D's own gate: it does not deliver an interruption unless the
		// agent holds the floor. Applying it here keeps this stand-in honest
		// about when the real detector would have called.
		switch {
		case !state.AgentSpeaking:
			a.BargeIn.Outcome = audiointel.BargeInNotSpeaking
		case controller == nil:
			a.BargeIn.Outcome = audiointel.BargeInNoController
		default:
			started := time.Now()
			err := controller.Interrupt(ctx, a.BargeIn.Reason)
			a.BargeIn.Latency = time.Since(started)

			s.mu.Lock()
			s.outcomes = append(s.outcomes, err)
			s.latencies = append(s.latencies, a.BargeIn.Latency)
			s.mu.Unlock()

			if err != nil {
				a.BargeIn.Outcome = audiointel.BargeInRefused
				a.BargeIn.Err = err
			} else {
				a.BargeIn.Outcome = audiointel.BargeInDelivered
			}
		}
	}
	return a, nil
}

// count returns how many frames this analyser has seen.
func (s *bargeIntel) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frames
}

// lastLatency returns the most recent controller-call duration.
func (s *bargeIntel) lastLatency() (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.latencies) == 0 {
		return 0, false
	}
	return s.latencies[len(s.latencies)-1], true
}

func (s *bargeIntel) results() []error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]error(nil), s.outcomes...)
}

// ---------------------------------------------------------------------------
// A Phase 11C session, behind the REAL adapter
// ---------------------------------------------------------------------------

// fakeSpeechSession satisfies audiobridge.SpeechSession.
//
// It stands in for speech.SpeechSession because constructing a real one pulls
// in a SpeechRuntime with provider routing, a turn manager and two
// orchestrators — the exact dependency audiobridge's own documentation gives
// for defining this interface. What is NOT faked is the adapter: the tests
// below drive a real *audiobridge.Adapter, and
// TestBargeIn_RealSpeechSessionSatisfiesTheAdapter carries the compile-time
// proof that the frozen session satisfies the same interface.
type fakeSpeechSession struct {
	interrupts atomic.Int64
	endpoints  atomic.Int64
	reasons    sync.Map

	// refuse makes Interrupt decline, as the frozen session does when its turn
	// is no longer responding or speaking.
	refuse atomic.Bool
}

func (f *fakeSpeechSession) Interrupt(reason string) (speech.InterruptResult, error) {
	n := f.interrupts.Add(1)
	f.reasons.Store(n, reason)

	if f.refuse.Load() {
		return speech.InterruptResult{}, fmt.Errorf(
			"%w: turn is not speaking", speech.ErrInternalFailure)
	}
	return speech.InterruptResult{
		PreviousTurn: speech.TurnID("prev"),
		NewTurn:      speech.TurnID("next"),
		Latency:      time.Millisecond,
	}, nil
}

func (f *fakeSpeechSession) EndOfSpeech() error {
	f.endpoints.Add(1)
	return nil
}

// TestBargeIn_RealSpeechSessionSatisfiesTheAdapter is the contract proof.
//
// The chain the phase brief requires — detection to
// speech.SpeechSession.Interrupt through packages/go/audiobridge — is checked
// by the compiler at every link:
//
//	audiointel.SpeechController  <- *audiobridge.Adapter   (bargein.go)
//	audiobridge.SpeechSession    <- *speech.SpeechSession  (here, and in audiobridge)
//
// If Phase 11C changes either signature, this file stops compiling.
func TestBargeIn_RealSpeechSessionSatisfiesTheAdapter(t *testing.T) {
	t.Parallel()

	var _ audiobridge.SpeechSession = (*speech.SpeechSession)(nil)
	var _ audiointel.SpeechController = (*audiobridge.Adapter)(nil)

	// And the adapter accepts this test's session with nothing in between.
	adapter, err := audiobridge.New(&fakeSpeechSession{})
	if err != nil {
		t.Fatalf("audiobridge.New: %v", err)
	}
	var _ audiointel.SpeechController = adapter

	// A nil session is refused rather than wrapped into something that looks
	// healthy — audiointel counts an absent controller distinctly.
	if _, err := audiobridge.New(nil); err == nil {
		t.Error("audiobridge accepted a nil session")
	}
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type bargeHarness struct {
	*harness
	intel   *bargeIntel
	session *fakeSpeechSession
	adapter *audiobridge.Adapter
}

type bargeOpts struct {
	bargeAt        int
	repeat         bool
	tokens         []string
	tokenGap       time.Duration
	framesPerChunk int
	ttsSynthDelay  time.Duration
	sinkDelay      time.Duration
	noController   bool
	refuse         bool
}

// newBargeHarness builds a pipeline whose Phase 11C seam is a real adapter.
func newBargeHarness(t *testing.T, o bargeOpts) *bargeHarness {
	t.Helper()

	if o.tokens == nil {
		o.tokens = []string{
			"Shall I read your transactions?",
			" There are twelve of them?",
			" Here is the first one?",
			" Here is the second one?",
		}
	}
	if o.tokenGap == 0 {
		o.tokenGap = 30 * time.Millisecond
	}
	if o.framesPerChunk == 0 {
		o.framesPerChunk = 40
	}
	if o.ttsSynthDelay == 0 {
		o.ttsSynthDelay = 20 * time.Millisecond
	}
	if o.sinkDelay == 0 {
		o.sinkDelay = 3 * time.Millisecond
	}

	base := newHarness(t, harnessOpts{
		tokens:         o.tokens,
		tokenGap:       o.tokenGap,
		framesPerChunk: o.framesPerChunk,
		ttsSynthDelay:  o.ttsSynthDelay,
		sinkDelay:      o.sinkDelay,
		maxFrames:      256,
	})

	session := &fakeSpeechSession{}
	session.refuse.Store(o.refuse)

	adapter, err := audiobridge.New(session)
	if err != nil {
		t.Fatalf("audiobridge.New: %v", err)
	}

	intel := &bargeIntel{onsetAt: 1, endAt: 3, bargeAt: o.bargeAt, repeat: o.repeat}

	// Rebuild the pipeline with the interruption seam wired in. The harness's
	// registry, providers, FSM and metrics are reused unchanged.
	cfg := base.pipeline.cfg
	cfg.Intel = intel
	if !o.noController {
		cfg.Controller = adapter
	}

	p, err := NewPipeline(cfg)
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	base.pipeline = p
	base.intel = nil

	return &bargeHarness{harness: base, intel: intel, session: session, adapter: adapter}
}

// feedUntil writes frames until cond holds or the deadline passes.
func (h *bargeHarness) feedUntil(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()

	deadline := time.After(d)
	for i := 0; ; i++ {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("condition not met within %s (%d frames written)", d, i)
		default:
		}
		if err := h.pipeline.WriteFrame(testFrame(uint64(i))); err != nil &&
			!errors.Is(err, ErrBackpressure) && !errors.Is(err, ErrSessionClosed) {
			t.Fatalf("WriteFrame(%d): %v", i, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// The central proof
// ---------------------------------------------------------------------------

// TestBargeIn_OldGenerationIsRejectedAndTheNewOneIsSpoken walks the exact
// sequence the phase brief sets out, asserting on MEDIA DELIVERY throughout.
//
// A counter can be incremented by code that never stopped a frame. What matters
// is what the caller heard, so every assertion here is about frames that
// reached the media sink.
func TestBargeIn_OldGenerationIsRejectedAndTheNewOneIsSpoken(t *testing.T) {
	t.Parallel()

	h := newBargeHarness(t, bargeOpts{bargeAt: 0}) // barge-in triggered manually below

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	// --- old generation produces audio that reaches the caller --------------
	h.feedUntil(t, 20*time.Second, func() bool { return h.out.count() > 0 })

	oldGeneration := h.pipeline.Generation()
	deliveredBefore := h.out.count()
	if deliveredBefore == 0 {
		t.Fatal("the first turn never spoke")
	}

	// --- barge-in ------------------------------------------------------------
	h.intel.mu.Lock()
	h.intel.bargeAt = h.intel.frames + 1
	h.intel.mu.Unlock()

	h.feedUntil(t, 20*time.Second, func() bool { return h.pipeline.BargeIns() > 0 })

	// --- the interruption reached Phase 11C through the real adapter --------
	if got := h.session.interrupts.Load(); got != 1 {
		t.Errorf("speech.SpeechSession.Interrupt was called %d times, want 1", got)
	}
	if got := h.adapter.Interrupts(); got != 1 {
		t.Errorf("the audiobridge adapter recorded %d interruptions, want 1", got)
	}

	// --- the generation moved on --------------------------------------------
	newGeneration := h.pipeline.Generation()
	if newGeneration == oldGeneration {
		t.Fatalf("the generation did not change across a barge-in (still %d)",
			oldGeneration)
	}

	// --- the old generation's audio is refused, measured at the sink ---------
	//
	// Counted PER SYNTHESIS STREAM. A plain frame count cannot answer this: the
	// interruption opens a new turn that legitimately starts speaking, and its
	// audio would otherwise look like the abandoned turn leaking through.
	const interruptedStream = 0

	playedFromOld := h.out.countFrom(interruptedStream)
	time.Sleep(400 * time.Millisecond)

	if after := h.out.countFrom(interruptedStream); after != playedFromOld {
		t.Errorf("%d further frames from the INTERRUPTED turn reached media after "+
			"the barge-in: the agent talked over the caller", after-playedFromOld)
	}

	produced := h.tts.producedFrames()
	if produced <= playedFromOld {
		t.Errorf("the synthesiser produced %d frames and %d from the interrupted "+
			"turn were delivered: nothing was left to be rejected, so the guard "+
			"was not exercised", produced, playedFromOld)
	}
	if withheld := h.pipeline.StaleFramesBlocked(); withheld == 0 {
		t.Error("no frames were recorded as withheld; the generation guard did not " +
			"fire on the barge-in path")
	}

	// --- the session passed through interrupted, by declared transitions ----
	//
	// Asserted on the HISTORY rather than the current state: by now the new
	// turn may already be speaking, which is the pipeline working.
	sawInterrupted := false
	for _, c := range h.fsm.History() {
		if !CanTransition(c.From, c.To) {
			t.Errorf("undeclared transition %s -> %s", c.From, c.To)
		}
		if c.To == StateInterrupted {
			sawInterrupted = true
		}
	}
	if !sawInterrupted {
		t.Error("the session never entered interrupted; the FSM was bypassed")
	}

	// --- a NEW turn speaks, and ITS audio is accepted -----------------------
	h.feedUntil(t, 25*time.Second, func() bool {
		return h.out.countFrom(interruptedStream+1) > 0
	})

	if got := h.out.countFrom(interruptedStream + 1); got == 0 {
		t.Error("the new generation delivered nothing: the barge-in left the " +
			"pipeline unable to speak again")
	}
	if h.out.countFrom(interruptedStream) != playedFromOld {
		t.Error("the interrupted turn resumed speaking once the new turn began")
	}

	// The controller call itself, timed where Phase 11D times it. Deliberately
	// NOT the wall time of the feeding loop above, which is dominated by this
	// test's own 2ms frame cadence and would be a measurement of the test.
	controllerCall, _ := h.intel.lastLatency()

	t.Logf("BARGE-IN, MEASURED AT THE MEDIA SINK (Aegis orchestration only —\n"+
		"  stand-in providers, no model or synthesiser inference):\n"+
		"  generation %d -> %d\n"+
		"  frames withheld from the interrupted turn: %d\n"+
		"  frames of the interrupted turn actually played: %d\n"+
		"  detection -> controller returned: %s\n"+
		"  ADR-0004 §12's 20ms is the PROVIDER cancellation/abort budget; it is\n"+
		"  not a time-to-first-token figure and this is not a measurement of it.",
		oldGeneration, newGeneration, h.pipeline.StaleFramesBlocked(),
		playedFromOld, controllerCall)
}

// ---------------------------------------------------------------------------
// Inbound audio survives
// ---------------------------------------------------------------------------

func TestBargeIn_DoesNotFlushInboundAudio(t *testing.T) {
	t.Parallel()

	// The caller is speaking — that is what caused the interruption. Discarding
	// the audio path would throw away the very words that interrupted, and the
	// caller would have to repeat themselves.
	h := newBargeHarness(t, bargeOpts{})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	h.feedUntil(t, 20*time.Second, func() bool { return h.out.count() > 0 })

	writtenBefore := h.stt.written.Load()
	openedBefore := h.stt.opened.Load()

	h.intel.mu.Lock()
	h.intel.bargeAt = h.intel.frames + 1
	h.intel.mu.Unlock()

	h.feedUntil(t, 20*time.Second, func() bool { return h.pipeline.BargeIns() > 0 })

	// A NEW recognition turn must open for the speech that interrupted.
	h.feedUntil(t, 20*time.Second, func() bool {
		return h.stt.opened.Load() > openedBefore
	})

	// ...and the frames arriving after the interruption must reach it.
	h.feedUntil(t, 20*time.Second, func() bool {
		return h.stt.written.Load() > writtenBefore
	})

	if got := h.stt.opened.Load(); got <= openedBefore {
		t.Errorf("no new recognition stream opened after the barge-in (%d -> %d): "+
			"the caller's interrupting speech had nowhere to go",
			openedBefore, got)
	}
	if got := h.stt.written.Load(); got <= writtenBefore {
		t.Errorf("no audio was written to the new recogniser (%d -> %d): the "+
			"inbound path was flushed", writtenBefore, got)
	}

	// The frame queue is still accepting, which is the other half of "not
	// flushed": the pipeline did not close its own input.
	if err := h.pipeline.WriteFrame(testFrame(9999)); err != nil &&
		!errors.Is(err, ErrBackpressure) {
		t.Errorf("the inbound path stopped accepting frames after a barge-in: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Timing cases
// ---------------------------------------------------------------------------

func TestBargeIn_ImmediatelyAfterSynthesisStarts(t *testing.T) {
	t.Parallel()

	// The window where the turn is committed but the first frame has not left.
	// StateSynthesizing counts as holding the floor precisely so this is
	// interruptible; a caller talking then is interrupting.
	h := newBargeHarness(t, bargeOpts{
		ttsSynthDelay: 400 * time.Millisecond, // no audio out yet
	})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	// Wait until the agent holds the floor, before any frame is delivered.
	h.feedUntil(t, 20*time.Second, func() bool {
		return h.fsm.State().AgentHoldsFloor()
	})
	if h.out.count() != 0 {
		t.Skip("audio was delivered sooner than this test can observe; the " +
			"no-audio-yet window was missed")
	}

	h.intel.mu.Lock()
	h.intel.bargeAt = h.intel.frames + 1
	h.intel.mu.Unlock()

	h.feedUntil(t, 20*time.Second, func() bool { return h.pipeline.BargeIns() > 0 })

	if got := h.session.interrupts.Load(); got != 1 {
		t.Errorf("Phase 11C was interrupted %d times, want 1", got)
	}

	// Nothing may EVER be spoken from the abandoned turn — it had produced no
	// audio at the moment it was interrupted, so it must produce none now.
	time.Sleep(600 * time.Millisecond)
	if got := h.out.countFrom(0); got != 0 {
		t.Errorf("%d frames from a turn interrupted before it spoke reached media", got)
	}

	sawInterrupted := false
	for _, c := range h.fsm.History() {
		if !CanTransition(c.From, c.To) {
			t.Errorf("undeclared transition %s -> %s", c.From, c.To)
		}
		if c.To == StateInterrupted {
			sawInterrupted = true
		}
	}
	if !sawInterrupted {
		t.Error("the session never entered interrupted")
	}
}

func TestBargeIn_WhenTheAgentIsNotSpeakingIsRefused(t *testing.T) {
	t.Parallel()

	// A caller talking while we are listening is not interrupting: their audio
	// already belongs to the live turn, and cancelling would throw away a
	// transcript in progress. Both layers apply this rule; this asserts ours.
	h := newBargeHarness(t, bargeOpts{})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	err := h.pipeline.interrupts.Interrupt(context.Background(), audiointel.ReasonCallerSpoke)
	if err == nil {
		t.Fatal("an interruption was accepted while the agent was not speaking")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("want ErrInvalidTransition, got %v", err)
	}
	if got := h.session.interrupts.Load(); got != 0 {
		t.Errorf("Phase 11C was interrupted %d times for a refused detection", got)
	}
	if got := h.pipeline.Generation(); got != 0 {
		t.Errorf("a refused interruption bumped the generation to %d; the live "+
			"turn's audio would have been silenced for nothing", got)
	}
	if got := h.pipeline.interrupts.RefusedByFloor(); got != 1 {
		t.Errorf("the refusal was not counted (%d)", got)
	}
}

func TestBargeIn_RepeatedSignalsEachInterruptExactlyOnce(t *testing.T) {
	t.Parallel()

	// A caller who keeps talking produces detection after detection.
	//
	// # What must NOT be asserted here
	//
	// "Only the first one is delivered." That was this test's first shape and it
	// was wrong: each interruption opens a new turn, and a turn that reaches
	// speaking again is legitimately interruptible again. With fast providers
	// that happens between detections, so repeated deliveries are the pipeline
	// working, not a bug.
	//
	// What must hold no matter how the timing falls is the CORRESPONDENCE: one
	// delivery, one Phase 11C interruption, one generation. A pipeline that
	// double-counted, or bumped the generation without telling Phase 11C, would
	// break it whichever way the race resolved.
	h := newBargeHarness(t, bargeOpts{repeat: true})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	h.feedUntil(t, 20*time.Second, func() bool { return h.out.count() > 0 })

	h.intel.mu.Lock()
	h.intel.bargeAt = h.intel.frames + 1
	h.intel.mu.Unlock()

	h.feedUntil(t, 20*time.Second, func() bool { return h.pipeline.BargeIns() > 0 })

	// Keep the detections coming.
	for i := 0; i < 40; i++ {
		_ = h.pipeline.WriteFrame(testFrame(uint64(1000 + i)))
		time.Sleep(2 * time.Millisecond)
	}

	delivered := h.pipeline.BargeIns()
	refused := h.pipeline.interrupts.RefusedByFloor()

	if delivered == 0 {
		t.Fatal("no interruption was delivered at all")
	}

	// One delivery, one Phase 11C interruption.
	if got := h.session.interrupts.Load(); int64(delivered) != got {
		t.Errorf("Phase 11C saw %d interruptions for %d delivered here: the two "+
			"layers disagree about how many times the caller interrupted",
			got, delivered)
	}

	// One delivery, one generation. A generation bumped without an interruption
	// would silence a turn nobody interrupted; an interruption without a bump
	// would let an abandoned turn keep speaking.
	if got := h.pipeline.Generation(); got != delivered {
		t.Errorf("the generation is %d after %d interruptions", got, delivered)
	}

	// A refused detection must cost nothing: it neither reaches Phase 11C nor
	// moves the generation, and the totals above already account for that.
	t.Logf("repeated detections: %d delivered, %d refused because the agent did "+
		"not hold the floor", delivered, refused)

	// Whatever the traffic, the session is in a valid state.
	if h.fsm.Terminal() {
		t.Error("repeated barge-ins ended the call")
	}
	for _, c := range h.fsm.History() {
		if !CanTransition(c.From, c.To) {
			t.Errorf("undeclared transition %s -> %s", c.From, c.To)
		}
	}

	// And no interrupted turn resumed: every stream except the newest must have
	// stopped growing.
	settled := make([]int, h.tts.streamCount())
	for i := range settled {
		settled[i] = h.out.countFrom(i)
	}
	time.Sleep(300 * time.Millisecond)
	for i := 0; i < len(settled)-1; i++ {
		if after := h.out.countFrom(i); after != settled[i] {
			t.Errorf("interrupted turn %d resumed speaking (%d -> %d frames)",
				i, settled[i], after)
		}
	}
}

// ---------------------------------------------------------------------------
// Races
// ---------------------------------------------------------------------------

func TestBargeIn_RacingCancellationLeavesAValidSession(t *testing.T) {
	t.Parallel()

	h := newBargeHarness(t, bargeOpts{repeat: true})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	h.feedUntil(t, 20*time.Second, func() bool { return h.out.count() > 0 })

	h.intel.mu.Lock()
	h.intel.bargeAt = h.intel.frames + 1
	h.intel.mu.Unlock()

	// Cancel and keep feeding barge-in frames at the same time.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 60; i++ {
			_ = h.pipeline.WriteFrame(testFrame(uint64(2000 + i)))
			time.Sleep(time.Millisecond)
		}
	}()

	time.Sleep(15 * time.Millisecond)
	if err := h.pipeline.Cancel(ReasonRequested); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	wg.Wait()

	select {
	case <-h.pipeline.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("the pipeline did not stop")
	}

	if got := h.fsm.State(); got != StateCancelled {
		t.Errorf("the session is in %s, want cancelled", got)
	}
	for _, c := range h.fsm.History() {
		if !CanTransition(c.From, c.To) {
			t.Errorf("undeclared transition %s -> %s", c.From, c.To)
		}
	}

	// Nothing may be spoken after the session ended.
	settled := h.out.count()
	time.Sleep(300 * time.Millisecond)
	if after := h.out.count(); after != settled {
		t.Errorf("%d frames reached media after cancellation", after-settled)
	}
}

func TestBargeIn_RacingDisconnectLeavesAValidSession(t *testing.T) {
	t.Parallel()

	h := newBargeHarness(t, bargeOpts{repeat: true})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	h.feedUntil(t, 20*time.Second, func() bool { return h.out.count() > 0 })

	h.intel.mu.Lock()
	h.intel.bargeAt = h.intel.frames + 1
	h.intel.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 60; i++ {
			_ = h.pipeline.WriteFrame(testFrame(uint64(3000 + i)))
			time.Sleep(time.Millisecond)
		}
	}()

	time.Sleep(15 * time.Millisecond)
	if err := h.pipeline.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	wg.Wait()

	select {
	case <-h.pipeline.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("the pipeline did not stop")
	}

	if got := h.fsm.State(); got != StateCompleted {
		t.Errorf("a hang-up left the session in %s, want completed", got)
	}
}

// ---------------------------------------------------------------------------
// Phase 11C disagreement
// ---------------------------------------------------------------------------

func TestBargeIn_ASpeechSessionRefusalIsRecordedNotPropagated(t *testing.T) {
	t.Parallel()

	// Phase 11C refuses when its own turn has moved on. This pipeline has still
	// interrupted its own synthesis, so reporting a refusal upward would make
	// audiointel count a BargeInRefused for an interruption that did happen.
	h := newBargeHarness(t, bargeOpts{refuse: true})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	h.feedUntil(t, 20*time.Second, func() bool { return h.out.count() > 0 })

	h.intel.mu.Lock()
	h.intel.bargeAt = h.intel.frames + 1
	h.intel.mu.Unlock()

	h.feedUntil(t, 20*time.Second, func() bool { return h.pipeline.BargeIns() > 0 })

	if got := h.session.interrupts.Load(); got == 0 {
		t.Error("Phase 11C was never asked")
	}
	if got := h.pipeline.SpeechInterruptRefusals(); got != 1 {
		t.Errorf("the disagreement was not recorded (%d)", got)
	}

	// The detection was still reported as delivered to Phase 11D.
	for _, err := range h.intel.results() {
		if err != nil {
			t.Errorf("the controller returned %v; a Phase 11C refusal must not "+
				"become a refusal of an interruption this layer performed", err)
		}
	}

	// And this layer really did interrupt: the abandoned turn stops, whatever
	// Phase 11C thought of it.
	played := h.out.countFrom(0)
	time.Sleep(300 * time.Millisecond)
	if after := h.out.countFrom(0); after != played {
		t.Errorf("%d further frames from the interrupted turn were spoken",
			after-played)
	}
}

func TestBargeIn_WithNoControllerStillProtectsTheCaller(t *testing.T) {
	t.Parallel()

	// A deployment with no Phase 11C session still has a pipeline that can
	// interrupt itself. What it must never do is keep speaking.
	h := newBargeHarness(t, bargeOpts{noController: true})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	h.feedUntil(t, 20*time.Second, func() bool { return h.out.count() > 0 })

	h.intel.mu.Lock()
	h.intel.bargeAt = h.intel.frames + 1
	h.intel.mu.Unlock()

	h.feedUntil(t, 20*time.Second, func() bool { return h.pipeline.BargeIns() > 0 })

	if got := h.session.interrupts.Load(); got != 0 {
		t.Errorf("a session that was never wired was interrupted %d times", got)
	}

	played := h.out.countFrom(0)
	time.Sleep(300 * time.Millisecond)
	if after := h.out.countFrom(0); after != played {
		t.Errorf("%d further frames from the interrupted turn were spoken",
			after-played)
	}

	sawInterrupted := false
	for _, c := range h.fsm.History() {
		if c.To == StateInterrupted {
			sawInterrupted = true
		}
	}
	if !sawInterrupted {
		t.Error("the session never entered interrupted without a controller wired")
	}
}

// ---------------------------------------------------------------------------
// Resources
// ---------------------------------------------------------------------------

func TestBargeIn_LeavesNoGoroutineOrUnboundedQueueBehind(t *testing.T) {
	// Not parallel: goroutine counting is meaningless while other tests run.

	settle := func() int {
		var n int
		for i := 0; i < 50; i++ {
			n = runtime.NumGoroutine()
			time.Sleep(20 * time.Millisecond)
			if runtime.NumGoroutine() == n {
				return n
			}
		}
		return n
	}

	before := settle()

	for i := 0; i < 3; i++ {
		h := newBargeHarness(t, bargeOpts{})

		if err := h.pipeline.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		h.feedUntil(t, 20*time.Second, func() bool { return h.out.count() > 0 })

		h.intel.mu.Lock()
		h.intel.bargeAt = h.intel.frames + 1
		h.intel.mu.Unlock()

		h.feedUntil(t, 20*time.Second, func() bool { return h.pipeline.BargeIns() > 0 })

		// The interrupted turn's queues must not still be holding audio.
		if depth := len(h.pipeline.frames); depth > cap(h.pipeline.frames) {
			t.Errorf("the inbound queue holds %d frames above its bound", depth)
		}

		_ = h.pipeline.Disconnect()
		select {
		case <-h.pipeline.Done():
		case <-time.After(20 * time.Second):
			t.Fatal("the pipeline did not stop after a barge-in")
		}
	}

	after := settle()
	if after > before+4 {
		t.Errorf("goroutines went from %d to %d across three barged-in sessions: "+
			"an interrupted turn is not being reclaimed", before, after)
	}
}

func TestBargeIn_MeasuresOrchestrationLatency(t *testing.T) {
	t.Parallel()

	// What THIS layer costs between Phase 11D handing over the detection and
	// the controller returning: the generation bump, the Phase 11C call through
	// the adapter, the dispatcher abort, the synthesiser close and two state
	// transitions.
	//
	// It is not ADR-0004 §12's 20 ms, which is the PROVIDER cancellation budget,
	// and it is not a time-to-first-token figure of any kind.
	const runs = 12
	var samples []time.Duration

	for i := 0; i < runs; i++ {
		h := newBargeHarness(t, bargeOpts{})
		if err := h.pipeline.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		h.feedUntil(t, 20*time.Second, func() bool { return h.out.count() > 0 })

		h.intel.mu.Lock()
		h.intel.bargeAt = h.intel.frames + 1
		h.intel.mu.Unlock()

		h.feedUntil(t, 20*time.Second, func() bool { return h.pipeline.BargeIns() > 0 })

		// Measured around the controller call, inside the analyser — the same
		// place Phase 11D measures it.
		latency, ok := h.intel.lastLatency()
		if !ok {
			t.Fatal("the interruption produced no latency sample")
		}
		samples = append(samples, latency)

		_ = h.pipeline.Disconnect()
		<-h.pipeline.Done()
	}

	// Clock granularity bounds what any of this can mean.
	var deltas time.Duration
	const probes = 200
	for i := 0; i < probes; i++ {
		a := time.Now()
		for {
			b := time.Now()
			if b.After(a) {
				deltas += b.Sub(a)
				break
			}
		}
	}
	resolution := deltas / probes

	var total, max time.Duration
	min := samples[0]
	for _, s := range samples {
		total += s
		if s > max {
			max = s
		}
		if s < min {
			min = s
		}
	}
	mean := total / time.Duration(len(samples))

	verdict := fmt.Sprintf("mean=%s min=%s max=%s", mean, min, max)
	if mean <= resolution {
		verdict = fmt.Sprintf("below measurable resolution (mean %s <= clock "+
			"granularity %s; max observed %s)", mean, resolution, max)
	}

	t.Logf("BARGE-IN ORCHESTRATION LATENCY over %d interruptions\n"+
		"  Aegis orchestration only: stand-in providers, no model, recogniser or\n"+
		"  synthesiser inference is included.\n"+
		"  Measured clock granularity on this machine: %s\n"+
		"  %s\n"+
		"  ADR-0004 §12's 20ms is the PROVIDER cancellation/abort budget. It is\n"+
		"  not a time-to-first-token figure and this is not a measurement of it.",
		runs, resolution, verdict)
}

// ---------------------------------------------------------------------------

func TestBargeIn_ControllerImplementsTheWholePort(t *testing.T) {
	t.Parallel()

	// EndOfSpeech is part of audiointel.SpeechController. A controller that
	// silently dropped it would be lying about what it implements.
	h := newBargeHarness(t, bargeOpts{})

	if err := h.pipeline.interrupts.EndOfSpeech(context.Background()); err != nil {
		t.Fatalf("EndOfSpeech: %v", err)
	}
	if got := h.session.endpoints.Load(); got != 1 {
		t.Errorf("EndOfSpeech reached Phase 11C %d times, want 1", got)
	}

	// With no controller wired it is a no-op rather than a panic.
	bare := newBargeHarness(t, bargeOpts{noController: true})
	if err := bare.pipeline.interrupts.EndOfSpeech(context.Background()); err != nil {
		t.Errorf("EndOfSpeech with no controller: %v", err)
	}
}

func TestBargeIn_ReasonCodesStayBounded(t *testing.T) {
	t.Parallel()

	// The reason reaches an event that leaves the process. Phase 11D's code
	// must be one this module's FSM will accept, or every interruption would
	// fail its state transition.
	if !validReason(ReasonBargeIn) {
		t.Error("ReasonBargeIn is not in the declared vocabulary")
	}
	if strings.TrimSpace(audiointel.ReasonCallerSpoke) == "" {
		t.Error("Phase 11D's reason code is empty")
	}
}

// TestBargeIn_RepeatedInterruptionsUnderLoadDoNotEndTheCall reproduces the
// Gate 4 defect: repeated barge-ins terminating the caller's call.
//
// # The failure this was written for
//
// Gate 4 of Task 19, run 5, seed 1786861324206691000:
//
//	the generation is 3 after 2 interruptions
//	repeated barge-ins ended the call
//
// Generation 3 with 2 deliveries means a third increment came from stop() —
// the session was ended. Turn teardown and frame ingest race constantly during
// repeated interruptions, and TWO benign losers of that race were being treated
// as unrecoverable faults:
//
//   - beginTurn's FSM transition, when the state moved between onFrame's
//     check and the transition itself;
//   - a frame written to a recognition stream that turn teardown closed
//     between the read of p.sttOpen and the Write.
//
// Neither is a fault. Both ended the call.
//
// # Why the scheduler pressure
//
// Both windows are microseconds wide. Gate 4 hits them because it runs the
// whole module ten times over with real subprocesses competing for every core;
// yielding pressure recreates that without starving neighbouring tests.
func TestBargeIn_RepeatedInterruptionsUnderLoadDoNotEndTheCall(t *testing.T) {
	// Not parallel: this test deliberately creates scheduler pressure.

	stop := make(chan struct{})
	for i := 0; i < runtime.NumCPU(); i++ {
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
					runtime.Gosched()
				}
			}
		}()
	}
	defer close(stop)

	for attempt := 0; attempt < 6; attempt++ {
		h := newBargeHarness(t, bargeOpts{repeat: true})

		if err := h.pipeline.Start(context.Background()); err != nil {
			t.Fatalf("attempt %d: Start: %v", attempt, err)
		}

		h.feedUntil(t, 30*time.Second, func() bool { return h.out.count() > 0 })

		h.intel.mu.Lock()
		h.intel.bargeAt = h.intel.frames + 1
		h.intel.mu.Unlock()

		// Keep the detections coming while turns are torn down and rebuilt.
		for i := 0; i < 120; i++ {
			_ = h.pipeline.WriteFrame(testFrame(uint64(4000 + i)))
			time.Sleep(time.Millisecond)
		}

		delivered := h.pipeline.BargeIns()

		// THE PROPERTY: a caller who keeps interrupting is still on the call.
		if h.fsm.Terminal() {
			t.Fatalf("attempt %d: repeated barge-ins ended the call; state=%s "+
				"delivered=%d generation=%d pipeline_err=%v",
				attempt, h.fsm.State(), delivered, h.pipeline.Generation(),
				h.pipeline.Err())
		}

		// One delivery, one generation. A third increment means stop() ran.
		if got := h.pipeline.Generation(); got != delivered {
			t.Fatalf("attempt %d: generation is %d after %d interruptions; the "+
				"extra increment means the session was stopped",
				attempt, got, delivered)
		}

		for _, c := range h.fsm.History() {
			if !CanTransition(c.From, c.To) {
				t.Errorf("attempt %d: undeclared transition %s -> %s",
					attempt, c.From, c.To)
			}
		}

		_ = h.pipeline.Disconnect()
		<-h.pipeline.Done()
	}
}
