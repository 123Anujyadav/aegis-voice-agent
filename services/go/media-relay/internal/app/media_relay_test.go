package app

import (
	"context"
	"testing"
	"time"
)

func TestRingBuffer_PushAndPop(t *testing.T) {
	buf := NewRingBuffer(5)

	frame1 := AudioFrame{SessionID: "sess_1", SequenceNo: 1, PCMData: []byte("PCM_FRAME_1")}
	frame2 := AudioFrame{SessionID: "sess_1", SequenceNo: 2, PCMData: []byte("PCM_FRAME_2")}

	if err := buf.Push(frame1); err != nil {
		t.Fatalf("Push frame1 failed: %v", err)
	}
	if err := buf.Push(frame2); err != nil {
		t.Fatalf("Push frame2 failed: %v", err)
	}

	if buf.Size() != 2 {
		t.Errorf("expected buffer size 2, got %d", buf.Size())
	}

	popped1, err := buf.Pop()
	if err != nil {
		t.Fatalf("Pop frame1 failed: %v", err)
	}
	if popped1.SequenceNo != 1 {
		t.Errorf("expected sequence 1, got %d", popped1.SequenceNo)
	}
}

func TestRingBuffer_Overflow(t *testing.T) {
	buf := NewRingBuffer(2)
	_ = buf.Push(AudioFrame{SequenceNo: 1})
	_ = buf.Push(AudioFrame{SequenceNo: 2})

	err := buf.Push(AudioFrame{SequenceNo: 3})
	if err != ErrBufferOverflow {
		t.Fatalf("expected ErrBufferOverflow, got %v", err)
	}
}

func TestConnectionManager_StateTransitions(t *testing.T) {
	mgr := NewConnectionManager()
	if mgr.State() != StateDisconnected {
		t.Errorf("expected initial state DISCONNECTED, got %s", mgr.State())
	}

	mgr.SetState(StateConnected)
	if mgr.State() != StateConnected {
		t.Errorf("expected state CONNECTED, got %s", mgr.State())
	}

	mgr.RecordReconnect()
	mgr.RecordDroppedFrame()
}

func TestSessionRegistry_LeaseExpiration(t *testing.T) {
	reg := NewSessionRegistry()
	sess := reg.Register("sess_test_1", 50*time.Millisecond)

	if sess.SessionID != "sess_test_1" {
		t.Errorf("expected session ID sess_test_1, got %s", sess.SessionID)
	}

	fetched, err := reg.Get("sess_test_1")
	if err != nil || fetched.SessionID != "sess_test_1" {
		t.Fatalf("failed to fetch valid session: %v", err)
	}

	time.Sleep(60 * time.Millisecond)
	_, err = reg.Get("sess_test_1")
	if err != ErrSessionExpired {
		t.Fatalf("expected ErrSessionExpired for expired lease, got %v", err)
	}
}

func TestFrameRouter_SequenceGapDetection(t *testing.T) {
	router := NewFrameRouter()

	_ = router.RouteFrame(AudioFrame{SequenceNo: 1})
	err := router.RouteFrame(AudioFrame{SequenceNo: 3}) // Sequence gap: expected 2

	if err == nil {
		t.Fatalf("expected sequence gap error")
	}
}

func TestLatencyMonitor_Metrics(t *testing.T) {
	monitor := NewLatencyMonitor()

	start := time.Now().Add(-15 * time.Millisecond)
	elapsed := monitor.RecordFrameLatency(start)

	if elapsed < 10*time.Millisecond {
		t.Errorf("expected latency >= 10ms, got %v", elapsed)
	}

	if monitor.AverageLatencyMs() <= 0 {
		t.Errorf("expected positive average latency")
	}
}

func TestProviderPool_AcquireRelease(t *testing.T) {
	pool := NewProviderPool(10)
	ctx := context.Background()

	if err := pool.Acquire(ctx); err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	pool.Release()
}
