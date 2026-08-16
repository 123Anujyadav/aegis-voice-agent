package memory

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Harness wires a memory runtime for test with a controllable clock, a
// recording publisher and a recording auditor.
//
// Exported rather than test-only because every service embedding this engine
// needs it: a service testing its own reaction to memory events should not have
// to reimplement a fake clock, a fake publisher and a consent checker. A
// harness only this package can use pushes every consumer into real time.
type Harness struct {
	// Clock is the controllable clock.
	Clock *rt.FakeClock
	// Metrics is the runtime's instrument set.
	Metrics *Metrics
	// Runtime is the engine under test.
	Runtime *Runtime
	// Events records everything published.
	Events *RecordingPublisher
	// Audit records every audited access.
	Audit *RecordingAuditor
	// Consent is the consent allow list.
	Consent *staticConsent
}

// HarnessOption customises a Harness.
type HarnessOption func(*harnessOptions)

type harnessOptions struct {
	cfg    *Config
	logger *slog.Logger
}

// WithHarnessConfig overrides the runtime configuration.
func WithHarnessConfig(c Config) HarnessOption {
	return func(o *harnessOptions) { o.cfg = &c }
}

// WithHarnessLogger sets the logger. Defaults to discarding, so a passing test
// is silent and a failing one is readable.
func WithHarnessLogger(l *slog.Logger) HarnessOption {
	return func(o *harnessOptions) { o.logger = l }
}

// NewHarness builds a memory runtime wired for test.
func NewHarness(opts ...HarnessOption) (*Harness, error) {
	o := &harnessOptions{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, opt := range opts {
		opt(o)
	}

	clock := rt.NewFakeClock(rt.SystemClock{}.Now().Truncate(0))
	metrics := NewMetrics()
	events := NewRecordingPublisher()
	audit := NewRecordingAuditor()
	consent := NewStaticConsent()

	cfg := DefaultConfig()
	if o.cfg != nil {
		cfg = *o.cfg
	}
	cfg.Policy.Auditor = audit
	if cfg.Policy.Consent == nil {
		cfg.Policy.Consent = consent
	}

	r, err := New(cfg,
		WithClock(clock), WithMetrics(metrics),
		WithLogger(o.logger), WithPublisher(events))
	if err != nil {
		return nil, err
	}

	return &Harness{Clock: clock, Metrics: metrics, Runtime: r,
		Events: events, Audit: audit, Consent: consent}, nil
}

// Bundle returns a namespace's stack, failing loudly on a typo rather than
// silently creating an empty namespace in a test.
func (h *Harness) Bundle(ns Namespace) *Bundle {
	b, ok := h.Runtime.Registry().Get(ns)
	if !ok {
		panic("memory: harness asked for unregistered namespace " + ns.String())
	}
	return b
}

// Assistant returns the consumer-assistant namespace, the common case in test.
func (h *Harness) Assistant() *Bundle { return h.Bundle(NamespaceAssistant) }

// Grant records a consent basis so Personal and Sensitive writes are admitted.
func (h *Harness) Grant(subject SubjectID, ref string) *Harness {
	h.Consent.Grant(subject, ref)
	return h
}

// Seed stores n conversation records for a subject, returning their keys.
//
// Advances the clock between writes so creation timestamps are distinct — a
// seeded set with identical timestamps makes every recency assertion depend on
// a tie-break rather than on the ordering under test.
func (h *Harness) Seed(ns Namespace, subject SubjectID, n int, kind Kind) []Key {
	b := h.Bundle(ns)
	keys := make([]Key, 0, n)
	for i := 0; i < n; i++ {
		r := &Record{
			Key:         Key{Subject: subject, Kind: kind, Name: fmt.Sprintf("seed-%03d", i)},
			Tier:        TierShortTerm,
			Value:       Value{ContentType: "text/plain", Data: []byte(fmt.Sprintf("record %d", i))},
			Sensitivity: Internal,
			Retention:   RetentionStandard,
			Provenance:  Provenance{Source: "harness", Ref: fmt.Sprintf("seed-%d", i)},
		}
		stored, err := b.Store.Store(r)
		if err != nil {
			panic("memory: harness seed failed: " + err.Error())
		}
		keys = append(keys, stored.Key)
		h.Clock.Advance(rt.SystemClock{}.Now().Sub(rt.SystemClock{}.Now()) + 1e6) // 1 ms
	}
	return keys
}

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// ConcatSummarizer is a deterministic Summarizer for test.
//
// It is the ONLY Summarizer in this module and it does no summarising: it
// concatenates. The Phase 10C brief excludes LLM summarisation, so a real one
// would breach scope. This exists to prove the compression FRAMEWORK — the
// selection, the budget, the classification inheritance, the all-or-nothing
// replacement — without a model.
type ConcatSummarizer struct {
	// Separator is placed between inputs.
	Separator string
	// FailAfter makes Summarize fail after n successful calls, for
	// failure-injection tests. Zero never fails.
	FailAfter int

	// FailAlways makes every call fail. A separate flag rather than a negative
	// FailAfter, because overloading a count with a sentinel value is how a
	// test ends up silently not testing what it names.
	FailAlways bool

	calls atomic.Int64
}

// Summarize concatenates the inputs, truncating to the budget.
func (c *ConcatSummarizer) Summarize(inputs []*Record, budget TokenBudget) (Value, error) {
	n := c.calls.Add(1)
	if c.FailAlways || (c.FailAfter > 0 && n > int64(c.FailAfter)) {
		return Value{}, errors.New("summarizer: scripted failure")
	}

	sep := c.Separator
	if sep == "" {
		sep = "\n"
	}
	var data []byte
	for i, in := range inputs {
		if i > 0 {
			data = append(data, sep...)
		}
		data = append(data, in.Value.Data...)
	}

	// Respect the budget by truncating. A real summariser would condense; this
	// one cuts, which is enough to prove the budget is honoured.
	est := budget.Estimator
	if est == nil {
		est = DefaultTokenEstimator()
	}
	for est.Estimate(data) > budget.Available() && len(data) > 0 {
		data = data[:len(data)*3/4]
	}

	return Value{ContentType: "text/plain", Data: data}, nil
}

// Calls returns how many times Summarize has been called.
func (c *ConcatSummarizer) Calls() int64 { return c.calls.Load() }

// JoinMerger is a deterministic Merger for test.
type JoinMerger struct{ Separator string }

// Merge concatenates inputs in the order given.
func (m JoinMerger) Merge(inputs []*Record) (Value, error) {
	sep := m.Separator
	if sep == "" {
		sep = "|"
	}
	var data []byte
	for i, in := range inputs {
		if i > 0 {
			data = append(data, sep...)
		}
		data = append(data, in.Value.Data...)
	}
	return Value{ContentType: "text/plain", Data: data}, nil
}

// HalfSplitter divides a record's payload in two, for test.
type HalfSplitter struct{}

// Split produces two parts named "-a" and "-b".
func (HalfSplitter) Split(in *Record) (map[string]Value, error) {
	data := in.Value.Data
	mid := len(data) / 2
	return map[string]Value{
		in.Key.Name + "-a": {ContentType: in.Value.ContentType, Data: data[:mid]},
		in.Key.Name + "-b": {ContentType: in.Value.ContentType, Data: data[mid:]},
	}, nil
}

// ReverseEncryptor is a trivial reversible transform for test.
//
// NOT CRYPTOGRAPHY, and named so nobody mistakes it for any. It exists to prove
// the [Encryptor] hook is wired on both the store and retrieve paths. Real key
// management belongs to the platform's KMS integration, which this module
// deliberately does not implement.
type ReverseEncryptor struct {
	mu     sync.Mutex
	failOn SubjectID
}

// FailFor makes Encrypt fail for a subject, for failure-injection tests.
func (e *ReverseEncryptor) FailFor(s SubjectID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.failOn = s
}

// Encrypt reverses the payload.
func (e *ReverseEncryptor) Encrypt(s SubjectID, plaintext []byte) ([]byte, error) {
	e.mu.Lock()
	fail := e.failOn == s
	e.mu.Unlock()
	if fail {
		return nil, errors.New("encryptor: scripted failure")
	}
	return reverseBytes(plaintext), nil
}

// Decrypt reverses the payload back.
func (e *ReverseEncryptor) Decrypt(_ SubjectID, ciphertext []byte) ([]byte, error) {
	return reverseBytes(ciphertext), nil
}

func reverseBytes(in []byte) []byte {
	out := make([]byte, len(in))
	for i, b := range in {
		out[len(in)-1-i] = b
	}
	return out
}

// PersonalRecord builds a Personal record with a consent reference, the shape
// most policy tests need.
func PersonalRecord(subject SubjectID, kind Kind, name, data, consentRef string) *Record {
	return &Record{
		Key:         Key{Subject: subject, Kind: kind, Name: name},
		Tier:        TierShortTerm,
		Value:       Value{ContentType: "text/plain", Data: []byte(data)},
		Sensitivity: Personal,
		Retention:   RetentionStandard,
		ConsentRef:  consentRef,
		Provenance:  Provenance{Source: "test"},
	}
}

// InternalRecord builds an Internal record, which needs no consent basis.
func InternalRecord(subject SubjectID, kind Kind, name, data string) *Record {
	return &Record{
		Key:         Key{Subject: subject, Kind: kind, Name: name},
		Tier:        TierShortTerm,
		Value:       Value{ContentType: "text/plain", Data: []byte(data)},
		Sensitivity: Internal,
		Retention:   RetentionStandard,
		Provenance:  Provenance{Source: "test"},
	}
}

// WithAttributes returns a copy of r carrying the given attributes.
func WithAttributes(r *Record, attrs map[string]string) *Record {
	c := r.Clone()
	if c.Value.Attributes == nil {
		c.Value.Attributes = make(map[string]string, len(attrs))
	}
	for k, v := range attrs {
		c.Value.Attributes[k] = v
	}
	return c
}
