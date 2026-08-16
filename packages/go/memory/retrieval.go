package memory

import (
	"sort"
	"time"
)

// Order names how results are sorted.
type Order int

const (
	// OrderRecent is newest first by creation. The default, because a memory
	// layer is asked "what do you know lately" far more than anything else.
	OrderRecent Order = iota
	// OrderOldest is oldest first, for reconstructing a history.
	OrderOldest
	// OrderAccessed is most-recently-read first.
	OrderAccessed
	// OrderFrequency is most-read first.
	OrderFrequency
	// OrderKey is lexicographic by key — the deterministic default for tests
	// and for any caller that needs a stable page boundary.
	OrderKey
)

// String renders the order for metric labels.
func (o Order) String() string {
	switch o {
	case OrderOldest:
		return "oldest"
	case OrderAccessed:
		return "accessed"
	case OrderFrequency:
		return "frequency"
	case OrderKey:
		return "key"
	default:
		return "recent"
	}
}

// Query describes a retrieval.
//
// One struct for every lookup rather than eight signatures. A caller states
// what it wants; the retriever picks the cheapest index that can serve it. That
// is what makes the layer provider-agnostic: swapping the index implementation
// changes nothing a caller wrote.
type Query struct {
	// Subject scopes the query. Required for everything except a pure
	// reference lookup, because an unscoped query over a shared memory layer
	// is a cross-subject read waiting to happen.
	Subject SubjectID

	// Kinds filters by kind. Empty means every kind.
	Kinds []Kind

	// Tiers filters by tier. Empty means every tier.
	Tiers []Tier

	// Name is an exact record name. When set, the query is a primary-index
	// lookup and everything else is ignored.
	Name string

	// Attribute and Value select a secondary-index posting list.
	Attribute string
	Value     string

	// From and To bound the time index. Zero values disable the bound.
	From, To time.Time

	// MaxSensitivity refuses records above a ceiling. The caller states what it
	// is entitled to see, rather than filtering afterwards and occasionally
	// forgetting.
	MaxSensitivity Sensitivity

	// IncludeExpired returns records past their TTL that have not been
	// reclaimed. Off by default.
	IncludeExpired bool

	// Order sorts the result.
	Order Order

	// Limit bounds the result. Zero means the retriever's default.
	Limit int

	// Offset skips results, for paging.
	Offset int
}

// Result carries retrieved records and how they were found.
type Result struct {
	// Records is the result set.
	Records []*Record
	// Total is how many matched before Limit and Offset.
	Total int
	// Index names which index served the query, for diagnosis.
	Index string
	// Scanned is how many candidates were examined. A large Scanned with a
	// small Total is the signal that a query is using the wrong index.
	Scanned int
	// Truncated reports that Limit cut the result.
	Truncated bool
}

// Retriever is the retrieval interface.
//
// PROVIDER AGNOSTIC AND EMBEDDING FREE. Every method below is exact match,
// reference lookup or time range. There is no similarity, no ranking by
// relevance and no vector — the Phase 10C brief excludes embeddings, and the
// access patterns this platform has do not need them.
//
// When embeddings arrive they arrive as an additional implementation of this
// interface, not as a change to it.
type Retriever interface {
	// Get is an exact-match lookup by key.
	Get(k Key, actor string) (*Record, error)
	// Recent returns the most recent records for a subject.
	Recent(subject SubjectID, limit int) (Result, error)
	// ByConversation returns records bound to a conversation.
	ByConversation(id string, q Query) (Result, error)
	// BySession returns records bound to a session.
	BySession(id string, q Query) (Result, error)
	// Preferences returns a subject's stated choices.
	Preferences(subject SubjectID) (Result, error)
	// ByContact returns records about a third party.
	ByContact(id string, q Query) (Result, error)
	// ByBusiness returns an organisation's records.
	ByBusiness(id string, q Query) (Result, error)
	// Between returns records created in a time range.
	Between(subject SubjectID, from, to time.Time, q Query) (Result, error)
	// Search runs an arbitrary query, choosing an index.
	Search(q Query, actor string) (Result, error)
}

// storeRetriever implements Retriever over a Store.
type storeRetriever struct {
	store   *Store
	metrics *Metrics
	limit   int
}

// NewRetriever returns a Retriever over a store.
func NewRetriever(s *Store, defaultLimit int) Retriever {
	if defaultLimit <= 0 {
		defaultLimit = 50
	}
	return &storeRetriever{store: s, metrics: s.metrics, limit: defaultLimit}
}

// Get is an exact-match lookup.
func (r *storeRetriever) Get(k Key, actor string) (*Record, error) {
	return r.store.Retrieve(k, actor)
}

// Recent returns the most recent records for a subject.
func (r *storeRetriever) Recent(subject SubjectID, limit int) (Result, error) {
	return r.Search(Query{Subject: subject, Order: OrderRecent, Limit: limit,
		MaxSensitivity: Sensitive}, "")
}

// ByConversation returns records bound to a conversation.
func (r *storeRetriever) ByConversation(id string, q Query) (Result, error) {
	q.Attribute, q.Value = AttrConversation, id
	return r.Search(q, "")
}

// BySession returns records bound to a session.
func (r *storeRetriever) BySession(id string, q Query) (Result, error) {
	q.Attribute, q.Value = AttrSession, id
	return r.Search(q, "")
}

// Preferences returns a subject's stated choices.
func (r *storeRetriever) Preferences(subject SubjectID) (Result, error) {
	return r.Search(Query{Subject: subject, Kinds: []Kind{KindPreference},
		Order: OrderKey, MaxSensitivity: Sensitive}, "")
}

// ByContact returns records about a third party.
func (r *storeRetriever) ByContact(id string, q Query) (Result, error) {
	q.Attribute, q.Value = AttrContact, id
	return r.Search(q, "")
}

// ByBusiness returns an organisation's records.
func (r *storeRetriever) ByBusiness(id string, q Query) (Result, error) {
	q.Attribute, q.Value = AttrBusiness, id
	return r.Search(q, "")
}

// Between returns records created in a time range.
func (r *storeRetriever) Between(subject SubjectID, from, to time.Time, q Query) (Result, error) {
	q.Subject, q.From, q.To = subject, from, to
	return r.Search(q, "")
}

// Search runs a query, choosing the cheapest index that can serve it.
//
// PLAN SELECTION, in order of selectivity:
//
//  1. Name set          → primary index, one lookup
//  2. Attribute set     → secondary posting list
//  3. Time range set    → time buckets
//  4. Subject only      → primary scan, filtered
//
// The order is the plan, and it is fixed rather than cost-based. A cost model
// over four access paths would be more machinery than the choice deserves, and
// a fixed order is reproducible — the same query always uses the same index,
// which is what makes Result.Index useful in a diagnosis.
func (r *storeRetriever) Search(q Query, actor string) (Result, error) {
	start := r.store.clock.Now()
	limit := q.Limit
	if limit <= 0 {
		limit = r.limit
	}

	var (
		candidates []Key
		indexUsed  string
	)

	switch {
	case q.Name != "" && q.Subject != "":
		kind := KindConversation
		if len(q.Kinds) > 0 {
			kind = q.Kinds[0]
		}
		candidates = []Key{{Subject: q.Subject, Kind: kind, Name: q.Name}}
		indexUsed = "primary"

	case q.Attribute != "":
		candidates = r.store.index.ByAttribute(q.Attribute, q.Value)
		indexUsed = "secondary:" + q.Attribute

	case !q.From.IsZero() || !q.To.IsZero():
		from, to := q.From, q.To
		if from.IsZero() {
			from = time.Unix(0, 0)
		}
		if to.IsZero() {
			to = r.store.clock.Now()
		}
		candidates = r.store.index.ByTimeRange(from, to)
		indexUsed = "time"

	case q.Subject != "":
		if len(q.Kinds) == 1 {
			candidates = r.store.index.ByKind(q.Subject, q.Kinds[0])
			indexUsed = "primary_scan:kind"
		} else {
			candidates = r.store.index.BySubject(q.Subject)
			indexUsed = "primary_scan:subject"
		}

	default:
		// An unscoped query would read across every subject in the engine.
		// Refusing is the only safe answer: a memory layer shared by several
		// subjects must never serve a query that names none of them.
		return Result{}, invariant("INV-MEM-5",
			"a query must be scoped by subject, attribute, name or time range")
	}

	r.metrics.IndexScans.Inc(indexUsed)

	// FILTER AND SORT ON METADATA, MATERIALISE ONLY THE SURVIVORS.
	//
	// The first version cloned every candidate, sorted the clones and threw most
	// away — 910 allocations to return 20 records out of 200. Sorting needs four
	// timestamps and a count, not a payload. Index.Meta copies those scalars
	// under the shard lock; the full record is fetched only for the records the
	// caller actually receives.
	now := r.store.clock.Now()
	metas := make([]Meta, 0, len(candidates))
	scanned := 0

	for _, k := range candidates {
		scanned++
		m, ok := r.store.index.Meta(k, q.Attribute)
		if !ok || !r.matchesMeta(m, q, now) {
			continue
		}
		metas = append(metas, m)
	}

	sortMetas(metas, q.Order)
	total := len(metas)

	if q.Offset > 0 {
		if q.Offset >= len(metas) {
			metas = nil
		} else {
			metas = metas[q.Offset:]
		}
	}
	truncated := false
	if len(metas) > limit {
		metas = metas[:limit]
		truncated = true
	}

	matched := make([]*Record, 0, len(metas))
	for _, m := range metas {
		rec, ok := r.store.index.Get(m.Key)
		if !ok {
			continue // deleted between projection and materialisation
		}
		matched = append(matched, rec)
	}

	// Audit after filtering, so an audit entry is written only for records the
	// caller actually received.
	for _, rec := range matched {
		r.store.auditRead(rec, "search", actor, true, indexUsed)
	}

	lookup := indexLabel(indexUsed)
	if total > 0 {
		r.metrics.Hits.Inc(lookup)
	} else {
		r.metrics.Misses.Inc(lookup, "no_match")
	}
	r.metrics.RetrieveLatency.Observe(r.store.clock.Since(start).Seconds(), lookup)

	return Result{Records: matched, Total: total, Index: indexUsed,
		Scanned: scanned, Truncated: truncated}, nil
}

// matchesMeta applies every filter a query carries, against a scalar
// projection rather than a materialised record.
func (r *storeRetriever) matchesMeta(m Meta, q Query, now time.Time) bool {
	if q.Subject != "" && m.Key.Subject != q.Subject {
		return false
	}
	if len(q.Kinds) > 0 && !containsKind(q.Kinds, m.Key.Kind) {
		return false
	}
	if len(q.Tiers) > 0 && !containsTier(q.Tiers, m.Tier) {
		return false
	}
	if q.Attribute != "" && m.Attr != q.Value {
		return false
	}
	if !q.From.IsZero() && m.CreatedAt.Before(q.From) {
		return false
	}
	if !q.To.IsZero() && !m.CreatedAt.Before(q.To) {
		return false
	}
	// MaxSensitivity defaults to Public, so a caller that forgets to state a
	// ceiling gets the least, not the most. Fail closed.
	if m.Sensitivity > q.MaxSensitivity {
		return false
	}
	if !m.State.Readable() {
		return false
	}
	if m.Expired(now) && !q.IncludeExpired {
		return false
	}
	return true
}

// sortMetas orders a result set deterministically.
//
// Every comparator falls back to the key, so two records with identical
// timestamps or access counts always resolve the same way. Without that tie-
// break the same query returns different orderings between runs, which makes
// paging incorrect and a test flaky.
func sortMetas(recs []Meta, order Order) {
	sort.SliceStable(recs, func(i, j int) bool {
		a, b := recs[i], recs[j]
		switch order {
		case OrderOldest:
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.Before(b.CreatedAt)
			}
		case OrderAccessed:
			if !a.AccessedAt.Equal(b.AccessedAt) {
				return a.AccessedAt.After(b.AccessedAt)
			}
		case OrderFrequency:
			if a.AccessCount != b.AccessCount {
				return a.AccessCount > b.AccessCount
			}
		case OrderKey:
			return a.Key.String() < b.Key.String()
		default: // OrderRecent
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.After(b.CreatedAt)
			}
		}
		return a.Key.String() < b.Key.String()
	})
}

func containsKind(ks []Kind, k Kind) bool {
	for _, x := range ks {
		if x == k {
			return true
		}
	}
	return false
}

func containsTier(ts []Tier, t Tier) bool {
	for _, x := range ts {
		if x == t {
			return true
		}
	}
	return false
}

// indexLabel reduces an index name to a bounded metric label. Without this the
// secondary-index label would carry an arbitrary attribute name and the metric
// cardinality would follow whatever a caller invented.
func indexLabel(index string) string {
	switch {
	case index == "primary":
		return "exact"
	case index == "time":
		return "temporal"
	case len(index) > 10 && index[:10] == "secondary:":
		switch index[10:] {
		case AttrConversation:
			return "conversation"
		case AttrSession:
			return "session"
		case AttrContact:
			return "contact"
		case AttrBusiness:
			return "business"
		default:
			return "secondary"
		}
	default:
		return "scan"
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
