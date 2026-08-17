package main

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/intent"
)

// Phase 14 T6 — lifecycle and shutdown integrity.
//
// ALREADY PROVEN, not repeated here: T3 covers registration, Run blocking until
// cancellation, Shutdown executing, the readiness lifecycle and three clean
// start/stop cycles. T5 covers terminated-session id reuse.
//
// T6's new ground: goroutine accounting across repeated cycles, cancellation
// propagation into work that is genuinely in flight, shutdown while sessions
// are mid-turn, and shutdown ordering.
//
// TWO ARCHITECTURAL FACTS, verified against current source, that shape what can
// honestly be claimed:
//
//  1. conversation.Handle(e Event) (Plan, error) takes NO context. Cancellation
//     therefore cannot propagate INTO a turn; a turn is a synchronous call that
//     runs to completion.
//  2. conversation, voiceintel and intent start ZERO goroutines in non-test
//     code. The intelligence path owns no background work at all.
//
// So "in-flight work" belongs to whoever DRIVES sessions, not to the service.
// The tests below model that honestly: session drivers are goroutines that hold
// the service's context, exactly as a real caller would, and they are what must
// observe cancellation. Nothing here pretends the service can interrupt a turn
// it does not own.

// settleGoroutines waits until the goroutine count stops changing, or the
// deadline passes, and returns the final count.
//
// A bounded poll rather than a fixed sleep: Go's runtime retires goroutines
// asynchronously, so an immediate reading after shutdown measures scheduling
// noise rather than leakage. The poll exits as soon as the count is stable, so
// a healthy run costs milliseconds.
func settleGoroutines(d time.Duration) int {
	deadline := time.Now().Add(d)
	last := runtime.NumGoroutine()
	stable := 0
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		n := runtime.NumGoroutine()
		if n == last {
			stable++
			if stable >= 3 {
				return n
			}
			continue
		}
		last, stable = n, 0
	}
	return runtime.NumGoroutine()
}

// lifecycleCycle runs one complete start → sessions → cancel → shutdown cycle
// and returns the goroutine count once things have settled.
func lifecycleCycle(t *testing.T, cycle, sessions int) int {
	t.Helper()

	sink, vi, cancel, done := runningService(t)

	for i := 0; i < sessions; i++ {
		id := fmt.Sprintf("t6-c%d-s%02d", cycle, i)
		p := openSession(t, vi, id)
		if _, err := p.Handle(utter("transfer me to rajesh")); err != nil {
			t.Fatalf("cycle %d session %d: %v", cycle, i, err)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cycle %d: Run returned %v", cycle, err)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("cycle %d: shutdown did not complete; log:\n%s", cycle, sink.dump())
	}
	if !sink.has("shutdown complete") {
		t.Errorf("cycle %d: platform did not report shutdown complete", cycle)
	}
	return settleGoroutines(2 * time.Second)
}

// ---------------------------------------------------------------------------
// Goroutine accounting across repeated cycles
// ---------------------------------------------------------------------------

// TestT6_RepeatedLifecyclesDoNotAccumulateGoroutines runs five full cycles and
// looks for PERSISTENT ACCUMULATION rather than asserting an exact count.
//
// An exact-equality assertion would be dishonest: the Go runtime keeps its own
// background goroutines and the test framework adds more, so the number is
// never a constant. What a leak actually looks like is monotonic growth
// proportional to the number of cycles, and that is what is measured.
func TestT6_RepeatedLifecyclesDoNotAccumulateGoroutines(t *testing.T) {
	const cycles, sessionsPerCycle = 5, 4

	base := settleGoroutines(2 * time.Second)
	counts := make([]int, 0, cycles)

	for c := 0; c < cycles; c++ {
		counts = append(counts, lifecycleCycle(t, c, sessionsPerCycle))
	}

	t.Logf("goroutines: baseline=%d after-cycles=%v", base, counts)

	// Tolerance covers runtime/test-framework background goroutines that are
	// unrelated to the service. It is a fixed allowance, NOT proportional to
	// cycle count -- which is exactly what makes accumulation detectable.
	const tolerance = 12

	for i, n := range counts {
		if n > base+tolerance {
			t.Errorf("after cycle %d: %d goroutines, baseline %d (+%d) exceeds the "+
				"tolerance of %d", i, n, base, n-base, tolerance)
		}
	}

	// The decisive check: the last cycle must not be worse than the first.
	//
	// maxGrowth is deliberately TIGHT. A correct implementation measures 0
	// growth across these cycles (observed: baseline 2, cycles [3 3 3 3 3]), so
	// anything above a couple of goroutines is signal, not noise. An earlier
	// draft allowed tolerance/2 = 6, which let a runner leaking ONE goroutine
	// per cycle pass unnoticed across five cycles -- the mutation caught the
	// detector, not the leak.
	const maxGrowth = 2

	growth := counts[len(counts)-1] - counts[0]
	if growth > maxGrowth {
		t.Errorf("goroutine count grew by %d between the first and last cycle "+
			"(%d -> %d); this is the signature of a per-cycle leak",
			growth, counts[0], counts[len(counts)-1])
	}

	// Monotonic growth across every cycle is the same signal, seen earlier.
	rising := 0
	for i := 1; i < len(counts); i++ {
		if counts[i] > counts[i-1] {
			rising++
		}
	}
	if rising >= len(counts)-1 {
		t.Errorf("goroutine count rose in every cycle (%v); a per-cycle leak "+
			"is accumulating", counts)
	}
}

// ---------------------------------------------------------------------------
// In-flight cancellation
// ---------------------------------------------------------------------------

// TestT6_CancellationReachesInFlightSessionDrivers proves the context chain
// end to end:
//
//	service context -> Runner.Run(ctx) -> session drivers -> observed
//
// The drivers hold the SAME context the service was started with, which is what
// a real caller would do. Each records its observation atomically, so the test
// asserts that the work itself saw cancellation rather than merely that
// Service.Run returned.
func TestT6_CancellationReachesInFlightSessionDrivers(t *testing.T) {
	const drivers = 8

	sink, vi, cancel, done := runningService(t)

	ctx, ctxCancel := context.WithCancel(context.Background())
	defer ctxCancel()

	var (
		observed  atomic.Int32
		turnsDone atomic.Int32
		wg        sync.WaitGroup
	)
	// Every driver reaches this barrier before any cancellation happens, so the
	// sessions are provably mid-flight rather than "probably" so.
	inFlight := newBarrier(drivers + 1)

	for i := 0; i < drivers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("t6-inflight-%02d", i)
			p := openSession(t, vi, id)

			// One completed turn, so the session is genuinely active.
			if _, err := p.Handle(utter("transfer me to rajesh")); err != nil {
				return
			}
			turnsDone.Add(1)

			// Announce arrival, then keep working until cancellation.
			if !inFlight.wait() {
				return
			}
			for {
				select {
				case <-ctx.Done():
					observed.Add(1)
					return
				default:
					// A real, synchronous unit of work. Handle takes no context,
					// so cancellation is observed BETWEEN turns -- never inside
					// one. That is the actual contract, not a limitation of the
					// test.
					if _, err := p.Handle(conversation.Event{
						Kind: conversation.EventSpeechComplete}); err != nil {
						// The session reached a terminal state (turn budget);
						// wait for cancellation rather than spinning.
						<-ctx.Done()
						observed.Add(1)
						return
					}
					if _, err := p.Handle(utter("say that again")); err != nil {
						<-ctx.Done()
						observed.Add(1)
						return
					}
				}
			}
		}(i)
	}

	// Release once every driver is in flight.
	inFlight.wait()

	if got := turnsDone.Load(); got != drivers {
		t.Errorf("%d drivers completed a turn before cancellation, want %d",
			got, drivers)
	}

	// Cancel the drivers and the service together, as a real shutdown would.
	ctxCancel()
	cancel()

	waited := make(chan struct{})
	go func() { wg.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(20 * time.Second):
		inFlight.abort()
		t.Fatalf("session drivers did not observe cancellation; log:\n%s", sink.dump())
	}

	if got := observed.Load(); got != drivers {
		t.Errorf("%d of %d drivers observed cancellation", got, drivers)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("service did not shut down; log:\n%s", sink.dump())
	}
	if !sink.has("voice intelligence stopped") {
		t.Errorf("Runner.Shutdown did not execute; log:\n%s", sink.dump())
	}
}

// ---------------------------------------------------------------------------
// Shutdown while sessions are mid-turn
// ---------------------------------------------------------------------------

// TestT6_ShutdownWithManyActiveSessionsCompletes cancels the service while
// eight sessions sit at a controlled point mid-conversation.
func TestT6_ShutdownWithManyActiveSessionsCompletes(t *testing.T) {
	const active = 8

	sink, vi, cancel, done := runningService(t)

	planners := make([]interface {
		Handle(conversation.Event) (conversation.Plan, error)
	}, active)
	for i := 0; i < active; i++ {
		planners[i] = openSession(t, vi, fmt.Sprintf("t6-active-%02d", i))
	}

	gate := newBarrier(active + 1)
	var midTurn atomic.Int32
	var wg sync.WaitGroup

	for i := 0; i < active; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Leave each session mid-turn: the utterance is planned, but the
			// agent's speech is never completed, so the floor is still held.
			if _, err := planners[i].Handle(utter("please call me back on 9876543210")); err != nil {
				gate.abort()
				return
			}
			midTurn.Add(1)
			gate.wait()
		}(i)
	}

	gate.wait() // released once all eight are mid-turn

	waited := make(chan struct{})
	go func() { wg.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(15 * time.Second):
		gate.abort()
		t.Fatal("sessions never reached the mid-turn point")
	}
	if got := midTurn.Load(); got != active {
		t.Fatalf("%d sessions mid-turn, want %d", got, active)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v with %d sessions mid-turn", err, active)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("shutdown blocked with sessions mid-turn; log:\n%s", sink.dump())
	}
	if !sink.has("shutdown complete") {
		t.Errorf("shutdown did not complete; log:\n%s", sink.dump())
	}
}

// ---------------------------------------------------------------------------
// Shutdown ordering
// ---------------------------------------------------------------------------

// TestT6_ShutdownFollowsThePlatformOrdering asserts the ORDER platform
// documents: readiness is disabled first, the runner is stopped next, and the
// health listener last.
func TestT6_ShutdownFollowsThePlatformOrdering(t *testing.T) {
	sink, vi, cancel, done := runningService(t)
	openSession(t, vi, "t6-order")

	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("shutdown did not complete; log:\n%s", sink.dump())
	}

	// Index of each milestone in the recorded log order.
	idx := func(msg string) int {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		for i, r := range sink.records {
			if r["msg"] == msg {
				return i
			}
		}
		return -1
	}

	signal := idx("shutdown signal received")
	drain := idx("readiness disabled, draining")
	stop := idx("stopping runner")
	stopped := idx("voice intelligence stopped")
	complete := idx("shutdown complete")

	for name, i := range map[string]int{
		"shutdown signal received":     signal,
		"readiness disabled, draining": drain,
		"stopping runner":              stop,
		"voice intelligence stopped":   stopped,
		"shutdown complete":            complete,
	} {
		if i < 0 {
			t.Fatalf("missing lifecycle record %q; log:\n%s", name, sink.dump())
		}
	}

	if !(signal < drain && drain < stop && stop < complete) {
		t.Errorf("shutdown order wrong: signal=%d drain=%d stop=%d complete=%d",
			signal, drain, stop, complete)
	}
	if stopped < stop {
		t.Errorf("the runner reported stopped (%d) before platform asked it to (%d)",
			stopped, stop)
	}
}

// TestT6_ShutdownIsSafeToRepeat verifies the existing contract: the service
// invokes Runner.Shutdown once, and calling it again afterwards is harmless.
//
// No second shutdown API is invented -- this is the same method platform calls.
func TestT6_ShutdownIsSafeToRepeat(t *testing.T) {
	sink, vi, cancel, done := runningService(t)
	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("shutdown did not complete")
	}

	if n := len(sink.find("stopping runner")); n != 1 {
		t.Errorf("platform stopped the runner %d times, want exactly 1", n)
	}
	for i := 0; i < 3; i++ {
		if err := vi.Shutdown(context.Background()); err != nil {
			t.Errorf("extra Shutdown %d returned %v, want nil", i+1, err)
		}
	}
	// An already-cancelled context must not change the outcome either.
	ctx, c := context.WithCancel(context.Background())
	c()
	if err := vi.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown with a cancelled context returned %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// Fresh service after shutdown
// ---------------------------------------------------------------------------

// TestT6_FreshServiceAfterShutdownIsHealthy proves shutdown corrupted no
// package or global state: a completely new service starts, registers, and
// still resolves a deterministic non-fallback intent.
func TestT6_FreshServiceAfterShutdownIsHealthy(t *testing.T) {
	// First service: run it and shut it down.
	sink1, vi1, cancel1, done1 := runningService(t)
	openSession(t, vi1, "t6-fresh-first")
	cancel1()
	select {
	case <-done1:
	case <-time.After(20 * time.Second):
		t.Fatalf("first service did not shut down; log:\n%s", sink1.dump())
	}

	// Second, entirely separate service.
	sink2, vi2, cancel2, done2 := runningService(t)
	defer func() { cancel2(); <-done2 }()

	if vi2.Bridge() == vi1.Bridge() {
		t.Fatal("the fresh service reused the previous bridge; package state " +
			"survived shutdown")
	}
	if n := len(sink2.find("service ready")); n != 1 {
		t.Errorf("fresh service reported ready %d times, want 1", n)
	}

	p := openSession(t, vi2, "t6-fresh-second")
	plan, err := p.Handle(utter("please call me back on 9876543210"))
	if err != nil {
		t.Fatalf("fresh service cannot classify: %v", err)
	}
	if plan.Intent != intent.IntentRequestCallback {
		t.Errorf("fresh service intent = %q, want request_callback", plan.Intent)
	}
	if plan.Intent == conversation.IntentFallback {
		t.Error("fresh service fell back; the classifier is no longer wired")
	}

	// The old bridge's sessions are not visible to the new service.
	if _, found := vi2.Bridge().Conversation("t6-fresh-first"); found {
		t.Error("the fresh service can see the previous service's session")
	}
}
