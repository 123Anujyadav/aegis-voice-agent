package providerconformance

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
)

func telephonyCall(provider telephony.ProviderID) func() telephony.CallContext {
	return func() telephony.CallContext {
		return telephony.CallContext{
			Caller:    telephony.Endpoint{Ref: "conformance-caller", Display: "unknown", Country: "IN"},
			Callee:    telephony.Endpoint{Ref: "conformance-callee", Display: "unknown", Country: "IN"},
			Direction: telephony.DirectionInbound,
			Channel:   telephony.ChannelPSTN,
			Provider:  provider,
		}
	}
}

func TestTheTelephonyFakeConforms(t *testing.T) {
	RunTelephony(t, TelephonySubject{
		New: func(*testing.T) telephony.Provider {
			return telephony.NewFakeProvider("fake-carrier", telephony.NewCapabilities())
		},
		Call:   telephonyCall("fake-carrier"),
		CallID: telephony.NewCallID,
	})
}

// A fake that holds every operation for an hour: the cancellation and expiry
// checks must still pass, because the fake honours its context.
func TestASlowButCancellableCarrierPassesTheCancellationChecks(t *testing.T) {
	checks := TelephonyChecks(t, TelephonySubject{
		New: func(*testing.T) telephony.Provider {
			p := telephony.NewFakeProvider("slow-carrier", telephony.NewCapabilities())
			for _, op := range []string{"dial", "answer", "reject", "hangup"} {
				p.DelayOn(op, time.Hour)
			}
			return p
		},
		Call:   telephonyCall("slow-carrier"),
		CallID: telephony.NewCallID,
	})
	for _, name := range []string{
		"a cancelled context makes every operation return promptly",
		"an expired deadline makes every operation return promptly",
	} {
		passing(t, checks, name)
	}
	failing(t, checks, "operations do not block indefinitely") // it waits for the deadline
}

// passing fails the test if the named check fails.
func passing(t *testing.T, checks []Check, name string) {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			if err := c.Run(); err != nil {
				t.Fatalf("check %q: %v", name, err)
			}
			return
		}
	}
	t.Fatalf("no check named %q", name)
}

// --- A carrier whose id changes and whose operations ignore their context.

type deafCarrier struct {
	ids     *atomic.Int64
	release <-chan struct{}
}

func (d deafCarrier) ID() telephony.ProviderID {
	if d.ids.Add(1) > 1 {
		return "deaf-carrier-renamed" // the defect: not stable
	}
	return "deaf-carrier"
}
func (deafCarrier) Capabilities() telephony.Capabilities { return telephony.NewCapabilities() }

// wait ignores ctx (the defect) and holds until the test ends.
func (d deafCarrier) wait() error { <-d.release; return nil }

func (d deafCarrier) Dial(context.Context, telephony.CallContext) error      { return d.wait() }
func (d deafCarrier) Answer(context.Context, telephony.CallID) error         { return d.wait() }
func (d deafCarrier) Reject(context.Context, telephony.CallID, string) error { return d.wait() }
func (d deafCarrier) Hangup(context.Context, telephony.CallID, string) error { return d.wait() }

func TestTheTelephonySuiteCatchesADeafCarrier(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	checks := TelephonyChecks(t, TelephonySubject{
		New: func(*testing.T) telephony.Provider {
			return deafCarrier{ids: new(atomic.Int64), release: release}
		},
		Call:        telephonyCall("someone-else"),
		CallID:      telephony.NewCallID,
		Bound:       100 * time.Millisecond,
		CancelBound: 50 * time.Millisecond,
	})
	failing(t, checks, "id is authored and stable")
	failing(t, checks, "the subject's call context is valid and names this provider")
	failing(t, checks, "operations do not block indefinitely")
	failing(t, checks, "a cancelled context makes every operation return promptly")
	failing(t, checks, "an expired deadline makes every operation return promptly")
	failing(t, checks, "safe for concurrent use")
}
