package main

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
)

// Phase 16 T1 — CLARIFICATION RESPONSE PATH.
//
// Phase 15 measured GAP B: the planner decided to ask, clarify or confirm, and
// the pipeline spoke nothing. Root cause was a single guard —
// `plan.Action != ActionRespond` — gating the entire speaking path, even though
// everything after it is action-agnostic and never reads plan.Action again.
//
// T1 widens that guard to the four actions that genuinely speak. It creates no
// second pipeline: governance, generation, chunking, TTS, delivery and
// EventSpeechComplete are the SAME path ActionRespond already used.
//
// EventResponseAbandoned is NOT used for these actions when they succeed. It
// remains what ADR-0020 defined it as: a response that was going to be spoken
// and then was not.

// t1Case is one speaking action reached through a real utterance, with the
// plan the frozen planner actually produces for it.
type t1Case struct {
	name       string
	utterance  string
	wantAction conversation.Action
	wantReason string
}

// t1Cases covers the three actions T1 exists for. Each utterance and expected
// plan was measured in Phase 14 T11's acceptance matrix, not guessed.
func t1Cases() []t1Case {
	return []t1Case{
		{
			// A required slot is missing, so the planner asks for it.
			name: "ask", utterance: "i want to leave a message",
			wantAction: conversation.ActionAsk, wantReason: "clarify_missing_slot",
		},
		{
			// Two candidates within the ambiguity margin.
			name: "clarify", utterance: "hold on call back",
			wantAction: conversation.ActionClarify, wantReason: "clarify_ambiguous",
		},
		{
			// Confidence in the clarify band, so the planner confirms.
			name: "confirm", utterance: "repeat pardon",
			wantAction: conversation.ActionConfirm, wantReason: "clarify_low_confidence",
		},
	}
}

// planFor drives one utterance through a bare conversation to observe the plan
// the frozen planner produces, independently of the pipeline.
func planFor(t *testing.T, utterance string, id string) conversation.Plan {
	t.Helper()
	_, vi := build(t)
	p, _, err := openPlanner(vi, conversation.ConversationID(id))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	plan, err := p.Handle(utter(utterance))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return plan
}

// ---------------------------------------------------------------------------
// The plans really are the three speaking actions
// ---------------------------------------------------------------------------

// TestT1_FixturesProduceTheIntendedSpeakingActions pins the premise. If the
// lexicon or thresholds ever move, this fails first and names the change,
// instead of the pipeline tests failing for a reason that looks like a
// pipeline defect.
func TestT1_FixturesProduceTheIntendedSpeakingActions(t *testing.T) {
	for _, c := range t1Cases() {
		t.Run(c.name, func(t *testing.T) {
			plan := planFor(t, c.utterance, "t1-premise-"+c.name)
			if plan.Action != c.wantAction {
				t.Errorf("action = %v, want %v", plan.Action, c.wantAction)
			}
			if plan.Reason != c.wantReason {
				t.Errorf("reason = %q, want %q", plan.Reason, c.wantReason)
			}
			if plan.NextState != conversation.StateSpeaking {
				t.Errorf("next state = %v, want speaking — these actions are "+
					"agent speech", plan.NextState)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Each speaking action now traverses the whole response path
// ---------------------------------------------------------------------------

// TestT1_SpeakingActionsReachGenerationAndAudio is the core evidence: each of
// the three actions now reaches generation, synthesis and output, completes
// exactly once, and hands the floor back.
func TestT1_SpeakingActionsReachGenerationAndAudio(t *testing.T) {
	for _, c := range t1Cases() {
		t.Run(c.name, func(t *testing.T) {
			r := newT2Rig(t, t2Segments(c.utterance), "t1-speak-"+c.name)
			r.driveTurn(t, 0)
			r.obs.awaitTurn(t, 30*time.Second, c.name)

			// generation ran
			if r.model.Calls() == 0 {
				t.Error("the generator was never called for a speaking action")
			}
			// synthesis ran
			if r.tts.Opened() != 1 {
				t.Errorf("TTS opened %d times, want 1", r.tts.Opened())
			}
			// audio was delivered
			frames, bytes := r.sink.stats()
			if frames == 0 || bytes == 0 {
				t.Errorf("no audio delivered (%d frames, %d bytes)", frames, bytes)
			}
			// a response chunk reached synthesis, with the generator's own text
			_, chunks, outcomes := r.obs.snapshot()
			if len(chunks) == 0 {
				t.Fatal("no response chunk reached synthesis")
			}
			if chunks[0].Text != "ok" {
				t.Errorf("chunk = %q, want the generator's %q", chunks[0].Text, "ok")
			}
			// exactly one completion, no abandonment
			if got := completions(r.conv); got != 1 {
				t.Errorf("%d speech completions, want exactly 1", got)
			}
			if got := abandonments(r.conv); got != 0 {
				t.Errorf("%d abandonments on a successfully spoken %s, want 0",
					got, c.name)
			}
			// floor handed back, session healthy
			if got := r.conv.Turns().Holder(); got != conversation.PartyCaller {
				t.Errorf("floor = %v after speaking, want caller", got)
			}
			if r.conv.State().IsTerminal() {
				t.Error("the conversation terminated")
			}
			if len(outcomes) == 0 || outcomes[0] != "completed/ok" {
				t.Errorf("turn outcomes = %v, want [completed/ok]", outcomes)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Multi-turn: the caller's answer is finally heard
// ---------------------------------------------------------------------------

// TestT1_ClarificationDialogueContinues is the behaviour GAP B destroyed: the
// agent asks, the caller answers, and the answer is CLASSIFIED rather than
// ignored as a backchannel.
func TestT1_ClarificationDialogueContinues(t *testing.T) {
	dialogues := []struct {
		name       string
		question   string // caller's first utterance -> a question from the agent
		answer     string // caller's reply
		wantIntent conversation.IntentName
	}{
		{"ask_then_answer", "i want to leave a message", "transfer me to rajesh",
			"request_transfer"},
		{"clarify_then_answer", "hold on call back", "say that again", "repeat"},
	}

	for _, d := range dialogues {
		t.Run(d.name, func(t *testing.T) {
			stt := newT4STT(d.question, d.answer)
			r := newT2RigWith(t, nil, stt, "t1-dialogue-"+d.name)

			// Turn 1: the agent asks, aloud.
			r.driveTurn(t, 0)
			r.obs.awaitTurn(t, 30*time.Second, "question")
			if frames, _ := r.sink.stats(); frames == 0 {
				t.Fatal("the question was never spoken")
			}
			if got := completions(r.conv); got != 1 {
				t.Fatalf("%d completions after the question, want 1", got)
			}
			if got := r.conv.Turns().Holder(); got != conversation.PartyCaller {
				t.Fatalf("floor = %v after the question, want caller", got)
			}

			// Turn 2: the caller answers and is HEARD.
			r.driveTurn(t, t2TurnFrames)
			r.obs.awaitTurn(t, 30*time.Second, "answer")

			last, ok := r.conv.Context().Get(conversation.ScopeConversation, "last_intent")
			if !ok {
				t.Fatal("the answer produced no intent")
			}
			if got := conversation.IntentName(fmt.Sprintf("%v", last.Value)); got != d.wantIntent {
				t.Errorf("answer intent = %q, want %q — if this is the question's "+
					"intent, the answer was ignored", got, d.wantIntent)
			}
			if got := completions(r.conv); got != 2 {
				t.Errorf("%d completions after question + answer, want 2", got)
			}
			if got := abandonments(r.conv); got != 0 {
				t.Errorf("%d abandonments in a healthy dialogue, want 0", got)
			}
		})
	}
}

// TestT1_ConfirmationDialogueContinues covers the confirm action specifically,
// including the yes/no expectation it establishes.
func TestT1_ConfirmationDialogueContinues(t *testing.T) {
	stt := newT4STT("repeat pardon", "yes")
	r := newT2RigWith(t, nil, stt, "t1-confirm-dialogue")

	r.driveTurn(t, 0)
	r.obs.awaitTurn(t, 30*time.Second, "confirmation")
	if frames, _ := r.sink.stats(); frames == 0 {
		t.Fatal("the confirmation was never spoken")
	}
	if got := completions(r.conv); got != 1 {
		t.Fatalf("%d completions after the confirmation, want 1", got)
	}
	// The confirmation established a yes/no expectation for the next turn.
	if got := r.conv.Turns().LastExpectation(); got != conversation.ExpectYesNo {
		t.Errorf("expectation after a confirmation = %v, want yes_no", got)
	}

	// "yes" is only meaningful inside that expectation.
	r.driveTurn(t, t2TurnFrames)
	r.obs.awaitTurn(t, 30*time.Second, "affirmation")

	last, _ := r.conv.Context().Get(conversation.ScopeConversation, "last_intent")
	if got := fmt.Sprintf("%v", last.Value); got != "affirm" {
		t.Errorf("answer intent = %q, want affirm", got)
	}
	if got := completions(r.conv); got != 2 {
		t.Errorf("%d completions after confirm + answer, want 2", got)
	}
}

// ---------------------------------------------------------------------------
// Failure matrix — every action against every failure
// ---------------------------------------------------------------------------

// TestT1_FailureMatrix runs all three speaking actions against every failure
// mode and asserts the ADR-0020 contract holds for each.
func TestT1_FailureMatrix(t *testing.T) {
	failures := []struct {
		name    string
		break_  func(*t2Rig)
		denying bool
	}{
		{"generation_failure", func(r *t2Rig) {
			r.model.FailNext(1, errors.New("scripted generation failure"))
		}, false},
		{"tts_failure", func(r *t2Rig) {
			r.tts.FailNext(errors.New("scripted synthesiser failure"))
		}, false},
		{"governance_denial", func(*t2Rig) {}, true},
	}

	for _, c := range t1Cases() {
		for _, f := range failures {
			t.Run(c.name+"/"+f.name, func(t *testing.T) {
				id := "t1-fm-" + c.name + "-" + f.name
				var r *t2Rig
				if f.denying {
					r = newT2RigDenying(t, t2Segments(c.utterance), id)
				} else {
					r = newT2Rig(t, t2Segments(c.utterance), id)
				}
				f.break_(r)
				r.driveTurn(t, 0)
				r.obs.awaitTurn(t, 30*time.Second, c.name+"/"+f.name)

				comp, aband := completions(r.conv), abandonments(r.conv)
				if comp != 0 {
					t.Errorf("%d speech completions although nothing was spoken, want 0",
						comp)
				}
				if aband != 1 {
					t.Errorf("%d abandonments, want exactly 1", aband)
				}
				// THE INVARIANT.
				if comp > 0 && aband > 0 {
					t.Error("this turn produced BOTH a completion and an abandonment")
				}
				if got := r.conv.Turns().Holder(); got != conversation.PartyCaller {
					t.Errorf("floor = %v after a failed %s, want caller", got, c.name)
				}
				if r.conv.State().IsTerminal() {
					t.Error("a failed question terminated the conversation")
				}
				if frames, _ := r.sink.stats(); frames != 0 {
					t.Errorf("%d frames delivered on a failed turn", frames)
				}
				if got := len(r.conv.Interruptions().History()); got != 0 {
					t.Errorf("%d interruption records on a failed turn, want 0", got)
				}
			})
		}
	}
}

// ---------------------------------------------------------------------------
// Governance is still consulted
// ---------------------------------------------------------------------------

// TestT1_SpeakingActionsStillPassThroughGovernance proves the widened guard did
// not route the three actions around authorisation: a denying engine stops them
// exactly as it stops ActionRespond.
func TestT1_SpeakingActionsStillPassThroughGovernance(t *testing.T) {
	for _, c := range t1Cases() {
		t.Run(c.name, func(t *testing.T) {
			r := newT2RigDenying(t, t2Segments(c.utterance), "t1-gov-"+c.name)
			r.driveTurn(t, 0)
			r.obs.awaitTurn(t, 30*time.Second, c.name)

			_, _, outcomes := r.obs.snapshot()
			if len(outcomes) == 0 || !contains(outcomes[0], "denied") {
				t.Fatalf("outcomes %v record no denial; governance was not "+
					"consulted for %s", outcomes, c.name)
			}
			// Denied means nothing was generated or spoken.
			if r.model.Calls() != 0 {
				t.Errorf("the generator ran %d times despite governance denying",
					r.model.Calls())
			}
			if frames, _ := r.sink.stats(); frames != 0 {
				t.Errorf("%d frames delivered despite governance denying", frames)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Non-speaking actions are unchanged
// ---------------------------------------------------------------------------

// TestT1_NonSpeakingActionsStillEndQuietly is the regression guard on the other
// side of the widened predicate: escalation must still not be spoken for.
func TestT1_NonSpeakingActionsStillEndQuietly(t *testing.T) {
	// "callback transfer" scores below the reject threshold, which the planner
	// escalates — measured in Phase 14 T7.
	plan := planFor(t, "callback transfer", "t1-nonspeak-premise")
	if plan.Action != conversation.ActionEscalate {
		t.Fatalf("premise: action = %v, want escalate; this fixture no longer "+
			"reaches the non-speaking path", plan.Action)
	}

	r := newT2Rig(t, t2Segments("callback transfer"), "t1-nonspeak")
	r.driveTurn(t, 0)
	r.obs.awaitTurn(t, 30*time.Second, "escalate")

	if r.model.Calls() != 0 {
		t.Errorf("the generator ran %d times for an escalation; this pipeline "+
			"must not speak for the layer above", r.model.Calls())
	}
	if frames, _ := r.sink.stats(); frames != 0 {
		t.Errorf("%d frames delivered for an escalation", frames)
	}
	if got := completions(r.conv); got != 0 {
		t.Errorf("%d completions for an escalation, want 0", got)
	}
	if got := abandonments(r.conv); got != 0 {
		t.Errorf("%d abandonments for an escalation, want 0 — nothing was "+
			"abandoned, the plan simply does not speak", got)
	}
}

// ---------------------------------------------------------------------------
// Determinism
// ---------------------------------------------------------------------------

// TestT1_ClarificationSignatureIsStable replays the three speaking actions on
// fresh rigs and requires an identical semantic signature.
func TestT1_ClarificationSignatureIsStable(t *testing.T) {
	sig := func(pass int) string {
		out := ""
		for _, c := range t1Cases() {
			r := newT2Rig(t, t2Segments(c.utterance),
				fmt.Sprintf("t1-det-%d-%s", pass, c.name))
			r.driveTurn(t, 0)
			r.obs.awaitTurn(t, 30*time.Second, c.name)
			frames, bytes := r.sink.stats()
			_, chunks, outcomes := r.obs.snapshot()
			out += fmt.Sprintf("%s=comp:%d/aband:%d/frames:%d/bytes:%d/chunks:%d/"+
				"holder:%v/state:%v/outcomes:%v|",
				c.name, completions(r.conv), abandonments(r.conv), frames, bytes,
				len(chunks), r.conv.Turns().Holder(), r.conv.State(), outcomes)
		}
		return out
	}
	want := sig(0)
	for i := 1; i <= 3; i++ {
		if got := sig(i); got != want {
			t.Fatalf("pass %d drifted\nwant: %s\n got: %s", i, want, got)
		}
	}
	t.Logf("stable signature: %s", want)
}
