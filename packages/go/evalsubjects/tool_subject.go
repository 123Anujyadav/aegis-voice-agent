package evalsubjects

import (
	"context"
	"errors"
	"time"

	ev "github.com/callscreen/callscreen-platform/packages/go/evaluation"
	tr "github.com/callscreen/callscreen-platform/packages/go/toolruntime"
)

// ToolSubject evaluates the Phase 10D tool calling runtime.
//
// It registers scripted tools and executes intents against them, which is the
// only honest way to evaluate a runtime whose whole design refuses to contain a
// real adapter: the platform evaluates the RUNTIME — planning, permission,
// idempotency, retry, compensation — and the tools are deliberately fake on both
// sides of the boundary.
type ToolSubject struct{}

// NewToolSubject builds the adapter.
func NewToolSubject() *ToolSubject { return &ToolSubject{} }

// Name identifies the subject.
func (t *ToolSubject) Name() ev.SubjectName { return SubjectTool }

// Capabilities lists what it can be asked to do.
func (t *ToolSubject) Capabilities() []ev.Capability {
	return []ev.Capability{
		"register", "execute", "plan", "idempotency", "compensation", "clock",
		ev.InjectionCapability(ev.FailTool),
		ev.InjectionCapability(ev.FailPermission),
	}
}

// Open starts a session with its own tool runtime.
func (t *ToolSubject) Open(ctx context.Context, spec ev.SessionSpec) (ev.Session, error) {
	h, err := tr.NewHarness()
	if err != nil {
		return nil, err
	}
	return &toolSession{h: h, tools: map[string]*tr.FakeTool{}}, nil
}

type toolSession struct {
	h      *tr.Harness
	tools  map[string]*tr.FakeTool
	writes map[string]*tr.CompensatingFake
	events []ev.EventRecord
	execs  int
}

// Advance moves the tool runtime's clock.
func (s *toolSession) Advance(d time.Duration) { s.h.Clock.Advance(d) }

func (s *toolSession) Execute(ctx context.Context, step ev.Step) ev.StepResult {
	capability := step.Args.Str("capability")
	if capability == "" {
		capability = "lookup"
	}

	switch step.Op {
	case "register_read":
		tool := &tr.FakeTool{}
		// A tool-failure injection makes the registered tool fail — real
		// failure inside the runtime's retry and classification machinery
		// rather than an error the adapter invented.
		if step.Inject != nil && step.Inject.Kind == ev.FailTool {
			tool.FailAlways = true
		}
		contract := tr.ReadContract(tr.ToolID(capability), "1.0.0", tr.CapabilityID(capability))
		if attempts := int(step.Args.Num("max_attempts")); attempts > 0 {
			contract.Retry = tr.RetrySpec{MaxAttempts: attempts, NoBackoff: true}
		}
		s.h.Register(contract, tool)
		s.tools[capability] = tool
		s.record("registered")
		return result(ok, ev.Values{"capability": ev.S(capability)})

	case "register_write":
		if s.writes == nil {
			s.writes = map[string]*tr.CompensatingFake{}
		}
		tool := &tr.CompensatingFake{}
		s.h.Register(tr.WriteContract(tr.ToolID(capability), "1.0.0",
			tr.CapabilityID(capability)), tool)
		s.writes[capability] = tool
		s.record("registered")
		return result(ok, ev.Values{"capability": ev.S(capability)})

	case "execute":
		args := tr.Arguments{"query": tr.String(step.Args.Str("query"))}
		if step.Args.Get("query").IsAbsent() {
			args = tr.Arguments{"query": tr.String("evaluated query")}
		}
		if _, isWrite := s.writes[capability]; isWrite {
			args = tr.Arguments{"subject": tr.String(step.Args.Str("subject"))}
			if step.Args.Get("subject").IsAbsent() {
				args = tr.Arguments{"subject": tr.String("evaluated subject")}
			}
		}

		intent := s.h.Intent(tr.CapabilityID(capability), args)
		// A permission-failure injection removes the actor's grant, which the
		// runtime refuses through its own permission engine.
		if step.Inject != nil && step.Inject.Kind == ev.FailPermission {
			intent.Grant = tr.Grant{Actor: "unauthorised"}
			intent.Actor = "unauthorised"
		}
		if corr := step.Args.Str("correlation"); corr != "" {
			// A fixed correlation is how a scenario exercises idempotency: two
			// executions in one correlation must produce one tool call.
			intent.Correlation = tr.CorrelationID(corr)
		}

		res, _ := s.h.Runtime.Execute(ctx, intent)
		s.execs++
		s.record("executed")

		out := ev.Values{
			"steps":    ev.N(float64(len(res.Steps))),
			"attempts": ev.N(float64(totalAttempts(res))),
			"replayed": ev.B(anyReplayed(res)),
		}
		if res.Compensation != nil {
			out["compensated"] = ev.N(float64(res.Compensation.Compensated))
			out["rollback_complete"] = ev.B(res.Compensation.Complete)
		}
		if res.Err != nil {
			return result(toolReason(res.Err), out)
		}
		return result(ok, out)

	case "plan":
		args := tr.Arguments{"query": tr.String("evaluated query")}
		intent := s.h.Intent(tr.CapabilityID(capability), args)
		plan, err := s.h.Runtime.Plan(intent)
		if err != nil {
			return result(toolReason(err), ev.Values{})
		}
		// Planning executes nothing, which is the invariant worth evaluating:
		// the tool's call count must not move.
		calls := int32(0)
		if tool, found := s.tools[capability]; found {
			calls = tool.Calls()
		}
		return result(ok, ev.Values{
			"shape":       ev.S(plan.Shape),
			"steps":       ev.N(float64(plan.StepCount())),
			"mutates":     ev.B(plan.Mutates()),
			"compensable": ev.B(plan.FullyCompensable()),
			"tool_calls":  ev.N(float64(calls)),
		})

	case "tool_calls":
		calls := int32(0)
		if tool, found := s.tools[capability]; found {
			calls = tool.Calls()
		}
		if w, found := s.writes[capability]; found {
			calls = w.Calls()
		}
		return result(ok, ev.Values{"calls": ev.N(float64(calls))})

	case "health":
		rep := s.h.Runtime.Coordinator().Health()
		return result(ok, ev.Values{
			"registrations": ev.N(float64(rep.Registrations)),
			"dead_letters":  ev.N(float64(rep.DeadLetters)),
			"abandoned":     ev.N(float64(rep.AbandonedGoroutines)),
		})

	default:
		return failed("unknown_op", "tool subject has no operation "+step.Op)
	}
}

func totalAttempts(res tr.PlanResult) int {
	total := 0
	for _, s := range res.Steps {
		total += s.Attempts
	}
	return total
}

func anyReplayed(res tr.PlanResult) bool {
	for _, s := range res.Steps {
		if s.Replayed {
			return true
		}
	}
	return false
}

// toolReason maps a tool runtime error to a bounded outcome code.
func toolReason(err error) string {
	switch {
	case err == nil:
		return ok
	case errors.Is(err, tr.ErrPermissionDenied):
		return "permission_denied"
	case errors.Is(err, tr.ErrConsentRequired):
		return "consent_required"
	case errors.Is(err, tr.ErrNoCapability):
		return "no_capability"
	case errors.Is(err, tr.ErrNoHealthyProvider):
		return "no_healthy_provider"
	case errors.Is(err, tr.ErrTimeout):
		return "timeout"
	case errors.Is(err, tr.ErrCancelled):
		return "cancelled"
	case errors.Is(err, tr.ErrCircuitOpen):
		return "circuit_open"
	case errors.Is(err, tr.ErrQueueFull):
		return "queue_full"
	case errors.Is(err, tr.ErrBudgetExceeded):
		return "budget_exceeded"
	case errors.Is(err, tr.ErrInvalidInput):
		return "invalid_input"
	case errors.Is(err, tr.ErrInvalidOutput):
		return "invalid_output"
	case errors.Is(err, tr.ErrCompensationFailed):
		return "compensation_failed"
	case errors.Is(err, tr.ErrDuplicate):
		return "duplicate"
	default:
		return "tool_error"
	}
}

func (s *toolSession) record(kind string) {
	s.events = append(s.events, ev.EventRecord{Type: kind})
}

func (s *toolSession) State() ev.Values {
	return ev.Values{
		"executions":    ev.N(float64(s.execs)),
		"registrations": ev.N(float64(s.h.Runtime.Registry().Len())),
	}
}

func (s *toolSession) Events() []ev.EventRecord {
	out := s.events
	s.events = nil
	return out
}

func (s *toolSession) Close() error { return s.h.Runtime.Stop() }
