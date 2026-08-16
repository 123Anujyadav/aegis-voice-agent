package telephony

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

func harness(t *testing.T, opts ...HarnessOption) *Harness {
	t.Helper()
	h, err := NewHarness(opts...)
	if err != nil {
		t.Fatalf("build harness: %v", err)
	}
	return h
}

func started(t *testing.T, opts ...HarnessOption) *Harness {
	t.Helper()
	h := harness(t, opts...)
	if _, err := h.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _, _ = h.Stop(context.Background()) })
	return h
}

// ---------------------------------------------------------------------------
// State machine
// ---------------------------------------------------------------------------

func TestState_FifteenStatesExist(t *testing.T) {
	t.Parallel()
	if got := len(AllStates()); got != 15 {
		t.Errorf("AllStates() has %d entries, the brief requires 15", got)
	}
	seen := map[CallState]bool{}
	for _, s := range AllStates() {
		if seen[s] {
			t.Errorf("state %s is listed twice", s)
		}
		seen[s] = true
		if !s.Valid() {
			t.Errorf("state %s does not validate", s)
		}
	}
}

// TestState_TransitionTableIsComplete is the check that "no implicit
// transitions" is enforceable.
//
// Every state must appear in the table, and every destination must be a
// declared state. A state missing from the table is one with no outgoing edges
// that nobody declared terminal — a call that reaches it stops forever, and the
// symptom is a leaked session rather than an error.
func TestState_TransitionTableIsComplete(t *testing.T) {
	t.Parallel()
	spec := transitionSpec()

	for _, s := range AllStates() {
		targets, present := spec[s]
		if !present {
			t.Errorf("state %s is absent from the transition table", s)
			continue
		}
		if s.Terminal() && len(targets) > 0 {
			t.Errorf("terminal state %s declares %d outgoing transitions", s, len(targets))
		}
		if !s.Terminal() && len(targets) == 0 {
			t.Errorf("non-terminal state %s has no outgoing transitions; a call "+
				"reaching it would stop forever", s)
		}
		for _, to := range targets {
			if !to.Valid() {
				t.Errorf("%s -> %s names an undeclared state", s, to)
			}
			if to == s {
				t.Errorf("%s declares a self-transition", s)
			}
		}
	}

	for from := range spec {
		if !from.Valid() {
			t.Errorf("transition table contains undeclared state %s", from)
		}
	}
}

// TestState_EveryStateIsReachable catches a state nobody can get to.
//
// Recovery is exempt: it is initial-only by design, entered by [Restore] rather
// than by a transition, because a live session that needs recovery is one this
// process already lost.
func TestState_EveryStateIsReachable(t *testing.T) {
	t.Parallel()

	reachable := map[CallState]bool{StateIdle: true, StateRecovery: true}
	for _, targets := range transitionSpec() {
		for _, to := range targets {
			reachable[to] = true
		}
	}

	for _, s := range AllStates() {
		if !reachable[s] {
			t.Errorf("state %s is unreachable", s)
		}
	}
}

func TestState_TerminalStates(t *testing.T) {
	t.Parallel()
	terminal := map[CallState]bool{StateEnded: true, StateFailed: true}
	for _, s := range AllStates() {
		if got := s.Terminal(); got != terminal[s] {
			t.Errorf("%s.Terminal() = %v, want %v", s, got, terminal[s])
		}
	}

	// Rejected and Timeout deliberately are not terminal: both still require
	// teardown, and a model where rejection ends the story has nowhere to put it.
	if StateRejected.Terminal() {
		t.Error("Rejected is terminal; the teardown that follows it would be unmodelled")
	}
	if StateTimeout.Terminal() {
		t.Error("Timeout is terminal; a timed-out call still needs teardown")
	}
}

func TestState_ActiveExcludesOnlyIdleAndTerminal(t *testing.T) {
	t.Parallel()
	for _, s := range AllStates() {
		want := s != StateIdle && !s.Terminal()
		if got := s.Active(); got != want {
			t.Errorf("%s.Active() = %v, want %v", s, got, want)
		}
	}
	// A rejected call still holds a channel until teardown. A scheduler that
	// assumed otherwise would over-admit.
	if !StateRejected.Active() {
		t.Error("Rejected is not Active; capacity accounting would under-count")
	}
}

func TestState_ConnectedVariants(t *testing.T) {
	t.Parallel()
	for _, s := range []CallState{StateConnected, StateMuted, StateHold} {
		if !s.Connected() {
			t.Errorf("%s.Connected() = false", s)
		}
	}
	for _, s := range []CallState{StateRinging, StateAccepted, StateEnded} {
		if s.Connected() {
			t.Errorf("%s.Connected() = true", s)
		}
	}
}

// TestState_MutedCannotTransfer pins a decision that looks arbitrary.
func TestState_MutedCannotTransfer(t *testing.T) {
	t.Parallel()
	if CanTransition(StateMuted, StateTransferred) {
		t.Error("a muted call can be transferred; the far end receives a silent " +
			"leg indistinguishable from a broken one")
	}
	if !CanTransition(StateMuted, StateConnected) {
		t.Error("a muted call cannot be unmuted")
	}
	if !CanTransition(StateHold, StateTransferred) {
		t.Error("a held call cannot be transferred, which is the normal case")
	}
}

func TestState_FSMRefusesUndeclaredTransition(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Now())
	fsm, err := newCallFSM(StateIdle, clock)
	if err != nil {
		t.Fatal(err)
	}

	// Idle straight to Connected: never declared, and the whole point of the
	// table is that this is impossible rather than discouraged.
	if _, err := fsm.To(StateConnected); !errors.Is(err, rt.ErrInvalidTransition) {
		t.Errorf("err = %v, want ErrInvalidTransition", err)
	}
	if fsm.State() != StateIdle {
		t.Errorf("state moved to %s after a refused transition", fsm.State())
	}
}

func TestState_CallMayOnlyBeginIdleOrRecovery(t *testing.T) {
	t.Parallel()
	clock := rt.NewFakeClock(time.Now())

	for _, s := range []CallState{StateIdle, StateRecovery} {
		if _, err := newCallFSM(s, clock); err != nil {
			t.Errorf("a call could not begin at %s: %v", s, err)
		}
	}
	// Fabricating a connected call that never rang must be impossible.
	for _, s := range []CallState{StateConnected, StateRinging, StateEnded} {
		if _, err := newCallFSM(s, clock); err == nil {
			t.Errorf("a call was allowed to begin at %s", s)
		}
	}
}

func TestTransitionsFrom_IsSorted(t *testing.T) {
	t.Parallel()
	for _, s := range AllStates() {
		got := TransitionsFrom(s)
		for i := 1; i < len(got); i++ {
			if got[i] < got[i-1] {
				t.Errorf("TransitionsFrom(%s) is unsorted: %v", s, got)
				break
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Identifiers
// ---------------------------------------------------------------------------

func TestIDs_AreUniqueAndPrefixed(t *testing.T) {
	t.Parallel()
	seen := make(map[CallID]bool, 2000)
	for i := 0; i < 2000; i++ {
		id := NewCallID()
		if seen[id] {
			t.Fatalf("duplicate call identifier after %d mints: %s", i, id)
		}
		seen[id] = true
		if !id.Valid() {
			t.Fatalf("identifier %q does not validate", id)
		}
		if !strings.HasPrefix(string(id), "call_") {
			t.Fatalf("identifier %q lacks its prefix", id)
		}
	}
}

// TestIDs_AreRoughlySortable checks that byte order matches time order.
//
// The timestamp prefix is what lets these be a database primary key without a
// secondary index on created_at — a UUIDv4 primary key scatters writes across
// every B-tree page.
//
// MANY PAIRS, not one. The first version compared a single pair and passed
// about half the time against an alphabet whose byte order did not match its
// value order; it took -count=3 -shuffle=on to fail it. One sample cannot
// distinguish "ordered" from "ordered by luck".
func TestIDs_AreRoughlySortable(t *testing.T) {
	t.Parallel()

	const samples = 40
	ids := make([]CallID, 0, samples)
	for i := 0; i < samples; i++ {
		ids = append(ids, NewCallID())
		time.Sleep(2 * time.Millisecond)
	}

	for i := 1; i < len(ids); i++ {
		if !(ids[i-1] < ids[i]) {
			t.Fatalf("identifiers are not time-ordered at %d: %s !< %s",
				i, ids[i-1], ids[i])
		}
	}
}

// TestIDs_AlphabetIsAsciiSortable checks the property directly.
//
// The encoding sorts correctly only if the alphabet is ascending in ASCII, so
// that base32 value order and byte order agree. Checking the alphabet is
// stronger than checking sampled identifiers, because it cannot pass by luck.
func TestIDs_AlphabetIsAsciiSortable(t *testing.T) {
	t.Parallel()

	const alphabet = "0123456789abcdefghjkmnpqrstvwxyz"
	if len(alphabet) != 32 {
		t.Fatalf("alphabet has %d characters, base32 needs 32", len(alphabet))
	}
	for i := 1; i < len(alphabet); i++ {
		if alphabet[i] <= alphabet[i-1] {
			t.Fatalf("alphabet is not ascending at %d (%q then %q): base32 value "+
				"order and byte order disagree, so the timestamp prefix does not sort",
				i, alphabet[i-1], alphabet[i])
		}
	}
	// Crockford omits these because they are confusable when read aloud.
	for _, r := range "ilou" {
		if strings.ContainsRune(alphabet, r) {
			t.Errorf("alphabet contains %q, which Crockford omits as ambiguous", r)
		}
	}

	// And the encoder in use must be the one just checked.
	if got := idAlphabet.EncodeToString([]byte{0, 0, 0, 0, 0}); got != "00000000" {
		t.Errorf("encoder produced %q for zero bytes; it is not the checked alphabet", got)
	}
}

func TestProviderID_Validation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		id   ProviderID
		want bool
	}{
		{"carrier-primary", true},
		{"carrier_2", true},
		{"", false},
		{"Carrier", false},         // uppercase breaks metric-label normalisation
		{"carrier.primary", false}, // dot collides with a topic segment
		{"carrier primary", false},
		{ProviderID(strings.Repeat("a", 65)), false},
	}
	for _, tc := range cases {
		if got := tc.id.Valid(); got != tc.want {
			t.Errorf("ProviderID(%q).Valid() = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Call context
// ---------------------------------------------------------------------------

func TestCallContext_ValidationReportsEveryProblem(t *testing.T) {
	t.Parallel()
	err := CallContext{}.Validate()
	if err == nil {
		t.Fatal("an empty context validated")
	}
	var cfg *ConfigError
	if !errors.As(err, &cfg) {
		t.Fatalf("err = %T, want *ConfigError", err)
	}
	// Four problems minimum: caller, callee, direction, channel, provider.
	if len(cfg.Problems) < 4 {
		t.Errorf("reported %d problems, want every one: %v", len(cfg.Problems), cfg.Problems)
	}
}

func TestCallContext_BoundsMetadataAndTags(t *testing.T) {
	t.Parallel()
	h := harness(t)

	cc := h.Inbound()
	cc.Metadata = make(map[string]string, maxMetadataEntries+1)
	for i := 0; i <= maxMetadataEntries; i++ {
		cc.Metadata[fmt.Sprintf("k%d", i)] = "v"
	}
	if err := cc.Validate(); err == nil {
		t.Error("oversized metadata validated; it reaches a durable event stream")
	}

	cc = h.Inbound()
	cc.Tags = make([]string, maxTags+1)
	for i := range cc.Tags {
		cc.Tags[i] = "t"
	}
	if err := cc.Validate(); err == nil {
		t.Error("too many tags validated")
	}
}

func TestCallContext_CloneIsDeep(t *testing.T) {
	t.Parallel()
	h := harness(t)
	cc := h.Inbound()
	cc.Metadata = map[string]string{"a": "1"}

	clone := cc.Clone()
	clone.Metadata["a"] = "2"
	clone.Tags[0] = "mutated"

	if cc.Metadata["a"] != "1" {
		t.Error("clone shares the metadata map")
	}
	if cc.Tags[0] == "mutated" {
		t.Error("clone shares the tags slice")
	}
}

func TestEndpoint_StringDisclosesNothing(t *testing.T) {
	t.Parallel()
	// The rendered form reaches log lines. It must not be able to carry a
	// number even if an adapter wrongly put one in Ref.
	e := Endpoint{Ref: "ref-abc", Display: "unknown", Country: "IN"}
	if got := e.String(); strings.Contains(got, "ref-abc") {
		t.Errorf("String() = %q, which discloses the reference", got)
	}
}

// ---------------------------------------------------------------------------
// Session
// ---------------------------------------------------------------------------

func TestSession_TransitionRecordsHistory(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	sess, err := h.Connected(ctx)
	if err != nil {
		t.Fatal(err)
	}

	history := sess.History()
	if len(history) != 5 {
		t.Fatalf("history has %d entries, want 5 (incoming, ringing, screening, "+
			"accepted, connected): %v", len(history), history)
	}
	want := []CallState{StateIncoming, StateRinging, StateScreening, StateAccepted, StateConnected}
	for i, w := range want {
		if history[i].To != w {
			t.Errorf("history[%d].To = %s, want %s", i, history[i].To, w)
		}
		if history[i].Seq != i+1 {
			t.Errorf("history[%d].Seq = %d, want %d", i, history[i].Seq, i+1)
		}
	}
}

// TestSession_SequenceOrdersWithinOneClockTick is why Seq exists.
func TestSession_SequenceOrdersWithinOneClockTick(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	// The fake clock never advances during this test, so every record shares a
	// timestamp. Without Seq a reader could not order them.
	sess, err := h.Connected(ctx)
	if err != nil {
		t.Fatal(err)
	}

	history := sess.History()
	for i := 1; i < len(history); i++ {
		if !history[i].At.Equal(history[0].At) {
			t.Skip("the clock advanced; this test needs a frozen clock")
		}
		if history[i].Seq <= history[i-1].Seq {
			t.Errorf("sequence did not advance: %d then %d",
				history[i-1].Seq, history[i].Seq)
		}
	}
}

func TestSession_ReasonCodeIsBounded(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	sess, err := h.BeginInbound(ctx)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		reason string
	}{
		{"empty", ""},
		{"too long", strings.Repeat("a", reasonCodeMax+1)},
		{"uppercase", "Rejected"},
		{"free text with a number", "caller +91 98765 43210 hung up"},
		{"hyphen", "screening-rejected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := sess.Transition(StateRinging, tc.reason); err == nil {
				t.Errorf("reason %q was accepted; free text on this path reaches "+
					"a durable event stream", tc.reason)
			}
		})
	}

	if err := sess.Transition(StateRinging, "carrier_alerting.1"); err != nil {
		t.Errorf("a valid reason code was refused: %v", err)
	}
}

func TestSession_DurationAndTalkDurationDiffer(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	sess, err := h.BeginInbound(ctx)
	if err != nil {
		t.Fatal(err)
	}
	l := h.Runtime.Lifecycle()

	h.Clock.Advance(30 * time.Second) // ringing
	if err := l.Ring(ctx, sess.ID()); err != nil {
		t.Fatal(err)
	}
	if err := l.Screen(ctx, sess.ID()); err != nil {
		t.Fatal(err)
	}
	if err := l.Accept(ctx, sess.ID(), "screening_passed"); err != nil {
		t.Fatal(err)
	}
	if err := l.Connect(ctx, sess.ID()); err != nil {
		t.Fatal(err)
	}
	h.Clock.Advance(5 * time.Second) // talking

	// A call that rang for thirty seconds and talked for five occupied a
	// channel for thirty-five and is billable for five.
	if got := sess.Duration(); got != 35*time.Second {
		t.Errorf("Duration() = %v, want 35s", got)
	}
	if got := sess.TalkDuration(); got != 5*time.Second {
		t.Errorf("TalkDuration() = %v, want 5s", got)
	}
}

func TestSession_TalkDurationIsZeroWhenNeverConnected(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	sess, err := h.BeginInbound(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h.Clock.Advance(10 * time.Second)

	if got := sess.TalkDuration(); got != 0 {
		t.Errorf("TalkDuration() = %v for a call that never connected, want 0", got)
	}
}

func TestSession_HistoryIsBoundedAndKeepsTheFirstRecord(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	sess, err := h.Connected(ctx)
	if err != nil {
		t.Fatal(err)
	}
	l := h.Runtime.Lifecycle()

	// Flap between hold and connected far past the bound.
	for i := 0; i < maxHistory+50; i++ {
		if err := l.Hold(ctx, sess.ID()); err != nil {
			t.Fatalf("hold %d: %v", i, err)
		}
		if err := l.Unhold(ctx, sess.ID()); err != nil {
			t.Fatalf("unhold %d: %v", i, err)
		}
	}

	history := sess.History()
	if len(history) > maxHistory {
		t.Errorf("history grew to %d, cap is %d", len(history), maxHistory)
	}
	// "How did this call begin" is the first question a review asks.
	if history[0].To != StateIncoming {
		t.Errorf("history[0].To = %s, want the first transition preserved", history[0].To)
	}
}

func TestSession_AttrsAreBounded(t *testing.T) {
	t.Parallel()
	h := started(t)
	sess, err := h.BeginInbound(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < maxMetadataEntries; i++ {
		if err := sess.SetAttr(fmt.Sprintf("k%d", i), "v"); err != nil {
			t.Fatalf("SetAttr %d: %v", i, err)
		}
	}
	if err := sess.SetAttr("overflow", "v"); err == nil {
		t.Error("attributes grew past the cap; this reaches the snapshot")
	}
	// Replacing an existing key must still work at the cap.
	if err := sess.SetAttr("k0", "replaced"); err != nil {
		t.Errorf("replacing an existing attribute at the cap failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

func TestRegistry_RegisterRefusesDuplicates(t *testing.T) {
	t.Parallel()
	h := harness(t)
	reg := NewCallRegistry()

	sess, err := reg.Create(h.Inbound(), h.Clock)
	if err != nil {
		t.Fatal(err)
	}
	// Replacing would silently discard a live call's session and orphan its
	// goroutines, with no error anywhere.
	if err := reg.Register(sess); !errors.Is(err, ErrCallExists) {
		t.Errorf("err = %v, want ErrCallExists", err)
	}
}

func TestRegistry_LenIsO1AndAccurate(t *testing.T) {
	t.Parallel()
	h := harness(t)
	reg := NewCallRegistry()

	var ids []CallID
	for i := 0; i < 500; i++ {
		s, err := reg.Create(h.Inbound(), h.Clock)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.ID())
	}
	if got := reg.Len(); got != 500 {
		t.Errorf("Len() = %d, want 500", got)
	}
	for _, id := range ids[:200] {
		reg.Remove(id)
	}
	if got := reg.Len(); got != 300 {
		t.Errorf("Len() = %d after removals, want 300", got)
	}
	if got := reg.Total(); got != 500 {
		t.Errorf("Total() = %d, want 500 — it counts every call ever", got)
	}
}

// TestRegistry_ShardsSpreadLoad guards the hash choice.
//
// Identifiers begin with a millisecond timestamp, so a hash over a prefix would
// put every call created in the same millisecond on one shard — exactly the
// burst a call storm produces.
func TestRegistry_ShardsSpreadLoad(t *testing.T) {
	t.Parallel()
	h := harness(t)
	reg := NewCallRegistry()

	const n = 4096
	for i := 0; i < n; i++ {
		if _, err := reg.Create(h.Inbound(), h.Clock); err != nil {
			t.Fatal(err)
		}
	}

	depths := reg.ShardDepths()
	expected := n / registryShards
	var empty, hot int
	for _, d := range depths {
		if d == 0 {
			empty++
		}
		if d > expected*3 {
			hot++
		}
	}
	if empty > 0 {
		t.Errorf("%d of %d shards are empty after %d calls", empty, registryShards, n)
	}
	if hot > 0 {
		t.Errorf("%d shards hold more than 3x the mean depth (%d)", hot, expected)
	}
}

// TestRegistry_EachDoesNotHoldAShardLock is a deadlock regression test.
func TestRegistry_EachDoesNotHoldAShardLock(t *testing.T) {
	t.Parallel()
	h := harness(t)
	reg := NewCallRegistry()

	for i := 0; i < 10; i++ {
		if _, err := reg.Create(h.Inbound(), h.Clock); err != nil {
			t.Fatal(err)
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// A callback that registers — which is what a sweep transitioning a
		// call into a new session would do — must not deadlock against the
		// shard lock Each was walking.
		reg.Each(func(s *CallSession) bool {
			reg.Has(s.ID())
			reg.Remove(s.ID())
			return true
		})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Each deadlocked while its callback touched the registry")
	}
}

func TestRegistry_ByStateIsOnePass(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if _, err := h.Connected(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := h.BeginInbound(ctx); err != nil {
			t.Fatal(err)
		}
	}

	counts := h.Runtime.Registry().ByState()
	if counts[StateConnected] != 5 {
		t.Errorf("connected = %d, want 5", counts[StateConnected])
	}
	if counts[StateIncoming] != 3 {
		t.Errorf("incoming = %d, want 3", counts[StateIncoming])
	}
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

func TestConfig_DefaultIsValid(t *testing.T) {
	t.Parallel()
	if problems := DefaultConfig().validate(); len(problems) > 0 {
		t.Errorf("DefaultConfig is invalid: %v", problems)
	}
}

func TestConfig_RefusesUnboundedConcurrency(t *testing.T) {
	t.Parallel()
	cfg := TestConfig()
	cfg.MaxConcurrentCalls = 0

	problems := cfg.validate()
	var found bool
	for _, p := range problems {
		if strings.Contains(p, "call storm") {
			found = true
		}
	}
	if !found {
		t.Errorf("problems = %v, want one explaining the consequence", problems)
	}
}

// TestConfig_RefusesASweepIntervalThatCannotEnforceADeadline catches a
// misconfiguration whose symptom is a deadline that silently fires late.
func TestConfig_RefusesASweepIntervalThatCannotEnforceADeadline(t *testing.T) {
	t.Parallel()
	cfg := TestConfig()
	cfg.ScreenTimeout = 500 * time.Millisecond
	cfg.SweepInterval = 5 * time.Second

	problems := cfg.validate()
	var found bool
	for _, p := range problems {
		if strings.Contains(p, "SweepInterval") {
			found = true
		}
	}
	if !found {
		t.Errorf("a 5s sweep with a 500ms deadline was accepted: %v", problems)
	}
}

func TestConfig_CapacityAppliesHighWater(t *testing.T) {
	t.Parallel()
	cfg := TestConfig()
	cfg.MaxConcurrentCalls = 1000
	cfg.AdmissionHighWater = 0.9

	if got := cfg.Capacity(); got != 900 {
		t.Errorf("Capacity() = %d, want 900", got)
	}
}

// ---------------------------------------------------------------------------
// Scheduler
// ---------------------------------------------------------------------------

func TestScheduler_ShedsAtCapacity(t *testing.T) {
	t.Parallel()
	cfg := TestConfig()
	cfg.MaxConcurrentCalls = 10
	cfg.MaxCallsPerProvider = 0
	cfg.AdmissionHighWater = 1.0

	h := started(t, WithHarnessConfig(cfg))
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		if _, err := h.BeginInbound(ctx); err != nil {
			t.Fatalf("call %d refused: %v", i, err)
		}
	}
	if _, err := h.BeginInbound(ctx); !errors.Is(err, ErrCapacityExceeded) {
		t.Errorf("err = %v, want ErrCapacityExceeded", err)
	}
	if got := h.Runtime.Shed(); got != 1 {
		t.Errorf("shed = %d, want 1", got)
	}
}

func TestScheduler_PerProviderCeiling(t *testing.T) {
	t.Parallel()
	cfg := TestConfig()
	cfg.MaxConcurrentCalls = 100
	cfg.MaxCallsPerProvider = 5
	cfg.AdmissionHighWater = 1.0

	h := started(t, WithHarnessConfig(cfg))
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if _, err := h.BeginInbound(ctx); err != nil {
			t.Fatalf("call %d refused: %v", i, err)
		}
	}
	// One carrier's retry storm must not consume the whole runtime.
	_, err := h.BeginInbound(ctx)
	if !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("err = %v, want ErrCapacityExceeded", err)
	}
	if !strings.Contains(err.Error(), "provider_capacity_exceeded") {
		t.Errorf("err = %v, want the provider ceiling named", err)
	}
}

// TestScheduler_ReleasePairsWithAdmit is the capacity-leak guard.
func TestScheduler_ReleasePairsWithAdmit(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	sess, err := h.Connected(ctx)
	if err != nil {
		t.Fatal(err)
	}
	provider := sess.Context().Provider
	if got := h.Runtime.Scheduler().Live(provider); got != 1 {
		t.Fatalf("live = %d after one call, want 1", got)
	}

	if err := h.Coordinator.End(ctx, sess.ID(), "caller_hung_up"); err != nil {
		t.Fatal(err)
	}
	if got := h.Runtime.Scheduler().Live(provider); got != 0 {
		t.Errorf("live = %d after the call ended; the slot leaked", got)
	}
}

// TestScheduler_FailedStartReleasesTheSlot covers the path that leaks quietly.
func TestScheduler_FailedStartReleasesTheSlot(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	// An outbound context against a provider that cannot dial: admitted, then
	// the lifecycle refuses. The slot must come back.
	cc := h.Outbound()
	cc.Capabilities = NewCapabilities(CapAnswer) // no CapDial

	if _, err := h.Coordinator.Begin(ctx, cc); err == nil {
		t.Fatal("a call was started against a provider that cannot dial")
	}
	if got := h.Runtime.Scheduler().Live(cc.Provider); got != 0 {
		t.Errorf("live = %d after a failed start; the slot leaked", got)
	}
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

func TestEvents_TopicShapeIsVersioned(t *testing.T) {
	t.Parallel()
	for _, typ := range AllEventTypes() {
		topic := typ.Topic()
		if !strings.HasPrefix(topic, "telephony.call.") {
			t.Errorf("%s topic %q lacks the domain prefix", typ, topic)
		}
		if !strings.HasSuffix(topic, ".v1") {
			t.Errorf("%s topic %q is unversioned; retrofitting a version onto a "+
				"live topic needs a dual-write migration", typ, topic)
		}
		if strings.Contains(topic, "-") {
			t.Errorf("%s topic %q contains a hyphen, which collides with "+
				"Prometheus metric-name normalisation", typ, topic)
		}
	}
}

// TestEvents_CarryNoContent is the invariant-I7 check.
func TestEvents_CarryNoContent(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	sess, err := h.Connected(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Coordinator.End(ctx, sess.ID(), "caller_hung_up"); err != nil {
		t.Fatal(err)
	}

	// The test applied during design: if this topic were retained forever and
	// could never be deleted, would that be a compliance failure?
	for _, e := range h.Events.Events() {
		rendered := fmt.Sprintf("%+v", e)
		for _, forbidden := range []string{"ref-caller-1", "ref-callee-1"} {
			if strings.Contains(rendered, forbidden) {
				t.Errorf("event %s carries the endpoint reference: %s", e.Type, rendered)
			}
		}
	}
}

func TestEvents_PartitionKeyIsTheCall(t *testing.T) {
	t.Parallel()
	// Keying on anything coarser puts a busy carrier's calls on one partition
	// and makes it the bottleneck.
	e := Event{Call: "call_abc", Provider: "carrier-1"}
	if got := e.PartitionKey(); got != "call_abc" {
		t.Errorf("PartitionKey() = %q, want the call identifier", got)
	}
}

func TestEvents_MutedAndHeldAreSilent(t *testing.T) {
	t.Parallel()
	if _, ok := stateEvent(StateMuted); ok {
		t.Error("Muted publishes an event; a call toggling hold twenty times " +
			"would produce forty events describing nothing a consumer acts on")
	}
	if _, ok := stateEvent(StateHold); ok {
		t.Error("Hold publishes an event")
	}
}

func TestEvents_SequenceAdvancesPerCall(t *testing.T) {
	t.Parallel()
	h := started(t)
	ctx := context.Background()

	sess, err := h.Connected(ctx)
	if err != nil {
		t.Fatal(err)
	}

	events := h.Events.ForCall(sess.ID())
	if len(events) < 2 {
		t.Fatalf("only %d events for the call", len(events))
	}
	for i := 1; i < len(events); i++ {
		if events[i].Sequence <= events[i-1].Sequence {
			t.Errorf("sequence did not advance: %d then %d",
				events[i-1].Sequence, events[i].Sequence)
		}
	}
}

func TestRecordingPublisher_IsBoundedAndReportsDrops(t *testing.T) {
	t.Parallel()
	p := NewBoundedRecordingPublisher(10)
	for i := 0; i < 25; i++ {
		if err := p.Publish(context.Background(), Event{Type: EventCallCreated}); err != nil {
			t.Fatal(err)
		}
	}
	if got := p.Len(); got != 10 {
		t.Errorf("Len() = %d, want the bound of 10", got)
	}
	// A test asserting on event counts must not be silently misled by
	// truncation.
	if got := p.Dropped(); got != 15 {
		t.Errorf("Dropped() = %d, want 15", got)
	}
}
