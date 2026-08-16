package audiointel

import (
	"context"
	"time"
)

// SpeechController is the outbound port to Phase 11C.
//
// # This is how §29's dependency rule and §8's integration requirement are both
// satisfied
//
// §8 requires barge-in to cancel synthesis "through the existing Phase 11C
// contract" and forbids touching a TTS provider directly. §26 and §29 require
// this module's dependency closure to be the standard library plus first-party
// modules, with no import of speech, conversation, governance or any provider
// SDK.
//
// Those two are reconcilable only through a port. The Phase 11C contract is
// speech.SpeechSession.Interrupt and speech.SpeechSession.EndOfSpeech; naming
// those types here would mean importing packages/go/speech, and packages/go/speech
// is FROZEN so it cannot be made to depend on this module instead.
//
// So this module declares the two operations it needs and calls them.
// packages/go/audiobridge implements this interface over a real
// *speech.SpeechSession and is the only place in the repository where the two
// meet. `go list -deps` on this module is therefore provably free of speech,
// and TestDependencies_ClosureIsFirstPartyOnly checks it.
//
// # Implementations must not block
//
// THIS PORT IS CALLED FROM THE FRAME PATH, synchronously, with no queue in
// front of it — because ADR-0004 §247 says a queue between the detector and the
// output is added interruption latency, and ADR-0004 §12 budgets the whole hop
// at one frame interval. An implementation that blocks holds a frame, and a
// held frame on this path is the agent still talking over somebody.
type SpeechController interface {
	// Interrupt cancels agent speech in progress.
	//
	// Corresponds to speech.SpeechSession.Interrupt. The reason is a bounded
	// code from classifications.go, never free text.
	//
	// An error is not a failure of this engine: Phase 11C legitimately refuses
	// an interruption when the turn is not responding or speaking, because a
	// caller talking while we are listening is not interrupting. The refusal is
	// counted as [BargeInRefused] and the detection is not retried.
	Interrupt(ctx context.Context, reason string) error

	// EndOfSpeech signals that the caller's turn has ended.
	//
	// Corresponds to speech.SpeechSession.EndOfSpeech, whose own documentation
	// calls itself "the VAD boundary, not a VAD" and points at exactly this
	// layer. ADR-0005 C6 records that endpointing is ours and vendor
	// endpointing is disabled or ignored.
	EndOfSpeech(ctx context.Context) error
}

// NopSpeechController accepts everything and does nothing.
//
// Named so that choosing it is visible in a configuration review. A nil
// controller is NOT the same thing: a detected barge-in with nowhere to send it
// is counted as [BargeInNoController], because a deployment that detects
// interruptions it cannot act on looks healthy on a dashboard while talking
// over every caller.
type NopSpeechController struct{}

// Interrupt discards.
func (NopSpeechController) Interrupt(context.Context, string) error { return nil }

// EndOfSpeech discards.
func (NopSpeechController) EndOfSpeech(context.Context) error { return nil }

// ---------------------------------------------------------------------------

// OutboundEnvelope supplies the agent's own output level over media time.
//
// # Optional, and it can only ever LOWER confidence
//
// Overlap detection without it is caller-speech-during-agent-speech, which
// includes the agent hearing itself through a handset. With it, this engine can
// notice that what it is hearing rises and falls in step with what it is
// SAYING, which is weak evidence of echo.
//
// Weak, and used only in that direction. Correlation with our own output
// suggests echo; its absence proves nothing, because a caller can perfectly
// well speak in the same rhythm as the agent. This engine performs no echo
// cancellation and no source separation, and
// docs/audio-intelligence/OVERLAP_DETECTION.md leads with that.
type OutboundEnvelope interface {
	// LevelAt returns the outbound RMS at a point on the media timeline,
	// normalised to full scale, and whether it is known.
	//
	// Unknown is a perfectly good answer — the agent may not have been speaking,
	// or the outbound path may not be instrumented — and the detector treats it
	// as "no evidence" rather than as silence.
	LevelAt(mediaTime time.Duration) (float64, bool)
}

// ---------------------------------------------------------------------------

// SpeechLikelihoodModel is the seam a future learned detector would occupy.
//
// # Declared, documented, and deliberately not implemented
//
// §14 of the phase brief forbids an opaque neural model in the hot path and
// requires every decision to be explainable through measured features and
// configured thresholds. It also asks that, if a learned model is desirable
// later, an adapter boundary be defined without being implemented.
//
// This is that boundary and nothing more. NOTHING IN THIS PACKAGE IMPLEMENTS
// IT, nothing calls it, and no code path reaches it.
// TestPorts_NoLearnedModelIsWiredIn checks that mechanically, so a later change
// that quietly puts a model on the frame path has to delete a test to do it.
//
// A future phase adopting this must also decide what happens to [Explanation]:
// a score from a model is not an explanation, and the requirement that every
// decision be explainable does not lapse because the decision got better.
type SpeechLikelihoodModel interface {
	// Score returns the likelihood that a frame contains speech, in [0,1].
	Score(FrameFeatures) (float64, error)
}
