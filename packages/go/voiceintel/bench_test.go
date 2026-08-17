package voiceintel_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/intent"
	"github.com/callscreen/callscreen-platform/packages/go/voiceintel"
)

// T12 — BENCHMARKS for the bridge, context and response-strategy path.
//
// CATEGORY A only. There is no provider or model inference anywhere in Phase
// 13, so no Category B number appears here and none is simulated.
//
// The context benchmarks measure the FROZEN conversation.ContextEngine as
// reached through the Phase 13 bridge. They measure it; they do not change it.

var (
	sinkPlan  conversation.Plan
	sinkEntry conversation.Entry
	sinkOK    bool
	sinkSize  int
)

// benchBridge builds a bridge with the real classifier and a fixed clock.
func benchBridge(b *testing.B) *voiceintel.Bridge {
	b.Helper()
	br, err := voiceintel.New(voiceintel.WithClock(fixedClock()))
	if err != nil {
		b.Fatalf("voiceintel.New: %v", err)
	}
	return br
}

// benchSession begins one session with its floor open, ready for turns.
func benchSession(b *testing.B, br *voiceintel.Bridge, id string) *conversation.Conversation {
	b.Helper()
	p, err := br.Planner(conversation.ConversationID(id), "")
	if err != nil {
		b.Fatalf("Planner(%s): %v", id, err)
	}
	if _, err := p.Handle(conversation.Event{Kind: conversation.EventStart}); err != nil {
		b.Fatalf("EventStart: %v", err)
	}
	if _, err := p.Handle(conversation.Event{
		Kind: conversation.EventGreetingComplete}); err != nil {
		b.Fatalf("EventGreetingComplete: %v", err)
	}
	conv, ok := br.Conversation(conversation.ConversationID(id))
	if !ok {
		b.Fatalf("conversation %s missing", id)
	}
	return conv
}

// ---------------------------------------------------------------------------
// 2. Context insertion
// ---------------------------------------------------------------------------

// BenchmarkContextInsert measures Set below the frozen bound, so eviction is
// NOT part of the measurement — eviction is benchmarked separately.
//
// The key cycles over a fixed window smaller than MaxEntriesPerScope, so each
// Set replaces an existing key. Replacement is the frozen fast path: it skips
// the eviction check entirely (context.go:221 `if _, replacing := m[e.Key]`).
func BenchmarkContextInsert(b *testing.B) {
	for _, window := range []int{1, 16, 128} {
		b.Run(fmt.Sprintf("window=%d", window), func(b *testing.B) {
			if window >= frozenMaxEntriesPerScope {
				b.Fatalf("window %d would trigger eviction; that is a different "+
					"benchmark", window)
			}
			br := benchBridge(b)
			conv := benchSession(b, br, "bench-insert")
			c := conv.Context()

			// Pre-populate so every measured Set is a replacement, and validate.
			for i := 0; i < window; i++ {
				if err := c.Set(conversation.Entry{
					Key: fmt.Sprintf("k%04d", i), Value: i,
					Scope: conversation.ScopeConversation, Source: "t12",
				}); err != nil {
					b.Fatalf("setup Set: %v", err)
				}
			}
			if got := c.Size(conversation.ScopeConversation); got != window {
				b.Fatalf("setup produced %d entries, want %d", got, window)
			}

			// Keys precomputed: fmt.Sprintf inside the timed loop allocates,
			// and that allocation would be reported as the cost of Set.
			keys := make([]string, window)
			for i := range keys {
				keys[i] = fmt.Sprintf("k%04d", i)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := c.Set(conversation.Entry{
					Key: keys[i%window], Value: i,
					Scope: conversation.ScopeConversation, Source: "t12",
				}); err != nil {
					b.Fatalf("Set: %v", err)
				}
			}
			b.StopTimer()
			// Post-check: the window never grew, so eviction never ran.
			if got := c.Size(conversation.ScopeConversation); got != window {
				b.Fatalf("size drifted to %d; eviction contaminated the measurement", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 3. Context lookup
// ---------------------------------------------------------------------------

// BenchmarkContextLookup measures Get (single scope) and Lookup (scope
// precedence order). Both hit and miss are measured: a miss walks every scope
// and is the more expensive path for Lookup.
func BenchmarkContextLookup(b *testing.B) {
	br := benchBridge(b)
	conv := benchSession(b, br, "bench-lookup")
	c := conv.Context()

	const n = 128
	for i := 0; i < n; i++ {
		if err := c.Set(conversation.Entry{
			Key: fmt.Sprintf("k%04d", i), Value: i,
			Scope: conversation.ScopeConversation, Source: "t12",
		}); err != nil {
			b.Fatalf("setup: %v", err)
		}
	}
	if _, ok := c.Get(conversation.ScopeConversation, "k0000"); !ok {
		b.Fatal("fixture: seeded key not found")
	}
	if _, ok := c.Get(conversation.ScopeConversation, "absent"); ok {
		b.Fatal("fixture: absent key was found")
	}

	// Precomputed for the same reason as above: a Sprintf in the timed loop
	// would be attributed to Get/Lookup. Get/miss originally reported 0 B/op
	// while Get/hit reported 5 B/op purely because the miss case used a
	// constant key — the difference was the harness, not the frozen engine.
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%04d", i)
	}

	b.Run("Get/hit", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkEntry, sinkOK = c.Get(conversation.ScopeConversation, keys[i%n])
		}
	})
	b.Run("Get/miss", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkEntry, sinkOK = c.Get(conversation.ScopeConversation, "absent")
		}
	})
	b.Run("Lookup/hit_first_scope", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkEntry, sinkOK = c.Lookup(keys[i%n])
		}
	})
	b.Run("Lookup/miss_all_scopes", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkEntry, sinkOK = c.Lookup("absent")
		}
	})
	b.Run("Size", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkSize = c.Size(conversation.ScopeConversation)
		}
	})
}

// ---------------------------------------------------------------------------
// 4. Context eviction
// ---------------------------------------------------------------------------

// BenchmarkContextEviction measures Set AT the frozen bound, where every
// insert of a new key evicts one entry.
//
// The evictOldestLocked helper is a linear scan of the scope
// (context.go:233), so this is
// expected to be materially more expensive than the replacement path in
// BenchmarkContextInsert. The two are separated precisely so that difference
// is visible instead of averaged away.
//
// The clock is advanced per insert so SetAt values are distinct — see T11 on
// the frozen tied-timestamp behaviour. That costs nothing measurable here and
// keeps the fixture honest about which entry is evicted.
func BenchmarkContextEviction(b *testing.B) {
	clock := fixedClock()
	br, err := voiceintel.New(voiceintel.WithClock(clock))
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	conv := benchSession(b, br, "bench-evict")
	c := conv.Context()

	// Fill exactly to the bound so the very first measured Set evicts.
	for i := 0; i < frozenMaxEntriesPerScope; i++ {
		if err := c.Set(conversation.Entry{
			Key: fmt.Sprintf("seed%05d", i), Value: i,
			Scope: conversation.ScopeConversation, Source: "t12",
		}); err != nil {
			b.Fatalf("setup: %v", err)
		}
		clock.Advance(time.Microsecond)
	}
	if got := c.Size(conversation.ScopeConversation); got != frozenMaxEntriesPerScope {
		b.Fatalf("setup produced %d entries, want the bound %d",
			got, frozenMaxEntriesPerScope)
	}

	// Precomputed so the measurement is eviction, not Sprintf.
	newKeys := make([]string, b.N)
	for i := range newKeys {
		newKeys[i] = fmt.Sprintf("new%09d", i)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := c.Set(conversation.Entry{
			Key: newKeys[i], Value: i,
			Scope: conversation.ScopeConversation, Source: "t12",
		}); err != nil {
			b.Fatalf("Set: %v", err)
		}
	}
	b.StopTimer()
	// Post-check: eviction really did run — the scope is still exactly at the
	// bound despite b.N fresh keys having been inserted.
	if got := c.Size(conversation.ScopeConversation); got != frozenMaxEntriesPerScope {
		b.Fatalf("size = %d after %d inserts; eviction did not run as assumed",
			got, b.N)
	}
}

// ---------------------------------------------------------------------------
// 7. Response strategy selection
// ---------------------------------------------------------------------------

// BenchmarkResponseStrategy measures a full caller turn through the production
// seam: voice.Planner -> conversation engine -> real classifier -> Plan.
//
// This is the end-to-end orchestration cost of deciding what to say. It is NOT
// a model latency and must never be compared to one.
//
// A fresh session per iteration is required — a conversation is stateful, and
// re-using one would measure turn 2, then 3, then escalation. The session
// setup is therefore inside the timed loop, and its cost is isolated by
// BenchmarkSessionSetupOnly below so it can be subtracted honestly.
func BenchmarkResponseStrategy(b *testing.B) {
	cases := []struct {
		name       string
		text       string
		wantAction conversation.Action
		wantIntent conversation.IntentName
	}{
		{"Respond", "please call me back on 9876543210",
			conversation.ActionRespond, intent.IntentRequestCallback},
		{"AskMissingSlot", "i want to leave a message",
			conversation.ActionAsk, intent.IntentLeaveMessage},
		{"Unknown", "zzzz qqqq wubble",
			conversation.ActionRespond, conversation.IntentFallback},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			// Validate the fixture once, outside the loop.
			br := benchBridge(b)
			conv := benchSession(b, br, "bench-strategy-probe")
			plan, err := conv.Handle(utteranceEvent(tc.text))
			if err != nil {
				b.Fatalf("fixture error: %v", err)
			}
			if plan.Action != tc.wantAction {
				b.Fatalf("fixture action = %v, want %v", plan.Action, tc.wantAction)
			}
			if plan.Intent != tc.wantIntent {
				b.Fatalf("fixture intent = %q, want %q", plan.Intent, tc.wantIntent)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				br := benchBridge(b)
				conv := benchSession(b, br, fmt.Sprintf("bench-strategy-%d", i))
				var err error
				sinkPlan, err = conv.Handle(utteranceEvent(tc.text))
				if err != nil {
					b.Fatalf("Handle: %v", err)
				}
			}
		})
	}
}

// BenchmarkSessionSetupOnly measures bridge construction plus session opening
// with NO caller turn.
//
// Its purpose is arithmetic, not performance: subtracting it from
// BenchmarkResponseStrategy gives the marginal cost of the turn itself.
// Without it, that benchmark's number would be dominated by setup and misread
// as the cost of deciding a response.
func BenchmarkSessionSetupOnly(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		br := benchBridge(b)
		conv := benchSession(b, br, fmt.Sprintf("bench-setup-%d", i))
		if conv == nil {
			b.Fatal("nil conversation")
		}
	}
}

// BenchmarkTurnOnReusedSession measures repeated turns on ONE session, which
// is what a long call actually does.
//
// Utterance chosen for repeatability: T10 measured that intents with an
// unfilled required slot escalate on the third repeat once the frozen
// clarification budget is spent, which would turn this into a benchmark of the
// terminal path.
func BenchmarkTurnOnReusedSession(b *testing.B) {
	br := benchBridge(b)
	conv := benchSession(b, br, "bench-reused")

	plan, err := conv.Handle(utteranceEvent("please call me back on 9876543210"))
	if err != nil {
		b.Fatalf("fixture: %v", err)
	}
	if plan.Intent != intent.IntentRequestCallback {
		b.Fatalf("fixture intent = %q", plan.Intent)
	}
	if _, err := conv.Handle(conversation.Event{
		Kind: conversation.EventSpeechComplete}); err != nil {
		b.Fatalf("fixture speech complete: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	turns := 1 // the fixture turn above already counted
	for i := 0; i < b.N; i++ {
		// The frozen persona bounds conversation length: at MaxTurns the
		// planner escalates with reason "max_turns_reached" and the
		// conversation becomes terminal (persona.go:177). MEASURED: the default
		// persona allows 20 turns. A benchmark whose b.N exceeds that must
		// rebuild the session, and the rebuild is excluded from the timer so it
		// is not attributed to the turn.
		if turns >= turnsPerSession {
			b.StopTimer()
			br = benchBridge(b)
			conv = benchSession(b, br, fmt.Sprintf("bench-reused-%d", i))
			turns = 0
			b.StartTimer()
		}

		var err error
		sinkPlan, err = conv.Handle(utteranceEvent("please call me back on 9876543210"))
		if err != nil {
			b.Fatalf("iteration %d: %v", i, err)
		}
		if _, err := conv.Handle(conversation.Event{
			Kind: conversation.EventSpeechComplete}); err != nil {
			b.Fatalf("iteration %d speech complete: %v", i, err)
		}
		turns++
	}
}

// turnsPerSession is the number of turns a benchmark takes on one session
// before rebuilding it.
//
// MEASURED, not guessed: the default persona's MaxTurns is 20 — turn 20
// escalates with "max_turns_reached". 15 leaves clear headroom, including for
// the turns a fixture validation already consumed.
const turnsPerSession = 15

// ---------------------------------------------------------------------------
// 8. Concurrent sessions through the bridge
// ---------------------------------------------------------------------------

// BenchmarkConcurrentSessions measures N sessions taking a turn simultaneously
// on one shared bridge and classifier — the production shape.
//
// Sessions are created in setup; only the turns are timed.
func BenchmarkConcurrentSessions(b *testing.B) {
	for _, sessions := range []int{1, 4, 16} {
		b.Run(fmt.Sprintf("sessions=%d", sessions), func(b *testing.B) {
			br := benchBridge(b)
			convs := make([]*conversation.Conversation, sessions)
			for i := range convs {
				convs[i] = benchSession(b, br, fmt.Sprintf("bench-conc-%02d", i))
			}
			// Validate every session before measuring.
			for i, c := range convs {
				plan, err := c.Handle(utteranceEvent("please call me back on 9876543210"))
				if err != nil {
					b.Fatalf("session %d fixture: %v", i, err)
				}
				if plan.Intent != intent.IntentRequestCallback {
					b.Fatalf("session %d intent = %q", i, plan.Intent)
				}
				if _, err := c.Handle(conversation.Event{
					Kind: conversation.EventSpeechComplete}); err != nil {
					b.Fatalf("session %d speech complete: %v", i, err)
				}
			}

			b.ReportAllocs()
			b.ResetTimer()
			turns := 1 // the validation turn above already counted
			for i := 0; i < b.N; i++ {
				// Same frozen MaxTurns bound as above; rebuild off the clock.
				if turns >= turnsPerSession {
					b.StopTimer()
					br = benchBridge(b)
					for j := range convs {
						convs[j] = benchSession(b, br,
							fmt.Sprintf("bench-conc-%02d-%d", j, i))
					}
					turns = 0
					b.StartTimer()
				}

				var wg sync.WaitGroup
				errCh := make(chan error, len(convs))
				for _, c := range convs {
					wg.Add(1)
					go func(c *conversation.Conversation) {
						defer wg.Done()
						if _, err := c.Handle(
							utteranceEvent("please call me back on 9876543210")); err != nil {
							errCh <- err
							return
						}
						if _, err := c.Handle(conversation.Event{
							Kind: conversation.EventSpeechComplete}); err != nil {
							errCh <- err
						}
					}(c)
				}
				wg.Wait()
				close(errCh)
				for err := range errCh {
					b.Fatalf("iteration %d: %v", i, err)
				}
				turns++
			}
		})
	}
}

// BenchmarkContextConcurrentAccess measures the frozen context engine's
// RWMutex behaviour under simultaneous readers and writers within one session.
func BenchmarkContextConcurrentAccess(b *testing.B) {
	br := benchBridge(b)
	conv := benchSession(b, br, "bench-ctx-parallel")
	c := conv.Context()

	pkeys := make([]string, 64)
	for i := range pkeys {
		pkeys[i] = fmt.Sprintf("k%04d", i)
	}
	for i := 0; i < 64; i++ {
		if err := c.Set(conversation.Entry{
			Key: pkeys[i], Value: i,
			Scope: conversation.ScopeConversation, Source: "t12",
		}); err != nil {
			b.Fatalf("setup: %v", err)
		}
	}

	b.Run("read_only", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			var e conversation.Entry
			var ok bool
			for pb.Next() {
				e, ok = c.Get(conversation.ScopeConversation, pkeys[i%64])
				i++
			}
			sinkEntry, sinkOK = e, ok
		})
	})

	b.Run("mixed_read_write", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				if i%4 == 0 {
					if err := c.Set(conversation.Entry{
						Key: pkeys[i%64], Value: i,
						Scope: conversation.ScopeConversation, Source: "t12",
					}); err != nil {
						panic(err)
					}
				} else {
					sinkEntry, sinkOK = c.Get(conversation.ScopeConversation, pkeys[i%64])
				}
				i++
			}
		})
	})
}
