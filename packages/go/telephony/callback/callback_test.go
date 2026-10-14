package callback

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
)

// Phase 19 P1-4 — idempotent callback ingestion.
//
// Every test drives the REAL telephony runtime through its exported harness
// (fake clock, fake provider, recording publisher). No network, no carrier.
// Where a failure must be injected, the real lifecycle is wrapped, not
// replaced.

const provider = telephony.ProviderID("fake-carrier")

var received = time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)

// ---------------------------------------------------------------------------
// Rig
// ---------------------------------------------------------------------------

type resolver struct {
	mu    sync.Mutex
	calls map[string]telephony.CallID // provider + "/" + provider call ID
}

func newResolver() *resolver { return &resolver{calls: map[string]telephony.CallID{}} }

func (r *resolver) bind(p telephony.ProviderID, providerCallID string, id telephony.CallID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[string(p)+"/"+providerCallID] = id
}

func (r *resolver) Resolve(p telephony.ProviderID, providerCallID string) (telephony.CallID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.calls[string(p)+"/"+providerCallID]
	if !ok {
		return "", ErrUnknownCall
	}
	return id, nil
}

// countingLifecycle wraps the real lifecycle, counting operations and failing
// the next N of them on request.
type countingLifecycle struct {
	Lifecycle
	ops      atomic.Int64
	failNext atomic.Int64
	delay    time.Duration
}

var errInjected = errors.New("injected lifecycle failure")

func (c *countingLifecycle) do(f func() error) error {
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	if c.failNext.Load() > 0 {
		c.failNext.Add(-1)
		return errInjected
	}
	c.ops.Add(1)
	return f()
}

func (c *countingLifecycle) Ring(ctx context.Context, id telephony.CallID) error {
	return c.do(func() error { return c.Lifecycle.Ring(ctx, id) })
}

func (c *countingLifecycle) ObserveRemoteAnswer(ctx context.Context, id telephony.CallID) error {
	return c.do(func() error { return c.Lifecycle.ObserveRemoteAnswer(ctx, id) })
}

func (c *countingLifecycle) ObserveRemoteEnd(ctx context.Context, id telephony.CallID) error {
	return c.do(func() error { return c.Lifecycle.ObserveRemoteEnd(ctx, id) })
}

func (c *countingLifecycle) Fail(ctx context.Context, id telephony.CallID, reason string) error {
	return c.do(func() error { return c.Lifecycle.Fail(ctx, id, reason) })
}

type rig struct {
	h     *telephony.Harness
	store *MemoryStore
	res   *resolver
	life  *countingLifecycle
	in    *Ingestor
}

func newRig(t *testing.T, capacity int) *rig {
	t.Helper()
	h, err := telephony.NewHarness()
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	if _, err := h.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _, _ = h.Stop(context.Background()) })

	store, err := NewMemoryStore(capacity)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	r := &rig{h: h, store: store, res: newResolver(), life: &countingLifecycle{Lifecycle: RuntimeLifecycle(h.Runtime)}}
	if r.in, err = NewIngestor(store, r.res, r.life); err != nil {
		t.Fatalf("ingestor: %v", err)
	}
	return r
}

// call admits an inbound call (state Incoming) bound to providerCallID.
func (r *rig) call(t *testing.T, providerCallID string) telephony.CallID {
	t.Helper()
	sess, err := r.h.BeginInbound(context.Background())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	r.res.bind(provider, providerCallID, sess.ID())
	return sess.ID()
}

// outbound places an outbound call (state Ringing) bound to providerCallID.
func (r *rig) outbound(t *testing.T, providerCallID string) telephony.CallID {
	t.Helper()
	sess, err := r.h.Coordinator.Begin(context.Background(), r.h.Outbound())
	if err != nil {
		t.Fatalf("begin outbound: %v", err)
	}
	r.res.bind(provider, providerCallID, sess.ID())
	return sess.ID()
}

// to drives a call to want using the lifecycle directly (as the platform
// itself would): inbound through Ring, Screen and Accept; outbound through an
// observed answer.
func (r *rig) to(t *testing.T, id telephony.CallID, want telephony.CallState) {
	t.Helper()
	ctx := context.Background()
	l := r.h.Runtime.Lifecycle()
	type step struct {
		state telephony.CallState
		do    func() error
	}
	steps := []step{
		{telephony.StateRinging, func() error { return l.Ring(ctx, id) }},
		{telephony.StateScreening, func() error { return l.Screen(ctx, id) }},
		{telephony.StateAccepted, func() error { return l.Accept(ctx, id, "screening_passed") }},
	}
	if dir, err := r.life.Direction(id); err != nil {
		t.Fatalf("direction: %v", err)
	} else if dir == telephony.DirectionOutbound {
		steps = []step{{telephony.StateAccepted, func() error { return l.ObserveRemoteAnswer(ctx, id) }}}
	}
	steps = append(steps,
		step{telephony.StateConnected, func() error { return l.Connect(ctx, id) }},
		step{telephony.StateHold, func() error { return l.Hold(ctx, id) }},
	)
	for _, s := range steps {
		if r.state(t, id) == want {
			return
		}
		if err := s.do(); err != nil {
			t.Fatalf("drive to %s: step %s: %v", want, s.state, err)
		}
	}
	if got := r.state(t, id); got != want {
		t.Fatalf("drove to %s, got %s", want, got)
	}
}

func (r *rig) state(t *testing.T, id telephony.CallID) telephony.CallState {
	t.Helper()
	s, err := r.life.State(id)
	if err != nil {
		t.Fatalf("state of %s: %v", id, err)
	}
	return s
}

func (r *rig) history(t *testing.T, id telephony.CallID) int {
	t.Helper()
	sess, err := r.h.Runtime.Registry().Get(id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return sess.HistoryLen()
}

func ev(eventID, providerCallID string, k Kind) Event {
	return Event{
		Provider: provider, EventID: eventID, ProviderCallID: providerCallID,
		Kind: k, ReceivedAt: received,
	}
}

func (r *rig) ingest(t *testing.T, e Event) Result {
	t.Helper()
	res, err := r.in.Ingest(context.Background(), e)
	if err != nil {
		t.Fatalf("Ingest(%s %s): %v", e.EventID, e.Kind, err)
	}
	return res
}

// ---------------------------------------------------------------------------
// The reconciliation table
// ---------------------------------------------------------------------------

// TestRules_AgreeWithTheTransitionTable: every edge the table applies is one
// the lifecycle declares, every state is classified at most once per kind, and
// no rule ever targets a state behind the one it applies from.
func TestRules_AgreeWithTheTransitionTable(t *testing.T) {
	t.Parallel()
	for k, r := range rules {
		seen := map[telephony.CallState]string{}
		for name, set := range map[string][]telephony.CallState{
			"apply": r.apply, "satisfied": r.satisfied, "stale": r.stale, "unrecordable": r.unrecordable,
		} {
			for _, s := range set {
				if prev, dup := seen[s]; dup {
					t.Errorf("%s: %s is in both %s and %s", k, s, prev, name)
				}
				seen[s] = name
				if s.Terminal() {
					t.Errorf("%s: terminal state %s is classified; terminal calls are never judged", k, s)
				}
			}
		}
		for _, from := range r.apply {
			if !telephony.CanTransition(from, r.to) {
				t.Errorf("%s: applies %s -> %s, which the lifecycle does not declare", k, from, r.to)
			}
		}
		if len(r.unrecordable) > 0 && r.gapReason == "" {
			t.Errorf("%s: unrecordable states without a reason", k)
		}
	}
	for _, k := range []Kind{KindRinging, KindAnswered, KindCompleted, KindBusy, KindNoAnswer, KindCancelled, KindFailed} {
		if !k.Valid() {
			t.Errorf("declared kind %s has no rule", k)
		}
	}
}

// ---------------------------------------------------------------------------
// 1-3. First delivery, duplicates, provider scoping
// ---------------------------------------------------------------------------

func TestIngest_AppliesAValidFirstTimeEvent(t *testing.T) {
	t.Parallel()
	r := newRig(t, 64)
	id := r.call(t, "pc-1")

	got := r.ingest(t, ev("e-1", "pc-1", KindRinging))
	want := Result{Outcome: OutcomeApplied, Call: id, From: telephony.StateIncoming, To: telephony.StateRinging}
	if got != want {
		t.Fatalf("Result = %+v, want %+v", got, want)
	}
	if s := r.state(t, id); s != telephony.StateRinging {
		t.Errorf("state = %s, want ringing", s)
	}
}

func TestIngest_DuplicateDeliveryAppliesOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t, 64)
	id := r.outbound(t, "pc-1")

	first := r.ingest(t, ev("e-ans", "pc-1", KindAnswered))
	if first.Outcome != OutcomeApplied {
		t.Fatalf("first = %+v, want applied", first)
	}
	historyAfterFirst := r.history(t, id)
	opsAfterFirst := r.life.ops.Load()

	for i := 0; i < 3; i++ {
		again := r.ingest(t, ev("e-ans", "pc-1", KindAnswered))
		if again.Outcome != OutcomeDuplicate || again.Prior != OutcomeApplied || again.Call != id {
			t.Errorf("redelivery %d = %+v, want duplicate of applied", i+1, again)
		}
	}
	if got := r.history(t, id); got != historyAfterFirst {
		t.Errorf("history grew from %d to %d on redelivery", historyAfterFirst, got)
	}
	if got := r.life.ops.Load(); got != opsAfterFirst {
		t.Errorf("lifecycle called %d more times on redelivery", got-opsAfterFirst)
	}
}

func TestIngest_EventIDsAreScopedToTheProvider(t *testing.T) {
	t.Parallel()
	r := newRig(t, 64)
	a := r.call(t, "pc-a")
	b := r.call(t, "pc-b")
	// The same provider event ID from a different provider, for a different
	// call. (The resolver is provider-scoped; the lifecycle does not care which
	// carrier reported the ring.)
	r.res.bind("other-carrier", "pc-b", b)

	if got := r.ingest(t, ev("same-id", "pc-a", KindRinging)); got.Outcome != OutcomeApplied {
		t.Fatalf("fake-carrier: %+v", got)
	}
	other := ev("same-id", "pc-b", KindRinging)
	other.Provider = "other-carrier"
	if got := r.ingest(t, other); got.Outcome != OutcomeApplied || got.Call != b {
		t.Errorf("other-carrier with the same event ID = %+v, want applied to %s", got, b)
	}
	if r.state(t, a) != telephony.StateRinging || r.state(t, b) != telephony.StateRinging {
		t.Error("both calls should be ringing")
	}
}

// ---------------------------------------------------------------------------
// 4. Malformed events
// ---------------------------------------------------------------------------

func TestIngest_RejectsMalformedEvents(t *testing.T) {
	t.Parallel()
	r := newRig(t, 64)
	id := r.call(t, "pc-1")

	for _, tc := range []struct {
		name   string
		mutate func(*Event)
		field  string
	}{
		{"missing provider", func(e *Event) { e.Provider = "" }, "Provider"},
		{"invalid provider", func(e *Event) { e.Provider = "Fake Carrier" }, "Provider"},
		{"missing event ID", func(e *Event) { e.EventID = "" }, "EventID"},
		{"missing call ID", func(e *Event) { e.ProviderCallID = "" }, "ProviderCallID"},
		{"unknown kind", func(e *Event) { e.Kind = "picked_up" }, "Kind"},
		{"missing kind", func(e *Event) { e.Kind = "" }, "Kind"},
		{"missing received time", func(e *Event) { e.ReceivedAt = time.Time{} }, "ReceivedAt"},
		{"oversized event ID", func(e *Event) { e.EventID = strings.Repeat("x", maxIDLen+1) }, "EventID"},
		{"event ID with a newline", func(e *Event) { e.EventID = "e-1\ninjected" }, "EventID"},
		{"call ID with a space", func(e *Event) { e.ProviderCallID = "pc 1" }, "ProviderCallID"},
		{"bad correlation", func(e *Event) { e.Correlation = "corr id" }, "Correlation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := ev("e-1", "pc-1", KindRinging)
			tc.mutate(&e)
			_, err := r.in.Ingest(context.Background(), e)
			if !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("Ingest = %v, want %v", err, ErrInvalidEvent)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("error does not name %s: %v", tc.field, err)
			}
			if strings.Contains(err.Error(), "injected") {
				t.Errorf("error echoes the raw value: %v", err)
			}
		})
	}

	// Nothing was reserved by any of them: the valid event with the same key
	// is new.
	if got := r.ingest(t, ev("e-1", "pc-1", KindRinging)); got.Outcome != OutcomeApplied || got.Call != id {
		t.Errorf("valid event after malformed ones = %+v, want applied", got)
	}

	// Optional fields are optional.
	ok := ev("e-2", "pc-1", KindRinging)
	ok.OccurredAt, ok.Correlation = received.Add(-time.Second), "corr_123"
	if err := ok.Validate(); err != nil {
		t.Errorf("valid event with optional fields: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 5-7. Ordering, conflicts, invalid transitions
// ---------------------------------------------------------------------------

func TestIngest_OutOfOrderEventsNeverMoveACallBackwards(t *testing.T) {
	t.Parallel()
	r := newRig(t, 64)
	id := r.outbound(t, "pc-1")

	// "answered" arrives before the "ringing" that preceded it. The provider
	// timestamps say ringing happened first; they are not used.
	answered := ev("e-2", "pc-1", KindAnswered)
	answered.OccurredAt = received.Add(2 * time.Second)
	ringing := ev("e-1", "pc-1", KindRinging)
	ringing.OccurredAt = received.Add(time.Second)

	if got := r.ingest(t, answered); got.Outcome != OutcomeApplied || got.To != telephony.StateAccepted {
		t.Fatalf("answered = %+v, want applied to accepted", got)
	}
	got := r.ingest(t, ringing)
	if got.Outcome != OutcomeStale || got.Reason != ReasonPastReportedState || got.From != telephony.StateAccepted {
		t.Errorf("late ringing = %+v, want stale from accepted", got)
	}
	if s := r.state(t, id); s != telephony.StateAccepted {
		t.Errorf("state = %s, want accepted (never moved backwards)", s)
	}
	if a := r.h.Provider.Calls("answer"); a != 0 {
		t.Errorf("%d answer commands for a carrier-reported answer", a)
	}
	// Settled: a redelivery of the stale event is a duplicate, not re-judged.
	if again := r.ingest(t, ringing); again.Outcome != OutcomeDuplicate || again.Prior != OutcomeStale {
		t.Errorf("redelivered stale event = %+v", again)
	}
}

func TestIngest_ClassifiesEveryKindAgainstState(t *testing.T) {
	t.Parallel()
	const in, out = false, true
	for _, tc := range []struct {
		kind     Kind
		outbound bool
		state    telephony.CallState
		want     Outcome
		reason   string
	}{
		{KindRinging, in, telephony.StateIncoming, OutcomeApplied, ""},
		{KindRinging, in, telephony.StateRinging, OutcomeAlreadySatisfied, ""},
		{KindRinging, in, telephony.StateHold, OutcomeStale, ReasonPastReportedState},
		{KindRinging, out, telephony.StateAccepted, OutcomeStale, ReasonPastReportedState},
		{KindAnswered, out, telephony.StateRinging, OutcomeApplied, ""},
		{KindAnswered, out, telephony.StateAccepted, OutcomeAlreadySatisfied, ""},
		{KindAnswered, out, telephony.StateConnected, OutcomeStale, ReasonPastReportedState},
		{KindAnswered, in, telephony.StateAccepted, OutcomeAlreadySatisfied, ""},
		{KindAnswered, in, telephony.StateHold, OutcomeStale, ReasonPastReportedState},
		{KindAnswered, in, telephony.StateRinging, OutcomeConflict, ReasonAnswerWithoutAccept},
		{KindAnswered, in, telephony.StateScreening, OutcomeConflict, ReasonAnswerWithoutAccept},
		{KindAnswered, in, telephony.StateIncoming, OutcomeConflict, ReasonAnswerWithoutAccept},
		{KindCompleted, in, telephony.StateConnected, OutcomeApplied, ""},
		{KindCompleted, out, telephony.StateHold, OutcomeApplied, ""},
		{KindCompleted, in, telephony.StateRinging, OutcomeConflict, ReasonRemoteEndUnrecordable},
		{KindCompleted, out, telephony.StateAccepted, OutcomeConflict, ReasonRemoteEndUnrecordable},
		{KindBusy, in, telephony.StateRinging, OutcomeApplied, ""},
		{KindBusy, in, telephony.StateConnected, OutcomeConflict, ReasonContradictsState},
		{KindNoAnswer, in, telephony.StateScreening, OutcomeApplied, ""},
		{KindNoAnswer, in, telephony.StateHold, OutcomeConflict, ReasonContradictsState},
		{KindCancelled, in, telephony.StateAccepted, OutcomeApplied, ""},
		{KindFailed, in, telephony.StateConnected, OutcomeApplied, ""},
		{KindFailed, out, telephony.StateHold, OutcomeApplied, ""},
	} {
		dir := "inbound"
		if tc.outbound {
			dir = "outbound"
		}
		t.Run(fmt.Sprintf("%s_%s_in_%s", tc.kind, dir, tc.state), func(t *testing.T) {
			t.Parallel()
			r := newRig(t, 64)
			var id telephony.CallID
			if tc.outbound {
				id = r.outbound(t, "pc-1")
			} else {
				id = r.call(t, "pc-1")
			}
			r.to(t, id, tc.state)
			before := r.history(t, id)

			got := r.ingest(t, ev("e-1", "pc-1", tc.kind))
			if got.Outcome != tc.want || got.Reason != tc.reason || got.From != tc.state {
				t.Fatalf("Result = %+v, want %s (%q) from %s", got, tc.want, tc.reason, tc.state)
			}
			if tc.want != OutcomeApplied {
				if after := r.history(t, id); after != before {
					t.Errorf("a %s outcome changed the call's history (%d -> %d)", got.Outcome, before, after)
				}
			}
			if tc.want == OutcomeConflict {
				cs := r.store.Conflicts()
				if len(cs) != 1 || cs[0].Reason != tc.reason || cs[0].Call != id || cs[0].State != tc.state {
					t.Errorf("conflicts = %+v, want one %q for %s in %s", cs, tc.reason, id, tc.state)
				}
			}
		})
	}
}

// TestIngest_CompletedEndsTheCallWithoutACarrierCommand: a carrier-reported end
// concludes the call through the observation path — no hangup is sent for a
// call the carrier just told us is over — and releases its capacity slot.
func TestIngest_CompletedEndsTheCallWithoutACarrierCommand(t *testing.T) {
	t.Parallel()
	r := newRig(t, 64)
	id := r.call(t, "pc-1")
	r.to(t, id, telephony.StateConnected)
	hangupsBefore := r.h.Provider.Calls("hangup")
	live := r.h.Runtime.Scheduler().Live(provider)

	got := r.ingest(t, ev("e-end", "pc-1", KindCompleted))
	if got.Outcome != OutcomeApplied || got.To != telephony.StateEnded {
		t.Fatalf("completed = %+v, want applied to ended", got)
	}
	if n := r.h.Provider.Calls("hangup") - hangupsBefore; n != 0 {
		t.Errorf("%d carrier hangup commands issued for a carrier-reported end", n)
	}
	if _, err := r.h.Runtime.Registry().Get(id); !errors.Is(err, telephony.ErrCallNotFound) {
		t.Errorf("ended call still registered: %v", err)
	}
	if got := r.h.Runtime.Scheduler().Live(provider); got != live-1 {
		t.Errorf("scheduler live %d -> %d, want the slot released", live, got)
	}

	// Redelivered, and then late events: nothing resurrects it.
	if again := r.ingest(t, ev("e-end", "pc-1", KindCompleted)); again.Outcome != OutcomeDuplicate {
		t.Errorf("redelivery = %+v, want duplicate", again)
	}
	for i, k := range []Kind{KindRinging, KindAnswered, KindCompleted} {
		late := r.ingest(t, ev(fmt.Sprintf("late-%d", i), "pc-1", k))
		if late.Outcome != OutcomeStale || late.Reason != ReasonCallTerminal {
			t.Errorf("late %s = %+v, want stale (call_terminal)", k, late)
		}
	}
	if got := r.h.Runtime.Scheduler().Live(provider); got != live-1 {
		t.Errorf("slot released more than once: live %d", got)
	}
	if n := r.h.Provider.Calls("hangup") - hangupsBefore; n != 0 {
		t.Errorf("%d hangup commands after late events", n)
	}
}

// TestIngest_FailureKindsReleaseTheCapacitySlot: a terminal Failed outcome goes
// through the coordinator, which releases the slot (P1-4 bypassed it).
func TestIngest_FailureKindsReleaseTheCapacitySlot(t *testing.T) {
	t.Parallel()
	r := newRig(t, 64)
	r.call(t, "pc-1")
	live := r.h.Runtime.Scheduler().Live(provider)
	if got := r.ingest(t, ev("e-busy", "pc-1", KindBusy)); got.Outcome != OutcomeApplied {
		t.Fatalf("busy = %+v", got)
	}
	if got := r.h.Runtime.Scheduler().Live(provider); got != live-1 {
		t.Errorf("scheduler live %d -> %d, want the slot released", live, got)
	}
}

// TestIngest_ConcurrentRemoteEndAppliesOnce: one carrier end delivered many
// times at once ends the call once, releases one slot, sends no hangup.
func TestIngest_ConcurrentRemoteEndAppliesOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t, 64)
	id := r.outbound(t, "pc-1")
	r.to(t, id, telephony.StateConnected)
	live := r.h.Runtime.Scheduler().Live(provider)
	r.life.delay = 2 * time.Millisecond

	const n = 24
	var wg sync.WaitGroup
	outcomes := make(chan Outcome, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := r.in.Ingest(context.Background(), ev("e-end", "pc-1", KindCompleted))
			if err != nil {
				outcomes <- Outcome("error: " + err.Error())
				return
			}
			outcomes <- res.Outcome
		}()
	}
	wg.Wait()
	close(outcomes)
	count := map[Outcome]int{}
	for o := range outcomes {
		count[o]++
	}
	if count[OutcomeApplied] != 1 || count[OutcomeApplied]+count[OutcomeDuplicate]+count[OutcomeInFlight] != n {
		t.Errorf("outcomes %v, want exactly one applied and the rest duplicate or in flight", count)
	}
	if got := r.h.Runtime.Scheduler().Live(provider); got != live-1 {
		t.Errorf("scheduler live %d -> %d, want exactly one release", live, got)
	}
	if a, hu := r.h.Provider.Calls("answer"), r.h.Provider.Calls("hangup"); a != 0 || hu != 0 {
		t.Errorf("provider received %d answers and %d hangups, want none", a, hu)
	}
}

func TestIngest_ReusedEventIDWithDifferentContentIsAConflict(t *testing.T) {
	t.Parallel()
	r := newRig(t, 64)
	id := r.call(t, "pc-1")
	r.call(t, "pc-2")

	if got := r.ingest(t, ev("e-1", "pc-1", KindRinging)); got.Outcome != OutcomeApplied {
		t.Fatalf("first: %+v", got)
	}
	for name, e := range map[string]Event{
		"different kind": ev("e-1", "pc-1", KindFailed),
		"different call": ev("e-1", "pc-2", KindRinging),
	} {
		got, err := r.in.Ingest(context.Background(), e)
		if !errors.Is(err, ErrEventIDReused) || got.Outcome != OutcomeConflict || got.Reason != ReasonEventIDReused {
			t.Errorf("%s: %+v, %v; want a reuse conflict", name, got, err)
		}
	}
	if s := r.state(t, id); s != telephony.StateRinging {
		t.Errorf("state = %s; a conflicting reuse must not apply", s)
	}
	// The first outcome still stands for a true redelivery.
	if got := r.ingest(t, ev("e-1", "pc-1", KindRinging)); got.Outcome != OutcomeDuplicate || got.Prior != OutcomeApplied {
		t.Errorf("true redelivery after conflicts = %+v", got)
	}
	if n := len(r.store.Conflicts()); n != 2 {
		t.Errorf("%d conflicts recorded, want 2", n)
	}
}

// TestIngest_LifecycleRefusalIsNotSettled: a transition the lifecycle refuses
// at application time (the call moved underneath the judgement) leaves the
// event retryable, and a retry is judged against the new state.
func TestIngest_LifecycleRefusalIsNotSettled(t *testing.T) {
	t.Parallel()
	r := newRig(t, 64)
	id := r.call(t, "pc-1")

	// A lifecycle that reports a stale state, so the ingestor tries Ring on a
	// call that is really in Screening.
	r.to(t, id, telephony.StateScreening)
	stale := &staleStateLifecycle{Lifecycle: r.life, report: telephony.StateIncoming}
	in, err := NewIngestor(r.store, r.res, stale)
	if err != nil {
		t.Fatal(err)
	}
	_, err = in.Ingest(context.Background(), ev("e-1", "pc-1", KindRinging))
	if !errors.Is(err, telephony.ErrInvalidTransition) {
		t.Fatalf("Ingest = %v, want the lifecycle's ErrInvalidTransition", err)
	}
	if s := r.state(t, id); s != telephony.StateScreening {
		t.Errorf("state = %s, want screening", s)
	}
	// Not settled: the redelivery is judged afresh, correctly, as stale.
	if got := r.ingest(t, ev("e-1", "pc-1", KindRinging)); got.Outcome != OutcomeStale {
		t.Errorf("retry = %+v, want stale", got)
	}
}

type staleStateLifecycle struct {
	Lifecycle
	report telephony.CallState
}

func (s *staleStateLifecycle) State(telephony.CallID) (telephony.CallState, error) {
	return s.report, nil
}

// ---------------------------------------------------------------------------
// 8. Failure and retry
// ---------------------------------------------------------------------------

func TestIngest_FailedApplicationIsRetryable(t *testing.T) {
	t.Parallel()
	r := newRig(t, 64)
	id := r.outbound(t, "pc-1")

	r.life.failNext.Store(1)
	_, err := r.in.Ingest(context.Background(), ev("e-ans", "pc-1", KindAnswered))
	if !errors.Is(err, errInjected) {
		t.Fatalf("Ingest = %v, want the injected failure", err)
	}
	if s := r.state(t, id); s != telephony.StateRinging {
		t.Fatalf("state = %s after a failed application, want ringing (unchanged)", s)
	}
	if remembered, _, _ := r.store.Stats(); remembered != 0 {
		t.Errorf("%d keys remembered after a failure; the reservation must be released", remembered)
	}

	got := r.ingest(t, ev("e-ans", "pc-1", KindAnswered))
	if got.Outcome != OutcomeApplied {
		t.Fatalf("retry = %+v, want applied", got)
	}
	if n := r.life.ops.Load(); n != 1 {
		t.Errorf("lifecycle applied %d times, want 1", n)
	}
}

func TestIngest_UnknownCallIsRetryable(t *testing.T) {
	t.Parallel()
	r := newRig(t, 64)

	// The carrier's event beat the call's registration.
	_, err := r.in.Ingest(context.Background(), ev("e-1", "pc-early", KindRinging))
	if !errors.Is(err, ErrUnknownCall) {
		t.Fatalf("Ingest = %v, want %v", err, ErrUnknownCall)
	}
	id := r.call(t, "pc-early")
	if got := r.ingest(t, ev("e-1", "pc-early", KindRinging)); got.Outcome != OutcomeApplied || got.Call != id {
		t.Errorf("after registration = %+v, want applied", got)
	}
}

func TestIngest_CancelledContextReservesNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t, 64)
	r.call(t, "pc-1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.in.Ingest(ctx, ev("e-1", "pc-1", KindRinging)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ingest = %v, want context.Canceled", err)
	}
	if got := r.ingest(t, ev("e-1", "pc-1", KindRinging)); got.Outcome != OutcomeApplied {
		t.Errorf("after cancellation = %+v, want applied", got)
	}
}

// ---------------------------------------------------------------------------
// 9. Concurrency
// ---------------------------------------------------------------------------

func TestIngest_ConcurrentDeliveryOfOneEventAppliesOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t, 64)
	id := r.outbound(t, "pc-1")
	r.life.delay = 5 * time.Millisecond // widen the window for in-flight overlap
	before := r.history(t, id)

	const n = 32
	var wg sync.WaitGroup
	outcomes := make(chan Outcome, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res, err := r.in.Ingest(context.Background(), ev("e-ans", "pc-1", KindAnswered))
			if err != nil {
				outcomes <- Outcome("error: " + err.Error())
				return
			}
			outcomes <- res.Outcome
		}()
	}
	close(start)
	wg.Wait()
	close(outcomes)

	count := map[Outcome]int{}
	for o := range outcomes {
		count[o]++
	}
	if count[OutcomeApplied] != 1 {
		t.Errorf("applied %d times, want exactly 1; outcomes %v", count[OutcomeApplied], count)
	}
	if count[OutcomeApplied]+count[OutcomeDuplicate]+count[OutcomeInFlight] != n {
		t.Errorf("unexpected outcomes: %v", count)
	}
	if after := r.history(t, id); after != before+1 {
		t.Errorf("history grew by %d, want 1", after-before)
	}
	if ops := r.life.ops.Load(); ops != 1 {
		t.Errorf("lifecycle called %d times, want 1", ops)
	}
}

// TestIngest_ConcurrentConflictingEventsForOneCall: different events for one
// call race. Exactly one terminal outcome wins; the call is never resurrected.
func TestIngest_ConcurrentConflictingEventsForOneCall(t *testing.T) {
	t.Parallel()
	r := newRig(t, 64)
	id := r.call(t, "pc-1")
	r.to(t, id, telephony.StateAccepted)

	kinds := []Kind{KindAnswered, KindBusy, KindNoAnswer, KindFailed, KindCancelled, KindRinging}
	var wg sync.WaitGroup
	errs := make(chan error, len(kinds))
	for i, k := range kinds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.in.Ingest(context.Background(), ev(fmt.Sprintf("e-%d", i), "pc-1", k)); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Ingest: %v", err)
	}
	if terminal, _ := r.store.Terminal(context.Background(), id); !terminal {
		// Answered may win first; a failure kind then conflicts (busy etc.) or
		// applies (failed). Either way, if the call is not terminal it must be
		// connected and the failure kinds recorded as conflicts.
		if s := r.state(t, id); s != telephony.StateConnected {
			t.Errorf("non-terminal call in %s, want connected", s)
		}
	}
}

// ---------------------------------------------------------------------------
// 10. Terminal protection
// ---------------------------------------------------------------------------

func TestIngest_TerminalCallsAreNeverResurrected(t *testing.T) {
	t.Parallel()
	r := newRig(t, 64)
	id := r.call(t, "pc-1")
	r.to(t, id, telephony.StateRinging)

	got := r.ingest(t, ev("e-busy", "pc-1", KindBusy))
	if got.Outcome != OutcomeApplied || got.To != telephony.StateFailed {
		t.Fatalf("busy = %+v, want applied to failed", got)
	}
	if _, err := r.h.Runtime.Registry().Get(id); !errors.Is(err, telephony.ErrCallNotFound) {
		t.Fatalf("the ended call is still registered: %v", err)
	}

	opsBefore := r.life.ops.Load()
	for i, k := range []Kind{KindAnswered, KindRinging, KindFailed, KindCompleted} {
		late := r.ingest(t, ev(fmt.Sprintf("late-%d", i), "pc-1", k))
		if late.Outcome != OutcomeStale || late.Reason != ReasonCallTerminal || late.Call != id {
			t.Errorf("late %s = %+v, want stale (call_terminal)", k, late)
		}
	}
	if n := r.life.ops.Load() - opsBefore; n != 0 {
		t.Errorf("%d lifecycle operations on a terminal call", n)
	}
	if _, err := r.h.Runtime.Registry().Get(id); !errors.Is(err, telephony.ErrCallNotFound) {
		t.Errorf("a terminal call reappeared: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 11. The in-memory store and its limits
// ---------------------------------------------------------------------------

func TestMemoryStore_RefusesNonPositiveCapacity(t *testing.T) {
	t.Parallel()
	for _, c := range []int{0, -1} {
		if _, err := NewMemoryStore(c); err == nil {
			t.Errorf("NewMemoryStore(%d) succeeded", c)
		}
	}
	if _, err := NewIngestor(nil, nil, nil); err == nil {
		t.Error("NewIngestor with no ports succeeded")
	}
}

func TestMemoryStore_FullOfInFlightKeysFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := NewMemoryStore(2)
	fp := Fingerprint{ProviderCallID: "pc", Kind: KindRinging}
	tokens := map[string]string{}
	for _, id := range []string{"a", "b"} {
		res, err := s.Reserve(ctx, Key{provider, id}, fp)
		if err != nil || res.Status != StatusReserved || res.Token == "" {
			t.Fatalf("Reserve(%s) = %+v, %v", id, res, err)
		}
		tokens[id] = res.Token
	}
	if tokens["a"] == tokens["b"] {
		t.Error("two reservations share a token")
	}
	if _, err := s.Reserve(ctx, Key{provider, "c"}, fp); !errors.Is(err, ErrStoreFull) {
		t.Fatalf("Reserve with every key in flight = %v, want %v", err, ErrStoreFull)
	}
	// Settling one frees room by forgetting it.
	if err := s.Settle(ctx, Key{provider, "a"}, tokens["a"], Result{Outcome: OutcomeApplied}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Reserve(ctx, Key{provider, "c"}, fp)
	if err != nil || res.Status != StatusReserved {
		t.Errorf("Reserve after a settle = %+v, %v", res, err)
	}
	if _, forgotten, _ := s.Stats(); forgotten != 1 {
		t.Errorf("forgotten = %d, want 1", forgotten)
	}
	// Settling or releasing without holding the key is refused.
	if err := s.Settle(ctx, Key{provider, "zzz"}, "anything", Result{}); !errors.Is(err, ErrNotOwner) {
		t.Errorf("Settle of an unreserved key = %v, want ErrNotOwner", err)
	}
	if err := s.Release(ctx, Key{provider, "c"}, tokens["b"]); !errors.Is(err, ErrNotOwner) {
		t.Errorf("Release with another reservation's token = %v, want ErrNotOwner", err)
	}
	if err := s.Settle(ctx, Key{provider, "c"}, "", Result{}); !errors.Is(err, ErrNotOwner) {
		t.Errorf("Settle with an empty token = %v, want ErrNotOwner", err)
	}
	if err := s.Release(ctx, Key{provider, "c"}, res.Token); err != nil {
		t.Errorf("Release of a held key: %v", err)
	}
	if err := s.Release(ctx, Key{provider, "c"}, res.Token); !errors.Is(err, ErrNotOwner) {
		t.Errorf("double Release = %v, want ErrNotOwner", err)
	}
	// A settled key cannot be settled or released again, even by its owner.
	if err := s.Release(ctx, Key{provider, "a"}, tokens["a"]); !errors.Is(err, ErrNotOwner) {
		t.Errorf("Release after Settle = %v, want ErrNotOwner", err)
	}
}

// TestMemoryStore_ForgottenKeysAreReJudgedNotReApplied documents the bounded
// window: once a key is forgotten, its redelivery is not recognised as a
// duplicate — but state-based judgement still refuses a second transition.
func TestMemoryStore_ForgottenKeysAreReJudgedNotReApplied(t *testing.T) {
	t.Parallel()
	r := newRig(t, 1)
	r.outbound(t, "pc-1")

	if got := r.ingest(t, ev("e-ans", "pc-1", KindAnswered)); got.Outcome != OutcomeApplied {
		t.Fatalf("first: %+v", got)
	}
	// A second key pushes the first out of a one-key window.
	if got := r.ingest(t, ev("e-hold-ring", "pc-1", KindRinging)); got.Outcome != OutcomeStale {
		t.Fatalf("second: %+v", got)
	}
	got := r.ingest(t, ev("e-ans", "pc-1", KindAnswered))
	if got.Outcome != OutcomeAlreadySatisfied {
		t.Errorf("redelivery after the window = %+v, want already_satisfied (re-judged, not duplicate)", got)
	}
	if n := r.life.ops.Load(); n != 1 {
		t.Errorf("lifecycle applied %d times, want 1", n)
	}
}

// TestMemoryStore_DoesNotSurviveARestart: a fresh store (a restarted process,
// or a second replica) does not know the event. The redelivery is re-judged
// and does not transition twice; this is the limit of the guarantee, not
// exactly-once.
func TestMemoryStore_DoesNotSurviveARestart(t *testing.T) {
	t.Parallel()
	r := newRig(t, 64)
	r.outbound(t, "pc-1")
	if got := r.ingest(t, ev("e-ans", "pc-1", KindAnswered)); got.Outcome != OutcomeApplied {
		t.Fatalf("first: %+v", got)
	}

	fresh, _ := NewMemoryStore(64)
	restarted, err := NewIngestor(fresh, r.res, r.life)
	if err != nil {
		t.Fatal(err)
	}
	got, err := restarted.Ingest(context.Background(), ev("e-ans", "pc-1", KindAnswered))
	if err != nil || got.Outcome != OutcomeAlreadySatisfied {
		t.Errorf("after restart = %+v, %v; want already_satisfied, not duplicate", got, err)
	}
	if n := r.life.ops.Load(); n != 1 {
		t.Errorf("lifecycle applied %d times, want 1", n)
	}
}

func TestMemoryStore_BoundsTerminalMarksAndConflicts(t *testing.T) {
	t.Parallel()
	s, _ := NewMemoryStore(2)
	ctx := context.Background()
	for _, id := range []telephony.CallID{"call_a", "call_b", "call_c"} {
		if err := s.MarkTerminal(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	term := func(id telephony.CallID) bool { ok, _ := s.Terminal(ctx, id); return ok }
	if term("call_a") || !term("call_b") || !term("call_c") {
		t.Error("terminal marks are not bounded oldest-first")
	}
	for i := 0; i < 3; i++ {
		if err := s.RecordConflict(ctx, Conflict{Reason: fmt.Sprintf("r%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	cs := s.Conflicts()
	if len(cs) != 2 || cs[0].Reason != "r1" {
		t.Errorf("conflicts = %+v, want the newest two", cs)
	}
	if _, _, dropped := s.Stats(); dropped != 1 {
		t.Errorf("droppedConflicts = %d, want 1", dropped)
	}
}
