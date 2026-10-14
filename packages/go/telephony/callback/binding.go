package callback

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
)

// Phase 19 P1-6 — provider-scoped carrier call-ID binding.
//
// A carrier call ID is unique only within its carrier. The binding is therefore
// keyed by (provider, carrier call ID), and it is CREATED, never inferred: the
// component that places or accepts a call, and learns the carrier's ID for it,
// calls [Bindings.Bind]. Nothing here derives a binding from a phone number, a
// timestamp or the order callbacks arrive in.

// Binding errors.
var (
	// ErrInvalidBinding is returned for an empty or malformed provider,
	// carrier call ID or internal call ID.
	ErrInvalidBinding = errors.New("callback: invalid call binding")

	// ErrBindingConflict is returned when a bind would map a carrier call ID
	// to a different internal call than it already maps to, or bind an
	// internal call that is already bound to a different carrier call.
	ErrBindingConflict = errors.New("callback: conflicting call binding")

	// ErrProviderMismatch is returned when the internal call is carried by a
	// different provider than the binding names.
	ErrProviderMismatch = errors.New("callback: provider does not carry this call")

	// ErrBindingsFull is returned when the registry is at capacity. It refuses
	// rather than forgets: a forgotten binding would turn a live call's
	// callbacks into unknown-call errors.
	ErrBindingsFull = errors.New("callback: binding registry is full")
)

// Binding maps one carrier call to one internal call.
type Binding struct {
	Provider       telephony.ProviderID
	ProviderCallID string
	Call           telephony.CallID
}

// Validate reports whether every field is present and well formed. It does not
// check the binding against any call.
func (b Binding) Validate() error { return b.validate() }

func (b Binding) validate() error {
	var problems []string
	if !b.Provider.Valid() {
		problems = append(problems, "Provider must be a valid provider ID")
	}
	if p := checkID("ProviderCallID", b.ProviderCallID, true); p != "" {
		problems = append(problems, p)
	}
	if !b.Call.Valid() {
		problems = append(problems, "Call must be a platform call ID")
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %v", ErrInvalidBinding, problems)
	}
	return nil
}

// CallProviders reports which provider carries an internal call. It is how a
// bind is checked against the call it names.
type CallProviders interface {
	// ProviderOf returns the call's provider, or an error wrapping
	// telephony.ErrCallNotFound.
	ProviderOf(id telephony.CallID) (telephony.ProviderID, error)
}

// RegistryProviders adapts a telephony call registry to [CallProviders].
func RegistryProviders(reg *telephony.CallRegistry) CallProviders { return registryProviders{reg} }

type registryProviders struct{ reg *telephony.CallRegistry }

func (r registryProviders) ProviderOf(id telephony.CallID) (telephony.ProviderID, error) {
	sess, err := r.reg.Get(id)
	if err != nil {
		return "", err
	}
	return sess.Context().Provider, nil
}

type carrierKey struct {
	provider       telephony.ProviderID
	providerCallID string
}

// Bindings is an in-process, provider-scoped binding registry. It implements
// [Resolver].
//
// # Rules
//
//   - One carrier call maps to exactly one internal call, and one internal
//     call to exactly one carrier call. Transfer legs and other multi-leg
//     shapes are not modelled; a second binding for a call is refused.
//   - Binding the same triple again is a no-op (idempotent registration).
//   - A bind is checked against the live call: it must exist and be carried by
//     the named provider.
//   - Bindings outlive the call, so a late callback still resolves and is
//     judged terminal by the ingestor rather than reported unknown. The owner
//     removes them with [Bindings.Remove] once its retention period is over.
//
// # Limits
//
// IN MEMORY ONLY. Every binding is lost on restart: callbacks for calls bound
// before the restart then fail with [ErrUnknownCall]. Not shared between
// replicas: a callback reaching a replica that did not bind the call fails the
// same way. At Capacity, Bind fails with [ErrBindingsFull].
type Bindings struct {
	calls    CallProviders
	capacity int

	mu         sync.RWMutex
	byCarrier  map[carrierKey]telephony.CallID
	byInternal map[telephony.CallID]carrierKey
}

// NewBindings builds a registry holding at most capacity bindings, checking
// each bind against calls.
func NewBindings(calls CallProviders, capacity int) (*Bindings, error) {
	if calls == nil {
		return nil, errors.New("callback: NewBindings requires a CallProviders")
	}
	if capacity <= 0 {
		return nil, fmt.Errorf("callback: Bindings capacity must be positive, got %d", capacity)
	}
	return &Bindings{
		calls:      calls,
		capacity:   capacity,
		byCarrier:  make(map[carrierKey]telephony.CallID),
		byInternal: make(map[telephony.CallID]carrierKey),
	}, nil
}

// Bind records that b.ProviderCallID at b.Provider is the internal call b.Call.
func (r *Bindings) Bind(b Binding) error {
	if err := b.validate(); err != nil {
		return err
	}
	carrier, err := r.calls.ProviderOf(b.Call)
	if err != nil {
		return fmt.Errorf("callback: bind %s: %w", b.Call, err)
	}
	if carrier != b.Provider {
		return fmt.Errorf("%w: %s is carried by %s, not %s", ErrProviderMismatch, b.Call, carrier, b.Provider)
	}

	key := carrierKey{b.Provider, b.ProviderCallID}
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, carrierBound := r.byCarrier[key]
	other, callBound := r.byInternal[b.Call]
	switch {
	case carrierBound && existing == b.Call:
		return nil // the same binding, registered again
	case carrierBound:
		return fmt.Errorf("%w: carrier call at %s is already bound to %s", ErrBindingConflict, b.Provider, existing)
	case callBound:
		return fmt.Errorf("%w: %s is already bound to a carrier call at %s", ErrBindingConflict, b.Call, other.provider)
	}
	if len(r.byCarrier) >= r.capacity {
		return ErrBindingsFull
	}
	r.byCarrier[key] = b.Call
	r.byInternal[b.Call] = key
	return nil
}

// BindCall implements [Binder]. The registry is in memory, so ctx is checked
// only for cancellation before the bind.
func (r *Bindings) BindCall(ctx context.Context, b Binding) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.Bind(b)
}

// Resolve implements [Resolver]. An unbound carrier call — including the same
// carrier call ID at a different provider — is [ErrUnknownCall].
func (r *Bindings) Resolve(provider telephony.ProviderID, providerCallID string) (telephony.CallID, error) {
	if !provider.Valid() || checkID("ProviderCallID", providerCallID, true) != "" {
		return "", fmt.Errorf("%w: %w", ErrUnknownCall, ErrInvalidBinding)
	}
	r.mu.RLock()
	id, ok := r.byCarrier[carrierKey{provider, providerCallID}]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("%w: no binding for this carrier call at %s", ErrUnknownCall, provider)
	}
	return id, nil
}

// Remove deletes a binding. It reports whether one existed.
func (r *Bindings) Remove(provider telephony.ProviderID, providerCallID string) bool {
	key := carrierKey{provider, providerCallID}
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byCarrier[key]
	if !ok {
		return false
	}
	delete(r.byCarrier, key)
	delete(r.byInternal, id)
	return true
}

// Len reports how many bindings are held.
func (r *Bindings) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byCarrier)
}
