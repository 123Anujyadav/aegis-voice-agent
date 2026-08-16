package evalsubjects

import (
	"context"
	"errors"
	"time"

	ev "github.com/callscreen/callscreen-platform/packages/go/evaluation"
	mem "github.com/callscreen/callscreen-platform/packages/go/memory"
)

// MemorySubject evaluates the Phase 10C memory engine.
//
// It exercises the operations whose behaviour other phases depend on: store,
// retrieve, forget, promote and sweep. Retrieval is the interesting one, because
// the memory engine deliberately distinguishes four negative outcomes and an
// adapter that collapsed them would make the platform blind to the day they
// stopped being distinguished.
type MemorySubject struct{}

// NewMemorySubject builds the adapter.
func NewMemorySubject() *MemorySubject { return &MemorySubject{} }

// Name identifies the subject.
func (m *MemorySubject) Name() ev.SubjectName { return SubjectMemory }

// Capabilities lists what it can be asked to do.
func (m *MemorySubject) Capabilities() []ev.Capability {
	return []ev.Capability{
		"store", "retrieve", "forget", "promote", "sweep", "consent", "clock",
		ev.InjectionCapability(ev.FailMemory),
	}
}

// Open starts a session with its own memory runtime.
func (m *MemorySubject) Open(ctx context.Context, spec ev.SessionSpec) (ev.Session, error) {
	h, err := mem.NewHarness()
	if err != nil {
		return nil, err
	}
	return &memorySession{h: h}, nil
}

type memorySession struct {
	h      *mem.Harness
	events []ev.EventRecord
	stored int
}

// Advance moves the memory engine's clock.
func (s *memorySession) Advance(d time.Duration) { s.h.Clock.Advance(d) }

func (s *memorySession) Execute(ctx context.Context, step ev.Step) ev.StepResult {
	store := s.h.Assistant().Store

	subject := mem.SubjectID(step.Args.Str("subject"))
	if subject == "" {
		subject = "evaluated-subject"
	}
	name := step.Args.Str("name")
	if name == "" {
		name = "record"
	}

	switch step.Op {
	case "store":
		rec := mem.InternalRecord(subject, kindFor(step.Args.Str("kind")), name,
			step.Args.Str("data"))

		// A memory-failure injection is expressed as a record the engine
		// itself must refuse — a payload over the size cap. That is real
		// enforcement rather than a faked error, which is the difference
		// between testing the subject and testing the adapter.
		if step.Inject != nil && step.Inject.Kind == ev.FailMemory {
			rec.Value.Data = make([]byte, 512*1024)
		}

		out, err := store.Store(rec)
		if err != nil {
			return result("refused", ev.Values{"reason": ev.S(memReason(err))})
		}
		s.stored++
		s.record("stored")
		return result(ok, ev.Values{
			"tier":    ev.S(out.Tier.String()),
			"version": ev.N(float64(out.Version)),
		})

	case "retrieve":
		key := mem.Key{Subject: subject, Kind: kindFor(step.Args.Str("kind")), Name: name}
		out, err := store.Retrieve(key, "evaluator")
		if err != nil {
			// FOUR DISTINCT NEGATIVE OUTCOMES, preserved rather than
			// collapsed. The memory engine went to trouble to distinguish
			// "never existed" from "aged out" from "destroyed on purpose", and
			// an adapter reporting them all as "error" would hide the day that
			// distinction was lost.
			return result(memReason(err), ev.Values{"found": ev.B(false)})
		}
		return result(ok, ev.Values{
			"found":   ev.B(true),
			"tier":    ev.S(out.Tier.String()),
			"version": ev.N(float64(out.Version)),
			"size":    ev.N(float64(len(out.Value.Data))),
		})

	case "promote":
		key := mem.Key{Subject: subject, Kind: kindFor(step.Args.Str("kind")), Name: name}
		if err := store.Promote(key); err != nil {
			return result("refused", ev.Values{"reason": ev.S(memReason(err))})
		}
		s.record("promoted")
		rec, _ := store.Index().Get(key)
		tier := ""
		if rec != nil {
			tier = rec.Tier.String()
		}
		return result(ok, ev.Values{"tier": ev.S(tier)})

	case "forget":
		res, err := s.h.Runtime.Coordinator().Forget(subject, "evaluator")
		if err != nil {
			return failed("forget_error", err.Error())
		}
		s.record("forgotten")
		return result(ok, ev.Values{
			"deleted":  ev.N(float64(res.TotalDeleted)),
			"redacted": ev.N(float64(res.TotalRedacted)),
			"retained": ev.N(float64(res.TotalRetained)),
			"complete": ev.B(res.Complete),
		})

	case "sweep":
		reports := s.h.Runtime.Sweep()
		var expired, promoted, demoted int
		for _, r := range reports {
			expired += r.Expired
			promoted += r.Promoted
			demoted += r.Demoted
		}
		return result(ok, ev.Values{
			"expired":  ev.N(float64(expired)),
			"promoted": ev.N(float64(promoted)),
			"demoted":  ev.N(float64(demoted)),
		})

	case "grant_consent":
		s.h.Grant(subject, step.Args.Str("ref"))
		return result(ok, ev.Values{"granted": ev.B(true)})

	case "count":
		return result(ok, ev.Values{"records": ev.N(float64(store.Count()))})

	default:
		return failed("unknown_op", "memory subject has no operation "+step.Op)
	}
}

// kindFor maps a scenario's kind string to a memory kind.
//
// Defaults to conversation rather than erroring, because a scenario that omits
// the kind is asking about the common case and refusing it would make every
// simple scenario carry a field it does not care about.
func kindFor(s string) mem.Kind {
	switch s {
	case "user":
		return mem.KindUser
	case "preference":
		return mem.KindPreference
	case "contact":
		return mem.KindContact
	case "business":
		return mem.KindBusiness
	case "session":
		return mem.KindSession
	case "scratchpad":
		return mem.KindScratchpad
	case "policy":
		return mem.KindPolicy
	default:
		return mem.KindConversation
	}
}

// memReason maps a memory error to a bounded outcome code.
//
// Bounded because it enters a behaviour fingerprint and a failure heatmap. An
// adapter passing an error string through would make every improved error
// message look like drift.
func memReason(err error) string {
	switch {
	case err == nil:
		return ok
	case errors.Is(err, mem.ErrNotFound):
		return "not_found"
	case errors.Is(err, mem.ErrExpired):
		return "expired"
	case errors.Is(err, mem.ErrRedacted):
		return "redacted"
	case errors.Is(err, mem.ErrArchived):
		return "archived"
	case errors.Is(err, mem.ErrConsentRequired):
		return "consent_required"
	case errors.Is(err, mem.ErrLegalHold):
		return "legal_hold"
	case errors.Is(err, mem.ErrVersionConflict):
		return "version_conflict"
	case errors.Is(err, mem.ErrBudgetExceeded):
		return "budget_exceeded"
	case errors.Is(err, mem.ErrInvariant):
		return "invariant"
	default:
		return "refused"
	}
}

func (s *memorySession) record(kind string) {
	s.events = append(s.events, ev.EventRecord{Type: kind})
}

func (s *memorySession) State() ev.Values {
	return ev.Values{
		"records": ev.N(float64(s.h.Assistant().Store.Count())),
		"stored":  ev.N(float64(s.stored)),
	}
}

func (s *memorySession) Events() []ev.EventRecord {
	out := s.events
	s.events = nil
	return out
}

func (s *memorySession) Close() error { return s.h.Runtime.Stop() }
