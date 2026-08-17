package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/intent"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	"github.com/callscreen/callscreen-platform/packages/go/voiceintel"
)

// Phase 14 T9 — service-level determinism across the complete intelligence
// path.
//
// WHAT ALREADY EXISTED, and why this is not a re-run of it:
//
//	T4  (e2e_test.go:656)          6 fields, single turn, 21 constructed services
//	T7  (failure_test.go:589)      5 fields + terminal, failure inputs only
//	T8  (concurrency_test.go:812)  7 fields + terminal, concurrent only
//	T11 (voiceintel/determinism_test.go:481) module-level golden, below the service
//
// All four project a handful of Plan fields from a service built with
// build(t) — constructed, never Run. T9 widens the projection to the whole
// observable semantic surface (below), drives it through genuinely RUNNING
// services, and adds multi-turn, clock-controlled context, serial-vs-concurrent
// equivalence, and a guard that time cannot enter the signature at all.
//
// THREE HONEST LIMITS, established by inspection and reported rather than
// papered over:
//
//  1. NO GOVERNANCE DECISION IS REACHABLE. The Phase 14 seam reaches
//     conversation and stops; bridge.go:18 states it joins "no governance
//     decision, no tool execution". There is no governance field to put in the
//     signature, so none is invented. Same boundary T7 recorded for tools.
//  2. THERE IS NO "RESPONSE STRATEGY" TYPE. The frozen Plan has no Strategy
//     field. Action (respond / ask / clarify / confirm / escalate) IS the
//     response strategy, and Escalation names why when it was chosen. Both are
//     in the signature under their real names.
//  3. TURN CLASSIFICATION IS NOT ON THE BRIDGE PATH. intent.ClassifyTurn is a
//     pure function the Bridge never calls — the Phase 13 T9 finding, still
//     true. Its inputs therefore cannot be observed from a running service, so
//     scraping them back out of the engine would mean inventing values. T9
//     instead proves ClassifyTurn deterministic over a table of declared frozen
//     inputs, labelled as such.

// ---------------------------------------------------------------------------
// The semantic signature
// ---------------------------------------------------------------------------

// INCLUDED: intent, confidence class, action, reason, escalation, expectation,
// clarification (kind/slot/round/final/candidates), next state, state reached,
// terminal flag, outcome, turn count, floor holder, last expectation,
// interruption history, intent lifecycle, ordered alternatives, context
// summary, typed error identity.
//
// EXCLUDED, deliberately: every timestamp (Plan.Deadline, Intent.At,
// TransitionRecord.At, Turn times), Elapsed, ConversationID, log content and
// ordering, goroutine scheduling, and raw confidence floats. Raw confidence is
// banded because a band is the semantic claim; a float is a measurement.
//
// TestT9_SignatureIsIndependentOfWallClock proves the exclusion is real rather
// than merely intended.

// maxCtxEntries is the frozen per-scope bound, read from the frozen default
// rather than restated as a literal, so a change to the contract shows up here
// as a behavioural difference instead of a stale copy.
func maxCtxEntries() int { return conversation.DefaultContextConfig().MaxEntriesPerScope }

// discardLogger is for the clock-controlled tests, whose subject is the
// signature rather than the log.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// confBand maps a confidence onto the frozen threshold bands.
func confBand(c float64) string {
	cfg := conversation.DefaultIntentConfig()
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

// errIdentity classifies an error by TYPE, never by message text.
//
// Message strings are not a contract and would make the signature sensitive to
// rewording; the sentinels and InvariantError.Invariant are the contract.
func errIdentity(err error) string {
	if err == nil {
		return "nil"
	}
	var inv *conversation.InvariantError
	if errors.As(err, &inv) {
		return "invariant:" + inv.Invariant
	}
	for _, s := range []struct {
		name string
		err  error
	}{
		{"terminal", conversation.ErrTerminal},
		{"invalid_transition", conversation.ErrInvalidTransition},
		{"not_allowed", conversation.ErrNotAllowed},
		{"floor_held", conversation.ErrFloorHeld},
		{"budget_exhausted", conversation.ErrBudgetExhausted},
		{"clarification_exhausted", conversation.ErrClarificationExhausted},
		{"no_intent", conversation.ErrNoIntent},
		{"persona_switch_denied", conversation.ErrPersonaSwitchDenied},
		{"invariant", conversation.ErrInvariant},
	} {
		if errors.Is(err, s.err) {
			return s.name
		}
	}
	return "untyped"
}

// planSignature projects a Plan. No timestamp is read.
func planSignature(p conversation.Plan) string {
	cands := make([]string, 0, len(p.Clarification.Candidates))
	for _, c := range p.Clarification.Candidates {
		cands = append(cands, string(c))
	}
	return fmt.Sprintf(
		"intent=%s conf=%s action=%v reason=%s escalation=%s expect=%v "+
			"clar=%v/slot=%s/round=%d/final=%v/cands=[%s] next=%v",
		p.Intent, confBand(p.Confidence), p.Action, p.Reason, p.Escalation,
		p.Expectation, p.Clarification.Kind, p.Clarification.Slot,
		p.Clarification.Round, p.Clarification.Final, strings.Join(cands, ","),
		p.NextState)
}

// convSignature projects the conversation's observable state.
//
// Ordering note: Context().Export sorts by (Scope, Key) inside the frozen
// engine (context.go:406), so the context summary is ordered by the frozen
// contract and not by this test. Interruption and intent histories are frozen
// slices, already in occurrence order. Nothing here iterates a map.
func convSignature(c *conversation.Conversation) string {
	if c == nil {
		return "conv=<nil>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "state=%v terminal=%v outcome=%s turns=%d holder=%v lastExpect=%v",
		c.State(), c.State().IsTerminal(), c.Outcome(), c.Turns().Count(),
		c.Turns().Holder(), c.Turns().LastExpectation())

	// Intent lifecycle, and the ordered alternatives that make ambiguity
	// detectable. Order is the frozen engine's ranking, so it IS semantic.
	if in, st, ok := c.Intents().Active(); ok {
		alts := make([]string, 0, len(in.Alternatives))
		for _, a := range in.Alternatives {
			alts = append(alts, fmt.Sprintf("%s:%s", a.Name, confBand(a.Confidence)))
		}
		fmt.Fprintf(&b, " active=%s/%s/%v alts=[%s]",
			in.Name, confBand(in.Confidence), st, strings.Join(alts, ","))
	} else {
		b.WriteString(" active=none")
	}

	for _, i := range c.Interruptions().History() {
		fmt.Fprintf(&b, " int=%v/%v/%s", i.Kind, i.By, i.Reason)
	}

	// Context summary, ordered by the frozen Export.
	for _, e := range c.Context().Export(conversation.SensitiveValue) {
		fmt.Fprintf(&b, " ctx=%v/%s=%v", e.Scope, e.Key, e.Value)
	}

	// State trace WITHOUT TransitionRecord.At, which is unix nanos.
	for _, tr := range c.Trace() {
		fmt.Fprintf(&b, " tr=%v>%v/%v/%s", tr.From, tr.To, tr.Trigger, tr.Note)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

type t9Op int

const (
	opUtter t9Op = iota
	opComplete
	opInterrupt
	opSilence
	opEnd
)

type t9Step struct {
	op   t9Op
	text string
}

type t9Fixture struct {
	name  string
	steps []t9Step
}

// t9Fixtures covers every scenario T9 requires, using only declared vocabulary
// and inputs already measured by T4 and T7.
func t9Fixtures() []t9Fixture {
	return []t9Fixture{
		{"normal_request", []t9Step{{opUtter, "i am calling about the invoice"}}},
		{"callback", []t9Step{{opUtter, "please call me back on 9876543210"}}},
		{"transfer", []t9Step{{opUtter, "transfer me to rajesh"}}},
		{"repeat", []t9Step{{opUtter, "say that again"}}},
		{"hold", []t9Step{{opUtter, "can you hold on a moment"}}},
		{"caller_identity", []t9Step{{opUtter, "this is rajesh sharma calling"}}},
		{"leave_message", []t9Step{{opUtter, "i want to leave a message"}}},
		{"low_confidence", []t9Step{{opUtter, "repeat pardon"}}},
		{"ambiguous", []t9Step{{opUtter, "hold on call back"}}},
		{"unknown", []t9Step{{opUtter, "zzzz qqqq wubble frotz"}}},
		{"empty", []t9Step{{opUtter, ""}}},
		{"below_reject", []t9Step{{opUtter, "callback transfer"}}},
		{"silence_boundary", []t9Step{{opSilence, ""}}},
		{
			// The interruption boundary: the agent holds the floor, the caller
			// barges in, and the session continues.
			"interruption_boundary", []t9Step{
				{opUtter, "transfer me to rajesh"},
				{opInterrupt, ""},
				{opUtter, "say that again"},
			},
		},
		{
			// The cancellation boundary: a session driven to a known state and
			// left there. T9's cancellation test captures this signature either
			// side of a service cancellation.
			"cancellation_boundary", []t9Step{
				{opUtter, "please call me back on 9876543210"},
				{opComplete, ""},
			},
		},
		{
			// Terminal, then a refused event.
			"terminal_session", []t9Step{
				{opUtter, "transfer me to rajesh"},
				{opEnd, ""},
				{opUtter, "say that again"},
			},
		},
		{
			"multi_turn_callback_transfer_repeat", []t9Step{
				{opUtter, "please call me back on 9876543210"},
				{opComplete, ""},
				{opUtter, "transfer me to rajesh"},
				{opComplete, ""},
				{opUtter, "say that again"},
			},
		},
		{
			"multi_turn_clarify_then_answer", []t9Step{
				{opUtter, "i want to leave a message"},
				{opComplete, ""},
				{opUtter, "this is rajesh sharma calling"},
				{opComplete, ""},
				{opUtter, "can you hold on a moment"},
			},
		},
	}
}

// fixtureNamed looks a fixture up by name.
//
// By name and not by index: an index silently retargets when the table is
// reordered, and a determinism test quietly pointed at the wrong fixture would
// still pass.
func fixtureNamed(t *testing.T, name string) t9Fixture {
	t.Helper()
	for _, f := range t9Fixtures() {
		if f.name == name {
			return f
		}
	}
	t.Fatalf("no fixture named %q", name)
	return t9Fixture{}
}

// runFixture executes one fixture and returns its full semantic signature,
// one line per step. Errors are recorded by TYPE and the run continues, so an
// expected refusal is part of the signature rather than the end of it.
func runFixture(vi *voiceIntelligence, id conversation.ConversationID, f t9Fixture) (string, error) {
	p, conv, err := openPlanner(vi, id)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for i, s := range f.steps {
		var (
			plan conversation.Plan
			hErr error
		)
		switch s.op {
		case opUtter:
			plan, hErr = p.Handle(utter(s.text))
		case opComplete:
			plan, hErr = p.Handle(conversation.Event{Kind: conversation.EventSpeechComplete})
		case opInterrupt:
			plan, hErr = p.Handle(conversation.Event{
				Kind:         conversation.EventInterrupt,
				Interruption: conversation.InterruptionUser,
				Party:        conversation.PartyCaller,
				Reason:       "barge_in",
			})
		case opSilence:
			plan, hErr = p.Handle(conversation.Event{Kind: conversation.EventSilence})
		case opEnd:
			hErr = conv.End("t9_terminated")
		}
		fmt.Fprintf(&b, "%s#%d err=%s %s %s\n",
			f.name, i, errIdentity(hErr), planSignature(plan), convSignature(conv))
	}
	return b.String(), nil
}

// matrixSignature runs every fixture on ONE service and concatenates the result.
func matrixSignature(t *testing.T, vi *voiceIntelligence, pass int) string {
	t.Helper()
	var b strings.Builder
	mustFinish(t, 60*time.Second, fmt.Sprintf("matrix pass %d", pass), func() error {
		for i, f := range t9Fixtures() {
			sig, err := runFixture(vi, conversation.ConversationID(
				fmt.Sprintf("t9-%d-%02d", pass, i)), f)
			if err != nil {
				return fmt.Errorf("%s: %w", f.name, err)
			}
			b.WriteString(sig)
		}
		return nil
	})
	return b.String()
}

// ---------------------------------------------------------------------------
// Step 2 — fresh-service repeatability
// ---------------------------------------------------------------------------

// TestT9_FreshRunningServiceSignatureIsStable builds 20 independent RUNNING
// services — each with its own ports, logger, bridge, engine and classifier —
// and requires a byte-identical signature from every one.
//
// This is what T4/T7/T8 did not do: they reused build(t), which constructs a
// service without running it. A defect that only appears once the service is
// actually serving would not have shown up there.
func TestT9_FreshRunningServiceSignatureIsStable(t *testing.T) {
	const passes = 20

	first := ""
	for pass := 0; pass < passes; pass++ {
		_, vi, cancel, done := runningService(t)
		got := matrixSignature(t, vi, pass)
		cancel()
		<-done

		if pass == 0 {
			first = got
			if strings.TrimSpace(first) == "" {
				t.Fatal("empty signature")
			}
			if n := strings.Count(first, "\n"); n < len(t9Fixtures()) {
				t.Fatalf("signature has %d lines for %d fixtures — fixtures are "+
					"not all executing", n, len(t9Fixtures()))
			}
			continue
		}
		if got != first {
			t.Fatalf("fresh service %d drifted:\n%s", pass, firstDiff(first, got))
		}
	}
	t.Logf("%d fresh running services, %d fixtures each, identical signatures",
		passes, len(t9Fixtures()))
}

// TestT9_MatrixMatchesTheGoldenSignature pins the semantic content itself.
//
// WHY THIS EXISTS, and it is the most important test in the file: every other
// test here compares a signature against ANOTHER RUN OF ITSELF. That detects
// nondeterminism and nothing else. A change that is wrong but CONSISTENTLY
// wrong — a mis-mapped response strategy, a suppressed turn transition —
// reproduces perfectly and sails through every equality comparison.
//
// This was not a hypothesis. Two determinism mutations (slots always reported
// filled; speech-complete suppressed) passed the entire equality suite before
// this test existed. The golden file is what closes that hole: it is checked in,
// so a semantic change has to be justified as a diff rather than absorbed.
//
// To regenerate deliberately: go test -run TestT9_MatrixMatchesTheGolden -update
func TestT9_MatrixMatchesTheGoldenSignature(t *testing.T) {
	const golden = "testdata/t9_service_signature.golden"

	_, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()
	got := matrixSignature(t, vi, 0)

	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", golden, len(got))
		return
	}

	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("reading %s: %v (regenerate with -update)", golden, err)
	}
	if got != string(want) {
		t.Fatalf("the service's semantic signature changed:\n%s\n\n"+
			"If this change is intended, regenerate with -update and justify the "+
			"diff. Do not regenerate to make a red test green.",
			firstDiff(string(want), got))
	}
}

var updateGolden = flag.Bool("update", false, "rewrite the golden signature")

// TestT9_SignatureIsNonVacuous is the guard on every other test in this file.
//
// A signature that never varies is trivially deterministic and proves nothing.
// Every test above compares signatures for EQUALITY, so if a limb of the
// projection were always empty — no clarification candidates, no interruption
// recorded, no escalation ever reached — those comparisons would still pass
// while silently covering nothing. This asserts each limb is actually exercised
// by the matrix.
func TestT9_SignatureIsNonVacuous(t *testing.T) {
	_, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	sig := matrixSignature(t, vi, 0)

	// Each entry must be produced by at least one fixture, or the limb of the
	// signature that carries it is dead weight.
	for _, want := range []struct{ limb, token string }{
		{"accepted intent", "reason=intent_accepted"},
		{"ambiguity clarification", "clar=ambiguous"},
		{"low-confidence clarification", "clar=low_confidence"},
		{"missing-slot clarification", "clar=missing_slot"},
		{"ORDERED clarification candidates", "cands=[hold,request_callback]"},
		{"ordered intent alternatives", "alts=[request_transfer:"},
		{"escalation", "escalation=rejected"},
		{"fallback", "reason=fallback"},
		{"silence", "reason=silence"},
		{"interruption history", "int=user/caller/barge_in"},
		{"context summary", "ctx=conversation/last_intent="},
		{"terminal outcome", "outcome=completed"},
		{"escalated outcome", "outcome=escalated"},
		{"typed terminal error", "err=terminal"},
		{"floor held by caller", "holder=caller"},
		{"floor held by agent", "holder=agent"},
		{"state trace", "tr=listening>thinking/utterance/"},
		{"expectation", "expect=slot_value"},
	} {
		if !strings.Contains(sig, want.token) {
			t.Errorf("the fixture matrix never produces %s (%q) — that limb of the "+
				"signature is inert and the equality comparisons cover nothing",
				want.limb, want.token)
		}
	}

	// Confidence banding must actually discriminate, not collapse to one band.
	bands := map[string]bool{}
	for _, b := range []string{"zero", "below_reject", "clarify_band", "accept", "certain"} {
		if strings.Contains(sig, "conf="+b+" ") {
			bands[b] = true
		}
	}
	if len(bands) < 4 {
		t.Errorf("only %d confidence bands appear (%v); the band projection is "+
			"not discriminating", len(bands), bands)
	}

	// Fixtures must differ from one another, or the matrix is one test repeated.
	lines := map[string]bool{}
	for _, l := range strings.Split(strings.TrimRight(sig, "\n"), "\n") {
		if i := strings.Index(l, " "); i > 0 {
			lines[l[i:]] = true // drop the fixture-name prefix
		}
	}
	if len(lines) < 12 {
		t.Errorf("only %d distinct signature bodies across %d fixtures — the "+
			"matrix is not covering distinct behaviour", len(lines), len(t9Fixtures()))
	}
}

// firstDiff reports the first differing line, which is far more useful than
// dumping two multi-kilobyte signatures.
func firstDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return fmt.Sprintf("line %d\nwant: %s\n got: %s", i, wl, gl)
		}
	}
	return "(no line differs — trailing content only)"
}

// ---------------------------------------------------------------------------
// Step 3 — multi-turn determinism
// ---------------------------------------------------------------------------

// TestT9_MultiTurnSignatureIsStablePerTurn checks the multi-turn fixtures turn
// by turn rather than only at the end, so a divergence that self-corrects by
// the last turn cannot hide.
func TestT9_MultiTurnSignatureIsStablePerTurn(t *testing.T) {
	multi := []t9Fixture{}
	for _, f := range t9Fixtures() {
		if len(f.steps) > 1 {
			multi = append(multi, f)
		}
	}
	if len(multi) < 4 {
		t.Fatalf("only %d multi-turn fixtures; the table is not exercising "+
			"multi-turn behaviour", len(multi))
	}

	const passes = 20
	perTurn := map[string][]string{} // fixture -> per-step signature lines

	for pass := 0; pass < passes; pass++ {
		_, vi, cancel, done := runningService(t)
		for i, f := range multi {
			var sig string
			mustFinish(t, 30*time.Second, "multi-turn "+f.name, func() error {
				s, err := runFixture(vi, conversation.ConversationID(
					fmt.Sprintf("t9-mt-%d-%02d", pass, i)), f)
				sig = s
				return err
			})
			lines := strings.Split(strings.TrimRight(sig, "\n"), "\n")
			if len(lines) != len(f.steps) {
				t.Fatalf("%s: %d signature lines for %d steps", f.name, len(lines), len(f.steps))
			}
			if pass == 0 {
				perTurn[f.name] = lines
				continue
			}
			for turn := range lines {
				if lines[turn] != perTurn[f.name][turn] {
					t.Fatalf("%s pass %d diverged at TURN %d\nwant: %s\n got: %s",
						f.name, pass, turn, perTurn[f.name][turn], lines[turn])
				}
			}
		}
		cancel()
		<-done
	}
	t.Logf("%d multi-turn fixtures x %d fresh services, identical at every turn",
		len(multi), passes)
}

// ---------------------------------------------------------------------------
// Step 4 — context / eviction determinism under a controlled clock
// ---------------------------------------------------------------------------

// TestT9_ContextDeterminismUnderControlledClock exercises insertion, lookup,
// update, eviction and expiry through the EXISTING clock seam
// (voiceintel.WithClock -> conversation.WithClock -> ContextEngine.clock).
//
// SEAM NOTE: newVoiceIntelligence(log) takes no clock, so the clock cannot be
// injected through the service constructor. Adding one would be a production
// change made solely to let a test control time — a declared stop condition —
// so this test uses the Bridge seam directly and the limitation is reported
// rather than engineered around.
//
// THE TIED-TIMESTAMP DISTINCTION (T7's finding, honoured here):
// evictOldestLocked picks the oldest entry by a linear scan. When two entries
// carry the SAME SetAt, which one is oldest is not decided by the contract, so
// victim identity is unspecified. This test therefore:
//
//	distinct timestamps -> asserts the exact victim, repeatedly
//	tied timestamps     -> asserts ONLY the guaranteed invariant (the bound
//	                       holds and the newest write survives)
//
// No deterministic victim identity is claimed for ties.
func TestT9_ContextDeterminismUnderControlledClock(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// --- distinct timestamps: full determinism, including victim identity ---
	distinct := func() string {
		clock := rt.NewFakeClock(start)
		bridge, err := voiceintel.New(voiceintel.WithClock(clock))
		if err != nil {
			t.Fatalf("bridge: %v", err)
		}
		conv, err := bridge.Engine().Begin("t9-ctx", "")
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		ctx := conv.Context()

		var b strings.Builder
		// insertion, each at a distinct instant
		for i := 0; i < 5; i++ {
			clock.Advance(time.Second)
			if err := ctx.Set(conversation.Entry{
				Key: fmt.Sprintf("k%d", i), Value: fmt.Sprintf("v%d", i),
				Scope: conversation.ScopeConversation, Source: "t9",
			}); err != nil {
				t.Fatalf("set k%d: %v", i, err)
			}
		}
		// lookup
		for i := 0; i < 5; i++ {
			e, ok := ctx.Get(conversation.ScopeConversation, fmt.Sprintf("k%d", i))
			fmt.Fprintf(&b, "get k%d=%v/%v ", i, e.Value, ok)
		}
		// update: k0 becomes the NEWEST entry
		clock.Advance(time.Second)
		if err := ctx.Set(conversation.Entry{
			Key: "k0", Value: "updated", Scope: conversation.ScopeConversation, Source: "t9",
		}); err != nil {
			t.Fatalf("update: %v", err)
		}
		e, _ := ctx.Get(conversation.ScopeConversation, "k0")
		fmt.Fprintf(&b, "| updated k0=%v size=%d ", e.Value, ctx.Size(conversation.ScopeConversation))

		// expiry: a temporary entry disappears once its TTL has passed
		clock.Advance(time.Second)
		if err := ctx.Set(conversation.Entry{
			Key: "temp", Value: "short", Scope: conversation.ScopeTemporary,
			Source: "t9", ExpiresAt: clock.Now().Add(10 * time.Second),
		}); err != nil {
			t.Fatalf("temp: %v", err)
		}
		_, before := ctx.Get(conversation.ScopeTemporary, "temp")
		clock.Advance(11 * time.Second)
		_, after := ctx.Get(conversation.ScopeTemporary, "temp")
		fmt.Fprintf(&b, "| temp before=%v after=%v ", before, after)

		// eviction: overflow the bound, every write at a distinct instant, so
		// the victim IS determined — k1 is the oldest surviving key because k0
		// was refreshed by the update above.
		for i := 0; i < maxCtxEntries(); i++ {
			clock.Advance(time.Millisecond)
			if err := ctx.Set(conversation.Entry{
				Key: fmt.Sprintf("f%03d", i), Value: i,
				Scope: conversation.ScopeConversation, Source: "t9",
			}); err != nil {
				t.Fatalf("fill %d: %v", i, err)
			}
		}
		_, k1Alive := ctx.Get(conversation.ScopeConversation, "k1")
		_, lastAlive := ctx.Get(conversation.ScopeConversation,
			fmt.Sprintf("f%03d", maxCtxEntries()-1))
		fmt.Fprintf(&b, "| size=%d k1Alive=%v newestAlive=%v",
			ctx.Size(conversation.ScopeConversation), k1Alive, lastAlive)
		return b.String()
	}

	want := distinct()
	for i := 1; i <= 20; i++ {
		if got := distinct(); got != want {
			t.Fatalf("clock-controlled context pass %d drifted\nwant: %s\n got: %s", i, want, got)
		}
	}
	// The bound and the newest-survives rule must actually hold, or the loop
	// above would merely be confirming a stable wrong answer.
	if !strings.Contains(want, fmt.Sprintf("size=%d", maxCtxEntries())) {
		t.Errorf("scope not at its bound after overflow: %s", want)
	}
	if !strings.Contains(want, "newestAlive=true") {
		t.Errorf("the newest write did not survive eviction: %s", want)
	}
	if !strings.Contains(want, "temp before=true after=false") {
		t.Errorf("expiry did not take effect on the controlled clock: %s", want)
	}
	t.Logf("distinct-timestamp context signature, 21 passes identical: %s", want)

	// --- tied timestamps: ONLY the invariant, never victim identity ---
	//
	// The clock is not advanced, so every entry shares one SetAt and
	// evictOldestLocked's victim is unspecified by the contract. Asserting a
	// particular survivor here would be inventing a guarantee.
	tied := func() (size, survivors int, newestAlive bool) {
		clock := rt.NewFakeClock(start)
		bridge, err := voiceintel.New(voiceintel.WithClock(clock))
		if err != nil {
			t.Fatalf("bridge: %v", err)
		}
		conv, err := bridge.Engine().Begin("t9-ctx-tied", "")
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		ctx := conv.Context()
		total := maxCtxEntries() + 32
		for i := 0; i < total; i++ { // no Advance: all timestamps tie
			if err := ctx.Set(conversation.Entry{
				Key: fmt.Sprintf("t%03d", i), Value: i,
				Scope: conversation.ScopeConversation, Source: "t9",
			}); err != nil {
				t.Fatalf("tied set %d: %v", i, err)
			}
		}
		for i := 0; i < total; i++ {
			if _, ok := ctx.Get(conversation.ScopeConversation, fmt.Sprintf("t%03d", i)); ok {
				survivors++
			}
		}
		_, newestAlive = ctx.Get(conversation.ScopeConversation, fmt.Sprintf("t%03d", total-1))
		return ctx.Size(conversation.ScopeConversation), survivors, newestAlive
	}

	for i := 0; i < 20; i++ {
		size, survivors, newestAlive := tied()
		// GUARANTEED: the bound holds, and the count of survivors matches it.
		if size != maxCtxEntries() {
			t.Fatalf("tied pass %d: size %d, want the bound %d",
				i, size, maxCtxEntries())
		}
		if survivors != size {
			t.Fatalf("tied pass %d: %d survivors but size %d", i, survivors, size)
		}
		// GUARANTEED: the write that just happened is present.
		if !newestAlive {
			t.Fatalf("tied pass %d: the newest write was evicted", i)
		}
		// NOT ASSERTED: which of the tied entries was evicted. The contract
		// does not decide it, so neither does this test.
	}
	t.Log("tied timestamps: bound and newest-survives asserted; victim identity " +
		"deliberately NOT asserted (unspecified by the frozen contract)")
}

// ---------------------------------------------------------------------------
// Step 5 — failure determinism, by type
// ---------------------------------------------------------------------------

// TestT9_FailureSignatureIsTypedAndStable reuses T7's reachable failure inputs
// and requires an identical TYPED signature across fresh running services.
// Error message strings are never compared.
func TestT9_FailureSignatureIsTypedAndStable(t *testing.T) {
	run := func(pass int) string {
		_, vi, cancel, done := runningService(t)
		defer func() { cancel(); <-done }()

		var b strings.Builder
		mustFinish(t, 30*time.Second, "failure pass", func() error {
			for i, tc := range failureCases() {
				p, conv, err := openPlanner(vi, conversation.ConversationID(
					fmt.Sprintf("t9-f-%d-%02d", pass, i)))
				if err != nil {
					return err
				}
				plan, hErr := p.Handle(utter(tc.utterance))
				fmt.Fprintf(&b, "%s err=%s %s %s\n",
					tc.name, errIdentity(hErr), planSignature(plan), convSignature(conv))
			}
			// The terminal refusal, reached deliberately and typed.
			p, conv, err := openPlanner(vi, conversation.ConversationID(
				fmt.Sprintf("t9-f-%d-term", pass)))
			if err != nil {
				return err
			}
			if _, err := p.Handle(utter("transfer me to rajesh")); err != nil {
				return err
			}
			if err := conv.End("t9_failure_terminal"); err != nil {
				return err
			}
			_, hErr := p.Handle(utter("say that again"))
			if !errors.Is(hErr, conversation.ErrTerminal) {
				return fmt.Errorf("terminal refusal was %v, want ErrTerminal", hErr)
			}
			fmt.Fprintf(&b, "terminal err=%s %s\n", errIdentity(hErr), convSignature(conv))
			return nil
		})
		return b.String()
	}

	want := run(0)
	if strings.TrimSpace(want) == "" {
		t.Fatal("empty failure signature")
	}
	for i := 1; i <= 20; i++ {
		if got := run(i); got != want {
			t.Fatalf("failure pass %d drifted:\n%s", i, firstDiff(want, got))
		}
	}
}

// ---------------------------------------------------------------------------
// Step 6 — interruption / cancellation boundary determinism
// ---------------------------------------------------------------------------

// TestT9_InterruptionBoundaryIsDeterministic drives the same initial state and
// the same EventInterrupt repeatedly and requires the same action, reason and
// next state every time.
//
// Boundary only. Handle is synchronous, so nothing here claims a turn already
// executing can be interrupted.
func TestT9_InterruptionBoundaryIsDeterministic(t *testing.T) {
	run := func(pass int) string {
		_, vi, cancel, done := runningService(t)
		defer func() { cancel(); <-done }()
		var sig string
		mustFinish(t, 30*time.Second, "interruption pass", func() error {
			s, err := runFixture(vi,
				conversation.ConversationID(fmt.Sprintf("t9-int-%d", pass)),
				fixtureNamed(t, "interruption_boundary"))
			sig = s
			return err
		})
		return sig
	}

	want := run(0)
	if !strings.Contains(want, "interrupted_user") {
		t.Fatalf("the interruption fixture never reached the barge-in path: %s", want)
	}
	for i := 1; i <= 20; i++ {
		if got := run(i); got != want {
			t.Fatalf("interruption pass %d drifted:\n%s", i, firstDiff(want, got))
		}
	}
}

// TestT9_CancellationDoesNotAlterSemanticState captures a session's signature,
// cancels the service it belongs to, and re-captures.
//
// The claim is exactly the supported one: cancellation is observed at a turn
// boundary and does not rewrite semantic state already established. It is NOT a
// claim that cancellation interrupts a turn in progress.
func TestT9_CancellationDoesNotAlterSemanticState(t *testing.T) {
	for pass := 0; pass < 20; pass++ {
		_, vi, cancel, done := runningService(t)

		id := conversation.ConversationID(fmt.Sprintf("t9-cancel-%d", pass))
		var before string
		mustFinish(t, 30*time.Second, "pre-cancel", func() error {
			s, err := runFixture(vi, id, fixtureNamed(t, "cancellation_boundary"))
			before = s
			return err
		})

		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("pass %d: Run returned %v", pass, err)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("pass %d: service did not shut down", pass)
		}

		conv, ok := vi.Bridge().Conversation(id)
		if !ok {
			t.Fatalf("pass %d: session vanished on cancellation", pass)
		}
		after := convSignature(conv)
		if !strings.Contains(before, after) {
			t.Fatalf("pass %d: cancellation altered semantic state\nafter: %s\nbefore tail: %s",
				pass, after, before)
		}
	}
}

// ---------------------------------------------------------------------------
// Step 7 — serial vs concurrent
// ---------------------------------------------------------------------------

// TestT9_SerialAndConcurrentSignaturesMatch runs the fixture matrix serially,
// then runs the identical fixtures concurrently on one service, and requires
// the same semantic result per fixture.
//
// Results are keyed by fixture name and sorted, so completion order — which is
// scheduling, not semantics — cannot enter the comparison. This is the
// comparison T8 did not make: T8 compared concurrent runs with each other.
func TestT9_SerialAndConcurrentSignaturesMatch(t *testing.T) {
	fixtures := t9Fixtures()

	_, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	serial := make(map[string]string, len(fixtures))
	mustFinish(t, 60*time.Second, "serial baseline", func() error {
		for i, f := range fixtures {
			sig, err := runFixture(vi, conversation.ConversationID(
				fmt.Sprintf("t9-ser-%02d", i)), f)
			if err != nil {
				return fmt.Errorf("%s: %w", f.name, err)
			}
			serial[f.name] = sig
		}
		return nil
	})

	_, vi2, cancel2, done2 := runningService(t)
	defer func() { cancel2(); <-done2 }()

	concurrent := make([]string, len(fixtures))
	failures := make([]string, len(fixtures))
	gate := newBarrier(len(fixtures))
	var wg sync.WaitGroup
	for i, f := range fixtures {
		wg.Add(1)
		go func(i int, f t9Fixture) {
			defer wg.Done()
			if !gate.wait() {
				return
			}
			sig, err := runFixture(vi2, conversation.ConversationID(
				fmt.Sprintf("t9-con-%02d", i)), f)
			if err != nil {
				failures[i] = fmt.Sprintf("%s: %v", f.name, err)
				return
			}
			concurrent[i] = sig
		}(i, f)
	}
	awaitAll(t, &wg, gate, 90*time.Second, "concurrent fixtures")

	for _, f := range failures {
		if f != "" {
			t.Fatalf("concurrent fixture failed: %s", f)
		}
	}

	// Compare per fixture, by name. Sorted purely so the report is stable.
	names := make([]string, 0, len(fixtures))
	for _, f := range fixtures {
		names = append(names, f.name)
	}
	sort.Strings(names)
	byName := map[string]string{}
	for i, f := range fixtures {
		byName[f.name] = concurrent[i]
	}
	for _, n := range names {
		if byName[n] != serial[n] {
			t.Errorf("%s: concurrency changed the semantic result\n%s",
				n, firstDiff(serial[n], byName[n]))
		}
	}
}

// ---------------------------------------------------------------------------
// Step 9 — the exclusion guard
// ---------------------------------------------------------------------------

// TestT9_SignatureIsIndependentOfWallClock proves the signature genuinely
// excludes time rather than merely intending to.
//
// Two bridges are built on fake clocks starting eleven years apart and driven
// through the identical fixtures. Every timestamp differs by construction; the
// semantic signature must not.
func TestT9_SignatureIsIndependentOfWallClock(t *testing.T) {
	run := func(start time.Time) string {
		clock := rt.NewFakeClock(start)
		bridge, err := voiceintel.New(voiceintel.WithClock(clock))
		if err != nil {
			t.Fatalf("bridge: %v", err)
		}
		vi := &voiceIntelligence{bridge: bridge, log: discardLogger()}
		var b strings.Builder
		for i, f := range t9Fixtures() {
			clock.Advance(time.Duration(i+1) * time.Second)
			sig, err := runFixture(vi, conversation.ConversationID(
				fmt.Sprintf("t9-clock-%02d", i)), f)
			if err != nil {
				t.Fatalf("%s: %v", f.name, err)
			}
			b.WriteString(sig)
		}
		return b.String()
	}

	early := run(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	late := run(time.Date(2037, 6, 14, 23, 59, 59, 0, time.UTC))
	if early != late {
		t.Fatalf("the signature depends on wall-clock time:\n%s", firstDiff(early, late))
	}
	if strings.TrimSpace(early) == "" {
		t.Fatal("empty signature")
	}
}

// ---------------------------------------------------------------------------
// Turn classification determinism (pure function, off the Bridge path)
// ---------------------------------------------------------------------------

// TestT9_TurnClassificationIsDeterministic covers the turn-classification limb
// of the signature.
//
// intent.ClassifyTurn is NOT called by the Bridge — the Phase 13 T9 finding,
// re-verified below — so its inputs cannot be read off a running service.
// Scraping approximate values out of the engine would be inventing them. The
// inputs here are therefore declared frozen constants, and the test proves what
// can honestly be proved: identical TurnInput yields an identical TurnSignal,
// with no dependence on call order.
func TestT9_TurnClassificationIsDeterministic(t *testing.T) {
	t.Parallel()

	cfg := conversation.DefaultIntentConfig()
	inputs := []struct {
		name string
		in   intent.TurnInput
	}{
		{"silence", intent.TurnInput{Event: conversation.EventSilence, Config: cfg}},
		{"barge_in", intent.TurnInput{
			Event: conversation.EventInterrupt, Interruption: conversation.InterruptionUser,
			Floor: conversation.FloorGranted, Config: cfg,
		}},
		{"backchannel", intent.TurnInput{
			Event: conversation.EventOverlap, Floor: conversation.FloorBackchannel, Config: cfg,
		}},
		{"new_request", intent.TurnInput{
			Event: conversation.EventUtterance, Verdict: conversation.IntentAccept,
			Utterance: conversation.Utterance{Text: "transfer me to rajesh", ASRConfidence: 0.95},
			Resolved:  conversation.Intent{Name: intent.IntentRequestTransfer, Confidence: 0.9},
			Config:    cfg,
		}},
		{"supersedes", intent.TurnInput{
			Event: conversation.EventUtterance, Verdict: conversation.IntentAccept,
			Utterance: conversation.Utterance{Text: "actually call me back", ASRConfidence: 0.95},
			Resolved:  conversation.Intent{Name: intent.IntentRequestCallback, Confidence: 0.9},
			Active:    intent.IntentRequestTransfer, Lifecycle: conversation.IntentActive,
			Config: cfg,
		}},
		{"cancellation", intent.TurnInput{
			Event: conversation.EventUtterance, Verdict: conversation.IntentAccept,
			Utterance: conversation.Utterance{Text: "never mind", ASRConfidence: 0.95},
			Active:    intent.IntentRequestTransfer, Lifecycle: conversation.IntentActive,
			Config: cfg,
		}},
		{"low_confidence", intent.TurnInput{
			Event: conversation.EventUtterance, Verdict: conversation.IntentClarify,
			Utterance: conversation.Utterance{Text: "repeat pardon", ASRConfidence: 0.5},
			Resolved:  conversation.Intent{Name: intent.IntentRepeat, Confidence: 0.5},
			Config:    cfg,
		}},
		{"rejected", intent.TurnInput{
			Event: conversation.EventUtterance, Verdict: conversation.IntentReject,
			Utterance: conversation.Utterance{Text: "callback transfer", ASRConfidence: 0.9},
			Config:    cfg,
		}},
	}

	sig := func() string {
		var b strings.Builder
		for _, tc := range inputs {
			s := intent.ClassifyTurn(tc.in)
			fmt.Fprintf(&b, "%s=%v/%v/%v/%v/%v/%s/%v\n", tc.name, s.Event, s.Floor,
				s.Interruption, s.Clarify, s.Lifecycle, s.Intent, s.Expectation)
		}
		return b.String()
	}

	want := sig()
	for i := 1; i <= 50; i++ {
		if got := sig(); got != want {
			t.Fatalf("pass %d drifted\nwant:\n%s\n got:\n%s", i, want, got)
		}
	}
	// Reversed order must not change any individual result: a pure function
	// cannot carry state between calls.
	forward := map[string]intent.TurnSignal{}
	for _, tc := range inputs {
		forward[tc.name] = intent.ClassifyTurn(tc.in)
	}
	for i := len(inputs) - 1; i >= 0; i-- {
		if got := intent.ClassifyTurn(inputs[i].in); got != forward[inputs[i].name] {
			t.Errorf("%s depends on call order: %+v vs %+v",
				inputs[i].name, got, forward[inputs[i].name])
		}
	}
}
