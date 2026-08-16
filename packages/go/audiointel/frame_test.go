package audiointel

import (
	"errors"
	"math"
	"testing"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
)

// frameFromSamples builds a frame from normalised samples, for the cases where
// a test needs an exactly known waveform rather than a generated one.
func frameFromSamples(t *testing.T, format media.AudioFormat, samples []float64) media.Frame {
	t.Helper()

	width := format.Format.BytesPerSample()
	payload := make([]byte, len(samples)*width)
	for i, s := range samples {
		writeSample(payload[i*width:], s, format.Format)
	}
	return media.Frame{Format: format, Payload: payload}
}

func mustAnalyzer(t *testing.T, format media.AudioFormat) *FrameAnalyzer {
	t.Helper()
	a, err := NewFrameAnalyzer(format)
	if err != nil {
		t.Fatalf("NewFrameAnalyzer(%s): %v", format, err)
	}
	return a
}

// TestFrameAnalyzer_KnownSignals checks the measurement arithmetic against
// waveforms whose features can be derived on paper.
//
// # Why analytic signals rather than recorded ones
//
// A recorded clip proves that the analyser produced SOME number. A full-scale
// square wave has an RMS of exactly 1 and a zero-crossing rate of exactly 1,
// and a sine of amplitude A has an RMS of exactly A/√2. Those are checkable
// claims, and a regression in the measurement loop breaks them by an amount a
// tolerance will not hide.
func TestFrameAnalyzer_KnownSignals(t *testing.T) {
	t.Parallel()

	const n = 160 // 20 ms at 8 kHz
	format := media.PCM16Mono8k()
	a := mustAnalyzer(t, format)

	square := make([]float64, n)
	for i := range square {
		if i%2 == 0 {
			square[i] = 1
		} else {
			square[i] = -1
		}
	}

	halfSine := make([]float64, n)
	quarterSine := make([]float64, n)
	for i := range halfSine {
		// Four full cycles across the frame: 200 Hz at 8 kHz.
		phase := 2 * math.Pi * 4 * float64(i) / float64(n)
		halfSine[i] = 0.5 * math.Sin(phase)
		quarterSine[i] = 0.25 * math.Sin(phase)
	}

	dc := make([]float64, n)
	for i := range dc {
		dc[i] = 0.5
	}

	cases := []struct {
		name    string
		samples []float64

		wantRMS  float64
		wantPeak float64
		wantZCR  float64
		tol      float64
	}{
		{
			name:    "digital silence",
			samples: make([]float64, n),
			// A signal resting at zero must report NO crossings. Treating zero
			// as its own sign would make silence look like white noise to the
			// one feature that exists to reject white noise.
			wantRMS: 0, wantPeak: 0, wantZCR: 0, tol: 1e-9,
		},
		{
			name:    "full-scale square wave",
			samples: square,
			// Every sample at full magnitude: RMS is 1. Every adjacent pair
			// changes sign: n-1 crossings over n-1 intervals is exactly 1.
			wantRMS: 1.0, wantPeak: 1.0, wantZCR: 1.0, tol: 1e-4,
		},
		{
			name:    "sine at amplitude 0.5",
			samples: halfSine,
			// RMS of a sine is A/√2.
			//
			// SEVEN crossings, not eight. Four cycles cross zero eight times,
			// but the eighth falls at sample index 160 — exactly the frame
			// boundary — and ZCR is measured strictly WITHIN a frame. This is
			// the boundary effect documented on FrameFeatures.ZCR, pinned here
			// so the documented behaviour and the measured behaviour cannot
			// drift apart.
			wantRMS: 0.5 / math.Sqrt2, wantPeak: 0.5, wantZCR: 7.0 / float64(n-1),
			tol: 5e-3,
		},
		{
			name:    "sine at amplitude 0.25",
			samples: quarterSine,
			// The crossing rate is a property of the waveform's frequency, not
			// its level: quartering the amplitude must not move it.
			wantRMS: 0.25 / math.Sqrt2, wantPeak: 0.25, wantZCR: 7.0 / float64(n-1),
			tol: 5e-3,
		},
		{
			name:    "constant DC offset",
			samples: dc,
			// A signal that never changes sign has no crossings, whatever its
			// level.
			wantRMS: 0.5, wantPeak: 0.5, wantZCR: 0, tol: 1e-3,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := a.Analyze(frameFromSamples(t, format, tc.samples))
			if err != nil {
				t.Fatalf("Analyze: %v", err)
			}

			if math.Abs(got.RMS-tc.wantRMS) > tc.tol {
				t.Errorf("RMS = %.6f, want %.6f (±%g)", got.RMS, tc.wantRMS, tc.tol)
			}
			if math.Abs(got.Peak-tc.wantPeak) > tc.tol {
				t.Errorf("Peak = %.6f, want %.6f (±%g)", got.Peak, tc.wantPeak, tc.tol)
			}
			if math.Abs(got.ZCR-tc.wantZCR) > tc.tol {
				t.Errorf("ZCR = %.6f, want %.6f (±%g)", got.ZCR, tc.wantZCR, tc.tol)
			}
			if got.Samples != n {
				t.Errorf("Samples = %d, want %d", got.Samples, n)
			}
			// Energy must be exactly RMS squared, or the two disagree about the
			// same signal and every downstream statistic inherits the argument.
			if math.Abs(got.Energy-got.RMS*got.RMS) > 1e-9 {
				t.Errorf("Energy %.9f is not RMS² (%.9f)", got.Energy, got.RMS*got.RMS)
			}
		})
	}
}

// TestFrameAnalyzer_CrestFactorSeparatesTonesFromBursts pins the flatness test.
func TestFrameAnalyzer_CrestFactorSeparatesTonesFromBursts(t *testing.T) {
	t.Parallel()

	format := media.PCM16Mono8k()
	a := mustAnalyzer(t, format)
	const n = 160

	// A sine's crest factor is exactly 3.01 dB — 20·log10(√2).
	sine := make([]float64, n)
	for i := range sine {
		sine[i] = 0.5 * math.Sin(2*math.Pi*4*float64(i)/float64(n))
	}
	f, err := a.Analyze(frameFromSamples(t, format, sine))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.CrestFactorDB(); math.Abs(got-3.01) > 0.1 {
		t.Errorf("sine crest factor = %.2f dB, want 3.01 dB", got)
	}

	// A burst in an otherwise quiet frame has a far higher crest factor, which
	// is the property that tells impulsive audio from a sustained tone.
	burst := make([]float64, n)
	for i := 0; i < n/20; i++ {
		burst[i] = 0.9
	}
	f, err = a.Analyze(frameFromSamples(t, format, burst))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.CrestFactorDB(); got < 10 {
		t.Errorf("burst crest factor = %.2f dB, want well above 10 dB", got)
	}
}

// TestFrameAnalyzer_DetectsClipping proves the clip counter sees a signal
// driven past full scale.
func TestFrameAnalyzer_DetectsClipping(t *testing.T) {
	t.Parallel()

	format := media.PCM16Mono8k()
	a := mustAnalyzer(t, format)
	const n = 100

	clean := make([]float64, n)
	for i := range clean {
		clean[i] = 0.5 * math.Sin(2*math.Pi*4*float64(i)/float64(n))
	}
	f, err := a.Analyze(frameFromSamples(t, format, clean))
	if err != nil {
		t.Fatal(err)
	}
	if f.ClipRatio != 0 {
		t.Errorf("clean signal reported ClipRatio %g, want 0", f.ClipRatio)
	}

	// Half the samples driven well past full scale.
	over := make([]float64, n)
	for i := range over {
		if i%2 == 0 {
			over[i] = 2.0
		} else {
			over[i] = 0.1
		}
	}
	f, err = a.Analyze(frameFromSamples(t, format, over))
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(f.ClipRatio-0.5) > 0.01 {
		t.Errorf("ClipRatio = %g, want 0.5", f.ClipRatio)
	}
}

// TestFrameAnalyzer_BothWidthsAgree proves PCM32 measures the same signal the
// same way PCM16 does.
//
// If they disagreed, every threshold in defaults.go would mean two different
// things depending on what the carrier happened to send.
func TestFrameAnalyzer_BothWidthsAgree(t *testing.T) {
	t.Parallel()

	const n = 160
	samples := make([]float64, n)
	for i := range samples {
		samples[i] = 0.4 * math.Sin(2*math.Pi*7*float64(i)/float64(n))
	}

	f16 := media.PCM16Mono8k()
	f32 := media.AudioFormat{Format: media.FormatPCM32, Layout: media.LayoutMono,
		Rate: media.Rate8kHz, Codec: media.CodecPCM}

	a16 := mustAnalyzer(t, f16)
	a32 := mustAnalyzer(t, f32)

	r16, err := a16.Analyze(frameFromSamples(t, f16, samples))
	if err != nil {
		t.Fatal(err)
	}
	r32, err := a32.Analyze(frameFromSamples(t, f32, samples))
	if err != nil {
		t.Fatal(err)
	}

	// The tolerance is the 16-bit quantisation step, which is the only reason
	// they should differ at all.
	const tol = 1e-4
	if math.Abs(r16.RMS-r32.RMS) > tol {
		t.Errorf("RMS differs by width: pcm16 %.6f, pcm32 %.6f", r16.RMS, r32.RMS)
	}
	if math.Abs(r16.Peak-r32.Peak) > tol {
		t.Errorf("Peak differs by width: pcm16 %.6f, pcm32 %.6f", r16.Peak, r32.Peak)
	}
	if math.Abs(r16.ZCR-r32.ZCR) > tol {
		t.Errorf("ZCR differs by width: pcm16 %.6f, pcm32 %.6f", r16.ZCR, r32.ZCR)
	}
}

// TestFrameAnalyzer_NegativeFullScaleNormalisesToMinusOne guards the two's
// complement asymmetry.
//
// int16 runs -32768..32767. Scaling by 32767 would put the most negative sample
// past -1.0, which makes "normalised to full scale" false exactly where it
// matters — at the clipping boundary the quality classifier watches.
func TestFrameAnalyzer_NegativeFullScaleNormalisesToMinusOne(t *testing.T) {
	t.Parallel()

	format := media.PCM16Mono8k()
	a := mustAnalyzer(t, format)

	// Written directly: -32768 cannot be produced by scaling a float in [-1,1]
	// through the writer's clamp path without relying on rounding.
	payload := make([]byte, 8)
	for i := 0; i < 4; i++ {
		payload[i*2] = 0x00
		payload[i*2+1] = 0x80 // -32768 little-endian
	}

	f, err := a.Analyze(media.Frame{Format: format, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(f.Peak-1.0) > 1e-9 {
		t.Errorf("most negative sample normalised to peak %.9f, want exactly 1.0", f.Peak)
	}
	if f.ClipRatio != 1.0 {
		t.Errorf("ClipRatio = %g for an all-full-scale frame, want 1.0", f.ClipRatio)
	}
}

func TestFrameAnalyzer_RefusesUnmeasurableFrames(t *testing.T) {
	t.Parallel()

	format := media.PCM16Mono8k()
	a := mustAnalyzer(t, format)

	t.Run("empty payload", func(t *testing.T) {
		t.Parallel()
		_, err := a.Analyze(media.Frame{Format: format})
		if !errors.Is(err, ErrInvalidFrame) {
			t.Fatalf("err = %v, want ErrInvalidFrame", err)
		}
	})

	t.Run("format mismatch", func(t *testing.T) {
		t.Parallel()
		_, err := a.Analyze(media.Frame{
			Format:  media.PCM16Mono16k(),
			Payload: make([]byte, 32),
		})
		if !errors.Is(err, ErrFormatMismatch) {
			t.Fatalf("err = %v, want ErrFormatMismatch", err)
		}
	})

	t.Run("partial sample", func(t *testing.T) {
		t.Parallel()
		// An odd byte count in a 2-byte format: the producer and this engine
		// disagree about the format, and measuring the truncated remainder
		// would report a plausible wrong number.
		_, err := a.Analyze(media.Frame{Format: format, Payload: make([]byte, 33)})
		if !errors.Is(err, ErrInvalidFrame) {
			t.Fatalf("err = %v, want ErrInvalidFrame", err)
		}
	})

	t.Run("single sample has no crossing rate", func(t *testing.T) {
		t.Parallel()
		f, err := a.Analyze(media.Frame{Format: format, Payload: []byte{0x00, 0x40}})
		if err != nil {
			t.Fatal(err)
		}
		if f.ZCR != 0 {
			t.Errorf("ZCR = %g for a one-sample frame; there is no interval to "+
				"measure a crossing across", f.ZCR)
		}
	})
}

func TestNewFrameAnalyzer_RefusesUnanalysableFormats(t *testing.T) {
	t.Parallel()

	for _, f := range []media.AudioFormat{
		{Format: media.FormatPCM16, Layout: media.LayoutStereo,
			Rate: media.Rate8kHz, Codec: media.CodecPCM},
		{Format: media.FormatPCM16, Layout: media.LayoutMono,
			Rate: media.Rate8kHz, Codec: media.CodecOpaque},
		{},
	} {
		if _, err := NewFrameAnalyzer(f); err == nil {
			t.Errorf("format %s was accepted; it must fail closed", f)
		}
	}
}

// TestFrameAnalyzer_PreservesPhase11BMetadata proves the transport signals
// survive analysis untouched.
//
// FlagSilence is the one that matters most: it means 11B INVENTED the frame to
// cover a gap, and a detector that treated invented silence as measured silence
// would end a caller's turn because of a network glitch.
func TestFrameAnalyzer_PreservesPhase11BMetadata(t *testing.T) {
	t.Parallel()

	format := media.PCM16Mono8k()
	a := mustAnalyzer(t, format)
	arrival := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)

	f, err := a.Analyze(media.Frame{
		Sequence:  4242,
		Timestamp: 3 * time.Second,
		Arrival:   arrival,
		Format:    format,
		Payload:   make([]byte, 320),
		Flags:     media.FlagSilence | media.FlagDiscontinuity,
	})
	if err != nil {
		t.Fatal(err)
	}

	if f.Sequence != 4242 {
		t.Errorf("Sequence = %d, want 4242", f.Sequence)
	}
	if f.Timestamp != 3*time.Second {
		t.Errorf("Timestamp = %s, want 3s", f.Timestamp)
	}
	if !f.Arrival.Equal(arrival) {
		t.Errorf("Arrival = %s, want %s", f.Arrival, arrival)
	}
	if !f.Synthesised() {
		t.Error("FlagSilence was lost; invented silence would be measured as real")
	}
	if !f.Discontinuous() {
		t.Error("FlagDiscontinuity was lost")
	}
	if want := 20 * time.Millisecond; f.Duration != want {
		t.Errorf("Duration = %s, want %s", f.Duration, want)
	}
	if got, want := f.End(), 3*time.Second+20*time.Millisecond; got != want {
		t.Errorf("End() = %s, want %s", got, want)
	}
}

// TestFrameAnalyzer_IsDeterministic is the base case for §14's determinism
// requirement: the same bytes must always produce the same numbers.
func TestFrameAnalyzer_IsDeterministic(t *testing.T) {
	t.Parallel()

	format := media.PCM16Mono8k()
	a := mustAnalyzer(t, format)

	gen := NewSignalGenerator(format, 20*time.Millisecond)
	frames := gen.NormalSpeech(50)

	first := make([]FrameFeatures, len(frames))
	for i, f := range frames {
		got, err := a.Analyze(f)
		if err != nil {
			t.Fatal(err)
		}
		first[i] = got
	}

	// A second analyser, a second pass, byte-identical results.
	b := mustAnalyzer(t, format)
	for i, f := range frames {
		got, err := b.Analyze(f)
		if err != nil {
			t.Fatal(err)
		}
		if got != first[i] {
			t.Fatalf("frame %d measured differently on the second pass:\n first: %+v\nsecond: %+v",
				i, first[i], got)
		}
	}
}

// TestSignalGenerator_ProducesTheStatisticsTheDetectorsRelyOn is the fixture
// contract.
//
// Every VAD test below rests on these signals having the properties their names
// claim. If the generator drifted — if "speech" stopped being modulated, or
// "noise" started being — the detector tests would keep passing while testing
// something else entirely. This asserts the fixtures themselves.
func TestSignalGenerator_ProducesTheStatisticsTheDetectorsRelyOn(t *testing.T) {
	t.Parallel()

	format := media.PCM16Mono8k()
	a := mustAnalyzer(t, format)
	const interval = 20 * time.Millisecond

	measure := func(frames []media.Frame) (meanRMS, meanZCR, modulation float64) {
		energies := make([]float64, 0, len(frames))
		var sumRMS, sumZCR float64
		for _, f := range frames {
			feat, err := a.Analyze(f)
			if err != nil {
				t.Fatal(err)
			}
			sumRMS += feat.RMS
			sumZCR += feat.ZCR
			energies = append(energies, feat.Energy)
		}
		n := float64(len(frames))
		meanRMS, meanZCR = sumRMS/n, sumZCR/n

		var mean float64
		for _, e := range energies {
			mean += e
		}
		mean /= n
		var variance float64
		for _, e := range energies {
			variance += (e - mean) * (e - mean)
		}
		variance /= n
		if mean > 0 {
			modulation = math.Sqrt(variance) / mean
		}
		return
	}

	t.Run("speech is strongly modulated and sits in the ZCR band", func(t *testing.T) {
		t.Parallel()
		g := NewSignalGenerator(format, interval)
		rms, zcr, mod := measure(g.NormalSpeech(100))

		if rms < defaultAbsoluteSilenceRMS*10 {
			t.Errorf("speech RMS %.5f is barely above the absolute silence floor", rms)
		}
		if mod < defaultMinEnergyModulation {
			t.Errorf("speech modulation %.3f is below MinEnergyModulation %.3f; the "+
				"fixture is not syllabic and every VAD test below is testing the "+
				"wrong thing", mod, defaultMinEnergyModulation)
		}
		if zcr < defaultZCRMin || zcr > defaultZCRMax {
			t.Errorf("speech ZCR %.3f is outside the band [%.2f, %.2f]",
				zcr, defaultZCRMin, defaultZCRMax)
		}
	})

	t.Run("stationary noise is not modulated", func(t *testing.T) {
		t.Parallel()
		g := NewSignalGenerator(format, interval)
		_, _, mod := measure(g.Noise(0.05, 100))

		if mod >= defaultMinEnergyModulation {
			t.Errorf("stationary noise modulation %.3f reaches MinEnergyModulation "+
				"%.3f; the fixture is not stationary and the noise-rejection tests "+
				"prove nothing", mod, defaultMinEnergyModulation)
		}
	})

	t.Run("a pure tone sits below the ZCR band", func(t *testing.T) {
		t.Parallel()
		g := NewSignalGenerator(format, interval)
		// 400 Hz at 8 kHz: 800 crossings per second over 8000 sample intervals
		// is a ZCR of 0.1... which is INSIDE the band. A low tone is what sits
		// below it, and that is the case worth pinning.
		_, zcr, _ := measure(g.Tone(30, 0.3, 50))

		if zcr >= defaultZCRMin {
			t.Errorf("a 30 Hz tone measured ZCR %.4f, at or above ZCRMin %.2f; the "+
				"feature that exists to reject tones would not reject it",
				zcr, defaultZCRMin)
		}
	})

	t.Run("clipped audio clips", func(t *testing.T) {
		t.Parallel()
		g := NewSignalGenerator(format, interval)
		frames := g.Clipped(50)

		var clipped int
		for _, f := range frames {
			feat, err := a.Analyze(f)
			if err != nil {
				t.Fatal(err)
			}
			if feat.ClipRatio > defaultDegradedClipRatio {
				clipped++
			}
		}
		if clipped == 0 {
			t.Error("the clipping fixture produced no frame above DegradedClipRatio")
		}
	})

	t.Run("silence is silent", func(t *testing.T) {
		t.Parallel()
		g := NewSignalGenerator(format, interval)
		rms, zcr, _ := measure(g.Silence(50))

		if rms != 0 {
			t.Errorf("silence RMS = %g, want exactly 0", rms)
		}
		if zcr != 0 {
			t.Errorf("silence ZCR = %g, want exactly 0", zcr)
		}
	})
}

// TestSignalGenerator_IsReproducible is what makes every scenario below a
// deterministic test rather than a sampling exercise.
func TestSignalGenerator_IsReproducible(t *testing.T) {
	t.Parallel()

	format := media.PCM16Mono8k()
	const interval = 20 * time.Millisecond

	a := NewSignalGenerator(format, interval).SpeechOverNoise(0.2, 0.05, 20)
	b := NewSignalGenerator(format, interval).SpeechOverNoise(0.2, 0.05, 20)

	if len(a) != len(b) {
		t.Fatalf("frame counts differ: %d and %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Sequence != b[i].Sequence || a[i].Timestamp != b[i].Timestamp {
			t.Fatalf("frame %d metadata differs between runs", i)
		}
		if string(a[i].Payload) != string(b[i].Payload) {
			t.Fatalf("frame %d payload differs between runs; the generator is not "+
				"reproducible and every fixture-based assertion is a coin flip", i)
		}
	}
}

// TestSignalGenerator_SkipLeavesAGap proves the missing-frame fixture actually
// removes sequence numbers.
func TestSignalGenerator_SkipLeavesAGap(t *testing.T) {
	t.Parallel()

	g := NewSignalGenerator(media.PCM16Mono8k(), 20*time.Millisecond)
	before := g.Silence(3)
	g.Skip(2)
	after := g.Silence(3)

	if got, want := before[2].Sequence, uint64(2); got != want {
		t.Fatalf("last pre-gap sequence = %d, want %d", got, want)
	}
	if got, want := after[0].Sequence, uint64(5); got != want {
		t.Fatalf("first post-gap sequence = %d, want %d (two skipped)", got, want)
	}
	if got, want := after[0].Timestamp, 100*time.Millisecond; got != want {
		t.Fatalf("first post-gap timestamp = %s, want %s — the media clock must "+
			"advance across a gap or the gap becomes invisible", got, want)
	}
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

// BenchmarkFrameAnalyzer_Analyze measures the per-frame cost of the hot path.
//
// §19 requires the hot path to be bounded. The allocation count is the number
// that matters: at fifty frames a second across a thousand sessions, one
// allocation per frame is fifty thousand allocations a second of garbage, and a
// GC pause in an audio path is audible.
func BenchmarkFrameAnalyzer_Analyze(b *testing.B) {
	format := media.PCM16Mono8k()
	a, err := NewFrameAnalyzer(format)
	if err != nil {
		b.Fatal(err)
	}

	frames := NewSignalGenerator(format, 20*time.Millisecond).NormalSpeech(64)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := a.Analyze(frames[i%len(frames)]); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFrameAnalyzer_AnalyzePCM32(b *testing.B) {
	format := media.AudioFormat{Format: media.FormatPCM32, Layout: media.LayoutMono,
		Rate: media.Rate16kHz, Codec: media.CodecPCM}
	a, err := NewFrameAnalyzer(format)
	if err != nil {
		b.Fatal(err)
	}

	frames := NewSignalGenerator(format, 20*time.Millisecond).NormalSpeech(64)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := a.Analyze(frames[i%len(frames)]); err != nil {
			b.Fatal(err)
		}
	}
}
