package telephony

import (
	"fmt"
	"time"
)

// Config is the runtime's tuning.
//
// Every field is a duration or a count, every one is validated, and there is no
// environment or file access anywhere in this package — a service supplies
// these values. That is what keeps the runtime testable offline and keeps
// secrets out of a module that has no business holding any.
type Config struct {
	// MaxConcurrentCalls bounds live sessions. Zero is refused: an unbounded
	// telephony runtime is one that discovers its limit during a call storm.
	MaxConcurrentCalls int

	// MaxCallsPerProvider bounds live sessions per provider, so one carrier's
	// outage-driven retry storm cannot consume the whole runtime's capacity.
	// Zero means no per-provider limit.
	MaxCallsPerProvider int

	// AdmissionHighWater is the fraction of MaxConcurrentCalls above which new
	// calls are shed. Below 1.0 so the runtime keeps headroom for calls already
	// in progress to transition — a runtime at exactly 100% cannot accept the
	// transfer leg of a call it is already carrying.
	AdmissionHighWater float64

	// Lifecycle deadlines. Each bounds how long a call may sit in a state
	// before the sweeper moves it to Timeout.
	SetupTimeout      time.Duration
	RingTimeout       time.Duration
	ScreenTimeout     time.Duration
	ConnectTimeout    time.Duration
	TeardownTimeout   time.Duration
	EscalationTimeout time.Duration
	RecoveryTimeout   time.Duration

	// SweepInterval is how often the timeout sweeper runs.
	SweepInterval time.Duration

	// ProviderTimeout bounds one provider call. A provider that ignores
	// cancellation holds a goroutine per call, so this is the last line of
	// defence against a hung carrier SDK — and the reason [Provider] documents
	// that implementations must honour context.
	ProviderTimeout time.Duration

	// PublishTimeout bounds one event publish. Short: a publish that takes
	// longer than this is a broker in trouble, and waiting on it delays the
	// transition that produced it.
	PublishTimeout time.Duration

	// SnapshotInterval is how often live sessions are persisted for recovery.
	// Zero disables periodic snapshots, leaving only the shutdown snapshot.
	SnapshotInterval time.Duration

	// DrainTimeout bounds graceful shutdown. After it, remaining calls are
	// snapshotted and abandoned rather than waited on indefinitely.
	DrainTimeout time.Duration
}

// DefaultConfig returns the platform baseline.
//
// The durations are chosen from what a caller experiences, not from what is
// convenient to implement:
//
//   - RingTimeout 45s — longer than a mobile carrier's own alerting timeout, so
//     the carrier gives up first and we record its reason rather than ours.
//   - ScreenTimeout 20s — a caller listening to a screening prompt for longer
//     than this has concluded the line is dead.
//   - ConnectTimeout 10s — accepted-but-not-connected is a carrier fault, and
//     ten seconds is generous for one.
//   - TeardownTimeout 15s — teardown that takes longer has failed.
func DefaultConfig() Config {
	return Config{
		MaxConcurrentCalls:  10_000,
		MaxCallsPerProvider: 5_000,
		AdmissionHighWater:  0.95,

		SetupTimeout:      5 * time.Second,
		RingTimeout:       45 * time.Second,
		ScreenTimeout:     20 * time.Second,
		ConnectTimeout:    10 * time.Second,
		TeardownTimeout:   15 * time.Second,
		EscalationTimeout: 5 * time.Minute,
		RecoveryTimeout:   30 * time.Second,

		SweepInterval:    time.Second,
		ProviderTimeout:  5 * time.Second,
		PublishTimeout:   2 * time.Second,
		SnapshotInterval: 10 * time.Second,
		DrainTimeout:     30 * time.Second,
	}
}

func (c Config) validate() []string {
	var problems []string

	if c.MaxConcurrentCalls <= 0 {
		problems = append(problems, "config: MaxConcurrentCalls must be positive; "+
			"an unbounded telephony runtime discovers its limit during a call storm")
	}
	if c.MaxCallsPerProvider < 0 {
		problems = append(problems, "config: MaxCallsPerProvider must not be negative")
	}
	if c.MaxCallsPerProvider > c.MaxConcurrentCalls && c.MaxConcurrentCalls > 0 {
		problems = append(problems, fmt.Sprintf(
			"config: MaxCallsPerProvider (%d) exceeds MaxConcurrentCalls (%d), so it "+
				"can never bind", c.MaxCallsPerProvider, c.MaxConcurrentCalls))
	}
	if c.AdmissionHighWater <= 0 || c.AdmissionHighWater > 1 {
		problems = append(problems, fmt.Sprintf(
			"config: AdmissionHighWater %g must be in (0, 1]", c.AdmissionHighWater))
	}

	durations := []struct {
		name  string
		value time.Duration
	}{
		{"SetupTimeout", c.SetupTimeout},
		{"RingTimeout", c.RingTimeout},
		{"ScreenTimeout", c.ScreenTimeout},
		{"ConnectTimeout", c.ConnectTimeout},
		{"TeardownTimeout", c.TeardownTimeout},
		{"EscalationTimeout", c.EscalationTimeout},
		{"RecoveryTimeout", c.RecoveryTimeout},
		{"SweepInterval", c.SweepInterval},
		{"ProviderTimeout", c.ProviderTimeout},
		{"PublishTimeout", c.PublishTimeout},
		{"DrainTimeout", c.DrainTimeout},
	}
	for _, d := range durations {
		if d.value <= 0 {
			problems = append(problems, fmt.Sprintf("config: %s must be positive", d.name))
		}
	}
	if c.SnapshotInterval < 0 {
		problems = append(problems, "config: SnapshotInterval must not be negative")
	}

	// The sweeper cannot enforce a deadline shorter than its own period. A
	// 500 ms ScreenTimeout with a 1 s sweep fires at one second, and an
	// operator who set 500 ms would reasonably believe otherwise.
	if c.SweepInterval > 0 {
		shortest := c.SetupTimeout
		name := "SetupTimeout"
		for _, d := range durations[:7] {
			if d.value > 0 && d.value < shortest {
				shortest, name = d.value, d.name
			}
		}
		if shortest > 0 && c.SweepInterval > shortest {
			problems = append(problems, fmt.Sprintf(
				"config: SweepInterval (%s) exceeds %s (%s), so that deadline cannot "+
					"be enforced closer than one sweep period", c.SweepInterval, name, shortest))
		}
	}

	return problems
}

// Capacity returns the admission ceiling after the high-water fraction.
func (c Config) Capacity() int {
	n := int(float64(c.MaxConcurrentCalls) * c.AdmissionHighWater)
	if n < 1 {
		return 1
	}
	return n
}
