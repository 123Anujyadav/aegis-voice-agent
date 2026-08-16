// Package ollama adapts a locally running Ollama daemon to the frozen Phase 10A
// generation port.
//
// # Development only, and structurally so
//
// ADR-0006 freezes a four-tier model ladder, every tier on Claude, with exact
// model identifiers — and explicitly REJECTED "self-hosted open-weight model
// (Llama/Mistral class)" as its Option 5. Phase 11E needs a local loop that runs
// without an API key. Those two facts govern different things: ADR-0006 is
// production model routing; this is a development provider.
//
// So this package amends nothing. It is not registered as an ADR-0006 tier, it
// makes no claim against C1, and [Provider.Capabilities] reports what the daemon
// can actually do rather than what a tier requires.
// TestProvider_IsNotAProductionTier is the executable form of that boundary.
//
// # No model name is a default
//
// [Config.Model] has no fallback. A hardcoded default would make some
// particular open-weight model an implicit part of the platform, and the first
// time a caller forgot to set it the system would quietly generate against a
// model nobody chose. Whatever is pulled on a given machine is an environment
// fact, not a supported model.
//
// # HTTP, not the CLI
//
// The daemon is spoken to over stdlib net/http at its configured base URL. No
// process is spawned and no command line is built, so the argv injection surface
// this phase worries about elsewhere is ZERO here — caller text travels as a
// JSON string field. It also means Phase 11E's process supervision does NOT
// apply to Ollama: the daemon's lifetime is the operator's business, and this
// package neither starts nor stops it.
//
// # Genuine streaming, and genuine cancellation
//
// Ollama streams newline-delimited JSON objects, one per token-ish increment.
// That is real streaming — [Provider.Generate] returns as soon as the response
// headers arrive, before the first token — and cancelling the context aborts the
// HTTP request, which is what the 20 ms abort budget in ADR-0011 §5.1 requires.
//
// The 250/550 ms time-to-first-token figures in ADR-0006 and ADR-0011 are NOT
// applied here. They were set for a hosted frontier model over a network; a
// local model on CPU is measured and reported against them as an observation,
// never asserted. This package creates no SLA.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Errors this adapter returns. Callers match with errors.Is.
var (
	// ErrUnavailable is returned when the daemon cannot be reached.
	ErrUnavailable = errors.New("ollama: daemon unavailable")

	// ErrModelMissing is returned when the daemon is up but has not pulled the
	// configured model.
	ErrModelMissing = errors.New("ollama: model not pulled")

	// ErrMalformedResponse is returned when the daemon's output is not what the
	// wire format promises.
	ErrMalformedResponse = errors.New("ollama: malformed response")
)

// DefaultBaseURL is where the daemon listens unless told otherwise.
//
// A default endpoint is safe in a way a default MODEL is not: it addresses a
// service the operator started, and being wrong about it produces a connection
// error rather than silent generation against something nobody chose.
const DefaultBaseURL = "http://127.0.0.1:11434"

// Config describes one Ollama daemon and the model to use on it.
type Config struct {
	// ID is the authored provider identifier. It becomes a metric label.
	ID rt.ProviderID

	// BaseURL is the daemon's address. Empty means [DefaultBaseURL].
	BaseURL string

	// Model is the model to generate with.
	//
	// REQUIRED, WITH NO DEFAULT. See the package comment: naming a model here
	// is a decision the operator makes, and this package refuses to make it for
	// them.
	Model rt.ModelID

	// MaxContextTokens is the context ceiling declared to the runtime.
	//
	// DECLARED, NOT DISCOVERED. Ollama reports a model's training context in
	// its metadata, but the value that matters is the one the daemon was
	// started with, and that is not on the wire. Overstating it produces
	// silent truncation inside the daemon, which looks like a model that
	// forgets rather than a misconfiguration.
	MaxContextTokens int

	// MaxOutputTokens bounds a completion.
	MaxOutputTokens int

	// RequestTimeout bounds one whole generation when the request carries no
	// deadline of its own.
	RequestTimeout time.Duration

	// ProbeTimeout bounds a readiness probe. It runs on the health path, so it
	// is deliberately short.
	ProbeTimeout time.Duration

	// MaxChunkBytes bounds one line of the streamed response.
	//
	// A daemon that never emits a newline would otherwise grow a buffer for the
	// length of the generation.
	MaxChunkBytes int

	// HTTPClient overrides the client. Optional; used by tests and by an
	// operator who needs a proxy.
	HTTPClient *http.Client
}

func (c Config) validate() error {
	var problems []string

	if c.ID == "" {
		problems = append(problems, "ID must be set; it is a metric label")
	}
	if c.Model == "" {
		problems = append(problems, "Model must be set: this adapter has no default "+
			"model, because a default would make some particular open-weight model "+
			"an implicit part of the platform")
	}
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		switch {
		case err != nil:
			problems = append(problems, "BaseURL is not a URL: "+err.Error())
		case u.Scheme != "http" && u.Scheme != "https":
			problems = append(problems, "BaseURL must be http or https, got "+u.Scheme)
		case u.Host == "":
			problems = append(problems, "BaseURL has no host")
		}
	}
	if c.MaxContextTokens <= 0 {
		problems = append(problems, "MaxContextTokens must be positive")
	}
	if c.MaxOutputTokens <= 0 {
		problems = append(problems, "MaxOutputTokens must be positive")
	}
	if c.RequestTimeout <= 0 {
		problems = append(problems, "RequestTimeout must be positive")
	}
	if c.ProbeTimeout <= 0 {
		problems = append(problems, "ProbeTimeout must be positive")
	}
	if c.MaxChunkBytes <= 0 {
		problems = append(problems, "MaxChunkBytes must be positive; an unbounded "+
			"line buffer grows for the length of a generation")
	}

	if len(problems) > 0 {
		return fmt.Errorf("ollama: invalid configuration: %s", strings.Join(problems, "; "))
	}
	return nil
}

// DefaultConfig returns the transport settings, leaving the decisions.
//
// ID and Model are left EMPTY on purpose: they are the two fields nobody should
// inherit from a helper.
func DefaultConfig() Config {
	return Config{
		BaseURL: DefaultBaseURL,
		// Sized for a small local model. An operator running something larger
		// raises them; nothing here guesses from the model name.
		MaxContextTokens: 8192,
		MaxOutputTokens:  2048,
		// Local CPU inference is slow, and a timeout tuned for a hosted model
		// would abort every generation on this machine.
		RequestTimeout: 5 * time.Minute,
		ProbeTimeout:   2 * time.Second,
		MaxChunkBytes:  1 << 20,
	}
}

// Provider implements runtime.Provider over a local Ollama daemon.
type Provider struct {
	cfg    Config
	base   string
	client *http.Client

	closed atomic.Bool
}

// Compile-time proof that the frozen port is satisfied.
var _ rt.Provider = (*Provider)(nil)

// New builds a provider. It does NOT contact the daemon.
//
// Construction stays offline so a process can be assembled without a running
// daemon and report unavailability through [Provider.Probe], which is the path
// the health check already uses. A constructor that dialled would make an
// absent development dependency a startup crash.
func New(cfg Config) (*Provider, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	base = strings.TrimRight(base, "/")

	client := cfg.HTTPClient
	if client == nil {
		// No client-level Timeout: it applies to the WHOLE exchange including
		// the response body, so on a streaming response it would kill a healthy
		// generation partway through. Bounding belongs on the context, which is
		// per-request and which cancellation already uses.
		client = &http.Client{
			Transport: &http.Transport{
				DialContext:           (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
				ResponseHeaderTimeout: 30 * time.Second,
				MaxIdleConnsPerHost:   4,
			},
		}
	}

	return &Provider{cfg: cfg, base: base, client: client}, nil
}

// ID returns the authored provider identifier.
func (p *Provider) ID() rt.ProviderID { return p.cfg.ID }

// Model returns the configured model. Not part of the port; for diagnostics and
// for the documentation that must record which model a measurement used.
func (p *Provider) Model() rt.ModelID { return p.cfg.Model }

// Capabilities describes what this daemon can do.
//
// # Thinking and ToolCalling are false, and that is the point
//
// Both are declared false regardless of the model configured. The runtime's
// Invariant I3 binds tool calling to extended thinking, and a tier that requires
// either cannot be served by this provider — which is exactly the boundary the
// package comment describes. Declaring them true to widen routing would put a
// development provider on a production path.
func (p *Provider) Capabilities() rt.Capabilities {
	return rt.Capabilities{
		Streaming:        true,
		Thinking:         false,
		ToolCalling:      false,
		MaxContextTokens: p.cfg.MaxContextTokens,
		MaxOutputTokens:  p.cfg.MaxOutputTokens,
	}
}

// Probe reports whether the daemon is able to serve.
//
// It checks two things, because they fail differently and an operator needs to
// know which: that the daemon answers at all, and that it has actually pulled
// the configured model. A daemon that is up but missing the model would
// otherwise fail on the first generation, halfway through a call.
func (p *Provider) Probe(ctx context.Context) error {
	if p.closed.Load() {
		return rt.ErrClosed
	}

	ctx, cancel := context.WithTimeout(ctx, p.cfg.ProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/api/tags", nil)
	if err != nil {
		return p.wrap(rt.KindInvalidRequest, 0, err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return p.wrap(rt.KindTransport, 0, fmt.Errorf(
			"%w at %s: %v\n  to fix: start the daemon with 'ollama serve', or set "+
				"BaseURL to where it is listening", ErrUnavailable, p.base, err))
	}
	defer func() {
		// Drained before closing so the connection can be reused rather than
		// torn down on every health check.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return p.wrap(rt.KindTransport, resp.StatusCode, fmt.Errorf(
			"%w: %s answered %s", ErrUnavailable, p.base, resp.Status))
	}

	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return p.wrap(rt.KindTransport, resp.StatusCode,
			fmt.Errorf("%w: /api/tags: %v", ErrMalformedResponse, err))
	}

	want := string(p.cfg.Model)
	available := make([]string, 0, len(tags.Models))
	for _, m := range tags.Models {
		available = append(available, m.Name)
		// Ollama reports "name:tag"; a configured bare name matches its
		// default tag, which is how everyone refers to these models.
		if m.Name == want || strings.TrimSuffix(m.Name, ":latest") == want {
			return nil
		}
	}

	return p.wrap(rt.KindInvalidRequest, resp.StatusCode, fmt.Errorf(
		"%w: %q is not pulled on %s (available: %s)\n  to fix: run 'ollama pull %s'",
		ErrModelMissing, want, p.base, strings.Join(available, ", "), want))
}

// Generate begins a generation and returns a stream.
//
// It returns as soon as the response headers arrive — BEFORE the first token.
// Blocking here until a token existed would defeat streaming and inflate
// time-to-first-token by the whole model latency, which the port explicitly
// forbids.
func (p *Provider) Generate(ctx context.Context, req rt.GenerateRequest) (rt.TokenStream, error) {
	if p.closed.Load() {
		return nil, rt.ErrClosed
	}
	if err := p.checkRequest(req); err != nil {
		return nil, err
	}

	body, err := json.Marshal(p.wireRequest(req))
	if err != nil {
		return nil, p.wrap(rt.KindInvalidRequest, 0, err)
	}

	// The stream owns the cancel function: the HTTP request must outlive this
	// call, and it must die when the stream is closed. Deferring cancel here
	// would abort the generation the instant Generate returned.
	streamCtx, cancel := p.streamContext(ctx, req)

	httpReq, err := http.NewRequestWithContext(streamCtx, http.MethodPost,
		p.base+"/api/chat", bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, p.wrap(rt.KindInvalidRequest, 0, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/x-ndjson")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		cancel()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, p.wrapModel(rt.KindTimeout, 0, req.Model, err)
		}
		return nil, p.wrapModel(rt.KindTransport, 0, req.Model, fmt.Errorf(
			"%w at %s: %v", ErrUnavailable, p.base, err))
	}

	if resp.StatusCode != http.StatusOK {
		detail := readBounded(resp.Body, 4<<10)
		_ = resp.Body.Close()
		cancel()
		return nil, p.wrapModel(kindForStatus(resp.StatusCode), resp.StatusCode,
			req.Model, fmt.Errorf("ollama: %s: %s", resp.Status, detail))
	}

	s := &stream{
		provider: p.cfg.ID,
		model:    req.Model,
		body:     resp.Body,
		cancel:   cancel,
		ctx:      streamCtx,
	}
	s.scanner = bufio.NewScanner(resp.Body)
	s.scanner.Buffer(make([]byte, 0, 8<<10), p.cfg.MaxChunkBytes)

	return s, nil
}

// checkRequest rejects what the daemon cannot serve, before the network.
func (p *Provider) checkRequest(req rt.GenerateRequest) error {
	var problems []string

	if len(req.Messages) == 0 && req.System == "" {
		problems = append(problems, "no messages and no system instruction")
	}
	if req.Thinking {
		// INVARIANT I3 boundary. A request that needs extended thinking has
		// been routed to a provider that declares it cannot do it. Serving it
		// anyway would silently drop the thinking, and I3 exists because that
		// failure is invisible.
		problems = append(problems, "Thinking was requested but this provider "+
			"declares Thinking=false; ADR-0006 keeps thinking tiers on production "+
			"models and this is a development provider")
	}
	for i, m := range req.Messages {
		switch m.Role {
		case rt.RoleSystem, rt.RoleUser, rt.RoleAssistant:
		default:
			problems = append(problems, fmt.Sprintf("message %d has unknown role %q", i, m.Role))
		}
	}

	if len(problems) > 0 {
		return p.wrapModel(rt.KindInvalidRequest, 0, req.Model,
			errors.New(strings.Join(problems, "; ")))
	}
	return nil
}

// streamContext derives the context the HTTP request runs under.
func (p *Provider) streamContext(ctx context.Context, req rt.GenerateRequest) (context.Context, context.CancelFunc) {
	// The request's own deadline wins where it has one: it was derived from a
	// budget already partly spent upstream, and replacing it with a fresh
	// timeout here is how a latency budget silently inflates.
	if !req.Deadline.IsZero() {
		return context.WithDeadline(ctx, req.Deadline)
	}
	return context.WithTimeout(ctx, p.cfg.RequestTimeout)
}

// wireRequest builds the daemon's JSON body.
//
// Caller text travels as a JSON STRING FIELD. Nothing here builds a command
// line, so there is no construction in which a caller's words become a command.
func (p *Provider) wireRequest(req rt.GenerateRequest) map[string]any {
	messages := make([]map[string]string, 0, len(req.Messages)+1)
	if req.System != "" {
		messages = append(messages, map[string]string{
			"role": string(rt.RoleSystem), "content": req.System,
		})
	}
	for _, m := range req.Messages {
		messages = append(messages, map[string]string{
			"role": string(m.Role), "content": m.Content,
		})
	}

	options := map[string]any{}
	if req.MaxOutputTokens > 0 {
		options["num_predict"] = req.MaxOutputTokens
	} else {
		options["num_predict"] = p.cfg.MaxOutputTokens
	}
	// A nil Temperature means "the provider's default", which is genuinely
	// different from zero — zero is a valid, meaningful temperature, and
	// sending 0 for "unset" would silently make every generation deterministic.
	if req.Temperature != nil {
		options["temperature"] = *req.Temperature
	}

	model := string(req.Model)
	if model == "" {
		model = string(p.cfg.Model)
	}

	return map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   true,
		"options":  options,
	}
}

// Close releases the provider. Idempotent.
func (p *Provider) Close() error {
	if p.closed.Swap(true) {
		return nil
	}
	if t, ok := p.client.Transport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
	return nil
}

func (p *Provider) wrap(kind rt.ProviderErrorKind, status int, err error) error {
	return p.wrapModel(kind, status, p.cfg.Model, err)
}

func (p *Provider) wrapModel(kind rt.ProviderErrorKind, status int, model rt.ModelID, err error) error {
	return &rt.ProviderError{
		Provider: p.cfg.ID, Model: model, Kind: kind, StatusCode: status, Err: err,
	}
}

// kindForStatus maps a transport status onto the runtime's failure taxonomy.
//
// The taxonomy drives retry and the circuit breaker, so a wrong mapping is not
// cosmetic: classifying our own malformed request as a provider fault would open
// a breaker against a healthy daemon.
func kindForStatus(status int) rt.ProviderErrorKind {
	switch {
	case status == http.StatusNotFound:
		// Ollama answers 404 for a model it has not pulled. That is our
		// configuration being wrong, not the daemon being unhealthy.
		return rt.KindInvalidRequest
	case status == http.StatusTooManyRequests:
		return rt.KindRateLimited
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return rt.KindTimeout
	case status == http.StatusServiceUnavailable:
		return rt.KindOverloaded
	case status >= 500:
		return rt.KindTransport
	case status >= 400:
		return rt.KindInvalidRequest
	default:
		return rt.KindUnknown
	}
}

// readBounded reads at most n bytes, for error detail.
func readBounded(r io.Reader, n int64) string {
	b, _ := io.ReadAll(io.LimitReader(r, n))
	return strings.TrimSpace(string(b))
}

// ---------------------------------------------------------------------------
// The stream
// ---------------------------------------------------------------------------

// stream is one live generation over a newline-delimited JSON response.
type stream struct {
	provider rt.ProviderID
	model    rt.ModelID

	body    io.ReadCloser
	scanner *bufio.Scanner
	cancel  context.CancelFunc
	ctx     context.Context

	mu     sync.Mutex
	closed bool
	index  int
	done   bool

	// pending holds a usage chunk produced alongside the final text chunk, so
	// neither is dropped and neither is merged into the other.
	pending []rt.Chunk
}

// Compile-time proof that the frozen stream port is satisfied.
var _ rt.TokenStream = (*stream)(nil)

// wireChunk is one line of the daemon's response.
type wireChunk struct {
	Model   string `json:"model"`
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	Done       bool   `json:"done"`
	DoneReason string `json:"done_reason"`
	Error      string `json:"error"`

	PromptEvalCount int `json:"prompt_eval_count"`
	EvalCount       int `json:"eval_count"`
}

// Recv returns the next chunk, or io.EOF at normal end.
func (s *stream) Recv() (rt.Chunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return rt.Chunk{}, rt.ErrClosed
	}
	if len(s.pending) > 0 {
		c := s.pending[0]
		s.pending = s.pending[1:]
		return s.number(c), nil
	}
	if s.done {
		return rt.Chunk{}, io.EOF
	}

	for {
		if !s.scanner.Scan() {
			return rt.Chunk{}, s.scanErr()
		}

		line := bytes.TrimSpace(s.scanner.Bytes())
		if len(line) == 0 {
			continue // keep-alive blank lines are not data
		}

		var w wireChunk
		if err := json.Unmarshal(line, &w); err != nil {
			// A body that is not the wire format is a malformed response, NOT
			// an empty generation. Returning EOF here would present a broken
			// daemon as a model that had nothing to say.
			// THE OFFENDING BYTES ARE NOT QUOTED.
			//
			// A truncated stream produces unparseable JSON that still contains
			// the generated text — `{"message":{"content":"...` — and model
			// output is downstream of whatever the caller said. runtime.Chunk
			// documents it as SENSITIVE and never logged; an error IS logged.
			//
			// The length and the parser's own complaint say everything
			// actionable ("the daemon sent something that is not our wire
			// format") without putting a caller's words in a log file.
			return rt.Chunk{}, s.fail(rt.KindTransport, fmt.Errorf(
				"%w: %v (%d bytes, content not quoted: a partial response carries "+
					"model output)", ErrMalformedResponse, err, len(line)))
		}

		// The daemon reports mid-stream failures in-band, with HTTP 200 already
		// sent. Without this they would look like a stream that simply stopped.
		if w.Error != "" {
			return rt.Chunk{}, s.fail(rt.KindTransport,
				fmt.Errorf("ollama: %s", w.Error))
		}

		if w.Done {
			s.done = true
			// Usage first, then the terminator: the runtime accumulates usage
			// and treats ChunkDone as the end, so emitting them the other way
			// round loses the accounting.
			usage := rt.Chunk{Kind: rt.ChunkUsage, Usage: rt.Usage{
				InputTokens:  w.PromptEvalCount,
				OutputTokens: w.EvalCount,
			}}
			s.pending = append(s.pending, rt.Chunk{Kind: rt.ChunkDone})

			if w.Message.Content != "" {
				// A final line may carry text as well as the terminator.
				s.pending = append([]rt.Chunk{usage}, s.pending...)
				return s.number(rt.Chunk{Kind: rt.ChunkText, Text: w.Message.Content}), nil
			}
			return s.number(usage), nil
		}

		if w.Message.Content == "" {
			continue // a role-only preamble carries no content
		}
		return s.number(rt.Chunk{Kind: rt.ChunkText, Text: w.Message.Content}), nil
	}
}

// number assigns the chunk's ordinal. The dispatcher detects gaps with it, so a
// lossy transport cannot hide a dropped chunk.
func (s *stream) number(c rt.Chunk) rt.Chunk {
	c.Index = s.index
	s.index++
	return c
}

// scanErr turns the end of the scan into the right error.
func (s *stream) scanErr() error {
	err := s.scanner.Err()

	switch {
	case err == nil && s.done:
		return io.EOF

	case err == nil:
		// The body ended without a done marker. The generation was cut short —
		// distinct from a clean end, and the runtime records it as such.
		return s.fail(rt.KindTransport, errors.New(
			"ollama: the response ended without a completion marker; the "+
				"generation was truncated"))

	case errors.Is(err, bufio.ErrTooLong):
		return s.fail(rt.KindTransport, fmt.Errorf(
			"%w: a single line exceeded the configured bound", ErrMalformedResponse))

	case errors.Is(s.ctx.Err(), context.DeadlineExceeded):
		return s.fail(rt.KindTimeout, fmt.Errorf("ollama: %w", context.DeadlineExceeded))

	case errors.Is(s.ctx.Err(), context.Canceled):
		// Cancellation is the CALLER's decision, not a provider fault. Counting
		// it against the breaker would make every barge-in evidence that the
		// daemon is unhealthy.
		return context.Canceled

	default:
		return s.fail(rt.KindTransport, fmt.Errorf("ollama: reading the stream: %w", err))
	}
}

func (s *stream) fail(kind rt.ProviderErrorKind, err error) error {
	return &rt.ProviderError{Provider: s.provider, Model: s.model, Kind: kind, Err: err}
}

// Close releases the stream. Idempotent, and safe alongside Recv.
//
// Cancelling the context is what aborts the HTTP request in flight; closing the
// body alone would leave the daemon generating into a connection nobody reads.
// The abort budget in ADR-0011 §5.1 depends on this being immediate.
func (s *stream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	// Outside the lock: Recv may be parked in a read, and cancel is what
	// unblocks it. Holding the lock here would deadlock against it.
	s.cancel()
	return s.body.Close()
}
