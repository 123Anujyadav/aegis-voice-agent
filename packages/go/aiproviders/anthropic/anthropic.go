// Package anthropic adapts runtime.Provider to Anthropic's Messages API.
//
// ADR-0006 (Accepted) puts the platform's four LLM tiers on Claude. This is the
// vendor side of the runtime's provider seam: the kernel never imports it.
//
// # Not activated
//
// Building a Provider needs an API key in a mounted file. No service holds one,
// and supplying one is the LLM spending category's approval, together with the
// DPDP review of sending caller transcripts outside India (E9, L1; ADR-0012
// names no LLM exception). Until then the adapter runs only against the local
// fake server in its tests.
//
// # Security properties
//
//   - The key is read from a file, never the environment, and is sent only to
//     api.anthropic.com over TLS, or to a loopback test server. Redirects are
//     never followed, so the key cannot be carried to another host.
//   - Prompts and output are SENSITIVE (runtime.Message). Nothing here logs
//     them, and an error never quotes a response body: only the vendor's error
//     type and the HTTP status.
//   - Every request is bounded: by the request's deadline or RequestTimeout,
//     and each streamed event by MaxEventBytes.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// DefaultBaseURL is Anthropic's API.
const DefaultBaseURL = "https://api.anthropic.com"

// DefaultAPIVersion is the anthropic-version header the wire format below
// follows.
const DefaultAPIVersion = "2023-06-01"

// Errors a caller can test with errors.Is, wrapped in a runtime.ProviderError.
var (
	// ErrMalformedResponse is a response that does not follow the wire format.
	ErrMalformedResponse = errors.New("anthropic: malformed response")
	// ErrTruncated is a stream that ended without message_stop.
	ErrTruncated = errors.New("anthropic: stream ended without message_stop")
	// ErrRefused is a generation the model declined (stop_reason "refusal").
	ErrRefused = errors.New("anthropic: the model declined to answer")
)

// Config describes one Anthropic provider.
type Config struct {
	// ID is the authored provider identifier. It becomes a metric label.
	ID rt.ProviderID
	// Model is the default model, used when a request names none. Required:
	// which Claude model serves which tier is ADR-0006's decision, recorded in
	// configuration, not guessed here.
	Model rt.ModelID
	// APIKeyFile is the absolute path of a file holding the API key, mounted
	// from a Secret. Never an environment variable.
	APIKeyFile string
	// BaseURL is the API's address. Empty means DefaultBaseURL. Only
	// https://api.anthropic.com or a loopback host (for tests) is accepted.
	BaseURL string
	// APIVersion is the anthropic-version header. Empty means DefaultAPIVersion.
	APIVersion string
	// MaxContextTokens and MaxOutputTokens are declared to the runtime.
	MaxContextTokens int
	MaxOutputTokens  int
	// Models holds the per-model wire parameters ADR-0006 fixes (effort,
	// speed). A model absent from it is sent with none. ADR0006Models returns
	// the Accepted ADR's table.
	Models map[rt.ModelID]ModelParams
	// CacheSystemPrompt marks the system prompt as a cacheable prefix
	// (ADR-0006: prompt caching on the stable prefix).
	CacheSystemPrompt bool
	// RequestTimeout bounds a generation whose request carries no deadline.
	RequestTimeout time.Duration
	// ProbeTimeout bounds a readiness probe.
	ProbeTimeout time.Duration
	// MaxEventBytes bounds one server-sent event line.
	MaxEventBytes int
	// HTTPClient overrides the client; used by tests. Its transport is the
	// caller's responsibility; redirects are refused either way.
	HTTPClient *http.Client
}

// ModelParams are the per-model request parameters of ADR-0006 §5.
type ModelParams struct {
	// Effort is output_config.effort: "low", "medium" or "high"; empty sends
	// none (Haiku does not support it).
	Effort string
	// Fast requests fast mode (speed "fast"), with the beta header it needs.
	Fast bool
}

// FastModeBeta is the beta header ADR-0006 names for Tier 3's fast mode.
const FastModeBeta = "fast-mode-2026-02-01"

// ADR0006Models returns the Accepted ADR-0006 tier table: Tier 1 Haiku with no
// effort, Tier 2 Sonnet at low effort, Tier 3 Opus at medium effort in fast
// mode. Thinking is adaptive whenever a request asks for it (Invariant I3 makes
// the runtime ask on tool-calling tiers).
func ADR0006Models() map[rt.ModelID]ModelParams {
	return map[rt.ModelID]ModelParams{
		"claude-haiku-4-5": {},
		"claude-sonnet-5":  {Effort: "low"},
		"claude-opus-5":    {Effort: "medium", Fast: true},
	}
}

// DefaultConfig returns transport settings, leaving the decisions (ID, Model,
// APIKeyFile) empty.
func DefaultConfig() Config {
	return Config{
		BaseURL:           DefaultBaseURL,
		APIVersion:        DefaultAPIVersion,
		MaxContextTokens:  200_000,
		MaxOutputTokens:   4096,
		Models:            ADR0006Models(),
		CacheSystemPrompt: true,
		RequestTimeout:    60 * time.Second,
		ProbeTimeout:      3 * time.Second,
		MaxEventBytes:     1 << 20,
	}
}

func (c Config) validate() error {
	var problems []string
	if c.ID == "" {
		problems = append(problems, "ID must be set; it is a metric label")
	}
	if c.Model == "" {
		problems = append(problems, "Model must be set: the model per tier is ADR-0006's decision")
	}
	switch {
	case c.APIKeyFile == "":
		problems = append(problems, "APIKeyFile must be set (a mounted Secret file, never an environment variable)")
	case !filepath.IsAbs(c.APIKeyFile):
		problems = append(problems, "APIKeyFile must be an absolute path")
	}
	if problem := endpointProblem(c.BaseURL); problem != "" {
		problems = append(problems, problem)
	}
	if c.MaxContextTokens <= 0 || c.MaxOutputTokens <= 0 {
		problems = append(problems, "MaxContextTokens and MaxOutputTokens must be positive")
	}
	for m, mp := range c.Models {
		switch mp.Effort {
		case "", "low", "medium", "high":
		default:
			problems = append(problems, fmt.Sprintf("model %q: Effort %q is not low, medium or high", m, mp.Effort))
		}
	}
	if c.RequestTimeout <= 0 || c.ProbeTimeout <= 0 {
		problems = append(problems, "RequestTimeout and ProbeTimeout must be positive")
	}
	if c.MaxEventBytes <= 0 {
		problems = append(problems, "MaxEventBytes must be positive")
	}
	if len(problems) > 0 {
		return fmt.Errorf("anthropic: invalid configuration: %s", strings.Join(problems, "; "))
	}
	return nil
}

// endpointProblem enforces where the key may be sent.
func endpointProblem(base string) string {
	if base == "" {
		return ""
	}
	u, err := url.Parse(base)
	switch {
	case err != nil:
		return "BaseURL is not a URL"
	case u.User != nil:
		return "BaseURL must not carry credentials"
	case u.Scheme != "https":
		return "BaseURL must be https: the API key travels in a header"
	case u.Hostname() == "api.anthropic.com":
		return ""
	case u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1":
		return "" // a local test server
	}
	return fmt.Sprintf("BaseURL host %q is not allowed: the key is sent only to api.anthropic.com", u.Hostname())
}

// Provider implements runtime.Provider over the Messages API.
type Provider struct {
	cfg     Config
	base    string
	version string
	key     string
	client  *http.Client
	closed  atomic.Bool
}

var _ rt.Provider = (*Provider)(nil)

// New builds a provider. It reads the key file but contacts nothing.
func New(cfg Config) (*Provider, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(cfg.APIKeyFile)
	if err != nil {
		// The path is configuration, not a secret; the content never appears.
		return nil, fmt.Errorf("anthropic: reading APIKeyFile %s: %w", cfg.APIKeyFile, err)
	}
	key := strings.TrimSpace(string(raw))
	if key == "" || strings.ContainsAny(key, "\r\n\t ") {
		return nil, errors.New("anthropic: APIKeyFile does not hold a single key")
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	version := cfg.APIVersion
	if version == "" {
		version = DefaultAPIVersion
	}
	return &Provider{
		cfg: cfg, base: strings.TrimRight(base, "/"), version: version,
		key: key, client: guardedClient(cfg.HTTPClient),
	}, nil
}

// guardedClient never follows a redirect: the key would follow it.
func guardedClient(supplied *http.Client) *http.Client {
	var c http.Client
	if supplied != nil {
		c = *supplied
	} else {
		c = http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: 30 * time.Second,
			MaxIdleConnsPerHost:   8,
			ForceAttemptHTTP2:     true,
		}}
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("anthropic: refusing to follow a redirect")
	}
	return &c
}

// ID returns the authored identifier.
func (p *Provider) ID() rt.ProviderID { return p.cfg.ID }

// Capabilities declares what this adapter supports. Tool calling is not
// implemented: the orchestration layer that would use it does not exist yet.
func (p *Provider) Capabilities() rt.Capabilities {
	return rt.Capabilities{
		Streaming:        true,
		Thinking:         true,
		ToolCalling:      false,
		MaxContextTokens: p.cfg.MaxContextTokens,
		MaxOutputTokens:  p.cfg.MaxOutputTokens,
	}
}

func (p *Provider) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	var r io.Reader = http.NoBody
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", p.key)
	req.Header.Set("anthropic-version", p.version)
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	return req, nil
}

// Probe checks the key and the default model with GET /v1/models/{model},
// which generates nothing.
func (p *Provider) Probe(ctx context.Context) error {
	if p.closed.Load() {
		return rt.ErrClosed
	}
	ctx, cancel := context.WithTimeout(ctx, p.cfg.ProbeTimeout)
	defer cancel()
	req, err := p.newRequest(ctx, http.MethodGet, "/v1/models/"+url.PathEscape(string(p.cfg.Model)), nil)
	if err != nil {
		return p.wrap(rt.KindInvalidRequest, 0, p.cfg.Model, err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return p.transportError(ctx, p.cfg.Model, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)) //nolint:errcheck // connection reuse is best-effort
		_ = resp.Body.Close()                                         //nolint:errcheck // the probe's result is already decided
	}()
	if resp.StatusCode != http.StatusOK {
		return p.statusError(resp, p.cfg.Model)
	}
	return nil
}

// Generate starts a streamed generation. It returns once the response headers
// arrive; tokens are read in Recv.
func (p *Provider) Generate(ctx context.Context, req rt.GenerateRequest) (rt.TokenStream, error) {
	if p.closed.Load() {
		return nil, rt.ErrClosed
	}
	model := req.Model
	if model == "" {
		model = p.cfg.Model
	}
	body, fast, err := p.wireRequest(req, model)
	if err != nil {
		return nil, p.wrap(rt.KindInvalidRequest, 0, model, err)
	}
	streamCtx, cancel := p.streamContext(ctx, req)
	httpReq, err := p.newRequest(streamCtx, http.MethodPost, "/v1/messages", body)
	if err != nil {
		cancel()
		return nil, p.wrap(rt.KindInvalidRequest, 0, model, err)
	}
	httpReq.Header.Set("accept", "text/event-stream")
	if fast {
		httpReq.Header.Set("anthropic-beta", FastModeBeta)
	}
	resp, err := p.client.Do(httpReq) //nolint:bodyclose // on success the returned stream owns the body and closes it
	if err != nil {
		cancel()
		return nil, p.transportError(streamCtx, model, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer cancel()
		defer drain(resp.Body)
		return nil, p.statusError(resp, model)
	}
	return newStream(streamCtx, p.cfg.ID, model, resp.Body, cancel, p.cfg.MaxEventBytes), nil
}

func (p *Provider) streamContext(ctx context.Context, req rt.GenerateRequest) (context.Context, context.CancelFunc) {
	if !req.Deadline.IsZero() {
		return context.WithDeadline(ctx, req.Deadline)
	}
	return context.WithTimeout(ctx, p.cfg.RequestTimeout)
}

type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type wireThinking struct {
	Type string `json:"type"`
}

type wireOutputConfig struct {
	Effort string `json:"effort"`
}

type wireCacheControl struct {
	Type string `json:"type"`
}

type wireSystemBlock struct {
	Type         string            `json:"type"`
	Text         string            `json:"text"`
	CacheControl *wireCacheControl `json:"cache_control,omitempty"`
}

type wireRequest struct {
	Model        string            `json:"model"`
	MaxTokens    int               `json:"max_tokens"`
	System       []wireSystemBlock `json:"system,omitempty"`
	Messages     []wireMessage     `json:"messages"`
	Stream       bool              `json:"stream"`
	Temperature  *float64          `json:"temperature,omitempty"`
	Thinking     *wireThinking     `json:"thinking,omitempty"`
	OutputConfig *wireOutputConfig `json:"output_config,omitempty"`
	Speed        string            `json:"speed,omitempty"`
}

// wireRequest maps the runtime request onto the Messages API. System-role
// messages join the system field: the API's message list is user and
// assistant only.
func (p *Provider) wireRequest(req rt.GenerateRequest, model rt.ModelID) ([]byte, bool, error) {
	maxTokens := req.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = p.cfg.MaxOutputTokens
	}
	system := []string{}
	if req.System != "" {
		system = append(system, req.System)
	}
	messages := make([]wireMessage, 0, len(req.Messages))
	for i, m := range req.Messages {
		switch m.Role {
		case rt.RoleSystem:
			system = append(system, m.Content)
		case rt.RoleUser, rt.RoleAssistant:
			messages = append(messages, wireMessage{Role: string(m.Role), Content: m.Content})
		default:
			return nil, false, fmt.Errorf("message %d has unknown role %q", i, m.Role)
		}
	}
	if len(messages) == 0 {
		return nil, false, errors.New("no user or assistant message")
	}
	if messages[0].Role != string(rt.RoleUser) {
		return nil, false, errors.New("the first message must be the user's")
	}
	w := wireRequest{
		Model: string(model), MaxTokens: maxTokens,
		Messages: messages, Stream: true, Temperature: req.Temperature,
	}
	if len(system) > 0 {
		block := wireSystemBlock{Type: "text", Text: strings.Join(system, "\n\n")}
		if p.cfg.CacheSystemPrompt {
			block.CacheControl = &wireCacheControl{Type: "ephemeral"}
		}
		w.System = []wireSystemBlock{block}
	}
	if req.Thinking {
		if req.Temperature != nil {
			return nil, false, errors.New("thinking is requested, so no temperature is sent")
		}
		w.Thinking = &wireThinking{Type: "adaptive"}
	}
	mp := p.cfg.Models[model]
	if mp.Effort != "" {
		w.OutputConfig = &wireOutputConfig{Effort: mp.Effort}
	}
	if mp.Fast {
		w.Speed = "fast"
	}
	body, err := json.Marshal(w)
	return body, mp.Fast, err
}

// Close releases idle connections. After it, Generate and Probe return
// ErrClosed.
func (p *Provider) Close() error {
	if p.closed.Swap(true) {
		return nil
	}
	if t, ok := p.client.Transport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
	return nil
}

func (p *Provider) wrap(kind rt.ProviderErrorKind, status int, model rt.ModelID, err error) error {
	return &rt.ProviderError{Provider: p.cfg.ID, Model: model, Kind: kind, StatusCode: status, Err: err}
}

// transportError classifies a failed exchange. Cancellation is the caller's
// decision, not a provider fault, so it is returned unwrapped.
func (p *Provider) transportError(ctx context.Context, model rt.ModelID, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return context.Canceled
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return p.wrap(rt.KindTimeout, 0, model, context.DeadlineExceeded)
	}
	return p.wrap(rt.KindTransport, 0, model, fmt.Errorf("anthropic: request failed: %w", err))
}

// statusError maps an error response. Only the vendor's error type is kept:
// a message could echo request content.
func (p *Provider) statusError(resp *http.Response, model rt.ModelID) error {
	var body struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&body) //nolint:errcheck // the status alone classifies; the type is a refinement
	kind := kindForStatus(resp.StatusCode)
	if k, ok := kindForType(body.Error.Type); ok {
		kind = k
	}
	return p.wrap(kind, resp.StatusCode, model,
		fmt.Errorf("anthropic: %s (%s)", resp.Status, typeOrUnknown(body.Error.Type)))
}

func kindForStatus(status int) rt.ProviderErrorKind {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return rt.KindAuth
	case status == http.StatusTooManyRequests:
		return rt.KindRateLimited
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return rt.KindTimeout
	case status == http.StatusServiceUnavailable || status == 529:
		return rt.KindOverloaded
	case status >= 500:
		return rt.KindTransport
	case status >= 400:
		return rt.KindInvalidRequest
	}
	return rt.KindUnknown
}

func kindForType(t string) (rt.ProviderErrorKind, bool) {
	switch t {
	case "invalid_request_error", "not_found_error", "request_too_large":
		return rt.KindInvalidRequest, true
	case "authentication_error", "permission_error":
		return rt.KindAuth, true
	case "rate_limit_error":
		return rt.KindRateLimited, true
	case "overloaded_error":
		return rt.KindOverloaded, true
	case "api_error":
		return rt.KindTransport, true
	case "timeout_error":
		return rt.KindTimeout, true
	}
	return rt.KindUnknown, false
}

func typeOrUnknown(t string) string {
	if t == "" {
		return "no error type"
	}
	// The type is a short vendor token; anything else is not echoed.
	for _, r := range t {
		if (r < 'a' || r > 'z') && r != '_' {
			return "unrecognised error type"
		}
	}
	return t
}

func drain(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 64<<10)) //nolint:errcheck // connection reuse is best-effort
	_ = body.Close()                                         //nolint:errcheck // as above
}
