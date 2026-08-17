package intent_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/intent"
)

// T12 — BENCHMARKS for the intent package.
//
// A MEASUREMENT task. No production code was changed to improve a number.
//
// CATEGORY A only. Phase 13 performs no provider or model inference, so there
// is no Category B here and none is simulated: inventing a model latency would
// make every ratio in this file meaningless.
//
// CORRECTNESS BEFORE MEASUREMENT. Every benchmark validates its fixture before
// b.ResetTimer and fails with b.Fatalf if the operation does not produce the
// expected result. A benchmark that measures an error path, an empty fallback
// or a mis-wired fixture reports a fast number for the wrong work — which is
// worse than no number at all.
//
// COMPILER ELISION. Results are assigned to package-level sinks. Without that,
// the compiler is free to delete a call whose result is unused, and the
// benchmark would report the cost of an empty loop.

// Sinks. Package-level on purpose, so the compiler cannot prove the results are
// dead. They live in a _test.go file, so the Phase 13 no-package-state guards —
// which parse non-test files only — are unaffected.
var (
	sinkCandidates []conversation.Candidate
	sinkSlots      []conversation.Slot
	sinkErr        error
	sinkSignal     intent.TurnSignal
)

// benchClassifier builds the real classifier with the real default config.
func benchClassifier(b *testing.B) *intent.Classifier {
	b.Helper()
	c, err := intent.New(intent.DefaultConfig())
	if err != nil {
		b.Fatalf("intent.New: %v", err)
	}
	return c
}

// ---------------------------------------------------------------------------
// 1. Intent classification
// ---------------------------------------------------------------------------

// classifyCase is one measured input, with the outcome it must actually produce.
type classifyCase struct {
	name string
	text string
	// wantCandidates is the exact number the fixture must yield. Checked before
	// measuring, so a lexicon change that silently empties a case turns into a
	// failure rather than a suspiciously fast benchmark.
	wantCandidates int
	wantTop        conversation.IntentName
}

func classifyCases() []classifyCase {
	return []classifyCase{
		{
			name:           "Normal", // single confident intent, one filled slot
			text:           "please call me back on 9876543210",
			wantCandidates: 1, wantTop: intent.IntentRequestCallback,
		},
		{
			name:           "Ambiguous", // multi-candidate, distinct confidences
			text:           "repeat call back",
			wantCandidates: 2, wantTop: intent.IntentRequestCallback,
		},
		{
			name:           "Unknown", // nothing in the closed vocabulary matches
			text:           "zzzz qqqq wubble frotz",
			wantCandidates: 0, wantTop: "",
		},
	}
}

func BenchmarkClassify(b *testing.B) {
	for _, tc := range classifyCases() {
		b.Run(tc.name, func(b *testing.B) {
			c := benchClassifier(b)
			u := conversation.Utterance{Text: tc.text, ASRConfidence: 0.95}

			// Fixture validation — before the timer starts.
			cands, _, err := c.Classify(u, conversation.ExpectNothing)
			if err != nil {
				b.Fatalf("fixture returned an error: %v", err)
			}
			if len(cands) != tc.wantCandidates {
				b.Fatalf("fixture yields %d candidates, want %d — the benchmark "+
					"would measure the wrong path", len(cands), tc.wantCandidates)
			}
			if tc.wantCandidates > 0 && cands[0].Name != tc.wantTop {
				b.Fatalf("fixture top intent = %q, want %q", cands[0].Name, tc.wantTop)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sinkCandidates, sinkSlots, sinkErr = c.Classify(u, conversation.ExpectNothing)
			}
		})
	}
}

// BenchmarkClassify_ByInputLength shows how cost scales with utterance length,
// which is the input dimension the caller actually controls.
//
// The frozen bound is maxTokens = 512; the longest case here sits above it so
// the truncation path is measured too.
func BenchmarkClassify_ByInputLength(b *testing.B) {
	for _, tokens := range []int{1, 8, 64, 512, 1024} {
		b.Run(fmt.Sprintf("tokens=%d", tokens), func(b *testing.B) {
			c := benchClassifier(b)

			// EXACTLY `tokens` words. The first draft grew a 7-word seed until
			// it was "at least" the target, so the tokens=1 case actually fed 7
			// words and reported the baseline cost under a misleading label.
			words := make([]string, 0, tokens)
			cycle := []string{"please", "call", "me", "back", "on", "9876543210", "and"}
			for i := 0; i < tokens; i++ {
				words = append(words, cycle[i%len(cycle)])
			}
			text := strings.Join(words, " ")
			if got := len(splitWords(text)); got != tokens {
				b.Fatalf("fixture has %d words, want exactly %d", got, tokens)
			}
			u := conversation.Utterance{Text: text, ASRConfidence: 0.95}

			if _, _, err := c.Classify(u, conversation.ExpectNothing); err != nil {
				b.Fatalf("fixture error: %v", err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sinkCandidates, sinkSlots, sinkErr = c.Classify(u, conversation.ExpectNothing)
			}
		})
	}
}

// splitWords is a test-local word count, deliberately not the production
// tokenizer: using production code to size a production benchmark's input
// would couple the fixture to the thing being measured.
func splitWords(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// ---------------------------------------------------------------------------
// 5. Turn classification
// ---------------------------------------------------------------------------

func BenchmarkClassifyTurn(b *testing.B) {
	cfg := conversation.DefaultIntentConfig()

	cases := []struct {
		name string
		in   intent.TurnInput
		want conversation.IntentState
	}{
		{
			name: "NewRequest",
			in: intent.TurnInput{
				Event:     conversation.EventUtterance,
				Utterance: conversation.Utterance{Text: "please call me back", ASRConfidence: 0.95},
				Verdict:   conversation.IntentAccept,
				Resolved:  conversation.Intent{Name: intent.IntentRequestCallback, Confidence: 0.9},
				Config:    cfg,
			},
			want: conversation.IntentProposed,
		},
		{
			name: "Cancellation", // the one path that tokenises
			in: intent.TurnInput{
				Event:     conversation.EventUtterance,
				Utterance: conversation.Utterance{Text: "never mind", ASRConfidence: 0.95},
				Verdict:   conversation.IntentAccept,
				Resolved:  conversation.Intent{Name: intent.IntentRequestCallback, Confidence: 0.9},
				Active:    intent.IntentRequestCallback,
				Lifecycle: conversation.IntentActive,
				Config:    cfg,
			},
			want: conversation.IntentAbandoned,
		},
		{
			name: "Silence", // early return, no tokenisation
			in: intent.TurnInput{
				Event:     conversation.EventSilence,
				Lifecycle: conversation.IntentActive,
				Active:    intent.IntentHold,
				Config:    cfg,
			},
			want: conversation.IntentActive,
		},
		{
			name: "Interruption",
			in: intent.TurnInput{
				Event:     conversation.EventInterrupt,
				Lifecycle: conversation.IntentActive,
				Config:    cfg,
			},
			want: conversation.IntentActive,
		},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			if got := intent.ClassifyTurn(tc.in); got.Lifecycle != tc.want {
				b.Fatalf("fixture lifecycle = %v, want %v", got.Lifecycle, tc.want)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sinkSignal = intent.ClassifyTurn(tc.in)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 8. Concurrent classification
// ---------------------------------------------------------------------------

// BenchmarkClassify_Parallel measures the real production workload: ONE
// immutable classifier shared by every concurrent session.
//
// This is not an artificial shared-mutable-object benchmark — the Bridge shares
// exactly one classifier across sessions, and T10 proved it holds no mutable
// state. What is being measured is whether that sharing costs anything.
func BenchmarkClassify_Parallel(b *testing.B) {
	c := benchClassifier(b)
	u := conversation.Utterance{Text: "please call me back on 9876543210", ASRConfidence: 0.95}

	if cands, _, err := c.Classify(u, conversation.ExpectNothing); err != nil || len(cands) != 1 {
		b.Fatalf("fixture invalid: %d candidates, err=%v", len(cands), err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var lc []conversation.Candidate
		var ls []conversation.Slot
		var le error
		for pb.Next() {
			lc, ls, le = c.Classify(u, conversation.ExpectNothing)
		}
		// Publish once per goroutine, not per iteration: writing a package
		// sink inside the loop would add cross-goroutine cache-line contention
		// that the production path does not have, and would be measured as if
		// it were classification cost.
		sinkCandidates, sinkSlots, sinkErr = lc, ls, le
	})
}

// BenchmarkClassify_ConcurrencyScaling measures fixed total work spread across
// increasing goroutine counts, so scaling is visible rather than inferred.
//
// Each sub-benchmark performs the SAME number of classifications per b.N
// iteration, so ns/op is directly comparable across goroutine counts.
func BenchmarkClassify_ConcurrencyScaling(b *testing.B) {
	const workPerOp = 64

	for _, goroutines := range []int{1, 2, 4, 8, 16} {
		b.Run(fmt.Sprintf("goroutines=%d", goroutines), func(b *testing.B) {
			c := benchClassifier(b)
			u := conversation.Utterance{
				Text: "please call me back on 9876543210", ASRConfidence: 0.95}

			if cands, _, err := c.Classify(u, conversation.ExpectNothing); err != nil ||
				len(cands) != 1 {
				b.Fatalf("fixture invalid: %d candidates, err=%v", len(cands), err)
			}
			per := workPerOp / goroutines
			if per == 0 {
				b.Fatalf("workPerOp %d does not divide across %d goroutines",
					workPerOp, goroutines)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var wg sync.WaitGroup
				for g := 0; g < goroutines; g++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						var lc []conversation.Candidate
						for k := 0; k < per; k++ {
							lc, _, _ = c.Classify(u, conversation.ExpectNothing)
						}
						if len(lc) == 0 {
							panic("classification returned nothing")
						}
					}()
				}
				wg.Wait()
			}
		})
	}
}
