package speech

import (
	"fmt"
	"sync"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// AssemblyReason explains an assembler decision.
//
// A bounded enum, so it is safe as a metric label. Every rejection is
// attributable to exactly one of these, which is what makes "why did we lose
// that transcript" answerable from a dashboard rather than from a log search.
type AssemblyReason string

// The assembly outcomes.
const (
	// ReasonApplied means the segment became the live partial or the final.
	ReasonApplied AssemblyReason = "applied"

	// ReasonDuplicate means this sequence was already seen for this turn.
	ReasonDuplicate AssemblyReason = "duplicate"

	// ReasonOutOfOrder means the sequence is behind what has been committed.
	ReasonOutOfOrder AssemblyReason = "out_of_order"

	// ReasonAfterFinal means a non-final segment arrived for a finalised turn.
	ReasonAfterFinal AssemblyReason = "after_final"

	// ReasonDoubleFinal means a second final arrived for a finalised turn.
	ReasonDoubleFinal AssemblyReason = "double_final"
)

// String implements fmt.Stringer.
func (r AssemblyReason) String() string { return string(r) }

// AssemblyResult is what the assembler decided about a segment.
type AssemblyResult struct {
	// Applied reports whether the segment changed the transcript.
	Applied bool

	// Superseded reports whether this segment replaced an earlier partial.
	Superseded bool

	// Reason explains the decision, applied or not.
	Reason AssemblyReason
}

// String renders the result.
func (r AssemblyResult) String() string {
	return fmt.Sprintf("applied=%v superseded=%v reason=%s", r.Applied, r.Superseded, r.Reason)
}

// maxTurnsRetained bounds how many turns one assembler keeps.
//
// A speech session lives for a whole call, and a long call is hundreds of
// turns. Retaining every one forever makes the assembler a transcript archive
// living in process memory, which is both an unbounded allocation and an
// unbounded retention of conversation content. The oldest finalised turns are
// evicted; a durable transcript is a downstream concern with its own retention
// policy (ADR-0012), not this type's.
const maxTurnsRetained = 256

// turnState is one turn's assembly state.
type turnState struct {
	partial     TranscriptSegment
	havePartial bool

	final     TranscriptSegment
	haveFinal bool

	// highest is the greatest sequence seen for this turn — the ordering gate.
	highest uint64
	// haveSeq marks whether highest is meaningful yet, so sequence 0 is legal.
	haveSeq bool

	// seen deduplicates sequences within the turn.
	seen map[uint64]struct{}
}

// TranscriptAssembler turns a stream of provider results into an ordered,
// immutable transcript.
//
// # A final is immutable, and that is the load-bearing rule
//
// Once a turn is finalised nothing rewrites it: not a late partial, not a
// second final, not a retry from a provider that already answered. Providers
// legitimately emit results after a stream closes — a network round trip does
// not stop because we stopped listening — and a transcript that could be
// rewritten AFTER the conversation engine acted on it is far worse than one
// that loses a word. The engine would have replied to something the record no
// longer says.
//
// # PartialTranscriptManager and FinalTranscriptManager are methods here
//
// The brief names them as separate subsystems. They are implemented as views on
// this one type ([TranscriptAssembler.Partial], [TranscriptAssembler.Final])
// rather than as separate structs, because they are two readings of a single
// piece of state. Splitting that state across three objects would let them
// disagree about whether a turn is final, and the whole point of this type is
// that exactly one answer to that question exists.
//
// # Session ownership
//
// An assembler belongs to one session and refuses segments from any other. That
// is the structural half of cross-session isolation: a provider callback
// carrying the wrong session identifier cannot contaminate a transcript, it
// errors.
type TranscriptAssembler struct {
	session SessionID
	clock   rt.Clock

	mu    sync.RWMutex
	turns map[TurnID]*turnState
	// order preserves turn arrival order for Segments(), and bounds retention.
	order []TurnID
}

// NewTranscriptAssembler builds an assembler owned by one session.
func NewTranscriptAssembler(sess SessionID, clock rt.Clock) *TranscriptAssembler {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	return &TranscriptAssembler{
		session: sess,
		clock:   clock,
		turns:   make(map[TurnID]*turnState),
	}
}

// Session returns the session this assembler belongs to.
func (a *TranscriptAssembler) Session() SessionID { return a.session }

// Apply offers a segment and reports what the assembler decided.
//
// An error is returned only for a segment that could not be evaluated at all —
// malformed, or belonging to another session. An ordinary rejection is reported
// in the result, because rejecting stale provider output is normal operation
// and returning an error for it would train callers to ignore errors.
func (a *TranscriptAssembler) Apply(s TranscriptSegment) (AssemblyResult, error) {
	if err := s.Validate(); err != nil {
		return AssemblyResult{}, err
	}
	if s.Session != a.session {
		return AssemblyResult{}, fmt.Errorf(
			"%w: segment belongs to session %s, this assembler owns %s",
			ErrInvalidTranscript, s.Session, a.session)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	ts := a.turns[s.Turn]
	if ts == nil {
		ts = &turnState{seen: make(map[uint64]struct{})}
		a.turns[s.Turn] = ts
		a.order = append(a.order, s.Turn)
		a.evictLocked()
	}

	// A finalised turn is closed to everything.
	if ts.haveFinal {
		if s.IsFinal {
			return AssemblyResult{Reason: ReasonDoubleFinal}, nil
		}
		return AssemblyResult{Reason: ReasonAfterFinal}, nil
	}

	if _, dup := ts.seen[s.Sequence]; dup {
		return AssemblyResult{Reason: ReasonDuplicate}, nil
	}

	// Out of order: strictly behind what has been committed. Equal sequences
	// are caught by the duplicate check above, so this is a genuine regression.
	if ts.haveSeq && s.Sequence < ts.highest {
		return AssemblyResult{Reason: ReasonOutOfOrder}, nil
	}

	ts.seen[s.Sequence] = struct{}{}
	ts.highest = s.Sequence
	ts.haveSeq = true

	superseded := ts.havePartial

	if s.IsFinal {
		ts.final = s
		ts.haveFinal = true
		// The live partial is cleared: a consumer rendering both would show the
		// caller's words twice, once provisionally and once for real.
		ts.partial = TranscriptSegment{}
		ts.havePartial = false
		return AssemblyResult{Applied: true, Superseded: superseded, Reason: ReasonApplied}, nil
	}

	ts.partial = s
	ts.havePartial = true
	return AssemblyResult{Applied: true, Superseded: superseded, Reason: ReasonApplied}, nil
}

// evictLocked drops the oldest turns beyond the retention bound. Caller holds
// the lock.
func (a *TranscriptAssembler) evictLocked() {
	for len(a.order) > maxTurnsRetained {
		oldest := a.order[0]
		a.order = a.order[1:]
		delete(a.turns, oldest)
	}
}

// Partial returns the live partial for a turn, if one is outstanding.
//
// This is the PartialTranscriptManager view.
func (a *TranscriptAssembler) Partial(turn TurnID) (TranscriptSegment, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	ts := a.turns[turn]
	if ts == nil || !ts.havePartial {
		return TranscriptSegment{}, false
	}
	return ts.partial, true
}

// Final returns the immutable final for a turn, if it has one.
//
// This is the FinalTranscriptManager view.
func (a *TranscriptAssembler) Final(turn TurnID) (TranscriptSegment, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	ts := a.turns[turn]
	if ts == nil || !ts.haveFinal {
		return TranscriptSegment{}, false
	}
	return ts.final, true
}

// IsFinalised reports whether a turn has been finalised.
func (a *TranscriptAssembler) IsFinalised(turn TurnID) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	ts := a.turns[turn]
	return ts != nil && ts.haveFinal
}

// Segments returns every finalised segment, in turn order.
//
// Finalised only: a partial is provisional by definition, and a caller building
// a conversation record from provisional text would record words the caller
// never said.
func (a *TranscriptAssembler) Segments() []TranscriptSegment {
	a.mu.RLock()
	defer a.mu.RUnlock()

	out := make([]TranscriptSegment, 0, len(a.order))
	for _, turn := range a.order {
		if ts := a.turns[turn]; ts != nil && ts.haveFinal {
			out = append(out, ts.final)
		}
	}
	return out
}

// Turns returns how many turns the assembler is retaining.
func (a *TranscriptAssembler) Turns() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.turns)
}

// Reset discards everything.
//
// Used when a session ends. Retention beyond the session is a downstream
// decision with its own policy; this type does not become an archive by
// accident.
func (a *TranscriptAssembler) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.turns = make(map[TurnID]*turnState)
	a.order = nil
}
