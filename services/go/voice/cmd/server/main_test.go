package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/packages/go/voiceintel"
)

// Phase 14 T2 — service wiring tests.
//
// These exercise buildService, the SAME function run() calls. Nothing here
// constructs a voiceintel.Bridge directly: doing so would prove that the
// package can be instantiated, which was never in doubt, rather than that the
// service actually wires it.

// testConfig returns a valid configuration. Ports are set but never bound —
// no test here calls svc.Run, so no listener is opened.
func testConfig() platform.ServiceConfig {
	return platform.ServiceConfig{
		Name:              serviceName,
		Environment:       "development",
		Region:            "in-south-1",
		Version:           "test",
		HTTPPort:          8080,
		HealthPort:        8081,
		LogLevel:          "info",
		LogFormat:         "json",
		ShutdownTimeout:   25 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// build runs the production construction path.
func build(t *testing.T) (*platform.Service, *voiceIntelligence) {
	t.Helper()
	cfg := testConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test configuration is invalid: %v", err)
	}
	svc, vi, err := buildService(cfg, testLogger(), platform.NewHealth(cfg))
	if err != nil {
		t.Fatalf("buildService: %v", err)
	}
	return svc, vi
}

// 1, 2, 5 — construction succeeds and the runner owns a real bridge.
func TestWiring_ServiceConstructsAndOwnsTheBridge(t *testing.T) {
	t.Parallel()

	svc, vi := build(t)
	if svc == nil {
		t.Fatal("nil service")
	}
	if vi == nil {
		t.Fatal("nil intelligence runner")
	}
	if vi.Bridge() == nil {
		t.Fatal("runner holds no bridge")
	}
	// The runner is what satisfies the lifecycle contract, and it is the owner
	// of the bridge — not a global, not a package var.
	var _ platform.Runner = vi
	if got := vi.Name(); got != "voice-intelligence" {
		t.Errorf("Name() = %q, want %q", got, "voice-intelligence")
	}
}

// 3, 4, 6 — the engine really has the real classifier.
//
// Asserted behaviourally, which is the only assertion that cannot be faked: an
// engine built without a classifier resolves EVERY utterance to the fallback
// intent (conversation/intent.go:277). A non-fallback intent is therefore proof
// that intent.Classifier is present and wired, and it is proof no test double
// or nil classifier was substituted.
func TestWiring_EngineHasTheRealClassifierNotTheFallback(t *testing.T) {
	t.Parallel()

	_, vi := build(t)

	if vi.Bridge().Engine() == nil {
		t.Fatal("bridge exposes no conversation engine")
	}

	p, err := vi.Bridge().Planner("wire-real", "")
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}
	// The frozen turn-taking floor: the agent holds it until the greeting
	// completes, so an utterance before that is queued rather than planned.
	if _, err := p.Handle(conversation.Event{Kind: conversation.EventStart}); err != nil {
		t.Fatalf("EventStart: %v", err)
	}
	if _, err := p.Handle(conversation.Event{
		Kind: conversation.EventGreetingComplete}); err != nil {
		t.Fatalf("EventGreetingComplete: %v", err)
	}

	plan, err := p.Handle(conversation.Event{
		Kind: conversation.EventUtterance,
		Utterance: conversation.Utterance{
			Text: "please call me back on 9876543210", ASRConfidence: 0.95},
		Party: conversation.PartyCaller,
	})
	if err != nil {
		t.Fatalf("Handle utterance: %v", err)
	}

	if plan.Intent == conversation.IntentFallback {
		t.Fatal("intent resolved to the fallback: the engine has no real " +
			"classifier, which is exactly the pre-Phase-13 behaviour this " +
			"wiring exists to end")
	}
	if plan.Intent != "request_callback" {
		t.Errorf("intent = %q, want %q", plan.Intent, "request_callback")
	}
	if plan.Action != conversation.ActionRespond {
		t.Errorf("action = %v, want ActionRespond", plan.Action)
	}
}

// 7 — lifecycle: Run blocks until cancellation, Shutdown is clean and
// repeatable.
func TestWiring_RunnerLifecycleIsCleanAndDoesNotPanic(t *testing.T) {
	t.Parallel()

	_, vi := build(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- vi.Run(ctx) }()

	// Run must NOT return before cancellation: platform.Service treats any
	// runner exit as a reason to shut the whole process down.
	select {
	case err := <-done:
		t.Fatalf("Run returned before cancellation (err=%v); the service would "+
			"have shut itself down at startup", err)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v after cancellation, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	// Shutdown is a no-op and must stay safe to call — including twice, since
	// a double close is the classic shutdown defect.
	for i := 0; i < 2; i++ {
		if err := vi.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown call %d returned %v, want nil", i+1, err)
		}
	}
}

// 8 — registering the runner does not disturb config or health.
func TestWiring_HealthAndConfigLifecycleIntact(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	health := platform.NewHealth(cfg)
	svc, _, err := buildService(cfg, testLogger(), health)
	if err != nil {
		t.Fatalf("buildService: %v", err)
	}

	if svc.Health() != health {
		t.Error("service does not hold the health instance it was given")
	}
	if svc.Config().Name != serviceName {
		t.Errorf("config name = %q, want %q", svc.Config().Name, serviceName)
	}
	if svc.Config().HealthPort != cfg.HealthPort {
		t.Error("health port changed during wiring")
	}
	if svc.Logger() == nil {
		t.Error("service has no logger")
	}
}

// 9, 10 — structural: no listener, no global mutable state, no session map.
//
// Source-level rather than behavioural, because the claim is about what the
// file CANNOT do, not about what one run happened not to do.
func TestWiring_NoListenerNoGlobalStateNoSessionMap(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing main.go: %v", err)
	}

	// No new listener or server of any kind.
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		switch path {
		case "net", "net/http", "net/url", "database/sql", "os/exec":
			t.Errorf("main.go imports %q; the wiring must open no listener, "+
				"make no network call and start no process", path)
		}
	}
	for _, banned := range []string{"NewHTTPRunner", "ListenAndServe", "http.Server"} {
		if strings.Contains(sourceOf(t), banned) {
			t.Errorf("main.go references %q; no second listener is permitted", banned)
		}
	}

	// No package-level mutable state. Consts and compile-time interface
	// assertions (`var _ T = ...`) are immutable and permitted.
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, id := range vs.Names {
				if id.Name == "_" {
					continue // interface assertion, not state
				}
				t.Errorf("main.go declares package-level var %q; the bridge must "+
					"be reachable only through the registered runner", id.Name)
			}
		}
	}

	// No session registry: nothing in this file may hold a map.
	ast.Inspect(f, func(n ast.Node) bool {
		if _, ok := n.(*ast.MapType); ok {
			t.Error("main.go declares a map type; the service keeps no session " +
				"registry — session identity is conversation.ConversationID " +
				"passed to Bridge.Planner")
		}
		return true
	})
}

func sourceOf(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	return string(b)
}

// Sessions are created through the bridge, and each gets its own context —
// the service adds no state of its own between them.
func TestWiring_SessionsComeFromTheBridgeAndStayIsolated(t *testing.T) {
	t.Parallel()

	_, vi := build(t)
	b := vi.Bridge()

	for _, id := range []conversation.ConversationID{"wire-a", "wire-b"} {
		p, err := b.Planner(id, "")
		if err != nil {
			t.Fatalf("Planner(%s): %v", id, err)
		}
		if _, err := p.Handle(conversation.Event{Kind: conversation.EventStart}); err != nil {
			t.Fatalf("%s EventStart: %v", id, err)
		}
	}

	convA, okA := b.Conversation("wire-a")
	convB, okB := b.Conversation("wire-b")
	if !okA || !okB {
		t.Fatal("bridge did not retain both sessions")
	}
	if convA == convB {
		t.Fatal("both session ids resolved to the same conversation")
	}
	if convA.Context() == convB.Context() {
		t.Fatal("both sessions share one context engine")
	}

	if err := convA.Context().Set(conversation.Entry{
		Key: "marker", Value: "from-a",
		Scope: conversation.ScopeConversation, Source: "t2",
	}); err != nil {
		t.Fatal(err)
	}
	if _, seen := convB.Context().Get(conversation.ScopeConversation, "marker"); seen {
		t.Error("session B observed session A's context through the service wiring")
	}
}

// The bridge the service registered is the one a caller reaches — there is no
// second bridge hiding anywhere.
func TestWiring_OnlyOneBridgeExists(t *testing.T) {
	t.Parallel()

	_, vi1 := build(t)
	_, vi2 := build(t)

	// Two independent service constructions must yield independent bridges;
	// a shared global would make them identical.
	if vi1.Bridge() == vi2.Bridge() {
		t.Fatal("two service constructions returned the same bridge: a " +
			"package-level singleton exists")
	}
	// And within one construction the runner's bridge is stable.
	if vi1.Bridge() != vi1.Bridge() {
		t.Fatal("Bridge() is not stable across calls")
	}
	var _ *voiceintel.Bridge = vi1.Bridge()
}
