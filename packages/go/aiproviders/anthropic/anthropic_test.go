package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/providerconformance"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// A local stand-in for the Messages API. It speaks the documented wire format
// over TLS on loopback; nothing here contacts Anthropic.

const testKey = "sk-test-not-a-real-key-0123456789"

type fake struct {
	srv *httptest.Server

	mu       sync.Mutex
	bodies   []map[string]any
	headers  []http.Header
	events   []string // SSE events for /v1/messages, each "event\x00json"
	status   int      // non-200 answers with this status
	errType  string
	hold     bool // send one delta, then wait until the request is cancelled
	probeErr int
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{status: http.StatusOK, events: textEvents("Hel", "lo")}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func textEvents(parts ...string) []string {
	ev := []string{
		`message_start` + "\x00" + `{"type":"message_start","message":{"usage":{"input_tokens":12,"output_tokens":1,"cache_read_input_tokens":3}}}`,
		`content_block_start` + "\x00" + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`ping` + "\x00" + `{"type":"ping"}`,
	}
	for _, p := range parts {
		b, _ := json.Marshal(map[string]any{ //nolint:errcheck // a map of strings always marshals
			"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": p},
		})
		ev = append(ev, "content_block_delta\x00"+string(b))
	}
	return append(ev,
		`content_block_stop`+"\x00"+`{"type":"content_block_stop","index":0}`,
		`message_delta`+"\x00"+`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		`message_stop`+"\x00"+`{"type":"message_stop"}`,
	)
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.headers = append(f.headers, r.Header.Clone())
	status, errType, events, hold, probeErr := f.status, f.errType, f.events, f.hold, f.probeErr
	f.mu.Unlock()

	if r.Header.Get("x-api-key") != testKey || r.Header.Get("anthropic-version") != DefaultAPIVersion {
		http.Error(w, `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`, http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/models/") {
		if probeErr != 0 {
			w.WriteHeader(probeErr)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"not_found_error","message":"model x"}}`) //nolint:errcheck // test server
			return
		}
		_, _ = io.WriteString(w, `{"id":"claude-sonnet-5","type":"model"}`) //nolint:errcheck // test server
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
		http.NotFound(w, r)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.bodies = append(f.bodies, body)
	f.mu.Unlock()

	if status != http.StatusOK {
		w.WriteHeader(status)
		// The message deliberately echoes "request content": it must never
		// reach an error the adapter returns.
		_, _ = fmt.Fprintf(w, `{"type":"error","error":{"type":%q,"message":"caller said: my PIN is 4321"}}`, errType) //nolint:errcheck // test server
		return
	}
	w.Header().Set("content-type", "text/event-stream")
	flusher, _ := w.(http.Flusher) //nolint:errcheck // httptest's writer flushes
	for i, e := range events {
		name, data, _ := strings.Cut(e, "\x00")
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data) //nolint:errcheck // test server
		flusher.Flush()
		if hold && i == 3 {
			<-r.Context().Done()
			return
		}
	}
}

func (f *fake) lastBody(t *testing.T) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		t.Fatal("no request reached the fake")
	}
	return f.bodies[len(f.bodies)-1]
}

func (f *fake) lastHeader() http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.headers[len(f.headers)-1]
}

func keyFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "anthropic-api-key")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func testConfig(t *testing.T, f *fake) Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.ID = "anthropic-test"
	cfg.Model = "claude-sonnet-5"
	cfg.APIKeyFile = keyFile(t, testKey+"\n")
	cfg.BaseURL = f.srv.URL
	cfg.HTTPClient = f.srv.Client()
	cfg.RequestTimeout = 5 * time.Second
	return cfg
}

func newProvider(t *testing.T, f *fake) *Provider {
	t.Helper()
	p, err := New(testConfig(t, f))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func request(model rt.ModelID) rt.GenerateRequest {
	return rt.GenerateRequest{
		Model:           model,
		System:          "You screen calls.",
		Messages:        []rt.Message{{Role: rt.RoleUser, Content: "hello"}},
		MaxOutputTokens: 64,
	}
}

func drainAll(t *testing.T, st rt.TokenStream) ([]rt.Chunk, error) {
	t.Helper()
	var out []rt.Chunk
	for {
		c, err := st.Recv()
		if err != nil {
			return out, err
		}
		out = append(out, c)
	}
}

func TestTheAdapterConformsToThePort(t *testing.T) {
	providerconformance.RunLLM(t, providerconformance.LLMSubject{
		New: func(t *testing.T) rt.Provider {
			return newProvider(t, newFake(t))
		},
		Request:     func() rt.GenerateRequest { return request("claude-sonnet-5") },
		ReturnBound: 5 * time.Second,
		Slack:       200 * time.Millisecond, // a TLS loopback round trip, not ignored cancellation
	})
}

func TestStream_TextThenUsageThenDone(t *testing.T) {
	f := newFake(t)
	st, err := newProvider(t, f).Generate(context.Background(), request("claude-sonnet-5"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close() //nolint:errcheck // test cleanup
	chunks, err := drainAll(t, st)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("stream ended with %v, want io.EOF", err)
	}
	if len(chunks) != 4 {
		t.Fatalf("chunks = %+v", chunks)
	}
	if chunks[0].Text != "Hel" || chunks[1].Text != "lo" || chunks[0].Kind != rt.ChunkText {
		t.Errorf("text chunks = %+v", chunks[:2])
	}
	u := chunks[2]
	if u.Kind != rt.ChunkUsage || u.Usage.InputTokens != 12 || u.Usage.OutputTokens != 5 || u.Usage.CachedInputTokens != 3 {
		t.Errorf("usage chunk = %+v", u)
	}
	if chunks[3].Kind != rt.ChunkDone {
		t.Errorf("last chunk = %+v, want Done", chunks[3])
	}
	for i, c := range chunks {
		if c.Index != i {
			t.Errorf("chunk %d has index %d", i, c.Index)
		}
	}
}

func TestRequest_FollowsADR0006PerTier(t *testing.T) {
	f := newFake(t)
	p := newProvider(t, f)

	for _, tc := range []struct {
		model    rt.ModelID
		thinking bool
		effort   string
		fast     bool
	}{
		{"claude-haiku-4-5", false, "", false},
		{"claude-sonnet-5", true, "low", false},
		{"claude-opus-5", true, "medium", true},
	} {
		req := request(tc.model)
		req.Thinking = tc.thinking
		req.Messages = []rt.Message{
			{Role: rt.RoleSystem, Content: "Subscriber rules."},
			{Role: rt.RoleUser, Content: "hello"},
		}
		st, err := p.Generate(context.Background(), req)
		if err != nil {
			t.Fatalf("%s: %v", tc.model, err)
		}
		_, _ = drainAll(t, st) //nolint:errcheck // the request shape is what is checked
		_ = st.Close()         //nolint:errcheck // test cleanup
		body := f.lastBody(t)

		if body["model"] != string(tc.model) || body["stream"] != true || body["max_tokens"] != float64(64) {
			t.Errorf("%s: body = %v", tc.model, body)
		}
		sys, _ := body["system"].([]any) //nolint:errcheck // checked below
		if len(sys) != 1 {
			t.Fatalf("%s: system = %v", tc.model, body["system"])
		}
		block := sys[0].(map[string]any) //nolint:errcheck,forcetypeassert // test
		if block["text"] != "You screen calls.\n\nSubscriber rules." {
			t.Errorf("%s: system text = %q", tc.model, block["text"])
		}
		if cc, _ := block["cache_control"].(map[string]any); cc["type"] != "ephemeral" { //nolint:errcheck // checked
			t.Errorf("%s: the stable prefix is not marked cacheable", tc.model)
		}
		msgs := body["messages"].([]any) //nolint:errcheck,forcetypeassert // test
		if len(msgs) != 1 {
			t.Errorf("%s: a system-role message leaked into messages: %v", tc.model, msgs)
		}
		thinking, hasThinking := body["thinking"].(map[string]any)
		if hasThinking != tc.thinking || (hasThinking && thinking["type"] != "adaptive") {
			t.Errorf("%s: thinking = %v", tc.model, body["thinking"])
		}
		oc, hasOC := body["output_config"].(map[string]any)
		if (tc.effort == "") == hasOC || (hasOC && oc["effort"] != tc.effort) {
			t.Errorf("%s: output_config = %v, want effort %q", tc.model, body["output_config"], tc.effort)
		}
		if (body["speed"] == "fast") != tc.fast {
			t.Errorf("%s: speed = %v", tc.model, body["speed"])
		}
		if (f.lastHeader().Get("anthropic-beta") == FastModeBeta) != tc.fast {
			t.Errorf("%s: anthropic-beta = %q", tc.model, f.lastHeader().Get("anthropic-beta"))
		}
	}
}

func TestRequest_InvalidRequestsAreRefusedBeforeSending(t *testing.T) {
	f := newFake(t)
	p := newProvider(t, f)
	temp := 0.2
	for name, req := range map[string]rt.GenerateRequest{
		"no messages":            {Model: "claude-sonnet-5", System: "x"},
		"assistant first":        {Model: "claude-sonnet-5", Messages: []rt.Message{{Role: rt.RoleAssistant, Content: "a"}}},
		"unknown role":           {Model: "claude-sonnet-5", Messages: []rt.Message{{Role: "tool", Content: "a"}}},
		"thinking + temperature": {Model: "claude-sonnet-5", Thinking: true, Temperature: &temp, Messages: []rt.Message{{Role: rt.RoleUser, Content: "a"}}},
	} {
		_, err := p.Generate(context.Background(), req)
		var pe *rt.ProviderError
		if !errors.As(err, &pe) || pe.Kind != rt.KindInvalidRequest {
			t.Errorf("%s: err = %v, want KindInvalidRequest", name, err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) != 0 {
		t.Errorf("%d invalid requests reached the API", len(f.bodies))
	}
}

func TestErrors_StatusesMapOntoTheRuntimeTaxonomyWithoutEchoingTheBody(t *testing.T) {
	for _, tc := range []struct {
		status  int
		errType string
		want    rt.ProviderErrorKind
	}{
		{http.StatusBadRequest, "invalid_request_error", rt.KindInvalidRequest},
		{http.StatusUnauthorized, "authentication_error", rt.KindAuth},
		{http.StatusForbidden, "permission_error", rt.KindAuth},
		{http.StatusNotFound, "not_found_error", rt.KindInvalidRequest},
		{http.StatusTooManyRequests, "rate_limit_error", rt.KindRateLimited},
		{http.StatusInternalServerError, "api_error", rt.KindTransport},
		{529, "overloaded_error", rt.KindOverloaded},
		{http.StatusServiceUnavailable, "", rt.KindOverloaded},
	} {
		f := newFake(t)
		f.status, f.errType = tc.status, tc.errType
		_, err := newProvider(t, f).Generate(context.Background(), request("claude-sonnet-5"))
		var pe *rt.ProviderError
		if !errors.As(err, &pe) || pe.Kind != tc.want || pe.StatusCode != tc.status {
			t.Errorf("status %d: err = %v, want kind %v", tc.status, err, tc.want)
			continue
		}
		for _, secret := range []string{"4321", "PIN", testKey} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("status %d: the error echoes %q: %v", tc.status, secret, err)
			}
		}
	}
}

func TestStream_ErrorEventsRefusalsAndTruncation(t *testing.T) {
	base := textEvents("partial")
	for _, tc := range []struct {
		name   string
		events []string
		kind   rt.ProviderErrorKind
		is     error
	}{
		{"in-band overload", append(base[:4:4], "error\x00"+`{"type":"error","error":{"type":"overloaded_error","message":"caller said x"}}`), rt.KindOverloaded, nil},
		{"refusal", append(base[:4:4],
			"message_delta\x00"+`{"type":"message_delta","delta":{"stop_reason":"refusal"},"usage":{"output_tokens":1}}`,
			"message_stop\x00"+`{"type":"message_stop"}`), rt.KindContentFiltered, ErrRefused},
		{"truncated", base[:4:4], rt.KindTransport, ErrTruncated},
		{"malformed", append(base[:4:4], "content_block_delta\x00{not json"), rt.KindTransport, ErrMalformedResponse},
	} {
		f := newFake(t)
		f.events = tc.events
		st, err := newProvider(t, f).Generate(context.Background(), request("claude-sonnet-5"))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		_, err = drainAll(t, st)
		_ = st.Close() //nolint:errcheck // test cleanup
		var pe *rt.ProviderError
		if !errors.As(err, &pe) || pe.Kind != tc.kind || (tc.is != nil && !errors.Is(err, tc.is)) {
			t.Errorf("%s: err = %v, want kind %v / %v", tc.name, err, tc.kind, tc.is)
		}
		if strings.Contains(fmt.Sprint(err), "caller said") || strings.Contains(fmt.Sprint(err), "not json") {
			t.Errorf("%s: the error quotes response content: %v", tc.name, err)
		}
	}
}

func TestStream_ThinkingIsDeliveredAsThinking(t *testing.T) {
	f := newFake(t)
	ev := textEvents("answer")
	f.events = append([]string{
		ev[0],
		"content_block_delta\x00" + `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`,
		"content_block_delta\x00" + `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
	}, ev[1:]...)
	req := request("claude-sonnet-5")
	req.Thinking = true
	st, err := newProvider(t, f).Generate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()             //nolint:errcheck // test cleanup
	chunks, _ := drainAll(t, st) //nolint:errcheck // the chunks are checked
	if len(chunks) < 2 || chunks[0].Kind != rt.ChunkThinking || chunks[0].Text != "hmm" || chunks[1].Kind != rt.ChunkText {
		t.Fatalf("chunks = %+v", chunks)
	}
}

func TestStream_CancellationAndCloseUnblockAParkedRecv(t *testing.T) {
	for _, viaClose := range []bool{false, true} {
		f := newFake(t)
		f.hold = true
		ctx, cancel := context.WithCancel(context.Background())
		st, err := newProvider(t, f).Generate(ctx, request("claude-sonnet-5"))
		if err != nil {
			t.Fatal(err)
		}
		if c, err := st.Recv(); err != nil || c.Text != "partial" && c.Text != "Hel" {
			t.Fatalf("first chunk = %+v, %v", c, err)
		}
		done := make(chan error, 1)
		go func() {
			var rerr error
			for rerr == nil {
				_, rerr = st.Recv()
			}
			done <- rerr
		}()
		time.Sleep(50 * time.Millisecond) // let Recv park on the held stream
		start := time.Now()
		if viaClose {
			_ = st.Close() //nolint:errcheck // the unblocking is what is checked
		} else {
			cancel()
		}
		select {
		case err := <-done:
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Errorf("close=%v: Recv took %v to return", viaClose, elapsed)
			}
			var pe *rt.ProviderError
			if !viaClose && (!errors.Is(err, context.Canceled) || errors.As(err, &pe)) {
				t.Errorf("cancel: Recv returned %v, want a bare context.Canceled: the caller's decision, "+
					"never a ProviderError that would count against the breaker", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("close=%v: a parked Recv never returned", viaClose)
		}
		cancel()
		_ = st.Close() //nolint:errcheck // idempotent
	}
}

func TestProbe_ChecksTheKeyAndModelWithoutGenerating(t *testing.T) {
	f := newFake(t)
	p := newProvider(t, f)
	if err := p.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	f.probeErr = http.StatusNotFound
	var pe *rt.ProviderError
	if err := p.Probe(context.Background()); !errors.As(err, &pe) || pe.Kind != rt.KindInvalidRequest {
		t.Errorf("Probe of a missing model = %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) != 0 {
		t.Error("Probe generated text")
	}
}

func TestEndpoint_TheKeyGoesOnlyToAnthropicOverTLS(t *testing.T) {
	for base, ok := range map[string]bool{
		"":                                       true,
		"https://api.anthropic.com":              true,
		"https://127.0.0.1:8443":                 true,
		"http://api.anthropic.com":               false,
		"https://api.anthropic.com.evil.example": false,
		"https://example.com":                    false,
		"https://user:pw@api.anthropic.com":      false,
	} {
		if got := endpointProblem(base) == ""; got != ok {
			t.Errorf("%q: allowed=%v, want %v", base, got, ok)
		}
	}
}

func TestKeyFile_IsRequiredAbsoluteAndSingle(t *testing.T) {
	f := newFake(t)
	for name, mutate := range map[string]func(*Config){
		"missing setting": func(c *Config) { c.APIKeyFile = "" },
		"relative":        func(c *Config) { c.APIKeyFile = "anthropic-api-key" },
		"absent file":     func(c *Config) { c.APIKeyFile = filepath.Join(t.TempDir(), "nope") },
		"empty":           func(c *Config) { c.APIKeyFile = keyFile(t, "  \n") },
		"two lines":       func(c *Config) { c.APIKeyFile = keyFile(t, testKey+"\nsecond") },
	} {
		cfg := testConfig(t, f)
		mutate(&cfg)
		_, err := New(cfg)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), testKey) {
			t.Errorf("%s: the error contains the key", name)
		}
	}
}

func TestRedirects_AreNeverFollowed(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	f := newFake(t)
	redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	cfg := testConfig(t, f)
	cfg.BaseURL = redirector.URL
	cfg.HTTPClient = redirector.Client()
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Generate(context.Background(), request("claude-sonnet-5")); err == nil {
		t.Error("Generate followed or ignored a redirect")
	}
	if err := p.Probe(context.Background()); err == nil {
		t.Error("Probe followed or ignored a redirect")
	}
	if hits.Load() != 0 {
		t.Errorf("the redirect target received %d requests (and the key)", hits.Load())
	}
}

func TestClose_RefusesFurtherWork(t *testing.T) {
	p := newProvider(t, newFake(t))
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Generate(context.Background(), request("claude-sonnet-5")); !errors.Is(err, rt.ErrClosed) {
		t.Errorf("Generate after Close = %v", err)
	}
	if err := p.Probe(context.Background()); !errors.Is(err, rt.ErrClosed) {
		t.Errorf("Probe after Close = %v", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("second Close = %v", err)
	}
}
