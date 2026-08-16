package media

import (
	"fmt"
	"sort"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// JitterEstimator measures arrival-time variation.
//
// # The measure is RFC 3550's, the transport is not
//
// This engine implements no RTP. But the jitter formula RTP uses is simply the
// right one for the problem — a smoothed mean deviation between the media
// interval and the arrival interval — and reinventing a worse estimator to
// avoid resembling a specification would be perverse.
//
//	D(i-1,i) = (arrival_i - arrival_{i-1}) - (media_i - media_{i-1})
//	J = J + (|D| - J) / 16
//
// The 1/16 gain is the standard smoothing: fast enough to track a real change
// in network conditions within a second at 50 fps, slow enough that one late
// frame does not move the estimate meaningfully. A jitter estimate that jumped
// on every outlier would make the adaptive buffer oscillate.
type JitterEstimator struct {
	mu sync.RWMutex

	jitter    time.Duration
	lastMedia time.Duration
	lastWall  time.Time
	started   bool
	samples   uint64

	// peak retains the largest single deviation, which the smoothed estimate
	// deliberately hides and an operator investigating an incident wants.
	peak time.Duration
}

// jitterGain is the RFC 3550 smoothing divisor.
const jitterGain = 16

// NewJitterEstimator builds an estimator.
func NewJitterEstimator() *JitterEstimator { return &JitterEstimator{} }

// Observe records a frame's media timestamp and arrival, returning the updated
// estimate.
func (j *JitterEstimator) Observe(media time.Duration, arrival time.Time) time.Duration {
	j.mu.Lock()
	defer j.mu.Unlock()

	if !j.started {
		j.lastMedia, j.lastWall, j.started = media, arrival, true
		j.samples = 1
		return 0
	}

	transit := arrival.Sub(j.lastWall) - (media - j.lastMedia)
	if transit < 0 {
		transit = -transit
	}
	j.jitter += (transit - j.jitter) / jitterGain
	if transit > j.peak {
		j.peak = transit
	}

	j.lastMedia, j.lastWall = media, arrival
	j.samples++
	return j.jitter
}

// Jitter returns the current estimate.
func (j *JitterEstimator) Jitter() time.Duration {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.jitter
}

// Peak returns the largest single deviation observed.
func (j *JitterEstimator) Peak() time.Duration {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.peak
}

// Samples returns how many observations have been made.
func (j *JitterEstimator) Samples() uint64 {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.samples
}

// Reset clears the estimate. Used when a stream recovers, because arrival
// history from before a reconnection describes a network that no longer exists.
func (j *JitterEstimator) Reset() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.jitter, j.peak, j.samples, j.started = 0, 0, 0, false
}

// ---------------------------------------------------------------------------
// Jitter buffer
// ---------------------------------------------------------------------------

// FrameDisposition is what the jitter buffer decided about a frame.
type FrameDisposition uint8

// The dispositions.
const (
	// FrameAccepted was buffered in order.
	FrameAccepted FrameDisposition = iota
	// FrameReordered arrived out of order and was inserted at its correct
	// position.
	FrameReordered
	// FrameLate arrived after its playout position had passed and was dropped.
	FrameLate
	// FrameDuplicate repeated a sequence already held or already released.
	FrameDuplicate
	// FrameTooEarly arrived so far ahead that buffering it would exceed the
	// window.
	FrameTooEarly
)

// String implements fmt.Stringer.
func (d FrameDisposition) String() string {
	switch d {
	case FrameReordered:
		return "reordered"
	case FrameLate:
		return "late"
	case FrameDuplicate:
		return "duplicate"
	case FrameTooEarly:
		return "too_early"
	default:
		return "accepted"
	}
}

// Dropped reports whether the disposition means the frame was discarded.
func (d FrameDisposition) Dropped() bool {
	return d == FrameLate || d == FrameDuplicate || d == FrameTooEarly
}

// JitterConfig configures a jitter buffer.
type JitterConfig struct {
	// TargetDelay is how much audio the buffer holds before releasing.
	//
	// The central trade in the whole engine: more delay absorbs more network
	// variation and adds exactly that much latency to the conversation. 60 ms
	// is the telephony default — three 20 ms frames, enough for ordinary
	// reordering, below the ~150 ms where humans start talking over each other.
	TargetDelay time.Duration

	// MaxDelay caps adaptive growth.
	MaxDelay time.Duration

	// MinDelay floors it.
	MinDelay time.Duration

	// Capacity bounds held frames.
	Capacity int

	// Adaptive lets the buffer track the measured jitter.
	Adaptive bool
}

// DefaultJitterConfig returns the telephony baseline.
func DefaultJitterConfig() JitterConfig {
	return JitterConfig{
		TargetDelay: 60 * time.Millisecond,
		MinDelay:    20 * time.Millisecond,
		MaxDelay:    200 * time.Millisecond,
		Capacity:    32,
		Adaptive:    true,
	}
}

func (c JitterConfig) validate() []string {
	var problems []string
	if c.TargetDelay <= 0 {
		problems = append(problems, "jitter: TargetDelay must be positive")
	}
	if c.MinDelay < 0 {
		problems = append(problems, "jitter: MinDelay must not be negative")
	}
	if c.MaxDelay <= 0 {
		problems = append(problems, "jitter: MaxDelay must be positive")
	}
	if c.MinDelay > c.MaxDelay {
		problems = append(problems, fmt.Sprintf(
			"jitter: MinDelay (%s) exceeds MaxDelay (%s)", c.MinDelay, c.MaxDelay))
	}
	if c.TargetDelay < c.MinDelay || c.TargetDelay > c.MaxDelay {
		problems = append(problems, fmt.Sprintf(
			"jitter: TargetDelay (%s) is outside [%s, %s]",
			c.TargetDelay, c.MinDelay, c.MaxDelay))
	}
	if c.Capacity <= 0 {
		problems = append(problems, "jitter: Capacity must be positive")
	}
	return problems
}

// heldFrame is a frame waiting for its playout time.
type heldFrame struct {
	frame Frame
	// releaseAt is the media time at which this frame may be delivered.
	releaseAt time.Duration
}

// JitterBuffer reorders frames and releases them on a delayed timeline.
//
// # It works in MEDIA time, not wall time
//
// A frame is released when the playout position reaches its timestamp, and the
// playout position advances by the audio consumed. This is what makes the buffer
// deterministic and testable: the same input sequence produces the same output
// sequence regardless of how fast the test ran.
//
// # It holds cloned frames
//
// Frames are retained across calls, so borrowing the caller's payload would be
// exactly the hazard [Frame] warns about. This is one of the two places in the
// package that clones deliberately.
type JitterBuffer struct {
	cfg   JitterConfig
	clock rt.Clock
	est   *JitterEstimator

	mu sync.Mutex

	held []heldFrame
	// playout is the media position frames are released against.
	playout time.Duration
	started bool

	// frontier is the highest media position any offered frame has reached.
	//
	// The anchor for the too-early bound. Unlike playout it advances on ARRIVAL
	// rather than on consumption, so a stalled consumer cannot freeze it and a
	// shrinking adaptive delay cannot narrow the window. What "too early" should
	// mean is "far ahead of the data", not "far ahead of a reader that stopped".
	frontier time.Duration

	// releaseFrontier is the monotonic high-water mark of playout+current.
	// See releaseLimitLocked.
	releaseFrontier time.Duration

	// highestReleased tracks the last sequence handed out, so a duplicate of an
	// already-released frame is recognised rather than treated as reordering.
	highestReleased uint64
	// seen holds sequences currently buffered, for duplicate detection.
	seen map[uint64]struct{}

	// current is the adaptive delay in force.
	current time.Duration

	accepted   uint64
	reordered  uint64
	late       uint64
	duplicates uint64
	tooEarly   uint64
	released   uint64
	// skipped counts holes stepped over to keep playout moving. An operator
	// reading a rising value is reading packet loss.
	skipped uint64
}

// NewJitterBuffer builds a jitter buffer.
func NewJitterBuffer(cfg JitterConfig, clock rt.Clock) (*JitterBuffer, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	if clock == nil {
		clock = rt.SystemClock{}
	}
	return &JitterBuffer{
		cfg: cfg, clock: clock, est: NewJitterEstimator(),
		held:    make([]heldFrame, 0, cfg.Capacity),
		seen:    make(map[uint64]struct{}, cfg.Capacity),
		current: cfg.TargetDelay,
	}, nil
}

// Delay returns the delay currently in force.
func (j *JitterBuffer) Delay() time.Duration {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.current
}

// Jitter returns the measured jitter.
func (j *JitterBuffer) Jitter() time.Duration { return j.est.Jitter() }

// Put offers a frame and returns what the buffer decided.
func (j *JitterBuffer) Put(f Frame) FrameDisposition {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.est.Observe(f.Timestamp, f.Arrival)

	if !j.started {
		// The first frame anchors the playout position one delay behind it, so
		// subsequent frames have somewhere to be late from.
		j.playout = f.Timestamp - j.current
		j.frontier = f.End()
		j.started = true
	}

	if j.cfg.Adaptive {
		j.adaptLocked()
	}

	// A duplicate of something buffered, or of something already released.
	if _, dup := j.seen[f.Sequence]; dup ||
		(j.released > 0 && f.Sequence <= j.highestReleased) {
		j.duplicates++
		return FrameDuplicate
	}

	// Late: its playout moment has already passed. Nothing useful can be done
	// with it — inserting it would deliver audio out of order to a consumer
	// that has already moved past that instant.
	if f.Timestamp < j.playout {
		j.late++
		return FrameLate
	}

	// Too early: so far ahead of the data already seen that buffering it would
	// exceed the window. Measured against the frontier, NOT against playout —
	// see the frontier field. Capacity, checked next, is what bounds a producer
	// that outruns its consumer; that is backpressure and belongs there.
	if f.Timestamp > j.frontier+j.cfg.MaxDelay {
		j.tooEarly++
		return FrameTooEarly
	}

	if len(j.held) >= j.cfg.Capacity {
		// Full. The oldest held frame is closest to release, so the newest is
		// the one to refuse — the same reasoning as DropNewest in the ring.
		j.tooEarly++
		return FrameTooEarly
	}

	held := heldFrame{frame: f.Clone(), releaseAt: f.Timestamp}

	// Insert in timestamp order. A linear scan from the end, because frames
	// arrive nearly in order and the insertion point is almost always the last
	// position — which makes this O(1) in the common case and O(n) only when
	// reordering actually happened.
	pos := len(j.held)
	for pos > 0 && j.held[pos-1].releaseAt > held.releaseAt {
		pos--
	}
	j.held = append(j.held, heldFrame{})
	copy(j.held[pos+1:], j.held[pos:])
	j.held[pos] = held
	if end := f.End(); end > j.frontier {
		j.frontier = end
	}
	j.seen[f.Sequence] = struct{}{}

	if pos != len(j.held)-1 {
		j.reordered++
		return FrameReordered
	}
	j.accepted++
	return FrameAccepted
}

// adaptLocked moves the delay toward the measured jitter. Caller holds the lock.
//
// Target is twice the measured jitter, bounded by the configured range. Twice
// because jitter is a mean deviation and a buffer sized at exactly the mean
// absorbs only half the variation; 2× covers the great majority without paying
// for the tail, which is what MaxDelay is for.
//
// Moves in one-frame steps rather than jumping. A buffer that resized abruptly
// would either discard audio (shrinking) or insert a gap (growing), both
// audible.
//
// # playout moves with current, and that is the point
//
// Release is gated on playout+current. playout was anchored under the delay in
// force at the time; changing current alone would move that sum and
// retroactively make already-due frames un-due, stalling the buffer. Shifting
// playout by the same delta holds the release frontier continuous, so adaptation
// changes how much audio is held going forward without disturbing what is
// already due.
func (j *JitterBuffer) adaptLocked() {
	target := j.est.Jitter() * 2
	if target < j.cfg.MinDelay {
		target = j.cfg.MinDelay
	}
	if target > j.cfg.MaxDelay {
		target = j.cfg.MaxDelay
	}

	const step = 5 * time.Millisecond
	switch {
	case target > j.current+step:
		j.current += step
	case target < j.current-step:
		j.current -= step
	}
}

// releaseLimitLocked returns the media position up to which frames are due.
//
// # It never moves backwards, and that is the whole point
//
// The limit is playout+current, but current SHRINKS as the estimator learns the
// line is clean. Reading playout+current directly means a frame that was due a
// moment ago stops being due, and a buffer whose consumer is idle — so playout
// is frozen — walks its own release limit backwards until it stalls holding
// audio it has already decided to deliver.
//
// Latching the maximum makes the limit monotonic. Adaptation still changes how
// much audio is held going forward, because playout advances on every release
// and the fresh playout+current overtakes the latch within a frame or two; what
// it can no longer do is retract a decision already made.
//
// playout itself is deliberately NOT adjusted here. It means "media already
// consumed" and late detection is gated on it; moving it to compensate for a
// delay change would report the next perfectly-on-time frame as late.
func (j *JitterBuffer) releaseLimitLocked() time.Duration {
	if limit := j.playout + j.current; limit > j.releaseFrontier {
		j.releaseFrontier = limit
	}
	return j.releaseFrontier
}

// Get releases the next frame whose playout time has arrived.
//
// Returns [ErrBufferEmpty] when nothing is due — which is normal, not a fault.
// The returned frame owns its payload, because the buffer cloned on Put.
func (j *JitterBuffer) Get() (Frame, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if len(j.held) == 0 {
		return Frame{}, ErrBufferEmpty
	}

	head := j.held[0]
	if head.releaseAt > j.releaseLimitLocked() {
		// Not yet due. Holding it is the entire point of the buffer — UNLESS a
		// lost frame is what stands between playout and the head.
		//
		// # The stall this prevents
		//
		// playout advances only here, on release. A frame is released only once
		// playout reaches it. When a sequence goes missing, the head sits at a
		// timestamp beyond where playout can reach, and nothing will ever move
		// either: the buffer holds audio it has decided is not due, forever. One
		// lost packet stalls the stream permanently.
		//
		// Waiting longer cannot help. The missing sequences are older than audio
		// already held, so if they arrive at all they arrive late and are refused
		// as late — which is the same outcome, minus the stall.
		//
		// So step over the hole: move playout to the head and release it. The
		// pipeline sees the sequence discontinuity and synthesises bounded
		// silence for it, which is where a gap is supposed to be handled.
		if !j.holeBlocksHeadLocked(head) {
			return Frame{}, ErrBufferEmpty
		}
		j.playout = head.releaseAt
		j.releaseFrontier = j.playout + j.current
		j.skipped++
	}

	j.held = j.held[1:]
	delete(j.seen, head.frame.Sequence)
	j.playout = head.frame.End()
	j.highestReleased = head.frame.Sequence
	j.released++
	return head.frame, nil
}

// holeBlocksHeadLocked reports whether a missing sequence sits between the last
// released frame and the head. Caller holds the lock.
//
// Requires that something has already been released: before the first release
// there is no "last delivered" to be discontinuous with, and the initial playout
// anchor sits deliberately behind the first frame, so treating that as a hole
// would release the whole buffer immediately and remove the delay entirely.
func (j *JitterBuffer) holeBlocksHeadLocked(head heldFrame) bool {
	return j.released > 0 && head.frame.Sequence > j.highestReleased+1
}

// Ready reports how many frames are due for release.
func (j *JitterBuffer) Ready() int {
	j.mu.Lock()
	defer j.mu.Unlock()

	limit := j.releaseLimitLocked()
	var n int
	for _, h := range j.held {
		if h.releaseAt > limit {
			break
		}
		n++
	}
	// A hole-blocked head is releasable even though it is not due, so reporting
	// zero here would tell a caller there is nothing to pump at exactly the
	// moment pumping is what unsticks the stream.
	if n == 0 && len(j.held) > 0 && j.holeBlocksHeadLocked(j.held[0]) {
		n = 1
	}
	return n
}

// Len returns how many frames are held.
func (j *JitterBuffer) Len() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.held)
}

// Depth returns the buffered audio duration.
func (j *JitterBuffer) Depth() time.Duration {
	j.mu.Lock()
	defer j.mu.Unlock()

	var total time.Duration
	for _, h := range j.held {
		total += h.frame.Duration()
	}
	return total
}

// Peek returns the held frames in playout order, oldest first, without
// disturbing the buffer.
//
// The frames are CLONED. The buffer's own copies stay live, and a caller that
// mutated a borrowed payload would corrupt audio still due for delivery — the
// hazard [Frame] warns about, in the one place a caller is most likely to hold
// on to what it is given.
//
// Used by [Stream.Snapshot], because audio held here is in flight and a
// snapshot that omitted it would restore a stream missing exactly the audio
// buffer recovery exists to save.
func (j *JitterBuffer) Peek() []Frame {
	j.mu.Lock()
	defer j.mu.Unlock()

	if len(j.held) == 0 {
		return nil
	}
	out := make([]Frame, 0, len(j.held))
	for _, h := range j.held {
		out = append(out, h.frame.Clone())
	}
	return out
}

// Flush discards held frames and returns how many went.
func (j *JitterBuffer) Flush() int {
	j.mu.Lock()
	defer j.mu.Unlock()

	n := len(j.held)
	j.held = j.held[:0]
	j.seen = make(map[uint64]struct{}, j.cfg.Capacity)
	return n
}

// Reset returns the buffer to its initial state.
//
// Used on recovery: sequence numbers and arrival history from before a
// reconnection describe a source that no longer exists, and carrying them
// forward would report every frame of the new source as a duplicate or as
// wildly late.
func (j *JitterBuffer) Reset() {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.held = j.held[:0]
	j.seen = make(map[uint64]struct{}, j.cfg.Capacity)
	j.playout = 0
	j.frontier = 0
	j.releaseFrontier = 0
	j.started = false
	j.highestReleased = 0
	j.skipped = 0
	j.current = j.cfg.TargetDelay
	j.est.Reset()
}

// JitterStats is a consistent view of the buffer.
type JitterStats struct {
	Held       int
	Capacity   int
	Depth      time.Duration
	Delay      time.Duration
	Jitter     time.Duration
	PeakJitter time.Duration
	Playout    time.Duration
	Accepted   uint64
	Reordered  uint64
	Late       uint64
	Duplicates uint64
	TooEarly   uint64
	Released   uint64
}

// Offered returns how many frames were presented.
func (s JitterStats) Offered() uint64 {
	return s.Accepted + s.Reordered + s.Late + s.Duplicates + s.TooEarly
}

// LossRate returns discarded frames over offered, or zero when none.
func (s JitterStats) LossRate() float64 {
	offered := s.Offered()
	if offered == 0 {
		return 0
	}
	return float64(s.Late+s.Duplicates+s.TooEarly) / float64(offered)
}

// String renders the stats.
func (s JitterStats) String() string {
	return fmt.Sprintf("jitter held=%d/%d depth=%s delay=%s jitter=%s peak=%s "+
		"accepted=%d reordered=%d late=%d dup=%d early=%d released=%d",
		s.Held, s.Capacity, s.Depth.Round(time.Millisecond),
		s.Delay.Round(time.Millisecond), s.Jitter.Round(time.Microsecond),
		s.PeakJitter.Round(time.Microsecond), s.Accepted, s.Reordered,
		s.Late, s.Duplicates, s.TooEarly, s.Released)
}

// Stats returns a consistent snapshot.
func (j *JitterBuffer) Stats() JitterStats {
	j.mu.Lock()
	defer j.mu.Unlock()

	var depth time.Duration
	for _, h := range j.held {
		depth += h.frame.Duration()
	}
	return JitterStats{
		Held: len(j.held), Capacity: j.cfg.Capacity, Depth: depth,
		Delay: j.current, Jitter: j.est.Jitter(), PeakJitter: j.est.Peak(),
		Playout: j.playout, Accepted: j.accepted, Reordered: j.reordered,
		Late: j.late, Duplicates: j.duplicates, TooEarly: j.tooEarly,
		Released: j.released,
	}
}

// sortHeld is a diagnostic that asserts the held slice is ordered.
//
// Exported behaviour depends on the invariant that held frames are sorted by
// release time; this makes it checkable from a test without exposing internals.
func (j *JitterBuffer) ordered() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return sort.SliceIsSorted(j.held, func(a, b int) bool {
		return j.held[a].releaseAt < j.held[b].releaseAt
	})
}
