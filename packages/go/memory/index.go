package memory

import (
	"hash/maphash"
	"sort"
	"sync"
	"time"
)

// Reserved attribute names.
//
// The dedicated conversation, session, contact and business indexes are
// specialisations of the secondary index over these names. That is the whole
// trick: rather than four bespoke structures with four sets of maintenance
// bugs, there is one attribute index and four reserved keys into it.
//
// A record participates in an index by carrying the attribute. Nothing is
// inferred, and the engine never parses a payload to discover a reference.
const (
	// AttrConversation binds a record to a conversation.
	AttrConversation = "conversation_id"
	// AttrSession binds a record to a session.
	AttrSession = "session_id"
	// AttrContact binds a record to a third party.
	AttrContact = "contact_id"
	// AttrBusiness binds a record to an organisation.
	AttrBusiness = "business_id"
)

// reservedAttributes lists the attribute names with dedicated lookups.
func reservedAttributes() []string {
	return []string{AttrConversation, AttrSession, AttrContact, AttrBusiness}
}

// IndexConfig tunes the index layer.
type IndexConfig struct {
	// Shards is the primary index's lock-stripe count. Rounded up to a power
	// of two so shard selection is a mask rather than a modulo.
	Shards int

	// TimeBucket is the granularity of the time index.
	//
	// Bucketed rather than a sorted slice deliberately. A sorted structure
	// gives O(log n) lookup and O(n) insert; at this engine's write rate the
	// insert dominates. Bucketing gives O(1) insert and a range query that
	// scans only the buckets it overlaps, which matches the query this index
	// actually serves: "what happened between these two instants".
	TimeBucket time.Duration

	// MaxKeysPerAttributeValue bounds one posting list. A runaway value — an
	// attribute accidentally set to a constant across a million records —
	// otherwise turns a lookup into a full scan.
	MaxKeysPerAttributeValue int
}

// DefaultIndexConfig returns the configuration used unless overridden.
func DefaultIndexConfig() IndexConfig {
	return IndexConfig{
		Shards:                   16,
		TimeBucket:               time.Minute,
		MaxKeysPerAttributeValue: 10_000,
	}
}

func (c IndexConfig) validate() []string {
	var p []string
	if c.Shards <= 0 {
		p = append(p, "index: Shards must be positive")
	}
	if c.TimeBucket <= 0 {
		p = append(p, "index: TimeBucket must be positive")
	}
	if c.MaxKeysPerAttributeValue <= 0 {
		p = append(p, "index: MaxKeysPerAttributeValue must be positive")
	}
	return p
}

// primaryShard is one lock stripe of the primary index.
type primaryShard struct {
	mu      sync.RWMutex
	records map[Key]*Record
}

// Index is the composite index layer.
//
// Seven logical indexes over three physical structures:
//
//	Primary       — sharded map, Key -> Record
//	Secondary     — attribute name -> value -> keys   (also serves the
//	                conversation, session, contact and business lookups)
//	Time          — bucketed timestamp -> keys
//
// There is no vector index and no embedding. The access patterns this platform
// has are exact-match, reference lookup and time range; none of them is a
// nearest-neighbour problem, and adding a vector store to serve them would be
// answering a question nobody asked.
type Index struct {
	cfg   IndexConfig
	seed  maphash.Seed
	mask  uint64
	shard []*primaryShard

	// Secondary and time indexes share one lock. They are written together on
	// every store and read together on most queries, so separate locks would
	// mean acquiring both in a fixed order everywhere — more contention, not
	// less, plus a deadlock class that does not currently exist.
	auxMu     sync.RWMutex
	secondary map[string]map[string][]Key
	timeline  map[int64][]Key

	count int64
}

// NewIndex constructs the index layer.
func NewIndex(cfg IndexConfig) (*Index, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	n := nextPow2(cfg.Shards)
	idx := &Index{
		cfg:       cfg,
		seed:      maphash.MakeSeed(),
		mask:      uint64(n - 1),
		shard:     make([]*primaryShard, n),
		secondary: make(map[string]map[string][]Key),
		timeline:  make(map[int64][]Key),
	}
	for i := range idx.shard {
		idx.shard[i] = &primaryShard{records: make(map[Key]*Record)}
	}
	return idx, nil
}

// shardFor selects the primary stripe for a key.
func (i *Index) shardFor(k Key) *primaryShard {
	var h maphash.Hash
	h.SetSeed(i.seed)
	_, _ = h.WriteString(string(k.Subject))
	_, _ = h.WriteString(k.Kind.String())
	_, _ = h.WriteString(k.Name)
	return i.shard[h.Sum64()&i.mask]
}

// bucketFor returns the time-index bucket for an instant.
func (i *Index) bucketFor(t time.Time) int64 {
	return t.UnixNano() / int64(i.cfg.TimeBucket)
}

// Insert adds or replaces a record across every index.
//
// The record pointer is stored directly and must not be mutated by the caller
// afterwards. Every read path clones, so the only writer of a stored record is
// the store itself under its own lock.
func (i *Index) Insert(r *Record) {
	sh := i.shardFor(r.Key)

	sh.mu.Lock()
	previous, existed := sh.records[r.Key]
	sh.records[r.Key] = r
	sh.mu.Unlock()

	i.auxMu.Lock()
	defer i.auxMu.Unlock()

	// A replacement must retract the old entries first, or an attribute that
	// changed value leaves a stale posting that resolves to a record which no
	// longer carries it — the classic secondary-index leak.
	if existed && previous != nil {
		i.retractLocked(previous)
	} else {
		i.count++
	}
	i.projectLocked(r)
}

// projectLocked adds a record's entries to the auxiliary indexes.
func (i *Index) projectLocked(r *Record) {
	for _, name := range r.attributeNames() {
		value := r.Value.Attributes[name]
		byValue, ok := i.secondary[name]
		if !ok {
			byValue = make(map[string][]Key)
			i.secondary[name] = byValue
		}
		if len(byValue[value]) < i.cfg.MaxKeysPerAttributeValue {
			byValue[value] = append(byValue[value], r.Key)
		}
	}
	b := i.bucketFor(r.CreatedAt)
	i.timeline[b] = append(i.timeline[b], r.Key)
}

// retractLocked removes a record's entries from the auxiliary indexes.
func (i *Index) retractLocked(r *Record) {
	for _, name := range r.attributeNames() {
		value := r.Value.Attributes[name]
		if byValue, ok := i.secondary[name]; ok {
			byValue[value] = removeKey(byValue[value], r.Key)
			if len(byValue[value]) == 0 {
				delete(byValue, value)
			}
			if len(byValue) == 0 {
				delete(i.secondary, name)
			}
		}
	}
	b := i.bucketFor(r.CreatedAt)
	if keys := i.timeline[b]; keys != nil {
		i.timeline[b] = removeKey(keys, r.Key)
		if len(i.timeline[b]) == 0 {
			delete(i.timeline, b)
		}
	}
}

// removeKey deletes a key from a posting list, preserving order.
//
// Order is preserved because posting lists are returned in insertion order and
// callers rely on that for stable, reproducible results. Swap-remove would be
// faster and would make the same query return different orderings over time.
func removeKey(keys []Key, k Key) []Key {
	for idx, existing := range keys {
		if existing == k {
			return append(keys[:idx], keys[idx+1:]...)
		}
	}
	return keys
}

// Get returns a CLONE of the stored record.
//
// It clones UNDER the read lock, and that is a correctness requirement rather
// than a convenience. The first version returned the live pointer and released
// the lock, so every caller then read fields — and Clone itself read fields —
// with no lock held, racing any concurrent Mutate. Returning a pointer out of a
// lock is returning a promise the lock cannot keep.
//
// Callers needing only scalars for filtering and sorting use [Index.Meta],
// which copies a handful of fields instead of a payload.
func (i *Index) Get(k Key) (*Record, bool) {
	sh := i.shardFor(k)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	r, ok := sh.records[k]
	if !ok {
		return nil, false
	}
	return r.Clone(), true
}

// Meta is the scalar projection of a record, for filtering and sorting without
// copying a payload.
type Meta struct {
	Key         Key
	Tier        Tier
	Sensitivity Sensitivity
	State       State
	CreatedAt   time.Time
	AccessedAt  time.Time
	ExpiresAt   time.Time
	AccessCount uint64
	// Attr is the value of the one attribute the caller asked about, if any.
	Attr string
}

// Meta returns a record's scalar projection, plus the value of one named
// attribute.
//
// This exists because retrieval used to clone every candidate in order to sort
// and then discard most of them — 910 allocations to return 20 records out of
// 200. Sorting needs four timestamps and a count, not a payload.
func (i *Index) Meta(k Key, attr string) (Meta, bool) {
	sh := i.shardFor(k)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	r, ok := sh.records[k]
	if !ok {
		return Meta{}, false
	}
	m := Meta{
		Key: r.Key, Tier: r.Tier, Sensitivity: r.Sensitivity, State: r.State,
		CreatedAt: r.CreatedAt, AccessedAt: r.AccessedAt,
		ExpiresAt: r.ExpiresAt, AccessCount: r.AccessCount,
	}
	if attr != "" && r.Value.Attributes != nil {
		m.Attr = r.Value.Attributes[attr]
	}
	return m, true
}

// Expired reports whether the projected record has passed its expiry.
func (m Meta) Expired(now time.Time) bool {
	return !m.ExpiresAt.IsZero() && !now.Before(m.ExpiresAt)
}

// Delete removes a record from every index.
func (i *Index) Delete(k Key) bool {
	sh := i.shardFor(k)

	sh.mu.Lock()
	r, ok := sh.records[k]
	if ok {
		delete(sh.records, k)
	}
	sh.mu.Unlock()

	if !ok {
		return false
	}

	i.auxMu.Lock()
	i.retractLocked(r)
	i.count--
	i.auxMu.Unlock()
	return true
}

// Count returns the number of indexed records.
func (i *Index) Count() int {
	i.auxMu.RLock()
	defer i.auxMu.RUnlock()
	return int(i.count)
}

// ---------------------------------------------------------------------------
// The seven lookups
// ---------------------------------------------------------------------------

// ByKey is the primary index lookup.
func (i *Index) ByKey(k Key) (*Record, bool) { return i.Get(k) }

// ByAttribute is the secondary index lookup.
//
// Returns keys in insertion order. The caller resolves them, because a posting
// list is cheap to return and a materialised record set is not.
func (i *Index) ByAttribute(name, value string) []Key {
	i.auxMu.RLock()
	defer i.auxMu.RUnlock()

	byValue, ok := i.secondary[name]
	if !ok {
		return nil
	}
	keys := byValue[value]
	out := make([]Key, len(keys))
	copy(out, keys)
	return out
}

// ByConversation returns records bound to a conversation.
func (i *Index) ByConversation(id string) []Key { return i.ByAttribute(AttrConversation, id) }

// BySession returns records bound to a session.
func (i *Index) BySession(id string) []Key { return i.ByAttribute(AttrSession, id) }

// ByContact returns records bound to a third party.
func (i *Index) ByContact(id string) []Key { return i.ByAttribute(AttrContact, id) }

// ByBusiness returns records bound to an organisation.
func (i *Index) ByBusiness(id string) []Key { return i.ByAttribute(AttrBusiness, id) }

// ByTimeRange returns records created within [from, to).
//
// Scans only the buckets the range overlaps. A range wider than the whole
// index degenerates to a full scan, which is correct and is bounded by the
// caller's own range choice.
func (i *Index) ByTimeRange(from, to time.Time) []Key {
	if !from.Before(to) {
		return nil
	}
	first := i.bucketFor(from)
	last := i.bucketFor(to)

	i.auxMu.RLock()
	defer i.auxMu.RUnlock()

	var out []Key
	for b := first; b <= last; b++ {
		out = append(out, i.timeline[b]...)
	}
	// Buckets are visited in order but records within a bucket are in
	// insertion order, so the result is only coarsely sorted. Callers wanting
	// strict ordering sort by the resolved record's timestamp — the index does
	// not sort, because most callers filter first and sorting the discarded
	// majority is wasted work.
	return out
}

// BySubject returns every key belonging to a subject.
//
// This is a full scan of the primary index, and it is deliberately not indexed.
// A subject index would be maintained on every write to serve a query used only
// by erasure and by diagnostics — both of which are rare and neither of which
// is latency-sensitive. Paying on every write for a query that runs on erasure
// is the wrong trade.
func (i *Index) BySubject(subject SubjectID) []Key {
	var out []Key
	for _, sh := range i.shard {
		sh.mu.RLock()
		for k := range sh.records {
			if k.Subject == subject {
				out = append(out, k)
			}
		}
		sh.mu.RUnlock()
	}
	sort.Slice(out, func(a, b int) bool { return out[a].String() < out[b].String() })
	return out
}

// ByKind returns every key of a kind for a subject.
func (i *Index) ByKind(subject SubjectID, kind Kind) []Key {
	var out []Key
	for _, sh := range i.shard {
		sh.mu.RLock()
		for k := range sh.records {
			if k.Subject == subject && k.Kind == kind {
				out = append(out, k)
			}
		}
		sh.mu.RUnlock()
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// Walk visits every record. The callback must not mutate what it receives and
// must not call back into the index — it runs under a shard read lock.
//
// Returning false stops the walk early.
func (i *Index) Walk(fn func(*Record) bool) {
	for _, sh := range i.shard {
		sh.mu.RLock()
		for _, r := range sh.records {
			if !fn(r) {
				sh.mu.RUnlock()
				return
			}
		}
		sh.mu.RUnlock()
	}
}

// Stats describes index occupancy, for diagnostics and the metrics sweep.
type Stats struct {
	// Records is the total indexed.
	Records int
	// AttributeNames is how many distinct attributes are indexed.
	AttributeNames int
	// PostingLists is how many (name, value) pairs exist.
	PostingLists int
	// TimeBuckets is how many buckets are occupied.
	TimeBuckets int
	// LargestPostingList is the biggest single posting list, which is the
	// early warning for an attribute being used as a constant.
	LargestPostingList int
}

// Stats returns index occupancy.
func (i *Index) Stats() Stats {
	i.auxMu.RLock()
	defer i.auxMu.RUnlock()

	s := Stats{
		Records:        int(i.count),
		AttributeNames: len(i.secondary),
		TimeBuckets:    len(i.timeline),
	}
	for _, byValue := range i.secondary {
		s.PostingLists += len(byValue)
		for _, keys := range byValue {
			if len(keys) > s.LargestPostingList {
				s.LargestPostingList = len(keys)
			}
		}
	}
	return s
}

// nextPow2 rounds up to a power of two, minimum 1. Shard counts are powers of
// two so shard selection is a mask rather than a modulo, which is measurably
// cheaper on a path taken for every lookup.
func nextPow2(n int) int {
	if n <= 1 {
		return 1
	}
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}
