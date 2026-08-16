package media

import (
	"fmt"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// MediaClock tracks a stream's media timeline against wall time.
//
// # Two timelines, and the gap between them is the whole subject
//
// MEDIA TIME advances by the duration of the audio produced: 50 frames of 20 ms
// is exactly one second of media, regardless of how long that took to arrive.
// WALL TIME advances by itself.
//
// A healthy stream keeps them together. A source running fast produces 1.02
// seconds of media per second of wall time; one running slow produces 0.98. That
// difference is DRIFT, it accumulates, and over a ten-minute call a 1% drift is
// six seconds of divergence — enough that audio and any timeline derived from it
// no longer agree about when anything happened.
//
// This clock measures drift. It does NOT correct it: correction means dropping
// or inserting samples, which is resampling, which this engine does not do.
// Reporting a number somebody can act on is the honest scope.
type MediaClock struct {
	clock rt.Clock

	mu sync.RWMutex
	// startWall and startMedia anchor the two timelines.
	startWall  time.Time
	startMedia time.Duration
	started    bool

	// mediaElapsed is the summed duration of frames observed.
	mediaElapsed time.Duration
	// frames counts observations, so a drift figure can state its sample size.
	frames uint64

	// lastWall and lastMedia are the previous observation, for instantaneous
	// rate.
	lastWall  time.Time
	lastMedia time.Duration

	// paused time is excluded from wall elapsed: a paused stream produces no
	// media, and counting the pause as drift would report every pause as a
	// fault.
	pausedAt    time.Time
	pausedTotal time.Duration
	paused      bool
}

// NewMediaClock builds a media clock.
func NewMediaClock(clock rt.Clock) *MediaClock {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	return &MediaClock{clock: clock}
}

// Start anchors the timeline at the current instant.
func (c *MediaClock) Start(mediaOffset time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.clock.Now()
	c.startWall = now
	c.startMedia = mediaOffset
	c.lastWall = now
	c.lastMedia = mediaOffset
	c.mediaElapsed = 0
	c.frames = 0
	c.pausedTotal = 0
	c.paused = false
	c.started = true
}

// Started reports whether the clock has been anchored.
func (c *MediaClock) Started() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.started
}

// Observe records that a frame of the given duration was produced.
func (c *MediaClock) Observe(d time.Duration) {
	if d <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.started {
		return
	}
	c.mediaElapsed += d
	c.frames++
	c.lastWall = c.clock.Now()
	c.lastMedia = c.startMedia + c.mediaElapsed
}

// Pause stops wall-time accrual.
func (c *MediaClock) Pause() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started && !c.paused {
		c.paused = true
		c.pausedAt = c.clock.Now()
	}
}

// Resume restarts wall-time accrual, excluding the paused interval.
func (c *MediaClock) Resume() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started && c.paused {
		c.pausedTotal += c.clock.Now().Sub(c.pausedAt)
		c.paused = false
	}
}

// MediaTime returns the current position on the media timeline.
func (c *MediaClock) MediaTime() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.startMedia + c.mediaElapsed
}

// WallElapsed returns wall time since Start, excluding pauses.
func (c *MediaClock) WallElapsed() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.wallElapsedLocked()
}

func (c *MediaClock) wallElapsedLocked() time.Duration {
	if !c.started {
		return 0
	}
	elapsed := c.clock.Now().Sub(c.startWall) - c.pausedTotal
	if c.paused {
		elapsed -= c.clock.Now().Sub(c.pausedAt)
	}
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

// Drift returns media time minus wall time.
//
// Positive means the source is producing FASTER than real time — more audio
// than seconds elapsed. Negative means slower, which is the common case and
// usually means frames are being lost somewhere upstream.
func (c *MediaClock) Drift() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.started || c.frames == 0 {
		return 0
	}
	return c.mediaElapsed - c.wallElapsedLocked()
}

// DriftRatio returns media time over wall time.
//
// 1.0 is perfect. 0.98 means the stream is producing 2% less audio than real
// time — around 1.2 seconds lost per minute.
//
// Returns 1.0 rather than zero when there is nothing to measure: a stream that
// has produced no frames has not drifted, and reporting 0.0 would look like
// total failure on a dashboard.
func (c *MediaClock) DriftRatio() float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.started || c.frames == 0 {
		return 1.0
	}
	wall := c.wallElapsedLocked()
	if wall <= 0 {
		return 1.0
	}
	return float64(c.mediaElapsed) / float64(wall)
}

// Frames returns how many observations have been made.
func (c *MediaClock) Frames() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.frames
}

// ClockStats is a consistent view of the clock.
type ClockStats struct {
	Started     bool
	Paused      bool
	MediaTime   time.Duration
	WallElapsed time.Duration
	Drift       time.Duration
	DriftRatio  float64
	Frames      uint64
	PausedTotal time.Duration
}

// String renders the stats.
func (s ClockStats) String() string {
	return fmt.Sprintf("media=%s wall=%s drift=%s ratio=%.4f frames=%d",
		s.MediaTime.Round(time.Millisecond), s.WallElapsed.Round(time.Millisecond),
		s.Drift.Round(time.Millisecond), s.DriftRatio, s.Frames)
}

// Stats returns a consistent snapshot.
func (c *MediaClock) Stats() ClockStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	wall := c.wallElapsedLocked()
	ratio := 1.0
	var drift time.Duration
	if c.started && c.frames > 0 {
		drift = c.mediaElapsed - wall
		if wall > 0 {
			ratio = float64(c.mediaElapsed) / float64(wall)
		}
	}
	return ClockStats{
		Started: c.started, Paused: c.paused,
		MediaTime: c.startMedia + c.mediaElapsed, WallElapsed: wall,
		Drift: drift, DriftRatio: ratio, Frames: c.frames,
		PausedTotal: c.pausedTotal,
	}
}

// ---------------------------------------------------------------------------
// FrameClock
// ---------------------------------------------------------------------------

// FrameClock predicts when the next frame is due.
//
// A stream at 20 ms frames expects one every 20 ms of media time. This clock
// holds the expected cadence and reports how late a frame is — which is the
// input to both jitter measurement and the stall detector.
type FrameClock struct {
	clock    rt.Clock
	interval time.Duration

	mu sync.RWMutex
	// nextDue is the wall time the next frame is expected by.
	nextDue time.Time
	started bool
	// tolerance is how late a frame may be before it counts as late.
	tolerance time.Duration
}

// NewFrameClock builds a frame clock for the given cadence.
//
// tolerance defaults to half the interval: a frame more than half a frame late
// is late by any reasonable standard, and a tighter bound would flag ordinary
// scheduler jitter on a loaded host.
func NewFrameClock(clock rt.Clock, interval, tolerance time.Duration) *FrameClock {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	if tolerance <= 0 {
		tolerance = interval / 2
	}
	return &FrameClock{clock: clock, interval: interval, tolerance: tolerance}
}

// Interval returns the expected frame cadence.
func (f *FrameClock) Interval() time.Duration { return f.interval }

// Start anchors the clock.
func (f *FrameClock) Start() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextDue = f.clock.Now().Add(f.interval)
	f.started = true
}

// Arrived records a frame arrival and returns how late it was.
//
// Negative means early. The next deadline advances by exactly one interval from
// the PREVIOUS deadline, not from the arrival — anchoring on arrival would let
// a series of late frames walk the schedule forward and make lateness
// unmeasurable, which is the classic mistake in cadence tracking.
func (f *FrameClock) Arrived() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.started {
		f.nextDue = f.clock.Now().Add(f.interval)
		f.started = true
		return 0
	}

	now := f.clock.Now()
	lateness := now.Sub(f.nextDue)
	f.nextDue = f.nextDue.Add(f.interval)

	// If the schedule has fallen so far behind that the next deadline is
	// already past, resynchronise. Otherwise a stream that stalled for a minute
	// spends the next minute reporting every frame as late while it catches up.
	if f.nextDue.Before(now) {
		f.nextDue = now.Add(f.interval)
	}
	return lateness
}

// Late reports whether a lateness exceeds the tolerance.
func (f *FrameClock) Late(lateness time.Duration) bool { return lateness > f.tolerance }

// Overdue reports whether the next frame is already past its deadline plus
// tolerance — the stall signal.
func (f *FrameClock) Overdue() bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if !f.started {
		return false
	}
	return f.clock.Now().After(f.nextDue.Add(f.tolerance))
}

// NextDue returns the wall time the next frame is expected by.
func (f *FrameClock) NextDue() time.Time {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.nextDue
}
