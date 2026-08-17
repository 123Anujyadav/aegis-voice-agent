package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/platform"
)

// Phase 14 T3 — real service construction and registration.
//
// T2 proved buildService RETURNS correctly wired components. That leaves the
// next boundary unproven: is the runner actually registered with
// platform.Service, and does the production lifecycle reach it?
//
// platform.Service keeps `runners []Runner` private and exposes no accessor
// (Register / SetMetricsHandler / Config / Logger / Health / Run are the only
// exported methods). Rather than export internals or edit platform, these tests
// prove registration BEHAVIOURALLY, using the log records platform.Service
// already emits from Run:
//
//	"runner starting"  slog.String("runner", r.Name())   <- names our runner
//	"service ready"    slog.Int("runners", len(runners)) <- counts them
//	"stopping runner"  slog.String("runner", r.Name())   <- shutdown reached it
//
// Those records are produced by platform's own code path, so observing them is
// evidence that Register worked and that Run started what was registered.

// logSink captures structured log records for assertion. Safe for the
// concurrent writes platform.Service makes from its runner goroutines.
type logSink struct {
	mu      sync.Mutex
	records []map[string]any
	raw     strings.Builder
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw.Write(p)
	for _, line := range strings.Split(strings.TrimSpace(string(p)), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err == nil {
			s.records = append(s.records, rec)
		}
	}
	return len(p), nil
}

// find returns records whose "msg" equals msg.
func (s *logSink) find(msg string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, r := range s.records {
		if r["msg"] == msg {
			out = append(out, r)
		}
	}
	return out
}

func (s *logSink) has(msg string) bool { return len(s.find(msg)) > 0 }

func (s *logSink) dump() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.raw.String()
}

// waitFor polls until cond holds or the deadline passes. Used only to
// synchronise with the service's own startup logging; no assertion depends on
// how long it took.
func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// freePort reserves an ephemeral port and releases it, so the health listener
// has somewhere to bind without colliding with a parallel test.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return port
}

// runningService builds the service exactly as run() does, starts it, and
// returns once platform reports it ready. The caller cancels to stop it.
func runningService(t *testing.T) (*logSink, *voiceIntelligence, context.CancelFunc, <-chan error) {
	t.Helper()

	sink := &logSink{}
	logger := slog.New(slog.NewJSONHandler(sink, nil))

	cfg := testConfig()
	cfg.HTTPPort = freePort(t)
	cfg.HealthPort = freePort(t)
	// Short, so the drain delay (ShutdownTimeout/5) does not dominate the test.
	cfg.ShutdownTimeout = time.Second
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config invalid: %v", err)
	}

	svc, vi, err := buildService(cfg, logger, platform.NewHealth(cfg))
	if err != nil {
		t.Fatalf("buildService: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	if !waitFor(5*time.Second, func() bool { return sink.has("service ready") }) {
		cancel()
		t.Fatalf("service never reported ready; log:\n%s", sink.dump())
	}
	return sink, vi, cancel, done
}

// ---------------------------------------------------------------------------
// Registration through the production path
// ---------------------------------------------------------------------------

// TestT3_RunnerIsRegisteredWithPlatformService proves requirements 2 and 3:
// the runner reached platform.Service.Register, and exactly one did.
func TestT3_RunnerIsRegisteredWithPlatformService(t *testing.T) {
	sink, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	// platform logs the count of registered runners as it becomes ready.
	ready := sink.find("service ready")
	if len(ready) != 1 {
		t.Fatalf("expected one 'service ready' record, got %d", len(ready))
	}
	count, ok := ready[0]["runners"].(float64) // JSON numbers decode as float64
	if !ok {
		t.Fatalf("'service ready' has no runners count: %v", ready[0])
	}
	if int(count) != 1 {
		t.Errorf("platform reports %d registered runners, want exactly 1", int(count))
	}

	// platform names each runner it starts. This record is emitted by
	// platform's own goroutine, so it cannot be produced without registration.
	starts := sink.find("runner starting")
	if len(starts) != 1 {
		t.Fatalf("expected one 'runner starting' record, got %d; log:\n%s",
			len(starts), sink.dump())
	}
	if got := starts[0]["runner"]; got != vi.Name() {
		t.Errorf("platform started runner %v, want %q", got, vi.Name())
	}

	// And our runner's own Run body executed.
	if !sink.has("voice intelligence ready") {
		t.Errorf("the runner's Run body never executed; log:\n%s", sink.dump())
	}
}

// TestT3_LiveServiceResolvesANonFallbackIntent proves requirements 4-7 against
// a RUNNING service: the registered runner's bridge reaches the real
// classifier while the service lifecycle is active.
func TestT3_LiveServiceResolvesANonFallbackIntent(t *testing.T) {
	sink, vi, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	bridge := vi.Bridge()
	if bridge == nil {
		t.Fatal("registered runner holds no bridge")
	}
	if bridge.Engine() == nil {
		t.Fatal("bridge holds no conversation engine")
	}

	p, err := bridge.Planner("t3-live", "")
	if err != nil {
		t.Fatalf("Planner: %v", err)
	}
	for _, e := range []conversation.Event{
		{Kind: conversation.EventStart},
		{Kind: conversation.EventGreetingComplete},
	} {
		if _, err := p.Handle(e); err != nil {
			t.Fatalf("%v: %v", e.Kind, err)
		}
	}

	plan, err := p.Handle(conversation.Event{
		Kind: conversation.EventUtterance,
		Utterance: conversation.Utterance{
			Text: "transfer me to rajesh", ASRConfidence: 0.95},
		Party: conversation.PartyCaller,
	})
	if err != nil {
		t.Fatalf("utterance: %v", err)
	}

	if plan.Intent == conversation.IntentFallback {
		t.Fatalf("live service resolved to the fallback intent; the running "+
			"service is not reaching the classifier. log:\n%s", sink.dump())
	}
	if plan.Intent != "request_transfer" {
		t.Errorf("intent = %q, want request_transfer", plan.Intent)
	}
	if plan.Reason != "intent_accepted" {
		t.Errorf("reason = %q, want intent_accepted", plan.Reason)
	}
}

// TestT3_WithoutClassifierTheSameUtteranceFallsBack is the contrast that makes
// the previous test meaningful.
//
// It builds a conversation engine the way the repository did BEFORE Phase 13 —
// conversation.NewEngine with no classifier — and drives the identical
// utterance. If that also produced request_transfer, the earlier assertion
// would prove nothing about the wiring.
func TestT3_WithoutClassifierTheSameUtteranceFallsBack(t *testing.T) {
	t.Parallel()

	const utterance = "transfer me to rajesh"

	// Pre-Phase-13 shape: no WithClassifier.
	bare, err := conversation.NewEngine(conversation.DefaultConfig())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	conv, err := bare.Begin("t3-bare", "")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for _, e := range []conversation.Event{
		{Kind: conversation.EventStart},
		{Kind: conversation.EventGreetingComplete},
	} {
		if _, err := conv.Handle(e); err != nil {
			t.Fatalf("%v: %v", e.Kind, err)
		}
	}
	bareplan, err := conv.Handle(conversation.Event{
		Kind:      conversation.EventUtterance,
		Utterance: conversation.Utterance{Text: utterance, ASRConfidence: 0.95},
		Party:     conversation.PartyCaller,
	})
	if err != nil {
		t.Fatalf("bare utterance: %v", err)
	}

	if bareplan.Intent != conversation.IntentFallback {
		t.Fatalf("an engine with no classifier resolved %q to %q; the contrast "+
			"this test relies on does not hold", utterance, bareplan.Intent)
	}

	// Same utterance through the service's wiring.
	_, vi := build(t)
	p, err := vi.Bridge().Planner("t3-wired", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []conversation.Event{
		{Kind: conversation.EventStart},
		{Kind: conversation.EventGreetingComplete},
	} {
		if _, err := p.Handle(e); err != nil {
			t.Fatalf("%v: %v", e.Kind, err)
		}
	}
	wired, err := p.Handle(conversation.Event{
		Kind:      conversation.EventUtterance,
		Utterance: conversation.Utterance{Text: utterance, ASRConfidence: 0.95},
		Party:     conversation.PartyCaller,
	})
	if err != nil {
		t.Fatalf("wired utterance: %v", err)
	}

	if wired.Intent == bareplan.Intent {
		t.Fatalf("wired and unwired engines produced the same intent %q; the "+
			"service wiring makes no observable difference", wired.Intent)
	}
	t.Logf("no classifier -> %q ; service wiring -> %q", bareplan.Intent, wired.Intent)
}

// ---------------------------------------------------------------------------
// Lifecycle through the real service
// ---------------------------------------------------------------------------

// TestT3_ServiceLifecycleReachesTheRunnerOnShutdown proves requirements 8 and 9
// and the Step 6 lifecycle checks, through platform.Service rather than by
// calling the runner directly.
func TestT3_ServiceLifecycleReachesTheRunnerOnShutdown(t *testing.T) {
	sink, vi, cancel, done := runningService(t)

	// The runner must still be running: platform treats any runner exit as a
	// reason to shut the service down, and would have logged it.
	if sink.has("runner exited unexpectedly without error") {
		t.Fatalf("runner returned early; log:\n%s", sink.dump())
	}
	select {
	case err := <-done:
		t.Fatalf("service returned before cancellation (err=%v); log:\n%s",
			err, sink.dump())
	case <-time.After(100 * time.Millisecond):
	}

	cancel()

	var runErr error
	select {
	case runErr = <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("service did not return after cancellation; log:\n%s", sink.dump())
	}
	if runErr != nil {
		t.Errorf("Run returned %v after clean cancellation, want nil", runErr)
	}

	// platform's shutdown must have reached OUR runner, by name.
	stops := sink.find("stopping runner")
	if len(stops) != 1 {
		t.Fatalf("expected one 'stopping runner' record, got %d; log:\n%s",
			len(stops), sink.dump())
	}
	if got := stops[0]["runner"]; got != vi.Name() {
		t.Errorf("platform stopped %v, want %q", got, vi.Name())
	}
	if !sink.has("voice intelligence stopped") {
		t.Errorf("the runner's Shutdown body never executed; log:\n%s", sink.dump())
	}
	// Readiness was disabled as part of the drain.
	if !sink.has("readiness disabled, draining") {
		t.Errorf("readiness lifecycle did not run; log:\n%s", sink.dump())
	}

	// Shutdown remains safe to call again after the service already invoked it.
	if err := vi.Shutdown(context.Background()); err != nil {
		t.Errorf("post-service Shutdown returned %v, want nil (double close)", err)
	}
}

// TestT3_ServiceStartsExactlyOneListener confirms the wiring added no second
// listener: platform opens the health listener and nothing else announces one.
func TestT3_ServiceStartsExactlyOneListener(t *testing.T) {
	sink, _, cancel, done := runningService(t)
	defer func() { cancel(); <-done }()

	if n := len(sink.find("health listener starting")); n != 1 {
		t.Errorf("%d health listeners started, want exactly 1", n)
	}
	// Our runner announces readiness but must never announce a listener.
	for _, rec := range sink.find("voice intelligence ready") {
		for k := range rec {
			if strings.Contains(strings.ToLower(k), "port") ||
				strings.Contains(strings.ToLower(k), "addr") {
				t.Errorf("the intelligence runner reported %q; it must open no "+
					"listener", k)
			}
		}
	}
}

// TestT3_RepeatedServiceLifecyclesAreClean runs the whole start/stop cycle
// several times, which is where a leaked goroutine or a double-close would
// show up as a hang or a panic.
func TestT3_RepeatedServiceLifecyclesAreClean(t *testing.T) {
	for i := 0; i < 3; i++ {
		t.Run(fmt.Sprintf("cycle-%d", i), func(t *testing.T) {
			sink, vi, cancel, done := runningService(t)
			if vi.Bridge() == nil {
				t.Fatal("no bridge")
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("cycle %d: Run returned %v", i, err)
				}
			case <-time.After(15 * time.Second):
				t.Fatalf("cycle %d: service did not stop; log:\n%s", i, sink.dump())
			}
			if !sink.has("voice intelligence stopped") {
				t.Errorf("cycle %d: shutdown did not reach the runner", i)
			}
		})
	}
}
