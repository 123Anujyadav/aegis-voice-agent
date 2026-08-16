package evalsubjects

import (
	"context"
	"fmt"
	"time"

	ev "github.com/callscreen/callscreen-platform/packages/go/evaluation"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// RuntimeSubject evaluates the Phase 10A runtime core.
//
// It exercises the three pieces every later phase depends on: the circuit
// breaker, the context window and the state machine. Those were chosen because
// they are the ones whose behaviour other phases ASSUME — a breaker that stops
// opening or an eviction policy that changes would break four subsystems
// silently, and nothing else in the platform would notice.
type RuntimeSubject struct{}

// NewRuntimeSubject builds the adapter.
func NewRuntimeSubject() *RuntimeSubject { return &RuntimeSubject{} }

// Name identifies the subject.
func (r *RuntimeSubject) Name() ev.SubjectName { return SubjectRuntime }

// Capabilities lists what it can be asked to do.
//
// It declares NO injection capabilities. The runtime core has no downstream to
// fail, so a failure scenario against it would be a scenario the adapter had to
// fake — and a faked injection tests the fake. Scenarios injecting faults here
// are skipped, with the missing capability named.
func (r *RuntimeSubject) Capabilities() []ev.Capability {
	return []ev.Capability{"breaker", "context_window", "fsm", "clock"}
}

// Open starts a session.
func (r *RuntimeSubject) Open(ctx context.Context, spec ev.SessionSpec) (ev.Session, error) {
	clock := rt.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	cfg := rt.DefaultBreakerConfig()
	if n := spec.Params.Num("failure_threshold"); n > 0 {
		cfg.FailureThreshold = int(n)
	}
	if n := spec.Params.Num("minimum_requests"); n > 0 {
		cfg.MinimumRequests = int(n)
	}
	breaker, err := rt.NewBreaker("evaluated", cfg, clock)
	if err != nil {
		return nil, fmt.Errorf("breaker: %w", err)
	}

	budget := 200
	if n := spec.Params.Num("window_budget"); n > 0 {
		budget = int(n)
	}

	return &runtimeSession{
		clock:   clock,
		breaker: breaker,
		window:  rt.NewContextWindow(budget, nil),
	}, nil
}

type runtimeSession struct {
	clock   *rt.FakeClock
	breaker *rt.Breaker
	window  *rt.ContextWindow
	events  []ev.EventRecord
	appends int
}

// Advance moves the session's clock, satisfying the optional interface the
// platform looks for. Only the adapter knows which clock a step means.
func (s *runtimeSession) Advance(d time.Duration) { s.clock.Advance(d) }

func (s *runtimeSession) Execute(ctx context.Context, step ev.Step) ev.StepResult {
	switch step.Op {
	case "breaker_allow":
		allowed, report := s.breaker.Allow()
		if !allowed {
			return result("circuit_open", ev.Values{"state": ev.S(s.breaker.State().String())})
		}
		// The scenario says whether this attempt succeeded, which is what
		// makes a breaker scenario a program rather than a coin toss.
		if step.Args.Get("fail").Kind() == ev.ValueBool {
			if f, _ := step.Args.Get("fail").Flag(); f {
				report(fmt.Errorf("scripted failure"))
				s.record("breaker_failure")
				return result("reported_failure", ev.Values{
					"state": ev.S(s.breaker.State().String())})
			}
		}
		report(nil)
		s.record("breaker_success")
		return result(ok, ev.Values{"state": ev.S(s.breaker.State().String())})

	case "breaker_state":
		return result(ok, ev.Values{"state": ev.S(s.breaker.State().String())})

	case "breaker_reset":
		s.breaker.Reset()
		return result(ok, ev.Values{"state": ev.S(s.breaker.State().String())})

	case "window_append":
		text := step.Args.Str("text")
		if text == "" {
			text = "an evaluated message of moderate length"
		}
		msg := rt.Message{Role: rt.RoleUser, Content: text}
		if step.Args.Str("role") == "assistant" {
			msg.Role = rt.RoleAssistant
		}
		if pinned, _ := step.Args.Get("pinned").Flag(); pinned {
			msg.Pinned = true
		}
		if err := s.window.Append(msg); err != nil {
			return result("rejected", ev.Values{"error": ev.S(err.Error())})
		}
		s.appends++
		s.record("window_append")
		return result(ok, ev.Values{
			"used":    ev.N(float64(s.window.Used())),
			"len":     ev.N(float64(s.window.Len())),
			"evicted": ev.N(float64(s.window.EvictedCount())),
		})

	case "window_assemble":
		max := int(step.Args.Num("max_tokens"))
		if max <= 0 {
			max = s.window.Budget()
		}
		msgs, err := s.window.Assemble(max)
		if err != nil {
			return failed("assemble_error", err.Error())
		}
		return result(ok, ev.Values{
			"messages": ev.N(float64(len(msgs))),
			"used":     ev.N(float64(s.window.Used())),
		})

	case "window_clear":
		keepPinned, _ := step.Args.Get("keep_pinned").Flag()
		s.window.Clear(keepPinned)
		return result(ok, ev.Values{"len": ev.N(float64(s.window.Len()))})

	case "clock_now":
		// Reported as an offset from the session's start rather than as an
		// absolute instant, so it is stable across runs and can therefore live
		// in a behaviour fingerprint at all.
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		return result(ok, ev.Values{
			"elapsed_ms": ev.N(float64(s.clock.Now().Sub(base).Milliseconds()))})

	default:
		return failed("unknown_op", "runtime subject has no operation "+step.Op)
	}
}

func (s *runtimeSession) record(kind string) {
	s.events = append(s.events, ev.EventRecord{Type: kind})
}

func (s *runtimeSession) State() ev.Values {
	return ev.Values{
		"breaker_state":  ev.S(s.breaker.State().String()),
		"window_used":    ev.N(float64(s.window.Used())),
		"window_len":     ev.N(float64(s.window.Len())),
		"window_evicted": ev.N(float64(s.window.EvictedCount())),
		"appends":        ev.N(float64(s.appends)),
	}
}

func (s *runtimeSession) Events() []ev.EventRecord {
	out := s.events
	s.events = nil
	return out
}

func (s *runtimeSession) Close() error { return nil }
