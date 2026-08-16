package media

import (
	"fmt"
	"time"
)

// SampleFormat is how one audio sample is encoded.
//
// The two the brief requires, plus a zero value that is deliberately invalid so
// an unset format is a configuration error rather than a silent default to
// whatever happens to be first.
type SampleFormat uint8

// The sample formats.
const (
	// FormatUnset is the zero value and is never valid.
	FormatUnset SampleFormat = iota

	// FormatPCM16 is signed 16-bit little-endian PCM. The telephony default.
	FormatPCM16

	// FormatPCM32 is signed 32-bit little-endian PCM.
	FormatPCM32
)

// AllFormats returns every declared format.
func AllFormats() []SampleFormat { return []SampleFormat{FormatPCM16, FormatPCM32} }

// String implements fmt.Stringer.
func (f SampleFormat) String() string {
	switch f {
	case FormatPCM16:
		return "pcm16"
	case FormatPCM32:
		return "pcm32"
	default:
		return "unset"
	}
}

// Valid reports whether the format is one of the declared ones.
func (f SampleFormat) Valid() bool { return f == FormatPCM16 || f == FormatPCM32 }

// BytesPerSample returns the width of one sample in one channel.
func (f SampleFormat) BytesPerSample() int {
	switch f {
	case FormatPCM16:
		return 2
	case FormatPCM32:
		return 4
	default:
		return 0
	}
}

// ChannelLayout is how channels are arranged.
type ChannelLayout uint8

// The channel layouts.
const (
	// LayoutUnset is the zero value and is never valid.
	LayoutUnset ChannelLayout = iota
	// LayoutMono is one channel.
	LayoutMono
	// LayoutStereo is two interleaved channels, left then right.
	LayoutStereo
)

// AllLayouts returns every declared layout.
func AllLayouts() []ChannelLayout { return []ChannelLayout{LayoutMono, LayoutStereo} }

// String implements fmt.Stringer.
func (c ChannelLayout) String() string {
	switch c {
	case LayoutMono:
		return "mono"
	case LayoutStereo:
		return "stereo"
	default:
		return "unset"
	}
}

// Valid reports whether the layout is declared.
func (c ChannelLayout) Valid() bool { return c == LayoutMono || c == LayoutStereo }

// Channels returns the channel count.
func (c ChannelLayout) Channels() int {
	switch c {
	case LayoutMono:
		return 1
	case LayoutStereo:
		return 2
	default:
		return 0
	}
}

// Codec names the encoding a frame's payload carries.
//
// EXTENSIBILITY POINT, NOT AN IMPLEMENTATION. This package encodes and decodes
// nothing. The field exists so a frame that is not raw PCM can travel the
// pipeline without the pipeline having to guess, and so adding Opus later is a
// new constant plus an adapter rather than a change to every struct that holds
// a frame.
//
// [CodecPCM] is the only value this engine understands. A frame carrying any
// other codec is transported unchanged and MUST NOT be inspected — validation
// skips the sample-count check, because only the codec's own decoder knows how
// many samples a compressed payload represents.
type Codec uint8

// The codecs.
const (
	// CodecPCM is raw uncompressed PCM in the frame's [SampleFormat].
	CodecPCM Codec = iota
	// CodecOpaque is any encoding this engine does not understand. Transported
	// verbatim; never inspected.
	CodecOpaque
)

// String implements fmt.Stringer.
func (c Codec) String() string {
	if c == CodecOpaque {
		return "opaque"
	}
	return "pcm"
}

// SampleRate is samples per second per channel.
type SampleRate uint32

// The common telephony and wideband rates. Others are permitted; these are
// named because they appear in configuration and metric labels.
const (
	Rate8kHz  SampleRate = 8000
	Rate16kHz SampleRate = 16000
	Rate24kHz SampleRate = 24000
	Rate48kHz SampleRate = 48000
)

// String implements fmt.Stringer.
func (r SampleRate) String() string { return fmt.Sprintf("%dHz", uint32(r)) }

// Valid reports whether the rate is usable.
//
// A range rather than a fixed set: 8 kHz to 192 kHz. Refusing an unlisted rate
// would make this engine an obstacle to a carrier using something unusual, and
// the engine does not resample — it moves whatever it is given.
func (r SampleRate) Valid() bool { return r >= 8000 && r <= 192000 }

// AudioFormat is the complete description of a stream's audio.
//
// Immutable for a stream's lifetime. A stream whose format changed mid-flight
// would produce frames a consumer cannot interpret without renegotiating, and
// nothing in this engine renegotiates.
type AudioFormat struct {
	Format SampleFormat
	Layout ChannelLayout
	Rate   SampleRate
	Codec  Codec
}

// PCM16Mono8k is the telephony baseline: 8 kHz, mono, signed 16-bit.
func PCM16Mono8k() AudioFormat {
	return AudioFormat{Format: FormatPCM16, Layout: LayoutMono, Rate: Rate8kHz, Codec: CodecPCM}
}

// PCM16Mono16k is the wideband baseline.
func PCM16Mono16k() AudioFormat {
	return AudioFormat{Format: FormatPCM16, Layout: LayoutMono, Rate: Rate16kHz, Codec: CodecPCM}
}

// Validate checks the format, reporting every problem.
func (a AudioFormat) Validate() error {
	var problems []string
	if !a.Format.Valid() {
		problems = append(problems, fmt.Sprintf("format: sample format %q is unset or unknown", a.Format))
	}
	if !a.Layout.Valid() {
		problems = append(problems, fmt.Sprintf("format: channel layout %q is unset or unknown", a.Layout))
	}
	if !a.Rate.Valid() {
		problems = append(problems, fmt.Sprintf(
			"format: sample rate %s is outside the supported 8kHz–192kHz range", a.Rate))
	}
	if len(problems) > 0 {
		return &ConfigError{Problems: problems}
	}
	return nil
}

// BytesPerFrameSample returns the bytes one sample occupies across all
// channels — the stride between successive sample instants.
func (a AudioFormat) BytesPerFrameSample() int {
	return a.Format.BytesPerSample() * a.Layout.Channels()
}

// BytesFor returns how many bytes a given duration of this audio occupies.
func (a AudioFormat) BytesFor(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	samples := int(int64(a.Rate) * d.Nanoseconds() / int64(time.Second))
	return samples * a.BytesPerFrameSample()
}

// DurationFor returns how long a payload of n bytes plays for.
//
// Zero for a codec this engine does not understand: only the codec's own
// decoder knows how many samples a compressed payload represents, and returning
// a plausible-looking wrong number would be worse than returning nothing.
func (a AudioFormat) DurationFor(n int) time.Duration {
	if a.Codec != CodecPCM {
		return 0
	}
	stride := a.BytesPerFrameSample()
	if stride == 0 || a.Rate == 0 {
		return 0
	}
	samples := n / stride
	return time.Duration(int64(samples) * int64(time.Second) / int64(a.Rate))
}

// String renders the format.
func (a AudioFormat) String() string {
	return fmt.Sprintf("%s/%s/%s/%s", a.Codec, a.Format, a.Layout, a.Rate)
}

// ---------------------------------------------------------------------------
// Frame
// ---------------------------------------------------------------------------

// Frame is one unit of audio moving through the engine.
//
// # Payload ownership is BORROWED, and this is the sharpest edge in the package
//
// A Frame's Payload is a slice into storage the frame does not own. When a
// frame comes from a [RingBuffer] read or a [FramePool], the bytes belong to
// that structure and remain valid only until the next operation on it.
//
// This is deliberate and it is what makes the engine zero-allocation. At fifty
// thousand frames per second, giving every frame its own heap-allocated payload
// is fifty thousand allocations per second of garbage — and GC pauses in a media
// path are audible. The cost of avoiding that is this rule:
//
//	A CONSUMER THAT KEEPS A FRAME BEYOND THE CALL THAT GAVE IT MUST CALL Clone.
//
// [Frame.Clone] allocates and returns a frame that owns its payload. Every
// place in this package that retains a frame across a call boundary clones it,
// and [RingBuffer.Read] documents the lifetime of what it returns.
//
// The alternative designs were considered and rejected: reference counting adds
// an atomic per frame and a class of leak that presents as memory growth under
// load; copy-on-write needs a write barrier this package has no way to enforce.
// An explicit, documented, tested rule is more honest than a mechanism that
// almost works.
type Frame struct {
	// Sequence orders frames within a stream. Monotonic from the producer; the
	// pipeline uses it to detect gaps, duplicates and reordering.
	Sequence uint64

	// Timestamp is the media time this frame's first sample represents,
	// measured from the stream's start. NOT wall time — a media timeline is
	// independent of when the frame happened to arrive, which is the entire
	// basis of jitter handling.
	Timestamp time.Duration

	// Arrival is when the frame entered the engine, on the injected clock.
	// Compared against Timestamp to measure jitter and drift.
	Arrival time.Time

	// Format describes the payload.
	Format AudioFormat

	// Payload is the audio bytes. BORROWED — see the type comment.
	Payload []byte

	// Flags carries per-frame markers.
	Flags FrameFlags
}

// FrameFlags are per-frame markers.
type FrameFlags uint8

// The frame flags.
const (
	// FlagSilence marks a frame the engine synthesised to fill a gap. A
	// consumer may treat it differently — a transcriber should not spend a
	// model call on invented silence.
	FlagSilence FrameFlags = 1 << iota

	// FlagDiscontinuity marks the first frame after a gap, so a consumer knows
	// its timeline jumped rather than assuming continuity.
	FlagDiscontinuity

	// FlagEndOfStream marks the final frame.
	FlagEndOfStream
)

// Has reports whether a flag is set.
func (f FrameFlags) Has(flag FrameFlags) bool { return f&flag != 0 }

// String renders the set flags.
func (f FrameFlags) String() string {
	if f == 0 {
		return "none"
	}
	var out string
	for _, pair := range []struct {
		flag FrameFlags
		name string
	}{
		{FlagSilence, "silence"},
		{FlagDiscontinuity, "discontinuity"},
		{FlagEndOfStream, "eos"},
	} {
		if f.Has(pair.flag) {
			if out != "" {
				out += "|"
			}
			out += pair.name
		}
	}
	return out
}

// Samples returns how many sample instants the payload holds.
//
// Zero for an opaque codec: only its decoder knows.
func (f Frame) Samples() int {
	if f.Format.Codec != CodecPCM {
		return 0
	}
	stride := f.Format.BytesPerFrameSample()
	if stride == 0 {
		return 0
	}
	return len(f.Payload) / stride
}

// Duration returns how long the frame plays for.
func (f Frame) Duration() time.Duration { return f.Format.DurationFor(len(f.Payload)) }

// End returns the media time immediately after this frame.
func (f Frame) End() time.Duration { return f.Timestamp + f.Duration() }

// Empty reports whether the frame carries no audio.
func (f Frame) Empty() bool { return len(f.Payload) == 0 }

// Clone returns a frame owning a copy of the payload.
//
// THE ESCAPE HATCH from borrowed ownership, and the only safe way to keep a
// frame beyond the call that produced it. Allocates, deliberately and visibly:
// a caller who needs to retain a frame should see the cost at the point where
// they choose to pay it.
func (f Frame) Clone() Frame {
	out := f
	if f.Payload != nil {
		out.Payload = make([]byte, len(f.Payload))
		copy(out.Payload, f.Payload)
	}
	return out
}

// CloneInto copies the frame into caller-supplied storage.
//
// For a caller that retains frames in a loop and wants to reuse its own buffer
// rather than allocate per frame. Returns the frame and whether dst was large
// enough; on false the frame is unusable and the caller should grow dst.
func (f Frame) CloneInto(dst []byte) (Frame, bool) {
	if len(dst) < len(f.Payload) {
		return Frame{}, false
	}
	out := f
	n := copy(dst, f.Payload)
	out.Payload = dst[:n]
	return out, true
}

// Validate checks the frame against its declared format.
//
// # What it deliberately does NOT check
//
// Sample values. A frame of pure silence, a frame of clipping noise and a frame
// of speech are all valid to a transport engine, and inspecting samples would be
// this package developing an opinion about audio content — which is voice
// activity detection, and is out of scope by name.
func (f Frame) Validate() error {
	if err := f.Format.Validate(); err != nil {
		return err
	}
	if f.Empty() {
		return invariant("INV-MED-1", "frame %d carries no payload", f.Sequence)
	}
	if f.Format.Codec != CodecPCM {
		// An opaque payload cannot be checked for sample alignment: only its
		// decoder knows the relationship between bytes and samples.
		return nil
	}
	stride := f.Format.BytesPerFrameSample()
	if len(f.Payload)%stride != 0 {
		return invariant("INV-MED-2",
			"frame %d payload is %d bytes, not a multiple of the %d-byte stride for %s — "+
				"a partial sample means the producer and this engine disagree about the format",
			f.Sequence, len(f.Payload), stride, f.Format)
	}
	return nil
}

// String renders the frame without its payload.
//
// NEVER the payload. A frame's bytes are somebody's voice; a log line carrying
// them would be a recording nobody consented to and nobody can delete.
func (f Frame) String() string {
	return fmt.Sprintf("frame#%d t=%s dur=%s %dB %s flags=%s",
		f.Sequence, f.Timestamp.Round(time.Microsecond),
		f.Duration().Round(time.Microsecond), len(f.Payload), f.Format, f.Flags)
}

// SilenceFrame builds a silent frame of the given duration.
//
// Used by the pipeline to fill a gap left by a lost frame. Allocates, and is
// called only on the loss path — a stream losing enough frames for this to
// matter has a bigger problem than an allocation.
func SilenceFrame(seq uint64, ts time.Duration, format AudioFormat, d time.Duration) Frame {
	n := format.BytesFor(d)
	return Frame{
		Sequence: seq, Timestamp: ts, Format: format,
		// Zero bytes are silence in signed PCM of either width, which is why
		// this needs no per-format special case.
		Payload: make([]byte, n),
		Flags:   FlagSilence | FlagDiscontinuity,
	}
}
