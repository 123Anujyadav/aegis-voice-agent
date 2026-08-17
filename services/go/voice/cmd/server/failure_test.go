package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/intent"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	"github.com/callscreen/callscreen-platform/packages/go/voice"
)

// Phase 14 T7 — failure propagation through the service intelligence path.
//
// REACHABILITY WAS MEASURED, not assumed. Each case below was probed against
// the real service seam before a test was written for it, and two of the nine
// required cases are NOT reachable from this phase's architecture. Those are
// reported as boundaries rather than faked:
//
//	governance denial  -- the Phase 14 intelligence path makes no governance
//	                      call; importing governance into intent/voiceintel to
//	                      manufacture one would widen the very boundary Phase 13
//	                      exists to keep narrow.
//	tool failure       -- the path executes no tools. conversation models the
//	                      wait but does not perform it, and voiceintel has no
//	                      toolruntime dependency.
//
// Both remain covered where they actually live: packages/go/voice/failure_test.go
// (Phase 11E, frozen). What T7 proves for them is that their typed vocabulary
// stays DISTINGUISHABLE at the supported boundary.
//
// No new error type, reason code or enum is introduced anywhere in this file.

// ---------------------------------------------------------------------------
// Reachable: unknown, low confidence, malformed, oversized
// ---------------------------------------------------------------------------

// failureCase is one input and the exact existing contract it must produce.
type failureCase struct {
	name       string
	utterance  string
	wantIntent conversation.IntentName
	wantAction conversation.Action
	wantReason string
	wantClar   conversation.ClarificationKind
	// terminal marks a legitimate TERMINAL SESSION OUTCOME -- not a service
	// failure, and not an error return.
	terminal bool
}

func failureCases() []failureCase {
	return []failureCase{
		{
			name: "unknown_vocabulary", utterance: "zzzz qqqq wubble frotz",
			wantIntent: conversation.IntentFallback,
			wantAction: conversation.ActionRespond, wantReason: "fallback",
			wantClar: conversation.ClarifyNone,
		},
		{
			name: "empty", utterance: "",
			wantIntent: conversation.IntentFallback,
			wantAction: conversation.ActionRespond, wantReason: "fallback",
			wantClar: conversation.ClarifyNone,
		},
		{
			name: "whitespace_only", utterance: "   \t\n  ",
			wantIntent: conversation.IntentFallback,
			wantAction: conversation.ActionRespond, wantReason: "fallback",
			wantClar: conversation.ClarifyNone,
		},
		{
			name: "control_bytes", utterance: "\x00\x01\x02",
			wantIntent: conversation.IntentFallback,
			wantAction: conversation.ActionRespond, wantReason: "fallback",
			wantClar: conversation.ClarifyNone,
		},
		{
			name: "low_confidence", utterance: "repeat pardon",
			wantIntent: intent.IntentRepeat,
			wantAction: conversation.ActionConfirm, wantReason: "clarify_low_confidence",
			wantClar: conversation.ClarifyLowConfidence,
		},
		{
			name: "ambiguous", utterance: "hold on call back",
			wantIntent: intent.IntentHold,
			wantAction: conversation.ActionClarify, wantReason: "clarify_ambiguous",
			wantClar: conversation.ClarifyAmbiguous,
		},
		{
			// MEASURED: 900 repeated cues score below the frozen reject
			// threshold, which the planner escalates. Terminal by design.
			name: "oversized_bounded_input", utterance: strings.Repeat("callback ", 900),
			wantIntent: intent.IntentRequestCallback,
			wantAction: conversation.ActionEscalate, wantReason: "intent_rejected",
			wantClar: conversation.ClarifyNone, terminal: true,
		},
		{
			name: "below_reject", utterance: "callback transfer",
			wantIntent: intent.IntentRequestCallback,
			wantAction: conversation.ActionEscalate, wantReason: "intent_rejected",
			wantClar: conversation.ClarifyNone, terminal: true,
		},
	}
}

// TestT7_ReachableFailuresProduceTypedBoundedOutcomes drives every reachable
// input failure through a running service and asserts the exact existing
// contract. It also proves the outcomes stay DISTINGUISHABLE rather than
// collapsing into one generic result.
func TestT7_ReachableFailuresProduceTypedBoundedOutcomes(t *testing.T) {
	sink, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	signatures := map[string]string{}

	for i, tc := range failureCases() {
		p := openSession(t, vi, fmt.Sprintf("t7-fail-%02d", i))

		plan, err := p.Handle(utter(tc.utterance))
		// None of these is an ERROR return: they are typed PLANS. A failure
		// that arrived as an error here would mean the planner had stopped
		// classifying rather than deciding.
		if err != nil {
			t.Errorf("%s: returned error %v; expected a typed plan", tc.name, err)
			continue
		}
		if plan.Intent != tc.wantIntent {
			t.Errorf("%s: intent = %q, want %q", tc.name, plan.Intent, tc.wantIntent)
		}
		if plan.Action != tc.wantAction {
			t.Errorf("%s: action = %v, want %v", tc.name, plan.Action, tc.wantAction)
		}
		if plan.Reason != tc.wantReason {
			t.Errorf("%s: reason = %q, want %q", tc.name, plan.Reason, tc.wantReason)
		}
		if plan.Clarification.Kind != tc.wantClar {
			t.Errorf("%s: clarification = %v, want %v",
				tc.name, plan.Clarification.Kind, tc.wantClar)
		}

		// Reason codes are bounded operational identifiers, never caller text.
		if len(plan.Reason) > 64 {
			t.Errorf("%s: reason is %d chars; operational codes must stay bounded",
				tc.name, len(plan.Reason))
		}
		if tc.utterance != "" && strings.Contains(plan.Reason, tc.utterance) {
			t.Errorf("%s: the reason code carries the caller's text", tc.name)
		}

		sig := fmt.Sprintf("%v|%s|%v", plan.Action, plan.Reason, plan.Clarification.Kind)
		signatures[sig] = tc.name
	}

	// Four distinct outcome shapes across eight inputs: fallback, confirm,
	// clarify, escalate. Collapsing any pair would show up here.
	if len(signatures) < 4 {
		t.Errorf("only %d distinct failure outcomes across %d inputs; cases have "+
			"collapsed: %v", len(signatures), len(failureCases()), signatures)
	}

	if sink.has("runner failed, shutting down service") {
		t.Errorf("an input failure became a service failure; log:\n%s", sink.dump())
	}
}

// ---------------------------------------------------------------------------
// Reachable: planner construction failure
// ---------------------------------------------------------------------------

// TestT7_PlannerFailureIsTypedAndScoped uses a real deterministic seam found by
// probing: Bridge.Planner with an unregistered persona fails inside
// conversation.Engine.Begin and the error is wrapped by voiceintel.
//
// This is a genuine production failure path, not a manufactured one.
func TestT7_PlannerFailureIsTypedAndScoped(t *testing.T) {
	sink, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	p, err := vi.Bridge().Planner("t7-bad-persona",
		conversation.PersonaID("no-such-persona"))

	if err == nil {
		t.Fatal("an unregistered persona produced no error")
	}
	if p != nil {
		t.Error("a failed Planner call returned a non-nil planner")
	}

	// Typed, not a bare string: the frozen configuration error survives the
	// wrap, asserted structurally rather than by message text.
	var cfgErr *conversation.ConfigError
	if !errors.As(err, &cfgErr) {
		t.Errorf("error %v is not a *conversation.ConfigError; the typed "+
			"contract was replaced", err)
	} else if len(cfgErr.Problems) == 0 {
		t.Error("ConfigError carries no problems")
	}
	// And voiceintel identifies which conversation failed.
	if !strings.Contains(err.Error(), "t7-bad-persona") {
		t.Errorf("error does not name the conversation: %v", err)
	}

	// Session-scoped: a healthy session is unaffected and the service is alive.
	healthy := openSession(t, vi, "t7-planner-neighbour")
	plan, hErr := healthy.Handle(utter("transfer me to rajesh"))
	if hErr != nil {
		t.Fatalf("neighbour broke after a planner failure: %v", hErr)
	}
	if plan.Intent != intent.IntentRequestTransfer {
		t.Errorf("neighbour intent = %q", plan.Intent)
	}
	if sink.has("runner failed, shutting down service") {
		t.Errorf("a planner failure became a service failure; log:\n%s", sink.dump())
	}
}

// ---------------------------------------------------------------------------
// Reachable: conversation-level failures
// ---------------------------------------------------------------------------

// TestT7_ConversationFailuresAreTypedAndNotSwallowed exercises two real frozen
// failure paths reached through the service, and asserts each keeps its own
// type.
//
// The invariant violation is the important one: it must NOT be swallowed or
// downgraded. A genuine invariant breach is exactly the error that should
// surface loudly.
func TestT7_ConversationFailuresAreTypedAndNotSwallowed(t *testing.T) {
	sink, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	// 1. Recovery attempted from a live state -- an undeclared transition.
	openSession(t, vi, "t7-recover")
	conv, ok := vi.Bridge().Conversation("t7-recover")
	if !ok {
		t.Fatal("conversation missing")
	}
	err := conv.Recover()
	if err == nil {
		t.Fatal("Recover from a live state was accepted")
	}
	if !errors.Is(err, rt.ErrInvalidTransition) {
		t.Errorf("Recover error %v is not rt.ErrInvalidTransition", err)
	}

	// 2. An unhandled event kind -- a real invariant violation.
	p2 := openSession(t, vi, "t7-invariant")
	plan, err := p2.Handle(conversation.Event{Kind: conversation.EventKind(99)})
	if err == nil {
		t.Fatal("an unhandled event kind produced no error; a genuine invariant " +
			"violation was swallowed")
	}
	var invErr *conversation.InvariantError
	if !errors.As(err, &invErr) {
		t.Errorf("error %v is not a *conversation.InvariantError", err)
	} else if invErr.Invariant != "INV-CV-1" {
		t.Errorf("invariant id = %q, want INV-CV-1", invErr.Invariant)
	}
	// The failed call must not also claim to have decided something.
	if plan.Reason != "" {
		t.Errorf("a failed Handle returned a reason %q", plan.Reason)
	}

	// Both are session-scoped: the service survives and neighbours work.
	if sink.has("runner failed, shutting down service") {
		t.Errorf("a conversation failure became a service failure; log:\n%s",
			sink.dump())
	}
	fresh := openSession(t, vi, "t7-after-conv-failure")
	if plan, err := fresh.Handle(utter("say that again")); err != nil ||
		plan.Intent != intent.IntentRepeat {
		t.Errorf("new session after conversation failures: intent=%q err=%v",
			plan.Intent, err)
	}
}

// ---------------------------------------------------------------------------
// Reachable: session termination
// ---------------------------------------------------------------------------

// TestT7_TerminalSessionRefusesFurtherWork distinguishes a TERMINAL SESSION
// OUTCOME from a service failure.
func TestT7_TerminalSessionRefusesFurtherWork(t *testing.T) {
	sink, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	p := openSession(t, vi, "t7-terminal")
	conv, _ := vi.Bridge().Conversation("t7-terminal")

	if _, err := p.Handle(utter("transfer me to rajesh")); err != nil {
		t.Fatal(err)
	}
	if err := conv.End("t7_done"); err != nil {
		t.Fatalf("End: %v", err)
	}
	if !conv.State().IsTerminal() {
		t.Fatalf("state %v is not terminal after End", conv.State())
	}

	// Every subsequent event is refused with the existing typed error.
	for _, e := range []conversation.Event{
		utter("are you still there"),
		{Kind: conversation.EventSpeechComplete},
		{Kind: conversation.EventUtterance, Utterance: conversation.Utterance{
			Text: "hello", ASRConfidence: 0.99}, Party: conversation.PartyCaller},
	} {
		_, err := p.Handle(e)
		if err == nil {
			t.Errorf("%v accepted after termination", e.Kind)
			continue
		}
		if !errors.Is(err, conversation.ErrTerminal) {
			t.Errorf("%v: error %v is not conversation.ErrTerminal", e.Kind, err)
		}
	}

	// Terminal session != service failure.
	if sink.has("runner failed, shutting down service") {
		t.Errorf("a terminal session became a service failure; log:\n%s", sink.dump())
	}
	other := openSession(t, vi, "t7-terminal-neighbour")
	if plan, err := other.Handle(utter("can you hold on a moment")); err != nil ||
		plan.Intent != intent.IntentHold {
		t.Errorf("neighbour after termination: intent=%q err=%v", plan.Intent, err)
	}
}

// ---------------------------------------------------------------------------
// Reachable only at the supported boundary: cancellation
// ---------------------------------------------------------------------------

// TestT7_CancellationIsObservedAtTheSupportedBoundary.
//
// T6 established that conversation.Handle takes NO context, so cancellation
// cannot interrupt a turn mid-execution. This test asserts only what the
// architecture supports: a session driver holding the service context observes
// cancellation BETWEEN turns, and the turn already in progress completes
// normally rather than being corrupted.
func TestT7_CancellationIsObservedAtTheSupportedBoundary(t *testing.T) {
	sink, vi, cancel, done := runningService(t)

	ctx, ctxCancel := context.WithCancel(context.Background())
	defer ctxCancel()

	p := openSession(t, vi, "t7-cancel")
	var observed atomic.Bool
	var lastErr atomic.Value
	finished := make(chan struct{})

	go func() {
		defer close(finished)
		for {
			select {
			case <-ctx.Done():
				observed.Store(true)
				return
			default:
				plan, err := p.Handle(utter("say that again"))
				if err != nil {
					lastErr.Store(err.Error())
					<-ctx.Done()
					observed.Store(true)
					return
				}
				// The turn completed normally: cancellation never corrupts a
				// turn, because it cannot enter one.
				if plan.Intent != intent.IntentRepeat && plan.Action != conversation.ActionEscalate {
					lastErr.Store(fmt.Sprintf("unexpected intent %q", plan.Intent))
				}
				if _, err := p.Handle(conversation.Event{
					Kind: conversation.EventSpeechComplete}); err != nil {
					lastErr.Store(err.Error())
					<-ctx.Done()
					observed.Store(true)
					return
				}
			}
		}
	}()

	ctxCancel()
	cancel()

	select {
	case <-finished:
	case <-time.After(20 * time.Second):
		t.Fatalf("driver did not observe cancellation; log:\n%s", sink.dump())
	}
	if !observed.Load() {
		t.Error("the session driver never observed cancellation")
	}
	if v := lastErr.Load(); v != nil {
		// A terminal error here is legitimate (turn budget); an unexpected
		// intent is not.
		if s, _ := v.(string); strings.HasPrefix(s, "unexpected intent") {
			t.Errorf("turn produced %s during cancellation", s)
		}
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("service did not shut down")
	}
}

// ---------------------------------------------------------------------------
// NOT reachable: governance denial and tool failure
// ---------------------------------------------------------------------------

// TestT7_GovernanceAndToolVocabularyRemainDistinguishable.
//
// Neither governance denial nor tool failure is reachable from the Phase 14
// service intelligence path: the path makes no governance decision and executes
// no tool. Rather than fake either, this asserts the property Phase 14 is
// actually responsible for -- that the frozen typed vocabulary stays
// distinguishable, so a future path that DOES reach them cannot conflate them.
//
// The runtime matrix for both lives in packages/go/voice/failure_test.go.
func TestT7_GovernanceAndToolVocabularyRemainDistinguishable(t *testing.T) {
	t.Parallel()

	// Denial is not failure, and denial counts as the system working.
	if voice.OutcomeDenied == voice.OutcomeFailed {
		t.Fatal("denial and failure are the same outcome")
	}
	if !voice.OutcomeDenied.Successful() {
		t.Error("OutcomeDenied no longer reports Successful; a refusal is the " +
			"system working, not a fault")
	}
	if voice.OutcomeFailed.Successful() {
		t.Error("OutcomeFailed reports Successful")
	}

	// Governance denial is distinguishable from provider failure.
	if errors.Is(voice.ErrGovernanceDenied, voice.ErrProviderUnavailable) ||
		errors.Is(voice.ErrProviderUnavailable, voice.ErrGovernanceDenied) {
		t.Error("governance denial and provider unavailability are conflated")
	}
	// And a policy refusal is distinguishable from an invariant violation.
	if errors.Is(conversation.ErrNotAllowed, conversation.ErrInvariant) {
		t.Error("policy refusal is indistinguishable from an invariant violation")
	}

	// The service's dependency boundary is what makes these unreachable, and
	// that boundary is verified in the T7 report's dependency section rather
	// than asserted here.
}

// ---------------------------------------------------------------------------
// Concurrent mixed failure
// ---------------------------------------------------------------------------

// TestT7_ConcurrentMixedFailuresStayIsolated runs failing and healthy sessions
// released together at a barrier, mixing SEVERAL failure kinds at once.
func TestT7_ConcurrentMixedFailuresStayIsolated(t *testing.T) {
	sink, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	const groups = 4 // healthy, below-reject, terminal, invariant
	const perGroup = 4
	total := groups * perGroup

	type worker struct {
		kind    string
		planner interface {
			Handle(conversation.Event) (conversation.Plan, error)
		}
		id string
	}
	workers := make([]worker, 0, total)
	kinds := []string{"healthy", "below_reject", "terminal", "invariant"}
	for g, kind := range kinds {
		for i := 0; i < perGroup; i++ {
			id := fmt.Sprintf("t7-mixed-%d-%d", g, i)
			workers = append(workers, worker{
				kind: kind, planner: openSession(t, vi, id), id: id,
			})
		}
	}

	gate := newBarrier(total)
	errs := make(chan string, total*2)
	var wg sync.WaitGroup

	for _, w := range workers {
		wg.Add(1)
		go func(w worker) {
			defer wg.Done()
			if !gate.wait() {
				return
			}

			switch w.kind {
			case "healthy":
				plan, err := w.planner.Handle(utter("transfer me to rajesh"))
				if err != nil {
					gate.abort()
					errs <- fmt.Sprintf("%s: healthy session broke: %v", w.id, err)
					return
				}
				if plan.Intent != intent.IntentRequestTransfer {
					gate.abort()
					errs <- fmt.Sprintf("%s: healthy intent %q", w.id, plan.Intent)
				}

			case "below_reject":
				plan, err := w.planner.Handle(utter("callback transfer"))
				if err != nil {
					gate.abort()
					errs <- fmt.Sprintf("%s: below-reject errored: %v", w.id, err)
					return
				}
				if plan.Action != conversation.ActionEscalate ||
					plan.Reason != "intent_rejected" {
					gate.abort()
					errs <- fmt.Sprintf("%s: action=%v reason=%q", w.id, plan.Action, plan.Reason)
				}

			case "terminal":
				conv, ok := vi.Bridge().Conversation(conversation.ConversationID(w.id))
				if !ok {
					gate.abort()
					errs <- w.id + ": missing"
					return
				}
				if err := conv.End("t7_mixed"); err != nil {
					gate.abort()
					errs <- fmt.Sprintf("%s: End: %v", w.id, err)
					return
				}
				if _, err := w.planner.Handle(utter("hello")); !errors.Is(err, conversation.ErrTerminal) {
					gate.abort()
					errs <- fmt.Sprintf("%s: post-terminal error %v is not ErrTerminal", w.id, err)
				}

			case "invariant":
				_, err := w.planner.Handle(conversation.Event{Kind: conversation.EventKind(99)})
				var invErr *conversation.InvariantError
				if !errors.As(err, &invErr) {
					gate.abort()
					errs <- fmt.Sprintf("%s: invariant error %v not typed", w.id, err)
				}
			}
		}(w)
	}

	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent mixed failure isolation broke: %s", e)
	}

	// Service alive, and a brand-new session still classifies.
	select {
	case err := <-done:
		t.Fatalf("service exited during mixed failures (err=%v); log:\n%s",
			err, sink.dump())
	default:
	}
	if sink.has("runner failed, shutting down service") {
		t.Errorf("mixed session failures became a service failure; log:\n%s",
			sink.dump())
	}
	fresh := openSession(t, vi, "t7-mixed-after")
	if plan, err := fresh.Handle(utter("please call me back on 9876543210")); err != nil ||
		plan.Intent != intent.IntentRequestCallback {
		t.Errorf("new session after mixed failures: intent=%q err=%v", plan.Intent, err)
	}
}

// ---------------------------------------------------------------------------
// Determinism
// ---------------------------------------------------------------------------

// TestT7_FailureSignatureIsStable replays every reachable failure on a fresh
// service and requires an identical semantic signature.
//
// Excludes timestamps, log ordering, goroutine order and session ids.
func TestT7_FailureSignatureIsStable(t *testing.T) {
	t.Parallel()

	run := func(pass int) string {
		_, vi := build(t)
		var b strings.Builder
		for i, tc := range failureCases() {
			p := openSession(t, vi, fmt.Sprintf("t7-det-%d-%02d", pass, i))
			plan, err := p.Handle(utter(tc.utterance))
			if err != nil {
				fmt.Fprintf(&b, "%s=err\n", tc.name)
				continue
			}
			conv, _ := vi.Bridge().Conversation(
				conversation.ConversationID(fmt.Sprintf("t7-det-%d-%02d", pass, i)))
			terminal := conv != nil && conv.State().IsTerminal()
			fmt.Fprintf(&b, "%s=%s|%v|%s|%v|%v\n",
				tc.name, plan.Intent, plan.Action, plan.Reason,
				plan.Clarification.Kind, terminal)
		}
		return b.String()
	}

	want := run(0)
	if want == "" {
		t.Fatal("empty signature")
	}
	for i := 1; i <= 15; i++ {
		if got := run(i); got != want {
			t.Fatalf("pass %d drifted\n got %s\nwant %s", i, got, want)
		}
	}
}
