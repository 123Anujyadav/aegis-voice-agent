package intent

import (
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
)

// T12 — internal benchmarks.
//
// In package `intent` rather than `intent_test` because the confidence
// computation is unexported. Measuring a copy of it from outside would report
// the cost of the copy, not of the code that runs in production.

var (
	sinkEvidence float64
	sinkPhrase   bool
	sinkTokens   []string
	sinkIntSlots []conversation.Slot
)

// ---------------------------------------------------------------------------
// 6. Confidence calculation
// ---------------------------------------------------------------------------

// BenchmarkConfidenceScoring measures score(), which is where confidence comes
// from: cue matching accumulates evidence, and Classify then divides it by the
// rule's saturation and caps at 1.
//
// The division and cap are three machine instructions and are inlined into
// Classify; the measurable cost of "calculating confidence" is the evidence
// accumulation, which is what this measures.
func BenchmarkConfidenceScoring(b *testing.B) {
	rules := DefaultRules()
	if len(rules) == 0 {
		b.Fatal("no default rules; the benchmark would measure nothing")
	}

	// The rule with the most cues, so the measured path is the expensive one
	// rather than an unrepresentative best case.
	widest := rules[0]
	for _, r := range rules {
		if len(r.Cues) > len(widest.Cues) {
			widest = r
		}
	}

	tokens := tokenize("please call me back on 9876543210 tomorrow morning")
	if len(tokens) == 0 {
		b.Fatal("tokenizer returned nothing")
	}

	b.Run("hit", func(b *testing.B) {
		r := ruleFor(b, rules, IntentRequestCallback)
		ev, _ := score(tokens, r)
		if ev == 0 {
			b.Fatalf("fixture produces zero evidence; the benchmark would measure " +
				"the miss path while claiming to measure a hit")
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkEvidence, sinkPhrase = score(tokens, r)
		}
	})

	b.Run("miss", func(b *testing.B) {
		r := ruleFor(b, rules, IntentEndCall)
		missTokens := tokenize("zzzz qqqq wubble frotz plugh xyzzy")
		if ev, _ := score(missTokens, r); ev != 0 {
			b.Fatalf("fixture produces evidence %v; expected a miss", ev)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkEvidence, sinkPhrase = score(missTokens, r)
		}
	})

	b.Run("widest_rule", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkEvidence, sinkPhrase = score(tokens, widest)
		}
	})
}

func ruleFor(b *testing.B, rules []Rule, name conversation.IntentName) Rule {
	b.Helper()
	for _, r := range rules {
		if r.Name == name {
			return r
		}
	}
	b.Fatalf("no rule for %q", name)
	return Rule{}
}

// BenchmarkTokenize measures the shared front end of both Classify and
// ClassifyTurn — every text-bearing path pays it exactly once.
func BenchmarkTokenize(b *testing.B) {
	for _, tc := range []struct{ name, text string }{
		{"short", "never mind"},
		{"typical", "please call me back on 9876543210 tomorrow morning"},
		{"long", longText(600)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			if len(tokenize(tc.text)) == 0 {
				b.Fatal("fixture tokenises to nothing")
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sinkTokens = tokenize(tc.text)
			}
		})
	}
}

func longText(words int) string {
	s := ""
	for i := 0; i < words; i++ {
		s += "callback "
	}
	return s
}

// BenchmarkSortCandidates measures the canonical ordering step. A fresh copy is
// made per iteration because sorting an already-sorted slice is a different and
// much cheaper operation — measuring that would overstate throughput.
func BenchmarkSortCandidates(b *testing.B) {
	base := []conversation.Candidate{
		{Name: "repeat", Confidence: 0.33},
		{Name: "request_callback", Confidence: 1.0},
		{Name: "hold", Confidence: 0.66},
		{Name: "request_transfer", Confidence: 0.5},
		{Name: "leave_message", Confidence: 0.5},
	}
	buf := make([]conversation.Candidate, len(base))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copy(buf, base) // included deliberately; see the note above
		sortCandidates(buf)
	}
}

// BenchmarkExtractSlots measures slot-shape extraction for the resolved intent.
func BenchmarkExtractSlots(b *testing.B) {
	tokens := tokenize("this is rajesh sharma from acme calling please call me " +
		"back on 9876543210 tomorrow")
	specs := slotSpecsFor(IntentRequestCallback)
	if len(specs) == 0 {
		b.Fatal("no slot specs; the benchmark would measure nothing")
	}
	if got := extractSlots(tokens, specs); len(got) == 0 {
		b.Fatal("fixture extracts no slots")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sinkIntSlots = extractSlots(tokens, specs)
	}
}
