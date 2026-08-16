package voice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	governance "github.com/callscreen/callscreen-platform/packages/go/governance"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// ---------------------------------------------------------------------------
// Governed actions
// ---------------------------------------------------------------------------
//
// # The bypass this file exists to make impossible
//
// A voice agent that can run a tool is a voice agent that can move money,
// disclose a balance or send a message, on the strength of something a stranger
// said out loud. The rule is therefore absolute: nothing originating here
// executes until governance.Engine.Decide has allowed it.
//
// A rule enforced by remembering to call something is not enforced. Three
// structural facts make the bypass hard to introduce by accident:
//
//  1. THIS MODULE CANNOT REACH A TOOL RUNTIME AT ALL. packages/go/voice does
//     not depend on packages/go/toolruntime, and TestGoverned_VoiceCannotReachToolRuntime
//     reads go.mod to keep it that way. Execution happens behind [ToolInvoker],
//     which a service wires up.
//
//  2. THE PORT CANNOT BE CALLED WITHOUT A DECISION. [ToolRequest] carries an
//     [Authorization] whose every field is unexported, so no package outside
//     this one can construct a populated one — not even by copying the struct
//     literal. The only thing that mints one is [ToolGateway.Invoke], after
//     Decide has returned Allow.
//
//  3. AN UNAUTHORISED REQUEST IS REFUSED AT THE PORT. A zero Authorization is
//     invalid, and [ToolRequest.Validate] rejects it, so an invoker
//     implementation that is handed one has a way — and a reason — to say no.
//
// # There is exactly one policy engine, and it is not here
//
// Nothing in this file decides anything. It assembles a request, asks Phase
// 10E, and obeys the answer. A denial is not reinterpreted, an obligation is
// not discharged locally, and no result is cached — a second decision derived
// from a first would be a second policy engine wearing a hat.

// Errors this file returns.
var (
	// ErrObligationsUnmet reports a decision that neither allowed nor refused:
	// something must happen first.
	ErrObligationsUnmet = errors.New("voice: governance obligations are unmet")

	// ErrUnauthorized reports a tool request carrying no valid authorisation.
	ErrUnauthorized = errors.New("voice: action is not authorised")
)

// ---------------------------------------------------------------------------
// Authorisation
// ---------------------------------------------------------------------------

// Authorization is proof that governance permitted one specific action.
//
// # Every field is unexported, and that is the security property
//
// A caller in another package can declare a variable of this type, but the zero
// value is invalid and there is no exported field to set and no constructor to
// call. Forging one requires editing this package, which is a code review
// rather than an oversight.
//
// It is deliberately NOT reusable. It names the operation and resource it was
// granted for, and [ToolRequest.Validate] checks they match, so an
// authorisation obtained to read a balance cannot be presented to transfer one.
type Authorization struct {
	granted   bool
	operation string
	resource  string
	decision  governance.DecisionID
	policy    governance.PolicyID
	reason    string
	at        time.Time
}

// Valid reports whether this authorisation permits anything.
func (a Authorization) Valid() bool { return a.granted && a.operation != "" }

// Decision returns the identifier of the decision that granted it, for audit
// correlation.
func (a Authorization) Decision() governance.DecisionID { return a.decision }

// DecidedBy returns the policy that allowed the action.
func (a Authorization) DecidedBy() governance.PolicyID { return a.policy }

// Operation and Resource report what was authorised.
func (a Authorization) Operation() string { return a.operation }
func (a Authorization) Resource() string  { return a.resource }

// String renders the authorisation, content-free.
func (a Authorization) String() string {
	if !a.Valid() {
		return "unauthorised"
	}
	return fmt.Sprintf("authorised %s on %s by %s (%s)",
		a.operation, a.resource, a.policy, a.decision)
}

// ---------------------------------------------------------------------------
// What the voice layer may ask for
// ---------------------------------------------------------------------------

// ToolIntent describes an action the voice layer wants performed.
//
// # It carries no content, and there is nowhere to put any
//
// Operation, Resource and the attribute values all become part of a governance
// decision, and a decision is written to a durable audit store. What a caller
// said must never reach one. Attributes are bounded codes and
// classifications — a fingerprint, an amount band, a resource name — and
// TestGoverned_IntentCannotCarryContent checks the struct by reflection so a
// future field called Transcript fails a test rather than a review.
type ToolIntent struct {
	// Operation is what to do, in the caller's vocabulary: "lookup", "transfer".
	Operation string

	// Resource is what it is done to.
	Resource string

	// Reversibility is the single most useful input a policy has.
	Reversibility governance.Reversibility

	// Classification is the sensitivity of the data involved.
	Classification governance.Classification

	// Attributes are bounded values a policy may condition on. NEVER content.
	Attributes governance.Attrs

	// Risk carries signals other phases produced. Aggregated by the engine,
	// never computed here: this module runs no model.
	Risk governance.RiskAssessment
}

func (i ToolIntent) validate() []string {
	var problems []string
	if strings.TrimSpace(i.Operation) == "" {
		problems = append(problems, "Operation must be set")
	}
	if strings.TrimSpace(i.Resource) == "" {
		problems = append(problems, "Resource must be set")
	}
	return problems
}

// ToolRequest is an authorised intent, ready to execute.
//
// Only [ToolGateway.Invoke] produces one.
type ToolRequest struct {
	// Auth is the proof. Unexported fields throughout; see [Authorization].
	Auth Authorization

	// Intent is what was authorised.
	Intent ToolIntent

	// Session and Turn correlate the execution with the call.
	Session SessionID
	Turn    TurnID

	// Deadline bounds the execution.
	Deadline time.Time
}

// Validate rejects a request that is not authorised for exactly this intent.
//
// # An invoker should call this, and the honest ones will
//
// It is the third of the three structural defences and the only one that
// operates at runtime. An implementation that skips it still cannot fabricate
// an Authorization, so the worst it can do is execute a request this package
// built — but calling it turns "cannot be forged" into "is checked".
func (r ToolRequest) Validate() error {
	switch {
	case !r.Auth.Valid():
		return fmt.Errorf("%w: no governance decision accompanies this request",
			ErrUnauthorized)
	case r.Auth.operation != r.Intent.Operation:
		return fmt.Errorf("%w: authorised for %q but the request is %q",
			ErrUnauthorized, r.Auth.operation, r.Intent.Operation)
	case r.Auth.resource != r.Intent.Resource:
		return fmt.Errorf("%w: authorised for resource %q but the request is %q",
			ErrUnauthorized, r.Auth.resource, r.Intent.Resource)
	}
	return nil
}

// ToolResult is what an invoker reports back.
//
// Deliberately narrow. The voice layer needs to know whether the action
// happened and how to describe it, not what the tool returned: a tool's payload
// is the orchestration layer's business, and carrying it here would put
// arbitrary content one field away from an event.
type ToolResult struct {
	// Completed reports whether the action was carried out.
	Completed bool

	// Code is a bounded outcome code for logs and metrics.
	Code string

	// Duration is how long the execution took.
	Duration time.Duration
}

// ToolInvoker executes an authorised action.
//
// # Implemented outside this module, over packages/go/toolruntime
//
// A port rather than an import, because the dependency would be the bypass: a
// package that can reach an executor can call it, and no amount of
// documentation prevents the one call site that forgets to ask first.
type ToolInvoker interface {
	// InvokeTool performs an authorised action. Implementations must call
	// [ToolRequest.Validate] and refuse anything that fails it.
	InvokeTool(ctx context.Context, req ToolRequest) (ToolResult, error)
}

// ---------------------------------------------------------------------------
// The gateway
// ---------------------------------------------------------------------------

// DenialError reports an action governance refused.
//
// It carries the decision's own vocabulary — the reason code, the deciding
// policy and any obligations — so a caller can tell "not allowed at all" from
// "not allowed yet".
type DenialError struct {
	Operation   string
	Resource    string
	Outcome     governance.Outcome
	Reason      string
	DecidedBy   governance.PolicyID
	Decision    governance.DecisionID
	Obligations []governance.Obligation
}

func (e *DenialError) Error() string {
	s := fmt.Sprintf("voice: governance %s %s on %s (%s)",
		e.Outcome, e.Operation, e.Resource, e.Reason)
	if len(e.Obligations) > 0 {
		parts := make([]string, 0, len(e.Obligations))
		for _, o := range e.Obligations {
			parts = append(parts, o.String())
		}
		s += "; unmet: " + strings.Join(parts, ", ")
	}
	return s
}

// Unwrap lets errors.Is match the sentinel that fits the outcome.
//
// A refusal and an unmet obligation are different situations: the first ends
// the matter, the second says a precondition has not been satisfied and the
// same request may succeed later. Collapsing them would make a caller retry
// something that will never be allowed, or give up on something that only
// needed a confirmation.
func (e *DenialError) Unwrap() error {
	if len(e.Obligations) > 0 || e.Outcome.NeedsHuman() {
		return ErrObligationsUnmet
	}
	return ErrGovernanceDenied
}

// Obligation returns the first obligation of a kind, and whether there was one.
func (e *DenialError) Obligation(kind governance.ObligationKind) (governance.Obligation, bool) {
	for _, o := range e.Obligations {
		if o.Kind == kind {
			return o, true
		}
	}
	return governance.Obligation{}, false
}

// GatewayConfig configures the governed-action gateway.
type GatewayConfig struct {
	// Governor is Phase 10E. Required; there is no mode without one.
	Governor Governor

	// Invoker executes what governance allows. Optional: a deployment with no
	// tools still benefits from the gateway refusing everything cleanly.
	Invoker ToolInvoker

	// Session identifies the call, for audit correlation.
	Session SessionID

	// Actor, Subject, Org and Business locate the request. Policies are
	// selected on Org and Business; Session is carried for audit only.
	Actor    governance.ActorID
	Subject  governance.SubjectID
	Org      governance.OrgID
	Business governance.BusinessID

	// Roles the actor holds, expanded by the caller. This module does not
	// resolve roles: the role map is Identity's, and a second copy would drift.
	Roles []string

	// Metrics counts decisions. Nil builds a fresh set.
	Metrics *VoiceMetrics

	// Publisher receives denial events. Optional.
	Publisher EventPublisher

	// Clock stamps authorisations. Nil means the system clock.
	Clock rt.Clock
}

// ToolGateway is the only way a governed action leaves the voice layer.
type ToolGateway struct {
	cfg   GatewayConfig
	clock rt.Clock
	m     *VoiceMetrics

	allowed atomic.Uint64
	denied  atomic.Uint64
	unmet   atomic.Uint64
	invoked atomic.Uint64
}

// NewToolGateway builds a gateway.
func NewToolGateway(cfg GatewayConfig) (*ToolGateway, error) {
	var problems []string

	if cfg.Governor == nil {
		problems = append(problems, "Governor is required: every action leaving "+
			"the voice layer passes through governance.Engine.Decide, and a "+
			"gateway without one would be the bypass this type exists to prevent")
	}
	if !cfg.Session.Valid() {
		problems = append(problems, fmt.Sprintf(
			"session %q is not a valid label", cfg.Session))
	}
	if len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}

	clock := cfg.Clock
	if clock == nil {
		clock = rt.SystemClock{}
	}
	m := cfg.Metrics
	if m == nil {
		m = NewVoiceMetrics()
	}

	return &ToolGateway{cfg: cfg, clock: clock, m: m}, nil
}

// Invoke asks governance, and executes only if it said yes.
//
// # The order is the contract
//
// Decide, then — and only then — invoke. There is no fast path, no cache and no
// branch that reaches the invoker first. A denial returns before the invoker is
// touched, which TestGoverned_DenialPreventsToolExecution asserts by giving the
// gateway an invoker that fails the test if it is ever called.
func (g *ToolGateway) Invoke(ctx context.Context, turn TurnID, intent ToolIntent) (ToolResult, error) {
	if problems := intent.validate(); len(problems) > 0 {
		return ToolResult{}, &ConfigError{Problems: problems}
	}

	// Cancellation is checked BEFORE asking. A decision obtained for a call
	// that has already ended is an audit record for something that never
	// happened.
	if err := ctx.Err(); err != nil {
		return ToolResult{}, fmt.Errorf("%w: before the governance decision: %v",
			ErrSessionClosed, err)
	}

	decision := g.cfg.Governor.Decide(governance.Request{
		Action: governance.Action{
			Kind:           governance.ActionTool,
			Operation:      intent.Operation,
			Resource:       intent.Resource,
			Reversibility:  intent.Reversibility,
			Classification: intent.Classification,
			Attributes:     intent.Attributes,
		},
		Actor:    g.cfg.Actor,
		Subject:  g.cfg.Subject,
		Org:      g.cfg.Org,
		Business: g.cfg.Business,
		Session:  governance.SessionID(g.cfg.Session),
		Roles:    g.cfg.Roles,
		Risk:     intent.Risk,
	})

	g.m.GovernanceDecisions.Inc(decision.Outcome.String())

	// ANYTHING THAT IS NOT Allow STOPS HERE.
	//
	// Written as "not permitted" rather than as a list of refusing outcomes on
	// purpose: a new outcome added to Phase 10E must default to refusing, not to
	// executing, and a switch enumerating denials would do the opposite.
	if !decision.Outcome.Permits() {
		return g.refuse(ctx, turn, intent, decision)
	}

	// An allowed decision that still carries obligations has not been
	// discharged. This module cannot satisfy one — a confirmation is the
	// caller's to give, a consent basis is Identity's — so it reports them
	// intact rather than proceeding.
	if len(decision.Obligations) > 0 {
		return g.refuse(ctx, turn, intent, decision)
	}

	g.allowed.Add(1)

	auth := Authorization{
		granted:   true,
		operation: intent.Operation,
		resource:  intent.Resource,
		decision:  decision.ID,
		policy:    decision.DecidedBy,
		reason:    decision.Reason,
		at:        g.clock.Now(),
	}

	if g.cfg.Invoker == nil {
		return ToolResult{}, fmt.Errorf(
			"%w: governance allowed %s on %s but no tool invoker is wired",
			ErrProviderUnavailable, intent.Operation, intent.Resource)
	}

	req := ToolRequest{
		Auth: auth, Intent: intent,
		Session: g.cfg.Session, Turn: turn,
	}
	if deadline, ok := ctx.Deadline(); ok {
		req.Deadline = deadline
	}

	g.invoked.Add(1)
	result, err := g.cfg.Invoker.InvokeTool(ctx, req)
	if err != nil {
		// A tool that fails AFTER approval is a tool failure, not a governance
		// one. Conflating them would make an outage look like a policy problem
		// and send an operator to the wrong runbook.
		return result, fmt.Errorf("%w: %s on %s: %v",
			ErrProviderFailed, intent.Operation, intent.Resource, err)
	}
	return result, nil
}

// refuse records a decision that did not permit execution.
func (g *ToolGateway) refuse(
	_ context.Context, turn TurnID, intent ToolIntent, d governance.Decision,
) (ToolResult, error) {
	err := &DenialError{
		Operation: intent.Operation, Resource: intent.Resource,
		Outcome: d.Outcome, Reason: d.Reason,
		DecidedBy: d.DecidedBy, Decision: d.ID,
		// COPIED. The caller may keep this error indefinitely, and an
		// obligation slice shared with the engine's decision could be mutated
		// underneath it.
		Obligations: append([]governance.Obligation(nil), d.Obligations...),
	}

	if len(err.Obligations) > 0 || d.Outcome.NeedsHuman() {
		g.unmet.Add(1)
	} else {
		g.denied.Add(1)
	}

	// The event carries CODES ONLY: the outcome, the reason and the policy. Not
	// the attributes, not the resource contents, and nothing anybody said.
	if g.cfg.Publisher != nil {
		_ = g.cfg.Publisher.Publish(context.Background(), VoiceEvent{
			Type:    EventGovernanceDenied,
			Session: g.cfg.Session,
			Turn:    turn,
			Reason:  ReasonDenied,
			At:      g.clock.Now(),
		})
	}
	return ToolResult{}, err
}

// Allowed, Denied, ObligationsUnmet and Invoked report what the gateway has
// done. Exported so a test can assert the invoker was never reached without
// reaching into the type.
func (g *ToolGateway) Allowed() uint64          { return g.allowed.Load() }
func (g *ToolGateway) Denied() uint64           { return g.denied.Load() }
func (g *ToolGateway) ObligationsUnmet() uint64 { return g.unmet.Load() }
func (g *ToolGateway) Invoked() uint64          { return g.invoked.Load() }

// ---------------------------------------------------------------------------
// Memory
// ---------------------------------------------------------------------------
//
// # There is no memory writer here, and that is the design
//
// packages/go/voice does not depend on packages/go/memory, and this file adds
// no port to reach one. A voice turn holds the most sensitive material in the
// system — what a caller said, in their words — and a convenience writer would
// be the shortest path from a live transcript to a durable store.
//
// What a session should remember is a decision for the layer above, which owns
// both the conversation's meaning and the governance identity to ask about
// storing it. TestGoverned_VoiceHasNoMemoryWritePath keeps that true by
// checking the dependency list and this package's exported surface.
