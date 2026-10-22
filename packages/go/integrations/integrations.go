// Package integrations declares the provider ports for telephony, speech,
// push, cloud and messaging integrations, with placeholder implementations.
// None of the implementations here contacts a provider: each returns a fixed
// or synthetic result.
package integrations

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/eventbus"
)

// Errors returned by integrations.
var (
	// ErrProviderUnavailable reports that the target provider cannot be reached.
	ErrProviderUnavailable = errors.New("integrations: target provider unavailable")
	// ErrFailoverExhausted reports that every failover provider failed.
	ErrFailoverExhausted = errors.New("integrations: all failover providers failed")
)

// TelcoProvider places outbound calls through a telephony carrier.
type TelcoProvider interface {
	Name() string
	InitiateCall(ctx context.Context, from, to string) (string, error)
	HealthCheck(ctx context.Context) error
}

// ExotelClient is a placeholder Exotel TelcoProvider. It makes no network call.
type ExotelClient struct {
	AccountSid string
	APIKey     string
}

// NewExotelClient returns an ExotelClient holding the given account SID and key.
func NewExotelClient(sid, key string) *ExotelClient {
	return &ExotelClient{AccountSid: sid, APIKey: key}
}

// Name returns "Exotel".
func (e *ExotelClient) Name() string { return "Exotel" }

// InitiateCall places no call; it returns a synthetic call ID.
func (e *ExotelClient) InitiateCall(ctx context.Context, from, to string) (string, error) {
	return fmt.Sprintf("exo_call_%d", time.Now().UnixNano()), nil
}

// HealthCheck always reports healthy.
func (e *ExotelClient) HealthCheck(ctx context.Context) error { return nil }

// PlivoClient is a placeholder Plivo TelcoProvider. It makes no network call.
type PlivoClient struct {
	AuthID    string
	AuthToken string
}

// NewPlivoClient returns a PlivoClient holding the given auth ID and token.
func NewPlivoClient(id, token string) *PlivoClient {
	return &PlivoClient{AuthID: id, AuthToken: token}
}

// Name returns "Plivo".
func (p *PlivoClient) Name() string { return "Plivo" }

// InitiateCall places no call; it returns a synthetic call ID.
func (p *PlivoClient) InitiateCall(ctx context.Context, from, to string) (string, error) {
	return fmt.Sprintf("plivo_call_%d", time.Now().UnixNano()), nil
}

// HealthCheck always reports healthy.
func (p *PlivoClient) HealthCheck(ctx context.Context) error { return nil }

// SpeechToTextProvider transcribes audio, returning text and a confidence.
type SpeechToTextProvider interface {
	Name() string
	TranscribeStream(ctx context.Context, pcmData []byte) (string, float64, error)
}

// NewSarvamSttClient returns a SarvamSttClient holding apiKey.
func NewSarvamSttClient(apiKey string) *SarvamSttClient {
	return &SarvamSttClient{APIKey: apiKey}
}

// SarvamSttClient is a placeholder Sarvam SpeechToTextProvider. It makes no
// network call.
type SarvamSttClient struct {
	APIKey string
}

// Name returns "SarvamIndicASR".
func (s *SarvamSttClient) Name() string { return "SarvamIndicASR" }

// TranscribeStream ignores the audio and returns a fixed transcript.
func (s *SarvamSttClient) TranscribeStream(ctx context.Context, pcmData []byte) (string, float64, error) {
	return "Hello, Aegis AI screening call", 0.98, nil
}

// GoogleSttClient is a placeholder Google Cloud SpeechToTextProvider. It makes
// no network call.
type GoogleSttClient struct {
	ProjectID string
}

// NewGoogleSttClient returns a GoogleSttClient for projectID.
func NewGoogleSttClient(projectID string) *GoogleSttClient {
	return &GoogleSttClient{ProjectID: projectID}
}

// Name returns "GoogleCloudSTT".
func (g *GoogleSttClient) Name() string { return "GoogleCloudSTT" }

// TranscribeStream ignores the audio and returns a fixed transcript.
func (g *GoogleSttClient) TranscribeStream(ctx context.Context, pcmData []byte) (string, float64, error) {
	return "Hello, Aegis AI screening call", 0.95, nil
}

// TextToSpeechProvider synthesises speech audio from text.
type TextToSpeechProvider interface {
	Name() string
	SynthesizeSpeech(ctx context.Context, text, voiceID string) ([]byte, error)
}

// ElevenLabsClient is a placeholder ElevenLabs TextToSpeechProvider. It makes
// no network call.
type ElevenLabsClient struct {
	APIKey string
}

// NewElevenLabsClient returns an ElevenLabsClient holding apiKey.
func NewElevenLabsClient(apiKey string) *ElevenLabsClient {
	return &ElevenLabsClient{APIKey: apiKey}
}

// Name returns "ElevenLabsFlash".
func (e *ElevenLabsClient) Name() string { return "ElevenLabsFlash" }

// SynthesizeSpeech ignores its input and returns fixed placeholder bytes, not
// audio.
func (e *ElevenLabsClient) SynthesizeSpeech(ctx context.Context, text, voiceID string) ([]byte, error) {
	return []byte("PCM_16KHZ_AUDIO_BYTES"), nil
}

// FcmPushDispatcher sends push alerts through Firebase Cloud Messaging.
type FcmPushDispatcher interface {
	DispatchAlert(ctx context.Context, deviceToken, title, body string, highPriority bool) (string, error)
}

// MockFcmDispatcher is an FcmPushDispatcher that sends nothing.
type MockFcmDispatcher struct{}

// DispatchAlert sends nothing; it returns a synthetic message ID.
func (m *MockFcmDispatcher) DispatchAlert(ctx context.Context, deviceToken, title, body string, highPriority bool) (string, error) {
	return fmt.Sprintf("fcm_msg_%d", time.Now().UnixNano()), nil
}

// AwsSdkWrapper is the AWS surface used for object storage and KMS encryption.
type AwsSdkWrapper interface {
	UploadS3Object(ctx context.Context, bucket, key string, data []byte) error
	EncryptKms(ctx context.Context, keyID string, plaintext []byte) ([]byte, error)
}

// MockAwsWrapper is an AwsSdkWrapper that contacts no AWS service.
type MockAwsWrapper struct{}

// UploadS3Object stores nothing and always succeeds.
func (a *MockAwsWrapper) UploadS3Object(ctx context.Context, bucket, key string, data []byte) error {
	return nil
}

// EncryptKms does NOT encrypt: it returns the plaintext with a "KMS_ENC:"
// prefix.
func (a *MockAwsWrapper) EncryptKms(ctx context.Context, keyID string, plaintext []byte) ([]byte, error) {
	return append([]byte("KMS_ENC:"), plaintext...), nil
}

// FailoverTelcoGateway places calls through a primary TelcoProvider, switching
// permanently to the fallback after three primary failures.
type FailoverTelcoGateway struct {
	primary  TelcoProvider
	fallback TelcoProvider
	mu       sync.RWMutex
	failures int
}

// NewFailoverTelcoGateway returns a gateway over primary and fallback.
func NewFailoverTelcoGateway(primary, fallback TelcoProvider) *FailoverTelcoGateway {
	return &FailoverTelcoGateway{
		primary:  primary,
		fallback: fallback,
	}
}

// InitiateCall uses the primary while it has failed fewer than three times,
// and the fallback otherwise or when the primary call fails.
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

// EventbusKafkaDriver is a placeholder Kafka event-bus driver that publishes
// nothing.
type EventbusKafkaDriver struct{}

// Publish discards the message and always succeeds.
func (k *EventbusKafkaDriver) Publish(ctx context.Context, topic eventbus.Topic, key string, payload []byte) error {
	return nil
}

// RedisPubSubRelay is a placeholder Redis pub/sub relay that publishes nothing.
type RedisPubSubRelay struct{}

// PublishChannel discards the message and always succeeds.
func (r *RedisPubSubRelay) PublishChannel(ctx context.Context, channel string, message []byte) error {
	return nil
}
