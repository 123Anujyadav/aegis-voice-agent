package speech

import (
	"context"
	"fmt"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
)

// Capabilities is bounded provider metadata used for routing.
//
// # This is the ONLY thing routing may consult
//
// A router that asked "is this Deepgram?" would embed a vendor name in the core
// and make replacement a code change. A router that asks "does this provider
// stream, and does it declare hi-IN?" works for a provider nobody has written
// yet.
type Capabilities struct {
	// Languages the provider declares. Declaring a language is not a quality
	// claim — see docs/speech/PROVIDER_ROUTING.md.
	Languages []Language

	// Streaming reports whether audio may be sent incrementally.
	Streaming bool

	// PartialResults reports whether interim transcripts are emitted.
	PartialResults bool

	// SampleRates the provider accepts.
	SampleRates []media.SampleRate

	// MaxSessionAudio bounds one stream's audio; zero means unbounded.
	MaxSessionAudio time.Duration
}

// Supports reports whether the provider declares a language.
func (c Capabilities) Supports(l Language) bool {
	for _, got := range c.Languages {
		if got == l {
			return true
		}
	}
	return false
}

// SupportsRate reports whether the provider accepts a sample rate.
func (c Capabilities) SupportsRate(r media.SampleRate) bool {
	if len(c.SampleRates) == 0 {
		return true // undeclared means unconstrained
	}
	for _, got := range c.SampleRates {
		if got == r {
			return true
		}
	}
	return false
}

// STTConfig configures one recognition stream.
//
// # Every field is provider-neutral
//
// There is no request struct, no JSON blob, no map[string]any and no API key.
// A provider adapter translates this into whatever its vendor wants; the vendor
// shape never travels back up.
type STTConfig struct {
	Session  SessionID
	Turn     TurnID
	Language Language
	Format   media.AudioFormat

	// Timeout bounds the whole stream. Zero means the caller's context governs.
	Timeout time.Duration
}

// Validate checks the configuration.
func (c STTConfig) Validate() error {
	var problems []string
	if !c.Session.Valid() {
		problems = append(problems, "stt: Session is not a valid identifier")
	}
	if !c.Turn.Valid() {
		problems = append(problems, "stt: Turn is not a valid identifier")
	}
	if err := c.Format.Validate(); err != nil {
		problems = append(problems, err.Error())
	}
	if c.Timeout < 0 {
		problems = append(problems, "stt: Timeout must not be negative")
	}
	if len(problems) > 0 {
		return &ConfigError{Problems: problems}
	}
	return nil
}

// VoiceID names a synthesis voice. Authored, bounded, never vendor output.
type VoiceID string

// Prosody carries neutral delivery hints. 1.0 is the provider's default.
//
// Three normalised scalars rather than SSML: SSML is a vendor-dialect swamp,
// and a core that passed it through would be passing a vendor schema.
type Prosody struct {
	Rate   float64
	Pitch  float64
	Volume float64
}

// DefaultProsody returns neutral delivery.
func DefaultProsody() Prosody { return Prosody{Rate: 1, Pitch: 1, Volume: 1} }

// Validate checks the prosody is within a sane range.
func (p Prosody) Validate() error {
	inRange := func(v float64) bool { return v >= 0.25 && v <= 4 }
	if !inRange(p.Rate) || !inRange(p.Pitch) || !inRange(p.Volume) {
		return fmt.Errorf("%w: prosody %v is outside 0.25..4", ErrInvalidTranscript, p)
	}
	return nil
}

// TTSConfig configures one synthesis stream.
type TTSConfig struct {
	Session  SessionID
	Turn     TurnID
	Language Language
	Format   media.AudioFormat
	Voice    VoiceID
	Prosody  Prosody

	// Timeout bounds the whole stream. Zero means the caller's context governs.
	Timeout time.Duration
}

// Validate checks the configuration.
func (c TTSConfig) Validate() error {
	var problems []string
	if !c.Session.Valid() {
		problems = append(problems, "tts: Session is not a valid identifier")
	}
	if !c.Turn.Valid() {
		problems = append(problems, "tts: Turn is not a valid identifier")
	}
	if err := c.Format.Validate(); err != nil {
		problems = append(problems, err.Error())
	}
	if err := c.Prosody.Validate(); err != nil {
		problems = append(problems, err.Error())
	}
	if c.Timeout < 0 {
		problems = append(problems, "tts: Timeout must not be negative")
	}
	if len(problems) > 0 {
		return &ConfigError{Problems: problems}
	}
	return nil
}

// STTProvider is the provider-neutral recognition contract.
//
// # No vendor appears here or anywhere below it
//
// Implementations for Google, Deepgram, Sarvam, Whisper and anything else live
// in adapter packages outside this module and are written against exactly these
// four types. A provider is replaceable without touching conversation logic,
// the telephony runtime, the media runtime, governance, memory or evaluation —
// which is checkable, because none of those appear in this package either.
type STTProvider interface {
	// ID returns the authored provider identifier.
	ID() ProviderID

	// Capabilities describes what the provider declares it can do.
	Capabilities() Capabilities

	// OpenSTT starts a streaming recognition session.
	OpenSTT(ctx context.Context, cfg STTConfig) (STTStream, error)
}

// STTStream is one live recognition stream.
type STTStream interface {
	// Write submits audio.
	//
	// MUST NOT BLOCK INDEFINITELY. A provider under pressure returns
	// ErrBackpressure; one that blocked would hold the media reader, and a
	// stalled media reader backs up into the carrier.
	Write(f media.Frame) error

	// Results yields partial and final segments. The provider closes it when
	// the stream ends, which is the signal that no further results will arrive.
	Results() <-chan TranscriptSegment

	// CloseSend signals end of audio. Finals may still arrive afterwards.
	CloseSend() error

	// Close abandons the stream and releases its resources. Idempotent.
	Close() error
}

// TTSProvider is the provider-neutral synthesis contract.
type TTSProvider interface {
	// ID returns the authored provider identifier.
	ID() ProviderID

	// Capabilities describes what the provider declares it can do.
	Capabilities() Capabilities

	// OpenTTS starts a streaming synthesis session.
	OpenTTS(ctx context.Context, cfg TTSConfig) (TTSStream, error)
}

// TTSStream is one live synthesis stream.
type TTSStream interface {
	// Synthesize submits one chunk of text.
	Synthesize(c Chunk) error

	// Audio yields synthesised frames. The provider closes it when synthesis
	// ends.
	//
	// FRAMES DELIVERED HERE ARE OWNED BY THE RECEIVER. Unlike media's ring
	// buffer, a provider hands over frames it will not touch again — an
	// adapter that reused a buffer would corrupt audio already queued for the
	// caller.
	Audio() <-chan media.Frame

	// CloseSend signals end of text. Audio may still arrive afterwards.
	CloseSend() error

	// Close abandons the stream and releases its resources. Idempotent.
	Close() error
}
