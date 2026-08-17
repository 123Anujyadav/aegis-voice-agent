package main

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/intent"
)

// Phase 14 T8 — concurrency of classification TOGETHER WITH lifecycle events.
//
// Already proven and not repeated here: T5 (16-session barrier isolation), T6
// (goroutine accounting, cancellation propagation), T7 (concurrent mixed
// failures). T8's new ground is classification running SIMULTANEOUSLY with
// interruption, cancellation, termination and fresh-session creation.
//
// TWO MEASURED CONSTRAINTS shape this file:
//
//  1. conversation.Handle is synchronous and takes NO context. Cancellation and
//     interruption are therefore observed at TURN BOUNDARIES, never mid-turn.
//     Nothing below claims otherwise.
//  2. The default persona bounds a conversation at MaxTurns 40 counting BOTH
//     parties — 20 caller round-trips. 40 iterations per session therefore
//     cannot run on a single conversation, so each session rotates through its
//     own private sequence of ConversationIDs. A "session" here is a logical
//     caller holding several sequential conversations, which is also what
//     production looks like.
//
// Sessions are opened on the test goroutine, never inside a worker: openSession
// calls t.Fatalf, which must not be reached from a non-test goroutine.

const (
	t8Sessions   = 16
	t8Iterations = 40
	t8BatchSize  = 12 // caller round-trips per conversation, inside the budget
	t8Batches    = (t8Iterations + t8BatchSize - 1) / t8BatchSize
)

// turnHandler names the anonymous interface openSession returns.
type turnHandler interface {
	Handle(conversation.Event) (conversation.Plan, error)
}

// t8Spec is one logical caller.
type t8Spec struct {
	index  int
	marker string
	text   string
	want   conversation.IntentName
}

func t8Specs() []t8Spec {
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
	out := make([]t8Spec, t8Sessions)
	for i := range out {
		b := base[i%len(base)]
		out[i] = t8Spec{
			index:  i,
			marker: fmt.Sprintf("t8-marker-%02d", i),
			text:   b.text,
			want:   b.want,
		}
	}
	return out
}

// id returns the ConversationID for one caller's nth conversation. Each caller
// owns a private id space, so a foreign marker can never appear by coincidence.
func (s t8Spec) id(prefix string, batch int) conversation.ConversationID {
	return conversation.ConversationID(fmt.Sprintf("%s-s%02d-b%d", prefix, s.index, batch))
}

// t8Session is one caller's pre-opened conversations.
type t8Session struct {
	spec     t8Spec
	planners []turnHandler
	convs    []*conversation.Conversation
}

// mustFinish runs fn off the test goroutine and fails if it has not returned
// within d.
//
// Setup is bounded for the same reason the workers are. A production defect
// that blocks a turn — a lock held across Handle, say — would otherwise hang
// session setup on the test goroutine, where nothing is watching, and the whole
// package would die on its -timeout with a stack dump instead of naming the
// problem. This turns that into a diagnostic failure. The blocked goroutine is
// deliberately abandoned: it cannot be unblocked, and the run is over anyway.
func mustFinish(t *testing.T, d time.Duration, what string, fn func() error) {
	t.Helper()
	res := make(chan error, 1)
	go func() { res <- fn() }()
	select {
	case err := <-res:
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	case <-time.After(d):
		t.Fatalf("%s blocked for %s — a turn is not completing", what, d)
	}
}

// openPlanner starts one conversation and opens the floor. It returns errors
// rather than calling t.Fatalf, so it is safe to run off the test goroutine
// under mustFinish.
func openPlanner(vi *voiceIntelligence, id conversation.ConversationID) (
	turnHandler, *conversation.Conversation, error,
) {
	p, err := vi.Bridge().Planner(id, "")
	if err != nil {
		return nil, nil, fmt.Errorf("Planner(%s): %w", id, err)
	}
	for _, e := range []conversation.Event{
		{Kind: conversation.EventStart},
		{Kind: conversation.EventGreetingComplete},
	} {
		if _, err := p.Handle(e); err != nil {
			return nil, nil, fmt.Errorf("%s %v: %w", id, e.Kind, err)
		}
	}
	c, ok := vi.Bridge().Conversation(id)
	if !ok {
		return nil, nil, fmt.Errorf("Conversation(%s) missing after Planner", id)
	}
	return p, c, nil
}

// openSessions opens `batches` conversations per caller and seeds each with the
// caller's private marker. Bounded, per mustFinish.
func openSessions(t *testing.T, vi *voiceIntelligence, prefix string, batches int) []t8Session {
	t.Helper()
	specs := t8Specs()
	out := make([]t8Session, len(specs))
	mustFinish(t, 30*time.Second, "opening sessions", func() error {
		for i, s := range specs {
			out[i].spec = s
			for b := 0; b < batches; b++ {
				id := s.id(prefix, b)
				p, c, err := openPlanner(vi, id)
				if err != nil {
					return err
				}
				if err := c.Context().Set(conversation.Entry{
					Key:    "owner",
					Value:  s.marker,
					Scope:  conversation.ScopeConversation,
					Source: "t8",
				}); err != nil {
					return fmt.Errorf("%s: seeding owner: %w", id, err)
				}
				out[i].planners = append(out[i].planners, p)
				out[i].convs = append(out[i].convs, c)
			}
		}
		return nil
	})
	return out
}

// awaitAll waits for wg, bounded. It aborts the barrier and fails on timeout so
// a stuck worker reports as a failure rather than as a package-level timeout.
func awaitAll(t *testing.T, wg *sync.WaitGroup, gate *barrier, d time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		gate.abort()
		t.Fatalf("%s did not finish within %s", what, d)
	}
}

// ---------------------------------------------------------------------------
// Concurrent classification under load
// ---------------------------------------------------------------------------

// TestT8_ConcurrentClassificationUnderLoad runs 16 callers x 40 classifications
// through one running service, phase-synchronised so the work genuinely
// overlaps rather than merely being scheduled together.
func TestT8_ConcurrentClassificationUnderLoad(t *testing.T) {
	sink, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	sessions := openSessions(t, vi, "t8-load", t8Batches)
	gate := newBarrier(len(sessions))
	errs := make(chan string, len(sessions)*4)
	var completed atomic.Int64
	var wg sync.WaitGroup

	for _, sess := range sessions {
		wg.Add(1)
		go func(sess t8Session) {
			defer wg.Done()
			fail := func(format string, a ...any) {
				gate.abort()
				errs <- fmt.Sprintf(format, a...)
			}

			for i := 0; i < t8Iterations; i++ {
				b := i / t8BatchSize
				p, conv, id := sess.planners[b], sess.convs[b], sess.spec.id("t8-load", b)

				// PHASE A — every caller classifies at the same instant.
				if !gate.wait() {
					return
				}
				plan, err := p.Handle(utter(sess.spec.text))
				if err != nil {
					fail("%s iter %d: %v", id, i, err)
					return
				}
				if plan.Intent != sess.spec.want {
					fail("%s iter %d: intent %q, want %q — another caller's "+
						"classification surfaced", id, i, plan.Intent, sess.spec.want)
					return
				}

				// PHASE B — every caller reads its own context back.
				if !gate.wait() {
					return
				}
				e, ok := conv.Context().Get(conversation.ScopeConversation, "owner")
				if !ok || e.Value != sess.spec.marker {
					fail("%s iter %d: owner = %v (present=%v), want %q",
						id, i, e.Value, ok, sess.spec.marker)
					return
				}

				// PHASE C — complete the turn so the floor returns to the caller.
				if !gate.wait() {
					return
				}
				if _, err := p.Handle(conversation.Event{
					Kind: conversation.EventSpeechComplete,
				}); err != nil {
					fail("%s iter %d: speech-complete: %v", id, i, err)
					return
				}
				completed.Add(1)
			}
		}(sess)
	}

	awaitAll(t, &wg, gate, 180*time.Second, "concurrent classification")
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent classification broke: %s", e)
	}

	// Every caller completed every iteration — no silent short-circuit.
	if got, want := completed.Load(), int64(len(sessions)*t8Iterations); got != want {
		t.Errorf("%d turns completed, want %d", got, want)
	}
	if sink.has("runner failed, shutting down service") {
		t.Errorf("concurrent load became a service failure; log:\n%s", sink.dump())
	}
}

// ---------------------------------------------------------------------------
// Interruption during concurrent classification
// ---------------------------------------------------------------------------

// TestT8_InterruptionDuringConcurrentClassification uses the EXISTING frozen
// mechanism — conversation.Event{Kind: EventInterrupt, Interruption:
// InterruptionUser}. No new state machine, no new enum, no new event kind.
//
// MEASURED semantics, asserted below: the plan is ActionIgnore with reason
// "interrupted_user", the floor is force-yielded from agent to caller, the
// interruption is recorded in the frozen history, and the session stays usable.
//
// Interruption is delivered at a TURN BOUNDARY. Handle is synchronous, so there
// is no such thing here as interrupting a turn already executing.
func TestT8_InterruptionDuringConcurrentClassification(t *testing.T) {
	sink, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	sessions := openSessions(t, vi, "t8-int", 1)
	gate := newBarrier(len(sessions))
	errs := make(chan string, len(sessions)*3)
	var interrupted, unaffected atomic.Int32
	var wg sync.WaitGroup

	for _, sess := range sessions {
		wg.Add(1)
		go func(sess t8Session) {
			defer wg.Done()
			p, conv, id := sess.planners[0], sess.convs[0], sess.spec.id("t8-int", 0)
			fail := func(format string, a ...any) {
				gate.abort()
				errs <- fmt.Sprintf(format, a...)
			}

			// Everyone takes a normal turn first, so the agent holds the floor:
			// that is what makes a barge-in meaningful.
			if !gate.wait() {
				return
			}
			if _, err := p.Handle(utter(sess.spec.text)); err != nil {
				fail("%s: first turn: %v", id, err)
				return
			}
			if got := conv.Turns().Holder(); got != conversation.PartyAgent {
				fail("%s: holder %v before interrupt, want agent", id, got)
				return
			}

			// Half barge in; the rest keep classifying at the same instant.
			if !gate.wait() {
				return
			}
			if sess.spec.index%2 == 0 {
				plan, err := p.Handle(conversation.Event{
					Kind:         conversation.EventInterrupt,
					Interruption: conversation.InterruptionUser,
					Party:        conversation.PartyCaller,
					Reason:       "barge_in",
				})
				if err != nil {
					fail("%s: interrupt: %v", id, err)
					return
				}
				if plan.Action != conversation.ActionIgnore {
					fail("%s: action %v, want ActionIgnore", id, plan.Action)
					return
				}
				if plan.Reason != "interrupted_user" {
					fail("%s: reason %q, want interrupted_user", id, plan.Reason)
					return
				}
				// The frozen floor was force-yielded to the caller.
				if got := conv.Turns().Holder(); got != conversation.PartyCaller {
					fail("%s: holder %v after barge-in, want caller", id, got)
					return
				}
				if n := len(conv.Interruptions().History()); n != 1 {
					fail("%s: %d interruptions recorded, want 1", id, n)
					return
				}
				interrupted.Add(1)

				// The session remains usable after being interrupted.
				next, err := p.Handle(utter(sess.spec.text))
				if err != nil {
					fail("%s: turn after interrupt: %v", id, err)
					return
				}
				if next.Intent != sess.spec.want {
					fail("%s: post-interrupt intent %q, want %q",
						id, next.Intent, sess.spec.want)
				}
				return
			}

			// Uninterrupted callers must be untouched by their neighbours.
			if _, err := p.Handle(conversation.Event{
				Kind: conversation.EventSpeechComplete,
			}); err != nil {
				fail("%s: speech-complete: %v", id, err)
				return
			}
			plan, err := p.Handle(utter(sess.spec.text))
			if err != nil {
				fail("%s: a neighbour's barge-in broke this session: %v", id, err)
				return
			}
			if plan.Intent != sess.spec.want {
				fail("%s: intent %q, want %q", id, plan.Intent, sess.spec.want)
				return
			}
			if n := len(conv.Interruptions().History()); n != 0 {
				fail("%s: %d interruptions recorded on a session that was never "+
					"interrupted", id, n)
				return
			}
			unaffected.Add(1)
		}(sess)
	}

	awaitAll(t, &wg, gate, 60*time.Second, "concurrent interruption")
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent interruption broke: %s", e)
	}

	if got := interrupted.Load(); got != t8Sessions/2 {
		t.Errorf("%d sessions interrupted, want %d", got, t8Sessions/2)
	}
	if got := unaffected.Load(); got != t8Sessions/2 {
		t.Errorf("%d sessions classified unaffected, want %d", got, t8Sessions/2)
	}
	if sink.has("runner failed, shutting down service") {
		t.Errorf("barge-in became a service failure; log:\n%s", sink.dump())
	}
}

// ---------------------------------------------------------------------------
// Cancellation and termination during concurrent classification
// ---------------------------------------------------------------------------

// TestT8_CancellationAndTerminationDuringClassification releases three things
// from the same barrier: sessions that terminate, sessions that keep
// classifying, and the cancellation of the service itself.
//
// Cancellation is observed BETWEEN turns. It does not abort a Handle already in
// progress, and this test does not assert that it does.
func TestT8_CancellationAndTerminationDuringClassification(t *testing.T) {
	sink, vi, cancel, done := runningService(t)

	ctx, ctxCancel := context.WithCancel(context.Background())
	defer ctxCancel()

	sessions := openSessions(t, vi, "t8-life", 1)
	// +1 participant: the canceller, released with everyone else.
	gate := newBarrier(len(sessions) + 1)
	errs := make(chan string, len(sessions)*3)
	var terminated, observedCancel atomic.Int32
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if !gate.wait() { // phase 1
			return
		}
		if !gate.wait() { // phase 2 — cancel as the workers act
			return
		}
		ctxCancel()
		cancel()
	}()

	wantTerminated := 0
	for _, sess := range sessions {
		if sess.spec.index%3 == 0 {
			wantTerminated++
		}
		wg.Add(1)
		go func(sess t8Session) {
			defer wg.Done()
			p, conv, id := sess.planners[0], sess.convs[0], sess.spec.id("t8-life", 0)
			fail := func(format string, a ...any) {
				gate.abort()
				errs <- fmt.Sprintf(format, a...)
			}

			if !gate.wait() { // phase 1
				return
			}
			if _, err := p.Handle(utter(sess.spec.text)); err != nil {
				fail("%s: first turn: %v", id, err)
				return
			}

			if !gate.wait() { // phase 2
				return
			}
			if sess.spec.index%3 == 0 {
				// Terminate while neighbours are mid-classification.
				if err := conv.End("t8_terminated"); err != nil {
					fail("%s: End: %v", id, err)
					return
				}
				if !conv.State().IsTerminal() {
					fail("%s: state %v after End, want terminal", id, conv.State())
					return
				}
				// A terminated session must refuse further work.
				if _, err := p.Handle(utter(sess.spec.text)); !errors.Is(err, conversation.ErrTerminal) {
					fail("%s: a terminated session accepted a later event (err=%v, "+
						"want ErrTerminal)", id, err)
					return
				}
				terminated.Add(1)
				return
			}

			// Everyone else classifies until cancellation is observed at a turn
			// boundary. Bounded by the frozen turn budget.
			for i := 0; i < t8BatchSize; i++ {
				select {
				case <-ctx.Done():
					observedCancel.Add(1)
					return
				default:
				}
				if _, err := p.Handle(conversation.Event{
					Kind: conversation.EventSpeechComplete,
				}); err != nil {
					fail("%s: speech-complete during cancellation: %v", id, err)
					return
				}
				if _, err := p.Handle(utter(sess.spec.text)); err != nil {
					fail("%s: a neighbour's termination broke this session: %v", id, err)
					return
				}
			}
			select {
			case <-ctx.Done():
				observedCancel.Add(1)
			case <-time.After(30 * time.Second):
				fail("%s: never observed cancellation", id)
			}
		}(sess)
	}

	awaitAll(t, &wg, gate, 90*time.Second, "cancellation/termination workers")
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent cancellation/termination broke: %s", e)
	}

	if got := terminated.Load(); got != int32(wantTerminated) {
		t.Errorf("%d sessions terminated, want %d", got, wantTerminated)
	}
	if got, want := observedCancel.Load(), int32(len(sessions)-wantTerminated); got != want {
		t.Errorf("%d sessions observed cancellation, want %d", got, want)
	}

	// The service still shuts down cleanly after all of that.
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v; log:\n%s", err, sink.dump())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("service did not shut down; log:\n%s", sink.dump())
	}
}

// TestT8_FreshSessionsDuringConcurrentLoad creates new sessions while existing
// ones are classifying: a caller arriving mid-load must be served, and must not
// disturb anyone already in progress.
func TestT8_FreshSessionsDuringConcurrentLoad(t *testing.T) {
	_, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	sessions := openSessions(t, vi, "t8-fresh", 1)
	// +1 participant: the goroutine admitting new callers.
	gate := newBarrier(len(sessions) + 1)
	errs := make(chan string, len(sessions)+8)
	var wg sync.WaitGroup

	// New arrivals go through Bridge.Planner directly — openSession cannot be
	// used off the test goroutine because it calls t.Fatalf.
	const arrivals = 8
	var admitted atomic.Int32
	wg.Add(1)
	go func() {
		defer wg.Done()
		fail := func(format string, a ...any) {
			gate.abort()
			errs <- fmt.Sprintf(format, a...)
		}
		if !gate.wait() {
			return
		}
		for i := 0; i < arrivals; i++ {
			id := conversation.ConversationID(fmt.Sprintf("t8-arrival-%02d", i))
			p, err := vi.Bridge().Planner(id, "")
			if err != nil {
				fail("%s: Planner: %v", id, err)
				return
			}
			for _, e := range []conversation.Event{
				{Kind: conversation.EventStart},
				{Kind: conversation.EventGreetingComplete},
			} {
				if _, err := p.Handle(e); err != nil {
					fail("%s: %v: %v", id, e.Kind, err)
					return
				}
			}
			plan, err := p.Handle(utter("transfer me to rajesh"))
			if err != nil {
				fail("%s: arrival turn: %v", id, err)
				return
			}
			if plan.Intent != intent.IntentRequestTransfer {
				fail("%s: arrival intent %q, want %q",
					id, plan.Intent, intent.IntentRequestTransfer)
				return
			}
			admitted.Add(1)
		}
	}()

	for _, sess := range sessions {
		wg.Add(1)
		go func(sess t8Session) {
			defer wg.Done()
			p, conv, id := sess.planners[0], sess.convs[0], sess.spec.id("t8-fresh", 0)
			fail := func(format string, a ...any) {
				gate.abort()
				errs <- fmt.Sprintf(format, a...)
			}
			if !gate.wait() {
				return
			}
			for i := 0; i < t8BatchSize; i++ {
				plan, err := p.Handle(utter(sess.spec.text))
				if err != nil {
					fail("%s iter %d: a new arrival broke this in-progress "+
						"session: %v", id, i, err)
					return
				}
				if plan.Intent != sess.spec.want {
					fail("%s iter %d: intent %q, want %q", id, i, plan.Intent, sess.spec.want)
					return
				}
				if _, err := p.Handle(conversation.Event{
					Kind: conversation.EventSpeechComplete,
				}); err != nil {
					fail("%s iter %d: speech-complete: %v", id, i, err)
					return
				}
			}
			e, ok := conv.Context().Get(conversation.ScopeConversation, "owner")
			if !ok || e.Value != sess.spec.marker {
				fail("%s: owner = %v, want %q — a new arrival reached an existing "+
					"session's context", id, e.Value, sess.spec.marker)
			}
		}(sess)
	}

	awaitAll(t, &wg, gate, 90*time.Second, "fresh sessions under load")
	close(errs)
	for e := range errs {
		t.Fatalf("fresh-session admission broke: %s", e)
	}
	if got := admitted.Load(); got != arrivals {
		t.Errorf("%d callers admitted mid-load, want %d", got, arrivals)
	}
}

// ---------------------------------------------------------------------------
// Shared classifier safety
// ---------------------------------------------------------------------------

// TestT8_SharedClassifierHoldsNoPerSessionState proves behaviourally that the
// one classifier shared by every session carries nothing between them: results
// from 16 concurrent callers must match a SERIAL baseline exactly.
//
// A classifier holding per-session state shows up here as one caller's input
// changing another caller's result.
func TestT8_SharedClassifierHoldsNoPerSessionState(t *testing.T) {
	_, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	specs := t8Specs()

	// Serial baseline first: the known-good answer, no concurrency involved.
	baseline := make([]string, len(specs))
	mustFinish(t, 30*time.Second, "serial baseline", func() error {
		for i, s := range specs {
			p, _, err := openPlanner(vi, conversation.ConversationID(
				fmt.Sprintf("t8-base-%02d", i)))
			if err != nil {
				return err
			}
			plan, err := p.Handle(utter(s.text))
			if err != nil {
				return fmt.Errorf("baseline %d: %w", i, err)
			}
			baseline[i] = fmt.Sprintf("%s|%v|%s", plan.Intent, plan.Action, plan.Reason)
		}
		return nil
	})

	// Now the same work concurrently, interleaved at one barrier.
	planners := make([]turnHandler, len(specs))
	mustFinish(t, 30*time.Second, "opening concurrent sessions", func() error {
		for i := range specs {
			p, _, err := openPlanner(vi, conversation.ConversationID(
				fmt.Sprintf("t8-conc-%02d", i)))
			if err != nil {
				return err
			}
			planners[i] = p
		}
		return nil
	})
	gate := newBarrier(len(specs))
	got := make([]string, len(specs))
	var wg sync.WaitGroup
	for i, s := range specs {
		wg.Add(1)
		go func(i int, s t8Spec) {
			defer wg.Done()
			if !gate.wait() {
				return
			}
			plan, err := planners[i].Handle(utter(s.text))
			if err != nil {
				got[i] = "err:" + err.Error()
				return
			}
			got[i] = fmt.Sprintf("%s|%v|%s", plan.Intent, plan.Action, plan.Reason)
		}(i, s)
	}
	awaitAll(t, &wg, gate, 60*time.Second, "shared-classifier comparison")

	for i := range specs {
		if got[i] != baseline[i] {
			t.Errorf("caller %d: concurrent %q != serial %q — the shared classifier "+
				"is carrying state between sessions", i, got[i], baseline[i])
		}
	}
}

// TestT8_ClassifierHasNoReceiverWritesOrLocking is the structural half: a
// classifier shared across sessions must be unable to hold state at all.
//
// The intended outcome is an immutable, config-driven classifier — NOT a mutex
// hiding shared mutable state, which is why this checks for receiver writes
// rather than for the presence of locking.
func TestT8_ClassifierHasNoReceiverWritesOrLocking(t *testing.T) {
	t.Parallel()

	const path = "../../../../../packages/go/intent/classifier.go"
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parsing classifier.go: %v", err)
	}

	var checked int
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 || fn.Body == nil {
			return true
		}
		recvType := fn.Recv.List[0].Type
		if star, ok := recvType.(*ast.StarExpr); ok {
			recvType = star.X
		}
		if id, ok := recvType.(*ast.Ident); !ok || id.Name != "Classifier" {
			return true
		}
		if len(fn.Recv.List[0].Names) == 0 {
			return true // an unnamed receiver cannot be written to
		}
		recv := fn.Recv.List[0].Names[0].Name
		checked++

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			var lhs []ast.Expr
			switch s := n.(type) {
			case *ast.AssignStmt:
				lhs = s.Lhs
			case *ast.IncDecStmt:
				lhs = []ast.Expr{s.X}
			default:
				return true
			}
			for _, l := range lhs {
				sel, ok := l.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == recv {
					t.Errorf("%s writes %s.%s — a classifier shared across sessions "+
						"must hold no per-call state", fn.Name.Name, recv, sel.Sel.Name)
				}
			}
			return true
		})
		return true
	})
	if checked == 0 {
		t.Fatal("inspected no Classifier methods; this guard would pass vacuously")
	}

	// A sync primitive here would mean shared mutable state was being guarded
	// rather than removed.
	for _, imp := range f.Imports {
		if strings.Trim(imp.Path.Value, `"`) == "sync" {
			t.Error("classifier.go imports sync — the design intent is an immutable " +
				"classifier, not a lock over shared mutable state")
		}
	}
}

// ---------------------------------------------------------------------------
// Determinism of the concurrent path
// ---------------------------------------------------------------------------

// TestT8_ConcurrentSemanticSignatureIsStable replays the concurrent workload 11
// times and requires an identical semantic signature every time.
//
// Per-caller results are sorted before comparison, so goroutine completion
// order — which is not a semantic property — cannot enter the signature.
func TestT8_ConcurrentSemanticSignatureIsStable(t *testing.T) {
	cfg := conversation.DefaultIntentConfig()
	confClass := func(c float64) string {
		switch {
		case c <= 0:
			return "zero"
		case c < cfg.RejectThreshold:
			return "below_reject"
		case c < cfg.AcceptThreshold:
			return "clarify_band"
		case c < 1:
			return "accept"
		default:
			return "certain"
		}
	}

	run := func(pass int) string {
		_, vi := build(t)
		specs := t8Specs()
		planners := make([]turnHandler, len(specs))
		convs := make([]*conversation.Conversation, len(specs))
		mustFinish(t, 30*time.Second, "opening signature sessions", func() error {
			for i := range specs {
				p, c, err := openPlanner(vi, conversation.ConversationID(
					fmt.Sprintf("t8-sig-%d-%02d", pass, i)))
				if err != nil {
					return err
				}
				planners[i], convs[i] = p, c
			}
			return nil
		})

		gate := newBarrier(len(specs))
		out := make([]string, len(specs))
		var wg sync.WaitGroup
		for i, s := range specs {
			wg.Add(1)
			go func(i int, s t8Spec) {
				defer wg.Done()
				if !gate.wait() {
					return
				}
				plan, err := planners[i].Handle(utter(s.text))
				if err != nil {
					out[i] = fmt.Sprintf("%02d=err:%v", s.index, err)
					return
				}
				out[i] = fmt.Sprintf("%02d=%s|%s|%v|%s|%v|%v|%v",
					s.index, plan.Intent, confClass(plan.Confidence), plan.Action,
					plan.Reason, plan.Clarification.Kind, plan.NextState,
					convs[i].State().IsTerminal())
			}(i, s)
		}
		awaitAll(t, &wg, gate, 60*time.Second, "signature pass")
		sort.Strings(out) // completion order is not a semantic property
		return strings.Join(out, "\n")
	}

	want := run(0)
	if strings.TrimSpace(want) == "" {
		t.Fatal("empty signature")
	}
	for i := 1; i <= 10; i++ {
		if got := run(i); got != want {
			t.Fatalf("pass %d drifted\n got:\n%s\nwant:\n%s", i, got, want)
		}
	}
}
