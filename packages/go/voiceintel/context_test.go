package voiceintel_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	"github.com/callscreen/callscreen-platform/packages/go/voiceintel"
)

// T7 — SESSION CONTEXT ISOLATION.
//
// This is a VERIFICATION task. Phase 13 builds no context system; it proves
// the FROZEN conversation.ContextEngine stays isolated when reached through
// the T6 bridge. Every value below is read out of the frozen source, not
// assumed:
//
//	MaxEntriesPerScope  256          (DefaultContextConfig, context.go:125)
//	DefaultTTL          10 minutes   (Conversation/Session/Shared scopes)
//	TemporaryTTL        30 seconds   (ScopeTemporary)
//	eviction            oldest by Entry.SetAt, on insert when a scope is full
//	                    (Set → evictOldestLocked, context.go:220-246)
//	expiry              evaluated on READ, not by a sweeper (context.go:250)
//	Lookup precedence   Temporary → Conversation → Session → Shared → Business
//
// The sanctioned route is the one T6 exposed:
// Bridge.Conversation(id).Context().

const (
	frozenMaxEntriesPerScope = 256
	frozenDefaultTTL         = 10 * time.Minute
)

// ctxOf returns one session's context engine through the sanctioned API.
func ctxOf(t *testing.T, b *voiceintel.Bridge, id string) *conversation.ContextEngine {
	t.Helper()
	conv, ok := b.Conversation(conversation.ConversationID(id))
	if !ok {
		t.Fatalf("no conversation %q", id)
	}
	return conv.Context()
}

// newBridgeWithSessions builds ONE bridge — one shared classifier, shared
// immutable config — and starts the named sessions on it.
func newBridgeWithSessions(t *testing.T, clock rt.Clock, ids ...string) *voiceintel.Bridge {
	t.Helper()
	b, err := voiceintel.New(voiceintel.WithClock(clock))
	if err != nil {
		t.Fatalf("voiceintel.New: %v", err)
	}
	for _, id := range ids {
		p, err := b.Planner(conversation.ConversationID(id), "")
		if err != nil {
			t.Fatalf("Planner(%s): %v", id, err)
		}
		openFloor(t, p)
	}
	return b
}

func setConv(t *testing.T, c *conversation.ContextEngine, key string, val any) {
	t.Helper()
	if err := c.Set(conversation.Entry{
		Key: key, Value: val,
		Scope: conversation.ScopeConversation, Source: "t7",
	}); err != nil {
		t.Fatalf("Set(%s): %v", key, err)
	}
}

// ---------------------------------------------------------------------------
// 1. Session A / B isolation
// ---------------------------------------------------------------------------

// TestContext_SessionAAndBAreIsolated is the behavioural core: the wrong
// session must not OBSERVE the other's data. It asserts on retrieved values
// through the public API, never on object identity.
func TestContext_SessionAAndBAreIsolated(t *testing.T) {
	t.Parallel()

	b := newBridgeWithSessions(t, fixedClock(), "sess-A", "sess-B")
	ctxA, ctxB := ctxOf(t, b, "sess-A"), ctxOf(t, b, "sess-B")

	const keyA, valA = "appointment_number", "A-7419"
	const valB = "B-8520"

	setConv(t, ctxA, keyA, valA)
	setConv(t, ctxB, keyA, valB) // SAME key, different session

	// Each retrieves its own.
	if got, ok := ctxA.Get(conversation.ScopeConversation, keyA); !ok || got.Value != valA {
		t.Errorf("A retrieved %v (ok=%v), want %q", got.Value, ok, valA)
	}
	if got, ok := ctxB.Get(conversation.ScopeConversation, keyA); !ok || got.Value != valB {
		t.Errorf("B retrieved %v (ok=%v), want %q", got.Value, ok, valB)
	}

	// Neither sees the other's.
	if got, _ := ctxA.Get(conversation.ScopeConversation, keyA); got.Value == valB {
		t.Error("session A observed session B's value — context leaked")
	}
	if got, _ := ctxB.Get(conversation.ScopeConversation, keyA); got.Value == valA {
		t.Error("session B observed session A's value — context leaked")
	}

	// And through Lookup, which searches every scope.
	if e, ok := ctxA.Lookup(keyA); !ok || e.Value != valA {
		t.Errorf("A Lookup = %v (ok=%v), want %q", e.Value, ok, valA)
	}
	if e, ok := ctxB.Lookup(keyA); !ok || e.Value != valB {
		t.Errorf("B Lookup = %v (ok=%v), want %q", e.Value, ok, valB)
	}
}

// TestContext_EveryScopeIsPerSessionIncludingShared — ScopeShared is
// documented as "visible across concurrent conversations for one subject"
// (context.go). That describes INTENT, not a shared store: every Conversation
// gets its own ContextEngine at engine.go:250, so a ScopeShared entry is still
// per-conversation. Pinned here because the name invites the opposite
// assumption, and a deployment that actually needs cross-conversation sharing
// must build it deliberately rather than expect it from the scope.
func TestContext_EveryScopeIsPerSessionIncludingShared(t *testing.T) {
	t.Parallel()

	b := newBridgeWithSessions(t, fixedClock(), "scope-A", "scope-B")
	ctxA, ctxB := ctxOf(t, b, "scope-A"), ctxOf(t, b, "scope-B")

	scopes := []conversation.Scope{
		conversation.ScopeConversation, conversation.ScopeSession,
		conversation.ScopeBusiness, conversation.ScopeTemporary,
		conversation.ScopeShared,
	}
	for _, s := range scopes {
		key := "k-" + s.String()
		if err := ctxA.Set(conversation.Entry{
			Key: key, Value: "from-A", Scope: s, Source: "t7",
		}); err != nil {
			t.Fatalf("Set in %v: %v", s, err)
		}
		if _, ok := ctxB.Get(s, key); ok {
			t.Errorf("scope %v: session B observed a value only A wrote", s)
		}
	}
}

// ---------------------------------------------------------------------------
// 2 + 3. Bound and deterministic eviction
// ---------------------------------------------------------------------------

// TestContext_StaysWithinTheFrozenBound inserts well past MaxEntriesPerScope
// and asserts the observable size never exceeds it.
func TestContext_StaysWithinTheFrozenBound(t *testing.T) {
	t.Parallel()

	clock := fixedClock()
	b := newBridgeWithSessions(t, clock, "bound")
	c := ctxOf(t, b, "bound")

	// Up to the bound.
	for i := 0; i < frozenMaxEntriesPerScope; i++ {
		setConv(t, c, fmt.Sprintf("k%04d", i), i)
	}
	if n := c.Size(conversation.ScopeConversation); n != frozenMaxEntriesPerScope {
		t.Errorf("size at the bound = %d, want %d", n, frozenMaxEntriesPerScope)
	}

	// Past it, by a lot.
	for i := frozenMaxEntriesPerScope; i < frozenMaxEntriesPerScope*3; i++ {
		setConv(t, c, fmt.Sprintf("k%04d", i), i)
	}
	if n := c.Size(conversation.ScopeConversation); n > frozenMaxEntriesPerScope {
		t.Errorf("size after overflow = %d, exceeding the frozen bound of %d",
			n, frozenMaxEntriesPerScope)
	}
}

// TestContext_EvictionDropsTheOldestDeterministically — the frozen policy is
// "oldest by Entry.SetAt" (evictOldestLocked). SetAt comes from the engine's
// clock, so the clock is ADVANCED between writes to give each entry a distinct
// timestamp. See the tie-case note in
// TestContext_EvictionOrderIsUnspecifiedWhenTimestampsTie for why that
// matters.
func TestContext_EvictionDropsTheOldestDeterministically(t *testing.T) {
	t.Parallel()

	run := func() []string {
		clock := fixedClock()
		b := newBridgeWithSessions(t, clock, "evict")
		c := ctxOf(t, b, "evict")

		for i := 0; i < frozenMaxEntriesPerScope; i++ {
			setConv(t, c, fmt.Sprintf("k%04d", i), i)
			clock.Advance(time.Millisecond) // distinct SetAt per entry
		}
		// One more: the oldest (k0000) must go.
		setConv(t, c, "overflow", "x")

		var survivors []string
		for i := 0; i < frozenMaxEntriesPerScope; i++ {
			k := fmt.Sprintf("k%04d", i)
			if _, ok := c.Get(conversation.ScopeConversation, k); ok {
				survivors = append(survivors, k)
			}
		}
		return survivors
	}

	first := run()
	if len(first) != frozenMaxEntriesPerScope-1 {
		t.Fatalf("%d survivors, want %d", len(first), frozenMaxEntriesPerScope-1)
	}
	if first[0] != "k0001" {
		t.Errorf("oldest surviving key = %q, want k0001 — k0000 should have been "+
			"evicted as the oldest by SetAt", first[0])
	}

	// 100 repeats of the identical sequence must give the identical outcome.
	want := strings.Join(first, ",")
	for i := 0; i < 100; i++ {
		if got := strings.Join(run(), ","); got != want {
			t.Fatalf("eviction not deterministic on iteration %d", i)
		}
	}
}

// TestContext_EvictionOrderIsUnspecifiedWhenTimestampsTie records a FROZEN
// property discovered while building this suite. It is not a Phase 13 defect
// and is not patched.
//
// The evictOldestLocked helper selects with `e.SetAt.Before(oldest)`, which
// is FALSE for
// equal timestamps — so when every entry shares a SetAt the victim is
// whichever key Go's randomised map iteration happens to yield first. That is
// reachable in production wherever writes land inside one clock tick.
//
// The test asserts only what the frozen contract guarantees: the bound holds
// and exactly one entry is evicted. It deliberately does NOT assert which one,
// because that would be asserting an accident.
func TestContext_EvictionOrderIsUnspecifiedWhenTimestampsTie(t *testing.T) {
	t.Parallel()

	clock := fixedClock() // never advanced: every SetAt identical
	b := newBridgeWithSessions(t, clock, "tie")
	c := ctxOf(t, b, "tie")

	for i := 0; i < frozenMaxEntriesPerScope; i++ {
		setConv(t, c, fmt.Sprintf("k%04d", i), i)
	}
	setConv(t, c, "overflow", "x")

	if n := c.Size(conversation.ScopeConversation); n != frozenMaxEntriesPerScope {
		t.Errorf("size = %d, want exactly the bound %d", n, frozenMaxEntriesPerScope)
	}
	if _, ok := c.Get(conversation.ScopeConversation, "overflow"); !ok {
		t.Error("the newest entry was evicted instead of an older one")
	}
}

// ---------------------------------------------------------------------------
// 4. Turn transitions
// ---------------------------------------------------------------------------

// TestContext_SurvivesLegitimateTurnTransitions drives the REAL frozen turn
// cycle discovered in T6 — greeting-complete, utterance, speech-complete — and
// checks conversation-scoped context persists across it.
func TestContext_SurvivesLegitimateTurnTransitions(t *testing.T) {
	t.Parallel()

	// Turns are driven through voice.Planner — the interface voice actually
	// holds — not through *conversation.Conversation, so this exercises the real
	// production seam rather than a shortcut past it.
	b, err := voiceintel.New(voiceintel.WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("voiceintel.New: %v", err)
	}
	p, err := b.Planner("turns", "")
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}
	openFloor(t, p)
	c := ctxOf(t, b, "turns")

	// Turn 1: store A.
	setConv(t, c, "fact_a", "alpha")

	if _, err := p.Handle(utteranceEvent("please call me back on 9876543210")); err != nil {
		t.Fatalf("turn 1 utterance: %v", err)
	}
	if _, err := p.Handle(conversation.Event{Kind: conversation.EventSpeechComplete}); err != nil {
		t.Fatalf("turn 1 speech-complete: %v", err)
	}

	// Turn 2: A must still be there; add B.
	if e, ok := c.Get(conversation.ScopeConversation, "fact_a"); !ok || e.Value != "alpha" {
		t.Fatalf("fact_a lost across a turn transition: %v (ok=%v)", e.Value, ok)
	}
	setConv(t, c, "fact_b", "beta")

	if _, err := p.Handle(utteranceEvent("transfer me to rajesh")); err != nil {
		t.Fatalf("turn 2 utterance: %v", err)
	}
	if _, err := p.Handle(conversation.Event{Kind: conversation.EventSpeechComplete}); err != nil {
		t.Fatalf("turn 2 speech-complete: %v", err)
	}

	// Turn 3: both survive.
	for k, want := range map[string]string{"fact_a": "alpha", "fact_b": "beta"} {
		if e, ok := c.Get(conversation.ScopeConversation, k); !ok || e.Value != want {
			t.Errorf("%s = %v (ok=%v) after two turns, want %q", k, e.Value, ok, want)
		}
	}

	// The engine writes its own conversation-scoped state too; last_intent is
	// set at engine.go:684. Its presence confirms the turns really ran.
	if _, ok := c.Get(conversation.ScopeConversation, "last_intent"); !ok {
		t.Error("last_intent absent; the turns did not reach the intent stage")
	}
}

// TestContext_TemporaryScopeExpiresOnTheFrozenSchedule — TemporaryTTL is 30s
// and expiry is evaluated on READ.
func TestContext_TemporaryScopeExpiresOnTheFrozenSchedule(t *testing.T) {
	t.Parallel()

	clock := fixedClock()
	b := newBridgeWithSessions(t, clock, "ttl")
	c := ctxOf(t, b, "ttl")

	if err := c.Set(conversation.Entry{
		Key: "scratch", Value: "v",
		Scope: conversation.ScopeTemporary, Source: "t7",
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get(conversation.ScopeTemporary, "scratch"); !ok {
		t.Fatal("temporary entry missing immediately after write")
	}

	clock.Advance(31 * time.Second) // past TemporaryTTL
	if _, ok := c.Get(conversation.ScopeTemporary, "scratch"); ok {
		t.Error("temporary entry survived past its 30s TTL")
	}

	// A conversation-scoped entry written at the same moment must NOT have
	// expired — its TTL is 10 minutes.
	setConv(t, c, "durable", "v")
	clock.Advance(31 * time.Second)
	if _, ok := c.Get(conversation.ScopeConversation, "durable"); !ok {
		t.Error("a conversation-scoped entry expired on the temporary schedule")
	}
	clock.Advance(frozenDefaultTTL)
	if _, ok := c.Get(conversation.ScopeConversation, "durable"); ok {
		t.Error("a conversation-scoped entry survived past DefaultTTL")
	}
}

// ---------------------------------------------------------------------------
// 5. Termination
// ---------------------------------------------------------------------------

// TestContext_TerminatedSessionCannotLeakIntoANewOne — the observable frozen
// contract, established by reading engine.go rather than assuming a policy:
// Begin stores into a sync.Map keyed by id (engine.go:289), so reusing an id
// REPLACES the entry with a brand-new Conversation carrying a brand-new
// ContextEngine. Nothing calls ClearScope — context dies with the object it
// belonged to.
//
// This test asserts that observable contract and imposes no new clearing
// policy.
func TestContext_TerminatedSessionCannotLeakIntoANewOne(t *testing.T) {
	t.Parallel()

	const id = "reused-id"
	b := newBridgeWithSessions(t, fixedClock(), id)

	old := ctxOf(t, b, id)
	setConv(t, old, "secret_from_first_session", "S-0001")

	// Terminate it for real, through the frozen lifecycle.
	conv, _ := b.Conversation(id)
	if _, err := conv.Handle(utteranceEvent("goodbye")); err != nil {
		t.Fatalf("goodbye: %v", err)
	}

	// A new session on the SAME id.
	p, err := b.Planner(id, "")
	if err != nil {
		t.Fatalf("re-Begin: %v", err)
	}
	openFloor(t, p)

	fresh := ctxOf(t, b, id)
	if _, ok := fresh.Get(conversation.ScopeConversation, "secret_from_first_session"); ok {
		t.Error("a new session on a reused id observed the terminated session's context")
	}
	if _, ok := fresh.Lookup("secret_from_first_session"); ok {
		t.Error("Lookup across all scopes found the terminated session's value")
	}
	if n := fresh.Size(conversation.ScopeConversation); n != 0 {
		t.Errorf("a fresh session began with %d conversation entries, want 0", n)
	}
}

// ---------------------------------------------------------------------------
// 6. Concurrency — 16 sessions
// ---------------------------------------------------------------------------

// TestContext_SixteenConcurrentSessionsStayIsolated — one bridge, one shared
// classifier, one shared immutable config; sixteen sessions writing and
// reading their own context concurrently. Every session must see only its own
// values, asserted through the public API.
//
// Not a race-detector claim — see the T7 report.
func TestContext_SixteenConcurrentSessionsStayIsolated(t *testing.T) {
	t.Parallel()

	const sessions, iterations = 16, 40

	ids := make([]string, sessions)
	for i := range ids {
		ids[i] = fmt.Sprintf("session-%02d", i)
	}
	b := newBridgeWithSessions(t, fixedClock(), ids...)

	var wg sync.WaitGroup
	errs := make(chan string, sessions*iterations)

	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("session-%02d", i)
			mine := fmt.Sprintf("value-%02d", i)

			conv, ok := b.Conversation(conversation.ConversationID(id))
			if !ok {
				errs <- fmt.Sprintf("%s: no conversation", id)
				return
			}
			c := conv.Context()

			for k := 0; k < iterations; k++ {
				// Same key in every session — only the value differs, so a
				// leak shows up as a wrong value rather than a missing one.
				if err := c.Set(conversation.Entry{
					Key: "session_marker", Value: mine,
					Scope: conversation.ScopeConversation, Source: "t7",
				}); err != nil {
					errs <- fmt.Sprintf("%s: set: %v", id, err)
					return
				}
				e, ok := c.Get(conversation.ScopeConversation, "session_marker")
				if !ok {
					errs <- fmt.Sprintf("%s: marker missing", id)
					return
				}
				if e.Value != mine {
					errs <- fmt.Sprintf("%s read %v, want %q — another session's "+
						"context leaked in", id, e.Value, mine)
					return
				}
				// A per-iteration key too, so growth and eviction are exercised.
				if err := c.Set(conversation.Entry{
					Key:   fmt.Sprintf("k%03d", k),
					Value: mine, Scope: conversation.ScopeConversation, Source: "t7",
				}); err != nil {
					errs <- fmt.Sprintf("%s: set k: %v", id, err)
					return
				}
			}

			// Final sweep: nothing in this session may carry another's value.
			for k := 0; k < iterations; k++ {
				if e, ok := c.Get(conversation.ScopeConversation, fmt.Sprintf("k%03d", k)); ok {
					if e.Value != mine {
						errs <- fmt.Sprintf("%s: k%03d holds %v, want %q", id, k, e.Value, mine)
						return
					}
				}
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent context isolation failed: %s", e)
	}
}

// ---------------------------------------------------------------------------
// 7-10. Empty, missing, repeated creation
// ---------------------------------------------------------------------------

func TestContext_EmptyAndMissingLookupsAreBounded(t *testing.T) {
	t.Parallel()

	b := newBridgeWithSessions(t, fixedClock(), "empty")
	c := ctxOf(t, b, "empty")

	if n := c.Size(conversation.ScopeSession); n != 0 {
		t.Errorf("a fresh session scope has %d entries, want 0", n)
	}
	if _, ok := c.Get(conversation.ScopeConversation, "never_written"); ok {
		t.Error("a missing key reported present")
	}
	if _, ok := c.Lookup("never_written"); ok {
		t.Error("Lookup found a key that was never written")
	}
	if c.Delete(conversation.ScopeConversation, "never_written") {
		t.Error("Delete reported removing a key that never existed")
	}
	// Adversarial keys must not panic or escape.
	for _, name := range []string{"empty", "100k-chars", "control-bytes", "traversal"} {
		var k string
		switch name {
		case "empty":
			k = ""
		case "100k-chars":
			k = strings.Repeat("x", 100000)
		case "control-bytes":
			k = "\x00\x01"
		case "traversal":
			k = "../../etc/passwd"
		}
		if _, ok := c.Get(conversation.ScopeConversation, k); ok {
			t.Errorf("Get(%s key) reported a hit", name)
		}
		if _, ok := c.Lookup(k); ok {
			t.Errorf("Lookup(%s key) reported a hit", name)
		}
	}
}

func TestContext_RepeatedSessionCreationYieldsIndependentContexts(t *testing.T) {
	t.Parallel()

	b := newBridgeWithSessions(t, fixedClock())
	seen := map[string]bool{}

	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("repeat-%02d", i)
		p, err := b.Planner(conversation.ConversationID(id), "")
		if err != nil {
			t.Fatal(err)
		}
		openFloor(t, p)

		c := ctxOf(t, b, id)
		if n := c.Size(conversation.ScopeConversation); n != 0 {
			t.Errorf("%s began with %d entries, want 0", id, n)
		}
		setConv(t, c, "marker", id)
		seen[id] = true
	}

	// Every session still holds only its own marker.
	for id := range seen {
		c := ctxOf(t, b, id)
		e, ok := c.Get(conversation.ScopeConversation, "marker")
		if !ok || e.Value != id {
			t.Errorf("%s marker = %v (ok=%v), want %q", id, e.Value, ok, id)
		}
	}
}

// TestContext_SharedClassifierDoesNotImplySharedContext is the adversarial
// arrangement: identical configuration, one classifier instance, many
// sessions.
func TestContext_SharedClassifierDoesNotImplySharedContext(t *testing.T) {
	t.Parallel()

	b := newBridgeWithSessions(t, fixedClock(), "shared-1", "shared-2", "shared-3")

	for i, id := range []string{"shared-1", "shared-2", "shared-3"} {
		c := ctxOf(t, b, id)
		setConv(t, c, "who", fmt.Sprintf("owner-%d", i))
	}
	for i, id := range []string{"shared-1", "shared-2", "shared-3"} {
		c := ctxOf(t, b, id)
		want := fmt.Sprintf("owner-%d", i)
		if e, ok := c.Get(conversation.ScopeConversation, "who"); !ok || e.Value != want {
			t.Errorf("%s: who = %v (ok=%v), want %q — a shared classifier must not "+
				"imply shared context", id, e.Value, ok, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Global mutable-state guard (AST)
// ---------------------------------------------------------------------------

// TestPhase13_HasNoPackageLevelMutableContextState — distinguishes mutable
// state from immutable configuration: a `const`, or a `var` holding a
// compile-time-constant-ish literal used as configuration, is fine. A map,
// slice, sync.Map or pointer at package level is a cross-session registry and
// is rejected.
//
// This complements — it does not replace — T4's
// TestPackage_HasNoPackageLevelMutableState, which covers the intent package.
// This one covers the bridge, which is where a session registry would most
// plausibly appear.
func TestPhase13_HasNoPackageLevelMutableContextState(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("parsed no non-test files; the guard would pass vacuously")
	}

	var inspected int
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			for _, decl := range f.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.VAR {
					continue // const and type are immutable by construction
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, id := range vs.Names {
						inspected++
						if id.Name == "_" {
							continue // interface assertion, not state
						}
						// A sentinel error is immutable in practice and is the
						// established idiom across this repository.
						if strings.HasPrefix(id.Name, "Err") {
							continue
						}
						var expr ast.Expr
						if i < len(vs.Values) {
							expr = vs.Values[i]
						}
						if isMutableContainer(vs.Type) || isMutableContainer(expr) {
							t.Errorf("%s: package-level var %q is a mutable container; "+
								"a cross-session context registry must not exist",
								name, id.Name)
							continue
						}
						t.Errorf("%s: unexpected package-level var %q — Phase 13 "+
							"holds no package state beyond sentinel errors",
							name, id.Name)
					}
				}
			}
		}
	}
	t.Logf("%d package-level var specs inspected", inspected)
}

// isMutableContainer reports whether an AST node denotes a map, slice,
// sync.Map or pointer — the shapes a session registry would take.
func isMutableContainer(e ast.Expr) bool {
	if e == nil {
		return false
	}
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.MapType, *ast.ArrayType, *ast.StarExpr:
			found = true
		case *ast.SelectorExpr:
			if id, ok := v.X.(*ast.Ident); ok && id.Name == "sync" {
				found = true
			}
		case *ast.CallExpr:
			if id, ok := v.Fun.(*ast.Ident); ok && (id.Name == "make" || id.Name == "new") {
				found = true
			}
		}
		return true
	})
	return found
}
