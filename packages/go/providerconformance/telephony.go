package providerconformance

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
)

// -----------------------------------------------------------------------------
// Telephony (call control).
// -----------------------------------------------------------------------------

// DefaultCancelBound is how soon a telephony operation must return once its
// context is cancelled. Generous on purpose: the contract is "must not ignore
// cancellation", not a latency target.
const DefaultCancelBound = 250 * time.Millisecond

// TelephonySubject describes one call-control provider under test.
type TelephonySubject struct {
	// New returns a fresh provider, wired to whatever local stand-in for the
	// carrier its tests use. Never a live carrier.
	New func(t *testing.T) telephony.Provider
	// Call returns a valid call context for Dial, carried by this provider.
	Call func() telephony.CallContext
	// CallID returns the id of a call the provider may be asked to answer,
	// reject or hang up.
	CallID func() telephony.CallID
	// Bound is how long an operation may take before it counts as blocked
	// (DefaultBound if zero).
	Bound time.Duration
	// CancelBound is how soon an operation must return once its context is
	// cancelled (DefaultCancelBound if zero).
	CancelBound time.Duration
}

// RunTelephony runs every telephony check as a subtest of t.
func RunTelephony(t *testing.T, s TelephonySubject) {
	t.Helper()
	run(t, TelephonyChecks(t, s))
}

type telephonySuite struct {
	t      *testing.T
	s      TelephonySubject
	bound  time.Duration
	cancel time.Duration
}

// operation is one call-control method, applied to a provider and a context.
type operation struct {
	name string
	call func(ctx context.Context, p telephony.Provider) error
}

func (c telephonySuite) operations() []operation {
	return []operation{
		{"Dial", func(ctx context.Context, p telephony.Provider) error { return p.Dial(ctx, c.s.Call()) }},
		{"Answer", func(ctx context.Context, p telephony.Provider) error { return p.Answer(ctx, c.s.CallID()) }},
		{"Reject", func(ctx context.Context, p telephony.Provider) error {
			return p.Reject(ctx, c.s.CallID(), "conformance")
		}},
		{"Hangup", func(ctx context.Context, p telephony.Provider) error {
			return p.Hangup(ctx, c.s.CallID(), "conformance")
		}},
	}
}

// TelephonyChecks returns the telephony checks for s.
func TelephonyChecks(t *testing.T, s TelephonySubject) []Check {
	cancelBound := s.CancelBound
	if cancelBound <= 0 {
		cancelBound = DefaultCancelBound
	}
	c := telephonySuite{t: t, s: s, bound: bound(s.Bound), cancel: cancelBound}
	return []Check{
		{"id is authored and stable", "ID identifies the provider. Authored and stable.", c.id},
		{
			"the subject's call context is valid and names this provider",
			"Provider is who is carrying the call. (A subject check: Dial is exercised with a context the runtime would accept.)",
			c.callContext,
		},
		{
			"operations do not block indefinitely",
			"Implementations ... must not block indefinitely.", c.promptly,
		},
		{
			"a cancelled context makes every operation return promptly",
			"The runtime bounds every call with a context deadline, but a provider that ignores cancellation holds a goroutine per call.",
			c.cancellation,
		},
		{
			"an expired deadline makes every operation return promptly",
			"The runtime bounds every call with a context deadline.", c.expired,
		},
		{"safe for concurrent use", "Implementations must be safe for concurrent use.", c.concurrent},
	}
}

func (c telephonySuite) id() error {
	if err := stableID(c.s.New(c.t).ID); err != nil {
		return err
	}
	if id := c.s.New(c.t).ID(); !id.Valid() {
		return fmt.Errorf("ID() %q is not a valid provider id (lower-case letters, digits, '-' and '_', at most 64)", id)
	}
	return nil
}

func (c telephonySuite) callContext() error {
	cc := c.s.Call()
	if err := cc.Validate(); err != nil {
		return fmt.Errorf("the subject's call context: %w", err)
	}
	if id := c.s.New(c.t).ID(); cc.Provider != id {
		return fmt.Errorf("the subject's call context names provider %q, not %q", cc.Provider, id)
	}
	return nil
}

// promptly calls each operation with a live context whose deadline is far
// beyond the bound: an operation that waits for the deadline blocks.
func (c telephonySuite) promptly() error {
	p := c.s.New(c.t)
	for _, op := range c.operations() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		ok := within(c.bound, func() { _ = op.call(ctx, p) }) //nolint:errcheck // only the time to return is checked: the contract does not require success
		cancel()
		if !ok {
			return fmt.Errorf("%s did not return within %v", op.name, c.bound)
		}
	}
	return nil
}

// cancellation starts each operation, cancels its context shortly after, and
// requires it to return within the cancel bound of the cancellation.
func (c telephonySuite) cancellation() error {
	p := c.s.New(c.t)
	for _, op := range c.operations() {
		ctx, cancel := context.WithCancel(context.Background())
		returned := make(chan struct{})
		go func() {
			defer close(returned)
			_ = op.call(ctx, p) //nolint:errcheck // only the time to return is checked
		}()
		time.Sleep(10 * time.Millisecond)
		cancel()
		select {
		case <-returned:
		case <-time.After(c.cancel):
			return fmt.Errorf("%s did not return within %v of its context being cancelled", op.name, c.cancel)
		}
	}
	return nil
}

func (c telephonySuite) expired() error {
	p := c.s.New(c.t)
	for _, op := range c.operations() {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		ok := within(c.cancel, func() { _ = op.call(ctx, p) }) //nolint:errcheck // an expired deadline may or may not be an error; only the time is checked
		cancel()
		if !ok {
			return fmt.Errorf("%s with an expired deadline took more than %v", op.name, c.cancel)
		}
	}
	return nil
}

// concurrent runs every operation from several goroutines on one provider.
// The race detector, which CI runs, turns unsynchronised state into a failure.
func (c telephonySuite) concurrent() error {
	p := c.s.New(c.t)
	ops := c.operations()
	const workers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for i := 0; i < workers; i++ {
		for _, op := range ops {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), c.bound)
				defer cancel()
				if err := op.call(ctx, p); errors.Is(err, context.DeadlineExceeded) {
					mu.Lock()
					errs = append(errs, fmt.Errorf("concurrent %s: %w", op.name, err))
					mu.Unlock()
				}
				_, _ = p.ID(), p.Capabilities() // read alongside the calls
			}()
		}
	}
	if !within(c.bound+c.cancel, wg.Wait) {
		return fmt.Errorf("concurrent operations did not finish within %v", c.bound+c.cancel)
	}
	return errors.Join(errs...)
}
