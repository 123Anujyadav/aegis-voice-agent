package audiointel

import (
	"math"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
)

// ---------------------------------------------------------------------------
// Language-timing fixtures
// ---------------------------------------------------------------------------
//
// # Read this before reading the generators below
//
// THESE ARE NOT HINDI. They are not speech in any language, they contain no
// phonemes, no formants and no words, and nothing in this package's evaluation
// infers recognition accuracy from them. Producing genuine Hindi speech
// requires a synthesiser, which is Phase 11C's concern and not something the
// standard library provides.
//
// What they DO reproduce is the timing structure this engine's algorithms
// actually measure. Every detector here counts milliseconds and compares
// decibels; none can tell one language from another, and the only way a
// language can affect a result is through its RHYTHM — how long syllables run,
// how deep the closures between them go, and where pauses fall. Those are
// measurable and reproducible, so those are what the fixtures vary.
//
// The traits modelled, and why each matters to a detector:
//
//   - SYLLABLE-TIMED RHYTHM. Hindi syllables run closer to equal duration than
//     English's stress-timed alternation of long and short. That changes the
//     energy modulation the voice activity detector's third feature measures.
//   - GEMINATE CLOSURES. Hindi contrasts single and doubled consonants, and a
//     geminate closure is a longer near-silence mid-word. It is the case most
//     likely to defeat a hangover that is too short, because it is a pause
//     INSIDE a word rather than between words.
//   - UTTERANCE-FINAL LENGTHENING. The final syllable stretches, which changes
//     where the energy trend sits when the endpoint window opens.
//   - CODE-SWITCH PAUSES. Hinglish speakers pause briefly at the switch between
//     languages, and those pauses land in the range separating an inter-word
//     gap from an endpoint — exactly the boundary EndpointPolicy tunes.
//
// docs/audio-intelligence/EVALUATION_REPORT.md states the same limitation in
// the same terms, because a fixture named "Hindi" invites precisely the reading
// this paragraph exists to prevent.

// Rhythm describes the timing structure of a synthetic utterance.
type Rhythm struct {
	// SyllableHz is the syllable rate.
	SyllableHz float64

	// Evenness is how uniform syllable durations are, in [0,1]. 1 is perfectly
	// syllable-timed; lower values alternate long and short, which is what
	// stress timing does.
	Evenness float64

	// GeminateEvery inserts a deeper, longer closure every n syllables. Zero
	// disables it.
	GeminateEvery int

	// FinalLengthening stretches the last part of the utterance by this factor.
	// 1 disables it.
	FinalLengthening float64
}

// HindiRhythm approximates the timing of conversational Hindi.
//
// Syllable-timed, faster than the English default, with geminate closures every
// few syllables and pronounced utterance-final lengthening.
func HindiRhythm() Rhythm {
	return Rhythm{
		SyllableHz:       hindiSyllableHz,
		Evenness:         hindiEvenness,
		GeminateEvery:    hindiGeminateEvery,
		FinalLengthening: hindiFinalLengthening,
	}
}

// EnglishRhythm approximates the timing of conversational Indian English.
//
// Stress-timed: syllables alternate long and short, which is what the lower
// evenness models.
func EnglishRhythm() Rhythm {
	return Rhythm{
		SyllableHz:       defaultSyllableHz,
		Evenness:         englishEvenness,
		GeminateEvery:    0,
		FinalLengthening: englishFinalLengthening,
	}
}

// SpeechWithRhythm produces speech-like audio with an explicit timing
// structure.
func (g *SignalGenerator) SpeechWithRhythm(
	amplitude float64, r Rhythm, frames int,
) []media.Frame {
	step := 2 * math.Pi * defaultF0 / float64(g.format.Rate)

	total := float64(frames) * float64(g.interval)
	finalFrom := total * (1 - finalLengtheningSpan)

	// Elapsed media time within this utterance, so the envelope can respond to
	// position as well as to phase.
	var elapsed float64
	perSample := float64(g.interval) / float64(g.SamplesPerFrame())

	var syllable int

	return g.generate(frames, func(_ int, _ float64) float64 {
		rate := r.SyllableHz

		// Utterance-final lengthening: the last stretch slows down.
		if r.FinalLengthening > 1 && elapsed >= finalFrom {
			rate /= r.FinalLengthening
		}

		// Stress timing: alternate syllables run long and short. Perfect
		// evenness leaves the rate untouched.
		if syllable%2 == 1 {
			rate *= 1 + (1-r.Evenness)*stressRatio
		}

		envStep := 2 * math.Pi * rate / float64(g.format.Rate)

		v := math.Sin(g.phase) + harmonic2Weight*math.Sin(2*g.phase)
		v /= 1 + harmonic2Weight
		v = (1-fricativeMix)*v + fricativeMix*(2*g.rng.Float64()-1)

		env := 0.5 * (1 - math.Cos(g.envPhase))
		env = math.Pow(env, envelopeSharpness)

		floor := envelopeFloor
		// A geminate closure goes deeper and lasts longer, which is what a
		// too-short hangover mistakes for the end of a word.
		if r.GeminateEvery > 0 && syllable%r.GeminateEvery == 0 {
			floor = geminateFloor
			env = math.Pow(env, geminateSharpness)
		}
		if env < floor {
			env = floor
		}

		g.phase += step
		if g.phase > 2*math.Pi {
			g.phase -= 2 * math.Pi
		}
		g.envPhase += envStep
		if g.envPhase > 2*math.Pi {
			g.envPhase -= 2 * math.Pi
			syllable++
		}
		elapsed += perSample

		return amplitude * v * env
	})
}

// HindiSpeech produces speech-like audio with Hindi timing.
//
// NOT HINDI. See the block comment at the top of this file.
func (g *SignalGenerator) HindiSpeech(frames int) []media.Frame {
	return g.SpeechWithRhythm(normalSpeechAmplitude, HindiRhythm(), frames)
}

// EnglishSpeech produces speech-like audio with Indian English timing.
func (g *SignalGenerator) EnglishSpeech(frames int) []media.Frame {
	return g.SpeechWithRhythm(normalSpeechAmplitude, EnglishRhythm(), frames)
}

// HinglishSpeech alternates Hindi and English timing with a pause at each
// switch.
//
// NOT HINGLISH. What it models is the code-switch pause: speakers pause briefly
// at the boundary, and those pauses land in the range separating an inter-word
// gap from an endpoint. An engine that treated one as a turn boundary would cut
// a code-mixing speaker off mid-sentence, which is the failure this fixture
// exists to detect.
func (g *SignalGenerator) HinglishSpeech(segments, framesPerSegment int) []media.Frame {
	var out []media.Frame

	for i := 0; i < segments; i++ {
		rhythm := HindiRhythm()
		if i%2 == 1 {
			rhythm = EnglishRhythm()
		}
		out = append(out, g.SpeechWithRhythm(normalSpeechAmplitude, rhythm,
			framesPerSegment)...)

		// Deliberately shorter than the endpoint window: a code switch is not
		// the end of a turn.
		if i < segments-1 {
			out = append(out, g.Silence(codeSwitchPauseFrames)...)
		}
	}

	return out
}

// Language-timing fixture constants.
//
// SHAPE, NOT POLICY. These describe what a generated waveform IS, not how the
// engine judges it, which is why they live here rather than in defaults.go.
const (
	// hindiSyllableHz is faster than the English default: conversational Hindi
	// runs at a higher syllable rate.
	hindiSyllableHz = 6.5

	// hindiEvenness is high — Hindi is syllable-timed, so syllable durations
	// cluster rather than alternating long and short.
	hindiEvenness = 0.9

	// englishEvenness is lower: stress timing alternates.
	englishEvenness = 0.45

	// stressRatio is how much a stressed syllable stretches at zero evenness.
	stressRatio = 0.6

	// hindiGeminateEvery inserts a doubled-consonant closure every fourth
	// syllable.
	hindiGeminateEvery = 4

	// hindiFinalLengthening and englishFinalLengthening stretch the final part
	// of an utterance.
	hindiFinalLengthening   = 1.6
	englishFinalLengthening = 1.25

	// finalLengtheningSpan is what fraction of the utterance counts as final.
	finalLengtheningSpan = 0.2

	// geminateFloor is how far a geminate closure drops and geminateSharpness
	// how abruptly. Deeper and sharper than an ordinary inter-syllable closure,
	// which is what makes it the hangover's hardest case.
	geminateFloor     = 0.004
	geminateSharpness = 3.5

	// codeSwitchPauseFrames is the pause at a language switch: 100 ms at the
	// default cadence, comfortably inside the 250 ms endpoint window.
	codeSwitchPauseFrames = 5
)
