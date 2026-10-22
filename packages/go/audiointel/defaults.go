package audiointel

import "time"

// ---------------------------------------------------------------------------
// Every default in this engine, named, in one file, each with its reasoning
// ---------------------------------------------------------------------------
//
// These are DEFAULTS, not constants of nature. Each is the starting point a
// deployment tunes from, and each is here rather than inline so that "what
// would we change" is answerable by reading one file. A number without a reason
// beside it is a number nobody can safely change, which is how tuning stalls.
//
// Where a value comes from a frozen document it says so and cites it. Where it
// was chosen for this phase it says that too, plainly, so a later reader can
// tell measured from reasoned from arbitrary.

// DefaultEndpointSilenceWindow is the silence a turn must contain before it is
// declared ended.
//
// FROZEN, NOT CHOSEN HERE. ADR-0011 §5.2 hop 1 budgets endpoint detection at
// 250 ms p50 / 350 ms p95 and records it as ours and as a product decision.
// ADR-0011 §7 adds that it is tuned by measuring false-endpoint rate, not by
// minimising latency. Exported because a deployment tuning it should be able to
// see what it is departing from.
const DefaultEndpointSilenceWindow = 250 * time.Millisecond

// BargeInBudget is the frozen interruption budget: one frame interval from the
// detection signal to outbound silence.
//
// FROZEN, NOT CHOSEN HERE. ADR-0004 §12 and ADR-0011 §5.1. Exported so the
// benchmark and the evaluation report measure against the document rather than
// against a number retyped from it.
const BargeInBudget = 20 * time.Millisecond

// Frame cadence and capacity.
const (
	// The defaultFrameInterval value matches Phase 11B's DefaultPipelineConfig. A stream
	// at a different cadence configures it; nothing here assumes 20 ms beyond
	// this default.
	defaultFrameInterval = 20 * time.Millisecond

	// The defaultMaxSessions value bounds concurrent sessions. One session per direction
	// per call, so this is roughly 512 concurrent calls on one process.
	defaultMaxSessions = 1024

	// The defaultSignalWindowFrames value is two seconds of feature history at the
	// default cadence. Long enough to measure energy modulation across several
	// syllables, short enough that the fixed allocation is trivial.
	defaultSignalWindowFrames = 100
)

// Noise floor estimation.
const (
	// The defaultNoiseWarmupFrames value is 300 ms of observation before the estimator
	// will assert anything. Long enough to see past a single transient at the
	// start of a call, short enough that it completes before the caller has
	// finished saying hello.
	defaultNoiseWarmupFrames = 15

	// The defaultNoiseWindowFrames value is two seconds of retained minima.
	defaultNoiseWindowFrames = 100

	// The defaultNoiseRiseAlpha value is deliberately small: at 50 frames per second an
	// alpha of 0.002 takes roughly ten seconds to move the floor most of the
	// way to a genuinely higher level. Slow is correct — rising is the
	// direction speech contamination pushes.
	defaultNoiseRiseAlpha = 0.002

	// The defaultNoiseFallAlpha value is 25x faster: a room that quietens is tracked in
	// under a second. A floor stuck above the true level suppresses real
	// speech, which is the worse of the two failures.
	defaultNoiseFallAlpha = 0.05

	// The defaultNoiseMaxRiseDBPerSecond value bounds upward movement absolutely. Six dB
	// per second means a full second of uninterrupted contamination moves the
	// floor by a factor of two — recoverable within a couple of seconds by the
	// faster fall rate.
	defaultNoiseMaxRiseDBPerSecond = 6.0

	// The defaultNoiseMinFloor value is about -100 dBFS. Its job is to stop a digitally
	// silent line producing a zero denominator, not to model any real noise.
	defaultNoiseMinFloor = 1e-5

	// The defaultNoiseMaxFloor value is -6 dBFS. A "noise floor" above this is not a
	// noise floor; it is a broken input, and clamping stops the estimator
	// chasing it.
	defaultNoiseMaxFloor = 0.5

	// The defaultNoiseConfidenceFrames value is one second: confidence reaches 1 after
	// the estimator has seen a second of audio.
	defaultNoiseConfidenceFrames = 50

	// The defaultNoiseQuietFloor value is about -60 dBFS. A background below this is a
	// good line in a quiet room. Purely a classification boundary — nothing in
	// the detector's behaviour changes when the floor crosses it.
	defaultNoiseQuietFloor = 1e-3

	// The defaultNoiseTransientModulation value is where a background stops looking like
	// a fan and starts looking like traffic. Set well above the speech
	// modulation threshold because background variation is measured with speech
	// frames already excluded, so anything reaching it is genuinely bursty.
	defaultNoiseTransientModulation = 0.80
)

// Voice activity detection.
const (
	// The defaultOnsetThresholdDB value is 9 dB above the floor — a factor of about 2.8
	// in amplitude. Comfortably above the frame-to-frame variation of
	// stationary noise, comfortably below the level of ordinary speech on a
	// telephony line.
	defaultOnsetThresholdDB = 9.0

	// The defaultReleaseThresholdDB value is 5 dB. The 4 dB gap to onset is the
	// hysteresis, and it is what stops a detector flapping on frames that sit
	// on the line.
	defaultReleaseThresholdDB = 5.0

	// The defaultMinOnsetFrames value is 2 — 40 ms at the default cadence. One frame is
	// a door slam; two consecutive is a sound that persisted. Each frame here
	// costs one frame interval of onset latency, so this is as low as it can
	// usefully be.
	defaultMinOnsetFrames = 2

	// The defaultMinSilence value is the hangover, 200 ms. Ordinary speech contains stop
	// closures and inter-word gaps of 50-150 ms; a hangover shorter than those
	// reports a new utterance for every syllable. Kept below the 250 ms
	// endpoint window so an offset is confirmed before the endpoint closes.
	defaultMinSilence = 200 * time.Millisecond

	// The defaultMinSpeech value is 120 ms — shorter than any word, longer than a cough
	// or a handset knock. A run below this emits no onset.
	defaultMinSpeech = 120 * time.Millisecond

	// The defaultZCRMin value excludes pure tones. Hold music, a dial tone, a fax carrier
	// and a ringback all cross zero at a rate set by one frequency, far below
	// the spread of even voiced speech.
	defaultZCRMin = 0.01

	// The defaultZCRMax value excludes broadband hiss, which crosses on nearly every
	// sample.
	//
	// MEASURED, NOT GUESSED. Against this package's fixtures at 8 kHz, speech
	// spans 0.157-0.346 and white noise spans 0.384-0.610. This sits above the
	// speech range with margin for real unvoiced fricatives, which run higher
	// than any synthetic fixture, and below most of the noise range.
	//
	// It does NOT cleanly separate the two, and it is not expected to: the
	// ranges are adjacent, and a 1 kHz tone measures 0.245 — indistinguishable
	// from speech by this feature alone. ZCR is one of three tests for exactly
	// that reason. Modulation is what rejects a steady tone; this is what
	// rejects a rumble below the speech band and the worst of broadband hiss.
	defaultZCRMax = 0.45

	// The defaultModulationWindowFrames value is 200 ms — roughly one syllable at
	// conversational speed, and the shortest window in which speech still
	// looks modulated between its own syllables.
	defaultModulationWindowFrames = 10

	// The defaultProfileGraceFrames value is 200 ms of continuous profile failure while
	// in Speech before the audio is reclassified as noise.
	//
	// Combined with the modulation window above, steady noise that began during
	// silence is reclassified about 400 ms after it started: 200 ms for the
	// short window to fill with it, then 200 ms of grace. That is the cost of
	// the ambiguity documented on SpeechDetector, and it is bounded.
	defaultProfileGraceFrames = 10

	// The defaultMinEnergyModulation value is the coefficient of variation of short-term
	// energy — stddev over mean, dimensionless. Stationary sources (a fan, line
	// hiss, an engine) sit well below 0.2; syllabic speech sits well above it.
	defaultMinEnergyModulation = 0.20

	// The defaultAbsoluteSilenceRMS value is about -80 dBFS. Below this nothing is
	// speech, whatever the ratio to the floor says.
	defaultAbsoluteSilenceRMS = 1e-4

	// The defaultNoiseHoldFrames value is 5 — 100 ms of sustained above-floor energy with
	// a non-speech profile before the Noise state is entered.
	defaultNoiseHoldFrames = 5
)

// Silence classification.
const (
	// The defaultInterWordMax value is 120 ms. Within-phrase pauses and stop closures
	// live below this.
	defaultInterWordMax = 120 * time.Millisecond

	// The defaultInterSentenceMax value equals the endpoint window, and it must not
	// exceed it: a pause longer than the endpoint window IS an endpoint.
	//
	// The honest consequence, documented rather than hidden: with a 250 ms
	// endpoint window this engine CANNOT distinguish a clause boundary from a
	// turn end. Anything long enough to be one is long enough to be the other.
	defaultInterSentenceMax = DefaultEndpointSilenceWindow

	// The defaultLongSilenceMin value is 3 s — a dropped call, a caller who walked away,
	// a hold. Long past anything conversational.
	defaultLongSilenceMin = 3 * time.Second

	// The defaultThinkingMin value is 700 ms of silence after the agent stopped speaking
	// with no caller response yet. A POSITIONAL name: it says the caller has
	// not started, not that they are thinking.
	defaultThinkingMin = 700 * time.Millisecond
)

// Endpointing.
const (
	// The defaultEndpointMinSpeech value is 200 ms. A turn shorter than this is a cough,
	// a "mm", or a line transient — not something to hand to a recogniser as a
	// complete utterance.
	defaultEndpointMinSpeech = 200 * time.Millisecond

	// The defaultMaxTurnDuration value is 60 s. A caller on a noisy line can otherwise
	// hold the detector in Speech indefinitely, and the symptom is an agent
	// that has apparently stopped listening.
	defaultMaxTurnDuration = 60 * time.Second

	// The defaultEnergyTrendTolerance value applies only when RequireFallingEnergy is
	// enabled: 10% of window mean energy may still be rising.
	defaultEnergyTrendTolerance = 0.10
)

// Barge-in.
const (
	// The defaultBargeInMinInterval value is 500 ms of debounce. A caller who interrupts
	// is usually still talking a moment later and the VAD may legitimately
	// re-confirm; without this, one interruption becomes several, each
	// cancelling the turn the previous one opened.
	defaultBargeInMinInterval = 500 * time.Millisecond

	// The defaultBargeInMaxAge value is 200 ms. Beyond that the agent has likely moved
	// on, and cancelling then cuts off whatever it started next — which to the
	// caller looks like the agent interrupting itself.
	defaultBargeInMaxAge = 200 * time.Millisecond
)

// Overlap.
const (
	// The defaultOverlapMinDuration value is 200 ms of sustained caller speech while the
	// agent holds the floor. Clicks, handset bumps and codec transients are
	// exactly what this excludes.
	defaultOverlapMinDuration = 200 * time.Millisecond

	// The defaultOverlapResolveAfter value is 300 ms after the conditions cease.
	defaultOverlapResolveAfter = 300 * time.Millisecond

	// The defaultEchoCorrelationPenalty value removes 40% of confidence when the inbound
	// envelope tracks our own outbound envelope. A penalty, never a verdict:
	// correlation with our own output is weak evidence that we are hearing it,
	// and its absence proves nothing at all.
	defaultEchoCorrelationPenalty = 0.40

	// The defaultOverlapMinConfidence value is 0.5 — overlap is confirmed only on the
	// balance of the evidence.
	defaultOverlapMinConfidence = 0.50
)

// Audio quality.
const (
	// The defaultMinSignalRMS value is about -66 dBFS. Speech below this is too quiet for
	// a recogniser to do anything useful with.
	defaultMinSignalRMS = 5e-4

	// The defaultDegradedClipRatio value is 0.2% of samples at full scale. Occasional
	// clipping is survivable; it is worth reporting.
	defaultDegradedClipRatio = 0.002

	// The defaultMaxClipRatio value is 2%. Beyond this the waveform is being destroyed
	// faster than any recogniser recovers from.
	defaultMaxClipRatio = 0.02

	// The defaultDegradedSNRDB value is 12 dB — recognisable but working hard.
	defaultDegradedSNRDB = 12.0

	// The defaultMinSNRDB value is 6 dB. Below this, speech and noise are within a factor
	// of two of each other.
	defaultMinSNRDB = 6.0

	// The defaultDegradedGapRatio value is 2% frame loss over the window.
	defaultDegradedGapRatio = 0.02

	// The defaultMaxGapRatio value is 10%. One frame in ten missing is not a stream that
	// carries a conversation.
	defaultMaxGapRatio = 0.10

	// The defaultMinDynamicRange value is 3 dB of peak-to-RMS spread. Below this the
	// signal is suspiciously flat: a stuck codec, a constant tone, a dead line
	// carrying only its own noise. Real speech has a crest factor far above it.
	defaultMinDynamicRange = 3.0

	// The defaultQualityWindowFrames value is two seconds, matching the signal window.
	defaultQualityWindowFrames = 100

	// The defaultQualityHysteresisFrames value is 5 — 100 ms of agreement before a class
	// change is adopted, so quality does not flap across a boundary.
	defaultQualityHysteresisFrames = 5
)

// Frame continuity.
const (
	// The defaultMaxTimestampJump value is 100 ms — five frames at the default cadence.
	// Beyond that the media timeline has jumped rather than merely gapped.
	defaultMaxTimestampJump = 100 * time.Millisecond

	// The defaultContinuityWindowFrames value is two seconds of continuity history.
	defaultContinuityWindowFrames = 100

	// The defaultContinuityRestoreFrames value is 25 — half a second of clean frames
	// before continuity is declared restored, so a stream flapping in and out
	// of loss does not emit a restore event between every gap.
	defaultContinuityRestoreFrames = 25
)
