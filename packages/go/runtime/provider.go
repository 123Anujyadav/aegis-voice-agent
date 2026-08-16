package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// Provider is the runtime's only knowledge of a language model vendor.
//
// Everything vendor-specific — authentication, wire format, streaming protocol,
// error taxonomy, token accounting — lives behind this interface, in a module
// that is not this one. The kernel has no import of any vendor SDK, which is
// what makes "provider agnostic" a structural property rather than a claim.
//
// A Provider is expected to be long-lived, shared across goroutines, and to own
// its own connection pooling. Implementations must be safe for concurrent use.
type Provider interface {
	// ID returns the provider's stable identifier. Used as a metric label and
	// in the registry, so it must not change between releases.
	ID() ProviderID

	// Capabilities describes what this provider can do. The runtime consults
	// this before routing, so a request is never sent to a provider that cannot
	// serve it.
	Capabilities() Capabilities

	// Generate begins a generation and returns a stream of chunks.
	//
	// It must return promptly — a Provider that blocks here until the first
	// token has arrived defeats streaming and inflates time-to-first-token by
	// the whole model latency. Any work that can be deferred belongs in the
	// stream's Recv.
	//
	// Cancelling ctx must terminate the underlying request and cause the
	// returned stream's Recv to return promptly. The runtime's barge-in budget
	// (20 ms, ADR-0011) depends on this, and a Provider that ignores
	// cancellation cannot meet it.
	Generate(ctx context.Context, req GenerateRequest) (TokenStream, error)

	// Probe reports whether the provider is currently able to serve. It must be
	// cheap and must respect the context deadline: it runs on the readiness
	// path, and a probe that blocks turns a health check into a timeout.
	Probe(ctx context.Context) error

	// Close releases the provider's resources. After Close, Generate must
	// return ErrClosed.
	Close() error
}

// Capabilities describes what a provider supports.
//
// The runtime routes on this rather than on the provider's name, so adding a
// vendor requires no change to routing logic.
type Capabilities struct {
	// Streaming reports whether the provider can emit incremental chunks. A
	// provider that cannot is still usable — the runtime collects its single
	// chunk — but it cannot serve a latency-sensitive path.
	Streaming bool

	// Thinking reports whether the provider supports extended thinking. This
	// matters for Invariant I3: a tool-calling tier requires it, and a provider
	// without it cannot be bound to such a model.
	Thinking bool

	// ToolCalling reports whether the provider can emit structured tool calls.
	// The runtime does not implement tool calling — that is the orchestration
	// layer's job — but it must know whether a chunk stream may contain them.
	ToolCalling bool

	// MaxContextTokens is the largest context the provider will accept. The
	// context manager treats this as a hard ceiling.
	MaxContextTokens int

	// MaxOutputTokens is the largest completion the provider will produce.
	MaxOutputTokens int
}

// Role identifies who authored a message.
//
// The runtime does not interpret roles; it carries them. The set is closed
// because a provider adapter must be able to map it exhaustively, and an open
// set would push an unmappable value onto every adapter.
type Role string

const (
	// RoleSystem carries instructions to the model.
	RoleSystem Role = "system"

	// RoleUser carries input from the model's counterparty.
	RoleUser Role = "user"

	// RoleAssistant carries the model's own prior output.
	RoleAssistant Role = "assistant"
)

// Message is one entry in a generation request's context.
//
// It is deliberately minimal. The runtime does not model attachments, tool
// results, or citations, because those are orchestration concepts. A provider
// adapter that needs them carries them in Metadata, which the runtime passes
// through untouched.
type Message struct {
	// Role identifies the author.
	Role Role

	// Content is the message text.
	//
	// PRIVACY. On the screening path this carries caller speech, which is
	// classified SENSITIVE by annotations.proto. It must never be logged, never
	// appear in a metric label, and never be attached to a span. The runtime's
	// logging and tracing helpers do not accept a Message for exactly this
	// reason — see [Kernel.Logger] and [Tracer].
	Content string

	// Pinned marks a message the context manager must never evict. Used for
	// content whose absence would change meaning rather than merely reduce it.
	Pinned bool

	// Tokens is the message's measured token count, filled in by the context
	// manager. Zero means unmeasured.
	Tokens int

	// Metadata is opaque provider-specific data, passed through untouched.
	Metadata map[string]string
}

// GenerateRequest is a fully-resolved request to a provider.
//
// "Fully resolved" is the important word: by the time a request reaches a
// provider, model selection, context assembly, budget enforcement and invariant
// checking have all happened. A provider adapter performs no policy.
type GenerateRequest struct {
	// RequestID correlates this request across logs, metrics and traces.
	RequestID RequestID

	// SessionID identifies the owning session.
	SessionID SessionID

	// Model is the resolved model to invoke.
	Model ModelID

	// Messages is the assembled context, oldest first.
	Messages []Message

	// System carries system-level instruction, where the provider distinguishes
	// it from the message list.
	System string

	// MaxOutputTokens bounds the completion.
	MaxOutputTokens int

	// Temperature controls sampling. A nil pointer means "provider default",
	// which is distinct from zero — zero is a valid, meaningful temperature and
	// a plain float64 could not express the difference.
	Temperature *float64

	// Thinking requests extended thinking.
	//
	// INVARIANT I3. For a model whose tier supports tool calling this must be
	// true. The runtime enforces it in [ModelRegistry.BuildRequest] rather than
	// trusting callers, because disabling it silently drops tool calls with no
	// error — an invisible failure, which is precisely why the invariant exists.
	Thinking bool

	// Deadline is the absolute instant by which the request must complete. It
	// is an instant rather than a duration because it is derived from a budget
	// that has already been partly spent upstream, and re-deriving a duration
	// at each hop is how latency budgets silently inflate.
	Deadline time.Time

	// Metadata is opaque provider-specific data.
	Metadata map[string]string
}

// ChunkKind classifies a streamed chunk.
type ChunkKind int

const (
	// ChunkText is model output intended for the counterparty.
	ChunkText ChunkKind = iota

	// ChunkThinking is extended-thinking output.
	//
	// It is emitted so the runtime can account for it and so a provider adapter
	// need not filter it. It must NEVER be persisted, published in an event, or
	// rendered to any user — Invariant INV-AI-10. [Dispatcher] enforces this by
	// refusing to deliver thinking chunks to a sink not explicitly marked as
	// accepting them.
	ChunkThinking

	// ChunkToolCall is a structured tool invocation request. The runtime
	// carries it; it does not execute it.
	ChunkToolCall

	// ChunkUsage reports token accounting. Providers may emit it at any point;
	// the runtime accumulates rather than overwriting.
	ChunkUsage

	// ChunkDone marks normal completion. A stream that ends without one has
	// terminated abnormally, and the runtime records that distinctly from a
	// clean end.
	ChunkDone
)

// String renders the kind for logs and metric labels.
func (k ChunkKind) String() string {
	switch k {
	case ChunkText:
		return "text"
	case ChunkThinking:
		return "thinking"
	case ChunkToolCall:
		return "tool_call"
	case ChunkUsage:
		return "usage"
	case ChunkDone:
		return "done"
	default:
		return "unknown"
	}
}

// Chunk is one increment of a generation.
type Chunk struct {
	// Kind classifies the chunk.
	Kind ChunkKind

	// Text carries content for ChunkText and ChunkThinking.
	//
	// PRIVACY. SENSITIVE on the screening path. Never logged, never a metric
	// label, never a span attribute.
	Text string

	// Index is the chunk's ordinal within its stream, starting at zero. The
	// dispatcher uses it to detect gaps, which a lossy transport would
	// otherwise hide.
	Index int

	// Usage carries accounting on a ChunkUsage chunk.
	Usage Usage

	// ToolCall carries the raw tool invocation on a ChunkToolCall chunk. It is
	// an opaque string: the runtime does not parse it.
	ToolCall string

	// ReceivedAt is when the runtime observed the chunk. Set by the runtime,
	// not the provider, because a provider's clock is not ours to trust and
	// time-to-first-token must be measured where it is experienced.
	ReceivedAt time.Time
}

// Usage reports token accounting for a generation.
type Usage struct {
	// InputTokens counts tokens in the request.
	InputTokens int

	// OutputTokens counts tokens generated.
	OutputTokens int

	// ThinkingTokens counts extended-thinking tokens, where the provider
	// reports them separately. They are billed and they consume the latency
	// budget, so conflating them with output would understate both.
	ThinkingTokens int

	// CachedInputTokens counts input tokens served from a provider-side cache.
	CachedInputTokens int
}

// Add accumulates other into u. Providers may report usage incrementally, and
// overwriting rather than accumulating loses everything but the last report.
func (u *Usage) Add(other Usage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.ThinkingTokens += other.ThinkingTokens
	u.CachedInputTokens += other.CachedInputTokens
}

// Total returns the sum of every counted token.
func (u Usage) Total() int {
	return u.InputTokens + u.OutputTokens + u.ThinkingTokens
}

// TokenStream delivers chunks from a provider.
//
// Recv returns io.EOF at normal end. Any other error terminates the stream.
// Close must be safe to call more than once and from a goroutine other than the
// one calling Recv — the dispatcher relies on both.
type TokenStream interface {
	// Recv returns the next chunk, or io.EOF when the stream has ended
	// normally.
	Recv() (Chunk, error)

	// Close releases the stream. It is idempotent and safe to call
	// concurrently with Recv.
	Close() error
}

// ---------------------------------------------------------------------------
// Stream helpers
// ---------------------------------------------------------------------------

// Collect drains a stream to completion, returning the concatenated text and
// accumulated usage.
//
// This is how a non-streaming call is served: as a stream collected to
// completion. The reverse — a batch API with streaming bolted on — is what
// produces runtimes where cancellation does not work, because there is nothing
// to cancel between request and response.
//
// Thinking chunks are counted in usage and deliberately excluded from the
// returned text (INV-AI-10).
func Collect(ctx context.Context, s TokenStream) (string, Usage, error) {
	defer func() { _ = s.Close() }()

	var (
		sb    []byte
		usage Usage
	)
	for {
		if err := ctx.Err(); err != nil {
			return string(sb), usage, err
		}
		chunk, err := s.Recv()
		if errors.Is(err, io.EOF) {
			return string(sb), usage, nil
		}
		if err != nil {
			return string(sb), usage, err
		}
		switch chunk.Kind {
		case ChunkText:
			sb = append(sb, chunk.Text...)
		case ChunkUsage:
			usage.Add(chunk.Usage)
		case ChunkDone:
			return string(sb), usage, nil
		}
	}
}

// sliceStream is a TokenStream over a fixed slice of chunks. It backs the test
// harness and any provider that cannot genuinely stream.
type sliceStream struct {
	mu     sync.Mutex
	chunks []Chunk
	pos    int
	closed bool
	clock  Clock
}

// NewSliceStream returns a TokenStream that emits the supplied chunks in order
// and then io.EOF.
//
// Exported so provider adapters for non-streaming vendors can wrap a single
// response without reimplementing the contract, and so tests in other modules
// can build streams without depending on the harness.
func NewSliceStream(clock Clock, chunks ...Chunk) TokenStream {
	if clock == nil {
		clock = SystemClock{}
	}
	return &sliceStream{chunks: chunks, clock: clock}
}

// Recv returns the next chunk.
func (s *sliceStream) Recv() (Chunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Chunk{}, ErrClosed
	}
	if s.pos >= len(s.chunks) {
		return Chunk{}, io.EOF
	}
	c := s.chunks[s.pos]
	c.Index = s.pos
	c.ReceivedAt = s.clock.Now()
	s.pos++
	return c, nil
}

// Close marks the stream closed. Idempotent.
func (s *sliceStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}
