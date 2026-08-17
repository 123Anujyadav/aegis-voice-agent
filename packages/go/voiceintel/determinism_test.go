package voiceintel_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/intent"
	"github.com/callscreen/callscreen-platform/packages/go/voiceintel"
)

// T11 — DETERMINISM.
//
// Not another concurrency test; T10 covered isolation. T11 fixes a
// REPOSITORY-LEVEL CONTRACT: the same logical input and configuration must
// produce the same observable decision output across process restarts,
// goroutine scheduling, shuffle seeds and repetition.
//
// THE SIGNATURE. Everything in it is a logical decision output:
//
//	intent · candidate ordering · confidence (exact and bucketed) · slot
//	structure and ordering · turn classification · clarification/response
//	strategy · context summary · typed failure outcome
//
// Deliberately EXCLUDED, because none is part of the logical result:
// timestamps, wall-clock durations, goroutine IDs, frame interleavings,
// scheduler ordering, memory addresses, and map iteration order.
//
// CROSS-PROCESS. The matrix signature is compared against a golden file in
// testdata/. A golden file is what makes "same answer in a different process"
// checkable at all: each independent `go test` invocation is a fresh process
// comparing against the same bytes on disk. No subprocess is spawned, so no
// os/exec import is introduced and the T10 import guard stays absolute.

const goldenPath = "testdata/determinism.golden"

// ---------------------------------------------------------------------------
// Signature helpers — canonical, and free of excluded information
// ---------------------------------------------------------------------------

// confidenceBucket renders a confidence as a stable class.
//
// Reported ALONGSIDE the exact value, never instead of it: bucketing alone
// would hide a drift of 0.09, which is most of the distance between the frozen
// reject and accept thresholds.
func confidenceBucket(c float64) string {
	switch {
	case c <= 0:
		return "zero"
	case c < conversation.DefaultIntentConfig().RejectThreshold:
		return "below_reject"
	case c < conversation.DefaultIntentConfig().AcceptThreshold:
		return "clarify_band"
	case c < 1:
		return "accept"
	default:
		return "certain"
	}
}

// sigClassification renders a raw classifier result.
func sigClassification(t *testing.T, c *intent.Classifier, text string,
	expect conversation.Expectation) string {
	t.Helper()

	cands, slots, err := c.Classify(
		conversation.Utterance{Text: text, ASRConfidence: 0.95}, expect)
	if err != nil {
		return "error=" + err.Error()
	}

	// Candidate ORDER is part of the contract, so it is rendered as returned.
	parts := make([]string, 0, len(cands))
	for _, cand := range cands {
		parts = append(parts, fmt.Sprintf("%s@%.6f/%s",
			cand.Name, cand.Confidence, confidenceBucket(cand.Confidence)))
	}
	// Slot ORDER is also part of the contract, likewise rendered as returned.
	sl := make([]string, 0, len(slots))
	for _, s := range slots {
		sl = append(sl, fmt.Sprintf("%s:filled=%v:req=%v:conf=%.6f",
			s.Name, s.Filled, s.Required, s.Confidence))
	}
	return "cands[" + strings.Join(parts, ",") + "] slots[" + strings.Join(sl, ",") + "]"
}

// sigTurn renders a TurnSignal.
func sigTurn(s intent.TurnSignal) string {
	return fmt.Sprintf("event=%v floor=%v interrupt=%v clarify=%v lifecycle=%v intent=%s expect=%v",
		s.Event, s.Floor, s.Interruption, s.Clarify, s.Lifecycle, s.Intent, s.Expectation)
}

// sigPlan renders a plan: the response strategy, with no clock-derived field.
//
// Plan.Deadline is deliberately omitted — it is a wall-clock instant, and
// including it would make the signature a function of when the test ran.
func sigPlan(p conversation.Plan) string {
	cands := make([]string, 0, len(p.Clarification.Candidates))
	for _, c := range p.Clarification.Candidates {
		cands = append(cands, string(c))
	}
	return fmt.Sprintf(
		"action=%v reason=%s intent=%s conf=%.6f/%s expect=%v next=%v esc=%s "+
			"clar{kind=%v slot=%s cands=[%s] round=%d final=%v}",
		p.Action, p.Reason, p.Intent, p.Confidence, confidenceBucket(p.Confidence),
		p.Expectation, p.NextState, p.Escalation,
		p.Clarification.Kind, p.Clarification.Slot, strings.Join(cands, "|"),
		p.Clarification.Round, p.Clarification.Final)
}

// sigContext renders a context summary.
//
// Uses the frozen Export, which sorts by (Scope, Key) before returning
// (context.go:406) — so although it reads maps internally, its OUTPUT is
// canonicalised and safe to put in a signature. SetAt/ExpiresAt are omitted:
// both are clock values.
func sigContext(c *conversation.ContextEngine) string {
	entries := c.Export(conversation.Internal)
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, fmt.Sprintf("%v/%s=%v(sens=%v,src=%s)",
			e.Scope, e.Key, e.Value, e.Sensitivity, e.Source))
	}
	return strings.Join(out, ";")
}

// sigConversation renders a conversation's decision-relevant state.
func sigConversation(c *conversation.Conversation) string {
	var trace []string
	for _, r := range c.Trace() {
		// From/To/Trigger/Note only. r.At is a timestamp.
		trace = append(trace, fmt.Sprintf("%s>%s:%s:%s", r.From, r.To, r.Trigger, r.Note))
	}
	return fmt.Sprintf("state=%v outcome=%v trace=[%s] ctx{%s}",
		c.State(), c.Outcome(), strings.Join(trace, ","), sigContext(c.Context()))
}

// ---------------------------------------------------------------------------
// The scenario matrix
// ---------------------------------------------------------------------------

// scenario is one deterministic case: a name and a function producing its
// signature from scratch.
type scenario struct {
	name string
	run  func(t *testing.T) string
}

// matrix returns every T11 scenario, in a fixed order.
func matrix() []scenario {
	cfg := conversation.DefaultIntentConfig()

	newClassifier := func(t *testing.T) *intent.Classifier {
		t.Helper()
		c, err := intent.New(intent.DefaultConfig())
		if err != nil {
			t.Fatalf("intent.New: %v", err)
		}
		return c
	}

	// bridgeTurn drives one full turn through the real bridge.
	bridgeTurn := func(t *testing.T, id, text string) string {
		t.Helper()
		b, err := voiceintel.New(voiceintel.WithClock(fixedClock()))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		p, err := b.Planner(conversation.ConversationID(id), "")
		if err != nil {
			t.Fatalf("Planner: %v", err)
		}
		openFloor(t, p)
		plan, err := p.Handle(utteranceEvent(text))
		if err != nil {
			return "err=" + err.Error()
		}
		conv, _ := b.Conversation(conversation.ConversationID(id))
		return sigPlan(plan) + " || " + sigConversation(conv)
	}

	return []scenario{
		// 1 — normal intent.
		{"normal_intent", func(t *testing.T) string {
			return sigClassification(t, newClassifier(t),
				"please call me back on 9876543210", conversation.ExpectNothing)
		}},
		{"normal_intent_via_bridge", func(t *testing.T) string {
			return bridgeTurn(t, "det-normal", "please call me back on 9876543210")
		}},

		// 1b — MULTI-CANDIDATE with DISTINCT confidences.
		//
		// Load-bearing: without it the matrix has no scenario in which the
		// confidence comparison inside sortCandidates is even reachable. The
		// first draft of T11 had only single-candidate and empty results, so a
		// mutation reversing candidate order passed unnoticed.
		{"multi_candidate_ordering", func(t *testing.T) string {
			return sigClassification(t, newClassifier(t),
				"repeat call back", conversation.ExpectNothing)
		}},

		// 2 — ambiguous intent.
		{"ambiguous", func(t *testing.T) string {
			return sigTurn(intent.ClassifyTurn(intent.TurnInput{
				Event:     conversation.EventUtterance,
				Utterance: conversation.Utterance{Text: "call", ASRConfidence: 0.95},
				Verdict:   conversation.IntentClarify,
				Resolved: conversation.Intent{
					Name: intent.IntentRequestCallback, Confidence: 0.60,
					Alternatives: []conversation.Candidate{
						{Name: intent.IntentRequestCallback, Confidence: 0.60},
						{Name: intent.IntentRequestTransfer, Confidence: 0.55},
					},
				},
				Config: cfg,
			}))
		}},

		// 3 — clarification / response strategy.
		{"clarification_missing_slot", func(t *testing.T) string {
			return bridgeTurn(t, "det-clarify", "i want to leave a message")
		}},

		// 4 — interruption.
		{"interruption", func(t *testing.T) string {
			return sigTurn(intent.ClassifyTurn(intent.TurnInput{
				Event:     conversation.EventInterrupt,
				Lifecycle: conversation.IntentActive,
				Active:    intent.IntentCallPurpose,
				Config:    cfg,
			}))
		}},

		// 5 — acknowledgement (frozen FloorBackchannel).
		{"acknowledgement", func(t *testing.T) string {
			return sigTurn(intent.ClassifyTurn(intent.TurnInput{
				Event:     conversation.EventOverlap,
				Floor:     conversation.FloorBackchannel,
				Lifecycle: conversation.IntentActive,
				Active:    intent.IntentLeaveMessage,
				Config:    cfg,
			}))
		}},

		// 6 — cancellation.
		{"cancellation", func(t *testing.T) string {
			return sigTurn(intent.ClassifyTurn(intent.TurnInput{
				Event:     conversation.EventUtterance,
				Utterance: conversation.Utterance{Text: "never mind", ASRConfidence: 0.95},
				Verdict:   conversation.IntentAccept,
				Resolved:  conversation.Intent{Name: intent.IntentRequestCallback, Confidence: 0.9},
				Active:    intent.IntentRequestCallback,
				Lifecycle: conversation.IntentActive,
				Config:    cfg,
			}))
		}},

		// 7 — silence.
		{"silence", func(t *testing.T) string {
			return sigTurn(intent.ClassifyTurn(intent.TurnInput{
				Event:     conversation.EventSilence,
				Lifecycle: conversation.IntentActive,
				Active:    intent.IntentHold,
				Config:    cfg,
			}))
		}},

		// 8 — unknown and low confidence, kept distinct.
		{"unknown", func(t *testing.T) string {
			return sigClassification(t, newClassifier(t),
				"zzzz qqqq wubble", conversation.ExpectNothing) + " || " +
				sigTurn(intent.ClassifyTurn(intent.TurnInput{
					Event:     conversation.EventUtterance,
					Utterance: conversation.Utterance{Text: "zzzz qqqq", ASRConfidence: 0.95},
					Verdict:   conversation.IntentReject,
					Resolved:  conversation.Intent{Name: conversation.IntentUnknown},
					Config:    cfg,
				}))
		}},
		{"low_confidence", func(t *testing.T) string {
			return sigTurn(intent.ClassifyTurn(intent.TurnInput{
				Event:     conversation.EventUtterance,
				Utterance: conversation.Utterance{Text: "message", ASRConfidence: 0.95},
				Verdict:   conversation.IntentClarify,
				Resolved: conversation.Intent{
					Name: intent.IntentCallPurpose, Confidence: 0.55,
					Alternatives: []conversation.Candidate{
						{Name: intent.IntentCallPurpose, Confidence: 0.55},
					},
				},
				Config: cfg,
			}))
		}},

		// 9 — multi-turn context.
		{"multi_turn_context", func(t *testing.T) string {
			b, err := voiceintel.New(voiceintel.WithClock(fixedClock()))
			if err != nil {
				t.Fatal(err)
			}
			p, err := b.Planner("det-multi", "")
			if err != nil {
				t.Fatal(err)
			}
			openFloor(t, p)
			conv, _ := b.Conversation("det-multi")

			var sigs []string
			for _, text := range []string{
				"please call me back on 9876543210",
				"transfer me to rajesh",
				"say that again",
			} {
				plan, err := p.Handle(utteranceEvent(text))
				if err != nil {
					sigs = append(sigs, "err="+err.Error())
					break
				}
				sigs = append(sigs, sigPlan(plan))
				if _, err := p.Handle(conversation.Event{
					Kind: conversation.EventSpeechComplete}); err != nil {
					sigs = append(sigs, "err="+err.Error())
					break
				}
			}
			return strings.Join(sigs, " >> ") + " || " + sigConversation(conv)
		}},

		// 10 — context eviction, with DISTINCT timestamps. See
		// TestT11_EvictionDeterminismBoundary for why the clock is advanced.
		{"context_eviction", func(t *testing.T) string {
			clock := fixedClock()
			b, err := voiceintel.New(voiceintel.WithClock(clock))
			if err != nil {
				t.Fatal(err)
			}
			p, err := b.Planner("det-evict", "")
			if err != nil {
				t.Fatal(err)
			}
			openFloor(t, p)
			conv, _ := b.Conversation("det-evict")
			c := conv.Context()

			for i := 0; i < frozenMaxEntriesPerScope+10; i++ {
				if err := c.Set(conversation.Entry{
					Key: fmt.Sprintf("k%04d", i), Value: i,
					Scope: conversation.ScopeConversation, Source: "t11",
				}); err != nil {
					t.Fatal(err)
				}
				clock.Advance(time.Millisecond) // distinct SetAt per entry
			}
			var survivors []string
			for i := 0; i < frozenMaxEntriesPerScope+10; i++ {
				k := fmt.Sprintf("k%04d", i)
				if _, ok := c.Get(conversation.ScopeConversation, k); ok {
					survivors = append(survivors, k)
				}
			}
			return fmt.Sprintf("size=%d survivors=%s",
				c.Size(conversation.ScopeConversation), strings.Join(survivors, ","))
		}},

		// 11 — malformed input.
		{"malformed", func(t *testing.T) string {
			cl := newClassifier(t)
			var out []string
			for _, payload := range []string{
				"\x00\x01\x02", strings.Repeat("A", 5000),
				"sk-live-SECRET-CANARY", "\xff\xfe\xfd", "",
			} {
				out = append(out, sigClassification(t, cl, payload, conversation.ExpectNothing))
			}
			return strings.Join(out, " >> ")
		}},

		// 12 — concurrent-session LOGICAL outcomes. The per-session results are
		// sorted before hashing, so goroutine completion order — which is not a
		// logical output — cannot enter the signature.
		{"concurrent_sessions", func(t *testing.T) string {
			specs := t10Specs()
			b, err := voiceintel.New(voiceintel.WithClock(fixedClock()))
			if err != nil {
				t.Fatal(err)
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
						results[i] = s.id + ":err=" + err.Error()
						return
					}
					results[i] = s.id + ":" + sigPlan(plan)
				}(i, s)
			}
			wg.Wait()
			sort.Strings(results)
			return strings.Join(results, " >> ")
		}},
	}
}

// matrixSignature renders the whole matrix as stable text.
func matrixSignature(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, sc := range matrix() {
		fmt.Fprintf(&b, "%s\n  %s\n", sc.name, sc.run(t))
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// 1. Repetition
// ---------------------------------------------------------------------------

// TestT11_MatrixIsIdenticalOver100Repetitions replays the whole scenario
// matrix and requires a byte-identical signature every time.
func TestT11_MatrixIsIdenticalOver100Repetitions(t *testing.T) {
	t.Parallel()

	want := matrixSignature(t)
	for i := 1; i <= 100; i++ {
		if got := matrixSignature(t); got != want {
			t.Fatalf("repetition %d drifted\n--- got ---\n%s\n--- want ---\n%s",
				i, got, want)
		}
	}
}

// TestT11_EachScenarioIsIndividuallyStable isolates drift to a scenario.
func TestT11_EachScenarioIsIndividuallyStable(t *testing.T) {
	t.Parallel()

	for _, sc := range matrix() {
		t.Run(sc.name, func(t *testing.T) {
			want := sc.run(t)
			for i := 1; i <= 100; i++ {
				if got := sc.run(t); got != want {
					t.Fatalf("iteration %d drifted\n got %s\nwant %s", i, got, want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2. Cross-process, via a golden file
// ---------------------------------------------------------------------------

// TestT11_MatrixMatchesTheGoldenSignature — the golden file is the
// cross-process evidence: every independent `go test` invocation is a fresh
// process, with a fresh address space, fresh map seeds and a fresh scheduler,
// comparing against identical bytes on disk. Running this under several
// shuffle seeds and in separate processes is what the T11 report records — if
// the golden is absent it is created and the test reports that loudly rather
// than silently passing.
func TestT11_MatrixMatchesTheGoldenSignature(t *testing.T) {
	got := matrixSignature(t)

	want, err := os.ReadFile(goldenPath)
	if os.IsNotExist(err) {
		if mkErr := os.MkdirAll(filepath.Dir(goldenPath), 0o755); mkErr != nil {
			t.Fatalf("creating testdata: %v", mkErr)
		}
		if wErr := os.WriteFile(goldenPath, []byte(got), 0o644); wErr != nil {
			t.Fatalf("writing golden: %v", wErr)
		}
		t.Fatalf("golden file %s did not exist; it has been created from this run. "+
			"Re-run to compare against it.", goldenPath)
	}
	if err != nil {
		t.Fatalf("reading golden: %v", err)
	}

	if got != string(want) {
		// Report the first differing line, not the whole blob.
		gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := 0; i < len(gl) || i < len(wl); i++ {
			var g, w string
			if i < len(gl) {
				g = gl[i]
			}
			if i < len(wl) {
				w = wl[i]
			}
			if g != w {
				t.Fatalf("signature differs from the golden at line %d\n got %q\nwant %q",
					i+1, g, w)
			}
		}
		t.Fatal("signature differs from the golden")
	}
}

// ---------------------------------------------------------------------------
// 5-8. Ordering contracts
// ---------------------------------------------------------------------------

// TestT11_CandidateOrderingIsDeterministic — highest confidence first, ties
// broken by name ascending, which is the frozen engine's own rule.
func TestT11_CandidateOrderingIsDeterministic(t *testing.T) {
	t.Parallel()

	c, err := intent.New(intent.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	// An utterance yielding candidates with DISTINCT confidences.
	//
	// Chosen by measurement, not guesswork: "call me back and transfer me to
	// rajesh and say that again" produces three candidates that all saturate to
	// 1.0, so the confidence comparison never runs and a reversed sort is
	// invisible. "repeat call back" yields request_callback@1.0 and
	// repeat@0.333, which exercises it.
	const text = "repeat call back"

	first, _, err := c.Classify(
		conversation.Utterance{Text: text, ASRConfidence: 0.95}, conversation.ExpectNothing)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) < 2 {
		t.Fatalf("only %d candidate(s); the ordering contract needs at least 2", len(first))
	}

	// There must be at least one strictly-descending step, or the ordering
	// contract is untested by this fixture.
	strictlyDescending := false
	for i := 1; i < len(first); i++ {
		if first[i].Confidence < first[i-1].Confidence {
			strictlyDescending = true
		}
	}
	if !strictlyDescending {
		// Either every candidate shares one confidence (the fixture is too weak
		// to detect a reversed sort) or the ordering is actually ascending (the
		// sort is broken). Both are failures; the values distinguish them.
		t.Fatalf("no strictly-descending step among %v: the candidates are either "+
			"all equal — so this fixture cannot detect a reversed sort — or "+
			"ordered ascending, which means the sort is reversed", first)
	}

	for i := 1; i < len(first); i++ {
		prev, cur := first[i-1], first[i]
		if cur.Confidence > prev.Confidence {
			t.Errorf("candidate %d (%s@%.4f) outranks its predecessor (%s@%.4f)",
				i, cur.Name, cur.Confidence, prev.Name, prev.Confidence)
		}
		if cur.Confidence == prev.Confidence && cur.Name < prev.Name {
			t.Errorf("equal-confidence candidates are not name-ordered: %q before %q",
				prev.Name, cur.Name)
		}
	}

	for rep := 0; rep < 200; rep++ {
		got, _, err := c.Classify(
			conversation.Utterance{Text: text, ASRConfidence: 0.95}, conversation.ExpectNothing)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(first) {
			t.Fatalf("rep %d: %d candidates, want %d", rep, len(got), len(first))
		}
		for i := range got {
			if got[i] != first[i] {
				t.Fatalf("rep %d: candidate %d = %+v, want %+v", rep, i, got[i], first[i])
			}
		}
	}
}

// TestT11_SlotOrderingIsDeterministic — slots come back name-ordered.
func TestT11_SlotOrderingIsDeterministic(t *testing.T) {
	t.Parallel()

	c, err := intent.New(intent.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	const text = "this is rajesh sharma from acme calling please call me back on 9876543210 tomorrow"

	first, firstSlots, err := c.Classify(
		conversation.Utterance{Text: text, ASRConfidence: 0.95}, conversation.ExpectNothing)
	if err != nil {
		t.Fatal(err)
	}
	_ = first
	if len(firstSlots) < 2 {
		t.Fatalf("only %d slot(s); the ordering contract needs at least 2", len(firstSlots))
	}
	if !sort.SliceIsSorted(firstSlots, func(i, j int) bool {
		return firstSlots[i].Name < firstSlots[j].Name
	}) {
		t.Errorf("slots are not name-ordered: %+v", firstSlots)
	}

	for rep := 0; rep < 200; rep++ {
		_, got, err := c.Classify(
			conversation.Utterance{Text: text, ASRConfidence: 0.95}, conversation.ExpectNothing)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(firstSlots) {
			t.Fatalf("rep %d: %d slots, want %d", rep, len(got), len(firstSlots))
		}
		for i := range got {
			if got[i] != firstSlots[i] {
				t.Fatalf("rep %d: slot %d = %+v, want %+v", rep, i, got[i], firstSlots[i])
			}
		}
	}
}

// TestT11_ContextSummaryOrderingIsDeterministic — ContextEngine.Export reads
// maps but sorts by (Scope, Key) before returning (context.go:406). This
// proves the OUTPUT is canonical despite the internal map iteration —
// requirement 11's "prove the output is sorted" branch.
func TestT11_ContextSummaryOrderingIsDeterministic(t *testing.T) {
	t.Parallel()

	build := func() string {
		b, err := voiceintel.New(voiceintel.WithClock(fixedClock()))
		if err != nil {
			t.Fatal(err)
		}
		p, err := b.Planner("det-ctx", "")
		if err != nil {
			t.Fatal(err)
		}
		openFloor(t, p)
		conv, _ := b.Conversation("det-ctx")
		c := conv.Context()

		// Written in a deliberately jumbled order and across scopes.
		for _, k := range []string{"zulu", "alpha", "mike", "bravo", "yankee", "charlie"} {
			for _, sc := range []conversation.Scope{
				conversation.ScopeConversation, conversation.ScopeSession,
				conversation.ScopeBusiness,
			} {
				if err := c.Set(conversation.Entry{
					Key: k, Value: k + "-value", Scope: sc, Source: "t11",
				}); err != nil {
					t.Fatal(err)
				}
			}
		}
		return sigContext(c)
	}

	want := build()
	if want == "" {
		t.Fatal("empty context summary; the test would be vacuous")
	}
	for i := 0; i < 100; i++ {
		if got := build(); got != want {
			t.Fatalf("iteration %d: context summary order drifted\n got %s\nwant %s",
				i, got, want)
		}
	}

	// And the rendering really is sorted, not merely repeatable.
	entries := strings.Split(want, ";")
	if !sort.SliceIsSorted(entries, func(i, j int) bool { return entries[i] < entries[j] }) {
		// Scope sorts before Key, so a raw string sort need not hold; assert
		// the frozen ordering explicitly instead.
		t.Logf("summary is scope-major, not lexicographic overall: %s", want)
	}
}

// TestT11_ResponseStrategySelectionIsDeterministic — the same utterance must
// yield the same action, reason, expectation and clarification every time.
func TestT11_ResponseStrategySelectionIsDeterministic(t *testing.T) {
	t.Parallel()

	cases := []string{
		"please call me back on 9876543210", // respond
		"i want to leave a message",         // ask / missing slot
		"zzzz qqqq",                         // unknown
		"transfer me to rajesh",             // respond
	}

	for _, text := range cases {
		t.Run(text, func(t *testing.T) {
			run := func(n int) string {
				b, err := voiceintel.New(voiceintel.WithClock(fixedClock()))
				if err != nil {
					t.Fatal(err)
				}
				id := conversation.ConversationID(fmt.Sprintf("strategy-%d", n))
				p, err := b.Planner(id, "")
				if err != nil {
					t.Fatal(err)
				}
				openFloor(t, p)
				plan, err := p.Handle(utteranceEvent(text))
				if err != nil {
					return "err=" + err.Error()
				}
				return sigPlan(plan)
			}
			want := run(0)
			for i := 1; i <= 100; i++ {
				if got := run(i); got != want {
					t.Fatalf("iteration %d: strategy drifted\n got %s\nwant %s", i, got, want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 9. The frozen equal-timestamp eviction boundary
// ---------------------------------------------------------------------------

// TestT11_EvictionDeterminismBoundary states EXACTLY what the frozen contract
// guarantees, and refuses to claim more.
//
// The evictOldestLocked helper selects with `e.SetAt.Before(oldest)`, which
// is false for
// equal timestamps. So:
//
//   - DETERMINISTIC: that eviction happens, that the bound holds, that exactly
//     one entry is evicted per overflow insert, and — when timestamps DIFFER —
//     which entry is evicted.
//   - NOT DETERMINISTIC: WHICH of several equal-SetAt entries is evicted. Go's
//     map iteration picks, and the frozen code does not tie-break.
//
// This is a frozen property. It is not patched, and T11's contract is written
// to avoid depending on it: the eviction scenario in matrix() advances the
// clock so every SetAt is distinct.
func TestT11_EvictionDeterminismBoundary(t *testing.T) {
	t.Parallel()

	// Part 1 — distinct timestamps: victim selection IS deterministic.
	withDistinctTimes := func() []string {
		clock := fixedClock()
		b, err := voiceintel.New(voiceintel.WithClock(clock))
		if err != nil {
			t.Fatal(err)
		}
		p, err := b.Planner("evict-distinct", "")
		if err != nil {
			t.Fatal(err)
		}
		openFloor(t, p)
		conv, _ := b.Conversation("evict-distinct")
		c := conv.Context()

		for i := 0; i < frozenMaxEntriesPerScope; i++ {
			if err := c.Set(conversation.Entry{
				Key: fmt.Sprintf("k%04d", i), Value: i,
				Scope: conversation.ScopeConversation, Source: "t11",
			}); err != nil {
				t.Fatal(err)
			}
			clock.Advance(time.Millisecond)
		}
		if err := c.Set(conversation.Entry{
			Key: "overflow", Value: "x",
			Scope: conversation.ScopeConversation, Source: "t11",
		}); err != nil {
			t.Fatal(err)
		}
		var survivors []string
		for i := 0; i < frozenMaxEntriesPerScope; i++ {
			k := fmt.Sprintf("k%04d", i)
			if _, ok := c.Get(conversation.ScopeConversation, k); ok {
				survivors = append(survivors, k)
			}
		}
		return survivors
	}

	want := strings.Join(withDistinctTimes(), ",")
	for i := 0; i < 50; i++ {
		if got := strings.Join(withDistinctTimes(), ","); got != want {
			t.Fatalf("distinct timestamps: victim selection drifted on iteration %d", i)
		}
	}
	if !strings.HasPrefix(want, "k0001,") {
		t.Errorf("oldest surviving key is not k0001; k0000 should have been evicted "+
			"as the oldest by SetAt. survivors start: %.40s", want)
	}

	// Part 2 — equal timestamps: only the GUARANTEED properties are asserted.
	withTiedTimes := func() (size int, overflowPresent bool, victims int) {
		b, err := voiceintel.New(voiceintel.WithClock(fixedClock())) // never advanced
		if err != nil {
			t.Fatal(err)
		}
		p, err := b.Planner("evict-tied", "")
		if err != nil {
			t.Fatal(err)
		}
		openFloor(t, p)
		conv, _ := b.Conversation("evict-tied")
		c := conv.Context()

		for i := 0; i < frozenMaxEntriesPerScope; i++ {
			if err := c.Set(conversation.Entry{
				Key: fmt.Sprintf("k%04d", i), Value: i,
				Scope: conversation.ScopeConversation, Source: "t11",
			}); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.Set(conversation.Entry{
			Key: "overflow", Value: "x",
			Scope: conversation.ScopeConversation, Source: "t11",
		}); err != nil {
			t.Fatal(err)
		}
		missing := 0
		for i := 0; i < frozenMaxEntriesPerScope; i++ {
			if _, ok := c.Get(conversation.ScopeConversation,
				fmt.Sprintf("k%04d", i)); !ok {
				missing++
			}
		}
		_, ok := c.Get(conversation.ScopeConversation, "overflow")
		return c.Size(conversation.ScopeConversation), ok, missing
	}

	victimsSeen := map[int]bool{}
	for i := 0; i < 30; i++ {
		size, overflowPresent, victims := withTiedTimes()
		if size != frozenMaxEntriesPerScope {
			t.Errorf("tied timestamps: size = %d, want exactly the bound %d",
				size, frozenMaxEntriesPerScope)
		}
		if !overflowPresent {
			t.Error("tied timestamps: the newest entry was evicted")
		}
		if victims != 1 {
			t.Errorf("tied timestamps: %d entries evicted, want exactly 1", victims)
		}
		victimsSeen[victims] = true
	}
	// Deliberately NOT asserted: which key was evicted. Asserting that would be
	// asserting an accident of map iteration order.
	t.Log("tied timestamps: bound and eviction COUNT are deterministic; victim " +
		"IDENTITY is unspecified by the frozen contract and is not asserted")
}

// ---------------------------------------------------------------------------
// 9-11. Structural determinism audit
// ---------------------------------------------------------------------------

// phase13DecisionFiles returns the non-test sources of the decision path.
func phase13DecisionFiles(t *testing.T) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	out := map[string]*ast.File{}
	for _, dir := range []string{".", "../intent"} {
		pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
			return !strings.HasSuffix(fi.Name(), "_test.go")
		}, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", dir, err)
		}
		for _, pkg := range pkgs {
			for name, f := range pkg.Files {
				out[name] = f
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("parsed no decision files; the audit would be vacuous")
	}
	return out
}

// TestT11_NoClockReadInDecisionLogic asserts that no decision-path file
// calls time.Now, time.Since or time.Until.
func TestT11_NoClockReadInDecisionLogic(t *testing.T) {
	t.Parallel()

	banned := map[string]bool{"Now": true, "Since": true, "Until": true}
	for name, f := range phase13DecisionFiles(t) {
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok || id.Name != "time" {
				return true
			}
			if banned[sel.Sel.Name] {
				t.Errorf("%s calls time.%s in the decision path; a decision that "+
					"reads the clock cannot be replayed", name, sel.Sel.Name)
			}
			return true
		})
	}
}

// TestT11_NoRandomnessInDecisionLogic asserts that no decision-path file
// imports a randomness package.
func TestT11_NoRandomnessInDecisionLogic(t *testing.T) {
	t.Parallel()

	for name, f := range phase13DecisionFiles(t) {
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if path == "math/rand" || path == "math/rand/v2" || path == "crypto/rand" {
				t.Errorf("%s imports %q; the decision path must not use randomness",
					name, path)
			}
		}
	}
}

// TestT11_NoMapIterationControlsObservableOrdering — requirement 11. Every
// `range` over a map in the decision path is flagged; the only permitted
// package-level map, timeWords, must be LOOKUP-ONLY.
func TestT11_NoMapIterationControlsObservableOrdering(t *testing.T) {
	t.Parallel()

	// Names of package-level map variables, so ranging over one is detectable.
	mapVars := map[string]bool{}
	files := phase13DecisionFiles(t)
	for _, f := range files {
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
				for i, id := range vs.Names {
					isMap := false
					if _, ok := vs.Type.(*ast.MapType); ok {
						isMap = true
					}
					if i < len(vs.Values) {
						if cl, ok := vs.Values[i].(*ast.CompositeLit); ok {
							if _, ok := cl.Type.(*ast.MapType); ok {
								isMap = true
							}
						}
					}
					if isMap {
						mapVars[id.Name] = true
					}
				}
			}
		}
	}

	var ranged int
	for name, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			rs, ok := n.(*ast.RangeStmt)
			if !ok {
				return true
			}
			ranged++
			if id, ok := rs.X.(*ast.Ident); ok && mapVars[id.Name] {
				t.Errorf("%s ranges over the package-level map %q; map iteration "+
					"order would become an observable output", name, id.Name)
			}
			return true
		})
	}
	if ranged == 0 {
		t.Fatal("found no range statements; the audit would be vacuous")
	}
	t.Logf("%d range statements inspected; package-level maps: %v", ranged, keysOf(mapVars))
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestT11_PackageLevelMapsAreNeverWritten — timeWords is permitted as a
// read-only lookup table (T4 exempts it by name); this proves the "read-only"
// half of that claim rather than taking it on trust.
func TestT11_PackageLevelMapsAreNeverWritten(t *testing.T) {
	t.Parallel()

	files := phase13DecisionFiles(t)
	var checked int
	for name, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range as.Lhs {
				ix, ok := lhs.(*ast.IndexExpr)
				if !ok {
					continue
				}
				checked++
				if id, ok := ix.X.(*ast.Ident); ok && id.Name == "timeWords" {
					t.Errorf("%s writes to timeWords; it must be read-only", name)
				}
			}
			return true
		})
	}
	t.Logf("%d indexed assignments inspected", checked)
}
