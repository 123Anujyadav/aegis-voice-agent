package memory

import (
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Namespace isolates one consumer's memory from another's.
//
// The consumer assistant, the business receptionist, fraud intelligence and a
// future multi-agent runtime each get a namespace. Isolation is by construction
// rather than by convention: a query is issued against a namespace and cannot
// reach across, so a fraud agent cannot read the assistant's working memory by
// guessing a key.
type Namespace string

// The namespaces the platform ships.
const (
	// NamespaceAssistant is the consumer AI assistant.
	NamespaceAssistant Namespace = "assistant"
	// NamespaceReceptionist is the business AI receptionist.
	NamespaceReceptionist Namespace = "receptionist"
	// NamespaceFraud is fraud intelligence.
	NamespaceFraud Namespace = "fraud"
	// NamespaceTelephony is telephony intelligence.
	NamespaceTelephony Namespace = "telephony"
)

// String implements fmt.Stringer.
func (n Namespace) String() string { return string(n) }

// Config is the memory runtime's complete configuration.
type Config struct {
	// Policy governs retention, consent, promotion and audit.
	Policy Policy
	// Index tunes the index layer.
	Index IndexConfig
	// Compression tunes the compressor.
	Compression CompressionPolicy
	// SweepInterval is how often the scheduler runs maintenance.
	SweepInterval time.Duration
	// SweepBudget bounds one sweep's duration, so maintenance cannot starve
	// the request path.
	SweepBudget time.Duration
	// DefaultQueryLimit bounds an unbounded query.
	DefaultQueryLimit int
	// Namespaces to create at start.
	Namespaces []Namespace
}

// DefaultConfig returns a fully-populated configuration.
func DefaultConfig() Config {
	return Config{
		Policy:            DefaultPolicy(),
		Index:             DefaultIndexConfig(),
		Compression:       DefaultCompressionPolicy(),
		SweepInterval:     30 * time.Second,
		SweepBudget:       2 * time.Second,
		DefaultQueryLimit: 50,
		Namespaces: []Namespace{NamespaceAssistant, NamespaceReceptionist,
			NamespaceFraud, NamespaceTelephony},
	}
}

func (c Config) validate() []string {
	var p []string
	p = append(p, c.Policy.validate()...)
	p = append(p, c.Index.validate()...)
	p = append(p, c.Compression.validate()...)
	if c.SweepInterval <= 0 {
		p = append(p, "runtime: SweepInterval must be positive")
	}
	if c.SweepBudget <= 0 {
		p = append(p, "runtime: SweepBudget must be positive")
	}
	if c.SweepBudget >= c.SweepInterval {
		p = append(p, "runtime: SweepBudget must be below SweepInterval, "+
			"or one sweep runs into the next and maintenance never yields")
	}
	if c.DefaultQueryLimit <= 0 {
		p = append(p, "runtime: DefaultQueryLimit must be positive")
	}
	if len(c.Namespaces) == 0 {
		p = append(p, "runtime: at least one namespace is required")
	}
	sort.Strings(p)
	return p
}

// Bundle is one namespace's complete stack.
type Bundle struct {
	// Namespace names it.
	Namespace Namespace
	// Store is the transactional core.
	Store *Store
	// Retriever serves queries.
	Retriever Retriever
	// Compressor manages size.
	Compressor *Compressor
	// Context assembles context.
	Context *ContextBuilder
}

// Registry holds the namespaces a runtime serves.
//
// Registration happens at construction and the map is read-only thereafter, so
// the request path takes no lock to resolve a namespace. Late registration is
// supported for a multi-agent runtime that discovers agents at run time, and it
// is copy-on-write for the same reason Phase 10B's persona registry is: a
// shared map written in place would race every concurrent reader.
type Registry struct {
	mu      sync.RWMutex
	bundles map[Namespace]*Bundle
}

// Get returns a namespace's bundle.
func (r *Registry) Get(n Namespace) (*Bundle, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.bundles[n]
	return b, ok
}

// Namespaces returns every registered namespace, sorted.
func (r *Registry) Namespaces() []Namespace {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Namespace, 0, len(r.bundles))
	for n := range r.bundles {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// register adds a bundle, copy-on-write.
func (r *Registry) register(b *Bundle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := make(map[Namespace]*Bundle, len(r.bundles)+1)
	for k, v := range r.bundles {
		next[k] = v
	}
	next[b.Namespace] = b
	r.bundles = next
}

// Runtime is the memory engine's top-level object.
//
// It owns the registry, the scheduler and the coordinator, and nothing global.
// Two runtimes in one process share nothing — the same property Phases 10A and
// 10B have, and the reason the test suite is parallel-safe.
type Runtime struct {
	cfg     Config
	clock   rt.Clock
	logger  *slog.Logger
	metrics *Metrics
	events  *Dispatcher

	registry *Registry

	started  atomic.Bool
	stopping atomic.Bool
	stopped  chan struct{}
	wg       sync.WaitGroup

	sweeps atomic.Uint64
}

// Option customises a Runtime.
type Option func(*Runtime)

// WithClock replaces the clock.
func WithClock(c rt.Clock) Option {
	return func(r *Runtime) {
		if c != nil {
			r.clock = c
		}
	}
}

// WithLogger replaces the logger.
func WithLogger(l *slog.Logger) Option {
	return func(r *Runtime) {
		if l != nil {
			r.logger = l
		}
	}
}

// WithMetrics replaces the instrument set.
func WithMetrics(m *Metrics) Option {
	return func(r *Runtime) {
		if m != nil {
			r.metrics = m
		}
	}
}

// WithPublisher adds an event publisher.
func WithPublisher(p Publisher) Option {
	return func(r *Runtime) {
		if p != nil {
			r.events.Add(p)
		}
	}
}

// New constructs a memory runtime.
func New(cfg Config, opts ...Option) (*Runtime, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}

	r := &Runtime{
		cfg:      cfg,
		clock:    rt.SystemClock{},
		logger:   slog.Default(),
		metrics:  NewMetrics(),
		registry: &Registry{bundles: make(map[Namespace]*Bundle)},
		stopped:  make(chan struct{}),
	}
	r.events = NewDispatcher(r.metrics)
	for _, o := range opts {
		o(r)
	}
	// Reconstruct the dispatcher against the final metrics, then re-apply any
	// publishers an option added. Options may replace metrics after the
	// dispatcher was built, and a dispatcher counting into a discarded
	// instrument set is a silent observability hole.
	existing := r.events
	r.events = NewDispatcher(r.metrics)
	existing.mu.RLock()
	for _, p := range existing.publishers {
		r.events.Add(p)
	}
	existing.mu.RUnlock()

	for _, ns := range cfg.Namespaces {
		if err := r.createNamespace(ns); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// createNamespace builds and registers one namespace's stack.
func (r *Runtime) createNamespace(ns Namespace) error {
	store, err := NewStore(r.cfg.Policy, r.cfg.Index, r.clock, r.metrics, r.events)
	if err != nil {
		return err
	}
	retriever := NewRetriever(store, r.cfg.DefaultQueryLimit)
	compressor, err := NewCompressor(store, r.cfg.Compression, nil)
	if err != nil {
		return err
	}
	r.registry.register(&Bundle{
		Namespace: ns, Store: store, Retriever: retriever,
		Compressor: compressor,
		Context:    NewContextBuilder(store, retriever, DefaultTokenEstimator()),
	})
	return nil
}

// Registry returns the namespace registry.
func (r *Runtime) Registry() *Registry { return r.registry }

// Metrics returns the runtime's instrument set.
func (r *Runtime) Metrics() *Metrics { return r.metrics }

// Clock returns the runtime's clock.
func (r *Runtime) Clock() rt.Clock { return r.clock }

// Events returns the event dispatcher.
func (r *Runtime) Events() *Dispatcher { return r.events }

// Namespace returns a namespace's bundle, creating it if absent.
//
// Creating on demand supports a multi-agent runtime that discovers agents at
// run time. It is safe because a namespace is empty until written to, so a
// typo produces an empty namespace rather than a cross-agent read.
func (r *Runtime) Namespace(ns Namespace) (*Bundle, error) {
	if b, ok := r.registry.Get(ns); ok {
		return b, nil
	}
	if r.stopping.Load() {
		return nil, ErrClosed
	}
	if err := r.createNamespace(ns); err != nil {
		return nil, err
	}
	b, _ := r.registry.Get(ns)
	return b, nil
}

// Start begins the maintenance scheduler.
func (r *Runtime) Start() error {
	if r.started.Swap(true) {
		return invariant("INV-MEM-10", "runtime already started")
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := r.clock.NewTicker(r.cfg.SweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C():
				r.Sweep()
			case <-r.stopped:
				return
			}
		}
	}()
	r.logger.Info("memory runtime started",
		slog.Int("namespaces", len(r.registry.Namespaces())))
	return nil
}

// Stop halts maintenance and drains.
func (r *Runtime) Stop() error {
	if !r.started.Load() {
		return nil
	}
	if r.stopping.Swap(true) {
		return nil
	}
	close(r.stopped)
	r.wg.Wait()
	r.logger.Info("memory runtime stopped", slog.Uint64("sweeps", r.sweeps.Load()))
	return nil
}

// SweepReport describes one maintenance pass.
type SweepReport struct {
	// Namespace swept.
	Namespace Namespace
	// Examined counts records visited.
	Examined int
	// Expired, Promoted, Demoted count actions taken.
	Expired, Promoted, Demoted int
	// Duration is how long the pass took.
	Duration time.Duration
	// BudgetExceeded reports that the pass stopped early.
	BudgetExceeded bool
}

// Sweep runs maintenance across every namespace.
//
// It is exported so a test can drive it deterministically rather than waiting
// for a ticker — the same reason Phase 10A exposes its session sweep.
func (r *Runtime) Sweep() []SweepReport {
	r.sweeps.Add(1)
	reports := make([]SweepReport, 0, len(r.registry.Namespaces()))
	for _, ns := range r.registry.Namespaces() {
		b, ok := r.registry.Get(ns)
		if !ok {
			continue
		}
		reports = append(reports, r.sweepNamespace(ns, b))
	}
	return reports
}

// sweepNamespace runs maintenance on one namespace.
//
// It collects decisions under a read pass and applies them afterwards. Mutating
// during the walk would mean holding a shard lock while taking others, which is
// a deadlock waiting for the right interleaving.
func (r *Runtime) sweepNamespace(ns Namespace, b *Bundle) SweepReport {
	start := r.clock.Now()
	report := SweepReport{Namespace: ns}
	deadline := start.Add(r.cfg.SweepBudget)

	type action struct {
		key      Key
		decision TierDecision
	}
	var actions []action

	b.Store.index.Walk(func(rec *Record) bool {
		report.Examined++

		// The budget is checked during the walk as well as during application,
		// because a very large namespace can exhaust it before a single
		// decision is made — and a sweep that never finishes its read pass
		// would never expire anything at all.
		if r.clock.Now().After(deadline) {
			report.BudgetExceeded = true
			return false
		}

		now := r.clock.Now()
		if rec.State == StateActive && rec.Expired(now) {
			actions = append(actions, action{rec.Key, TierExpireNow})
			return true
		}
		if d := r.cfg.Policy.Promotion.Evaluate(rec, now); d != TierHold {
			actions = append(actions, action{rec.Key, d})
		}
		return true
	})

	// Deterministic application order, so two runs over the same state produce
	// the same sequence of events.
	sort.Slice(actions, func(i, j int) bool {
		return actions[i].key.String() < actions[j].key.String()
	})

	for _, a := range actions {
		if r.clock.Now().After(deadline) {
			report.BudgetExceeded = true
			break
		}
		switch a.decision {
		case TierExpireNow:
			if err := b.Store.Expire(a.key); err == nil {
				report.Expired++
			}
		case TierPromoteUp:
			if err := b.Store.Promote(a.key); err == nil {
				report.Promoted++
			}
		case TierDemoteDown:
			if err := b.Store.Demote(a.key); err == nil {
				report.Demoted++
			}
		}
	}

	report.Duration = r.clock.Since(start)
	r.metrics.SweepLatency.Observe(report.Duration.Seconds(), "maintenance")
	r.metrics.IndexPostings.Set(float64(b.Store.index.Stats().PostingLists))
	return report
}

// Sweeps returns how many maintenance passes have run.
func (r *Runtime) Sweeps() uint64 { return r.sweeps.Load() }

// ---------------------------------------------------------------------------
// Coordinator
// ---------------------------------------------------------------------------

// Coordinator runs operations that must span every namespace.
//
// Erasure is the one that matters. A subject's memories are spread across the
// assistant, the receptionist, fraud and telephony namespaces, and an erasure
// that reached only one of them would be a compliance failure that looks like a
// success. The coordinator exists so no caller has to remember the full list.
type Coordinator struct {
	runtime *Runtime
}

// Coordinator returns the runtime's coordinator.
func (r *Runtime) Coordinator() *Coordinator { return &Coordinator{runtime: r} }

// ErasureReport describes a cross-namespace erasure.
type ErasureReport struct {
	// Subject erased.
	Subject SubjectID
	// PerNamespace holds each namespace's result.
	PerNamespace map[Namespace]ForgetResult
	// TotalDeleted, TotalRedacted, TotalRetained aggregate the results.
	TotalDeleted, TotalRedacted, TotalRetained int
	// Complete reports that nothing survived.
	Complete bool
}

// Forget erases a subject from every namespace.
//
// It reports precisely what survived and where. A subject exercising an erasure
// right is entitled to know what was kept and on what basis — an erasure that
// silently retains is worse than one that refuses.
func (c *Coordinator) Forget(subject SubjectID, actor string) (ErasureReport, error) {
	report := ErasureReport{
		Subject:      subject,
		PerNamespace: make(map[Namespace]ForgetResult),
	}

	for _, ns := range c.runtime.registry.Namespaces() {
		b, ok := c.runtime.registry.Get(ns)
		if !ok {
			continue
		}
		res, err := b.Store.Forget(subject, actor)
		if err != nil {
			return report, err
		}
		report.PerNamespace[ns] = res
		report.TotalDeleted += res.Deleted
		report.TotalRedacted += res.Redacted
		report.TotalRetained += res.RetainedCount
	}
	report.Complete = report.TotalRedacted == 0 && report.TotalRetained == 0
	return report, nil
}

// Count returns the total records across every namespace.
func (c *Coordinator) Count() int {
	total := 0
	for _, ns := range c.runtime.registry.Namespaces() {
		if b, ok := c.runtime.registry.Get(ns); ok {
			total += b.Store.Count()
		}
	}
	return total
}

// Bytes returns the total payload held across every namespace.
func (c *Coordinator) Bytes() int64 {
	var total int64
	for _, ns := range c.runtime.registry.Namespaces() {
		if b, ok := c.runtime.registry.Get(ns); ok {
			total += b.Store.Bytes()
		}
	}
	return total
}

// SnapshotAll captures a subject across every namespace, returning the
// snapshot identifiers needed to roll the whole set back.
func (c *Coordinator) SnapshotAll(subject SubjectID, label string) map[Namespace]SnapshotID {
	out := make(map[Namespace]SnapshotID)
	for _, ns := range c.runtime.registry.Namespaces() {
		if b, ok := c.runtime.registry.Get(ns); ok {
			out[ns] = b.Store.Snapshot(subject, label)
		}
	}
	return out
}

// RollbackAll restores a subject across every namespace.
//
// Best-effort per namespace: a failure in one does not abandon the rest,
// because a partial rollback that stopped halfway is worse than one that
// completed everywhere it could and reported where it did not.
func (c *Coordinator) RollbackAll(ids map[Namespace]SnapshotID) error {
	var firstErr error
	namespaces := make([]Namespace, 0, len(ids))
	for ns := range ids {
		namespaces = append(namespaces, ns)
	}
	sort.Slice(namespaces, func(i, j int) bool { return namespaces[i] < namespaces[j] })

	for _, ns := range namespaces {
		b, ok := c.runtime.registry.Get(ns)
		if !ok {
			continue
		}
		if err := b.Store.Rollback(ids[ns]); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
