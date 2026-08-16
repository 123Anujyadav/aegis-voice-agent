package audiointel

import (
	"fmt"
	"math"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
)

// FeatureWindow is a fixed-size ring of [FrameFeatures].
//
// # It is allocated once and never grows
//
// §19 requires the hot path to be bounded. This is where that is enforced: the
// backing array is sized at construction from configuration, and Push
// overwrites rather than appending. A session that runs for six hours holds
// exactly the same memory as one that has run for six frames.
//
// It holds FEATURES, never audio. See [FrameFeatures] — scalars only, no
// payload, no slice into a frame's storage.
//
// Not safe for concurrent use. One window per session, and a session's frames
// are analysed on one goroutine, which is what the synchronous design buys.
type FeatureWindow struct {
	buf []FrameFeatures

	// next is where the following Push writes.
	next int

	// filled counts entries written, capped at len(buf). Distinguishing a
	// partly filled window from a full one matters: a statistic over three
	// frames is not the same claim as one over a hundred, and confidence
	// depends on knowing which it is.
	filled int

	// total counts every frame ever pushed, which is what tells a warm-up
	// check whether enough audio has been seen — a number the ring itself
	// forgets by design.
	total uint64

	// stats caches the last computed statistics. Invalidated by Push.
	//
	// Recomputing is O(window) and the VAD asks for it once per frame; without
	// the cache, a second caller in the same frame pays for the same answer
	// twice.
	stats      SignalStats
	statsValid bool
}

// NewFeatureWindow builds a window of the given capacity.
func NewFeatureWindow(capacity int) (*FeatureWindow, error) {
	if capacity <= 0 {
		return nil, &ConfigError{Problems: []string{
			fmt.Sprintf("window: capacity %d must be positive", capacity)}}
	}
	return &FeatureWindow{buf: make([]FrameFeatures, capacity)}, nil
}

// Cap returns the fixed capacity.
func (w *FeatureWindow) Cap() int { return len(w.buf) }

// Len returns how many features are held, never more than Cap.
func (w *FeatureWindow) Len() int { return w.filled }

// Total returns how many features have ever been pushed.
func (w *FeatureWindow) Total() uint64 { return w.total }

// Full reports whether the window has as much history as it will ever hold.
func (w *FeatureWindow) Full() bool { return w.filled == len(w.buf) }

// Push adds one frame's features, overwriting the oldest when full.
//
// O(1), allocation-free. The oldest entry is overwritten in place; nothing is
// copied, moved or reallocated.
func (w *FeatureWindow) Push(f FrameFeatures) {
	w.buf[w.next] = f
	w.next = (w.next + 1) % len(w.buf)
	if w.filled < len(w.buf) {
		w.filled++
	}
	w.total++
	w.statsValid = false
}

// Newest returns the most recently pushed features and whether any exist.
func (w *FeatureWindow) Newest() (FrameFeatures, bool) {
	if w.filled == 0 {
		return FrameFeatures{}, false
	}
	i := w.next - 1
	if i < 0 {
		i = len(w.buf) - 1
	}
	return w.buf[i], true
}

// Oldest returns the least recently pushed features still held.
func (w *FeatureWindow) Oldest() (FrameFeatures, bool) {
	if w.filled == 0 {
		return FrameFeatures{}, false
	}
	if w.filled < len(w.buf) {
		return w.buf[0], true
	}
	return w.buf[w.next], true
}

// At returns the nth-newest entry: At(0) is the newest.
func (w *FeatureWindow) At(n int) (FrameFeatures, bool) {
	if n < 0 || n >= w.filled {
		return FrameFeatures{}, false
	}
	i := w.next - 1 - n
	for i < 0 {
		i += len(w.buf)
	}
	return w.buf[i], true
}

// Reset empties the window without releasing its storage.
//
// Used on session recovery. The array is retained deliberately: releasing and
// reallocating it would be the one allocation this design exists to avoid,
// arriving at the worst moment.
func (w *FeatureWindow) Reset() {
	w.next = 0
	w.filled = 0
	w.total = 0
	w.statsValid = false
}

// SignalStats is a consistent statistical view of the window.
//
// Every field is derived from the features held; nothing is carried forward
// from before the window's span, which is what makes two sessions fed identical
// audio produce identical statistics regardless of when they started.
type SignalStats struct {
	// Frames is how many features the statistics cover.
	Frames int

	// Span is the media time from the oldest frame's start to the newest
	// frame's end.
	Span time.Duration

	// MeanRMS, MeanEnergy and MeanZCR are the window averages.
	MeanEnergy float64
	MeanRMS    float64
	MeanZCR    float64

	// MinRMS and MaxRMS bound the levels observed. MinRMS is what the noise
	// floor estimator reads.
	MinRMS float64
	MaxRMS float64

	// EnergyModulation is the coefficient of variation of frame energy —
	// standard deviation over mean.
	//
	// THE FEATURE THAT SEPARATES SPEECH FROM A STEADY BACKGROUND, and it is
	// dimensionless on purpose. An absolute variance threshold would mean
	// something different at every input level and would need recalibrating for
	// every line; a ratio means the same thing at any level.
	//
	// Stationary sources — a fan, line hiss, an engine — sit low. Syllabic
	// speech sits high. A tone sits at essentially zero.
	EnergyModulation float64

	// Trend is the fractional change in mean energy from the older half of the
	// window to the newer half.
	//
	// Positive means rising. Used by [EndpointPolicy.RequireFallingEnergy] to
	// defer an endpoint on a caller who is audibly winding up to say more.
	// Halves rather than a least-squares fit: the fit is more precise about a
	// quantity nobody needs precision in, and it costs a division per frame to
	// produce a number used as a sign test.
	Trend float64

	// ClipRatio is the mean fraction of clipped samples across the window.
	ClipRatio float64

	// SynthesisedFrames counts frames Phase 11B invented to cover gaps.
	//
	// Invented silence is not measured silence. A quality classifier that
	// ignored this would report a clean quiet line while the network dropped
	// every second packet.
	SynthesisedFrames int
}

// Stats returns the window's statistics, computing them at most once per push.
//
// # Recomputed over the window rather than maintained incrementally
//
// An incremental sum with eviction is O(1) instead of O(window), and it was the
// obvious first choice. It is also not reproducible: subtracting an evicted
// value from a running float sum leaves a residue that depends on the entire
// arithmetic history, so two sessions fed byte-identical audio but started at
// different points compute slightly different variances — and a threshold
// comparison sitting near a boundary then goes different ways.
//
// §14 requires deterministic decisions and §20 requires replay to be
// reproducible. Recomputation over a bounded window costs a fixed few hundred
// nanoseconds and is exactly reproducible, so it wins. The window is fixed at
// construction, so this is O(1) in the length of the CALL, which is the bound
// that matters.
//
// Two passes for the variance, not the one-pass E[x²]−E[x]² identity: frame
// energies are around 1e-3, their squares around 1e-6, and subtracting two
// nearly equal small numbers is where catastrophic cancellation lives. The
// second pass costs another hundred iterations and removes the failure mode.
func (w *FeatureWindow) Stats() SignalStats {
	if w.statsValid {
		return w.stats
	}

	var s SignalStats
	if w.filled == 0 {
		w.stats, w.statsValid = s, true
		return s
	}

	s.Frames = w.filled
	s.MinRMS = math.MaxFloat64

	// # Both passes walk the ring in CHRONOLOGICAL order, and that is the whole
	// reproducibility argument
	//
	// Floating-point addition is not associative, so the order of summation is
	// part of the answer. Walking the backing array in index order would start
	// wherever `next` happens to sit, which depends on how many frames the
	// session has processed — and two sessions holding identical audio would
	// then compute sums that differ in the last bit. Near a threshold, that
	// decides differently for the same sound.
	//
	// Walking oldest-to-newest depends only on the window's CONTENTS, which is
	// the property TestWindow_StatisticsAreReproducibleRegardlessOfHistory
	// asserts directly.
	//
	// The walk is open-coded rather than going through At(): At has to
	// normalise a possibly negative index on every call, and at three hundred
	// calls per frame that overhead was six times the cost of the frame
	// measurement itself.
	start := w.next - w.filled
	if start < 0 {
		start += len(w.buf)
	}

	// half is the size of the NEWER half, matching the trend definition below.
	half := w.filled / 2
	olderCount := w.filled - half

	var sumEnergy, sumRMS, sumZCR, sumClip float64
	var olderEnergy, newerEnergy float64

	idx := start
	for k := 0; k < w.filled; k++ {
		f := &w.buf[idx]

		sumEnergy += f.Energy
		sumRMS += f.RMS
		sumZCR += f.ZCR
		sumClip += f.ClipRatio

		if k < olderCount {
			olderEnergy += f.Energy
		} else {
			newerEnergy += f.Energy
		}

		if f.RMS < s.MinRMS {
			s.MinRMS = f.RMS
		}
		if f.RMS > s.MaxRMS {
			s.MaxRMS = f.RMS
		}
		if f.Flags.Has(media.FlagSilence) {
			s.SynthesisedFrames++
		}

		if k == 0 {
			s.Span = -f.Timestamp
		}
		if k == w.filled-1 {
			s.Span += f.Timestamp + f.Duration
		}

		idx++
		if idx == len(w.buf) {
			idx = 0
		}
	}

	n := float64(w.filled)
	s.MeanEnergy = sumEnergy / n
	s.MeanRMS = sumRMS / n
	s.MeanZCR = sumZCR / n
	s.ClipRatio = sumClip / n

	// Second pass: variance about the mean just computed.
	//
	// Not the one-pass E[x²]−E[x]² identity. Frame energies are around 1e-3 and
	// their squares around 1e-6; subtracting two nearly equal small numbers is
	// where catastrophic cancellation lives, and it can produce a negative
	// variance. A second hundred-iteration pass removes the failure mode.
	if s.MeanEnergy > 0 {
		var sumSquaredDeviation float64
		idx = start
		for k := 0; k < w.filled; k++ {
			d := w.buf[idx].Energy - s.MeanEnergy
			sumSquaredDeviation += d * d
			idx++
			if idx == len(w.buf) {
				idx = 0
			}
		}
		s.EnergyModulation = math.Sqrt(sumSquaredDeviation/n) / s.MeanEnergy
	}

	// Trend: the fractional change in mean energy from the older half of the
	// window to the newer half, accumulated in the pass above.
	//
	// Needs at least two frames per half to say anything; below that it reports
	// zero rather than a slope derived from one observation, which would be
	// noise presented as a measurement.
	const minPerHalf = 2
	if w.filled >= minPerHalf*2 && s.MeanEnergy > 0 {
		s.Trend = (newerEnergy/float64(half) - olderEnergy/float64(olderCount)) /
			s.MeanEnergy
	}

	w.stats, w.statsValid = s, true
	return s
}

// RecentMinRMS returns the lowest RMS among the n newest frames.
//
// What the noise floor estimator reads: the quietest recent moment is the best
// available estimate of the background, because speech only ever adds energy.
func (w *FeatureWindow) RecentMinRMS(n int) (float64, bool) {
	if n <= 0 || w.filled == 0 {
		return 0, false
	}
	if n > w.filled {
		n = w.filled
	}

	// Walked newest-first, open-coded for the same reason Stats is. A minimum
	// is order-independent, so unlike a sum this direction carries no
	// reproducibility consequence — but the indexing cost is identical and this
	// runs on every frame.
	min := math.MaxFloat64
	idx := w.next - 1
	for k := 0; k < n; k++ {
		if idx < 0 {
			idx += len(w.buf)
		}
		if r := w.buf[idx].RMS; r < min {
			min = r
		}
		idx--
	}
	return min, true
}

// RecentModulation returns the coefficient of variation of energy across the
// newest n frames.
//
// # Why this exists alongside SignalStats.EnergyModulation
//
// The full-window statistic answers "has this line been modulated recently",
// which is the right question for an operator and the wrong one for an onset
// decision. At the instant a sound begins, the full window is mostly the
// silence that preceded it, so ANY sudden sound — a word, a fan switching on,
// hold music starting — measures as strongly modulated. Steady noise would
// therefore pass the onset test and, once past it, stay in Speech on the
// energy test alone.
//
// A short window fills with the new sound in a few hundred milliseconds and
// then tells the truth about it. That is the shortest honest answer available:
// you cannot know a sound is steady before you have heard enough of it to be
// steady, and 200 ms is roughly one syllable. See the limitation documented on
// [SpeechDetector].
//
// Returns 0 when fewer than n frames are held: a coefficient of variation over
// three frames is a number, not a measurement.
func (w *FeatureWindow) RecentModulation(n int) float64 {
	if n <= 1 || w.filled < n {
		return 0
	}

	start := w.next - n
	for start < 0 {
		start += len(w.buf)
	}

	var sum float64
	idx := start
	for k := 0; k < n; k++ {
		sum += w.buf[idx].Energy
		idx++
		if idx == len(w.buf) {
			idx = 0
		}
	}
	mean := sum / float64(n)
	if mean <= 0 {
		return 0
	}

	var sumSquaredDeviation float64
	idx = start
	for k := 0; k < n; k++ {
		d := w.buf[idx].Energy - mean
		sumSquaredDeviation += d * d
		idx++
		if idx == len(w.buf) {
			idx = 0
		}
	}
	return math.Sqrt(sumSquaredDeviation/float64(n)) / mean
}

// String renders the window's shape, never its contents.
func (w *FeatureWindow) String() string {
	return fmt.Sprintf("window %d/%d (%d total)", w.filled, len(w.buf), w.total)
}
