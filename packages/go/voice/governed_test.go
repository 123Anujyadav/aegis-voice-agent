package voice

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	governance "github.com/callscreen/callscreen-platform/packages/go/governance"
)

// ---------------------------------------------------------------------------
// Doubles
// ---------------------------------------------------------------------------

// scriptedDecision lets a test answer as the engine would.
type scriptedDecision struct {
	outcome     governance.Outcome
	reason      string
	obligations []governance.Obligation
	policy      governance.PolicyID
}

// recordingGovernor answers with a script and records what it was asked.
type recordingGovernor struct {
	answer scriptedDecision

	calls    atomic.Int64
	mu       sync.Mutex
	requests []governance.Request
}

func (g *recordingGovernor) Decide(r governance.Request) governance.Decision {
	g.calls.Add(1)
	g.mu.Lock()
	g.requests = append(g.requests, r)
	g.mu.Unlock()

	return governance.Decision{
		ID:          governance.DecisionID("dec-1"),
		Outcome:     g.answer.outcome,
		Reason:      g.answer.reason,
		DecidedBy:   g.answer.policy,
		Obligations: g.answer.obligations,
	}
}

func (g *recordingGovernor) lastRequest(t *testing.T) governance.Request {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.requests) == 0 {
		t.Fatal("governance was never consulted")
	}
	return g.requests[len(g.requests)-1]
}

// recordingInvoker stands in for a service's toolruntime adapter.
type recordingInvoker struct {
	calls atomic.Int64
	err   error
	delay time.Duration

	// forbidden fails the test if it is ever called.
	forbidden bool
	t         *testing.T

	mu       sync.Mutex
	requests []ToolRequest
}

func (i *recordingInvoker) InvokeTool(ctx context.Context, req ToolRequest) (ToolResult, error) {
	if i.forbidden {
		i.t.Errorf("the tool invoker was reached for %q on %q: governance did not "+
			"permit this and nothing may execute", req.Intent.Operation, req.Intent.Resource)
	}
	i.calls.Add(1)

	i.mu.Lock()
	i.requests = append(i.requests, req)
	i.mu.Unlock()

	// A real adapter must do this, and this one does, so the check is exercised
	// rather than merely documented.
	if err := req.Validate(); err != nil {
		return ToolResult{}, err
	}

	if i.delay > 0 {
		select {
		case <-time.After(i.delay):
		case <-ctx.Done():
			return ToolResult{}, ctx.Err()
		}
	}
	if i.err != nil {
		return ToolResult{}, i.err
	}
	return ToolResult{Completed: true, Code: "ok"}, nil
}

func (i *recordingInvoker) lastRequest(t *testing.T) ToolRequest {
	t.Helper()
	i.mu.Lock()
	defer i.mu.Unlock()
	if len(i.requests) == 0 {
		t.Fatal("the invoker was never called")
	}
	return i.requests[len(i.requests)-1]
}

func testIntent() ToolIntent {
	return ToolIntent{
		Operation:      "lookup",
		Resource:       "account.balance",
		Reversibility:  governance.ReversibleNone,
		Classification: governance.ClassPersonal,
	}
}

func newGateway(t *testing.T, g Governor, inv ToolInvoker) *ToolGateway {
	t.Helper()

	gw, err := NewToolGateway(GatewayConfig{
		Governor: g, Invoker: inv,
		Session: SessionID("ses-governed"),
		Actor:   governance.ActorID("voice-agent"),
		Org:     governance.OrgID("org-1"),
	})
	if err != nil {
		t.Fatalf("NewToolGateway: %v", err)
	}
	return gw
}

// ---------------------------------------------------------------------------
// The path: decide, then execute
// ---------------------------------------------------------------------------

func TestGoverned_AllowedActionReachesTheTool(t *testing.T) {
	t.Parallel()

	gov := &recordingGovernor{answer: scriptedDecision{
		outcome: governance.OutcomeAllow, reason: "permitted",
		policy: governance.PolicyID("pol-tools"),
	}}
	inv := &recordingInvoker{}
	gw := newGateway(t, gov, inv)

	result, err := gw.Invoke(context.Background(), TurnID("turn-1"), testIntent())
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !result.Completed {
		t.Error("the action was not reported completed")
	}

	// Governance first, exactly once, and then the tool.
	if got := gov.calls.Load(); got != 1 {
		t.Errorf("governance was consulted %d times, want 1", got)
	}
	if got := inv.calls.Load(); got != 1 {
		t.Errorf("the tool was invoked %d times, want 1", got)
	}

	// The request carried the decision that authorised it, so an audit trail
	// can be reassembled from either end.
	req := inv.lastRequest(t)
	if !req.Auth.Valid() {
		t.Error("the invoker received an invalid authorisation")
	}
	if got := req.Auth.Decision(); got != governance.DecisionID("dec-1") {
		t.Errorf("the authorisation names decision %q", got)
	}
	if got := req.Auth.DecidedBy(); got != governance.PolicyID("pol-tools") {
		t.Errorf("the authorisation names policy %q", got)
	}

	// And the request governance saw described the action, as a tool action.
	asked := gov.lastRequest(t)
	if asked.Action.Kind != governance.ActionTool {
		t.Errorf("the action kind was %v, want ActionTool", asked.Action.Kind)
	}
	if asked.Action.Operation != "lookup" || asked.Action.Resource != "account.balance" {
		t.Errorf("governance was asked about %q/%q",
			asked.Action.Operation, asked.Action.Resource)
	}
}

func TestGoverned_DenialPreventsToolExecution(t *testing.T) {
	t.Parallel()

	// The invoker fails the test if it is reached at all: the assertion is not
	// "the result was an error" but "nothing executed".
	inv := &recordingInvoker{forbidden: true, t: t}
	gov := &recordingGovernor{answer: scriptedDecision{
		outcome: governance.OutcomeDeny, reason: "not_permitted",
		policy: governance.PolicyID("pol-deny"),
	}}
	gw := newGateway(t, gov, inv)

	_, err := gw.Invoke(context.Background(), TurnID("turn-1"), testIntent())
	if err == nil {
		t.Fatal("a denied action returned no error")
	}
	if !errors.Is(err, ErrGovernanceDenied) {
		t.Errorf("want ErrGovernanceDenied, got %v", err)
	}

	var denial *DenialError
	if !errors.As(err, &denial) {
		t.Fatalf("want a *DenialError, got %T", err)
	}
	if denial.Reason != "not_permitted" || denial.DecidedBy != governance.PolicyID("pol-deny") {
		t.Errorf("the denial lost the decision's vocabulary: %+v", denial)
	}

	if got := inv.calls.Load(); got != 0 {
		t.Errorf("the tool ran %d times for a denied action", got)
	}
	if got := gw.Invoked(); got != 0 {
		t.Errorf("the gateway counted %d invocations for a denial", got)
	}
	if got := gw.Denied(); got != 1 {
		t.Errorf("the denial was not counted (%d)", got)
	}
}

func TestGoverned_ObligationsArePreservedAndStopExecution(t *testing.T) {
	t.Parallel()

	// An allowed decision that still carries obligations has not been
	// discharged. This layer cannot satisfy one — a confirmation is the
	// caller's to give — so it must report them intact rather than proceeding.
	deadline := time.Now().Add(time.Minute)
	inv := &recordingInvoker{forbidden: true, t: t}
	gov := &recordingGovernor{answer: scriptedDecision{
		outcome: governance.OutcomeAllow,
		reason:  "allowed_with_obligations",
		policy:  governance.PolicyID("pol-confirm"),
		obligations: []governance.Obligation{
			{
				Kind: governance.ObligationConfirmation, Target: "user",
				Reason: "high_value", Deadline: deadline,
				Policy: governance.PolicyID("pol-confirm"),
			},
		},
	}}
	gw := newGateway(t, gov, inv)

	_, err := gw.Invoke(context.Background(), TurnID("turn-1"), testIntent())
	if err == nil {
		t.Fatal("an action with unmet obligations executed")
	}

	// "Not yet" is a different situation from "no", and a caller that cannot
	// tell them apart either retries forever or gives up too early.
	if !errors.Is(err, ErrObligationsUnmet) {
		t.Errorf("want ErrObligationsUnmet, got %v", err)
	}
	if errors.Is(err, ErrGovernanceDenied) {
		t.Error("an unmet obligation must not read as a flat refusal")
	}

	var denial *DenialError
	if !errors.As(err, &denial) {
		t.Fatalf("want a *DenialError, got %T", err)
	}

	ob, ok := denial.Obligation(governance.ObligationConfirmation)
	if !ok {
		t.Fatalf("the confirmation obligation disappeared: %+v", denial.Obligations)
	}
	if ob.Target != "user" || ob.Reason != "high_value" {
		t.Errorf("the obligation lost its detail: %+v", ob)
	}
	if !ob.Deadline.Equal(deadline) {
		t.Error("the obligation's deadline was dropped")
	}
	if ob.Policy != governance.PolicyID("pol-confirm") {
		t.Error("the obligation no longer names the policy that imposed it")
	}

	if got := inv.calls.Load(); got != 0 {
		t.Errorf("the tool ran despite %d unmet obligations", len(denial.Obligations))
	}
	if got := gw.ObligationsUnmet(); got != 1 {
		t.Errorf("the unmet obligation was not counted (%d)", got)
	}
}

func TestGoverned_EveryNonAllowOutcomeStopsExecution(t *testing.T) {
	t.Parallel()

	// Written against the outcome set rather than a list of refusals: an
	// outcome added to Phase 10E later must default to refusing. A gateway that
	// enumerated denials would execute anything it had not heard of.
	for _, outcome := range []governance.Outcome{
		governance.OutcomeDeny,
		governance.OutcomeEscalate,
		governance.OutcomeRequireConfirmation,
		governance.OutcomeRequireHuman,
		governance.OutcomeRequireSupervisor,
	} {
		t.Run(outcome.String(), func(t *testing.T) {
			t.Parallel()

			inv := &recordingInvoker{forbidden: true, t: t}
			gov := &recordingGovernor{answer: scriptedDecision{
				outcome: outcome, reason: "test",
			}}
			gw := newGateway(t, gov, inv)

			if _, err := gw.Invoke(context.Background(), TurnID("t"), testIntent()); err == nil {
				t.Fatalf("outcome %s permitted execution", outcome)
			}
			if got := inv.calls.Load(); got != 0 {
				t.Errorf("outcome %s reached the tool", outcome)
			}
		})
	}
}

func TestGoverned_GovernanceFailureIsTreatedAsRefusal(t *testing.T) {
	t.Parallel()

	// A closed or malfunctioning engine answers Deny with its own reason code.
	// The safe reading of "the policy engine is not working" is "nothing runs".
	inv := &recordingInvoker{forbidden: true, t: t}
	gov := &recordingGovernor{answer: scriptedDecision{
		outcome: governance.OutcomeDeny, reason: "engine_closed",
	}}
	gw := newGateway(t, gov, inv)

	_, err := gw.Invoke(context.Background(), TurnID("t"), testIntent())
	if err == nil {
		t.Fatal("a failed governance engine permitted execution")
	}
	if !strings.Contains(err.Error(), "engine_closed") {
		t.Errorf("the engine's own reason was lost: %v", err)
	}
	if got := inv.calls.Load(); got != 0 {
		t.Error("a tool ran while the policy engine was unavailable")
	}
}

func TestGoverned_ToolFailureAfterApprovalIsATypedToolFailure(t *testing.T) {
	t.Parallel()

	// A tool that fails after approval is a tool failure, not a governance one.
	// Conflating them sends an operator to the wrong runbook.
	inv := &recordingInvoker{err: errors.New("upstream banking API timed out")}
	gov := &recordingGovernor{answer: scriptedDecision{
		outcome: governance.OutcomeAllow, reason: "permitted",
	}}
	gw := newGateway(t, gov, inv)

	_, err := gw.Invoke(context.Background(), TurnID("t"), testIntent())
	if err == nil {
		t.Fatal("a failing tool reported success")
	}
	if !errors.Is(err, ErrProviderFailed) {
		t.Errorf("want ErrProviderFailed, got %v", err)
	}
	if errors.Is(err, ErrGovernanceDenied) || errors.Is(err, ErrObligationsUnmet) {
		t.Error("a tool failure was reported as a governance outcome")
	}
	if !strings.Contains(err.Error(), "upstream banking API timed out") {
		t.Errorf("the tool's own message was lost: %v", err)
	}

	// Governance still allowed it, and the counters say so.
	if got := gw.Allowed(); got != 1 {
		t.Errorf("the approval was not counted (%d)", got)
	}
}

func TestGoverned_CancellationBeforeTheDecisionAsksNobody(t *testing.T) {
	t.Parallel()

	// A decision obtained for a call that has already ended is an audit record
	// for something that never happened.
	inv := &recordingInvoker{forbidden: true, t: t}
	gov := &recordingGovernor{answer: scriptedDecision{outcome: governance.OutcomeAllow}}
	gw := newGateway(t, gov, inv)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := gw.Invoke(ctx, TurnID("t"), testIntent())
	if err == nil {
		t.Fatal("a cancelled action was decided and executed")
	}
	if !errors.Is(err, ErrSessionClosed) {
		t.Errorf("want ErrSessionClosed, got %v", err)
	}
	if got := gov.calls.Load(); got != 0 {
		t.Errorf("governance was consulted %d times for a cancelled action", got)
	}
	if got := inv.calls.Load(); got != 0 {
		t.Error("a cancelled action reached the tool")
	}
}

func TestGoverned_CancellationDuringExecutionPropagates(t *testing.T) {
	t.Parallel()

	inv := &recordingInvoker{delay: 5 * time.Second}
	gov := &recordingGovernor{answer: scriptedDecision{outcome: governance.OutcomeAllow}}
	gw := newGateway(t, gov, inv)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := gw.Invoke(ctx, TurnID("t"), testIntent())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a cancelled execution reported success")
	}
	if elapsed > 3*time.Second {
		t.Errorf("cancellation took %s to reach the tool", elapsed)
	}
}

// ---------------------------------------------------------------------------
// Structural: the bypass must be hard to write
// ---------------------------------------------------------------------------

func TestGoverned_VoiceCannotReachToolRuntime(t *testing.T) {
	t.Parallel()

	// The strongest guarantee available: a package that cannot import an
	// executor cannot call one, whatever anybody forgets.
	mod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}

	for _, forbidden := range []string{"packages/go/toolruntime", "packages/go/memory"} {
		if strings.Contains(string(mod), forbidden) {
			t.Errorf("this module now depends on %s. Tool execution belongs behind "+
				"ToolInvoker and memory behind the layer above; a direct dependency "+
				"is the bypass governed.go exists to prevent", forbidden)
		}
	}

	// ...and no source file IMPORTS one either, in case a dependency arrives
	// transitively.
	//
	// The imports are parsed rather than grepped: an earlier version of this
	// test searched the file text and failed on its own documentation, which
	// mentions both paths by name. A comment naming the thing you must not
	// import is not an import.
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing the package: %v", err)
	}

	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				for _, forbidden := range []string{
					"packages/go/toolruntime", "packages/go/memory",
				} {
					if strings.Contains(path, forbidden) {
						t.Errorf("%s imports %s", name, path)
					}
				}
			}
		}
	}
}

func TestGoverned_AuthorizationCannotBeForgedOutsideThisPackage(t *testing.T) {
	t.Parallel()

	// Every field unexported means no other package can populate one: not by a
	// struct literal, not by assignment. Forging one requires editing this
	// package, which is a code review rather than an oversight.
	typ := reflect.TypeOf(Authorization{})
	for i := 0; i < typ.NumField(); i++ {
		if f := typ.Field(i); f.PkgPath == "" {
			t.Errorf("Authorization.%s is exported; an outside package could "+
				"construct an authorisation governance never granted", f.Name)
		}
	}

	// The zero value authorises nothing.
	var zero Authorization
	if zero.Valid() {
		t.Error("a zero-value Authorization reports itself valid")
	}

	// And a request carrying one is refused at the port.
	req := ToolRequest{Intent: testIntent()}
	if err := req.Validate(); err == nil {
		t.Fatal("an unauthorised request passed validation")
	} else if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("want ErrUnauthorized, got %v", err)
	}
}

func TestGoverned_AnAuthorizationIsNotReusableForAnotherAction(t *testing.T) {
	t.Parallel()

	// An authorisation obtained to read a balance must not transfer one.
	inv := &recordingInvoker{}
	gov := &recordingGovernor{answer: scriptedDecision{outcome: governance.OutcomeAllow}}
	gw := newGateway(t, gov, inv)

	if _, err := gw.Invoke(context.Background(), TurnID("t"), testIntent()); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	granted := inv.lastRequest(t)

	// Re-present the same authorisation for something else.
	swapped := ToolRequest{
		Auth:    granted.Auth,
		Intent:  ToolIntent{Operation: "transfer", Resource: "account.balance"},
		Session: granted.Session,
	}
	if err := swapped.Validate(); err == nil {
		t.Fatal("an authorisation for lookup was accepted for transfer")
	} else if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("want ErrUnauthorized, got %v", err)
	}

	// The same trick on the resource.
	swapped.Intent = ToolIntent{Operation: "lookup", Resource: "account.statements"}
	if err := swapped.Validate(); err == nil {
		t.Fatal("an authorisation for one resource was accepted for another")
	}
}

func TestGoverned_GatewayRefusesToExistWithoutGovernance(t *testing.T) {
	t.Parallel()

	_, err := NewToolGateway(GatewayConfig{
		Session: SessionID("ses-1"), Invoker: &recordingInvoker{},
	})
	if err == nil {
		t.Fatal("a gateway was built with no governor")
	}
	if !strings.Contains(err.Error(), "Governor is required") {
		t.Errorf("the error should say why: %v", err)
	}
}

func TestGoverned_IntentCannotCarryContent(t *testing.T) {
	t.Parallel()

	// An intent becomes a governance decision, and a decision is written to a
	// durable audit store. What a caller said must never reach one.
	forbidden := []string{
		"transcript", "text", "content", "utterance", "response", "prompt",
		"message", "audio", "payload", "pcm", "sample", "phone", "msisdn",
		"credential", "token", "secret", "key", "password",
	}

	for _, typ := range []reflect.Type{
		reflect.TypeOf(ToolIntent{}),
		reflect.TypeOf(ToolRequest{}),
		reflect.TypeOf(ToolResult{}),
		reflect.TypeOf(DenialError{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			lower := strings.ToLower(field.Name)
			for _, bad := range forbidden {
				if strings.Contains(lower, bad) {
					t.Errorf("%s.%s may carry content into an audit record",
						typ.Name(), field.Name)
				}
			}
			// A byte slice is how audio would arrive.
			if field.Type.Kind() == reflect.Slice &&
				field.Type.Elem().Kind() == reflect.Uint8 {
				t.Errorf("%s.%s is a byte sequence and could carry audio",
					typ.Name(), field.Name)
			}
		}
	}
}

func TestGoverned_VoiceHasNoMemoryWritePath(t *testing.T) {
	t.Parallel()

	// A voice turn holds the most sensitive material in the system — what a
	// caller said, in their words. A convenience writer here would be the
	// shortest path from a live transcript to a durable store, so there is
	// none: not an import, not a port, not a method.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package: %v", err)
	}

	suspicious := []string{
		"MemoryWriter", "MemoryStore", "WriteMemory", "StoreMemory",
		"RememberTranscript", "PersistTranscript", "SaveTranscript",
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") ||
			strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		for _, name := range suspicious {
			if strings.Contains(string(body), name) {
				t.Errorf("%s declares %s; what a session should remember is the "+
					"layer above's decision, and it owns the governance identity "+
					"to ask about storing it", e.Name(), name)
			}
		}
	}
}

func TestGoverned_NoRawAudioIsPersisted(t *testing.T) {
	t.Parallel()

	// Raw PCM must never reach durable storage. The package writes no files at
	// all, which is the simplest way to be sure of it.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package: %v", err)
	}

	writers := []string{
		"os.Create", "os.WriteFile", "os.OpenFile", "ioutil.WriteFile",
		"bufio.NewWriter(f", "encoding/gob", "database/sql",
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") ||
			strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		for _, w := range writers {
			if strings.Contains(string(body), w) {
				t.Errorf("%s uses %s; this package persists nothing, and audio "+
					"least of all", e.Name(), w)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Integration with the live pipeline
// ---------------------------------------------------------------------------

// newGovernedPipeline returns a running pipeline with a gateway wired in.
func newGovernedPipeline(
	t *testing.T, gov Governor, inv ToolInvoker,
) (*harness, *ToolGateway) {
	t.Helper()

	h := newHarness(t, harnessOpts{
		tokens:         []string{"One moment?", " Checking that for you?"},
		tokenGap:       30 * time.Millisecond,
		framesPerChunk: 40,
		ttsSynthDelay:  20 * time.Millisecond,
		sinkDelay:      3 * time.Millisecond,
		maxFrames:      256,
	})

	gw, err := NewToolGateway(GatewayConfig{
		Governor: gov, Invoker: inv,
		Session: SessionID("ses-pipeline"),
		Actor:   governance.ActorID("voice-agent"),
	})
	if err != nil {
		t.Fatalf("NewToolGateway: %v", err)
	}

	cfg := h.pipeline.cfg
	cfg.Tools = gw
	p, err := NewPipeline(cfg)
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	h.pipeline = p
	return h, gw
}

func TestGoverned_PipelineActionPassesThroughGovernance(t *testing.T) {
	t.Parallel()

	inv := &recordingInvoker{}
	gov := &recordingGovernor{answer: scriptedDecision{outcome: governance.OutcomeAllow}}
	h, gw := newGovernedPipeline(t, gov, inv)

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	result, err := h.pipeline.InvokeTool(context.Background(), testIntent())
	if err != nil {
		t.Fatalf("InvokeTool: %v", err)
	}
	if !result.Completed {
		t.Error("the action did not complete")
	}
	if got := gov.calls.Load(); got != 1 {
		t.Errorf("governance was consulted %d times", got)
	}
	if got := gw.Invoked(); got != 1 {
		t.Errorf("the gateway invoked %d times", got)
	}
}

func TestGoverned_ADeniedActionLeavesTheTurnValid(t *testing.T) {
	t.Parallel()

	// A refusal is not a fault. The call continues, the session stays live, and
	// nothing about the voice loop is disturbed.
	inv := &recordingInvoker{forbidden: true, t: t}
	gov := &recordingGovernor{answer: scriptedDecision{
		outcome: governance.OutcomeDeny, reason: "not_permitted",
	}}
	h, _ := newGovernedPipeline(t, gov, inv)

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = h.pipeline.Disconnect() }()

	stateBefore := h.fsm.State()

	if _, err := h.pipeline.InvokeTool(context.Background(), testIntent()); err == nil {
		t.Fatal("a denied action reported success")
	}

	if h.fsm.Terminal() {
		t.Error("a governance refusal ended the call")
	}
	if got := h.fsm.State(); got != stateBefore {
		t.Errorf("a refusal moved the session from %s to %s", stateBefore, got)
	}
	if err := h.pipeline.Err(); err != nil {
		t.Errorf("a refusal was recorded as a pipeline fault: %v", err)
	}

	// The voice loop still works afterwards.
	h.feed(t, 4)
	h.obs.waitTurn(t, 20*time.Second)
}

func TestGoverned_SessionEndCancelsAnActionInFlight(t *testing.T) {
	t.Parallel()

	// An action must not outlive the call it was requested for.
	inv := &recordingInvoker{delay: 10 * time.Second}
	gov := &recordingGovernor{answer: scriptedDecision{outcome: governance.OutcomeAllow}}
	h, _ := newGovernedPipeline(t, gov, inv)

	if err := h.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := h.pipeline.InvokeTool(context.Background(), testIntent())
		done <- err
	}()

	time.Sleep(80 * time.Millisecond)
	if err := h.pipeline.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Error("an action outlived the call and reported success")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the action did not stop when the call ended")
	}
}

func TestGoverned_BargeInDuringAGovernedActionLeavesBothValid(t *testing.T) {
	t.Parallel()

	// A caller interrupting while a tool runs must not corrupt either. The
	// interruption is about speech; the action's authorisation is unaffected.
	inv := &recordingInvoker{delay: 200 * time.Millisecond}
	gov := &recordingGovernor{answer: scriptedDecision{outcome: governance.OutcomeAllow}}

	bh := newBargeHarness(t, bargeOpts{})
	gw, err := NewToolGateway(GatewayConfig{
		Governor: gov, Invoker: inv,
		Session: SessionID("ses-pipeline"),
		Actor:   governance.ActorID("voice-agent"),
	})
	if err != nil {
		t.Fatalf("NewToolGateway: %v", err)
	}

	cfg := bh.pipeline.cfg
	cfg.Tools = gw
	p, err := NewPipeline(cfg)
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	bh.pipeline = p

	if err := bh.pipeline.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = bh.pipeline.Disconnect() }()

	bh.feedUntil(t, 20*time.Second, func() bool { return bh.out.count() > 0 })

	// Start the action, then interrupt the agent mid-sentence.
	done := make(chan error, 1)
	go func() {
		_, err := bh.pipeline.InvokeTool(context.Background(), testIntent())
		done <- err
	}()

	bh.intel.mu.Lock()
	bh.intel.bargeAt = bh.intel.frames + 1
	bh.intel.mu.Unlock()

	bh.feedUntil(t, 20*time.Second, func() bool { return bh.pipeline.BargeIns() > 0 })

	select {
	case err := <-done:
		// Either outcome is legitimate: the action may complete, or the session
		// may have cancelled it. What must NOT happen is a governance decision
		// being skipped or an unauthorised execution.
		if err != nil && !errors.Is(err, ErrProviderFailed) &&
			!errors.Is(err, ErrSessionClosed) && !errors.Is(err, context.Canceled) {
			t.Errorf("unexpected action failure during a barge-in: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the governed action never finished")
	}

	if got := gov.calls.Load(); got != 1 {
		t.Errorf("governance was consulted %d times, want exactly 1", got)
	}
	if bh.fsm.Terminal() {
		t.Error("a barge-in during a governed action ended the call")
	}
	for _, c := range bh.fsm.History() {
		if !CanTransition(c.From, c.To) {
			t.Errorf("undeclared transition %s -> %s", c.From, c.To)
		}
	}
}

func TestGoverned_ConcurrentSessionsDecideIndependently(t *testing.T) {
	t.Parallel()

	// Each session asks about its own action, and one session's decision must
	// never authorise another's.
	const sessions = 6

	var wg sync.WaitGroup
	errs := make(chan error, sessions*2)

	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()

			// Half the sessions are refused, so an authorisation leaking
			// between them would be visible as an execution that should not
			// have happened.
			allow := n%2 == 0
			outcome := governance.OutcomeDeny
			if allow {
				outcome = governance.OutcomeAllow
			}

			inv := &recordingInvoker{}
			gov := &recordingGovernor{answer: scriptedDecision{outcome: outcome}}

			gw, err := NewToolGateway(GatewayConfig{
				Governor: gov, Invoker: inv,
				Session: SessionID(fmt.Sprintf("ses-%d", n)),
				Actor:   governance.ActorID("voice-agent"),
			})
			if err != nil {
				errs <- fmt.Errorf("session %d: %w", n, err)
				return
			}

			intent := testIntent()
			intent.Resource = fmt.Sprintf("account.%d", n)

			_, invokeErr := gw.Invoke(context.Background(), TurnID("t"), intent)

			switch {
			case allow && invokeErr != nil:
				errs <- fmt.Errorf("session %d was allowed but failed: %w", n, invokeErr)
			case !allow && invokeErr == nil:
				errs <- fmt.Errorf("session %d was denied but executed", n)
			}

			if allow {
				req := inv.lastRequest(t)
				if req.Auth.Resource() != intent.Resource {
					errs <- fmt.Errorf("session %d executed against %q, want %q",
						n, req.Auth.Resource(), intent.Resource)
				}
				if req.Session != SessionID(fmt.Sprintf("ses-%d", n)) {
					errs <- fmt.Errorf("session %d's request names %q", n, req.Session)
				}
			} else if got := inv.calls.Load(); got != 0 {
				errs <- fmt.Errorf("denied session %d invoked a tool %d times", n, got)
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestGoverned_NoInvokerWiredIsNotASilentSuccess(t *testing.T) {
	t.Parallel()

	// Governance allowing an action this deployment cannot perform must be an
	// error, not a quiet no-op that looks like it worked.
	gov := &recordingGovernor{answer: scriptedDecision{outcome: governance.OutcomeAllow}}
	gw := newGateway(t, gov, nil)

	_, err := gw.Invoke(context.Background(), TurnID("t"), testIntent())
	if err == nil {
		t.Fatal("an action with no invoker reported success")
	}
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Errorf("want ErrProviderUnavailable, got %v", err)
	}
}

func TestGoverned_RefusesAnIncompleteIntent(t *testing.T) {
	t.Parallel()

	inv := &recordingInvoker{forbidden: true, t: t}
	gov := &recordingGovernor{answer: scriptedDecision{outcome: governance.OutcomeAllow}}
	gw := newGateway(t, gov, inv)

	for _, intent := range []ToolIntent{
		{Resource: "account.balance"},
		{Operation: "lookup"},
		{},
	} {
		if _, err := gw.Invoke(context.Background(), TurnID("t"), intent); err == nil {
			t.Errorf("an incomplete intent %+v was accepted", intent)
		}
	}
	if got := gov.calls.Load(); got != 0 {
		t.Errorf("governance was asked about %d malformed intents", got)
	}
}
