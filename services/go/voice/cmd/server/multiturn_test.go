package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/media"
	"github.com/callscreen/callscreen-platform/packages/go/speech"
	"github.com/callscreen/callscreen-platform/packages/go/voice"
)

// Phase 15 T4 — MULTI-TURN VOICE LIFECYCLE.
//
// T3 established that conversation.EventSpeechComplete means "the agent
// finishing its output" and that handleSpeechComplete is the ONLY thing that
// performs the agent -> caller floor handoff. Production sent it from nowhere,
// so a call produced one spoken response and then silence.
//
// T4 implements ADR-0019 Option A: voice.Pipeline emits it at the delivery
// boundary. These tests prove the PRODUCTION path performs the handoff — the
// T2 test-side compensation has been deleted, so nothing here sends the event.
//
// COMPLETION IS COUNTED FROM PUBLIC STATE. conversation.Trace() records every
// transition with its Trigger, and TriggerSpeechComplete appears exactly once
// per completion. No private field is inspected.

// completions counts agent-speech completions recorded by the frozen engine.
func completions(conv *conversation.Conversation) int {
	if conv == nil {
		return 0
	}
	n := 0
	for _, tr := range conv.Trace() {
		if tr.Trigger == conversation.TriggerSpeechComplete {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Local per-stream STT double
// ---------------------------------------------------------------------------

// t4STT is a test-only speech.STTProvider that hands a DIFFERENT script to each
// stream it opens.
//
// WHY IT EXISTS: speech.FakeSTTProvider copies one script into every stream
// (harness.go:109) and flushes the remainder on CloseSend (:196), so it cannot
// express "stream 1 says A, stream 2 says B". speech is FROZEN, so rather than
// change it, this implements the same exported interfaces —
// speech.STTProvider and speech.STTStream — exactly as packages/go/voice's own
// e2e test already does with its local recordingSTT.
//
// It is a TEST DOUBLE, not a second production provider abstraction: it lives
// in a _test.go file and nothing production references it.
type t4STT struct {
	id   speech.ProviderID
	caps speech.Capabilities

	mu      sync.Mutex
	scripts [][]speech.TranscriptSegment // one per stream, in order
	opened  int
}

var _ speech.STTProvider = (*t4STT)(nil)

func newT4STT(utterances ...string) *t4STT {
	p := &t4STT{
		id: speech.ProviderID("t4-stt"),
		// The same languages the frozen fake declares, so routing behaves
		// identically and this double changes nothing about selection.
		caps: speech.Capabilities{
			Languages:      []speech.Language{speech.LangEnglishIN, speech.LangHindi, speech.LangHinglish},
			Streaming:      true,
			PartialResults: true,
			SampleRates:    []media.SampleRate{media.Rate8kHz, media.Rate16kHz},
		},
	}
	for i, u := range utterances {
		p.scripts = append(p.scripts, []speech.TranscriptSegment{
			{Sequence: uint64(2 * i), Text: u[:len(u)/2], IsFinal: false,
				Confidence: 0.55, Language: t2Lang},
			{Sequence: uint64(2*i + 1), Text: u, IsFinal: true,
				Confidence: 0.94, Language: t2Lang},
		})
	}
	return p
}

func (p *t4STT) ID() speech.ProviderID             { return p.id }
func (p *t4STT) Capabilities() speech.Capabilities { return p.caps }

func (p *t4STT) Opened() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.opened
}

func (p *t4STT) OpenSTT(_ context.Context, cfg speech.STTConfig) (speech.STTStream, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	idx := p.opened
	p.opened++
	var script []speech.TranscriptSegment
	if idx < len(p.scripts) {
		script = append([]speech.TranscriptSegment(nil), p.scripts[idx]...)
	}
	p.mu.Unlock()

	// A stream beyond the script is an empty stream rather than a repeat: a
	// double that silently replayed the last utterance would make an extra
	// unexpected turn look like a correct one.
	return &t4Stream{
		results: make(chan speech.TranscriptSegment, len(script)+1),
		script:  script, cfg: cfg, id: p.id,
	}, nil
}

// t4Stream is one recognition stream over one script.
type t4Stream struct {
	mu      sync.Mutex
	results chan speech.TranscriptSegment
	script  []speech.TranscriptSegment
	cfg     speech.STTConfig
	id      speech.ProviderID
	sent    int
	closed  bool
	ended   bool
}

var _ speech.STTStream = (*t4Stream)(nil)

func (s *t4Stream) Write(_ media.Frame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return speech.ErrSTTCancelled
	}
	// Partials are released as audio arrives; the final waits for CloseSend,
	// which is the same shape the frozen fake uses.
	if s.sent < len(s.script) && !s.script[s.sent].IsFinal {
		s.emitLocked()
	}
	return nil
}

func (s *t4Stream) emitLocked() {
	seg := s.script[s.sent]
	seg.Session = s.cfg.Session
	seg.Turn = s.cfg.Turn
	seg.Segment = speech.NewSegmentID()
	seg.Meta.Provider = s.id
	if seg.Language == speech.LangUnknown {
		seg.Language = s.cfg.Language
	}
	s.sent++
	select {
	case s.results <- seg:
	default:
	}
}

func (s *t4Stream) CloseSend() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ended {
		return nil
	}
	s.ended = true
	for s.sent < len(s.script) {
		s.emitLocked()
	}
	close(s.results)
	return nil
}

func (s *t4Stream) Results() <-chan speech.TranscriptSegment { return s.results }

func (s *t4Stream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if !s.ended {
		s.ended = true
		close(s.results)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Multi-turn with distinct utterances
// ---------------------------------------------------------------------------

// TestT4_MultiTurnResolvesADistinctIntentPerTurn is the evidence T2 could not
// produce: three different caller utterances, three different intents, on one
// session, with the floor handed back by PRODUCTION between each.
func TestT4_MultiTurnResolvesADistinctIntentPerTurn(t *testing.T) {
	turns := []struct{ text, wantIntent string }{
		// TWO MEASURED LEXICON FACTS, neither a lifecycle defect:
		//   1. the brief's "call me tomorrow" matches NO declared callback cue
		//      (intent/lexicon.go:171) and resolves to fallback;
		//   2. "call me back tomorrow" classifies but its REQUIRED
		//      callback_number slot is unfilled (slots.go:152, hasPhoneLikeRun),
		//      so the planner ASKS instead of responding — no speech, and
		//      therefore correctly no completion.
		// A phone-like run supplies the slot and the turn is actually spoken.
		{"call me back on 9876543210", "request_callback"},
		{"transfer me to rajesh", "request_transfer"},
		{"repeat that", "repeat"},
	}
	texts := make([]string, len(turns))
	for i, tn := range turns {
		texts[i] = tn.text
	}
	stt := newT4STT(texts...)
	r := newT2RigWith(t, nil, stt, "t4-multi")

	var seen []string
	lastFrames := 0
	for i, tn := range turns {
		r.driveTurn(t, uint64(i*t2TurnFrames))
		r.obs.awaitTurn(t, 30*time.Second, fmt.Sprintf("turn %d", i))

		last, ok := r.conv.Context().Get(conversation.ScopeConversation, "last_intent")
		if !ok {
			t.Fatalf("turn %d: no intent recorded", i)
		}
		got := fmt.Sprintf("%v", last.Value)
		seen = append(seen, got)
		if got != tn.wantIntent {
			t.Errorf("turn %d (%q): intent = %q, want %q",
				i, tn.text, got, tn.wantIntent)
		}

		// Each turn produced its own audio.
		frames, _ := r.sink.stats()
		if frames <= lastFrames {
			t.Errorf("turn %d produced no new output frames (still %d)", i, frames)
		}
		lastFrames = frames

		// Exactly one completion per spoken turn, recorded by the engine.
		if got, want := completions(r.conv), i+1; got != want {
			t.Errorf("after turn %d: %d speech completions, want %d", i, got, want)
		}
	}

	if stt.Opened() != len(turns) {
		t.Errorf("STT opened %d streams for %d turns", stt.Opened(), len(turns))
	}
	// Three DISTINCT intents, in the order sent — no cross-turn contamination.
	if len(seen) != 3 || seen[0] == seen[1] || seen[1] == seen[2] || seen[0] == seen[2] {
		t.Errorf("intents %v are not three distinct results", seen)
	}
	if r.conv.State().IsTerminal() {
		t.Error("conversation terminated during a healthy multi-turn sequence")
	}
	if got := r.conv.Turns().Count(); got < len(turns) {
		t.Errorf("conversation turn count = %d after %d turns", got, len(turns))
	}
	t.Logf("intents in order: %v | %d frames | %d completions | turn count %d",
		seen, lastFrames, completions(r.conv), r.conv.Turns().Count())
}

// TestT4_ProductionPerformsTheFloorHandoff isolates the handoff itself.
//
// Nothing in this test sends a conversation event. If the pipeline stopped
// emitting EventSpeechComplete, the floor would stay with the agent and the
// second turn would not resolve — which is exactly what M1 shows.
func TestT4_ProductionPerformsTheFloorHandoff(t *testing.T) {
	stt := newT4STT("transfer me to rajesh", "call me back on 9876543210")
	r := newT2RigWith(t, nil, stt, "t4-handoff")

	r.driveTurn(t, 0)
	r.obs.awaitTurn(t, 30*time.Second, "first turn")

	// After the agent has spoken, the CALLER holds the floor and the engine is
	// awaiting them.
	if got := r.conv.Turns().Holder(); got != conversation.PartyCaller {
		t.Fatalf("floor holder = %v after the agent spoke, want caller. The "+
			"pipeline is not emitting EventSpeechComplete", got)
	}
	if !r.conv.State().IsAwaiting() {
		t.Errorf("state = %v after the agent spoke; the engine should be "+
			"awaiting the caller", r.conv.State())
	}
	if got := completions(r.conv); got != 1 {
		t.Fatalf("%d speech completions after one spoken turn, want 1", got)
	}

	// And the next caller turn is accepted and answered.
	r.driveTurn(t, t2TurnFrames)
	r.obs.awaitTurn(t, 30*time.Second, "second turn")

	last, _ := r.conv.Context().Get(conversation.ScopeConversation, "last_intent")
	if fmt.Sprintf("%v", last.Value) != "request_callback" {
		t.Errorf("second turn intent = %v, want request_callback", last.Value)
	}
	if got := completions(r.conv); got != 2 {
		t.Errorf("%d speech completions after two spoken turns, want 2", got)
	}
}

// ---------------------------------------------------------------------------
// Exactly-once, and the paths that must produce ZERO
// ---------------------------------------------------------------------------

// TestT4_CompletionIsExactlyOncePerSpokenTurn covers the success case and every
// path where no agent speech was completed.
func TestT4_CompletionIsExactlyOncePerSpokenTurn(t *testing.T) {
	t.Run("one_spoken_turn_one_completion", func(t *testing.T) {
		r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t4-once")
		r.driveTurn(t, 0)
		r.obs.awaitTurn(t, 30*time.Second, "spoken turn")
		if got := completions(r.conv); got != 1 {
			t.Errorf("%d completions for one spoken turn, want exactly 1", got)
		}
	})

	// A second endpoint on an already-completed turn must not manufacture a
	// second completion: the caller now holds the floor and there is no agent
	// output in progress to finish.
	t.Run("duplicate_endpoint_adds_no_completion", func(t *testing.T) {
		r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t4-dup")
		r.driveTurn(t, 0)
		r.obs.awaitTurn(t, 30*time.Second, "first turn")
		before := completions(r.conv)

		// Drive the endpoint frame again without a fresh onset. No new turn
		// starts, so no new completion may appear.
		if err := r.pipeline.WriteFrame(t2Frame(t2EndAt)); err != nil &&
			!errors.Is(err, voice.ErrBackpressure) &&
			!errors.Is(err, voice.ErrSessionClosed) {
			t.Fatalf("WriteFrame: %v", err)
		}
		if got := completions(r.conv); got != before {
			t.Errorf("completions went %d -> %d on a duplicate endpoint",
				before, got)
		}
	})

	t.Run("tts_failure_produces_zero", func(t *testing.T) {
		r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t4-zero-tts")
		r.tts.FailNext(errors.New("scripted synthesiser failure"))
		r.driveTurn(t, 0)
		r.obs.awaitTurn(t, 30*time.Second, "tts failure")
		if got := completions(r.conv); got != 0 {
			t.Errorf("%d completions although synthesis failed and nothing was "+
				"spoken, want 0", got)
		}
	})

	t.Run("generation_failure_produces_zero", func(t *testing.T) {
		r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t4-zero-gen")
		r.model.FailNext(1, errors.New("scripted generation failure"))
		r.driveTurn(t, 0)
		r.obs.awaitTurn(t, 30*time.Second, "generation failure")
		if got := completions(r.conv); got != 0 {
			t.Errorf("%d completions although generation failed and nothing was "+
				"spoken, want 0", got)
		}
	})

	t.Run("stt_failure_produces_zero", func(t *testing.T) {
		r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t4-zero-stt")
		r.stt.FailNext(errors.New("scripted recogniser failure"))
		r.driveTurn(t, 0)
		// No turn completes when recognition never opens; bounded wait on the
		// recorded error instead.
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) && r.pipeline.Err() == nil {
			select {
			case <-r.pipeline.Done():
			case <-time.After(50 * time.Millisecond):
			}
		}
		if got := completions(r.conv); got != 0 {
			t.Errorf("%d completions although recognition failed, want 0", got)
		}
	})
}

// TestT4_CancellationProducesNoCompletion cancels mid-turn and requires zero
// completions plus a bounded, panic-free stop.
func TestT4_CancellationProducesNoCompletion(t *testing.T) {
	r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t4-cancel")

	// Drive PAST the onset but STOP BEFORE the endpoint frame, so recognition
	// is open and no turn can possibly have completed. This makes the expected
	// completion count exactly zero rather than a race against the turn.
	for i := uint64(0); i < t2EndAt; i++ {
		if err := r.pipeline.WriteFrame(t2Frame(i)); err != nil &&
			!errors.Is(err, voice.ErrBackpressure) &&
			!errors.Is(err, voice.ErrSessionClosed) {
			t.Fatalf("WriteFrame(%d): %v", i, err)
		}
	}
	if got := completions(r.conv); got != 0 {
		t.Fatalf("%d completions before any turn ended", got)
	}

	if err := r.pipeline.Cancel(voice.ReasonCallEnded); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case <-r.pipeline.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the pipeline did not finish after Cancel")
	}

	if err := r.pipeline.WriteFrame(t2Frame(99)); err == nil {
		t.Error("a cancelled pipeline accepted another frame")
	}
	if got := completions(r.conv); got != 0 {
		t.Errorf("%d speech completions after cancellation, want 0 — the agent "+
			"never finished speaking", got)
	}
	if r.conv.Turns().Holder() == conversation.PartyCaller {
		t.Error("cancellation handed the floor to the caller; no completion " +
			"should have occurred")
	}
}

// TestT4_InterruptionProducesNoCompletion requires a delivered barge-in to
// produce zero agent-speech completions.
//
// It now uses interruptedRig (Phase 16 T3) rather than a stalled synthesiser
// plus a condition poll. The old approach depended on OpenTTS still being
// parked when the frames arrived, which under load timed out first
// (failed/start_failed) and proved nothing.
func TestT4_InterruptionProducesNoCompletion(t *testing.T) {
	r := interruptedRig(t, "t4-interrupt")

	if got := r.pipeline.BargeIns(); got == 0 {
		t.Fatal("no interruption was delivered; this test would prove nothing")
	}
	if got := completions(r.conv); got != 0 {
		t.Errorf("%d speech completions although a barge-in superseded the turn, "+
			"want 0", got)
	}
	if got := abandonments(r.conv); got != 0 {
		t.Errorf("%d abandonments for an interruption, want 0", got)
	}
	if r.conv.State().IsTerminal() {
		t.Error("the conversation is terminal after an interruption")
	}
}

// ---------------------------------------------------------------------------
// Determinism
// ---------------------------------------------------------------------------

// TestT4_MultiTurnSignatureIsStable replays the three-turn scenario on fresh
// rigs and requires an identical semantic signature.
func TestT4_MultiTurnSignatureIsStable(t *testing.T) {
	sig := func(pass int) string {
		stt := newT4STT("call me back on 9876543210", "transfer me to rajesh", "repeat that")
		r := newT2RigWith(t, nil, stt, fmt.Sprintf("t4-det-%d", pass))

		var intents []string
		for i := 0; i < 3; i++ {
			r.driveTurn(t, uint64(i*t2TurnFrames))
			r.obs.awaitTurn(t, 30*time.Second, "determinism turn")
			last, _ := r.conv.Context().Get(conversation.ScopeConversation, "last_intent")
			intents = append(intents, fmt.Sprintf("%v", last.Value))
		}
		frames, bytes := r.sink.stats()
		_, _, outcomes := r.obs.snapshot()
		return fmt.Sprintf(
			"intents=%v holder=%v turns=%d completions=%d frames=%d bytes=%d "+
				"outcomes=%v terminal=%v",
			intents, r.conv.Turns().Holder(), r.conv.Turns().Count(),
			completions(r.conv), frames, bytes, outcomes,
			r.conv.State().IsTerminal())
	}

	want := sig(0)
	for i := 1; i <= 4; i++ {
		if got := sig(i); got != want {
			t.Fatalf("pass %d drifted\nwant: %s\n got: %s", i, want, got)
		}
	}
	t.Logf("stable multi-turn signature: %s", want)
}

// ---------------------------------------------------------------------------
// Reaching the completion tail deterministically (T4 follow-up, M3/M4)
// ---------------------------------------------------------------------------
//
// M3 and M4 were previously NOT CAUGHT because the mutated branches sit in the
// completion tail and the rig could never park a turn there. Both tests below
// park the turn INSIDE audio delivery using a channel handshake in the sink,
// change the turn's status while it is parked, then release it so it walks into
// the tail. No sleep, no polling, no timing luck.

// TestT4_SupersededTurnReachesTheTailWithoutCompleting parks the turn
// mid-delivery, lands a real barge-in so the generation is superseded, then
// releases it into the completion tail.
//
// It now uses interruptedRig (Phase 16 T3), which is deterministic. The earlier
// inline version walked the frame sequence while retrying the interruption,
// which re-triggered the cyclic onset/endpoint and started a SECOND turn — a
// rig flaw that made this test flaky. The assertions are unchanged and one is
// added.
func TestT4_SupersededTurnReachesTheTailWithoutCompleting(t *testing.T) {
	r := interruptedRig(t, "t4-tail-interrupt")

	if got := completions(r.conv); got != 0 {
		t.Errorf("%d speech completions for a turn superseded by a barge-in, "+
			"want 0 — EventInterrupt owns that floor transition, not "+
			"EventSpeechComplete", got)
	}
	// UPDATED BY PHASE 16 T3: the floor now returns to the caller, via
	// EventInterrupt rather than via a completion. Before T3 the pipeline never
	// told the conversation and the floor stayed with the agent.
	if got := r.conv.Turns().Holder(); got != conversation.PartyCaller {
		t.Errorf("floor = %v after a superseded turn, want caller", got)
	}
	if got := len(r.conv.Interruptions().History()); got != 1 {
		t.Errorf("%d interruption records for a superseded turn, want 1", got)
	}
}

// TestT4_CancelledTurnReachesTheTailWithoutCompleting parks the turn
// mid-delivery, cancels the session, then releases it into the completion tail.
//
// The tail's p.ctx.Err() guard must take the cancelled exit and record NO
// completion. M4 makes that branch emit one.
func TestT4_CancelledTurnReachesTheTailWithoutCompleting(t *testing.T) {
	r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t4-tail-cancel")

	atDeliver := make(chan struct{}, 1)
	hold := make(chan struct{})
	r.sink.mu.Lock()
	r.sink.atDeliver, r.sink.hold = atDeliver, hold
	r.sink.mu.Unlock()

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
		t.Fatal("the turn never reached audio delivery; the tail cannot be exercised")
	}

	// Cancel while the turn is parked, then release it into the tail.
	if err := r.pipeline.Cancel(voice.ReasonCallEnded); err != nil {
		close(hold)
		t.Fatalf("Cancel: %v", err)
	}
	close(hold)

	select {
	case <-r.pipeline.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the pipeline did not finish after Cancel")
	}

	if got := completions(r.conv); got != 0 {
		t.Errorf("%d speech completions for a cancelled turn, want 0 — the "+
			"agent never finished speaking", got)
	}
	if r.conv.Turns().Holder() == conversation.PartyCaller {
		t.Error("cancellation handed the conversation floor to the caller")
	}
}

// ---------------------------------------------------------------------------
// Response abandonment (ADR-0020)
// ---------------------------------------------------------------------------
//
// When an accepted response plan produces no agent speech, the pipeline sends
// conversation.EventResponseAbandoned so the caller can speak again. Before
// this, the agent kept the floor and every later utterance was classified as a
// backchannel and ignored — a silent stuck call.

// abandonments counts response abandonments recorded by the frozen engine.
func abandonments(conv *conversation.Conversation) int {
	if conv == nil {
		return 0
	}
	n := 0
	for _, tr := range conv.Trace() {
		if tr.Trigger == conversation.TriggerResponseAbandoned {
			n++
		}
	}
	return n
}

// TestT4_FailedResponseReleasesTheFloor covers the three approved failures.
func TestT4_FailedResponseReleasesTheFloor(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(*t2Rig)
	}{
		{"generation_failure", func(r *t2Rig) {
			r.model.FailNext(1, errors.New("scripted generation failure"))
		}},
		{"tts_failure", func(r *t2Rig) {
			r.tts.FailNext(errors.New("scripted synthesiser failure"))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t4-ra-"+c.name)
			c.break_(r)
			r.driveTurn(t, 0)
			r.obs.awaitTurn(t, 30*time.Second, c.name)

			// (1-3) the floor came back
			if got := r.conv.Turns().Holder(); got != conversation.PartyCaller {
				t.Errorf("floor holder = %v after a failed response, want caller", got)
			}
			if got := r.conv.State(); got != conversation.StateListening {
				t.Errorf("state = %v, want listening", got)
			}
			// (7) non-terminal
			if r.conv.State().IsTerminal() {
				t.Error("a failed response terminated the conversation")
			}
			// (8) exactly one abandonment
			if got := abandonments(r.conv); got != 1 {
				t.Errorf("%d abandonments, want exactly 1", got)
			}
			// (6/10) never reported as speech
			if got := completions(r.conv); got != 0 {
				t.Errorf("%d speech completions for a response that was never "+
					"spoken, want 0", got)
			}
			// (5) not an interruption
			if got := len(r.conv.Interruptions().History()); got != 0 {
				t.Errorf("%d interruption records for an abandoned response, "+
					"want 0 — nothing interrupted the agent", got)
			}

			// (4) the next caller utterance is heard, not treated as a backchannel
			plan, err := r.conv.Handle(conversation.Event{
				Kind: conversation.EventUtterance,
				Utterance: conversation.Utterance{
					Text: "say that again", ASRConfidence: 0.94},
				Party: conversation.PartyCaller,
			})
			if err != nil {
				t.Fatalf("next utterance: %v", err)
			}
			if plan.Reason == "backchannel" {
				t.Error("the next caller utterance was still ignored as a " +
					"backchannel; the floor was not really released")
			}
			if plan.Intent != "repeat" {
				t.Errorf("next utterance intent = %q, want repeat", plan.Intent)
			}
		})
	}
}

// TestT4_GovernanceDenialReleasesTheFloor covers the denial path, which must NOT
// be reported as a fault: denial is a domain outcome.
func TestT4_GovernanceDenialReleasesTheFloor(t *testing.T) {
	// An empty policy registry denies everything with "no_policy_matched",
	// which is the real deny-by-default posture rather than a stubbed Governor.
	r := newT2RigDenying(t, t2Segments("transfer me to rajesh"), "t4-ra-denied")
	r.driveTurn(t, 0)
	r.obs.awaitTurn(t, 30*time.Second, "denied turn")

	_, _, outcomes := r.obs.snapshot()
	if len(outcomes) == 0 || !contains(outcomes[0], string(voice.OutcomeDenied)) {
		t.Fatalf("outcomes %v record no denial; this test would prove nothing",
			outcomes)
	}
	if got := abandonments(r.conv); got != 1 {
		t.Errorf("%d abandonments after a denial, want exactly 1", got)
	}
	if got := completions(r.conv); got != 0 {
		t.Errorf("%d speech completions for a denied response, want 0", got)
	}
	if got := r.conv.Turns().Holder(); got != conversation.PartyCaller {
		t.Errorf("floor holder = %v after a denial, want caller", got)
	}
	if r.conv.State().IsTerminal() {
		t.Error("a governance denial terminated the conversation")
	}
	if got := len(r.conv.Interruptions().History()); got != 0 {
		t.Errorf("%d interruption records after a denial, want 0", got)
	}
}

// TestT4_SuccessfulResponseDoesNotAbandon is the regression guard: the success
// path must still complete and must never abandon.
func TestT4_SuccessfulResponseDoesNotAbandon(t *testing.T) {
	r := newT2Rig(t, t2Segments("transfer me to rajesh"), "t4-ra-success")
	r.driveTurn(t, 0)
	r.obs.awaitTurn(t, 30*time.Second, "successful turn")

	if got := completions(r.conv); got != 1 {
		t.Errorf("%d speech completions for a spoken turn, want exactly 1", got)
	}
	if got := abandonments(r.conv); got != 0 {
		t.Errorf("%d abandonments for a successfully spoken turn, want 0", got)
	}
}

// TestT4_AbandonmentIsSessionScoped proves one session's failure does not move
// another session's floor.
func TestT4_AbandonmentIsSessionScoped(t *testing.T) {
	bad := newT2Rig(t, t2Segments("transfer me to rajesh"), "t4-ra-bad")
	good := newT2Rig(t, t2Segments("transfer me to rajesh"), "t4-ra-good")

	bad.model.FailNext(1, errors.New("scripted generation failure"))
	bad.driveTurn(t, 0)
	bad.obs.awaitTurn(t, 30*time.Second, "failing session")

	good.driveTurn(t, 0)
	good.obs.awaitTurn(t, 30*time.Second, "healthy session")

	if got := abandonments(bad.conv); got != 1 {
		t.Errorf("failing session: %d abandonments, want 1", got)
	}
	if got := abandonments(good.conv); got != 0 {
		t.Errorf("healthy session: %d abandonments, want 0 — one session's "+
			"failure reached another", got)
	}
	if got := completions(good.conv); got != 1 {
		t.Errorf("healthy session: %d completions, want 1", got)
	}
}
