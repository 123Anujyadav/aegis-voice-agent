package callback

import (
	"context"
	"errors"
	"fmt"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
)

// Phase 19 P1-10 — carrier call-ID registration.
//
// A binding may be created only from the carrier's OWN identifier for a call,
// taken from the response to an operation the carrier accepted (a dial, an
// answer). [RegisterAfter] enforces that order: it runs the operation first and
// binds only what the operation returned, only if it succeeded.
//
// # Why this is a function and not a step inside CallLifecycle
//
// The frozen [telephony.Provider] port returns only an error from Dial and
// Answer: it has no way to hand back the carrier's call ID. Threading one
// through would change that frozen interface (the A1 class of decision), and
// the component that will own call control (telephony-gateway, per A4) is
// still a skeleton. So the hook is provider-neutral and stands alone: a
// provider adapter whose own API returns the carrier call ID calls it, and
// nothing else does yet.

// Binder durably records a carrier call binding. [Bindings] implements it in
// memory; packages/go/telephonystore implements it in PostgreSQL.
type Binder interface {
	BindCall(ctx context.Context, b Binding) error
}

// CarrierOperation performs one carrier operation and, on success, returns the
// carrier's identifier for the call exactly as the carrier's response gave it.
// It must not synthesise an identifier: if the response carried none, it
// returns "" and the registration fails.
type CarrierOperation func(ctx context.Context) (providerCallID string, err error)

// ErrRegistrationFailed is returned when the carrier operation SUCCEEDED but no
// binding could be recorded. The carrier side of the call is live and
// unbound: its callbacks will be refused as unknown. The caller must decide
// what to do with the call (typically end it); this function does not.
var ErrRegistrationFailed = errors.New("callback: carrier accepted the operation but the call binding was not recorded")

// RegisterAfter runs op and, only if it succeeds with a usable carrier call ID,
// binds (provider, that ID) to call.
//
//   - Inputs are checked BEFORE op runs, so a registration that could never
//     succeed sends nothing to the carrier.
//   - If op fails, its error is returned and nothing is bound.
//   - If op succeeds but returns an empty or malformed ID, or the bind fails,
//     the error wraps [ErrRegistrationFailed] together with the cause
//     ([ErrInvalidBinding], [ErrBindingConflict], [ErrProviderMismatch], a
//     store failure).
func RegisterAfter(ctx context.Context, binder Binder, provider telephony.ProviderID,
	call telephony.CallID, op CarrierOperation,
) (Binding, error) {
	if binder == nil || op == nil {
		return Binding{}, errors.New("callback: RegisterAfter requires a binder and an operation")
	}
	if !provider.Valid() || !call.Valid() {
		return Binding{}, fmt.Errorf("%w: provider and call must be valid before any carrier operation", ErrInvalidBinding)
	}
	if err := ctx.Err(); err != nil {
		return Binding{}, err
	}

	providerCallID, err := op(ctx)
	if err != nil {
		return Binding{}, err
	}

	b := Binding{Provider: provider, ProviderCallID: providerCallID, Call: call}
	if err := b.Validate(); err != nil {
		return Binding{}, fmt.Errorf("%w: %w", ErrRegistrationFailed, err)
	}
	// The carrier has acted; the bind must not be abandoned because the
	// caller's context ended a moment later.
	if err := binder.BindCall(context.WithoutCancel(ctx), b); err != nil {
		return Binding{}, fmt.Errorf("%w: %w", ErrRegistrationFailed, err)
	}
	return b, nil
}
