package audiointel

import (
	"hash/maphash"
	"sync"
	"sync/atomic"
)

// registryShards is how many independent maps the registry is split across.
//
// A single mutex-guarded map is the obvious implementation and it becomes the
// bottleneck at a few hundred concurrent sessions: every open, close and lookup
// serialises on it. Thirty-two shards means two sessions contend only when
// their identifiers hash to the same one, which at any realistic session count
// is rare.
//
// The same figure Phase 11B chose, for the same reason and at the same scale.
const registryShards = 32

type registryShard struct {
	mu       sync.RWMutex
	sessions map[SessionID]*Session
}

// SessionRegistry holds the live sessions.
//
// Safe for concurrent use. Sharded, so a thousand concurrent sessions contend
// only when they land on the same shard.
type SessionRegistry struct {
	shards [registryShards]*registryShard
	seed   maphash.Seed

	live  atomic.Int64
	total atomic.Uint64
}

// NewSessionRegistry builds an empty registry.
func NewSessionRegistry() *SessionRegistry {
	r := &SessionRegistry{seed: maphash.MakeSeed()}
	for i := range r.shards {
		r.shards[i] = &registryShard{sessions: make(map[SessionID]*Session)}
	}
	return r
}

// shardFor picks the shard owning an identifier.
//
// maphash rather than a hand-rolled hash: identifiers carry a millisecond
// timestamp prefix, so sessions opened in the same instant share a long common
// prefix and a naive hash would pile them onto one shard — which is exactly the
// burst a busy moment produces.
func (r *SessionRegistry) shardFor(id SessionID) *registryShard {
	h := maphash.String(r.seed, string(id))
	return r.shards[h%registryShards]
}

// Register adds a session, refusing a duplicate identifier.
func (r *SessionRegistry) Register(s *Session) error {
	shard := r.shardFor(s.id)

	shard.mu.Lock()
	defer shard.mu.Unlock()

	if _, exists := shard.sessions[s.id]; exists {
		return ErrSessionExists
	}
	shard.sessions[s.id] = s

	r.live.Add(1)
	r.total.Add(1)
	return nil
}

// Get returns a session by identifier.
func (r *SessionRegistry) Get(id SessionID) (*Session, error) {
	shard := r.shardFor(id)

	shard.mu.RLock()
	defer shard.mu.RUnlock()

	s, ok := shard.sessions[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return s, nil
}

// Has reports whether an identifier is registered.
func (r *SessionRegistry) Has(id SessionID) bool {
	shard := r.shardFor(id)
	shard.mu.RLock()
	defer shard.mu.RUnlock()
	_, ok := shard.sessions[id]
	return ok
}

// Remove deletes a session, reporting whether it was present.
func (r *SessionRegistry) Remove(id SessionID) bool {
	shard := r.shardFor(id)

	shard.mu.Lock()
	defer shard.mu.Unlock()

	if _, ok := shard.sessions[id]; !ok {
		return false
	}
	delete(shard.sessions, id)
	r.live.Add(-1)
	return true
}

// Len returns how many sessions are live. O(1).
func (r *SessionRegistry) Len() int { return int(r.live.Load()) }

// Total returns how many sessions have ever been registered.
func (r *SessionRegistry) Total() uint64 { return r.total.Load() }

// Each calls fn for every live session until it returns false.
//
// The shard lock is held while fn runs for that shard, so fn MUST NOT call back
// into the registry — a Remove from inside Each deadlocks. Documented rather
// than defended against, because copying every session out to avoid it would
// allocate on a path a sweeper walks regularly.
func (r *SessionRegistry) Each(fn func(*Session) bool) {
	for _, shard := range r.shards {
		shard.mu.RLock()
		for _, s := range shard.sessions {
			if !fn(s) {
				shard.mu.RUnlock()
				return
			}
		}
		shard.mu.RUnlock()
	}
}

// IDs returns every live session identifier.
func (r *SessionRegistry) IDs() []SessionID {
	out := make([]SessionID, 0, r.Len())
	r.Each(func(s *Session) bool {
		out = append(out, s.id)
		return true
	})
	return out
}

// ShardDepths returns how many sessions each shard holds.
//
// For checking the hash actually spreads. A registry with every session on one
// shard has the contention profile of a single mutex while looking sharded.
func (r *SessionRegistry) ShardDepths() []int {
	out := make([]int, registryShards)
	for i, shard := range r.shards {
		shard.mu.RLock()
		out[i] = len(shard.sessions)
		shard.mu.RUnlock()
	}
	return out
}
