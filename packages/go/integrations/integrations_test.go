package integrations

import (
	"context"
	"errors"
	"testing"
)

type FailingMockProvider struct{}

func (f *FailingMockProvider) Name() string { return "FailingMock" }
func (f *FailingMockProvider) InitiateCall(ctx context.Context, from, to string) (string, error) {
	return "", errors.New("mock call failed")
}
func (f *FailingMockProvider) HealthCheck(ctx context.Context) error {
	return errors.New("unhealthy")
}

func TestTelcoProvider_FailoverCircuitBreaker(t *testing.T) {
	primary := &FailingMockProvider{}
	fallback := NewPlivoClient("test_id", "test_token")
	gateway := NewFailoverTelcoGateway(primary, fallback)

	ctx := context.Background()
	from := "+919876543210"
	to := "+911140008899"

	// 1-3. Trigger primary failures to trip circuit breaker
	for i := 0; i < 3; i++ {
		_, _ = gateway.InitiateCall(ctx, from, to)
	}

	// 4. Verify automatic failover to fallback (Plivo)
	callID, err := gateway.InitiateCall(ctx, from, to)
	if err != nil {
		t.Fatalf("Failover call failed: %v", err)
	}
	if callID == "" {
		t.Fatalf("expected non-empty call ID from fallback provider")
	}
}

func TestSarvamStt_Transcription(t *testing.T) {
	client := NewSarvamSttClient("sarvam_api_key")
	text, confidence, err := client.TranscribeStream(context.Background(), []byte("PCM_STREAM"))
	if err != nil {
		t.Fatalf("TranscribeStream failed: %v", err)
	}
	if confidence < 0.90 {
		t.Errorf("expected high confidence > 0.90, got %f", confidence)
	}
	if text == "" {
		t.Errorf("expected non-empty transcript text")
	}
}

func TestElevenLabs_TTS(t *testing.T) {
	client := NewElevenLabsClient("eleven_labs_key")
	audio, err := client.SynthesizeSpeech(context.Background(), "Hello", "voice_sarah")
	if err != nil {
		t.Fatalf("SynthesizeSpeech failed: %v", err)
	}
	if len(audio) == 0 {
		t.Errorf("expected non-empty audio payload")
	}
}

func TestFcmPushDispatcher(t *testing.T) {
	dispatcher := &MockFcmDispatcher{}
	msgID, err := dispatcher.DispatchAlert(context.Background(), "dev_token_123", "Scam Warning", "High risk scam blocked", true)
	if err != nil {
		t.Fatalf("DispatchAlert failed: %v", err)
	}
	if msgID == "" {
		t.Errorf("expected valid message ID")
	}
}
