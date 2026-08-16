package audiointel

import (
	"fmt"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
)

// ---------------------------------------------------------------------------
// Why every number in this engine lives in this file
// ---------------------------------------------------------------------------
//
// A voice activity detector is a pile of thresholds wearing a state machine.
// Scattered through the implementation, those thresholds become undiscoverable:
// nobody can answer "what would we change to make this work on a noisier line"
// without reading every detector, and nobody can tell which numbers were
// reasoned about from which were typed once and never revisited.
//
// So they are all here, they are all named, they are all validated at
// construction, and TestConfig_NoDurationLiteralsInDetectors enforces that no
// detector file contains a time.Millisecond or time.Second literal. An invalid
// configuration fails closed — a detector built from nonsense would produce
// nonsense confidently, which is worse than refusing to start.

// ---------------------------------------------------------------------------
// Format
// ---------------------------------------------------------------------------

// validateAnalysisFormat reports why a format cannot be analysed, or nil.
//
// # Two refusals, both deliberate
//
// STEREO IS REFUSED. Voice activity on an interleaved two-channel payload is
// undefined without a stated mixing policy: analysing channel 0 silently
// discards half the audio, and summing the channels silently changes the level
// every threshold in this file is calibrated against. Either could be right for
// a given deployment. Choosing one invisibly is wrong for the other, and the
// symptom would be a detector that works in the lab and misses speech in
// production. Telephony audio is mono; a stereo stream reaching here means an
// upstream misconfiguration that should be fixed there.
//
// CodecOpaque IS REFUSED. Its sample count is unknowable to anything but its
// own decoder — media.Frame.Samples returns zero for it by design — so no
// feature in this package can be computed from it. Accepting it would produce
// an RMS of zero for every frame and a detector that reports permanent silence.
func validateAnalysisFormat(f media.AudioFormat) error {
	if err := f.Validate(); err != nil {
		return err
	}
	if f.Codec != media.CodecPCM {
		return fmt.Errorf("%w: codec %s cannot be inspected — only its own decoder "+
			"knows how many samples its payload holds, so no feature can be measured "+
			"from it; decode upstream and present PCM", ErrUnsupportedFormat, f.Codec)
	}
	if f.Layout != media.LayoutMono {
		return fmt.Errorf("%w: layout %s has no defined mixing policy here — analysing "+
			"one channel discards half the audio and summing channels changes the level "+
			"every threshold is calibrated against; downmix upstream and present mono",
			ErrUnsupportedFormat, f.Layout)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Noise
// ---------------------------------------------------------------------------

// NoiseConfig configures the adaptive noise floor estimator.
//
// See docs/audio-intelligence/NOISE_AND_QUALITY.md. The asymmetry between
// RiseAlpha and FallAlpha is the load-bearing part: a floor that rises as
// readily as it falls is a floor that speech eventually redefines.
type NoiseConfig struct {
	// WarmupFrames is how many frames the estimator observes before it will
	// assert anything.
	//
	// During warm-up the detector reports Uncertain and refuses to declare
	// speech. That is honest: a speech decision is a comparison against the
	// floor, and before the floor exists there is nothing to compare against.
	WarmupFrames int

	// WindowFrames bounds the retained minima history. Fixed size, allocated
	// once at construction. Nothing here grows.
	WindowFrames int

	// RiseAlpha is the per-frame adaptation coefficient when the floor moves UP.
	//
	// Deliberately small. A rising floor is the direction speech contamination
	// pushes it, so adaptation upward is grudging.
	RiseAlpha float64

	// FallAlpha is the per-frame adaptation coefficient when the floor moves
	// DOWN.
	//
	// Deliberately larger than RiseAlpha. A room that quietens should be
	// tracked promptly — a floor stuck above the true level suppresses real
	// speech, which is the worse failure of the two.
	FallAlpha float64

	// MaxRiseDBPerSecond hard-clamps upward movement regardless of RiseAlpha.
	//
	// THE GUARD AGAINST ONE LOUD FRAME REDEFINING THE FLOOR PERMANENTLY. The
	// speech gate should mean speech never reaches the estimator at all; this
	// bounds the damage when it does anyway.
	MaxRiseDBPerSecond float64

	// MinFloor and MaxFloor clamp the estimate to a plausible range.
	//
	// MinFloor matters most: on a digitally silent line the measured floor is
	// zero, and a ratio against zero is an infinity that makes the first
	// non-zero sample look like a shout.
	MinFloor float64
	MaxFloor float64

	// ConfidenceFrames is the observation count at which floor confidence
	// reaches 1.
	ConfidenceFrames int

	// QuietFloor is the estimate below which the background is called quiet.
	//
	// A classification boundary, not a clamp. Nothing changes in the detector's
	// behaviour when the floor crosses it; it changes what [NoiseClass] the
	// engine reports, which is an operator-facing signal.
	QuietFloor float64

	// TransientModulation is the coefficient of variation of the BACKGROUND —
	// speech frames excluded — above which the noise is called transient rather
	// than stationary.
	//
	// A fan produces a background that barely varies. Traffic, a busy room and
	// slamming doors produce one that varies enormously. The two need different
	// handling by anything downstream, and the distinction is measurable.
	TransientModulation float64
}

func (c NoiseConfig) validate() []string {
	var p []string
	if c.WarmupFrames <= 0 {
		p = append(p, "noise: WarmupFrames must be positive; without a warm-up the "+
			"first frame defines the floor and the first word defines it forever")
	}
	if c.WindowFrames <= 0 {
		p = append(p, "noise: WindowFrames must be positive")
	}
	if c.WindowFrames < c.WarmupFrames {
		p = append(p, fmt.Sprintf("noise: WindowFrames (%d) must be at least "+
			"WarmupFrames (%d), or warm-up observes more than the window retains",
			c.WindowFrames, c.WarmupFrames))
	}
	if c.RiseAlpha <= 0 || c.RiseAlpha >= 1 {
		p = append(p, fmt.Sprintf("noise: RiseAlpha %g must be in (0,1)", c.RiseAlpha))
	}
	if c.FallAlpha <= 0 || c.FallAlpha >= 1 {
		p = append(p, fmt.Sprintf("noise: FallAlpha %g must be in (0,1)", c.FallAlpha))
	}
	if c.RiseAlpha >= c.FallAlpha {
		p = append(p, fmt.Sprintf("noise: RiseAlpha (%g) must be smaller than FallAlpha "+
			"(%g) — a floor that rises as readily as it falls is one that speech "+
			"eventually redefines", c.RiseAlpha, c.FallAlpha))
	}
	if c.MaxRiseDBPerSecond <= 0 {
		p = append(p, "noise: MaxRiseDBPerSecond must be positive; it is the only hard "+
			"bound on speech contaminating the floor")
	}
	if c.MinFloor <= 0 {
		p = append(p, "noise: MinFloor must be positive; a floor of zero makes every "+
			"ratio against it an infinity")
	}
	if c.MaxFloor <= c.MinFloor {
		p = append(p, fmt.Sprintf("noise: MaxFloor (%g) must exceed MinFloor (%g)",
			c.MaxFloor, c.MinFloor))
	}
	if c.ConfidenceFrames <= 0 {
		p = append(p, "noise: ConfidenceFrames must be positive")
	}
	if c.QuietFloor <= 0 {
		p = append(p, "noise: QuietFloor must be positive")
	}
	if c.QuietFloor < c.MinFloor {
		p = append(p, fmt.Sprintf("noise: QuietFloor (%g) is below MinFloor (%g), so "+
			"no estimate could ever be classified as quiet", c.QuietFloor, c.MinFloor))
	}
	if c.TransientModulation <= 0 {
		p = append(p, "noise: TransientModulation must be positive")
	}
	return p
}

// ---------------------------------------------------------------------------
// Voice activity
// ---------------------------------------------------------------------------

// VADConfig configures voice activity detection.
//
// Three independent features, each with its own threshold, combined by the rule
// documented in docs/audio-intelligence/VAD_ARCHITECTURE.md. There is
// deliberately no single energy threshold: a fixed one is wrong on every line
// whose noise floor differs from the one it was tuned on, which is all of them.
type VADConfig struct {
	// OnsetThresholdDB is how far above the noise floor a frame must sit to be
	// evidence of speech.
	OnsetThresholdDB float64

	// ReleaseThresholdDB is how far above the floor a frame must sit to keep
	// speech going.
	//
	// LOWER THAN OnsetThresholdDB, AND THE GAP IS THE HYSTERESIS. Speech that
	// has already started is given the benefit of the doubt, because the
	// alternative is a detector that chops every word at its quiet trailing
	// consonant.
	ReleaseThresholdDB float64

	// MinOnsetFrames is how many consecutive above-threshold frames confirm
	// speech.
	//
	// A single loud frame is a door slam, not a word. Each frame of
	// confirmation costs one frame interval of onset latency, which is the
	// trade-off being made here explicitly rather than accidentally.
	MinOnsetFrames int

	// MinSilence is the hangover: how long sub-threshold audio must persist
	// before speech is declared over.
	//
	// A single silent frame never ends speech. Ordinary speech contains stop
	// closures and inter-word gaps far longer than one frame, and a detector
	// without a hangover reports a new utterance for every syllable.
	MinSilence time.Duration

	// MinSpeech is the shortest run that counts as speech at all. A run shorter
	// than this is reclassified as a transient and emits no onset.
	MinSpeech time.Duration

	// ZCRMin and ZCRMax bound the zero-crossing rate band consistent with
	// speech.
	//
	// A pure tone — a hold-music note, a fax carrier, a dial tone — crosses
	// zero at a rate set by its frequency and sits below the band. Broadband
	// hiss crosses on nearly every sample and sits above it. Speech, voiced and
	// unvoiced together, spans the middle. This feature is a REJECTOR: it does
	// not identify speech, it excludes two things that are definitely not.
	ZCRMin float64
	ZCRMax float64

	// ModulationWindowFrames is how much recent history the modulation test
	// looks at.
	//
	// DELIBERATELY SHORT, and much shorter than SignalWindowFrames. At the
	// instant any sound begins, a long window is mostly the silence that
	// preceded it, so a fan switching on measures as strongly modulated as a
	// word. A short window fills with the new sound within a few hundred
	// milliseconds and then tells the truth about it.
	//
	// Roughly one syllable is the floor: shorter than that and genuine speech
	// stops looking modulated between its own syllables.
	ModulationWindowFrames int

	// ProfileGraceFrames is how many consecutive frames the speech profile may
	// fail while already in Speech before the audio is reclassified as noise.
	//
	// THE ESCAPE HATCH FROM THE "ONE TEST TO STAY" RULE, and it exists for a
	// specific failure: steady noise or hold music that began during silence
	// passes the onset test — because at that moment nothing has had time to
	// look steady — and would otherwise hold the detector in Speech for as long
	// as it continued, on the energy test alone.
	//
	// Generous on purpose. A real speaker whose delivery goes briefly flat must
	// not be reclassified as a fan, so this is measured in hundreds of
	// milliseconds rather than tens.
	ProfileGraceFrames int

	// MinEnergyModulation is the coefficient of variation of short-term energy
	// across the modulation window, below which the signal is treated as
	// stationary.
	//
	// DIMENSIONLESS ON PURPOSE — stddev over mean, so it means the same thing
	// at any level. Stationary noise (a fan, line hiss, a running engine) has
	// low modulation. Speech is strongly modulated by syllables. An absolute
	// variance threshold would need recalibrating for every input level; this
	// one does not.
	MinEnergyModulation float64

	// AbsoluteSilenceRMS is the level below which nothing is speech, whatever
	// the ratio says.
	//
	// THE GUARD AGAINST A DIGITALLY SILENT LINE. When the floor sits at
	// MinFloor and a frame arrives at twice that, the ratio is +6 dB and the
	// detector would call inaudible dither a word.
	AbsoluteSilenceRMS float64

	// NoiseHoldFrames is how many consecutive frames of above-floor energy with
	// a non-speech profile enter the Noise state.
	NoiseHoldFrames int
}

func (c VADConfig) validate(frameInterval time.Duration) []string {
	var p []string
	if c.OnsetThresholdDB <= 0 {
		p = append(p, "vad: OnsetThresholdDB must be positive — speech must be LOUDER "+
			"than the noise floor to be distinguishable from it")
	}
	if c.ReleaseThresholdDB <= 0 {
		p = append(p, "vad: ReleaseThresholdDB must be positive")
	}
	if c.ReleaseThresholdDB >= c.OnsetThresholdDB {
		p = append(p, fmt.Sprintf("vad: ReleaseThresholdDB (%g) must be BELOW "+
			"OnsetThresholdDB (%g) — equal thresholds are no hysteresis, and a "+
			"detector without hysteresis flaps on every frame that sits on the line",
			c.ReleaseThresholdDB, c.OnsetThresholdDB))
	}
	if c.MinOnsetFrames < 1 {
		p = append(p, "vad: MinOnsetFrames must be at least 1")
	}
	if c.MinSilence <= 0 {
		p = append(p, "vad: MinSilence must be positive; without a hangover one silent "+
			"frame ends speech and every stop consonant becomes a turn boundary")
	}
	if frameInterval > 0 && c.MinSilence < frameInterval {
		p = append(p, fmt.Sprintf("vad: MinSilence (%s) is shorter than one frame (%s), "+
			"so the hangover cannot span even a single silent frame",
			c.MinSilence, frameInterval))
	}
	if c.MinSpeech < 0 {
		p = append(p, "vad: MinSpeech must not be negative")
	}
	if c.ZCRMin < 0 || c.ZCRMin >= 1 {
		p = append(p, fmt.Sprintf("vad: ZCRMin %g must be in [0,1)", c.ZCRMin))
	}
	if c.ZCRMax <= 0 || c.ZCRMax > 1 {
		p = append(p, fmt.Sprintf("vad: ZCRMax %g must be in (0,1]", c.ZCRMax))
	}
	if c.ZCRMin >= c.ZCRMax {
		p = append(p, fmt.Sprintf("vad: ZCRMin (%g) must be below ZCRMax (%g)",
			c.ZCRMin, c.ZCRMax))
	}
	if c.MinEnergyModulation < 0 {
		p = append(p, "vad: MinEnergyModulation must not be negative")
	}
	if c.ModulationWindowFrames < 2 {
		p = append(p, "vad: ModulationWindowFrames must be at least 2; a coefficient "+
			"of variation over one observation is not a measurement")
	}
	if c.ProfileGraceFrames < 1 {
		p = append(p, "vad: ProfileGraceFrames must be at least 1; without a grace "+
			"period a single unmodulated frame reclassifies a speaker as a fan")
	}
	if c.AbsoluteSilenceRMS <= 0 {
		p = append(p, "vad: AbsoluteSilenceRMS must be positive; it is the only guard "+
			"against a digitally silent line making dither look like speech")
	}
	if c.NoiseHoldFrames < 1 {
		p = append(p, "vad: NoiseHoldFrames must be at least 1")
	}
	return p
}

// ---------------------------------------------------------------------------
// Silence
// ---------------------------------------------------------------------------

// SilenceConfig configures silence classification.
//
// # These are timing signals, not language understanding
//
// A pause of 300 ms in a certain position is called an InterSentencePause
// because that is a useful operational name, NOT because this engine has
// determined that a sentence ended. Acoustic silence carries no semantics. See
// docs/audio-intelligence/README.md §Silence.
type SilenceConfig struct {
	// InterWordMax is the upper bound on a pause treated as within a word
	// group.
	InterWordMax time.Duration

	// InterSentenceMax is the upper bound on a pause treated as between
	// clauses.
	//
	// Cross-validated against EndpointPolicy.SilenceWindow: it may not exceed
	// it, because a pause longer than the endpoint window IS an endpoint by
	// definition and calling it something else would be two names for one
	// event.
	InterSentenceMax time.Duration

	// LongSilenceMin is where a silence becomes notable in its own right — a
	// dropped call, a caller who walked away, a hold.
	LongSilenceMin time.Duration

	// ThinkingMin is how long silence must persist AFTER the agent stopped
	// speaking, with no caller speech yet, to be called a thinking pause.
	//
	// A POSITIONAL classification, not a semantic one. It says the caller has
	// not started responding yet; it does not say they are thinking.
	ThinkingMin time.Duration
}

func (c SilenceConfig) validate() []string {
	var p []string
	if c.InterWordMax <= 0 {
		p = append(p, "silence: InterWordMax must be positive")
	}
	if c.InterSentenceMax <= c.InterWordMax {
		p = append(p, fmt.Sprintf("silence: InterSentenceMax (%s) must exceed "+
			"InterWordMax (%s)", c.InterSentenceMax, c.InterWordMax))
	}
	if c.LongSilenceMin <= c.InterSentenceMax {
		p = append(p, fmt.Sprintf("silence: LongSilenceMin (%s) must exceed "+
			"InterSentenceMax (%s)", c.LongSilenceMin, c.InterSentenceMax))
	}
	if c.ThinkingMin <= 0 {
		p = append(p, "silence: ThinkingMin must be positive")
	}
	return p
}

// ---------------------------------------------------------------------------
// Endpointing
// ---------------------------------------------------------------------------

// EndpointPolicy configures turn endpointing.
//
// # Endpointing is a product decision, and the numbers come from ADR-0011
//
// ADR-0011 §5.2 hop 1 budgets endpoint detection at 250 ms p50 / 350 ms p95 and
// records it as the largest single item in the end-to-end budget and the most
// tunable. ADR-0005 C6 records that we own it and vendor endpointing is
// disabled or ignored.
//
// # There is no English pause model here
//
// Every threshold is configuration. A deployment serving Hindi or Hinglish
// callers tunes SilenceWindow; it does not change code. The default is the
// ADR-0011 figure, which was itself set for this platform's traffic, not
// derived from any language's phonology.
type EndpointPolicy struct {
	// SilenceWindow is how long silence must persist from its START before a
	// turn is declared ended.
	//
	// Measured from the first sub-threshold frame, NOT from the moment the VAD
	// confirmed the offset. Measuring from offset confirmation would silently
	// add the hangover to this budget and make the ADR-0011 comparison wrong.
	SilenceWindow time.Duration

	// MinSpeechDuration is the shortest utterance that may produce an endpoint.
	// A cough is not a turn.
	MinSpeechDuration time.Duration

	// MaxTurnDuration forces an endpoint on a turn that will not end.
	//
	// A caller on a noisy line can hold the detector in Speech indefinitely.
	// Without this the conversation never advances and the symptom is an agent
	// that has apparently stopped listening.
	MaxTurnDuration time.Duration

	// RequireFallingEnergy gates confirmation on a non-rising energy trend.
	//
	// OFF BY DEFAULT. It suppresses endpoints where the caller is audibly
	// winding up to say more, but it also defers endpoints on a rising noise
	// floor. Simple behaviour first; a deployment that measures a benefit turns
	// it on.
	RequireFallingEnergy bool

	// EnergyTrendTolerance is how much rise is tolerated when
	// RequireFallingEnergy is set, as a fraction of window mean energy.
	EnergyTrendTolerance float64

	// SuppressWhileAgentSpeaking withholds endpoints while the agent holds the
	// floor.
	//
	// What the caller says while the agent is talking is a barge-in, and
	// barge-in opens its own turn. Endpointing it as well would end a turn that
	// the interruption already replaced.
	SuppressWhileAgentSpeaking bool

	// SuppressDuringBargeIn withholds endpoints while a barge-in is unresolved.
	SuppressDuringBargeIn bool
}

func (c EndpointPolicy) validate(frameInterval time.Duration) []string {
	var p []string
	if c.SilenceWindow <= 0 {
		p = append(p, "endpoint: SilenceWindow must be positive")
	}
	if frameInterval > 0 && c.SilenceWindow < frameInterval {
		p = append(p, fmt.Sprintf("endpoint: SilenceWindow (%s) is shorter than one "+
			"frame (%s) and would confirm on the first silent frame",
			c.SilenceWindow, frameInterval))
	}
	if c.MinSpeechDuration < 0 {
		p = append(p, "endpoint: MinSpeechDuration must not be negative")
	}
	if c.MaxTurnDuration <= 0 {
		p = append(p, "endpoint: MaxTurnDuration must be positive; without a forced "+
			"endpoint a caller on a noisy line holds the turn forever")
	}
	if c.MaxTurnDuration <= c.MinSpeechDuration {
		p = append(p, fmt.Sprintf("endpoint: MaxTurnDuration (%s) must exceed "+
			"MinSpeechDuration (%s)", c.MaxTurnDuration, c.MinSpeechDuration))
	}
	if c.EnergyTrendTolerance < 0 {
		p = append(p, "endpoint: EnergyTrendTolerance must not be negative")
	}
	return p
}

// ---------------------------------------------------------------------------
// Barge-in
// ---------------------------------------------------------------------------

// BargeInPolicy configures interruption detection.
//
// ADR-0004 §12 and ADR-0011 §5.1 budget barge-in at one frame interval — 20 ms
// — from the detection signal to outbound silence. ADR-0004 §247 adds that any
// queue between the detector and the output is added interruption latency,
// which is why this engine has none.
//
// Read that budget precisely: it runs from the DETECTION, not from acoustic
// onset. The frames VADConfig.MinOnsetFrames spends confirming an onset are
// upstream of it. PERFORMANCE.md reports both numbers separately so neither is
// mistaken for the other.
type BargeInPolicy struct {
	// Enabled turns interruption detection on.
	Enabled bool

	// MinInterval debounces repeat detections.
	//
	// A caller who interrupts is usually still talking a moment later, and the
	// VAD may legitimately re-confirm. Without this every barge-in is followed
	// by several more, each cancelling a turn the previous one already opened.
	MinInterval time.Duration

	// MaxAge discards a detection older than this against the injected clock.
	//
	// A STALE INTERRUPTION IS WORSE THAN A MISSED ONE. Cancelling speech the
	// agent finished saying half a second ago cuts off whatever it started
	// next, and to the caller it looks like the agent interrupting itself.
	MaxAge time.Duration

	// ConfirmFrames requires extra above-threshold frames beyond the VAD's own
	// onset confirmation.
	//
	// ZERO BY DEFAULT: fire when the VAD is sure. Each frame here costs one
	// frame interval of interruption latency against a 20 ms budget, so this is
	// a knob for a deployment measuring false barge-ins, not a default posture.
	ConfirmFrames int

	// RequireAgentSpeaking withholds barge-in unless the agent holds the floor.
	//
	// TRUE BY DEFAULT, and it mirrors Phase 11C: SpeechSession.Interrupt refuses
	// unless the turn is responding or speaking, because a caller talking while
	// we are listening is not interrupting, they are just talking. Firing anyway
	// would cancel a turn that was recognising speech perfectly well.
	RequireAgentSpeaking bool
}

func (c BargeInPolicy) validate() []string {
	var p []string
	if !c.Enabled {
		return nil
	}
	if c.MinInterval <= 0 {
		p = append(p, "bargein: MinInterval must be positive when barge-in is enabled; "+
			"without a debounce one interruption becomes several")
	}
	if c.MaxAge <= 0 {
		p = append(p, "bargein: MaxAge must be positive when barge-in is enabled; "+
			"without it a stale detection cancels speech the agent already finished")
	}
	if c.ConfirmFrames < 0 {
		p = append(p, "bargein: ConfirmFrames must not be negative")
	}
	return p
}

// ---------------------------------------------------------------------------
// Overlap
// ---------------------------------------------------------------------------

// OverlapPolicy configures double-talk detection.
//
// # Read the limitation before the fields
//
// Without an acoustic echo canceller, and without the outbound reference signal
// being sample-aligned with the inbound one, ECHO AND GENUINE DOUBLE-TALK ARE
// NOT SEPARABLE. This engine does not claim source separation and does not
// perform any. Every output is confidence-based. See
// docs/audio-intelligence/OVERLAP_DETECTION.md, which states this before it
// states anything else.
type OverlapPolicy struct {
	// Enabled turns overlap detection on.
	Enabled bool

	// MinDuration is how long caller speech must persist while the agent holds
	// the floor before overlap is confirmed rather than merely possible.
	//
	// Short acoustic artifacts — a click, a handset bump, a codec transient —
	// are exactly what this excludes.
	MinDuration time.Duration

	// ResolveAfter is how long after the overlap conditions cease before the
	// state returns to resolved.
	ResolveAfter time.Duration

	// EchoCorrelationPenalty reduces confidence when the inbound envelope
	// tracks the supplied outbound envelope.
	//
	// USED ONLY TO LOWER CONFIDENCE, NEVER TO ASSERT SEPARATION. Correlation
	// with our own output is weak evidence that what we are hearing is our own
	// output. It is not proof, and the absence of correlation is not proof of
	// the opposite.
	EchoCorrelationPenalty float64

	// MinConfidence is the floor below which no overlap is confirmed.
	MinConfidence float64
}

func (c OverlapPolicy) validate() []string {
	var p []string
	if !c.Enabled {
		return nil
	}
	if c.MinDuration <= 0 {
		p = append(p, "overlap: MinDuration must be positive when overlap detection is "+
			"enabled, or every acoustic click confirms double-talk")
	}
	if c.ResolveAfter <= 0 {
		p = append(p, "overlap: ResolveAfter must be positive when overlap detection is "+
			"enabled")
	}
	if c.EchoCorrelationPenalty < 0 || c.EchoCorrelationPenalty > 1 {
		p = append(p, fmt.Sprintf("overlap: EchoCorrelationPenalty %g must be in [0,1]",
			c.EchoCorrelationPenalty))
	}
	if c.MinConfidence < 0 || c.MinConfidence > 1 {
		p = append(p, fmt.Sprintf("overlap: MinConfidence %g must be in [0,1]",
			c.MinConfidence))
	}
	return p
}

// ---------------------------------------------------------------------------
// Quality
// ---------------------------------------------------------------------------

// QualityThresholds configures audio quality classification.
//
// Four classes from measurable properties. Nothing here is a subjective
// judgement dressed up as a number: every input is something this engine
// measured or something Phase 11B reported.
type QualityThresholds struct {
	// MinSignalRMS is the level below which audio is too quiet to be usable.
	MinSignalRMS float64

	// DegradedClipRatio and MaxClipRatio are the clipping bands. Above
	// MaxClipRatio the audio is unusable; between the two it is degraded.
	DegradedClipRatio float64
	MaxClipRatio      float64

	// DegradedSNRDB and MinSNRDB are the signal-to-noise bands, in dB.
	DegradedSNRDB float64
	MinSNRDB      float64

	// DegradedGapRatio and MaxGapRatio are frame-loss bands over the window.
	DegradedGapRatio float64
	MaxGapRatio      float64

	// MinDynamicRange is the peak-to-RMS spread, in dB, below which the signal
	// is suspiciously flat — a stuck codec, a constant tone, a dead line
	// carrying only its own noise.
	MinDynamicRange float64

	// WindowFrames is the classification window. Fixed size.
	WindowFrames int

	// HysteresisFrames is how many consecutive frames must agree before a new
	// class is adopted.
	//
	// A quality metric that flaps between Good and Degraded on alternate frames
	// is noise on a dashboard and pages nobody usefully.
	HysteresisFrames int
}

func (c QualityThresholds) validate() []string {
	var p []string
	if c.MinSignalRMS <= 0 {
		p = append(p, "quality: MinSignalRMS must be positive")
	}
	if c.DegradedClipRatio <= 0 || c.DegradedClipRatio > 1 {
		p = append(p, fmt.Sprintf("quality: DegradedClipRatio %g must be in (0,1]",
			c.DegradedClipRatio))
	}
	if c.MaxClipRatio <= c.DegradedClipRatio || c.MaxClipRatio > 1 {
		p = append(p, fmt.Sprintf("quality: MaxClipRatio (%g) must exceed "+
			"DegradedClipRatio (%g) and not exceed 1", c.MaxClipRatio, c.DegradedClipRatio))
	}
	if c.DegradedSNRDB <= c.MinSNRDB {
		p = append(p, fmt.Sprintf("quality: DegradedSNRDB (%g) must exceed MinSNRDB "+
			"(%g) — degraded is the better of the two bands", c.DegradedSNRDB, c.MinSNRDB))
	}
	if c.DegradedGapRatio <= 0 || c.DegradedGapRatio > 1 {
		p = append(p, fmt.Sprintf("quality: DegradedGapRatio %g must be in (0,1]",
			c.DegradedGapRatio))
	}
	if c.MaxGapRatio <= c.DegradedGapRatio || c.MaxGapRatio > 1 {
		p = append(p, fmt.Sprintf("quality: MaxGapRatio (%g) must exceed "+
			"DegradedGapRatio (%g) and not exceed 1", c.MaxGapRatio, c.DegradedGapRatio))
	}
	if c.MinDynamicRange < 0 {
		p = append(p, "quality: MinDynamicRange must not be negative")
	}
	if c.WindowFrames <= 0 {
		p = append(p, "quality: WindowFrames must be positive")
	}
	if c.HysteresisFrames < 1 {
		p = append(p, "quality: HysteresisFrames must be at least 1")
	}
	return p
}

// ---------------------------------------------------------------------------
// Continuity
// ---------------------------------------------------------------------------

// ContinuityConfig configures frame continuity detection.
//
// This engine CONSUMES Phase 11B's signals — sequence numbers, media
// timestamps, FlagSilence and FlagDiscontinuity, and the optional
// media.PipelineResult — and classifies them. It re-implements none of 11B's
// buffering, jitter handling or gap filling.
type ContinuityConfig struct {
	// MaxTimestampJump is the media-time step beyond which a frame's timestamp
	// is a discontinuity rather than a gap.
	MaxTimestampJump time.Duration

	// WindowFrames is the window over which gap ratio is measured.
	WindowFrames int

	// RestoreFrames is how many consecutive clean frames declare continuity
	// restored.
	RestoreFrames int
}

func (c ContinuityConfig) validate() []string {
	var p []string
	if c.MaxTimestampJump <= 0 {
		p = append(p, "continuity: MaxTimestampJump must be positive")
	}
	if c.WindowFrames <= 0 {
		p = append(p, "continuity: WindowFrames must be positive")
	}
	if c.RestoreFrames < 1 {
		p = append(p, "continuity: RestoreFrames must be at least 1")
	}
	return p
}

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

// Config is the complete, immutable configuration of an audio intelligence
// runtime.
//
// Validated once at construction. Copied by value into every session, so a
// session's configuration cannot be mutated underneath it by a concurrent
// reconfiguration that this package deliberately does not offer.
type Config struct {
	// Format is the audio analysed. Mono PCM only — see [validateAnalysisFormat].
	Format media.AudioFormat

	// FrameInterval is the expected frame cadence, used to convert every
	// duration threshold into a frame count once at construction rather than on
	// every frame.
	FrameInterval time.Duration

	// MaxSessions bounds concurrent sessions. Zero means unbounded, which is
	// refused: an unbounded session registry in a long-running process is a
	// memory leak that presents as a slow crash three days later.
	MaxSessions int

	// SignalWindowFrames is the bounded feature history every analyser shares.
	SignalWindowFrames int

	Noise      NoiseConfig
	VAD        VADConfig
	Silence    SilenceConfig
	Endpoint   EndpointPolicy
	BargeIn    BargeInPolicy
	Overlap    OverlapPolicy
	Quality    QualityThresholds
	Continuity ContinuityConfig
}

// DefaultConfig returns a telephony-shaped configuration for the given format.
//
// Every number is justified in docs/audio-intelligence/VAD_ARCHITECTURE.md and
// NOISE_AND_QUALITY.md. The endpoint window is the ADR-0011 figure and is the
// only value here taken from a frozen document rather than chosen for this
// phase.
func DefaultConfig(format media.AudioFormat) Config {
	return Config{
		Format:             format,
		FrameInterval:      defaultFrameInterval,
		MaxSessions:        defaultMaxSessions,
		SignalWindowFrames: defaultSignalWindowFrames,

		Noise: NoiseConfig{
			WarmupFrames:        defaultNoiseWarmupFrames,
			WindowFrames:        defaultNoiseWindowFrames,
			RiseAlpha:           defaultNoiseRiseAlpha,
			FallAlpha:           defaultNoiseFallAlpha,
			MaxRiseDBPerSecond:  defaultNoiseMaxRiseDBPerSecond,
			MinFloor:            defaultNoiseMinFloor,
			MaxFloor:            defaultNoiseMaxFloor,
			ConfidenceFrames:    defaultNoiseConfidenceFrames,
			QuietFloor:          defaultNoiseQuietFloor,
			TransientModulation: defaultNoiseTransientModulation,
		},

		VAD: VADConfig{
			OnsetThresholdDB:       defaultOnsetThresholdDB,
			ReleaseThresholdDB:     defaultReleaseThresholdDB,
			MinOnsetFrames:         defaultMinOnsetFrames,
			MinSilence:             defaultMinSilence,
			MinSpeech:              defaultMinSpeech,
			ZCRMin:                 defaultZCRMin,
			ZCRMax:                 defaultZCRMax,
			ModulationWindowFrames: defaultModulationWindowFrames,
			ProfileGraceFrames:     defaultProfileGraceFrames,
			MinEnergyModulation:    defaultMinEnergyModulation,
			AbsoluteSilenceRMS:     defaultAbsoluteSilenceRMS,
			NoiseHoldFrames:        defaultNoiseHoldFrames,
		},

		Silence: SilenceConfig{
			InterWordMax:     defaultInterWordMax,
			InterSentenceMax: defaultInterSentenceMax,
			LongSilenceMin:   defaultLongSilenceMin,
			ThinkingMin:      defaultThinkingMin,
		},

		Endpoint: EndpointPolicy{
			SilenceWindow:              DefaultEndpointSilenceWindow,
			MinSpeechDuration:          defaultEndpointMinSpeech,
			MaxTurnDuration:            defaultMaxTurnDuration,
			RequireFallingEnergy:       false,
			EnergyTrendTolerance:       defaultEnergyTrendTolerance,
			SuppressWhileAgentSpeaking: true,
			SuppressDuringBargeIn:      true,
		},

		BargeIn: BargeInPolicy{
			Enabled:              true,
			MinInterval:          defaultBargeInMinInterval,
			MaxAge:               defaultBargeInMaxAge,
			ConfirmFrames:        0,
			RequireAgentSpeaking: true,
		},

		Overlap: OverlapPolicy{
			Enabled:                true,
			MinDuration:            defaultOverlapMinDuration,
			ResolveAfter:           defaultOverlapResolveAfter,
			EchoCorrelationPenalty: defaultEchoCorrelationPenalty,
			MinConfidence:          defaultOverlapMinConfidence,
		},

		Quality: QualityThresholds{
			MinSignalRMS:      defaultMinSignalRMS,
			DegradedClipRatio: defaultDegradedClipRatio,
			MaxClipRatio:      defaultMaxClipRatio,
			DegradedSNRDB:     defaultDegradedSNRDB,
			MinSNRDB:          defaultMinSNRDB,
			DegradedGapRatio:  defaultDegradedGapRatio,
			MaxGapRatio:       defaultMaxGapRatio,
			MinDynamicRange:   defaultMinDynamicRange,
			WindowFrames:      defaultQualityWindowFrames,
			HysteresisFrames:  defaultQualityHysteresisFrames,
		},

		Continuity: ContinuityConfig{
			MaxTimestampJump: defaultMaxTimestampJump,
			WindowFrames:     defaultContinuityWindowFrames,
			RestoreFrames:    defaultContinuityRestoreFrames,
		},
	}
}

// Validate checks the whole configuration, reporting every problem.
func (c Config) Validate() error {
	if problems := c.validate(); len(problems) > 0 {
		return &ConfigError{Problems: problems}
	}
	return nil
}

func (c Config) validate() []string {
	var p []string

	if err := validateAnalysisFormat(c.Format); err != nil {
		p = append(p, err.Error())
	}
	if c.FrameInterval <= 0 {
		p = append(p, "config: FrameInterval must be positive; every duration threshold "+
			"is converted to frames against it")
	}
	if c.MaxSessions <= 0 {
		p = append(p, "config: MaxSessions must be positive; an unbounded session "+
			"registry is a memory leak that presents as a slow crash days later")
	}
	if c.SignalWindowFrames <= 0 {
		p = append(p, "config: SignalWindowFrames must be positive")
	}

	p = append(p, c.Noise.validate()...)
	p = append(p, c.VAD.validate(c.FrameInterval)...)
	p = append(p, c.Silence.validate()...)
	p = append(p, c.Endpoint.validate(c.FrameInterval)...)
	p = append(p, c.BargeIn.validate()...)
	p = append(p, c.Overlap.validate()...)
	p = append(p, c.Quality.validate()...)
	p = append(p, c.Continuity.validate()...)

	// Cross-section invariants. Each of these is a pair of individually valid
	// settings that contradict each other, which is exactly the class of
	// mistake per-section validation cannot catch.

	if c.Endpoint.SilenceWindow > 0 && c.Silence.InterSentenceMax > c.Endpoint.SilenceWindow {
		p = append(p, fmt.Sprintf("config: Silence.InterSentenceMax (%s) exceeds "+
			"Endpoint.SilenceWindow (%s) — a pause longer than the endpoint window IS "+
			"an endpoint, and two names for one event make the signal ambiguous",
			c.Silence.InterSentenceMax, c.Endpoint.SilenceWindow))
	}
	if c.Silence.LongSilenceMin > 0 && c.Endpoint.SilenceWindow >= c.Silence.LongSilenceMin {
		p = append(p, fmt.Sprintf("config: Endpoint.SilenceWindow (%s) must be below "+
			"Silence.LongSilenceMin (%s), or every endpoint is also a long silence",
			c.Endpoint.SilenceWindow, c.Silence.LongSilenceMin))
	}
	if c.VAD.MinSilence > c.Endpoint.SilenceWindow && c.Endpoint.SilenceWindow > 0 {
		p = append(p, fmt.Sprintf("config: VAD.MinSilence (%s) exceeds "+
			"Endpoint.SilenceWindow (%s) — the endpoint would confirm before the VAD "+
			"has decided speech ended, so no endpoint could ever carry a speech duration",
			c.VAD.MinSilence, c.Endpoint.SilenceWindow))
	}
	if c.VAD.AbsoluteSilenceRMS > 0 && c.Noise.MinFloor > c.VAD.AbsoluteSilenceRMS {
		p = append(p, fmt.Sprintf("config: Noise.MinFloor (%g) exceeds "+
			"VAD.AbsoluteSilenceRMS (%g) — the floor would sit above the level at "+
			"which speech is possible at all, and nothing would ever be detected",
			c.Noise.MinFloor, c.VAD.AbsoluteSilenceRMS))
	}
	if c.Quality.WindowFrames > c.SignalWindowFrames {
		p = append(p, fmt.Sprintf("config: Quality.WindowFrames (%d) exceeds "+
			"SignalWindowFrames (%d); the quality window cannot see more history than "+
			"the signal window retains", c.Quality.WindowFrames, c.SignalWindowFrames))
	}

	return p
}

// frames converts a duration to a frame count at this configuration's cadence,
// rounding UP.
//
// Rounding up on purpose. A hangover of 200 ms at a 30 ms cadence is 6.67
// frames; rounding down to 6 makes the effective hangover 180 ms, which is
// shorter than the configured value and silently changes behaviour on any
// cadence that does not divide evenly. Every threshold in this engine is a
// MINIMUM, so overshooting is correct and undershooting is a bug.
func (c Config) frames(d time.Duration) int {
	if d <= 0 || c.FrameInterval <= 0 {
		return 0
	}
	n := int((d + c.FrameInterval - 1) / c.FrameInterval)
	if n < 1 {
		n = 1
	}
	return n
}
