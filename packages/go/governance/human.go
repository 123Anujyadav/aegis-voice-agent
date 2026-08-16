package governance

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// EscalationKind classifies what kind of human involvement is needed.
//
// FIVE KINDS, because "a human should look at this" is five different requests
// with five different response times and five different people.
type EscalationKind uint8

// The escalation kinds.
const (
	// EscalationConfirmation asks the END USER to confirm. Seconds, in-call.
	EscalationConfirmation EscalationKind = iota

	// EscalationApproval asks any authorised operator. Minutes.
	EscalationApproval

	// EscalationSupervisor asks a supervisor specifically. Distinct from
	// approval because "someone looked" and "someone accountable looked" are
	// different controls, and conflating them means the second never happens.
	EscalationSupervisor

	// EscalationTakeover asks a human to take the call over entirely. The
	// terminal form: the AI stops and a person continues.
	EscalationTakeover

	// EscalationBusiness routes to the business the call is about, rather than
	// to platform staff. Hours, and frequently the correct answer for a
	// decision only the business can make.
	EscalationBusiness
)

// String renders the kind. Used as a metric label.
func (k EscalationKind) String() string {
	switch k {
	case EscalationApproval:
		return "approval"
	case EscalationSupervisor:
		return "supervisor"
	case EscalationTakeover:
		return "takeover"
	case EscalationBusiness:
		return "business"
	default:
		return "confirmation"
	}
}

// KindFor maps an outcome to the escalation it implies.
func KindFor(o Outcome) (EscalationKind, bool) {
	switch o {
	case OutcomeRequireConfirmation:
		return EscalationConfirmation, true
	case OutcomeRequireHuman, OutcomeEscalate:
		return EscalationApproval, true
	case OutcomeRequireSupervisor:
		return EscalationSupervisor, true
	default:
		return EscalationConfirmation, false
	}
}

// Resolution is how an escalation ended.
type Resolution uint8

// The resolutions.
const (
	// ResolutionPending means nobody has acted yet.
	ResolutionPending Resolution = iota
	// ResolutionApproved permits the action.
	ResolutionApproved
	// ResolutionRejected refuses it.
	ResolutionRejected
	// ResolutionTakenOver means a human took the interaction over; the
	// original action does not proceed as an AI action.
	ResolutionTakenOver
	// ResolutionExpired means nobody acted in time.
	//
	// EXPIRY RESOLVES TO A REFUSAL, not to an allowance. An escalation that
	// times out is one nobody approved, and treating silence as consent is how
	// an approval gate becomes a delay.
	ResolutionExpired
)

// String renders the resolution. Used as a metric label.
func (r Resolution) String() string {
	switch r {
	case ResolutionApproved:
		return "approved"
	case ResolutionRejected:
		return "rejected"
	case ResolutionTakenOver:
		return "taken_over"
	case ResolutionExpired:
		return "expired"
	default:
		return "pending"
	}
}

// Permits reports whether the resolution lets the action proceed.
func (r Resolution) Permits() bool { return r == ResolutionApproved }

// Escalation is one pending request for human involvement.
type Escalation struct {
	// ID identifies it.
	ID DecisionID

	// Kind is what is being asked for.
	Kind EscalationKind

	// Decision is the governance decision that raised it.
	Decision DecisionID

	// Correlation, Session, Actor and Subject locate it.
	Correlation CorrelationID
	Session     SessionID
	Actor       ActorID
	Subject     SubjectID

	// ActionLabel is the bounded description of what is waiting. NOT the full
	// action: an escalation queue is read by humans and rendered in consoles,
	// and a resource identifier in it is a resource identifier in a screenshot.
	ActionLabel string

	// Reason is why it escalated.
	Reason string

	// Policy names the policy that demanded it, so the person deciding knows
	// which rule they are being asked to satisfy.
	Policy PolicyID

	// Risk is the assessment at the time.
	Risk RiskLevel

	// RaisedAt and Deadline bound it.
	RaisedAt time.Time
	Deadline time.Time

	// Resolution is how it ended.
	Resolution Resolution

	// ResolvedBy names the human. Required to resolve: an anonymous approval
	// is an approval nobody can be asked about.
	ResolvedBy string

	// ResolvedAt is when.
	ResolvedAt time.Time

	// Note is the human's free-text explanation. Retained verbatim, because an
	// approval review reads reasons and a bounded code cannot carry "spoke to
	// the customer, they confirmed".
	Note string
}

// Pending reports whether the escalation is still waiting.
func (e Escalation) Pending() bool { return e.Resolution == ResolutionPending }

// Overdue reports whether the deadline has passed while still pending.
func (e Escalation) Overdue(now time.Time) bool {
	return e.Pending() && !e.Deadline.IsZero() && !now.Before(e.Deadline)
}

// HumanRuntime holds escalations awaiting people.
//
// In-memory and bounded, like every other store in this module. That is a real
// limit and it is stated rather than discovered: an escalation queue that does
// not survive a restart loses the approvals in flight, and a durable queue is
// the Aurora-backed work this phase does not deliver. See ENGINEERING_AUDIT §A3.
type HumanRuntime struct {
	clock   rt.Clock
	metrics *Metrics
	audit   Auditor
	events  *EventDispatcher

	mu      sync.RWMutex
	pending map[DecisionID]*Escalation
	// resolved is a bounded ring of recent outcomes, so a caller that asked a
	// moment too late can still find out what happened.
	resolved []Escalation
	maxDone  int

	raised   atomic.Uint64
	approved atomic.Uint64
	rejected atomic.Uint64
	expiredN atomic.Uint64
}

// DefaultEscalationTimeouts gives each kind a deadline.
//
// The numbers come from what the person on the other end can tolerate rather
// than from what is convenient. A caller waiting on a confirmation is on the
// phone; a business approving a refund is not.
func DefaultEscalationTimeouts() map[EscalationKind]time.Duration {
	return map[EscalationKind]time.Duration{
		EscalationConfirmation: 30 * time.Second,
		EscalationApproval:     5 * time.Minute,
		EscalationSupervisor:   15 * time.Minute,
		EscalationTakeover:     60 * time.Second,
		EscalationBusiness:     4 * time.Hour,
	}
}

// NewHumanRuntime builds an escalation runtime.
func NewHumanRuntime(clock rt.Clock, m *Metrics, a Auditor, e *EventDispatcher) *HumanRuntime {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	return &HumanRuntime{
		clock: clock, metrics: m, audit: a, events: e,
		pending: make(map[DecisionID]*Escalation), maxDone: 512,
	}
}

// Raise creates an escalation from a decision.
//
// Idempotent per decision: raising twice for one decision returns the existing
// escalation rather than creating a second. Two entries for one decision would
// mean two humans asked to approve one thing, and the second approval would
// approve something already decided.
func (h *HumanRuntime) Raise(d Decision, kind EscalationKind, timeout time.Duration) (*Escalation, error) {
	now := h.clock.Now()

	h.mu.Lock()
	if existing, ok := h.pending[d.ID]; ok {
		out := *existing
		h.mu.Unlock()
		return &out, nil
	}

	esc := &Escalation{
		ID: d.ID, Kind: kind, Decision: d.ID, Correlation: d.Correlation,
		Session: d.Session, Actor: d.Actor, Subject: d.Subject,
		ActionLabel: d.ActionLabel, Reason: d.Reason, Policy: d.DecidedBy,
		Risk: d.Risk.Level, RaisedAt: now, Resolution: ResolutionPending,
	}
	if timeout > 0 {
		esc.Deadline = now.Add(timeout)
	}
	h.pending[d.ID] = esc
	depth := len(h.pending)
	out := *esc
	h.mu.Unlock()

	h.raised.Add(1)
	if h.metrics != nil {
		h.metrics.Escalated.Inc(d.Reason)
		h.metrics.EscalationDepth.Set(float64(depth))
	}
	h.record(AuditEscalated, out, "raised")
	h.emit(EventEscalated, out)
	return &out, nil
}

// Resolve records a human's decision.
//
// The FIRST resolution wins and the second is refused with
// [ErrAlreadyResolved]. Two humans resolving one escalation differently is a
// race with a real-world consequence, and silently accepting the later one
// means the action taken is whichever person happened to be slower.
func (h *HumanRuntime) Resolve(id DecisionID, r Resolution, by, note string) (*Escalation, error) {
	if by == "" {
		return nil, &ConfigError{Problems: []string{
			"escalation: ResolvedBy is required; an anonymous approval is an approval " +
				"nobody can be asked about"}}
	}
	if r == ResolutionPending {
		return nil, &ConfigError{Problems: []string{
			"escalation: cannot resolve to pending"}}
	}

	h.mu.Lock()
	esc, ok := h.pending[id]
	if !ok {
		for i := range h.resolved {
			if h.resolved[i].ID == id {
				h.mu.Unlock()
				return nil, fmt.Errorf("%w: %s", ErrAlreadyResolved, id)
			}
		}
		h.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrEscalationNotFound, id)
	}

	esc.Resolution = r
	esc.ResolvedBy = by
	esc.ResolvedAt = h.clock.Now()
	esc.Note = note
	out := *esc

	delete(h.pending, id)
	h.resolved = append(h.resolved, out)
	if len(h.resolved) > h.maxDone {
		h.resolved = h.resolved[1:]
	}
	depth := len(h.pending)
	h.mu.Unlock()

	switch r {
	case ResolutionApproved:
		h.approved.Add(1)
	case ResolutionExpired:
		h.expiredN.Add(1)
	default:
		h.rejected.Add(1)
	}

	if h.metrics != nil {
		h.metrics.Resolved.Inc(r.String())
		h.metrics.EscalationDepth.Set(float64(depth))
		h.metrics.EscalationWait.Observe(out.ResolvedAt.Sub(out.RaisedAt).Seconds())
	}
	h.record(AuditEscalationResolved, out, r.String())
	h.emit(EventEscalationResolved, out)
	return &out, nil
}

// Approve is the common case.
func (h *HumanRuntime) Approve(id DecisionID, by, note string) (*Escalation, error) {
	return h.Resolve(id, ResolutionApproved, by, note)
}

// Reject refuses.
func (h *HumanRuntime) Reject(id DecisionID, by, note string) (*Escalation, error) {
	return h.Resolve(id, ResolutionRejected, by, note)
}

// TakeOver records that a human took the interaction over.
func (h *HumanRuntime) TakeOver(id DecisionID, by, note string) (*Escalation, error) {
	return h.Resolve(id, ResolutionTakenOver, by, note)
}

// Get returns an escalation, pending or recently resolved.
func (h *HumanRuntime) Get(id DecisionID) (Escalation, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if esc, ok := h.pending[id]; ok {
		return *esc, true
	}
	for i := len(h.resolved) - 1; i >= 0; i-- {
		if h.resolved[i].ID == id {
			return h.resolved[i], true
		}
	}
	return Escalation{}, false
}

// Pending returns waiting escalations, oldest first.
//
// Oldest first because that is the order a human should work them, and because
// a queue that presents newest-first quietly starves the oldest item forever.
func (h *HumanRuntime) Pending() []Escalation {
	h.mu.RLock()
	out := make([]Escalation, 0, len(h.pending))
	for _, esc := range h.pending {
		out = append(out, *esc)
	}
	h.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		if !out[i].RaisedAt.Equal(out[j].RaisedAt) {
			return out[i].RaisedAt.Before(out[j].RaisedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// PendingFor returns waiting escalations of one kind.
func (h *HumanRuntime) PendingFor(k EscalationKind) []Escalation {
	var out []Escalation
	for _, esc := range h.Pending() {
		if esc.Kind == k {
			out = append(out, esc)
		}
	}
	return out
}

// Sweep expires overdue escalations and returns how many expired.
//
// Expiry resolves to [ResolutionExpired], which does NOT permit the action.
// The alternative — letting an unanswered escalation time out into an approval
// — turns every approval gate into a delay, and is the single most common way
// an approval control is quietly defeated.
func (h *HumanRuntime) Sweep() int {
	now := h.clock.Now()

	h.mu.RLock()
	var overdue []DecisionID
	for id, esc := range h.pending {
		if esc.Overdue(now) {
			overdue = append(overdue, id)
		}
	}
	h.mu.RUnlock()

	sort.Slice(overdue, func(i, j int) bool { return overdue[i] < overdue[j] })
	n := 0
	for _, id := range overdue {
		if _, err := h.Resolve(id, ResolutionExpired, "system", "deadline passed"); err == nil {
			n++
		}
	}
	return n
}

// Depth returns the pending count.
func (h *HumanRuntime) Depth() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.pending)
}

// Stats returns escalation counters.
func (h *HumanRuntime) Stats() EscalationStats {
	return EscalationStats{
		Raised: h.raised.Load(), Approved: h.approved.Load(),
		Rejected: h.rejected.Load(), Expired: h.expiredN.Load(),
		Pending: h.Depth(),
	}
}

// EscalationStats is a summary for operators.
type EscalationStats struct {
	Raised   uint64
	Approved uint64
	Rejected uint64
	Expired  uint64
	Pending  int
}

// ApprovalRate returns approvals over resolutions, or zero when none resolved.
func (s EscalationStats) ApprovalRate() float64 {
	resolved := s.Approved + s.Rejected + s.Expired
	if resolved == 0 {
		return 0
	}
	return float64(s.Approved) / float64(resolved)
}

func (h *HumanRuntime) record(kind AuditKind, e Escalation, reason string) {
	if h.audit == nil {
		return
	}
	details := map[string]string{
		"escalation": e.Kind.String(),
		"policy":     string(e.Policy),
	}
	if e.ResolvedBy != "" {
		details["resolved_by"] = e.ResolvedBy
	}
	if e.Note != "" {
		details["note"] = e.Note
	}
	_ = h.audit.Record(AuditEntry{
		At: h.clock.Now(), Kind: kind, Decision: e.Decision, Correlation: e.Correlation,
		Session: e.Session, Actor: e.Actor, Subject: e.Subject, Policy: e.Policy,
		Reason: reason, ActionLabel: e.ActionLabel, Risk: e.Risk, Details: details,
	})
}

func (h *HumanRuntime) emit(t EventType, e Escalation) {
	if h.events == nil {
		return
	}
	h.events.Dispatch(Event{
		Type: t, Decision: e.Decision, Correlation: e.Correlation, Session: e.Session,
		Actor: e.Actor, Subject: e.Subject, Policy: e.Policy, ActionLabel: e.ActionLabel,
		Reason: e.Reason, Risk: e.Risk,
		Details: map[string]string{
			"escalation": e.Kind.String(),
			"resolution": e.Resolution.String(),
		},
	})
}
