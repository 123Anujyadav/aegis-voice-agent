package integrations

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/eventbus"
)

var (
	ErrProviderUnavailable = errors.New("integrations: target provider unavailable")
	ErrFailoverExhausted   = errors.New("integrations: all failover providers failed")
)

type TelcoProvider interface {
	Name() string
	InitiateCall(ctx context.Context, from, to string) (string, error)
	HealthCheck(ctx context.Context) error
}

type ExotelClient struct {
	AccountSid string
	ApiKey     string
}

func NewExotelClient(sid, key string) *ExotelClient {
	return &ExotelClient{AccountSid: sid, ApiKey: key}
}

func (e *ExotelClient) Name() string { return "Exotel" }
func (e *ExotelClient) InitiateCall(ctx context.Context, from, to string) (string, error) {
	return fmt.Sprintf("exo_call_%d", time.Now().UnixNano()), nil
}
func (e *ExotelClient) HealthCheck(ctx context.Context) error { return nil }

type PlivoClient struct {
	AuthId    string
	AuthToken string
}

func NewPlivoClient(id, token string) *PlivoClient {
	return &PlivoClient{AuthId: id, AuthToken: token}
}

func (p *PlivoClient) Name() string { return "Plivo" }
func (p *PlivoClient) InitiateCall(ctx context.Context, from, to string) (string, error) {
	return fmt.Sprintf("plivo_call_%d", time.Now().UnixNano()), nil
}
func (p *PlivoClient) HealthCheck(ctx context.Context) error { return nil }

type SpeechToTextProvider interface {
	Name() string
	TranscribeStream(ctx context.Context, pcmData []byte) (string, float64, error)
}

func NewSarvamSttClient(apiKey string) *SarvamSttClient {
	return &SarvamSttClient{ApiKey: apiKey}
}

type SarvamSttClient struct {
	ApiKey string
}

func (s *SarvamSttClient) Name() string { return "SarvamIndicASR" }
func (s *SarvamSttClient) TranscribeStream(ctx context.Context, pcmData []byte) (string, float64, error) {
	return "Hello, Aegis AI screening call", 0.98, nil
}

type GoogleSttClient struct {
	ProjectID string
}

func NewGoogleSttClient(projectID string) *GoogleSttClient {
	return &GoogleSttClient{ProjectID: projectID}
}

func (g *GoogleSttClient) Name() string { return "GoogleCloudSTT" }
func (g *GoogleSttClient) TranscribeStream(ctx context.Context, pcmData []byte) (string, float64, error) {
	return "Hello, Aegis AI screening call", 0.95, nil
}

type TextToSpeechProvider interface {
	Name() string
	SynthesizeSpeech(ctx context.Context, text, voiceID string) ([]byte, error)
}

type ElevenLabsClient struct {
	ApiKey string
}

func NewElevenLabsClient(apiKey string) *ElevenLabsClient {
	return &ElevenLabsClient{ApiKey: apiKey}
}

func (e *ElevenLabsClient) Name() string { return "ElevenLabsFlash" }
func (e *ElevenLabsClient) SynthesizeSpeech(ctx context.Context, text, voiceID string) ([]byte, error) {
	return []byte("PCM_16KHZ_AUDIO_BYTES"), nil
}

type FcmPushDispatcher interface {
	DispatchAlert(ctx context.Context, deviceToken, title, body string, highPriority bool) (string, error)
}

type MockFcmDispatcher struct{}

func (m *MockFcmDispatcher) DispatchAlert(ctx context.Context, deviceToken, title, body string, highPriority bool) (string, error) {
	return fmt.Sprintf("fcm_msg_%d", time.Now().UnixNano()), nil
}

type AwsSdkWrapper interface {
	UploadS3Object(ctx context.Context, bucket, key string, data []byte) error
	EncryptKms(ctx context.Context, keyID string, plaintext []byte) ([]byte, error)
}

type MockAwsWrapper struct{}

func (a *MockAwsWrapper) UploadS3Object(ctx context.Context, bucket, key string, data []byte) error {
	return nil
}

func (a *MockAwsWrapper) EncryptKms(ctx context.Context, keyID string, plaintext []byte) ([]byte, error) {
	return append([]byte("KMS_ENC:"), plaintext...), nil
}

type FailoverTelcoGateway struct {
	primary  TelcoProvider
	fallback TelcoProvider
	mu       sync.RWMutex
	failures int
}

func NewFailoverTelcoGateway(primary, fallback TelcoProvider) *FailoverTelcoGateway {
	return &FailoverTelcoGateway{
		primary:  primary,
		fallback: fallback,
	}
}

func (f *FailoverTelcoGateway) InitiateCall(ctx context.Context, from, to string) (string, error) {
	f.mu.RLock()
	isPrimaryFailed := f.failures >= 3
	f.mu.RUnlock()

	if !isPrimaryFailed {
		callID, err := f.primary.InitiateCall(ctx, from, to)
		if err == nil {
			return callID, nil
		}
		f.mu.Lock()
		f.failures++
		f.mu.Unlock()
	}

	return f.fallback.InitiateCall(ctx, from, to)
}

type EventbusKafkaDriver struct{}

func (k *EventbusKafkaDriver) Publish(ctx context.Context, topic eventbus.Topic, key string, payload []byte) error {
	return nil
}

type RedisPubSubRelay struct{}

func (r *RedisPubSubRelay) PublishChannel(ctx context.Context, channel string, message []byte) error {
	return nil
}
