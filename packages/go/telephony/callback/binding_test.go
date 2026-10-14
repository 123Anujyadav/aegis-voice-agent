package callback

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
)

// Phase 19 P1-6 — provider-scoped call-ID binding, against a fake call table
// so two providers can be exercised without registering a second carrier.

type fakeCalls map[telephony.CallID]telephony.ProviderID

func (f fakeCalls) ProviderOf(id telephony.CallID) (telephony.ProviderID, error) {
	p, ok := f[id]
	if !ok {
		return "", fmt.Errorf("%w: %s", telephony.ErrCallNotFound, id)
	}
	return p, nil
}

const (
	carrierA = telephony.ProviderID("carrier-a")
	carrierB = telephony.ProviderID("carrier-b")
	callA1   = telephony.CallID("call_a1")
	callA2   = telephony.CallID("call_a2")
	callB1   = telephony.CallID("call_b1")
)

func newBindings(t *testing.T, capacity int) *Bindings {
	t.Helper()
	b, err := NewBindings(fakeCalls{callA1: carrierA, callA2: carrierA, callB1: carrierB}, capacity)
	if err != nil {
		t.Fatalf("NewBindings: %v", err)
	}
	return b
}

func TestBindings_ResolvesTheBoundCall(t *testing.T) {
	t.Parallel()
	b := newBindings(t, 8)
	if err := b.Bind(Binding{Provider: carrierA, ProviderCallID: "pc-1", Call: callA1}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	got, err := b.Resolve(carrierA, "pc-1")
	if err != nil || got != callA1 {
		t.Fatalf("Resolve = %s, %v; want %s", got, err, callA1)
	}
}

func TestBindings_RefusesInvalidIdentifiers(t *testing.T) {
	t.Parallel()
	b := newBindings(t, 8)
	for _, tc := range []struct {
		name string
		bind Binding
	}{
		{"empty provider", Binding{ProviderCallID: "pc-1", Call: callA1}},
		{"invalid provider", Binding{Provider: "Carrier A", ProviderCallID: "pc-1", Call: callA1}},
		{"empty carrier call ID", Binding{Provider: carrierA, Call: callA1}},
		{"oversized carrier call ID", Binding{Provider: carrierA, ProviderCallID: strings.Repeat("x", maxIDLen+1), Call: callA1}},
		{"carrier call ID with a space", Binding{Provider: carrierA, ProviderCallID: "pc 1", Call: callA1}},
		{"empty internal call", Binding{Provider: carrierA, ProviderCallID: "pc-1"}},
		{"internal call without prefix", Binding{Provider: carrierA, ProviderCallID: "pc-1", Call: "a1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := b.Bind(tc.bind); !errors.Is(err, ErrInvalidBinding) {
				t.Errorf("Bind = %v, want %v", err, ErrInvalidBinding)
			}
		})
	}
	if b.Len() != 0 {
		t.Errorf("%d bindings recorded from invalid input", b.Len())
	}
	for _, in := range []struct {
		p  telephony.ProviderID
		id string
	}{{"", "pc-1"}, {carrierA, ""}, {carrierA, "pc\n1"}} {
		if _, err := b.Resolve(in.p, in.id); !errors.Is(err, ErrUnknownCall) {
			t.Errorf("Resolve(%q, %q) = %v, want %v", in.p, in.id, err, ErrUnknownCall)
		}
	}
}

func TestBindings_UnknownCarrierCallFailsClosed(t *testing.T) {
	t.Parallel()
	b := newBindings(t, 8)
	if _, err := b.Resolve(carrierA, "never-bound"); !errors.Is(err, ErrUnknownCall) {
		t.Fatalf("Resolve = %v, want %v", err, ErrUnknownCall)
	}
}

func TestBindings_CarrierCallIDsAreProviderScoped(t *testing.T) {
	t.Parallel()
	b := newBindings(t, 8)
	if err := b.Bind(Binding{Provider: carrierA, ProviderCallID: "same", Call: callA1}); err != nil {
		t.Fatal(err)
	}
	// The same carrier call ID at another provider resolves to nothing...
	if _, err := b.Resolve(carrierB, "same"); !errors.Is(err, ErrUnknownCall) {
		t.Fatalf("cross-provider Resolve = %v, want %v", err, ErrUnknownCall)
	}
	// ...until that provider binds it, to its own call.
	if err := b.Bind(Binding{Provider: carrierB, ProviderCallID: "same", Call: callB1}); err != nil {
		t.Fatalf("Bind at carrier-b: %v", err)
	}
	if got, _ := b.Resolve(carrierA, "same"); got != callA1 {
		t.Errorf("carrier-a resolves to %s, want %s", got, callA1)
	}
	if got, _ := b.Resolve(carrierB, "same"); got != callB1 {
		t.Errorf("carrier-b resolves to %s, want %s", got, callB1)
	}
}

func TestBindings_RefusesAProviderThatDoesNotCarryTheCall(t *testing.T) {
	t.Parallel()
	b := newBindings(t, 8)
	err := b.Bind(Binding{Provider: carrierB, ProviderCallID: "pc-1", Call: callA1})
	if !errors.Is(err, ErrProviderMismatch) {
		t.Fatalf("Bind = %v, want %v", err, ErrProviderMismatch)
	}
	if err := b.Bind(Binding{Provider: carrierA, ProviderCallID: "pc-1", Call: "call_unknown"}); !errors.Is(err, telephony.ErrCallNotFound) {
		t.Errorf("bind of an unknown call = %v, want ErrCallNotFound", err)
	}
	if b.Len() != 0 {
		t.Errorf("%d bindings recorded", b.Len())
	}
}

func TestBindings_RefusesConflictingRebinding(t *testing.T) {
	t.Parallel()
	b := newBindings(t, 8)
	if err := b.Bind(Binding{Provider: carrierA, ProviderCallID: "pc-1", Call: callA1}); err != nil {
		t.Fatal(err)
	}
	// The same carrier call to a different internal call.
	if err := b.Bind(Binding{Provider: carrierA, ProviderCallID: "pc-1", Call: callA2}); !errors.Is(err, ErrBindingConflict) {
		t.Errorf("rebinding a carrier call = %v, want %v", err, ErrBindingConflict)
	}
	// The same internal call to a second carrier call.
	if err := b.Bind(Binding{Provider: carrierA, ProviderCallID: "pc-2", Call: callA1}); !errors.Is(err, ErrBindingConflict) {
		t.Errorf("second carrier call for one internal call = %v, want %v", err, ErrBindingConflict)
	}
	if got, _ := b.Resolve(carrierA, "pc-1"); got != callA1 {
		t.Errorf("the original binding changed: %s", got)
	}
	if _, err := b.Resolve(carrierA, "pc-2"); !errors.Is(err, ErrUnknownCall) {
		t.Errorf("the refused binding resolves: %v", err)
	}
}

func TestBindings_DuplicateRegistrationIsIdempotent(t *testing.T) {
	t.Parallel()
	b := newBindings(t, 8)
	bind := Binding{Provider: carrierA, ProviderCallID: "pc-1", Call: callA1}
	for i := 0; i < 3; i++ {
		if err := b.Bind(bind); err != nil {
			t.Fatalf("Bind #%d: %v", i+1, err)
		}
	}
	if b.Len() != 1 {
		t.Errorf("Len = %d, want 1", b.Len())
	}
}

func TestBindings_CapacityRefusesRatherThanForgets(t *testing.T) {
	t.Parallel()
	b := newBindings(t, 1)
	if err := b.Bind(Binding{Provider: carrierA, ProviderCallID: "pc-1", Call: callA1}); err != nil {
		t.Fatal(err)
	}
	if err := b.Bind(Binding{Provider: carrierA, ProviderCallID: "pc-2", Call: callA2}); !errors.Is(err, ErrBindingsFull) {
		t.Fatalf("Bind at capacity = %v, want %v", err, ErrBindingsFull)
	}
	if got, _ := b.Resolve(carrierA, "pc-1"); got != callA1 {
		t.Error("an existing binding was forgotten to make room")
	}
	// The same binding again is not a new one, even when full.
	if err := b.Bind(Binding{Provider: carrierA, ProviderCallID: "pc-1", Call: callA1}); err != nil {
		t.Errorf("idempotent Bind at capacity: %v", err)
	}
	if !b.Remove(carrierA, "pc-1") || b.Remove(carrierA, "pc-1") {
		t.Error("Remove should report true once, then false")
	}
	if err := b.Bind(Binding{Provider: carrierA, ProviderCallID: "pc-2", Call: callA2}); err != nil {
		t.Errorf("Bind after Remove: %v", err)
	}
	// Removing pc-1 freed call_a1 (no conflict), but the registry is full again.
	if err := b.Bind(Binding{Provider: carrierA, ProviderCallID: "pc-9", Call: callA1}); !errors.Is(err, ErrBindingsFull) {
		t.Errorf("Bind = %v, want %v (still full)", err, ErrBindingsFull)
	}
}

func TestBindings_ConcurrentBindAndResolveAgree(t *testing.T) {
	t.Parallel()
	b := newBindings(t, 8)

	const n = 32
	var wg sync.WaitGroup
	won := make(chan telephony.CallID, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		target := callA1
		if i%2 == 1 {
			target = callA2
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := b.Bind(Binding{Provider: carrierA, ProviderCallID: "contested", Call: target})
			switch {
			case err == nil:
				won <- target
			case errors.Is(err, ErrBindingConflict):
			default:
				t.Errorf("Bind: unexpected %v", err)
			}
			if got, err := b.Resolve(carrierA, "contested"); err == nil && got != callA1 && got != callA2 {
				t.Errorf("Resolve returned %s", got)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(won)

	winners := map[telephony.CallID]bool{}
	for id := range won {
		winners[id] = true
	}
	if len(winners) != 1 {
		t.Fatalf("binding won by %v, want exactly one internal call", winners)
	}
	for id := range winners {
		if got, _ := b.Resolve(carrierA, "contested"); got != id {
			t.Errorf("Resolve = %s, want the winner %s", got, id)
		}
	}
	if b.Len() != 1 {
		t.Errorf("Len = %d, want 1", b.Len())
	}
}

func TestNewBindings_RefusesBadConfiguration(t *testing.T) {
	t.Parallel()
	if _, err := NewBindings(nil, 8); err == nil {
		t.Error("NewBindings(nil) succeeded")
	}
	if _, err := NewBindings(fakeCalls{}, 0); err == nil {
		t.Error("NewBindings with zero capacity succeeded")
	}
}
