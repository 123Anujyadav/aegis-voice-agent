package governance

import (
	"io"
	"log/slog"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Harness wires a governance engine for test with a controllable clock, a
// recording publisher and a recording auditor.
//
// Exported rather than test-only because every service embedding this engine
// needs it. A service testing its own reaction to a denial should not have to
// reimplement a fake clock, a fake publisher and a policy fixture — and a
// harness only this package can use pushes every consumer into real time,
// which is how a test suite becomes slow enough that somebody starts skipping
// it.
type Harness struct {
	// Clock is the controllable clock.
	Clock *rt.FakeClock
	// Metrics is the engine's instrument set.
	Metrics *Metrics
	// Engine is the system under test.
	Engine *Engine
	// Events records everything published.
	Events *RecordingPublisher
	// Audit records every audit entry.
	Audit *RecordingAuditor
}

// HarnessOption customises a harness.
type HarnessOption func(*harnessOptions)

type harnessOptions struct {
	cfg      *Config
	logger   *slog.Logger
	baseline bool
}

// WithHarnessConfig overrides the engine configuration.
func WithHarnessConfig(c Config) HarnessOption {
	return func(o *harnessOptions) { o.cfg = &c }
}

// WithHarnessLogger sets the logger. Defaults to discarding, so a passing test
// is silent and a failing one is readable.
func WithHarnessLogger(l *slog.Logger) HarnessOption {
	return func(o *harnessOptions) { o.logger = l }
}

// WithBaseline loads [BaselinePolicies] into the harness.
//
// OFF by default, so a test starts from an empty registry and therefore from
// total denial. That is the honest default: most tests want to state exactly
// the policies they are about, and a harness that silently loaded five would
// make every assertion depend on rules the test never mentioned.
func WithBaseline() HarnessOption {
	return func(o *harnessOptions) { o.baseline = true }
}

// NewHarness builds a governance engine wired for test.
func NewHarness(opts ...HarnessOption) (*Harness, error) {
	o := &harnessOptions{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, opt := range opts {
		opt(o)
	}

	clock := rt.NewFakeClock(rt.SystemClock{}.Now().Truncate(0))
	metrics := NewMetrics()
	events := NewRecordingPublisher()
	audit := NewRecordingAuditor(0)

	cfg := DefaultConfig()
	if o.cfg != nil {
		cfg = *o.cfg
	}

	e, err := New(cfg, WithClock(clock), WithMetrics(metrics),
		WithLogger(o.logger), WithPublisher(events), WithAuditor(audit))
	if err != nil {
		return nil, err
	}

	h := &Harness{Clock: clock, Metrics: metrics, Engine: e, Events: events, Audit: audit}
	if o.baseline {
		if err := e.Policies().RegisterAll(BaselinePolicies()...); err != nil {
			return nil, err
		}
	}
	return h, nil
}

// Register adds policies, failing loudly on an invalid one.
//
// Panics rather than returning an error because a test that registers an
// invalid policy has a bug in the test, and threading an error return through
// every setup line buries the assertion the test is actually about.
func (h *Harness) Register(policies ...Policy) *Harness {
	if err := h.Engine.Policies().RegisterAll(policies...); err != nil {
		panic("governance: harness registration failed: " + err.Error())
	}
	return h
}

// Grant records consent, failing loudly on an invalid record.
func (h *Harness) Grant(subject SubjectID, basis string, ttl time.Duration) ConsentRecord {
	rec := ConsentRecord{
		Subject: subject, Basis: basis, TermsVersion: h.Engine.Consent().TermsVersion(basis),
		Method: "test", Evidence: FingerprintString("test-evidence"),
	}
	if ttl > 0 {
		rec.ExpiresAt = h.Clock.Now().Add(ttl)
	}
	out, err := h.Engine.Consent().Grant(rec)
	if err != nil {
		panic("governance: harness grant failed: " + err.Error())
	}
	return out
}

// Ask builds and decides a request in one call, the common test shape.
func (h *Harness) Ask(a Action, actor ActorID, subject SubjectID) Decision {
	return h.Engine.Decide(Request{
		Action: a, Actor: actor, Subject: subject,
		Correlation: NewCorrelationID(), Session: "sess-test",
	})
}

// ---------------------------------------------------------------------------
// Action builders
// ---------------------------------------------------------------------------

// ReadAction builds a non-mutating memory read, the most common shape.
func ReadAction(resource string) Action {
	return Action{
		Kind: ActionMemory, Operation: "read", Resource: resource,
		Reversibility: ReversibleNone, Classification: ClassInternal,
	}
}

// WriteAction builds a reversible memory write of personal data.
func WriteAction(resource string) Action {
	return Action{
		Kind: ActionMemory, Operation: "write", Resource: resource,
		Reversibility: ReversibleFully, Classification: ClassPersonal,
	}
}

// ToolAction builds a tool execution.
func ToolAction(capability string, rev Reversibility) Action {
	return Action{
		Kind: ActionTool, Operation: "invoke", Resource: capability,
		Reversibility: rev, Classification: ClassInternal,
		Attributes: Attrs{"capability": Str(capability)},
	}
}

// NotifyAction builds a notification.
func NotifyAction(channel string) Action {
	return Action{
		Kind: ActionNotification, Operation: "send", Resource: channel,
		Reversibility: ReversibleNever, Classification: ClassPersonal,
		Attributes: Attrs{"channel": Str(channel)},
	}
}

// ExternalAction builds an action leaving the platform boundary.
func ExternalAction(destination string, class Classification) Action {
	return Action{
		Kind: ActionExternal, Operation: "post", Resource: destination,
		Reversibility: ReversibleNever, Classification: class,
		Attributes: Attrs{"destination": Str(destination)},
	}
}

// ---------------------------------------------------------------------------
// Policy builders
// ---------------------------------------------------------------------------

// AllowPolicy builds a policy that allows everything it matches.
//
// A test fixture, and named so nobody mistakes it for something to deploy: a
// blanket allow is exactly what the baseline is careful not to be.
func AllowPolicy(id PolicyID, scope Scope, priority int, m Match) Policy {
	return Policy{
		ID: id, Version: 1, Scope: scope, Priority: priority,
		Title: "test allow", Owner: "test", Enabled: true, Match: m,
		Rules: []Rule{{Name: "allow", Then: OutcomeAllow, Reason: "test_allow",
			Explanation: "test fixture allows this"}},
		Default: OutcomeAllow, DefaultReason: "test_allow_default",
	}
}

// DenyPolicy builds a policy that denies everything it matches.
func DenyPolicy(id PolicyID, scope Scope, priority int, m Match) Policy {
	return Policy{
		ID: id, Version: 1, Scope: scope, Priority: priority,
		Title: "test deny", Owner: "test", Enabled: true, Match: m,
		Rules: []Rule{{Name: "deny", Then: OutcomeDeny, Reason: "test_deny",
			Explanation: "test fixture denies this"}},
		Default: OutcomeDeny, DefaultReason: "test_deny_default",
	}
}

// OutcomePolicy builds a policy producing one outcome, for the middle eight.
func OutcomePolicy(id PolicyID, scope Scope, priority int, m Match, o Outcome, reason string) Policy {
	rule := Rule{Name: "rule", Then: o, Reason: reason,
		Explanation: "test fixture produces " + o.String()}
	if o == OutcomeRetryLater {
		rule.RetryAfter = time.Minute
	}
	return Policy{
		ID: id, Version: 1, Scope: scope, Priority: priority,
		Title: "test " + o.String(), Owner: "test", Enabled: true, Match: m,
		Rules: []Rule{rule}, Default: OutcomeDeny, DefaultReason: "test_default",
	}
}

// ConsentPolicy builds a policy requiring a named consent basis.
func ConsentPolicy(id PolicyID, scope Scope, priority int, m Match, basis string) Policy {
	return Policy{
		ID: id, Version: 1, Scope: scope, Priority: priority,
		Title: "test consent", Owner: "test", Enabled: true, Match: m,
		Rules: []Rule{{
			Name: "needs-consent", Then: OutcomeRequireConsent, Reason: "needs_consent",
			Explanation: "test fixture requires consent to " + basis,
			Obligations: []Obligation{{Kind: ObligationConsent, Target: basis,
				Reason: "test", Policy: id}},
		}},
		Default: OutcomeAllow, DefaultReason: "test_consent_default",
	}
}

// TestEmergency builds a valid emergency for tests.
func TestEmergency(name string, expires time.Time, policies ...Policy) Emergency {
	return Emergency{
		Name: name, Policies: policies, ExpiresAt: expires,
		AuthorisedBy: "test-oncall", Reason: "test emergency", Ticket: "TEST-1",
		Scopes: []Scope{ScopeGlobal, ScopeOrganization, ScopeBusiness},
	}
}

// EmergencyAllowPolicy builds an emergency-scope override that permits what it
// matches.
func EmergencyAllowPolicy(id PolicyID, m Match) Policy {
	return Policy{
		ID: id, Version: 1, Scope: ScopeEmergency, Priority: 500,
		Title: "emergency override", Owner: "test-oncall", Enabled: true,
		Match: m, Override: true,
		Rules: []Rule{{Name: "override-allow", Then: OutcomeAllow,
			Reason: "emergency_override", Explanation: "emergency override in effect"}},
		Default: OutcomeAllow, DefaultReason: "emergency_default",
	}
}

// SignalSet builds risk signals for tests.
func SignalSet(level RiskLevel, sources ...string) []Signal {
	out := make([]Signal, 0, len(sources))
	for _, s := range sources {
		out = append(out, Signal{Source: s, Kind: "test", Level: level, Weight: 1})
	}
	return out
}
