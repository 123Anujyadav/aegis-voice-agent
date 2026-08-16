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
	ErrBufferOverflow  = errors.New("media: ring buffer overflow")
	ErrBufferUnderflow = errors.New("media: ring buffer underflow")
	ErrSessionExpired  = errors.New("media: session lease expired")
	ErrSequenceGap     = errors.New("media: audio frame sequence gap detected")
)

type AudioFrame struct {
	SessionID  string    `json:"sessionId"`
	SequenceNo int64     `json:"sequenceNo"`
	Timestamp  time.Time `json:"timestamp"`
	PCMData    []byte    `json:"pcmData"`
	IsSilence  bool      `json:"isSilence"`
}

type RingBuffer struct {
	capacity int
	data     []AudioFrame
	head     int
	tail     int
	size     int
	mu       sync.Mutex
}

func NewRingBuffer(capacity int) *RingBuffer {
	return &RingBuffer{
		capacity: capacity,
		data:     make([]AudioFrame, capacity),
	}
}

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

func (r *RingBuffer) Size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

type ConnectionState string

const (
	StateDisconnected ConnectionState = "DISCONNECTED"
	StateConnecting   ConnectionState = "CONNECTING"
	StateConnected    ConnectionState = "CONNECTED"
	StateReconnecting ConnectionState = "RECONNECTING"
	StateFailed       ConnectionState = "FAILED"
)

type ConnectionManager struct {
	state          ConnectionState
	reconnectCount int64
	droppedFrames  int64
	mu             sync.RWMutex
}

func NewConnectionManager() *ConnectionManager {
	return &ConnectionManager{
		state: StateDisconnected,
	}
}

func (c *ConnectionManager) SetState(state ConnectionState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = state
}

func (c *ConnectionManager) State() ConnectionState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

func (c *ConnectionManager) RecordReconnect() {
	atomic.AddInt64(&c.reconnectCount, 1)
}

func (c *ConnectionManager) RecordDroppedFrame() {
	atomic.AddInt64(&c.droppedFrames, 1)
}

type RuntimeSession struct {
	SessionID string
	State     ConnectionState
	LeaseTTL  time.Duration
	ExpiresAt time.Time
}

type SessionRegistry struct {
	sessions map[string]*RuntimeSession
	mu       sync.RWMutex
}

func NewSessionRegistry() *SessionRegistry {
	return &SessionRegistry{
		sessions: make(map[string]*RuntimeSession),
	}
}

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

func (s *SessionRegistry) Get(sessionID string) (*RuntimeSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sess, exists := s.sessions[sessionID]
	if !exists || time.Now().After(sess.ExpiresAt) {
		return nil, ErrSessionExpired
	}
	return sess, nil
}

type FrameRouter struct {
	lastSeq int64
	mu      sync.Mutex
}

func NewFrameRouter() *FrameRouter {
	return &FrameRouter{lastSeq: 0}
}

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

type LatencyMonitor struct {
	totalLatencyMs int64
	frameCount     int64
}

func NewLatencyMonitor() *LatencyMonitor {
	return &LatencyMonitor{}
}

func (l *LatencyMonitor) RecordFrameLatency(startTime time.Time) time.Duration {
	elapsed := time.Since(startTime)
	atomic.AddInt64(&l.totalLatencyMs, elapsed.Milliseconds())
	atomic.AddInt64(&l.frameCount, 1)
	return elapsed
}

func (l *LatencyMonitor) AverageLatencyMs() float64 {
	count := atomic.LoadInt64(&l.frameCount)
	if count == 0 {
		return 0.0
	}
	total := atomic.LoadInt64(&l.totalLatencyMs)
	return float64(total) / float64(count)
}

type ProviderPool struct {
	poolSize int
	mu       sync.Mutex
}

func NewProviderPool(size int) *ProviderPool {
	return &ProviderPool{poolSize: size}
}

func (p *ProviderPool) Acquire(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return nil
}

func (p *ProviderPool) Release() {}
