package telephony

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// Phase 19 P1-5 — carrier-observed answer and remote end.
//
// Every test counts the fake provider's commands. The property under test is
// as much what is NOT sent to the carrier as what the lifecycle records.

// outboundRinging places an outbound call, which starts in Ringing.
func outboundRinging(t *testing.T, h *Harness) *CallSession {
	t.Helper()
	sess, err := h.Coordinator.Begin(context.Background(), h.Outbound())
	if err != nil {
		t.Fatalf("begin outbound: %v", err)
	}
	if s := sess.State(); s != StateRinging {
		t.Fatalf("outbound call starts in %s, want ringing", s)
	}
	return sess
}

func commands(h *Harness) (answers, hangups int) {
	return h.Provider.Calls("answer"), h.Provider.Calls("hangup")
}

func TestObserveRemoteAnswer_RecordsAcceptedWithoutAnswering(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()
	sess := outboundRinging(t, h)
	before := sess.HistoryLen()

	if err := h.Runtime.Lifecycle().ObserveRemoteAnswer(ctx, sess.ID()); err != nil {
		t.Fatalf("ObserveRemoteAnswer: %v", err)
	}
	if s := sess.State(); s != StateAccepted {
		t.Errorf("state = %s, want accepted (not connected: no media was reported)", s)
	}
	if a, hu := commands(h); a != 0 || hu != 0 {
		t.Errorf("provider received %d answer and %d hangup commands, want none", a, hu)
	}
	hist := sess.History()
	if len(hist) != before+1 || hist[len(hist)-1].Reason != ReasonRemoteAnswered {
		t.Errorf("history = %v, want one remote_answered transition", hist)
	}

	// Media then comes up through the existing path, unchanged.
	if err := h.Runtime.Lifecycle().Connect(ctx, sess.ID()); err != nil {
		t.Fatalf("Connect after the observed answer: %v", err)
	}
	if !sess.AnsweredAt().Equal(h.Clock.Now()) {
		t.Errorf("AnsweredAt not recorded on connect")
	}
}

func TestObserveRemoteAnswer_RepeatedObservationsAreNoOps(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()
	l := h.Runtime.Lifecycle()
	sess := outboundRinging(t, h)

	if err := l.ObserveRemoteAnswer(ctx, sess.ID()); err != nil {
		t.Fatal(err)
	}
	afterFirst := sess.HistoryLen()
	for i := 0; i < 3; i++ {
		if err := l.ObserveRemoteAnswer(ctx, sess.ID()); err != nil {
			t.Errorf("repeat %d: %v", i+1, err)
		}
	}
	// Still a no-op once media is up, on hold, and so on.
	if err := l.Connect(ctx, sess.ID()); err != nil {
		t.Fatal(err)
	}
	if err := l.Hold(ctx, sess.ID()); err != nil {
		t.Fatal(err)
	}
	if err := l.ObserveRemoteAnswer(ctx, sess.ID()); err != nil {
		t.Errorf("observation on a held call: %v", err)
	}
	if got := sess.HistoryLen(); got != afterFirst+2 {
		t.Errorf("history = %d, want %d (only Connect and Hold added)", got, afterFirst+2)
	}
	if s := sess.State(); s != StateHold {
		t.Errorf("state = %s, want hold (never moved backwards)", s)
	}
	if a, _ := commands(h); a != 0 {
		t.Errorf("%d answer commands", a)
	}
}

func TestObserveRemoteAnswer_RefusesInvalidTransitions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("inbound call not yet accepted", func(t *testing.T) {
		h := started(t)
		sess, err := h.BeginInbound(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.Runtime.Lifecycle().Ring(ctx, sess.ID()); err != nil {
			t.Fatal(err)
		}
		before := sess.HistoryLen()
		err = h.Runtime.Lifecycle().ObserveRemoteAnswer(ctx, sess.ID())
		var te *TransitionError
		if !errors.Is(err, ErrInvalidTransition) || !errors.As(err, &te) || te.Reason != reasonInboundAnswer {
			t.Fatalf("err = %v, want an inbound-answer TransitionError", err)
		}
		if sess.State() != StateRinging || sess.HistoryLen() != before {
			t.Errorf("state %s / history %d changed on refusal", sess.State(), sess.HistoryLen())
		}
		if a, _ := commands(h); a != 0 {
			t.Errorf("%d answer commands", a)
		}
	})

	t.Run("outbound call already timed out", func(t *testing.T) {
		h := started(t)
		sess := outboundRinging(t, h)
		if err := h.Runtime.Lifecycle().Timeout(ctx, sess.ID(), "test_timeout"); err != nil {
			t.Fatal(err)
		}
		err := h.Runtime.Lifecycle().ObserveRemoteAnswer(ctx, sess.ID())
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("err = %v, want ErrInvalidTransition", err)
		}
		if sess.State() != StateTimeout {
			t.Errorf("state = %s, want timeout", sess.State())
		}
	})

	t.Run("unknown call", func(t *testing.T) {
		h := started(t)
		if err := h.Runtime.Lifecycle().ObserveRemoteAnswer(ctx, NewCallID()); !errors.Is(err, ErrCallNotFound) {
			t.Fatalf("err = %v, want ErrCallNotFound", err)
		}
	})
}

func TestObserveRemoteEnd_EndsWithoutHangingUp(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()
	sess, err := h.Connected(ctx)
	if err != nil {
		t.Fatal(err)
	}
	provider := sess.Context().Provider
	liveBefore := h.Runtime.Scheduler().Live(provider)
	_, hangupsBefore := commands(h)

	if err := h.Coordinator.ObserveRemoteEnd(ctx, sess.ID()); err != nil {
		t.Fatalf("ObserveRemoteEnd: %v", err)
	}
	if s := sess.State(); s != StateEnded {
		t.Errorf("state = %s, want ended", s)
	}
	if sess.EndReason() != ReasonRemoteEnded {
		t.Errorf("end reason = %q, want %q", sess.EndReason(), ReasonRemoteEnded)
	}
	if _, hu := commands(h); hu != hangupsBefore {
		t.Errorf("%d hangup commands sent for a carrier-reported end", hu-hangupsBefore)
	}
	if _, err := h.Runtime.Registry().Get(sess.ID()); !errors.Is(err, ErrCallNotFound) {
		t.Errorf("ended call still registered: %v", err)
	}
	if got := h.Runtime.Scheduler().Live(provider); got != liveBefore-1 {
		t.Errorf("scheduler live = %d, want %d (slot released)", got, liveBefore-1)
	}
	evs := h.Events.ForCall(sess.ID())
	if len(evs) == 0 || evs[len(evs)-1].To != StateEnded || evs[len(evs)-1].Reason != ReasonRemoteEnded {
		t.Errorf("events = %+v, want the last to be the remote_ended terminal event", evs)
	}
}

func TestObserveRemoteEnd_RepeatedAndUnknown(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()
	sess, err := h.Connected(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Coordinator.ObserveRemoteEnd(ctx, sess.ID()); err != nil {
		t.Fatal(err)
	}
	live := h.Runtime.Scheduler().Live(sess.Context().Provider)

	// A repeated observation finds no live call: explicit, and harmless.
	for i := 0; i < 2; i++ {
		if err := h.Coordinator.ObserveRemoteEnd(ctx, sess.ID()); !errors.Is(err, ErrCallNotFound) {
			t.Errorf("repeat %d: err = %v, want ErrCallNotFound", i+1, err)
		}
	}
	if s := sess.State(); s != StateEnded {
		t.Errorf("state = %s; an ended call must stay ended", s)
	}
	if got := h.Runtime.Scheduler().Live(sess.Context().Provider); got != live {
		t.Errorf("slot released again: live %d -> %d", live, got)
	}
	if _, hu := commands(h); hu != 0 {
		t.Errorf("%d hangup commands", hu)
	}
	if err := h.Runtime.Lifecycle().ObserveRemoteEnd(ctx, NewCallID()); !errors.Is(err, ErrCallNotFound) {
		t.Errorf("unknown call: err = %v, want ErrCallNotFound", err)
	}
}

func TestObserveRemoteEnd_RefusesStatesWithoutAnEndedEdge(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()
	sess := outboundRinging(t, h)
	if err := h.Runtime.Lifecycle().ObserveRemoteAnswer(ctx, sess.ID()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []CallState{StateAccepted} {
		if err := h.Coordinator.ObserveRemoteEnd(ctx, sess.ID()); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("from %s: err = %v, want ErrInvalidTransition", want, err)
		}
		if s := sess.State(); s != want {
			t.Errorf("state = %s, want %s unchanged", s, want)
		}
	}
	other := outboundRinging(t, h)
	if err := h.Coordinator.ObserveRemoteEnd(ctx, other.ID()); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("from ringing: err = %v, want ErrInvalidTransition", err)
	}
	if _, err := h.Runtime.Registry().Get(other.ID()); err != nil {
		t.Errorf("a refused call was removed: %v", err)
	}
}

// TestObserve_ConcurrentObservationsTransitionOnce: identical observations
// racing produce one transition, one slot release and no provider command.
func TestObserve_ConcurrentObservationsTransitionOnce(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()
	l := h.Runtime.Lifecycle()
	sess := outboundRinging(t, h)
	provider := sess.Context().Provider
	before := sess.HistoryLen()

	const n = 16
	race := func(f func() error) []error {
		var wg sync.WaitGroup
		errs := make([]error, n)
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = f()
			}()
		}
		close(start)
		wg.Wait()
		return errs
	}

	for i, err := range race(func() error { return l.ObserveRemoteAnswer(ctx, sess.ID()) }) {
		if err != nil {
			t.Errorf("answer %d: %v", i, err)
		}
	}
	if got := sess.HistoryLen(); got != before+1 {
		t.Errorf("answer transitions = %d, want 1", got-before)
	}

	if err := l.Connect(ctx, sess.ID()); err != nil {
		t.Fatal(err)
	}
	live := h.Runtime.Scheduler().Live(provider)
	ended := 0
	for i, err := range race(func() error { return h.Coordinator.ObserveRemoteEnd(ctx, sess.ID()) }) {
		switch {
		case err == nil:
			ended++
		case errors.Is(err, ErrCallNotFound), errors.Is(err, ErrTerminal):
		default:
			t.Errorf("end %d: unexpected %v", i, err)
		}
	}
	if ended != 1 {
		t.Errorf("%d observations ended the call, want 1", ended)
	}
	if got := h.Runtime.Scheduler().Live(provider); got != live-1 {
		t.Errorf("scheduler live %d -> %d, want exactly one release", live, got)
	}
	if a, hu := commands(h); a != 0 || hu != 0 {
		t.Errorf("provider received %d answers and %d hangups, want none", a, hu)
	}
}

// The commanded paths are unchanged: Accept still answers once, and End still
// hangs up once.
func TestCommands_AcceptAndEndStillCommandTheCarrier(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()
	sess, err := h.Connected(ctx) // Ring, Screen, Accept, Connect
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := commands(h); a != 1 || !h.Provider.Answered(sess.ID()) {
		t.Errorf("Accept sent %d answer commands, want 1", a)
	}
	if err := h.Coordinator.End(ctx, sess.ID(), "caller_done"); err != nil {
		t.Fatal(err)
	}
	if _, hu := commands(h); hu != 1 {
		t.Errorf("End sent %d hangup commands, want 1", hu)
	}
	if reason, ok := h.Provider.HungUp(sess.ID()); !ok || reason != "caller_done" {
		t.Errorf("HungUp = %q, %v", reason, ok)
	}
}
