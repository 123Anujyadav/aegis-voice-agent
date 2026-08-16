package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// ---------------------------------------------------------------------------
// A real HTTP server, because the boundary IS HTTP
// ---------------------------------------------------------------------------
//
// These tests run against httptest — a real listener, real sockets, real
// streaming, real cancellation semantics — rather than a stubbed http.Client.
// The whole value of this adapter is what it does at the transport boundary:
// whether Generate returns before the first token, whether a cancelled context
// actually aborts a request in flight, whether a half-written response is
// reported or silently treated as a finished answer. A fake round-tripper
// would answer none of those honestly.
//
// It does NOT fabricate model output. Every token below is obviously synthetic
// test text. TestProvider_RealDaemonAvailability reports the real daemon's
// absence rather than papering over it.

const testModel = rt.ModelID("test-model-for-adapter-only")

// daemon is a scriptable stand-in for the Ollama HTTP API.
type daemon struct {
	server *httptest.Server

	// tags is what /api/tags reports as pulled.
	tags []string

	// chat is the handler for /api/chat.
	chat func(w http.ResponseWriter, r *http.Request)

	// lastBody records the request body the adapter sent.
	lastBody atomic.Value // []byte

	// cancelled is closed when a chat request's context ends before the
	// handler finished, which is how "cancellation reached the server" is
	// observed rather than assumed.
	cancelled chan struct{}
	once      sync.Once
}

func newDaemon(t *testing.T) *daemon {
	t.Helper()

	d := &daemon{
		tags:      []string{string(testModel)},
		cancelled: make(chan struct{}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		models := make([]map[string]string, 0, len(d.tags))
		for _, name := range d.tags {
			models = append(models, map[string]string{"name": name})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
	})
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		if body, err := io.ReadAll(r.Body); err == nil {
			d.lastBody.Store(body)
			// Recording the body consumes it. Handlers that inspect the request
			// need it back, and one that silently received an empty body would
			// make a crossed-streams test pass by comparing two blanks.
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		if d.chat != nil {
			d.chat(w, r)
		}
	})

	d.server = httptest.NewServer(mux)
	t.Cleanup(d.server.Close)
	return d
}

func (d *daemon) noteCancelled() { d.once.Do(func() { close(d.cancelled) }) }

// sentRequest returns the decoded body of the last /api/chat request.
func (d *daemon) sentRequest(t *testing.T) map[string]any {
	t.Helper()

	raw, _ := d.lastBody.Load().([]byte)
	if raw == nil {
		t.Fatal("the adapter sent no chat request")
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("the adapter sent malformed JSON: %v\n%s", err, raw)
	}
	return body
}

// writeLine writes one newline-delimited JSON object and flushes it.
//
// The flush is the point: without it the test server buffers the whole
// response and every "streaming" assertion below would pass against an adapter
// that did not stream at all.
func writeLine(w http.ResponseWriter, v any) {
	b, _ := json.Marshal(v)
	_, _ = w.Write(append(b, '\n'))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func textLine(content string) map[string]any {
	return map[string]any{
		"model":   string(testModel),
		"message": map[string]string{"role": "assistant", "content": content},
		"done":    false,
	}
}

func doneLine(in, out int) map[string]any {
	return map[string]any{
		"model":             string(testModel),
		"message":           map[string]string{"role": "assistant", "content": ""},
		"done":              true,
		"done_reason":       "stop",
		"prompt_eval_count": in,
		"eval_count":        out,
	}
}

// testConfig returns a configuration pointing at the stand-in daemon.
func testConfig(d *daemon) Config {
	cfg := DefaultConfig()
	cfg.ID = rt.ProviderID("ollama-dev")
	cfg.Model = testModel
	cfg.BaseURL = d.server.URL
	cfg.RequestTimeout = 10 * time.Second
	cfg.ProbeTimeout = 2 * time.Second
	cfg.HTTPClient = d.server.Client()
	return cfg
}

func newProvider(t *testing.T, d *daemon) *Provider {
	t.Helper()

	p, err := New(testConfig(d))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func testRequest() rt.GenerateRequest {
	return rt.GenerateRequest{
		RequestID: rt.NewRequestID(),
		SessionID: rt.NewSessionID(),
		Model:     testModel,
		System:    "You are a test fixture.",
		Messages: []rt.Message{
			{Role: rt.RoleUser, Content: "Say something short."},
		},
		MaxOutputTokens: 64,
	}
}

// drain collects every chunk until the stream ends.
func drain(t *testing.T, s rt.TokenStream) ([]rt.Chunk, error) {
	t.Helper()

	var chunks []rt.Chunk
	for {
		c, err := s.Recv()
		if errors.Is(err, io.EOF) {
			return chunks, nil
		}
		if err != nil {
			return chunks, err
		}
		chunks = append(chunks, c)
	}
}

func textOf(chunks []rt.Chunk) string {
	var sb strings.Builder
	for _, c := range chunks {
		if c.Kind == rt.ChunkText {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// The frozen port, and the development-only boundary
// ---------------------------------------------------------------------------

func TestProvider_SatisfiesTheFrozenPort(t *testing.T) {
	t.Parallel()

	// Compile-time: the package cannot BUILD if Phase 10A's port drifts.
	var _ rt.Provider = (*Provider)(nil)
	var _ rt.TokenStream = (*stream)(nil)

	d := newDaemon(t)
	var p any = newProvider(t, d)
	if _, ok := p.(rt.Provider); !ok {
		t.Fatal("Provider must be usable as a bare runtime.Provider")
	}
}

// TestProvider_IsNotAProductionTier guards the ADR-0006 boundary.
//
// ADR-0006 freezes a four-tier ladder, every tier on Claude, and explicitly
// REJECTED a self-hosted open-weight model as its Option 5. This adapter exists
// because Phase 11E needs a local development loop with no API key. Nothing here
// amends that ADR, and this test is what keeps the boundary from eroding by
// accident.
func TestProvider_IsNotAProductionTier(t *testing.T) {
	t.Parallel()

	d := newDaemon(t)
	caps := newProvider(t, d).Capabilities()

	// Invariant I3 binds tool calling to extended thinking. Declaring either
	// true would make this provider routable for a production tier's work.
	if caps.Thinking {
		t.Error("Thinking must be false: a thinking tier is an ADR-0006 production " +
			"tier, and this is a development provider")
	}
	if caps.ToolCalling {
		t.Error("ToolCalling must be false: Invariant I3 binds tool calling to " +
			"extended thinking, which this provider does not offer")
	}

	// No production model identifier may be written into this package. A
	// hardcoded Claude model here would be this adapter quietly claiming a tier.
	source, err := os.ReadFile("ollama.go")
	if err != nil {
		t.Fatalf("reading the adapter source: %v", err)
	}
	for _, forbidden := range []string{"claude-", "claude_", "anthropic"} {
		if strings.Contains(strings.ToLower(string(source)), forbidden) {
			t.Errorf("the adapter source names %q; a development provider must not "+
				"reference a production model or vendor", forbidden)
		}
	}
}

func TestProvider_HasNoDefaultModel(t *testing.T) {
	t.Parallel()

	// A default would make some particular open-weight model an implicit part
	// of the platform, and the first time a caller forgot to set it the system
	// would generate against a model nobody chose.
	if got := DefaultConfig().Model; got != "" {
		t.Errorf("DefaultConfig supplies the model %q; it must supply none", got)
	}

	cfg := DefaultConfig()
	cfg.ID = rt.ProviderID("ollama-dev")

	_, err := New(cfg)
	if err == nil {
		t.Fatal("New accepted a configuration with no model")
	}
	if !strings.Contains(err.Error(), "Model must be set") {
		t.Errorf("the error must say the model is required, got %v", err)
	}
}

func TestProvider_ProcessSupervisionDoesNotApply(t *testing.T) {
	t.Parallel()

	// The daemon is externally managed: this package neither starts nor stops
	// it. Importing the process supervisor would be the first step towards
	// this adapter spawning a daemon behind an operator's back, so the absence
	// of that import is asserted rather than assumed.
	source, err := os.ReadFile("ollama.go")
	if err != nil {
		t.Fatalf("reading the adapter source: %v", err)
	}
	for _, forbidden := range []string{"providers/process", "os/exec"} {
		if strings.Contains(string(source), forbidden) {
			t.Errorf("the adapter imports %q; Ollama's lifetime is the operator's, "+
				"and this package must not spawn or supervise it", forbidden)
		}
	}
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

func TestProvider_RefusesInvalidConfiguration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"no ID", func(c *Config) { c.ID = "" }, "ID must be set"},
		{"no model", func(c *Config) { c.Model = "" }, "Model must be set"},
		{"unparseable URL", func(c *Config) { c.BaseURL = "http://[::1" }, "BaseURL"},
		{"wrong scheme", func(c *Config) { c.BaseURL = "ftp://localhost:11434" }, "http or https"},
		{"no host", func(c *Config) { c.BaseURL = "http://" }, "BaseURL"},
		{"zero context", func(c *Config) { c.MaxContextTokens = 0 }, "MaxContextTokens"},
		{"zero output", func(c *Config) { c.MaxOutputTokens = 0 }, "MaxOutputTokens"},
		{"zero timeout", func(c *Config) { c.RequestTimeout = 0 }, "RequestTimeout"},
		{"zero probe timeout", func(c *Config) { c.ProbeTimeout = 0 }, "ProbeTimeout"},
		{"unbounded chunk", func(c *Config) { c.MaxChunkBytes = 0 }, "MaxChunkBytes"},
	}

	d := newDaemon(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := testConfig(d)
			tc.mutate(&cfg)

			_, err := New(cfg)
			if err == nil {
				t.Fatalf("New accepted an invalid configuration (%s)", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want mention of %q, got %v", tc.want, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Probe
// ---------------------------------------------------------------------------

func TestProbe_AcceptsAPulledModel(t *testing.T) {
	t.Parallel()

	d := newDaemon(t)
	if err := newProvider(t, d).Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
}

func TestProbe_AcceptsTheDefaultTagSuffix(t *testing.T) {
	t.Parallel()

	// Ollama reports "name:tag"; configuration usually names the bare model,
	// which is how everyone refers to these. Failing that match would report a
	// pulled model as missing.
	d := newDaemon(t)
	d.tags = []string{string(testModel) + ":latest"}

	if err := newProvider(t, d).Probe(context.Background()); err != nil {
		t.Fatalf("a model pulled as %q was reported missing: %v", d.tags[0], err)
	}
}

func TestProbe_ReportsAModelThatIsNotPulled(t *testing.T) {
	t.Parallel()

	// The daemon being up is not the same as the model being there. Without
	// this, the failure surfaces on the first generation — halfway through a
	// call rather than on the health path.
	d := newDaemon(t)
	d.tags = []string{"some-other-model:latest"}

	err := newProvider(t, d).Probe(context.Background())
	if !errors.Is(err, ErrModelMissing) {
		t.Fatalf("want ErrModelMissing, got %v", err)
	}
	if !strings.Contains(err.Error(), "ollama pull") {
		t.Errorf("the error must carry the remedy, got %v", err)
	}
	if !strings.Contains(err.Error(), "some-other-model") {
		t.Errorf("the error should list what IS available, got %v", err)
	}

	// Our configuration is wrong; the daemon is healthy. Classifying this as a
	// provider fault would open a circuit breaker against a working daemon.
	var pe *rt.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("want a *runtime.ProviderError, got %T", err)
	}
	if pe.Kind.CountsAgainstBreaker() {
		t.Errorf("a missing model is our misconfiguration, not provider ill health; "+
			"kind %s must not count against the breaker", pe.Kind)
	}
}

func TestProbe_ReportsAnUnreachableDaemon(t *testing.T) {
	t.Parallel()

	d := newDaemon(t)
	cfg := testConfig(d)
	d.server.Close() // nothing is listening now

	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = p.Probe(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
	if !strings.Contains(err.Error(), "ollama serve") {
		t.Errorf("§9 requires the remedy, got %v", err)
	}

	// The runtime routes on this: an unreachable provider must be visible as
	// unavailable to the layer that would otherwise keep sending it work.
	if !errors.Is(err, rt.ErrProviderUnavailable) {
		t.Errorf("an unreachable daemon must match runtime.ErrProviderUnavailable, "+
			"got %v", err)
	}
}

func TestProbe_RespectsItsDeadline(t *testing.T) {
	t.Parallel()

	// A probe runs on the readiness path. One that blocks turns a health check
	// into a timeout for everything waiting behind it.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer slow.Close()

	cfg := DefaultConfig()
	cfg.ID = rt.ProviderID("ollama-dev")
	cfg.Model = testModel
	cfg.BaseURL = slow.URL
	cfg.ProbeTimeout = 200 * time.Millisecond
	cfg.HTTPClient = slow.Client()

	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := time.Now()
	if err := p.Probe(context.Background()); err == nil {
		t.Fatal("a probe against a hanging daemon returned success")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("the probe took %s against a 200ms budget: it is not respecting "+
			"its deadline", elapsed)
	}
}

// ---------------------------------------------------------------------------
// Streaming
// ---------------------------------------------------------------------------

func TestGenerate_ReturnsBeforeTheFirstToken(t *testing.T) {
	t.Parallel()

	// The port is explicit: a Provider that blocks in Generate until the first
	// token has arrived defeats streaming and inflates time-to-first-token by
	// the whole model latency. This is that requirement, executable.
	const modelLatency = 400 * time.Millisecond

	d := newDaemon(t)
	d.chat = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush() // headers now, tokens later
		}
		time.Sleep(modelLatency)
		writeLine(w, textLine("first"))
		writeLine(w, doneLine(3, 1))
	}

	p := newProvider(t, d)

	start := time.Now()
	s, err := p.Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	defer func() { _ = s.Close() }()
	returned := time.Since(start)

	if returned >= modelLatency {
		t.Errorf("Generate blocked for %s, until the model produced a token. It "+
			"must return once the response headers arrive", returned)
	}

	first, err := s.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if first.Kind != rt.ChunkText || first.Text != "first" {
		t.Errorf("first chunk is %v %q", first.Kind, first.Text)
	}
	t.Logf("Generate returned in %s; the first token took %s (the stand-in "+
		"daemon's scripted delay, not a model measurement)", returned, time.Since(start))
}

func TestStream_DeliversTokensInOrderWithGaplessIndices(t *testing.T) {
	t.Parallel()

	tokens := []string{"Hello", ", ", "this", " is", " a", " test", "."}

	d := newDaemon(t)
	d.chat = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		for _, tok := range tokens {
			writeLine(w, textLine(tok))
		}
		writeLine(w, doneLine(12, len(tokens)))
	}

	s, err := newProvider(t, d).Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	chunks, err := drain(t, s)
	if err != nil {
		t.Fatalf("draining: %v", err)
	}

	if got, want := textOf(chunks), strings.Join(tokens, ""); got != want {
		t.Errorf("reassembled text is %q, want %q", got, want)
	}

	// The dispatcher detects dropped chunks with Index, so a gap must be
	// impossible to produce here.
	for i, c := range chunks {
		if c.Index != i {
			t.Fatalf("chunk %d carries index %d; indices must be gapless", i, c.Index)
		}
	}

	// A stream that ends without a done marker terminated abnormally, and the
	// runtime records that distinctly from a clean end.
	if last := chunks[len(chunks)-1]; last.Kind != rt.ChunkDone {
		t.Errorf("the stream ended with %v, want ChunkDone", last.Kind)
	}
}

func TestStream_ReportsUsageWithoutLosingTheFinalToken(t *testing.T) {
	t.Parallel()

	// Ollama may carry text on the same line as the completion marker.
	// Handling only one of the two loses either the last word of the response
	// or the whole token accounting.
	d := newDaemon(t)
	d.chat = func(w http.ResponseWriter, _ *http.Request) {
		writeLine(w, textLine("part one"))
		final := doneLine(21, 9)
		final["message"] = map[string]string{"role": "assistant", "content": " and the end"}
		writeLine(w, final)
	}

	s, err := newProvider(t, d).Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	chunks, err := drain(t, s)
	if err != nil {
		t.Fatalf("draining: %v", err)
	}

	if got, want := textOf(chunks), "part one and the end"; got != want {
		t.Errorf("text is %q, want %q: the final line's content was dropped", got, want)
	}

	var usage rt.Usage
	for _, c := range chunks {
		if c.Kind == rt.ChunkUsage {
			usage.Add(c.Usage)
		}
	}
	if usage.InputTokens != 21 || usage.OutputTokens != 9 {
		t.Errorf("usage is %+v, want 21 in and 9 out", usage)
	}
}

func TestStream_SkipsKeepAlivesAndRolePreambles(t *testing.T) {
	t.Parallel()

	d := newDaemon(t)
	d.chat = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("\n")) // keep-alive blank line
		writeLine(w, textLine(""))   // role-only preamble, no content
		writeLine(w, textLine("real content"))
		writeLine(w, doneLine(1, 1))
	}

	s, err := newProvider(t, d).Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	chunks, err := drain(t, s)
	if err != nil {
		t.Fatalf("draining: %v", err)
	}
	if got := textOf(chunks); got != "real content" {
		t.Errorf("text is %q; empty lines must not become empty chunks", got)
	}
	for _, c := range chunks {
		if c.Kind == rt.ChunkText && c.Text == "" {
			t.Error("an empty text chunk reached the caller")
		}
	}
}

// ---------------------------------------------------------------------------
// Cancellation: the 20 ms abort budget's dependency
// ---------------------------------------------------------------------------

func TestStream_CancellationAbortsTheRequestInFlight(t *testing.T) {
	t.Parallel()

	// ADR-0011 §5.1 fixes the abort budget at 20 ms, and the port states that a
	// Provider ignoring cancellation cannot meet it. What is asserted here is
	// the MECHANISM — the request in flight is actually aborted, promptly —
	// not a latency figure for a local model.
	d := newDaemon(t)
	d.chat = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		for i := 0; ; i++ {
			select {
			case <-r.Context().Done():
				d.noteCancelled()
				return
			default:
			}
			writeLine(w, textLine(fmt.Sprintf("token%d ", i)))
			time.Sleep(10 * time.Millisecond)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	s, err := newProvider(t, d).Generate(ctx, testRequest())
	if err != nil {
		cancel()
		t.Fatalf("Generate: %v", err)
	}

	if _, err := s.Recv(); err != nil { // generation genuinely under way
		cancel()
		t.Fatalf("first Recv: %v", err)
	}

	start := time.Now()
	cancel()

	// Recv must return promptly, and with the CALLER's cancellation rather than
	// a provider fault: counting a barge-in against the breaker would make
	// every interruption evidence that the daemon is unhealthy.
	var recvErr error
	for recvErr == nil {
		_, recvErr = s.Recv()
	}
	elapsed := time.Since(start)

	if !errors.Is(recvErr, context.Canceled) {
		t.Errorf("after cancellation Recv returned %v, want context.Canceled", recvErr)
	}
	var pe *rt.ProviderError
	if errors.As(recvErr, &pe) && pe.Kind.CountsAgainstBreaker() {
		t.Errorf("cancellation was classified as %s, which counts against the "+
			"breaker; the caller stopping is not the provider failing", pe.Kind)
	}

	select {
	case <-d.cancelled:
		// The server observed it: the request was genuinely aborted, not just
		// abandoned locally while the daemon kept generating.
	case <-time.After(5 * time.Second):
		t.Error("the daemon never saw the request end; cancellation did not reach " +
			"the transport, and the model kept generating into a dead connection")
	}

	t.Logf("Recv returned %s after cancel (orchestration overhead; the 20 ms "+
		"budget is a signal-path budget, and this figure includes HTTP teardown)",
		elapsed)
	_ = s.Close()
}

func TestStream_CloseUnblocksAParkedRecv(t *testing.T) {
	t.Parallel()

	// Close must be safe from a goroutine other than the one in Recv — the
	// dispatcher relies on it, and a barge-in is exactly that call.
	d := newDaemon(t)
	d.chat = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		writeLine(w, textLine("one token then silence"))
		<-r.Context().Done() // never completes on its own
		d.noteCancelled()
	}

	s, err := newProvider(t, d).Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := s.Recv(); err != nil {
		t.Fatalf("first Recv: %v", err)
	}

	parked := make(chan error, 1)
	go func() {
		_, err := s.Recv()
		parked <- err
	}()

	time.Sleep(100 * time.Millisecond) // let Recv park in the read
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case <-parked:
		// Unblocked, which is the whole point.
	case <-time.After(5 * time.Second):
		t.Fatal("Recv was still parked five seconds after Close: a barge-in would " +
			"hang the turn it was interrupting")
	}
}

func TestStream_CloseIsIdempotent(t *testing.T) {
	t.Parallel()

	d := newDaemon(t)
	d.chat = func(w http.ResponseWriter, _ *http.Request) {
		writeLine(w, textLine("hello"))
		writeLine(w, doneLine(1, 1))
	}

	s, err := newProvider(t, d).Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := s.Close(); err != nil {
			t.Errorf("Close #%d: %v", i+1, err)
		}
	}
	if _, err := s.Recv(); !errors.Is(err, rt.ErrClosed) {
		t.Errorf("Recv after Close returned %v, want runtime.ErrClosed", err)
	}
}

func TestStream_RequestDeadlineIsHonoured(t *testing.T) {
	t.Parallel()

	// The request's Deadline is an instant derived from a budget already partly
	// spent upstream. Replacing it with a fresh per-hop timeout is how latency
	// budgets silently inflate, so the deadline must win.
	d := newDaemon(t)
	d.chat = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		writeLine(w, textLine("starting"))
		<-r.Context().Done()
		d.noteCancelled()
	}

	req := testRequest()
	req.Deadline = time.Now().Add(300 * time.Millisecond)

	s, err := newProvider(t, d).Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	defer func() { _ = s.Close() }()

	start := time.Now()
	var recvErr error
	for recvErr == nil {
		_, recvErr = s.Recv()
	}

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the stream ran %s past a 300ms deadline", elapsed)
	}

	var pe *rt.ProviderError
	if !errors.As(recvErr, &pe) || pe.Kind != rt.KindTimeout {
		t.Errorf("an expired deadline must be reported as a timeout, got %v", recvErr)
	}
}

// ---------------------------------------------------------------------------
// Malformed and failing responses
// ---------------------------------------------------------------------------

func TestStream_MalformedJSONIsReportedNotTreatedAsAnEmptyAnswer(t *testing.T) {
	t.Parallel()

	// The dangerous failure is not the error — it is returning EOF, which
	// presents a broken daemon as a model that had nothing to say.
	d := newDaemon(t)
	d.chat = func(w http.ResponseWriter, _ *http.Request) {
		writeLine(w, textLine("a good line"))
		_, _ = w.Write([]byte("{this is not json}\n"))
	}

	s, err := newProvider(t, d).Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	defer func() { _ = s.Close() }()

	_, drainErr := drain(t, s)
	if drainErr == nil {
		t.Fatal("a malformed response ended the stream cleanly; a broken daemon " +
			"must not look like an empty answer")
	}
	if !errors.Is(drainErr, ErrMalformedResponse) {
		t.Errorf("want ErrMalformedResponse, got %v", drainErr)
	}
}

func TestStream_InBandErrorIsReported(t *testing.T) {
	t.Parallel()

	// The daemon reports mid-stream failures in the body, with HTTP 200 already
	// sent. Ignoring that field makes the failure look like a stream that
	// simply stopped.
	d := newDaemon(t)
	d.chat = func(w http.ResponseWriter, _ *http.Request) {
		writeLine(w, textLine("starting to answer"))
		writeLine(w, map[string]any{"error": "an unexpected error occurred during inference"})
	}

	s, err := newProvider(t, d).Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	defer func() { _ = s.Close() }()

	_, drainErr := drain(t, s)
	if drainErr == nil {
		t.Fatal("an in-band error ended the stream cleanly")
	}
	if !strings.Contains(drainErr.Error(), "during inference") {
		t.Errorf("the daemon's own message must reach the caller, got %v", drainErr)
	}
}

func TestStream_TruncationIsDistinctFromACleanEnd(t *testing.T) {
	t.Parallel()

	// A connection that drops mid-generation produces a partial answer. Ending
	// with io.EOF would let the caller speak half a sentence as though it were
	// the whole one.
	d := newDaemon(t)
	d.chat = func(w http.ResponseWriter, _ *http.Request) {
		writeLine(w, textLine("the first half of the answer"))
		// No completion marker: the handler just returns.
	}

	s, err := newProvider(t, d).Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	defer func() { _ = s.Close() }()

	chunks, drainErr := drain(t, s)
	if drainErr == nil {
		t.Fatal("a truncated generation ended cleanly; half an answer would be " +
			"spoken as though it were the whole one")
	}
	if !strings.Contains(drainErr.Error(), "truncated") {
		t.Errorf("the error should name truncation, got %v", drainErr)
	}
	if got := textOf(chunks); got != "the first half of the answer" {
		t.Errorf("the partial text should still be delivered, got %q", got)
	}
}

func TestStream_OneLineCannotGrowWithoutBound(t *testing.T) {
	t.Parallel()

	// A daemon that never emits a newline would otherwise grow a buffer for the
	// length of the generation.
	d := newDaemon(t)
	d.chat = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		chunk := strings.Repeat("x", 4096)
		for i := 0; i < 64; i++ { // 256 KiB, no newline anywhere
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
		}
	}

	cfg := testConfig(d)
	cfg.MaxChunkBytes = 8 << 10

	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = p.Close() }()

	s, err := p.Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	defer func() { _ = s.Close() }()

	if _, drainErr := drain(t, s); drainErr == nil {
		t.Fatal("an unbounded line was accepted")
	} else if !errors.Is(drainErr, ErrMalformedResponse) {
		t.Errorf("want ErrMalformedResponse, got %v", drainErr)
	}
}

func TestGenerate_TransportStatusesMapOntoTheRuntimeTaxonomy(t *testing.T) {
	t.Parallel()

	// The taxonomy drives retry and the circuit breaker. A wrong mapping is not
	// cosmetic: classifying our own bad request as a provider fault opens a
	// breaker against a healthy daemon.
	cases := []struct {
		status   int
		wantKind rt.ProviderErrorKind
		breaker  bool
	}{
		{http.StatusNotFound, rt.KindInvalidRequest, false},
		{http.StatusBadRequest, rt.KindInvalidRequest, false},
		{http.StatusTooManyRequests, rt.KindRateLimited, false},
		{http.StatusServiceUnavailable, rt.KindOverloaded, true},
		{http.StatusInternalServerError, rt.KindTransport, true},
	}

	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			t.Parallel()

			d := newDaemon(t)
			d.chat = func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "the daemon said no", tc.status)
			}

			_, err := newProvider(t, d).Generate(context.Background(), testRequest())
			if err == nil {
				t.Fatalf("HTTP %d was accepted as success", tc.status)
			}

			var pe *rt.ProviderError
			if !errors.As(err, &pe) {
				t.Fatalf("want a *runtime.ProviderError, got %T: %v", err, err)
			}
			if pe.Kind != tc.wantKind {
				t.Errorf("HTTP %d classified as %s, want %s", tc.status, pe.Kind, tc.wantKind)
			}
			if pe.StatusCode != tc.status {
				t.Errorf("StatusCode is %d, want %d", pe.StatusCode, tc.status)
			}
			if got := pe.Kind.CountsAgainstBreaker(); got != tc.breaker {
				t.Errorf("HTTP %d counts against the breaker = %v, want %v",
					tc.status, got, tc.breaker)
			}
		})
	}
}

func TestGenerate_ReportsAnUnreachableDaemon(t *testing.T) {
	t.Parallel()

	d := newDaemon(t)
	cfg := testConfig(d)
	d.server.Close()

	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = p.Close() }()

	_, err = p.Generate(context.Background(), testRequest())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
	if !errors.Is(err, rt.ErrProviderUnavailable) {
		t.Errorf("the runtime must see this as provider-unavailable, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// The request the adapter builds
// ---------------------------------------------------------------------------

func TestGenerate_BuildsTheRequestFaithfully(t *testing.T) {
	t.Parallel()

	d := newDaemon(t)
	d.chat = func(w http.ResponseWriter, _ *http.Request) {
		writeLine(w, doneLine(1, 0))
	}

	req := testRequest()
	temp := 0.0 // a MEANINGFUL zero, distinct from "unset"
	req.Temperature = &temp

	s, err := newProvider(t, d).Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := drain(t, s); err != nil {
		t.Fatalf("draining: %v", err)
	}

	body := d.sentRequest(t)

	if body["model"] != string(testModel) {
		t.Errorf("model is %v, want %v", body["model"], testModel)
	}
	if body["stream"] != true {
		t.Error("stream must be true; a batch request cannot be cancelled mid-answer")
	}

	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages is %v, want the system instruction plus one user turn", body["messages"])
	}
	first, _ := messages[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "You are a test fixture." {
		t.Errorf("the system instruction must lead the message list, got %v", first)
	}
	second, _ := messages[1].(map[string]any)
	if second["role"] != "user" || second["content"] != "Say something short." {
		t.Errorf("the user turn was not carried faithfully, got %v", second)
	}

	options, ok := body["options"].(map[string]any)
	if !ok {
		t.Fatalf("options is %v", body["options"])
	}
	if options["num_predict"] != float64(64) {
		t.Errorf("num_predict is %v, want the request's 64", options["num_predict"])
	}
	if options["temperature"] != float64(0) {
		t.Errorf("temperature is %v; an explicit zero must be sent, not dropped as "+
			"though it were unset", options["temperature"])
	}
}

func TestGenerate_OmitsTemperatureWhenUnset(t *testing.T) {
	t.Parallel()

	// A nil Temperature means "the provider's default". Sending 0 for it would
	// silently make every generation deterministic.
	d := newDaemon(t)
	d.chat = func(w http.ResponseWriter, _ *http.Request) {
		writeLine(w, doneLine(1, 0))
	}

	req := testRequest()
	req.Temperature = nil

	s, err := newProvider(t, d).Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := drain(t, s); err != nil {
		t.Fatalf("draining: %v", err)
	}

	options, _ := d.sentRequest(t)["options"].(map[string]any)
	if _, present := options["temperature"]; present {
		t.Errorf("temperature was sent as %v for an unset value", options["temperature"])
	}
}

func TestGenerate_RefusesAThinkingRequest(t *testing.T) {
	t.Parallel()

	// Invariant I3 boundary. This provider declares Thinking=false; serving a
	// thinking request anyway would silently drop the thinking, which is
	// precisely the invisible failure I3 exists to prevent.
	d := newDaemon(t)
	req := testRequest()
	req.Thinking = true

	_, err := newProvider(t, d).Generate(context.Background(), req)
	if err == nil {
		t.Fatal("a thinking request was accepted by a provider that cannot think")
	}

	var pe *rt.ProviderError
	if !errors.As(err, &pe) || pe.Kind != rt.KindInvalidRequest {
		t.Errorf("want KindInvalidRequest — the fault is ours, not the daemon's — "+
			"got %v", err)
	}
	if pe.Kind.CountsAgainstBreaker() {
		t.Error("our own bad request must not count against the daemon's breaker")
	}
}

func TestGenerate_RefusesAnEmptyRequest(t *testing.T) {
	t.Parallel()

	d := newDaemon(t)
	req := testRequest()
	req.Messages = nil
	req.System = ""

	if _, err := newProvider(t, d).Generate(context.Background(), req); err == nil {
		t.Fatal("a request with no messages and no system instruction was accepted")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle and isolation
// ---------------------------------------------------------------------------

func TestProvider_CloseIsIdempotentAndStopsFurtherWork(t *testing.T) {
	t.Parallel()

	d := newDaemon(t)
	p, err := New(testConfig(d))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("a second Close must be a no-op, got %v", err)
	}

	// The port requires it: after Close, Generate returns ErrClosed.
	if _, err := p.Generate(context.Background(), testRequest()); !errors.Is(err, rt.ErrClosed) {
		t.Errorf("Generate after Close returned %v, want runtime.ErrClosed", err)
	}
	if err := p.Probe(context.Background()); !errors.Is(err, rt.ErrClosed) {
		t.Errorf("Probe after Close returned %v, want runtime.ErrClosed", err)
	}
}

func TestProvider_ConcurrentGenerationsAreIndependent(t *testing.T) {
	t.Parallel()

	// A Provider is shared across goroutines by contract. Two sessions must not
	// see each other's tokens.
	d := newDaemon(t)
	d.chat = func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		// Echo the caller's own last message back, so a crossed stream is
		// visible rather than merely possible.
		mark := "unknown"
		if n := len(body.Messages); n > 0 {
			mark = body.Messages[n-1].Content
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		for i := 0; i < 5; i++ {
			writeLine(w, textLine(mark))
			time.Sleep(2 * time.Millisecond)
		}
		writeLine(w, doneLine(1, 5))
	}

	p := newProvider(t, d)

	const sessions = 6
	var wg sync.WaitGroup
	errs := make(chan error, sessions)

	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()

			req := testRequest()
			mine := fmt.Sprintf("session-%d-marker", n)
			req.Messages = []rt.Message{{Role: rt.RoleUser, Content: mine}}

			s, err := p.Generate(context.Background(), req)
			if err != nil {
				errs <- fmt.Errorf("session %d: Generate: %w", n, err)
				return
			}
			defer func() { _ = s.Close() }()

			chunks, err := drain(t, s)
			if err != nil {
				errs <- fmt.Errorf("session %d: %w", n, err)
				return
			}
			if got := textOf(chunks); got != strings.Repeat(mine, 5) {
				errs <- fmt.Errorf("session %d received %q; another session's tokens "+
					"reached it", n, got)
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// ---------------------------------------------------------------------------
// The real daemon
// ---------------------------------------------------------------------------

// TestProvider_RealDaemonAvailability reports on the actual local runtime.
//
// It asserts nothing about whether Ollama is installed: Phase 11E requires the
// development loop to work without it, and this adapter's contract is proven
// against a real HTTP server above. What it forbids is SILENCE about the
// difference between "measured against a model" and "measured against a stand-in".
func TestProvider_RealDaemonAvailability(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.ID = rt.ProviderID("ollama-dev")
	// Deliberately a placeholder: this test reports reachability, and naming a
	// model here would be this file choosing one.
	cfg.Model = rt.ModelID("<configured-by-operator>")
	cfg.ProbeTimeout = 2 * time.Second

	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = p.Close() }()

	err = p.Probe(context.Background())
	switch {
	case err == nil:
		t.Logf("a daemon at %s answered and reported the placeholder model as "+
			"pulled, which should not happen", DefaultBaseURL)

	case errors.Is(err, ErrModelMissing):
		// The daemon IS running: it answered /api/tags. Whether it has anything
		// to generate WITH is a separate question, and the two failure modes
		// need different remedies — so they are reported separately rather than
		// both being called "available".
		if installed := pulledModels(t); len(installed) == 0 {
			t.Logf("LOCAL PROVIDER RUNTIME PARTIALLY AVAILABLE\n"+
				"  provider:   ollama (runtime.Provider)\n"+
				"  daemon:     RUNNING and reachable at %s\n"+
				"  missing:    NO MODELS ARE PULLED — /api/tags returns an empty list\n"+
				"  needs:      'ollama pull <model>' for a model the operator chooses.\n"+
				"              This package supplies no default model on purpose, so\n"+
				"              there is nothing for it to fall back to.\n"+
				"  effect:     the LLM leg of the local end-to-end run cannot execute.\n"+
				"              No generation figure anywhere in this phase is a\n"+
				"              measurement of a model on this machine.\n"+
				"  probe said: %v", DefaultBaseURL, err)
			return
		}
		t.Logf("LOCAL PROVIDER RUNTIME AVAILABLE\n"+
			"  daemon:     reachable at %s\n"+
			"  pulled:     %s\n"+
			"  note:       set Config.Model to one of those. This package supplies\n"+
			"              no default model on purpose.\n"+
			"  probe said: %v", DefaultBaseURL, strings.Join(pulledModels(t), ", "), err)

	case errors.Is(err, ErrUnavailable):
		t.Logf("LOCAL PROVIDER RUNTIME NOT AVAILABLE\n"+
			"  provider:   ollama (runtime.Provider)\n"+
			"  missing:    no daemon answering at %s\n"+
			"  checked:    %s/api/tags\n"+
			"  needs:      the Ollama runtime (https://ollama.com/download), started\n"+
			"              with 'ollama serve', plus a pulled model chosen by the\n"+
			"              operator ('ollama pull <model>'). No model name is a\n"+
			"              default in this package.\n"+
			"  effect:     the LLM leg of the local end-to-end run reports\n"+
			"              unavailable. This adapter's contract is proven against a\n"+
			"              real HTTP server speaking Ollama's wire format; no model\n"+
			"              output is fabricated and no latency figure here is a\n"+
			"              measurement of any model.\n"+
			"  detail:     %v", DefaultBaseURL, DefaultBaseURL, err)

	default:
		t.Logf("probing %s produced an unclassified result: %v", DefaultBaseURL, err)
	}
}

// pulledModels asks the real daemon what it has, for reporting only.
//
// Returns nil when there is no daemon or it cannot be read: this is diagnostic
// output, and a failure to gather it must not fail a test that is deliberately
// asserting nothing about the local machine.
func pulledModels(t *testing.T) []string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DefaultBaseURL+"/api/tags", nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()

	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil
	}

	names := make([]string, 0, len(tags.Models))
	for _, m := range tags.Models {
		names = append(names, m.Name)
	}
	return names
}
