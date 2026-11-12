package deepgram

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	"github.com/callscreen/callscreen-platform/packages/go/providerconformance"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
)

// A local stand-in for wss://api.deepgram.com/v1/listen: an httptest TLS
// server on loopback, upgraded with coder/websocket's server side. The client
// reaches it as wss://127.0.0.1:port, so these tests exercise TLS end to end.
//
// It records the upgrade request and everything the client sends, and is
// scripted per test: refuse the upgrade, send messages on open, close with a
// code after some frames, answer CloseStream with replies, stay silent, or
// stop reading altogether.

const testKey = "dg-test-not-a-real-key-0123456789"

// secret is text a provider might echo back (a close reason, an error body, a
// malformed message). No error may ever quote it.
const secret = "the caller said my PIN is 4321"

const (
	interim = `{"type":"Results","channel_index":[0,1],"duration":1.0,"start":0.5,"is_final":false,` +
		`"speech_final":false,"channel":{"alternatives":[{"transcript":"hello","confidence":0.8,"words":[]}]}}`
	final = `{"type":"Results","channel_index":[0,1],"duration":1.5,"start":0.5,"is_final":true,` +
		`"speech_final":true,"channel":{"alternatives":[{"transcript":"hello there","confidence":0.95,"words":[]}]}}`
	empty = `{"type":"Results","duration":0.2,"start":2.0,"is_final":true,` +
		`"channel":{"alternatives":[{"transcript":"","confidence":0}]}}`
	metadata      = `{"type":"Metadata","request_id":"r-1","model_info":{"name":"nova-3"}}`
	speechStarted = `{"type":"SpeechStarted","channel":[0],"timestamp":0.4}`
	utteranceEnd  = `{"type":"UtteranceEnd","channel":[0,1],"last_word_end":2.1}`
)

type fake struct {
	srv     *httptest.Server
	release chan struct{} // closed at cleanup: a stalled handler returns

	mu            sync.Mutex
	requests      int
	path, query   string
	auth          string
	frames        [][]byte
	texts         []string
	closeStreamAt int // binary frames received before CloseStream; -1 before

	// The script. Set before the first request.
	status    int                  // refuse the upgrade with this HTTP status
	onOpen    []string             // sent as soon as the socket is open
	replies   []string             // sent after CloseStream
	closeCode websocket.StatusCode // close code after the replies (normal if 0)
	silent    bool                 // never answer CloseStream
	stall     bool                 // never read anything
	failAfter int                  // close with failCode (or drop) after this many frames
	failCode  websocket.StatusCode
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{release: make(chan struct{}), closeStreamAt: -1, replies: []string{metadata, interim, final}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	t.Cleanup(func() { close(f.release) }) // runs first: cleanups are LIFO
	return f
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests++
	f.path, f.query, f.auth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
	status := f.status
	f.mu.Unlock()

	if r.Header.Get("Authorization") != "Token "+testKey {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"err_msg":"`+secret+`"}`) //nolint:errcheck // test server
		return
	}
	if status != 0 {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"err_msg":"`+secret+`"}`) //nolint:errcheck // test server
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow() //nolint:errcheck // test server
	c.SetReadLimit(1 << 20)
	ctx := r.Context() // valid until this handler returns

	f.mu.Lock()
	onOpen, stall := f.onOpen, f.stall
	f.mu.Unlock()
	for _, m := range onOpen {
		if c.Write(ctx, websocket.MessageText, []byte(m)) != nil {
			return
		}
	}
	if stall {
		<-f.release
		return
	}
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			f.mu.Lock()
			f.frames = append(f.frames, data)
			n, failAfter, failCode := len(f.frames), f.failAfter, f.failCode
			f.mu.Unlock()
			if failAfter > 0 && n == failAfter {
				if failCode != 0 {
					_ = c.Close(failCode, secret) //nolint:errcheck // test server
				}
				return // with no code: the deferred CloseNow drops the connection
			}
			continue
		}
		f.mu.Lock()
		f.texts = append(f.texts, string(data))
		isEnd := string(data) == `{"type":"CloseStream"}`
		if isEnd {
			f.closeStreamAt = len(f.frames)
		}
		replies, silent, code := f.replies, f.silent, f.closeCode
		f.mu.Unlock()
		if !isEnd || silent {
			continue
		}
		for _, m := range replies {
			if c.Write(ctx, websocket.MessageText, []byte(m)) != nil {
				return
			}
		}
		if code == 0 {
			code = websocket.StatusNormalClosure
		}
		_ = c.Close(code, secret) //nolint:errcheck // test server
		return
	}
}

func (f *fake) wsURL() string { return "wss" + strings.TrimPrefix(f.srv.URL, "https") }

func (f *fake) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func keyFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "deepgram-api-key")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func testConfig(t *testing.T, f *fake) Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.ID = "deepgram-test"
	cfg.APIKeyFile = keyFile(t, testKey+"\n")
	cfg.BaseURL = f.wsURL()
	cfg.HTTPClient = f.srv.Client()
	cfg.Languages = []speech.Language{speech.LangEnglishIN, "hi"}
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

func sttConfig() speech.STTConfig {
	return speech.STTConfig{
		Session: speech.NewSessionID(), Turn: speech.NewTurnID(),
		Language: speech.LangEnglishIN, Format: media.PCM16Mono8k(),
	}
}

// frame is 20 ms of 8 kHz PCM16 with every byte equal to b.
func frame(seq uint64, b byte) media.Frame {
	format := media.PCM16Mono8k()
	return media.Frame{
		Sequence: seq, Timestamp: time.Duration(seq) * 20 * time.Millisecond,
		Format: format, Payload: bytes.Repeat([]byte{b}, format.BytesFor(20*time.Millisecond)),
	}
}

func open(t *testing.T, p *Provider) *stream {
	t.Helper()
	st, err := p.OpenSTT(context.Background(), sttConfig())
	if err != nil {
		t.Fatalf("OpenSTT: %v", err)
	}
	s, ok := st.(*stream)
	if !ok {
		t.Fatalf("OpenSTT returned %T", st)
	}
	return s
}

// collect reads Results until it closes.
func collect(t *testing.T, s *stream) []speech.TranscriptSegment {
	t.Helper()
	var got []speech.TranscriptSegment
	deadline := time.After(5 * time.Second)
	for {
		select {
		case seg, ok := <-s.Results():
			if !ok {
				return got
			}
			got = append(got, seg)
		case <-deadline:
			t.Fatal("Results did not close")
		}
	}
}

// assertNoLeak fails if err quotes the key or anything the provider sent.
func assertNoLeak(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, s := range []string{testKey, secret, "PIN", "4321"} {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("error quotes %q: %v", s, err)
		}
	}
}

// ---------------------------------------------------------------------------
// The port.
// ---------------------------------------------------------------------------

func TestTheAdapterConformsToThePort(t *testing.T) {
	providerconformance.RunSTT(t, providerconformance.STTSubject{
		New:    func(t *testing.T) speech.STTProvider { return newProvider(t, newFake(t)) },
		Config: sttConfig,
		Bound:  5 * time.Second,
	})
}

func TestCapabilities(t *testing.T) {
	caps := newProvider(t, newFake(t)).Capabilities()
	if !caps.Streaming || !caps.PartialResults {
		t.Fatalf("Streaming=%v PartialResults=%v, want both", caps.Streaming, caps.PartialResults)
	}
	if !caps.Supports(speech.LangEnglishIN) || !caps.Supports("hi") || caps.Supports(speech.LangTamil) {
		t.Fatalf("Languages = %v, want exactly the configured ones", caps.Languages)
	}
	for _, r := range []media.SampleRate{media.Rate8kHz, media.Rate16kHz} {
		if !caps.SupportsRate(r) {
			t.Fatalf("rate %v not declared", r)
		}
	}
	if caps.SupportsRate(media.Rate24kHz) {
		t.Fatal("24 kHz declared")
	}
}

// ---------------------------------------------------------------------------
// The request.
// ---------------------------------------------------------------------------

func TestTheRequestShape(t *testing.T) {
	f := newFake(t)
	s := open(t, newProvider(t, f))
	sent := []media.Frame{frame(0, 1), frame(1, 2), frame(2, 3)}
	for _, fr := range sent {
		if err := s.Write(fr); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CloseSend(); err != nil {
		t.Fatal(err)
	}
	collect(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.path != "/v1/listen" {
		t.Fatalf("path = %q", f.path)
	}
	q, err := url.ParseQuery(f.query)
	if err != nil {
		t.Fatal(err)
	}
	want := url.Values{
		"model": {"nova-3"}, "language": {"en-IN"}, "encoding": {"linear16"}, "sample_rate": {"8000"},
		"channels": {"1"}, "interim_results": {"true"}, "endpointing": {"false"}, "punctuate": {"true"},
	}
	if len(q) != len(want) {
		t.Fatalf("query = %v, want exactly %v", q, want)
	}
	for k, v := range want {
		if got := q[k]; len(got) != 1 || got[0] != v[0] {
			t.Fatalf("query %s = %v, want %v", k, got, v)
		}
	}
	if f.auth != "Token "+testKey {
		t.Fatal("the Authorization header is not 'Token <key>'")
	}
	if len(f.frames) != len(sent) {
		t.Fatalf("server received %d frames, want %d", len(f.frames), len(sent))
	}
	for i, fr := range sent {
		if !bytes.Equal(f.frames[i], fr.Payload) {
			t.Fatalf("frame %d differs or is out of order", i)
		}
	}
	if len(f.texts) != 1 || f.texts[0] != `{"type":"CloseStream"}` || f.closeStreamAt != len(sent) {
		t.Fatalf("texts = %q, CloseStream after %d frames; want one CloseStream after the last frame",
			f.texts, f.closeStreamAt)
	}
}

func TestTheLanguageTagIsPassedUnchanged(t *testing.T) {
	f := newFake(t)
	p := newProvider(t, f)
	cfg := sttConfig()
	cfg.Language, cfg.Format = "hi", media.PCM16Mono16k()
	st, err := p.OpenSTT(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	q, _ := url.ParseQuery(f.query) //nolint:errcheck // checked by the values below
	if q.Get("language") != "hi" || q.Get("sample_rate") != "16000" {
		t.Fatalf("language=%q sample_rate=%q", q.Get("language"), q.Get("sample_rate"))
	}
}

func TestWriteSendsACopy(t *testing.T) {
	f := newFake(t)
	f.replies = nil
	s := open(t, newProvider(t, f))
	fr := frame(0, 7)
	if err := s.Write(fr); err != nil {
		t.Fatal(err)
	}
	for i := range fr.Payload { // the caller reuses its buffer at once
		fr.Payload[i] = 0
	}
	if err := s.CloseSend(); err != nil {
		t.Fatal(err)
	}
	collect(t, s)
	_ = s.Close() //nolint:errcheck // the frame is what is checked
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.frames) != 1 || !bytes.Equal(f.frames[0], frame(0, 7).Payload) {
		t.Fatal("the adapter sent the caller's reused buffer, not a copy")
	}
}

// ---------------------------------------------------------------------------
// Results.
// ---------------------------------------------------------------------------

func TestResultsAreMapped(t *testing.T) {
	f := newFake(t)
	f.replies = []string{metadata, speechStarted, interim, empty, utteranceEnd, final, metadata}
	p := newProvider(t, f)
	cfg := sttConfig()
	st, err := p.OpenSTT(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := st.(*stream) //nolint:forcetypeassert // the adapter's own type
	if err := s.Write(frame(0, 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseSend(); err != nil {
		t.Fatal(err)
	}
	got := collect(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("a clean end must not be a failure: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d segments, want 2 (empty transcripts and metadata skipped)", len(got))
	}
	want := []struct {
		text       string
		final      bool
		confidence float64
		start, end time.Duration
	}{
		{"hello", false, 0.8, 500 * time.Millisecond, 1500 * time.Millisecond},
		{"hello there", true, 0.95, 500 * time.Millisecond, 2 * time.Second},
	}
	for i, w := range want {
		g := got[i]
		if g.Text != w.text || g.IsFinal != w.final || g.Confidence != w.confidence ||
			g.StartTime != w.start || g.EndTime != w.end {
			t.Fatalf("segment %d = %+v (text %q), want %+v", i, g, g.Text, w)
		}
		if g.Sequence != uint64(i+1) || g.Session != cfg.Session || g.Turn != cfg.Turn ||
			g.Language != cfg.Language || g.Role != speech.RoleCaller ||
			g.Meta.Provider != "deepgram-test" || g.Meta.Model != DefaultModel {
			t.Fatalf("segment %d metadata = %s", i, g.Redacted())
		}
		if err := g.Validate(); err != nil {
			t.Fatalf("segment %d invalid: %v", i, err)
		}
	}
	if got[0].Segment == got[1].Segment {
		t.Fatal("segment ids repeat")
	}
}

func TestAMissingConfidenceIsUnknown(t *testing.T) {
	f := newFake(t)
	f.replies = []string{`{"type":"Results","is_final":true,"channel":{"alternatives":[{"transcript":"hi"}]}}`}
	s := open(t, newProvider(t, f))
	if err := s.CloseSend(); err != nil {
		t.Fatal(err)
	}
	got := collect(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Confidence != speech.ConfidenceUnknown {
		t.Fatalf("got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Failures: each is recorded, ends Results, and quotes nothing.
// ---------------------------------------------------------------------------

func expectFailure(t *testing.T, s *stream, sentinel error) {
	t.Helper()
	collect(t, s)
	err := s.Err()
	if !errors.Is(err, sentinel) {
		t.Fatalf("Err() = %v, want %v", err, sentinel)
	}
	assertNoLeak(t, err)
	if cerr := s.Close(); !errors.Is(cerr, sentinel) {
		t.Fatalf("Close() = %v, want the recorded failure", cerr)
	}
}

func TestAnErrorCloseMidStreamIsAFailure(t *testing.T) {
	f := newFake(t)
	f.failAfter, f.failCode = 1, websocket.StatusInternalError
	s := open(t, newProvider(t, f))
	if err := s.Write(frame(0, 1)); err != nil {
		t.Fatal(err)
	}
	expectFailure(t, s, speech.ErrProviderUnavailable)
}

func TestADroppedConnectionIsAFailure(t *testing.T) {
	f := newFake(t)
	f.failAfter = 1 // and no close code: the connection just ends
	s := open(t, newProvider(t, f))
	if err := s.Write(frame(0, 1)); err != nil {
		t.Fatal(err)
	}
	expectFailure(t, s, speech.ErrProviderUnavailable)
}

func TestTryAgainLaterIsARateLimit(t *testing.T) {
	f := newFake(t)
	f.failAfter, f.failCode = 1, websocket.StatusTryAgainLater
	s := open(t, newProvider(t, f))
	if err := s.Write(frame(0, 1)); err != nil {
		t.Fatal(err)
	}
	expectFailure(t, s, speech.ErrProviderRateLimited)
}

func TestANormalCloseBeforeTheEndOfAudioIsAFailure(t *testing.T) {
	f := newFake(t)
	f.failAfter, f.failCode = 1, websocket.StatusNormalClosure
	s := open(t, newProvider(t, f))
	if err := s.Write(frame(0, 1)); err != nil {
		t.Fatal(err)
	}
	expectFailure(t, s, speech.ErrProviderUnavailable)
}

func TestAnErrorCloseAfterCloseStreamIsAFailure(t *testing.T) {
	f := newFake(t)
	f.replies, f.closeCode = []string{interim}, websocket.StatusInternalError
	s := open(t, newProvider(t, f))
	if err := s.CloseSend(); err != nil {
		t.Fatal(err)
	}
	expectFailure(t, s, speech.ErrProviderUnavailable)
}

func TestMalformedMessagesAreFailures(t *testing.T) {
	for name, msg := range map[string]string{
		"not json":         `{"type":"Results","channel": ` + secret,
		"unknown type":     `{"type":"` + secret + `"}`,
		"no type":          `{"note":"` + secret + `"}`,
		"no channel":       `{"type":"Results","is_final":true}`,
		"no is_final":      `{"type":"Results","channel":{"alternatives":[{"transcript":"` + secret + `"}]}}`,
		"bad confidence":   `{"type":"Results","is_final":true,"channel":{"alternatives":[{"transcript":"x","confidence":7}]}}`,
		"negative start":   `{"type":"Results","is_final":true,"start":-1,"channel":{"alternatives":[{"transcript":"x"}]}}`,
		"wrong field type": `{"type":"Results","is_final":"` + secret + `","channel":{"alternatives":[]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.onOpen = []string{msg}
			s := open(t, newProvider(t, f))
			expectFailure(t, s, speech.ErrInternalFailure)
		})
	}
}

func TestABinaryMessageIsAFailure(t *testing.T) {
	// The fake sends text only: this server sends one binary message.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()                                                //nolint:errcheck // test server
		_ = c.Write(r.Context(), websocket.MessageBinary, []byte(secret)) //nolint:errcheck // test server
		_, _, _ = c.Read(r.Context())                                     //nolint:errcheck,dogsled // wait for the client to go
	}))
	defer srv.Close()
	cfg := testConfig(t, newFake(t))
	cfg.BaseURL, cfg.HTTPClient = "wss"+strings.TrimPrefix(srv.URL, "https"), srv.Client()
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	expectFailure(t, open(t, p), speech.ErrInternalFailure)
}

func TestAnOversizedMessageIsAFailure(t *testing.T) {
	f := newFake(t)
	f.onOpen = []string{`{"type":"Metadata","pad":"` + strings.Repeat("x", 4096) + `"}`}
	cfg := testConfig(t, f)
	cfg.MaxMessageBytes = 1024
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	expectFailure(t, open(t, p), speech.ErrInternalFailure)
}

func TestSilenceAfterCloseSendTimesOut(t *testing.T) {
	f := newFake(t)
	f.silent = true
	cfg := testConfig(t, f)
	cfg.ResultTimeout = 100 * time.Millisecond
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := open(t, p)
	if err := s.Write(frame(0, 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseSend(); err != nil {
		t.Fatal(err)
	}
	expectFailure(t, s, speech.ErrProviderTimeout)
}

func TestTheCallersCancellationIsRecorded(t *testing.T) {
	f := newFake(t)
	f.silent = true
	ctx, cancel := context.WithCancel(context.Background())
	st, err := newProvider(t, f).OpenSTT(ctx, sttConfig())
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	expectFailure(t, st.(*stream), speech.ErrSTTCancelled) //nolint:forcetypeassert // the adapter's own type
}

func TestTheStreamTimeoutIsRecorded(t *testing.T) {
	f := newFake(t)
	f.silent = true
	cfg := sttConfig()
	cfg.Timeout = 100 * time.Millisecond
	st, err := newProvider(t, f).OpenSTT(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	expectFailure(t, st.(*stream), speech.ErrProviderTimeout) //nolint:forcetypeassert // the adapter's own type
}

// ---------------------------------------------------------------------------
// Opening.
// ---------------------------------------------------------------------------

func TestUpgradeRefusalsAreClassified(t *testing.T) {
	for _, tc := range []struct {
		status   int
		sentinel error
	}{
		{http.StatusUnauthorized, speech.ErrProviderUnavailable},
		{http.StatusForbidden, speech.ErrProviderUnavailable},
		{http.StatusPaymentRequired, speech.ErrProviderUnavailable},
		{http.StatusTooManyRequests, speech.ErrProviderRateLimited},
		{http.StatusGatewayTimeout, speech.ErrProviderTimeout},
		{http.StatusServiceUnavailable, speech.ErrProviderUnavailable},
		{http.StatusBadRequest, speech.ErrInternalFailure},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			f := newFake(t)
			f.status = tc.status
			st, err := newProvider(t, f).OpenSTT(context.Background(), sttConfig())
			if st != nil || !errors.Is(err, tc.sentinel) {
				t.Fatalf("OpenSTT = %v, %v; want %v", st, err, tc.sentinel)
			}
			assertNoLeak(t, err)
		})
	}
}

func TestAWrongKeyIsAnAuthFailure(t *testing.T) {
	f := newFake(t)
	cfg := testConfig(t, f)
	cfg.APIKeyFile = keyFile(t, "dg-some-other-key")
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.OpenSTT(context.Background(), sttConfig())
	if !errors.Is(err, speech.ErrProviderUnavailable) || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("OpenSTT = %v, want a credentials failure", err)
	}
	if strings.Contains(err.Error(), "dg-some-other-key") {
		t.Fatal("the error quotes the key")
	}
	assertNoLeak(t, err)
}

func TestARefusedConnectionFailsOpen(t *testing.T) {
	f := newFake(t)
	p := newProvider(t, f)
	f.srv.Close() // nothing listens any more
	_, err := p.OpenSTT(context.Background(), sttConfig())
	if !errors.Is(err, speech.ErrProviderUnavailable) {
		t.Fatalf("OpenSTT = %v, want ErrProviderUnavailable", err)
	}
	assertNoLeak(t, err)
}

func TestAnUnansweredUpgradeTimesOut(t *testing.T) {
	hold := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-hold }))
	defer srv.Close()
	defer close(hold)
	cfg := testConfig(t, newFake(t))
	cfg.BaseURL, cfg.HTTPClient = "wss"+strings.TrimPrefix(srv.URL, "https"), srv.Client()
	cfg.OpenTimeout = 100 * time.Millisecond
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.OpenSTT(context.Background(), sttConfig())
	if !errors.Is(err, speech.ErrProviderTimeout) {
		t.Fatalf("OpenSTT = %v, want ErrProviderTimeout", err)
	}
}

func TestOpenRefusesUndeclaredLanguagesAndFormats(t *testing.T) {
	f := newFake(t)
	p := newProvider(t, f)

	cfg := sttConfig()
	cfg.Language = speech.LangTamil
	if _, err := p.OpenSTT(context.Background(), cfg); !errors.Is(err, speech.ErrUnsupportedLanguage) {
		t.Fatalf("undeclared language: %v", err)
	}
	for name, format := range map[string]media.AudioFormat{
		"stereo":  {Format: media.FormatPCM16, Layout: media.LayoutStereo, Rate: media.Rate8kHz, Codec: media.CodecPCM},
		"pcm32":   {Format: media.FormatPCM32, Layout: media.LayoutMono, Rate: media.Rate8kHz, Codec: media.CodecPCM},
		"24 kHz":  {Format: media.FormatPCM16, Layout: media.LayoutMono, Rate: media.Rate24kHz, Codec: media.CodecPCM},
		"opaque":  {Format: media.FormatPCM16, Layout: media.LayoutMono, Rate: media.Rate8kHz, Codec: media.CodecOpaque},
		"invalid": {},
	} {
		cfg := sttConfig()
		cfg.Format = format
		_, err := p.OpenSTT(context.Background(), cfg)
		if err == nil {
			t.Fatalf("%s: OpenSTT accepted it", name)
		}
		if name != "invalid" && !errors.Is(err, speech.ErrInvalidAudio) {
			t.Fatalf("%s: %v, want ErrInvalidAudio", name, err)
		}
	}
	if n := f.requestCount(); n != 0 {
		t.Fatalf("the server was contacted %d times for refused configs", n)
	}
}

// ---------------------------------------------------------------------------
// Writing and closing.
// ---------------------------------------------------------------------------

func TestWriteRefusesTheWrongFormat(t *testing.T) {
	s := open(t, newProvider(t, newFake(t)))
	defer s.Close() //nolint:errcheck // checked elsewhere

	wide := frame(0, 1)
	wide.Format = media.PCM16Mono16k()
	odd := frame(0, 1)
	odd.Payload = odd.Payload[:len(odd.Payload)-1]
	emptyFrame := frame(0, 1)
	emptyFrame.Payload = nil
	long := frame(0, 1)
	long.Payload = make([]byte, media.PCM16Mono8k().BytesFor(2*time.Second))
	for name, fr := range map[string]media.Frame{"rate": wide, "partial sample": odd, "empty": emptyFrame, "too long": long} {
		if err := s.Write(fr); !errors.Is(err, speech.ErrInvalidAudio) {
			t.Fatalf("%s: Write = %v, want ErrInvalidAudio", name, err)
		}
	}
}

func TestWriteAfterCloseSendOrCloseIsRefused(t *testing.T) {
	s := open(t, newProvider(t, newFake(t)))
	if err := s.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseSend(); err != nil {
		t.Fatalf("a second CloseSend: %v", err)
	}
	if err := s.Write(frame(0, 1)); !errors.Is(err, speech.ErrSpeechSessionClosed) {
		t.Fatalf("Write after CloseSend = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(frame(1, 1)); !errors.Is(err, speech.ErrSpeechSessionClosed) {
		t.Fatalf("Write after Close = %v", err)
	}
}

func TestWriteAfterTheStreamEndedIsRefused(t *testing.T) {
	f := newFake(t)
	f.failAfter, f.failCode = 1, websocket.StatusInternalError
	s := open(t, newProvider(t, f))
	if err := s.Write(frame(0, 1)); err != nil {
		t.Fatal(err)
	}
	collect(t, s)
	if err := s.Write(frame(1, 1)); !errors.Is(err, speech.ErrSpeechSessionClosed) {
		t.Fatalf("Write after the end = %v", err)
	}
	_ = s.Close() //nolint:errcheck // the failure is checked elsewhere
}

// When the server stops reading, the socket's buffers fill, the sender
// blocks, and the queue fills: Write must then refuse, not wait.
func TestWriteIsBackpressureNotBlocking(t *testing.T) {
	f := newFake(t)
	f.stall = true
	cfg := testConfig(t, f)
	cfg.MaxPendingFrames = 2
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sc := sttConfig()
	sc.Format = media.PCM16Mono16k()
	st, err := p.OpenSTT(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	s := st.(*stream) //nolint:forcetypeassert // the adapter's own type
	fr := media.Frame{Format: sc.Format, Payload: make([]byte, sc.Format.BytesFor(time.Second))}
	for i := range 4000 { // up to 128 MB: far beyond any loopback buffer
		done := make(chan error, 1)
		go func() { done <- s.Write(fr) }()
		select {
		case err := <-done:
			if errors.Is(err, speech.ErrBackpressure) {
				if cerr := s.Close(); cerr != nil {
					t.Fatalf("Close: %v", cerr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Write %d: %v", i, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("Write %d blocked: it must return ErrBackpressure instead", i)
		}
	}
	t.Fatal("Write never reported backpressure")
}

func TestCloseIsIdempotentAndPromptWhileTheServerIsSilent(t *testing.T) {
	f := newFake(t)
	f.silent = true
	s := open(t, newProvider(t, f))
	if err := s.Write(frame(0, 1)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := s.Close(); err != nil {
		t.Fatalf("a caller's Close is not a failure: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Close took %v", d)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("a second Close: %v", err)
	}
	if _, ok := <-s.Results(); ok {
		t.Fatal("Results is open after Close")
	}
	if s.Err() != nil {
		t.Fatalf("Err after a caller's Close = %v", s.Err())
	}
}

func TestConcurrentCloseIsSafe(t *testing.T) {
	s := open(t, newProvider(t, newFake(t)))
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.Write(frame(0, 1)) //nolint:errcheck // racing Close on purpose
			_ = s.CloseSend()        //nolint:errcheck // as above
			_ = s.Close()            //nolint:errcheck // as above
		}()
	}
	wg.Wait()
	if _, ok := <-s.Results(); ok {
		t.Fatal("Results is open after Close")
	}
}

func TestNoGoroutineLeaks(t *testing.T) {
	f := newFake(t)
	p := newProvider(t, f)
	before := runtime.NumGoroutine()

	for range 5 {
		s := open(t, p) // a full, clean stream
		if err := s.Write(frame(0, 1)); err != nil {
			t.Fatal(err)
		}
		if err := s.CloseSend(); err != nil {
			t.Fatal(err)
		}
		collect(t, s)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}

		s = open(t, p) // abandoned mid-stream
		if err := s.Write(frame(0, 1)); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}

	f.srv.Client().CloseIdleConnections()
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := runtime.NumGoroutine()
		if n <= before+2 {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("goroutines: %d before, %d after\n%s", before, n, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// Security.
// ---------------------------------------------------------------------------

func TestTheKeyFileIsRequiredAbsoluteAndSingle(t *testing.T) {
	f := newFake(t)
	for name, path := range map[string]string{
		"missing":   "",
		"relative":  "deepgram-api-key",
		"absent":    filepath.Join(t.TempDir(), "absent"),
		"empty":     keyFile(t, "  \n"),
		"two":       keyFile(t, testKey+" "+testKey),
		"two lines": keyFile(t, testKey+"\n"+testKey),
	} {
		cfg := testConfig(t, f)
		cfg.APIKeyFile = path
		_, err := New(cfg)
		if err == nil {
			t.Fatalf("%s: New accepted it", name)
		}
		assertNoLeak(t, err)
	}
}

func TestTheConfigurationIsValidated(t *testing.T) {
	f := newFake(t)
	for name, mutate := range map[string]func(*Config){
		"no id":          func(c *Config) { c.ID = "" },
		"bad id":         func(c *Config) { c.ID = "Deepgram Test" },
		"no languages":   func(c *Config) { c.Languages = nil },
		"bad language":   func(c *Config) { c.Languages = []speech.Language{"en IN"} },
		"bad model":      func(c *Config) { c.Model = "nova 3&x=y" },
		"negative bound": func(c *Config) { c.MaxPendingFrames = -1 },
		"negative time":  func(c *Config) { c.ResultTimeout = -time.Second },
	} {
		cfg := testConfig(t, f)
		mutate(&cfg)
		_, err := New(cfg)
		if err == nil {
			t.Fatalf("%s: New accepted it", name)
		}
		assertNoLeak(t, err)
	}
	cfg := testConfig(t, f)
	cfg.Model, cfg.MaxPendingFrames, cfg.OpenTimeout, cfg.ResultTimeout, cfg.MaxMessageBytes = "", 0, 0, 0, 0
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("zero values take the defaults: %v", err)
	}
	if p.cfg.Model != DefaultModel || p.cfg.MaxPendingFrames <= 0 || p.cfg.ResultTimeout <= 0 {
		t.Fatalf("defaults not applied: %+v", p.cfg)
	}
}

func TestTheHostIsPinned(t *testing.T) {
	key := keyFile(t, testKey)
	build := func(base string) error {
		_, err := New(Config{
			ID: "deepgram-test", APIKeyFile: key, BaseURL: base,
			Languages: []speech.Language{speech.LangEnglishIN},
		})
		return err
	}
	for _, ok := range []string{
		"", // DefaultBaseURL
		"wss://api.deepgram.com",
		"wss://api.deepgram.com/",
		"wss://api.deepgram.com:443",
		"wss://127.0.0.1:8443",
		"ws://127.0.0.1:8080",
		"ws://localhost:8080",
		"wss://[::1]:8443",
	} {
		if err := build(ok); err != nil {
			t.Fatalf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"wss://evil.example",
		"wss://api.deepgram.com.evil.example",
		"wss://evil.example/api.deepgram.com",
		"wss://api.deepgram.com@evil.example",
		"wss://user:pass@api.deepgram.com",
		"https://api.deepgram.com",
		"http://api.deepgram.com",
		"ws://api.deepgram.com",
		"wss://api.deepgram.com:8443",
		"wss://api.deepgram.com/v1/listen",
		"wss://api.deepgram.com?x=1",
		"wss://api.deepgram.com#x",
		"wss:api.deepgram.com",
		"https://127.0.0.1:8443",
		"http://127.0.0.1:8080",
		"wss://10.0.0.1",
		"api.deepgram.com",
		"::not a url",
	} {
		err := build(bad)
		if err == nil {
			t.Fatalf("%q accepted: the key would be sent there", bad)
		}
		assertNoLeak(t, err)
	}
}

func TestARedirectAtUpgradeIsNotFollowed(t *testing.T) {
	var mu sync.Mutex
	reached := 0
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		reached++
		mu.Unlock()
		w.WriteHeader(http.StatusTeapot)
	}))
	defer target.Close()
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+r.URL.RequestURI(), status)
		}))
		cfg := testConfig(t, newFake(t))
		cfg.BaseURL, cfg.HTTPClient = "wss"+strings.TrimPrefix(redirector.URL, "https"), redirector.Client()
		p, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.OpenSTT(context.Background(), sttConfig())
		redirector.Close()
		if !errors.Is(err, speech.ErrProviderUnavailable) || !strings.Contains(err.Error(), "redirect") {
			t.Fatalf("%d: OpenSTT = %v, want a refused redirect", status, err)
		}
		assertNoLeak(t, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if reached != 0 {
		t.Fatalf("the redirect target received %d requests", reached)
	}
}

func TestTheProvidersHTTPClientIsNotMutated(t *testing.T) {
	f := newFake(t)
	client := f.srv.Client()
	cfg := testConfig(t, f)
	cfg.HTTPClient = client
	if _, err := New(cfg); err != nil {
		t.Fatal(err)
	}
	if client.CheckRedirect != nil {
		t.Fatal("New changed the caller's client")
	}
}
