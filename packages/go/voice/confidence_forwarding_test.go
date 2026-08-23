package voice

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/speech"
)

// Phase 16 T14 — THE PIPELINE FABRICATES NO CONFIDENCE.
//
// # Why this file exists
//
// The T14 policy has two halves. The frozen conversation engine now skips the
// acoustic gate when no confidence was measured, and the T14 service tests
// cover that. This is the OTHER half, and it had no coverage at all:
//
//	the pipeline must forward what the provider reported, UNCHANGED.
//
// # Found by mutation, not by foresight
//
// Mutation M3 replaced pipeline.go's forwarding with "if the provider reported
// nothing, send 1.0". Every T14 and T11 test still passed — 20s of suite, all
// green. That is not a gap in those tests: conversation.Utterance.ASRConfidence
// has exactly ONE reader (conversation/intent.go:309), and a fabricated 1.0 and
// an honestly-unreported value take the SAME branch of it. The two are
// behaviourally indistinguishable downstream, so no downstream assertion can
// ever separate them.
//
// The only place the difference is observable is the seam itself, at the moment
// the Utterance is constructed. That is what this test watches.
//
// This matters beyond tidiness: "never fabricate a confidence" is an explicit
// term of the approved policy, and the whispercli/whispercpp adapters have
// their own tests pinning that they report nothing rather than invent a number.
// Without this test the pipeline could quietly undo both, and every other test
// in the repository would stay green.
func TestT14_PipelineForwardsProviderConfidenceVerbatim(t *testing.T) {
	for _, tc := range []struct {
		name       string
		confidence float64
	}{
		// The value a real whisper-class adapter reports (whispercli.go:691).
		{"provider reports nothing", speech.ConfidenceUnknown},
		// A measured low confidence must not be "helpfully" raised either.
		{"measured low confidence", 0.12},
		// Nor a measured high one altered.
		{"measured high confidence", 0.94},
		// A genuine zero is distinct from unreported and must survive as zero.
		{"measured zero", 0.0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, harnessOpts{
				segments: []speech.TranscriptSegment{{
					Segment:    speech.SegmentID("seg-final"),
					Sequence:   0,
					Text:       "transfer me to rajesh",
					IsFinal:    true,
					Confidence: tc.confidence,
				}},
			})

			// Captured ON the pipeline's own goroutine as the event is handled,
			// so this is the exact value the frozen engine was given — not a
			// value reconstructed afterwards.
			var (
				mu     sync.Mutex
				got    float64
				seen   bool
				gotTxt string
			)
			h.planner.atEvent = func(e conversation.Event) {
				if e.Kind != conversation.EventUtterance {
					return
				}
				mu.Lock()
				got, gotTxt, seen = e.Utterance.ASRConfidence, e.Utterance.Text, true
				mu.Unlock()
			}

			if err := h.pipeline.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer func() { _ = h.pipeline.Disconnect() }()

			h.feed(t, 4)
			h.obs.waitTurn(t, 15*time.Second)

			mu.Lock()
			defer mu.Unlock()
			if !seen {
				t.Fatal("no utterance reached the planner, so nothing was forwarded")
			}
			if gotTxt != "transfer me to rajesh" {
				t.Errorf("forwarded text = %q, want the provider's transcript", gotTxt)
			}
			// EXACT equality, deliberately. Any mapping, clamping, defaulting or
			// substitution — however well intentioned — changes this number, and
			// changing it is precisely what the policy forbids.
			if got != tc.confidence {
				t.Errorf("the pipeline forwarded ASRConfidence %v, but the provider "+
					"reported %v: the pipeline must forward it UNCHANGED and "+
					"fabricate nothing", got, tc.confidence)
			}
		})
	}
}
