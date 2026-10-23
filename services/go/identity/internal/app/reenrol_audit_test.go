package app

import (
	"context"
	"errors"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/dpop"
)

func eventsOf(a *recordingSecurityAuditor, typ SecurityEventType) []SecurityEvent {
	var out []SecurityEvent
	for _, e := range a.snapshot() {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// A second device enrolling for the same subscriber is a re-enrolment: it is
// audited, naming the new session. The same device enrolling again is not.
func TestSecurityEvents_ReenrolmentOnANewDeviceIsAudited(t *testing.T) {
	ctx := context.Background()
	r, k1, _ := newRefreshRig(ctx, t) // the first device enrols here
	aud := &recordingSecurityAuditor{}
	r.svc.SetSecurityAuditor(aud)

	again := r.enrolKey(ctx, t, "req_same", k1)
	if got := eventsOf(aud, EventDeviceReenrolled); len(got) != 0 {
		t.Fatalf("the same device re-enrolling was audited as a new device: %+v", got)
	}
	newer := r.enrolKey(ctx, t, "req_new", deviceKey(t))
	got := eventsOf(aud, EventDeviceReenrolled)
	if len(got) != 1 || got[0].SessionID != newer.SessionID || got[0].SessionID == again.SessionID {
		t.Fatalf("re-enrolment events %+v; want one naming the new device's session", got)
	}
	if n := len(eventsOf(aud, EventDeviceEnrolled)); n != 2 {
		t.Fatalf("%d enrolment events, want 2 (every enrolment)", n)
	}
}

// lookupFailingRepo fails the re-enrolment lookup only.
type lookupFailingRepo struct{ *MemoryIdentityRepository }

func (lookupFailingRepo) OtherDeviceKeyEnrolled(context.Context, string, string, string) (bool, error) {
	return false, errors.New("store down")
}

// A failed lookup never fails the enrolment; it is counted as an audit
// failure and nothing is invented.
func TestSecurityEvents_ReenrolmentLookupFailureIsCountedNotFatal(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryIdentityRepository()
	r := newEnrollRig(t, lookupFailingRepo{m}, m, dpop.NewMemoryReplayStore())
	aud := &recordingSecurityAuditor{}
	r.svc.SetSecurityAuditor(aud)
	sess := r.enrolKey(ctx, t, "req_a", deviceKey(t))
	if sess == nil || sess.DeviceKeyThumbprint == "" {
		t.Fatal("the enrolment failed because the re-enrolment lookup did")
	}
	if got := eventsOf(aud, EventDeviceReenrolled); len(got) != 0 {
		t.Fatalf("a failed lookup produced a re-enrolment event: %+v", got)
	}
	if len(eventsOf(aud, EventDeviceEnrolled)) != 1 || r.svc.AuditFailures() != 1 {
		t.Fatalf("enrolled events %d, audit failures %d; want 1 and 1", len(eventsOf(aud, EventDeviceEnrolled)), r.svc.AuditFailures())
	}
}
