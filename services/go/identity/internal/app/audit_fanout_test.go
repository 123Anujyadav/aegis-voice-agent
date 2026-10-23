package app

import (
	"context"
	"errors"
	"testing"
)

// AddSecurityAuditor keeps what is configured and adds the new destination:
// every event reaches each, a failing one is an audit failure and changes no
// outcome, and nil is ignored.
func TestAddSecurityAuditor_FansOutToEveryDestination(t *testing.T) {
	ctx := context.Background()
	svc := withFixedOtp(NewIdentityService(NewMemoryIdentityRepository(), []byte("test_secret_key")))
	first, second := &recordingSecurityAuditor{}, &recordingSecurityAuditor{}
	svc.AddSecurityAuditor(first) // replaces the no-op default
	svc.AddSecurityAuditor(nil)
	svc.AddSecurityAuditor(second)
	session := validatedSession(ctx, t, svc)
	if _, _, err := svc.RefreshToken(ctx, session.SessionID, session.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RefreshToken(ctx, session.SessionID, session.RefreshToken); !errors.Is(err, ErrTokenReused) {
		t.Fatal(err)
	}
	if len(reuseEvents(first)) != 1 || len(reuseEvents(second)) != 1 {
		t.Fatalf("first %d, second %d reuse events; want one each", len(reuseEvents(first)), len(reuseEvents(second)))
	}

	// A failing destination is counted once per event and does not stop the
	// others or change the outcome.
	svc2 := withFixedOtp(NewIdentityService(NewMemoryIdentityRepository(), []byte("test_secret_key")))
	ok := &recordingSecurityAuditor{}
	svc2.AddSecurityAuditor(&recordingSecurityAuditor{err: errors.New("database down")})
	svc2.AddSecurityAuditor(ok)
	s2 := validatedSession(ctx, t, svc2)
	_, _, _ = svc2.RefreshToken(ctx, s2.SessionID, s2.RefreshToken)
	if _, _, err := svc2.RefreshToken(ctx, s2.SessionID, s2.RefreshToken); !errors.Is(err, ErrTokenReused) {
		t.Fatalf("a failing auditor changed the outcome: %v", err)
	}
	if len(reuseEvents(ok)) != 1 || svc2.AuditFailures() != 1 {
		t.Fatalf("healthy destination got %d, failures %d; want 1 and 1", len(reuseEvents(ok)), svc2.AuditFailures())
	}
}

// A device enrolment is audited once, naming the new session only.
func TestSecurityEvents_DeviceEnrolmentIsAudited(t *testing.T) {
	ctx := context.Background()
	r, k, _ := newRefreshRig(ctx, t)
	aud := &recordingSecurityAuditor{}
	r.svc.SetSecurityAuditor(aud)
	sess := r.enrolKey(ctx, t, "req_b", k)
	var got []SecurityEvent
	for _, e := range aud.snapshot() {
		if e.Type == EventDeviceEnrolled {
			got = append(got, e)
		}
	}
	if len(got) != 1 || got[0].SessionID != sess.SessionID || got[0].RequestID != "" || got[0].At.IsZero() {
		t.Fatalf("enrolment events %+v; want one naming the session", got)
	}
}
