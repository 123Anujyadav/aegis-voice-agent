package telephony

import (
	"context"

	"testing"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Benchmarks for the telephony hot paths. Every number in
// docs/telephony/PERFORMANCE.md comes from these, on the machine named there.
//
// The runtime is in-process and carries no media, so these measure lifecycle
// bookkeeping: state transitions, registry operations, admission decisions and
// event construction. Carrier latency is a provider adapter's cost and is not
// modelled — the FakeProvider returns immediately.

func benchHarness(b *testing.B) *Harness {
	b.Helper()
	cfg := TestConfig()
	cfg.MaxConcurrentCalls = 1_000_000
	cfg.MaxCallsPerProvider = 0
	cfg.AdmissionHighWater = 1.0

	h, err := NewHarness(WithHarnessConfig(cfg))
	if err != nil {
		b.Fatal(err)
	}
	if _, err := h.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _, _ = h.Stop(context.Background()) })
	return h
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// BenchmarkFullCallLifecycle is the headline number: one call from arrival to
// teardown, through all five transitions plus admission and release.
func BenchmarkFullCallLifecycle(b *testing.B) {
	h := benchHarness(b)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sess, err := h.Connected(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if err := h.Coordinator.End(ctx, sess.ID(), "caller_hung_up"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFullCallLifecycleParallel(b *testing.B) {
	h := benchHarness(b)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			sess, err := h.Connected(ctx)
			if err != nil {
				b.Error(err)
				return
			}
			if err := h.Coordinator.End(ctx, sess.ID(), "caller_hung_up"); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

// BenchmarkTransition isolates one state change: FSM move, history append,
// metric, event.
func BenchmarkTransition(b *testing.B) {
	h := benchHarness(b)
	ctx := context.Background()
	l := h.Runtime.Lifecycle()

	sess, err := h.Connected(ctx)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Hold and unhold: two declared transitions that publish no event, so
		// this measures the state machine rather than the publisher.
		if err := l.Hold(ctx, sess.ID()); err != nil {
			b.Fatal(err)
		}
		if err := l.Unhold(ctx, sess.ID()); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkTransitionRefused measures the cost of an undeclared transition,
// which is what a duplicate carrier callback produces.
func BenchmarkTransitionRefused(b *testing.B) {
	h := benchHarness(b)
	ctx := context.Background()

	sess, err := h.Connected(ctx)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sess.Transition(StateIncoming, "impossible")
	}
}

func BenchmarkDispatchNotApplicable(b *testing.B) {
	h := benchHarness(b)
	ctx := context.Background()

	sess, err := h.Connected(ctx)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// The common carrier behaviour: a late or duplicated callback. It must
		// be cheap, because a chatty carrier sends many.
		_, _ = h.Dispatcher.Dispatch(ctx, sess.ID(), StateRinging, "late_callback")
	}
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

func BenchmarkRegistryRegister(b *testing.B) {
	h := benchHarness(b)
	reg := NewCallRegistry()
	cc := h.Inbound()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := reg.Create(cc, h.Clock); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRegistryGet(b *testing.B) {
	h := benchHarness(b)
	reg := NewCallRegistry()

	var ids []CallID
	for i := 0; i < 10_000; i++ {
		s, err := reg.Create(h.Inbound(), h.Clock)
		if err != nil {
			b.Fatal(err)
		}
		ids = append(ids, s.ID())
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := reg.Get(ids[i%len(ids)]); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRegistryGetParallel(b *testing.B) {
	h := benchHarness(b)
	reg := NewCallRegistry()

	var ids []CallID
	for i := 0; i < 10_000; i++ {
		s, err := reg.Create(h.Inbound(), h.Clock)
		if err != nil {
			b.Fatal(err)
		}
		ids = append(ids, s.ID())
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			i++
			if _, err := reg.Get(ids[i%len(ids)]); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

// BenchmarkRegistryLen guards the O(1) claim.
//
// Deriving the live count by walking 64 shards would put a shard-lock sweep on
// the admission path — the shape of defect Phase 10F paid 45x for.
func BenchmarkRegistryLen(b *testing.B) {
	h := benchHarness(b)
	reg := NewCallRegistry()
	for i := 0; i < 10_000; i++ {
		if _, err := reg.Create(h.Inbound(), h.Clock); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = reg.Len()
	}
}

func BenchmarkRegistryByState(b *testing.B) {
	h := benchHarness(b)
	reg := NewCallRegistry()
	for i := 0; i < 5_000; i++ {
		if _, err := reg.Create(h.Inbound(), h.Clock); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = reg.ByState()
	}
}

// ---------------------------------------------------------------------------
// Scheduler
// ---------------------------------------------------------------------------

func BenchmarkAdmitAndRelease(b *testing.B) {
	h := benchHarness(b)
	sched := h.Runtime.Scheduler()
	provider := h.Provider.ID()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if d := sched.Admit(provider); d.Admitted {
			sched.Release(provider)
		}
	}
}

func BenchmarkAdmitAndReleaseParallel(b *testing.B) {
	h := benchHarness(b)
	sched := h.Runtime.Scheduler()
	provider := h.Provider.ID()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if d := sched.Admit(provider); d.Admitted {
				sched.Release(provider)
			}
		}
	})
}

// BenchmarkAdmitShedPath measures the refusal, which is the path that matters
// under overload — it must be cheap or the runtime spends its remaining
// capacity refusing calls.
func BenchmarkAdmitShedPath(b *testing.B) {
	cfg := TestConfig()
	cfg.MaxConcurrentCalls = 1
	cfg.MaxCallsPerProvider = 0
	cfg.AdmissionHighWater = 1.0

	h, err := NewHarness(WithHarnessConfig(cfg))
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	if _, err := h.Start(ctx); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _, _ = h.Stop(ctx) })

	if _, err := h.BeginInbound(ctx); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Runtime.Scheduler().Admit(h.Provider.ID())
	}
}

// ---------------------------------------------------------------------------
// Session
// ---------------------------------------------------------------------------

func BenchmarkSnapshot(b *testing.B) {
	h := benchHarness(b)
	ctx := context.Background()

	sess, err := h.Connected(ctx)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sess.Snapshot()
	}
}

func BenchmarkRestore(b *testing.B) {
	h := benchHarness(b)
	ctx := context.Background()

	sess, err := h.Connected(ctx)
	if err != nil {
		b.Fatal(err)
	}
	snap := sess.Snapshot()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Restore(snap, h.Clock); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkContextClone(b *testing.B) {
	h := benchHarness(b)
	cc := h.Inbound()
	cc.Metadata = map[string]string{"a": "1", "b": "2", "c": "3"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cc.Clone()
	}
}

func BenchmarkContextValidate(b *testing.B) {
	h := benchHarness(b)
	cc := h.Inbound()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := cc.Validate(); err != nil {
			b.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------------------
// Identifiers and events
// ---------------------------------------------------------------------------

// BenchmarkNewCallID measures the crypto/rand cost paid once per call.
func BenchmarkNewCallID(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewCallID()
	}
}

func BenchmarkEventPublish(b *testing.B) {
	p := NewRecordingPublisher()
	ctx := context.Background()
	e := Event{
		Type: EventCallConnected, Call: NewCallID(), Session: NewSessionID(),
		Correlation: NewCorrelationID(), From: StateAccepted, To: StateConnected,
		Reason: "media_established", Direction: DirectionInbound,
		Channel: ChannelPSTN, Provider: "carrier-1", Tags: []string{"unknown-caller"},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := p.Publish(ctx, e); err != nil {
			b.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------------------
// Maintenance
// ---------------------------------------------------------------------------

// BenchmarkSweep measures one maintenance pass over a populated runtime. It
// runs on a ticker, so its cost bounds how often the runtime can afford to
// enforce a deadline.
func BenchmarkSweep(b *testing.B) {
	h := benchHarness(b)
	ctx := context.Background()

	for i := 0; i < 2_000; i++ {
		if _, err := h.Connected(ctx); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Runtime.Sweep(ctx)
	}
}

func BenchmarkSnapshotAll(b *testing.B) {
	h := benchHarness(b)
	ctx := context.Background()

	for i := 0; i < 1_000; i++ {
		if _, err := h.Connected(ctx); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := h.Runtime.snapshotAll(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMetricsSnapshot measures the scrape path.
func BenchmarkMetricsSnapshot(b *testing.B) {
	h := benchHarness(b)
	ctx := context.Background()

	for i := 0; i < 100; i++ {
		sess, err := h.Connected(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if err := h.Coordinator.End(ctx, sess.ID(), "caller_hung_up"); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Metrics.Snapshot()
	}
}

// BenchmarkClockResolution records the injected clock's granularity, so a
// reader of the latency figures can tell which of them the clock can see.
//
// The Phase 10F lesson: on Windows, time.Now() resolves to roughly 520 µs, and
// every per-operation latency below that reads as zero.
func BenchmarkClockResolution(b *testing.B) {
	clock := rt.SystemClock{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = clock.Now()
	}
}

// BenchmarkHistoryAppendAtCap measures the bounded-history path, which a
// flapping call takes on every transition once it has run long enough.
func BenchmarkHistoryAppendAtCap(b *testing.B) {
	h := benchHarness(b)
	ctx := context.Background()

	sess, err := h.Connected(ctx)
	if err != nil {
		b.Fatal(err)
	}
	l := h.Runtime.Lifecycle()
	for i := 0; i < maxHistory; i++ {
		if err := l.Hold(ctx, sess.ID()); err != nil {
			b.Fatal(err)
		}
		if err := l.Unhold(ctx, sess.ID()); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := l.Hold(ctx, sess.ID()); err != nil {
			b.Fatal(err)
		}
		if err := l.Unhold(ctx, sess.ID()); err != nil {
			b.Fatal(err)
		}
	}
}
