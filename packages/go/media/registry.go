package media

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// registryShards is how many independent maps the registry holds.
//
// 32, half Phase 11A's 64. A media runtime carries fewer streams than a
// telephony runtime carries calls — two streams per call at most — and the
// per-stream work is far heavier, so lookup contention is a smaller share of
// the total. A power of two, so the modulo is a mask.
const registryShards = 32

type registryShard struct {
	mu      sync.RWMutex
	streams map[StreamID]*Stream
}

// MediaRegistry holds every live stream.
//
// Sharded by stream identifier, so two streams on different shards contend for
// nothing. It stores and finds streams and does nothing else: it does not
// transition them, does not publish events and does not know what a pipeline is.
// Every attempt to give a registry a second job ends with a lock held across a
// frame operation.
type MediaRegistry struct {
	shards [registryShards]*registryShard

	// live is maintained incrementally rather than derived by walking shards.
	// It is read on every admission decision, and Phase 10F's 45× lesson was
	// exactly this shape.
	live  atomic.Int64
	total atomic.Uint64
}

// NewMediaRegistry builds an empty registry.
func NewMediaRegistry() *MediaRegistry {
	r := &MediaRegistry{}
	for i := range r.shards {
		r.shards[i] = &registryShard{streams: make(map[StreamID]*Stream)}
	}
	return r
}

// shardFor selects a shard from a stream identifier.
//
// FNV-1a over the whole identifier. Hashing a prefix would put every stream
// created in the same millisecond on one shard — the timestamp prefix makes the
// first six bytes nearly identical for streams created together, which is
// exactly what a burst of calls produces.
func (r *MediaRegistry) shardFor(id StreamID) *registryShard {
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

// Register adds a stream, refusing a duplicate identifier.
//
// Refusing rather than replacing: replacing would silently discard a live
// stream's buffer and orphan its producer, with no error anywhere.
func (r *MediaRegistry) Register(s *Stream) error {
	if s == nil {
		return invariant("INV-MED-5", "cannot register a nil stream")
	}
	sh := r.shardFor(s.id)

	sh.mu.Lock()
	if _, exists := sh.streams[s.id]; exists {
		sh.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrStreamExists, s.id)
	}
	sh.streams[s.id] = s
	sh.mu.Unlock()

	r.live.Add(1)
	r.total.Add(1)
	return nil
}

// Get returns a stream.
func (r *MediaRegistry) Get(id StreamID) (*Stream, error) {
	sh := r.shardFor(id)
	sh.mu.RLock()
	s, ok := sh.streams[id]
	sh.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrStreamNotFound, id)
	}
	return s, nil
}

// Has reports whether a stream is registered.
func (r *MediaRegistry) Has(id StreamID) bool {
	sh := r.shardFor(id)
	sh.mu.RLock()
	_, ok := sh.streams[id]
	sh.mu.RUnlock()
	return ok
}

// Remove deletes a stream and reports whether it was present.
func (r *MediaRegistry) Remove(id StreamID) bool {
	sh := r.shardFor(id)
	sh.mu.Lock()
	_, ok := sh.streams[id]
	if ok {
		delete(sh.streams, id)
	}
	sh.mu.Unlock()

	if ok {
		r.live.Add(-1)
	}
	return ok
}

// Len returns the number of live streams. O(1).
func (r *MediaRegistry) Len() int { return int(r.live.Load()) }

// Total returns how many streams have ever been registered.
func (r *MediaRegistry) Total() uint64 { return r.total.Load() }

// Each calls fn for every live stream.
//
// # It does not hold a shard lock while fn runs
//
// Streams are collected under the lock, the lock is released, then fn is
// called. A callback that transitions a stream — which is what the stall sweep
// does — would otherwise deadlock against registration, and only under load.
//
// The cost is that fn may see a stream removed a moment ago. For a sweep that is
// correct: acting on a concluded stream is a no-op, because the FSM refuses
// transitions out of a terminal state.
func (r *MediaRegistry) Each(fn func(*Stream) bool) {
	for _, sh := range r.shards {
		sh.mu.RLock()
		batch := make([]*Stream, 0, len(sh.streams))
		for _, s := range sh.streams {
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

// IDs returns every live stream identifier, sorted.
func (r *MediaRegistry) IDs() []StreamID {
	out := make([]StreamID, 0, r.Len())
	r.Each(func(s *Stream) bool {
		out = append(out, s.id)
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ByState returns the live stream count per state.
//
// One pass, not nine. Nine passes over thousands of streams would be the same
// class of defect as a gauge that walks a collection on the hot path.
func (r *MediaRegistry) ByState() map[StreamState]int {
	out := make(map[StreamState]int, len(AllStates()))
	r.Each(func(s *Stream) bool {
		out[s.State()]++
		return true
	})
	return out
}

// ShardDepths returns the stream count per shard.
//
// Diagnostic. A registry whose load sits on a few shards is one whose hash is
// not spreading, and the symptom — lock contention under load — is otherwise
// very hard to attribute.
func (r *MediaRegistry) ShardDepths() []int {
	out := make([]int, registryShards)
	for i, sh := range r.shards {
		sh.mu.RLock()
		out[i] = len(sh.streams)
		sh.mu.RUnlock()
	}
	return out
}

// Create builds a stream and registers it.
func (r *MediaRegistry) Create(sc StreamContext, cfg PipelineConfig,
	clock rt.Clock) (*Stream, error) {
	return r.CreateWithID(NewStreamID(), sc, cfg, clock)
}

// CreateWithID builds a stream with a caller-supplied identifier.
func (r *MediaRegistry) CreateWithID(id StreamID, sc StreamContext,
	cfg PipelineConfig, clock rt.Clock) (*Stream, error) {
	if !id.Valid() {
		return nil, invariant("INV-MED-5",
			"stream identifier %q is malformed; it must carry the strm_ prefix", id)
	}

	s, err := newStream(id, NewSessionID(), sc, cfg, StateIdle, clock)
	if err != nil {
		return nil, err
	}
	if err := r.Register(s); err != nil {
		return nil, err
	}
	return s, nil
}
