package media

import (
	"fmt"
	"sync"
	"time"
)

// OverflowPolicy is what a full buffer does with a new frame.
type OverflowPolicy uint8

// The overflow policies.
const (
	// DropNewest refuses the incoming frame and keeps what is buffered.
	//
	// THE DEFAULT, and the right one for real-time audio. When a buffer is
	// full the consumer is behind; the buffered frames are older and therefore
	// closer to being played, and discarding them to make room for newer audio
	// would create a gap in what the consumer is about to read while the newest
	// frame sits waiting behind everything else anyway.
	DropNewest OverflowPolicy = iota

	// DropOldest evicts the oldest buffered frame to make room.
	//
	// Correct when freshness beats continuity — a live monitoring feed where an
	// operator wants the current moment, not a faithful recording of a stalled
	// one. Wrong for a transcription pipeline, which needs continuity.
	DropOldest

	// Block refuses with [ErrBufferFull] and lets the caller decide.
	//
	// Named "Block" for the caller's intent, but this buffer NEVER blocks
	// internally: a blocking write inside a media engine holds a producer
	// goroutine that is usually a network reader, and a stalled network reader
	// backs up into the carrier. The caller blocks if it chooses to.
	Block
)

// String implements fmt.Stringer.
func (p OverflowPolicy) String() string {
	switch p {
	case DropOldest:
		return "drop_oldest"
	case Block:
		return "block"
	default:
		return "drop_newest"
	}
}

// Valid reports whether the policy is declared.
func (p OverflowPolicy) Valid() bool { return p <= Block }

// slot is one frame's storage inside a ring buffer.
//
// Payload is a sub-slice of the buffer's single backing array. Frames never own
// their bytes and the array is allocated once, which is what makes the steady
// state allocation-free.
type slot struct {
	sequence  uint64
	timestamp time.Duration
	arrival   time.Time
	flags     FrameFlags
	// off and length locate this frame's bytes in the backing array.
	off    int
	length int
}

// RingBuffer is a bounded frame queue with contiguous sample storage.
//
// # Two rings, not one
//
// A ring of frame METADATA and a single backing array for PAYLOADS. The
// alternative — a ring of []byte, one allocation per frame — is the obvious
// design and it allocates fifty thousand times a second at a thousand streams.
//
// The cost of the chosen design is that payload storage is contiguous and a
// frame's bytes may not wrap, so a frame that would straddle the end of the
// array is placed at the start instead and the tail of the array is left unused
// until the read pointer passes it. That wastes at most one frame's worth of
// space and removes an entire class of allocation.
//
// # Safe for one producer and one consumer, and for more with the lock
//
// Every operation takes the buffer's mutex. A lock-free SPSC ring was
// considered: it is faster in a microbenchmark and it makes Snapshot, Flush and
// the drop policies either impossible or subtly wrong, because all three need a
// consistent view of both pointers at once. At the measured cost of a mutex
// against a 20 ms frame budget, correctness wins by five orders of magnitude.
type RingBuffer struct {
	format   AudioFormat
	policy   OverflowPolicy
	maxFrame int

	mu sync.Mutex

	slots []slot
	// head is the next slot to read; tail the next to write.
	head, tail int
	count      int

	// data is the payload backing array, allocated once and never grown.
	data []byte
	// dataHead and dataTail bound the live region of data.
	dataHead, dataTail int
	dataUsed           int

	// Counters. Read under the lock, so a Stats call is a consistent snapshot
	// rather than a set of independently-racing atomics.
	written    uint64
	read       uint64
	dropped    uint64
	overflows  uint64
	underflows uint64

	closed bool
}

// BufferConfig configures a ring buffer.
type BufferConfig struct {
	// Format is the audio the buffer carries.
	Format AudioFormat

	// Capacity is the maximum number of frames held.
	Capacity int

	// MaxFrameBytes bounds a single frame's payload. A frame larger than this
	// is refused with [ErrFrameTooLarge] rather than accepted and truncated.
	MaxFrameBytes int

	// Policy is what a full buffer does.
	Policy OverflowPolicy
}

// DefaultBufferConfig returns a configuration sized for telephony.
//
// 50 frames at 20 ms is one second of audio — enough to absorb a GC pause or a
// scheduler hiccup, short enough that a stalled consumer is detected within a
// second rather than accumulating a minute of stale audio nobody will play.
func DefaultBufferConfig(format AudioFormat) BufferConfig {
	return BufferConfig{
		Format:   format,
		Capacity: 50,
		// 4 KB holds 20 ms of 48 kHz stereo PCM16 with room to spare, and
		// refuses anything that is plainly not one frame of audio.
		MaxFrameBytes: 4096,
		Policy:        DropNewest,
	}
}

func (c BufferConfig) validate() []string {
	var problems []string
	if err := c.Format.Validate(); err != nil {
		problems = append(problems, err.Error())
	}
	if c.Capacity <= 0 {
		problems = append(problems, "buffer: Capacity must be positive; an unbounded "+
			"media buffer accumulates stale audio nobody will play")
	}
	if c.MaxFrameBytes <= 0 {
		problems = append(problems, "buffer: MaxFrameBytes must be positive")
	}
	if !c.Policy.Valid() {
		problems = append(problems, fmt.Sprintf("buffer: unknown overflow policy %d", c.Policy))
	}
	return problems
}

// NewRingBuffer builds a ring buffer.
//
// Allocates its entire payload array up front — capacity × maxFrameBytes — so
// no write ever allocates. That is a deliberate trade of memory for latency
// predictability: at the defaults it is 200 KB per stream, and a thousand
// streams is 200 MB, which is the number to size a deployment against.
func NewRingBuffer(cfg BufferConfig) (*RingBuffer, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	return &RingBuffer{
		format:   cfg.Format,
		policy:   cfg.Policy,
		maxFrame: cfg.MaxFrameBytes,
		slots:    make([]slot, cfg.Capacity),
		data:     make([]byte, cfg.Capacity*cfg.MaxFrameBytes),
	}, nil
}

// Capacity returns the frame capacity.
func (b *RingBuffer) Capacity() int { return len(b.slots) }

// Format returns the audio format.
func (b *RingBuffer) Format() AudioFormat { return b.format }

// Policy returns the overflow policy.
func (b *RingBuffer) Policy() OverflowPolicy { return b.policy }

// Len returns the number of buffered frames.
func (b *RingBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}

// Full reports whether the buffer cannot accept another frame.
func (b *RingBuffer) Full() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count == len(b.slots)
}

// Empty reports whether the buffer holds nothing.
func (b *RingBuffer) Empty() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count == 0
}

// Depth returns the buffered audio duration.
//
// THE number an operator watches. Frame count is misleading across formats — 50
// frames is one second at 20 ms and 250 ms at 5 ms — and buffer depth in time is
// what maps to the latency a listener experiences.
func (b *RingBuffer) Depth() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.depthLocked()
}

func (b *RingBuffer) depthLocked() time.Duration {
	var total time.Duration
	for i := 0; i < b.count; i++ {
		s := &b.slots[(b.head+i)%len(b.slots)]
		total += b.format.DurationFor(s.length)
	}
	return total
}

// Write appends a frame.
//
// # It copies the payload and does not retain the caller's slice
//
// The caller may reuse its buffer immediately. This is the opposite of the read
// side's borrowing rule and it is the right asymmetry: a producer usually reads
// into a reusable scratch buffer, and requiring it to allocate per frame would
// push the allocation this design exists to avoid one layer up.
func (b *RingBuffer) Write(f Frame) error {
	if len(f.Payload) > b.maxFrame {
		return fmt.Errorf("%w: %d bytes exceeds the %d-byte maximum",
			ErrFrameTooLarge, len(f.Payload), b.maxFrame)
	}
	if f.Format != b.format {
		return fmt.Errorf("%w: buffer is %s, frame is %s",
			ErrFormatMismatch, b.format, f.Format)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return ErrStreamClosed
	}

	if b.count == len(b.slots) {
		b.overflows++
		switch b.policy {
		case DropOldest:
			// Evict the oldest to make room. The dropped frame is counted, so a
			// consumer that later sees a sequence gap can distinguish "the
			// network lost it" from "we chose to discard it".
			b.dropOldestLocked()
		default:
			// DropNewest and Block both refuse. They differ only in what the
			// caller is expected to do about it.
			b.dropped++
			return ErrBufferFull
		}
	}

	// Place the payload. If it would straddle the end of the backing array,
	// start at the beginning instead — the tail remainder is left unused until
	// the read pointer passes it. Wastes at most one frame; removes wrapping
	// from every read.
	n := len(f.Payload)
	if b.dataTail+n > len(b.data) {
		b.dataTail = 0
	}
	copy(b.data[b.dataTail:b.dataTail+n], f.Payload)

	b.slots[b.tail] = slot{
		sequence: f.Sequence, timestamp: f.Timestamp, arrival: f.Arrival,
		flags: f.Flags, off: b.dataTail, length: n,
	}
	b.dataTail += n
	b.dataUsed += n
	b.tail = (b.tail + 1) % len(b.slots)
	b.count++
	b.written++
	return nil
}

// dropOldestLocked evicts the head frame. The caller holds the lock.
func (b *RingBuffer) dropOldestLocked() {
	s := &b.slots[b.head]
	b.dataUsed -= s.length
	b.head = (b.head + 1) % len(b.slots)
	b.count--
	b.dropped++
}

// Read removes and returns the oldest frame.
//
// # THE RETURNED FRAME'S PAYLOAD IS BORROWED
//
// It points into the buffer's backing array and stays valid only until enough
// subsequent writes wrap around and overwrite it. A caller that keeps the frame
// must call [Frame.Clone] or [Frame.CloneInto].
//
// This is the rule that makes the engine zero-allocation, and it is the sharpest
// edge in the package. Every place inside this package that retains a frame
// clones it, and TestBuffer_ReadPayloadIsBorrowed demonstrates the hazard so
// nobody has to discover it in production.
func (b *RingBuffer) Read() (Frame, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.count == 0 {
		b.underflows++
		return Frame{}, ErrBufferEmpty
	}

	s := b.slots[b.head]
	b.head = (b.head + 1) % len(b.slots)
	b.count--
	b.dataUsed -= s.length
	b.read++

	return Frame{
		Sequence: s.sequence, Timestamp: s.timestamp, Arrival: s.arrival,
		Format: b.format, Payload: b.data[s.off : s.off+s.length], Flags: s.flags,
	}, nil
}

// Peek returns the oldest frame without removing it.
//
// Same borrowing rule as [RingBuffer.Read].
func (b *RingBuffer) Peek() (Frame, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.count == 0 {
		return Frame{}, ErrBufferEmpty
	}
	s := b.slots[b.head]
	return Frame{
		Sequence: s.sequence, Timestamp: s.timestamp, Arrival: s.arrival,
		Format: b.format, Payload: b.data[s.off : s.off+s.length], Flags: s.flags,
	}, nil
}

// ReadInto copies the oldest frame into caller storage and removes it.
//
// The safe alternative to [RingBuffer.Read] for a caller that retains frames.
// Returns [ErrFrameTooLarge] when dst is too small, without consuming the
// frame — a partial read that silently truncated audio would be worse than a
// refusal.
func (b *RingBuffer) ReadInto(dst []byte) (Frame, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.count == 0 {
		b.underflows++
		return Frame{}, ErrBufferEmpty
	}
	s := b.slots[b.head]
	if len(dst) < s.length {
		return Frame{}, fmt.Errorf("%w: frame is %d bytes, destination is %d",
			ErrFrameTooLarge, s.length, len(dst))
	}

	n := copy(dst, b.data[s.off:s.off+s.length])
	b.head = (b.head + 1) % len(b.slots)
	b.count--
	b.dataUsed -= s.length
	b.read++

	return Frame{
		Sequence: s.sequence, Timestamp: s.timestamp, Arrival: s.arrival,
		Format: b.format, Payload: dst[:n], Flags: s.flags,
	}, nil
}

// Drain removes and returns up to n frames, cloning each.
//
// CLONES, unlike Read. Drain exists for the shutdown path, where the caller
// keeps what it takes, and returning a slice of borrowed frames that all point
// into a buffer about to be reused would be a trap with no safe use.
func (b *RingBuffer) Drain(n int) []Frame {
	b.mu.Lock()
	defer b.mu.Unlock()

	if n <= 0 || n > b.count {
		n = b.count
	}
	out := make([]Frame, 0, n)
	for i := 0; i < n; i++ {
		s := b.slots[b.head]
		payload := make([]byte, s.length)
		copy(payload, b.data[s.off:s.off+s.length])
		out = append(out, Frame{
			Sequence: s.sequence, Timestamp: s.timestamp, Arrival: s.arrival,
			Format: b.format, Payload: payload, Flags: s.flags,
		})
		b.head = (b.head + 1) % len(b.slots)
		b.count--
		b.dataUsed -= s.length
		b.read++
	}
	return out
}

// Flush discards every buffered frame and returns how many went.
//
// Distinct from Drain: Flush THROWS AWAY, Drain hands over. Both empty the
// buffer, and confusing them loses a second of somebody's audio.
func (b *RingBuffer) Flush() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	n := b.count
	b.head, b.tail, b.count = 0, 0, 0
	b.dataHead, b.dataTail, b.dataUsed = 0, 0, 0
	b.dropped += uint64(n)
	return n
}

// Close marks the buffer closed. Writes are refused; buffered frames remain
// readable so a drain can complete. Idempotent.
func (b *RingBuffer) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
}

// Closed reports whether the buffer is closed.
func (b *RingBuffer) Closed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

// BufferStats is a consistent view of a buffer's counters.
type BufferStats struct {
	Capacity   int
	Len        int
	Depth      time.Duration
	BytesUsed  int
	Written    uint64
	Read       uint64
	Dropped    uint64
	Overflows  uint64
	Underflows uint64
	Closed     bool
}

// Utilisation returns buffered frames over capacity, in [0, 1].
func (s BufferStats) Utilisation() float64 {
	if s.Capacity == 0 {
		return 0
	}
	return float64(s.Len) / float64(s.Capacity)
}

// DropRate returns dropped frames over frames offered, or zero when none.
func (s BufferStats) DropRate() float64 {
	offered := s.Written + s.Dropped
	if offered == 0 {
		return 0
	}
	return float64(s.Dropped) / float64(offered)
}

// String renders the stats.
func (s BufferStats) String() string {
	return fmt.Sprintf("buffer %d/%d frames depth=%s written=%d read=%d dropped=%d "+
		"overflows=%d underflows=%d",
		s.Len, s.Capacity, s.Depth.Round(time.Millisecond), s.Written, s.Read,
		s.Dropped, s.Overflows, s.Underflows)
}

// Stats returns a consistent snapshot of the counters.
//
// Under one lock acquisition, so the numbers agree with each other. Independent
// atomics would be faster and would let a reader see a Len that never existed
// alongside a Written that never coexisted with it — and buffer diagnostics are
// read precisely when somebody is trying to explain an inconsistency.
func (b *RingBuffer) Stats() BufferStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return BufferStats{
		Capacity: len(b.slots), Len: b.count, Depth: b.depthLocked(),
		BytesUsed: b.dataUsed, Written: b.written, Read: b.read,
		Dropped: b.dropped, Overflows: b.overflows, Underflows: b.underflows,
		Closed: b.closed,
	}
}

// BufferSnapshot captures a buffer's contents and counters.
//
// Frames are CLONED, so the snapshot survives the buffer. Used by the recovery
// path and by diagnostics; it allocates in proportion to buffered audio, which
// is why nothing calls it on the frame path.
type BufferSnapshot struct {
	Stats  BufferStats
	Frames []Frame
	Format AudioFormat
}

// Snapshot captures the buffer without disturbing it.
func (b *RingBuffer) Snapshot() BufferSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()

	frames := make([]Frame, 0, b.count)
	for i := 0; i < b.count; i++ {
		s := b.slots[(b.head+i)%len(b.slots)]
		payload := make([]byte, s.length)
		copy(payload, b.data[s.off:s.off+s.length])
		frames = append(frames, Frame{
			Sequence: s.sequence, Timestamp: s.timestamp, Arrival: s.arrival,
			Format: b.format, Payload: payload, Flags: s.flags,
		})
	}

	return BufferSnapshot{
		Stats: BufferStats{
			Capacity: len(b.slots), Len: b.count, Depth: b.depthLocked(),
			BytesUsed: b.dataUsed, Written: b.written, Read: b.read,
			Dropped: b.dropped, Overflows: b.overflows, Underflows: b.underflows,
			Closed: b.closed,
		},
		Frames: frames,
		Format: b.format,
	}
}

// Restore replaces the buffer's contents from a snapshot.
//
// Counters are NOT restored. They count what this buffer instance did, and
// carrying a previous instance's totals across a restore would make a drop rate
// describe two different buffers averaged together.
func (b *RingBuffer) Restore(snap BufferSnapshot) error {
	if snap.Format != b.format {
		return fmt.Errorf("%w: buffer is %s, snapshot is %s",
			ErrFormatMismatch, b.format, snap.Format)
	}
	if len(snap.Frames) > len(b.slots) {
		return fmt.Errorf("%w: snapshot holds %d frames, capacity is %d",
			ErrBufferFull, len(snap.Frames), len(b.slots))
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.head, b.tail, b.count = 0, 0, 0
	b.dataHead, b.dataTail, b.dataUsed = 0, 0, 0

	for _, f := range snap.Frames {
		n := len(f.Payload)
		if b.dataTail+n > len(b.data) {
			b.dataTail = 0
		}
		copy(b.data[b.dataTail:b.dataTail+n], f.Payload)
		b.slots[b.tail] = slot{
			sequence: f.Sequence, timestamp: f.Timestamp, arrival: f.Arrival,
			flags: f.Flags, off: b.dataTail, length: n,
		}
		b.dataTail += n
		b.dataUsed += n
		b.tail = (b.tail + 1) % len(b.slots)
		b.count++
	}
	return nil
}

// ---------------------------------------------------------------------------
// FramePool
// ---------------------------------------------------------------------------

// FramePool recycles payload buffers.
//
// For producers that need somewhere to build a frame before writing it. The
// ring buffer copies on write, so a producer can take a buffer, fill it, write,
// and return it — allocating nothing per frame.
//
// A plain slice under a mutex rather than sync.Pool: sync.Pool is
// GC-cooperative and drops its contents at every collection, which is exactly
// when a media path least wants to start allocating again. A fixed pool has
// predictable occupancy, which is what real-time work needs.
type FramePool struct {
	mu     sync.Mutex
	bufs   [][]byte
	size   int
	max    int
	gets   uint64
	puts   uint64
	misses uint64
}

// NewFramePool builds a pool of buffers of the given size.
func NewFramePool(bufferSize, maxBuffers int) *FramePool {
	if bufferSize <= 0 {
		bufferSize = 4096
	}
	if maxBuffers <= 0 {
		maxBuffers = 64
	}
	return &FramePool{size: bufferSize, max: maxBuffers}
}

// Get returns a buffer of the pool's size.
//
// Allocates on a miss rather than blocking or failing: a producer that cannot
// get a buffer has no useful fallback, and an allocation is better than a
// dropped frame.
func (p *FramePool) Get() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.gets++
	if n := len(p.bufs); n > 0 {
		buf := p.bufs[n-1]
		p.bufs = p.bufs[:n-1]
		return buf[:p.size]
	}
	p.misses++
	return make([]byte, p.size)
}

// Put returns a buffer to the pool.
//
// A buffer of the wrong size is discarded rather than kept: a pool holding
// mixed sizes hands out buffers that are sometimes too small, and the symptom
// is a truncated frame rather than an error.
func (p *FramePool) Put(buf []byte) {
	if cap(buf) < p.size {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	p.puts++
	if len(p.bufs) >= p.max {
		return
	}
	p.bufs = append(p.bufs, buf[:cap(buf)])
}

// PoolStats reports pool behaviour.
type PoolStats struct {
	Available int
	Max       int
	Size      int
	Gets      uint64
	Puts      uint64
	Misses    uint64
}

// MissRate returns misses over gets, or zero when none.
//
// The number that says whether the pool is sized correctly. A non-zero miss
// rate at steady state means the pool is too small and the producer is
// allocating anyway.
func (s PoolStats) MissRate() float64 {
	if s.Gets == 0 {
		return 0
	}
	return float64(s.Misses) / float64(s.Gets)
}

// Stats returns a consistent view of the pool.
func (p *FramePool) Stats() PoolStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return PoolStats{
		Available: len(p.bufs), Max: p.max, Size: p.size,
		Gets: p.gets, Puts: p.puts, Misses: p.misses,
	}
}
