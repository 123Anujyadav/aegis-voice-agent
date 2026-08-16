package audiointel

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
)

// FrameFeatures is everything this engine measures about one frame.
//
// # Scalars only, and that is the privacy boundary
//
// There is no payload here, no slice, no pointer into the frame's storage.
// [media.Frame.Payload] is borrowed from a ring buffer that is overwritten as
// it wraps, so retaining any part of it would be a use-after-free waiting for
// load. It would also be a recording.
//
// This struct is what survives [FrameAnalyzer.Analyze], it is the only thing
// that enters the bounded window, and every downstream detector works from it
// alone. A level in dBFS says how loud somebody was; it says nothing about what
// they said and cannot be reassembled into anything that does.
//
// Copied by value throughout. It is 96 bytes and lives on the stack.
type FrameFeatures struct {
	// Sequence is the frame's position in the stream, from Phase 11B.
	Sequence uint64

	// Timestamp is the media time of this frame's FIRST sample.
	//
	// Media time, not wall time — the two diverge under jitter, and every
	// duration this engine reports about the audio (speech length, silence
	// length, onset position) is a media-time measurement. Using arrival time
	// would make a burst of late frames look like a burst of fast speech.
	Timestamp time.Duration

	// Arrival is when the frame entered the engine, on the injected clock. Used
	// only for latency measurement — never for audio timing.
	Arrival time.Time

	// Duration is how long this frame plays for.
	Duration time.Duration

	// Samples is how many sample instants the frame held.
	Samples int

	// RMS is the root-mean-square level, normalised to full scale, in [0,1].
	//
	// The primary loudness measure. Preferred to peak because a single sample
	// spike moves peak and barely moves RMS, and speech detection cares about
	// sustained energy rather than instantaneous excursions.
	RMS float64

	// Peak is the maximum absolute sample, normalised to full scale, in [0,1].
	Peak float64

	// Energy is the mean square — RMS squared, kept because the modulation
	// statistics work in the energy domain and squaring back and forth on every
	// frame is wasted arithmetic.
	Energy float64

	// ZCR is the zero-crossing rate: sign changes over sample intervals, in
	// [0,1].
	//
	// Measured WITHIN the frame only. A crossing that happens exactly at a
	// frame boundary is missed, which at fifty frames a second costs at most
	// fifty crossings a second out of thousands — far below the width of the
	// band it feeds. Carrying the previous frame's last sample across would
	// make the analyser stateful and the feature un-reproducible from a frame
	// in isolation, which is a poor trade for that much accuracy.
	ZCR float64

	// ClipRatio is the fraction of samples at or beyond full scale, in [0,1].
	ClipRatio float64

	// Flags carries Phase 11B's per-frame markers, unmodified.
	//
	// FlagSilence means 11B INVENTED this frame to cover a gap. That matters
	// enormously here: invented silence is not measured silence, and a detector
	// that endpointed on it would end a caller's turn because of a network
	// glitch.
	Flags media.FrameFlags
}

// Synthesised reports whether Phase 11B invented this frame to fill a gap.
func (f FrameFeatures) Synthesised() bool { return f.Flags.Has(media.FlagSilence) }

// Discontinuous reports whether this is the first frame after a gap.
func (f FrameFeatures) Discontinuous() bool { return f.Flags.Has(media.FlagDiscontinuity) }

// End returns the media time immediately after this frame.
func (f FrameFeatures) End() time.Duration { return f.Timestamp + f.Duration }

// LevelDBFS returns the RMS level in decibels relative to full scale.
//
// Negative for everything below clipping; 0 dBFS is a full-scale square wave.
func (f FrameFeatures) LevelDBFS() float64 { return dbfs(f.RMS) }

// PeakDBFS returns the peak level in decibels relative to full scale.
func (f FrameFeatures) PeakDBFS() float64 { return dbfs(f.Peak) }

// CrestFactorDB returns the peak-to-RMS ratio in decibels.
//
// THE FLATNESS TEST. A sine wave sits at 3 dB, a square wave at 0, and speech
// — which is bursts of energy separated by near-silence — typically sits well
// above 10. A signal with a suspiciously low crest factor is a tone, a stuck
// codec, or a dead line carrying only its own noise, none of which is somebody
// talking.
func (f FrameFeatures) CrestFactorDB() float64 {
	if f.RMS <= 0 {
		return 0
	}
	return dbfs(f.Peak) - dbfs(f.RMS)
}

// String renders the features. NEVER a sample — see the type comment.
func (f FrameFeatures) String() string {
	return fmt.Sprintf("frame#%d t=%s %.1fdBFS peak=%.1fdBFS zcr=%.3f clip=%.4f n=%d",
		f.Sequence, f.Timestamp.Round(time.Microsecond),
		f.LevelDBFS(), f.PeakDBFS(), f.ZCR, f.ClipRatio, f.Samples)
}

// ---------------------------------------------------------------------------
// Decibel helpers
// ---------------------------------------------------------------------------

// minAmplitude is the smallest amplitude the decibel helpers will take a
// logarithm of.
//
// About -200 dBFS, far below the least significant bit of any format this
// engine accepts. Its only job is to keep log10 from returning -Inf on a
// digitally silent frame; an infinity here would propagate into a threshold
// comparison and make every subsequent decision meaningless.
const minAmplitude = 1e-10

// dbfs converts a normalised amplitude to decibels relative to full scale.
func dbfs(amplitude float64) float64 {
	if amplitude < minAmplitude {
		amplitude = minAmplitude
	}
	return 20 * math.Log10(amplitude)
}

// dbRatio returns how many decibels a sits above b.
//
// The core comparison of this whole engine: every speech decision is "is this
// frame far enough above the noise floor", and this is that question.
func dbRatio(a, b float64) float64 {
	if a < minAmplitude {
		a = minAmplitude
	}
	if b < minAmplitude {
		b = minAmplitude
	}
	return 20 * math.Log10(a/b)
}

// ---------------------------------------------------------------------------
// FrameAnalyzer
// ---------------------------------------------------------------------------

// FrameAnalyzer turns one frame of PCM into [FrameFeatures].
//
// # Stateless, and allocation-free
//
// It holds the format and the constants derived from it, nothing else. Two
// calls with the same frame produce byte-identical results, which is what makes
// deterministic replay possible at all.
//
// The measurement loop makes ONE pass over the payload and allocates nothing.
// At fifty frames a second per session and a thousand sessions, an allocation
// per frame is fifty thousand allocations a second of garbage, and a GC pause
// in an audio path is audible.
type FrameAnalyzer struct {
	format media.AudioFormat

	// bytesPerSample and scale are derived from the format once, so the hot
	// loop does no format dispatch beyond one branch on width.
	bytesPerSample int

	// scale converts an integer sample to the normalised [-1,1] range.
	//
	// The NEGATIVE full-scale magnitude, because two's complement is asymmetric:
	// int16 runs -32768..32767. Dividing by 32768 puts the most negative sample
	// at exactly -1.0 and the most positive at 0.99997, which is correct.
	// Dividing by 32767 would put the most negative sample past -1 and make
	// "normalised to full scale" a lie in the one place it matters.
	scale float64

	// clipMagnitude is the absolute integer value at which a sample counts as
	// clipped.
	clipMagnitude int64
}

// NewFrameAnalyzer builds an analyser for one audio format.
//
// Refuses anything it cannot measure — see [validateAnalysisFormat]. Failing
// closed here rather than returning zeroed features means a stereo stream
// produces a startup error a human reads, not a detector that silently reports
// permanent silence.
func NewFrameAnalyzer(format media.AudioFormat) (*FrameAnalyzer, error) {
	if err := validateAnalysisFormat(format); err != nil {
		return nil, err
	}

	a := &FrameAnalyzer{
		format:         format,
		bytesPerSample: format.Format.BytesPerSample(),
	}

	switch format.Format {
	case media.FormatPCM16:
		a.scale = 1.0 / 32768.0
		a.clipMagnitude = 32767
	case media.FormatPCM32:
		a.scale = 1.0 / 2147483648.0
		a.clipMagnitude = 2147483647
	default:
		// Unreachable: validateAnalysisFormat accepts only the two widths.
		return nil, invariant("INV-AI-1",
			"format %s passed validation but has no sample scale", format)
	}

	return a, nil
}

// Format returns the audio format this analyser measures.
func (a *FrameAnalyzer) Format() media.AudioFormat { return a.format }

// Analyze measures one frame.
//
// # The payload is read in place and never retained
//
// Everything this function returns is a scalar. The frame's bytes are touched
// exactly once, inside this call, and no reference to them outlives it. That is
// what makes the borrowed-payload contract safe here and what makes §24
// structural rather than a policy somebody has to remember.
func (a *FrameAnalyzer) Analyze(f media.Frame) (FrameFeatures, error) {
	if f.Format != a.format {
		return FrameFeatures{}, fmt.Errorf("%w: frame is %s, analyser measures %s",
			ErrFormatMismatch, f.Format, a.format)
	}
	if len(f.Payload) == 0 {
		return FrameFeatures{}, fmt.Errorf("%w: frame %d carries no payload",
			ErrInvalidFrame, f.Sequence)
	}
	if len(f.Payload)%a.bytesPerSample != 0 {
		return FrameFeatures{}, fmt.Errorf(
			"%w: frame %d is %d bytes, not a multiple of the %d-byte sample width for "+
				"%s — a partial sample means the producer and this engine disagree "+
				"about the format",
			ErrInvalidFrame, f.Sequence, len(f.Payload), a.bytesPerSample, a.format)
	}

	feat := FrameFeatures{
		Sequence:  f.Sequence,
		Timestamp: f.Timestamp,
		Arrival:   f.Arrival,
		Duration:  f.Duration(),
		Flags:     f.Flags,
	}

	if a.bytesPerSample == 2 {
		feat.measurePCM16(f.Payload, a.scale, a.clipMagnitude)
	} else {
		feat.measurePCM32(f.Payload, a.scale, a.clipMagnitude)
	}

	return feat, nil
}

// measurePCM16 makes one pass over signed 16-bit little-endian samples.
//
// Split by width rather than dispatching per sample through an interface or a
// closure: the per-sample cost is a handful of instructions, and an indirect
// call around it would dominate. Duplicating twenty lines is the cheaper
// mistake.
func (f *FrameFeatures) measurePCM16(p []byte, scale float64, clipAt int64) {
	n := len(p) / 2

	var sumSquares float64
	var peak int64
	var crossings, clipped int
	var prevPositive bool

	for i := 0; i < n; i++ {
		s := int64(int16(binary.LittleEndian.Uint16(p[i*2:])))

		norm := float64(s) * scale
		sumSquares += norm * norm

		mag := s
		if mag < 0 {
			mag = -mag
		}
		if mag > peak {
			peak = mag
		}
		if mag >= clipAt {
			clipped++
		}

		// A sample of exactly zero is treated as non-negative rather than as its
		// own sign. Otherwise a signal resting at zero — digital silence — would
		// register a crossing on every transition into and out of it and report
		// the zero-crossing rate of white noise.
		positive := s >= 0
		if i > 0 && positive != prevPositive {
			crossings++
		}
		prevPositive = positive
	}

	f.finish(n, sumSquares, float64(peak)*scale, crossings, clipped)
}

// measurePCM32 makes one pass over signed 32-bit little-endian samples.
func (f *FrameFeatures) measurePCM32(p []byte, scale float64, clipAt int64) {
	n := len(p) / 4

	var sumSquares float64
	var peak int64
	var crossings, clipped int
	var prevPositive bool

	for i := 0; i < n; i++ {
		s := int64(int32(binary.LittleEndian.Uint32(p[i*4:])))

		norm := float64(s) * scale
		sumSquares += norm * norm

		mag := s
		if mag < 0 {
			mag = -mag
		}
		if mag > peak {
			peak = mag
		}
		if mag >= clipAt {
			clipped++
		}

		positive := s >= 0
		if i > 0 && positive != prevPositive {
			crossings++
		}
		prevPositive = positive
	}

	f.finish(n, sumSquares, float64(peak)*scale, crossings, clipped)
}

// finish turns the accumulated sums into the normalised features.
func (f *FrameFeatures) finish(n int, sumSquares, peak float64, crossings, clipped int) {
	f.Samples = n
	f.Peak = peak

	if n > 0 {
		f.Energy = sumSquares / float64(n)
		f.RMS = math.Sqrt(f.Energy)
		f.ClipRatio = float64(clipped) / float64(n)
	}

	// n-1 intervals between n samples. A single-sample frame has no interval
	// and therefore no measurable crossing rate — reporting 0 rather than
	// dividing by zero.
	if n > 1 {
		f.ZCR = float64(crossings) / float64(n-1)
	}
}
