package voiceintel_test

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/intent"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	"github.com/callscreen/callscreen-platform/packages/go/voiceintel"
)

// T10 — CONCURRENCY, ISOLATION & CANCELLATION.
//
// A VERIFICATION task. No production synchronisation, cache, registry or shared
// mutable state was added; the tests below either hold or the design is wrong.
//
// WHAT IS NEW HERE. Earlier tasks already cover parts of this ground and are not
// repeated:
//
//	T7  TestContext_SixteenConcurrentSessionsStayIsolated — context only
//	T8  TestTurn_ConcurrentClassificationIsIsolated       — pure ClassifyTurn
//	T9  TestFailure13_ConcurrentFailuresStayIsolated      — failure pressure
//	T6  TestConcurrentBridgeIsolation                     — per-session intents
//
// T10's ground is what happens when those run TOGETHER and against each other:
// classification through the real bridge while contexts are written, while
// sessions are cancelled, interrupted and terminated, all on one shared
// classifier — and the structural question of whether the Bridge itself can
// hold cross-session state at all.
//
// RACE DETECTOR: not run locally, no C compiler. See the T10 report. Nothing
// here is evidence of race safety; these are value-isolation and determinism
// properties.

const t10Sessions = 16

// sessionSpec is one concurrent session's fixture: a distinct utterance, the
// intent it must resolve to, and a marker unique to the session.
type sessionSpec struct {
	id     string
	text   string
	want   conversation.IntentName
	marker string
}

// t10Specs builds 16 sessions spread across the closed vocabulary, each with a
// unique marker. Utterances repeat across sessions on purpose: if isolation
// depended on inputs being distinct it would not be isolation.
func t10Specs() []sessionSpec {
	// Only utterances that resolve to a COMPLETE intent belong here.
	//
	// MEASURED, not assumed: "i want to leave a message" resolves to
	// leave_message, whose required message_body slot the utterance never
	// fills, so the planner asks (ActionAsk / clarify_missing_slot) and the
	// frozen clarification budget escalates the conversation on the third
	// attempt -- clarification_exhausted -> StateEscalated, which is terminal.
	// The same is true of call_purpose and greeting. That is the budget working
	// as designed, and it is exercised deliberately in
	// TestT10_ClarificationBudgetEscalationIsPerSession rather than being
	// allowed to masquerade as a concurrency failure here.
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
	specs := make([]sessionSpec, t10Sessions)
	for i := range specs {
		b := base[i%len(base)]
		specs[i] = sessionSpec{
			id:     fmt.Sprintf("t10-%02d", i),
			text:   b.text,
			want:   b.want,
			marker: fmt.Sprintf("marker-%02d-%s", i, strings.ToUpper(string(b.want))),
		}
	}
	return specs
}

// realBridge builds one bridge with the REAL deterministic classifier: one
// immutable classifier and one immutable config shared by every session.
func realBridge(t *testing.T) *voiceintel.Bridge {
	t.Helper()
	b, err := voiceintel.New(voiceintel.WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("voiceintel.New: %v", err)
	}
	return b
}

// openSessions begins every session and opens its floor, serially, so the
// concurrent phase starts from a known state.
func openSessions(t *testing.T, b *voiceintel.Bridge, specs []sessionSpec) {
	t.Helper()
	for _, s := range specs {
		p, err := b.Planner(conversation.ConversationID(s.id), "")
		if err != nil {
			t.Fatalf("Planner(%s): %v", s.id, err)
		}
		openFloor(t, p)
	}
}

// turn drives one complete caller turn and returns the plan.
func turn(p interface {
	Handle(conversation.Event) (conversation.Plan, error)
}, text string) (conversation.Plan, error) {
	plan, err := p.Handle(utteranceEvent(text))
	if err != nil {
		return plan, err
	}
	_, err = p.Handle(conversation.Event{Kind: conversation.EventSpeechComplete})
	return plan, err
}

// ---------------------------------------------------------------------------
// 1 + 2 + 3. Sixteen sessions, shared classifier, unique markers
// ---------------------------------------------------------------------------

// TestT10_SixteenSessionsClassifyAndStoreConcurrently is the core matrix:
// sixteen sessions on one bridge, each classifying through the REAL classifier
// while writing its own context, all at once.
func TestT10_SixteenSessionsClassifyAndStoreConcurrently(t *testing.T) {
	t.Parallel()

	specs := t10Specs()
	b := realBridge(t)
	openSessions(t, b, specs)

	var wg sync.WaitGroup
	errs := make(chan string, len(specs)*8)

	for _, s := range specs {
		wg.Add(1)
		go func(s sessionSpec) {
			defer wg.Done()

			p, ok := b.Conversation(conversation.ConversationID(s.id))
			if !ok {
				errs <- s.id + ": conversation missing"
				return
			}

			for round := 0; round < 12; round++ {
				// Context write unique to this session.
				if err := p.Context().Set(conversation.Entry{
					Key: "session_marker", Value: s.marker,
					Scope: conversation.ScopeConversation, Source: "t10",
				}); err != nil {
					errs <- fmt.Sprintf("%s: set: %v", s.id, err)
					return
				}

				// Classification through the shared classifier.
				plan, err := turn(p, s.text)
				if err != nil {
					errs <- fmt.Sprintf("%s round %d: %v", s.id, round, err)
					return
				}

				// 2 — the classification belongs to THIS session.
				if plan.Intent != s.want {
					errs <- fmt.Sprintf("%s round %d: intent %q, want %q — another "+
						"session's classification surfaced here",
						s.id, round, plan.Intent, s.want)
					return
				}

				// 2 — no other session's context is observable.
				e, ok := p.Context().Get(conversation.ScopeConversation, "session_marker")
				if !ok {
					errs <- s.id + ": marker vanished"
					return
				}
				if e.Value != s.marker {
					errs <- fmt.Sprintf("%s: marker is %v, want %q — context crossed "+
						"sessions", s.id, e.Value, s.marker)
					return
				}

				// 4 — context stays bounded under concurrent write pressure.
				if n := p.Context().Size(conversation.ScopeConversation); n > frozenMaxEntriesPerScope {
					errs <- fmt.Sprintf("%s: context grew to %d", s.id, n)
					return
				}
			}
		}(s)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent session matrix failed: %s", e)
	}

	// Final sweep, serially: every session still holds exactly its own marker
	// and its own last_intent.
	for _, s := range specs {
		conv, _ := b.Conversation(conversation.ConversationID(s.id))
		e, ok := conv.Context().Get(conversation.ScopeConversation, "session_marker")
		if !ok || e.Value != s.marker {
			t.Errorf("%s: final marker = %v (ok=%v), want %q", s.id, e.Value, ok, s.marker)
		}
		if li, ok := conv.Context().Get(conversation.ScopeConversation, "last_intent"); ok {
			if li.Value != string(s.want) {
				t.Errorf("%s: last_intent = %v, want %q — another session's intent "+
					"was recorded here", s.id, li.Value, s.want)
			}
		}
	}
}

// TestT10_SharedClassifierGivesIdenticalResultsUnderConcurrency.
//
// 3 — identical inputs must produce identical outputs whether classified alone
// or by 64 goroutines at once. The baseline is computed serially first, so the
// comparison is against a known-good answer rather than against whatever the
// concurrent run happened to agree on.
func TestT10_SharedClassifierGivesIdenticalResultsUnderConcurrency(t *testing.T) {
	t.Parallel()

	classifier, err := intent.New(intent.DefaultConfig())
	if err != nil {
		t.Fatalf("intent.New: %v", err)
	}

	inputs := []string{
		"please call me back on 9876543210",
		"transfer me to rajesh",
		"say that again",
		"i want to leave a message",
		"can you hold on",
		"this is rajesh sharma",
		"goodbye",
		"zzzz qqqq unknown words",
		"",
	}

	// Serial baseline.
	baseline := make([]string, len(inputs))
	for i, in := range inputs {
		baseline[i] = candidateSignature(t, classifier, in)
	}

	var wg sync.WaitGroup
	errs := make(chan string, 64*len(inputs))
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rep := 0; rep < 25; rep++ {
				for i, in := range inputs {
					if got := candidateSignature(t, classifier, in); got != baseline[i] {
						errs <- fmt.Sprintf("input %q: got %s, want %s", in, got, baseline[i])
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("shared classifier diverged under concurrency: %s", e)
	}
}

// candidateSignature renders a classification deterministically.
func candidateSignature(t *testing.T, c *intent.Classifier, text string) string {
	t.Helper()
	cands, slots, err := c.Classify(
		conversation.Utterance{Text: text, ASRConfidence: 0.95},
		conversation.ExpectNothing)
	if err != nil {
		return "err=" + err.Error()
	}
	parts := make([]string, 0, len(cands)+len(slots))
	for _, c := range cands {
		parts = append(parts, fmt.Sprintf("%s:%.6f", c.Name, c.Confidence))
	}
	names := make([]string, 0, len(slots))
	for _, s := range slots {
		names = append(names, fmt.Sprintf("%s/%v/%.3f", s.Name, s.Filled, s.Confidence))
	}
	sort.Strings(names)
	return strings.Join(parts, ",") + "|" + strings.Join(names, ",")
}

// ---------------------------------------------------------------------------
// 4. Concurrent context inserts / lookups / evictions
// ---------------------------------------------------------------------------

// TestT10_ConcurrentContextChurnStaysBoundedAndIsolated drives inserts past the
// frozen bound (forcing eviction) in every session simultaneously, with lookups
// and deletes interleaved.
func TestT10_ConcurrentContextChurnStaysBoundedAndIsolated(t *testing.T) {
	t.Parallel()

	specs := t10Specs()
	b := realBridge(t)
	openSessions(t, b, specs)

	var wg sync.WaitGroup
	errs := make(chan string, len(specs)*4)

	for _, s := range specs {
		wg.Add(1)
		go func(s sessionSpec) {
			defer wg.Done()
			conv, ok := b.Conversation(conversation.ConversationID(s.id))
			if !ok {
				errs <- s.id + ": missing"
				return
			}
			c := conv.Context()

			// Past the frozen bound, so eviction runs concurrently everywhere.
			for i := 0; i < frozenMaxEntriesPerScope+64; i++ {
				if err := c.Set(conversation.Entry{
					Key: fmt.Sprintf("k%04d", i), Value: s.marker,
					Scope: conversation.ScopeConversation, Source: "t10",
				}); err != nil {
					errs <- fmt.Sprintf("%s: set: %v", s.id, err)
					return
				}
				if i%7 == 0 {
					c.Delete(conversation.ScopeConversation, fmt.Sprintf("k%04d", i/2))
				}
				if i%5 == 0 {
					if e, ok := c.Lookup(fmt.Sprintf("k%04d", i)); ok && e.Value != s.marker {
						errs <- fmt.Sprintf("%s: lookup returned %v, want %q",
							s.id, e.Value, s.marker)
						return
					}
				}
				if n := c.Size(conversation.ScopeConversation); n > frozenMaxEntriesPerScope {
					errs <- fmt.Sprintf("%s: size %d past bound", s.id, n)
					return
				}
			}

			// Every surviving entry belongs to this session.
			for i := 0; i < frozenMaxEntriesPerScope+64; i++ {
				if e, ok := c.Get(conversation.ScopeConversation, fmt.Sprintf("k%04d", i)); ok {
					if e.Value != s.marker {
						errs <- fmt.Sprintf("%s: k%04d holds %v, want %q",
							s.id, i, e.Value, s.marker)
						return
					}
				}
			}
		}(s)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent context churn failed: %s", e)
	}
}

// ---------------------------------------------------------------------------
// 5. Cancellation during classification
// ---------------------------------------------------------------------------

// TestT10_CancellingOneSessionDoesNotCancelOthers.
//
// Half the sessions are cancelled through the existing mechanism (End) while
// the other half classify continuously. The cancelled ones must terminate by
// the declared semantics; the others must finish every turn correctly.
func TestT10_CancellingOneSessionDoesNotCancelOthers(t *testing.T) {
	t.Parallel()

	specs := t10Specs()
	b := realBridge(t)
	openSessions(t, b, specs)

	var wg sync.WaitGroup
	errs := make(chan string, len(specs)*4)
	var cancelled, survived sync.Map

	start := make(chan struct{})

	for i, s := range specs {
		wg.Add(1)
		go func(i int, s sessionSpec) {
			defer wg.Done()
			conv, ok := b.Conversation(conversation.ConversationID(s.id))
			if !ok {
				errs <- s.id + ": missing"
				return
			}
			<-start // release everyone together, maximising overlap

			if i%2 == 0 {
				// Cancel mid-flight.
				if _, err := conv.Handle(utteranceEvent(s.text)); err != nil {
					errs <- fmt.Sprintf("%s: pre-cancel turn: %v", s.id, err)
					return
				}
				if err := conv.End("cancelled_by_test"); err != nil {
					errs <- fmt.Sprintf("%s: End: %v", s.id, err)
					return
				}
				if !conv.State().IsTerminal() {
					errs <- fmt.Sprintf("%s: state %v after End is not terminal",
						s.id, conv.State())
					return
				}
				cancelled.Store(s.id, conv.State())
				return
			}

			// Survivors keep classifying throughout, re-looking-up their
			// session each round the way a service handling an inbound event
			// does. That keeps the Bridge's lookup path under concurrent load
			// rather than resolving it once before the storm starts.
			for round := 0; round < 15; round++ {
				conv, ok := b.Conversation(conversation.ConversationID(s.id))
				if !ok {
					errs <- fmt.Sprintf("%s round %d: session disappeared while a "+
						"neighbour was cancelled", s.id, round)
					return
				}
				if conv.State().IsTerminal() {
					errs <- fmt.Sprintf("%s round %d: session went terminal without "+
						"being cancelled", s.id, round)
					return
				}
				plan, err := turn(conv, s.text)
				if err != nil {
					errs <- fmt.Sprintf("%s round %d: a neighbour's cancellation "+
						"broke this session: %v", s.id, round, err)
					return
				}
				if plan.Intent != s.want {
					errs <- fmt.Sprintf("%s round %d: intent %q, want %q",
						s.id, round, plan.Intent, s.want)
					return
				}
			}
			survived.Store(s.id, conv.State())
		}(i, s)
	}

	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("cross-session cancellation failed: %s", e)
	}

	// Exactly the intended sessions were cancelled.
	nCancelled, nSurvived := 0, 0
	cancelled.Range(func(k, v any) bool {
		nCancelled++
		if !v.(conversation.State).IsTerminal() {
			t.Errorf("%v: cancelled session is not terminal", k)
		}
		return true
	})
	survived.Range(func(k, v any) bool {
		nSurvived++
		if v.(conversation.State).IsTerminal() {
			t.Errorf("%v: an uncancelled session was terminated by a neighbour", k)
		}
		return true
	})
	if nCancelled != t10Sessions/2 || nSurvived != t10Sessions/2 {
		t.Errorf("cancelled=%d survived=%d, want %d each",
			nCancelled, nSurvived, t10Sessions/2)
	}
}

// ---------------------------------------------------------------------------
// 6. Interruption during classification / context access
// ---------------------------------------------------------------------------

// TestT10_InterruptionAffectsOnlyTheInterruptedSession.
//
// Barge-in is injected into half the sessions while all sixteen classify. Only
// the interrupted sessions may show it, and no result from before the
// interruption may be committed as that session's outcome afterwards.
func TestT10_InterruptionAffectsOnlyTheInterruptedSession(t *testing.T) {
	t.Parallel()

	specs := t10Specs()
	b := realBridge(t)
	openSessions(t, b, specs)

	var wg sync.WaitGroup
	errs := make(chan string, len(specs)*4)
	start := make(chan struct{})

	for i, s := range specs {
		wg.Add(1)
		go func(i int, s sessionSpec) {
			defer wg.Done()
			conv, ok := b.Conversation(conversation.ConversationID(s.id))
			if !ok {
				errs <- s.id + ": missing"
				return
			}
			<-start

			if i%2 == 0 {
				// Establish an intent, then barge in before it is completed.
				if _, err := conv.Handle(utteranceEvent(s.text)); err != nil {
					errs <- fmt.Sprintf("%s: first turn: %v", s.id, err)
					return
				}
				if _, err := conv.Handle(conversation.Event{
					Kind: conversation.EventInterrupt}); err != nil {
					errs <- fmt.Sprintf("%s: interrupt: %v", s.id, err)
					return
				}

				// The frozen interruption vocabulary, not a second mechanism.
				sig := intent.ClassifyTurn(intent.TurnInput{
					Event:     conversation.EventInterrupt,
					Lifecycle: conversation.IntentActive,
					Active:    s.want,
					Config:    conversation.DefaultIntentConfig(),
				})
				if sig.Interruption != conversation.InterruptionUser {
					errs <- fmt.Sprintf("%s: interruption kind %v, want InterruptionUser",
						s.id, sig.Interruption)
					return
				}

				// No stale result: the interrupted session must still be in a
				// declared state and must not have silently completed the turn
				// it was interrupted out of.
				if !validStates()[conv.State()] {
					errs <- fmt.Sprintf("%s: invalid state %v after interruption",
						s.id, conv.State())
					return
				}
				return
			}

			// Uninterrupted sessions must be untouched.
			for round := 0; round < 15; round++ {
				plan, err := turn(conv, s.text)
				if err != nil {
					errs <- fmt.Sprintf("%s round %d: a neighbour's interruption "+
						"broke this session: %v", s.id, round, err)
					return
				}
				if plan.Intent != s.want {
					errs <- fmt.Sprintf("%s round %d: intent %q, want %q — an "+
						"interrupted session's result surfaced here",
						s.id, round, plan.Intent, s.want)
					return
				}
			}
		}(i, s)
	}

	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent interruption failed: %s", e)
	}

	// Uninterrupted sessions kept their own intent on record.
	for i, s := range specs {
		if i%2 == 0 {
			continue
		}
		conv, _ := b.Conversation(conversation.ConversationID(s.id))
		if li, ok := conv.Context().Get(conversation.ScopeConversation, "last_intent"); ok {
			if li.Value != string(s.want) {
				t.Errorf("%s: last_intent = %v, want %q", s.id, li.Value, s.want)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 7. Termination during concurrent work
// ---------------------------------------------------------------------------

// TestT10_TerminationDuringConcurrentWorkLeavesNoStaleState.
//
// Sessions are terminated while their neighbours work, then their ids are
// REUSED. The reused session must begin clean — that is the frozen contract
// (Begin stores into a sync.Map, replacing the entry) and the property most
// likely to break under concurrency.
func TestT10_TerminationDuringConcurrentWorkLeavesNoStaleState(t *testing.T) {
	t.Parallel()

	specs := t10Specs()
	b := realBridge(t)
	openSessions(t, b, specs)

	var wg sync.WaitGroup
	errs := make(chan string, len(specs)*4)
	start := make(chan struct{})

	for i, s := range specs {
		wg.Add(1)
		go func(i int, s sessionSpec) {
			defer wg.Done()
			conv, ok := b.Conversation(conversation.ConversationID(s.id))
			if !ok {
				errs <- s.id + ": missing"
				return
			}
			<-start

			if i%3 == 0 {
				if err := conv.Context().Set(conversation.Entry{
					Key: "secret_before_termination", Value: s.marker,
					Scope: conversation.ScopeConversation, Source: "t10",
				}); err != nil {
					errs <- fmt.Sprintf("%s: set: %v", s.id, err)
					return
				}
				if err := conv.End("terminated_by_test"); err != nil {
					errs <- fmt.Sprintf("%s: End: %v", s.id, err)
					return
				}
				// Post-termination work must be refused.
				if _, err := conv.Handle(utteranceEvent(s.text)); err == nil {
					errs <- s.id + ": accepted work after termination"
					return
				}
				return
			}

			for round := 0; round < 12; round++ {
				plan, err := turn(conv, s.text)
				if err != nil {
					errs <- fmt.Sprintf("%s round %d: neighbour termination broke "+
						"this session: %v", s.id, round, err)
					return
				}
				if plan.Intent != s.want {
					errs <- fmt.Sprintf("%s round %d: intent %q, want %q",
						s.id, round, plan.Intent, s.want)
					return
				}
			}
		}(i, s)
	}

	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("termination under concurrency failed: %s", e)
	}

	// Reuse every terminated id; each must start clean.
	for i, s := range specs {
		if i%3 != 0 {
			continue
		}
		p, err := b.Planner(conversation.ConversationID(s.id), "")
		if err != nil {
			t.Fatalf("%s: re-Begin: %v", s.id, err)
		}
		openFloor(t, p)

		fresh, _ := b.Conversation(conversation.ConversationID(s.id))
		if _, ok := fresh.Context().Get(conversation.ScopeConversation,
			"secret_before_termination"); ok {
			t.Errorf("%s: a reused session observed the terminated session's secret",
				s.id)
		}
		if fresh.State().IsTerminal() {
			t.Errorf("%s: a reused session started terminal", s.id)
		}
		plan, err := turn(fresh, s.text)
		if err != nil {
			t.Errorf("%s: reused session cannot work: %v", s.id, err)
			continue
		}
		if plan.Intent != s.want {
			t.Errorf("%s: reused session intent %q, want %q", s.id, plan.Intent, s.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 8. Mixed failure pressure
// ---------------------------------------------------------------------------

// TestT10_HealthySessionsSurviveMixedFailurePressure runs healthy sessions
// alongside sessions suffering classifier failure, cancellation, interruption,
// malformed input and termination — simultaneously, on one bridge.
//
// The healthy sessions here use the REAL classifier, so "completes correctly"
// means the exact expected intent, not merely the absence of an error.
func TestT10_HealthySessionsSurviveMixedFailurePressure(t *testing.T) {
	t.Parallel()

	specs := t10Specs()
	b := realBridge(t)
	openSessions(t, b, specs)

	// A second bridge whose classifier always fails, exercised in parallel so
	// classifier failure is genuinely concurrent with healthy classification.
	failing := conversation.NewScriptedClassifier().FailWith(errors.New("injected"))
	fb, err := voiceintel.New(voiceintel.WithClock(fixedClock()),
		voiceintel.WithClassifier(failing))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		p, err := fb.Planner(conversation.ConversationID(fmt.Sprintf("t10-fail-%d", i)), "")
		if err != nil {
			t.Fatal(err)
		}
		openFloor(t, p)
	}

	var wg sync.WaitGroup
	errs := make(chan string, 64)
	start := make(chan struct{})

	// Pressure goroutines.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conv, ok := fb.Conversation(conversation.ConversationID(fmt.Sprintf("t10-fail-%d", i)))
			if !ok {
				errs <- "failing session missing"
				return
			}
			<-start
			for r := 0; r < 10; r++ {
				_, _ = conv.Handle(utteranceEvent("please call me back"))
				_, _ = conv.Handle(conversation.Event{Kind: conversation.EventInterrupt})
				_, _ = conv.Handle(conversation.Event{
					Kind: conversation.EventUtterance,
					Utterance: conversation.Utterance{
						Text: "\x00\xff malformed", ASRConfidence: 0.95},
					Party: conversation.PartyCaller})
			}
			_ = conv.End("pressure_done")
		}(i)
	}

	// Healthy sessions, which must be unaffected.
	for _, s := range specs {
		wg.Add(1)
		go func(s sessionSpec) {
			defer wg.Done()
			conv, ok := b.Conversation(conversation.ConversationID(s.id))
			if !ok {
				errs <- s.id + ": missing"
				return
			}
			<-start
			for round := 0; round < 12; round++ {
				plan, err := turn(conv, s.text)
				if err != nil {
					errs <- fmt.Sprintf("%s round %d: healthy session failed under "+
						"pressure: %v", s.id, round, err)
					return
				}
				if plan.Intent != s.want {
					errs <- fmt.Sprintf("%s round %d: intent %q, want %q under pressure",
						s.id, round, plan.Intent, s.want)
					return
				}
			}
		}(s)
	}

	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("mixed failure pressure broke a healthy session: %s", e)
	}
}

// ---------------------------------------------------------------------------
// 9. Repetition
// ---------------------------------------------------------------------------

// TestT10_RepeatedConcurrentRoundsAgree reruns the whole matrix repeatedly and
// requires the per-session OUTCOME to be identical each time.
//
// It asserts on outcomes, never on goroutine scheduling or interleaving: which
// goroutine runs first is not a property of the system.
func TestT10_RepeatedConcurrentRoundsAgree(t *testing.T) {
	t.Parallel()

	run := func(iteration int) string {
		specs := t10Specs()
		b := realBridge(t)
		for i := range specs {
			specs[i].id = fmt.Sprintf("rep%02d-%s", iteration, specs[i].id)
		}
		openSessions(t, b, specs)

		var wg sync.WaitGroup
		results := make([]string, len(specs))
		for i, s := range specs {
			wg.Add(1)
			go func(i int, s sessionSpec) {
				defer wg.Done()
				conv, ok := b.Conversation(conversation.ConversationID(s.id))
				if !ok {
					results[i] = s.id + ":missing"
					return
				}
				plan, err := turn(conv, s.text)
				if err != nil {
					results[i] = fmt.Sprintf("%s:err=%v", s.id, err)
					return
				}
				results[i] = fmt.Sprintf("%s:%s:%s", s.want, plan.Intent, plan.Action)
			}(i, s)
		}
		wg.Wait()
		return strings.Join(results, "|")
	}

	want := run(0)
	for i := 1; i <= 20; i++ {
		if got := run(i); got != want {
			t.Fatalf("iteration %d disagreed\n got %s\nwant %s", i, got, want)
		}
	}
}

// TestT10_ClarificationBudgetEscalationIsPerSession.
//
// Found while building T10: an utterance whose intent has an unfilled required
// slot cannot be repeated indefinitely. The frozen clarification budget asks
// twice and then escalates -- clarification_exhausted -> StateEscalated. That is
// correct behaviour, and this test pins it AND proves the escalation is
// confined to the session that caused it.
func TestT10_ClarificationBudgetEscalationIsPerSession(t *testing.T) {
	t.Parallel()

	specs := t10Specs() // healthy, repeatable sessions
	b := realBridge(t)
	openSessions(t, b, specs)

	// Sessions that will exhaust their clarification budget.
	const exhausting = 4
	for i := 0; i < exhausting; i++ {
		p, err := b.Planner(conversation.ConversationID(fmt.Sprintf("t10-exhaust-%d", i)), "")
		if err != nil {
			t.Fatal(err)
		}
		openFloor(t, p)
	}

	var wg sync.WaitGroup
	errs := make(chan string, 64)
	start := make(chan struct{})

	for i := 0; i < exhausting; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("t10-exhaust-%d", i)
			conv, ok := b.Conversation(conversation.ConversationID(id))
			if !ok {
				errs <- id + ": missing"
				return
			}
			<-start

			var escalated bool
			for round := 0; round < 6 && !escalated; round++ {
				plan, err := conv.Handle(utteranceEvent("i want to leave a message"))
				if err != nil {
					escalated = true
					break
				}
				if plan.Reason == "clarification_exhausted" {
					escalated = true
				}
				if _, err := conv.Handle(conversation.Event{
					Kind: conversation.EventSpeechComplete}); err != nil {
					escalated = true
				}
			}
			if !escalated {
				errs <- id + ": the clarification budget never ran out"
				return
			}
			if got := conv.State(); got != conversation.StateEscalated {
				errs <- fmt.Sprintf("%s: state %v, want StateEscalated", id, got)
				return
			}
			if !conv.State().IsTerminal() {
				errs <- id + ": escalated state is not terminal"
			}
		}(i)
	}

	// Healthy neighbours run throughout and must be untouched.
	for _, s := range specs {
		wg.Add(1)
		go func(s sessionSpec) {
			defer wg.Done()
			conv, ok := b.Conversation(conversation.ConversationID(s.id))
			if !ok {
				errs <- s.id + ": missing"
				return
			}
			<-start
			for round := 0; round < 12; round++ {
				plan, err := turn(conv, s.text)
				if err != nil {
					errs <- fmt.Sprintf("%s round %d: a neighbour's escalation broke "+
						"this session: %v", s.id, round, err)
					return
				}
				if plan.Intent != s.want {
					errs <- fmt.Sprintf("%s round %d: intent %q, want %q",
						s.id, round, plan.Intent, s.want)
					return
				}
			}
			if conv.State().IsTerminal() {
				errs <- fmt.Sprintf("%s: a healthy session was terminated by a "+
					"neighbour's escalation", s.id)
			}
		}(s)
	}

	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("clarification-budget escalation leaked across sessions: %s", e)
	}
}

// ---------------------------------------------------------------------------
// Structural checks
// ---------------------------------------------------------------------------

// TestT10_BridgeHoldsNoCrossSessionState.
//
// The structural question behind every isolation test: CAN the Bridge hold
// per-session state? Its fields are inspected by AST; anything map-, slice- or
// sync-shaped would be a cross-session registry, and a second context or memory
// system would have to appear here first.
//
// This complements T7's package-level guard, which covers package vars rather
// than struct fields.
func TestT10_BridgeHoldsNoCrossSessionState(t *testing.T) {
	t.Parallel()

	src, err := parser.ParseFile(token.NewFileSet(), "bridge.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing bridge.go: %v", err)
	}

	var found bool
	ast.Inspect(src, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "Bridge" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			t.Fatal("Bridge is not a struct")
		}
		found = true

		for _, f := range st.Fields.List {
			name := "<embedded>"
			if len(f.Names) > 0 {
				name = f.Names[0].Name
			}
			switch ft := f.Type.(type) {
			case *ast.StarExpr:
				// A pointer to a frozen engine is the intended shape: session
				// state lives inside conversation, which owns it.
				sel, ok := ft.X.(*ast.SelectorExpr)
				if !ok {
					t.Errorf("Bridge.%s is a pointer to a non-frozen type", name)
					continue
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "conversation" {
					t.Errorf("Bridge.%s points at %v, not a conversation type",
						name, sel.X)
				}
			case *ast.MapType:
				t.Errorf("Bridge.%s is a map — a cross-session registry", name)
			case *ast.ArrayType:
				t.Errorf("Bridge.%s is a slice — accumulating cross-session state", name)
			case *ast.SelectorExpr:
				if id, ok := ft.X.(*ast.Ident); ok && id.Name == "sync" {
					t.Errorf("Bridge.%s is a sync primitive; the Bridge holds no "+
						"mutable state to protect", name)
				}
			default:
				t.Errorf("Bridge.%s has unexpected type %T", name, f.Type)
			}
		}
		return true
	})
	if !found {
		t.Fatal("Bridge type not found; the guard would pass vacuously")
	}
}

// TestT10_ClassifierIsImmutableAfterConstruction — the classifier is shared by
// every session, so any mutable field on it is shared mutable state.
func TestT10_ClassifierIsImmutableAfterConstruction(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	src, err := parser.ParseFile(fset, "../intent/classifier.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing classifier.go: %v", err)
	}

	// No method with a pointer receiver on Classifier may assign to a field of
	// the receiver: that is precisely per-call mutation of shared state.
	var checked int
	ast.Inspect(src, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
			return true
		}
		star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
		if !ok {
			return true
		}
		if id, ok := star.X.(*ast.Ident); !ok || id.Name != "Classifier" {
			return true
		}
		if len(fn.Recv.List[0].Names) == 0 {
			return true
		}
		recv := fn.Recv.List[0].Names[0].Name
		checked++

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range as.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == recv {
					t.Errorf("%s mutates %s.%s — shared classifier state written "+
						"per call", fn.Name.Name, recv, sel.Sel.Name)
				}
			}
			return true
		})
		return true
	})
	if checked == 0 {
		t.Fatal("inspected no *Classifier methods; the guard would pass vacuously")
	}
	t.Logf("%d *Classifier methods inspected for receiver mutation", checked)
}

// TestT10_NoForbiddenOrThirdPartyImportsAdded re-proves the boundary at the
// file level for every Phase 13 source file, including this task's tests.
func TestT10_NoForbiddenOrThirdPartyImportsAdded(t *testing.T) {
	t.Parallel()

	forbidden := []string{
		"packages/go/governance", "packages/go/toolruntime", "packages/go/memory",
		"packages/go/persistence", "packages/go/redis", "packages/go/repository",
		"packages/go/telemetry", "net/http", "database/sql", "os/exec",
	}

	fset := token.NewFileSet()
	var files, imports int
	for _, dir := range []string{".", "../intent"} {
		pkgs, err := parser.ParseDir(fset, dir, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", dir, err)
		}
		for _, pkg := range pkgs {
			for name, f := range pkg.Files {
				files++
				for _, imp := range f.Imports {
					imports++
					path := strings.Trim(imp.Path.Value, `"`)
					for _, bad := range forbidden {
						if strings.Contains(path, bad) {
							t.Errorf("%s imports forbidden %q", name, path)
						}
					}
					// Third party: a dotted first segment that is not ours.
					if first := strings.SplitN(path, "/", 2)[0]; strings.Contains(first, ".") {
						if !strings.HasPrefix(path, "github.com/callscreen/") {
							t.Errorf("%s imports third-party %q", name, path)
						}
					}
				}
			}
		}
	}
	if files == 0 || imports == 0 {
		t.Fatal("parsed nothing; the guard would pass vacuously")
	}
	t.Logf("%d files, %d imports inspected", files, imports)
}

var _ = rt.SystemClock{}
