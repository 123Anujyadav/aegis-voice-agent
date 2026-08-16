package voice

// ---------------------------------------------------------------------------
// The complete label vocabulary of this module
// ---------------------------------------------------------------------------
//
// Every value that can reach a metric label, an event reason or a log field is
// declared here. Nothing else may.
//
// WHY ONE FILE. "Are our metric labels bounded" has to be answerable during a
// review, and answering it by reading eleven files means it gets answered
// "probably". Here it is one file, and TestMetrics_LabelVocabularyIsBounded
// checks it mechanically.
//
// The hazard is sharper in this module than in the ones below it, because this
// is the layer that touches a recogniser's output and a model's output. Either
// could put a caller's words into a metric label if anything here took a
// provider's string and used it as one. Nothing does.

// ProviderKind is which port a provider implements.
type ProviderKind string

// The provider kinds.
const (
	// KindSTT is recognition — speech.STTProvider.
	KindSTT ProviderKind = "stt"
	// KindTTS is synthesis — speech.TTSProvider.
	KindTTS ProviderKind = "tts"
	// KindModel is generation — runtime.Provider.
	KindModel ProviderKind = "model"
)

// AllProviderKinds returns every kind.
func AllProviderKinds() []ProviderKind { return []ProviderKind{KindSTT, KindTTS, KindModel} }

// Valid reports whether the kind is declared.
func (k ProviderKind) Valid() bool {
	return k == KindSTT || k == KindTTS || k == KindModel
}

// String implements fmt.Stringer.
func (k ProviderKind) String() string { return string(k) }

// ---------------------------------------------------------------------------

// TurnOutcome is how a voice turn ended.
type TurnOutcome string

// The turn outcomes.
const (
	// OutcomeCompleted is a turn that ran to the end and produced audio.
	OutcomeCompleted TurnOutcome = "completed"

	// OutcomeInterrupted is a turn the caller spoke over. NOT a failure — it is
	// the system working, and counting it as an error would make a responsive
	// agent look broken.
	OutcomeInterrupted TurnOutcome = "interrupted"

	// OutcomeCancelled is a turn ended by request.
	OutcomeCancelled TurnOutcome = "cancelled"

	// OutcomeDenied is a turn governance refused. Also not a failure: something
	// asked to do what it may not, and was stopped.
	OutcomeDenied TurnOutcome = "denied"

	// OutcomeFailed is a turn that ended on a fault.
	OutcomeFailed TurnOutcome = "failed"

	// OutcomeTimeout is a turn that exceeded TurnTimeout.
	OutcomeTimeout TurnOutcome = "timeout"
)

// AllTurnOutcomes returns every outcome.
func AllTurnOutcomes() []TurnOutcome {
	return []TurnOutcome{
		OutcomeCompleted, OutcomeInterrupted, OutcomeCancelled,
		OutcomeDenied, OutcomeFailed, OutcomeTimeout,
	}
}

// String implements fmt.Stringer.
func (o TurnOutcome) String() string { return string(o) }

// Successful reports whether the outcome represents the system working.
//
// Completed, interrupted and denied all do. Lumping a refusal or a barge-in in
// with a crash would make the failure rate meaningless — and a barge-in is
// arguably the system working at its best.
func (o TurnOutcome) Successful() bool {
	switch o {
	case OutcomeCompleted, OutcomeInterrupted, OutcomeDenied, OutcomeCancelled:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------

// PipelineStage names a point in the voice loop, for backpressure and drop
// accounting.
type PipelineStage string

// The pipeline stages.
const (
	StageAudioIn    PipelineStage = "audio_in"
	StageRecognise  PipelineStage = "recognise"
	StagePlan       PipelineStage = "plan"
	StageGenerate   PipelineStage = "generate"
	StageChunk      PipelineStage = "chunk"
	StageSynthesise PipelineStage = "synthesise"
	StageAudioOut   PipelineStage = "audio_out"
)

// AllPipelineStages returns every stage, in flow order.
func AllPipelineStages() []PipelineStage {
	return []PipelineStage{
		StageAudioIn, StageRecognise, StagePlan, StageGenerate,
		StageChunk, StageSynthesise, StageAudioOut,
	}
}

// String implements fmt.Stringer.
func (s PipelineStage) String() string { return string(s) }

// ---------------------------------------------------------------------------

// Reason codes. Every one is a literal declared here, so no provider-supplied
// or caller-supplied string ever reaches a label.
const (
	// Session lifecycle.
	ReasonRequested      = "requested"
	ReasonRuntimeStopped = "runtime_stopped"
	ReasonCallEnded      = "call_ended"

	// Provider outcomes.
	ReasonOK            = "ok"
	ReasonUnavailable   = "unavailable"
	ReasonTimeout       = "timeout"
	ReasonCrashed       = "crashed"
	ReasonInvalidOutput = "invalid_output"
	ReasonCancelled     = "cancelled"
	ReasonRestarted     = "restarted"
	ReasonStartFailed   = "start_failed"
	ReasonStopFailed    = "stop_failed"
	ReasonExitNonZero   = "exit_non_zero"

	// Turn outcomes.
	ReasonBargeIn           = "barge_in"
	ReasonGovernanceDeny    = "governance_denied"
	ReasonTurnTimeout       = "turn_timeout"
	ReasonEmptyTranscript   = "empty_transcript"
	ReasonTranscriptTooLong = "transcript_too_long"
	ReasonResponseTooLong   = "response_too_long"

	// Governance outcomes.
	ReasonAllowed = "allowed"
	ReasonDenied  = "denied"

	// Flow control.
	ReasonQueueFull  = "queue_full"
	ReasonStaleChunk = "stale_chunk"
)

// allReasonCodes is every declared reason, for the vocabulary test.
func allReasonCodes() []string {
	return []string{
		ReasonRequested, ReasonRuntimeStopped, ReasonCallEnded,
		ReasonOK, ReasonUnavailable, ReasonTimeout, ReasonCrashed,
		ReasonInvalidOutput, ReasonCancelled, ReasonRestarted,
		ReasonStartFailed, ReasonStopFailed, ReasonExitNonZero,
		ReasonBargeIn, ReasonGovernanceDeny, ReasonTurnTimeout,
		ReasonEmptyTranscript, ReasonTranscriptTooLong, ReasonResponseTooLong,
		ReasonAllowed, ReasonDenied,
		ReasonQueueFull, ReasonStaleChunk,
	}
}
