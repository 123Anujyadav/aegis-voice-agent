package evalsubjects

import (
	"context"
	"time"

	conv "github.com/callscreen/callscreen-platform/packages/go/conversation"
	ev "github.com/callscreen/callscreen-platform/packages/go/evaluation"
)

// ConversationSubject evaluates the Phase 10B conversation intelligence engine.
//
// It drives the engine through the Simulator the phase exported, which is the
// only way to exercise a dialogue without inventing a caller. What it observes
// is the STATE MACHINE and the PLANNER — the conversation's state after each
// turn, the action the planner chose, and the reason it gave.
//
// It deliberately does NOT observe the text of anything. A golden containing
// what somebody said would be a permanent record of a conversation, and the
// platform's whole approach to observations is that they carry codes and
// fingerprints.
type ConversationSubject struct{}

// NewConversationSubject builds the adapter.
func NewConversationSubject() *ConversationSubject { return &ConversationSubject{} }

// Name identifies the subject.
func (c *ConversationSubject) Name() ev.SubjectName { return SubjectConversation }

// Capabilities lists what it can be asked to do.
func (c *ConversationSubject) Capabilities() []ev.Capability {
	return []ev.Capability{
		"begin", "say", "plan", "state", "clock",
		ev.InjectionCapability(ev.FailCancellation),
	}
}

// Open starts a session with its own conversation engine.
func (c *ConversationSubject) Open(ctx context.Context, spec ev.SessionSpec) (ev.Session, error) {
	h, err := conv.NewHarness()
	if err != nil {
		return nil, err
	}
	return &conversationSession{h: h}, nil
}

type conversationSession struct {
	h      *conv.Harness
	conv   *conv.Conversation
	sim    *conv.Simulator
	events []ev.EventRecord
	turns  int
}

// Advance moves the conversation engine's clock.
func (s *conversationSession) Advance(d time.Duration) { s.h.Clock.Advance(d) }

func (s *conversationSession) Execute(ctx context.Context, step ev.Step) ev.StepResult {
	switch step.Op {
	case "begin":
		id := step.Args.Str("id")
		if id == "" {
			id = "evaluated-conversation"
		}
		c, err := s.h.Begin(conv.ConversationID(id))
		if err != nil {
			return failed("begin_error", err.Error())
		}
		s.conv = c
		s.sim = conv.NewSimulator(c, s.h.Clock)
		s.record("begun")
		return result(ok, ev.Values{"state": ev.S(c.State().String())})

	case "start":
		if s.sim == nil {
			return failed("no_conversation", "begin must precede start")
		}
		s.sim.Start()
		return result(ok, ev.Values{"state": ev.S(s.conv.State().String())})

	case "say":
		if s.sim == nil {
			return failed("no_conversation", "begin must precede say")
		}
		text := step.Args.Str("text")
		if text == "" {
			text = "an evaluated utterance"
		}
		s.sim.Say(text)
		s.turns++
		s.record("turn")

		plan := s.sim.LastPlan()
		// THE ACTION AND THE REASON, NEVER THE TEXT. Both are bounded codes
		// the engine itself defines, and both are exactly what a golden should
		// hold: a conversation that starts clarifying where it used to answer
		// has changed, and that is visible here without recording a word
		// anybody said.
		return result(ok, ev.Values{
			"state":      ev.S(s.conv.State().String()),
			"action":     ev.S(plan.Action.String()),
			"reason":     ev.S(plan.Reason),
			"next_state": ev.S(plan.NextState.String()),
			"turns":      ev.N(float64(s.turns)),
		})

	case "reply":
		if s.sim == nil {
			return failed("no_conversation", "begin must precede reply")
		}
		s.sim.Reply()
		return result(ok, ev.Values{"state": ev.S(s.conv.State().String())})

	case "state":
		if s.conv == nil {
			return failed("no_conversation", "begin must precede state")
		}
		return result(ok, ev.Values{
			"state":   ev.S(s.conv.State().String()),
			"outcome": ev.S(string(s.conv.Outcome())),
			"idle":    ev.B(s.conv.Idle()),
			"trace":   ev.N(float64(len(s.conv.Trace()))),
		})

	case "escalate":
		if s.conv == nil {
			return failed("no_conversation", "begin must precede escalate")
		}
		if err := s.conv.Escalate("evaluation"); err != nil {
			return result("escalate_refused", ev.Values{
				"state": ev.S(s.conv.State().String())})
		}
		s.record("escalated")
		return result(ok, ev.Values{"state": ev.S(s.conv.State().String())})

	case "end":
		if s.conv == nil {
			return failed("no_conversation", "begin must precede end")
		}
		if err := s.conv.End("evaluation"); err != nil {
			return result("end_refused", ev.Values{
				"state": ev.S(s.conv.State().String())})
		}
		s.record("ended")
		return result(ok, ev.Values{
			"state":   ev.S(s.conv.State().String()),
			"outcome": ev.S(string(s.conv.Outcome())),
		})

	default:
		return failed("unknown_op", "conversation subject has no operation "+step.Op)
	}
}

func (s *conversationSession) record(kind string) {
	s.events = append(s.events, ev.EventRecord{Type: kind})
}

func (s *conversationSession) State() ev.Values {
	if s.conv == nil {
		return ev.Values{"state": ev.S("<none>"), "turns": ev.N(0)}
	}
	return ev.Values{
		"state":   ev.S(s.conv.State().String()),
		"outcome": ev.S(string(s.conv.Outcome())),
		"turns":   ev.N(float64(s.turns)),
	}
}

func (s *conversationSession) Events() []ev.EventRecord {
	out := s.events
	s.events = nil
	return out
}

func (s *conversationSession) Close() error { return nil }
