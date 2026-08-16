package media

import (
	"fmt"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// DropReason classifies why the pipeline discarded a frame.
//
// Bounded codes, because these become metric labels. A free-text reason on this
// path would produce unbounded label cardinality — the classic way to take down
// a metrics backend from application code.
type DropReason string

// The drop reasons.
const (
	DropInvalid      DropReason = "invalid"
	DropFormat       DropReason = "format_mismatch"
	DropLate         DropReason = "late"
	DropDuplicate    DropReason = "duplicate"
	DropTooEarly     DropReason = "too_early"
	DropBufferFull   DropReason = "buffer_full"
	DropNotAccepting DropReason = "not_accepting"
	DropTimestamp    DropReason = "timestamp_invalid"
	DropOversized    DropReason = "oversized"
)

// AllDropReasons returns every reason.
func AllDropReasons() []DropReason {
	return []DropReason{DropInvalid, DropFormat, DropLate, DropDuplicate,
		DropTooEarly, DropBufferFull, DropNotAccepting, DropTimestamp, DropOversized}
}

// PipelineResult describes what happened to one frame.
type PipelineResult struct {
	// Accepted reports whether the frame entered the buffer.
	Accepted bool
	// Disposition is the jitter buffer's decision.
	Disposition FrameDisposition
	// Reason explains a drop, empty when accepted.
	Reason DropReason
	// Lateness is how late the frame arrived against the frame clock.
	Lateness time.Duration
	// GapFilled counts silence frames synthesised ahead of this one.
	GapFilled int
}

// PipelineConfig configures a pipeline.
type PipelineConfig struct {
	// Format is the audio the pipeline carries.
	Format AudioFormat

	// FrameInterval is the expected cadence.
	FrameInterval time.Duration

	// MaxTimestampSkew bounds how far a frame's timestamp may sit from the
	// pipeline's own media position before it is refused.
	//
	// A source whose timestamps jump hours ahead — a clock bug, a corrupted
	// header, a replayed session — would otherwise poison the jitter buffer's
	// playout position permanently. This is the guard against one bad frame
	// stalling a stream forever.
	MaxTimestampSkew time.Duration

	// FillGaps synthesises silence for missing sequences.
	FillGaps bool

	// MaxGapFill bounds how much silence one gap may produce. A source that
	// vanished for a minute must not cause a minute of invented audio.
	MaxGapFill time.Duration

	// Jitter configures the reordering buffer.
	Jitter JitterConfig

	// Buffer configures the output ring.
	Buffer BufferConfig
}

// DefaultPipelineConfig returns a telephony-shaped configuration.
func DefaultPipelineConfig(format AudioFormat) PipelineConfig {
	return PipelineConfig{
		Format:        format,
		FrameInterval: 20 * time.Millisecond,
		// Five minutes. Generous enough that a legitimately long stream is
		// never refused, tight enough that a corrupted timestamp is caught
		// before it moves playout somewhere unreachable.
		MaxTimestampSkew: 5 * time.Minute,
		FillGaps:         true,
		MaxGapFill:       200 * time.Millisecond,
		Jitter:           DefaultJitterConfig(),
		Buffer:           DefaultBufferConfig(format),
	}
}

func (c PipelineConfig) validate() []string {
	var problems []string
	if err := c.Format.Validate(); err != nil {
		problems = append(problems, err.Error())
	}
	if c.FrameInterval <= 0 {
		problems = append(problems, "pipeline: FrameInterval must be positive")
	}
	if c.MaxTimestampSkew <= 0 {
		problems = append(problems, "pipeline: MaxTimestampSkew must be positive; "+
			"without it one corrupted timestamp stalls a stream permanently")
	}
	if c.MaxGapFill < 0 {
		problems = append(problems, "pipeline: MaxGapFill must not be negative")
	}
	if c.Buffer.Format != c.Format {
		problems = append(problems, fmt.Sprintf(
			"pipeline: buffer format %s does not match pipeline format %s",
			c.Buffer.Format, c.Format))
	}
	problems = append(problems, c.Jitter.validate()...)
	problems = append(problems, c.Buffer.validate()...)
	return problems
}

// Pipeline validates, orders and delivers frames.
//
// # The stages, in order
//
//  1. Validate     — format, payload alignment, non-empty
//  2. Timestamp    — within skew of the pipeline's media position
//  3. Jitter       — reorder, detect late/duplicate/early
//  4. Sequence     — detect gaps, optionally fill with silence
//  5. Deliver      — into the output ring buffer
//
// Each stage can drop, and every drop is counted under a bounded reason. A
// pipeline that dropped silently would make "the audio is bad" an
// uninvestigable complaint.
type Pipeline struct {
	cfg    PipelineConfig
	clock  rt.Clock
	jitter *JitterBuffer
	out    *RingBuffer
	fclock *FrameClock

	mu sync.Mutex
	// mediaPos is the pipeline's own view of the media timeline, used for the
	// skew check and gap synthesis.
	mediaPos time.Duration
	started  bool
	// nextExpected is the sequence the pipeline expects next, for gap detection.
	nextExpected uint64
	// lastDelivered is the sequence most recently written to the output.
	lastDelivered uint64
	haveDelivered bool

	accepted  uint64
	delivered uint64
	dropped   map[DropReason]uint64
	// droppedTotal mirrors the sum of dropped, so the pump loop can read it
	// without copying the map. See DroppedTotal.
	droppedTotal uint64
	gaps      uint64
	gapFrames uint64
}

// NewPipeline builds a pipeline.
func NewPipeline(cfg PipelineConfig, clock rt.Clock) (*Pipeline, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	if clock == nil {
		clock = rt.SystemClock{}
	}

	jb, err := NewJitterBuffer(cfg.Jitter, clock)
	if err != nil {
		return nil, err
	}
	out, err := NewRingBuffer(cfg.Buffer)
	if err != nil {
		return nil, err
	}

	return &Pipeline{
		cfg: cfg, clock: clock, jitter: jb, out: out,
		fclock:  NewFrameClock(clock, cfg.FrameInterval, 0),
		dropped: make(map[DropReason]uint64, len(AllDropReasons())),
	}, nil
}

// Buffer returns the output ring buffer.
func (p *Pipeline) Buffer() *RingBuffer { return p.out }

// Jitter returns the jitter buffer.
func (p *Pipeline) Jitter() *JitterBuffer { return p.jitter }

// Push offers a frame to the pipeline.
//
// Returns what happened. An error is returned only for a frame the pipeline
// could not evaluate at all; an ordinary drop is reported in the result, because
// dropping frames is normal operation and returning an error for it would train
// callers to ignore errors.
func (p *Pipeline) Push(f Frame) PipelineResult {
	// Stage 1: validate.
	if f.Format != p.cfg.Format {
		p.countDrop(DropFormat)
		return PipelineResult{Reason: DropFormat}
	}
	if err := f.Validate(); err != nil {
		p.countDrop(DropInvalid)
		return PipelineResult{Reason: DropInvalid}
	}
	if len(f.Payload) > p.cfg.Buffer.MaxFrameBytes {
		p.countDrop(DropOversized)
		return PipelineResult{Reason: DropOversized}
	}

	p.mu.Lock()
	if !p.started {
		p.mediaPos = f.Timestamp
		p.nextExpected = f.Sequence
		p.started = true
		p.fclock.Start()
	}

	// Stage 2: timestamp sanity. Checked against the pipeline's own position,
	// which one bad frame cannot move — unlike the jitter buffer's playout.
	skew := f.Timestamp - p.mediaPos
	if skew < 0 {
		skew = -skew
	}
	if skew > p.cfg.MaxTimestampSkew {
		p.mu.Unlock()
		p.countDrop(DropTimestamp)
		return PipelineResult{Reason: DropTimestamp}
	}
	p.mu.Unlock()

	if f.Arrival.IsZero() {
		f.Arrival = p.clock.Now()
	}
	lateness := p.fclock.Arrived()

	// Stage 3: jitter and reordering.
	disp := p.jitter.Put(f)
	if disp.Dropped() {
		reason := DropLate
		switch disp {
		case FrameDuplicate:
			reason = DropDuplicate
		case FrameTooEarly:
			reason = DropTooEarly
		}
		p.countDrop(reason)
		return PipelineResult{Disposition: disp, Reason: reason, Lateness: lateness}
	}

	p.mu.Lock()
	p.accepted++
	if f.End() > p.mediaPos {
		p.mediaPos = f.End()
	}
	p.mu.Unlock()

	return PipelineResult{Accepted: true, Disposition: disp, Lateness: lateness}
}

// Pump moves every due frame from the jitter buffer into the output ring.
//
// Returns how many frames were delivered. Called by the stream's pump loop and
// directly by tests — a pipeline that only advanced on its own ticker could not
// be tested without waiting.
func (p *Pipeline) Pump() int {
	var delivered int

	for {
		// BACKPRESSURE, NOT DESTRUCTION.
		//
		// Taking a frame out of the jitter buffer to hand it to a full ring
		// destroys it: the ring refuses, the frame is gone, and the producer is
		// never told. Leaving it held instead lets the jitter buffer fill to its
		// capacity, at which point Put refuses and the producer learns — one
		// frame's worth of loss instead of an unbounded silent stream of it.
		//
		// DropOldest is exempt: that policy is a deliberate statement that the
		// newest audio matters more than continuity, so the ring is always
		// willing to make room and holding frames back would defeat it.
		if p.out.Full() && p.out.Policy() != DropOldest {
			return delivered
		}

		f, err := p.jitter.Get()
		if err != nil {
			return delivered
		}

		// Stage 4: sequencing and gap detection.
		gapFrames := p.fillGaps(f)

		// Stage 5: deliver.
		if err := p.out.Write(f); err != nil {
			p.countDrop(DropBufferFull)
			continue
		}

		p.mu.Lock()
		p.delivered++
		p.lastDelivered = f.Sequence
		p.haveDelivered = true
		p.nextExpected = f.Sequence + 1
		p.gapFrames += uint64(gapFrames)
		p.mu.Unlock()

		delivered += 1 + gapFrames
	}
}

// fillGaps synthesises silence for sequences missing before f.
//
// Returns how many silence frames were written. Bounded by MaxGapFill: a source
// that vanished for a minute must not produce a minute of invented audio, which
// would delay real audio behind it and tell a transcriber there was a minute of
// silence rather than a minute of nothing.
func (p *Pipeline) fillGaps(f Frame) int {
	if !p.cfg.FillGaps {
		return 0
	}

	p.mu.Lock()
	expected := p.nextExpected
	have := p.haveDelivered
	p.mu.Unlock()

	if !have || f.Sequence <= expected {
		return 0
	}

	missing := f.Sequence - expected
	frameDur := p.cfg.FrameInterval
	maxFrames := int(p.cfg.MaxGapFill / frameDur)
	if maxFrames < 0 {
		maxFrames = 0
	}
	if int(missing) > maxFrames {
		missing = uint64(maxFrames)
	}

	var written int
	ts := f.Timestamp - time.Duration(missing)*frameDur
	for i := uint64(0); i < missing; i++ {
		silence := SilenceFrame(expected+i, ts, p.cfg.Format, frameDur)
		if err := p.out.Write(silence); err != nil {
			break
		}
		ts += frameDur
		written++
	}

	if written > 0 {
		p.mu.Lock()
		p.gaps++
		p.mu.Unlock()
	}
	return written
}

func (p *Pipeline) countDrop(r DropReason) {
	p.mu.Lock()
	p.dropped[r]++
	p.droppedTotal++
	p.mu.Unlock()
}

// DroppedTotal returns how many frames this pipeline has discarded, for any
// reason.
//
// O(1) and cheap enough for the pump loop, which is why it exists alongside
// [Pipeline.Stats]: Stats copies a map and allocates, and the runtime needs this
// figure on every pump of every stream to keep the drop metric honest.
//
// # Why the runtime has to poll for it
//
// Drops happen on two paths. Push-path drops are returned to the caller in a
// [PipelineResult] and the dispatcher records them. Pump-path drops — the output
// ring was full — have no caller to return to, so without this they were counted
// in the pipeline and invisible everywhere else, which is precisely the moment an
// operator most needs to see them.
func (p *Pipeline) DroppedTotal() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.droppedTotal
}

// Flush discards everything held and buffered, returning the total.
func (p *Pipeline) Flush() int {
	return p.jitter.Flush() + p.out.Flush()
}

// Reset returns the pipeline to its initial state, for recovery.
func (p *Pipeline) Reset() {
	p.jitter.Reset()
	p.out.Flush()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.started = false
	p.mediaPos = 0
	p.nextExpected = 0
	p.lastDelivered = 0
	p.haveDelivered = false
}

// PipelineStats is a consistent view of the pipeline.
type PipelineStats struct {
	Accepted  uint64
	Delivered uint64
	Dropped   map[DropReason]uint64
	TotalDrop uint64
	Gaps      uint64
	GapFrames uint64
	MediaPos  time.Duration
	Jitter    JitterStats
	Buffer    BufferStats
}

// DropRate returns dropped frames over frames offered, or zero when none.
func (s PipelineStats) DropRate() float64 {
	offered := s.Accepted + s.TotalDrop
	if offered == 0 {
		return 0
	}
	return float64(s.TotalDrop) / float64(offered)
}

// String renders the stats.
func (s PipelineStats) String() string {
	return fmt.Sprintf("pipeline accepted=%d delivered=%d dropped=%d gaps=%d(%d frames) pos=%s",
		s.Accepted, s.Delivered, s.TotalDrop, s.Gaps, s.GapFrames,
		s.MediaPos.Round(time.Millisecond))
}

// Stats returns a consistent snapshot.
func (p *Pipeline) Stats() PipelineStats {
	p.mu.Lock()
	dropped := make(map[DropReason]uint64, len(p.dropped))
	var total uint64
	for k, v := range p.dropped {
		dropped[k] = v
		total += v
	}
	stats := PipelineStats{
		Accepted: p.accepted, Delivered: p.delivered, Dropped: dropped,
		TotalDrop: total, Gaps: p.gaps, GapFrames: p.gapFrames, MediaPos: p.mediaPos,
	}
	p.mu.Unlock()

	stats.Jitter = p.jitter.Stats()
	stats.Buffer = p.out.Stats()
	return stats
}
