package audiointel

import (
	"fmt"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
)

// ContinuityReport describes what the transport did to one frame.
type ContinuityReport struct {
	// Fault is what went wrong, or FaultNone.
	Fault ContinuityFault

	// MissingBefore is how many sequence numbers were skipped ahead of this
	// frame.
	MissingBefore int

	// TimestampDelta is the media-time step from the previous frame's END to
	// this frame's start. Zero on a perfectly continuous stream.
	TimestampDelta time.Duration

	// GapRatio is the fraction of the recent window that carried a fault.
	GapRatio float64

	// Degraded reports whether continuity is currently considered broken.
	Degraded bool

	// GapOpened is set on the frame where continuity first broke.
	GapOpened bool

	// Restored is set on the frame where continuity was declared recovered.
	Restored bool
}

// String renders the report.
func (r ContinuityReport) String() string {
	return fmt.Sprintf("continuity %s missing=%d delta=%s ratio=%.3f degraded=%t",
		r.Fault, r.MissingBefore, r.TimestampDelta.Round(time.Millisecond),
		r.GapRatio, r.Degraded)
}

// ContinuityDetector classifies transport faults in the frame stream.
//
// # It consumes Phase 11B's signals and re-implements none of them
//
// Phase 11B owns the jitter buffer, the reordering window and the gap fill. It
// publishes sequence numbers, media timestamps, FlagSilence and
// FlagDiscontinuity, and a per-frame [media.PipelineResult]. This detector
// reads those and says what they mean for the audio; it does not buffer,
// reorder or synthesise anything.
//
// §13 of the phase brief is explicit about that boundary, and the reason is
// that two jitter buffers in series is worse than one: the second re-orders
// what the first already ordered, and the two disagree about what "late" means.
//
// # Duplicate detection is a 64-bit sliding window
//
// Recognising a repeated sequence number needs memory of what has been seen,
// and remembering every sequence in a call is unbounded. A bitmask covering the
// 64 sequences below the highest seen is O(1) in time and space, and 64 frames
// is 1.3 seconds at the default cadence — far beyond any reordering Phase 11B
// would let through, whose own window is measured in tens of milliseconds.
//
// A duplicate older than that is reported as out-of-order instead. That is the
// honest answer: at that distance the two are indistinguishable without the
// unbounded memory this deliberately does not keep.
//
// Not safe for concurrent use. One detector per session.
type ContinuityDetector struct {
	cfg ContinuityConfig

	// highestSeq is the largest sequence number seen, and seenMask records
	// which of the 64 below it have arrived. Bit 0 is highestSeq itself.
	highestSeq uint64
	seenMask   uint64
	haveSeq    bool

	// lastEnd is the media time just past the previous frame.
	lastEnd time.Duration
	haveEnd bool

	// faults is a fixed ring of per-frame fault flags, for the gap ratio.
	faults []bool
	next   int
	filled int
	bad    int

	// cleanRun counts consecutive clean frames toward a restore.
	cleanRun int
	degraded bool
}

// NewContinuityDetector builds a detector for one session.
func NewContinuityDetector(cfg ContinuityConfig) (*ContinuityDetector, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	return &ContinuityDetector{cfg: cfg, faults: make([]bool, cfg.WindowFrames)}, nil
}

// Observe classifies one frame.
func (c *ContinuityDetector) Observe(f FrameFeatures) ContinuityReport {
	report := ContinuityReport{Fault: c.classify(f)}

	if report.Fault == FaultMissing {
		report.MissingBefore = int(f.Sequence - c.highestSeq - 1)
	}

	// Timestamp continuity is measured from the previous frame's END, so a
	// perfectly continuous stream reports exactly zero and any non-zero value
	// is a real step rather than the frame duration.
	if c.haveEnd {
		report.TimestampDelta = f.Timestamp - c.lastEnd
	}

	c.advance(f)
	c.record(report.Fault)

	report.GapRatio = c.gapRatio()

	switch {
	case report.Fault.Degrades():
		c.cleanRun = 0
		if !c.degraded {
			c.degraded = true
			report.GapOpened = true
		}

	default:
		c.cleanRun++
		if c.degraded && c.cleanRun >= c.cfg.RestoreFrames {
			c.degraded = false
			c.cleanRun = 0
			report.Restored = true
		}
	}

	report.Degraded = c.degraded
	return report
}

// classify decides what, if anything, is wrong with this frame.
//
// # Sequence is evaluated before timestamp, and the order is load-bearing
//
// A frame that is late or duplicated by SEQUENCE necessarily also carries an
// old TIMESTAMP — that is what being late means. Checking timestamps first
// therefore reports every duplicate and every reordering as a backwards media
// clock, which is a different and much more alarming fault, and it hides the
// one an operator can actually act on.
//
// So the sequence questions are answered first, and the timestamp questions are
// asked only of frames whose sequence says they are in order. FaultTimestampReverse
// then means what it should: the sequence numbers say forward and the media
// clock says backward, which is a genuine upstream bug rather than ordinary
// reordering.
//
// A missing sequence outranks the timestamp jump it causes, for the same
// reason: the gap is the fault, the jump is its consequence.
func (c *ContinuityDetector) classify(f FrameFeatures) ContinuityFault {
	// Phase 11B invented this frame. Reported as such even though a gap is
	// implied, because "11B covered a hole" and "a hole is open" call for
	// different responses downstream.
	if f.Synthesised() {
		return FaultSynthesised
	}

	if c.haveSeq {
		switch {
		case f.Sequence == c.highestSeq:
			return FaultDuplicate

		case f.Sequence < c.highestSeq:
			if delta := c.highestSeq - f.Sequence; delta < 64 {
				if c.seenMask&(1<<delta) != 0 {
					return FaultDuplicate
				}
				return FaultOutOfOrder
			}
			// Older than the window remembers. Reported as out-of-order rather
			// than guessed at: at this distance a duplicate and a very late
			// frame are indistinguishable without unbounded memory.
			return FaultOutOfOrder

		case f.Sequence > c.highestSeq+1:
			return FaultMissing
		}
	}

	// In order by sequence, so the media clock is now worth questioning.
	if c.haveEnd {
		switch {
		case f.Timestamp < c.lastEnd-c.cfg.MaxTimestampJump:
			return FaultTimestampReverse
		case f.Timestamp > c.lastEnd+c.cfg.MaxTimestampJump:
			return FaultTimestampJump
		}
	}

	return FaultNone
}

// advance moves the sliding window and the timestamp reference.
func (c *ContinuityDetector) advance(f FrameFeatures) {
	if !c.haveSeq {
		c.highestSeq = f.Sequence
		c.seenMask = 1
		c.haveSeq = true
	} else if f.Sequence > c.highestSeq {
		shift := f.Sequence - c.highestSeq
		if shift >= 64 {
			c.seenMask = 1
		} else {
			c.seenMask = (c.seenMask << shift) | 1
		}
		c.highestSeq = f.Sequence
	} else if delta := c.highestSeq - f.Sequence; delta < 64 {
		c.seenMask |= 1 << delta
	}

	// A frame that arrived out of order must not drag the timestamp reference
	// backwards: doing so would make the NEXT in-order frame look like a jump.
	if end := f.End(); !c.haveEnd || end > c.lastEnd {
		c.lastEnd = end
		c.haveEnd = true
	}
}

// record folds one verdict into the bounded fault ring.
func (c *ContinuityDetector) record(fault ContinuityFault) {
	if c.filled == len(c.faults) && c.faults[c.next] {
		c.bad--
	}
	c.faults[c.next] = fault.Degrades()
	if fault.Degrades() {
		c.bad++
	}
	c.next = (c.next + 1) % len(c.faults)
	if c.filled < len(c.faults) {
		c.filled++
	}
}

func (c *ContinuityDetector) gapRatio() float64 {
	if c.filled == 0 {
		return 0
	}
	return float64(c.bad) / float64(c.filled)
}

// ObserveDelivery folds in Phase 11B's own verdict on a frame it refused or
// could not deliver.
//
// # Why this exists alongside Observe
//
// Observe sees frames that ARRIVED. It cannot see a frame the pipeline dropped
// for backpressure or a read that found an empty buffer, because by definition
// no frame reaches this engine for those. Phase 11B knows, and reports it in a
// [media.PipelineResult], so this is the seam through which the two views are
// joined.
//
// Called by the session when a caller supplies the pipeline result. Entirely
// optional — a deployment that does not thread the result through gets
// arrival-side continuity only, which is most of it.
func (c *ContinuityDetector) ObserveDelivery(r media.PipelineResult) ContinuityFault {
	if r.Accepted {
		return FaultNone
	}

	fault := FaultNone
	switch r.Reason {
	case media.DropDuplicate:
		fault = FaultDuplicate
	case media.DropLate, media.DropTooEarly:
		fault = FaultOutOfOrder
	case media.DropTimestamp:
		fault = FaultTimestampJump
	case media.DropBufferFull, media.DropNotAccepting:
		fault = FaultStarvation
	default:
		// DropInvalid, DropFormat and DropOversized are malformed input rather
		// than transport continuity faults. They are the producer disagreeing
		// with the pipeline about the format, which the media runtime already
		// counts and which says nothing about whether the audio is continuous.
		return FaultNone
	}

	c.record(fault)
	c.cleanRun = 0
	c.degraded = true
	return fault
}

// Degraded reports whether continuity is currently broken.
func (c *ContinuityDetector) Degraded() bool { return c.degraded }

// GapRatio returns the fraction of the recent window that carried a fault.
func (c *ContinuityDetector) GapRatio() float64 { return c.gapRatio() }

// Reset returns the detector to its initial state, keeping its storage.
func (c *ContinuityDetector) Reset() {
	c.highestSeq, c.seenMask, c.haveSeq = 0, 0, false
	c.lastEnd, c.haveEnd = 0, false
	for i := range c.faults {
		c.faults[i] = false
	}
	c.next, c.filled, c.bad = 0, 0, 0
	c.cleanRun, c.degraded = 0, false
}
