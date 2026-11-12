package app

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// # Sign-in delivery controls (F-1; ADR-0033 §7, accepted for M1 2026-11-12)
//
// Once a real SMS provider is configured, anyone can ask for a sign-in code
// for any number, and the per-number limits alone leave the cost of many
// numbers unbounded (SMS pumping). Two controls bound it, both configured
// together by SetSignInDeliveryControls:
//
//   - THE TESTER ALLOWLIST: only the listed numbers are sent a code. Any
//     other number is answered ErrOtpDeliveryUnavailable, the same answer as
//     "no delivery configured", BEFORE any other work: no challenge, no
//     per-number allowance and no budget is spent, and nothing is sent. The
//     list is held only as MSISDN pseudonyms (HMAC under the service key),
//     never as numbers.
//   - THE GLOBAL HOURLY CAP: at most hourlyCap sign-in codes are handed to the
//     sender in each clock hour (UTC), across every number and every replica
//     sharing the repository (IdentityRepository.ReserveSignInDelivery, one
//     atomic step). It is reserved after the number's own limits, immediately
//     before the send; a spent hour, or a repository error, sends nothing and
//     withdraws the challenge, exactly as a failed delivery does. A
//     reservation counts the hand-over, whether or not the provider then
//     delivers.
//
// Without them (the default) RequestOtp behaves exactly as before. Bootstrap
// requires both whenever a real provider is configured (internal/bootstrap,
// otp_controls.go). The allowlist also applies to account-deletion codes
// (web_erasure.go), which are not wired (decision O13), so they can never
// bypass it; they have their own hourly budget instead of this cap.

// MaxSignInHourlyCap is the largest accepted hourly cap.
const MaxSignInHourlyCap = 10000

// ErrInvalidSignInDeliveryControls is a refused SetSignInDeliveryControls.
// Its messages name a position in the list, never a number.
var ErrInvalidSignInDeliveryControls = errors.New("identity: invalid sign-in delivery controls")

// signInDeliveryControls is the configured allowlist, as pseudonyms, and cap.
type signInDeliveryControls struct {
	allowed   map[string]struct{}
	hourlyCap int
}

func (c *signInDeliveryControls) allows(pseudonym string) bool {
	_, ok := c.allowed[pseudonym]
	return ok
}

// SetSignInDeliveryControls configures the tester allowlist and the global
// hourly cap (see above). The list must hold at least one number, each in
// canonical E.164 and none twice, and the cap must be in [1,
// MaxSignInHourlyCap]. On error nothing changes, and the error names
// positions in the list (1-based), never a number. Construction-time
// configuration, like SetOtpSender: not safe to change while serving.
func (s *IdentityService) SetSignInDeliveryControls(allowed []string, hourlyCap int) error {
	if hourlyCap < 1 || hourlyCap > MaxSignInHourlyCap {
		return fmt.Errorf("%w: the hourly cap must be between 1 and %d", ErrInvalidSignInDeliveryControls, MaxSignInHourlyCap)
	}
	if len(allowed) == 0 {
		return fmt.Errorf("%w: the allowlist holds no number", ErrInvalidSignInDeliveryControls)
	}
	set := make(map[string]struct{}, len(allowed))
	first := make(map[string]int, len(allowed))
	for i, number := range allowed {
		pseudonym, err := s.msisdnPseudonym(number)
		if err != nil {
			return fmt.Errorf("%w: allowlist entry %d is not a canonical E.164 number", ErrInvalidSignInDeliveryControls, i+1)
		}
		if at, dup := first[pseudonym]; dup {
			return fmt.Errorf("%w: allowlist entries %d and %d are the same number", ErrInvalidSignInDeliveryControls, at, i+1)
		}
		first[pseudonym] = i + 1
		set[pseudonym] = struct{}{}
	}
	s.signInControls = &signInDeliveryControls{allowed: set, hourlyCap: hourlyCap}
	return nil
}

// SignInDeliveryControls reports whether the sign-in delivery controls are
// configured and, if so, how many numbers the allowlist holds and the hourly
// cap. It reveals no number.
func (s *IdentityService) SignInDeliveryControls() (allowedNumbers, hourlyCap int, configured bool) {
	c := s.signInControls
	if c == nil {
		return 0, 0, false
	}
	return len(c.allowed), c.hourlyCap, true
}

// reserveSignInDelivery reserves one delivery in the current hour when the
// controls are configured. A spent hour and a repository error are both
// ErrOtpDeliveryUnavailable: the cap fails closed, and the store's own text
// goes no further.
func (s *IdentityService) reserveSignInDelivery(ctx context.Context) error {
	c := s.signInControls
	if c == nil {
		return nil
	}
	within, err := s.repo.ReserveSignInDelivery(ctx, s.now().UTC().Truncate(time.Hour), c.hourlyCap)
	if err != nil || !within {
		return ErrOtpDeliveryUnavailable
	}
	return nil
}

// ReserveSignInDelivery implements IdentityRepository under the write lock:
// the check and the increment are one step, within this process. Hours
// before the previous one are dropped, so the map stays small.
func (m *MemoryIdentityRepository) ReserveSignInDelivery(_ context.Context, hourStart time.Time, limit int) (bool, error) {
	if limit < 1 {
		return false, nil
	}
	hour := hourStart.UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	for h := range m.signInDeliveries {
		if h.Before(hour.Add(-time.Hour)) {
			delete(m.signInDeliveries, h)
		}
	}
	if m.signInDeliveries[hour] >= limit {
		return false, nil
	}
	m.signInDeliveries[hour]++
	return true, nil
}
