package audiointel

// ---------------------------------------------------------------------------
// The complete label and classification vocabulary of this engine
// ---------------------------------------------------------------------------
//
// Every value that can reach a metric label, an event Classification or an
// event Reason is declared here. Nothing else may.
//
// WHY ONE FILE. "Are our metric labels bounded" is a question somebody has to
// be able to answer during a review, and answering it by reading nine detectors
// means it gets answered "probably". Here it is answered by reading one file and
// checking that every constant is a literal — which is also what
// TestMetrics_LabelVocabularyIsBounded does mechanically.
//
// These strings reach Prometheus and Kafka, so all of them are lowercase
// alphanumerics and underscores. No hyphens: packages/go/eventbus prohibits
// them in topic segments and Prometheus normalises them away.

// Direction is which way the analysed audio flows.
//
// Mirrors media.StreamDirection rather than importing its values as labels, so
// a change to the media enum cannot silently change this engine's metric
// cardinality.
type Direction string

// The directions.
const (
	// DirectionInbound is audio from the caller. The direction voice activity,
	// endpointing and barge-in detection care about.
	DirectionInbound Direction = "inbound"

	// DirectionOutbound is synthesised audio toward the caller. Analysed only
	// for the outbound envelope that overlap detection uses to lower its own
	// confidence — never for voice activity, because the agent's speech is not
	// something to detect the onset of.
	DirectionOutbound Direction = "outbound"
)

// AllDirections returns every direction.
func AllDirections() []Direction { return []Direction{DirectionInbound, DirectionOutbound} }

// Valid reports whether the direction is declared.
func (d Direction) Valid() bool { return d == DirectionInbound || d == DirectionOutbound }

// String implements fmt.Stringer.
func (d Direction) String() string { return string(d) }

// ---------------------------------------------------------------------------

// VADState is one of the six voice activity states.
//
// A string rather than an integer. These values reach metric labels, event
// payloads and log lines; an integer enum renumbers itself the moment somebody
// inserts a constant in the middle.
type VADState string

// The six voice activity states, in lifecycle order.
const (
	// VADUncertain is the INITIAL state and it is honest: before the noise
	// floor has converged there is nothing to compare a frame against, so the
	// detector does not know. It refuses to assert speech here.
	VADUncertain VADState = "uncertain"

	// VADSilence is audio at or near the noise floor.
	VADSilence VADState = "silence"

	// VADCandidateSpeech is above-threshold energy that has not yet persisted
	// for MinOnsetFrames. A door slam gets this far and no further.
	VADCandidateSpeech VADState = "candidate_speech"

	// VADSpeech is confirmed speech.
	VADSpeech VADState = "speech"

	// VADCandidateSilence is sub-threshold audio during the hangover. Speech
	// has NOT ended here — a stop closure or an inter-word gap looks exactly
	// like this, and a detector that ended speech in this state would report a
	// new utterance for every syllable.
	VADCandidateSilence VADState = "candidate_silence"

	// VADNoise is sustained above-floor energy whose profile is not speech: a
	// fan, hold music, line hiss, an engine. Distinct from silence because a
	// noisy line is not a quiet one, and distinct from speech because it is
	// not.
	VADNoise VADState = "noise"
)

// AllVADStates returns every state in lifecycle order.
func AllVADStates() []VADState {
	return []VADState{
		VADUncertain, VADSilence, VADCandidateSpeech,
		VADSpeech, VADCandidateSilence, VADNoise,
	}
}

// String implements fmt.Stringer.
func (s VADState) String() string { return string(s) }

// Valid reports whether the state is one of the declared six.
func (s VADState) Valid() bool {
	for _, known := range AllVADStates() {
		if s == known {
			return true
		}
	}
	return false
}

// Active reports whether the state represents audio a recogniser should be
// receiving.
//
// Speech AND CandidateSilence. The hangover is deliberately included: the whole
// point of a hangover is that speech has not ended yet, and cutting the
// recogniser off during it would clip every trailing consonant.
func (s VADState) Active() bool { return s == VADSpeech || s == VADCandidateSilence }

// ---------------------------------------------------------------------------

// SilenceClass names a period of silence by its duration and position.
//
// # These are timing signals, not language understanding
//
// InterSentencePause is a name for a pause of a certain length in a certain
// position. It is NOT a determination that a sentence ended — acoustic silence
// carries no semantics, and this engine has no access to words. The names are
// operationally useful and semantically empty, and the distinction matters
// enough to repeat wherever they appear.
type SilenceClass string

// The six silence classes.
const (
	// SilenceInitial is silence before any speech in the session — the pause
	// after the call connects and before anybody says anything.
	SilenceInitial SilenceClass = "initial"

	// SilenceInterWord is a pause short enough to sit inside a phrase.
	SilenceInterWord SilenceClass = "inter_word"

	// SilenceInterSentence is a pause long enough to sit between clauses and
	// short enough not to be an endpoint.
	SilenceInterSentence SilenceClass = "inter_sentence"

	// SilenceThinking is silence after the agent stopped speaking with no
	// caller response yet. POSITIONAL: it says the caller has not started, not
	// that they are thinking.
	SilenceThinking SilenceClass = "thinking"

	// SilenceEndpoint is silence that has reached the endpoint window.
	SilenceEndpoint SilenceClass = "endpoint"

	// SilenceLong is silence past anything conversational — a dropped call, a
	// hold, a caller who walked away.
	SilenceLong SilenceClass = "long"
)

// AllSilenceClasses returns every class, shortest to longest.
func AllSilenceClasses() []SilenceClass {
	return []SilenceClass{
		SilenceInitial, SilenceInterWord, SilenceInterSentence,
		SilenceThinking, SilenceEndpoint, SilenceLong,
	}
}

// String implements fmt.Stringer.
func (c SilenceClass) String() string { return string(c) }

// ---------------------------------------------------------------------------

// QualityClass is a bounded judgement about whether the audio is usable.
type QualityClass string

// The four quality classes, best to worst.
const (
	// QualityGood is audio a recogniser should handle without difficulty.
	QualityGood QualityClass = "good"

	// QualityDegraded is usable audio with a measurable problem: some clipping,
	// a poor signal-to-noise ratio, occasional frame loss.
	QualityDegraded QualityClass = "degraded"

	// QualityPoor is audio a recogniser will struggle with.
	QualityPoor QualityClass = "poor"

	// QualityUnusable is audio no recogniser will do anything useful with:
	// destroyed by clipping, buried in noise, or arriving in pieces.
	QualityUnusable QualityClass = "unusable"

	// QualityUnknown is the state before enough audio has arrived to judge.
	//
	// NOT A FIFTH QUALITY LEVEL. It means "not measured yet", and reporting it
	// as Good would be a claim the engine has not earned.
	QualityUnknown QualityClass = "unknown"
)

// AllQualityClasses returns every class, best to worst, with unknown last.
func AllQualityClasses() []QualityClass {
	return []QualityClass{
		QualityGood, QualityDegraded, QualityPoor, QualityUnusable, QualityUnknown,
	}
}

// String implements fmt.Stringer.
func (c QualityClass) String() string { return string(c) }

// Rank orders the classes for comparison, best (0) to worst (3).
//
// Unknown ranks alongside Good deliberately: it must not trigger a degradation
// alert, because "we have not measured yet" is not a fault.
func (c QualityClass) Rank() int {
	switch c {
	case QualityGood, QualityUnknown:
		return 0
	case QualityDegraded:
		return 1
	case QualityPoor:
		return 2
	case QualityUnusable:
		return 3
	default:
		return 0
	}
}

// Usable reports whether a recogniser should be given this audio.
func (c QualityClass) Usable() bool { return c != QualityUnusable }

// ---------------------------------------------------------------------------

// OverlapState is one of the four double-talk states.
type OverlapState string

// The four overlap states.
const (
	// OverlapNone is one party or neither holding the floor.
	OverlapNone OverlapState = "none"

	// OverlapPossible is caller speech during agent speech that has not yet
	// persisted for MinDuration. A click, a handset bump or a codec transient
	// reaches this state and no further.
	OverlapPossible OverlapState = "possible"

	// OverlapConfirmed is sustained simultaneous speech.
	//
	// READ THE CONFIDENCE, NOT THE STATE. Without an echo canceller and a
	// sample-aligned outbound reference, echo and genuine double-talk are not
	// separable. See docs/audio-intelligence/OVERLAP_DETECTION.md.
	OverlapConfirmed OverlapState = "confirmed"

	// OverlapResolved is a confirmed overlap whose conditions have ceased. A
	// distinct state rather than a return to none, so a consumer can tell an
	// overlap that ended from one that never happened.
	OverlapResolved OverlapState = "resolved"
)

// AllOverlapStates returns every state in lifecycle order.
func AllOverlapStates() []OverlapState {
	return []OverlapState{OverlapNone, OverlapPossible, OverlapConfirmed, OverlapResolved}
}

// String implements fmt.Stringer.
func (s OverlapState) String() string { return string(s) }

// ---------------------------------------------------------------------------

// BargeInOutcome is what happened to a detected interruption.
//
// EVERY DETECTION PRODUCES EXACTLY ONE OUTCOME, including the ones deliberately
// not delivered. A detection that vanished without a counter is a barge-in
// nobody can explain the absence of, and "the agent talked over me" is the
// hardest complaint to investigate after the fact.
type BargeInOutcome string

// The barge-in outcomes.
const (
	// BargeInDelivered reached the speech controller and it accepted.
	BargeInDelivered BargeInOutcome = "delivered"

	// BargeInDebounced arrived within MinInterval of the previous one.
	BargeInDebounced BargeInOutcome = "debounced"

	// BargeInStale was older than MaxAge and was discarded. Cancelling speech
	// the agent already finished cuts off whatever it started next.
	BargeInStale BargeInOutcome = "stale"

	// BargeInNotSpeaking fired while the agent did not hold the floor. Mirrors
	// Phase 11C: a caller talking while we are listening is not interrupting.
	BargeInNotSpeaking BargeInOutcome = "not_speaking"

	// BargeInRefused reached the speech controller and it refused — most often
	// because the turn had already moved on.
	BargeInRefused BargeInOutcome = "refused"

	// BargeInNoController fired with no port wired. A configuration fault, and
	// counted rather than swallowed: a deployment that detects interruptions it
	// cannot act on looks healthy while talking over every caller.
	BargeInNoController BargeInOutcome = "no_controller"

	// BargeInDisabled fired with the policy switched off.
	BargeInDisabled BargeInOutcome = "disabled"
)

// AllBargeInOutcomes returns every outcome.
func AllBargeInOutcomes() []BargeInOutcome {
	return []BargeInOutcome{
		BargeInDelivered, BargeInDebounced, BargeInStale, BargeInNotSpeaking,
		BargeInRefused, BargeInNoController, BargeInDisabled,
	}
}

// String implements fmt.Stringer.
func (o BargeInOutcome) String() string { return string(o) }

// ---------------------------------------------------------------------------

// ContinuityFault names a transport problem observed in the frame stream.
//
// These are CONSUMED from Phase 11B, not re-derived. 11B owns the jitter
// buffer, the reordering window and the gap fill; this engine reads the
// sequence numbers, timestamps and flags it publishes and classifies what it
// sees.
type ContinuityFault string

// The continuity faults.
const (
	// FaultNone is a clean frame.
	FaultNone ContinuityFault = "none"

	// FaultMissing is one or more absent sequence numbers.
	FaultMissing ContinuityFault = "missing_sequence"

	// FaultDuplicate is a sequence number already seen.
	FaultDuplicate ContinuityFault = "duplicate"

	// FaultOutOfOrder is a sequence number below the highest seen.
	FaultOutOfOrder ContinuityFault = "out_of_order"

	// FaultTimestampJump is a media timestamp that moved further than
	// ContinuityConfig.MaxTimestampJump.
	FaultTimestampJump ContinuityFault = "timestamp_jump"

	// FaultTimestampReverse is a media timestamp that moved backwards. Distinct
	// from a jump because a backwards timeline is a different upstream bug from
	// a forwards leap, and merging them makes both harder to find.
	FaultTimestampReverse ContinuityFault = "timestamp_reverse"

	// FaultSynthesised is a frame Phase 11B invented to fill a gap, marked with
	// media.FlagSilence. Not a fault in the stream — it is 11B correctly
	// covering one — but it is not real audio either, and a detector that
	// treated invented silence as measured silence would endpoint on a network
	// glitch.
	FaultSynthesised ContinuityFault = "synthesised"

	// FaultStarvation is the source having produced nothing for long enough
	// that the pipeline ran dry.
	FaultStarvation ContinuityFault = "starvation"
)

// AllContinuityFaults returns every fault.
func AllContinuityFaults() []ContinuityFault {
	return []ContinuityFault{
		FaultNone, FaultMissing, FaultDuplicate, FaultOutOfOrder,
		FaultTimestampJump, FaultTimestampReverse, FaultSynthesised,
		FaultStarvation,
	}
}

// String implements fmt.Stringer.
func (f ContinuityFault) String() string { return string(f) }

// Degrades reports whether a fault should count against audio quality.
//
// FaultNone obviously does not. FaultSynthesised does: 11B covering a gap means
// there WAS a gap, and the covering silence is not audio the caller produced.
func (f ContinuityFault) Degrades() bool { return f != FaultNone }

// ---------------------------------------------------------------------------

// NoiseClass characterises the background the caller is speaking against.
//
// Confidence-based and deliberately coarse. This engine does not perform source
// separation and does not claim to.
type NoiseClass string

// The noise classes.
const (
	// NoiseQuiet is a background at or near the practical silence floor.
	NoiseQuiet NoiseClass = "quiet"

	// NoiseStationary is a steady background: a fan, an engine, line hiss.
	// Characterised by low energy modulation.
	NoiseStationary NoiseClass = "stationary"

	// NoiseTransient is a background dominated by intermittent bursts: traffic,
	// a busy room, doors.
	NoiseTransient NoiseClass = "transient"

	// NoiseClipping is an input being driven past full scale.
	NoiseClipping NoiseClass = "clipping"

	// NoiseVeryLow is an input so quiet that speech may be below the practical
	// detection floor — a muted handset, a distant speaker, a broken gain
	// stage.
	NoiseVeryLow NoiseClass = "very_low"

	// NoiseUnknown is the state before the floor estimate has converged.
	NoiseUnknown NoiseClass = "unknown"
)

// AllNoiseClasses returns every class.
func AllNoiseClasses() []NoiseClass {
	return []NoiseClass{
		NoiseQuiet, NoiseStationary, NoiseTransient, NoiseClipping,
		NoiseVeryLow, NoiseUnknown,
	}
}

// String implements fmt.Stringer.
func (c NoiseClass) String() string { return string(c) }

// ---------------------------------------------------------------------------

// Reason codes. Every one is a literal declared here, so no caller-supplied or
// input-derived string ever reaches a label.
const (
	// False-trigger reasons — why a candidate onset did not become speech.
	ReasonOnsetTooShort  = "onset_too_short"
	ReasonNonSpeechZCR   = "non_speech_zcr"
	ReasonStationary     = "stationary_energy"
	ReasonBelowFloor     = "below_absolute_floor"
	ReasonRunTooShort    = "run_shorter_than_min_speech"
	ReasonFloorUncertain = "noise_floor_uncertain"

	// Endpoint suppression reasons.
	ReasonAgentSpeaking    = "agent_speaking"
	ReasonBargeInActive    = "barge_in_active"
	ReasonSpeechTooShort   = "speech_shorter_than_minimum"
	ReasonEnergyRising     = "energy_still_rising"
	ReasonWindowNotElapsed = "silence_window_not_elapsed"

	// Endpoint confirmation reasons.
	ReasonSilenceWindow = "silence_window_elapsed"
	ReasonMaxTurn       = "max_turn_duration"

	// Frame refusal reasons.
	ReasonFormatMismatch = "format_mismatch"
	ReasonEmptyFrame     = "empty_frame"
	ReasonSessionClosed  = "session_closed"

	// Degradation and recovery reasons.
	ReasonClipping    = "clipping"
	ReasonLowSNR      = "low_snr"
	ReasonLowLevel    = "low_level"
	ReasonFrameLoss   = "frame_loss"
	ReasonFlatSignal  = "flat_signal"
	ReasonQualityRose = "quality_improved"

	// Session lifecycle reasons.
	ReasonClosedByCaller = "closed"
	ReasonRuntimeStopped = "runtime_stopped"

	// Barge-in and speech reasons.
	ReasonCallerSpoke     = "caller_spoke"
	ReasonOnsetConfirmed  = "onset_confirmed"
	ReasonHangoverElapsed = "hangover_elapsed"
)

// allReasonCodes is every declared reason, for the vocabulary test.
//
// Kept in sync by TestMetrics_LabelVocabularyIsBounded, which fails if a reason
// reaches a metric without appearing here.
func allReasonCodes() []string {
	return []string{
		ReasonOnsetTooShort, ReasonNonSpeechZCR, ReasonStationary,
		ReasonBelowFloor, ReasonRunTooShort, ReasonFloorUncertain,
		ReasonAgentSpeaking, ReasonBargeInActive, ReasonSpeechTooShort,
		ReasonEnergyRising, ReasonWindowNotElapsed,
		ReasonSilenceWindow, ReasonMaxTurn,
		ReasonFormatMismatch, ReasonEmptyFrame, ReasonSessionClosed,
		ReasonClipping, ReasonLowSNR, ReasonLowLevel, ReasonFrameLoss,
		ReasonFlatSignal, ReasonQualityRose,
		ReasonClosedByCaller, ReasonRuntimeStopped,
		ReasonCallerSpoke, ReasonOnsetConfirmed, ReasonHangoverElapsed,
	}
}
