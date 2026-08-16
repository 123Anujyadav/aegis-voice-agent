package audiointel

import (
	"math"
	"testing"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
)

// featuresWithRMS builds features at a chosen level, for tests about the
// estimator's arithmetic rather than about measurement.
func featuresWithRMS(seq uint64, rms float64) FrameFeatures {
	return FrameFeatures{
		Sequence:  seq,
		Timestamp: time.Duration(seq) * 20 * time.Millisecond,
		Duration:  20 * time.Millisecond,
		RMS:       rms,
		Energy:    rms * rms,
		Peak:      rms * 2,
		Samples:   160,
	}
}

func mustNoiseAnalyzer(t *testing.T, cfg Config) *NoiseAnalyzer {
	t.Helper()
	n, err := NewNoiseAnalyzer(cfg)
	if err != nil {
		t.Fatalf("NewNoiseAnalyzer: %v", err)
	}
	return n
}

// TestNoise_ConvergesWithinWarmup proves the estimator reaches a usable floor
// on the schedule its configuration promises.
func TestNoise_ConvergesWithinWarmup(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(media.PCM16Mono8k())
	n := mustNoiseAnalyzer(t, cfg)

	const background = 0.01

	for i := 0; i < cfg.Noise.WarmupFrames-1; i++ {
		e := n.Observe(featuresWithRMS(uint64(i), background), false)
		if e.Converged {
			t.Fatalf("converged after %d frames, before the configured warm-up of %d",
				i+1, cfg.Noise.WarmupFrames)
		}
		if e.Class != NoiseUnknown {
			t.Errorf("frame %d classified %s during warm-up, want %s",
				i, e.Class, NoiseUnknown)
		}
		if e.Confidence != 0 {
			t.Errorf("frame %d reported confidence %g during warm-up, want 0; a floor "+
				"that does not exist yet cannot be trusted at all", i, e.Confidence)
		}
	}

	e := n.Observe(featuresWithRMS(uint64(cfg.Noise.WarmupFrames-1), background), false)
	if !e.Converged {
		t.Fatalf("not converged after the full warm-up of %d frames", cfg.Noise.WarmupFrames)
	}
	if math.Abs(e.Floor-background) > 1e-9 {
		t.Errorf("floor = %.9f after warm-up on a constant %g background, want %g",
			e.Floor, background, background)
	}
}

// TestNoise_WarmupUsesTheMinimumNotTheMean is the property that stops a caller
// saying hello during warm-up from defining the floor as their voice.
//
// There is no speech gate during warm-up — the detector that provides it does
// not exist yet — so the only available defence is that speech ADDS energy and
// the quietest observed moment is therefore the best estimate of what lies
// underneath.
func TestNoise_WarmupUsesTheMinimumNotTheMean(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(media.PCM16Mono8k())
	n := mustNoiseAnalyzer(t, cfg)

	const quiet = 0.005
	const loud = 0.30

	// Most of the warm-up is somebody talking; three frames are the gaps
	// between their words.
	var e NoiseEstimate
	for i := 0; i < cfg.Noise.WarmupFrames; i++ {
		level := loud
		if i%5 == 0 {
			level = quiet
		}
		e = n.Observe(featuresWithRMS(uint64(i), level), false)
	}

	if !e.Converged {
		t.Fatal("not converged after warm-up")
	}
	if math.Abs(e.Floor-quiet) > 1e-9 {
		t.Errorf("floor = %.6f, want the minimum %g\n"+
			"the mean of this warm-up is about %.3f — converging there would put the "+
			"floor inside the caller's voice and nothing would ever be detected",
			e.Floor, quiet, (loud*4+quiet)/5)
	}
}

// TestNoise_SpeechDoesNotMoveTheFloor is the primary defence, checked directly.
func TestNoise_SpeechDoesNotMoveTheFloor(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(media.PCM16Mono8k())
	n := mustNoiseAnalyzer(t, cfg)

	const background = 0.008
	for i := 0; i < cfg.Noise.WarmupFrames; i++ {
		n.Observe(featuresWithRMS(uint64(i), background), false)
	}
	before := n.Floor()

	// Five seconds of loud speech, gated.
	for i := 0; i < 250; i++ {
		n.Observe(featuresWithRMS(uint64(100+i), 0.45), true)
	}

	if n.Floor() != before {
		t.Errorf("the floor moved from %.9f to %.9f across 250 gated speech frames; "+
			"the gate is the primary defence and it must be absolute",
			before, n.Floor())
	}
}

// TestNoise_OneLoudFrameCannotRedefineTheFloor proves the hard rise clamp holds
// even when the speech gate fails completely.
//
// §4 of the phase brief in one test: "Do not allow a sudden loud speech frame to
// permanently redefine the noise floor."
func TestNoise_OneLoudFrameCannotRedefineTheFloor(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(media.PCM16Mono8k())
	n := mustNoiseAnalyzer(t, cfg)

	const background = 0.005
	for i := 0; i < cfg.Noise.WarmupFrames; i++ {
		n.Observe(featuresWithRMS(uint64(i), background), false)
	}
	before := n.Floor()

	// A full-scale frame presented as BACKGROUND — the gate has failed, which
	// is the case this clamp exists for.
	n.Observe(featuresWithRMS(999, 1.0), false)
	after := n.Floor()

	// The clamp permits at most MaxRiseDBPerSecond × one frame interval.
	maxRiseDB := cfg.Noise.MaxRiseDBPerSecond * cfg.FrameInterval.Seconds()
	gotRiseDB := dbRatio(after, before)

	if gotRiseDB > maxRiseDB+1e-9 {
		t.Errorf("one full-scale frame raised the floor by %.4f dB; the clamp permits "+
			"at most %.4f dB per frame", gotRiseDB, maxRiseDB)
	}
	if after <= before {
		t.Error("the floor did not move at all; the estimator should adapt, just slowly")
	}

	// And it must recover: the fast downward rate brings it back once the
	// contamination stops.
	for i := 0; i < 100; i++ {
		n.Observe(featuresWithRMS(uint64(2000+i), background), false)
	}
	if math.Abs(n.Floor()-background) > background*0.1 {
		t.Errorf("after 100 quiet frames the floor is %.6f, want close to %g; the "+
			"contamination was not recovered", n.Floor(), background)
	}
}

// TestNoise_AdaptationIsAsymmetric pins the direction that matters.
//
// A floor stuck ABOVE the true level suppresses real speech, and a missed word
// makes a caller repeat themselves. A floor stuck BELOW it causes a false
// trigger, which costs a wasted cycle. The rates encode that asymmetry, and
// this proves they do.
func TestNoise_AdaptationIsAsymmetric(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(media.PCM16Mono8k())

	const start = 0.02
	const target = 0.002 // a tenth: the room got quiet

	down := mustNoiseAnalyzer(t, cfg)
	for i := 0; i < cfg.Noise.WarmupFrames; i++ {
		down.Observe(featuresWithRMS(uint64(i), start), false)
	}
	for i := 0; i < 50; i++ {
		down.Observe(featuresWithRMS(uint64(100+i), target), false)
	}
	fellBy := dbRatio(start, down.Floor())

	up := mustNoiseAnalyzer(t, cfg)
	for i := 0; i < cfg.Noise.WarmupFrames; i++ {
		up.Observe(featuresWithRMS(uint64(i), target), false)
	}
	for i := 0; i < 50; i++ {
		up.Observe(featuresWithRMS(uint64(100+i), start), false)
	}
	roseBy := dbRatio(up.Floor(), target)

	if fellBy <= roseBy {
		t.Errorf("in 50 frames the floor fell %.2f dB and rose %.2f dB; downward "+
			"adaptation must be the faster of the two", fellBy, roseBy)
	}
}

// TestNoise_IgnoresSynthesisedFrames proves invented silence does not drag the
// floor toward digital zero.
//
// Phase 11B fills a gap with zeros this engine generated. Folding them in would
// pull the floor down every time the network hiccuped, and the next real frame
// would then look like a shout — turning a packet loss into a false speech
// onset.
func TestNoise_IgnoresSynthesisedFrames(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(media.PCM16Mono8k())
	n := mustNoiseAnalyzer(t, cfg)

	const background = 0.01
	for i := 0; i < cfg.Noise.WarmupFrames; i++ {
		n.Observe(featuresWithRMS(uint64(i), background), false)
	}
	before := n.Floor()

	for i := 0; i < 50; i++ {
		f := featuresWithRMS(uint64(100+i), 0)
		f.Flags = media.FlagSilence | media.FlagDiscontinuity
		n.Observe(f, false)
	}

	if n.Floor() != before {
		t.Errorf("50 synthesised frames moved the floor from %.9f to %.9f; frames "+
			"Phase 11B invented are not evidence about the line",
			before, n.Floor())
	}
}

// TestNoise_ClampsHoldTheEstimateInRange guards the digital-silence case, where
// the measured level is zero and every ratio against it would be an infinity.
func TestNoise_ClampsHoldTheEstimateInRange(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(media.PCM16Mono8k())

	t.Run("digital silence cannot drive the floor to zero", func(t *testing.T) {
		t.Parallel()
		n := mustNoiseAnalyzer(t, cfg)
		for i := 0; i < 500; i++ {
			n.Observe(featuresWithRMS(uint64(i), 0), false)
		}
		if n.Floor() < cfg.Noise.MinFloor {
			t.Errorf("floor = %g, below MinFloor %g", n.Floor(), cfg.Noise.MinFloor)
		}
		// And the SNR of a subsequent real frame must be finite.
		e := n.Observe(featuresWithRMS(999, 0.3), false)
		if math.IsInf(e.SNRDB, 0) || math.IsNaN(e.SNRDB) {
			t.Errorf("SNR against a silent-line floor = %v, want a finite number", e.SNRDB)
		}
	})

	t.Run("a broken input cannot drive the floor above MaxFloor", func(t *testing.T) {
		t.Parallel()
		n := mustNoiseAnalyzer(t, cfg)
		for i := 0; i < 5000; i++ {
			n.Observe(featuresWithRMS(uint64(i), 1.0), false)
		}
		if n.Floor() > cfg.Noise.MaxFloor {
			t.Errorf("floor = %g, above MaxFloor %g", n.Floor(), cfg.Noise.MaxFloor)
		}
	})
}

// TestNoise_ConfidenceReflectsCoverageAndStability checks the two factors
// separately, because they mean different things and a consumer acts on both.
func TestNoise_ConfidenceReflectsCoverageAndStability(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(media.PCM16Mono8k())

	t.Run("confidence grows with observation", func(t *testing.T) {
		t.Parallel()
		n := mustNoiseAnalyzer(t, cfg)

		var last float64
		for i := 0; i < cfg.Noise.ConfidenceFrames; i++ {
			e := n.Observe(featuresWithRMS(uint64(i), 0.01), false)
			if e.Converged && e.Confidence < last-1e-12 {
				t.Fatalf("confidence fell from %g to %g on a perfectly steady "+
					"background at frame %d", last, e.Confidence, i)
			}
			if e.Converged {
				last = e.Confidence
			}
		}
		if last < 0.9 {
			t.Errorf("confidence after ConfidenceFrames of steady background = %.3f, "+
				"want close to 1", last)
		}
	})

	t.Run("an unsteady background lowers confidence", func(t *testing.T) {
		t.Parallel()

		steady := mustNoiseAnalyzer(t, cfg)
		erratic := mustNoiseAnalyzer(t, cfg)

		var steadyEst, erraticEst NoiseEstimate
		for i := 0; i < 200; i++ {
			steadyEst = steady.Observe(featuresWithRMS(uint64(i), 0.01), false)

			level := 0.001
			if i%3 == 0 {
				level = 0.08
			}
			erraticEst = erratic.Observe(featuresWithRMS(uint64(i), level), false)
		}

		if erraticEst.Confidence >= steadyEst.Confidence {
			t.Errorf("erratic background confidence %.3f is not below steady %.3f",
				erraticEst.Confidence, steadyEst.Confidence)
		}
		if erraticEst.Stability >= steadyEst.Stability {
			t.Errorf("erratic stability %.3f is not below steady %.3f",
				erraticEst.Stability, steadyEst.Stability)
		}
	})
}

// TestNoise_Classification walks every reachable class.
func TestNoise_Classification(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(media.PCM16Mono8k())

	cases := []struct {
		name  string
		drive func(n *NoiseAnalyzer) NoiseEstimate
		want  NoiseClass
	}{
		{
			name: "before warm-up nothing is known",
			drive: func(n *NoiseAnalyzer) NoiseEstimate {
				return n.Observe(featuresWithRMS(0, 0.01), false)
			},
			want: NoiseUnknown,
		},
		{
			name: "a quiet line",
			// Below QuietFloor (1e-3) but still above MinSignalRMS (5e-4).
			// A quiet room is not the same thing as a muted handset, and the
			// two classes are separated by exactly that boundary.
			drive: func(n *NoiseAnalyzer) NoiseEstimate {
				var e NoiseEstimate
				for i := 0; i < 100; i++ {
					e = n.Observe(featuresWithRMS(uint64(i), 6e-4), false)
				}
				return e
			},
			want: NoiseQuiet,
		},
		{
			name: "a fan",
			drive: func(n *NoiseAnalyzer) NoiseEstimate {
				var e NoiseEstimate
				for i := 0; i < 100; i++ {
					e = n.Observe(featuresWithRMS(uint64(i), 0.02), false)
				}
				return e
			},
			want: NoiseStationary,
		},
		{
			name: "a busy street",
			drive: func(n *NoiseAnalyzer) NoiseEstimate {
				var e NoiseEstimate
				for i := 0; i < 200; i++ {
					level := 0.002
					if i%4 == 0 {
						level = 0.2
					}
					e = n.Observe(featuresWithRMS(uint64(i), level), false)
				}
				return e
			},
			want: NoiseTransient,
		},
		{
			name: "an overdriven input",
			drive: func(n *NoiseAnalyzer) NoiseEstimate {
				for i := 0; i < 50; i++ {
					n.Observe(featuresWithRMS(uint64(i), 0.02), false)
				}
				f := featuresWithRMS(100, 0.9)
				f.ClipRatio = 0.05
				return n.Observe(f, false)
			},
			want: NoiseClipping,
		},
		{
			name: "a muted handset",
			drive: func(n *NoiseAnalyzer) NoiseEstimate {
				var e NoiseEstimate
				// Both signal and background below the usable level.
				for i := 0; i < 100; i++ {
					e = n.Observe(featuresWithRMS(uint64(i), 1e-5), false)
				}
				return e
			},
			want: NoiseVeryLow,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.drive(mustNoiseAnalyzer(t, cfg))
			if got.Class != tc.want {
				t.Errorf("class = %s, want %s (floor %.6f, stability %.3f)",
					got.Class, tc.want, got.Floor, got.Stability)
			}
		})
	}
}

// TestNoise_SNRIsTheCoreComparison checks the number every speech decision
// reduces to.
func TestNoise_SNRIsTheCoreComparison(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(media.PCM16Mono8k())
	n := mustNoiseAnalyzer(t, cfg)

	const background = 0.01
	for i := 0; i < cfg.Noise.WarmupFrames; i++ {
		n.Observe(featuresWithRMS(uint64(i), background), false)
	}

	// A frame ten times the floor is exactly 20 dB above it.
	e := n.Observe(featuresWithRMS(500, background*10), true)
	if math.Abs(e.SNRDB-20) > 0.01 {
		t.Errorf("SNR of a 10x frame = %.3f dB, want 20 dB", e.SNRDB)
	}

	// A frame at the floor is exactly 0 dB above it.
	e = n.Observe(featuresWithRMS(501, background), true)
	if math.Abs(e.SNRDB) > 0.01 {
		t.Errorf("SNR of a frame at the floor = %.3f dB, want 0 dB", e.SNRDB)
	}
}

// TestNoise_ResetKeepsItsStorage guards against an allocation during session
// recovery.
func TestNoise_ResetKeepsItsStorage(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(media.PCM16Mono8k())
	n := mustNoiseAnalyzer(t, cfg)

	for i := 0; i < 200; i++ {
		n.Observe(featuresWithRMS(uint64(i), 0.05), false)
	}
	before := &n.ring[0]

	n.Reset()

	if n.Converged() {
		t.Error("Reset left the estimator converged")
	}
	if n.Observations() != 0 {
		t.Errorf("Reset left %d observations", n.Observations())
	}
	if &n.ring[0] != before {
		t.Error("Reset reallocated the ring")
	}
	if n.Floor() != cfg.Noise.MinFloor {
		t.Errorf("Reset left the floor at %g, want MinFloor %g",
			n.Floor(), cfg.Noise.MinFloor)
	}
}

// TestNoise_IsDeterministic proves two estimators fed the same frames agree
// exactly.
func TestNoise_IsDeterministic(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(media.PCM16Mono8k())
	a := mustNoiseAnalyzer(t, cfg)
	b := mustNoiseAnalyzer(t, cfg)

	for i := 0; i < 500; i++ {
		f := featuresWithRMS(uint64(i), 0.003+float64(i%17)*0.0011)
		speech := i%23 < 7

		ea := a.Observe(f, speech)
		eb := b.Observe(f, speech)
		if ea != eb {
			t.Fatalf("frame %d: estimates diverged\n a: %+v\n b: %+v", i, ea, eb)
		}
	}
}

func TestNewNoiseAnalyzer_RefusesInvalidConfiguration(t *testing.T) {
	t.Parallel()

	base := DefaultConfig(media.PCM16Mono8k())

	bad := base
	bad.Noise.WarmupFrames = 0
	if _, err := NewNoiseAnalyzer(bad); err == nil {
		t.Error("an invalid noise configuration was accepted")
	}

	bad = base
	bad.FrameInterval = 0
	if _, err := NewNoiseAnalyzer(bad); err == nil {
		t.Error("a zero frame interval was accepted; the rise clamp cannot be " +
			"computed without it")
	}
}

func BenchmarkNoiseAnalyzer_Observe(b *testing.B) {
	cfg := DefaultConfig(media.PCM16Mono8k())
	n, err := NewNoiseAnalyzer(cfg)
	if err != nil {
		b.Fatal(err)
	}

	frames := make([]FrameFeatures, 128)
	for i := range frames {
		frames[i] = featuresWithRMS(uint64(i), 0.01+float64(i%11)*0.002)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n.Observe(frames[i%len(frames)], i%7 == 0)
	}
}

// TestNoise_RetractionRemovesTheOnsetLeak guards a defect found by measurement,
// not by inspection.
//
// The speech gate lags one frame, so MinOnsetFrames frames of every utterance
// reach the estimator labelled as background. Two loud frames in a hundred-frame
// ring roughly HALVE the coefficient-of-variation stability score, and that
// score multiplies into every downstream confidence — so the confidence of every
// speech, endpoint and overlap decision dropped by half for two seconds at the
// start of every utterance.
//
// It presented as overlap detection never reaching Confirmed. See
// docs/audio-intelligence/ENGINEERING_AUDIT.md D-2.
func TestNoise_RetractionRemovesTheOnsetLeak(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(media.PCM16Mono8k())
	const background = 0.01

	settle := func(n *NoiseAnalyzer) NoiseEstimate {
		var e NoiseEstimate
		for i := 0; i < cfg.Noise.ConfidenceFrames*2; i++ {
			e = n.Observe(featuresWithRMS(uint64(i), background), false)
		}
		return e
	}

	// A clean baseline: nothing but background.
	clean := mustNoiseAnalyzer(t, cfg)
	baseline := settle(clean)

	// The same, then two loud frames leak past the gate.
	leaked := mustNoiseAnalyzer(t, cfg)
	settle(leaked)
	var afterLeak NoiseEstimate
	for i := 0; i < cfg.VAD.MinOnsetFrames; i++ {
		afterLeak = leaked.Observe(featuresWithRMS(uint64(1000+i), 0.4), false)
	}

	if afterLeak.Confidence >= baseline.Confidence {
		t.Fatalf("setup: the leak did not lower confidence (%.3f vs %.3f), so this "+
			"test proves nothing", afterLeak.Confidence, baseline.Confidence)
	}
	t.Logf("%d leaked frames dropped confidence from %.3f to %.3f",
		cfg.VAD.MinOnsetFrames, baseline.Confidence, afterLeak.Confidence)

	// Retracting them restores the estimate to the clean baseline exactly.
	leaked.Retract(cfg.VAD.MinOnsetFrames)
	restored := leaked.Observe(featuresWithRMS(2000, background), true)

	if math.Abs(restored.Stability-baseline.Stability) > 1e-9 {
		t.Errorf("after retraction stability is %.6f, want the clean baseline "+
			"%.6f", restored.Stability, baseline.Stability)
	}
	if restored.Confidence < afterLeak.Confidence {
		t.Errorf("retraction lowered confidence further, from %.3f to %.3f",
			afterLeak.Confidence, restored.Confidence)
	}
}

// TestNoise_RetractionIsBounded checks the edges rather than only the happy path.
func TestNoise_RetractionIsBounded(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(media.PCM16Mono8k())
	n := mustNoiseAnalyzer(t, cfg)

	// Retracting from an empty estimator must not underflow.
	n.Retract(5)
	if n.Observations() != 0 {
		t.Errorf("Observations = %d after retracting from empty, want 0", n.Observations())
	}

	for i := 0; i < 20; i++ {
		n.Observe(featuresWithRMS(uint64(i), 0.01), false)
	}
	before := n.Observations()

	// Retracting more than is held must clamp, not wrap.
	n.Retract(1000)
	if n.Observations() != 0 {
		t.Errorf("Observations = %d after over-retracting %d, want 0",
			n.Observations(), before)
	}
	// And zero or negative is a no-op.
	n.Observe(featuresWithRMS(100, 0.01), false)
	got := n.Observations()
	n.Retract(0)
	n.Retract(-3)
	if n.Observations() != got {
		t.Errorf("a non-positive retraction changed the count from %d to %d",
			got, n.Observations())
	}
}
