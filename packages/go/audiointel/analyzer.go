package audiointel

import (
	"context"
	"fmt"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// ConversationState is everything this engine needs to know about the
// conversation, and it is deliberately two fields.
//
// # It does not know what a conversation is
//
// Barge-in and overlap are undefined without knowing whether the agent holds
// the floor, and endpointing needs to know whether an interruption is
// unresolved. That is the whole dependency. Taking a conversation object, a
// turn manager or a dialogue state would put dialogue vocabulary into an audio
// pipeline and make this engine untestable without one — the same reasoning
// packages/go/media used to avoid depending on packages/go/telephony, and
// packages/go/speech used to avoid depending on packages/go/conversation.
type ConversationState struct {
	// AgentSpeaking reports whether the agent currently holds the floor.
	AgentSpeaking bool

	// Turn identifies the speech turn, where the caller has one. Opaque, and
	// carried onto events for correlation only.
	Turn TurnID
}

// Analysis is everything this engine concluded about one frame.
//
// Returned by value. Every field is a value type holding scalars, so a caller
// may retain it indefinitely without retaining any audio — which is what makes
// it safe to hand to a consumer that logs, buffers or forwards it.
type Analysis struct {
	// Signal is the measurement and the bounded statistics.
	Signal SignalView

	// VAD is the voice activity verdict, with its explanation.
	VAD VADDecision

	// Continuity is what the transport did to this frame.
	Continuity ContinuityReport

	// Silence classifies the current pause, when there is one.
	Silence SilenceReport

	// Endpoint is the turn-boundary verdict.
	Endpoint EndpointDecision

	// BargeIn is the interruption verdict, and whether one was delivered.
	BargeIn BargeInDecision

	// Overlap is the double-talk verdict. READ ITS CONFIDENCE, not its state.
	Overlap OverlapDecision

	// Quality is the usability judgement.
	Quality QualityReport
}

// Frame returns the measured features. Convenience for the common access.
func (a Analysis) Frame() FrameFeatures { return a.Signal.Frame }

// SpeechActive reports whether a recogniser should be receiving this audio.
func (a Analysis) SpeechActive() bool { return a.VAD.SpeechActive() }

// String renders the analysis, content-free.
func (a Analysis) String() string {
	return fmt.Sprintf("%s | %s | %s", a.Signal, a.VAD.State, a.Quality.Class)
}

// AudioAnalyzer composes every detector for one direction of one call's audio.
//
// # The ordering below is the design, not an implementation detail
//
// Each stage consumes what the previous one produced, and two of the couplings
// are subtle enough that getting them backwards silently degrades the engine
// rather than breaking it:
//
//  1. MEASUREMENT — FrameAnalyzer turns PCM into scalars. The payload is read
//     here and nowhere else, and nothing downstream can see it.
//  2. SIGNAL — the noise floor adapts using the PREVIOUS frame's speech verdict
//     (the gate must lag; see SignalAnalyzer), then the bounded window updates.
//  3. VOICE ACTIVITY — the state machine decides against the floor from step 2.
//  4. RETRACTION — if an onset just confirmed, the frames the floor recorded
//     during the confirmation window were speech and are withdrawn. Without
//     this, every utterance halves the reported confidence of everything
//     downstream for two seconds.
//  5. CONTINUITY, SILENCE, ENDPOINT, BARGE-IN, OVERLAP, QUALITY — all consume
//     steps 1–3 and none of them feeds back.
//
// Nothing here allocates and nothing spawns a goroutine.
//
// Not safe for concurrent use. [Session] provides the lock; this type is the
// unsynchronised core so a caller that already has one goroutine per direction
// pays for nothing.
type AudioAnalyzer struct {
	cfg Config

	frames     *FrameAnalyzer
	signal     *SignalAnalyzer
	vad        *SpeechDetector
	continuity *ContinuityDetector
	silence    *SilenceClassifier
	endpoint   *EndpointDetector
	bargeIn    *BargeInDetector
	overlap    *OverlapDetector
	quality    *QualityAnalyzer

	// last carries the previous frame's verdict, which is what the noise gate
	// lags on.
	last VADDecision
}

// NewAudioAnalyzer builds the detector chain for one direction.
func NewAudioAnalyzer(cfg Config, clock rt.Clock) (*AudioAnalyzer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if clock == nil {
		clock = rt.SystemClock{}
	}

	a := &AudioAnalyzer{cfg: cfg}

	var err error
	if a.frames, err = NewFrameAnalyzer(cfg.Format); err != nil {
		return nil, err
	}
	if a.signal, err = NewSignalAnalyzer(cfg); err != nil {
		return nil, err
	}
	if a.vad, err = NewSpeechDetector(cfg, clock); err != nil {
		return nil, err
	}
	if a.continuity, err = NewContinuityDetector(cfg.Continuity); err != nil {
		return nil, err
	}
	if a.silence, err = NewSilenceClassifier(cfg); err != nil {
		return nil, err
	}
	if a.endpoint, err = NewEndpointDetector(cfg); err != nil {
		return nil, err
	}
	if a.bargeIn, err = NewBargeInDetector(cfg, clock); err != nil {
		return nil, err
	}
	if a.overlap, err = NewOverlapDetector(cfg, clock); err != nil {
		return nil, err
	}
	if a.quality, err = NewQualityAnalyzer(cfg.Quality); err != nil {
		return nil, err
	}

	return a, nil
}

// Analyze runs one frame through the whole chain.
//
// The frame's payload is read in place and never retained — see
// [FrameFeatures]. Runs inline on the caller's goroutine.
func (a *AudioAnalyzer) Analyze(
	ctx context.Context,
	f media.Frame,
	state ConversationState,
	controller SpeechController,
	envelope OutboundEnvelope,
) (Analysis, error) {
	features, err := a.frames.Analyze(f)
	if err != nil {
		return Analysis{}, err
	}

	view := a.signal.Observe(features, a.last.SpeechActive())
	decision := a.vad.Observe(view)

	// Step 4: withdraw the frames the floor mistook for background while the
	// onset was being confirmed. See NoiseAnalyzer.Retract.
	if decision.OnsetConfirmed {
		a.signal.RetractOnsetLeak()
	}
	a.last = decision

	out := Analysis{Signal: view, VAD: decision}

	out.Continuity = a.continuity.Observe(features)

	// The silence classifier needs to know the conversational position, which
	// only the voice activity verdict and the caller's own state can supply.
	if decision.OnsetConfirmed {
		a.silence.NoteSpeech()
	}
	out.Silence = a.silence.Observe(decision.SilenceDuration)

	out.Endpoint = a.endpoint.Observe(view, decision, EndpointGates{
		AgentSpeaking: state.AgentSpeaking,
		BargeInActive: a.bargeIn.Active(),
	})

	out.BargeIn = a.bargeIn.Observe(ctx, view, decision, state.AgentSpeaking, controller)
	out.Overlap = a.overlap.Observe(view, decision, state.AgentSpeaking, envelope)
	out.Quality = a.quality.Observe(view, out.Continuity.GapRatio)

	return out, nil
}

// NoteAgentFinished tells the silence classifier the agent stopped speaking, so
// the following pause is classified by position rather than only by duration.
func (a *AudioAnalyzer) NoteAgentFinished() { a.silence.NoteAgentFinished() }

// Config returns the configuration this analyser was built with.
func (a *AudioAnalyzer) Config() Config { return a.cfg }

// VADState returns the current voice activity state.
func (a *AudioAnalyzer) VADState() VADState { return a.vad.State() }

// QualityClass returns the current usability judgement.
func (a *AudioAnalyzer) QualityClass() QualityClass { return a.quality.Class() }

// OverlapState returns the current double-talk state.
func (a *AudioAnalyzer) OverlapState() OverlapState { return a.overlap.State() }

// NoiseFloor returns the current adaptive background estimate.
func (a *AudioAnalyzer) NoiseFloor() float64 { return a.signal.Noise().Floor() }

// ObserveDelivery folds in Phase 11B's verdict on a frame that never arrived.
func (a *AudioAnalyzer) ObserveDelivery(r media.PipelineResult) ContinuityFault {
	return a.continuity.ObserveDelivery(r)
}

// Reset returns every detector to its initial state, keeping their storage.
//
// For a session recovering mid-call. The storage is retained deliberately:
// reallocating the windows would be the one allocation this design avoids,
// arriving at the worst moment.
func (a *AudioAnalyzer) Reset() {
	a.signal.Reset()
	a.vad.Reset()
	a.continuity.Reset()
	a.silence.Reset()
	a.endpoint.Reset()
	a.bargeIn.Reset()
	a.overlap.Reset()
	a.quality.Reset()
	a.last = VADDecision{}
}
