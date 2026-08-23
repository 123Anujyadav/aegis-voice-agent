package main

import (
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/voice"
)

// Phase 16 T9 — STT FAILURE POLICY.
//
// Phase 16 T8 found the gap these tests close: an STT provider that cannot open
// ends the VOICE session (`stop(StateFailed)`) and tells the CONVERSATION
// nothing. The conversation stays open beside a dead session, holding whatever
// floor it had, and nothing ever closes it.
//
// With deterministic fakes that path never fires. With a real subprocess
// recogniser — which crashes, is missing, or refuses to start — it is the
// ordinary case, which is why it had to be settled before whispercli was wired.
//
// THE POLICY, in the frozen vocabulary and with no new event:
//
//	a provider failure BEFORE any response was accepted
//	  -> EventFault   (StateError: this is why the call died)
//	  -> EventHangup  (StateEnded: and this is the call being closed)
//
// NOT EventResponseAbandoned — nothing was accepted for speech. NOT
// EventSpeechComplete — nothing was said. NOT EventInterrupt — nobody
// interrupted.

// sttFailure is the shape every one of these tests asserts.
type sttFailure struct {
	call *Call
	rig  *t7Rig
}

// awaitCallDead waits, bounded, for the call to finish CLOSING.
//
// Closed(), not Done(). Done reports that the pipeline's goroutines returned;
// the conversation is closed after that, by the watcher, so waiting on Done
// alone would race the very events these tests assert — and would have made
// this whole file intermittently green for the wrong reason.
func awaitCallDead(t *testing.T, c *Call) {
	t.Helper()
	select {
	case <-c.Closed():
	case <-time.After(30 * time.Second):
		t.Fatalf("the call did not finish closing after the STT failure; "+
			"session state=%v", c.State())
	}
}

// assertClosedByFault is the whole policy, asserted once.
func (f sttFailure) assertClosedByFault(t *testing.T, wantTyped error) {
	t.Helper()

	conv := f.call.Conversation()

	// --- the voice session is dead, and honestly so ------------------------
	if got := f.call.State(); got != voice.StateFailed {
		t.Errorf("voice session state = %v, want failed", got)
	}
	err := f.call.Pipeline().Err()
	if err == nil {
		t.Fatal("the pipeline recorded no error for a provider failure; the " +
			"typed cause is lost for observability")
	}
	if wantTyped != nil && !errors.Is(err, wantTyped) {
		t.Errorf("pipeline error = %v, want it to wrap %v", err, wantTyped)
	}

	// --- the conversation is CLOSED, not merely faulted --------------------
	//
	// StateError is explicitly NOT terminal in the frozen engine ("that is the
	// difference between an error and a failure", state.go:111), so recording
	// the fault alone would leave the conversation open and still accepting
	// events beside a session that no longer exists.
	if got := conv.State(); got != conversation.StateEnded {
		t.Errorf("conversation state = %v, want ended", got)
	}
	if !hasTrigger(conv, conversation.TriggerFault) {
		t.Error("the trace records no fault; the call was closed without saying why")
	}
	if !hasTrigger(conv, conversation.TriggerHangup) {
		t.Error("the trace records no hangup; the conversation was left open")
	}

	// --- and nothing was fabricated ----------------------------------------
	if got := completions(conv); got != 0 {
		t.Errorf("%d speech completions for a call that never spoke, want 0", got)
	}
	// Exactly the greeting, which genuinely happened before the recogniser was
	// ever needed. Asserting zero here would be asserting that the failure
	// erased a turn that really occurred.
	if got := greetings(conv); got != 1 {
		t.Errorf("%d greeting completions, want 1 (the greeting succeeded before "+
			"the recogniser was reached)", got)
	}
	if got := abandonments(conv); got != 0 {
		t.Errorf("%d abandonments, want 0: no response was ever accepted, so "+
			"there was nothing to abandon", got)
	}
	if got := len(conv.Interruptions().History()); got != 0 {
		t.Errorf("%d interruptions, want 0: nobody interrupted anything", got)
	}

	// --- and the call is genuinely finished --------------------------------
	if _, herr := conv.Handle(utter("transfer me to rajesh")); !errors.Is(herr, conversation.ErrTerminal) {
		t.Errorf("a closed conversation accepted another utterance (%v)", herr)
	}
	if aerr := f.call.Audio(t2Frame(99)); aerr == nil {
		t.Error("a dead call accepted another frame")
	}
}

// hasTrigger reports whether the frozen trace records a trigger.
func hasTrigger(conv *conversation.Conversation, want conversation.Trigger) bool {
	for _, tr := range conv.Trace() {
		if tr.Trigger == want {
			return true
		}
	}
	return false
}

// failingSTTCall places a call whose recogniser will refuse to open, greets it,
// then drives audio until the session dies.
func failingSTTCall(t *testing.T, r *t7Rig, cause error) sttFailure {
	t.Helper()

	call := r.place(t, "transfer me to rajesh")
	r.stt.failFor(call.Session(), cause)
	r.greet(t, call)

	// The caller speaks. The onset opens a turn, the turn opens recognition,
	// and recognition refuses.
	for i := uint64(0); i < t2TurnFrames; i++ {
		if err := call.Audio(t2Frame(i)); err != nil &&
			!errors.Is(err, voice.ErrBackpressure) &&
			!errors.Is(err, voice.ErrSessionClosed) {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	awaitCallDead(t, call)
	return sttFailure{call: call, rig: r}
}

// ---------------------------------------------------------------------------
// The policy
// ---------------------------------------------------------------------------

// TestSTTStartupFailureTerminatesCleanly — the recogniser will not start.
//
// This is the missing-binary case: whisper is not installed, or its path is
// wrong. The provider refuses at OpenSTT.
func TestSTTStartupFailureTerminatesCleanly(t *testing.T) {
	r := newT7Rig(t, t7Opts{})
	f := failingSTTCall(t, r, errors.New("t9: the recogniser will not start"))
	f.assertClosedByFault(t, voice.ErrProviderUnavailable)
}

// TestSTTProcessFailureTerminatesCleanly — the recogniser dies while opening.
//
// Distinct from the above in cause and identical in consequence, which is the
// point: the policy does not branch on why the provider failed.
func TestSTTProcessFailureTerminatesCleanly(t *testing.T) {
	r := newT7Rig(t, t7Opts{})
	f := failingSTTCall(t, r,
		fmt.Errorf("%w: the recogniser exited immediately", voice.ErrProcessFailed))
	f.assertClosedByFault(t, voice.ErrProviderUnavailable)
}

// TestSTTFailureDoesNotEmitResponseAbandoned.
//
// EventResponseAbandoned means "an ACCEPTED response will not be spoken"
// (Phase 15 T4). At an STT failure no plan was ever produced, so there is no
// accepted response and nothing to abandon. Sending it would put a phantom
// response in the record of every failed call.
func TestSTTFailureDoesNotEmitResponseAbandoned(t *testing.T) {
	r := newT7Rig(t, t7Opts{})
	f := failingSTTCall(t, r, errors.New("t9: no recogniser"))

	if got := abandonments(f.call.Conversation()); got != 0 {
		t.Errorf("%d abandonments for a turn that never produced a plan, want 0", got)
	}
}

// TestSTTFailureDoesNotEmitSpeechComplete.
func TestSTTFailureDoesNotEmitSpeechComplete(t *testing.T) {
	r := newT7Rig(t, t7Opts{})
	f := failingSTTCall(t, r, errors.New("t9: no recogniser"))

	conv := f.call.Conversation()
	if got := completions(conv); got != 0 {
		t.Errorf("%d speech completions, want 0: the agent said nothing", got)
	}
	// The greeting DID speak, before the failure — but its completion is a
	// greeting completion, and a failure must not add a speech one.
	if got := greetings(conv); got != 1 {
		t.Errorf("%d greeting completions, want 1 (the greeting succeeded)", got)
	}
}

// TestSTTFailureDoesNotCreateInterruption.
func TestSTTFailureDoesNotCreateInterruption(t *testing.T) {
	r := newT7Rig(t, t7Opts{})
	f := failingSTTCall(t, r, errors.New("t9: no recogniser"))

	if got := len(f.call.Conversation().Interruptions().History()); got != 0 {
		t.Errorf("%d interruptions recorded for a provider failure, want 0", got)
	}
}

// TestSTTFailureDoesNotLeakSession.
//
// The session's goroutines must all finish, and the driver must not be left
// holding anything: a failed call is exactly the case where a leak would
// accumulate, because nobody called End.
func TestSTTFailureDoesNotLeakSession(t *testing.T) {
	before := runtime.NumGoroutine()

	r := newT7Rig(t, t7Opts{})
	f := failingSTTCall(t, r, errors.New("t9: no recogniser"))

	// Done is closed, which means every pipeline goroutine returned.
	select {
	case <-f.call.Done():
	default:
		t.Fatal("the pipeline is still running after the failure")
	}

	// Repeated termination after a fault-driven close is safe.
	for i := 0; i < 3; i++ {
		if err := f.call.End(voice.ReasonCallEnded); err != nil {
			t.Errorf("End #%d after a fault: %v", i+1, err)
		}
	}
	if got := len(f.call.Conversation().Trace()); got == 0 {
		t.Error("the conversation has an empty trace")
	}

	r.stop(t)
	if after := settleGoroutines(2 * time.Second); after > before+8 {
		t.Errorf("goroutines grew from %d to %d across one failed call", before, after)
	}
}

// TestHealthySessionUnaffectedByNeighbourSTTFailure.
//
// One shared registry, one shared provider, two calls — and only one of them
// loses its recogniser. A real subprocess recogniser fails per session, and the
// blast radius must be one call.
func TestHealthySessionUnaffectedByNeighbourSTTFailure(t *testing.T) {
	r := newT7Rig(t, t7Opts{})

	sick := r.place(t, "transfer me to rajesh")
	healthy := r.place(t, "call me back on 9876543210")
	r.stt.failFor(sick.Session(), errors.New("t9: this session's recogniser is gone"))

	r.greet(t, sick)
	r.greet(t, healthy)

	for i := uint64(0); i < t2TurnFrames; i++ {
		_ = sick.Audio(t2Frame(i))
	}
	awaitCallDead(t, sick)

	// The neighbour takes a complete turn afterwards.
	r.speak(t, healthy, 0)

	hc := healthy.Conversation()
	if got := completions(hc); got != 1 {
		t.Errorf("the healthy call recorded %d completions, want 1", got)
	}
	if got := hc.State(); got == conversation.StateEnded {
		t.Error("the healthy call ended when its neighbour's recogniser failed")
	}
	last, ok := hc.Context().Get(conversation.ScopeConversation, "last_intent")
	if !ok || last.Value != "request_callback" {
		t.Errorf("healthy call last_intent = %v, want request_callback", last.Value)
	}
	if got := sick.Conversation().State(); got != conversation.StateEnded {
		t.Errorf("the failed call is %v, want ended", got)
	}
}

// ---------------------------------------------------------------------------
// Cases that are NOT failures, and must not be treated as any
// ---------------------------------------------------------------------------

// TestSTTSilenceEndsTheTurnNotTheCall.
//
// A recogniser that produces no final transcript — because the caller said
// nothing, or because the provider's own deadline expired — is SILENCE, not a
// fault. The turn ends and the call continues.
//
// Recorded because it is the case most easily confused with a failure: a
// whispercli process that hits its ResultTimeout closes its stream with nothing
// in it, and looks identical to a caller who did not speak.
func TestSTTSilenceEndsTheTurnNotTheCall(t *testing.T) {
	r := newT7Rig(t, t7Opts{})
	call := r.place(t)
	r.stt.silentFor(call.Session())
	r.greet(t, call)
	p := r.ports.of(t, call)

	for i := uint64(0); i < t2TurnFrames; i++ {
		if err := call.Audio(t2Frame(i)); err != nil &&
			!errors.Is(err, voice.ErrBackpressure) {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	p.obs.awaitTurn(t, 30*time.Second, "the silent turn")

	conv := call.Conversation()
	if got := conv.State(); got == conversation.StateEnded {
		t.Fatal("silence ended the call; a caller who says nothing has not failed")
	}
	if got := call.State(); got.Terminal() {
		t.Errorf("the voice session is %v after silence, want a live session", got)
	}
	if got := completions(conv) + abandonments(conv); got != 0 {
		t.Errorf("%d lifecycle events for a silent turn, want 0", got)
	}
	if err := call.Audio(t2Frame(99)); err != nil &&
		!errors.Is(err, voice.ErrBackpressure) {
		t.Errorf("the call refused audio after a silent turn: %v", err)
	}
}

// TestSTTCancellationTerminatesWithoutFalseCompletion.
//
// Cancellation is not a provider failure: the call is being taken away. It must
// close without a fault, without a completion, and without an abandonment.
func TestSTTCancellationTerminatesWithoutFalseCompletion(t *testing.T) {
	r := newT7Rig(t, t7Opts{})
	call := r.place(t, "transfer me to rajesh")
	r.greet(t, call)

	// Audio in flight, recognition open.
	for i := uint64(0); i <= t2OnsetAt; i++ {
		if err := call.Audio(t2Frame(i)); err != nil &&
			!errors.Is(err, voice.ErrBackpressure) {
			t.Fatalf("frame %d: %v", i, err)
		}
	}

	if err := call.End(voice.ReasonCallEnded); err != nil {
		t.Fatalf("End: %v", err)
	}
	awaitCallDead(t, call)

	conv := call.Conversation()
	if got := completions(conv); got != 0 {
		t.Errorf("%d speech completions after cancellation, want 0", got)
	}
	if got := abandonments(conv); got != 0 {
		t.Errorf("%d abandonments after cancellation, want 0", got)
	}
	if got := len(conv.Interruptions().History()); got != 0 {
		t.Errorf("%d interruptions after cancellation, want 0", got)
	}
	if hasTrigger(conv, conversation.TriggerFault) {
		t.Error("cancellation was recorded as a fault; the call was ended, not broken")
	}
	if got := conv.State(); got != conversation.StateEnded {
		t.Errorf("conversation state after End = %v, want ended", got)
	}
	if call.Pipeline().Err() != nil {
		t.Errorf("cancellation recorded a fault: %v", call.Pipeline().Err())
	}
}

// TestSTTFailureReasonIsTypedNotGeneric.
//
// "crashed" and "unavailable" send an operator to different places: one to the
// audio, one to the installation. A provider that was never there must not be
// reported as one that died.
func TestSTTFailureReasonIsTypedNotGeneric(t *testing.T) {
	r := newT7Rig(t, t7Opts{})
	f := failingSTTCall(t, r, errors.New("t9: the recogniser will not start"))

	var reason string
	for _, ch := range f.call.fsm.History() {
		if ch.To == voice.StateFailed {
			reason = ch.Reason
		}
	}
	if reason != voice.ReasonUnavailable {
		t.Errorf("the session failed with reason %q, want %q: a provider that "+
			"could not start is unavailable, not crashed",
			reason, voice.ReasonUnavailable)
	}
}
