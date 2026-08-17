// Package voiceintel wires the Phase 13 intent classifier into the voice path.
//
// It is composition, not architecture. The pieces it joins already existed and
// already fit:
//
//	voice.Planner            (pipeline.go:96)   — what voice asks for
//	*conversation.Conversation                  — what already satisfies it
//	conversation.WithClassifier (engine.go:201) — the injection point
//	conversation.IntentClassifier (intent.go:111) — implemented by intent
//
// The only thing missing was somebody calling WithClassifier with a real
// classifier. Before this package, conversation.NewEngine appeared exactly
// once in the repository — in voice/e2e_test.go — and without it, which meant
// intent.go:277 applied to every production utterance: "an engine with no
// classifier resolves every utterance to the fallback intent".
//
// NOTHING IS DUPLICATED HERE. No planner, no intent engine, no FSM, no context
// engine, no governance decision, no tool execution. Those all live in frozen
// modules and are reached exactly as they were.
//
// See docs/adr/0016-intent-classification.md.
package voiceintel

import (
	"errors"
	"fmt"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/intent"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	"github.com/callscreen/callscreen-platform/packages/go/voice"
)

// Compile-time proof that the type this package hands back is exactly what
// voice asks for.
//
// The voice package already declares this at pipeline.go:123. Restating it
// here is not
// redundancy: it means a change to voice.Planner breaks THIS package's build,
// at the seam, rather than silently leaving the composition wired to an
// interface nobody satisfies any more.
var _ voice.Planner = (*conversation.Conversation)(nil)

// Bridge builds planners backed by the real classifier.
//
// One Bridge per deployment; one planner per call. The classifier inside is
// immutable and shared — it holds no per-utterance state (proved in T5), so
// sharing it across concurrent sessions cannot leak one caller's words into
// another's classification. Session state lives where it always did, in the
// per-conversation engines that conversation itself creates.
type Bridge struct {
	engine *conversation.Engine
}

// Option configures a Bridge.
type Option func(*options)

type options struct {
	convCfg    conversation.Config
	intentCfg  intent.Config
	clock      rt.Clock
	classifier conversation.IntentClassifier
}

// WithConversationConfig overrides the conversation configuration.
//
// The intent thresholds live here — AcceptThreshold, RejectThreshold,
// AmbiguityMargin, MinASRConfidence — and they stay conversation's to own.
// This package passes the config through and applies none of them itself.
func WithConversationConfig(c conversation.Config) Option {
	return func(o *options) { o.convCfg = c }
}

// WithIntentConfig overrides the classifier configuration (lexicon, candidate
// cap).
func WithIntentConfig(c intent.Config) Option {
	return func(o *options) { o.intentCfg = c }
}

// WithClock injects a clock, so a caller can drive conversations
// deterministically.
func WithClock(c rt.Clock) Option {
	return func(o *options) { o.clock = c }
}

// WithClassifier substitutes a different [conversation.IntentClassifier].
//
// Present for two reasons: a model-backed classifier is expected to arrive
// behind this same interface (ADR-0016 §8), and a test needs to be able to
// prove that swapping the classifier changes the outcome — which is how the
// wiring is shown to be load-bearing rather than decorative.
func WithClassifier(c conversation.IntentClassifier) Option {
	return func(o *options) { o.classifier = c }
}

// New builds a Bridge with the deterministic classifier wired in.
func New(opts ...Option) (*Bridge, error) {
	o := &options{
		convCfg:   conversation.DefaultConfig(),
		intentCfg: intent.DefaultConfig(),
	}
	for _, opt := range opts {
		opt(o)
	}

	classifier := o.classifier
	if classifier == nil {
		c, err := intent.New(o.intentCfg)
		if err != nil {
			return nil, fmt.Errorf("voiceintel: building classifier: %w", err)
		}
		classifier = c
	}

	engineOpts := []conversation.Option{conversation.WithClassifier(classifier)}
	if o.clock != nil {
		engineOpts = append(engineOpts, conversation.WithClock(o.clock))
	}

	eng, err := conversation.NewEngine(o.convCfg, engineOpts...)
	if err != nil {
		return nil, fmt.Errorf("voiceintel: building conversation engine: %w", err)
	}
	return &Bridge{engine: eng}, nil
}

// ErrNoBridge is returned by a method on a nil Bridge.
var ErrNoBridge = errors.New("voiceintel: bridge is nil")

// Planner begins a conversation and returns it as a [voice.Planner].
//
// The return type is the interface voice consumes, not the concrete type, so a
// caller cannot reach past the seam into conversation internals — and so this
// package cannot quietly grow into a second dialogue API.
//
// The persona may be empty, which selects the engine's default.
func (b *Bridge) Planner(id conversation.ConversationID, persona conversation.PersonaID) (voice.Planner, error) {
	if b == nil {
		return nil, ErrNoBridge
	}
	conv, err := b.engine.Begin(id, persona)
	if err != nil {
		return nil, fmt.Errorf("voiceintel: beginning conversation %q: %w", id, err)
	}
	return conv, nil
}

// Conversation returns the underlying conversation for a planner this Bridge
// created — needed because slot VALUES cannot cross the classifier port —
// conversation.Slot carries name, filled, confidence and required, and no
// value (intent.go:32) — so anything that wants values must go to the context
// engine, which is reached through Conversation.Context(). Exposing the
// conversation is how a caller reaches that sanctioned API; this package does
// not read or write context itself, and invents no parallel value transport.
// See T4's finding.
func (b *Bridge) Conversation(id conversation.ConversationID) (*conversation.Conversation, bool) {
	if b == nil {
		return nil, false
	}
	return b.engine.Get(id)
}

// Engine exposes the wired engine, for a caller that needs its metrics or
// clock.
func (b *Bridge) Engine() *conversation.Engine {
	if b == nil {
		return nil
	}
	return b.engine
}
