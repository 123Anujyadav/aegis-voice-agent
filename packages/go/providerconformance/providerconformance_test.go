package providerconformance

import (
	"context"
	"errors"
	"testing"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	"github.com/callscreen/callscreen-platform/packages/go/speech"
)

// The suites run against the ports' own fakes, which must conform, and against
// deliberately broken providers, each of which must fail the check aimed at its
// defect: a suite that passed everything would prove nothing.

func sttConfig() speech.STTConfig {
	return speech.STTConfig{
		Session: speech.NewSessionID(), Turn: speech.NewTurnID(),
		Language: speech.LangEnglishIN, Format: media.PCM16Mono8k(),
	}
}

func ttsConfig() speech.TTSConfig {
	return speech.TTSConfig{
		Session: speech.NewSessionID(), Turn: speech.NewTurnID(),
		Language: speech.LangEnglishIN, Format: media.PCM16Mono8k(),
		Voice: "conformance", Prosody: speech.DefaultProsody(),
	}
}

func llmRequest() rt.GenerateRequest {
	return rt.GenerateRequest{
		Model:           "conformance",
		Messages:        []rt.Message{{Role: rt.RoleUser, Content: "hello"}},
		MaxOutputTokens: 16,
		Deadline:        time.Now().Add(time.Minute),
	}
}

func fakeSTT(*testing.T) speech.STTProvider {
	return speech.NewFakeSTTProvider("fake-stt", []speech.TranscriptSegment{
		{Text: "hello", IsFinal: false},
		{Text: "hello there", IsFinal: true},
	}, nil)
}

func TestTheSpeechFakesConform(t *testing.T) {
	RunSTT(t, STTSubject{New: fakeSTT, Config: sttConfig})
	RunTTS(t, TTSSubject{
		New:    func(*testing.T) speech.TTSProvider { return speech.NewFakeTTSProvider("fake-tts", 2, nil) },
		Config: ttsConfig,
	})
}

func TestTheRuntimeFakeConforms(t *testing.T) {
	RunLLM(t, LLMSubject{
		New:     func(*testing.T) rt.Provider { return rt.NewFakeProvider("fake-llm") },
		Request: llmRequest,
	})
}

// failing returns the named check's error, or fails the test if it passed.
func failing(t *testing.T, checks []Check, name string) {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			if err := c.Run(); err == nil {
				t.Fatalf("check %q passed a provider built to fail it", name)
			}
			return
		}
	}
	t.Fatalf("no check named %q", name)
}

// --- A recognition stream that blocks, never closes its results, and fails a second Close.

type brokenSTT struct{ caps speech.Capabilities }

func (brokenSTT) ID() speech.ProviderID               { return "broken-stt" }
func (b brokenSTT) Capabilities() speech.Capabilities { return b.caps }
func (brokenSTT) OpenSTT(context.Context, speech.STTConfig) (speech.STTStream, error) {
	return &brokenSTTStream{results: make(chan speech.TranscriptSegment)}, nil
}

type brokenSTTStream struct {
	results chan speech.TranscriptSegment
	closed  bool
}

func (s *brokenSTTStream) Write(media.Frame) error {
	select {} // blocks forever: the defect
}
func (s *brokenSTTStream) Results() <-chan speech.TranscriptSegment { return s.results }
func (s *brokenSTTStream) CloseSend() error                         { return nil }
func (s *brokenSTTStream) Close() error {
	if s.closed {
		return errors.New("already closed")
	}
	s.closed = true
	return nil
}

func TestTheSTTSuiteCatchesBrokenStreams(t *testing.T) {
	p := brokenSTT{caps: speech.Capabilities{SampleRates: []media.SampleRate{media.Rate16kHz}}}
	checks := STTChecks(t, STTSubject{
		New:    func(*testing.T) speech.STTProvider { return p },
		Config: sttConfig,
		Bound:  50 * time.Millisecond,
	})
	failing(t, checks, "write does not block")
	failing(t, checks, "the subject's config is one the provider declares") // 8 kHz is not declared
	failing(t, checks, "close is idempotent")
}

// --- A synthesis stream that reuses one payload buffer for every frame.

type sharedBufferTTS struct{}

func (sharedBufferTTS) ID() speech.ProviderID             { return "shared-buffer-tts" }
func (sharedBufferTTS) Capabilities() speech.Capabilities { return speech.Capabilities{} }
func (sharedBufferTTS) OpenTTS(_ context.Context, cfg speech.TTSConfig) (speech.TTSStream, error) {
	return &sharedBufferStream{audio: make(chan media.Frame, 16), buf: make([]byte, 320), format: cfg.Format}, nil
}

type sharedBufferStream struct {
	audio  chan media.Frame
	buf    []byte
	format media.AudioFormat
	ended  bool
}

func (s *sharedBufferStream) Synthesize(speech.Chunk) error {
	for i := 0; i < 2; i++ {
		s.audio <- media.Frame{Format: s.format, Payload: s.buf} // the defect: one buffer for all
	}
	return nil
}
func (s *sharedBufferStream) Audio() <-chan media.Frame { return s.audio }
func (s *sharedBufferStream) CloseSend() error {
	if !s.ended {
		s.ended = true
		close(s.audio)
	}
	return nil
}
func (s *sharedBufferStream) Close() error { return s.CloseSend() }

func TestTheTTSSuiteCatchesASharedBuffer(t *testing.T) {
	checks := TTSChecks(t, TTSSubject{
		New:    func(*testing.T) speech.TTSProvider { return sharedBufferTTS{} },
		Config: ttsConfig,
	})
	failing(t, checks, "frames are owned by the receiver")
}

// --- A language model that ignores cancellation and forgets Close.

type stubbornLLM struct{}

func (stubbornLLM) ID() rt.ProviderID             { return "stubborn-llm" }
func (stubbornLLM) Capabilities() rt.Capabilities { return rt.Capabilities{Streaming: true} }
func (stubbornLLM) Generate(context.Context, rt.GenerateRequest) (rt.TokenStream, error) {
	return stubbornStream{}, nil // ignores ctx, and ignores Close
}
func (stubbornLLM) Probe(context.Context) error { return nil }
func (stubbornLLM) Close() error                { return nil }

type stubbornStream struct{}

func (stubbornStream) Recv() (rt.Chunk, error) {
	time.Sleep(200 * time.Millisecond) // a slow token that cancellation does not interrupt
	return rt.Chunk{}, nil
}
func (stubbornStream) Close() error { return nil }

func TestTheLLMSuiteCatchesIgnoredCancellationAndClose(t *testing.T) {
	checks := LLMChecks(t, LLMSubject{
		New:         func(*testing.T) rt.Provider { return stubbornLLM{} },
		Request:     llmRequest,
		ReturnBound: 300 * time.Millisecond,
	})
	failing(t, checks, "cancelling the context makes Recv return promptly")
	failing(t, checks, "after Close, Generate returns ErrClosed")
	failing(t, checks, "generate returns promptly and the stream ends with io.EOF")
}
