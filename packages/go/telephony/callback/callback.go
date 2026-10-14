// Package callback ingests normalised carrier call events idempotently and
// reconciles them against the telephony call lifecycle.
//
// PHASE 19 P1-4. Provider-neutral: nothing here parses a carrier's webhook, and
// no carrier's event names, ordering or retry behaviour is assumed. A provider
// adapter (not built) turns a carrier payload into an [Event]; this package
// decides whether that event is new, and what — if anything — it may do to the
// call.
//
//	carrier webhook ─► (adapter: auth, parse — NOT BUILT) ─► Event ─► [THIS] ─► telephony.CallLifecycle
//
// # Deduplication
//
// The key is ([telephony.ProviderID], provider event ID) — scoped to the
// provider, never the event type, a timestamp or a payload hash. A key is
// RESERVED before anything is applied and SETTLED only after the outcome is
// final, so concurrent redeliveries of one event cannot both apply it: the
// second sees the reservation and is told the event is in flight.
//
// A failed attempt RELEASES the reservation. A redelivery is then processed
// from scratch, so a transient failure is never recorded as a success.
//
// A key reused with a different call or event kind is not a duplicate; it is a
// conflict, recorded for reconciliation, and the first event's outcome stands.
//
// # Ordering
//
// Arrival order is not event order, and provider timestamps are recorded but
// NOT used to order anything: this package has no evidence of any carrier's
// clock or delivery guarantees. Instead every event is judged against the
// call's CURRENT state, using an explicit table ([rules]) checked against the
// lifecycle's own transition table:
//
//   - applicable here  → applied through CallLifecycle (Applied)
//   - already true     → no-op (AlreadySatisfied)
//   - the call is past it → no-op (Stale); a call is never moved backwards
//   - contradicts the state, or cannot be recorded → no-op, recorded (Conflict)
//
// Because the judgement is by state, applying an event twice is refused by the
// lifecycle itself even if a deduplication record is lost: the second attempt
// finds the state already satisfied.
//
// # Observations are never turned into commands
//
// A carrier's answer and remote end are recorded through
// [telephony.CallLifecycle.ObserveRemoteAnswer] and
// [telephony.CallCoordinator.ObserveRemoteEnd] (P1-5), which issue no provider
// command — never Accept (Provider.Answer) or Disconnect (Provider.Hangup).
//
//   - "answered" moves an OUTBOUND call to Accepted, not Connected: the media
//     path is a separate report ([telephony.CallLifecycle.Connect]). The
//     platform answers inbound calls itself, through Accept, so "answered" for
//     an inbound call it has not accepted is a Conflict.
//   - "completed" ends a call from any state the lifecycle declares an Ended
//     edge for. From a state without one (Incoming, Ringing, Screening,
//     Accepted) it is a Conflict: the lifecycle could only conclude such a call
//     as Failed, and choosing that would invent a reason.
//
// Terminal outcomes go through the coordinator, which releases the call's
// capacity slot as [telephony.CallCoordinator.End] does.
//
// # Guarantees, stated exactly
//
// Within ONE process and ONE [Store]: an event key is applied at most once,
// concurrent deliveries of one key cannot both apply, and events for one call
// are serialised. That is at-most-once application per key and idempotent
// state-based reconciliation — NOT exactly-once processing. [MemoryStore]
// forgets everything on restart and is not shared between replicas; see its
// documentation.
package callback

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
)

// Errors. Callers match with errors.Is.
var (
	// ErrInvalidEvent is returned for an event missing a required field or
	// carrying a malformed one. Nothing is reserved for it.
	ErrInvalidEvent = errors.New("callback: invalid event")

	// ErrUnknownCall is returned when the event's call cannot be found. The
	// reservation is released: the call may not be registered YET, and a
	// redelivery may succeed. Retryable.
	ErrUnknownCall = errors.New("callback: unknown call")

	// ErrEventIDReused is returned when a provider event ID arrives again with
	// a different call or kind. The first event's outcome stands.
	ErrEventIDReused = errors.New("callback: provider event ID reused with different content")

	// ErrStoreFull is returned when a store cannot reserve a key without
	// forgetting one that is still in flight.
	ErrStoreFull = errors.New("callback: event store is full")
)

// ---------------------------------------------------------------------------
// The event
// ---------------------------------------------------------------------------

// Kind is a normalised, provider-neutral call observation.
type Kind string

// The kinds. Observations, not commands: each says what the carrier reports
// happened, and none asks the platform to do anything.
const (
	KindRinging   Kind = "ringing"   // the called party is being alerted
	KindAnswered  Kind = "answered"  // the call was answered
	KindCompleted Kind = "completed" // the call ended normally
	KindBusy      Kind = "busy"      // the called party was busy
	KindNoAnswer  Kind = "no_answer" // the called party did not answer
	KindCancelled Kind = "cancelled" // the call was abandoned before answer
	KindFailed    Kind = "failed"    // the carrier reports a failure
)

// Valid reports whether k is a declared kind.
func (k Kind) Valid() bool {
	_, ok := rules[k]
	return ok
}

// maxIDLen bounds every carrier-supplied identifier. They reach logs, metrics
// and stores; an identifier is not a payload.
const maxIDLen = 128

// Event is one normalised carrier observation about one call.
//
// IDENTIFIERS ONLY (frozen invariant I7). No number, name or audio reference
// belongs here.
type Event struct {
	// Provider is the carrier the event came from. Required.
	Provider telephony.ProviderID

	// EventID is the provider's identifier for THIS event, stable across
	// redeliveries. Required; it is the deduplication key.
	EventID string

	// ProviderCallID is the provider's identifier for the call. Required.
	ProviderCallID string

	// Kind is the normalised observation. Required.
	Kind Kind

	// OccurredAt is the provider's timestamp, if it sent one. Recorded, never
	// used for ordering.
	OccurredAt time.Time

	// ReceivedAt is when this process received the event. Required.
	ReceivedAt time.Time

	// Correlation is an optional correlation identifier for tracing.
	Correlation telephony.CorrelationID
}

// Validate reports every problem with e.
func (e Event) Validate() error {
	var problems []string
	if !e.Provider.Valid() {
		problems = append(problems, "Provider is required and must be a valid provider ID")
	}
	if p := checkID("EventID", e.EventID, true); p != "" {
		problems = append(problems, p)
	}
	if p := checkID("ProviderCallID", e.ProviderCallID, true); p != "" {
		problems = append(problems, p)
	}
	if !e.Kind.Valid() {
		problems = append(problems, "Kind is required and must be a declared kind")
	}
	if e.ReceivedAt.IsZero() {
		problems = append(problems, "ReceivedAt is required")
	}
	if p := checkID("Correlation", string(e.Correlation), false); p != "" {
		problems = append(problems, p)
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidEvent, strings.Join(problems, "; "))
	}
	return nil
}

// checkID bounds an identifier to a printable, log-safe alphabet. Problems
// name the field, never echo the value.
func checkID(field, v string, required bool) string {
	if v == "" {
		if required {
			return field + " is required"
		}
		return ""
	}
	if len(v) > maxIDLen {
		return fmt.Sprintf("%s is longer than %d bytes", field, maxIDLen)
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == ':':
		default:
			return field + " contains a character outside [A-Za-z0-9._:-]"
		}
	}
	return ""
}

// Key is the deduplication key: an event ID, scoped to its provider.
type Key struct {
	Provider telephony.ProviderID
	EventID  string
}

// Key returns e's deduplication key.
func (e Event) Key() Key { return Key{Provider: e.Provider, EventID: e.EventID} }

// Fingerprint is what must match for a repeated key to be a true redelivery.
//
// Timestamps are deliberately excluded: a carrier may restamp a retry, and a
// retry is still the same event.
type Fingerprint struct {
	ProviderCallID string
	Kind           Kind
}

// Fingerprint returns e's fingerprint.
func (e Event) Fingerprint() Fingerprint {
	return Fingerprint{ProviderCallID: e.ProviderCallID, Kind: e.Kind}
}

// ---------------------------------------------------------------------------
// Outcomes
// ---------------------------------------------------------------------------

// Outcome is what ingestion concluded about one event.
type Outcome string

// The outcomes.
const (
	OutcomeApplied          Outcome = "applied"           // a lifecycle transition was made
	OutcomeAlreadySatisfied Outcome = "already_satisfied" // the call is already in the reported state
	OutcomeStale            Outcome = "stale"             // the call is past the reported state, or terminal
	OutcomeConflict         Outcome = "conflict"          // contradicts the state, or cannot be recorded; see Conflicts
	OutcomeDuplicate        Outcome = "duplicate"         // this key was already settled
	OutcomeInFlight         Outcome = "in_flight"         // this key is being processed right now
)

// Result describes one ingestion.
type Result struct {
	Outcome Outcome
	// Call is the platform call the event resolved to, when it resolved.
	Call telephony.CallID
	// From is the call's state when the event was judged.
	From telephony.CallState
	// To is the state the call moved to. Set only for Applied.
	To telephony.CallState
	// Reason is a bounded code explaining a non-Applied outcome.
	Reason string
	// Prior is the original outcome, for a Duplicate.
	Prior Outcome
}

// Conflict is an event that needs reconciliation by something other than this
// package.
type Conflict struct {
	Key        Key
	Call       telephony.CallID // empty when the call never resolved
	Kind       Kind
	State      telephony.CallState // empty when not judged against a state
	Reason     string
	ReceivedAt time.Time
}

// Reason codes.
const (
	ReasonEventIDReused         = "event_id_reused"
	ReasonContradictsState      = "contradicts_state"
	ReasonAnswerWithoutAccept   = "answer_without_accept"
	ReasonRemoteEndUnrecordable = "remote_end_unrecordable"
	ReasonCallTerminal          = "call_terminal"
	ReasonPastReportedState     = "past_reported_state"
)

// ---------------------------------------------------------------------------
// Reconciliation rules
// ---------------------------------------------------------------------------

type op int

const (
	opNone op = iota
	opRing
	opObserveAnswer
	opObserveEnd
	opFail
)

// rule is how one kind relates to each call state. A state in none of the sets
// is a contradiction.
type rule struct {
	to         telephony.CallState
	op         op
	failReason string // for opFail: the lifecycle reason code

	apply        []telephony.CallState // apply op from here
	satisfied    []telephony.CallState // already true
	stale        []telephony.CallState // the call is past it
	unrecordable []telephony.CallState // true, but the lifecycle has no side-effect-free path
	gapReason    string                // reason for unrecordable

	// outboundOnly restricts apply to outbound calls; an inbound call in an
	// apply state is a conflict with inboundReason.
	outboundOnly  bool
	inboundReason string
}

var (
	preAnswer   = []telephony.CallState{telephony.StateIncoming, telephony.StateRinging, telephony.StateScreening, telephony.StateAccepted, telephony.StateTimeout}
	pastConnect = []telephony.CallState{telephony.StateMuted, telephony.StateHold, telephony.StateTransferred, telephony.StateEscalated}
	nonTerminal = func() []telephony.CallState {
		var out []telephony.CallState
		for _, s := range telephony.AllStates() {
			if !s.Terminal() {
				out = append(out, s)
			}
		}
		return out
	}()
)

// withEdgeTo returns the non-terminal states that do (has) or do not (!has)
// declare an edge to target — derived from the lifecycle's own table so the
// two cannot drift.
func withEdgeTo(target telephony.CallState, has bool) []telephony.CallState {
	var out []telephony.CallState
	for _, s := range nonTerminal {
		if telephony.CanTransition(s, target) == has {
			out = append(out, s)
		}
	}
	return out
}

func failRule(reason string) rule {
	return rule{to: telephony.StateFailed, op: opFail, failReason: reason, apply: preAnswer}
}

// rules is THE reconciliation table. TestRules_AgreeWithTheTransitionTable
// proves every apply edge is declared by the lifecycle.
var rules = map[Kind]rule{
	KindRinging: {
		to: telephony.StateRinging, op: opRing,
		apply:     []telephony.CallState{telephony.StateIncoming},
		satisfied: []telephony.CallState{telephony.StateRinging},
		stale: append([]telephony.CallState{
			telephony.StateScreening, telephony.StateAccepted, telephony.StateRejected, telephony.StateConnected,
		}, pastConnect...),
	},
	KindAnswered: {
		to: telephony.StateAccepted, op: opObserveAnswer,
		apply:     []telephony.CallState{telephony.StateRinging, telephony.StateScreening},
		satisfied: []telephony.CallState{telephony.StateAccepted},
		// Escalated is NOT stale: Screening → Escalated happens before any
		// answer, so an answer reported for an escalated call is a conflict.
		stale: []telephony.CallState{
			telephony.StateConnected, telephony.StateMuted, telephony.StateHold, telephony.StateTransferred,
		},
		outboundOnly:  true,
		inboundReason: ReasonAnswerWithoutAccept,
		// An inbound call still being offered was never accepted by us.
		unrecordable: []telephony.CallState{telephony.StateIncoming},
		gapReason:    ReasonAnswerWithoutAccept,
	},
	KindCompleted: {
		to: telephony.StateEnded, op: opObserveEnd,
		apply:        withEdgeTo(telephony.StateEnded, true),
		unrecordable: withEdgeTo(telephony.StateEnded, false),
		gapReason:    ReasonRemoteEndUnrecordable,
	},
	KindBusy:      failRule("carrier_busy"),
	KindNoAnswer:  failRule("carrier_no_answer"),
	KindCancelled: failRule("carrier_cancelled"),
	KindFailed: {
		to: telephony.StateFailed, op: opFail, failReason: "carrier_failed",
		apply: nonTerminal,
	},
}

type action int

const (
	actConflict action = iota
	actApply
	actSatisfied
	actStale
	actUnrecordable
)

func contains(set []telephony.CallState, s telephony.CallState) bool {
	for _, x := range set {
		if x == s {
			return true
		}
	}
	return false
}

// classify judges kind against state and direction. It never consults a
// timestamp. The returned reason is set for actConflict and actUnrecordable.
func classify(k Kind, s telephony.CallState, dir telephony.Direction) (rule, action, string) {
	r := rules[k]
	switch {
	case contains(r.apply, s) && r.outboundOnly && dir != telephony.DirectionOutbound:
		return r, actConflict, r.inboundReason
	case contains(r.apply, s):
		return r, actApply, ""
	case contains(r.satisfied, s):
		return r, actSatisfied, ""
	case contains(r.stale, s):
		return r, actStale, ""
	case contains(r.unrecordable, s):
		return r, actUnrecordable, r.gapReason
	default:
		return r, actConflict, ReasonContradictsState
	}
}

// ---------------------------------------------------------------------------
// Ports
// ---------------------------------------------------------------------------

// Resolver maps a provider's call identifier to the platform call.
//
// Provider-scoped by construction. Nothing in the repository creates this
// binding yet; it belongs to the provider adapter that places or accepts the
// call. An unknown call is reported with an error wrapping [ErrUnknownCall].
type Resolver interface {
	Resolve(provider telephony.ProviderID, providerCallID string) (telephony.CallID, error)
}

// Lifecycle is the slice of the telephony runtime this package drives.
//
// Every operation here records what the carrier reported and issues NO
// provider command.
type Lifecycle interface {
	// State and Direction describe the call, or return an error wrapping
	// telephony.ErrCallNotFound.
	State(id telephony.CallID) (telephony.CallState, error)
	Direction(id telephony.CallID) (telephony.Direction, error)

	Ring(ctx context.Context, id telephony.CallID) error
	ObserveRemoteAnswer(ctx context.Context, id telephony.CallID) error
	// ObserveRemoteEnd and Fail conclude the call and must release its
	// capacity slot, as the runtime's coordinator does.
	ObserveRemoteEnd(ctx context.Context, id telephony.CallID) error
	Fail(ctx context.Context, id telephony.CallID, reason string) error
}

// RuntimeLifecycle adapts a telephony runtime to [Lifecycle]. Terminal
// outcomes go through the runtime's [telephony.CallCoordinator], which pairs
// the lifecycle termination with the capacity-slot release.
func RuntimeLifecycle(r *telephony.TelephonyRuntime) Lifecycle {
	return runtimeLifecycle{reg: r.Registry(), lc: r.Lifecycle(), co: r.Coordinator()}
}

type runtimeLifecycle struct {
	reg *telephony.CallRegistry
	lc  *telephony.CallLifecycle
	co  *telephony.CallCoordinator
}

func (r runtimeLifecycle) State(id telephony.CallID) (telephony.CallState, error) {
	sess, err := r.reg.Get(id)
	if err != nil {
		return "", err
	}
	return sess.State(), nil
}

func (r runtimeLifecycle) Direction(id telephony.CallID) (telephony.Direction, error) {
	sess, err := r.reg.Get(id)
	if err != nil {
		return "", err
	}
	return sess.Context().Direction, nil
}

func (r runtimeLifecycle) Ring(ctx context.Context, id telephony.CallID) error {
	return r.lc.Ring(ctx, id)
}

func (r runtimeLifecycle) ObserveRemoteAnswer(ctx context.Context, id telephony.CallID) error {
	return r.lc.ObserveRemoteAnswer(ctx, id)
}

func (r runtimeLifecycle) ObserveRemoteEnd(ctx context.Context, id telephony.CallID) error {
	return r.co.ObserveRemoteEnd(ctx, id)
}

func (r runtimeLifecycle) Fail(ctx context.Context, id telephony.CallID, reason string) error {
	return r.co.Fail(ctx, id, reason)
}

// ---------------------------------------------------------------------------
// The ingestor
// ---------------------------------------------------------------------------

// stripes serialises event processing per call. Fixed, so it cannot grow with
// the number of calls.
const stripes = 64

// Ingestor deduplicates and applies events. Safe for concurrent use.
type Ingestor struct {
	store    Store
	resolver Resolver
	life     Lifecycle
	locks    [stripes]sync.Mutex
}

// NewIngestor builds an ingestor. Every port is required.
func NewIngestor(store Store, resolver Resolver, life Lifecycle) (*Ingestor, error) {
	var missing []string
	if store == nil {
		missing = append(missing, "store")
	}
	if resolver == nil {
		missing = append(missing, "resolver")
	}
	if life == nil {
		missing = append(missing, "lifecycle")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("callback: NewIngestor requires %s", strings.Join(missing, ", "))
	}
	return &Ingestor{store: store, resolver: resolver, life: life}, nil
}

func (in *Ingestor) lockFor(id telephony.CallID) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return &in.locks[h.Sum32()%stripes]
}

// Ingest processes one event.
//
// A nil error means the event's key is SETTLED and a redelivery will be
// reported as Duplicate — including when the outcome is Conflict, which is
// recorded for reconciliation rather than retried. A non-nil error means the
// key was NOT settled (or was refused before reservation) and a redelivery
// will be processed again; the exception is [ErrEventIDReused], where the key
// stays settled with its first outcome.
func (in *Ingestor) Ingest(ctx context.Context, e Event) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := e.Validate(); err != nil {
		return Result{}, err
	}

	key := e.Key()
	// A store failure is never read as "new event": nothing is applied.
	res, err := in.store.Reserve(ctx, key, e.Fingerprint())
	if err != nil {
		return Result{}, err
	}
	switch res.Status {
	case StatusDuplicate:
		return Result{Outcome: OutcomeDuplicate, Call: res.Prior.Call, Prior: res.Prior.Outcome}, nil
	case StatusInFlight:
		return Result{Outcome: OutcomeInFlight}, nil
	case StatusMismatch:
		if err := in.store.RecordConflict(ctx, Conflict{Key: key, Kind: e.Kind, Reason: ReasonEventIDReused, ReceivedAt: e.ReceivedAt}); err != nil {
			return Result{Outcome: OutcomeConflict, Reason: ReasonEventIDReused}, errors.Join(ErrEventIDReused, err)
		}
		return Result{Outcome: OutcomeConflict, Reason: ReasonEventIDReused}, ErrEventIDReused
	case StatusReserved:
	default:
		return Result{}, fmt.Errorf("callback: store returned unknown reservation status %d", res.Status)
	}

	// From here the key is reserved and MUST be settled or released. Released
	// on every path that does not settle — including a panic — and released
	// even if ctx has ended, since a held reservation blocks every retry until
	// its lease expires. A failed release is not fatal for the same reason: the
	// lease bounds it.
	token := res.Token
	settled := false
	defer func() {
		if !settled {
			_ = in.store.Release(context.WithoutCancel(ctx), key, token)
		}
	}()

	id, err := in.resolver.Resolve(e.Provider, e.ProviderCallID)
	if err != nil {
		if !errors.Is(err, ErrUnknownCall) {
			err = fmt.Errorf("%w: %w", ErrUnknownCall, err)
		}
		return Result{}, err
	}

	mu := in.lockFor(id)
	mu.Lock()
	defer mu.Unlock()

	r, err := in.judge(ctx, e, key, id)
	if err != nil {
		return Result{}, err
	}
	if err := in.store.Settle(ctx, key, token, r); err != nil {
		// The outcome happened but was not recorded; the key is released, and
		// a redelivery is re-judged against the state, which already reflects
		// it — so it cannot apply twice.
		return r, err
	}
	settled = true
	return r, nil
}

// judge decides and, if applicable, applies. The caller holds the call's lock.
func (in *Ingestor) judge(ctx context.Context, e Event, key Key, id telephony.CallID) (Result, error) {
	terminal, err := in.store.Terminal(ctx, id)
	if err != nil {
		// Unknown is not "live": refuse rather than judge without it.
		return Result{}, err
	}
	if terminal {
		return Result{Outcome: OutcomeStale, Call: id, Reason: ReasonCallTerminal}, nil
	}

	state, err := in.life.State(id)
	if err != nil {
		if errors.Is(err, telephony.ErrCallNotFound) {
			return Result{}, fmt.Errorf("%w: %s is not registered", ErrUnknownCall, id)
		}
		return Result{}, err
	}
	if state.Terminal() {
		if err := in.store.MarkTerminal(ctx, id); err != nil {
			return Result{}, err
		}
		return Result{Outcome: OutcomeStale, Call: id, From: state, Reason: ReasonCallTerminal}, nil
	}

	dir, err := in.life.Direction(id)
	if err != nil {
		if errors.Is(err, telephony.ErrCallNotFound) {
			return Result{}, fmt.Errorf("%w: %s is not registered", ErrUnknownCall, id)
		}
		return Result{}, err
	}

	r, act, reason := classify(e.Kind, state, dir)
	switch act {
	case actApply:
		if err := in.apply(ctx, r, id); err != nil {
			return Result{}, fmt.Errorf("callback: applying %s to %s in %s: %w", e.Kind, id, state, err)
		}
		if r.to.Terminal() {
			// Recorded before the lock is released, so a concurrent event for
			// this call sees it — the registry no longer holds the session.
			//
			// If the mark cannot be recorded the event is NOT settled: the
			// transition happened, but a redelivery finds the call gone and is
			// refused as unknown — it can never apply twice.
			if err := in.store.MarkTerminal(ctx, id); err != nil {
				return Result{}, fmt.Errorf("callback: %s reached %s but its terminal mark was not recorded: %w", id, r.to, err)
			}
		}
		return Result{Outcome: OutcomeApplied, Call: id, From: state, To: r.to}, nil
	case actSatisfied:
		return Result{Outcome: OutcomeAlreadySatisfied, Call: id, From: state}, nil
	case actStale:
		return Result{Outcome: OutcomeStale, Call: id, From: state, Reason: ReasonPastReportedState}, nil
	default: // actConflict, actUnrecordable
		return in.conflict(ctx, e, key, id, state, reason)
	}
}

// conflict records a conflict for reconciliation. If it cannot be recorded the
// event is not settled, so a redelivery tries again rather than losing it.
func (in *Ingestor) conflict(ctx context.Context, e Event, key Key, id telephony.CallID, state telephony.CallState, reason string) (Result, error) {
	if err := in.store.RecordConflict(ctx, Conflict{
		Key: key, Call: id, Kind: e.Kind, State: state, Reason: reason, ReceivedAt: e.ReceivedAt,
	}); err != nil {
		return Result{}, err
	}
	return Result{Outcome: OutcomeConflict, Call: id, From: state, Reason: reason}, nil
}

func (in *Ingestor) apply(ctx context.Context, r rule, id telephony.CallID) error {
	switch r.op {
	case opRing:
		return in.life.Ring(ctx, id)
	case opObserveAnswer:
		return in.life.ObserveRemoteAnswer(ctx, id)
	case opObserveEnd:
		return in.life.ObserveRemoteEnd(ctx, id)
	case opFail:
		return in.life.Fail(ctx, id, r.failReason)
	case opNone:
		return fmt.Errorf("callback: rule for %s declares no lifecycle operation", r.to)
	default:
		return fmt.Errorf("callback: no lifecycle operation for %s", r.to)
	}
}
