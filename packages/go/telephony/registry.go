package telephony

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// registryShards is how many independent maps the registry holds.
//
// 64, which is not arbitrary. The brief requires thousands of concurrent
// sessions; a single map behind one mutex serialises every lookup, and call
// setup, every state transition and every metrics sweep all take it. Sixty-four
// shards means two random calls collide about 1.5% of the time, and the shard
// array is 64 pointers — small enough to stay resident, large enough that
// contention stops being the thing you measure.
//
// A power of two so the modulo is a mask.
const registryShards = 64

type registryShard struct {
	mu    sync.RWMutex
	calls map[CallID]*CallSession
}

// CallRegistry holds every live session.
//
// # Sharded by call identifier
//
// Two calls on different shards contend for nothing — not for lookup, not for
// registration, not for removal. This is what makes "thousands of concurrent
// sessions" a property of the design rather than a hope.
//
// # It owns nothing else
//
// The registry stores and finds sessions. It does not transition them, does not
// publish events and does not know what a provider is. Every attempt to give a
// registry a second job ends with a lock held across an event publish.
type CallRegistry struct {
	shards [registryShards]*registryShard

	// live is maintained incrementally rather than derived by walking shards.
	//
	// The Phase 10F lesson, applied before it could bite: a gauge that walks a
	// collection costs whatever the collection costs, and this one would be
	// read on every admission decision. Deriving Len() by locking 64 shards on
	// the call-setup path would be a quadratic-shaped mistake in a different
	// disguise.
	live atomic.Int64

	// total counts every call ever registered, for the lifetime rate.
	total atomic.Uint64
}

// NewCallRegistry builds an empty registry.
func NewCallRegistry() *CallRegistry {
	r := &CallRegistry{}
	for i := range r.shards {
		r.shards[i] = &registryShard{calls: make(map[CallID]*CallSession)}
	}
	return r
}

// shardFor selects a shard from a call identifier.
//
// FNV-1a over the identifier bytes. The identifier's first six bytes are a
// millisecond timestamp, so hashing on a prefix would put every call created in
// the same millisecond on one shard — exactly the burst a call storm produces.
// Hashing the whole string spreads them.
func (r *CallRegistry) shardFor(id CallID) *registryShard {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for i := 0; i < len(id); i++ {
		h ^= uint64(id[i])
		h *= prime64
	}
	return r.shards[h&(registryShards-1)]
}

// Register adds a session, refusing a duplicate identifier.
//
// Refusing rather than replacing. A duplicate call identifier means either an
// identifier collision or a provider replaying a callback; replacing would
// silently discard a live call's session and leave its goroutines orphaned,
// with no error anywhere.
func (r *CallRegistry) Register(s *CallSession) error {
	if s == nil {
		return invariant("INV-TEL-1", "cannot register a nil session")
	}
	sh := r.shardFor(s.id)

	sh.mu.Lock()
	if _, exists := sh.calls[s.id]; exists {
		sh.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrCallExists, s.id)
	}
	sh.calls[s.id] = s
	sh.mu.Unlock()

	r.live.Add(1)
	r.total.Add(1)
	return nil
}

// Get returns a session.
func (r *CallRegistry) Get(id CallID) (*CallSession, error) {
	sh := r.shardFor(id)
	sh.mu.RLock()
	s, ok := sh.calls[id]
	sh.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrCallNotFound, id)
	}
	return s, nil
}

// Has reports whether a call is registered.
func (r *CallRegistry) Has(id CallID) bool {
	sh := r.shardFor(id)
	sh.mu.RLock()
	_, ok := sh.calls[id]
	sh.mu.RUnlock()
	return ok
}

// Remove deletes a session and reports whether it was present.
func (r *CallRegistry) Remove(id CallID) bool {
	sh := r.shardFor(id)
	sh.mu.Lock()
	_, ok := sh.calls[id]
	if ok {
		delete(sh.calls, id)
	}
	sh.mu.Unlock()

	if ok {
		r.live.Add(-1)
	}
	return ok
}

// Len returns the number of live sessions. O(1).
func (r *CallRegistry) Len() int { return int(r.live.Load()) }

// Total returns how many calls have ever been registered.
func (r *CallRegistry) Total() uint64 { return r.total.Load() }

// Each calls fn for every live session.
//
// # It does not hold a shard lock while fn runs
//
// Each shard's sessions are collected under its lock, the lock is released, and
// then fn is called. A callback that transitions a call — which is what a
// timeout sweep does — would otherwise deadlock against the registration path,
// and it would deadlock only under load, when a sweep and a new call land on
// the same shard.
//
// The cost is that fn may see a session removed a moment ago. For a sweep that
// is correct: acting on a call that just ended is a no-op, because the FSM
// refuses transitions out of a terminal state.
func (r *CallRegistry) Each(fn func(*CallSession) bool) {
	for _, sh := range r.shards {
		sh.mu.RLock()
		batch := make([]*CallSession, 0, len(sh.calls))
		for _, s := range sh.calls {
			batch = append(batch, s)
		}
		sh.mu.RUnlock()

		for _, s := range batch {
			if !fn(s) {
				return
			}
		}
	}
}

// Snapshot returns every live session's snapshot, oldest first.
//
// The graceful-shutdown path: capture everything, hand it to a [SessionStore],
// then drain. Ordered deterministically so two shutdowns of the same state
// produce the same sequence, which is what makes a recovery test reproducible.
func (r *CallRegistry) Snapshot() []Snapshot {
	out := make([]Snapshot, 0, r.Len())
	r.Each(func(s *CallSession) bool {
		out = append(out, s.Snapshot())
		return true
	})
	sortSnapshots(out)
	return out
}

// IDs returns every live call identifier, sorted.
func (r *CallRegistry) IDs() []CallID {
	out := make([]CallID, 0, r.Len())
	r.Each(func(s *CallSession) bool {
		out = append(out, s.id)
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ByState returns the live session count per state.
//
// One pass, not fifteen. Used by the metrics sweep, where fifteen passes over
// thousands of sessions would be the same class of defect as Phase 10F's
// quadratic gauge.
func (r *CallRegistry) ByState() map[CallState]int {
	out := make(map[CallState]int, len(AllStates()))
	r.Each(func(s *CallSession) bool {
		out[s.State()]++
		return true
	})
	return out
}

// Find returns live sessions matching a predicate, up to limit.
//
// limit exists so a diagnostic query on a runtime holding fifty thousand calls
// cannot materialise fifty thousand pointers into an operator's terminal.
func (r *CallRegistry) Find(match func(*CallSession) bool, limit int) []*CallSession {
	var out []*CallSession
	r.Each(func(s *CallSession) bool {
		if match(s) {
			out = append(out, s)
		}
		return limit <= 0 || len(out) < limit
	})
	return out
}

// ShardDepths returns the session count per shard.
//
// Diagnostic. A registry whose load sits on a few shards is a registry whose
// hash is not spreading, and the symptom — lock contention under load — is
// otherwise very hard to attribute.
func (r *CallRegistry) ShardDepths() []int {
	out := make([]int, registryShards)
	for i, sh := range r.shards {
		sh.mu.RLock()
		out[i] = len(sh.calls)
		sh.mu.RUnlock()
	}
	return out
}

// Create builds a session and registers it.
//
// The ordinary entry point. Callers that need a specific identifier — a
// provider echoing its own call reference — use [CallRegistry.CreateWithID].
func (r *CallRegistry) Create(cc CallContext, clock rt.Clock) (*CallSession, error) {
	return r.CreateWithID(NewCallID(), NewCorrelationID(), cc, clock)
}

// CreateWithID builds a session with caller-supplied identifiers.
func (r *CallRegistry) CreateWithID(id CallID, corr CorrelationID, cc CallContext,
	clock rt.Clock) (*CallSession, error) {
	if !id.Valid() {
		return nil, invariant("INV-TEL-1",
			"call identifier %q is malformed; it must carry the call_ prefix", id)
	}
	if corr == "" {
		corr = NewCorrelationID()
	}

	s, err := newSession(id, NewSessionID(), corr, cc, StateIdle, clock)
	if err != nil {
		return nil, err
	}
	if err := r.Register(s); err != nil {
		return nil, err
	}
	return s, nil
}
