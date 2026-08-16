package evalsubjects

import (
	"context"
	"time"

	ev "github.com/callscreen/callscreen-platform/packages/go/evaluation"
	gov "github.com/callscreen/callscreen-platform/packages/go/governance"
)

// GovernanceSubject evaluates the Phase 10E safety, policy and governance
// engine.
//
// The most important subject in the platform to evaluate, because it is the one
// whose failure mode is invisible: a governance engine that started allowing
// something would produce no error anywhere, and only a scenario asserting the
// refusal would notice.
//
// It loads the baseline policies, so the scenarios evaluate the rules the
// platform actually ships with rather than fixtures invented here.
type GovernanceSubject struct{}

// NewGovernanceSubject builds the adapter.
func NewGovernanceSubject() *GovernanceSubject { return &GovernanceSubject{} }

// Name identifies the subject.
func (g *GovernanceSubject) Name() ev.SubjectName { return SubjectGovernance }

// Capabilities lists what it can be asked to do.
func (g *GovernanceSubject) Capabilities() []ev.Capability {
	return []ev.Capability{
		"decide", "consent", "emergency", "escalation", "clock",
		ev.InjectionCapability(ev.FailPermission),
		ev.InjectionCapability(ev.FailGovernance),
	}
}

// Open starts a session with its own governance engine.
func (g *GovernanceSubject) Open(ctx context.Context, spec ev.SessionSpec) (ev.Session, error) {
	opts := []gov.HarnessOption{}
	if baseline, _ := spec.Params.Get("baseline").Flag(); baseline || spec.Params.Get("baseline").IsAbsent() {
		opts = append(opts, gov.WithBaseline())
	}
	h, err := gov.NewHarness(opts...)
	if err != nil {
		return nil, err
	}
	return &governanceSession{h: h}, nil
}

type governanceSession struct {
	h         *gov.Harness
	events    []ev.EventRecord
	decisions int
	lastID    gov.DecisionID
}

// Advance moves the governance engine's clock.
func (s *governanceSession) Advance(d time.Duration) { s.h.Clock.Advance(d) }

func (s *governanceSession) Execute(ctx context.Context, step ev.Step) ev.StepResult {
	switch step.Op {
	case "decide":
		action := s.actionFor(step)
		subject := gov.SubjectID(step.Args.Str("subject"))
		if subject == "" {
			subject = "evaluated-subject"
		}
		actor := gov.ActorID(step.Args.Str("actor"))
		if actor == "" {
			actor = "evaluated-actor"
		}

		req := gov.Request{
			Action: action, Actor: actor, Subject: subject,
			Correlation: "eval-correlation", Session: "eval-session",
		}
		// A governance-failure injection is expressed as a request the engine
		// must refuse structurally — a malformed action. Real enforcement
		// rather than a faked error.
		if step.Inject != nil && step.Inject.Kind == ev.FailGovernance {
			req.Action.Operation = "not_a_real_operation"
		}
		if step.Inject != nil && step.Inject.Kind == ev.FailPermission {
			req.Actor = ""
		}
		if level := step.Args.Str("risk"); level != "" {
			if parsed, okLevel := gov.ParseRiskLevel(level); okLevel {
				req.Risk = gov.RiskAssessment{
					Signals: []gov.Signal{{Source: "evaluation", Kind: "scripted",
						Level: parsed, Weight: 1}}}
			}
		}

		d := s.h.Engine.Decide(req)
		s.decisions++
		s.lastID = d.ID
		s.record("decided")

		out := ev.Values{
			"outcome":     ev.S(d.Outcome.String()),
			"reason":      ev.S(d.Reason),
			"decided_by":  ev.S(string(d.DecidedBy)),
			"scope":       ev.S(d.Scope.String()),
			"obligations": ev.N(float64(len(d.Obligations))),
			"trace":       ev.N(float64(len(d.Trace))),
			"risk":        ev.S(d.Risk.Level.String()),
		}
		// THE OUTCOME CODE IS THE GOVERNANCE OUTCOME, not "ok". A decision to
		// deny is the engine working; reporting it as a step outcome of "ok"
		// would make a golden unable to distinguish allow from deny.
		return result(d.Outcome.String(), out)

	case "grant_consent":
		basis := step.Args.Str("basis")
		if basis == "" {
			basis = "data_processing"
		}
		subject := gov.SubjectID(step.Args.Str("subject"))
		if subject == "" {
			subject = "evaluated-subject"
		}
		ttl := time.Duration(step.Args.Num("ttl_seconds")) * time.Second
		if ttl <= 0 {
			ttl = time.Hour
		}
		s.h.Grant(subject, basis, ttl)
		s.record("consent_granted")
		return result(ok, ev.Values{"basis": ev.S(basis)})

	case "revoke_consent":
		basis := step.Args.Str("basis")
		if basis == "" {
			basis = "data_processing"
		}
		subject := gov.SubjectID(step.Args.Str("subject"))
		if subject == "" {
			subject = "evaluated-subject"
		}
		if _, err := s.h.Engine.Consent().Revoke(subject, basis, "evaluation"); err != nil {
			return result("revoke_refused", ev.Values{"basis": ev.S(basis)})
		}
		s.record("consent_revoked")
		return result(ok, ev.Values{"basis": ev.S(basis)})

	case "check_consent":
		basis := step.Args.Str("basis")
		if basis == "" {
			basis = "data_processing"
		}
		subject := gov.SubjectID(step.Args.Str("subject"))
		if subject == "" {
			subject = "evaluated-subject"
		}
		check := s.h.Engine.Consent().Check(subject, basis)
		return result(check.Reason, ev.Values{"valid": ev.B(check.Valid)})

	case "activate_emergency":
		name := step.Args.Str("name")
		if name == "" {
			name = "eval-incident"
		}
		em := gov.TestEmergency(name, s.h.Clock.Now().Add(time.Hour),
			gov.EmergencyAllowPolicy(gov.PolicyID(name+"-allow"), gov.Match{}))
		if err := s.h.Engine.Emergency().Activate(em); err != nil {
			return result("activation_refused", ev.Values{"error": ev.S(err.Error())})
		}
		s.record("emergency_activated")
		return result(ok, ev.Values{"active": ev.N(float64(len(s.h.Engine.Emergency().Active())))})

	case "escalations":
		return result(ok, ev.Values{
			"pending": ev.N(float64(s.h.Engine.Human().Depth()))})

	case "health":
		rep := s.h.Engine.Coordinator().Health()
		return result(ok, ev.Values{
			"policies":    ev.N(float64(rep.Policies)),
			"conflicts":   ev.N(float64(rep.Conflicts)),
			"decisions":   ev.N(float64(rep.Decisions)),
			"panics":      ev.N(float64(rep.Panics)),
			"emergencies": ev.N(float64(len(rep.ActiveEmergencies))),
		})

	default:
		return failed("unknown_op", "governance subject has no operation "+step.Op)
	}
}

// actionFor builds a governance action from a step's arguments.
//
// It uses the exported action builders rather than constructing the struct, so
// a change to what a "write action" means in Phase 10E is picked up here rather
// than silently diverging.
func (s *governanceSession) actionFor(step ev.Step) gov.Action {
	resource := step.Args.Str("resource")
	if resource == "" {
		resource = "evaluated-resource"
	}

	switch step.Args.Str("action") {
	case "write":
		return gov.WriteAction(resource)
	case "tool":
		rev := gov.ReversibleFully
		if step.Args.Str("reversibility") == "never" {
			rev = gov.ReversibleNever
		}
		return gov.ToolAction(resource, rev)
	case "notify":
		return gov.NotifyAction(resource)
	case "external":
		class := gov.ClassInternal
		if step.Args.Str("classification") == "sensitive" {
			class = gov.ClassSensitive
		}
		return gov.ExternalAction(resource, class)
	case "secret":
		a := gov.WriteAction(resource)
		a.Classification = gov.ClassSecret
		return a
	default:
		return gov.ReadAction(resource)
	}
}

func (s *governanceSession) record(kind string) {
	s.events = append(s.events, ev.EventRecord{Type: kind})
}

func (s *governanceSession) State() ev.Values {
	return ev.Values{
		"decisions": ev.N(float64(s.decisions)),
		"policies":  ev.N(float64(s.h.Engine.Policies().Len())),
		"consents":  ev.N(float64(s.h.Engine.Consent().Len())),
		"pending":   ev.N(float64(s.h.Engine.Human().Depth())),
	}
}

func (s *governanceSession) Events() []ev.EventRecord {
	out := s.events
	s.events = nil
	return out
}

func (s *governanceSession) Close() error { return s.h.Engine.Stop() }
