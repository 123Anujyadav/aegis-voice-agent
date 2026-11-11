// Package elevenlabs adapts speech.TTSProvider to ElevenLabs' streaming
// text-to-speech API.
//
// ADR-0007 (Accepted) makes ElevenLabs Flash v2.5 the default synthesiser,
// with sentence-level streaming: each speakable chunk is synthesised as soon as
// it exists, and audio frames stream out while later chunks are still being
// generated.
//
// # Not activated
//
// Building a Provider needs an API key in a mounted file. No service holds one.
// Using it is the speech spending category, which the owner deferred (OD-17),
// plus ADR-0012's cross-border conditions: the text is the agent's own
// generated speech, not caller audio, but it still leaves India.
//
// # Audio format
//
// The adapter asks for raw 16-bit PCM at the stream's own rate (pcm_8000,
// pcm_16000, pcm_22050, pcm_24000 or pcm_44100). ADR-0007 prefers 8 kHz µ-law
// "so the frame handed to media-relay needs no resample"; pcm_8000 also needs
// no resample, and the media engine's frames are PCM (media has no µ-law
// codec). µ-law encoding, if a carrier wants it, belongs to media-relay.
//
// # Security properties
//
//   - The key is read from a file and sent only to api.elevenlabs.io over TLS,
//     or to a loopback test server. Redirects are never followed.
//   - Text to be spoken is personal data (ADR-0007). It is never logged, and an
//     error never quotes a response body.
//   - Every request is bounded by the stream's context, and the queue of text
//     by MaxPendingChunks.
package elevenlabs

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
	"sync"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
)

// DefaultBaseURL is ElevenLabs' API.
const DefaultBaseURL = "https://api.elevenlabs.io"

// DefaultModel is ADR-0007's primary: Flash v2.5.
const DefaultModel = "eleven_flash_v2_5"

// Config describes one ElevenLabs provider.
type Config struct {
	// ID is the authored provider identifier.
	ID speech.ProviderID
	// APIKeyFile is the absolute path of a file holding the key, mounted from
	// a Secret. Never an environment variable.
	APIKeyFile string
	// BaseURL is the API's address. Empty means DefaultBaseURL. Only
	// https://api.elevenlabs.io or a loopback host (for tests) is accepted.
	BaseURL string
	// Model is the synthesis model. Empty means DefaultModel.
	Model string
	// Languages the provider declares. Required: which voice languages ship is
	// OD-17's decision, recorded in configuration, not assumed here.
	Languages []speech.Language
	// FrameDuration is the length of each emitted frame (20 ms if zero).
	FrameDuration time.Duration
	// MaxPendingChunks bounds text queued but not yet synthesised.
	MaxPendingChunks int
	// MaxPendingFrames bounds frames synthesised but not yet read.
	MaxPendingFrames int
	// MaxChunkChars bounds one chunk's text.
	MaxChunkChars int
	// ChunkTimeout bounds one chunk's synthesis request.
	ChunkTimeout time.Duration
	// HTTPClient overrides the client; used by tests. Redirects are refused
	// either way.
	HTTPClient *http.Client
}

// DefaultConfig returns transport settings, leaving the decisions (ID,
// APIKeyFile, Languages) empty.
func DefaultConfig() Config {
	return Config{
		BaseURL:          DefaultBaseURL,
		Model:            DefaultModel,
		FrameDuration:    20 * time.Millisecond,
		MaxPendingChunks: 16,
		MaxPendingFrames: 256,
		MaxChunkChars:    1000,
		ChunkTimeout:     15 * time.Second,
	}
}

func (c Config) validate() error {
	var problems []string
	if c.ID == "" {
		problems = append(problems, "ID must be set")
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
	if len(c.Languages) == 0 {
		problems = append(problems, "Languages must be set: the voice languages are OD-17's decision")
	}
	if c.FrameDuration < 0 || c.MaxPendingChunks <= 0 || c.MaxPendingFrames <= 0 ||
		c.MaxChunkChars <= 0 || c.ChunkTimeout <= 0 {
		problems = append(problems, "FrameDuration must not be negative, and the bounds and ChunkTimeout must be positive")
	}
	if len(problems) > 0 {
		return fmt.Errorf("elevenlabs: invalid configuration: %s", strings.Join(problems, "; "))
	}
	return nil
}

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
	case u.Hostname() == "api.elevenlabs.io":
		return ""
	case u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1":
		return ""
	}
	return fmt.Sprintf("BaseURL host %q is not allowed: the key is sent only to api.elevenlabs.io", u.Hostname())
}

// Provider implements speech.TTSProvider.
type Provider struct {
	cfg    Config
	base   string
	key    string
	client *http.Client
}

var _ speech.TTSProvider = (*Provider)(nil)

// New builds a provider. It reads the key file but contacts nothing.
func New(cfg Config) (*Provider, error) {
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.FrameDuration == 0 {
		cfg.FrameDuration = 20 * time.Millisecond
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(cfg.APIKeyFile)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs: reading APIKeyFile %s: %w", cfg.APIKeyFile, err)
	}
	key := strings.TrimSpace(string(raw))
	if key == "" || strings.ContainsAny(key, "\r\n\t ") {
		return nil, errors.New("elevenlabs: APIKeyFile does not hold a single key")
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	c := http.Client{Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConnsPerHost: 8, ForceAttemptHTTP2: true,
	}}
	if cfg.HTTPClient != nil {
		c = *cfg.HTTPClient
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("elevenlabs: refusing to follow a redirect")
	}
	return &Provider{cfg: cfg, base: strings.TrimRight(base, "/"), key: key, client: &c}, nil
}

// ID returns the authored identifier.
func (p *Provider) ID() speech.ProviderID { return p.cfg.ID }

// supportedRates are the PCM output rates the API offers.
var supportedRates = []media.SampleRate{8000, 16000, 22050, 24000, 44100}

// Capabilities declares the configured languages, streaming, and the PCM rates.
func (p *Provider) Capabilities() speech.Capabilities {
	return speech.Capabilities{
		Languages:   append([]speech.Language(nil), p.cfg.Languages...),
		Streaming:   true,
		SampleRates: append([]media.SampleRate(nil), supportedRates...),
	}
}

// OpenTTS starts a synthesis stream. It contacts nothing until the first
// chunk.
func (p *Provider) OpenTTS(ctx context.Context, cfg speech.TTSConfig) (speech.TTSStream, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !p.Capabilities().Supports(cfg.Language) {
		return nil, fmt.Errorf("%w: %q is not declared", speech.ErrUnsupportedLanguage, cfg.Language)
	}
	f := cfg.Format
	if f.Codec != media.CodecPCM || f.Format != media.FormatPCM16 || f.Layout.Channels() != 1 ||
		!p.Capabilities().SupportsRate(f.Rate) {
		return nil, fmt.Errorf("%w: elevenlabs produces mono PCM16 at %v, not %s",
			speech.ErrInvalidAudio, supportedRates, f)
	}
	if !validVoice(string(cfg.Voice)) {
		return nil, fmt.Errorf("%w: voice id must be 1-64 letters or digits", speech.ErrInvalidAudio)
	}
	if cfg.Prosody.Pitch != 1 || cfg.Prosody.Volume != 1 || cfg.Prosody.Rate < 0.7 || cfg.Prosody.Rate > 1.2 {
		return nil, fmt.Errorf("%w: elevenlabs supports a rate of 0.7-1.2 and no pitch or volume change", speech.ErrInvalidAudio)
	}
	streamCtx, cancel := context.WithCancel(ctx)
	if cfg.Timeout > 0 {
		streamCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
	}
	s := &stream{
		p: p, cfg: cfg, ctx: streamCtx, cancel: cancel,
		chunks:     make(chan speech.Chunk, p.cfg.MaxPendingChunks),
		audio:      make(chan media.Frame, p.cfg.MaxPendingFrames),
		frameBytes: f.BytesFor(p.cfg.FrameDuration),
		done:       make(chan struct{}),
	}
	if s.frameBytes <= 0 {
		cancel()
		return nil, fmt.Errorf("%w: FrameDuration gives an empty frame", speech.ErrInvalidAudio)
	}
	go s.run(streamCtx)
	return s, nil
}

func validVoice(v string) bool {
	if v == "" || len(v) > 64 {
		return false
	}
	for _, r := range v {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// languageCode maps "hi-IN" to "hi": the API takes ISO 639-1.
func languageCode(l speech.Language) string {
	code, _, _ := strings.Cut(string(l), "-")
	return strings.ToLower(code)
}

// outputFormat is the API's name for the stream's PCM rate.
func outputFormat(r media.SampleRate) string { return fmt.Sprintf("pcm_%d", uint32(r)) }

// synthesise sends one chunk and returns the response body.
func (p *Provider) synthesise(ctx context.Context, cfg speech.TTSConfig, text string) (io.ReadCloser, error) {
	body, err := json.Marshal(map[string]any{
		"text":          text,
		"model_id":      p.cfg.Model,
		"language_code": languageCode(cfg.Language),
		"voice_settings": map[string]any{
			"speed": cfg.Prosody.Rate,
		},
	})
	if err != nil {
		return nil, err
	}
	u := fmt.Sprintf("%s/v1/text-to-speech/%s/stream?output_format=%s",
		p.base, url.PathEscape(string(cfg.Voice)), outputFormat(cfg.Format.Rate))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", p.key)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "audio/pcm")
	resp, err := p.client.Do(req) //nolint:bodyclose // on success the caller owns and closes the body
	if err != nil {
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			return nil, speech.ErrTTSCancelled
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return nil, speech.ErrProviderTimeout
		}
		return nil, fmt.Errorf("%w: elevenlabs: request failed: %w", speech.ErrProviderUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 16<<10)) //nolint:errcheck // the status decides; the body is never quoted
		_ = resp.Body.Close()                                         //nolint:errcheck // as above
		return nil, statusError(resp.StatusCode)
	}
	return resp.Body, nil
}

// statusError maps an HTTP status. The body is never quoted: it may echo the
// text being spoken.
func statusError(status int) error {
	switch {
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("%w: elevenlabs answered %d", speech.ErrProviderRateLimited, status)
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return fmt.Errorf("%w: elevenlabs answered %d", speech.ErrProviderTimeout, status)
	case status >= 500:
		return fmt.Errorf("%w: elevenlabs answered %d", speech.ErrProviderUnavailable, status)
	}
	return fmt.Errorf("%w: elevenlabs refused the request (%d)", speech.ErrInternalFailure, status)
}

// stream is one synthesis session. A single worker synthesises chunks in
// order, so audio is never reordered.
type stream struct {
	p      *Provider
	cfg    speech.TTSConfig
	ctx    context.Context
	cancel context.CancelFunc

	chunks     chan speech.Chunk
	audio      chan media.Frame
	frameBytes int
	done       chan struct{}

	mu       sync.Mutex
	sentEnd  bool
	closed   bool
	nextSeq  uint64
	haveSeq  bool
	failure  error
	frameSeq uint64
	played   time.Duration
}

var _ speech.TTSStream = (*stream)(nil)

// Synthesize queues one chunk. It never blocks: a full queue is backpressure.
func (s *stream) Synthesize(c speech.Chunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return speech.ErrSpeechSessionClosed
	case s.sentEnd:
		return fmt.Errorf("%w: text has ended (CloseSend)", speech.ErrSpeechSessionClosed)
	case len([]rune(c.Text)) > s.p.cfg.MaxChunkChars:
		return fmt.Errorf("%w: a chunk is longer than %d characters", speech.ErrInvalidTranscript, s.p.cfg.MaxChunkChars)
	case s.haveSeq && c.Sequence != s.nextSeq:
		return fmt.Errorf("%w: chunk %d after %d", speech.ErrTranscriptOutOfOrder, c.Sequence, s.nextSeq-1)
	}
	select {
	case s.chunks <- c:
		s.haveSeq, s.nextSeq = true, c.Sequence+1
		if c.IsFinal {
			s.sentEnd = true
			close(s.chunks)
		}
		return nil
	default:
		return speech.ErrBackpressure
	}
}

// Audio yields frames, each owned by the receiver.
func (s *stream) Audio() <-chan media.Frame { return s.audio }

// CloseSend ends the text. Audio for queued chunks still arrives; then Audio
// closes. Idempotent.
func (s *stream) CloseSend() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.sentEnd {
		return nil
	}
	s.sentEnd = true
	close(s.chunks)
	return nil
}

// Close abandons the stream: the in-flight request is cancelled, queued text
// is dropped, and Audio closes. Idempotent. It returns the failure that ended
// synthesis, if one did.
func (s *stream) Close() error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		if !s.sentEnd {
			s.sentEnd = true
			close(s.chunks)
		}
	}
	s.mu.Unlock()
	s.cancel()
	<-s.done
	return s.Err()
}

// Err reports why synthesis failed, or nil. The port reports errors only from
// calls a caller makes; a failed request has no such call, so without this a
// failure would look like silence.
func (s *stream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failure
}

func (s *stream) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure == nil && !s.closed {
		s.failure = err
	}
}

func (s *stream) run(ctx context.Context) {
	defer close(s.done)
	defer close(s.audio)
	defer s.cancel()
	for c := range s.chunks {
		if ctx.Err() != nil {
			return
		}
		if strings.TrimSpace(c.Text) == "" {
			continue // an empty chunk says nothing
		}
		if err := s.speak(ctx, c.Text); err != nil {
			s.fail(err)
			return
		}
	}
}

// speak synthesises one chunk and emits its audio in frames. A trailing
// partial frame is padded with silence, not dropped.
func (s *stream) speak(ctx context.Context, text string) error {
	chunkCtx, cancel := context.WithTimeout(ctx, s.p.cfg.ChunkTimeout)
	defer cancel()
	body, err := s.p.synthesise(chunkCtx, s.cfg, text)
	if err != nil {
		return err
	}
	defer body.Close() //nolint:errcheck // a read error below is what is reported
	for {
		buf := make([]byte, s.frameBytes) // owned by the receiver: never reused
		n, err := io.ReadFull(body, buf)
		// A short final read is emitted as a whole frame: the unread tail of
		// buf is zero, which is PCM silence, so a partial sample or frame is
		// padded rather than dropped.
		if n > 0 && !s.emit(buf) {
			return nil // closed
		}
		switch {
		case err == nil:
			continue
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			return nil
		case errors.Is(ctx.Err(), context.Canceled):
			return nil
		case errors.Is(chunkCtx.Err(), context.DeadlineExceeded):
			return speech.ErrProviderTimeout
		}
		return fmt.Errorf("%w: elevenlabs: reading audio: %w", speech.ErrProviderUnavailable, err)
	}
}

// emit delivers one frame, waiting for room; false when the stream is closed.
func (s *stream) emit(payload []byte) bool {
	s.mu.Lock()
	f := media.Frame{
		Sequence:  s.frameSeq,
		Timestamp: s.played,
		Format:    s.cfg.Format,
		Payload:   payload,
	}
	s.frameSeq++
	s.played += s.cfg.Format.DurationFor(len(payload))
	s.mu.Unlock()
	select {
	case s.audio <- f:
		return true
	case <-s.ctx.Done():
		return false
	}
}
