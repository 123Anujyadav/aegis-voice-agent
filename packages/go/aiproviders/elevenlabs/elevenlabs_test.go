package elevenlabs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	"github.com/callscreen/callscreen-platform/packages/go/providerconformance"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
)

// A local stand-in for the streaming text-to-speech endpoint, over TLS on
// loopback. Each request answers with audioBytes bytes, every byte equal to
// the request's ordinal (1, 2, 3 ...), so ordering is visible in the audio.

const testKey = "el-test-not-a-real-key-0123456789"

type fake struct {
	srv *httptest.Server

	mu         sync.Mutex
	requests   []recorded
	audioBytes int
	status     int
	hold       bool
	count      int
}

type recorded struct {
	path, query string
	body        map[string]any
	key         string
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{audioBytes: 1000, status: http.StatusOK}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck // recorded as-is
	f.mu.Lock()
	f.count++
	n := f.count
	f.requests = append(f.requests, recorded{r.URL.Path, r.URL.RawQuery, body, r.Header.Get("xi-api-key")})
	status, size, hold := f.status, f.audioBytes, f.hold
	f.mu.Unlock()

	if r.Header.Get("xi-api-key") != testKey {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"detail":{"message":"the text was: my PIN is 4321"}}`) //nolint:errcheck // test server
		return
	}
	w.Header().Set("content-type", "audio/pcm")
	_, _ = w.Write(bytes.Repeat([]byte{byte(n)}, size)) //nolint:errcheck // test server
	if hold {
		w.(http.Flusher).Flush() //nolint:forcetypeassert // httptest flushes
		<-r.Context().Done()
	}
}

func keyFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "elevenlabs-api-key")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func testConfig(t *testing.T, f *fake) Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.ID = "elevenlabs-test"
	cfg.APIKeyFile = keyFile(t, testKey)
	cfg.BaseURL = f.srv.URL
	cfg.HTTPClient = f.srv.Client()
	cfg.Languages = []speech.Language{speech.LangEnglishIN, speech.LangHindi}
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

func ttsConfig() speech.TTSConfig {
	return speech.TTSConfig{
		Session: speech.NewSessionID(), Turn: speech.NewTurnID(),
		Language: speech.LangEnglishIN, Format: media.PCM16Mono8k(),
		Voice: "conformance", Prosody: speech.DefaultProsody(),
	}
}

func collect(t *testing.T, st speech.TTSStream) []media.Frame {
	t.Helper()
	var frames []media.Frame
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f, ok := <-st.Audio():
			if !ok {
				return frames
			}
			frames = append(frames, f)
		case <-deadline:
			t.Fatal("Audio did not close")
		}
	}
}

func TestTheAdapterConformsToThePort(t *testing.T) {
	providerconformance.RunTTS(t, providerconformance.TTSSubject{
		New:    func(t *testing.T) speech.TTSProvider { return newProvider(t, newFake(t)) },
		Config: ttsConfig,
		Bound:  5 * time.Second,
	})
}

func TestStream_FramesAreWholeOrderedPaddedAndTimed(t *testing.T) {
	f := newFake(t)
	st, err := newProvider(t, f).OpenTTS(context.Background(), ttsConfig())
	if err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{"Hello.", "Who is calling?"} {
		if err := st.Synthesize(speech.Chunk{Sequence: uint64(i), Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CloseSend(); err != nil {
		t.Fatal(err)
	}
	frames := collect(t, st)
	// 1000 bytes per chunk at 320 bytes (20 ms of 8 kHz PCM16) per frame: four
	// frames each, the last padded with silence.
	if len(frames) != 8 {
		t.Fatalf("got %d frames, want 8", len(frames))
	}
	for i, fr := range frames {
		if len(fr.Payload) != 320 || fr.Sequence != uint64(i) || fr.Timestamp != time.Duration(i)*20*time.Millisecond {
			t.Errorf("frame %d: %d bytes, sequence %d, timestamp %v", i, len(fr.Payload), fr.Sequence, fr.Timestamp)
		}
		want := byte(1)
		if i >= 4 {
			want = 2 // the second chunk's audio, after the first's
		}
		if fr.Payload[0] != want {
			t.Errorf("frame %d carries chunk %d's audio, want %d", i, fr.Payload[0], want)
		}
	}
	last := frames[3].Payload
	if !bytes.Equal(last[40:], make([]byte, 280)) || last[39] != 1 {
		t.Error("the trailing partial frame was not padded with silence after its 40 real bytes")
	}
	if err := st.Close(); err != nil {
		t.Errorf("Close after a clean run = %v", err)
	}
}

func TestRequest_ShapeAndLanguage(t *testing.T) {
	f := newFake(t)
	cfg := ttsConfig()
	cfg.Language = speech.LangHindi
	cfg.Prosody.Rate = 1.1
	st, err := newProvider(t, f).OpenTTS(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Synthesize(speech.Chunk{Text: "नमस्ते", IsFinal: true}); err != nil {
		t.Fatal(err)
	}
	collect(t, st)
	_ = st.Close() //nolint:errcheck // checked elsewhere
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.requests[0]
	if r.path != "/v1/text-to-speech/conformance/stream" || r.query != "output_format=pcm_8000" {
		t.Errorf("path %q query %q", r.path, r.query)
	}
	if r.body["model_id"] != DefaultModel || r.body["language_code"] != "hi" || r.body["text"] != "नमस्ते" {
		t.Errorf("body = %v", r.body)
	}
	if vs, _ := r.body["voice_settings"].(map[string]any); vs["speed"] != 1.1 { //nolint:errcheck // checked
		t.Errorf("voice_settings = %v", r.body["voice_settings"])
	}
}

func TestOpen_RefusesWhatTheProviderCannotDo(t *testing.T) {
	p := newProvider(t, newFake(t))
	stereo := media.PCM16Mono8k()
	stereo.Layout = media.LayoutStereo
	for name, mutate := range map[string]func(*speech.TTSConfig){
		"undeclared language": func(c *speech.TTSConfig) { c.Language = speech.LangTamil },
		"stereo":              func(c *speech.TTSConfig) { c.Format = stereo },
		"pcm32":               func(c *speech.TTSConfig) { c.Format.Format = media.FormatPCM32 },
		"unsupported rate":    func(c *speech.TTSConfig) { c.Format.Rate = 48000 },
		"voice path":          func(c *speech.TTSConfig) { c.Voice = "../admin" },
		"pitch":               func(c *speech.TTSConfig) { c.Prosody.Pitch = 1.5 },
		"rate":                func(c *speech.TTSConfig) { c.Prosody.Rate = 2 },
	} {
		cfg := ttsConfig()
		mutate(&cfg)
		if st, err := p.OpenTTS(context.Background(), cfg); err == nil {
			_ = st.Close() //nolint:errcheck // the test has already failed
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSynthesize_IsBoundedOrderedAndEndsWithCloseSend(t *testing.T) {
	f := newFake(t)
	f.hold = true
	cfg := testConfig(t, f)
	cfg.MaxPendingChunks = 1
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st, err := p.OpenTTS(context.Background(), ttsConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close() //nolint:errcheck // test cleanup
	if err := st.Synthesize(speech.Chunk{Sequence: 0, Text: "one"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // the worker takes chunk 0 and is held by the server
	if err := st.Synthesize(speech.Chunk{Sequence: 1, Text: "two"}); err != nil {
		t.Fatalf("the queue's one slot: %v", err)
	}
	if err := st.Synthesize(speech.Chunk{Sequence: 2, Text: "three"}); !errors.Is(err, speech.ErrBackpressure) {
		t.Errorf("a full queue = %v, want ErrBackpressure", err)
	}
	if err := st.Synthesize(speech.Chunk{Sequence: 5, Text: "skip"}); !errors.Is(err, speech.ErrTranscriptOutOfOrder) {
		t.Errorf("a gap in sequence = %v, want ErrTranscriptOutOfOrder", err)
	}
	if err := st.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if err := st.Synthesize(speech.Chunk{Sequence: 2, Text: "late"}); !errors.Is(err, speech.ErrSpeechSessionClosed) {
		t.Errorf("after CloseSend = %v", err)
	}
}

func TestClose_CancelsAnInFlightRequestPromptly(t *testing.T) {
	f := newFake(t)
	f.hold = true
	st, err := newProvider(t, f).OpenTTS(context.Background(), ttsConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Synthesize(speech.Chunk{Text: "a long answer"}); err != nil {
		t.Fatal(err)
	}
	<-st.Audio() // audio is flowing; the server now holds the response open
	start := time.Now()
	if err := st.Close(); err != nil {
		t.Errorf("Close = %v (a caller's Close is not a failure)", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Close took %v", elapsed)
	}
	for range st.Audio() { //nolint:revive // draining until closed
	}
}

func TestErrors_AreReportedWithoutQuotingTheBodyOrKey(t *testing.T) {
	for status, want := range map[int]error{
		http.StatusTooManyRequests:     speech.ErrProviderRateLimited,
		http.StatusInternalServerError: speech.ErrProviderUnavailable,
		http.StatusBadRequest:          speech.ErrInternalFailure,
		http.StatusGatewayTimeout:      speech.ErrProviderTimeout,
	} {
		f := newFake(t)
		f.status = status
		st, err := newProvider(t, f).OpenTTS(context.Background(), ttsConfig())
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Synthesize(speech.Chunk{Text: "my PIN is 4321", IsFinal: true}); err != nil {
			t.Fatal(err)
		}
		if frames := collect(t, st); len(frames) != 0 {
			t.Errorf("status %d: %d frames", status, len(frames))
		}
		s := st.(*stream) //nolint:forcetypeassert // the adapter's own type
		if !errors.Is(s.Err(), want) {
			t.Errorf("status %d: Err() = %v, want %v (a failure must not look like silence)", status, s.Err(), want)
		}
		cerr := st.Close()
		if !errors.Is(cerr, want) {
			t.Errorf("status %d: Close() = %v", status, cerr)
		}
		for _, secret := range []string{"4321", "PIN", testKey} {
			if strings.Contains(s.Err().Error(), secret) {
				t.Errorf("status %d: the error echoes %q", status, secret)
			}
		}
	}
}

func TestEndpoint_TheKeyGoesOnlyToElevenLabsOverTLS(t *testing.T) {
	for base, ok := range map[string]bool{
		"":                                    true,
		"https://api.elevenlabs.io":           true,
		"https://127.0.0.1:8443":              true,
		"http://api.elevenlabs.io":            false,
		"https://api.elevenlabs.io.evil.test": false,
		"https://user:pw@api.elevenlabs.io":   false,
	} {
		if got := endpointProblem(base) == ""; got != ok {
			t.Errorf("%q: allowed=%v, want %v", base, got, ok)
		}
	}
}

func TestKeyFile_IsRequiredAbsoluteAndSingle(t *testing.T) {
	f := newFake(t)
	for name, mutate := range map[string]func(*Config){
		"missing":   func(c *Config) { c.APIKeyFile = "" },
		"relative":  func(c *Config) { c.APIKeyFile = "key" },
		"empty":     func(c *Config) { c.APIKeyFile = keyFile(t, "\n") },
		"two lines": func(c *Config) { c.APIKeyFile = keyFile(t, testKey+"\nx") },
		"languages": func(c *Config) { c.Languages = nil },
	} {
		cfg := testConfig(t, f)
		mutate(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), testKey) {
			t.Errorf("%s: the error contains the key", name)
		}
	}
}

func TestRedirects_AreNeverFollowed(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	cfg := testConfig(t, newFake(t))
	cfg.BaseURL, cfg.HTTPClient = redirector.URL, redirector.Client()
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st, err := p.OpenTTS(context.Background(), ttsConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Synthesize(speech.Chunk{Text: "hello", IsFinal: true}); err != nil {
		t.Fatal(err)
	}
	collect(t, st)
	if st.Close() == nil {
		t.Error("a redirect was followed or ignored")
	}
	if hits.Load() != 0 {
		t.Errorf("the redirect target received %d requests (and the key)", hits.Load())
	}
}
