package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/pgstore"
	"github.com/jackc/pgx/v5/pgconn"
)

// HousekeepingInterval is the time between iterations on each replica. Every
// replica runs the loop; the advisory lock lets one of them work per round.
// Five minutes keeps each batch small against the expiry horizons involved
// (5-minute OTPs, a 15-minute rate window, 24-hour sessions, a 1-hour margin).
const HousekeepingInterval = 5 * time.Minute

// HousekeepingUnhealthyAfter is how many consecutive failed iterations make
// the housekeeping readiness check report a failure.
const HousekeepingUnhealthyAfter = 3

// housekeepingLogEvery limits failure logging: the first failure, then every
// Nth consecutive one, then the recovery.
const housekeepingLogEvery = 10

type onceRunner interface {
	RunOnce(ctx context.Context) (pgstore.HousekeepingResult, error)
}

// HousekeepingRunner runs bounded identity housekeeping on an interval, under
// the platform lifecycle. A failed iteration is logged (by category only) and
// retried at the next tick; nothing a single failure does stops the loop.
//
// Health: a NON-CRITICAL readiness check. Housekeeping only bounds storage
// growth; no authentication, rate-limit, lockout, refresh or replay decision
// depends on it running, so its failure must not remove the instance from
// service. After HousekeepingUnhealthyAfter consecutive failures readiness
// reports "degraded" with a fixed message, so the failure is visible.
type HousekeepingRunner struct {
	hk       onceRunner
	interval time.Duration
	log      *slog.Logger

	mu       sync.Mutex
	failures int
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewHousekeepingRunner wraps hk; interval must be positive.
func NewHousekeepingRunner(hk onceRunner, interval time.Duration, log *slog.Logger) *HousekeepingRunner {
	return &HousekeepingRunner{hk: hk, interval: interval, log: log, done: make(chan struct{})}
}

// Name implements platform.Runner.
func (r *HousekeepingRunner) Name() string { return "identity-housekeeping" }

// Run iterates immediately and then every interval until ctx ends or Shutdown
// is called. It returns nil: housekeeping failing is not a reason to stop the
// service.
func (r *HousekeepingRunner) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()
	defer close(r.done)
	defer cancel()

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		r.iterate(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Shutdown cancels any iteration in progress — its transaction rolls back —
// and waits for the loop to exit, or for ctx's deadline.
func (r *HousekeepingRunner) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	cancel := r.cancel
	r.mu.Unlock()
	if cancel == nil {
		return nil // never started
	}
	cancel()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return errors.New("identity housekeeping did not stop before the shutdown deadline")
	}
}

func (r *HousekeepingRunner) iterate(ctx context.Context) {
	res, err := r.hk.RunOnce(ctx)
	if ctx.Err() != nil {
		return // shutting down: not a failure
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.failures++
		if r.failures == 1 || r.failures%housekeepingLogEvery == 0 {
			r.log.Warn("identity housekeeping iteration failed; will retry",
				slog.String("cause", failureCategory(err)), slog.Int("consecutive_failures", r.failures))
		}
		return
	}
	if r.failures > 0 {
		r.log.Info("identity housekeeping recovered", slog.Int("after_failures", r.failures))
	}
	r.failures = 0
	if res.Total() > 0 {
		r.log.Info("identity housekeeping removed expired records",
			slog.Int64("otp_challenges", res.Challenges), slog.Int64("otp_requests", res.Requests),
			slog.Int64("otp_failures", res.Failures), slog.Int64("otp_rate_states", res.RateStates),
			slog.Int64("sessions", res.Sessions), slog.Int64("proof_reservations", res.Proofs))
	}
}

// Check reports the runner unhealthy after repeated failed iterations; with
// Name it makes the runner a platform.Checker.
func (r *HousekeepingRunner) Check(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failures >= HousekeepingUnhealthyAfter {
		return errors.New("identity housekeeping failing")
	}
	return nil
}

// failureCategory is the only detail logged about a failure: a fixed word or a
// SQLSTATE, never error text, which could carry values from the database.
func failureCategory(err error) string {
	var pg *pgconn.PgError
	switch {
	case errors.Is(err, pgstore.ErrConnect):
		return "connection"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &pg):
		return "sqlstate " + pg.Code
	default:
		return "other"
	}
}
