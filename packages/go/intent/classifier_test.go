package intent_test

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/intent"
)

// TEST DESIGN RULE OBSERVED THROUGHOUT THIS FILE.
//
// Every expected confidence below is computed BY HAND from the algorithm
// documented on Classify, and written as a literal. No test calls
// DefaultRules, cueWeight or score to derive what it then asserts — a test
// that recomputes the implementation's arithmetic with the implementation's
// own tables proves only that the code equals itself.
//
// The documented algorithm, restated so the arithmetic below can be checked
// without reading the implementation:
//
//	cue weight   = 1 for a one-token cue, len+1 for a phrase
//	evidence     = sum of matched cue weights, each cue counted at most once
//	confidence   = min(evidence / 3.0, 1.0)      (3.0 = default saturation)
//
// So a lone keyword scores 1/3, a two-token phrase scores 3/3 = 1.0, and a
// three-token phrase scores 4/3 which caps at 1.0.

const oneToken = 1.0 / 3.0 // a single-token cue: 1 / 3.0

func newClassifier(t *testing.T) *intent.Classifier {
	t.Helper()
	c, err := intent.New(intent.DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func say(text string) conversation.Utterance {
	// ASRConfidence is well above the frozen MinASRConfidence of 0.40 so these
	// utterances would reach a classifier in production. The classifier itself
	// deliberately ignores the field.
	return conversation.Utterance{Text: text, ASRConfidence: 0.9}
}

func classify(t *testing.T, c *intent.Classifier, text string,
	expect conversation.Expectation,
) []conversation.Candidate {
	t.Helper()
	got, slots, err := c.Classify(say(text), expect)
	if err != nil {
		t.Fatalf("Classify(%q): %v", text, err)
	}
	// Until T4 this asserted "no slots", which is what made T4's first change
	// show up as a failing test rather than pass unnoticed. Now that slots are
	// produced, the guard is replaced by the stronger invariants they must hold
	// on EVERY call in this file — not just in the slot-specific tests.
	if len(slots) > intent.MaxSlotsPerIntent {
		t.Errorf("Classify(%q) returned %d slots, exceeding the cap of %d",
			text, len(slots), intent.MaxSlotsPerIntent)
	}
	for _, s := range slots {
		if !expectedSlotVocabulary[s.Name] {
			t.Errorf("Classify(%q) emitted slot %q, outside the closed vocabulary",
				text, s.Name)
		}
		if s.Confidence < 0 || s.Confidence > 1 {
			t.Errorf("Classify(%q) slot %q confidence %v outside [0,1]",
				text, s.Name, s.Confidence)
		}
		if !s.Filled && s.Confidence != 0 {
			t.Errorf("Classify(%q) slot %q is unfilled but carries confidence %v",
				text, s.Name, s.Confidence)
		}
	}
	return got
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func names(cs []conversation.Candidate) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, string(c.Name))
	}
	return out
}

// ---------------------------------------------------------------------------
// 1-2. Known intents
// ---------------------------------------------------------------------------

func TestClassify_KnownIntent_Greeting(t *testing.T) {
	t.Parallel()

	got := classify(t, newClassifier(t), "hello", conversation.ExpectNothing)

	if len(got) != 1 {
		t.Fatalf("got %d candidates %v, want exactly 1", len(got), names(got))
	}
	if got[0].Name != "greeting" {
		t.Errorf("name = %q, want %q", got[0].Name, "greeting")
	}
	// "hello" is a single-token cue: evidence 1, confidence 1/3.
	if !approx(got[0].Confidence, oneToken) {
		t.Errorf("confidence = %v, want %v", got[0].Confidence, oneToken)
	}
}

// TestClassify_KnownIntent_PhraseIsDecisive — a multi-token phrase saturates.
//
// "please call me back": the cue "call me back" is three tokens, weight 4,
// evidence 4, confidence 4/3 which caps at 1.0. The two-token cue "call back"
// does NOT also match, because the tokens are call/me/back and a phrase cue
// matches only as a contiguous run.
func TestClassify_KnownIntent_PhraseIsDecisive(t *testing.T) {
	t.Parallel()

	got := classify(t, newClassifier(t), "please call me back", conversation.ExpectNothing)

	if len(got) != 1 {
		t.Fatalf("got %d candidates %v, want exactly 1", len(got), names(got))
	}
	if got[0].Name != "request_callback" {
		t.Errorf("name = %q, want %q", got[0].Name, "request_callback")
	}
	if !approx(got[0].Confidence, 1.0) {
		t.Errorf("confidence = %v, want 1.0 (4/3 capped)", got[0].Confidence)
	}
}

// ---------------------------------------------------------------------------
// 3. Unknown → empty, NOT an invented intent
// ---------------------------------------------------------------------------

func TestClassify_UnknownUtteranceReturnsNoCandidates(t *testing.T) {
	t.Parallel()
	c := newClassifier(t)

	for _, text := range []string{
		"xyzzy plugh frobnicate",
		"the quick brown fox jumped",
		"42 17 99",
	} {
		got := classify(t, c, text, conversation.ExpectNothing)
		if len(got) != 0 {
			t.Errorf("Classify(%q) = %v, want no candidates — unknown must be an "+
				"empty slice, never an invented intent", text, names(got))
		}
	}
}

// ---------------------------------------------------------------------------
// 4. Low confidence is a REAL candidate, distinct from unknown
// ---------------------------------------------------------------------------

// TestClassify_LowConfidenceIsACandidateNotSilence pins the distinction the
// frozen engine depends on: a weak match is a named candidate the engine can
// reject or clarify on; an unrecognised utterance is an empty slice that
// becomes the fallback. Collapsing the two would hide one case inside the
// other.
//
// "message" is a single-token cue for leave_message: evidence 1, confidence
// 1/3 = 0.333, which sits BELOW the frozen RejectThreshold of 0.45 — so the
// engine will reject it, and that is the engine's decision, not this
// package's.
func TestClassify_LowConfidenceIsACandidateNotSilence(t *testing.T) {
	t.Parallel()

	got := classify(t, newClassifier(t), "message", conversation.ExpectNothing)

	if len(got) != 1 {
		t.Fatalf("got %d candidates %v, want exactly 1", len(got), names(got))
	}
	if got[0].Name != "leave_message" {
		t.Errorf("name = %q, want %q", got[0].Name, "leave_message")
	}
	if !approx(got[0].Confidence, oneToken) {
		t.Errorf("confidence = %v, want %v", got[0].Confidence, oneToken)
	}
	const frozenRejectThreshold = 0.45
	if got[0].Confidence >= frozenRejectThreshold {
		t.Errorf("confidence %v is not below the frozen RejectThreshold %v; "+
			"this fixture no longer demonstrates the low-confidence case",
			got[0].Confidence, frozenRejectThreshold)
	}
}

// ---------------------------------------------------------------------------
// 5-6. Multiple candidates and ordering
// ---------------------------------------------------------------------------

// "hello please transfer me":
//
//	greeting          "hello"       1 token  → 1 → 1/3
//	request_transfer  "transfer me" 2 tokens → 3 → 3/3 = 1.0
//
// so request_transfer must come first.
func TestClassify_MultipleCandidatesOrderedByConfidence(t *testing.T) {
	t.Parallel()

	got := classify(t, newClassifier(t), "hello please transfer me", conversation.ExpectNothing)

	if len(got) != 2 {
		t.Fatalf("got %d candidates %v, want 2", len(got), names(got))
	}
	if got[0].Name != "request_transfer" || !approx(got[0].Confidence, 1.0) {
		t.Errorf("first = %q@%v, want request_transfer@1.0", got[0].Name, got[0].Confidence)
	}
	if got[1].Name != "greeting" || !approx(got[1].Confidence, oneToken) {
		t.Errorf("second = %q@%v, want greeting@%v", got[1].Name, got[1].Confidence, oneToken)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Confidence < got[i].Confidence {
			t.Errorf("not descending at %d: %v then %v",
				i, got[i-1].Confidence, got[i].Confidence)
		}
	}
}

// TestClassify_MarginIsMeaningful — the ordering exists so the frozen
// Intent.Margin() can see ambiguity a threshold alone misses.
func TestClassify_MarginIsMeaningful(t *testing.T) {
	t.Parallel()

	got := classify(t, newClassifier(t), "hello please transfer me", conversation.ExpectNothing)
	in := conversation.Intent{
		Name: got[0].Name, Confidence: got[0].Confidence, Alternatives: got,
	}

	// 1.0 - 1/3 = 2/3, comfortably above the frozen AmbiguityMargin of 0.15.
	want := 1.0 - oneToken
	if !approx(in.Margin(), want) {
		t.Errorf("Margin() = %v, want %v", in.Margin(), want)
	}
}

// ---------------------------------------------------------------------------
// 7. Tie-break
// ---------------------------------------------------------------------------

// "hello wait":
//
//	greeting "hello" → 1 → 1/3
//	hold     "wait"  → 1 → 1/3
//
// Equal. The documented tie-break is lexicographic by name ASCENDING, matching
// the frozen engine's own rule, so "greeting" precedes "hold".
func TestClassify_EqualScoresBreakTiesByNameAscending(t *testing.T) {
	t.Parallel()

	got := classify(t, newClassifier(t), "hello wait", conversation.ExpectNothing)

	if len(got) != 2 {
		t.Fatalf("got %d candidates %v, want 2", len(got), names(got))
	}
	if !approx(got[0].Confidence, got[1].Confidence) {
		t.Fatalf("fixture no longer produces a tie: %v vs %v",
			got[0].Confidence, got[1].Confidence)
	}
	if got[0].Name != "greeting" || got[1].Name != "hold" {
		t.Errorf("order = %v, want [greeting hold] — ties break by name ascending",
			names(got))
	}
}

// ---------------------------------------------------------------------------
// 8. Confidence bounds
// ---------------------------------------------------------------------------

func TestClassify_ConfidenceAlwaysWithinUnitInterval(t *testing.T) {
	t.Parallel()
	c := newClassifier(t)

	inputs := []string{
		"hello", "yes", "no", "message", "please call me back",
		"hello hello hello hello hello",
		"call me back call me back call me back",
		"hello hi hey namaste good morning good evening",
		"leave a message and call me back and transfer me and goodbye",
		strings.Repeat("hello ", 200),
		"", "   ", "!!!", "héllo", "\x00\x01\x02",
	}
	for _, text := range inputs {
		for _, e := range []conversation.Expectation{
			conversation.ExpectNothing,
			conversation.ExpectDisambiguation,
			conversation.ExpectYesNo,
			conversation.ExpectSlotValue,
		} {
			for _, cand := range classify(t, c, text, e) {
				if cand.Confidence < 0 || cand.Confidence > 1 {
					t.Errorf("Classify(%q, %v) → %q confidence %v is outside [0,1]",
						text, e, cand.Name, cand.Confidence)
				}
			}
		}
	}
}

// TestClassify_RepetitionCannotInflateConfidence — each cue counts once, so
// saying a word ten times is not ten times the evidence.
func TestClassify_RepetitionCannotInflateConfidence(t *testing.T) {
	t.Parallel()
	c := newClassifier(t)

	once := classify(t, c, "message", conversation.ExpectNothing)
	many := classify(t, c, "message message message message message", conversation.ExpectNothing)

	if len(once) != 1 || len(many) != 1 {
		t.Fatalf("want one candidate each, got %d and %d", len(once), len(many))
	}
	if !approx(once[0].Confidence, many[0].Confidence) {
		t.Errorf("repetition changed confidence: %v -> %v",
			once[0].Confidence, many[0].Confidence)
	}
}

// ---------------------------------------------------------------------------
// 9. Closed vocabulary
// ---------------------------------------------------------------------------

// expectedVocabulary is written out by hand rather than read from
// intent.Vocabulary(). Asserting the implementation's list against itself
// would pass no matter what the list contained.
var expectedVocabulary = map[string]bool{
	"affirm": true, "deny": true, "greeting": true, "caller_identity": true,
	"call_purpose": true, "leave_message": true, "request_callback": true,
	"request_transfer": true, "repeat": true, "hold": true, "end_call": true,
}

func TestClassify_NeverEmitsOutOfVocabularyIntent(t *testing.T) {
	t.Parallel()
	c := newClassifier(t)

	corpus := []string{
		"hello", "yes", "no", "message", "please call me back", "transfer me",
		"say that again", "hold on", "goodbye", "this is priya calling",
		"i am calling about the invoice", "namaste", "haan ji", "nahi",
		"xyzzy", "", "!!!", strings.Repeat("wait ", 50),
		"leave a message and then call me back and transfer me",
	}
	for _, text := range corpus {
		for _, e := range []conversation.Expectation{
			conversation.ExpectNothing, conversation.ExpectDisambiguation,
			conversation.ExpectYesNo, conversation.ExpectSlotValue,
		} {
			for _, cand := range classify(t, c, text, e) {
				if !expectedVocabulary[string(cand.Name)] {
					t.Errorf("Classify(%q) emitted %q, which is outside the closed "+
						"vocabulary", text, cand.Name)
				}
			}
		}
	}
}

func TestVocabulary_MatchesTheDeclaredSet(t *testing.T) {
	t.Parallel()

	got := intent.Vocabulary()
	if len(got) != len(expectedVocabulary) {
		t.Errorf("Vocabulary() has %d names, want %d", len(got), len(expectedVocabulary))
	}
	seen := map[string]bool{}
	for _, n := range got {
		if !expectedVocabulary[string(n)] {
			t.Errorf("Vocabulary() contains unexpected name %q", n)
		}
		if seen[string(n)] {
			t.Errorf("Vocabulary() contains %q twice", n)
		}
		seen[string(n)] = true
	}
	for want := range expectedVocabulary {
		if !seen[want] {
			t.Errorf("Vocabulary() is missing %q", want)
		}
	}
}

// TestNew_RejectsAnOutOfVocabularyRule — the closed vocabulary is enforced at
// construction, not discovered in an audit record.
func TestNew_RejectsAnOutOfVocabularyRule(t *testing.T) {
	t.Parallel()

	_, err := intent.New(intent.Config{Rules: []intent.Rule{
		{Name: conversation.IntentName("exfiltrate_everything"),
			Cues: [][]string{{"hello"}}},
	}})
	if err == nil {
		t.Fatal("New accepted a rule naming an out-of-vocabulary intent")
	}
	if !strings.Contains(err.Error(), "closed vocabulary") {
		t.Errorf("error %q does not explain the closed-vocabulary violation", err)
	}
}

// ---------------------------------------------------------------------------
// 10. Expectation
// ---------------------------------------------------------------------------

// TestClassify_ExpectSlotValueSuppressesSingleKeywordMatches pins the one
// expectation-sensitive rule.
//
// While answering a slot question, a lone keyword is far more likely to be
// part of the answer than a new request. A phrase-level cue still counts.
//
//	"message"          → single-token cue only  → suppressed under ExpectSlotValue
//	"leave a message"  → phrase cue matches     → kept
func TestClassify_ExpectSlotValueSuppressesSingleKeywordMatches(t *testing.T) {
	t.Parallel()
	c := newClassifier(t)

	if got := classify(t, c, "message", conversation.ExpectSlotValue); len(got) != 0 {
		t.Errorf("under ExpectSlotValue a lone keyword produced %v; it should be "+
			"treated as part of the slot answer", names(got))
	}
	if got := classify(t, c, "message", conversation.ExpectNothing); len(got) != 1 {
		t.Errorf("under ExpectNothing the same keyword produced %d candidates, want 1",
			len(got))
	}

	got := classify(t, c, "leave a message", conversation.ExpectSlotValue)
	if len(got) != 1 || got[0].Name != "leave_message" {
		t.Fatalf("phrase cue under ExpectSlotValue = %v, want [leave_message]", names(got))
	}
	// "leave a message" (3 tokens → 4) plus "message" (1 token → 1) = 5;
	// 5/3 caps at 1.0.
	if !approx(got[0].Confidence, 1.0) {
		t.Errorf("confidence = %v, want 1.0", got[0].Confidence)
	}
}

// TestClassify_OtherExpectationsDoNotChangeScoring — only ExpectSlotValue is
// special. ExpectYesNo never reaches a classifier at all (the frozen engine
// short-circuits it at intent.go:315), and this asserts the classifier does
// not quietly behave differently for it anyway.
func TestClassify_OtherExpectationsDoNotChangeScoring(t *testing.T) {
	t.Parallel()
	c := newClassifier(t)

	base := classify(t, c, "hello please transfer me", conversation.ExpectNothing)
	for _, e := range []conversation.Expectation{
		conversation.ExpectDisambiguation, conversation.ExpectYesNo,
	} {
		got := classify(t, c, "hello please transfer me", e)
		if len(got) != len(base) {
			t.Fatalf("expectation %v changed candidate count: %d vs %d", e, len(got), len(base))
		}
		for i := range got {
			if got[i] != base[i] {
				t.Errorf("expectation %v changed candidate %d: %+v vs %+v",
					e, i, got[i], base[i])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 11-12. Empty, whitespace, adversarial
// ---------------------------------------------------------------------------

func TestClassify_EmptyAndWhitespaceProduceNothing(t *testing.T) {
	t.Parallel()
	c := newClassifier(t)

	for _, text := range []string{"", " ", "\t\n", "   \r\n  ", "!!!", "...", "?!,."} {
		if got := classify(t, c, text, conversation.ExpectNothing); len(got) != 0 {
			t.Errorf("Classify(%q) = %v, want none", text, names(got))
		}
	}
}

func TestClassify_AdversarialInputStaysBounded(t *testing.T) {
	t.Parallel()
	c := newClassifier(t)

	for _, text := range []string{
		strings.Repeat("hello ", 5000),
		strings.Repeat("a", 100000),
		strings.Repeat("call me back ", 2000),
		"\x00\x01\x02\x03 hello \x7f",
		"héllo wörld नमस्ते こんにちは",
		strings.Repeat("hello wait message goodbye ", 500),
	} {
		got := classify(t, c, text, conversation.ExpectNothing)
		if len(got) > intent.DefaultMaxCandidates {
			t.Errorf("Classify(len=%d) returned %d candidates, exceeding the cap of %d",
				len(text), len(got), intent.DefaultMaxCandidates)
		}
		for _, cand := range got {
			if cand.Confidence < 0 || cand.Confidence > 1 {
				t.Errorf("confidence %v outside [0,1] on adversarial input", cand.Confidence)
			}
			if !expectedVocabulary[string(cand.Name)] {
				t.Errorf("adversarial input produced out-of-vocabulary %q", cand.Name)
			}
		}
	}
}

// TestClassify_CandidateCountIsCapped — the engine copies the whole slice into
// Intent.Alternatives, which reaches an audit record, so it must be bounded.
func TestClassify_CandidateCountIsCapped(t *testing.T) {
	t.Parallel()

	c, err := intent.New(intent.Config{MaxCandidates: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// An utterance deliberately matching many rules at once.
	got := classify(t, c,
		"hello yes no message call back transfer me repeat wait goodbye",
		conversation.ExpectNothing)

	if len(got) > 2 {
		t.Errorf("got %d candidates %v, want at most 2", len(got), names(got))
	}
}

// ---------------------------------------------------------------------------
// 13. Determinism
// ---------------------------------------------------------------------------

// TestClassify_IsDeterministicAcross100Executions is the headline property.
//
// The complete result — every name and every confidence, in order — must be
// identical every time. Anything derived from map iteration, a clock, entropy
// or scheduling would show up here.
func TestClassify_IsDeterministicAcross100Executions(t *testing.T) {
	t.Parallel()

	fixtures := []struct {
		text   string
		expect conversation.Expectation
	}{
		{"hello please transfer me and leave a message", conversation.ExpectNothing},
		{"hello wait", conversation.ExpectNothing},
		{"call me back tomorrow", conversation.ExpectNothing},
		{"message", conversation.ExpectSlotValue},
		{"leave a message", conversation.ExpectSlotValue},
		{"xyzzy", conversation.ExpectNothing},
		{"", conversation.ExpectNothing},
		{"hello yes no message call back transfer me repeat wait goodbye",
			conversation.ExpectDisambiguation},
	}

	for _, f := range fixtures {
		// A fresh classifier each iteration, so construction order cannot be
		// the thing that happens to be stable.
		first := render(classify(t, newClassifier(t), f.text, f.expect))
		for i := 0; i < 100; i++ {
			got := render(classify(t, newClassifier(t), f.text, f.expect))
			if got != first {
				t.Fatalf("Classify(%q, %v) not deterministic on iteration %d\n"+
					"  first: %s\n  got:   %s", f.text, f.expect, i, first, got)
			}
		}
	}
}

// render is a total, ordered string form of a result, so a comparison catches
// a reordering as readily as a value change.
func render(cs []conversation.Candidate) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("n=%d", len(cs)))
	for _, c := range cs {
		fmt.Fprintf(&b, "|%s=%.17g", c.Name, c.Confidence)
	}
	return b.String()
}

// TestClassify_DoesNotMutateItsInput — the utterance belongs to the caller and
// carries SENSITIVE text.
func TestClassify_DoesNotMutateItsInput(t *testing.T) {
	t.Parallel()

	u := say("hello please transfer me")
	before := u
	if _, _, err := newClassifier(t).Classify(u, conversation.ExpectNothing); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if u != before {
		t.Errorf("Classify mutated its Utterance: %+v -> %+v", before, u)
	}
}

// TestClassify_ConcurrentUseIsConsistent — a Classifier is shared across
// sessions, so the same input must classify identically under contention.
// (Not a race-detector claim; see the T3 report.)
func TestClassify_ConcurrentUseIsConsistent(t *testing.T) {
	t.Parallel()

	c := newClassifier(t)
	want := render(classify(t, c, "hello please transfer me", conversation.ExpectNothing))

	const workers, iterations = 16, 50
	errs := make(chan string, workers*iterations)
	done := make(chan struct{})

	for w := 0; w < workers; w++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < iterations; i++ {
				got, _, err := c.Classify(say("hello please transfer me"),
					conversation.ExpectNothing)
				if err != nil {
					errs <- fmt.Sprintf("err: %v", err)
					return
				}
				if r := render(got); r != want {
					errs <- fmt.Sprintf("got %s, want %s", r, want)
					return
				}
			}
		}()
	}
	for w := 0; w < workers; w++ {
		<-done
	}
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent classification diverged: %s", e)
	}
}
