package governance

import (
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Config configures an [Engine].
type Config struct {
	// Default is the outcome when no policy matches.
	//
	// VALIDATION REFUSES ANYTHING BUT DENY. The field exists so the choice is
	// visible in a configuration review rather than buried in a constant, not
	// so it can be changed. A safety engine whose failure mode is "allow" has
	// stopped being a safety engine at the exact moment it broke.
	Default Outcome

	// DefaultReason is the code attached to the default outcome.
	DefaultReason string

	// Validator checks that actions are well-formed before policy sees them.
	Validator Validator

	// Privacy declares retention, export and masking rules.
	Privacy PrivacyRules

	// Aggregator combines risk signals.
	Aggregator Aggregator

	// Thresholds turn risk into outcomes.
	Thresholds Thresholds

	// EscalationTimeouts bound each escalation kind.
	EscalationTimeouts map[EscalationKind]time.Duration

	// AutoEscalate raises an escalation automatically whenever a decision
	// needs a human.
	//
	// ON by default. A decision that says "a human must approve this" and does
	// not put it in front of a human is a decision that quietly becomes a
	// denial when the caller gives up.
	AutoEscalate bool

	// SweepInterval is how often consent, emergencies and escalations are
	// swept for expiry.
	SweepInterval time.Duration

	// RequireAuditor refuses to start without an auditor.
	//
	// ON by default. An engine that decides whether the platform may act, with
	// no record of what it decided, cannot answer "why did it do that" — which
	// is the one question governance exists to answer.
	RequireAuditor bool

	// FailClosedOnPanic decides what a panic inside evaluation produces.
	//
	// TRUE means deny; there is no option to make it allow. The field exists to
	// document the choice, and validation refuses false — see [Config.validate].
	FailClosedOnPanic bool
}

// DefaultConfig returns the platform baseline.
func DefaultConfig() Config {
	return Config{
		Default:            OutcomeDeny,
		DefaultReason:      "no_policy_matched",
		Validator:          DefaultValidator(),
		Privacy:            DefaultPrivacyRules(),
		Aggregator:         DefaultAggregator(),
		Thresholds:         DefaultThresholds(),
		EscalationTimeouts: DefaultEscalationTimeouts(),
		AutoEscalate:       true,
		SweepInterval:      30 * time.Second,
		RequireAuditor:     true,
		FailClosedOnPanic:  true,
	}
}

func (c Config) validate() []string {
	var problems []string

	if c.Default != OutcomeDeny {
		problems = append(problems, "config: Default must be deny. A governance engine "+
			"whose failure mode is 'allow' has stopped being a governance engine at "+
			"the exact moment it broke; if a deployment needs permissive behaviour it "+
			"writes an allow policy, which appears in every trace")
	}
	if c.DefaultReason == "" {
		problems = append(problems, "config: DefaultReason is required; every denial "+
			"must be explainable, including the default one")
	}
	if !c.FailClosedOnPanic {
		problems = append(problems, "config: FailClosedOnPanic must be true. A panic "+
			"inside evaluation means the engine does not know what the policies say, "+
			"and 'we do not know' resolves to deny")
	}
	if c.SweepInterval <= 0 {
		problems = append(problems, "config: SweepInterval must be positive")
	}
	problems = append(problems, c.Validator.validate()...)
	problems = append(problems, c.Privacy.validate()...)
	problems = append(problems, c.Aggregator.validate()...)
	return problems
}

// Option customises an engine.
type Option func(*options)

type options struct {
	clock     rt.Clock
	metrics   *Metrics
	logger    *slog.Logger
	publisher Publisher
	auditor   Auditor
}

// WithClock injects a clock.
func WithClock(c rt.Clock) Option { return func(o *options) { o.clock = c } }

// WithMetrics injects an instrument set.
func WithMetrics(m *Metrics) Option { return func(o *options) { o.metrics = m } }

// WithLogger injects a logger.
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.logger = l } }

// WithPublisher injects an event publisher.
func WithPublisher(p Publisher) Option { return func(o *options) { o.publisher = p } }

// WithAuditor injects an auditor.
func WithAuditor(a Auditor) Option { return func(o *options) { o.auditor = a } }

// Engine is the Safety, Policy & Governance Engine.
//
// ONE DOOR: [Engine.Decide]. Everything else on this type is registration,
// inspection or maintenance.
//
// It owns everything and shares nothing. Two engines in one process have
// separate registries, consent stores, escalation queues and metrics, which is
// what makes the test suite parallel-safe and "no global mutable state" a
// checkable property rather than an aspiration.
type Engine struct {
	cfg     Config
	clock   rt.Clock
	logger  *slog.Logger
	metrics *Metrics
	audit   Auditor
	events  *EventDispatcher

	registry  *PolicyRegistry
	consent   *ConsentRegistry
	emergency *EmergencyEngine
	human     *HumanRuntime
	evaluator Evaluator

	started atomic.Bool
	stopped atomic.Bool
	done    chan struct{}
	wg      sync.WaitGroup

	decisions atomic.Uint64
	sweeps    atomic.Uint64
	panics    atomic.Uint64
}

// New builds a governance engine.
func New(cfg Config, opts ...Option) (*Engine, error) {
	o := &options{
		clock:     rt.SystemClock{},
		publisher: NoopPublisher{},
		logger:    slog.Default(),
	}
	for _, opt := range opts {
		opt(o)
	}

	problems := cfg.validate()
	if cfg.RequireAuditor && o.auditor == nil {
		problems = append(problems, "config: RequireAuditor is set and no auditor was "+
			"supplied; an engine that decides whether the platform may act, with no "+
			"record of what it decided, cannot answer why it did that")
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, &ConfigError{Problems: problems}
	}

	metrics := o.metrics
	if metrics == nil {
		metrics = NewMetrics()
	}
	audit := o.auditor
	if audit == nil {
		audit = NoopAuditor{}
	}

	e := &Engine{
		cfg: cfg, clock: o.clock, logger: o.logger, metrics: metrics,
		audit: audit, done: make(chan struct{}),
	}
	e.events = NewEventDispatcher(metrics, o.publisher, o.clock.Now)
	e.registry = NewPolicyRegistry(o.clock, metrics, audit)
	e.consent = NewConsentRegistry(o.clock, metrics, audit)
	e.emergency = NewEmergencyEngine(o.clock, metrics, audit, e.registry)
	e.human = NewHumanRuntime(o.clock, metrics, audit, e.events)
	e.evaluator = Evaluator{
		Default:       cfg.Default,
		DefaultReason: cfg.DefaultReason,
		Thresholds:    cfg.Thresholds,
		Privacy:       cfg.Privacy,
		PrivacyPolicy: "<privacy>",
	}
	return e, nil
}

// Policies returns the policy registry.
func (e *Engine) Policies() *PolicyRegistry { return e.registry }

// Consent returns the consent registry.
func (e *Engine) Consent() *ConsentRegistry { return e.consent }

// Emergency returns the emergency engine.
func (e *Engine) Emergency() *EmergencyEngine { return e.emergency }

// Human returns the escalation runtime.
func (e *Engine) Human() *HumanRuntime { return e.human }

// Metrics returns the instrument set.
func (e *Engine) Metrics() *Metrics { return e.metrics }

// Events returns the event dispatcher.
func (e *Engine) Events() *EventDispatcher { return e.events }

// Clock returns the injected clock.
func (e *Engine) Clock() rt.Clock { return e.clock }

// Evaluator returns a copy of the pure evaluator.
//
// Exported so a caller can evaluate against a snapshot with no side effects at
// all — no metrics, no audit, no events — which is what a policy-authoring tool
// or a what-if console needs.
func (e *Engine) Evaluator() Evaluator { return e.evaluator }

// Coordinator returns the cross-cutting operations handle.
func (e *Engine) Coordinator() *Coordinator { return &Coordinator{engine: e} }

// Decisions returns how many decisions have been made.
//
// The bypass detector. Compare it against each subsystem's own action count:
// a subsystem that took more actions than it asked about has a bypass.
func (e *Engine) Decisions() uint64 { return e.decisions.Load() }

// Sweeps returns how many maintenance passes have run.
func (e *Engine) Sweeps() uint64 { return e.sweeps.Load() }

// Panics returns how many evaluations panicked and were failed closed.
func (e *Engine) Panics() uint64 { return e.panics.Load() }

// Start begins background maintenance.
//
// An engine starts once (INV-GOV-10). A second Start would run two sweepers
// over one consent registry, and two sweepers is not twice as good.
func (e *Engine) Start() error {
	if e.stopped.Load() {
		return ErrClosed
	}
	if !e.started.CompareAndSwap(false, true) {
		return invariant("INV-GOV-10", "engine already started")
	}

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		ticker := e.clock.NewTicker(e.cfg.SweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C():
				e.Sweep()
			case <-e.done:
				return
			}
		}
	}()
	return nil
}

// Stop halts maintenance.
//
// It does NOT resolve pending escalations. An escalation is a request to a
// human, and a deploy is not an answer from one — silently expiring the queue
// on shutdown would turn every rolling restart into a wave of refusals.
func (e *Engine) Stop() error {
	if !e.stopped.CompareAndSwap(false, true) {
		return nil
	}
	if e.started.Load() {
		close(e.done)
		e.wg.Wait()
	}
	return nil
}

// Decide is the one door.
//
// EVERY conversation decision, tool execution, memory write and external action
// passes through here. There is no second entry point, no fast path and no
// bypass flag; a subsystem that skips this does so by not calling it, which is
// visible in review and countable in production.
func (e *Engine) Decide(r Request) Decision {
	start := e.clock.Now()

	if e.stopped.Load() {
		return e.refuse(r, "engine_closed", start)
	}

	// ---- structural validation --------------------------------------------
	//
	// Before policy, because "this action is malformed" and "this action is
	// refused" are different facts. A caller with a typo in an operation name
	// should learn that, not conclude the platform is over-restrictive.
	if err := e.cfg.Validator.Check(r); err != nil {
		d := e.refuse(r, "malformed_action", start)
		d.Explanation = err.Error()
		return d
	}

	// ---- risk aggregation --------------------------------------------------
	//
	// Signals arrive on the request; the aggregate is recomputed here so a
	// caller cannot assert a level directly. A caller-asserted risk level would
	// let a compromised subsystem declare itself low-risk.
	if len(r.Risk.Signals) > 0 || r.Risk.Level != RiskLow {
		r.Risk = e.cfg.Aggregator.Aggregate(r.Risk.Signals...)
	}
	if e.metrics != nil {
		e.metrics.RiskLevels.Inc(r.Risk.Level.String())
		for _, s := range r.Risk.Signals {
			e.metrics.RiskSignals.Inc(s.Source, s.Level.String())
		}
	}

	// ---- evaluation --------------------------------------------------------
	snap := e.registry.Snapshot()
	d := e.evaluateSafely(snap, r, start)

	d.ID = NewDecisionID()
	d.Duration = e.clock.Since(start)

	// ---- consent obligations resolved against the registry -----------------
	//
	// A policy states WHICH consent is needed; only the registry knows whether
	// it is on file. Resolving here rather than in the evaluator keeps the
	// evaluator pure — it is the single reason consent lookup is not inside it.
	d = e.resolveConsent(r, d)

	// ---- attribution and side effects --------------------------------------
	if d.Scope == ScopeEmergency {
		if name := e.emergency.ActiveNameFor(d.DecidedBy); name != "" {
			d.Emergency = name
			e.emergency.NoteUse(name, d)
		}
	}

	e.decisions.Add(1)
	e.observe(d, r)
	e.recordDecision(d)
	e.events.Dispatch(eventFor(EventDecided, d))
	if d.Outcome == OutcomeDeny {
		e.events.Dispatch(eventFor(EventDeniedE, d))
	}

	// ---- automatic escalation ----------------------------------------------
	if e.cfg.AutoEscalate && d.Outcome.NeedsHuman() {
		if kind, ok := KindFor(d.Outcome); ok {
			timeout := e.cfg.EscalationTimeouts[kind]
			_, _ = e.human.Raise(d, kind, timeout)
		}
	}
	return d
}

// evaluateSafely runs the evaluator and converts a panic into a denial.
//
// A panic inside evaluation means the engine does not know what the policies
// say, and "we do not know" resolves to deny. There is no configuration that
// makes it resolve to allow — see [Config.FailClosedOnPanic].
func (e *Engine) evaluateSafely(snap *PolicySnapshot, r Request, start time.Time) (d Decision) {
	// Everything the recovery path needs is captured BEFORE evaluation begins.
	//
	// The first version built the fallback decision inside the deferred handler
	// and read snap.Version there. When the panic was a nil snapshot — which is
	// exactly the shape of bug this exists to survive — the handler dereferenced
	// the same nil and panicked again, taking the process down at the precise
	// moment it was supposed to fail closed. A recovery path that can fail the
	// same way as the thing it recovers from is not a recovery path.
	// See ENGINEERING_AUDIT F1.
	var version uint64
	if snap != nil {
		version = snap.Version
	}
	fingerprint := r.Fingerprint()

	defer func() {
		if rec := recover(); rec != nil {
			e.panics.Add(1)
			// The panic value is deliberately NOT put in the decision: it can
			// contain caller content, and a decision travels into logs, events
			// and a durable audit store.
			d = Decision{
				Outcome: OutcomeDeny, Reason: "evaluation_panic",
				Explanation:   "policy evaluation panicked; failing closed",
				DecidedBy:     "<engine>",
				PolicyVersion: version, RequestPrint: fingerprint,
				Risk: r.Risk, Correlation: r.Correlation, Session: r.Session,
				Actor: r.Actor, Subject: r.Subject, ActionLabel: r.Action.Label(),
				DecidedAt: start,
			}
		}
	}()
	return e.evaluator.Evaluate(snap, r, start)
}

// resolveConsent turns consent obligations into outcomes.
//
// An obligation naming a basis the subject has already consented to is
// SATISFIED and dropped. One naming a basis they have not is left in place and
// the outcome is raised to RequireConsent, carrying the registry's specific
// reason — not_found, expired, revoked or superseded — so a caller knows
// whether to ask, ask again, or stop asking.
func (e *Engine) resolveConsent(r Request, d Decision) Decision {
	if r.Subject == "" {
		return d
	}

	var (
		remaining []Obligation
		asked     bool
		unmet     bool
		reason    string
	)
	for _, o := range d.Obligations {
		if o.Kind != ObligationConsent {
			remaining = append(remaining, o)
			continue
		}
		asked = true
		check := e.consent.Check(r.Subject, o.Target)
		if check.Valid {
			continue // satisfied; nothing further is required of the caller
		}
		unmet = true
		if reason == "" {
			reason = "consent_" + check.Reason
		}
		o.Reason = check.Reason
		remaining = append(remaining, o)
	}
	d.Obligations = remaining

	switch {
	case unmet && d.Outcome.severity() < OutcomeRequireConsent.severity():
		// Something demanded a consent the subject has not given, and the
		// outcome was milder. Raise it.
		d.Outcome = OutcomeRequireConsent
		d.Reason = reason

	case asked && !unmet && d.Outcome == OutcomeRequireConsent:
		// THE GATE OPENS. Every consent a policy demanded is on file, so the
		// thing the policy was waiting for has happened.
		//
		// The first version only ever raised, which meant a require_consent
		// outcome was permanent: obtaining consent dropped the obligation and
		// left the outcome unchanged, so the gate could never open and the
		// caller looped forever. See ENGINEERING_AUDIT F4.
		//
		// It resolves to Allow rather than to whatever the next-most-severe
		// policy wanted, because the evaluator keeps one winner rather than a
		// ranking. A policy that wants something further AFTER consent states
		// it as its own rule, which is clearer than a runner-up nobody can see
		// in the trace. Recorded as a decision a reviewer might challenge in
		// ENGINEERING_AUDIT §5.
		d.Outcome = OutcomeAllow
		d.Reason = "consent_satisfied"
	}
	return d
}

// refuse builds a denial for a request that never reached policy.
func (e *Engine) refuse(r Request, reason string, start time.Time) Decision {
	d := Decision{
		ID: NewDecisionID(), Outcome: OutcomeDeny, Reason: reason,
		DecidedBy: "<engine>", PolicyVersion: e.registry.Version(),
		RequestPrint: r.Fingerprint(), Risk: r.Risk, Correlation: r.Correlation,
		Session: r.Session, Actor: r.Actor, Subject: r.Subject,
		ActionLabel: r.Action.Label(), DecidedAt: start,
		Duration: e.clock.Since(start),
	}
	e.decisions.Add(1)
	e.observe(d, r)
	e.recordDecision(d)
	e.events.Dispatch(eventFor(EventDeniedE, d))
	return d
}

func (e *Engine) observe(d Decision, r Request) {
	if e.metrics == nil {
		return
	}
	e.metrics.Decisions.Inc(d.Outcome.String(), r.Action.Label())
	e.metrics.EvalLatency.Observe(d.Duration.Seconds(), r.Action.Label())
	e.metrics.TraceLength.Observe(float64(len(d.Trace)))

	switch {
	case d.Outcome == OutcomeDeny:
		e.metrics.Denials.Inc(d.Reason, d.Scope.String())
	case d.Outcome.NeedsHuman():
		e.metrics.Escalations.Inc(d.Outcome.String(), d.Scope.String())
	}
	if d.DecidedBy == "<default>" {
		e.metrics.NoPolicy.Inc(r.Action.Label())
	}
	if d.DecidedBy == "<risk>" {
		e.metrics.Thresholds.Inc(d.Reason)
	}
}

func (e *Engine) recordDecision(d Decision) {
	if e.audit == nil {
		return
	}
	kind := AuditDecision
	switch {
	case d.Outcome == OutcomeDeny:
		kind = AuditDenied
	case d.Outcome.NeedsHuman():
		kind = AuditEscalated
	}

	details := map[string]string{"outcome": d.Outcome.String()}
	if d.Emergency != "" {
		details["emergency"] = d.Emergency
	}
	if len(d.Obligations) > 0 {
		kinds := make([]string, 0, len(d.Obligations))
		for _, o := range d.Obligations {
			kinds = append(kinds, string(o.Kind))
		}
		sort.Strings(kinds)
		details["obligations"] = joinComma(kinds)
	}

	err := e.audit.Record(AuditEntry{
		At: d.DecidedAt, Kind: kind, Decision: d.ID, Correlation: d.Correlation,
		Session: d.Session, Actor: d.Actor, Subject: d.Subject, Policy: d.DecidedBy,
		Scope: d.Scope, Outcome: d.Outcome, Reason: d.Reason,
		ActionLabel: d.ActionLabel, RequestPrint: d.RequestPrint,
		PolicyVersion: d.PolicyVersion, Risk: d.Risk.Level, Duration: d.Duration,
		Details: details,
	})
	if e.metrics == nil {
		return
	}
	if err != nil {
		e.metrics.AuditFailed.Inc(string(kind))
		return
	}
	e.metrics.AuditWritten.Inc(string(kind))
}

func joinComma(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

// Sweep runs one maintenance pass, returning what expired.
func (e *Engine) Sweep() SweepReport {
	e.sweeps.Add(1)
	rep := SweepReport{
		ConsentExpired:     e.consent.Sweep(),
		EmergenciesEnded:   e.emergency.Sweep(),
		EscalationsExpired: e.human.Sweep(),
		PoliciesExpired:    len(e.registry.Expired()),
		At:                 e.clock.Now(),
	}
	if e.metrics != nil {
		e.metrics.ConsentRecords.Set(float64(e.consent.Len()))
		e.metrics.EscalationDepth.Set(float64(e.human.Depth()))
	}
	return rep
}

// SweepReport is one maintenance pass's result.
type SweepReport struct {
	// ConsentExpired counts consent records that lapsed.
	ConsentExpired int
	// EmergenciesEnded counts emergencies that expired.
	EmergenciesEnded int
	// EscalationsExpired counts escalations nobody answered in time.
	EscalationsExpired int
	// PoliciesExpired counts policies past their effective window. REPORTED,
	// not removed: a lapsed policy is a fact an operator should see.
	PoliciesExpired int
	// At is the pass instant.
	At time.Time
}

// Empty reports that nothing expired.
func (s SweepReport) Empty() bool {
	return s.ConsentExpired == 0 && s.EmergenciesEnded == 0 &&
		s.EscalationsExpired == 0 && s.PoliciesExpired == 0
}

// Coordinator performs operations that span the whole engine.
type Coordinator struct{ engine *Engine }

// Replay recomputes a decision against the current policy set and reports drift.
//
// It needs the ORIGINAL REQUEST. A fingerprint identifies inputs without
// containing them, so replay from an audit record alone is impossible by
// construction — which is a privacy property rather than a gap, and is stated
// here rather than discovered by somebody expecting otherwise.
//
// What this answers: "would today's policies decide the same way?" That is the
// question a policy change review actually has.
func (c *Coordinator) Replay(original ReplayMetadata, r Request) (Decision, Drift) {
	snap := c.engine.registry.Snapshot()
	now := c.engine.clock.Now()

	// Evaluated through the pure evaluator, NOT through Decide: a replay must
	// not publish events, raise escalations or count as a decision. A replay
	// that had side effects would make reviewing a policy change a thing that
	// changes the system.
	d := c.engine.evaluator.Evaluate(snap, r, now)

	drift := Drift{
		Same:          d.Outcome == original.Outcome,
		WasOutcome:    original.Outcome,
		NowOutcome:    d.Outcome,
		WasPolicy:     original.DecidedBy,
		NowPolicy:     d.DecidedBy,
		PolicyChanged: snap.Digest != original.PolicyDigest,
		WasVersion:    original.PolicyVersion,
		NowVersion:    snap.Version,
	}
	return d, drift
}

// Conflicts returns policy pairs that could decide differently at equal
// precedence.
//
// Run at boot and after every policy load. A static check cannot prove two
// policies WILL disagree — that depends on the request — but it can prove they
// might, which is the thing an operator can fix before a decision goes the
// wrong way in production.
func (c *Coordinator) Conflicts() []*ConflictError {
	conflicts := ConflictsIn(c.engine.registry.Snapshot())
	if c.engine.metrics != nil {
		for _, cf := range conflicts {
			c.engine.metrics.Conflicts.Inc(cf.Scope.String())
		}
	}
	return conflicts
}

// ForgetSubject withdraws every consent a subject holds.
//
// The governance half of an erasure request. It does not delete data — this
// module stores none — it withdraws the lawful basis, which is what makes every
// other subsystem's retention obligation change. Phase 10C's coordinator does
// the deletion.
func (c *Coordinator) ForgetSubject(s SubjectID, reason string) []ConsentRecord {
	return c.engine.consent.RevokeAll(s, reason)
}

// Health summarises the engine for an operator.
type Health struct {
	// Policies is the registered policy count.
	Policies int
	// PolicyVersion is the current snapshot version.
	PolicyVersion uint64
	// PolicyDigest fingerprints the snapshot's decision-relevant content. Two
	// deployments with the same digest decide identically.
	PolicyDigest Fingerprint
	// Coverage reports how many policies could match each action kind. A kind
	// with zero is denied by default, which is safe and usually means somebody
	// has not finished writing the rules.
	Coverage map[ActionKind]int
	// Conflicts counts policy pairs that could disagree at equal precedence.
	Conflicts int
	// ConsentRecords is the live consent count.
	ConsentRecords int
	// ActiveEmergencies lists emergencies in force. Present in the health
	// report rather than buried in an audit query, because an emergency
	// nobody notices is an emergency nobody ends.
	ActiveEmergencies []string
	// Escalations summarises the human queue.
	Escalations EscalationStats
	// Decisions is the lifetime decision count — the bypass detector.
	Decisions uint64
	// Panics counts evaluations that failed closed.
	Panics uint64
	// AllowRate, DenyRate and EscalationRate summarise outcomes.
	AllowRate      float64
	DenyRate       float64
	EscalationRate float64
}

// Health builds a health report.
func (c *Coordinator) Health() Health {
	e := c.engine
	snap := e.registry.Snapshot()

	var emergencies []string
	for _, em := range e.emergency.Active() {
		emergencies = append(emergencies, em.Name)
	}

	return Health{
		Policies:          snap.Len(),
		PolicyVersion:     snap.Version,
		PolicyDigest:      snap.Digest,
		Coverage:          e.registry.Coverage(),
		Conflicts:         len(ConflictsIn(snap)),
		ConsentRecords:    e.consent.Len(),
		ActiveEmergencies: emergencies,
		Escalations:       e.human.Stats(),
		Decisions:         e.decisions.Load(),
		Panics:            e.panics.Load(),
		AllowRate:         e.metrics.AllowRate(),
		DenyRate:          e.metrics.DenyRate(),
		EscalationRate:    e.metrics.EscalationRate(),
	}
}
