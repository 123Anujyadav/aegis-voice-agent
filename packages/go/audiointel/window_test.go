package audiointel

import (
	"math"
	"testing"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
)

func mustWindow(t *testing.T, capacity int) *FeatureWindow {
	t.Helper()
	w, err := NewFeatureWindow(capacity)
	if err != nil {
		t.Fatalf("NewFeatureWindow(%d): %v", capacity, err)
	}
	return w
}

// featuresWithEnergy builds features carrying a chosen energy, for tests about
// the window's arithmetic rather than about measurement.
func featuresWithEnergy(seq uint64, energy float64) FrameFeatures {
	return FrameFeatures{
		Sequence:  seq,
		Timestamp: time.Duration(seq) * 20 * time.Millisecond,
		Duration:  20 * time.Millisecond,
		Energy:    energy,
		RMS:       math.Sqrt(energy),
		Samples:   160,
	}
}

// TestWindow_NeverGrows is the §19 bound, checked directly rather than inferred.
//
// A session runs for the length of a call. A window that grew by one entry per
// frame would hold 180,000 entries after an hour, and the failure presents as a
// process that gets slower and then dies — days after the deploy that caused
// it.
func TestWindow_NeverGrows(t *testing.T) {
	t.Parallel()

	const capacity = 64
	w := mustWindow(t, capacity)

	for i := 0; i < 1_000_000; i++ {
		w.Push(featuresWithEnergy(uint64(i), 0.01))

		if w.Len() > capacity {
			t.Fatalf("after %d pushes Len() = %d, above the capacity of %d",
				i+1, w.Len(), capacity)
		}
		if cap(w.buf) != capacity || len(w.buf) != capacity {
			t.Fatalf("after %d pushes the backing array is %d/%d, want %d",
				i+1, len(w.buf), cap(w.buf), capacity)
		}
	}

	if w.Len() != capacity {
		t.Errorf("Len() = %d after a million pushes, want a full window of %d",
			w.Len(), capacity)
	}
	if w.Total() != 1_000_000 {
		t.Errorf("Total() = %d, want 1000000 — the ring forgets entries but the "+
			"count of what it has seen is what a warm-up check reads", w.Total())
	}
}

// TestWindow_RingOrderIsNewestFirst pins the indexing every detector relies on.
func TestWindow_RingOrderIsNewestFirst(t *testing.T) {
	t.Parallel()

	w := mustWindow(t, 4)

	if _, ok := w.Newest(); ok {
		t.Error("an empty window reported a newest entry")
	}
	if _, ok := w.At(0); ok {
		t.Error("an empty window returned At(0)")
	}

	for i := 0; i < 3; i++ {
		w.Push(featuresWithEnergy(uint64(i), 0.01))
	}

	// Partly filled: newest is 2, oldest is 0.
	if n, _ := w.Newest(); n.Sequence != 2 {
		t.Errorf("Newest().Sequence = %d, want 2", n.Sequence)
	}
	if o, _ := w.Oldest(); o.Sequence != 0 {
		t.Errorf("Oldest().Sequence = %d, want 0", o.Sequence)
	}
	for n := 0; n < 3; n++ {
		f, ok := w.At(n)
		if !ok {
			t.Fatalf("At(%d) reported no entry in a window of 3", n)
		}
		if want := uint64(2 - n); f.Sequence != want {
			t.Errorf("At(%d).Sequence = %d, want %d", n, f.Sequence, want)
		}
	}
	if _, ok := w.At(3); ok {
		t.Error("At(3) returned an entry in a window of 3")
	}

	// Wrapped: 4 and 5 evict 0 and 1, so the window holds 2..5.
	w.Push(featuresWithEnergy(3, 0.01))
	w.Push(featuresWithEnergy(4, 0.01))
	w.Push(featuresWithEnergy(5, 0.01))

	if n, _ := w.Newest(); n.Sequence != 5 {
		t.Errorf("after wrapping Newest().Sequence = %d, want 5", n.Sequence)
	}
	if o, _ := w.Oldest(); o.Sequence != 2 {
		t.Errorf("after wrapping Oldest().Sequence = %d, want 2", o.Sequence)
	}
	for n := 0; n < 4; n++ {
		f, _ := w.At(n)
		if want := uint64(5 - n); f.Sequence != want {
			t.Errorf("after wrapping At(%d).Sequence = %d, want %d", n, f.Sequence, want)
		}
	}
}

// TestWindow_StatisticsAreArithmeticallyCorrect checks the statistics against
// values worked out by hand.
func TestWindow_StatisticsAreArithmeticallyCorrect(t *testing.T) {
	t.Parallel()

	w := mustWindow(t, 4)

	// Energies 0.01, 0.02, 0.03, 0.04. Mean 0.025.
	// Deviations ±0.015, ±0.005. Population variance = (2·0.015² + 2·0.005²)/4
	//                                                = (0.00045 + 0.00005)/4
	//                                                = 0.000125
	// Stddev = 0.0111803. CV = 0.0111803/0.025 = 0.4472136.
	for i, e := range []float64{0.01, 0.02, 0.03, 0.04} {
		w.Push(featuresWithEnergy(uint64(i), e))
	}

	s := w.Stats()

	if s.Frames != 4 {
		t.Errorf("Frames = %d, want 4", s.Frames)
	}
	if math.Abs(s.MeanEnergy-0.025) > 1e-12 {
		t.Errorf("MeanEnergy = %.12f, want 0.025", s.MeanEnergy)
	}
	if math.Abs(s.EnergyModulation-0.4472136) > 1e-6 {
		t.Errorf("EnergyModulation = %.7f, want 0.4472136", s.EnergyModulation)
	}
	if math.Abs(s.MinRMS-math.Sqrt(0.01)) > 1e-12 {
		t.Errorf("MinRMS = %.12f, want %.12f", s.MinRMS, math.Sqrt(0.01))
	}
	if math.Abs(s.MaxRMS-math.Sqrt(0.04)) > 1e-12 {
		t.Errorf("MaxRMS = %.12f, want %.12f", s.MaxRMS, math.Sqrt(0.04))
	}

	// Newer half (0.03, 0.04) mean 0.035; older half (0.01, 0.02) mean 0.015.
	// Trend = (0.035 − 0.015) / 0.025 = 0.8.
	if math.Abs(s.Trend-0.8) > 1e-12 {
		t.Errorf("Trend = %.12f, want 0.8 for a rising ramp", s.Trend)
	}

	// Span runs from the oldest frame's start to the newest frame's end: four
	// 20 ms frames is 80 ms.
	if want := 80 * time.Millisecond; s.Span != want {
		t.Errorf("Span = %s, want %s", s.Span, want)
	}
}

// TestWindow_ModulationSeparatesSteadyFromVarying is the property the VAD's
// third feature depends on.
func TestWindow_ModulationSeparatesSteadyFromVarying(t *testing.T) {
	t.Parallel()

	steady := mustWindow(t, 20)
	for i := 0; i < 20; i++ {
		steady.Push(featuresWithEnergy(uint64(i), 0.01))
	}
	// Not bit-exactly zero, and it should not be asserted as such: summing
	// twenty copies of 0.01 does not land on exactly 0.2, so the mean is off by
	// an ulp and the deviations about it are around 1e-18. The measured
	// modulation is therefore ~1e-16 — sixteen orders of magnitude below the
	// threshold it feeds, which is the claim worth making.
	if got := steady.Stats().EnergyModulation; got > 1e-12 {
		t.Errorf("a perfectly steady signal reported modulation %g, want ~0", got)
	}

	varying := mustWindow(t, 20)
	for i := 0; i < 20; i++ {
		e := 0.001
		if i%2 == 0 {
			e = 0.05
		}
		varying.Push(featuresWithEnergy(uint64(i), e))
	}
	if got := varying.Stats().EnergyModulation; got <= defaultMinEnergyModulation {
		t.Errorf("a strongly alternating signal reported modulation %g, at or below "+
			"MinEnergyModulation %g", got, defaultMinEnergyModulation)
	}
}

// TestWindow_TrendIsZeroWithoutEnoughHistory guards against a slope derived
// from one observation per half being presented as a measurement.
func TestWindow_TrendIsZeroWithoutEnoughHistory(t *testing.T) {
	t.Parallel()

	w := mustWindow(t, 10)
	for i := 0; i < 3; i++ {
		w.Push(featuresWithEnergy(uint64(i), float64(i+1)*0.01))
		if got := w.Stats().Trend; got != 0 {
			t.Errorf("with %d frames Trend = %g, want 0", i+1, got)
		}
	}

	w.Push(featuresWithEnergy(3, 0.04))
	if got := w.Stats().Trend; got == 0 {
		t.Error("with four frames on a rising ramp Trend is still 0")
	}
}

// TestWindow_CountsSynthesisedFrames proves invented audio is tracked
// separately from measured audio.
func TestWindow_CountsSynthesisedFrames(t *testing.T) {
	t.Parallel()

	w := mustWindow(t, 10)
	for i := 0; i < 10; i++ {
		f := featuresWithEnergy(uint64(i), 0.01)
		if i%5 == 0 {
			f.Flags = media.FlagSilence | media.FlagDiscontinuity
		}
		w.Push(f)
	}

	if got := w.Stats().SynthesisedFrames; got != 2 {
		t.Errorf("SynthesisedFrames = %d, want 2; Phase 11B covering a gap means "+
			"there WAS a gap, and the covering silence is not audio the caller "+
			"produced", got)
	}
}

// TestWindow_RecentMinRMSReadsTheQuietestMoment is what the noise floor
// estimator relies on: speech only ever ADDS energy, so the quietest recent
// frame is the best available estimate of the background.
func TestWindow_RecentMinRMSReadsTheQuietestMoment(t *testing.T) {
	t.Parallel()

	w := mustWindow(t, 10)
	if _, ok := w.RecentMinRMS(5); ok {
		t.Error("an empty window returned a minimum")
	}

	energies := []float64{0.09, 0.01, 0.04, 0.16, 0.25}
	for i, e := range energies {
		w.Push(featuresWithEnergy(uint64(i), e))
	}

	// Newest three are 0.04, 0.16, 0.25 → minimum RMS is sqrt(0.04) = 0.2.
	got, ok := w.RecentMinRMS(3)
	if !ok {
		t.Fatal("RecentMinRMS reported no value")
	}
	if math.Abs(got-0.2) > 1e-12 {
		t.Errorf("RecentMinRMS(3) = %.12f, want 0.2", got)
	}

	// Asking for more than is held returns the minimum of what is held.
	got, _ = w.RecentMinRMS(100)
	if math.Abs(got-0.1) > 1e-12 {
		t.Errorf("RecentMinRMS(100) = %.12f, want 0.1 (sqrt of the 0.01 entry)", got)
	}
}

// TestWindow_ResetKeepsItsStorage guards the one allocation this design exists
// to avoid arriving at the worst moment — during a session recovery.
func TestWindow_ResetKeepsItsStorage(t *testing.T) {
	t.Parallel()

	w := mustWindow(t, 32)
	for i := 0; i < 100; i++ {
		w.Push(featuresWithEnergy(uint64(i), 0.01))
	}

	before := &w.buf[0]
	w.Reset()

	if w.Len() != 0 || w.Total() != 0 {
		t.Errorf("after Reset: Len=%d Total=%d, want 0/0", w.Len(), w.Total())
	}
	if cap(w.buf) != 32 {
		t.Errorf("Reset reallocated the backing array to capacity %d", cap(w.buf))
	}
	if &w.buf[0] != before {
		t.Error("Reset moved the backing array; it must be reused in place")
	}
	if s := w.Stats(); s.Frames != 0 || s.MeanEnergy != 0 {
		t.Errorf("Reset left stale statistics: %+v", s)
	}
}

// TestWindow_StatisticsAreReproducibleRegardlessOfHistory is the reason Stats
// recomputes rather than maintaining incremental sums.
//
// Two windows holding the SAME newest entries must produce byte-identical
// statistics even when one of them has churned through thousands of frames to
// get there. An incremental sum with eviction leaves an arithmetic residue that
// depends on the whole history, and a threshold comparison sitting near a
// boundary then goes different ways for two callers hearing identical audio.
func TestWindow_StatisticsAreReproducibleRegardlessOfHistory(t *testing.T) {
	t.Parallel()

	const capacity = 16

	tail := make([]FrameFeatures, capacity)
	for i := range tail {
		// Values chosen to be unrepresentable in binary floating point, so an
		// accumulated residue would actually show.
		tail[i] = featuresWithEnergy(uint64(i), 0.001+float64(i)*0.0007)
	}

	fresh := mustWindow(t, capacity)
	for _, f := range tail {
		fresh.Push(f)
	}

	churned := mustWindow(t, capacity)
	for i := 0; i < 50_000; i++ {
		churned.Push(featuresWithEnergy(uint64(i), 0.3+float64(i%7)*0.11))
	}
	for _, f := range tail {
		churned.Push(f)
	}

	a, b := fresh.Stats(), churned.Stats()
	if a != b {
		t.Errorf("identical window contents produced different statistics after "+
			"different histories\n  fresh: %+v\nchurned: %+v", a, b)
	}
}

// TestWindow_StatsAreCachedPerPush proves the O(window) recomputation happens
// once per frame rather than once per caller.
func TestWindow_StatsAreCachedPerPush(t *testing.T) {
	t.Parallel()

	w := mustWindow(t, 8)
	w.Push(featuresWithEnergy(0, 0.01))

	first := w.Stats()
	if !w.statsValid {
		t.Fatal("Stats did not mark its cache valid")
	}
	if second := w.Stats(); second != first {
		t.Error("a second Stats call in the same frame returned different values")
	}

	w.Push(featuresWithEnergy(1, 0.09))
	if w.statsValid {
		t.Error("Push did not invalidate the cache; a detector would read stale " +
			"statistics for the frame it is deciding on")
	}
	if after := w.Stats(); after == first {
		t.Error("statistics did not change after a frame with different energy")
	}
}

func TestNewFeatureWindow_RefusesNonPositiveCapacity(t *testing.T) {
	t.Parallel()

	for _, c := range []int{0, -1, -100} {
		if _, err := NewFeatureWindow(c); err == nil {
			t.Errorf("capacity %d was accepted", c)
		}
	}
}

// BenchmarkFeatureWindow_PushAndStats measures the per-frame cost of the
// bounded window, which is the price paid for reproducible statistics.
func BenchmarkFeatureWindow_PushAndStats(b *testing.B) {
	w, err := NewFeatureWindow(defaultSignalWindowFrames)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.Push(featuresWithEnergy(uint64(i), 0.01+float64(i%13)*0.001))
		_ = w.Stats()
	}
}

func BenchmarkFeatureWindow_PushOnly(b *testing.B) {
	w, err := NewFeatureWindow(defaultSignalWindowFrames)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.Push(featuresWithEnergy(uint64(i), 0.01))
	}
}
