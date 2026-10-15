package fence

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// Phase 19 P1-31 — fence timing and configuration audit regressions.

// TestFence_ConfirmationFreshnessIsMeasuredFromWhenTheQueryWasSent reproduces
// the P1-31 defect (failed before the fix): a confirmation was timestamped when
// the reply ARRIVED. The server verified the lock at some instant between send
// and receipt; if it released the lock right after answering, the old owner's
// window (measured from receipt) outlived the new owner's grace by up to the
// reply latency. Freshness is now measured from the SEND time.
func TestFence_ConfirmationFreshnessIsMeasuredFromWhenTheQueryWasSent(t *testing.T) {
	t.Parallel()
	var latency atomic.Int64
	var c atomic.Pointer[clock]
	s := &fakeSession{holds: func(context.Context) (bool, error) {
		c.Load().advance(time.Duration(latency.Load())) // the round trip takes time
		return true, nil
	}}
	r := newRig(t, nil, s)
	c.Store(r.c)
	r.start(t)
	r.c.step(t)
	r.c.step(t)
	eventually(t, "ownership", r.f.Owned)

	latency.Store(int64(1500 * time.Millisecond)) // within HeartbeatTimeout (2s)
	before := s.holdsHit.Load()
	r.c.step(t) // the sleep ends at T_send; Holds returns at T_send + 1.5s
	eventually(t, "the slow confirmation to be recorded", func() bool {
		if s.holdsHit.Load() != before+1 {
			return false
		}
		r.f.mu.Lock()
		defer r.f.mu.Unlock()
		return r.c.now().Sub(r.f.confirmed) >= 1500*time.Millisecond
	})
	// now = T_send + 1.5s. Move to T_send + Validity + 1ms.
	r.c.advance(cfg.Validity() - 1500*time.Millisecond + time.Millisecond)
	if r.f.Owned() {
		t.Errorf("still owner %s after the confirmation query was SENT (validity %s): freshness is counted from receipt",
			cfg.Validity()+time.Millisecond, cfg.Validity())
	}
}

// The same rule at acquisition: the first confirmation is the TryLock's send
// time, while the grace runs from its receipt (the conservative end).
func TestFence_AcquisitionConfirmsFromSendAndGracesFromReceipt(t *testing.T) {
	t.Parallel()
	var c atomic.Pointer[clock]
	s := &fakeSession{tryLock: func(context.Context) (bool, error) {
		c.Load().advance(time.Second) // a slow lock round trip
		return true, nil
	}}
	r := newRig(t, nil, s)
	c.Store(r.c)
	start := r.c.now()
	r.start(t)
	eventually(t, "the lock", func() bool { r.f.mu.Lock(); defer r.f.mu.Unlock(); return r.f.held })
	r.f.mu.Lock()
	acquired, confirmed := r.f.acquired, r.f.confirmed
	r.f.mu.Unlock()
	if !confirmed.Equal(start) || !acquired.Equal(start.Add(time.Second)) {
		t.Errorf("confirmed %s, acquired %s; want send time %s and receipt %s",
			confirmed.Sub(start), acquired.Sub(start), time.Duration(0), time.Second)
	}
}

// A server that is not a writable primary (a replica endpoint, a demoted
// node) is never used to hold the fence: advisory locks are per server.
func TestFence_NeverOwnsOnANonPrimary(t *testing.T) {
	t.Parallel()
	for name, primary := range map[string]func(context.Context) (bool, error){
		"in recovery":  func(context.Context) (bool, error) { return false, nil },
		"check failed": func(context.Context) (bool, error) { return false, errors.New("boom") },
	} {
		var tries atomic.Int32
		s := &fakeSession{primary: primary, tryLock: func(context.Context) (bool, error) { tries.Add(1); return true, nil }}
		r := newRig(t, nil, s)
		r.start(t)
		for range 3 {
			r.c.step(t)
			if r.f.Owned() {
				t.Fatalf("%s: owner on a non-primary", name)
			}
		}
		if tries.Load() != 0 {
			t.Errorf("%s: TryLock ran %d times on a non-primary", name, tries.Load())
		}
		if s.closes.Load() < 3 {
			t.Errorf("%s: sessions to a non-primary were not closed", name)
		}
		_ = r.f.Shutdown(context.Background())
	}
}
