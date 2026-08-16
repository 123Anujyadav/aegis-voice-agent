package runtime

import (
	"fmt"
	"sync"
	"unicode/utf8"
)

// TokenCounter estimates the token cost of text.
//
// It is an interface because token counting is model-specific and genuinely
// expensive to do exactly: an exact count means running the model's tokeniser,
// which is a per-vendor dependency the kernel refuses to take. A provider
// adapter that has the real tokeniser supplies one; everything else uses
// [HeuristicTokenCounter].
type TokenCounter interface {
	// Count estimates tokens in s.
	Count(s string) int
}

// HeuristicTokenCounter estimates tokens without a tokeniser.
//
// The estimate is deliberately CONSERVATIVE — it over-counts rather than
// under-counts. Under-counting causes a provider to reject an over-long request
// after the latency has already been spent, which on the screening path means a
// failed turn. Over-counting merely trims a message earlier than strictly
// necessary, which costs a little context and no time.
//
// The ratio accounts for Indic scripts, which the platform serves first
// (docs/design §Foundations). Devanagari and Tamil tokenise far less
// efficiently than Latin under byte-pair encodings — frequently two to three
// times more tokens for the same visual text — so a Latin-calibrated ratio
// would under-count exactly the languages most of our users speak.
type HeuristicTokenCounter struct {
	// BytesPerToken for predominantly-ASCII text. 3.5 is conservative against
	// the ~4 typical of English BPE.
	BytesPerToken float64

	// NonASCIIBytesPerToken for text with significant non-ASCII content.
	NonASCIIBytesPerToken float64
}

// NewHeuristicTokenCounter returns a counter with calibrated defaults.
func NewHeuristicTokenCounter() HeuristicTokenCounter {
	return HeuristicTokenCounter{
		BytesPerToken:         3.5,
		NonASCIIBytesPerToken: 1.8,
	}
}

// Count estimates tokens in s.
func (h HeuristicTokenCounter) Count(s string) int {
	if s == "" {
		return 0
	}
	bytes := len(s)
	runes := utf8.RuneCountInString(s)

	// A byte:rune ratio above ~1.2 means substantial multi-byte content.
	ratio := h.BytesPerToken
	if runes > 0 && float64(bytes)/float64(runes) > 1.2 {
		ratio = h.NonASCIIBytesPerToken
	}
	if ratio <= 0 {
		ratio = 3.5
	}

	est := int(float64(bytes)/ratio) + 1
	// Every message carries per-message framing overhead in every provider's
	// format. Four tokens is a conservative allowance; omitting it is how a
	// request that "fits" gets rejected.
	return est + 4
}

// EvictionPolicy decides what to drop when a context window overflows.
type EvictionPolicy int

const (
	// EvictOldest drops the oldest unpinned message first. The default, and
	// correct for a conversation: recent turns carry most of the meaning.
	EvictOldest EvictionPolicy = iota

	// EvictNone refuses to evict and fails instead.
	//
	// Correct where silently dropping context would change an answer's meaning
	// without the caller knowing. A caller that would rather fail than be
	// quietly wrong chooses this.
	EvictNone
)

// String renders the policy for metric labels.
func (p EvictionPolicy) String() string {
	switch p {
	case EvictNone:
		return "none"
	default:
		return "oldest"
	}
}

// ContextWindow holds the messages for a session, under a token budget.
//
// It manages SIZE, not MEANING. It does not know that the first message is an
// announcement, that a message is caller speech, or that a turn is a turn — it
// knows tokens, order and pinning. Everything semantic is the orchestration
// layer's, which is why Pinned exists: the layer above marks what must survive,
// and the runtime honours it without knowing why.
type ContextWindow struct {
	mu       sync.RWMutex
	messages []Message
	budget   int
	used     int
	counter  TokenCounter
	policy   EvictionPolicy
	metrics  *Metrics
	evicted  int
}

// NewContextWindow constructs a window with the given token budget.
func NewContextWindow(budget int, metrics *Metrics) *ContextWindow {
	if budget <= 0 {
		budget = 8192
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	return &ContextWindow{
		budget:  budget,
		counter: NewHeuristicTokenCounter(),
		policy:  EvictOldest,
		metrics: metrics,
	}
}

// SetTokenCounter replaces the counter, for a provider that has a real
// tokeniser. Existing messages are re-measured, because a mix of estimates from
// two counters produces a total that means nothing.
func (c *ContextWindow) SetTokenCounter(tc TokenCounter) {
	if tc == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counter = tc
	c.used = 0
	for i := range c.messages {
		c.messages[i].Tokens = tc.Count(c.messages[i].Content)
		c.used += c.messages[i].Tokens
	}
}

// SetPolicy sets the eviction policy.
func (c *ContextWindow) SetPolicy(p EvictionPolicy) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.policy = p
}

// SetBudget changes the token budget, evicting if the new budget is smaller.
func (c *ContextWindow) SetBudget(budget int) error {
	if budget <= 0 {
		return fmt.Errorf("%w: context budget must be positive", ErrBudgetExceeded)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.budget = budget
	return c.enforceLocked()
}

// Budget returns the token budget.
func (c *ContextWindow) Budget() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.budget
}

// Used returns the tokens currently held.
func (c *ContextWindow) Used() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.used
}

// Available returns remaining budget, never below zero.
func (c *ContextWindow) Available() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.used >= c.budget {
		return 0
	}
	return c.budget - c.used
}

// Len returns the message count.
func (c *ContextWindow) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.messages)
}

// EvictedCount returns how many messages have been evicted over the window's
// life. A rising count is the signal that a session's budget is too small for
// its traffic, which is invisible from Used alone.
func (c *ContextWindow) EvictedCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.evicted
}

// Append adds a message, evicting if necessary.
//
// A message larger than the entire budget is refused rather than evicting
// everything else to make room for it: emptying the window to admit one
// oversized message produces a request with no context, which is almost never
// what the caller wanted and is impossible to diagnose from the outside.
func (c *ContextWindow) Append(m Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if m.Tokens <= 0 {
		m.Tokens = c.counter.Count(m.Content)
	}
	if m.Tokens > c.budget {
		c.metrics.ContextOverflow.Inc()
		return fmt.Errorf("%w: message of %d tokens exceeds the whole budget of %d",
			ErrBudgetExceeded, m.Tokens, c.budget)
	}

	c.messages = append(c.messages, m)
	c.used += m.Tokens

	if err := c.enforceLocked(); err != nil {
		// Roll back the append so a refused message leaves no trace. A window
		// that half-accepts is worse than one that refuses.
		c.messages = c.messages[:len(c.messages)-1]
		c.used -= m.Tokens
		return err
	}
	c.metrics.ContextTokens.Observe(float64(c.used))
	return nil
}

// enforceLocked evicts until the window fits its budget. Caller holds c.mu.
func (c *ContextWindow) enforceLocked() error {
	if c.used <= c.budget {
		return nil
	}
	if c.policy == EvictNone {
		c.metrics.ContextOverflow.Inc()
		return fmt.Errorf("%w: context is %d tokens over a budget of %d and eviction is disabled",
			ErrBudgetExceeded, c.used-c.budget, c.budget)
	}

	// Evict oldest-first, skipping pinned. Iterating forward and compacting in
	// place avoids reallocating on every eviction, which matters because this
	// runs on the request path for every long session.
	write := 0
	for read := 0; read < len(c.messages); read++ {
		m := c.messages[read]
		if c.used > c.budget && !m.Pinned {
			c.used -= m.Tokens
			c.evicted++
			c.metrics.ContextEvicted.Inc(c.policy.String())
			continue
		}
		c.messages[write] = m
		write++
	}
	c.messages = c.messages[:write]

	if c.used > c.budget {
		// Everything remaining is pinned. Refusing is correct: the caller
		// pinned more than fits, and silently unpinning would defeat the one
		// guarantee pinning offers.
		c.metrics.ContextOverflow.Inc()
		return fmt.Errorf("%w: %d tokens of pinned context exceed a budget of %d",
			ErrBudgetExceeded, c.used, c.budget)
	}
	return nil
}

// Messages returns a copy of the window's messages, oldest first.
//
// A copy, because the caller hands this to a provider that may hold it beyond
// the call, and a slice into live state would be mutated underneath it by the
// next Append.
func (c *ContextWindow) Messages() []Message {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Message, len(c.messages))
	copy(out, c.messages)
	return out
}

// Assemble returns messages fitting within maxTokens, preferring recent ones
// and always including pinned ones.
//
// This is the read path used to build a request. It does not mutate the window:
// two concurrent requests on one session must see consistent context, and a
// read that evicts would make the second request's context depend on the
// first's timing.
func (c *ContextWindow) Assemble(maxTokens int) ([]Message, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if maxTokens <= 0 {
		maxTokens = c.budget
	}

	var pinnedTokens int
	for _, m := range c.messages {
		if m.Pinned {
			pinnedTokens += m.Tokens
		}
	}
	if pinnedTokens > maxTokens {
		return nil, fmt.Errorf("%w: %d tokens of pinned context exceed the %d-token limit",
			ErrBudgetExceeded, pinnedTokens, maxTokens)
	}

	// Walk backwards taking as much recent context as fits, then restore
	// chronological order. Pinned messages are already reserved above.
	keep := make([]bool, len(c.messages))
	remaining := maxTokens - pinnedTokens
	for i := len(c.messages) - 1; i >= 0; i-- {
		m := c.messages[i]
		if m.Pinned {
			keep[i] = true
			continue
		}
		if m.Tokens <= remaining {
			keep[i] = true
			remaining -= m.Tokens
		}
	}

	out := make([]Message, 0, len(c.messages))
	for i, m := range c.messages {
		if keep[i] {
			out = append(out, m)
		}
	}
	return out, nil
}

// Clear removes every message, optionally retaining pinned ones.
func (c *ContextWindow) Clear(keepPinned bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !keepPinned {
		c.messages = nil
		c.used = 0
		return
	}
	write := 0
	used := 0
	for _, m := range c.messages {
		if m.Pinned {
			c.messages[write] = m
			used += m.Tokens
			write++
		}
	}
	c.messages = c.messages[:write]
	c.used = used
}
