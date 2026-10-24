package app

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/dpop"
)

type failureLog struct {
	mu  sync.Mutex
	got []SecurityEventType
}

func (l *failureLog) observe(t SecurityEventType) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, t)
}

// Every refused audit event is reported to the observer by type, exactly as
// it is counted; a healthy auditor reports nothing; removing the observer
// stops reporting and does not panic.
func TestAuditFailureObserver_ReportsEachRefusedEventByType(t *testing.T) {
	ctx := context.Background()
	svc := withFixedOtp(NewIdentityService(NewMemoryIdentityRepository(), []byte("test_secret_key")))
	svc.AddSecurityAuditor(&recordingSecurityAuditor{err: errors.New("database down")})
	log := &failureLog{}
	svc.SetAuditFailureObserver(log.observe)
	s := validatedSession(ctx, t, svc)
	_, _, _ = svc.RefreshToken(ctx, s.SessionID, s.RefreshToken)
	if _, _, err := svc.RefreshToken(ctx, s.SessionID, s.RefreshToken); !errors.Is(err, ErrTokenReused) {
		t.Fatalf("an observed failure changed the outcome: %v", err)
	}
	if !slices.Equal(log.got, []SecurityEventType{EventRefreshTokenReused}) || svc.AuditFailures() != 1 {
		t.Fatalf("observed %v, counted %d; want one %s", log.got, svc.AuditFailures(), EventRefreshTokenReused)
	}

	healthy := withFixedOtp(NewIdentityService(NewMemoryIdentityRepository(), []byte("test_secret_key")))
	healthy.AddSecurityAuditor(&recordingSecurityAuditor{})
	quiet := &failureLog{}
	healthy.SetAuditFailureObserver(quiet.observe)
	h := validatedSession(ctx, t, healthy)
	_, _, _ = healthy.RefreshToken(ctx, h.SessionID, h.RefreshToken)
	_, _, _ = healthy.RefreshToken(ctx, h.SessionID, h.RefreshToken)
	if len(quiet.got) != 0 {
		t.Fatalf("a healthy auditor reported failures: %v", quiet.got)
	}

	svc.SetAuditFailureObserver(nil)
	s2 := validatedSession(ctx, t, svc)
	_, _, _ = svc.RefreshToken(ctx, s2.SessionID, s2.RefreshToken)
	_, _, _ = svc.RefreshToken(ctx, s2.SessionID, s2.RefreshToken)
	if len(log.got) != 1 || svc.AuditFailures() != 2 {
		t.Fatalf("after removal: observed %v, counted %d; want 1 and 2", log.got, svc.AuditFailures())
	}
}

// A failed re-enrolment lookup is reported as the event it may have missed.
func TestAuditFailureObserver_ReportsAFailedReenrolmentLookup(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryIdentityRepository()
	r := newEnrollRig(t, lookupFailingRepo{m}, m, dpop.NewMemoryReplayStore())
	r.svc.SetSecurityAuditor(&recordingSecurityAuditor{})
	log := &failureLog{}
	r.svc.SetAuditFailureObserver(log.observe)
	if sess := r.enrolKey(ctx, t, "req_a", deviceKey(t)); sess == nil {
		t.Fatal("the enrolment failed")
	}
	if !slices.Equal(log.got, []SecurityEventType{EventDeviceReenrolled}) {
		t.Fatalf("observed %v, want one %s", log.got, EventDeviceReenrolled)
	}
}
