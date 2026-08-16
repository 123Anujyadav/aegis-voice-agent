package toolruntime

import (
	"sync"
	"sync/atomic"
	"time"
)

// ChunkKind classifies a stream chunk.
type ChunkKind uint8

// The chunk kinds.
const (
	// ChunkPartial carries an incremental result fragment.
	ChunkPartial ChunkKind = iota
	// ChunkProgress carries a completion estimate and no data. A long tool
	// with nothing to show yet still owes the caller a sign of life.
	ChunkProgress
	// ChunkHeartbeat carries neither data nor progress. It exists so that
	// "still working" and "hung" are distinguishable, which they are not from
	// silence alone.
	ChunkHeartbeat
)

// String renders the kind.
func (k ChunkKind) String() string {
	switch k {
	case ChunkProgress:
		return "progress"
	case ChunkHeartbeat:
		return "heartbeat"
	default:
		return "partial"
	}
}

// Chunk is one streamed unit from a tool.
type Chunk struct {
	// Kind classifies it.
	Kind ChunkKind
	// Execution identifies the producing execution.
	Execution ExecutionID
	// Step names the plan step.
	Step StepID
	// Sequence is monotonic within one execution, starting at 1. A consumer
	// detects a gap without needing a global ordering service.
	Sequence uint64
	// Fields carries partial results. Nil for progress and heartbeat chunks.
	Fields Result
	// Progress is a completion estimate in [0, 1]. Negative means unknown,
	// which is honest and common; a tool that does not know how far along it
	// is should not invent a number.
	Progress float64
	// At is the emission instant on the runtime clock.
	At time.Time
}

// SizeBytes estimates the chunk's payload size, for budget charging.
func (c Chunk) SizeBytes() int {
	if c.Fields == nil {
		return 0
	}
	return c.Fields.SizeBytes()
}

// StreamSink receives chunks from a streaming tool.
//
// Implementations MUST NOT BLOCK. A sink that blocks stalls the tool that is
// producing, which stalls the execution, which holds a sandbox slot, which
// eventually stalls the runtime. The two implementations here both drop rather
// than block, and both count what they dropped — a silent drop is worse than a
// slow consumer, because nobody finds out.
type StreamSink interface {
	// Write delivers a chunk. A returned error stops the stream; it does not
	// fail the execution, because a consumer going away is not the tool's
	// fault and the final result is still worth having.
	Write(Chunk) error

	// Close signals the end of the stream, carrying the terminal error if any.
	Close(err error)
}

// NoopSink discards every chunk.
//
// The default, so a streaming tool invoked by a caller that does not care about
// partial results still works and still runs its streaming code path. A
// streaming tool whose stream path is never exercised in production until
// someone subscribes is a streaming tool with an untested branch.
type NoopSink struct{}

// Write discards the chunk.
func (NoopSink) Write(Chunk) error { return nil }

// Close does nothing.
func (NoopSink) Close(error) {}

// BufferedSink buffers chunks for a consumer, dropping the newest when full.
//
// DROPS THE NEWEST, not the oldest. For a partial-result stream the early
// chunks are the ones a caller has already started rendering; dropping those
// would rewrite what a person has already seen. Dropping the newest degrades
// the tail, which the final result replaces anyway.
type BufferedSink struct {
	ch      chan Chunk
	dropped atomic.Int64

	mu     sync.Mutex
	closed bool
	err    error
}

// NewBufferedSink builds a sink with a bounded buffer.
func NewBufferedSink(depth int) *BufferedSink {
	if depth <= 0 {
		depth = 32
	}
	return &BufferedSink{ch: make(chan Chunk, depth)}
}

// Write buffers a chunk, dropping it if the buffer is full.
func (s *BufferedSink) Write(c Chunk) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	s.mu.Unlock()

	select {
	case s.ch <- c:
		return nil
	default:
		s.dropped.Add(1)
		return nil
	}
}

// Close ends the stream. Safe to call more than once.
func (s *BufferedSink) Close(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.err = err
	close(s.ch)
}

// Chunks returns the receive channel.
func (s *BufferedSink) Chunks() <-chan Chunk { return s.ch }

// Dropped returns how many chunks were discarded for want of buffer.
func (s *BufferedSink) Dropped() int64 { return s.dropped.Load() }

// Err returns the terminal error, if any.
func (s *BufferedSink) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// FuncSink adapts a function to a sink.
type FuncSink struct {
	// OnChunk receives every chunk. Must not block.
	OnChunk func(Chunk) error
	// OnClose receives the terminal error. Optional.
	OnClose func(error)
}

// Write calls OnChunk.
func (f FuncSink) Write(c Chunk) error {
	if f.OnChunk == nil {
		return nil
	}
	return f.OnChunk(c)
}

// Close calls OnClose.
func (f FuncSink) Close(err error) {
	if f.OnClose != nil {
		f.OnClose(err)
	}
}

// meteredSink charges a lease for every chunk and enforces the output budget.
//
// It wraps whatever sink the caller supplied, which is why an oversized stream
// is caught even when the caller is using a NoopSink and would never have
// noticed. Budget enforcement that only works when someone is watching is not
// enforcement.
type meteredSink struct {
	inner StreamSink
	lease *Lease
	clock func() time.Time

	seq      atomic.Uint64
	exceeded atomic.Bool
	emitted  atomic.Uint64
}

func newMeteredSink(inner StreamSink, lease *Lease, now func() time.Time) *meteredSink {
	if inner == nil {
		inner = NoopSink{}
	}
	return &meteredSink{inner: inner, lease: lease, clock: now}
}

// Write stamps, charges and forwards a chunk.
func (m *meteredSink) Write(c Chunk) error {
	if m.exceeded.Load() {
		return ErrBudgetExceeded
	}
	c.Sequence = m.seq.Add(1)
	if c.At.IsZero() && m.clock != nil {
		c.At = m.clock()
	}
	if m.lease != nil {
		if err := m.lease.ChargeOutput(c.SizeBytes()); err != nil {
			m.exceeded.Store(true)
			return err
		}
	}
	m.emitted.Add(1)
	return m.inner.Write(c)
}

// Close forwards the terminal signal.
func (m *meteredSink) Close(err error) { m.inner.Close(err) }

// Exceeded reports whether the output budget was blown mid-stream.
func (m *meteredSink) Exceeded() bool { return m.exceeded.Load() }

// Emitted returns how many chunks were forwarded.
func (m *meteredSink) Emitted() uint64 { return m.emitted.Load() }
