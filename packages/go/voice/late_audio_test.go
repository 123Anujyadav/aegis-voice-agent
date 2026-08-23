package voice

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Phase 16 T10 — AUDIO THAT ARRIVES AFTER THE UTTERANCE ENDS.
//
// # Why this file exists
//
// Every recogniser stand-in in this package tolerated a Write after CloseSend,
// so for sixteen phases nothing noticed that the pipeline kept sending frames
// to a stream whose send side it had already closed. The first real subprocess
// recogniser wired in — openai-whisper, Phase 16 T10 — refused them with
// "audio already submitted", and the pipeline failed the SESSION on the very
// first caller turn.
//
// The frames themselves are not a bug: a carrier does not stop delivering audio
// because a voice-activity detector decided an utterance ended. Silence, breath
// and the beginning of the next turn all arrive after the endpoint, and every
// non-terminal state accepts them.
//
// These tests are deterministic and need no external tool, so the fix stays
// protected on a machine where whisper is not installed.

// TestPipeline_AudioAfterTheEndpointDoesNotReachTheRecogniser.
//
// The recogniser here refuses a late write with an error of its OWN spelling —
// not one of speech's sentinels — because the port does not say which error
// this is, and a pipeline that recognised only one provider's spelling would
// break on the next provider.
func TestPipeline_AudioAfterTheEndpointDoesNotReachTheRecogniser(t *testing.T) {
	t.Parallel()

	h := newHarness(t, harnessOpts{sttStrictSend: true})
	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = h.pipeline.Disconnect(); <-h.pipeline.Done() })

	// Well past the scripted endpoint: the tail is the audio that used to kill
	// the call.
	h.feed(t, 12)
	h.obs.waitTurn(t, 30*time.Second)

	if got := h.stt.lateWrites.Load(); got != 0 {
		t.Errorf("%d frames were written to the recogniser after its send side "+
			"was closed; a provider entitled to refuse them ends the call", got)
	}
	if err := h.pipeline.Err(); err != nil {
		t.Errorf("the pipeline recorded a fault for ordinary post-endpoint audio: %v", err)
	}
	if got := h.fsm.State(); got.Terminal() {
		t.Errorf("the session is %v after post-endpoint audio, want a live session", got)
	}
	if got := h.out.count(); got == 0 {
		t.Error("the turn produced no audio, so the path under test did not run")
	}
}

// TestPipeline_ARefusedEndOfUtteranceFailsTheTurnRatherThanHanging.
//
// For a batch recogniser CloseSend is where inference STARTS — it is the moment
// the process is spawned. The pipeline used to discard this error, so a
// recogniser that could not start left the turn waiting on a results channel
// that would never close or carry anything, and the call hung.
//
// Measured before the fix: a turn that never completed and a session stuck in
// transcribing for two minutes.
func TestPipeline_ARefusedEndOfUtteranceFailsTheTurnRatherThanHanging(t *testing.T) {
	t.Parallel()

	h := newHarness(t, harnessOpts{
		sttCloseSendErr: errors.New("recogniser: the process would not start"),
	})
	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = h.pipeline.Disconnect(); <-h.pipeline.Done() })

	h.feed(t, 8)

	// BOUNDED. The whole point is that this terminates rather than hanging.
	select {
	case <-h.pipeline.Done():
	case <-time.After(30 * time.Second):
		t.Fatalf("the session did not end after the recogniser refused the end of "+
			"the utterance; state=%v", h.fsm.State())
	}

	err := h.pipeline.Err()
	if err == nil {
		t.Fatal("the refusal was discarded; nothing recorded that recognition failed")
	}
	if !errors.Is(err, ErrProviderFailed) {
		t.Errorf("error = %v, want ErrProviderFailed", err)
	}
	if got := h.fsm.State(); got != StateFailed {
		t.Errorf("session state = %v, want failed", got)
	}
}
