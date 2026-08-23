package voice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
)

// Phase 16 T6 — THE PLAN-INPUT SEAM.
//
// Everything here exercises Pipeline.Speak: a plan the conversation has already
// decided on, submitted into the SAME governance -> generation -> chunking ->
// TTS -> delivery path a caller turn uses. There is no second response engine,
// and these tests would notice one: they assert on the same generator, the same
// synthesiser and the same media sink the transcript path uses.
//
// The completion contract is the part that needs care. EventGreetingComplete
// and EventSpeechComplete BOTH leave the frozen conversation in listening, so
// no test here may assert only the resulting state.

// frozenGreetingPlan returns the greeting plan the REAL frozen engine produces.
//
// Not a literal written here. A hand-built plan would test this package against
// its author's memory of conversation's contract; driving EventStart through the
// frozen engine tests it against the contract itself, so a change to what a
// greeting plan looks like reaches these tests instead of passing under them.
func frozenGreetingPlan(t *testing.T) conversation.Plan {
	t.Helper()

	h, err := conversation.NewHarness()
	if err != nil {
		t.Fatalf("conversation.NewHarness: %v", err)
	}
	c, err := h.Begin(conversation.ConversationID("cv-t6-greeting"))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	plan, err := c.Handle(conversation.Event{Kind: conversation.EventStart})
	if err != nil {
		t.Fatalf("EventStart: %v", err)
	}
	if plan.NextState != conversation.StateGreeting {
		t.Fatalf("the frozen greeting plan no longer carries NextState=greeting "+
			"(got %v); the discriminator this package relies on has changed",
			plan.NextState)
	}
	return plan
}

// startedHarness builds a pipeline and starts it, leaving the session
// listening — exactly where a call driver would call Speak.
func startedHarness(t *testing.T, o harnessOpts) *harness {
	t.Helper()

	h := newHarness(t, o)
	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = h.pipeline.Disconnect()
		<-h.pipeline.Done()
	})
	return h
}

// ---------------------------------------------------------------------------
// 1. The seam exists and the whole path runs
// ---------------------------------------------------------------------------

// TestSpeak_GreetingReachesGenerationSynthesisAndTheCaller is the T4 blocker,
// inverted: a greeting plan now has a route into the speaking path.
//
// It asserts every stage, because "the greeting was spoken" is only true if all
// four happened — a seam that generated text but opened no voice, or opened a
// voice but delivered no frames, would be a greeting nobody heard.
func TestSpeak_GreetingReachesGenerationSynthesisAndTheCaller(t *testing.T) {
	t.Parallel()

	h := startedHarness(t, harnessOpts{
		greetingSystem: "open the call with the lawful-basis announcement",
	})

	if err := h.pipeline.Speak(context.Background(), frozenGreetingPlan(t)); err != nil {
		t.Fatalf("Speak: %v", err)
	}

	if got := h.gen.started.Load(); got != 1 {
		t.Errorf("the generator ran %d times for one greeting, want 1", got)
	}
	if got := h.tts.streamCount(); got != 1 {
		t.Errorf("synthesis opened %d times for one greeting, want 1", got)
	}
	if got := len(h.tts.submissions()); got == 0 {
		t.Error("no clause reached the synthesiser: the greeting was generated " +
			"but never spoken")
	}
	if got := h.out.count(); got == 0 {
		t.Error("no audio frame reached the caller for the greeting")
	}

	// The caller said nothing, and nothing pretended they did.
	if got := h.planner.calls.Load(); got != 0 {
		t.Errorf("the planner was asked to make %d dialogue decisions for a "+
			"greeting the conversation had already decided", got)
	}
	if got := h.stt.opened.Load(); got != 0 {
		t.Errorf("recognition opened %d times for a greeting", got)
	}
}

// ---------------------------------------------------------------------------
// 2. The completion contract — the critical test
// ---------------------------------------------------------------------------

// TestSpeak_GreetingCompletesAsAGreetingAndNotAsSpeech is the mutation-critical
// assertion of this task.
//
// Both EventGreetingComplete and EventSpeechComplete are accepted from
// StateGreeting and both leave the conversation in listening, so a test that
// checked the state would pass with either. The IDENTITY of the event, and
// exactly-once, are the only things that separate a correct implementation from
// one that corrupts the turn record on the very first turn of every call.
func TestSpeak_GreetingCompletesAsAGreetingAndNotAsSpeech(t *testing.T) {
	t.Parallel()

	h := startedHarness(t, harnessOpts{greetingSystem: "greet"})

	if err := h.pipeline.Speak(context.Background(), frozenGreetingPlan(t)); err != nil {
		t.Fatalf("Speak: %v", err)
	}

	if got := h.planner.greetings.Load(); got != 1 {
		t.Errorf("EventGreetingComplete was sent %d times, want exactly 1", got)
	}
	if got := h.planner.completions.Load(); got != 0 {
		t.Errorf("EventSpeechComplete was sent %d times for a GREETING, want 0: "+
			"the conversation would record an ordinary turn where the opening "+
			"announcement belongs", got)
	}
	if got := h.planner.abandons.Load(); got != 0 {
		t.Errorf("EventResponseAbandoned was sent %d times for a greeting that "+
			"was delivered, want 0", got)
	}
}

// TestSpeak_TheCompletionFollowsTheLastDeliveredFrame pins the ORDERING.
//
// A completion sent while audio is still in flight tells the conversation the
// agent has finished speaking when it has not: the floor moves to the caller
// mid-announcement, and the caller's first words are classified against a turn
// that is still playing.
//
// Observed at the instant of the event, on the pipeline's own goroutine.
func TestSpeak_TheCompletionFollowsTheLastDeliveredFrame(t *testing.T) {
	t.Parallel()

	h := startedHarness(t, harnessOpts{greetingSystem: "greet"})

	deliveredAtCompletion := -1
	h.planner.atEvent = func(e conversation.Event) {
		if e.Kind == conversation.EventGreetingComplete {
			deliveredAtCompletion = h.out.count()
		}
	}

	if err := h.pipeline.Speak(context.Background(), frozenGreetingPlan(t)); err != nil {
		t.Fatalf("Speak: %v", err)
	}

	total := h.out.count()
	if total == 0 {
		t.Fatal("no audio was delivered, so the ordering is untestable")
	}
	if deliveredAtCompletion < 0 {
		t.Fatalf("EventGreetingComplete was never sent, so the ordering is "+
			"untestable; %d frames were delivered", total)
	}
	if deliveredAtCompletion != total {
		t.Errorf("EventGreetingComplete was sent with %d of %d frames delivered; "+
			"the conversation was told the greeting had finished while it was "+
			"still playing", deliveredAtCompletion, total)
	}
}

// ---------------------------------------------------------------------------
// 3. Generation input
// ---------------------------------------------------------------------------

// TestSpeak_GreetingIsGeneratedFromInstructionWithNoFabricatedTranscript
// records the second-order problem T4 found, now answered.
//
// The transcript path builds Messages from the caller's words. A greeting has no
// caller — so the request must carry NO message at all (the frozen kernel
// assembles from session context when Messages is empty, kernel.go:573) and the
// instruction must come from configuration. An empty user message would be a
// fabricated transcript; a greeting string in this package would be this package
// writing the product's words.
func TestSpeak_GreetingIsGeneratedFromInstructionWithNoFabricatedTranscript(t *testing.T) {
	t.Parallel()

	const instruction = "open the call with the lawful-basis announcement"
	h := startedHarness(t, harnessOpts{
		system:         "session-wide instruction",
		greetingSystem: instruction,
	})

	if err := h.pipeline.Speak(context.Background(), frozenGreetingPlan(t)); err != nil {
		t.Fatalf("Speak: %v", err)
	}

	spec, ok := h.gen.lastSpec()
	if !ok {
		t.Fatal("no generation request was made for the greeting")
	}
	if len(spec.Messages) != 0 {
		t.Errorf("the greeting request carried %d message(s): %v — a greeting has "+
			"no caller transcript and none may be invented",
			len(spec.Messages), spec.Messages)
	}
	if spec.System != instruction {
		t.Errorf("the greeting request carried System=%q, want the configured "+
			"greeting instruction %q", spec.System, instruction)
	}
}

// TestSpeak_ANonGreetingPlanUsesTheSessionInstruction keeps the greeting
// instruction where it belongs. It is the opening turn's, not every turn's.
func TestSpeak_ANonGreetingPlanUsesTheSessionInstruction(t *testing.T) {
	t.Parallel()

	h := startedHarness(t, harnessOpts{
		system:         "session-wide instruction",
		greetingSystem: "greeting-only instruction",
	})

	plan := conversation.Plan{
		Action:    conversation.ActionClarify,
		Reason:    "ambiguous",
		NextState: conversation.StateSpeaking,
	}
	if err := h.pipeline.Speak(context.Background(), plan); err != nil {
		t.Fatalf("Speak: %v", err)
	}

	spec, ok := h.gen.lastSpec()
	if !ok {
		t.Fatal("no generation request was made")
	}
	if spec.System != "session-wide instruction" {
		t.Errorf("System=%q for an ordinary plan, want the session instruction",
			spec.System)
	}
}

// TestSpeak_AnOrdinaryPlanStillCompletesAsSpeech is the other half of the
// discriminator, and the regression guard for every non-greeting use of the
// seam.
func TestSpeak_AnOrdinaryPlanStillCompletesAsSpeech(t *testing.T) {
	t.Parallel()

	h := startedHarness(t, harnessOpts{})

	plan := conversation.Plan{
		Action:    conversation.ActionAsk,
		Reason:    "slot_missing",
		NextState: conversation.StateSpeaking,
	}
	if err := h.pipeline.Speak(context.Background(), plan); err != nil {
		t.Fatalf("Speak: %v", err)
	}

	if got := h.planner.completions.Load(); got != 1 {
		t.Errorf("EventSpeechComplete was sent %d times, want 1", got)
	}
	if got := h.planner.greetings.Load(); got != 0 {
		t.Errorf("EventGreetingComplete was sent %d times for an ordinary plan, "+
			"want 0: the frozen engine ACCEPTS it mid-conversation and would "+
			"record a greeting where a turn belongs", got)
	}
}

// ---------------------------------------------------------------------------
// 4. Failure paths
// ---------------------------------------------------------------------------

// TestSpeak_GenerationFailureAbandonsTheGreeting.
//
// The greeting was accepted and then produced no audio. Without the abandonment
// the conversation keeps the agent's floor forever and the caller is never
// heard — and INV-CV-2 makes the opening turn NON-YIELDING, so this is the one
// turn where a stranded floor cannot be recovered by the caller talking.
func TestSpeak_GenerationFailureAbandonsTheGreeting(t *testing.T) {
	t.Parallel()

	h := startedHarness(t, harnessOpts{
		greetingSystem: "greet",
		generatorErr:   errors.New("model unavailable"),
	})

	err := h.pipeline.Speak(context.Background(), frozenGreetingPlan(t))
	if err == nil {
		t.Fatal("Speak reported success for a greeting that was never generated")
	}

	if got := h.planner.abandons.Load(); got != 1 {
		t.Errorf("EventResponseAbandoned was sent %d times, want exactly 1", got)
	}
	if got := h.planner.greetings.Load(); got != 0 {
		t.Errorf("EventGreetingComplete was sent %d times for a greeting that "+
			"failed, want 0", got)
	}
	if got := h.planner.completions.Load(); got != 0 {
		t.Errorf("EventSpeechComplete was sent %d times for a greeting that "+
			"failed, want 0", got)
	}
	if got := h.out.count(); got != 0 {
		t.Errorf("%d frames were delivered for a greeting that never generated", got)
	}
}

// TestSpeak_SynthesisFailureAbandonsTheGreeting — the same contract one stage
// later, where the voice itself will not open.
func TestSpeak_SynthesisFailureAbandonsTheGreeting(t *testing.T) {
	t.Parallel()

	h := startedHarness(t, harnessOpts{
		greetingSystem: "greet",
		ttsOpenErr:     errors.New("no voice"),
	})

	err := h.pipeline.Speak(context.Background(), frozenGreetingPlan(t))
	if err == nil {
		t.Fatal("Speak reported success for a greeting that was never synthesised")
	}
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Errorf("Speak returned %v, want an ErrProviderUnavailable", err)
	}

	if got := h.planner.abandons.Load(); got != 1 {
		t.Errorf("EventResponseAbandoned was sent %d times, want exactly 1", got)
	}
	if got := h.planner.greetings.Load() + h.planner.completions.Load(); got != 0 {
		t.Errorf("%d completion events were sent for a greeting that was never "+
			"spoken, want 0", got)
	}
}

// TestSpeak_GovernanceDenialAbandonsWithoutSpeaking.
func TestSpeak_GovernanceDenialAbandonsWithoutSpeaking(t *testing.T) {
	t.Parallel()

	h := startedHarness(t, harnessOpts{greetingSystem: "greet", deny: true})

	if err := h.pipeline.Speak(context.Background(), frozenGreetingPlan(t)); err == nil {
		t.Fatal("Speak reported success for a greeting governance refused")
	} else if !errors.Is(err, ErrGovernanceDenied) {
		t.Errorf("Speak returned %v, want ErrGovernanceDenied", err)
	}

	if got := h.gen.started.Load(); got != 0 {
		t.Errorf("generation ran %d times after a refusal", got)
	}
	if got := h.planner.abandons.Load(); got != 1 {
		t.Errorf("EventResponseAbandoned was sent %d times, want 1", got)
	}
	if got := h.planner.greetings.Load(); got != 0 {
		t.Errorf("EventGreetingComplete was sent %d times for a refused greeting", got)
	}
}

// ---------------------------------------------------------------------------
// 5. Cancellation
// ---------------------------------------------------------------------------

// TestSpeak_CancellationEmitsNeitherCompletionNorAbandonment.
//
// A cancelled greeting did not finish and was not abandoned mid-response: the
// call is going away. Fabricating either event would put a lifecycle record on a
// conversation nobody is having.
//
// The cancellation lands while the first frame is being delivered, held there by
// the sink handshake — no sleep, and no guess about the width of the window.
func TestSpeak_CancellationEmitsNeitherCompletionNorAbandonment(t *testing.T) {
	t.Parallel()

	gate := make(chan struct{})
	arrived := make(chan struct{})
	h := startedHarness(t, harnessOpts{
		greetingSystem: "greet",
		sinkGate:       gate, sinkArrived: arrived,
	})

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- h.pipeline.Speak(ctx, frozenGreetingPlan(t)) }()

	select {
	case <-arrived:
	case err := <-errc:
		t.Fatalf("Speak finished before any audio was delivered: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("no audio reached the sink")
	}

	cancel()
	close(gate)

	select {
	case err := <-errc:
		if err == nil {
			t.Error("Speak reported success for a cancelled greeting")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Speak did not return after cancellation")
	}

	if got := h.planner.greetings.Load(); got != 0 {
		t.Errorf("EventGreetingComplete was sent %d times after cancellation, want 0", got)
	}
	if got := h.planner.completions.Load(); got != 0 {
		t.Errorf("EventSpeechComplete was sent %d times after cancellation, want 0", got)
	}
	if got := h.planner.abandons.Load(); got != 0 {
		t.Errorf("EventResponseAbandoned was sent %d times after cancellation, "+
			"want 0: the call was cancelled, not abandoned mid-response", got)
	}
}

// ---------------------------------------------------------------------------
// 6. Concurrency
// ---------------------------------------------------------------------------

// TestSpeak_ASecondSpeakIsRefusedWhileOneIsInFlight.
//
// Refused, NOT QUEUED. Two responses queued behind one another would play the
// second into a conversation that has already moved on, and the pipeline holds
// exactly one active TTS stream and one active dispatcher — a queued second
// call would silently overwrite both.
func TestSpeak_ASecondSpeakIsRefusedWhileOneIsInFlight(t *testing.T) {
	t.Parallel()

	gate := make(chan struct{})
	arrived := make(chan struct{})
	h := startedHarness(t, harnessOpts{
		greetingSystem: "greet",
		sinkGate:       gate, sinkArrived: arrived,
	})

	errc := make(chan error, 1)
	go func() { errc <- h.pipeline.Speak(context.Background(), frozenGreetingPlan(t)) }()

	select {
	case <-arrived:
	case err := <-errc:
		t.Fatalf("the first Speak finished before the handshake: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("no audio reached the sink")
	}

	second := h.pipeline.Speak(context.Background(), conversation.Plan{
		Action: conversation.ActionRespond, NextState: conversation.StateSpeaking,
	})
	if second == nil {
		t.Error("a second Speak was accepted while one was already in flight")
	} else if !errors.Is(second, ErrInvalidTransition) {
		t.Errorf("the second Speak returned %v, want an ErrInvalidTransition", second)
	}

	close(gate)
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("the first Speak failed: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the first Speak did not return")
	}

	// One response, one completion. The refusal produced nothing at all.
	if got := h.planner.greetings.Load(); got != 1 {
		t.Errorf("%d greeting completions, want 1", got)
	}
	if got := h.gen.started.Load(); got != 1 {
		t.Errorf("generation ran %d times, want 1: the refused call must produce "+
			"no work", got)
	}
}

// ---------------------------------------------------------------------------
// 7. Refusals
// ---------------------------------------------------------------------------

// TestSpeak_RefusesAPlanThatDoesNotSpeak keeps the seam inside the policy the
// transcript path already follows.
//
// Transfer, escalation, rejection, ignore, wait and end are the layer above's to
// execute. A seam that spoke them would let a caller hear the pipeline's words
// for a decision it did not make — and ActionReject in particular is excluded
// deliberately, exactly as planSpeaks documents.
func TestSpeak_RefusesAPlanThatDoesNotSpeak(t *testing.T) {
	t.Parallel()

	for _, action := range []conversation.Action{
		conversation.ActionTransfer,
		conversation.ActionEscalate,
		conversation.ActionReject,
		conversation.ActionIgnore,
		conversation.ActionWait,
		conversation.ActionEnd,
	} {
		t.Run(action.String(), func(t *testing.T) {
			t.Parallel()

			h := startedHarness(t, harnessOpts{})
			err := h.pipeline.Speak(context.Background(),
				conversation.Plan{Action: action, NextState: conversation.StateSpeaking})

			if err == nil {
				t.Fatalf("Speak accepted %v, which produces no agent speech", action)
			}
			if !errors.Is(err, ErrPlanNotSpeakable) {
				t.Errorf("Speak returned %v, want ErrPlanNotSpeakable", err)
			}
			if got := h.gen.started.Load(); got != 0 {
				t.Errorf("generation ran %d times for a plan that does not speak", got)
			}
			if got := h.out.count(); got != 0 {
				t.Errorf("%d frames were delivered for a plan that does not speak", got)
			}
			if got := h.planner.greetings.Load() + h.planner.completions.Load() +
				h.planner.abandons.Load(); got != 0 {
				t.Errorf("%d lifecycle events were sent for a refused plan, want 0", got)
			}
		})
	}
}

// TestSpeak_RefusesASessionThatIsNotRunning covers both ends of the session's
// life: before Start, and after the call has gone.
func TestSpeak_RefusesASessionThatIsNotRunning(t *testing.T) {
	t.Parallel()

	t.Run("not started", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, harnessOpts{greetingSystem: "greet"})
		err := h.pipeline.Speak(context.Background(), frozenGreetingPlan(t))
		if !errors.Is(err, ErrSessionClosed) {
			t.Errorf("Speak on an unstarted pipeline returned %v, want ErrSessionClosed", err)
		}
	})

	t.Run("terminal", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, harnessOpts{greetingSystem: "greet"})
		if err := h.pipeline.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := h.pipeline.Disconnect(); err != nil {
			t.Fatalf("Disconnect: %v", err)
		}
		<-h.pipeline.Done()

		err := h.pipeline.Speak(context.Background(), frozenGreetingPlan(t))
		if !errors.Is(err, ErrSessionClosed) {
			t.Errorf("Speak on an ended session returned %v, want ErrSessionClosed", err)
		}
		if got := h.gen.started.Load(); got != 0 {
			t.Errorf("generation ran %d times on an ended session", got)
		}
	})
}

// ---------------------------------------------------------------------------
// 8. The transcript path is unchanged
// ---------------------------------------------------------------------------

// TestSpeak_TheTranscriptPathStillOwnsItsOwnCompletion.
//
// Speak and runTurn share one implementation. This drives an ordinary caller
// turn — no Speak anywhere — and pins the behaviour that sharing must not have
// altered: one dialogue decision, one EventSpeechComplete, no greeting event.
func TestSpeak_TheTranscriptPathStillOwnsItsOwnCompletion(t *testing.T) {
	t.Parallel()

	h := startedHarness(t, harnessOpts{system: "session-wide instruction"})
	h.feed(t, 6)
	h.obs.waitTurn(t, 30*time.Second)

	if got := h.planner.calls.Load(); got != 1 {
		t.Errorf("the planner made %d dialogue decisions for one caller turn, want 1", got)
	}
	if got := h.planner.completions.Load(); got != 1 {
		t.Errorf("EventSpeechComplete was sent %d times, want 1", got)
	}
	if got := h.planner.greetings.Load(); got != 0 {
		t.Errorf("EventGreetingComplete was sent %d times for a caller turn, want 0", got)
	}

	// And the caller's words still reach the generator.
	spec, ok := h.gen.lastSpec()
	if !ok {
		t.Fatal("no generation request was made for the caller turn")
	}
	if len(spec.Messages) != 1 {
		t.Fatalf("the caller turn's request carried %d messages, want 1", len(spec.Messages))
	}
	if spec.Messages[0].Content != defaultSegments()[2].Text {
		t.Errorf("the request carried %q, want the final transcript",
			spec.Messages[0].Content)
	}
	if spec.System != "session-wide instruction" {
		t.Errorf("the caller turn's request carried System=%q, want the session "+
			"instruction", spec.System)
	}
}
