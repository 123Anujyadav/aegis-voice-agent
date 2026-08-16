package telephony

import (
	"fmt"
	"sort"
	"sync"
)

// AdmissionDecision is the outcome of an admission request.
type AdmissionDecision struct {
	// Admitted reports whether the call may proceed.
	Admitted bool
	// Reason is a bounded code explaining a refusal, empty when admitted.
	Reason string
	// Live and Capacity are the counts the decision was made against, so a log
	// line explains itself without a second query.
	Live     int
	Capacity int
}

// String renders the decision.
func (d AdmissionDecision) String() string {
	if d.Admitted {
		return fmt.Sprintf("admitted (%d/%d)", d.Live, d.Capacity)
	}
	return fmt.Sprintf("shed: %s (%d/%d)", d.Reason, d.Live, d.Capacity)
}

// CallScheduler decides whether a call may be admitted.
//
// # It sheds, it does not queue
//
// The single most important decision in this file, and it is worth defending.
//
// A queue is the obvious response to overload and it is wrong for telephony. A
// caller waiting in a queue is a caller listening to silence, and by the time
// the runtime reaches them the carrier has usually given up — so the work is
// done twice and satisfies nobody. Worse, a queue converts an overload into a
// latency problem that spreads: calls that WOULD have been served promptly now
// wait behind calls that will be abandoned.
//
// Shedding is honest. A refused call is refused in milliseconds, the carrier is
// told immediately and can route elsewhere, and the calls already in progress
// keep their resources. This is the same argument the Phase 10A scheduler makes
// about generation requests, and it applies more sharply here because the far
// end is a person holding a phone.
//
// # Per-provider limits exist for outage storms
//
// A carrier having a bad day retries aggressively. Without a per-provider
// ceiling, one carrier's retry storm consumes the whole runtime's capacity and
// takes down calls on every other carrier with it.
type CallScheduler struct {
	cfg      Config
	registry *CallRegistry
	metrics  *RuntimeMetrics

	mu sync.RWMutex
	// perProvider counts live calls per provider. Maintained incrementally
	// rather than derived by walking the registry, for the same reason the
	// registry keeps its own count: this is read on every admission.
	perProvider map[ProviderID]int
}

// NewCallScheduler builds a scheduler.
func NewCallScheduler(cfg Config, reg *CallRegistry, m *RuntimeMetrics) *CallScheduler {
	return &CallScheduler{
		cfg: cfg, registry: reg, metrics: m,
		perProvider: make(map[ProviderID]int),
	}
}

// Admit decides whether a call may start, and reserves a slot if so.
//
// # Reservation and decision are one atomic step
//
// Checking capacity and then reserving in two steps lets N goroutines all
// observe capacity for one slot and all take it. Under a call storm — which is
// exactly when this matters — that over-admits by however many goroutines
// arrived together. The lock spans both.
func (s *CallScheduler) Admit(provider ProviderID) AdmissionDecision {
	capacity := s.cfg.Capacity()

	s.mu.Lock()
	live := s.registry.Len()

	decision := AdmissionDecision{Live: live, Capacity: capacity}

	switch {
	case live >= capacity:
		decision.Reason = "capacity_exceeded"
	case s.cfg.MaxCallsPerProvider > 0 &&
		s.perProvider[provider] >= s.cfg.MaxCallsPerProvider:
		decision.Reason = "provider_capacity_exceeded"
	default:
		decision.Admitted = true
		s.perProvider[provider]++
	}
	s.mu.Unlock()

	if decision.Admitted {
		s.metrics.Admitted.Inc(string(provider))
	} else {
		s.metrics.Shed.Inc(string(provider), decision.Reason)
	}
	return decision
}

// Release returns a provider's slot.
//
// Must be called exactly once per admitted call. A missed release leaks a slot
// and the runtime slowly loses capacity — which is why the runtime calls this
// from one place, on the terminal path, rather than from every site that ends
// a call.
func (s *CallScheduler) Release(provider ProviderID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.perProvider[provider]; n > 0 {
		s.perProvider[provider] = n - 1
		if s.perProvider[provider] == 0 {
			// Deleted rather than left at zero, so a runtime that has served a
			// thousand providers over its life does not hold a thousand map
			// entries forever.
			delete(s.perProvider, provider)
		}
	}
}

// Live returns the live count for a provider.
func (s *CallScheduler) Live(provider ProviderID) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.perProvider[provider]
}

// Providers returns the providers with live calls, sorted.
func (s *CallScheduler) Providers() []ProviderID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ProviderID, 0, len(s.perProvider))
	for p := range s.perProvider {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Utilisation returns live calls over capacity, in [0, ∞).
//
// May exceed 1: capacity is the ADMISSION ceiling, and calls already in flight
// are not evicted when the ceiling is reached. A utilisation above 1 means the
// high-water mark is doing its job.
func (s *CallScheduler) Utilisation() float64 {
	capacity := s.cfg.Capacity()
	if capacity == 0 {
		return 0
	}
	return float64(s.registry.Len()) / float64(capacity)
}

// ---------------------------------------------------------------------------
// Provider registry
// ---------------------------------------------------------------------------

// providerRegistry holds the registered provider adapters.
//
// Unexported: providers are registered through [TelephonyRuntime.RegisterProvider]
// at start-up, and a second registry surface would let a caller swap a provider
// under a live call.
type providerRegistry struct {
	mu        sync.RWMutex
	providers map[ProviderID]Provider
}

func newProviderRegistry() *providerRegistry {
	return &providerRegistry{providers: make(map[ProviderID]Provider)}
}

func (r *providerRegistry) register(p Provider) error {
	if p == nil {
		return invariant("INV-TEL-5", "cannot register a nil provider")
	}
	id := p.ID()
	if !id.Valid() {
		return invariant("INV-TEL-5",
			"provider identifier %q must be lowercase alphanumerics, hyphen or "+
				"underscore — it becomes a metric label", id)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[id]; exists {
		// Refused rather than replaced. Replacing a provider with live calls
		// leaves those calls holding an adapter nothing can reach.
		return fmt.Errorf("telephony: provider %s is already registered", id)
	}
	r.providers[id] = p
	return nil
}

func (r *providerRegistry) get(id ProviderID) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrProviderNotRegistered, id)
	}
	return p, nil
}

func (r *providerRegistry) ids() []ProviderID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ProviderID, 0, len(r.providers))
	for id := range r.providers {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (r *providerRegistry) len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.providers)
}
