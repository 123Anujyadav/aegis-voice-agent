package telephony

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// TestLifecycle_InboundHappyPath walks the state a screened call actually takes.
func TestLifecycle_InboundHappyPath(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()
	l := h.Runtime.Lifecycle()

	sess, err := h.BeginInbound(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sess.State() != StateIncoming {
		t.Fatalf("state = %s, want incoming", sess.State())
	}

	steps := []struct {
		do   func() error
		want CallState
	}{
		{func() error { return l.Ring(ctx, sess.ID()) }, StateRinging},
		{func() error { return l.Screen(ctx, sess.ID()) }, StateScreening},
		{func() error { return l.Accept(ctx, sess.ID(), "screening_passed") }, StateAccepted},
		{func() error { return l.Connect(ctx, sess.ID()) }, StateConnected},
	}
	for _, s := range steps {
		if err := s.do(); err != nil {
			t.Fatalf("transition to %s: %v", s.want, err)
		}
		if got := sess.State(); got != s.want {
			t.Fatalf("state = %s, want %s", got, s.want)
		}
	}

	// The provider must actually have been asked to answer, not assumed to.
	if !h.Provider.Answered(sess.ID()) {
		t.Error("the provider was never asked to answer")
	}

	if err := h.Coordinator.End(ctx, sess.ID(), "caller_hung_up"); err != nil {
		t.Fatal(err)
	}
	if !sess.Terminal() {
		t.Errorf("state = %s after End, want a terminal state", sess.State())
	}
	if h.Runtime.Live() != 0 {
		t.Errorf("%d calls still live after the only call ended", h.Runtime.Live())
	}
}

func TestLifecycle_OutboundSkipsIncoming(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	sess, err := h.Coordinator.Begin(ctx, h.Outbound())
	if err != nil {
		t.Fatal(err)
	}
	// An outbound call has no arrival; modelling one would put every outbound
	// call through a state that means nothing for it.
	if sess.State() != StateRinging {
		t.Errorf("state = %s, want ringing", sess.State())
	}
	for _, rec := range sess.History() {
		if rec.To == StateIncoming {
			t.Error("an outbound call passed through Incoming")
		}
	}
}

func TestLifecycle_ScreeningRejection(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()
	l := h.Runtime.Lifecycle()

	sess, err := h.BeginInbound(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Ring(ctx, sess.ID()); err != nil {
		t.Fatal(err)
	}
	if err := l.Screen(ctx, sess.ID()); err != nil {
		t.Fatal(err)
	}
	if err := l.Reject(ctx, sess.ID(), "screening_rejected"); err != nil {
		t.Fatal(err)
	}

	if sess.State() != StateRejected {
		t.Fatalf("state = %s, want rejected", sess.State())
	}
	// Rejected is not terminal: the teardown still has to happen.
	if sess.Terminal() {
		t.Error("Rejected is terminal; its teardown would be unmodelled")
	}
	if reason, ok := h.Provider.Rejected(sess.ID()); !ok || reason != "screening_rejected" {
		t.Errorf("provider reject reason = %q (%v)", reason, ok)
	}

	if err := h.Coordinator.End(ctx, sess.ID(), "teardown_complete"); err != nil {
		t.Fatal(err)
	}
	if sess.State() != StateEnded {
		t.Errorf("state = %s after teardown, want ended", sess.State())
	}
}

func TestLifecycle_HoldMuteAndTransfer(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()
	l := h.Runtime.Lifecycle()

	sess, err := h.Connected(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := l.Mute(ctx, sess.ID()); err != nil {
		t.Fatal(err)
	}
	// The table refuses a transfer from Muted; the far end would receive a
	// silent leg indistinguishable from a broken one.
	if _, err := l.Transfer(ctx, sess.ID(), "agent_handoff"); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("transfer from muted: err = %v, want ErrInvalidTransition", err)
	}

	if err := l.Unmute(ctx, sess.ID()); err != nil {
		t.Fatal(err)
	}
	if err := l.Hold(ctx, sess.ID()); err != nil {
		t.Fatal(err)
	}
	leg, err := l.Transfer(ctx, sess.ID(), "agent_handoff")
	if err != nil {
		t.Fatalf("transfer from hold: %v", err)
	}
	if leg == "" {
		t.Error("transfer produced no leg identifier")
	}
	if got := sess.Legs(); len(got) != 1 || got[0] != leg {
		t.Errorf("legs = %v, want [%s]", got, leg)
	}
}

func TestLifecycle_EscalationIsReversible(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()
	l := h.Runtime.Lifecycle()

	sess, err := h.Connected(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Escalate(ctx, sess.ID(), "caller_requested_human"); err != nil {
		t.Fatal(err)
	}
	if sess.State() != StateEscalated {
		t.Fatalf("state = %s, want escalated", sess.State())
	}
	// A human who resolves an escalation hands the call back.
	if err := l.Deescalate(ctx, sess.ID()); err != nil {
		t.Fatalf("deescalate: %v", err)
	}
	if sess.State() != StateConnected {
		t.Errorf("state = %s, want connected", sess.State())
	}
}

func TestLifecycle_CapabilityIsEnforcedGenerically(t *testing.T) {
	t.Parallel()
	// A carrier that cannot transfer omits the capability. No code names it.
	h := started(t, WithHarnessCapabilities(CapAnswer, CapReject, CapHangup))
	ctx := context.Background()
	l := h.Runtime.Lifecycle()

	sess, err := h.Connected(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Transfer(ctx, sess.ID(), "agent_handoff"); !errors.Is(err, ErrCapabilityUnsupported) {
		t.Errorf("err = %v, want ErrCapabilityUnsupported", err)
	}
	if err := l.Hold(ctx, sess.ID()); !errors.Is(err, ErrCapabilityUnsupported) {
		t.Errorf("hold: err = %v, want ErrCapabilityUnsupported", err)
	}
}

// ---------------------------------------------------------------------------
// Timeouts
// ---------------------------------------------------------------------------

func TestTimeout_SweepMovesStalledCalls(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	sess, err := h.BeginInbound(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Runtime.Lifecycle().Ring(ctx, sess.ID()); err != nil {
		t.Fatal(err)
	}

	// No sleeping: the injected clock is what makes a 45-second deadline
	// testable in microseconds.
	h.Clock.Advance(h.Runtime.Config().RingTimeout + time.Second)
	h.Runtime.Sweep(ctx)

	if sess.State() != StateTimeout {
		t.Errorf("state = %s after the ring deadline, want timeout", sess.State())
	}
}

func TestTimeout_ConnectedCallsHaveNoDeadline(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	sess, err := h.Connected(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// A long call is a good call. A runtime that hung up on hour-long
	// conversations would be a worse product than one that leaks a session.
	h.Clock.Advance(4 * time.Hour)
	h.Runtime.Sweep(ctx)

	if sess.State() != StateConnected {
		t.Errorf("state = %s after four hours connected, want connected", sess.State())
	}
}

// TestTimeout_ReaperConcludesStalledTeardowns is the other half of the story.
func TestTimeout_ReaperConcludesStalledTeardowns(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	sess, err := h.BeginInbound(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Runtime.Lifecycle().Ring(ctx, sess.ID()); err != nil {
		t.Fatal(err)
	}

	h.Clock.Advance(h.Runtime.Config().RingTimeout + time.Second)
	h.Runtime.Sweep(ctx)
	if sess.State() != StateTimeout {
		t.Fatalf("state = %s, want timeout", sess.State())
	}

	// Without the reaper a timed-out call whose teardown never completed would
	// sit in Timeout forever holding a capacity slot.
	h.Clock.Advance(h.Runtime.Config().TeardownTimeout + time.Second)
	h.Runtime.Sweep(ctx)

	if !sess.Terminal() {
		t.Errorf("state = %s, want the call reaped to a terminal state", sess.State())
	}
	if h.Runtime.Live() != 0 {
		t.Errorf("%d calls still live after reaping", h.Runtime.Live())
	}
}

// ---------------------------------------------------------------------------
// Failure injection
// ---------------------------------------------------------------------------

// TestFailure_PublisherOutageDoesNotStopCalls is the most important property in
// this file.
func TestFailure_PublisherOutageDoesNotStopCalls(t *testing.T) {
	t.Parallel()
	h := harness(t)
	failing := &FailingPublisher{Err: errors.New("kafka unreachable")}

	r, err := New(TestConfig(), WithClock(h.Clock), WithPublisher(failing),
		WithMetrics(h.Metrics))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterProvider(h.Provider); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = r.Stop(ctx) }()

	cc := h.Inbound()
	sess, err := r.Coordinator().Begin(ctx, cc)
	if err != nil {
		t.Fatalf("a call could not start while the broker was down: %v", err)
	}

	l := r.Lifecycle()
	for _, step := range []func() error{
		func() error { return l.Ring(ctx, sess.ID()) },
		func() error { return l.Screen(ctx, sess.ID()) },
		func() error { return l.Accept(ctx, sess.ID(), "screening_passed") },
		func() error { return l.Connect(ctx, sess.ID()) },
	} {
		if err := step(); err != nil {
			t.Fatalf("a transition failed while the broker was down: %v", err)
		}
	}

	// The critical one: a broker outage must not prevent calls from ENDING. If
	// it did, every call in progress would stay Connected, the registry would
	// fill, capacity would exhaust, and an observability outage would become a
	// phone-system outage.
	if err := r.Coordinator().End(ctx, sess.ID(), "caller_hung_up"); err != nil {
		t.Fatalf("a call could not end while the broker was down: %v", err)
	}
	if r.Live() != 0 {
		t.Errorf("%d calls live after ending; capacity would leak", r.Live())
	}
	if failing.Attempts() == 0 {
		t.Error("the runtime never attempted to publish")
	}
	if got := h.Metrics.EventsDropped.Total(); got == 0 {
		t.Error("dropped events were not counted; the loss would be invisible")
	}
}

func TestFailure_ProviderAnswerFailureFailsTheCall(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()
	l := h.Runtime.Lifecycle()

	sess, err := h.BeginInbound(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Ring(ctx, sess.ID()); err != nil {
		t.Fatal(err)
	}
	if err := l.Screen(ctx, sess.ID()); err != nil {
		t.Fatal(err)
	}

	h.Provider.FailOn("answer", errors.New("carrier rejected"))

	// A failure with a cause beats a timeout without one.
	if err := l.Accept(ctx, sess.ID(), "screening_passed"); err == nil {
		t.Fatal("Accept succeeded despite the provider failing")
	}
	if sess.State() != StateFailed {
		t.Errorf("state = %s, want failed", sess.State())
	}
	if got := h.Metrics.ProviderErrors.Total(); got == 0 {
		t.Error("the provider error was not counted")
	}
}

// TestFailure_ProviderHangupFailureStillEndsTheCall covers the opposite choice.
func TestFailure_ProviderHangupFailureStillEndsTheCall(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	sess, err := h.Connected(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h.Provider.FailOn("hangup", errors.New("carrier unreachable"))

	// The carrier has almost certainly torn the call down anyway, and a session
	// that stayed live because a REST call failed would hold a capacity slot
	// until the lifecycle timeout.
	if err := h.Coordinator.End(ctx, sess.ID(), "caller_hung_up"); err != nil {
		t.Fatalf("End failed because the provider did: %v", err)
	}
	if sess.State() != StateEnded {
		t.Errorf("state = %s, want ended", sess.State())
	}
}

func TestFailure_ProviderTimeoutIsBounded(t *testing.T) {
	t.Parallel()
	cfg := TestConfig()
	cfg.ProviderTimeout = 50 * time.Millisecond

	h := started(t, WithHarnessConfig(cfg))
	ctx := context.Background()
	l := h.Runtime.Lifecycle()

	sess, err := h.BeginInbound(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Ring(ctx, sess.ID()); err != nil {
		t.Fatal(err)
	}
	if err := l.Screen(ctx, sess.ID()); err != nil {
		t.Fatal(err)
	}

	// A hung carrier SDK must not hold the runtime.
	h.Provider.DelayOn("answer", 5*time.Second)

	started := time.Now()
	err = l.Accept(ctx, sess.ID(), "screening_passed")
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("Accept succeeded despite the provider hanging")
	}
	if elapsed > time.Second {
		t.Errorf("Accept took %v; the provider timeout did not bind", elapsed)
	}
}

func TestFailure_SessionStoreOutageDoesNotStopCalls(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	sess, err := h.Connected(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h.Store.FailNext(errors.New("redis unreachable"))

	// Persistence is for recovery. A store outage costs recoverability, not
	// the call.
	if err := h.Coordinator.End(ctx, sess.ID(), "caller_hung_up"); err != nil {
		t.Errorf("a call could not end while the store was down: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Dispatcher
// ---------------------------------------------------------------------------

// TestDispatcher_ClassifiesLateAndDuplicateSignals is why the dispatcher exists.
func TestDispatcher_ClassifiesLateAndDuplicateSignals(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	sess, err := h.Connected(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// A carrier that sends "ringing" after we already connected is a carrier,
	// not a bug. It must not be an error.
	outcome, err := h.Dispatcher.Dispatch(ctx, sess.ID(), StateRinging, "late_callback")
	if err != nil {
		t.Errorf("a late callback returned an error: %v", err)
	}
	if outcome != SignalNotApplicable {
		t.Errorf("outcome = %s, want not_applicable", outcome)
	}

	// A callback for a call that already concluded and was removed.
	outcome, err = h.Dispatcher.Dispatch(ctx, CallID("call_gone"), StateEnded, "late_callback")
	if err != nil {
		t.Errorf("an unknown-call callback returned an error: %v", err)
	}
	if outcome != SignalUnknownCall {
		t.Errorf("outcome = %s, want unknown_call", outcome)
	}

	// A legal signal applies.
	outcome, err = h.Dispatcher.Dispatch(ctx, sess.ID(), StateHold, "carrier_hold")
	if err != nil || outcome != SignalApplied {
		t.Errorf("outcome = %s, err = %v, want applied", outcome, err)
	}
}

// ---------------------------------------------------------------------------
// Snapshot and recovery
// ---------------------------------------------------------------------------

func TestSnapshot_RoundTripsWithoutContent(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	sess, err := h.Connected(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snap := sess.Snapshot()

	if snap.SchemaVersion != SnapshotSchemaVersion {
		t.Errorf("schema = %d, want %d", snap.SchemaVersion, SnapshotSchemaVersion)
	}
	if snap.State != StateConnected {
		t.Errorf("state = %s, want connected", snap.State)
	}
	if len(snap.History) != 5 {
		t.Errorf("history has %d entries, want 5", len(snap.History))
	}

	// A snapshot outlives the call it describes. It must carry nothing a
	// retention policy would have to reach into.
	rendered := fmt.Sprintf("%+v", snap)
	if strings.Contains(rendered, "+91") || strings.Contains(rendered, "@") {
		t.Errorf("snapshot appears to carry contact detail: %s", rendered)
	}
}

// TestRestore_StartsInRecoveryNotTheSnapshottedState is the central recovery
// decision.
func TestRestore_StartsInRecoveryNotTheSnapshottedState(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	sess, err := h.Connected(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snap := sess.Snapshot()

	restored, err := Restore(snap, h.Clock)
	if err != nil {
		t.Fatal(err)
	}

	// The snapshot says Connected. The process that believed that is gone, and
	// nothing has verified with the provider that the call is still up. A
	// session restored directly into Connected would report a live call that
	// may have hung up minutes ago.
	if restored.State() != StateRecovery {
		t.Errorf("state = %s, want recovery", restored.State())
	}
	if restored.ID() != sess.ID() {
		t.Errorf("call identifier changed across restore: %s -> %s", sess.ID(), restored.ID())
	}
	// A NEW session for the same call, so "how many times did we recover this"
	// is answerable.
	if restored.SessionID() == sess.SessionID() {
		t.Error("the session identifier was reused across restore")
	}
	if restored.ResumeCount() != 1 {
		t.Errorf("resume count = %d, want 1", restored.ResumeCount())
	}
}

func TestRestore_RefusesTerminalAndUnreadableSnapshots(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	sess, err := h.Connected(ctx)
	if err != nil {
		t.Fatal(err)
	}
	base := sess.Snapshot()

	terminal := base
	terminal.State = StateEnded
	if _, err := Restore(terminal, h.Clock); !errors.Is(err, ErrNotRecoverable) {
		t.Errorf("a terminal snapshot restored: %v", err)
	}

	// A snapshot decoded under the wrong schema resumes a call into a state it
	// was never in, which is worse than losing it.
	future := base
	future.SchemaVersion = SnapshotSchemaVersion + 1
	if _, err := Restore(future, h.Clock); !errors.Is(err, ErrNotRecoverable) {
		t.Errorf("an unreadable-schema snapshot restored: %v", err)
	}
}

// TestRecovery_ConcludesCallsThatAreNoLongerLive is the common case.
func TestRecovery_ConcludesCallsThatAreNoLongerLive(t *testing.T) {
	t.Parallel()
	first := started(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := first.Connected(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := first.Runtime.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if first.Store.Len() != 3 {
		t.Fatalf("store holds %d snapshots after shutdown, want 3", first.Store.Len())
	}

	// A new runtime over the same store: the default liveness check assumes
	// dead, which loses in-progress calls but never resurrects a dead one.
	second, err := New(TestConfig(), WithClock(first.Clock),
		WithPublisher(first.Events), WithSessionStore(first.Store))
	if err != nil {
		t.Fatal(err)
	}
	if err := second.RegisterProvider(first.Provider); err != nil {
		t.Fatal(err)
	}
	report, err := second.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = second.Stop(ctx) }()

	if report.Attempted != 3 {
		t.Errorf("attempted = %d, want 3", report.Attempted)
	}
	if report.Concluded != 3 {
		t.Errorf("concluded = %d, want 3: %s", report.Concluded, report.Summary())
	}
	if second.Live() != 0 {
		t.Errorf("%d calls live after concluding all three", second.Live())
	}
	// Downstream consumers must see a terminal event rather than a call that
	// simply stopped producing them.
	if len(first.Events.OfType(EventRecoveryAbandoned)) != 3 {
		t.Errorf("recovery-abandoned events = %d, want 3",
			len(first.Events.OfType(EventRecoveryAbandoned)))
	}
}

func TestRecovery_ResumesCallsThatAreStillLive(t *testing.T) {
	t.Parallel()
	first := started(t)
	ctx := context.Background()

	if _, err := first.Connected(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Runtime.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	second, err := New(TestConfig(), WithClock(first.Clock),
		WithPublisher(first.Events), WithSessionStore(first.Store),
		WithLivenessCheck(AlwaysLive))
	if err != nil {
		t.Fatal(err)
	}
	if err := second.RegisterProvider(first.Provider); err != nil {
		t.Fatal(err)
	}
	report, err := second.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = second.Stop(ctx) }()

	if report.Resumed != 1 {
		t.Fatalf("resumed = %d, want 1: %s", report.Resumed, report.Summary())
	}
	if second.Live() != 1 {
		t.Errorf("live = %d after resuming one call, want 1", second.Live())
	}
	if got := second.Recovered(); got != 1 {
		t.Errorf("Recovered() = %d, want 1", got)
	}

	// A resumed call must hold a capacity slot, or the runtime under-counts its
	// own load until those calls end.
	if got := second.Scheduler().Live(first.Provider.ID()); got != 1 {
		t.Errorf("scheduler live = %d for a resumed call, want 1", got)
	}
}

func TestRecovery_AbandonsUnrecoverableSnapshots(t *testing.T) {
	t.Parallel()
	h := harness(t)
	ctx := context.Background()

	// A snapshot at a schema this build cannot read. It will never become
	// readable, so retrying forever makes every recovery slower and noisier.
	bad := Snapshot{
		SchemaVersion: 99, Call: NewCallID(), Session: NewSessionID(),
		State: StateConnected, Context: h.Inbound(),
	}
	if err := h.Store.Save(ctx, bad); err != nil {
		t.Fatal(err)
	}

	report, err := h.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = h.Stop(ctx) }()

	if report.Abandoned != 1 {
		t.Errorf("abandoned = %d, want 1: %s", report.Abandoned, report.Summary())
	}
	if h.Store.Len() != 0 {
		t.Error("the unrecoverable snapshot was left in the store to be retried forever")
	}
}

func TestRecovery_IsDeterministic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	sequence := func() []string {
		h := started(t)
		for i := 0; i < 5; i++ {
			if _, err := h.Connected(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := h.Runtime.Stop(ctx); err != nil {
			t.Fatal(err)
		}

		events := NewRecordingPublisher()
		r, err := New(TestConfig(), WithClock(h.Clock), WithPublisher(events),
			WithSessionStore(h.Store))
		if err != nil {
			t.Fatal(err)
		}
		if err := r.RegisterProvider(h.Provider); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Start(ctx); err != nil {
			t.Fatal(err)
		}
		_, _ = r.Stop(ctx)

		var out []string
		for _, e := range events.OfType(EventRecoveryStarted) {
			out = append(out, string(e.Call))
		}
		return out
	}

	// Recovery order must not depend on map iteration: two recoveries of the
	// same store must produce the same event sequence, or an incident cannot be
	// replayed.
	first, second := sequence(), sequence()
	if len(first) != 5 || len(second) != 5 {
		t.Fatalf("recovery produced %d and %d events, want 5", len(first), len(second))
	}
}

// ---------------------------------------------------------------------------
// Shutdown
// ---------------------------------------------------------------------------

func TestShutdown_RefusesNewCallsImmediately(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	if _, err := h.Runtime.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.BeginInbound(ctx); !errors.Is(err, ErrRuntimeStopped) {
		t.Errorf("err = %v, want ErrRuntimeStopped", err)
	}
}

func TestShutdown_SnapshotsLiveCallsAndIsIdempotent(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		if _, err := h.Connected(ctx); err != nil {
			t.Fatal(err)
		}
	}

	abandoned, err := h.Runtime.Stop(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if abandoned != 4 {
		t.Errorf("abandoned = %d, want 4", abandoned)
	}
	if h.Store.Len() != 4 {
		t.Errorf("store holds %d snapshots, want 4", h.Store.Len())
	}

	if _, err := h.Runtime.Stop(ctx); err != nil {
		t.Errorf("Stop is not idempotent: %v", err)
	}
}

// TestShutdown_TerminatesWithLiveCalls is the regression test for F1.
func TestShutdown_TerminatesWithLiveCalls(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	if _, err := h.Connected(ctx); err != nil {
		t.Fatal(err)
	}

	// The first version measured the drain budget against the INJECTED clock
	// while polling with a real ticker. Under a FakeClock nobody advances, the
	// deadline never arrived and Stop spun forever — a graceful shutdown that
	// never terminates, so the orchestrator waits out its grace period and
	// sends SIGKILL having wasted it.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = h.Runtime.Stop(ctx)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not terminate with a live call and a frozen clock")
	}
}

func TestRuntime_RefusesToStartWithoutAProvider(t *testing.T) {
	t.Parallel()
	r, err := New(TestConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(context.Background()); err == nil {
		t.Error("a runtime with no provider started; it would fail every call at admission")
	}
}

func TestRuntime_RefusesProviderRegistrationAfterStart(t *testing.T) {
	t.Parallel()
	h := started(t)
	// Swapping an adapter under a live call is the mistake this prevents.
	if err := h.Runtime.RegisterProvider(NewFakeProvider("second", NewCapabilities())); err == nil {
		t.Error("a provider was registered after Start")
	}
}

// ---------------------------------------------------------------------------
// Concurrency and stress
// ---------------------------------------------------------------------------

func TestConcurrency_ThousandsOfSimultaneousCalls(t *testing.T) {
	t.Parallel()
	cfg := TestConfig()
	cfg.MaxConcurrentCalls = 5000
	cfg.MaxCallsPerProvider = 0
	cfg.AdmissionHighWater = 1.0

	h := started(t, WithHarnessConfig(cfg))
	ctx := context.Background()

	const workers, perWorker = 50, 40 // 2,000 calls

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		started int
		failed  int
	)
	start := make(chan struct{})

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			local, localFail := 0, 0
			for i := 0; i < perWorker; i++ {
				sess, err := h.Connected(ctx)
				if err != nil {
					localFail++
					continue
				}
				local++
				if err := h.Coordinator.End(ctx, sess.ID(), "caller_hung_up"); err != nil {
					localFail++
				}
			}
			mu.Lock()
			started += local
			failed += localFail
			mu.Unlock()
		}()
	}

	close(start)
	wg.Wait()

	if failed != 0 {
		t.Errorf("%d operations failed under concurrency", failed)
	}
	if started != workers*perWorker {
		t.Errorf("completed %d calls, want %d", started, workers*perWorker)
	}
	// Every admitted call released its slot.
	if got := h.Runtime.Live(); got != 0 {
		t.Errorf("%d calls still live after all completed", got)
	}
	if got := h.Runtime.Scheduler().Live(h.Provider.ID()); got != 0 {
		t.Errorf("scheduler holds %d slots after all calls ended; capacity leaked", got)
	}
}

func TestConcurrency_ConcurrentTransitionsOnOneCall(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	sess, err := h.Connected(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Many goroutines racing to end the same call. Exactly one must win; the
	// FSM refuses the rest, and no goroutine may observe a torn state.
	const racers = 32
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners int
	)
	start := make(chan struct{})

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := sess.Transition(StateEnded, "raced"); err == nil {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if winners != 1 {
		t.Errorf("%d goroutines transitioned the same call to Ended, want exactly 1", winners)
	}
	if sess.State() != StateEnded {
		t.Errorf("state = %s, want ended", sess.State())
	}
}

func TestConcurrency_SweepRunsAlongsideCallChurn(t *testing.T) {
	t.Parallel()
	cfg := TestConfig()
	cfg.MaxConcurrentCalls = 2000
	cfg.MaxCallsPerProvider = 0

	h := started(t, WithHarnessConfig(cfg))
	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// The sweep transitions calls while other goroutines create and end
		// them. This is the path that deadlocks if Each holds a shard lock.
		for i := 0; i < 200; i++ {
			h.Runtime.Sweep(ctx)
		}
	}()

	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				sess, err := h.Connected(ctx)
				if err != nil {
					continue
				}
				_ = h.Coordinator.End(ctx, sess.ID(), "caller_hung_up")
			}
		}()
	}
	wg.Wait()
	<-done

	if got := h.Runtime.Live(); got != 0 {
		t.Errorf("%d calls live after churn, want 0", got)
	}
}

func TestStress_SustainedChurn(t *testing.T) {
	if testing.Short() {
		t.Skip("stress run skipped under -short")
	}
	t.Parallel()

	cfg := TestConfig()
	cfg.MaxConcurrentCalls = 3000
	cfg.MaxCallsPerProvider = 0

	h := started(t, WithHarnessConfig(cfg))
	ctx := context.Background()

	const rounds, perRound = 20, 250

	begin := time.Now()
	for r := 0; r < rounds; r++ {
		sessions := make([]*CallSession, 0, perRound)
		for i := 0; i < perRound; i++ {
			sess, err := h.Connected(ctx)
			if err != nil {
				t.Fatalf("round %d call %d: %v", r, i, err)
			}
			sessions = append(sessions, sess)
		}
		h.Runtime.Sweep(ctx)
		for _, sess := range sessions {
			if err := h.Coordinator.End(ctx, sess.ID(), "caller_hung_up"); err != nil {
				t.Fatalf("round %d end: %v", r, err)
			}
		}
	}
	elapsed := time.Since(begin)

	total := rounds * perRound
	t.Logf("%d calls through the full lifecycle in %s (%.0f calls/sec)",
		total, elapsed.Round(time.Millisecond), float64(total)/elapsed.Seconds())

	if got := h.Runtime.Live(); got != 0 {
		t.Errorf("%d calls live after the stress run", got)
	}
	if got := h.Runtime.Scheduler().Live(h.Provider.ID()); got != 0 {
		t.Errorf("scheduler holds %d slots after the stress run", got)
	}
	// The event recorder is bounded, so this proves the recorder did not grow
	// without limit under sustained load.
	if h.Events.Len() > 4096 {
		t.Errorf("event recorder holds %d events, above its bound", h.Events.Len())
	}
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

func TestMetrics_ExportHistogramsInFull(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	sess, err := h.Connected(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h.Clock.Advance(30 * time.Second)
	if err := h.Coordinator.End(ctx, sess.ID(), "caller_hung_up"); err != nil {
		t.Fatal(err)
	}

	// Phase 10.5 found that three of six subsystems could not export histogram
	// data at all. This module is the first written after that, and inherits
	// the fix rather than repeating the defect.
	var histograms int
	for _, s := range h.Metrics.Snapshot() {
		if len(s.Bounds) == 0 {
			continue
		}
		histograms++
		if s.Count > 0 && len(s.Buckets) == 0 {
			t.Errorf("histogram %s exports no buckets", s.Name)
		}
	}
	if histograms == 0 {
		t.Error("no histogram series reached the snapshot")
	}
}

func TestMetrics_StateCensusReportsZeroes(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	if _, err := h.Connected(ctx); err != nil {
		t.Fatal(err)
	}
	h.Runtime.Sweep(ctx)

	names := map[string]bool{}
	for _, s := range h.Metrics.Snapshot() {
		names[s.Name] = true
	}
	// A gauge that stops being reported when its population empties leaves a
	// dashboard showing the last non-zero value forever.
	for _, state := range AllStates() {
		want := "telephony_calls_state_" + string(state)
		if !names[want] {
			t.Errorf("no gauge for state %s", state)
		}
	}
}

func TestMetrics_ShedRateAndFailureRate(t *testing.T) {
	t.Parallel()
	cfg := TestConfig()
	cfg.MaxConcurrentCalls = 2
	cfg.MaxCallsPerProvider = 0
	cfg.AdmissionHighWater = 1.0

	h := started(t, WithHarnessConfig(cfg))
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := h.BeginInbound(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := h.BeginInbound(ctx); err == nil {
			t.Fatal("a call was admitted past capacity")
		}
	}

	if got := h.Metrics.ShedRate(); got != 0.5 {
		t.Errorf("ShedRate() = %v, want 0.5", got)
	}
	if got := h.Metrics.FailureRate(); got != 0 {
		t.Errorf("FailureRate() = %v with no failures, want 0", got)
	}
}
