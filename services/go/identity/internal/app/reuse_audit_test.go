package app

import (
	"context"
	"errors"
	"testing"
)

// Refresh-token reuse is the exfiltration alarm (ADR-0010 §6, §9): every
// detection is audited once, with the revoked session's id and nothing else;
// a refused refresh that is NOT reuse is not.

// validatedSession is an unbound session through the real OTP flow.
func validatedSession(ctx context.Context, t *testing.T, svc *IdentityService) *UserSession {
	t.Helper()
	reqID, err := svc.RequestOtp(ctx, "+919876543210")
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := svc.ValidateOtp(ctx, reqID, testOtpCode)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func reuseEvents(a *recordingSecurityAuditor) []SecurityEvent {
	var out []SecurityEvent
	for _, e := range a.snapshot() {
		if e.Type == EventRefreshTokenReused {
			out = append(out, e)
		}
	}
	return out
}

func TestSecurityEvents_DeviceBoundReuseIsAuditedOnce(t *testing.T) {
	ctx := context.Background()
	r, k, sess := newRefreshRig(ctx, t)
	aud := &recordingSecurityAuditor{}
	r.svc.SetSecurityAuditor(aud)

	if _, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", r.refreshProof(t, k, "a1")); err != nil {
		t.Fatal(err)
	}
	if got := reuseEvents(aud); len(got) != 0 {
		t.Fatalf("a normal refresh was audited as reuse: %+v", got)
	}
	// The rotated-away token again: reuse.
	if _, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", r.refreshProof(t, k, "a2")); !errors.Is(err, ErrTokenReused) {
		t.Fatalf("err = %v, want ErrTokenReused", err)
	}
	got := reuseEvents(aud)
	if len(got) != 1 || got[0].SessionID != sess.SessionID || got[0].RequestID != "" || got[0].At.IsZero() {
		t.Fatalf("reuse events = %+v; want one, naming the session only", got)
	}
	// The family is now revoked: a further attempt is a dead session, not a
	// second detection.
	if _, _, err := r.svc.RefreshTokenWithDeviceProof(ctx, sess.SessionID, sess.RefreshToken, "POST", r.refreshProof(t, k, "a3")); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("after revocation: %v", err)
	}
	if got := reuseEvents(aud); len(got) != 1 {
		t.Fatalf("a revoked session was audited again: %+v", got)
	}
}

func TestSecurityEvents_UnboundReuseIsAuditedOnce(t *testing.T) {
	ctx := context.Background()
	svc := withFixedOtp(NewIdentityService(NewMemoryIdentityRepository(), []byte("test_secret_key")))
	aud := &recordingSecurityAuditor{}
	svc.SetSecurityAuditor(aud)
	session := validatedSession(ctx, t, svc)
	old := session.RefreshToken
	if _, _, err := svc.RefreshToken(ctx, session.SessionID, old); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RefreshToken(ctx, session.SessionID, old); !errors.Is(err, ErrTokenReused) {
		t.Fatalf("err = %v, want ErrTokenReused", err)
	}
	if got := reuseEvents(aud); len(got) != 1 || got[0].SessionID != session.SessionID {
		t.Fatalf("reuse events = %+v; want one naming the session", got)
	}
}

// Unknown sessions are refused without an alarm: they are not evidence that a
// real token was stolen.
func TestSecurityEvents_UnknownSessionIsNotReuse(t *testing.T) {
	ctx := context.Background()
	svc := withFixedOtp(NewIdentityService(NewMemoryIdentityRepository(), []byte("test_secret_key")))
	aud := &recordingSecurityAuditor{}
	svc.SetSecurityAuditor(aud)
	if _, _, err := svc.RefreshToken(ctx, "sess_unknown", "whatever"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v", err)
	}
	if got := reuseEvents(aud); len(got) != 0 {
		t.Fatalf("an unknown session was audited as reuse: %+v", got)
	}
}

// A nil auditor restores the default, which discards events and counts no
// failures.
func TestSetSecurityAuditor_NilRestoresTheDefault(t *testing.T) {
	ctx := context.Background()
	svc := withFixedOtp(NewIdentityService(NewMemoryIdentityRepository(), []byte("test_secret_key")))
	svc.SetSecurityAuditor(&recordingSecurityAuditor{err: errors.New("sink down")})
	svc.SetSecurityAuditor(nil)
	session := validatedSession(ctx, t, svc)
	if _, _, err := svc.RefreshToken(ctx, session.SessionID, session.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RefreshToken(ctx, session.SessionID, session.RefreshToken); !errors.Is(err, ErrTokenReused) {
		t.Fatal(err)
	}
	if svc.AuditFailures() != 0 {
		t.Fatalf("audit failures = %d; the failing auditor is still installed", svc.AuditFailures())
	}
}
