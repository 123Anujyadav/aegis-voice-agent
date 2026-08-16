package correlation_test

import (
	"context"
	"testing"

	gov "github.com/callscreen/callscreen-platform/packages/go/governance"
	"github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	tel "github.com/callscreen/callscreen-platform/packages/go/telephony"
	tr "github.com/callscreen/callscreen-platform/packages/go/toolruntime"
)

// The gate for Phase 12 T6: one correlation identity, minted once, surviving a
// trip through four real engines.
//
// These tests drive the actual runtimes — telephony's call registry, media's
// coordinator, governance's decision engine, toolruntime's executor — not
// doubles. A test that asserted propagation through fakes would prove only that
// the fakes copy a field.
//
// The identity is minted by [rt.NewCorrelationID] and never re-minted. Every
// assertion below compares against that one value, so a subsystem that drops it,
// replaces it, or mints its own fails here.

// observed records what one subsystem reported, so a failure names the boundary.
type observed struct {
	subsystem string
	got       rt.CorrelationID
}

func TestCorrelation_PropagatesAcrossFourRealSubsystems(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// ONE identity. Minted by runtime, which is the only place that mints.
	want := rt.NewCorrelationID()
	if want == "" {
		t.Fatal("runtime minted an empty correlation identity")
	}

	var seen []observed

	// --- 1. telephony -------------------------------------------------------
	th, err := tel.NewHarness()
	if err != nil {
		t.Fatalf("telephony harness: %v", err)
	}
	if _, err := th.Start(ctx); err != nil {
		t.Fatalf("telephony start: %v", err)
	}
	t.Cleanup(func() { _, _ = th.Stop(ctx) })

	call, err := th.Runtime.Registry().CreateWithID(tel.NewCallID(), want, th.Inbound(), th.Clock)
	if err != nil {
		t.Fatalf("telephony create call: %v", err)
	}
	seen = append(seen, observed{"telephony", call.Correlation()})

	// --- 2. media -----------------------------------------------------------
	mh, err := media.NewHarness()
	if err != nil {
		t.Fatalf("media harness: %v", err)
	}
	if _, err := mh.Start(ctx); err != nil {
		t.Fatalf("media start: %v", err)
	}
	t.Cleanup(func() { _, _ = mh.Stop(ctx) })

	// The call's correlation, carried into the stream that serves it — assigned
	// directly, with no conversion. Before ADR-0014 this line required
	// media.CorrelationID(string(want)).
	sctx := mh.Context()
	sctx.Correlation = want

	stream, err := mh.Coordinator.Open(ctx, sctx)
	if err != nil {
		t.Fatalf("media open: %v", err)
	}
	seen = append(seen, observed{"media", stream.Context().Correlation})

	// --- 3. governance ------------------------------------------------------
	gh, err := gov.NewHarness()
	if err != nil {
		t.Fatalf("governance harness: %v", err)
	}

	decision := gh.Engine.Decide(gov.Request{
		Action:      gov.ReadAction("contact/self"),
		Actor:       "actor-test",
		Subject:     "subject-test",
		Session:     "sess-test",
		Correlation: want,
	})
	seen = append(seen, observed{"governance", decision.Correlation})

	// --- 4. toolruntime -----------------------------------------------------
	trh, err := tr.NewHarness()
	if err != nil {
		t.Fatalf("toolruntime harness: %v", err)
	}
	trh.Register(tr.ReadContract("t", "1.0.0", "cap"), &tr.FakeTool{})

	intent := trh.Intent("cap", tr.Arguments{"query": tr.String("q")})
	intent.Correlation = want
	if _, err := trh.Runtime.Execute(ctx, intent); err != nil {
		t.Fatalf("toolruntime execute: %v", err)
	}

	trace := tr.TraceFor(trh.Audit, want)
	if len(trace.Entries) == 0 {
		t.Fatal("toolruntime recorded no audit entry for the correlation the " +
			"execution was given: the identity did not reach the audit trail")
	}
	seen = append(seen, observed{"toolruntime", trace.Correlation})
	for i, e := range trace.Entries {
		if e.Correlation != want {
			t.Errorf("toolruntime audit entry %d carries %q, want %q",
				i, e.Correlation, want)
		}
	}

	// --- the gate -----------------------------------------------------------
	if len(seen) < 3 {
		t.Fatalf("only %d subsystems observed; the gate requires at least 3", len(seen))
	}
	for _, o := range seen {
		if o.got != want {
			t.Errorf("%s reported correlation %q, want %q — the identity was "+
				"dropped or replaced at that boundary", o.subsystem, o.got, want)
		}
	}
	if t.Failed() {
		return
	}
	t.Logf("one correlation identity %q survived %d real subsystems: %s, %s, %s, %s",
		want, len(seen), seen[0].subsystem, seen[1].subsystem,
		seen[2].subsystem, seen[3].subsystem)
}

// TestCorrelation_GovernanceTraceAssemblesByTheCallsIdentity is the trace
// assembly the phase is actually for: query one subsystem's audit trail using
// the identity another subsystem minted, with no conversion.
func TestCorrelation_GovernanceTraceAssemblesByTheCallsIdentity(t *testing.T) {
	t.Parallel()

	// Minted as telephony would mint it, at the edge of the platform.
	callCorrelation := tel.NewCorrelationID()

	gh, err := gov.NewHarness()
	if err != nil {
		t.Fatalf("governance harness: %v", err)
	}
	gh.Engine.Decide(gov.Request{
		Action:      gov.ReadAction("contact/self"),
		Actor:       "actor-test",
		Subject:     "subject-test",
		Session:     "sess-test",
		Correlation: callCorrelation,
	})

	// The assembly. callCorrelation is a telephony value being used to index a
	// governance audit trail; this compiles only because they are one type.
	trace := gov.TraceFor(gh.Audit, callCorrelation)
	if len(trace.Entries) == 0 {
		t.Fatalf("no governance audit entry found for the call's correlation %q",
			callCorrelation)
	}
	for i, e := range trace.Entries {
		if e.Correlation != callCorrelation {
			t.Errorf("governance audit entry %d carries %q, want %q",
				i, e.Correlation, callCorrelation)
		}
	}
}

// TestCorrelation_EveryMintReturnsTheOneType guards against a subsystem
// reintroducing its own minting convention. All four delegate to runtime, so
// every identity shares one prefix and one format.
func TestCorrelation_EveryMintReturnsTheOneType(t *testing.T) {
	t.Parallel()

	mints := map[string]rt.CorrelationID{
		"runtime":     rt.NewCorrelationID(),
		"telephony":   tel.NewCorrelationID(),
		"media":       media.NewCorrelationID(),
		"governance":  gov.NewCorrelationID(),
		"toolruntime": tr.NewCorrelationID(),
	}

	const prefix = "corr_"
	seen := map[rt.CorrelationID]string{}
	for name, id := range mints {
		if len(id) <= len(prefix) || string(id[:len(prefix)]) != prefix {
			t.Errorf("%s minted %q, which does not use the one platform "+
				"convention %q — a second minting convention has returned",
				name, id, prefix)
		}
		if prev, dup := seen[id]; dup {
			t.Errorf("%s and %s minted the identical value %q", prev, name, id)
		}
		seen[id] = name
	}
}

// TestCorrelation_CrossSubsystemAssignmentNeedsNoConversion states the property
// the alias buys, as an executable claim rather than a comment.
func TestCorrelation_CrossSubsystemAssignmentNeedsNoConversion(t *testing.T) {
	t.Parallel()

	origin := tel.NewCorrelationID()

	// Each of these is a plain assignment. If any subsystem reverted to its own
	// named type this function would not compile.
	var (
		toMedia   media.CorrelationID = origin
		toGov     gov.CorrelationID   = toMedia
		toTool    tr.CorrelationID    = toGov
		toRuntime rt.CorrelationID    = toTool
	)

	if toRuntime != origin {
		t.Fatalf("value changed while crossing four subsystems: %q -> %q",
			origin, toRuntime)
	}
}
