package audiointel

import (
	"fmt"
	"math"
)

// NoiseEstimate is what the noise analyser knows at one instant.
type NoiseEstimate struct {
	// Floor is the adaptive background level estimate, normalised, in [0,1].
	Floor float64

	// FloorDBFS is the same value in decibels relative to full scale.
	FloorDBFS float64

	// SNRDB is how far the observed frame sits above the floor.
	//
	// THE CORE COMPARISON OF THIS WHOLE ENGINE. Every speech decision reduces
	// to whether this number clears a configured threshold.
	SNRDB float64

	// Converged reports whether warm-up has completed.
	//
	// Until it has, the detector refuses to assert speech. That is honest: a
	// speech decision is a comparison against the floor, and before the floor
	// exists there is nothing to compare against.
	Converged bool

	// Confidence is how much the estimate should be trusted, in [0,1].
	//
	// The product of coverage (how much background has been observed) and
	// stability (how steady it was). A high-confidence floor over a fan is
	// worth more than a low-confidence one over a building site, and a
	// consumer that treats the two identically will be wrong on the second.
	Confidence float64

	// Stability is the steadiness of the observed background, in [0,1].
	Stability float64

	// Class is the coarse characterisation of the background.
	Class NoiseClass

	// Observations is how many background frames the estimate rests on.
	Observations uint64
}

// String renders the estimate.
func (e NoiseEstimate) String() string {
	return fmt.Sprintf("noise %s floor=%.1fdBFS snr=%.1fdB conf=%.2f n=%d",
		e.Class, e.FloorDBFS, e.SNRDB, e.Confidence, e.Observations)
}

// NoiseAnalyzer estimates the background level and keeps it there.
//
// # The three mechanisms that stop speech redefining the floor
//
// A naive estimator tracks the recent average level, and on a call it
// converges on the level of the caller's voice. Then nothing is above the
// floor, nothing is speech, and the agent never responds. Three separate
// mechanisms prevent that here, and each is needed:
//
//  1. SPEECH GATING. The estimator does not observe frames the voice activity
//     detector has classified as speech or candidate speech. This is the
//     primary defence and in normal operation it is the only one that acts.
//
//  2. ASYMMETRIC ADAPTATION. Downward movement is fast, upward movement is
//     slow. The gate is not perfect — a word can begin in a frame the detector
//     has not yet promoted — so the direction contamination pushes is the
//     direction the estimator resists.
//
//  3. A HARD RISE CLAMP. MaxRiseDBPerSecond bounds upward movement regardless
//     of the adaptation coefficient, so even a sustained failure of the first
//     two mechanisms moves the floor slowly enough for the fast downward rate
//     to recover it within seconds.
//
// # Warm-up uses the minimum, not the mean
//
// Before the detector exists there is no gate, so warm-up frames may contain
// speech. The minimum observed level is used rather than the average, because
// SPEECH ONLY EVER ADDS ENERGY: the quietest moment in the warm-up window is
// the best available estimate of what is underneath it. An average over a
// warm-up in which somebody said hello converges on hello.
//
// Not safe for concurrent use. One analyser per session.
type NoiseAnalyzer struct {
	cfg NoiseConfig

	// floor is the current estimate.
	floor float64

	// ring holds recent BACKGROUND levels — speech frames excluded after
	// warm-up. Fixed size, allocated once.
	ring    []float64
	next    int
	filled  int
	total   uint64
	statsOK bool
	mean    float64
	cv      float64

	// warmupSeen counts frames observed during warm-up, and warmupMin is the
	// quietest of them.
	warmupSeen int
	warmupMin  float64

	// converged latches true and never goes back. A floor that un-converged
	// would make the detector oscillate between asserting and refusing to
	// assert, which is worse than either.
	converged bool

	// maxRiseFactor is the largest multiplicative step upward permitted in one
	// frame, precomputed from MaxRiseDBPerSecond and the frame cadence so the
	// hot path does no exponentiation.
	maxRiseFactor float64

	// clipRatioAlert is the clip ratio at which the background is called
	// clipping, carried from the quality thresholds so the two agree.
	clipRatioAlert float64

	// veryLowLevel is the level below which the input is called very quiet.
	veryLowLevel float64
}

// NewNoiseAnalyzer builds an estimator from the runtime configuration.
//
// Takes the whole [Config] rather than just [NoiseConfig] because two of its
// classification boundaries are deliberately shared with the quality
// thresholds: an input this analyser calls "clipping" and one the quality
// classifier calls "clipping" must be the same input, or an operator reading
// both sees a contradiction.
func NewNoiseAnalyzer(cfg Config) (*NoiseAnalyzer, error) {
	if problems := cfg.Noise.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	if cfg.FrameInterval <= 0 {
		return nil, &ConfigError{Problems: []string{
			"noise: FrameInterval must be positive to bound the rise rate"}}
	}

	// The rise clamp, converted from dB per second to a per-frame amplitude
	// ratio once. 10^(dB/20) is the amplitude form; doing this per frame would
	// put a pow() on the hot path for a constant.
	maxRiseDB := cfg.Noise.MaxRiseDBPerSecond * cfg.FrameInterval.Seconds()

	return &NoiseAnalyzer{
		cfg:            cfg.Noise,
		floor:          cfg.Noise.MinFloor,
		ring:           make([]float64, cfg.Noise.WindowFrames),
		warmupMin:      math.MaxFloat64,
		maxRiseFactor:  math.Pow(10, maxRiseDB/20),
		clipRatioAlert: cfg.Quality.DegradedClipRatio,
		veryLowLevel:   cfg.Quality.MinSignalRMS,
	}, nil
}

// Observe folds one frame into the estimate and returns the current view.
//
// speechActive must be the voice activity detector's verdict for the PREVIOUS
// frame, not this one — this analyser runs before the detector, and asking it
// to wait for a verdict derived from its own output is the circular dependency
// this ordering exists to avoid. One frame of lag on the gate is 20 ms, well
// inside the onset confirmation window, so a word's first frame is gated before
// it can move anything.
func (n *NoiseAnalyzer) Observe(f FrameFeatures, speechActive bool) NoiseEstimate {
	switch {
	case !n.converged:
		n.observeWarmup(f)

	case speechActive:
		// GATED. The estimator does not look at speech, which is the primary
		// defence against speech redefining the background.

	case f.Synthesised():
		// Phase 11B invented this frame to cover a gap. Its samples are zeros
		// this engine generated, not silence the caller produced, and folding
		// them in would drag the floor toward digital zero every time the
		// network hiccuped — making the next real frame look like a shout.

	default:
		n.observeBackground(f)
	}

	return n.estimate(f)
}

// observeWarmup accumulates the initial estimate.
func (n *NoiseAnalyzer) observeWarmup(f FrameFeatures) {
	// A synthesised frame is not evidence about the line, even during warm-up.
	if !f.Synthesised() {
		if f.RMS < n.warmupMin {
			n.warmupMin = f.RMS
		}
		n.push(f.RMS)
	}
	n.warmupSeen++

	if n.warmupSeen < n.cfg.WarmupFrames {
		return
	}

	// Warm-up complete. The MINIMUM, not the mean — speech only ever adds
	// energy, so the quietest moment observed is the best estimate of what lies
	// underneath whatever else happened during warm-up.
	floor := n.warmupMin
	if floor == math.MaxFloat64 {
		// Every warm-up frame was synthesised: the source produced nothing real
		// at all. Start at the configured minimum rather than at zero, and let
		// ordinary adaptation take over once audio arrives.
		floor = n.cfg.MinFloor
	}
	n.floor = n.clamp(floor)
	n.converged = true
}

// observeBackground adapts the floor toward one background frame.
func (n *NoiseAnalyzer) observeBackground(f FrameFeatures) {
	n.push(f.RMS)

	target := f.RMS

	switch {
	case target < n.floor:
		// DOWNWARD: fast. A room that quietens must be tracked promptly,
		// because a floor stuck above the true level suppresses real speech —
		// the worse of the two failures, since a missed word is a caller
		// repeating themselves and a false trigger is merely a wasted cycle.
		n.floor += n.cfg.FallAlpha * (target - n.floor)

	case target > n.floor:
		// UPWARD: slow, and then clamped again.
		candidate := n.floor + n.cfg.RiseAlpha*(target-n.floor)

		// THE HARD BOUND. Even if RiseAlpha were misconfigured, even if the
		// speech gate failed completely, the floor cannot move up faster than
		// MaxRiseDBPerSecond. One second of total contamination at the default
		// 6 dB/s moves it by a factor of two, which the fast downward rate
		// recovers in well under a second once the contamination stops.
		if ceiling := n.floor * n.maxRiseFactor; candidate > ceiling {
			candidate = ceiling
		}
		n.floor = candidate
	}

	n.floor = n.clamp(n.floor)
}

// clamp holds the estimate inside the configured range.
//
// MinFloor matters most: on a digitally silent line the measured level is zero,
// and a ratio against zero is an infinity that makes the first non-zero sample
// look like a shout.
func (n *NoiseAnalyzer) clamp(v float64) float64 {
	if v < n.cfg.MinFloor {
		return n.cfg.MinFloor
	}
	if v > n.cfg.MaxFloor {
		return n.cfg.MaxFloor
	}
	return v
}

// push records one background level in the bounded ring.
func (n *NoiseAnalyzer) push(rms float64) {
	n.ring[n.next] = rms
	n.next = (n.next + 1) % len(n.ring)
	if n.filled < len(n.ring) {
		n.filled++
	}
	n.total++
	n.statsOK = false
}

// backgroundStats returns the mean and coefficient of variation of the observed
// background, computed at most once per push.
//
// Walked chronologically and open-coded, for the reasons [FeatureWindow.Stats]
// documents at length: summation order is part of a floating-point answer, and
// a reproducible detector needs it to depend on contents rather than history.
func (n *NoiseAnalyzer) backgroundStats() (mean, cv float64) {
	if n.statsOK {
		return n.mean, n.cv
	}
	if n.filled == 0 {
		n.mean, n.cv, n.statsOK = 0, 0, true
		return 0, 0
	}

	start := n.next - n.filled
	if start < 0 {
		start += len(n.ring)
	}

	var sum float64
	idx := start
	for k := 0; k < n.filled; k++ {
		sum += n.ring[idx]
		idx++
		if idx == len(n.ring) {
			idx = 0
		}
	}
	mean = sum / float64(n.filled)

	if mean > 0 {
		var sumSquaredDeviation float64
		idx = start
		for k := 0; k < n.filled; k++ {
			d := n.ring[idx] - mean
			sumSquaredDeviation += d * d
			idx++
			if idx == len(n.ring) {
				idx = 0
			}
		}
		cv = math.Sqrt(sumSquaredDeviation/float64(n.filled)) / mean
	}

	n.mean, n.cv, n.statsOK = mean, cv, true
	return mean, cv
}

// estimate assembles the current view.
func (n *NoiseAnalyzer) estimate(f FrameFeatures) NoiseEstimate {
	_, cv := n.backgroundStats()

	// Stability falls as the background varies. 1/(1+cv) rather than 1-cv
	// because the latter goes negative on a genuinely chaotic background and a
	// negative confidence is meaningless.
	stability := 1 / (1 + cv)

	// Coverage is how much background the estimate rests on. Capped at 1 so a
	// long call does not report a confidence above one.
	coverage := float64(n.total) / float64(n.cfg.ConfidenceFrames)
	if coverage > 1 {
		coverage = 1
	}

	e := NoiseEstimate{
		Floor:        n.floor,
		FloorDBFS:    dbfs(n.floor),
		SNRDB:        dbRatio(f.RMS, n.floor),
		Converged:    n.converged,
		Stability:    stability,
		Observations: n.total,
	}

	if n.converged {
		e.Confidence = coverage * stability
	}
	e.Class = n.classify(f, cv)

	return e
}

// classify characterises the background.
//
// Ordered by severity: a clipping input is a clipping input regardless of what
// its floor estimate says, and reporting it as "stationary" because the level
// happens to be steady would be technically true and operationally useless.
func (n *NoiseAnalyzer) classify(f FrameFeatures, cv float64) NoiseClass {
	if !n.converged {
		return NoiseUnknown
	}

	switch {
	case f.ClipRatio >= n.clipRatioAlert:
		return NoiseClipping

	case f.RMS > 0 && f.RMS < n.veryLowLevel && n.floor < n.veryLowLevel:
		// Both the signal AND the background are below the usable level: this
		// is a muted handset or a broken gain stage, not a quiet room. Testing
		// the signal alone would classify every inter-word gap as very low.
		return NoiseVeryLow

	case cv >= n.cfg.TransientModulation:
		return NoiseTransient

	case n.floor <= n.cfg.QuietFloor:
		return NoiseQuiet

	default:
		return NoiseStationary
	}
}

// Retract removes the n most recently recorded background levels.
//
// # Why the estimator needs to be able to change its mind
//
// The speech gate lags by one frame — see [SignalAnalyzer] — so the frames
// between where an utterance begins and where the voice activity detector
// confirms it reach this estimator labelled as background. There are exactly
// VADConfig.MinOnsetFrames of them, and the moment the onset is confirmed they
// are known to have been speech.
//
// Leaving them in was measurably wrong. Two loud frames in a hundred-frame ring
// roughly halve the coefficient-of-variation stability score, and that score
// multiplies into every downstream confidence — so the reported confidence of
// every speech, endpoint and overlap decision dropped by half for two seconds
// at the start of EVERY utterance. It looked like a bursty background and it
// was an artefact of the gate's lag.
//
// The alternatives were worse. A level band excluding loud frames from the ring
// would also exclude the genuinely loud half of a bursty background and report
// a building site as a quiet room, destroying [NoiseTransient] detection. A
// trimmed or median statistic would be robust but needs a sort on the frame
// path. Retracting exactly the frames now known to be misclassified is precise,
// costs two array writes, and leaves every other case untouched.
//
// # The floor itself is deliberately NOT rewound
//
// Those frames did nudge the estimate upward, but the rise clamp bounds that to
// MaxRiseDBPerSecond — about 0.12 dB per frame at the defaults — and the fast
// downward rate erases it within a few frames of the next silence. Unwinding an
// exponential average exactly is not possible without keeping the history this
// design exists to avoid, and approximating it would introduce an error larger
// than the one being corrected.
func (n *NoiseAnalyzer) Retract(count int) {
	if count <= 0 || n.filled == 0 {
		return
	}
	if count > n.filled {
		count = n.filled
	}

	for i := 0; i < count; i++ {
		n.next--
		if n.next < 0 {
			n.next += len(n.ring)
		}
		n.ring[n.next] = 0
	}
	n.filled -= count

	// The retracted frames are no longer evidence about the background, so they
	// no longer count toward coverage either.
	if n.total >= uint64(count) {
		n.total -= uint64(count)
	} else {
		n.total = 0
	}

	n.statsOK = false
}

// Floor returns the current estimate.
func (n *NoiseAnalyzer) Floor() float64 { return n.floor }

// Converged reports whether warm-up has completed.
func (n *NoiseAnalyzer) Converged() bool { return n.converged }

// Observations returns how many background frames the estimate rests on.
func (n *NoiseAnalyzer) Observations() uint64 { return n.total }

// Reset returns the analyser to its initial state, keeping its storage.
//
// The ring is retained deliberately: reallocating it would be the one
// allocation this design avoids, arriving during a session recovery.
func (n *NoiseAnalyzer) Reset() {
	n.floor = n.cfg.MinFloor
	n.next, n.filled, n.total = 0, 0, 0
	n.statsOK, n.mean, n.cv = false, 0, 0
	n.warmupSeen, n.warmupMin = 0, math.MaxFloat64
	n.converged = false
}
