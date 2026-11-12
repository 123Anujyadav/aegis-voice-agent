// Package deepgram adapts speech.STTProvider to Deepgram's streaming
// speech-to-text API (wss://api.deepgram.com/v1/listen).
//
// ADR-0005 (Accepted) makes Deepgram Nova-3 the secondary recogniser and the
// failover target; Google STT v2 in asia-south1 stays the primary. Deepgram
// streams only over WebSocket, so ADR-0034 (approved by the owner 2026-11-12)
// admits github.com/coder/websocket, contained to this module.
//
// # Not activated
//
// Building a Provider needs an API key in a mounted file. No service holds
// one. Deepgram processes outside India, so every use is a cross-border
// transfer of caller audio and is consent-gated (ADR-0012). Activation needs
// the speech spending category (OD-17) and the DPDP review (E9).
//
// # What the adapter asks for
//
// Mono 16-bit PCM ("linear16") at the stream's rate, interim results on, and
// vendor endpointing OFF: ADR-0005 says endpointing is ours (media-relay's VAD
// and session-orchestrator decide when a turn ended), so the adapter reports
// is_final and ignores Deepgram's speech_final and UtteranceEnd.
//
// # Security properties
//
//   - The key is read from an absolute-path mounted file and sent only in the
//     upgrade request's Authorization header, only to api.deepgram.com over
//     TLS (wss), or to a loopback test server. Redirects are never followed.
//   - Audio and transcripts are personal data (ADR-0005 §10). Neither is ever
//     logged, and an error never quotes a provider message, close reason or
//     response body.
//   - Write never blocks: audio waits in a bounded queue, and a full queue is
//     speech.ErrBackpressure. Messages from the provider are bounded by
//     MaxMessageBytes.
//   - A failure that ends the stream is recorded (Err) and returned from
//     Close, so a crashed stream never looks like a silent caller.
package deepgram

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
)

// DefaultBaseURL is Deepgram's streaming API.
const DefaultBaseURL = "wss://api.deepgram.com"

// DefaultModel is ADR-0005's secondary: Nova-3.
const DefaultModel = "nova-3"

// apiHost is the only non-loopback host the key is ever sent to.
const apiHost = "api.deepgram.com"

// listenPath is the streaming recognition endpoint.
const listenPath = "/v1/listen"

// Config describes one Deepgram provider.
type Config struct {
	// ID is the authored provider identifier.
	ID speech.ProviderID
	// APIKeyFile is the absolute path of a file holding the key, mounted from
	// a Secret. Never an environment variable.
	APIKeyFile string
	// BaseURL is the API's address. Empty means DefaultBaseURL. Only
	// wss://api.deepgram.com, or a loopback host for tests (wss, or ws), is
	// accepted.
	BaseURL string
	// Model is the recognition model. Empty means DefaultModel.
	Model string
	// Languages the provider declares, as BCP-47 tags passed to Deepgram
	// unchanged. Required: which languages are routed to a cross-border
	// recogniser is OD-17's and ADR-0012's decision, not this adapter's.
	Languages []speech.Language
	// MaxPendingFrames bounds audio frames queued but not yet sent.
	MaxPendingFrames int
	// OpenTimeout bounds the connection and upgrade.
	OpenTimeout time.Duration
	// ResultTimeout bounds the silence after CloseSend: if no message arrives
	// for this long once the end of audio was sent, the stream fails.
	ResultTimeout time.Duration
	// MaxMessageBytes bounds one message from the provider.
	MaxMessageBytes int64
	// HTTPClient overrides the client used for the upgrade; used by tests.
	// Redirects are refused either way.
	HTTPClient *http.Client
}

// The transport defaults DefaultConfig and New apply.
const (
	defaultMaxPendingFrames = 100 // two seconds of 20 ms frames
	defaultOpenTimeout      = 10 * time.Second
	defaultResultTimeout    = 10 * time.Second
	defaultMaxMessageBytes  = 128 << 10
)

// DefaultConfig returns transport settings, leaving the decisions (ID,
// APIKeyFile, Languages) empty.
func DefaultConfig() Config {
	return Config{
		BaseURL:          DefaultBaseURL,
		Model:            DefaultModel,
		MaxPendingFrames: defaultMaxPendingFrames,
		OpenTimeout:      defaultOpenTimeout,
		ResultTimeout:    defaultResultTimeout,
		MaxMessageBytes:  defaultMaxMessageBytes,
	}
}

// withDefaults fills unset (zero) fields. Negative values are left for
// validate to refuse.
func (c Config) withDefaults() Config {
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	if c.Model == "" {
		c.Model = DefaultModel
	}
	if c.MaxPendingFrames == 0 {
		c.MaxPendingFrames = defaultMaxPendingFrames
	}
	if c.OpenTimeout == 0 {
		c.OpenTimeout = defaultOpenTimeout
	}
	if c.ResultTimeout == 0 {
		c.ResultTimeout = defaultResultTimeout
	}
	if c.MaxMessageBytes == 0 {
		c.MaxMessageBytes = defaultMaxMessageBytes
	}
	return c
}

func (c Config) validate() error {
	var problems []string
	if !c.ID.Valid() {
		problems = append(problems, "ID must be a valid provider identifier")
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
	if !validToken(c.Model, 64, true) {
		problems = append(problems, "Model must be 1-64 letters, digits, '-', '_' or '.'")
	}
	if len(c.Languages) == 0 {
		problems = append(problems,
			"Languages must be set: routing caller audio to a cross-border recogniser is OD-17's and ADR-0012's decision")
	}
	for _, l := range c.Languages {
		if !validToken(string(l), 35, false) {
			problems = append(problems, "every language must be a BCP-47 tag of letters, digits and '-'")
			break
		}
	}
	if c.MaxPendingFrames <= 0 || c.OpenTimeout <= 0 || c.ResultTimeout <= 0 || c.MaxMessageBytes <= 0 {
		problems = append(problems, "MaxPendingFrames, OpenTimeout, ResultTimeout and MaxMessageBytes must be positive")
	}
	if len(problems) > 0 {
		return fmt.Errorf("deepgram: invalid configuration: %s", strings.Join(problems, "; "))
	}
	return nil
}

// validToken reports whether s is 1..maxLen ASCII letters, digits or '-'
// (and '_' and '.' when punct).
func validToken(s string, maxLen int, punct bool) bool {
	if s == "" || len(s) > maxLen {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
		case punct && (r == '_' || r == '.'):
		default:
			return false
		}
	}
	return true
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// endpointProblem returns why base is not an allowed endpoint, or "".
//
// The key travels in the upgrade request, so the endpoint decides who
// receives it: only api.deepgram.com over TLS, or a loopback test server.
func endpointProblem(base string) string {
	u, err := url.Parse(base)
	switch {
	case err != nil:
		return "BaseURL is not a URL"
	case u.User != nil:
		return "BaseURL must not carry credentials"
	case !hostOnly(u):
		return "BaseURL must be a scheme and host only"
	}
	host := u.Hostname()
	switch {
	case host == apiHost:
		if u.Scheme != "wss" {
			return "BaseURL must be wss: the API key travels in the upgrade request"
		}
		if p := u.Port(); p != "" && p != "443" {
			return "BaseURL must use Deepgram's standard port"
		}
		return ""
	case isLoopback(host):
		if u.Scheme != "wss" && u.Scheme != "ws" {
			return "BaseURL must be wss (or ws on loopback, for tests)"
		}
		return ""
	}
	return "BaseURL host is not allowed: the key is sent only to " + apiHost
}

// hostOnly reports whether u has nothing after its host but an optional "/".
func hostOnly(u *url.URL) bool {
	return u.Opaque == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" &&
		(u.Path == "" || u.Path == "/")
}

// errRedirect is what the client returns instead of following a redirect.
var errRedirect = errors.New("deepgram: refusing to follow a redirect")

// Provider implements speech.STTProvider.
type Provider struct {
	cfg    Config
	base   string
	key    string
	client *http.Client
}

var _ speech.STTProvider = (*Provider)(nil)

// New builds a provider. It reads the key file but contacts nothing.
func New(cfg Config) (*Provider, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(cfg.APIKeyFile)
	if err != nil {
		return nil, fmt.Errorf("deepgram: reading APIKeyFile %s: %w", cfg.APIKeyFile, err)
	}
	key := strings.TrimSpace(string(raw))
	if key == "" || strings.ContainsAny(key, "\r\n\t ") {
		return nil, errors.New("deepgram: APIKeyFile does not hold a single key")
	}
	c := http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   cfg.OpenTimeout,
		ResponseHeaderTimeout: cfg.OpenTimeout,
	}}
	if cfg.HTTPClient != nil {
		c = *cfg.HTTPClient
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return errRedirect }
	return &Provider{cfg: cfg, base: strings.TrimRight(cfg.BaseURL, "/"), key: key, client: &c}, nil
}

// ID returns the authored identifier.
func (p *Provider) ID() speech.ProviderID { return p.cfg.ID }

// supportedRates are the rates the adapter streams: telephony narrowband and
// wideband. ADR-0004 delivers 8 kHz.
var supportedRates = []media.SampleRate{media.Rate8kHz, media.Rate16kHz}

// Capabilities declares the configured languages, streaming with interim
// results, and the PCM rates.
func (p *Provider) Capabilities() speech.Capabilities {
	return speech.Capabilities{
		Languages:      append([]speech.Language(nil), p.cfg.Languages...),
		Streaming:      true,
		PartialResults: true,
		SampleRates:    append([]media.SampleRate(nil), supportedRates...),
	}
}

// listenURL is the streaming endpoint with the stream's parameters.
func (p *Provider) listenURL(cfg speech.STTConfig) string {
	q := url.Values{}
	q.Set("model", p.cfg.Model)
	q.Set("language", string(cfg.Language))
	q.Set("encoding", "linear16")
	q.Set("sample_rate", strconv.FormatUint(uint64(cfg.Format.Rate), 10))
	q.Set("channels", "1")
	q.Set("interim_results", "true")
	q.Set("endpointing", "false") // ADR-0005: endpointing is ours
	q.Set("punctuate", "true")
	return p.base + listenPath + "?" + q.Encode()
}

// OpenSTT connects and starts a recognition stream. Connection and upgrade
// failures are returned here; later failures are recorded on the stream.
func (p *Provider) OpenSTT(ctx context.Context, cfg speech.STTConfig) (speech.STTStream, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !p.Capabilities().Supports(cfg.Language) {
		return nil, fmt.Errorf("%w: %q is not declared", speech.ErrUnsupportedLanguage, cfg.Language)
	}
	f := cfg.Format
	if f.Codec != media.CodecPCM || f.Format != media.FormatPCM16 || f.Layout.Channels() != 1 ||
		!p.Capabilities().SupportsRate(f.Rate) {
		return nil, fmt.Errorf("%w: deepgram takes mono PCM16 at %v, not %s",
			speech.ErrInvalidAudio, supportedRates, f)
	}

	// The stream's context: cancelled with errStopped when the adapter itself
	// ends the stream, so that a caller's cancellation and a deadline can be
	// told apart from it.
	parent, cancelTimeout := ctx, context.CancelFunc(func() {})
	if cfg.Timeout > 0 {
		parent, cancelTimeout = context.WithTimeout(ctx, cfg.Timeout)
	}
	streamCtx, cancel := context.WithCancelCause(parent)

	conn, err := p.dial(streamCtx, cfg)
	if err != nil {
		cancel(errStopped)
		cancelTimeout()
		return nil, err
	}
	conn.SetReadLimit(p.cfg.MaxMessageBytes)
	s := newStream(streamCtx, p, cfg, conn, func() { cancel(errStopped); cancelTimeout() })
	return s, nil
}

// dial performs the upgrade, bounded by OpenTimeout. The returned error never
// quotes the response: it is classified by status only.
func (p *Provider) dial(ctx context.Context, cfg speech.STTConfig) (*websocket.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, p.cfg.OpenTimeout)
	defer cancel()
	header := http.Header{}
	header.Set("Authorization", "Token "+p.key)
	// The library owns resp.Body ("you never need to close resp.Body
	// yourself"); only the status is read.
	conn, resp, err := websocket.Dial(dialCtx, p.listenURL(cfg), &websocket.DialOptions{ //nolint:bodyclose // see above
		HTTPClient: p.client,
		HTTPHeader: header,
	})
	if err == nil {
		return conn, nil
	}
	if ctx.Err() != nil {
		if failure := contextFailure(ctx); failure != nil {
			return nil, failure // the caller's cancellation or the stream's deadline
		}
	}
	switch {
	case resp != nil:
		return nil, upgradeError(resp.StatusCode)
	case errors.Is(err, errRedirect):
		return nil, fmt.Errorf("%w: deepgram: the upgrade was redirected, and redirects are refused", speech.ErrProviderUnavailable)
	case dialCtx.Err() != nil:
		return nil, fmt.Errorf("%w: deepgram: the connection was not open within %v", speech.ErrProviderTimeout, p.cfg.OpenTimeout)
	}
	// A socket-level error names only the address, never provider text;
	// anything else is summarised.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return nil, fmt.Errorf("%w: deepgram: could not connect: %w", speech.ErrProviderUnavailable, opErr)
	}
	return nil, fmt.Errorf("%w: deepgram: the connection or upgrade failed", speech.ErrProviderUnavailable)
}

// upgradeError maps the status of a refused upgrade. The body is never
// quoted: it may echo the request.
func upgradeError(status int) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w: deepgram refused the credentials (%d)", speech.ErrProviderUnavailable, status)
	case status == http.StatusPaymentRequired:
		return fmt.Errorf("%w: deepgram refused the account (%d)", speech.ErrProviderUnavailable, status)
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("%w: deepgram answered %d", speech.ErrProviderRateLimited, status)
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return fmt.Errorf("%w: deepgram answered %d", speech.ErrProviderTimeout, status)
	case status >= 500:
		return fmt.Errorf("%w: deepgram answered %d", speech.ErrProviderUnavailable, status)
	case status >= 300 && status < 400:
		return fmt.Errorf("%w: deepgram: the upgrade was redirected (%d), and redirects are refused", speech.ErrProviderUnavailable, status)
	}
	return fmt.Errorf("%w: deepgram refused the upgrade (%d)", speech.ErrInternalFailure, status)
}

// errStopped is the cancellation cause when the adapter ends a stream itself.
var errStopped = errors.New("deepgram: stream stopped")

// contextFailure classifies why ctx ended: nil when the adapter stopped the
// stream itself (whoever stopped it recorded why), a timeout for a deadline,
// otherwise the caller's cancellation.
func contextFailure(ctx context.Context) error {
	cause := context.Cause(ctx)
	switch {
	case errors.Is(cause, errStopped):
		return nil
	case errors.Is(cause, context.DeadlineExceeded):
		return fmt.Errorf("%w: deepgram: the stream's deadline passed", speech.ErrProviderTimeout)
	}
	return speech.ErrSTTCancelled
}
