package callback

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
)

// Phase 19 P1-11 — the ingestor against a store that fails.
//
// A durable store can be unreachable. Every failure must stop processing
// before the lifecycle is touched, or — when the lifecycle already moved —
// leave the event retryable without any path to a second transition. A store
// failure is never read as "new event" or "not terminal".

var errStoreDown = errors.New("store unavailable (injected)")

type failingStore struct {
	*MemoryStore
	mu       sync.Mutex
	fail     map[string]bool
	releases int
}

func newFailingStore(t *testing.T) *failingStore {
	t.Helper()
	m, err := NewMemoryStore(64)
	if err != nil {
		t.Fatal(err)
	}
	return &failingStore{MemoryStore: m, fail: map[string]bool{}}
}

func (f *failingStore) set(op string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[op] = on
}

func (f *failingStore) failing(op string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fail[op]
}

func (f *failingStore) Reserve(ctx context.Context, k Key, fp Fingerprint) (Reservation, error) {
	if f.failing("reserve") {
		return Reservation{}, errStoreDown
	}
	return f.MemoryStore.Reserve(ctx, k, fp)
}

func (f *failingStore) Settle(ctx context.Context, k Key, token string, r Result) error {
	if f.failing("settle") {
		return errStoreDown
	}
	return f.MemoryStore.Settle(ctx, k, token, r)
}

func (f *failingStore) Release(ctx context.Context, k Key, token string) error {
	f.mu.Lock()
	f.releases++
	f.mu.Unlock()
	return f.MemoryStore.Release(ctx, k, token)
}

func (f *failingStore) MarkTerminal(ctx context.Context, id telephony.CallID) error {
	if f.failing("mark") {
		return errStoreDown
	}
	return f.MemoryStore.MarkTerminal(ctx, id)
}

func (f *failingStore) Terminal(ctx context.Context, id telephony.CallID) (bool, error) {
	if f.failing("terminal") {
		return false, errStoreDown
	}
	return f.MemoryStore.Terminal(ctx, id)
}

func (f *failingStore) RecordConflict(ctx context.Context, c Conflict) error {
	if f.failing("conflict") {
		return errStoreDown
	}
	return f.MemoryStore.RecordConflict(ctx, c)
}

func failingRig(t *testing.T) (*rig, *failingStore) {
	t.Helper()
	r := newRig(t, 64)
	fs := newFailingStore(t)
	in, err := NewIngestor(fs, r.res, r.life)
	if err != nil {
		t.Fatal(err)
	}
	r.in = in
	return r, fs
}

func (f *failingStore) remembered() int {
	n, _, _ := f.Stats()
	return n
}

func TestStoreFailure_ReserveOutageAppliesNothing(t *testing.T) {
	t.Parallel()
	r, fs := failingRig(t)
	id := r.call(t, "pc-1")
	fs.set("reserve", true)

	if _, err := r.in.Ingest(context.Background(), ev("e-1", "pc-1", KindRinging)); !errors.Is(err, errStoreDown) {
		t.Fatalf("Ingest = %v, want the store failure", err)
	}
	if n := r.life.ops.Load(); n != 0 || r.state(t, id) != telephony.StateIncoming {
		t.Errorf("lifecycle ran %d times during a store outage", n)
	}

	fs.set("reserve", false)
	if got := r.ingest(t, ev("e-1", "pc-1", KindRinging)); got.Outcome != OutcomeApplied {
		t.Errorf("after recovery = %+v, want applied", got)
	}
}

func TestStoreFailure_UnknownTerminalStatusIsNotTreatedAsLive(t *testing.T) {
	t.Parallel()
	r, fs := failingRig(t)
	id := r.call(t, "pc-1")
	fs.set("terminal", true)

	if _, err := r.in.Ingest(context.Background(), ev("e-1", "pc-1", KindRinging)); !errors.Is(err, errStoreDown) {
		t.Fatalf("Ingest = %v, want the store failure", err)
	}
	if n := r.life.ops.Load(); n != 0 || r.state(t, id) != telephony.StateIncoming {
		t.Errorf("lifecycle ran %d times without a terminal answer", n)
	}
	if fs.remembered() != 0 {
		t.Error("the reservation was not released")
	}
	fs.set("terminal", false)
	if got := r.ingest(t, ev("e-1", "pc-1", KindRinging)); got.Outcome != OutcomeApplied {
		t.Errorf("retry = %+v, want applied", got)
	}
}

func TestStoreFailure_SettleFailureReleasesAndNeverReapplies(t *testing.T) {
	t.Parallel()
	r, fs := failingRig(t)
	r.outbound(t, "pc-1")
	fs.set("settle", true)

	got, err := r.in.Ingest(context.Background(), ev("e-ans", "pc-1", KindAnswered))
	if !errors.Is(err, errStoreDown) || got.Outcome != OutcomeApplied {
		t.Fatalf("Ingest = %+v, %v; want the applied result and the store failure", got, err)
	}
	if fs.remembered() != 0 {
		t.Error("an unsettled reservation was left held")
	}

	fs.set("settle", false)
	again := r.ingest(t, ev("e-ans", "pc-1", KindAnswered))
	if again.Outcome != OutcomeAlreadySatisfied {
		t.Errorf("redelivery = %+v, want already_satisfied (re-judged)", again)
	}
	if n := r.life.ops.Load(); n != 1 {
		t.Errorf("lifecycle applied %d times, want 1", n)
	}
}

func TestStoreFailure_LostTerminalMarkNeverResurrects(t *testing.T) {
	t.Parallel()
	r, fs := failingRig(t)
	id := r.call(t, "pc-1")
	fs.set("mark", true)

	if _, err := r.in.Ingest(context.Background(), ev("e-busy", "pc-1", KindBusy)); !errors.Is(err, errStoreDown) {
		t.Fatalf("Ingest = %v, want the store failure", err)
	}
	if _, err := r.h.Runtime.Registry().Get(id); !errors.Is(err, telephony.ErrCallNotFound) {
		t.Fatalf("the call should have failed: %v", err)
	}
	fs.set("mark", false)
	// The redelivery finds the call gone and no mark: refused as unknown,
	// never applied again.
	if _, err := r.in.Ingest(context.Background(), ev("e-busy", "pc-1", KindBusy)); !errors.Is(err, ErrUnknownCall) {
		t.Errorf("redelivery = %v, want ErrUnknownCall", err)
	}
	if n := r.life.ops.Load(); n != 1 {
		t.Errorf("lifecycle applied %d times, want 1", n)
	}
}

func TestStoreFailure_UnrecordedConflictIsRetried(t *testing.T) {
	t.Parallel()
	r, fs := failingRig(t)
	id := r.call(t, "pc-1")
	r.to(t, id, telephony.StateConnected)
	fs.set("conflict", true)

	if _, err := r.in.Ingest(context.Background(), ev("e-busy", "pc-1", KindBusy)); !errors.Is(err, errStoreDown) {
		t.Fatalf("Ingest = %v, want the store failure", err)
	}
	if len(fs.Conflicts()) != 0 || fs.remembered() != 0 {
		t.Error("a conflict that was not recorded was settled anyway")
	}
	fs.set("conflict", false)
	if got := r.ingest(t, ev("e-busy", "pc-1", KindBusy)); got.Outcome != OutcomeConflict {
		t.Errorf("retry = %+v, want conflict", got)
	}
	if len(fs.Conflicts()) != 1 {
		t.Errorf("conflicts = %d, want 1", len(fs.Conflicts()))
	}
}

func TestStoreFailure_ReleaseRunsEvenAfterTheRequestIsCancelled(t *testing.T) {
	t.Parallel()
	r, fs := failingRig(t)
	r.call(t, "pc-1")
	fs.set("terminal", true)

	// The request's context ends while it holds the reservation; the release
	// must still happen, or the key stays blocked.
	ctx, cancel := context.WithCancel(context.Background())
	r.life.delay = 0
	cancelling := &cancelOnTerminal{failingStore: fs, cancel: cancel}
	in, err := NewIngestor(cancelling, r.res, r.life)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.Ingest(ctx, ev("e-1", "pc-1", KindRinging)); err == nil {
		t.Fatal("Ingest succeeded")
	}
	if fs.remembered() != 0 {
		t.Error("the reservation survived a cancelled request")
	}
}

type cancelOnTerminal struct {
	*failingStore
	cancel context.CancelFunc
}

func (c *cancelOnTerminal) Terminal(ctx context.Context, id telephony.CallID) (bool, error) {
	c.cancel()
	return c.failingStore.Terminal(ctx, id)
}
