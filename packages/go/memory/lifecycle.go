package memory

import (
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// State is a record's lifecycle position.
//
// Seven states, every transition declared. A record's state and its [Tier] are
// independent: a record may be promoted from working to long-term while
// remaining Active, and it may be Archived at any tier.
type State int

const (
	// StateActive is a live, readable record.
	StateActive State = iota

	// StateExpired has passed its TTL and is no longer readable, but has not
	// yet been reclaimed. It exists as a distinct state so that "expired" and
	// "never existed" are answerable differently — a caller asking for an
	// expired memory deserves a different answer from one asking for a memory
	// that was never written.
	StateExpired

	// StateArchived has left the hot store for cold storage. Readable, but
	// only through an explicit restore.
	StateArchived

	// StateRedacted retains the record's existence and metadata while its
	// payload has been destroyed. Distinct from deletion because proving
	// something was here and is gone is frequently the obligation.
	StateRedacted

	// StateDeleted is terminal. The record is gone; only a tombstone remains,
	// so that erasure is provable and a key is never silently reused.
	StateDeleted

	// StatePendingErasure is a record scheduled for erasure whose removal
	// spans stores. It is not readable, and it is not yet gone.
	StatePendingErasure

	// StateCorrupt is a record that failed an integrity check. Not readable,
	// never silently repaired, and retained for diagnosis.
	StateCorrupt
)

// String renders the state for logs, metrics and diagrams.
func (s State) String() string {
	switch s {
	case StateExpired:
		return "expired"
	case StateArchived:
		return "archived"
	case StateRedacted:
		return "redacted"
	case StateDeleted:
		return "deleted"
	case StatePendingErasure:
		return "pending_erasure"
	case StateCorrupt:
		return "corrupt"
	default:
		return "active"
	}
}

// Readable reports whether a record in this state may be returned to a caller.
func (s State) Readable() bool { return s == StateActive || s == StateRedacted }

// Terminal reports whether no transition may follow.
func (s State) Terminal() bool { return s == StateDeleted }

// AllStates returns every state, in declaration order.
func AllStates() []State {
	return []State{StateActive, StateExpired, StateArchived, StateRedacted,
		StateDeleted, StatePendingErasure, StateCorrupt}
}

// lifecycleTable declares every legal state transition.
//
// This table IS the lifecycle. It is one literal so the whole machine can be
// read, diffed and diagrammed in one place, and so that adding a state without
// declaring its edges fails a test rather than producing an unreachable state.
//
// Properties asserted by TestLifecycle_TableIsWellFormed:
//   - every state is reachable from Active
//   - every non-terminal state can reach Deleted
//   - Deleted has no outgoing edges
func lifecycleTable() map[State][]State {
	return map[State][]State{
		// The ordinary path and every exit from it.
		StateActive: {
			StateExpired, StateArchived, StateRedacted,
			StatePendingErasure, StateCorrupt, StateDeleted,
		},

		// An expired record may be revived by a TTL extension — a subscriber
		// raising their retention preference should not have destroyed data
		// that has not yet been reclaimed.
		StateExpired: {StateActive, StateArchived, StateDeleted, StatePendingErasure},

		// Archived records restore, or are eventually reclaimed.
		StateArchived: {StateActive, StateDeleted, StatePendingErasure, StateRedacted},

		// A redacted record has no payload to restore. It can only be reclaimed.
		// There is deliberately no Redacted -> Active edge: reviving a redacted
		// record would mean recovering content that was destroyed on purpose.
		StateRedacted: {StateDeleted, StatePendingErasure, StateArchived},

		// Erasure completes, or is refused because a legal hold applies.
		StatePendingErasure: {StateDeleted, StateActive},

		// A corrupt record is diagnosed and reclaimed. It is never repaired
		// into Active: silently "fixing" a record that failed an integrity
		// check is how corruption becomes indistinguishable from truth.
		StateCorrupt: {StateDeleted, StateArchived},

		StateDeleted: {},
	}
}

// newLifecycleFSM builds the record lifecycle machine on the frozen runtime FSM.
//
// Reusing runtime.FSM rather than writing another is deliberate: it already
// refuses undeclared transitions and exit from terminal states, runs hooks
// outside its lock, and is tested. A second state machine here would be a
// second thing to get wrong.
func newLifecycleFSM(clock rt.Clock) (*rt.FSM[State], error) {
	return rt.NewFSM(rt.FSMSpec[State]{
		Initial:     StateActive,
		Transitions: lifecycleTable(),
		Terminal:    []State{StateDeleted},
	}, clock)
}

// canTransition reports whether a record may move between two states, without
// constructing an FSM.
//
// The store holds hundreds of thousands of records and one FSM each would be
// hundreds of thousands of maps and mutexes for a machine that is a pure
// function of the table. The FSM above exists so the table is validated once at
// construction and by test; this is the hot-path check.
func canTransition(from, to State) bool {
	if from == to {
		return false
	}
	for _, allowed := range lifecycleTable()[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Promotion and demotion
// ---------------------------------------------------------------------------

// PromotionPolicy decides when a record moves between tiers.
//
// EXPLICIT THRESHOLDS, NOT HEURISTICS. A memory layer whose promotion rules are
// implicit produces a system nobody can explain — and "why does it remember
// that about me" is a question this engine must always be able to answer with a
// rule rather than a shrug.
type PromotionPolicy struct {
	// AccessesToPromote is the read count at which a record is eligible to
	// move up a tier.
	AccessesToPromote uint64

	// MinAgeToPromote prevents a burst of reads in the first seconds from
	// promoting something that will never be read again.
	MinAgeToPromote time.Duration

	// IdleToDemote is the time without a read after which a record drops a
	// tier.
	IdleToDemote time.Duration

	// IdleToExpire is the time without a read after which a working-tier
	// record expires outright.
	IdleToExpire time.Duration

	// PromoteDerived allows inferred memories to be promoted.
	//
	// Defaults to FALSE. An inferred memory promoted to long term is the
	// platform deciding, on its own, to permanently remember something a person
	// never told it. That should be a deliberate deployment choice, not a
	// default.
	PromoteDerived bool
}

// DefaultPromotionPolicy returns the policy used unless overridden.
//
// The numbers derive from the platform's traffic shape rather than being round:
// a screening runs 20–40 seconds (ADR-0002 §11), so a working record read three
// times inside one call is genuinely load-bearing, while thirty seconds of idle
// spans a whole conversation.
func DefaultPromotionPolicy() PromotionPolicy {
	return PromotionPolicy{
		AccessesToPromote: 3,
		MinAgeToPromote:   5 * time.Second,
		IdleToDemote:      10 * time.Minute,
		IdleToExpire:      30 * time.Second,
		PromoteDerived:    false,
	}
}

func (p PromotionPolicy) validate() []string {
	var out []string
	if p.AccessesToPromote == 0 {
		out = append(out, "promotion: AccessesToPromote must be positive, "+
			"or every record promotes on creation")
	}
	if p.MinAgeToPromote < 0 {
		out = append(out, "promotion: MinAgeToPromote cannot be negative")
	}
	if p.IdleToDemote <= 0 {
		out = append(out, "promotion: IdleToDemote must be positive")
	}
	if p.IdleToExpire <= 0 {
		out = append(out, "promotion: IdleToExpire must be positive")
	}
	if p.IdleToDemote <= p.IdleToExpire {
		out = append(out, "promotion: IdleToDemote must exceed IdleToExpire, "+
			"or a record expires before it can be demoted")
	}
	return out
}

// TierDecision is the outcome of evaluating a record against the policy.
type TierDecision int

const (
	// TierHold leaves the record where it is.
	TierHold TierDecision = iota
	// TierPromoteUp moves it up one tier.
	TierPromoteUp
	// TierDemoteDown moves it down one tier.
	TierDemoteDown
	// TierExpireNow expires it.
	TierExpireNow
)

// String renders the decision for metric labels.
func (d TierDecision) String() string {
	switch d {
	case TierPromoteUp:
		return "promote"
	case TierDemoteDown:
		return "demote"
	case TierExpireNow:
		return "expire"
	default:
		return "hold"
	}
}

// Evaluate decides what should happen to a record's tier.
//
// A pure function of the record, the policy and the instant — no clock read, no
// map iteration, no randomness. That is what makes the promotion ladder
// exhaustively table-testable and what makes a memory's history reproducible.
//
// Order matters: expiry is checked before demotion, and pinning before both,
// so a pinned record is never quietly reclaimed and an expired one is not first
// demoted into a tier it will immediately leave.
func (p PromotionPolicy) Evaluate(r *Record, now time.Time) TierDecision {
	if r.Pinned {
		return TierHold
	}
	if r.State != StateActive {
		return TierHold
	}

	idle := r.Idle(now)

	// Working-tier records that nobody has read are the engine's garbage. They
	// expire rather than demote, because there is no tier below working.
	if r.Tier == TierWorking && idle > p.IdleToExpire {
		return TierExpireNow
	}

	if idle > p.IdleToDemote && r.Tier > TierWorking {
		return TierDemoteDown
	}

	if r.Tier < TierLongTerm &&
		r.AccessCount >= p.AccessesToPromote &&
		r.Age(now) >= p.MinAgeToPromote {

		// An inferred memory is weaker evidence about a person than a stated
		// one. Promoting it to permanence is a decision a deployment makes,
		// not a default the engine takes.
		if r.Provenance.Derived && !p.PromoteDerived {
			return TierHold
		}
		return TierPromoteUp
	}

	return TierHold
}
