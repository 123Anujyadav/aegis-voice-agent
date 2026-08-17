package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sync"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/intent"
)

// Phase 14 T5 — service-level session isolation under concurrent load.
//
// WHAT IS ALREADY PROVEN, and deliberately not rebuilt here:
//
//	Phase 13 T7  TestContext_SixteenConcurrentSessionsStayIsolated  (bridge)
//	Phase 13 T10 TestT10_SixteenSessionsClassifyAndStoreConcurrently (bridge)
//	Phase 13 T10 TestT10_ConcurrentContextChurnStaysBoundedAndIsolated
//	Phase 14 T4  TestT4_TwoServiceSessionsProduceIndependentPlans (service, 2)
//
// T5's new evidence is the combination those miss: MANY sessions, running
// CONCURRENTLY, with their operations INTERLEAVED at forced scheduling points,
// through the actual Phase 14 service wiring rather than a bridge built in a
// test.

const (
	isoSessions = 16
	isoRounds   = 3
)

// barrier releases every participant only once all of them have arrived.
//
// This is what creates genuine interleaving: without it, goroutines may simply
// run to completion one after another and the test would pass by scheduling
// luck rather than by isolation. Reusable across phases, and it contains no
// sleep — arrival is the only synchronisation.
type barrier struct {
	mu      sync.Mutex
	n       int
	count   int
	release chan struct{}
	aborted chan struct{}
	once    sync.Once
}

func newBarrier(n int) *barrier {
	return &barrier{n: n, release: make(chan struct{}), aborted: make(chan struct{})}
}

// abort releases every waiter permanently.
//
// Load-bearing, and learned the hard way: an earlier version had no abort, so
// when a participant hit an error and returned, the survivors blocked forever
// at the next phase and the TEST HUNG instead of failing. A mutation that
// breaks isolation must produce a failure, not a timeout — a hanging test tells
// nobody what went wrong.
func (b *barrier) abort() { b.once.Do(func() { close(b.aborted) }) }

// wait blocks until every participant arrives. It reports false if the run has
// been aborted, in which case the caller should stop.
func (b *barrier) wait() bool {
	select {
	case <-b.aborted:
		return false
	default:
	}

	b.mu.Lock()
	ch := b.release
	b.count++
	if b.count == b.n {
		b.count = 0
		b.release = make(chan struct{}) // arm the next phase before releasing
		close(ch)
	}
	b.mu.Unlock()

	select {
	case <-ch:
		return true
	case <-b.aborted:
		return false
	}
}

// isoSpec is one concurrent session: a unique id, a unique marker, and a
// deterministic utterance with the intent it must resolve to.
type isoSpec struct {
	id     conversation.ConversationID
	marker string
	text   string
	want   conversation.IntentName
}

// isoSpecs builds the session fixtures. Markers are synthetic — no real PII.
// Utterances repeat across sessions on purpose: isolation that depended on
// distinct inputs would not be isolation.
func isoSpecs() []isoSpec {
	base := []struct {
		text string
		want conversation.IntentName
	}{
		{"please call me back on 9876543210", intent.IntentRequestCallback},
		{"transfer me to rajesh", intent.IntentRequestTransfer},
		{"say that again", intent.IntentRepeat},
		{"can you hold on a moment", intent.IntentHold},
		{"this is rajesh sharma calling", intent.IntentCallerIdentity},
	}
	out := make([]isoSpec, isoSessions)
	for i := range out {
		b := base[i%len(base)]
		out[i] = isoSpec{
			id:     conversation.ConversationID(fmt.Sprintf("t5-session-%02d", i)),
			marker: fmt.Sprintf("marker-%02d", i),
			text:   b.text,
			want:   b.want,
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// The concurrent, interleaved isolation test
// ---------------------------------------------------------------------------

// TestT5_ConcurrentSessionsStayIsolatedThroughTheService runs 16 sessions on
// one running service, forcing them through six shared phases per round so that
// their context writes, classifications and reads are genuinely interleaved.
//
// NOT race-detector evidence — see the T5 report.
func TestT5_ConcurrentSessionsStayIsolatedThroughTheService(t *testing.T) {
	sink, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	specs := isoSpecs()

	// Sessions are opened serially so the concurrent phase starts from a known
	// state; the frozen floor must be open before any utterance is planned.
	planners := make([]interface {
		Handle(conversation.Event) (conversation.Plan, error)
	}, len(specs))
	for i, s := range specs {
		planners[i] = openSession(t, vi, string(s.id))
	}

	gate := newBarrier(len(specs))
	errs := make(chan string, len(specs)*isoRounds*4)
	var wg sync.WaitGroup

	for i, s := range specs {
		wg.Add(1)
		go func(i int, s isoSpec) {
			defer wg.Done()

			conv, ok := vi.Bridge().Conversation(s.id)
			if !ok {
				gate.abort()
				errs <- string(s.id) + ": conversation missing"
				return
			}
			ctx := conv.Context()

			for round := 0; round < isoRounds; round++ {
				key := fmt.Sprintf("round-%d", round)

				// PHASE A — every session writes its own context.
				if !gate.wait() {
					return
				}
				if err := ctx.Set(conversation.Entry{
					Key: key, Value: s.marker,
					Scope: conversation.ScopeConversation, Source: "t5",
				}); err != nil {
					gate.abort()
					errs <- fmt.Sprintf("%s: set: %v", s.id, err)
					return
				}

				// PHASE B — every session classifies.
				if !gate.wait() {
					return
				}
				plan, err := planners[i].Handle(utter(s.text))
				if err != nil {
					gate.abort()
					errs <- fmt.Sprintf("%s round %d: %v", s.id, round, err)
					return
				}
				if plan.Intent != s.want {
					gate.abort()
					errs <- fmt.Sprintf("%s round %d: intent %q, want %q — another "+
						"session's classification surfaced here",
						s.id, round, plan.Intent, s.want)
					return
				}

				// PHASE C — every session reads its own marker back.
				if !gate.wait() {
					return
				}
				e, found := ctx.Get(conversation.ScopeConversation, key)
				if !found {
					gate.abort()
					errs <- fmt.Sprintf("%s round %d: own marker vanished", s.id, round)
					return
				}
				if e.Value != s.marker {
					gate.abort()
					errs <- fmt.Sprintf("%s round %d: read %v, want %q — context "+
						"crossed sessions", s.id, round, e.Value, s.marker)
					return
				}

				// PHASE D — every session writes a second value.
				if !gate.wait() {
					return
				}
				if err := ctx.Set(conversation.Entry{
					Key: "latest", Value: s.marker,
					Scope: conversation.ScopeConversation, Source: "t5",
				}); err != nil {
					gate.abort()
					errs <- fmt.Sprintf("%s: set latest: %v", s.id, err)
					return
				}

				// PHASE E — every session completes its turn, so the floor
				// returns and the next round can classify again.
				if !gate.wait() {
					return
				}
				if _, err := planners[i].Handle(conversation.Event{
					Kind: conversation.EventSpeechComplete}); err != nil {
					gate.abort()
					errs <- fmt.Sprintf("%s round %d: speech complete: %v",
						s.id, round, err)
					return
				}

				// PHASE F — no FOREIGN marker may be observable, in any scope.
				if !gate.wait() {
					return
				}
				for j, other := range specs {
					if j == i {
						continue
					}
					if v, found := ctx.Get(conversation.ScopeConversation, "latest"); found {
						if v.Value == other.marker {
							gate.abort()
							errs <- fmt.Sprintf("%s observed %s's marker %q",
								s.id, other.id, other.marker)
							return
						}
					}
				}
				// And every earlier round's value is still this session's own.
				for r := 0; r <= round; r++ {
					k := fmt.Sprintf("round-%d", r)
					if v, found := ctx.Get(conversation.ScopeConversation, k); found {
						if v.Value != s.marker {
							gate.abort()
							errs <- fmt.Sprintf("%s: %s holds %v, want %q",
								s.id, k, v.Value, s.marker)
							return
						}
					}
				}
			}
		}(i, s)
	}

	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("service-level isolation broke: %s", e)
	}

	// Final serial sweep: each session still holds exactly its own values, and
	// the engine recorded its own last_intent.
	for _, s := range specs {
		conv, ok := vi.Bridge().Conversation(s.id)
		if !ok {
			t.Errorf("%s: conversation missing after the run", s.id)
			continue
		}
		if e, found := conv.Context().Get(conversation.ScopeConversation, "latest"); !found ||
			e.Value != s.marker {
			t.Errorf("%s: final marker = %v (found=%v), want %q",
				s.id, e.Value, found, s.marker)
		}
		if li, found := conv.Context().Get(conversation.ScopeConversation, "last_intent"); found {
			if li.Value != string(s.want) {
				t.Errorf("%s: last_intent = %v, want %q", s.id, li.Value, s.want)
			}
		}
	}

	// The service itself survived the whole run.
	if sink.has("runner failed, shutting down service") {
		t.Errorf("the service failed during concurrent load; log:\n%s", sink.dump())
	}
}

// TestT5_ContextPersistsAcrossTurnsUnderConcurrency isolates the persistence
// invariant that mutations M3 and M5 attack: a value written before a turn must
// still be there after later turns on the same session.
func TestT5_ContextPersistsAcrossTurnsUnderConcurrency(t *testing.T) {
	_, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	specs := isoSpecs()
	planners := make([]interface {
		Handle(conversation.Event) (conversation.Plan, error)
	}, len(specs))
	for i, s := range specs {
		planners[i] = openSession(t, vi, string(s.id))
	}

	gate := newBarrier(len(specs))
	errs := make(chan string, len(specs)*4)
	var wg sync.WaitGroup

	for i, s := range specs {
		wg.Add(1)
		go func(i int, s isoSpec) {
			defer wg.Done()
			conv, ok := vi.Bridge().Conversation(s.id)
			if !ok {
				gate.abort()
				errs <- string(s.id) + ": missing"
				return
			}
			ctx := conv.Context()

			// Written once, before any turn.
			if err := ctx.Set(conversation.Entry{
				Key: "established_before_turns", Value: s.marker,
				Scope: conversation.ScopeConversation, Source: "t5",
			}); err != nil {
				gate.abort()
				errs <- fmt.Sprintf("%s: set: %v", s.id, err)
				return
			}

			for round := 0; round < isoRounds; round++ {
				if !gate.wait() {
					return
				}
				if _, err := planners[i].Handle(utter(s.text)); err != nil {
					gate.abort()
					errs <- fmt.Sprintf("%s round %d: %v", s.id, round, err)
					return
				}
				if _, err := planners[i].Handle(conversation.Event{
					Kind: conversation.EventSpeechComplete}); err != nil {
					gate.abort()
					errs <- fmt.Sprintf("%s round %d: complete: %v", s.id, round, err)
					return
				}

				if !gate.wait() {
					return
				}
				e, found := ctx.Get(conversation.ScopeConversation, "established_before_turns")
				if !found {
					gate.abort()
					errs <- fmt.Sprintf("%s: context did not survive turn %d",
						s.id, round)
					return
				}
				if e.Value != s.marker {
					gate.abort()
					errs <- fmt.Sprintf("%s: value after turn %d = %v, want %q",
						s.id, round, e.Value, s.marker)
					return
				}
			}
		}(i, s)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("context persistence broke: %s", e)
	}
}

// ---------------------------------------------------------------------------
// Termination and id reuse, through the service
// ---------------------------------------------------------------------------

// TestT5_ReusedSessionIDStartsCleanThroughTheService asserts the behaviour the
// existing conversation.Engine already provides: it deletes a conversation from
// its active map at a terminal state, so Begin on the same id necessarily
// creates a fresh one. No new cleanup mechanism is introduced.
func TestT5_ReusedSessionIDStartsCleanThroughTheService(t *testing.T) {
	_, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	const id = conversation.ConversationID("t5-reused")

	openSession(t, vi, string(id))
	first, ok := vi.Bridge().Conversation(id)
	if !ok {
		t.Fatal("first session missing")
	}
	if err := first.Context().Set(conversation.Entry{
		Key: "secret_from_first", Value: "marker-first",
		Scope: conversation.ScopeConversation, Source: "t5",
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.End("t5_terminated"); err != nil {
		t.Fatalf("End: %v", err)
	}
	if !first.State().IsTerminal() {
		t.Fatalf("state after End = %v, not terminal", first.State())
	}

	// Reuse the same id through the service's bridge.
	//
	// NOTE: Bridge.Planner always calls engine.Begin, and Begin STORES into the
	// engine's active map — so a second call for a live id replaces the
	// conversation rather than returning it. The planner from this call is
	// therefore the one to keep; asking for another would discard the session
	// just opened. (An earlier draft of this test did exactly that and observed
	// an empty intent, because the third conversation had never had its floor
	// opened.)
	reused := openSession(t, vi, string(id))
	second, ok := vi.Bridge().Conversation(id)
	if !ok {
		t.Fatal("reused session missing")
	}
	if second == first {
		t.Fatal("reusing the id returned the SAME conversation")
	}
	if _, found := second.Context().Get(conversation.ScopeConversation, "secret_from_first"); found {
		t.Error("the reused session observed the terminated session's context")
	}
	if _, found := second.Context().Lookup("secret_from_first"); found {
		t.Error("Lookup across scopes found the terminated session's value")
	}
	if second.State().IsTerminal() {
		t.Error("the reused session started terminal")
	}

	// And it works, using the planner from the reopened session.
	plan, err := reused.Handle(utter("transfer me to rajesh"))
	if err != nil {
		t.Fatalf("reused session cannot classify: %v", err)
	}
	if plan.Intent != intent.IntentRequestTransfer {
		t.Errorf("reused session intent = %q", plan.Intent)
	}
}

// ---------------------------------------------------------------------------
// Concurrent failure isolation
// ---------------------------------------------------------------------------

// TestT5_ConcurrentFailureStaysScopedToItsSession runs failing and healthy
// sessions simultaneously, released together, so the failures land while the
// healthy sessions are mid-flight.
func TestT5_ConcurrentFailureStaysScopedToItsSession(t *testing.T) {
	sink, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	specs := isoSpecs()
	type session struct {
		spec    isoSpec
		planner interface {
			Handle(conversation.Event) (conversation.Plan, error)
		}
		failing bool
	}
	sessions := make([]session, len(specs))
	for i, s := range specs {
		sessions[i] = session{
			spec:    s,
			planner: openSession(t, vi, string(s.id)),
			failing: i%2 == 0, // half will escalate
		}
	}

	gate := newBarrier(len(sessions))
	errs := make(chan string, len(sessions)*2)
	var wg sync.WaitGroup

	for _, sn := range sessions {
		wg.Add(1)
		go func(sn session) {
			defer wg.Done()
			if !gate.wait() {
				return
			}

			if sn.failing {
				// Below the frozen reject threshold: a real typed escalation.
				plan, err := sn.planner.Handle(utter("callback transfer"))
				if err != nil {
					gate.abort()
					errs <- fmt.Sprintf("%s: below-reject returned an error rather "+
						"than a plan: %v", sn.spec.id, err)
					return
				}
				if plan.Action != conversation.ActionEscalate {
					gate.abort()
					errs <- fmt.Sprintf("%s: action %v, want ActionEscalate",
						sn.spec.id, plan.Action)
					return
				}
				if plan.Reason != "intent_rejected" {
					gate.abort()
					errs <- fmt.Sprintf("%s: reason %q, want intent_rejected",
						sn.spec.id, plan.Reason)
					return
				}
				// Terminal: further work is refused, not panicked.
				if _, err := sn.planner.Handle(utter("are you there")); err == nil {
					gate.abort()
					errs <- fmt.Sprintf("%s: terminal session accepted more work",
						sn.spec.id)
				}
				return
			}

			// Healthy session, running at the same moment.
			plan, err := sn.planner.Handle(utter(sn.spec.text))
			if err != nil {
				gate.abort()
				errs <- fmt.Sprintf("%s: a sibling's failure broke this session: %v",
					sn.spec.id, err)
				return
			}
			if plan.Intent != sn.spec.want {
				gate.abort()
				errs <- fmt.Sprintf("%s: intent %q, want %q while siblings failed",
					sn.spec.id, plan.Intent, sn.spec.want)
			}
		}(sn)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent failure isolation broke: %s", e)
	}

	// The service is alive and a brand-new session still works.
	select {
	case err := <-done:
		t.Fatalf("service exited during concurrent failures (err=%v); log:\n%s",
			err, sink.dump())
	default:
	}
	if sink.has("runner failed, shutting down service") {
		t.Errorf("session failures became a service failure; log:\n%s", sink.dump())
	}
	fresh := openSession(t, vi, "t5-after-failures")
	plan, err := fresh.Handle(utter("say that again"))
	if err != nil {
		t.Fatalf("new session after concurrent failures: %v", err)
	}
	if plan.Intent != intent.IntentRepeat {
		t.Errorf("new session intent = %q", plan.Intent)
	}
}

// ---------------------------------------------------------------------------
// Structural: the service introduces no context architecture of its own
// ---------------------------------------------------------------------------

// TestT5_ServiceIntroducesNoSessionRegistryOrGlobalContext is the guard
// mutation M4 attacks. It inspects the service's own source: a session map, a
// global bridge or any package-level mutable state would all be visible here.
func TestT5_ServiceIntroducesNoSessionRegistryOrGlobalContext(t *testing.T) {
	t.Parallel()

	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing main.go: %v", err)
	}

	// No package-level mutable state. Consts and `var _ T = ...` assertions are
	// immutable and permitted.
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, id := range vs.Names {
				if id.Name == "_" {
					continue
				}
				t.Errorf("main.go declares package-level var %q; sessions must be "+
					"reachable only through Bridge.Planner(ConversationID)", id.Name)
			}
		}
	}

	// No map anywhere: a session registry or context cache would need one.
	ast.Inspect(f, func(n ast.Node) bool {
		if _, ok := n.(*ast.MapType); ok {
			t.Error("main.go declares a map type; the service keeps no session " +
				"registry and no per-session cache")
		}
		return true
	})

	// No persistence or second context system reachable from the service file.
	for _, imp := range f.Imports {
		path := trimQuotes(imp.Path.Value)
		for _, banned := range []string{
			"packages/go/memory", "database/sql", "packages/go/persistence",
			"packages/go/redis", "packages/go/repository",
		} {
			if pathContains(path, banned) {
				t.Errorf("main.go imports %q; no persistence or second memory "+
					"system is permitted", path)
			}
		}
	}

	// The service must not construct a ContextEngine itself.
	if pathContains(sourceOf(t), "NewContextEngine") {
		t.Error("main.go constructs a ContextEngine; context belongs to the " +
			"frozen conversation.Engine, one per conversation")
	}
}

func trimQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func pathContains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}()
}
