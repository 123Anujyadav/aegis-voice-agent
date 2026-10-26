// Package otpdelivery composes OTP delivery providers into the one
// app.OtpSender identity uses: an ordered failover across named providers,
// each attempt bounded in time (ADR-0010 §9: "a fallback provider is
// required"; §15: "Secondary OTP provider; delivery rate monitored").
//
// WHAT THIS IS, AND WHAT IT IS NOT. No SMS provider is implemented in this
// repository. A real Provider needs a commercial account, credentials
// provisioned as Secrets, egress to the provider, and in India registered
// sender ids and message templates (TRAI DLT). None of those exist, so nothing
// here delivers a message, and identity is not configured with it: without an
// OtpSender, identity refuses OTP requests with ERR_OTP_DELIVERY_UNAVAILABLE,
// truthfully. This package is the boundary a provider adapter plugs into, and
// its failover semantics, tested with fakes.
//
// # Semantics
//
// Providers are tried in order. The first that accepts the message ends the
// attempt successfully. A provider that fails, or does not answer within
// AttemptTimeout, is followed by the next. When the caller's context ends —
// the request deadline, or the client going away — no further provider is
// tried: a code delivered after its request has failed is a code nobody is
// waiting for.
//
// The same code is sent to each provider tried. If a provider timed out but
// delivered late, the subscriber may receive the same code twice; never two
// different codes.
//
// Errors never carry a provider's text: it may echo the number or the code.
// The sender returns ErrAllProvidersFailed, or the caller's context error.
//
// # Account-deletion codes
//
// A web account-deletion code (app.DeletionCodeSender) is a DIFFERENT message:
// India DLT requires each message template to be registered, and a deletion
// message is not a sign-in message. A provider that has a registered deletion
// template implements DeletionCodeProvider as well as Provider; one that does
// not is never asked to send a deletion code. Deletion codes get the same
// failover, attempt bound and error discipline as sign-in codes, over the
// deletion-capable providers only, with their own health and observations (a
// rejected deletion template says nothing about sign-in delivery).
// Sender.DeletionCodes returns a deletion sender only when at least one
// provider can deliver one: identity must not be told deletion codes can be
// sent when none can.
package otpdelivery

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

var (
	_ app.OtpSender          = (*Sender)(nil)
	_ app.DeletionCodeSender = (*deletionSender)(nil)
)

// Provider delivers one OTP message. Implementations must honour ctx, must not
// retry internally (Sender owns failover), and must not log the number or the
// code. Name identifies the provider in observations; it must be a fixed,
// low-cardinality label such as "primary-sms".
type Provider interface {
	Name() string
	SendOtp(ctx context.Context, msisdn, code string) error
}

// DeletionCodeProvider is a Provider that can also deliver an account-deletion
// code, with the provider's separately registered deletion template. The same
// rules apply: honour ctx, no internal retry, never log the number or the
// code, never return text that may echo them.
type DeletionCodeProvider interface {
	Provider
	SendDeletionCode(ctx context.Context, msisdn, code string) error
}

// Outcome is the result of one provider attempt. The vocabulary is closed.
type Outcome string

const (
	// OutcomeDelivered is a provider that accepted the message.
	OutcomeDelivered Outcome = "delivered"
	// OutcomeFailed is a provider that returned an error.
	OutcomeFailed Outcome = "failed"
	// OutcomeTimeout is a provider that did not answer within AttemptTimeout.
	OutcomeTimeout Outcome = "timeout"
	// OutcomeAbandoned is an attempt cut short because the caller's context
	// ended; no further provider is tried.
	OutcomeAbandoned Outcome = "abandoned"
)

// ErrAllProvidersFailed is every provider failing or timing out. It carries no
// provider text.
var ErrAllProvidersFailed = errors.New("otpdelivery: no provider delivered the code")

// ErrInvalidConfig is a Sender configuration New refuses.
var ErrInvalidConfig = errors.New("otpdelivery: invalid configuration")

// Bounds on configuration. The identity request handler allows 10 s in
// total, so the attempts of one request must fit well inside it.
const (
	MaxProviders      = 3
	MinAttemptTimeout = 500 * time.Millisecond
	MaxAttemptTimeout = 4 * time.Second
)

// Config configures a Sender.
type Config struct {
	// Providers, in the order they are tried. At least one, at most
	// MaxProviders, with distinct names.
	Providers []Provider
	// AttemptTimeout bounds each provider attempt, within
	// [MinAttemptTimeout, MaxAttemptTimeout].
	AttemptTimeout time.Duration
	// Observe, if set, is called once per sign-in code attempt with the
	// provider's name and its outcome: the hook for delivery-rate metrics
	// (ADR-0010 §9). It receives nothing from the message. It must be safe
	// for concurrent use.
	Observe func(provider string, outcome Outcome)
	// ObserveDeletion is the same, for account-deletion code attempts, kept
	// apart so deletion sends can be watched against their own budget.
	ObserveDeletion func(provider string, outcome Outcome)
}

var providerNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// Sender is an app.OtpSender over an ordered list of providers, and, through
// DeletionCodes, an app.DeletionCodeSender over the deletion-capable ones.
type Sender struct {
	signIn   *chain
	deletion *chain // nil when no provider can deliver a deletion code
}

// chain is one ordered failover: its providers' send functions, the attempt
// bound, the observation hook and the providers' health for this message.
type chain struct {
	names   []string
	sends   []func(ctx context.Context, msisdn, code string) error
	timeout time.Duration
	observe func(string, Outcome)
	now     func() time.Time

	mu     sync.Mutex
	health map[string]*ProviderHealth
}

// ProviderHealth is what the sender has observed of one provider. It carries
// no message data.
type ProviderHealth struct {
	// Name is the provider's name.
	Name string
	// LastOutcome is the provider's most recent attempt's outcome, or "" if
	// it has not been tried.
	LastOutcome Outcome
	// LastAttemptAt is when that attempt ended.
	LastAttemptAt time.Time
	// ConsecutiveFailures counts failed or timed-out attempts since the last
	// delivery. An abandoned attempt (the caller gave up) does not count: it
	// says nothing about the provider.
	ConsecutiveFailures int
}

// New validates cfg and returns a Sender.
func New(cfg Config) (*Sender, error) {
	if len(cfg.Providers) == 0 || len(cfg.Providers) > MaxProviders {
		return nil, fmt.Errorf("%w: between 1 and %d providers are required", ErrInvalidConfig, MaxProviders)
	}
	if cfg.AttemptTimeout < MinAttemptTimeout || cfg.AttemptTimeout > MaxAttemptTimeout {
		return nil, fmt.Errorf("%w: the attempt timeout must be within [%v, %v]", ErrInvalidConfig, MinAttemptTimeout, MaxAttemptTimeout)
	}
	seen := map[string]bool{}
	for _, p := range cfg.Providers {
		if p == nil {
			return nil, fmt.Errorf("%w: a provider is nil", ErrInvalidConfig)
		}
		name := p.Name()
		if !providerNamePattern.MatchString(name) {
			return nil, fmt.Errorf("%w: a provider name must be a short lower-case label", ErrInvalidConfig)
		}
		if seen[name] {
			return nil, fmt.Errorf("%w: provider names must be distinct", ErrInvalidConfig)
		}
		seen[name] = true
	}
	signIn := newChain(cfg.AttemptTimeout, cfg.Observe)
	deletion := newChain(cfg.AttemptTimeout, cfg.ObserveDeletion)
	for _, p := range cfg.Providers {
		signIn.add(p.Name(), p.SendOtp)
		if d, ok := p.(DeletionCodeProvider); ok {
			deletion.add(d.Name(), d.SendDeletionCode)
		}
	}
	s := &Sender{signIn: signIn}
	if len(deletion.names) > 0 {
		s.deletion = deletion
	}
	return s, nil
}

func newChain(timeout time.Duration, observe func(string, Outcome)) *chain {
	if observe == nil {
		observe = func(string, Outcome) {}
	}
	return &chain{timeout: timeout, observe: observe, now: time.Now, health: map[string]*ProviderHealth{}}
}

func (c *chain) add(name string, send func(ctx context.Context, msisdn, code string) error) {
	c.names = append(c.names, name)
	c.sends = append(c.sends, send)
	c.health[name] = &ProviderHealth{Name: name}
}

// Health returns a snapshot of every provider's sign-in delivery health, in
// the order the providers are tried. It is safe for concurrent use.
func (s *Sender) Health() []ProviderHealth { return s.signIn.snapshot() }

// DeletionHealth returns the deletion-capable providers' account-deletion
// delivery health, in order; empty when none can deliver one.
func (s *Sender) DeletionHealth() []ProviderHealth {
	if s.deletion == nil {
		return nil
	}
	return s.deletion.snapshot()
}

// DeletionCodes returns the account-deletion code sender, and false when no
// configured provider can deliver a deletion code. Identity is configured
// with it (SetDeletionCodeSender) only when it exists: otherwise identity
// would accept deletion requests whose codes nobody can deliver.
func (s *Sender) DeletionCodes() (app.DeletionCodeSender, bool) {
	if s.deletion == nil {
		return nil, false
	}
	return &deletionSender{s.deletion}, true
}

// SendOtp implements app.OtpSender: it delivers through the first provider
// that accepts, in order. See the package documentation for the semantics.
func (s *Sender) SendOtp(ctx context.Context, msisdn, code string) error {
	return s.signIn.send(ctx, msisdn, code)
}

// deletionSender is the app.DeletionCodeSender over the deletion chain.
type deletionSender struct{ c *chain }

// SendDeletionCode implements app.DeletionCodeSender, with the sign-in
// semantics over the deletion-capable providers.
func (d *deletionSender) SendDeletionCode(ctx context.Context, msisdn, code string) error {
	return d.c.send(ctx, msisdn, code)
}

func (c *chain) snapshot() []ProviderHealth {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ProviderHealth, 0, len(c.names))
	for _, n := range c.names {
		out = append(out, *c.health[n])
	}
	return out
}

// record updates name's health with one attempt's outcome.
func (c *chain) record(name string, o Outcome) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.health[name]
	h.LastOutcome, h.LastAttemptAt = o, c.now()
	switch o {
	case OutcomeDelivered:
		h.ConsecutiveFailures = 0
	case OutcomeFailed, OutcomeTimeout:
		h.ConsecutiveFailures++
	}
}

// send tries the chain's providers in order: see the package documentation.
func (c *chain) send(ctx context.Context, msisdn, code string) error {
	for i, name := range c.names {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		outcome := c.attempt(ctx, c.sends[i], msisdn, code)
		c.record(name, outcome)
		c.observe(name, outcome)
		switch outcome {
		case OutcomeDelivered:
			return nil
		case OutcomeAbandoned:
			return ctx.Err()
		}
	}
	return ErrAllProvidersFailed
}

// attempt runs one provider under its own deadline and classifies the result.
//
// The wait is bounded even for a provider that ignores its context: the
// attempt stops waiting at the deadline, and the provider's eventual answer is
// discarded (the channel is buffered, so its goroutine always finishes).
func (c *chain) attempt(ctx context.Context, send func(context.Context, string, string) error, msisdn, code string) Outcome {
	actx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- send(actx, msisdn, code) }()
	var err error
	select {
	case err = <-done:
	case <-actx.Done():
		err = actx.Err()
	}
	switch {
	case ctx.Err() != nil:
		// The caller's context ended during the attempt: whatever the
		// provider said, nobody is waiting for this code.
		return OutcomeAbandoned
	case err == nil:
		return OutcomeDelivered
	case errors.Is(err, context.DeadlineExceeded) || actx.Err() != nil:
		return OutcomeTimeout
	default:
		return OutcomeFailed
	}
}
