package conversation

import (
	"sort"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Scope bounds a context entry's lifetime and visibility.
type Scope int

const (
	// ScopeConversation lasts for this conversation and dies with it.
	ScopeConversation Scope = iota

	// ScopeSession outlives the conversation within one session — a caller who
	// hangs up and rings back within the session window.
	ScopeSession

	// ScopeBusiness is durable reference data: hours, routing, configuration.
	// Read-mostly and never written by the conversation itself.
	ScopeBusiness

	// ScopeTemporary is scratch state for one decision cycle, discarded at the
	// end of the turn. It exists so a planner can stash intermediate work
	// without polluting conversation context that will be snapshotted.
	ScopeTemporary

	// ScopeShared is visible across concurrent conversations for one subject —
	// a caller reaching two lines of the same organisation.
	ScopeShared
)

// String renders the scope for logs and metric labels.
func (s Scope) String() string {
	switch s {
	case ScopeSession:
		return "session"
	case ScopeBusiness:
		return "business"
	case ScopeTemporary:
		return "temporary"
	case ScopeShared:
		return "shared"
	default:
		return "conversation"
	}
}

// Sensitivity classifies a context value for handling.
//
// Mirrors the platform's frozen annotations.proto vocabulary rather than
// inventing a parallel scheme. The engine uses it for one thing and one thing
// only: deciding what may appear in a snapshot that leaves the process.
type Sensitivity int

const (
	// Public carries no information about a person.
	Public Sensitivity = iota
	// Internal is operational data with no personal content.
	Internal
	// Personal relates to an identifiable individual.
	Personal
	// SensitiveValue is personal data whose disclosure creates elevated harm.
	SensitiveValue
)

// String renders the sensitivity for logs.
func (s Sensitivity) String() string {
	switch s {
	case Internal:
		return "internal"
	case Personal:
		return "personal"
	case SensitiveValue:
		return "sensitive"
	default:
		return "public"
	}
}

// Entry is one context value.
type Entry struct {
	// Key identifies it within its scope.
	Key string
	// Value is the datum. Opaque to the engine.
	Value any
	// Scope bounds its lifetime.
	Scope Scope
	// Sensitivity governs whether it may leave the process in a snapshot.
	Sensitivity Sensitivity
	// SetAt is when it was written.
	SetAt time.Time
	// ExpiresAt is when it becomes invisible. Zero means no expiry.
	ExpiresAt time.Time
	// Source names what wrote it, for provenance during recovery.
	Source string
}

// Expired reports whether the entry has passed its expiry.
func (e Entry) Expired(now time.Time) bool {
	return !e.ExpiresAt.IsZero() && !now.Before(e.ExpiresAt)
}

// ContextConfig tunes the context engine.
type ContextConfig struct {
	// DefaultTTL applies to conversation-scoped entries with no explicit
	// expiry. Zero means no default expiry.
	DefaultTTL time.Duration
	// TemporaryTTL applies to ScopeTemporary entries.
	TemporaryTTL time.Duration
	// MaxEntriesPerScope bounds growth. Zero means unbounded.
	MaxEntriesPerScope int
	// MaxSnapshots bounds retained snapshots.
	MaxSnapshots int
}

// DefaultContextConfig returns the configuration used unless overridden.
func DefaultContextConfig() ContextConfig {
	return ContextConfig{
		DefaultTTL:         10 * time.Minute,
		TemporaryTTL:       30 * time.Second,
		MaxEntriesPerScope: 256,
		MaxSnapshots:       8,
	}
}

func (c ContextConfig) validate() []string {
	var p []string
	if c.DefaultTTL < 0 {
		p = append(p, "context: DefaultTTL cannot be negative")
	}
	if c.TemporaryTTL <= 0 {
		p = append(p, "context: TemporaryTTL must be positive")
	}
	if c.MaxEntriesPerScope < 0 {
		p = append(p, "context: MaxEntriesPerScope cannot be negative")
	}
	if c.MaxSnapshots < 1 {
		p = append(p, "context: MaxSnapshots must be at least 1, or recovery has nothing to restore")
	}
	return p
}

// Snapshot is a point-in-time copy of context, used for recovery.
type Snapshot struct {
	// ID orders snapshots within a conversation.
	ID uint64
	// At is when it was taken.
	At time.Time
	// Label names why. Never caller content.
	Label string
	// entries is the captured state.
	entries map[Scope]map[string]Entry
}

// ContextEngine holds conversation, session, business, temporary and shared
// context under scope, expiry and snapshot rules.
//
// Scopes are separate maps rather than one map with a scope field. That makes a
// cross-scope read a deliberate act rather than a filter someone forgot: a
// lookup names its scope, so a conversation-scoped key cannot accidentally
// resolve against business data.
type ContextEngine struct {
	cfg     ContextConfig
	clock   rt.Clock
	metrics *Metrics

	mu        sync.RWMutex
	scopes    map[Scope]map[string]Entry
	snapshots []Snapshot
	nextSnap  uint64
}

// NewContextEngine constructs a context engine.
func NewContextEngine(cfg ContextConfig, clock rt.Clock, metrics *Metrics) (*ContextEngine, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	if clock == nil {
		clock = rt.SystemClock{}
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	c := &ContextEngine{cfg: cfg, clock: clock, metrics: metrics,
		scopes: make(map[Scope]map[string]Entry, 5)}
	for _, s := range []Scope{ScopeConversation, ScopeSession, ScopeBusiness,
		ScopeTemporary, ScopeShared} {
		c.scopes[s] = make(map[string]Entry)
	}
	return c, nil
}

// Set writes an entry, applying the scope's default expiry when none is given.
func (c *ContextEngine) Set(e Entry) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.clock.Now()
	e.SetAt = now
	if e.ExpiresAt.IsZero() {
		switch e.Scope {
		case ScopeTemporary:
			e.ExpiresAt = now.Add(c.cfg.TemporaryTTL)
		case ScopeConversation, ScopeSession, ScopeShared:
			if c.cfg.DefaultTTL > 0 {
				e.ExpiresAt = now.Add(c.cfg.DefaultTTL)
			}
		case ScopeBusiness:
			// Business context is reference data and does not expire on a
			// conversation's timescale. Expiring it mid-call would make the
			// agent forget the opening hours it just used.
		}
	}

	m := c.scopes[e.Scope]
	if c.cfg.MaxEntriesPerScope > 0 {
		if _, replacing := m[e.Key]; !replacing && len(m) >= c.cfg.MaxEntriesPerScope {
			c.evictOldestLocked(e.Scope)
		}
	}
	m[e.Key] = e

	c.metrics.ContextWrites.Inc(e.Scope.String())
	c.metrics.ContextSize.Observe(float64(len(m)), e.Scope.String())
	return nil
}

// evictOldestLocked drops the oldest non-business entry in a scope.
func (c *ContextEngine) evictOldestLocked(s Scope) {
	m := c.scopes[s]
	var oldestKey string
	var oldest time.Time
	for k, e := range m {
		if oldestKey == "" || e.SetAt.Before(oldest) {
			oldestKey, oldest = k, e.SetAt
		}
	}
	if oldestKey != "" {
		delete(m, oldestKey)
		c.metrics.ContextExpired.Inc(s.String())
	}
}

// Get reads an entry from a specific scope, honouring expiry.
//
// Expiry is evaluated on read rather than by a sweeper. A per-conversation
// sweeper goroutine would be one goroutine per concurrent call for the sole
// purpose of deleting map entries nobody is looking at, and concurrency is this
// platform's capacity unit.
func (c *ContextEngine) Get(scope Scope, key string) (Entry, bool) {
	c.mu.RLock()
	e, ok := c.scopes[scope][key]
	c.mu.RUnlock()

	if !ok {
		return Entry{}, false
	}
	if e.Expired(c.clock.Now()) {
		c.mu.Lock()
		delete(c.scopes[scope], key)
		c.mu.Unlock()
		c.metrics.ContextExpired.Inc(scope.String())
		return Entry{}, false
	}
	return e, true
}

// Lookup searches scopes in precedence order and returns the first live match.
//
// Order: Temporary, Conversation, Session, Shared, Business. Most specific and
// most recent first, so a value set for this turn wins over a session default,
// which wins over organisation configuration.
func (c *ContextEngine) Lookup(key string) (Entry, bool) {
	for _, s := range []Scope{ScopeTemporary, ScopeConversation, ScopeSession,
		ScopeShared, ScopeBusiness} {
		if e, ok := c.Get(s, key); ok {
			return e, true
		}
	}
	return Entry{}, false
}

// Delete removes an entry.
func (c *ContextEngine) Delete(scope Scope, key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.scopes[scope][key]; !ok {
		return false
	}
	delete(c.scopes[scope], key)
	return true
}

// ClearScope removes every entry in a scope. Used to discard temporary state at
// the end of a decision cycle.
func (c *ContextEngine) ClearScope(scope Scope) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scopes[scope] = make(map[string]Entry)
}

// Size returns the live entry count in a scope.
func (c *ContextEngine) Size(scope Scope) int {
	now := c.clock.Now()
	c.mu.RLock()
	defer c.mu.RUnlock()
	n := 0
	for _, e := range c.scopes[scope] {
		if !e.Expired(now) {
			n++
		}
	}
	return n
}

// TakeSnapshot captures conversation-scoped state for recovery.
//
// Business and shared scopes are excluded deliberately. Business context is
// reference data that a rollback must not revert — restoring stale opening
// hours because a conversation errored would be worse than the error — and
// shared context belongs to other conversations that did not fail.
func (c *ContextEngine) TakeSnapshot(label string) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.nextSnap++
	snap := Snapshot{
		ID:      c.nextSnap,
		At:      c.clock.Now(),
		Label:   label,
		entries: make(map[Scope]map[string]Entry, 2),
	}
	for _, s := range []Scope{ScopeConversation, ScopeSession} {
		cp := make(map[string]Entry, len(c.scopes[s]))
		for k, v := range c.scopes[s] {
			cp[k] = v
		}
		snap.entries[s] = cp
	}

	c.snapshots = append(c.snapshots, snap)
	if len(c.snapshots) > c.cfg.MaxSnapshots {
		c.snapshots = c.snapshots[len(c.snapshots)-c.cfg.MaxSnapshots:]
	}
	return snap
}

// Restore reverts conversation and session scopes to a snapshot.
func (c *ContextEngine) Restore(id uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, s := range c.snapshots {
		if s.ID != id {
			continue
		}
		for scope, entries := range s.entries {
			cp := make(map[string]Entry, len(entries))
			for k, v := range entries {
				cp[k] = v
			}
			c.scopes[scope] = cp
		}
		return nil
	}
	return invariant("INV-CV-6", "snapshot %d is not retained", id)
}

// LatestSnapshot returns the most recent snapshot, if any.
func (c *ContextEngine) LatestSnapshot() (Snapshot, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.snapshots) == 0 {
		return Snapshot{}, false
	}
	return c.snapshots[len(c.snapshots)-1], true
}

// Export returns live entries at or below a sensitivity ceiling, in stable
// key order.
//
// The ceiling is the whole point. An export crossing a process boundary — a
// debug dump, a support view, a handover payload — must not carry SENSITIVE
// values merely because they were in scope. The caller states what it is
// allowed to see and the engine enforces it, rather than the caller filtering
// afterwards and occasionally forgetting.
func (c *ContextEngine) Export(max Sensitivity) []Entry {
	now := c.clock.Now()
	c.mu.RLock()
	defer c.mu.RUnlock()

	var out []Entry
	for _, scope := range []Scope{ScopeConversation, ScopeSession, ScopeBusiness,
		ScopeTemporary, ScopeShared} {
		for _, e := range c.scopes[scope] {
			if e.Expired(now) || e.Sensitivity > max {
				continue
			}
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		return out[i].Key < out[j].Key
	})
	return out
}
