package audiointel

import (
	"math"
	"testing"
)

func mustQualityAnalyzer(t *testing.T, cfg QualityThresholds) *QualityAnalyzer {
	t.Helper()
	q, err := NewQualityAnalyzer(cfg)
	if err != nil {
		t.Fatalf("NewQualityAnalyzer: %v", err)
	}
	return q
}

// qualityView builds a signal view with chosen measurements, for tests about
// the classifier's thresholds rather than about measurement.
func qualityView(rms, excessDB, clipRatio, crestDB float64) SignalView {
	return SignalView{
		Ready: true,
		Frame: FrameFeatures{
			RMS:       rms,
			Peak:      rms * dbToRatio(crestDB),
			Energy:    rms * rms,
			ClipRatio: clipRatio,
			Samples:   160,
			Duration:  testInterval,
		},
		Window: SignalStats{
			Frames:     100,
			MeanRMS:    rms,
			MaxRMS:     rms,
			MeanEnergy: rms * rms,
			ClipRatio:  clipRatio,
		},
		Noise:    NoiseEstimate{Converged: true, Confidence: 1},
		ExcessDB: excessDB,
	}
}

// dbToRatio converts a decibel figure to an amplitude ratio.
func dbToRatio(db float64) float64 { return math.Pow(10, db/20) }

// TestQuality_EveryClassIsReachable walks the four classes from measurable
// inputs.
func TestQuality_EveryClassIsReachable(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat()).Quality

	cases := []struct {
		name     string
		view     SignalView
		gapRatio float64
		want     QualityClass
		reason   string
	}{
		{
			name:   "a clean line",
			view:   qualityView(0.05, 30, 0, 12),
			want:   QualityGood,
			reason: "",
		},
		{
			name:   "occasional clipping",
			view:   qualityView(0.5, 30, cfg.DegradedClipRatio, 12),
			want:   QualityDegraded,
			reason: ReasonClipping,
		},
		{
			name:     "occasional frame loss",
			view:     qualityView(0.05, 30, 0, 12),
			gapRatio: cfg.DegradedGapRatio,
			want:     QualityDegraded,
			reason:   ReasonFrameLoss,
		},
		{
			name:   "a working-hard signal-to-noise ratio",
			view:   qualityView(0.05, cfg.DegradedSNRDB-1, 0, 12),
			want:   QualityDegraded,
			reason: ReasonLowSNR,
		},
		{
			name:   "a suspiciously flat signal",
			view:   qualityView(0.05, 30, 0, cfg.MinDynamicRange-1),
			want:   QualityDegraded,
			reason: ReasonFlatSignal,
		},
		{
			name:   "buried in noise",
			view:   qualityView(0.05, cfg.MinSNRDB-1, 0, 12),
			want:   QualityPoor,
			reason: ReasonLowSNR,
		},
		{
			name:   "too quiet to use",
			view:   qualityView(cfg.MinSignalRMS/2, 30, 0, 12),
			want:   QualityPoor,
			reason: ReasonLowLevel,
		},
		{
			name:   "destroyed by clipping",
			view:   qualityView(0.9, 30, cfg.MaxClipRatio, 12),
			want:   QualityUnusable,
			reason: ReasonClipping,
		},
		{
			name:     "arriving in pieces",
			view:     qualityView(0.05, 30, 0, 12),
			gapRatio: cfg.MaxGapRatio,
			want:     QualityUnusable,
			reason:   ReasonFrameLoss,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := mustQualityAnalyzer(t, cfg)

			// Enough frames to clear the hysteresis and the evidence minimum.
			var last QualityReport
			for i := 0; i < cfg.HysteresisFrames*3; i++ {
				last = q.Observe(tc.view, tc.gapRatio)
			}

			if last.Class != tc.want {
				t.Errorf("class = %s, want %s\n%s", last.Class, tc.want, last)
			}
			if last.Reason != tc.reason {
				t.Errorf("reason = %q, want %q", last.Reason, tc.reason)
			}
		})
	}
}

// TestQuality_UnknownBeforeThereIsEvidence guards the alerting case.
//
// Reporting Good before measuring would be a claim the engine has not earned;
// reporting Degraded would page somebody every time a call connects.
func TestQuality_UnknownBeforeThereIsEvidence(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat()).Quality
	q := mustQualityAnalyzer(t, cfg)

	if q.Class() != QualityUnknown {
		t.Errorf("a fresh analyser reports %s, want %s", q.Class(), QualityUnknown)
	}

	// A view that is not ready must not produce a judgement whatever it says.
	notReady := qualityView(0.05, 30, 0, 12)
	notReady.Ready = false
	for i := 0; i < 50; i++ {
		if got := q.Observe(notReady, 0).Class; got != QualityUnknown {
			t.Fatalf("frame %d judged %s before the signal was ready", i, got)
		}
	}

	// And Unknown must not read as a fault.
	if QualityUnknown.Rank() != QualityGood.Rank() {
		t.Error("Unknown ranks as worse than Good; it would trigger a degradation alert")
	}
}

// TestQuality_HysteresisPreventsFlapping is the property that makes the metric
// worth putting on a dashboard.
func TestQuality_HysteresisPreventsFlapping(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat()).Quality
	q := mustQualityAnalyzer(t, cfg)

	good := qualityView(0.05, 30, 0, 12)
	bad := qualityView(0.05, cfg.MinSNRDB-1, 0, 12)

	// Settle on good.
	for i := 0; i < cfg.HysteresisFrames*3; i++ {
		q.Observe(good, 0)
	}
	if q.Class() != QualityGood {
		t.Fatalf("did not settle on good, got %s", q.Class())
	}

	// Alternate on every frame. Nothing may ever be adopted, because no
	// candidate survives HysteresisFrames in a row.
	var changes int
	for i := 0; i < 200; i++ {
		v := good
		if i%2 == 0 {
			v = bad
		}
		if q.Observe(v, 0).Changed {
			changes++
		}
	}

	if changes != 0 {
		t.Errorf("a signal alternating every frame produced %d adopted class "+
			"changes; hysteresis of %d frames must absorb all of them",
			changes, cfg.HysteresisFrames)
	}
	if q.Class() != QualityGood {
		t.Errorf("the adopted class drifted to %s under alternation", q.Class())
	}
}

// TestQuality_ReportsDegradationAndRecoveryExactlyOnce is what a consumer keys
// audio_degraded and audio_recovered on.
func TestQuality_ReportsDegradationAndRecoveryExactlyOnce(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat()).Quality
	q := mustQualityAnalyzer(t, cfg)

	good := qualityView(0.05, 30, 0, 12)
	bad := qualityView(0.05, cfg.MinSNRDB-1, 0, 12)

	settle := cfg.HysteresisFrames * 4

	var degradations, recoveries int
	count := func(r QualityReport) {
		if r.Degraded {
			degradations++
		}
		if r.Recovered {
			recoveries++
		}
	}

	for i := 0; i < settle; i++ {
		count(q.Observe(good, 0))
	}
	for i := 0; i < settle; i++ {
		count(q.Observe(bad, 0))
	}
	for i := 0; i < settle; i++ {
		count(q.Observe(good, 0))
	}

	// The first settle produces one transition out of Unknown, which ranks
	// alongside Good and is therefore neither a degradation nor a recovery.
	if degradations != 1 {
		t.Errorf("%d degradations across one good→bad→good cycle, want 1", degradations)
	}
	if recoveries != 1 {
		t.Errorf("%d recoveries across one good→bad→good cycle, want 1", recoveries)
	}
}

// TestQuality_WorstProblemWins pins the dominant-reason rule.
//
// An operator looking at degraded audio needs to know what to fix FIRST.
// Reporting four simultaneous reasons is four things nobody acts on.
func TestQuality_WorstProblemWins(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat()).Quality
	q := mustQualityAnalyzer(t, cfg)

	// Everything wrong at once: destroyed by clipping, missing frames, buried
	// in noise and far too quiet.
	everything := qualityView(cfg.MinSignalRMS/10, cfg.MinSNRDB-5, cfg.MaxClipRatio, 1)

	var last QualityReport
	for i := 0; i < cfg.HysteresisFrames*3; i++ {
		last = q.Observe(everything, cfg.MaxGapRatio)
	}

	if last.Class != QualityUnusable {
		t.Errorf("class = %s, want %s", last.Class, QualityUnusable)
	}
	if last.Reason != ReasonClipping {
		t.Errorf("reason = %q, want %q — destroyed samples cannot be recovered by "+
			"anything downstream, so clipping is what to fix first",
			last.Reason, ReasonClipping)
	}
}

// TestQuality_ReportCarriesItsEvidence is what makes a quality claim reviewable.
func TestQuality_ReportCarriesItsEvidence(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat()).Quality
	q := mustQualityAnalyzer(t, cfg)

	v := qualityView(0.05, 14.5, 0.001, 11)
	const gap = 0.03

	var last QualityReport
	for i := 0; i < cfg.HysteresisFrames*3; i++ {
		last = q.Observe(v, gap)
	}

	if last.SNRDB != 14.5 {
		t.Errorf("SNRDB = %g, want 14.5", last.SNRDB)
	}
	if last.ClipRatio != 0.001 {
		t.Errorf("ClipRatio = %g, want 0.001", last.ClipRatio)
	}
	if last.GapRatio != gap {
		t.Errorf("GapRatio = %g, want %g", last.GapRatio, gap)
	}
	if last.String() == "" {
		t.Error("the report renders empty")
	}
}

func TestQuality_Reset(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat()).Quality
	q := mustQualityAnalyzer(t, cfg)

	bad := qualityView(0.05, cfg.MinSNRDB-1, 0, 12)
	for i := 0; i < cfg.HysteresisFrames*3; i++ {
		q.Observe(bad, 0)
	}
	if q.Class() == QualityUnknown {
		t.Fatal("setup: the analyser never adopted a class")
	}

	q.Reset()

	if q.Class() != QualityUnknown {
		t.Errorf("after Reset the class is %s, want %s", q.Class(), QualityUnknown)
	}
}

func TestNewQualityAnalyzer_RefusesInvalidConfiguration(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(testFormat()).Quality
	cfg.HysteresisFrames = 0
	if _, err := NewQualityAnalyzer(cfg); err == nil {
		t.Error("a zero hysteresis was accepted")
	}
}

func BenchmarkQualityAnalyzer_Observe(b *testing.B) {
	cfg := DefaultConfig(testFormat()).Quality
	q, err := NewQualityAnalyzer(cfg)
	if err != nil {
		b.Fatal(err)
	}
	v := qualityView(0.05, 25, 0, 12)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q.Observe(v, 0)
	}
}
