package toolruntime

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// DeriveKey computes the idempotency key for an execution.
//
// DERIVED, NEVER SUPPLIED BY A CALLER. A caller-chosen key is a caller-chosen
// bug: two different requests share a key and one silently returns the other's
// result, or one request generates a fresh key per attempt and deduplication
// never fires. Deriving it means the key is a function of what is actually
// being asked.
//
// The inputs are exactly the things that determine what will happen:
//
//	descriptor  — the pinned tool and version. A different version may do a
//	              different thing with the same arguments, so it must not
//	              share a key.
//	arguments   — fingerprinted canonically, with sorted map keys.
//	actor       — two subscribers asking the same question are two questions.
//	scope       — the caller's deduplication window, normally the correlation
//	              identifier so retries within one turn deduplicate and the
//	              same request in a later turn does not.
//
// Deliberately EXCLUDED: attempt number, execution ID, timestamps. Including
// any of them would make every retry a fresh key, which defeats the point.
func DeriveKey(d Descriptor, args Arguments, actor ActorID, scope string) IdempotencyKey {
	var buf []byte
	buf = append(buf, d.String()...)
	buf = append(buf, '|')
	buf = append(buf, actor...)
	buf = append(buf, '|')
	buf = append(buf, scope...)
	buf = append(buf, '|')
	buf = append(buf, args.canonicalBytes()...)
	return IdempotencyKey(fingerprintOf(buf))
}

// LedgerState is an entry's stage.
type LedgerState uint8

// The ledger states.
const (
	// LedgerInFlight means an execution holds the key and has not finished.
	LedgerInFlight LedgerState = iota
	// LedgerCompleted means the execution succeeded and the result is stored.
	LedgerCompleted
	// LedgerFailed means the execution failed permanently. STORED, not
	// forgotten: a caller retrying a permanently failed mutating call deserves
	// the same answer rather than a second attempt at something that cannot
	// work.
	LedgerFailed
)

// String renders the state.
func (s LedgerState) String() string {
	switch s {
	case LedgerCompleted:
		return "completed"
	case LedgerFailed:
		return "failed"
	default:
		return "in_flight"
	}
}

// LedgerEntry is one claimed execution key.
type LedgerEntry struct {
	// Key is the derived idempotency key.
	Key IdempotencyKey
	// Descriptor is what claimed it.
	Descriptor Descriptor
	// Execution is the claiming execution.
	Execution ExecutionID
	// State is the stage.
	State LedgerState
	// Result is the stored outcome, populated on completion.
	Result Result
	// Reason is a short code for a stored failure. Never the raw error text: a
	// downstream error message can contain caller content, and the ledger is
	// long-lived.
	Reason string
	// ClaimedAt, SettledAt and ExpiresAt are runtime-clock instants.
	ClaimedAt time.Time
	SettledAt time.Time
	ExpiresAt time.Time
	// Replays counts how many later executions were served from this entry.
	Replays uint64

	done chan struct{}
}

// Settled reports whether the entry has an outcome.
func (e *LedgerEntry) Settled() bool { return e.State != LedgerInFlight }

// Ledger deduplicates executions.
//
// In-memory and per-process for Phase 10D. THAT IS A REAL LIMIT AND IT IS
// STATED HERE RATHER THAN DISCOVERED IN PRODUCTION: two runtime replicas do not
// share a ledger, so exactly-once holds within a replica and at-least-once
// holds across them. Making it cross-replica means backing it with Redis or
// Aurora, which is the declared seam and Phase 10E's work. Until then the
// honest claim is "exactly-once where possible", which is the phrase the brief
// itself uses.
type Ledger struct {
	clock   rt.Clock
	metrics *Metrics
	ttl     time.Duration
	max     int

	mu      sync.Mutex
	entries map[IdempotencyKey]*LedgerEntry
	// order tracks insertion order for bounded eviction, oldest first.
	order []IdempotencyKey
}

// NewLedger builds a ledger.
//
// The TTL is a deduplication window, not a cache lifetime. It should span the
// longest plausible retry storm — a conversation plus a client's own retries —
// and no longer, because an entry that outlives its usefulness is a stored
// result that could be served to a caller who wanted fresh work.
func NewLedger(clock rt.Clock, metrics *Metrics, ttl time.Duration, max int) *Ledger {
	if clock == nil {
		clock = rt.SystemClock{}
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if max <= 0 {
		max = 10_000
	}
	return &Ledger{clock: clock, metrics: metrics, ttl: ttl, max: max,
		entries: make(map[IdempotencyKey]*LedgerEntry)}
}

// Claim takes ownership of a key, or reports the existing entry.
//
// The three outcomes are the whole design:
//
//	fresh, nil, nil      — the caller owns the key and must Settle it.
//	nil, entry, nil      — a settled entry exists; the caller replays it.
//	nil, entry, ErrDuplicate — another execution holds the key right now. The
//	                       caller may Await it.
//
// A caller that receives a fresh claim and never settles it holds the key until
// the TTL expires, which would block every duplicate for that window. The
// executor therefore settles in a defer, always.
func (l *Ledger) Claim(key IdempotencyKey, d Descriptor, exec ExecutionID) (*LedgerEntry, *LedgerEntry, error) {
	now := l.clock.Now()

	l.mu.Lock()
	defer l.mu.Unlock()
	l.evictHeadLocked(now)

	if existing, ok := l.entries[key]; ok {
		if existing.Settled() {
			existing.Replays++
			if l.metrics != nil {
				l.metrics.LedgerHits.Inc(string(d.Tool))
			}
			return nil, existing, nil
		}
		if l.metrics != nil {
			l.metrics.LedgerHits.Inc(string(d.Tool))
		}
		return nil, existing, ErrDuplicate
	}

	entry := &LedgerEntry{
		Key: key, Descriptor: d, Execution: exec, State: LedgerInFlight,
		ClaimedAt: now, ExpiresAt: now.Add(l.ttl), done: make(chan struct{}),
	}
	l.entries[key] = entry
	l.order = append(l.order, key)
	l.evictOverflowLocked()

	if l.metrics != nil {
		l.metrics.LedgerMisses.Inc(string(d.Tool))
		l.metrics.LedgerSize.Set(float64(len(l.entries)))
	}
	return entry, nil, nil
}

// Settle records an outcome and wakes anyone awaiting the key.
func (l *Ledger) Settle(key IdempotencyKey, res Result, reason string, ok bool) {
	l.mu.Lock()
	entry, exists := l.entries[key]
	if !exists || entry.Settled() {
		l.mu.Unlock()
		return
	}
	entry.State = LedgerFailed
	if ok {
		entry.State = LedgerCompleted
		entry.Result = res.Clone()
	}
	entry.Reason = reason
	entry.SettledAt = l.clock.Now()
	done := entry.done
	l.mu.Unlock()

	close(done)
}

// Release abandons a claim without recording an outcome.
//
// Used when an execution never actually invoked the tool — a permission denial,
// a budget refusal. Storing those as failures would mean a later attempt with
// a fixed grant is served the old denial, which is the opposite of helpful.
func (l *Ledger) Release(key IdempotencyKey) {
	l.mu.Lock()
	entry, exists := l.entries[key]
	if !exists || entry.Settled() {
		l.mu.Unlock()
		return
	}
	delete(l.entries, key)
	for i, k := range l.order {
		if k == key {
			l.order = append(l.order[:i], l.order[i+1:]...)
			break
		}
	}
	done := entry.done
	l.mu.Unlock()

	close(done)
}

// Await blocks until an in-flight entry settles, the context ends, or the
// deadline passes.
//
// This is what turns a concurrent duplicate into a shared result rather than a
// second invocation. Two identical requests arriving at once — which is exactly
// what a client retry storm looks like — produce one tool call and two answers.
func (l *Ledger) Await(ctx context.Context, e *LedgerEntry) (*LedgerEntry, error) {
	select {
	case <-e.done:
		l.mu.Lock()
		defer l.mu.Unlock()
		if current, ok := l.entries[e.Key]; ok {
			return current, nil
		}
		// Released rather than settled: the holder never invoked the tool, so
		// the awaiting caller should proceed on its own rather than inherit a
		// non-outcome.
		return nil, ErrNotRegistered
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Get returns an entry.
func (l *Ledger) Get(key IdempotencyKey) (*LedgerEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	return e, ok
}

// Len returns the live entry count.
func (l *Ledger) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// Sweep removes every expired entry and returns how many went.
//
// The FULL pass, run on the runtime's maintenance timer. Claim runs the cheap
// front-of-queue eviction instead; doing a full pass on every claim made the
// claim path O(ledger size) and turned a map insert into 88 microseconds. See
// ENGINEERING_AUDIT F4.
func (l *Ledger) Sweep() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	before := len(l.entries)
	l.evictExpiredLocked(l.clock.Now())
	if l.metrics != nil {
		l.metrics.LedgerSize.Set(float64(len(l.entries)))
	}
	return before - len(l.entries)
}

// evictHeadLocked drops expired entries from the front of the queue.
//
// Amortised O(1) per claim. Entries are appended in claim order and every entry
// takes the same TTL, so the queue is ordered by expiry: once the head has not
// expired, nothing behind it has either. It stops at the first entry it cannot
// evict — including an in-flight one, which is never evicted at all — and
// leaves the rest to Sweep. A conservative stop is the right trade here: the
// cost of stopping early is a slightly larger ledger for a few seconds, and the
// cost of not stopping is walking the whole thing on the request path.
func (l *Ledger) evictHeadLocked(now time.Time) {
	const maxPerClaim = 8

	dropped := 0
	for dropped < maxPerClaim && len(l.order) > 0 {
		key := l.order[0]
		e, ok := l.entries[key]
		if !ok {
			l.order = l.order[1:]
			continue
		}
		if !e.Settled() || now.Before(e.ExpiresAt) {
			return
		}
		delete(l.entries, key)
		l.order = l.order[1:]
		dropped++
	}
}

// evictExpiredLocked drops settled entries past their expiry.
//
// IN-FLIGHT ENTRIES ARE NEVER EVICTED, however old. Evicting one would release
// a key an execution still holds, and a duplicate would then run the same
// mutating call a second time — the precise failure the ledger exists to
// prevent. A stuck in-flight entry is a bug to find, not a row to reclaim.
func (l *Ledger) evictExpiredLocked(now time.Time) {
	if len(l.entries) == 0 {
		return
	}
	kept := l.order[:0]
	for _, key := range l.order {
		e, ok := l.entries[key]
		if !ok {
			continue
		}
		if e.Settled() && !now.Before(e.ExpiresAt) {
			delete(l.entries, key)
			continue
		}
		kept = append(kept, key)
	}
	l.order = kept
}

// evictOverflowLocked drops the oldest settled entries when over capacity.
//
// It walks only as far as it must: it stops as soon as it has freed the
// overflow, rather than rebuilding the whole queue. The first version rebuilt
// it every time, which made a claim against a full ledger O(ledger size) — and
// since the ledger sits at its cap in steady state, that is the normal case
// rather than an edge one. See ENGINEERING_AUDIT F4.
//
// In-flight entries encountered on the way are preserved in order and never
// counted towards the overflow, for the same reason expiry never evicts them:
// releasing a key an execution still holds would let a duplicate run the same
// mutating call a second time.
func (l *Ledger) evictOverflowLocked() {
	over := len(l.entries) - l.max
	if over <= 0 {
		return
	}

	held, i := 0, 0
	for over > 0 && i < len(l.order) {
		key := l.order[i]
		e, ok := l.entries[key]
		switch {
		case !ok:
			// Already gone; drop the stale queue slot.
		case !e.Settled():
			l.order[held] = key
			held++
		default:
			delete(l.entries, key)
			over--
		}
		i++
	}
	// Compact: the in-flight entries kept from the prefix, then the untouched
	// tail. held <= i always, so this copies forward and the overlap is safe.
	l.order = append(l.order[:held], l.order[i:]...)
}

// Entries returns a snapshot, sorted by claim time then key.
func (l *Ledger) Entries() []LedgerEntry {
	l.mu.Lock()
	out := make([]LedgerEntry, 0, len(l.entries))
	for _, e := range l.entries {
		c := *e
		c.done = nil
		c.Result = e.Result.Clone()
		out = append(out, c)
	}
	l.mu.Unlock()

	sort.Slice(out, func(i, j int) bool {
		if !out[i].ClaimedAt.Equal(out[j].ClaimedAt) {
			return out[i].ClaimedAt.Before(out[j].ClaimedAt)
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// String renders an entry for diagnostics.
func (e *LedgerEntry) String() string {
	return fmt.Sprintf("%s %s %s replays=%d", e.Key, e.Descriptor, e.State, e.Replays)
}
