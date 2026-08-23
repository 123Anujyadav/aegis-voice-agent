package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/speech"
	"github.com/callscreen/callscreen-platform/packages/go/voice"
)

// Phase 16 T13 — PINNING THE REAL-PROVIDER CONFIDENCE CASCADE.
//
// # What this file is, and what it deliberately is not
//
// It is EVIDENCE, not a fix. Every assertion here pins behaviour that is
// currently WRONG for real callers. Nothing in this file argues for a
// particular repair, and no production file is changed by the task that added
// it. When the confidence policy is decided and implemented, these tests are
// expected to FAIL — that is their purpose, and they must be updated
// deliberately rather than deleted.
//
// # The chain, traced from source rather than inferred
//
//	whispercli.go:691   Confidence: speech.ConfidenceUnknown   (-1.0, hardcoded)
//	  |                 the tool is run with --output_format txt
//	  |                 (whispercli.go:482), which carries no confidence at all
//	  v
//	transcript.go:171   the speech contract EXPLICITLY permits the sentinel
//	  v
//	pipeline.go:1074    ASRConfidence: final.Confidence   <- forwarded verbatim
//	  |                 pipeline.go:1072 is the ONLY production site that
//	  |                 constructs a conversation.Utterance
//	  v
//	intent.go:127       the field's documented domain is "0..1"
//	intent.go:309       if u.ASRConfidence < MinASRConfidence -> IntentNoise
//	  |                 -1.0 < 0.40, so the sentinel reads as the LOWEST
//	  |                 possible confidence rather than as "not reported"
//	  v
//	clarification.go:168  verdict IntentNoise -> Request{Kind: ClarifyNoise}
//	clarification.go:210  noiseRounds >= RepeatOnNoise (default 2) -> allowed=false
//	  v
//	planner.go:149      rounds 1-2: ActionClarify  "noise_repeat"
//	planner.go:156      round 3:    ActionEscalate "noise_unrecoverable"
//	  v
//	state.go:164        StateEscalated IS TERMINAL
//
// Rungs 1-3 of the planner ladder do not preempt rung 4 here: the default
// persona is PersonaBusinessReceptionist (engine.go:146), which sets
// MaxTurns 40 and leaves EscalateOnUncertainty false (persona.go:137). Only
// PersonaEmergencyAssistant sets it, and it is not the default.
//
// # Why this is severe
//
// The transcript is PERFECT. The words below resolve cleanly through the real
// classifier when a confidence is present — TestT13_TheSameWordsResolveWhenA
// ConfidenceIsPresent proves exactly that on the same path. Only the absent
// confidence stands between the caller and their intent, and the call is
// terminated for it.

const (
	// t13Words is a transcript the EXISTING lexicon resolves cleanly.
	//
	// Chosen deliberately: if the cascade below fired on gibberish it would
	// prove nothing, because gibberish SHOULD fail. These words are known-good,
	// so everything that goes wrong is attributable to the confidence alone.
	t13Words = "transfer me to rajesh"

	// t13ResolvedIntent is what those words resolve to when a confidence is
	// present. Asserted by the control test, not assumed.
	t13ResolvedIntent = "request_transfer"

	// t13NoiseRounds is conversation's RepeatOnNoise default
	// (clarification.go:109). The third noise turn is the one that escalates.
	t13NoiseRounds = 2
)

// newUnknownConfidenceSTT reuses the EXISTING per-stream double and changes
// exactly one thing: the confidence it reports.
//
// Reuse is the point. t4STT already drives the real pipeline turn by turn, so
// building a second double would risk the cascade being a property of the new
// double rather than of the production path. speech.ConfidenceUnknown is the
// same representation whispercli.go:691 emits, so the pipeline receives exactly
// what a real recogniser gives it — without needing whisper, a model, a
// subprocess or a network.
func newUnknownConfidenceSTT(utterances ...string) *t4STT {
	p := newT4STT(utterances...)
	for i := range p.scripts {
		for j := range p.scripts[i] {
			p.scripts[i][j].Confidence = speech.ConfidenceUnknown
		}
	}
	return p
}

// t13Planned returns the Notes of every planner-driven transition, in order.
//
// conversation.Trace is public state and TransitionRecord.Note carries the
// plan's Reason (state.go:385), so the planner's decisions are observable
// without touching a private field or re-deriving them.
func t13Planned(conv *conversation.Conversation) []string {
	var out []string
	for _, tr := range conv.Trace() {
		if tr.Trigger == conversation.TriggerPlanned {
			out = append(out, tr.Note)
		}
	}
	return out
}

// t13Verdicts returns the Note of every utterance transition — the IntentEngine
// verdict for each caller turn (engine.go:774, verdict.String()).
func t13Verdicts(conv *conversation.Conversation) []string {
	var out []string
	for _, tr := range conv.Trace() {
		if tr.Trigger == conversation.TriggerUtterance {
			out = append(out, tr.Note)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// The pin
// ---------------------------------------------------------------------------

// TestT14_UnknownConfidenceNeverEntersTheNoiseCascade is the CONVERTED T13
// guard.
//
// # What it used to be
//
// Until the T14 amendment this test was named
// TestT13_UnknownConfidenceEscalatesTheCallOnTheThirdTurn, and it pinned the
// DEFECT: three turns of a perfectly recognised transcript produced
// noise_repeat, noise_repeat, noise_unrecoverable and terminated the call. It
// was written to fail the moment the policy changed, and it did. It is
// converted rather than deleted so the boundary it guards is never re-crossed
// silently.
//
// # What it guards now
//
// The approved semantics: a recogniser that reports NO confidence reaches
// normal intent classification. Not because its confidence is high — none was
// supplied, and none is fabricated — but because the acoustic gate has nothing
// to measure and therefore does not apply.
//
// The whole path is production: the real voice.Pipeline constructs the
// Utterance at pipeline.go:1072, the amended gate at intent.go:309 admits it,
// and the real classifier and planner decide what it means.
//
// The cascade must not merely be shorter — it must be ABSENT. A single
// noise_repeat anywhere, or any escalation, fails this test.
func TestT14_UnknownConfidenceNeverEntersTheNoiseCascade(t *testing.T) {
	stt := newUnknownConfidenceSTT(t13Words, t13Words, t13Words)
	r := newT2RigWith(t, nil, stt, "t14-no-cascade")

	// Three turns: one more than RepeatOnNoise, so if any noise ladder were
	// still being climbed this loop would reach its escalation rung.
	for i := 0; i < t13NoiseRounds+1; i++ {
		r.driveTurn(t, uint64(i*t2TurnFrames))
		r.obs.awaitTurn(t, 30*time.Second, fmt.Sprintf("turn %d", i+1))

		last, ok := r.conv.Context().Get(conversation.ScopeConversation, "last_intent")
		if !ok {
			t.Fatalf("turn %d: the engine recorded no intent at all", i+1)
		}
		// THE CLASSIFIER WAS CONSULTED. A resolved intent name can only come
		// from the classifier; the gate's own short-circuit returns
		// IntentUnknown without ever calling it (intent.go:309).
		if got := fmt.Sprintf("%v", last.Value); got != t13ResolvedIntent {
			t.Errorf("turn %d: intent = %q, want %q — an unknown confidence must "+
				"reach classification", i+1, got, t13ResolvedIntent)
		}
		if r.conv.State().IsTerminal() {
			t.Fatalf("turn %d: the conversation is terminal (%v); an unreported "+
				"confidence must not end a call", i+1, r.conv.State())
		}
	}

	// --- no turn was judged noise -------------------------------------------
	verdicts := t13Verdicts(r.conv)
	if len(verdicts) != t13NoiseRounds+1 {
		t.Fatalf("%d caller turns reached the engine, want %d: %v",
			len(verdicts), t13NoiseRounds+1, verdicts)
	}
	for i, v := range verdicts {
		if v == "noise" {
			t.Errorf("turn %d: verdict = noise — the acoustic gate rejected an "+
				"utterance whose confidence was never measured", i+1)
		}
		if v != "accept" {
			t.Errorf("turn %d: verdict = %q, want accept", i+1, v)
		}
	}

	// --- the plan sequence carries no trace of the ladder --------------------
	planned := t13Planned(r.conv)
	want := []string{"intent_accepted", "intent_accepted", "intent_accepted"}
	if len(planned) != len(want) {
		t.Fatalf("planner produced %v, want exactly %v", planned, want)
	}
	for i := range want {
		if planned[i] != want[i] {
			t.Errorf("plan %d = %q, want %q", i+1, planned[i], want[i])
		}
	}
	// Explicit, so a future regression names itself rather than showing up as a
	// generic mismatch.
	for _, p := range planned {
		if p == "noise_repeat" || p == "noise_unrecoverable" {
			t.Errorf("the noise ladder is still reachable: %v", planned)
		}
	}

	// --- the call is alive and the caller holds the floor --------------------
	if got := r.conv.State(); got != conversation.StateListening {
		t.Errorf("final state = %v, want listening", got)
	}
	if r.conv.State().IsTerminal() {
		t.Errorf("the conversation is terminal (%v)", r.conv.State())
	}
	if got := r.conv.Outcome(); got == conversation.OutcomeEscalated {
		t.Error("the conversation escalated")
	}
	if got := r.conv.Turns().Holder(); got != conversation.PartyCaller {
		t.Errorf("floor = %v, want caller", got)
	}

	t.Logf("APPROVED SEMANTICS: %d turns of %q reporting ConfidenceUnknown (%v) "+
		"produced verdicts=%v plans=%v, state=%v, floor=%v. No confidence value "+
		"was fabricated — the acoustic gate simply does not apply to a "+
		"measurement that was never taken.",
		len(verdicts), t13Words, speech.ConfidenceUnknown, verdicts, planned,
		r.conv.State(), r.conv.Turns().Holder())
}

// TestT14_UnknownConfidenceCompletesTheNormalResponseLifecycle is the CONVERTED
// T13 lifecycle guard.
//
// # What it used to be
//
// TestT13_TheCascadeLifecycleIsPinnedStageByStage measured what each pipeline
// stage did while the cascade ran: two clarification turns that each spent a
// real generation and a real synthesis telling the caller they could not be
// heard, then an escalation turn that skipped generation entirely. Converted,
// not deleted.
//
// # What it guards now
//
// The complete approved path, stage by stage, for every turn:
//
//	ConfidenceUnknown -> gate admits -> classifier consulted -> real intent
//	  -> plan -> generation -> TTS -> audio -> EventSpeechComplete
//	  -> floor returns to the caller -> conversation stays alive
//
// EVERY turn must now speak, including the third — under the old behaviour the
// third turn produced no audio at all because ActionEscalate does not speak
// (pipeline.go:1015-1022). That difference is the single clearest lifecycle
// signature of the fix, so it is asserted per turn rather than only in
// aggregate.
func TestT14_UnknownConfidenceCompletesTheNormalResponseLifecycle(t *testing.T) {
	stt := newUnknownConfidenceSTT(t13Words, t13Words, t13Words)
	r := newT2RigWith(t, nil, stt, "t14-lifecycle")

	type stage struct {
		generated   bool // a generation request was issued for this turn
		chunks      int  // clauses that reached the synthesiser, cumulative
		frames      int  // audio frames delivered to the caller, cumulative
		completions int  // EventSpeechComplete recorded by the frozen engine
		lastOutcome string
	}
	var seen []stage

	for i := 0; i < t13NoiseRounds+1; i++ {
		before, _ := r.sink.stats()
		_, chunksBefore, _ := r.obs.snapshot()
		reqBefore := r.model.LastRequest()

		r.driveTurn(t, uint64(i*t2TurnFrames))
		r.obs.awaitTurn(t, 30*time.Second, fmt.Sprintf("turn %d", i+1))

		frames, _ := r.sink.stats()
		_, chunks, outcomes := r.obs.snapshot()
		req := r.model.LastRequest()

		// "Generation was reached on THIS turn" means a request exists and it
		// is not simply the previous turn's request still sitting there.
		generated := req != nil && (reqBefore == nil || req.RequestID != reqBefore.RequestID)

		last := ""
		if len(outcomes) > 0 {
			last = outcomes[len(outcomes)-1]
		}
		seen = append(seen, stage{
			generated: generated, chunks: len(chunks), frames: frames,
			completions: completions(r.conv), lastOutcome: last,
		})

		// EVERY turn speaks now — no exception for the third.
		if !generated {
			t.Errorf("turn %d: no generation request was issued; an accepted "+
				"intent must reach the response path", i+1)
		}
		if len(chunks) <= len(chunksBefore) {
			t.Errorf("turn %d: no new clause reached the synthesiser", i+1)
		}
		if frames <= before {
			t.Errorf("turn %d: no new audio reached the caller", i+1)
		}

		// The caller's words reached generation verbatim — evidence the intent
		// was formed from the transcript rather than from a substituted value.
		if req != nil && len(req.Messages) == 1 && req.Messages[0].Content != t13Words {
			t.Errorf("turn %d: generation ran on %q, want the caller's words %q",
				i+1, req.Messages[0].Content, t13Words)
		}

		// EXACTLY-ONCE, checked per turn rather than only at the end: a
		// duplicated completion would otherwise be invisible until the total
		// happened to be wrong.
		if got := completions(r.conv); got != i+1 {
			t.Errorf("after turn %d there are %d speech completions, want %d",
				i+1, got, i+1)
		}
	}

	// --- every turn completed cleanly ---------------------------------------
	_, _, outcomes := r.obs.snapshot()
	if len(outcomes) != t13NoiseRounds+1 {
		t.Fatalf("pipeline recorded %d turn outcomes, want %d: %v",
			len(outcomes), t13NoiseRounds+1, outcomes)
	}
	wantOutcome := string(voice.OutcomeCompleted) + "/" + voice.ReasonOK
	for i, got := range outcomes {
		if got != wantOutcome {
			t.Errorf("turn %d: pipeline outcome = %q, want %q", i+1, got, wantOutcome)
		}
	}
	if got := abandonments(r.conv); got != 0 {
		t.Errorf("%d response abandonments, want 0", got)
	}
	if got := len(r.conv.Interruptions().History()); got != 0 {
		t.Errorf("%d interruptions, want 0", got)
	}
	if got := completions(r.conv); got != t13NoiseRounds+1 {
		t.Errorf("%d speech completions, want exactly %d — one per turn, no "+
			"duplicates", got, t13NoiseRounds+1)
	}
	if got := r.conv.Turns().Holder(); got != conversation.PartyCaller {
		t.Errorf("floor = %v, want caller: EventSpeechComplete must hand it back", got)
	}
	if got := r.conv.State(); got != conversation.StateListening {
		t.Errorf("state = %v, want listening", got)
	}

	// --- the session is healthy while the conversation is over ---------------
	//
	// The split matters. voice.SessionFSM is not terminal — the transport would
	// happily carry more audio — while the frozen conversation has escalated
	// --- session and conversation now AGREE ---------------------------------
	//
	// Under the old behaviour these disagreed: the transport stayed open while
	// the conversation had escalated and refused further audio. Both being
	// non-terminal is what makes the call actually usable.
	if r.fsm.State().Terminal() {
		t.Errorf("the voice session is terminal (%v)", r.fsm.State())
	}
	if r.conv.State().IsTerminal() {
		t.Errorf("the conversation is terminal (%v) after three accepted turns",
			r.conv.State())
	}

	t.Logf("APPROVED LIFECYCLE: turn1=%+v turn2=%+v turn3=%+v | conversation=%v "+
		"session=%v floor=%v. EVERY turn now reaches generation, TTS and audio "+
		"and completes exactly once; under the previous behaviour the third "+
		"produced no audio at all and ended the call.",
		seen[0], seen[1], seen[2], r.conv.State(), r.fsm.State(),
		r.conv.Turns().Holder())
}

// TestT13_TheSameWordsResolveWhenAConfidenceIsPresent is the control, and it is
// what makes the pin non-vacuous.
//
// Identical rig, identical transcript, identical path. The ONLY difference is
// that the recogniser reports a confidence. If this resolves and the test above
// escalates, then the confidence — and nothing about the words, the lexicon,
// the persona or the pipeline — is the sole cause.
//
// Without this control the cascade test would still pass if the transcript were
// simply unclassifiable, which would be correct behaviour wrongly pinned as a
// defect.
func TestT13_TheSameWordsResolveWhenAConfidenceIsPresent(t *testing.T) {
	// newT4STT's own default confidence (0.94), untouched.
	stt := newT4STT(t13Words)
	r := newT2RigWith(t, nil, stt, "t13-control")

	r.driveTurn(t, 0)
	r.obs.awaitTurn(t, 30*time.Second, "the control turn")

	last, ok := r.conv.Context().Get(conversation.ScopeConversation, "last_intent")
	if !ok {
		t.Fatal("the control turn recorded no intent")
	}
	if got := fmt.Sprintf("%v", last.Value); got != t13ResolvedIntent {
		t.Fatalf("intent = %q, want %q — if these words no longer resolve, the "+
			"cascade test is no longer evidence about CONFIDENCE and both "+
			"tests must be re-examined", got, t13ResolvedIntent)
	}
	if planned := t13Planned(r.conv); len(planned) != 1 || planned[0] != "intent_accepted" {
		t.Errorf("plans = %v, want exactly [intent_accepted]", planned)
	}
	if got := t13Verdicts(r.conv); len(got) != 1 || got[0] != "accept" {
		t.Errorf("verdicts = %v, want exactly [accept]", got)
	}
	if r.conv.State().IsTerminal() {
		t.Errorf("the control conversation is terminal (%v); a resolved intent "+
			"must not end the call", r.conv.State())
	}

	t.Logf("CONTROL: the same words %q with a reported confidence resolve to "+
		"%q and the call continues. The cascade is caused by the ABSENT "+
		"CONFIDENCE alone.", t13Words, t13ResolvedIntent)
}

// TestT14_TheConfidenceMatrix is the whole approved policy in one table.
//
// # The invariant it exists to protect
//
// UNKNOWN CONFIDENCE IS NOT LOW CONFIDENCE. Everything else here is the
// unchanged threshold behaviour, asserted alongside it so that the two can
// never be conflated again — which is exactly the defect T13 pinned.
//
// MinASRConfidence is the repository's real value, 0.40 (intent.go:209), read
// from DefaultIntentConfig rather than written as a literal, so the table
// cannot drift away from the configuration it claims to describe.
//
// The rows straddling the threshold (0.39 / 0.40) are what prove the amendment
// did not weaken the gate: the >= 0 guard admits only values OUTSIDE the
// documented domain, and every value inside it compares exactly as before.
func TestT14_TheConfidenceMatrix(t *testing.T) {
	t.Parallel()

	min := conversation.DefaultIntentConfig().MinASRConfidence
	if min != 0.40 {
		t.Fatalf("MinASRConfidence = %v, want 0.40 — this table was written "+
			"against 0.40 and must be re-derived if the threshold changes", min)
	}

	for _, tc := range []struct {
		name       string
		confidence float64
		wantNoise  bool
	}{
		// --- MEASURED confidences: behaviour is unchanged by T14 -------------
		{"well below the threshold", 0.20, true},
		{"just below the threshold", 0.39, true},
		{"exactly at the threshold", 0.40, false},
		{"comfortably above", 0.80, false},
		{"genuinely unintelligible", 0.10, true},
		{"clearly intelligible", 0.94, false},

		// --- NO measurement: the acoustic gate does not apply ----------------
		//
		// Both rows matter. The first is the sentinel a real provider reports;
		// the second proves the contract is the SIGN, not one magic constant,
		// so conversation needs no dependency on speech.
		{"provider reports nothing", speech.ConfidenceUnknown, false},
		{"any negative value", -0.5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, vi := build(t)
			p, conv, err := openPlannerRaw(t, vi,
				conversation.ConversationID("t13-gate-"+tc.name))
			if err != nil {
				t.Fatalf("Planner: %v", err)
			}
			for _, e := range []conversation.Event{
				{Kind: conversation.EventStart},
				{Kind: conversation.EventGreetingComplete},
			} {
				if _, err := p.Handle(e); err != nil {
					t.Fatalf("opening the floor: %v", err)
				}
			}

			plan, err := p.Handle(conversation.Event{
				Kind:  conversation.EventUtterance,
				Party: conversation.PartyCaller,
				Utterance: conversation.Utterance{
					Text: t13Words, ASRConfidence: tc.confidence,
				},
			})
			if err != nil {
				t.Fatalf("utterance: %v", err)
			}

			verdicts := t13Verdicts(conv)
			if len(verdicts) != 1 {
				t.Fatalf("verdicts = %v, want exactly one", verdicts)
			}
			gotNoise := verdicts[0] == "noise"
			if gotNoise != tc.wantNoise {
				t.Errorf("ASRConfidence %v produced verdict %q (noise=%v), want noise=%v",
					tc.confidence, verdicts[0], gotNoise, tc.wantNoise)
			}

			// NOT-NOISE MUST MEAN CLASSIFIED, not merely "did not trip the
			// gate". Asserting the resolved intent is what distinguishes the
			// two: only the classifier can produce this name, so reaching it
			// proves the utterance travelled the normal path rather than
			// landing in some other non-noise branch.
			if !tc.wantNoise {
				if string(plan.Intent) != t13ResolvedIntent {
					t.Errorf("ASRConfidence %v: intent = %q, want %q — the "+
						"classifier was not consulted",
						tc.confidence, plan.Intent, t13ResolvedIntent)
				}
				if plan.Reason != "intent_accepted" {
					t.Errorf("ASRConfidence %v: reason = %q, want intent_accepted",
						tc.confidence, plan.Reason)
				}
			}
		})
	}

	t.Log("APPROVED POLICY: a NEGATIVE ASRConfidence means no measurement was " +
		"taken and the acoustic gate does not apply; every value in the " +
		"documented 0..1 domain is compared against MinASRConfidence exactly " +
		"as before. No confidence value is fabricated anywhere.")
}
