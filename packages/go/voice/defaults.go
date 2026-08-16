package voice

import (
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
)

// ---------------------------------------------------------------------------
// Every default in this module, named, with its reasoning
// ---------------------------------------------------------------------------
//
// These are DEFAULTS, not constants of nature. Each is a starting point a
// deployment tunes from, and each is here rather than inline so "what would we
// change" is answerable by reading one file.
//
// NOTHING HERE NAMES A MODEL OR A BINARY. Paths and model identifiers are the
// operator's; a default path would be wrong on every machine but one, and a
// default model name would read as an endorsement ADR-0006 has not made.

// Frame cadence, capacity and turn bounds.
const (
	// defaultFrameInterval matches Phase 11B's DefaultPipelineConfig.
	defaultFrameInterval = 20 * time.Millisecond

	// defaultMaxSessions is deliberately far below Phase 11D's 1024. A voice
	// session holds a recognition process and a synthesis process; the binding
	// constraint is local CPU and memory, not bookkeeping.
	defaultMaxSessions = 32

	// defaultTurnTimeout bounds one whole turn. Generous, because a local model
	// on a CPU is slow and cutting a turn off mid-sentence is worse than
	// waiting — but finite, because a stuck provider must not hold a call open
	// forever.
	defaultTurnTimeout = 60 * time.Second

	// defaultMaxTranscriptChars bounds one transcript. Long enough for any
	// plausible utterance, short enough that a malfunctioning recogniser
	// emitting a wall of text is caught here rather than in a token bill.
	defaultMaxTranscriptChars = 4096

	// defaultMaxResponseChars bounds one generated response.
	defaultMaxResponseChars = 8192
)

// Process supervision.
const (
	// defaultStartTimeout is how long a provider may take to become ready.
	// A model load from cold disk is slow; ten seconds covers it.
	defaultStartTimeout = 10 * time.Second

	// defaultStopTimeout is the grace period before a child is killed.
	// Two seconds is long enough to flush and exit, short enough that shutdown
	// does not feel hung.
	defaultStopTimeout = 2 * time.Second

	// defaultMaxStderrBytes retains the most recent 64 KiB of a provider's
	// stderr — enough to hold a stack trace or a model-load error, bounded so a
	// chatty provider cannot grow it for the length of a call.
	defaultMaxStderrBytes = 64 << 10

	// defaultMaxRestarts allows two automatic restarts. A provider that has
	// died three times is not going to work on the fourth attempt, and
	// restarting forever turns a broken install into a busy loop.
	defaultMaxRestarts = 2

	// defaultRestartBackoff is the delay before the first restart, doubling
	// after that.
	defaultRestartBackoff = 500 * time.Millisecond
)

// Recognition.
const (
	// defaultSTTResultTimeout bounds the wait for a result after audio ends.
	// A batch recogniser on a CPU can take seconds on a short utterance.
	defaultSTTResultTimeout = 30 * time.Second

	// defaultSTTMaxPendingFrames is ten seconds of audio at the default
	// cadence. A recogniser slower than real time will hit this, which is the
	// point: backpressure is reported rather than absorbed silently.
	defaultSTTMaxPendingFrames = 500
)

// Synthesis.
const (
	// defaultTTSChunkTimeout bounds synthesis of one text chunk.
	defaultTTSChunkTimeout = 20 * time.Second

	// defaultTTSMaxPendingChunks bounds queued text. A response is chunked into
	// clauses; sixteen is several sentences ahead of the speaker.
	defaultTTSMaxPendingChunks = 16

	// defaultTTSMaxPendingFrames is ten seconds of synthesised audio waiting to
	// go out.
	defaultTTSMaxPendingFrames = 500

	// defaultTTSSpeed is the model's natural rate.
	defaultTTSSpeed = 1.0

	// defaultTTSSpeakerID means the model's own default voice.
	defaultTTSSpeakerID = -1
)

// Local language model.
const (
	// defaultModelEndpoint is the conventional local daemon address.
	//
	// A LOOPBACK ADDRESS, NOT A SERVICE. Naming it here is not an endorsement
	// of any particular daemon; it is the port the local-first convention uses,
	// and it is overridable.
	defaultModelEndpoint = "http://127.0.0.1:11434"

	// defaultModelRequestTimeout bounds a whole generation. Long, because a
	// 12-billion-parameter model on a laptop CPU is slow.
	defaultModelRequestTimeout = 120 * time.Second

	// defaultModelFirstTokenTimeout bounds the wait for the first token.
	//
	// A TIMEOUT, NOT A BUDGET, AND THE DISTINCTION MATTERS. ADR-0011 §5.2 hop 6
	// budgets time-to-first-token at 250 ms p50 / 550 ms p95 — for
	// claude-sonnet-5 at effort "low", over a network. A local model on a
	// developer's CPU will not come close, is not held to it, and this phase
	// creates no target of its own.
	//
	// Thirty seconds is a liveness check on a daemon that has stopped
	// responding, nothing more.
	defaultModelFirstTokenTimeout = 30 * time.Second

	// defaultModelMaxOutputTokens bounds a response.
	defaultModelMaxOutputTokens = 512

	// defaultModelMaxPendingChunks bounds the token stream's buffer.
	defaultModelMaxPendingChunks = 256
)

// FrozenBargeInBudget is the abort budget every layer of this platform shares.
//
// FROZEN, NOT CHOSEN HERE. ADR-0004 §12 and ADR-0011 §5.1 fix barge-in at one
// frame interval, and runtime.Provider's own contract restates it: a provider
// that ignores cancellation cannot meet it. Phase 11D measures detection
// against it; this phase measures cancellation against it.
//
// Exported so a benchmark compares against the document rather than a number
// somebody retyped.
const FrozenBargeInBudget = 20 * time.Millisecond

// ReferenceFirstTokenP50 and ReferenceFirstTokenP95 are ADR-0011 hop 6.
//
// # A REFERENCE, NOT A TARGET FOR THIS PHASE
//
// ADR-0011 §5.2 hop 6 and ADR-0006 C1 budget LLM time-to-first-token at 250 ms
// p50 / 550 ms p95. That budget was set for claude-sonnet-5 at effort "low"
// reached over a network, and it is owned by ai-orchestrator.
//
// A local open-weight model running on a developer's CPU is a different system
// entirely. Its first-token latency is measured and reported AGAINST this
// reference so a reader has something to scale by — never asserted, never
// presented as compliance, and never replaced with a softer number that this
// phase would then have invented.
//
// ADR-0006 additionally rejected self-hosted open-weight models for production;
// nothing here amends that. See docs/voice/LLM_PROVIDER_ADAPTER.md.
const (
	ReferenceFirstTokenP50 = 250 * time.Millisecond
	ReferenceFirstTokenP95 = 550 * time.Millisecond
)

// DefaultConfig returns a configuration with every provider DISABLED.
//
// # Disabled, deliberately
//
// There is no correct default path to a whisper binary or a Piper voice, and a
// guessed one would be wrong on every machine but the author's. A runtime built
// from this starts cleanly and does nothing, which is the honest behaviour for
// "no provider has been configured yet".
//
// An operator enables what they have. [DefaultProcessConfig],
// [DefaultSTTConfig], [DefaultTTSConfig] and [DefaultModelConfig] supply
// everything except the paths only they can know.
func DefaultConfig(format media.AudioFormat) Config {
	return Config{
		Format:             format,
		FrameInterval:      defaultFrameInterval,
		MaxSessions:        defaultMaxSessions,
		TurnTimeout:        defaultTurnTimeout,
		MaxTranscriptChars: defaultMaxTranscriptChars,
		MaxResponseChars:   defaultMaxResponseChars,
		STT:                STTProviderConfig{Enabled: false},
		TTS:                TTSProviderConfig{Enabled: false},
		Model:              ModelProviderConfig{Enabled: false},
	}
}

// DefaultProcessConfig returns supervision settings for a local provider.
//
// The executable is the caller's to supply; everything else has a defensible
// default.
func DefaultProcessConfig(executable string, args ...string) ProcessConfig {
	return ProcessConfig{
		Executable:      executable,
		Args:            args,
		InheritPathVars: true,
		StartTimeout:    defaultStartTimeout,
		StopTimeout:     defaultStopTimeout,
		MaxStderrBytes:  defaultMaxStderrBytes,
		MaxRestarts:     defaultMaxRestarts,
		RestartBackoff:  defaultRestartBackoff,
	}
}

// DefaultSTTConfig returns recognition settings for a given binary, model,
// language and rate.
func DefaultSTTConfig(
	id ProviderID, executable, modelPath, language string, rate media.SampleRate,
) STTProviderConfig {
	return STTProviderConfig{
		Enabled:          true,
		ID:               id,
		Process:          DefaultProcessConfig(executable),
		ModelPath:        modelPath,
		Language:         language,
		SampleRate:       rate,
		ResultTimeout:    defaultSTTResultTimeout,
		MaxPendingFrames: defaultSTTMaxPendingFrames,
	}
}

// DefaultTTSConfig returns synthesis settings for a given binary, voice model,
// language and rate.
func DefaultTTSConfig(
	id ProviderID, executable, modelPath, language string, rate media.SampleRate,
) TTSProviderConfig {
	return TTSProviderConfig{
		Enabled:          true,
		ID:               id,
		Process:          DefaultProcessConfig(executable),
		ModelPath:        modelPath,
		SpeakerID:        defaultTTSSpeakerID,
		Language:         language,
		Speed:            defaultTTSSpeed,
		SampleRate:       rate,
		ChunkTimeout:     defaultTTSChunkTimeout,
		MaxPendingChunks: defaultTTSMaxPendingChunks,
		MaxPendingFrames: defaultTTSMaxPendingFrames,
	}
}

// DefaultModelConfig returns local model settings.
//
// The model identifier is required and has no default — see
// [ModelProviderConfig].
func DefaultModelConfig(id ProviderID, model ModelID) ModelProviderConfig {
	return ModelProviderConfig{
		Enabled:           true,
		ID:                id,
		Endpoint:          defaultModelEndpoint,
		Model:             model,
		RequestTimeout:    defaultModelRequestTimeout,
		FirstTokenTimeout: defaultModelFirstTokenTimeout,
		MaxOutputTokens:   defaultModelMaxOutputTokens,
		MaxPendingChunks:  defaultModelMaxPendingChunks,
	}
}
