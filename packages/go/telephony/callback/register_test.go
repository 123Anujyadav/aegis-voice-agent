package callback

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
)

// Phase 19 P1-10 — registration happens only after a carrier operation
// succeeds, and binds only the identifier that operation returned.

type countingBinder struct {
	inner Binder
	calls atomic.Int64
	err   error
}

func (c *countingBinder) BindCall(ctx context.Context, b Binding) error {
	c.calls.Add(1)
	if c.err != nil {
		return c.err
	}
	return c.inner.BindCall(ctx, b)
}

func registrationRig(t *testing.T) (*Bindings, *countingBinder, telephony.CallID) {
	t.Helper()
	r := newRig(t, 8)
	b, err := NewBindings(RegistryProviders(r.h.Runtime.Registry()), 8)
	if err != nil {
		t.Fatal(err)
	}
	return b, &countingBinder{inner: b}, r.outbound(t, "unused")
}

func carrierReturns(id string, err error) (CarrierOperation, *atomic.Int64) {
	var n atomic.Int64
	return func(context.Context) (string, error) { n.Add(1); return id, err }, &n
}

func TestRegisterAfter_BindsTheIDTheCarrierReturned(t *testing.T) {
	t.Parallel()
	b, binder, call := registrationRig(t)
	op, ran := carrierReturns("carrier-call-1", nil)

	got, err := RegisterAfter(context.Background(), binder, provider, call, op)
	if err != nil {
		t.Fatalf("RegisterAfter: %v", err)
	}
	if want := (Binding{Provider: provider, ProviderCallID: "carrier-call-1", Call: call}); got != want {
		t.Errorf("binding = %+v, want %+v", got, want)
	}
	if ran.Load() != 1 || binder.calls.Load() != 1 {
		t.Errorf("operation ran %d, binder %d; want 1 and 1", ran.Load(), binder.calls.Load())
	}
	if id, err := b.Resolve(provider, "carrier-call-1"); err != nil || id != call {
		t.Errorf("Resolve = %s, %v", id, err)
	}
}

func TestRegisterAfter_FailedOperationBindsNothing(t *testing.T) {
	t.Parallel()
	b, binder, call := registrationRig(t)
	carrierDown := errors.New("carrier refused")
	op, _ := carrierReturns("carrier-call-1", carrierDown)

	if _, err := RegisterAfter(context.Background(), binder, provider, call, op); !errors.Is(err, carrierDown) {
		t.Fatalf("err = %v, want the operation's error", err)
	}
	if binder.calls.Load() != 0 || b.Len() != 0 {
		t.Errorf("binder called %d times, %d bindings; want none", binder.calls.Load(), b.Len())
	}
}

func TestRegisterAfter_RefusesAnUnusableCarrierID(t *testing.T) {
	t.Parallel()
	for name, id := range map[string]string{"empty": "", "malformed": "id with spaces"} {
		t.Run(name, func(t *testing.T) {
			b, binder, call := registrationRig(t)
			op, _ := carrierReturns(id, nil)
			_, err := RegisterAfter(context.Background(), binder, provider, call, op)
			if !errors.Is(err, ErrRegistrationFailed) || !errors.Is(err, ErrInvalidBinding) {
				t.Fatalf("err = %v, want ErrRegistrationFailed and ErrInvalidBinding", err)
			}
			if binder.calls.Load() != 0 || b.Len() != 0 {
				t.Errorf("an invented or malformed ID reached the binder")
			}
		})
	}
}

func TestRegisterAfter_BindFailureIsReportedAsRegistrationFailure(t *testing.T) {
	t.Parallel()
	_, binder, call := registrationRig(t)
	binder.err = ErrBindingConflict
	op, _ := carrierReturns("carrier-call-1", nil)

	_, err := RegisterAfter(context.Background(), binder, provider, call, op)
	if !errors.Is(err, ErrRegistrationFailed) || !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("err = %v, want ErrRegistrationFailed wrapping the conflict", err)
	}
}

func TestRegisterAfter_InvalidInputsSendNothingToTheCarrier(t *testing.T) {
	t.Parallel()
	_, binder, call := registrationRig(t)
	op, ran := carrierReturns("carrier-call-1", nil)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	for name, tc := range map[string]struct {
		ctx      context.Context
		binder   Binder
		provider telephony.ProviderID
		call     telephony.CallID
	}{
		"nil binder":       {context.Background(), nil, provider, call},
		"invalid provider": {context.Background(), binder, "Bad Carrier", call},
		"invalid call":     {context.Background(), binder, provider, "not-a-call"},
		"cancelled":        {cancelled, binder, provider, call},
	} {
		if _, err := RegisterAfter(tc.ctx, tc.binder, tc.provider, tc.call, op); err == nil {
			t.Errorf("%s: RegisterAfter succeeded", name)
		}
	}
	if ran.Load() != 0 {
		t.Errorf("the carrier operation ran %d times for registrations that could not succeed", ran.Load())
	}
}

// The registration and the existing admission path agree: a call registered
// through the hook is the call an authenticated callback resolves to.
func TestRegisterAfter_RegisteredCallIsWhatAdmissionResolves(t *testing.T) {
	t.Parallel()
	a := newAdmissionRig(t)
	sess, err := a.h.Coordinator.Begin(context.Background(), a.h.Outbound())
	if err != nil {
		t.Fatal(err)
	}
	op, _ := carrierReturns("pc-1", nil)
	if _, err := RegisterAfter(context.Background(), a.bindings, provider, sess.ID(), op); err != nil {
		t.Fatal(err)
	}
	got, err := a.adm.Admit(context.Background(), request("e-1|pc-1|answered", true))
	if err != nil || got.Outcome != OutcomeApplied || got.Call != sess.ID() {
		t.Errorf("Admit = %+v, %v; want applied to %s", got, err, sess.ID())
	}
}
