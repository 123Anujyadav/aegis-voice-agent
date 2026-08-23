package main

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/voice"
)

// Phase 16 T3 — PRODUCTION INTERRUPTION FORWARDING.
//
// Phase 16 T2 established that production barge-in DETECTION exists
// (voice.interruptController, called by audiointel from inside Analyze) but was
// never forwarded to the conversation: bargein.go referenced neither Planner nor
// conversation, so the frozen InterruptionEngine recorded nothing and the
// dialogue floor was never yielded.
//
// T3 forwards it. These tests drive a REAL barge-in through the real detector
// while the agent genuinely holds the floor, using the channel handshake from
// Phase 15 — no sleeps, no polling, no timing luck.

// interruptedRig parks a turn inside audio delivery, lands a real barge-in, and
// releases the turn. It returns once the barge-in has been delivered.
//
// Determinism comes from two existing seams, not from timing:
//   - the sink's atDeliver/hold handshake parks the turn in the delivery loop,
//     which is provably a floor-holding state;
//   - scriptedIntel retries the interruption on each frame until one is
//     DELIVERED, signalling bargedC when it is.
func interruptedRig(t *testing.T, id string) *t2Rig {
	t.Helper()
	r := newT2Rig(t, t2Segments("transfer me to rajesh"), id)

	atDeliver := make(chan struct{}, 1)
	hold := make(chan struct{})
	barged := make(chan struct{})
	r.sink.mu.Lock()
	r.sink.atDeliver, r.sink.hold = atDeliver, hold
	r.sink.mu.Unlock()
	r.intel.mu.Lock()
	r.intel.bargeAt, r.intel.bargedC = t2EndAt+1, barged
	r.intel.mu.Unlock()

	for i := uint64(0); i <= t2EndAt; i++ {
		if err := r.pipeline.WriteFrame(t2Frame(i)); err != nil &&
			!errors.Is(err, voice.ErrBackpressure) &&
			!errors.Is(err, voice.ErrSessionClosed) {
			t.Fatalf("WriteFrame(%d): %v", i, err)
		}
	}

	select {
	case <-atDeliver:
	case <-time.After(30 * time.Second):
		t.Fatal("the turn never reached audio delivery; the agent never held " +
			"the floor and no barge-in window existed")
	}

	// Parked mid-delivery: the agent genuinely holds the floor.
	// Retry on the SAME sequence number, which maps to a position in the turn
	// cycle that is neither the onset nor the endpoint. Walking the sequence
	// forward would re-trigger both and start a SECOND turn, whose EventUtterance
	// would move the conversation back to speaking and mask the barge-in — a
	// flake in the rig, not in the code under test.
	go func() {
		for n := 0; n < 4*t2TurnFrames; n++ {
			select {
			case <-barged:
				return
			default:
			}
			_ = r.pipeline.WriteFrame(t2Frame(t2EndAt + 1))
		}
	}()
	select {
	case <-barged:
	case <-time.After(30 * time.Second):
		close(hold)
		t.Fatal("no barge-in was delivered while the turn was parked in delivery")
	}
	if r.pipeline.BargeIns() == 0 {
		close(hold)
		t.Fatal("the interruption was not recorded as delivered by the pipeline")
	}

	close(hold) // release the turn into the completion tail
	r.obs.awaitTurn(t, 30*time.Second, "interrupted turn")
	return r
}

// TestT3_ProductionBargeInReachesTheConversation is the core evidence.
//
// Nothing in this test sends a conversation event. If the forwarding were
// removed, the interruption history would stay empty and the floor would stay
// with the agent — which is exactly what M1 demonstrates.
func TestT3_ProductionBargeInReachesTheConversation(t *testing.T) {
	r := interruptedRig(t, "t3-bargein")

	// --- the interruption was recorded by the FROZEN engine ----------------
	history := r.conv.Interruptions().History()
	if len(history) != 1 {
		t.Fatalf("%d interruption records, want exactly 1 — the production "+
			"barge-in did not reach the conversation", len(history))
	}
	if got := history[0].Kind; got != conversation.InterruptionUser {
		t.Errorf("interruption kind = %v, want user", got)
	}
	if got := history[0].By; got != conversation.PartyCaller {
		t.Errorf("interruption by = %v, want caller", got)
	}

	// --- the floor came back -----------------------------------------------
	if got := r.conv.Turns().Holder(); got != conversation.PartyCaller {
		t.Errorf("floor = %v after a barge-in, want caller", got)
	}
	if got := r.conv.State(); got != conversation.StateListening {
		t.Errorf("state = %v after a barge-in, want listening", got)
	}
	if r.conv.State().IsTerminal() {
		t.Error("a barge-in terminated the conversation")
	}

	// --- an interruption is NOT a completion and NOT a failure --------------
	if got := completions(r.conv); got != 0 {
		t.Errorf("%d speech completions for an interrupted response, want 0 — "+
			"the agent did not finish speaking", got)
	}
	if got := abandonments(r.conv); got != 0 {
		t.Errorf("%d abandonments for an interruption, want 0 — an interruption "+
			"is not a failed response", got)
	}
}

// TestT3_CallerCanContinueAfterInterrupting proves the interruption is not
// merely recorded: the caller can actually keep talking.
func TestT3_CallerCanContinueAfterInterrupting(t *testing.T) {
	r := interruptedRig(t, "t3-continue")

	if got := r.conv.Turns().Holder(); got != conversation.PartyCaller {
		t.Fatalf("floor = %v before the follow-up turn, want caller", got)
	}

	plan, err := r.conv.Handle(conversation.Event{
		Kind: conversation.EventUtterance,
		Utterance: conversation.Utterance{
			Text: "say that again", ASRConfidence: 0.94},
		Party: conversation.PartyCaller,
	})
	if err != nil {
		t.Fatalf("post-interruption utterance: %v", err)
	}
	if plan.Reason == "backchannel" {
		t.Fatal("the caller's next utterance was ignored as a backchannel; the " +
			"floor was recorded but not actually returned")
	}
	if plan.Intent != "repeat" || plan.Reason != "intent_accepted" {
		t.Errorf("post-interruption plan = %q/%q, want repeat/intent_accepted",
			plan.Intent, plan.Reason)
	}
	// Still exactly one interruption; the follow-up added none.
	if got := len(r.conv.Interruptions().History()); got != 1 {
		t.Errorf("%d interruption records after the follow-up turn, want 1", got)
	}
}

// TestT3_OneDetectionForwardsExactlyOnce covers the exactly-once requirement.
func TestT3_OneDetectionForwardsExactlyOnce(t *testing.T) {
	r := interruptedRig(t, "t3-once")

	if got := r.pipeline.BargeIns(); got != 1 {
		t.Errorf("the pipeline delivered %d interruptions, want 1", got)
	}
	if got := len(r.conv.Interruptions().History()); got != 1 {
		t.Errorf("%d interruption records for one delivered barge-in, want 1", got)
	}
	if got := completions(r.conv); got != 0 {
		t.Errorf("%d completions, want 0", got)
	}
	if got := abandonments(r.conv); got != 0 {
		t.Errorf("%d abandonments, want 0", got)
	}

	// The detector's own precondition prevents a duplicate: once the barge-in
	// has moved the SessionFSM to listening, the agent no longer holds the
	// floor and a further detection is refused rather than forwarded. This is
	// the EXISTING guard, not a deduplication policy invented here.
	before := len(r.conv.Interruptions().History())
	// Re-arm the scripted detector so it genuinely ATTEMPTS a second
	// interruption. Without this the detector stops after its first delivery
	// and the precondition is never exercised.
	r.intel.mu.Lock()
	r.intel.barged = false
	r.intel.mu.Unlock()
	for i := uint64(0); i < uint64(4*t2TurnFrames); i++ {
		_ = r.pipeline.WriteFrame(t2Frame(t2EndAt + 1))
	}
	if got := len(r.conv.Interruptions().History()); got != before {
		t.Errorf("interruption records went %d -> %d on further detection "+
			"attempts while the agent did not hold the floor", before, got)
	}
}

// TestT3_InterruptionIsSessionScoped proves one session's barge-in does not
// touch another's conversation.
func TestT3_InterruptionIsSessionScoped(t *testing.T) {
	// Session B runs a clean turn first, so it has a settled state to compare.
	b := newT2Rig(t, t2Segments("transfer me to rajesh"), "t3-iso-b")
	b.driveTurn(t, 0)
	b.obs.awaitTurn(t, 30*time.Second, "session B turn")

	bCompletions := completions(b.conv)
	bFloor := b.conv.Turns().Holder()
	if bCompletions != 1 {
		t.Fatalf("session B: %d completions before the interruption, want 1",
			bCompletions)
	}

	// Session A is interrupted.
	a := interruptedRig(t, "t3-iso-a")

	if got := len(a.conv.Interruptions().History()); got != 1 {
		t.Errorf("session A: %d interruption records, want 1", got)
	}
	if got := len(b.conv.Interruptions().History()); got != 0 {
		t.Errorf("session B: %d interruption records, want 0 — session A's "+
			"barge-in reached session B", got)
	}
	if got := completions(b.conv); got != bCompletions {
		t.Errorf("session B: completions changed %d -> %d", bCompletions, got)
	}
	if got := b.conv.Turns().Holder(); got != bFloor {
		t.Errorf("session B: floor changed %v -> %v", bFloor, got)
	}
	if b.conv.State().IsTerminal() {
		t.Error("session B terminated because session A was interrupted")
	}

	// And B is still usable.
	plan, err := b.conv.Handle(conversation.Event{
		Kind: conversation.EventUtterance,
		Utterance: conversation.Utterance{
			Text: "say that again", ASRConfidence: 0.94},
		Party: conversation.PartyCaller,
	})
	if err != nil {
		t.Fatalf("session B follow-up: %v", err)
	}
	if plan.Reason != "intent_accepted" {
		t.Errorf("session B follow-up plan reason = %q, want intent_accepted",
			plan.Reason)
	}
}

// TestT3_CancellationIsNotAnInterruption keeps the two mechanisms separate.
func TestT3_CancellationIsNotAnInterruption(t *testing.T) {
	r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t3-cancel")

	// Stop before the endpoint so no turn can complete, then cancel.
	for i := uint64(0); i < t2EndAt; i++ {
		if err := r.pipeline.WriteFrame(t2Frame(i)); err != nil &&
			!errors.Is(err, voice.ErrBackpressure) &&
			!errors.Is(err, voice.ErrSessionClosed) {
			t.Fatalf("WriteFrame: %v", err)
		}
	}
	if err := r.pipeline.Cancel(voice.ReasonCallEnded); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case <-r.pipeline.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("cancellation did not finish")
	}

	if got := len(r.conv.Interruptions().History()); got != 0 {
		t.Errorf("%d interruption records after cancellation, want 0 — "+
			"cancelling is not interrupting", got)
	}
	if got := completions(r.conv); got != 0 {
		t.Errorf("%d completions after cancellation, want 0", got)
	}
	if got := abandonments(r.conv); got != 0 {
		t.Errorf("%d abandonments after cancellation, want 0", got)
	}
	if got := r.pipeline.BargeIns(); got != 0 {
		t.Errorf("%d barge-ins recorded for a cancellation, want 0", got)
	}
}

// TestT3_InterruptionAndAbandonmentAreDisjoint covers the interaction with the
// already-approved ResponseAbandoned path.
func TestT3_InterruptionAndAbandonmentAreDisjoint(t *testing.T) {
	t.Run("interrupted_speech", func(t *testing.T) {
		r := interruptedRig(t, "t3-disjoint-int")
		comp, aband := completions(r.conv), abandonments(r.conv)
		intr := len(r.conv.Interruptions().History())
		if comp != 0 || aband != 0 || intr != 1 {
			t.Errorf("interrupted speech: completions=%d abandonments=%d "+
				"interruptions=%d, want 0/0/1", comp, aband, intr)
		}
	})

	for _, f := range []struct {
		name   string
		break_ func(*t2Rig)
	}{
		{"generation_failure", func(r *t2Rig) {
			r.model.FailNext(1, errors.New("scripted generation failure"))
		}},
		{"tts_failure", func(r *t2Rig) {
			r.tts.FailNext(errors.New("scripted synthesiser failure"))
		}},
	} {
		t.Run(f.name, func(t *testing.T) {
			r := newT2Rig(t, t2Segments("transfer me to rajesh"),
				"t3-disjoint-"+f.name)
			f.break_(r)
			r.driveTurn(t, 0)
			r.obs.awaitTurn(t, 30*time.Second, f.name)

			comp, aband := completions(r.conv), abandonments(r.conv)
			intr := len(r.conv.Interruptions().History())
			if comp != 0 || aband != 1 || intr != 0 {
				t.Errorf("%s: completions=%d abandonments=%d interruptions=%d, "+
					"want 0/1/0", f.name, comp, aband, intr)
			}
			// THE INVARIANT: never an interruption and an abandonment together.
			if intr > 0 && aband > 0 {
				t.Error("one turn produced BOTH an interruption and an abandonment")
			}
		})
	}
}

// TestT3_InterruptionSignatureIsStable replays the barge-in on fresh rigs.
func TestT3_InterruptionSignatureIsStable(t *testing.T) {
	sig := func(pass int) string {
		r := interruptedRig(t, fmt.Sprintf("t3-det-%d", pass))
		h := r.conv.Interruptions().History()
		kinds := ""
		for _, i := range h {
			kinds += fmt.Sprintf("%v/%v/%s;", i.Kind, i.By, i.Reason)
		}
		return fmt.Sprintf("interrupts=%d[%s] comp=%d aband=%d holder=%v state=%v "+
			"terminal=%v bargeIns=%d",
			len(h), kinds, completions(r.conv), abandonments(r.conv),
			r.conv.Turns().Holder(), r.conv.State(), r.conv.State().IsTerminal(),
			r.pipeline.BargeIns())
	}
	want := sig(0)
	for i := 1; i <= 3; i++ {
		if got := sig(i); got != want {
			t.Fatalf("pass %d drifted\nwant: %s\n got: %s", i, want, got)
		}
	}
	t.Logf("stable interruption signature: %s", want)
}
