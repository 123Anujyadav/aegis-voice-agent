package audiointel

import (
	"encoding/binary"
	"math"
	"math/rand"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
)

// ---------------------------------------------------------------------------
// Deterministic synthetic audio
// ---------------------------------------------------------------------------
//
// Every fixture in this file is generated from arithmetic with a fixed seed. No
// microphone, no recorded audio, no file on disk, nothing checked into the
// repository that anybody ever said out loud.
//
// That is a privacy decision as much as a testing one. A test corpus of real
// speech is a recording with all the obligations that implies, and it would
// have to live in git forever. It is also a reproducibility decision: a
// generated fixture is identical on every machine and in every run, so a
// threshold regression shows up as a failing assertion rather than as a
// flake somebody reruns.
//
// WHAT THESE FIXTURES CAN AND CANNOT PROVE. They prove that the detectors
// respond correctly to signals with known statistical properties — a known
// level, a known modulation depth, a known zero-crossing rate, a known silence
// duration. They do NOT prove anything about recognition accuracy on real
// speech, in any language. See docs/audio-intelligence/EVALUATION_REPORT.md,
// which says so in those words.

// SignalGenerator builds deterministic synthetic frames.
//
// EXPORTED ON PURPOSE, following the convention every phase since 10A has
// established: a service embedding this engine needs to test its own code
// against it, and forcing every consumer to rebuild this scaffolding is how six
// subtly different fakes come to exist.
//
// Not safe for concurrent use. One generator per test, which is also what keeps
// the sequence numbers it mints meaningful.
type SignalGenerator struct {
	format   media.AudioFormat
	interval time.Duration
	rng      *rand.Rand

	// seq and mediaTime advance with every frame produced, so a generator
	// yields a continuous stream by default and a caller must go out of its way
	// to create a gap.
	seq       uint64
	mediaTime time.Duration

	// phase carries oscillator phase across frames so a tone is continuous
	// rather than restarting — a restarted oscillator produces a discontinuity
	// at every frame boundary, which reads as a broadband click and would show
	// up in the zero-crossing rate as something the caller did not do.
	phase float64

	// envPhase carries the syllabic envelope phase across frames for the same
	// reason.
	envPhase float64

	// arrival is the wall time stamped on produced frames.
	arrival time.Time
}

// FixtureSeed is the seed every generator uses unless told otherwise.
//
// A fixed constant, exported so a failing test can be reproduced exactly and so
// a reader can tell at a glance that nothing here is random in the sense that
// matters.
const FixtureSeed = 0x11D

// NewSignalGenerator builds a generator for one format at one cadence.
//
// math/rand rather than crypto/rand, deliberately: this needs to be
// REPRODUCIBLE, which is the opposite of what a cryptographic source provides.
// Nothing here mints an identifier or a secret.
func NewSignalGenerator(format media.AudioFormat, interval time.Duration) *SignalGenerator {
	return &SignalGenerator{
		format:   format,
		interval: interval,
		//nolint:gosec // Reproducibility is the requirement; see the comment above.
		rng: rand.New(rand.NewSource(FixtureSeed)),
	}
}

// Reseed restarts the noise source, so two runs of a scenario are identical.
func (g *SignalGenerator) Reseed(seed int64) {
	//nolint:gosec // Reproducibility is the requirement.
	g.rng = rand.New(rand.NewSource(seed))
}

// SetArrival fixes the wall time stamped on subsequent frames.
//
// Frames from a FakeClock-driven test should carry that clock's time, or
// latency measurements compare a fake instant against a real one and produce
// numbers that look like decades.
func (g *SignalGenerator) SetArrival(t time.Time) { g.arrival = t }

// Sequence returns the next sequence number the generator will mint.
func (g *SignalGenerator) Sequence() uint64 { return g.seq }

// MediaTime returns the media position the next frame will start at.
func (g *SignalGenerator) MediaTime() time.Duration { return g.mediaTime }

// SamplesPerFrame returns how many sample instants one frame holds.
func (g *SignalGenerator) SamplesPerFrame() int {
	return int(int64(g.format.Rate) * g.interval.Nanoseconds() / int64(time.Second))
}

// Skip advances the sequence and media clock without producing frames.
//
// THE MISSING-FRAME FIXTURE. A stream with a hole in it, exactly as a lost
// packet leaves one.
func (g *SignalGenerator) Skip(frames int) {
	for i := 0; i < frames; i++ {
		g.seq++
		g.mediaTime += g.interval
	}
}

// Silence produces digitally silent frames.
func (g *SignalGenerator) Silence(frames int) []media.Frame {
	return g.generate(frames, func(_ int, _ float64) float64 { return 0 })
}

// Tone produces a continuous sine wave.
//
// THE PURE-TONE REJECTION FIXTURE: hold music, a dial tone, a fax carrier, a
// ringback. Loud, sustained, and not speech. A detector that fires on this
// answers every call by talking over a ringing phone.
func (g *SignalGenerator) Tone(hz, amplitude float64, frames int) []media.Frame {
	step := 2 * math.Pi * hz / float64(g.format.Rate)
	return g.generate(frames, func(_ int, _ float64) float64 {
		v := amplitude * math.Sin(g.phase)
		g.phase += step
		if g.phase > 2*math.Pi {
			g.phase -= 2 * math.Pi
		}
		return v
	})
}

// Noise produces stationary broadband noise at a constant level.
//
// THE STATIONARY-BACKGROUND FIXTURE: a fan, line hiss, an engine, an air
// conditioner. Constant level means low energy modulation, which is the feature
// that separates it from speech regardless of how loud it is.
func (g *SignalGenerator) Noise(amplitude float64, frames int) []media.Frame {
	return g.generate(frames, func(_ int, _ float64) float64 {
		return amplitude * (2*g.rng.Float64() - 1)
	})
}

// Speech produces a synthetic speech-like signal.
//
// # What this is, precisely
//
// A harmonic source at f0 with three harmonics, mixed with a broadband
// component, multiplied by a syllabic envelope at syllableHz that closes to
// near-silence between syllables.
//
// That construction reproduces the three properties this engine's detectors
// actually measure:
//
//   - HIGH ENERGY MODULATION. The syllabic envelope gives a coefficient of
//     variation well above VADConfig.MinEnergyModulation, which is what
//     distinguishes it from stationary noise.
//   - A MID-BAND ZERO-CROSSING RATE. The harmonic source sits low, the
//     broadband component sits high, and the mixture lands between them —
//     inside the band VADConfig.ZCRMin and ZCRMax bracket.
//   - A HIGH CREST FACTOR. Bursts separated by closures, like speech and unlike
//     a tone.
//
// # What it is not
//
// It is not speech. It has no formants, no articulation, no phonemes and no
// language. It sounds like a buzzing insect. Nothing in this package's
// evaluation claims otherwise, and no recognition accuracy is inferred from it.
func (g *SignalGenerator) Speech(amplitude, f0, syllableHz float64, frames int) []media.Frame {
	step := 2 * math.Pi * f0 / float64(g.format.Rate)
	envStep := 2 * math.Pi * syllableHz / float64(g.format.Rate)

	return g.generate(frames, func(_ int, _ float64) float64 {
		// Harmonic source: fundamental plus two harmonics at falling amplitude,
		// which is roughly how a glottal source rolls off.
		v := math.Sin(g.phase) +
			harmonic2Weight*math.Sin(2*g.phase) +
			harmonic3Weight*math.Sin(3*g.phase)
		v /= 1 + harmonic2Weight + harmonic3Weight

		// Broadband component, standing in for the aperiodic energy of
		// fricatives and aspiration. Without it the zero-crossing rate sits at
		// the bottom of the band and the signal is indistinguishable from a
		// tone by that feature.
		v = (1-fricativeMix)*v + fricativeMix*(2*g.rng.Float64()-1)

		// Syllabic envelope. Raised cosine so it closes smoothly to a floor
		// rather than clicking, and raised to a power so the closures are
		// narrower than the openings — which is what makes the modulation deep
		// enough to measure.
		env := 0.5 * (1 - math.Cos(g.envPhase))
		env = math.Pow(env, envelopeSharpness)
		if env < envelopeFloor {
			env = envelopeFloor
		}

		g.phase += step
		if g.phase > 2*math.Pi {
			g.phase -= 2 * math.Pi
		}
		g.envPhase += envStep
		if g.envPhase > 2*math.Pi {
			g.envPhase -= 2 * math.Pi
		}

		return amplitude * v * env
	})
}

// NormalSpeech produces speech-like audio at a conversational level.
func (g *SignalGenerator) NormalSpeech(frames int) []media.Frame {
	return g.Speech(normalSpeechAmplitude, defaultF0, defaultSyllableHz, frames)
}

// QuietSpeech produces speech-like audio at a level a distant or soft speaker
// would produce — well above the noise floor but far below normal.
func (g *SignalGenerator) QuietSpeech(frames int) []media.Frame {
	return g.Speech(quietSpeechAmplitude, defaultF0, defaultSyllableHz, frames)
}

// LoudSpeech produces speech-like audio close to full scale.
func (g *SignalGenerator) LoudSpeech(frames int) []media.Frame {
	return g.Speech(loudSpeechAmplitude, defaultF0, defaultSyllableHz, frames)
}

// RapidSpeech produces speech-like audio with a fast syllable rate.
func (g *SignalGenerator) RapidSpeech(frames int) []media.Frame {
	return g.Speech(normalSpeechAmplitude, defaultF0, rapidSyllableHz, frames)
}

// SpeechOverNoise produces speech-like audio on a noisy line.
func (g *SignalGenerator) SpeechOverNoise(speechAmp, noiseAmp float64, frames int) []media.Frame {
	step := 2 * math.Pi * defaultF0 / float64(g.format.Rate)
	envStep := 2 * math.Pi * defaultSyllableHz / float64(g.format.Rate)

	return g.generate(frames, func(_ int, _ float64) float64 {
		v := math.Sin(g.phase) + harmonic2Weight*math.Sin(2*g.phase)
		v /= 1 + harmonic2Weight
		v = (1-fricativeMix)*v + fricativeMix*(2*g.rng.Float64()-1)

		env := 0.5 * (1 - math.Cos(g.envPhase))
		env = math.Pow(env, envelopeSharpness)
		if env < envelopeFloor {
			env = envelopeFloor
		}

		g.phase += step
		if g.phase > 2*math.Pi {
			g.phase -= 2 * math.Pi
		}
		g.envPhase += envStep
		if g.envPhase > 2*math.Pi {
			g.envPhase -= 2 * math.Pi
		}

		return speechAmp*v*env + noiseAmp*(2*g.rng.Float64()-1)
	})
}

// Transient produces one frame of a sharp burst followed by quiet.
//
// THE DOOR-SLAM FIXTURE: loud, broadband and over in well under the onset
// confirmation window. A detector without consecutive-frame confirmation starts
// a turn on this.
func (g *SignalGenerator) Transient(amplitude float64, frames int) []media.Frame {
	samplesPerFrame := g.SamplesPerFrame()
	burst := int(float64(samplesPerFrame) * transientFraction)

	produced := 0
	return g.generate(frames, func(i int, _ float64) float64 {
		defer func() { produced++ }()
		if produced%samplesPerFrame == 0 {
			produced = 0
		}
		if i < burst {
			// Exponential decay, which is what an impulsive source does.
			decay := math.Exp(-float64(i) / (float64(burst) * transientDecay))
			return amplitude * decay * (2*g.rng.Float64() - 1)
		}
		return 0
	})
}

// Clipped produces a signal driven hard past full scale.
//
// THE CLIPPING FIXTURE. Generated at an amplitude above 1.0 so the sample
// writer's clamp does the clipping, which is exactly how a real overdriven gain
// stage produces it.
func (g *SignalGenerator) Clipped(frames int) []media.Frame {
	return g.Speech(clippedAmplitude, defaultF0, defaultSyllableHz, frames)
}

// generate builds frames from a per-sample function.
//
// The function receives the sample index within the frame. It allocates a
// payload per frame, which is correct HERE and wrong in production: fixtures
// are retained by the test that built them, so each needs storage it owns.
func (g *SignalGenerator) generate(frames int, sample func(i int, t float64) float64) []media.Frame {
	if frames <= 0 {
		return nil
	}

	n := g.SamplesPerFrame()
	width := g.format.Format.BytesPerSample()
	out := make([]media.Frame, 0, frames)

	for f := 0; f < frames; f++ {
		payload := make([]byte, n*width)
		for i := 0; i < n; i++ {
			writeSample(payload[i*width:], sample(i, float64(i)/float64(g.format.Rate)),
				g.format.Format)
		}

		out = append(out, media.Frame{
			Sequence:  g.seq,
			Timestamp: g.mediaTime,
			Arrival:   g.arrival,
			Format:    g.format,
			Payload:   payload,
		})

		g.seq++
		g.mediaTime += g.interval
		if !g.arrival.IsZero() {
			g.arrival = g.arrival.Add(g.interval)
		}
	}

	return out
}

// writeSample encodes one normalised sample, clamping to full scale.
//
// The clamp IS the clipping model: a caller asking for 1.4 gets the same
// flat-topped waveform an overdriven preamp produces, and the clip counter in
// [FrameAnalyzer] sees exactly what it would see on a real overdriven line.
func writeSample(dst []byte, v float64, format media.SampleFormat) {
	if v > 1 {
		v = 1
	}
	if v < -1 {
		v = -1
	}

	switch format {
	case media.FormatPCM16:
		s := int32(v * 32767)
		if s > 32767 {
			s = 32767
		}
		if s < -32768 {
			s = -32768
		}
		binary.LittleEndian.PutUint16(dst, uint16(int16(s)))
	case media.FormatPCM32:
		s := int64(v * 2147483647)
		if s > 2147483647 {
			s = 2147483647
		}
		if s < -2147483648 {
			s = -2147483648
		}
		binary.LittleEndian.PutUint32(dst, uint32(int32(s)))
	}
}

// ---------------------------------------------------------------------------
// Fixture shape constants
// ---------------------------------------------------------------------------
//
// These describe the SHAPE of a generated waveform, not a decision threshold.
// They belong here rather than in defaults.go because changing one changes what
// a test signal is, not how the engine judges it.

const (
	// harmonic2Weight and harmonic3Weight roll the harmonic source off, which
	// is roughly what a glottal source does.
	harmonic2Weight = 0.5
	harmonic3Weight = 0.25

	// fricativeMix is how much aperiodic energy is blended in. Without it the
	// zero-crossing rate sits at the bottom of the speech band and the signal
	// looks like a tone to the feature that exists to reject tones.
	fricativeMix = 0.35

	// envelopeSharpness narrows the syllable openings so the closures between
	// them are deep enough to produce measurable modulation.
	envelopeSharpness = 2.0

	// envelopeFloor stops the envelope reaching exact zero. Real speech does
	// not go digitally silent between syllables, and a fixture that did would
	// make the hangover look better than it is.
	envelopeFloor = 0.02

	// defaultF0 is a low adult fundamental, in hertz.
	defaultF0 = 120.0

	// defaultSyllableHz is roughly five syllables a second, which is ordinary
	// conversational speed across languages.
	defaultSyllableHz = 5.0

	// rapidSyllableHz is roughly nine syllables a second — fast, hurried
	// speech.
	rapidSyllableHz = 9.0

	// The three speech levels, as normalised amplitudes.
	quietSpeechAmplitude  = 0.02
	normalSpeechAmplitude = 0.25
	loudSpeechAmplitude   = 0.80

	// clippedAmplitude drives the signal past full scale so the writer's clamp
	// produces genuine flat-topping.
	clippedAmplitude = 1.40

	// transientFraction is how much of a frame a burst occupies.
	transientFraction = 0.15

	// transientDecay shapes the exponential tail of a burst.
	transientDecay = 0.30
)
