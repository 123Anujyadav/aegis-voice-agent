package voice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	governance "github.com/callscreen/callscreen-platform/packages/go/governance"
	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
	"github.com/callscreen/callscreen-platform/packages/go/voice/providers/process"
)

// ---------------------------------------------------------------------------
// The failure matrix
// ---------------------------------------------------------------------------
//
// # What is being asserted, and what is not
//
// "An error was returned" is close to worthless here. Every one of these
// failures is survivable, and the question that matters is what the system
// looks like AFTERWARDS: whether a process is still running, whether a
// goroutine is still parked, whether a queue is still holding a call's worth of
// audio, whether the next caller can be served. A pipeline can return a
// beautifully typed error and still have leaked a subprocess.
//
// So every case below ends in [assertRecovered], which checks the same
// invariants regardless of what went wrong.
//
// # Deterministic, not timed
//
// Nothing here sleeps waiting for a crash to maybe happen. Faults are scripted
// behaviours in the stand-in providers, the circuit breaker's cooldown is
// advanced on an injected clock rather than waited out, and process cleanup is
// observed through a file the child writes to rather than through a signal
// probe that is meaningless on Windows.

// recovery describes the state a pipeline must be in after a failure.
type recovery struct {
	// terminal reports whether the session should have ended. A provider fault
	// mid-turn should NOT end a call; a disconnect should.
	terminal bool

	// wantState is the state to insist on, or empty for "any valid state".
	wantState SessionState
}

// assertRecovered is the post-failure contract, applied to every case.
func assertRecovered(t *testing.T, h *harness, want recovery) {
	t.Helper()

	p, fsm := h.pipeline, h.fsm

	// --- the session is where it should be, by declared transitions only ----
	if got := fsm.Terminal(); got != want.terminal {
		t.Errorf("session terminal=%v, want %v (state %s)", got, want.terminal, fsm.State())
	}
	if want.wantState != "" && fsm.State() != want.wantState {
		t.Errorf("session is in %s, want %s", fsm.State(), want.wantState)
	}
	if !fsm.State().Valid() {
		t.Errorf("session is in an undeclared state %q", fsm.State())
	}
	for _, c := range fsm.History() {
		if !CanTransition(c.From, c.To) {
			t.Errorf("the failure produced an undeclared transition %s -> %s",
				c.From, c.To)
		}
	}

	// --- no queue is holding anything unbounded ------------------------------
	if depth, bound := len(p.frames), cap(p.frames); depth > bound {
		t.Errorf("the inbound queue holds %d frames against a bound of %d", depth, bound)
	}

	// --- no stale audio may arrive from here on -----------------------------
	//
	// Sampled per synthesis stream, because a later turn legitimately speaking
	// is not the failed turn leaking.
	streams := h.tts.streamCount()
	before := make([]int, streams)
	for i := range before {
		before[i] = h.out.countFrom(i)
	}
	time.Sleep(250 * time.Millisecond)
	for i := 0; i < streams-1; i++ {
		if after := h.out.countFrom(i); after != before[i] {
			t.Errorf("synthesis stream %d delivered %d more frames after the "+
				"failure: stale audio reached the caller", i, after-before[i])
		}
	}

	// --- the pipeline stops when it is meant to -----------------------------
	if want.terminal {
		select {
		case <-p.Done():
		case <-time.After(15 * time.Second):
			t.Error("the pipeline did not shut down after a terminal failure")
		}
	}
}

// assertNoGoroutineLeak brackets a body and checks the count settles back.
//
// Counting is meaningless while other tests run, so callers must not be
// parallel.
func assertNoGoroutineLeak(t *testing.T, tolerance int, body func()) {
	t.Helper()

	settle := func() int {
		var n int
		for i := 0; i < 60; i++ {
			n = runtime.NumGoroutine()
			time.Sleep(20 * time.Millisecond)
			if runtime.NumGoroutine() == n {
				return n
			}
		}
		return n
	}

	before := settle()
	body()
	after := settle()

	if after > before+tolerance {
		t.Errorf("goroutines went from %d to %d across the failure: something the "+
			"failed turn started is still running", before, after)
	}
}

// assertNextSessionWorks proves the process is still able to serve a call.
//
// The most consequential post-failure question there is: a leak that only
// damages the NEXT caller is the one that reaches production.
func assertNextSessionWorks(t *testing.T) {
	t.Helper()

	h := newHarness(t, harnessOpts{})
	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("a fresh session could not start after a failure: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	h.feed(t, 4)
	h.obs.waitTurn(t, 20*time.Second)

	if h.out.count() == 0 {
		t.Error("a fresh session produced no audio after an earlier failure")
	}
	if err := h.pipeline.Err(); err != nil {
		t.Errorf("a fresh session failed after an earlier failure: %v", err)
	}
}

// runFailure starts a pipeline, feeds it, and waits for the turn to resolve.
func runFailure(t *testing.T, o harnessOpts, wait time.Duration) *harness {
	t.Helper()

	h := newHarness(t, o)
	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Frames are written TOLERANTLY here, unlike harness.feed.
	//
	// Some faults end the session on the very first frame — a missing
	// recogniser cannot open a turn at all — and the remaining frames are then
	// correctly refused. That refusal is the pipeline working, and racing it is
	// what made this intermittent: whether frame 2 arrived before or after the
	// ingest goroutine failed the session depended on the scheduler.
	for i := 0; i < 4; i++ {
		if err := h.pipeline.WriteFrame(testFrame(uint64(i))); err != nil &&
			!errors.Is(err, ErrSessionClosed) && !errors.Is(err, ErrBackpressure) {
			t.Fatalf("WriteFrame(%d): %v", i, err)
		}
	}

	select {
	case <-h.obs.turnDone:
	case <-time.After(wait):
		// Not every fault resolves into a completed turn — a hung provider
		// never will — so this is a bound, not an assertion.
	}
	return h
}

// ---------------------------------------------------------------------------
// 1-4: recognition
// ---------------------------------------------------------------------------

func TestFailure_STTProviderMissing(t *testing.T) {
	t.Parallel()

	// Case 1. No recogniser is registered for the language at all — the state a
	// developer without whisper installed is actually in.
	reg := newRegistry(t, ModeDevelopment)

	tts := &recordingTTS{id: speech.ProviderID("tts-test"), framesPerChunk: 1}
	spec := sttSpec()
	spec.Engine = "piper"
	if err := reg.RegisterTTS(tts, spec); err != nil {
		t.Fatalf("RegisterTTS: %v", err)
	}

	_, err := reg.PickSTT(langEN)
	if err == nil {
		t.Fatal("a language with no recogniser was routed")
	}
	// The router's two errors mean different things and must not be flattened:
	// nobody declares it, versus some do and none is healthy.
	if !errors.Is(err, speech.ErrUnsupportedLanguage) {
		t.Errorf("want ErrUnsupportedLanguage, got %v", err)
	}

	h := runFailure(t, harnessOpts{
		sttOpenErr: fmt.Errorf("%w: whisper.cpp is not installed",
			speech.ErrProviderUnavailable),
	}, 15*time.Second)
	defer func() { _ = h.pipeline.Disconnect() }()

	// A missing provider is a session fault, not a silent nothing: the pipeline
	// cannot open a turn at all.
	if err := h.pipeline.Err(); err == nil {
		t.Error("a missing recogniser produced no recorded failure")
	} else if !errors.Is(err, ErrProviderUnavailable) {
		t.Errorf("want ErrProviderUnavailable, got %v", err)
	}

	assertRecovered(t, h, recovery{terminal: true})
}

func TestFailure_STTProcessCrash(t *testing.T) {
	t.Parallel()

	// Case 2. The recogniser dies mid-utterance: a partial arrived, then the
	// stream ended with no final. The caller said something and it was lost.
	h := runFailure(t, harnessOpts{sttFault: "crash"}, 20*time.Second)
	defer func() { _ = h.pipeline.Disconnect() }()

	h.obs.mu.Lock()
	segments := len(h.obs.transcript)
	outcome := h.obs.outcome
	h.obs.mu.Unlock()

	if segments == 0 {
		t.Error("the partial that did arrive was not forwarded")
	}

	// A lost utterance returns the floor to the caller. It is NOT a failed
	// session: the caller can simply say it again, and ending the call would be
	// a far worse outcome than a repeated sentence.
	if outcome != OutcomeCompleted {
		t.Errorf("turn outcome is %q; a lost utterance is a completed turn with "+
			"nothing to say, not a failure", outcome)
	}

	assertRecovered(t, h, recovery{terminal: false, wantState: StateListening})
}

func TestFailure_STTTimeout(t *testing.T) {
	t.Parallel()

	// Case 3. Recognition that never answers and never ends. Nothing but the
	// session's own lifetime can end it, so the test proves exactly that.
	h := runFailure(t, harnessOpts{sttFault: "hang"}, 2*time.Second)

	if h.fsm.State() != StateTranscribing {
		t.Errorf("a hung recogniser left the session in %s, want transcribing",
			h.fsm.State())
	}

	// Disconnect must reclaim it rather than waiting for a provider that never
	// answers.
	if err := h.pipeline.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	assertRecovered(t, h, recovery{terminal: true, wantState: StateCompleted})
}

func TestFailure_STTInvalidOutput(t *testing.T) {
	t.Parallel()

	// Case 4. A final that carries nothing usable. Harder than a crash: the
	// provider believes it succeeded, so nothing upstream reports an error.
	h := runFailure(t, harnessOpts{sttFault: "invalid"}, 20*time.Second)
	defer func() { _ = h.pipeline.Disconnect() }()

	// An empty transcript must not become an empty question to the model.
	if got := h.planner.calls.Load(); got != 0 {
		t.Errorf("the planner was consulted %d times for an empty transcript", got)
	}
	if got := h.gen.started.Load(); got != 0 {
		t.Errorf("generation ran %d times for an empty transcript", got)
	}
	if subs := h.tts.submissions(); len(subs) != 0 {
		t.Errorf("%d clauses were synthesised for an empty transcript", len(subs))
	}

	assertRecovered(t, h, recovery{terminal: false, wantState: StateListening})
}

// ---------------------------------------------------------------------------
// 5-8: synthesis
// ---------------------------------------------------------------------------

func TestFailure_TTSProviderMissing(t *testing.T) {
	t.Parallel()

	// Case 5. The voice will not open. The turn is lost; the call is not.
	h := runFailure(t, harnessOpts{
		ttsOpenErr: fmt.Errorf("%w: piper is not installed", speech.ErrProviderUnavailable),
	}, 20*time.Second)
	defer func() { _ = h.pipeline.Disconnect() }()

	if err := h.pipeline.Err(); err == nil {
		t.Error("a missing voice produced no recorded failure")
	} else if !errors.Is(err, ErrProviderUnavailable) {
		t.Errorf("want ErrProviderUnavailable, got %v", err)
	}

	h.obs.mu.Lock()
	outcome := h.obs.outcome
	h.obs.mu.Unlock()
	if outcome != OutcomeFailed {
		t.Errorf("turn outcome is %q, want failed", outcome)
	}

	// One provider hiccup must not end a conversation the caller is still in.
	assertRecovered(t, h, recovery{terminal: false, wantState: StateListening})
}

func TestFailure_TTSProcessCrash(t *testing.T) {
	t.Parallel()

	// Case 6. The voice accepts text and then dies: no audio for the response.
	h := runFailure(t, harnessOpts{ttsFault: "crash"}, 20*time.Second)
	defer func() { _ = h.pipeline.Disconnect() }()

	if h.out.count() != 0 {
		t.Errorf("%d frames arrived from a crashed synthesiser", h.out.count())
	}
	if subs := h.tts.submissions(); len(subs) == 0 {
		t.Error("no text reached the synthesiser at all; the test did not " +
			"exercise a crash during synthesis")
	}

	assertRecovered(t, h, recovery{terminal: false, wantState: StateListening})
}

func TestFailure_TTSTimeout(t *testing.T) {
	t.Parallel()

	// Case 7. The voice accepts text and produces nothing, forever. The turn
	// timeout is what must reclaim it.
	h := runFailure(t, harnessOpts{
		ttsFault: "hang", turnTimeout: 800 * time.Millisecond,
	}, 20*time.Second)
	defer func() { _ = h.pipeline.Disconnect() }()

	// The turn ended without the session ending: a hung voice costs one answer.
	if h.fsm.Terminal() {
		t.Error("a hung synthesiser ended the call")
	}
	if h.out.count() != 0 {
		t.Errorf("%d frames arrived from a synthesiser that produced none",
			h.out.count())
	}

	assertRecovered(t, h, recovery{terminal: false})
}

func TestFailure_TTSInvalidOutput(t *testing.T) {
	t.Parallel()

	// Case 8. Synthesis refuses every chunk it is given. The response exists but
	// cannot be spoken.
	h := runFailure(t, harnessOpts{
		ttsSynthErr: fmt.Errorf("%w: unsupported voice", speech.ErrInvalidAudio),
	}, 20*time.Second)
	defer func() { _ = h.pipeline.Disconnect() }()

	if h.out.count() != 0 {
		t.Errorf("%d frames arrived from a voice that refused every chunk",
			h.out.count())
	}
	if subs := h.tts.submissions(); len(subs) != 0 {
		t.Errorf("%d chunks were recorded as submitted despite being refused",
			len(subs))
	}

	assertRecovered(t, h, recovery{terminal: false})
}

// ---------------------------------------------------------------------------
// 9: generation
// ---------------------------------------------------------------------------

func TestFailure_LLMUnavailable(t *testing.T) {
	t.Parallel()

	// Case 9. The model daemon is down. On this machine that is also the real
	// state of affairs: Ollama has no model pulled, so this is the failure a
	// developer here would actually hit.
	h := runFailure(t, harnessOpts{
		generatorErr: fmt.Errorf("%w: no model is available", rt.ErrProviderUnavailable),
	}, 20*time.Second)
	defer func() { _ = h.pipeline.Disconnect() }()

	err := h.pipeline.Err()
	if err == nil {
		t.Fatal("an unavailable model produced no recorded failure")
	}
	if !errors.Is(err, ErrProviderFailed) {
		t.Errorf("want ErrProviderFailed, got %v", err)
	}

	if h.out.count() != 0 {
		t.Error("audio was produced without a model")
	}

	assertRecovered(t, h, recovery{terminal: false, wantState: StateListening})
}

// ---------------------------------------------------------------------------
// 10-11: provider switch and recovery
// ---------------------------------------------------------------------------

func TestFailure_ProviderSwitchAndRecovery(t *testing.T) {
	t.Parallel()

	// Cases 10 and 11, in one scenario because recovery is only meaningful
	// after a switch.
	//
	// The mechanism is speech.ProviderRouter's, unchanged: consecutive failures
	// open the primary's breaker, the secondary takes over, and the cooldown
	// returns the primary. Nothing here re-implements any of it — the registry
	// delegates, and this drives it through the registry.
	clock := rt.NewFakeClock(time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC))
	router := newRouterWithClock(t, clock)
	reg := newRegistryWithRouter(t, router)

	primary := &recordingSTT{
		id: speech.ProviderID("stt-primary"), segments: defaultSegments(),
		openErr: errors.New("primary recogniser is down"),
	}
	secondary := &recordingSTT{
		id: speech.ProviderID("stt-secondary"), segments: defaultSegments(),
	}

	primarySpec := sttSpec()
	secondarySpec := sttSpec()
	secondarySpec.Tier = speech.TierSecondary

	if err := reg.RegisterSTT(primary, primarySpec); err != nil {
		t.Fatalf("RegisterSTT(primary): %v", err)
	}
	if err := reg.RegisterSTT(secondary, secondarySpec); err != nil {
		t.Fatalf("RegisterSTT(secondary): %v", err)
	}

	// While healthy, the primary is chosen.
	if p, err := reg.PickSTT(langEN); err != nil || p.ID() != primary.id {
		t.Fatalf("the healthy primary was not chosen: %v %v", p, err)
	}

	// --- provider A fails, repeatedly ---------------------------------------
	//
	// The failures are REAL open attempts, not reported outcomes invented by
	// the test: the router is told what actually happened, which is the only
	// way this proves the pipeline's own failure path feeds the breaker.
	threshold := speech.DefaultRouterConfig().FailureThreshold
	for i := 0; i < threshold; i++ {
		chosen, pickErr := reg.PickSTT(langEN)
		if pickErr != nil {
			t.Fatalf("attempt %d: no provider available: %v", i, pickErr)
		}
		if _, openErr := chosen.OpenSTT(context.Background(), speech.STTConfig{
			Session:  speech.SessionID("ses-failover"),
			Turn:     speech.TurnID(fmt.Sprintf("turn-%d", i)),
			Language: langEN,
			Format:   media.PCM16Mono16k(),
		}); openErr == nil {
			t.Fatalf("attempt %d: the failing primary opened successfully", i)
		}
		reg.Report(ProviderID(chosen.ID()), speech.OutcomeFailure)
	}

	// --- the switch ---------------------------------------------------------
	picked, err := reg.PickSTT(langEN)
	if err != nil {
		t.Fatalf("no provider served after the primary failed: %v", err)
	}
	if picked.ID() != secondary.id {
		t.Fatalf("after %d failures the router chose %s, want the secondary",
			threshold, picked.ID())
	}

	if h := reg.Health(ProviderID(primary.id)); h.State != speech.CircuitOpen {
		t.Errorf("the primary's circuit is %s, want open", h.State)
	}
	if h := reg.Health(ProviderID(secondary.id)); !h.Available() {
		t.Error("the secondary is not available to take over")
	}

	// --- provider B serves a real session, with nothing of A's in it --------
	stream, err := picked.OpenSTT(context.Background(), speech.STTConfig{
		Session:  speech.SessionID("ses-failover"),
		Turn:     speech.TurnID("turn-1"),
		Language: langEN,
		Format:   media.PCM16Mono16k(),
	})
	if err != nil {
		t.Fatalf("the secondary could not open a stream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	var got []speech.TranscriptSegment
	for seg := range stream.Results() {
		got = append(got, seg)
	}
	if len(got) == 0 {
		t.Error("the secondary produced no transcript")
	}

	// No state from A leaked into B: A was never opened successfully, and B's
	// stream carries B's session.
	if primary.opened.Load() == 0 {
		t.Error("the primary was never attempted; the switch proved nothing")
	}
	for _, seg := range got {
		if seg.Session != speech.SessionID("ses-failover") {
			t.Errorf("the secondary's transcript carries session %q", seg.Session)
		}
	}

	// --- recovery, on the injected clock ------------------------------------
	//
	// Advancing rather than waiting: the cooldown is thirty seconds, and a test
	// that slept for it would be a timing bet as well as slow.
	clock.Advance(speech.DefaultRouterConfig().CooldownPeriod + time.Second)

	recovered, err := reg.PickSTT(langEN)
	if err != nil {
		t.Fatalf("nothing served after the cooldown: %v", err)
	}
	if recovered.ID() != primary.id {
		t.Errorf("after the cooldown the router chose %s; the primary should get "+
			"its trial request back", recovered.ID())
	}
	if h := reg.Health(ProviderID(primary.id)); h.State != speech.CircuitHalfOpen {
		t.Errorf("the primary's circuit is %s after cooldown, want half_open", h.State)
	}

	// And a success closes it again.
	reg.Report(ProviderID(primary.id), speech.OutcomeSuccess)
	if h := reg.Health(ProviderID(primary.id)); h.State != speech.CircuitClosed {
		t.Errorf("a successful trial left the circuit %s, want closed", h.State)
	}
}

// ---------------------------------------------------------------------------
// 12-15: lifecycle
// ---------------------------------------------------------------------------

func TestFailure_Cancellation(t *testing.T) {
	t.Parallel()

	// Case 12.
	h := runFailure(t, harnessOpts{
		tokens: []string{"A long answer?", " That keeps going?"}, tokenGap: 150 * time.Millisecond,
		framesPerChunk: 30, ttsSynthDelay: 40 * time.Millisecond, sinkDelay: 3 * time.Millisecond,
	}, 300*time.Millisecond)

	if err := h.pipeline.Cancel(ReasonRequested); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	assertRecovered(t, h, recovery{terminal: true, wantState: StateCancelled})
}

func TestFailure_BargeInDuringTTS(t *testing.T) {
	t.Parallel()

	// Case 13, through the real Task 12 orchestration: Phase 11D's detection
	// reaches Phase 11C through audiobridge, and this layer invalidates the
	// generation. Nothing about detection is reimplemented here.
	bh := newBargeHarness(t, bargeOpts{})

	if err := bh.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = bh.pipeline.Disconnect() }()

	bh.feedUntil(t, 20*time.Second, func() bool { return bh.out.count() > 0 })

	playedBefore := bh.out.countFrom(0)

	bh.intel.mu.Lock()
	bh.intel.bargeAt = bh.intel.frames + 1
	bh.intel.mu.Unlock()

	bh.feedUntil(t, 20*time.Second, func() bool { return bh.pipeline.BargeIns() > 0 })

	// Phase 11C was actually interrupted, through the real adapter.
	if got := bh.session.interrupts.Load(); got != 1 {
		t.Errorf("speech.SpeechSession.Interrupt was called %d times, want 1", got)
	}

	// No old-generation audio, no stale queue, no stale session.
	time.Sleep(300 * time.Millisecond)
	if after := bh.out.countFrom(0); after != playedBefore {
		t.Errorf("%d frames of the interrupted turn reached media afterwards",
			after-playedBefore)
	}
	if withheld := bh.pipeline.StaleFramesBlocked(); withheld == 0 {
		t.Error("nothing was withheld; the generation guard did not engage")
	}

	assertRecovered(t, bh.harness, recovery{terminal: false})
}

func TestFailure_DisconnectDuringSTT(t *testing.T) {
	t.Parallel()

	// Case 14. The caller hangs up mid-sentence, while recognition is open.
	h := newHarness(t, harnessOpts{segmentGap: 200 * time.Millisecond})
	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.feed(t, 4)

	// Wait for recognition to be genuinely open before hanging up.
	deadline := time.After(15 * time.Second)
	for h.fsm.State() != StateTranscribing {
		select {
		case <-deadline:
			t.Fatalf("recognition never opened; session is in %s", h.fsm.State())
		case <-time.After(2 * time.Millisecond):
		}
	}

	if err := h.pipeline.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	// A hang-up is a completed call, not a cancelled one: recording it as an
	// abort would make every ordinary ending look like a fault.
	assertRecovered(t, h, recovery{terminal: true, wantState: StateCompleted})

	if err := h.pipeline.WriteFrame(testFrame(999)); !errors.Is(err, ErrSessionClosed) {
		t.Errorf("a disconnected pipeline still accepts audio: %v", err)
	}
}

func TestFailure_DisconnectDuringTTS(t *testing.T) {
	t.Parallel()

	// Case 15. The caller hangs up while the agent is speaking.
	h := newHarness(t, harnessOpts{
		tokens:   []string{"Reading your statement?", " Item one?", " Item two?"},
		tokenGap: 40 * time.Millisecond, framesPerChunk: 40,
		ttsSynthDelay: 30 * time.Millisecond, sinkDelay: 3 * time.Millisecond,
	})
	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.feed(t, 4)

	deadline := time.After(20 * time.Second)
	for h.out.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("the agent never spoke")
		case <-time.After(2 * time.Millisecond):
		}
	}

	if err := h.pipeline.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	assertRecovered(t, h, recovery{terminal: true, wantState: StateCompleted})
}

// ---------------------------------------------------------------------------
// 16-17: governance and tools
// ---------------------------------------------------------------------------

func TestFailure_GovernanceDenial(t *testing.T) {
	t.Parallel()

	// Case 16. The invoker fails the test if it is reached: the assertion is
	// "nothing executed", not "an error came back".
	inv := &recordingInvoker{forbidden: true, t: t}
	gov := &recordingGovernor{answer: scriptedDecision{
		outcome: governance.OutcomeDeny, reason: "not_permitted",
	}}

	h, gw := newGovernedPipeline(t, gov, inv)
	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	_, err := h.pipeline.InvokeTool(context.Background(), testIntent())
	if err == nil {
		t.Fatal("a denied action reported success")
	}
	if !errors.Is(err, ErrGovernanceDenied) {
		t.Errorf("want ErrGovernanceDenied, got %v", err)
	}
	if got := inv.calls.Load(); got != 0 {
		t.Errorf("the tool invoker was reached %d times for a denial", got)
	}
	if got := gw.Invoked(); got != 0 {
		t.Errorf("the gateway invoked %d times for a denial", got)
	}

	// A refusal is not a fault: the call carries on.
	if err := h.pipeline.Err(); err != nil {
		t.Errorf("a refusal was recorded as a pipeline fault: %v", err)
	}
	assertRecovered(t, h, recovery{terminal: false})
}

func TestFailure_ToolFailsAfterGovernanceApproval(t *testing.T) {
	t.Parallel()

	// Case 17. The distinction that sends an operator to the right runbook: a
	// tool that breaks after approval is a TOOL failure. Reporting it as a
	// governance denial would have somebody editing policy to fix an outage.
	inv := &recordingInvoker{err: errors.New("upstream banking API timed out")}
	gov := &recordingGovernor{answer: scriptedDecision{
		outcome: governance.OutcomeAllow, reason: "permitted",
	}}

	h, gw := newGovernedPipeline(t, gov, inv)
	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	_, err := h.pipeline.InvokeTool(context.Background(), testIntent())
	if err == nil {
		t.Fatal("a failing tool reported success")
	}

	if !errors.Is(err, ErrProviderFailed) {
		t.Errorf("want ErrProviderFailed, got %v", err)
	}
	if errors.Is(err, ErrGovernanceDenied) || errors.Is(err, ErrObligationsUnmet) {
		t.Error("a tool failure was misclassified as a governance outcome")
	}

	// Governance did allow it, and the record says so.
	if got := gw.Allowed(); got != 1 {
		t.Errorf("the approval was not counted (%d)", got)
	}
	if got := inv.calls.Load(); got != 1 {
		t.Errorf("the tool ran %d times, want 1", got)
	}

	assertRecovered(t, h, recovery{terminal: false})
}

// ---------------------------------------------------------------------------
// Resources, isolation and real process cleanup
// ---------------------------------------------------------------------------

func TestFailure_NoGoroutineLeakAcrossTheMatrix(t *testing.T) {
	// Not parallel: goroutine counting is meaningless while other tests run.

	faults := []struct {
		name string
		opts harnessOpts
	}{
		{"stt crash", harnessOpts{sttFault: "crash"}},
		{"stt invalid", harnessOpts{sttFault: "invalid"}},
		{"tts crash", harnessOpts{ttsFault: "crash"}},
		{"tts refuses", harnessOpts{ttsSynthErr: errors.New("refused")}},
		{"llm unavailable", harnessOpts{generatorErr: errors.New("down")}},
		{"tts missing", harnessOpts{ttsOpenErr: errors.New("absent")}},
	}

	assertNoGoroutineLeak(t, 6, func() {
		for _, f := range faults {
			h := runFailure(t, f.opts, 15*time.Second)
			_ = h.pipeline.Disconnect()
			select {
			case <-h.pipeline.Done():
			case <-time.After(20 * time.Second):
				t.Errorf("%s: the pipeline did not shut down", f.name)
			}
		}
	})
}

func TestFailure_ASubsequentSessionStillWorks(t *testing.T) {
	t.Parallel()

	// Run a fault, then prove the process can still serve a caller. A leak that
	// only damages the NEXT call is the one that reaches production.
	h := runFailure(t, harnessOpts{generatorErr: errors.New("model down")},
		15*time.Second)
	_ = h.pipeline.Disconnect()
	<-h.pipeline.Done()

	assertNextSessionWorks(t)
}

func TestFailure_ConcurrentFailuresStayIsolated(t *testing.T) {
	t.Parallel()

	// Independent sessions failing in different ways at the same time. A fault
	// in one must not cancel, corrupt or leak into another.
	faults := []harnessOpts{
		{sttFault: "crash"},
		{ttsFault: "crash"},
		{generatorErr: errors.New("model down")},
		{ttsOpenErr: errors.New("voice absent")},
		{}, // a healthy session, which must survive its neighbours
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(faults)*3)
	healthySpoke := make(chan bool, 1)

	for i, f := range faults {
		wg.Add(1)
		go func(n int, o harnessOpts) {
			defer wg.Done()

			healthy := o.sttFault == "" && o.ttsFault == "" &&
				o.generatorErr == nil && o.ttsOpenErr == nil

			h := newHarness(t, o)
			if err := h.pipeline.Start(context.Background()); err != nil {
				errs <- fmt.Errorf("session %d: Start: %w", n, err)
				return
			}
			defer func() { _ = h.pipeline.Disconnect() }()

			for i := 0; i < 4; i++ {
				if err := h.pipeline.WriteFrame(testFrame(uint64(i))); err != nil {
					errs <- fmt.Errorf("session %d: WriteFrame: %w", n, err)
					return
				}
			}

			select {
			case <-h.obs.turnDone:
			case <-time.After(25 * time.Second):
				if healthy {
					errs <- fmt.Errorf("session %d (healthy) never completed a turn", n)
				}
				return
			}

			if healthy {
				if h.out.count() == 0 {
					errs <- fmt.Errorf("a healthy session produced no audio while " +
						"its neighbours were failing")
				}
				if err := h.pipeline.Err(); err != nil {
					errs <- fmt.Errorf("a healthy session failed alongside broken "+
						"ones: %w", err)
				}
				healthySpoke <- true
			}

			// Whatever happened, this session's own state is coherent.
			if !h.fsm.State().Valid() {
				errs <- fmt.Errorf("session %d is in an undeclared state %q",
					n, h.fsm.State())
			}
			for _, c := range h.fsm.History() {
				if !CanTransition(c.From, c.To) {
					errs <- fmt.Errorf("session %d: undeclared transition %s -> %s",
						n, c.From, c.To)
				}
			}
		}(i, f)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	select {
	case <-healthySpoke:
	default:
		t.Error("the healthy session never spoke; isolation was not demonstrated")
	}
}

// TestFailure_CrashedProviderLeavesNoOrphanProcess uses a REAL child process.
//
// # Why this one does not use a stand-in provider
//
// Every other case here drives an in-memory stub, which is the right way to
// make a failure deterministic. But "no orphan process" is not a property a
// stub can have: it is a property of the operating system, and a mock of that
// boundary would test the mock.
//
// So this compiles a small program, supervises it with the same
// providers/process package the real adapters use, kills it the way a failing
// turn would, and then watches a file the child appends to. If the file stops
// growing, the child stopped running. That check is portable and cannot
// silently degrade into a no-op — unlike a signal probe, which on Windows
// reports "not alive" for every process including live ones.
func TestFailure_CrashedProviderLeavesNoOrphanProcess(t *testing.T) {
	t.Parallel()

	bin := buildHeartbeatProgram(t)
	beat := filepath.Join(t.TempDir(), "heartbeat")

	proc, err := process.New(process.Config{
		Executable:     bin,
		Args:           []string{beat},
		StartTimeout:   10 * time.Second,
		StopTimeout:    500 * time.Millisecond,
		MaxStderrBytes: 4 << 10,
	})
	if err != nil {
		t.Fatalf("process.New: %v", err)
	}
	if err := proc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	size := func() int64 {
		info, statErr := os.Stat(beat)
		if statErr != nil {
			return -1
		}
		return info.Size()
	}

	// It must genuinely be running, or the orphan check proves nothing.
	deadline := time.After(15 * time.Second)
	for size() <= 0 {
		select {
		case <-deadline:
			t.Fatalf("the child never started beating; stderr: %s", proc.StderrTail())
		case <-time.After(10 * time.Millisecond):
		}
	}

	// This child ignores the closing of its stdin, so it has to be killed —
	// which is the case a crashed or wedged provider actually presents.
	stopErr := proc.Stop(context.Background())
	if stopErr != nil && !errors.Is(stopErr, process.ErrStopTimeout) {
		t.Fatalf("Stop: %v", stopErr)
	}

	time.Sleep(150 * time.Millisecond) // let any write in flight land
	before := size()
	time.Sleep(400 * time.Millisecond)

	if after := size(); after != before {
		t.Errorf("the provider process is still running after its supervisor "+
			"stopped it: heartbeat grew from %d to %d bytes", before, after)
	}
	if proc.Running() {
		t.Error("the supervisor still reports the child as running")
	}
}

var (
	heartbeatOnce sync.Once
	heartbeatPath string
	heartbeatErr  error
)

const heartbeatSource = `package main

// A provider that will not stop when asked: it ignores the closing of its
// stdin and keeps working. Whether its side effects continue after a Stop is
// how the orphan check observes the operating system rather than trusting it.

import (
	"os"
	"time"
)

func main() {
	path := os.Args[1]
	for {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString("x")
			_ = f.Close()
		}
		time.Sleep(20 * time.Millisecond)
	}
}
`

func buildHeartbeatProgram(t *testing.T) string {
	t.Helper()

	heartbeatOnce.Do(func() {
		dir, err := os.MkdirTemp("", "voice-failure-heartbeat")
		if err != nil {
			heartbeatErr = err
			return
		}
		src := filepath.Join(dir, "main.go")
		if err := os.WriteFile(src, []byte(heartbeatSource), 0o600); err != nil {
			heartbeatErr = err
			return
		}
		out := filepath.Join(dir, "heartbeat")
		if runtime.GOOS == "windows" {
			out += ".exe"
		}
		if b, err := exec.Command("go", "build", "-o", out, src).CombinedOutput(); err != nil {
			heartbeatErr = fmt.Errorf("building the heartbeat program: %v\n%s", err, b)
			return
		}
		heartbeatPath = out
	})

	if heartbeatErr != nil {
		t.Skipf("cannot build the heartbeat program: %v", heartbeatErr)
	}
	return heartbeatPath
}

// TestFailure_RecognitionClosedMidTurnDoesNotEndTheCall is the deterministic
// form of the Gate 4 barge-in defect.
//
// # What the race is, made reproducible
//
// onFrame reads p.sttOpen under the mutex and then calls Write OUTSIDE it, so
// turn teardown — which runs constantly during repeated barge-ins — can close
// the stream in between. The recogniser then reports a closed stream, and
// every error that was not backpressure was treated as unrecoverable:
//
//	return fmt.Errorf("%w: writing audio: %v", ErrProviderFailed, err)
//
// which fails the session and ends the caller's call.
//
// A stream closing underneath a frame is ORDINARY during a barge-in. Losing
// that race costs one frame of audio. It must not cost the call.
func TestFailure_RecognitionClosedMidTurnDoesNotEndTheCall(t *testing.T) {
	t.Parallel()

	// The recogniser accepts one frame and then reports itself closed, exactly
	// as it would if the turn had been torn down between the read and the write.
	// segmentGap keeps recognition OPEN across the frames below. Without it the
	// turn finishes on the first frame, closeRecognition nils the stream, and
	// no write is attempted at all — the test would pass while exercising
	// nothing.
	h := newHarness(t, harnessOpts{
		sttCloseAfterWrites: 1,
		segmentGap:          400 * time.Millisecond,
	})

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	for i := 0; i < 8; i++ {
		if err := h.pipeline.WriteFrame(testFrame(uint64(i))); err != nil &&
			!errors.Is(err, ErrBackpressure) && !errors.Is(err, ErrSessionClosed) {
			t.Fatalf("WriteFrame(%d): %v", i, err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Give the ingest goroutine time to process every frame.
	time.Sleep(300 * time.Millisecond)

	if h.fsm.Terminal() {
		t.Errorf("a recognition stream closing mid-turn ended the call: state=%s "+
			"err=%v", h.fsm.State(), h.pipeline.Err())
	}
	if got := h.fsm.State(); got == StateFailed {
		t.Errorf("the session was marked failed by a benign teardown race")
	}
	for _, c := range h.fsm.History() {
		if !CanTransition(c.From, c.To) {
			t.Errorf("undeclared transition %s -> %s", c.From, c.To)
		}
	}
}

// TestFailure_ARealInvariantViolationIsStillFatal pins the other half of the
// race classifier.
//
// Tolerating a lost race must not become tolerating everything. If the session
// is still in the state a transition was predicated on and the transition was
// STILL refused, the state table and the code disagree — a genuine invariant
// violation, and the one class of bug the Task 10 machine exists to make loud.
func TestFailure_ARealInvariantViolationIsStillFatal(t *testing.T) {
	t.Parallel()

	reg := newRegistry(t, ModeDevelopment)
	fsm, err := NewSessionFSM(FSMConfig{Session: SessionID("ses-invariant")})
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewPipeline(PipelineConfig{
		Session: SessionID("ses-invariant"), Language: langEN,
		Format: media.PCM16Mono16k(), Registry: reg,
		Intel: &scriptedIntel{}, Planner: &scriptedPlanner{},
		Governor:  benchGovernor{outcome: governance.OutcomeAllow},
		Generator: &scriptedGenerator{}, Output: &countingSink{}, FSM: fsm,
		MaxPendingFrames: 8, MaxPendingSegments: 8, MaxPendingAudio: 8,
		TurnTimeout: time.Second, Tier: rt.TierFast,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := fsm.To(context.Background(), StateListening, ReasonOK); err != nil {
		t.Fatal(err)
	}

	refused := &TransitionError{
		Session: SessionID("ses-invariant"),
		From:    StateListening, To: StateSpeakingDetected,
		Reason: "synthetic, for this test",
	}

	// The session is STILL in the state the transition assumed. Nothing raced
	// it, so the refusal is a real disagreement and must stay fatal.
	got := p.classifyTransitionRace(refused, StateListening)
	if errors.Is(got, errTurnRaceLost) {
		t.Errorf("a refusal with the session still in %s was excused as a lost "+
			"race; a genuine invariant violation would be invisible", StateListening)
	}
	if !errors.Is(got, ErrInvalidTransition) {
		t.Errorf("the original error was lost: %v", got)
	}

	// Now the session HAS moved on. Same refusal, different meaning: this
	// goroutine simply lost, and the call must survive it.
	if err := fsm.To(context.Background(), StateSpeakingDetected, ReasonOK); err != nil {
		t.Fatal(err)
	}
	if got := p.classifyTransitionRace(refused, StateListening); !errors.Is(got, errTurnRaceLost) {
		t.Errorf("a refusal after the session moved to %s was treated as a fault: %v",
			fsm.State(), got)
	}

	// An error that is not a transition refusal is never excused.
	other := fmt.Errorf("%w: the provider is gone", ErrProviderUnavailable)
	if got := p.classifyTransitionRace(other, StateListening); errors.Is(got, errTurnRaceLost) {
		t.Error("a provider failure was excused as a lost transition race")
	}
}
