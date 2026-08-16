package evaluation

import (
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// ClockResolution measures the smallest non-zero interval a clock can report.
//
// THIS EXISTS BECAUSE THE PLATFORM'S FIRST LATENCY NUMBERS WERE ALL ZERO.
//
// On Windows, Go's monotonic clock has a granularity of roughly half a
// millisecond. A memory retrieve takes about 300 nanoseconds. Timed with
// clock.Since, it measures as exactly zero — and so does a retrieve that has
// become a thousand times slower.
//
// That is not a rounding annoyance. It means every latency tolerance, every
// benchmark percentile and every latency-regression check was comparing
// quantisation noise, and would have kept reporting "within tolerance" while a
// subsystem degraded by three orders of magnitude. See ENGINEERING_AUDIT §F1.
//
// The platform cannot make the clock finer. What it can do is KNOW how coarse
// the clock is, refuse to draw conclusions below that, and say so in the report
// — which is what this function and [Config.LatencyFloor] exist for.
//
// It is measured rather than assumed because the answer differs by an order of
// magnitude between Linux (~1 µs), macOS and Windows (~500 µs), and a constant
// would be wrong on two of the three.
func ClockResolution(clock rt.Clock) time.Duration {
	if clock == nil {
		clock = rt.SystemClock{}
	}

	// A fake clock reports zero: it advances only when told to, so there is no
	// resolution to measure and no floor to impose. A determinism or scenario
	// test driving a fake clock gets exact durations.
	if _, isFake := clock.(*rt.FakeClock); isFake {
		return 0
	}

	// Sample the smallest observable delta a few times and take the minimum.
	// The minimum rather than the mean, because a scheduling hiccup inflates a
	// sample and never deflates one, so the mean would overstate the floor and
	// blind the platform to real changes.
	const samples = 5
	best := time.Duration(0)

	for i := 0; i < samples; i++ {
		start := clock.Now()
		var delta time.Duration
		// Spin until the clock reports movement. Bounded by an iteration cap so
		// a pathological clock cannot hang platform construction.
		for n := 0; n < 1_000_000; n++ {
			if d := clock.Since(start); d > 0 {
				delta = d
				break
			}
		}
		if delta == 0 {
			continue
		}
		if best == 0 || delta < best {
			best = delta
		}
	}
	return best
}

// MeasurableAt reports whether a duration is large enough to be distinguished
// from zero by a clock of the given resolution.
//
// The rule of thumb is ten times the resolution: below that, a measurement is
// dominated by the quantisation step and a doubling is indistinguishable from a
// rounding boundary.
func MeasurableAt(d, resolution time.Duration) bool {
	if resolution <= 0 {
		return true
	}
	return d >= 10*resolution
}

// resolutionFloor returns the latency floor a clock justifies.
//
// Ten times the measured resolution, for the reason above. A configured floor
// higher than this is respected — an operator may have a reason to ignore
// larger variations — but a configured floor LOWER than this is raised, because
// honouring it would mean reporting latency drift the clock cannot actually see.
func resolutionFloor(configured, resolution time.Duration) time.Duration {
	floor := 10 * resolution
	if configured > floor {
		return configured
	}
	return floor
}
