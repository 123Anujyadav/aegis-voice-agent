package telephony

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Harness wires a runtime with fakes for testing.
//
// EXPORTED ON PURPOSE, following the convention every phase since 10A has
// established: a service embedding this runtime needs to test its own code
// against it, and forcing every consumer to rebuild this scaffolding is how
// six subtly different fakes come to exist. Phase 10F's adapters were built
// almost entirely on the harnesses the earlier phases exported.
type Harness struct {
	Runtime     *TelephonyRuntime
	Clock       *rt.FakeClock
	Events      *RecordingPublisher
	Store       *MemorySessionStore
	Provider    *FakeProvider
	Metrics     *RuntimeMetrics
	Coordinator *CallCoordinator
	Dispatcher  *CallDispatcher
}

// TestConfig returns [DefaultConfig] with the drain budget shortened.
//
// EXPORTED BECAUSE THE ALTERNATIVE IS A FOOTGUN. The drain budget is measured
// in real time (see [TelephonyRuntime.drain]), so a runtime stopped with live
// calls waits it out for real — thirty seconds, at the default. A test that
// builds its own Config with DefaultConfig and passes it to
// [WithHarnessConfig] therefore silently reintroduces a thirty-second shutdown,
// and the symptom is a suite that grew slow for no visible reason.
//
// Every test that customises configuration should start here rather than at
// DefaultConfig.
func TestConfig() Config {
	cfg := DefaultConfig()
	// Long enough to exercise the drain loop, short enough that a suite
	// stopping a hundred runtimes does not spend half a minute in shutdown.
	cfg.DrainTimeout = 100 * time.Millisecond
	return cfg
}

// HarnessOption customises a harness.
type HarnessOption func(*harnessOptions)

type harnessOptions struct {
	cfg      Config
	logger   *slog.Logger
	liveness LivenessCheck
	caps     []Capability
	noStore  bool
}

// WithHarnessConfig overrides the configuration.
func WithHarnessConfig(c Config) HarnessOption {
	return func(o *harnessOptions) { o.cfg = c }
}

// WithHarnessLogger attaches a logger, for debugging a failing test.
func WithHarnessLogger(l *slog.Logger) HarnessOption {
	return func(o *harnessOptions) { o.logger = l }
}

// WithHarnessLiveness sets the recovery liveness probe.
func WithHarnessLiveness(f LivenessCheck) HarnessOption {
	return func(o *harnessOptions) { o.liveness = f }
}

// WithHarnessCapabilities sets the fake provider's capabilities.
func WithHarnessCapabilities(caps ...Capability) HarnessOption {
	return func(o *harnessOptions) { o.caps = caps }
}

// WithoutSessionStore builds a runtime with no store, to exercise the
// no-recovery path.
func WithoutSessionStore() HarnessOption {
	return func(o *harnessOptions) { o.noStore = true }
}

// NewHarness builds a runtime with a fake clock, provider, store and publisher.
//
// The runtime is NOT started: a test that wants recovery seeds the store first
// and then calls Start, and a harness that started eagerly would make that
// impossible.
func NewHarness(opts ...HarnessOption) (*Harness, error) {
	o := &harnessOptions{
		cfg:    TestConfig(),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		caps: []Capability{CapDial, CapAnswer, CapReject, CapHangup,
			CapHold, CapMute, CapTransfer},
	}
	for _, opt := range opts {
		opt(o)
	}

	clock := rt.NewFakeClock(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
	events := NewRecordingPublisher()
	provider := NewFakeProvider("fake-carrier", NewCapabilities(o.caps...))
	m := NewRuntimeMetrics()

	runtimeOpts := []Option{
		WithClock(clock), WithLogger(o.logger), WithPublisher(events),
		WithMetrics(m),
	}

	var store *MemorySessionStore
	if !o.noStore {
		store = NewMemorySessionStore()
		runtimeOpts = append(runtimeOpts, WithSessionStore(store))
	}
	if o.liveness != nil {
		runtimeOpts = append(runtimeOpts, WithLivenessCheck(o.liveness))
	}

	r, err := New(o.cfg, runtimeOpts...)
	if err != nil {
		return nil, err
	}
	if err := r.RegisterProvider(provider); err != nil {
		return nil, err
	}

	return &Harness{
		Runtime: r, Clock: clock, Events: events, Store: store,
		Provider: provider, Metrics: m,
		Coordinator: r.Coordinator(), Dispatcher: r.Dispatcher(),
	}, nil
}

// Start starts the runtime.
func (h *Harness) Start(ctx context.Context) (RecoveryReport, error) {
	return h.Runtime.Start(ctx)
}

// Stop stops the runtime.
func (h *Harness) Stop(ctx context.Context) (int, error) { return h.Runtime.Stop(ctx) }

// Inbound returns a valid inbound call context for the fake provider.
func (h *Harness) Inbound() CallContext {
	return CallContext{
		Caller:       Endpoint{Ref: "ref-caller-1", Display: "unknown", Country: "IN"},
		Callee:       Endpoint{Ref: "ref-callee-1", Display: "subscriber", Country: "IN"},
		Direction:    DirectionInbound,
		Channel:      ChannelPSTN,
		Provider:     h.Provider.ID(),
		Capabilities: h.Provider.Capabilities(),
		Tags:         []string{"unknown-caller"},
	}
}

// Outbound returns a valid outbound call context for the fake provider.
func (h *Harness) Outbound() CallContext {
	cc := h.Inbound()
	cc.Direction = DirectionOutbound
	cc.Tags = []string{"callback"}
	return cc
}

// BeginInbound admits an inbound call.
func (h *Harness) BeginInbound(ctx context.Context) (*CallSession, error) {
	return h.Coordinator.Begin(ctx, h.Inbound())
}

// Connected drives a call all the way to Connected and returns it.
//
// The setup most lifecycle tests need. Written once here so a test that cares
// about hold, transfer or recovery does not restate five transitions first.
func (h *Harness) Connected(ctx context.Context) (*CallSession, error) {
	sess, err := h.BeginInbound(ctx)
	if err != nil {
		return nil, err
	}
	l := h.Runtime.Lifecycle()
	if err := l.Ring(ctx, sess.ID()); err != nil {
		return nil, err
	}
	if err := l.Screen(ctx, sess.ID()); err != nil {
		return nil, err
	}
	if err := l.Accept(ctx, sess.ID(), "screening_passed"); err != nil {
		return nil, err
	}
	if err := l.Connect(ctx, sess.ID()); err != nil {
		return nil, err
	}
	return sess, nil
}

// ---------------------------------------------------------------------------
// FakeProvider
// ---------------------------------------------------------------------------

// FakeProvider is an in-memory [Provider].
//
// It carries no media and reaches no network — like the real adapters will not,
// from this module's point of view. Every method can be made to fail, which is
// what the failure-injection tests drive.
type FakeProvider struct {
	id   ProviderID
	caps Capabilities

	mu sync.Mutex
	// Failures maps an operation name to the error it should return.
	failures map[string]error
	// delays maps an operation to an artificial latency, applied against the
	// caller's context so a test can prove the runtime's timeout binds.
	delays map[string]time.Duration
	calls  map[string]int
	// answered records which calls were answered, so a test can verify the
	// provider was actually asked rather than assuming.
	answered map[CallID]bool
	hungUp   map[CallID]string
	rejected map[CallID]string
}

// NewFakeProvider builds a fake provider.
func NewFakeProvider(id ProviderID, caps Capabilities) *FakeProvider {
	return &FakeProvider{
		id: id, caps: caps,
		failures: make(map[string]error),
		delays:   make(map[string]time.Duration),
		calls:    make(map[string]int),
		answered: make(map[CallID]bool),
		hungUp:   make(map[CallID]string),
		rejected: make(map[CallID]string),
	}
}

// ID implements Provider.
func (f *FakeProvider) ID() ProviderID { return f.id }

// Capabilities implements Provider.
func (f *FakeProvider) Capabilities() Capabilities { return f.caps }

// FailOn makes an operation return err. Operations: dial, answer, reject,
// hangup.
func (f *FakeProvider) FailOn(op string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[op] = err
}

// ClearFailures removes every injected failure.
func (f *FakeProvider) ClearFailures() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = make(map[string]error)
}

// DelayOn makes an operation take d, honouring context cancellation.
func (f *FakeProvider) DelayOn(op string, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delays[op] = d
}

// Calls returns how many times an operation was invoked.
func (f *FakeProvider) Calls(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[op]
}

// Answered reports whether a call was answered at the provider.
func (f *FakeProvider) Answered(id CallID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.answered[id]
}

// HungUp returns the hangup reason recorded for a call.
func (f *FakeProvider) HungUp(id CallID) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.hungUp[id]
	return r, ok
}

// Rejected returns the reject reason recorded for a call.
func (f *FakeProvider) Rejected(id CallID) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rejected[id]
	return r, ok
}

func (f *FakeProvider) enter(ctx context.Context, op string) error {
	f.mu.Lock()
	f.calls[op]++
	err := f.failures[op]
	delay := f.delays[op]
	f.mu.Unlock()

	if delay > 0 {
		// A real timer, not the fake clock: this simulates a slow carrier, and
		// the property under test is that the runtime's context deadline binds.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	if err != nil {
		return err
	}
	return ctx.Err()
}

// Dial implements Provider.
func (f *FakeProvider) Dial(ctx context.Context, _ CallContext) error {
	return f.enter(ctx, "dial")
}

// Answer implements Provider.
func (f *FakeProvider) Answer(ctx context.Context, id CallID) error {
	if err := f.enter(ctx, "answer"); err != nil {
		return err
	}
	f.mu.Lock()
	f.answered[id] = true
	f.mu.Unlock()
	return nil
}

// Reject implements Provider.
func (f *FakeProvider) Reject(ctx context.Context, id CallID, reason string) error {
	if err := f.enter(ctx, "reject"); err != nil {
		return err
	}
	f.mu.Lock()
	f.rejected[id] = reason
	f.mu.Unlock()
	return nil
}

// Hangup implements Provider.
func (f *FakeProvider) Hangup(ctx context.Context, id CallID, reason string) error {
	if err := f.enter(ctx, "hangup"); err != nil {
		return err
	}
	f.mu.Lock()
	f.hungUp[id] = reason
	f.mu.Unlock()
	return nil
}

// AlwaysLive is a [LivenessCheck] that reports every call still up.
//
// For testing the resume path. Named so its use in production would be
// obviously wrong — see [LivenessCheck] on why assuming a call is live is the
// more dangerous default.
func AlwaysLive(context.Context, Snapshot) bool { return true }

// LiveIf builds a [LivenessCheck] from a predicate on the call identifier.
func LiveIf(pred func(CallID) bool) LivenessCheck {
	return func(_ context.Context, snap Snapshot) bool { return pred(snap.Call) }
}

// String renders the harness for a failure message.
func (h *Harness) String() string {
	return fmt.Sprintf("harness: runtime=%s live=%d events=%d snapshots=%d",
		h.Runtime.State(), h.Runtime.Live(), h.Events.Len(), h.storeLen())
}

func (h *Harness) storeLen() int {
	if h.Store == nil {
		return 0
	}
	return h.Store.Len()
}
