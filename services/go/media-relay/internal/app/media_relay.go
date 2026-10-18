// Package app holds the media-relay service's in-memory building blocks: a
// bounded audio-frame ring buffer, connection-state tracking, a leased session
// registry, sequence-gap detection, latency averaging and a provider-pool
// placeholder.
package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrBufferOverflow is returned by RingBuffer.Push when the buffer is full.
	ErrBufferOverflow = errors.New("media: ring buffer overflow")
	// ErrBufferUnderflow is returned by RingBuffer.Pop when the buffer is empty.
	ErrBufferUnderflow = errors.New("media: ring buffer underflow")
	// ErrSessionExpired is returned by SessionRegistry.Get for a session that is
	// unknown or whose lease has expired.
	ErrSessionExpired = errors.New("media: session lease expired")
	// ErrSequenceGap is wrapped by FrameRouter.RouteFrame when a frame does not
	// follow the previous one.
	ErrSequenceGap = errors.New("media: audio frame sequence gap detected")
)

// AudioFrame is one chunk of a session's audio, numbered in sequence.
type AudioFrame struct {
	SessionID  string    `json:"sessionId"`
	SequenceNo int64     `json:"sequenceNo"`
	Timestamp  time.Time `json:"timestamp"`
	PCMData    []byte    `json:"pcmData"`
	IsSilence  bool      `json:"isSilence"`
}

// RingBuffer is a fixed-capacity FIFO of audio frames, safe for concurrent use.
// Push refuses a frame when the buffer is full rather than overwriting the
// oldest one.
type RingBuffer struct {
	capacity int
	data     []AudioFrame
	head     int
	tail     int
	size     int
	mu       sync.Mutex
}

// NewRingBuffer returns an empty buffer that holds at most capacity frames.
func NewRingBuffer(capacity int) *RingBuffer {
	return &RingBuffer{
		capacity: capacity,
		data:     make([]AudioFrame, capacity),
	}
}

// Push appends frame, or returns ErrBufferOverflow if the buffer is full.
func (r *RingBuffer) Push(frame AudioFrame) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.size == r.capacity {
		return ErrBufferOverflow
	}

	r.data[r.head] = frame
	r.head = (r.head + 1) % r.capacity
	r.size++
	return nil
}

// Pop removes and returns the oldest frame, or returns ErrBufferUnderflow if
// the buffer is empty.
func (r *RingBuffer) Pop() (AudioFrame, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.size == 0 {
		return AudioFrame{}, ErrBufferUnderflow
	}

	frame := r.data[r.tail]
	r.tail = (r.tail + 1) % r.capacity
	r.size--
	return frame, nil
}

// Size reports how many frames are buffered.
func (r *RingBuffer) Size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

// ConnectionState is the state of a media connection.
type ConnectionState string

// The connection states a ConnectionManager records.
const (
	StateDisconnected ConnectionState = "DISCONNECTED"
	StateConnecting   ConnectionState = "CONNECTING"
	StateConnected    ConnectionState = "CONNECTED"
	StateReconnecting ConnectionState = "RECONNECTING"
	StateFailed       ConnectionState = "FAILED"
)

// ConnectionManager tracks one connection's state and counts its reconnects
// and dropped frames. It is safe for concurrent use.
type ConnectionManager struct {
	state          ConnectionState
	reconnectCount int64
	droppedFrames  int64
	mu             sync.RWMutex
}

// NewConnectionManager returns a manager in StateDisconnected.
func NewConnectionManager() *ConnectionManager {
	return &ConnectionManager{
		state: StateDisconnected,
	}
}

// SetState records the connection's state.
func (c *ConnectionManager) SetState(state ConnectionState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = state
}

// State reports the connection's current state.
func (c *ConnectionManager) State() ConnectionState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

// RecordReconnect counts one reconnect.
func (c *ConnectionManager) RecordReconnect() {
	atomic.AddInt64(&c.reconnectCount, 1)
}

// RecordDroppedFrame counts one dropped frame.
func (c *ConnectionManager) RecordDroppedFrame() {
	atomic.AddInt64(&c.droppedFrames, 1)
}

// RuntimeSession is a registered session and its lease.
type RuntimeSession struct {
	SessionID string
	State     ConnectionState
	LeaseTTL  time.Duration
	ExpiresAt time.Time
}

// SessionRegistry holds leased sessions by ID. It is safe for concurrent use.
type SessionRegistry struct {
	sessions map[string]*RuntimeSession
	mu       sync.RWMutex
}

// NewSessionRegistry returns an empty registry.
func NewSessionRegistry() *SessionRegistry {
	return &SessionRegistry{
		sessions: make(map[string]*RuntimeSession),
	}
}

// Register adds the session sessionID, replacing any existing one, in
// StateConnected with a lease that expires ttl from now.
func (s *SessionRegistry) Register(sessionID string, ttl time.Duration) *RuntimeSession {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess := &RuntimeSession{
		SessionID: sessionID,
		State:     StateConnected,
		LeaseTTL:  ttl,
		ExpiresAt: time.Now().Add(ttl),
	}
	s.sessions[sessionID] = sess
	return sess
}

// Get returns the session sessionID, or ErrSessionExpired if it is unknown or
// its lease has expired. An expired session is not removed.
func (s *SessionRegistry) Get(sessionID string) (*RuntimeSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sess, exists := s.sessions[sessionID]
	if !exists || time.Now().After(sess.ExpiresAt) {
		return nil, ErrSessionExpired
	}
	return sess, nil
}

// FrameRouter checks that frames arrive in consecutive sequence order. It is
// safe for concurrent use.
type FrameRouter struct {
	lastSeq int64
	mu      sync.Mutex
}

// NewFrameRouter returns a router that accepts any first frame.
func NewFrameRouter() *FrameRouter {
	return &FrameRouter{lastSeq: 0}
}

// RouteFrame records frame's sequence number and returns an error wrapping
// ErrSequenceGap if it does not follow the previously recorded one. The frame
// is recorded either way, so the next frame is checked against it.
func (f *FrameRouter) RouteFrame(frame AudioFrame) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.lastSeq > 0 && frame.SequenceNo != f.lastSeq+1 {
		f.lastSeq = frame.SequenceNo
		return fmt.Errorf("%w: expected %d, got %d", ErrSequenceGap, f.lastSeq+1, frame.SequenceNo)
	}

	f.lastSeq = frame.SequenceNo
	return nil
}

// LatencyMonitor averages per-frame latency in whole milliseconds. Its methods
// may be called concurrently; an average read while frames are being recorded
// can combine a total and a count from slightly different moments.
type LatencyMonitor struct {
	totalLatencyMs int64
	frameCount     int64
}

// NewLatencyMonitor returns a monitor with no samples.
func NewLatencyMonitor() *LatencyMonitor {
	return &LatencyMonitor{}
}

// RecordFrameLatency records the time elapsed since startTime, truncated to
// whole milliseconds, and returns the untruncated duration.
func (l *LatencyMonitor) RecordFrameLatency(startTime time.Time) time.Duration {
	elapsed := time.Since(startTime)
	atomic.AddInt64(&l.totalLatencyMs, elapsed.Milliseconds())
	atomic.AddInt64(&l.frameCount, 1)
	return elapsed
}

// AverageLatencyMs returns the mean recorded latency in milliseconds, or 0 when
// nothing has been recorded.
func (l *LatencyMonitor) AverageLatencyMs() float64 {
	count := atomic.LoadInt64(&l.frameCount)
	if count == 0 {
		return 0.0
	}
	total := atomic.LoadInt64(&l.totalLatencyMs)
	return float64(total) / float64(count)
}

// ProviderPool is a placeholder for limiting concurrent provider use. It does
// not limit anything yet: Acquire always succeeds and Release does nothing.
type ProviderPool struct {
	poolSize int
	mu       sync.Mutex
}

// NewProviderPool returns a pool of the given size. The size is stored but not
// enforced.
func NewProviderPool(size int) *ProviderPool {
	return &ProviderPool{poolSize: size}
}

// Acquire always returns nil. It neither waits nor consults ctx.
func (p *ProviderPool) Acquire(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return nil
}

// Release does nothing.
func (p *ProviderPool) Release() {}
