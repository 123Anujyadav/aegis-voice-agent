// Package google adapts speech.STTProvider to Google Cloud Speech-to-Text v2
// streaming recognition (the StreamingRecognize gRPC method).
//
// ADR-0005 (Accepted) makes Google STT v2 the primary recogniser: the
// `telephony` model, pinned to asia-south1 (Mumbai) so that call audio is
// processed in India. ADR-0034 (approved by the owner on 2026-11-12) adds the
// gRPC transport and Google's generated Speech v2 client, contained to this
// module, because v2 streams only over gRPC.
//
// # Not activated
//
// No service builds a Provider. Using one is the speech spending category
// (deferred with OD-17), consent (OD-4), and the processor terms (E9). See
// README.md.
//
// # Credentials
//
// In production Config.ClientOptions is empty and the client library finds
// credentials through Google's default chain, which on EKS must resolve to a
// Workload Identity Federation configuration (PLAY-INTEGRITY-DECISION-MEMO.md
// §7), never a service-account key. That lookup, and the gRPC connection, are
// made lazily on the first OpenSTT: New reads nothing from the environment and
// contacts nothing. Tests always pass option.WithGRPCConn (an in-process
// connection) and option.WithoutAuthentication.
//
// # Endpointing is ours (ADR-0005 §5)
//
// Voice-activity events are not requested, and the v2 endpointing knob
// (StreamingRecognitionFeatures.endpointing_sensitivity) is left unset; the
// adapter never treats a vendor event as the end of a turn. A final result
// only marks a segment that will not be revised.
//
// # Security properties
//
//   - Audio and transcripts are never logged. The client library's own logger
//     is replaced with a discarding one, so an environment variable cannot turn
//     on request logging, and its OpenTelemetry instrumentation is disabled.
//   - An error never quotes Google's status message, which could echo what was
//     said; only the gRPC code is reported.
//   - The endpoint is a *.googleapis.com host on port 443, or loopback for a
//     test; the recogniser path is built from validated parts.
//   - Every queue is bounded: audio by MaxPendingFrames, results by
//     MaxPendingResults.
package google

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	speechapi "cloud.google.com/go/speech/apiv2"
	"cloud.google.com/go/speech/apiv2/speechpb"
	"google.golang.org/api/option"
	"google.golang.org/grpc/status"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
)

// The defaults ADR-0005 decides.
const (
	// DefaultLocation is the only location accepted: ADR-0005 pins recognition
	// to asia-south1 for residency (C4).
	DefaultLocation = "asia-south1"
	// DefaultRecognizer is the implicit recogniser: the request carries the
	// whole configuration, so no Recognizer resource has to exist.
	DefaultRecognizer = "_"
	// DefaultModel is ADR-0005's narrowband model (C2).
	DefaultModel = "telephony"
	// DefaultEndpoint is the regional endpoint for DefaultLocation.
	DefaultEndpoint = "asia-south1-speech.googleapis.com:443"
)

// maxAudioPerRequest bounds the audio in one StreamingRecognizeRequest. The
// v2 API documents the audio field as "Maximum size for this field is 15 KB
// per request" (speechpb, StreamingRecognizeRequest_Audio). 15000 is the
// conservative reading of "15 KB", and even, so a PCM16 sample is never split.
const maxAudioPerRequest = 15000

// Config describes one Google STT v2 provider.
type Config struct {
	// ID is the authored provider identifier.
	ID speech.ProviderID
	// Project is the Google Cloud project ID or number. Required.
	Project string
	// Location must be asia-south1 (empty means DefaultLocation). Any other
	// location, including "global", is refused: moving recognition out of
	// India is a residency decision (ADR-0005, ADR-0012), not configuration.
	Location string
	// Recognizer is the recogniser ID (empty means DefaultRecognizer).
	Recognizer string
	// Model is the recognition model (empty means DefaultModel).
	Model string
	// Languages the provider declares, as BCP-47 tags sent to Google as-is.
	// Required: which languages ship is OD-17's decision.
	Languages []speech.Language
	// Endpoint is host:port (empty means DefaultEndpoint). Only a
	// *.googleapis.com host on port 443, or a loopback host for tests.
	Endpoint string
	// ClientOptions is a test seam, appended before the adapter's own options
	// (so it cannot override the endpoint, logger or telemetry settings).
	// Tests pass option.WithGRPCConn and option.WithoutAuthentication.
	// Production leaves it empty, which selects Google's default credentials.
	ClientOptions []option.ClientOption
	// MaxPendingFrames bounds audio frames accepted but not yet sent.
	MaxPendingFrames int
	// MaxPendingResults bounds segments received but not yet read.
	MaxPendingResults int
	// ResultTimeout bounds the wait for the end of results after the last
	// audio is sent (and between responses in that period).
	ResultTimeout time.Duration
}

// DefaultConfig returns the ADR-0005 settings and transport bounds, leaving
// the decisions (ID, Project, Languages) empty.
func DefaultConfig() Config {
	return Config{
		Location:          DefaultLocation,
		Recognizer:        DefaultRecognizer,
		Model:             DefaultModel,
		Endpoint:          DefaultEndpoint,
		MaxPendingFrames:  256,
		MaxPendingResults: 64,
		ResultTimeout:     5 * time.Second,
	}
}

var (
	projectID     = regexp.MustCompile(`^([a-z][a-z0-9-]{4,28}[a-z0-9]|\d{1,20})$`)
	recognizerID  = regexp.MustCompile(`^([a-z]([a-z0-9-]{0,61}[a-z0-9])?|_)$`)
	modelName     = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	languageTag   = regexp.MustCompile(`^[A-Za-z]{2,8}(-[A-Za-z0-9]{1,8}){0,4}$`)
	endpointHost  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
	loopbackHosts = map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
)

func (c *Config) applyDefaults() {
	d := DefaultConfig()
	if c.Location == "" {
		c.Location = d.Location
	}
	if c.Recognizer == "" {
		c.Recognizer = d.Recognizer
	}
	if c.Model == "" {
		c.Model = d.Model
	}
	if c.Endpoint == "" {
		c.Endpoint = d.Endpoint
	}
	if c.MaxPendingFrames == 0 {
		c.MaxPendingFrames = d.MaxPendingFrames
	}
	if c.MaxPendingResults == 0 {
		c.MaxPendingResults = d.MaxPendingResults
	}
	if c.ResultTimeout == 0 {
		c.ResultTimeout = d.ResultTimeout
	}
}

func (c *Config) validate() error {
	var problems []string
	if !c.ID.Valid() {
		problems = append(problems, "ID must be a valid provider identifier")
	}
	if !projectID.MatchString(c.Project) {
		problems = append(problems, "Project must be a Google Cloud project ID or number")
	}
	if c.Location != DefaultLocation {
		problems = append(problems, fmt.Sprintf(
			"Location %q is refused: ADR-0005 pins recognition to %s (in-country); "+
				"another location is a residency decision, not configuration", c.Location, DefaultLocation))
	}
	if !recognizerID.MatchString(c.Recognizer) {
		problems = append(problems, "Recognizer must be \"_\" or a recogniser ID")
	}
	if !modelName.MatchString(c.Model) {
		problems = append(problems, "Model must be 1-64 lowercase letters, digits or underscores")
	}
	if len(c.Languages) == 0 {
		problems = append(problems, "Languages must be set: the recognition languages are OD-17's decision")
	}
	for _, l := range c.Languages {
		if !languageTag.MatchString(string(l)) {
			problems = append(problems, fmt.Sprintf("language %q is not a BCP-47 tag", l))
		}
	}
	if problem := endpointProblem(c.Endpoint); problem != "" {
		problems = append(problems, problem)
	}
	if c.MaxPendingFrames <= 0 || c.MaxPendingResults <= 0 || c.ResultTimeout <= 0 {
		problems = append(problems, "MaxPendingFrames, MaxPendingResults and ResultTimeout must be positive")
	}
	if len(problems) > 0 {
		return fmt.Errorf("google: invalid configuration: %s", strings.Join(problems, "; "))
	}
	return nil
}

// endpointProblem pins the endpoint: a *.googleapis.com host on port 443, or
// a loopback host on any port for a test.
func endpointProblem(endpoint string) string {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "Endpoint must be host:port"
	}
	if n, perr := strconv.Atoi(port); perr != nil || n <= 0 || n > 65535 {
		return "Endpoint port must be a number"
	}
	if loopbackHosts[host] {
		return ""
	}
	if !endpointHost.MatchString(host) || !strings.HasSuffix(host, ".googleapis.com") {
		return fmt.Sprintf("Endpoint host %q is not allowed: audio is sent only to a *.googleapis.com host", host)
	}
	if port != "443" {
		return "Endpoint port must be 443 for a googleapis.com host"
	}
	return ""
}

// Provider implements speech.STTProvider over one lazily created client.
//
// It also implements io.Closer: Close releases the client's gRPC connection.
// Close streams first; a stream still open when its provider closes ends with
// a recorded failure.
type Provider struct {
	cfg Config

	mu     sync.Mutex
	client *speechapi.Client
	closed bool
}

var _ speech.STTProvider = (*Provider)(nil)

// New builds a provider. It validates the configuration and nothing else: it
// reads no credentials and contacts nothing.
func New(cfg Config) (*Provider, error) {
	cfg.applyDefaults()
	cfg.Languages = append([]speech.Language(nil), cfg.Languages...)
	cfg.ClientOptions = append([]option.ClientOption(nil), cfg.ClientOptions...)
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Provider{cfg: cfg}, nil
}

// ID returns the authored identifier.
func (p *Provider) ID() speech.ProviderID { return p.cfg.ID }

// supportedRates are the rates the adapter sends: telephony narrowband
// (ADR-0004) and wideband.
var supportedRates = []media.SampleRate{media.Rate8kHz, media.Rate16kHz}

// Capabilities declares the configured languages, streaming with partial
// results, and the two PCM rates.
func (p *Provider) Capabilities() speech.Capabilities {
	return speech.Capabilities{
		Languages:      append([]speech.Language(nil), p.cfg.Languages...),
		Streaming:      true,
		PartialResults: true,
		SampleRates:    append([]media.SampleRate(nil), supportedRates...),
	}
}

// Recognizer returns the recogniser resource name every stream uses.
func (p *Provider) Recognizer() string {
	return fmt.Sprintf("projects/%s/locations/%s/recognizers/%s",
		p.cfg.Project, p.cfg.Location, p.cfg.Recognizer)
}

// Close releases the client, if one was created. Idempotent; OpenSTT fails
// afterwards.
func (p *Provider) Close() error {
	p.mu.Lock()
	c := p.client
	p.client, p.closed = nil, true
	p.mu.Unlock()
	if c == nil {
		return nil
	}
	return c.Close()
}

// discardLogger replaces the client library's logger, which an environment
// variable could otherwise enable.
var discardLogger = slog.New(slog.DiscardHandler)

// speechClient returns the client, creating it on first use. Creating it is
// where credentials are looked up (only when ClientOptions supply none) and
// where a non-blocking gRPC connection is set up.
func (p *Provider) speechClient(ctx context.Context) (*speechapi.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, fmt.Errorf("%w: google stt: the provider is closed", speech.ErrProviderUnavailable)
	}
	if p.client != nil {
		return p.client, nil
	}
	opts := append(append([]option.ClientOption(nil), p.cfg.ClientOptions...),
		option.WithEndpoint(p.cfg.Endpoint),
		option.WithLogger(discardLogger),
		option.WithTelemetryDisabled(),
	)
	// The connection outlives this call: the caller's cancellation must not
	// reach the client's own setup.
	c, err := speechapi.NewClient(context.WithoutCancel(ctx), opts...)
	if err != nil {
		return nil, fmt.Errorf("%w: google stt: creating the client: %w", speech.ErrProviderUnavailable, err)
	}
	p.client = c
	return c, nil
}

// OpenSTT starts a streaming recognition session. It opens the gRPC stream;
// the configuration message is the first thing the sender sends.
func (p *Provider) OpenSTT(ctx context.Context, cfg speech.STTConfig) (speech.STTStream, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !p.Capabilities().Supports(cfg.Language) {
		return nil, fmt.Errorf("%w: %q is not declared", speech.ErrUnsupportedLanguage, cfg.Language)
	}
	hz := sampleRateHertz(cfg.Format)
	if hz == 0 {
		return nil, fmt.Errorf("%w: google stt takes mono PCM16 at %v, not %s",
			speech.ErrInvalidAudio, supportedRates, cfg.Format)
	}
	client, err := p.speechClient(ctx)
	if err != nil {
		return nil, err
	}

	streamCtx, cancel := context.WithCancel(ctx)
	if cfg.Timeout > 0 {
		streamCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
	}
	rpc, err := client.StreamingRecognize(streamCtx)
	if err != nil {
		cancel()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %w", speech.ErrSTTCancelled, ctx.Err())
		}
		return nil, statusError(status.Code(err))
	}

	s := &stream{
		id:            p.cfg.ID,
		model:         p.cfg.Model,
		languages:     p.cfg.Languages,
		resultTimeout: p.cfg.ResultTimeout,
		cfg:           cfg,
		parent:        ctx,
		ctx:           streamCtx,
		cancel:        cancel,
		rpc:           rpc,
		first:         p.configRequest(cfg, hz),
		audio:         make(chan []byte, p.cfg.MaxPendingFrames),
		results:       make(chan speech.TranscriptSegment, p.cfg.MaxPendingResults),
		activity:      make(chan struct{}, 1),
		recvDone:      make(chan struct{}),
	}
	s.wg.Add(2)
	go s.send()
	go s.receive()
	return s, nil
}

// sampleRateHertz returns the rate for a format the adapter accepts (mono
// PCM16 at 8 or 16 kHz), or zero.
func sampleRateHertz(f media.AudioFormat) int32 {
	if f.Codec != media.CodecPCM || f.Format != media.FormatPCM16 || f.Layout != media.LayoutMono {
		return 0
	}
	switch f.Rate {
	case media.Rate8kHz:
		return 8000
	case media.Rate16kHz:
		return 16000
	}
	return 0
}

// configRequest is the first message: the recogniser and the whole
// recognition configuration, and no audio.
func (p *Provider) configRequest(cfg speech.STTConfig, hz int32) *speechpb.StreamingRecognizeRequest {
	return &speechpb.StreamingRecognizeRequest{
		Recognizer: p.Recognizer(),
		StreamingRequest: &speechpb.StreamingRecognizeRequest_StreamingConfig{
			StreamingConfig: &speechpb.StreamingRecognitionConfig{
				Config: &speechpb.RecognitionConfig{
					DecodingConfig: &speechpb.RecognitionConfig_ExplicitDecodingConfig{
						ExplicitDecodingConfig: &speechpb.ExplicitDecodingConfig{
							Encoding:          speechpb.ExplicitDecodingConfig_LINEAR16,
							SampleRateHertz:   hz,
							AudioChannelCount: 1,
						},
					},
					Model:         p.cfg.Model,
					LanguageCodes: []string{string(cfg.Language)},
					Features:      &speechpb.RecognitionFeatures{EnableAutomaticPunctuation: true},
				},
				// Interim results only. EnableVoiceActivityEvents,
				// VoiceActivityTimeout and EndpointingSensitivity stay unset:
				// endpointing is ours (ADR-0005 §5).
				StreamingFeatures: &speechpb.StreamingRecognitionFeatures{InterimResults: true},
			},
		},
	}
}
