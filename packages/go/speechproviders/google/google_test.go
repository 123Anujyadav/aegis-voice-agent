package google

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/speech/apiv2/speechpb"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	"github.com/callscreen/callscreen-platform/packages/go/providerconformance"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
)

// An in-process stand-in for Google's Speech v2 service on a bufconn
// listener: nothing here touches a network, and every provider is built with
// option.WithGRPCConn and option.WithoutAuthentication, so no credential is
// ever looked up.

// secret is what a hostile or chatty status message might echo. No error the
// adapter returns may contain it.
const secret = "the caller said: my PIN is 4321"

// handler scripts one StreamingRecognize call.
type handler func(rec *recording, srv speechpb.Speech_StreamingRecognizeServer) error

type fake struct {
	speechpb.UnimplementedSpeechServer

	lis    *bufconn.Listener
	handle handler

	mu   sync.Mutex
	recs []*recording
}

// recording is what the server saw on one call.
type recording struct {
	mu         sync.Mutex
	first      *speechpb.StreamingRecognizeRequest
	audio      [][]byte
	halfClosed bool
	sawAudio   chan struct{}
	sawOnce    sync.Once
}

func (r *recording) snapshot() (*speechpb.StreamingRecognizeRequest, [][]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.first, append([][]byte(nil), r.audio...), r.halfClosed
}

func newFake(t *testing.T, h handler) *fake {
	t.Helper()
	f := &fake{lis: bufconn.Listen(1 << 20), handle: h}
	srv := grpc.NewServer()
	speechpb.RegisterSpeechServer(srv, f)
	go func() { _ = srv.Serve(f.lis) }() //nolint:errcheck // ends with Stop
	t.Cleanup(srv.Stop)
	return f
}

func (f *fake) StreamingRecognize(srv speechpb.Speech_StreamingRecognizeServer) error {
	rec := &recording{sawAudio: make(chan struct{})}
	f.mu.Lock()
	f.recs = append(f.recs, rec)
	h := f.handle
	f.mu.Unlock()
	return h(rec, srv)
}

func (f *fake) calls() []*recording {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*recording(nil), f.recs...)
}

// readConfig records the first message.
func readConfig(rec *recording, srv speechpb.Speech_StreamingRecognizeServer) error {
	req, err := srv.Recv()
	if err != nil {
		return err
	}
	rec.mu.Lock()
	rec.first = proto.CloneOf(req)
	rec.mu.Unlock()
	return nil
}

// readAudio records audio until the client half-closes.
func readAudio(rec *recording, srv speechpb.Speech_StreamingRecognizeServer) error {
	for {
		req, err := srv.Recv()
		if errors.Is(err, io.EOF) {
			rec.mu.Lock()
			rec.halfClosed = true
			rec.mu.Unlock()
			return nil
		}
		if err != nil {
			return err
		}
		rec.mu.Lock()
		rec.audio = append(rec.audio, append([]byte(nil), req.GetAudio()...))
		rec.mu.Unlock()
		rec.sawOnce.Do(func() { close(rec.sawAudio) })
	}
}

func result(text string, final bool, confidence float32, end time.Duration, lang string) *speechpb.StreamingRecognitionResult {
	r := &speechpb.StreamingRecognitionResult{IsFinal: final, ResultEndOffset: durationpb.New(end), LanguageCode: lang}
	if text != "\x00" {
		r.Alternatives = []*speechpb.SpeechRecognitionAlternative{{Transcript: text, Confidence: confidence}}
	}
	return r
}

// echoFinal answers the whole audio with one final result.
func echoFinal(rec *recording, srv speechpb.Speech_StreamingRecognizeServer) error {
	if err := readConfig(rec, srv); err != nil {
		return err
	}
	if err := readAudio(rec, srv); err != nil {
		return err
	}
	return srv.Send(&speechpb.StreamingRecognizeResponse{
		Results: []*speechpb.StreamingRecognitionResult{result("hello", true, 0.9, time.Second, "en-in")},
	})
}

// block reads nothing and waits for the client to go away.
func block(_ *recording, srv speechpb.Speech_StreamingRecognizeServer) error {
	<-srv.Context().Done()
	return srv.Context().Err()
}

const testProject = "aegis-test-project"

func newProvider(t *testing.T, f *fake, edit func(*Config)) *Provider {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return f.lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.ID = "google-stt-test"
	cfg.Project = testProject
	cfg.Languages = []speech.Language{speech.LangEnglishIN, speech.LangHindi}
	cfg.ClientOptions = []option.ClientOption{option.WithGRPCConn(conn), option.WithoutAuthentication()}
	cfg.ResultTimeout = 2 * time.Second
	if edit != nil {
		edit(&cfg)
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = p.Close()    //nolint:errcheck // also closes conn
		_ = conn.Close() //nolint:errcheck // already closed by the client
	})
	return p
}

func sttConfig(format media.AudioFormat, lang speech.Language) speech.STTConfig {
	return speech.STTConfig{Session: speech.NewSessionID(), Turn: speech.NewTurnID(), Language: lang, Format: format}
}

func frame(format media.AudioFormat, seq uint64, fill byte) media.Frame {
	return media.Frame{
		Sequence: seq, Timestamp: time.Duration(seq) * 20 * time.Millisecond, Format: format,
		Payload: bytes.Repeat([]byte{fill}, format.BytesFor(20*time.Millisecond)),
	}
}

func open(t *testing.T, p *Provider, cfg speech.STTConfig) *stream {
	t.Helper()
	st, err := p.OpenSTT(context.Background(), cfg)
	if err != nil {
		t.Fatalf("OpenSTT: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() }) //nolint:errcheck // checked where it matters
	return st.(*stream)                  //nolint:forcetypeassert // the adapter's own type
}

// collect reads Results until it closes.
func collect(t *testing.T, st *stream, d time.Duration) []speech.TranscriptSegment {
	t.Helper()
	var got []speech.TranscriptSegment
	deadline := time.After(d)
	for {
		select {
		case seg, ok := <-st.Results():
			if !ok {
				return got
			}
			got = append(got, seg)
		case <-deadline:
			t.Fatalf("Results did not close within %v", d)
		}
	}
}

// writeWithin calls Write and fails the test if it blocks.
func writeWithin(t *testing.T, st *stream, f media.Frame, d time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- st.Write(f) }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		t.Fatalf("Write blocked for more than %v", d)
		return nil
	}
}

func TestTheAdapterConformsToThePort(t *testing.T) {
	f := newFake(t, echoFinal)
	providerconformance.RunSTT(t, providerconformance.STTSubject{
		New:    func(t *testing.T) speech.STTProvider { return newProvider(t, f, nil) },
		Config: func() speech.STTConfig { return sttConfig(media.PCM16Mono8k(), speech.LangEnglishIN) },
	})
}

func TestRequest_ShapeFollowsADR0005(t *testing.T) {
	for _, tc := range []struct {
		format media.AudioFormat
		hz     int32
		lang   speech.Language
	}{
		{media.PCM16Mono8k(), 8000, speech.LangEnglishIN},
		{media.PCM16Mono16k(), 16000, speech.LangHindi},
	} {
		t.Run(tc.format.String(), func(t *testing.T) {
			f := newFake(t, echoFinal)
			p := newProvider(t, f, nil)
			st := open(t, p, sttConfig(tc.format, tc.lang))
			var sent [][]byte
			for i := range 3 {
				fr := frame(tc.format, uint64(i), byte(i+1))
				sent = append(sent, fr.Payload)
				if err := writeWithin(t, st, fr, time.Second); err != nil {
					t.Fatalf("Write %d: %v", i, err)
				}
			}
			if err := st.CloseSend(); err != nil {
				t.Fatal(err)
			}
			collect(t, st, 5*time.Second)
			if err := st.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			calls := f.calls()
			if len(calls) != 1 {
				t.Fatalf("%d calls, want 1", len(calls))
			}
			first, audio, halfClosed := calls[0].snapshot()
			if got, want := first.GetRecognizer(), "projects/"+testProject+"/locations/asia-south1/recognizers/_"; got != want {
				t.Errorf("recognizer %q, want %q", got, want)
			}
			if first.GetAudio() != nil {
				t.Error("the first message carries audio")
			}
			sc := first.GetStreamingConfig()
			rc := sc.GetConfig()
			dec := rc.GetExplicitDecodingConfig()
			switch {
			case rc.GetModel() != "telephony":
				t.Errorf("model %q", rc.GetModel())
			case dec.GetEncoding() != speechpb.ExplicitDecodingConfig_LINEAR16:
				t.Errorf("encoding %v", dec.GetEncoding())
			case dec.GetSampleRateHertz() != tc.hz:
				t.Errorf("rate %d, want %d", dec.GetSampleRateHertz(), tc.hz)
			case dec.GetAudioChannelCount() != 1:
				t.Errorf("channels %d", dec.GetAudioChannelCount())
			case len(rc.GetLanguageCodes()) != 1 || rc.GetLanguageCodes()[0] != string(tc.lang):
				t.Errorf("languages %v", rc.GetLanguageCodes())
			case !rc.GetFeatures().GetEnableAutomaticPunctuation():
				t.Error("punctuation is off")
			case !sc.GetStreamingFeatures().GetInterimResults():
				t.Error("interim results are off")
			}
			// ADR-0005: endpointing is ours.
			sf := sc.GetStreamingFeatures()
			if sf.GetEnableVoiceActivityEvents() || sf.GetVoiceActivityTimeout() != nil ||
				sf.GetEndpointingSensitivity() != speechpb.StreamingRecognitionFeatures_ENDPOINTING_SENSITIVITY_UNSPECIFIED {
				t.Errorf("vendor endpointing is configured: %v", sf)
			}
			if len(audio) != len(sent) {
				t.Fatalf("server saw %d audio messages, want %d", len(audio), len(sent))
			}
			for i := range sent {
				if !bytes.Equal(audio[i], sent[i]) {
					t.Errorf("audio message %d differs", i)
				}
			}
			if !halfClosed {
				t.Error("the server never saw CloseSend")
			}
		})
	}
}

func TestRequest_AFrameLargerThanTheRequestLimitIsSplit(t *testing.T) {
	f := newFake(t, echoFinal)
	p := newProvider(t, f, nil)
	format := media.PCM16Mono16k()
	st := open(t, p, sttConfig(format, speech.LangEnglishIN))
	big := make([]byte, format.BytesFor(time.Second)) // 32000 bytes
	for i := range big {
		big[i] = byte(i)
	}
	if err := st.Write(media.Frame{Format: format, Payload: big}); err != nil {
		t.Fatal(err)
	}
	_ = st.CloseSend() //nolint:errcheck // cannot fail here
	collect(t, st, 5*time.Second)
	_, audio, _ := f.calls()[0].snapshot()
	var joined []byte
	for _, a := range audio {
		if len(a) > maxAudioPerRequest {
			t.Errorf("a request carries %d bytes, over the 15 KB limit", len(a))
		}
		joined = append(joined, a...)
	}
	if len(audio) != 3 || !bytes.Equal(joined, big) {
		t.Errorf("got %d requests; reassembled audio equal: %v", len(audio), bytes.Equal(joined, big))
	}
}

func TestResults_InterimThenFinalWithEmptyTranscriptsSkipped(t *testing.T) {
	f := newFake(t, func(rec *recording, srv speechpb.Speech_StreamingRecognizeServer) error {
		if err := readConfig(rec, srv); err != nil {
			return err
		}
		if err := readAudio(rec, srv); err != nil {
			return err
		}
		for _, results := range [][]*speechpb.StreamingRecognitionResult{
			{result("hel", false, 0, 400*time.Millisecond, "")},
			{result("   ", false, 0, 500*time.Millisecond, ""), result("\x00", false, 0, 0, "")},
			{result("hello world", true, 0.92, 1200*time.Millisecond, "en-in"), result("and", false, 0, 1300*time.Millisecond, "")},
		} {
			if err := srv.Send(&speechpb.StreamingRecognizeResponse{Results: results}); err != nil {
				return err
			}
		}
		return nil
	})
	p := newProvider(t, f, nil)
	cfg := sttConfig(media.PCM16Mono8k(), speech.LangEnglishIN)
	st := open(t, p, cfg)
	if err := st.Write(frame(cfg.Format, 0, 1)); err != nil {
		t.Fatal(err)
	}
	_ = st.CloseSend() //nolint:errcheck // cannot fail here
	got := collect(t, st, 5*time.Second)
	if err := st.Err(); err != nil {
		t.Fatalf("Err: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("%d segments, want 3 (empty transcripts skipped)", len(got))
	}
	want := []struct {
		text       string
		final      bool
		confidence float64
		start, end time.Duration
	}{
		{"hel", false, speech.ConfidenceUnknown, 0, 400 * time.Millisecond},
		{"hello world", true, float64(float32(0.92)), 0, 1200 * time.Millisecond},
		{"and", false, speech.ConfidenceUnknown, 1200 * time.Millisecond, 1300 * time.Millisecond},
	}
	for i, w := range want {
		g := got[i]
		if g.Text != w.text || g.IsFinal != w.final || g.Confidence != w.confidence || g.StartTime != w.start || g.EndTime != w.end {
			t.Errorf("segment %d = %q final=%v conf=%v %v-%v, want %+v", i, g.Text, g.IsFinal, g.Confidence, g.StartTime, g.EndTime, w)
		}
		if g.Sequence != uint64(i+1) || g.Session != cfg.Session || g.Turn != cfg.Turn || g.Role != speech.RoleCaller {
			t.Errorf("segment %d identity: seq=%d session=%s turn=%s role=%s", i, g.Sequence, g.Session, g.Turn, g.Role)
		}
		if g.Language != speech.LangEnglishIN || g.Meta.Provider != "google-stt-test" || g.Meta.Model != "telephony" {
			t.Errorf("segment %d language=%s meta=%+v", i, g.Language, g.Meta)
		}
		if err := g.Validate(); err != nil {
			t.Errorf("segment %d invalid: %v", i, err)
		}
	}
}

// failAfterAudio waits for the first audio, then ends the call with code.
func failAfterAudio(code codes.Code) handler {
	return func(rec *recording, srv speechpb.Speech_StreamingRecognizeServer) error {
		if err := readConfig(rec, srv); err != nil {
			return err
		}
		go func() { _ = readAudio(rec, srv) }() //nolint:errcheck // ends with the call
		select {
		case <-rec.sawAudio:
		case <-srv.Context().Done():
			return srv.Context().Err()
		}
		return status.Error(code, secret)
	}
}

func TestErrors_StatusCodesMapWithoutQuotingTheMessage(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want error
	}{
		{codes.Unauthenticated, speech.ErrProviderUnavailable},
		{codes.PermissionDenied, speech.ErrProviderUnavailable},
		{codes.ResourceExhausted, speech.ErrProviderRateLimited},
		{codes.DeadlineExceeded, speech.ErrProviderTimeout},
		{codes.Unavailable, speech.ErrProviderUnavailable},
		{codes.InvalidArgument, speech.ErrInternalFailure},
		{codes.NotFound, speech.ErrInternalFailure},
		{codes.Internal, speech.ErrProviderUnavailable},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			f := newFake(t, failAfterAudio(tc.code))
			p := newProvider(t, f, nil)
			cfg := sttConfig(media.PCM16Mono8k(), speech.LangEnglishIN)
			st := open(t, p, cfg)
			if err := st.Write(frame(cfg.Format, 0, 1)); err != nil {
				t.Fatal(err)
			}
			collect(t, st, 5*time.Second) // Results closes on the failure
			err := st.Err()
			if !errors.Is(err, tc.want) {
				t.Fatalf("Err = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "4321") || strings.Contains(err.Error(), "caller said") {
				t.Fatalf("the error quotes Google's status message: %v", err)
			}
			if !strings.Contains(err.Error(), tc.code.String()) {
				t.Errorf("the error does not name the code: %v", err)
			}
			if werr := st.Write(frame(cfg.Format, 1, 1)); !errors.Is(werr, tc.want) {
				t.Errorf("Write after the failure = %v, want %v", werr, tc.want)
			}
			if cerr := st.Close(); !errors.Is(cerr, tc.want) {
				t.Errorf("Close = %v, want the recorded failure", cerr)
			}
		})
	}
}

func TestResultTimeout_AServerSilentAfterCloseSendFails(t *testing.T) {
	f := newFake(t, func(rec *recording, srv speechpb.Speech_StreamingRecognizeServer) error {
		if err := readConfig(rec, srv); err != nil {
			return err
		}
		if err := readAudio(rec, srv); err != nil {
			return err
		}
		<-srv.Context().Done() // never answers, never ends
		return nil
	})
	p := newProvider(t, f, func(c *Config) { c.ResultTimeout = 100 * time.Millisecond })
	cfg := sttConfig(media.PCM16Mono8k(), speech.LangEnglishIN)
	st := open(t, p, cfg)
	if err := st.Write(frame(cfg.Format, 0, 1)); err != nil {
		t.Fatal(err)
	}
	_ = st.CloseSend() //nolint:errcheck // cannot fail here
	collect(t, st, 5*time.Second)
	if err := st.Err(); !errors.Is(err, speech.ErrProviderTimeout) {
		t.Fatalf("Err = %v, want ErrProviderTimeout", err)
	}
}

func TestTimeout_TheStreamTimeoutIsAProviderTimeout(t *testing.T) {
	f := newFake(t, block)
	p := newProvider(t, f, nil)
	cfg := sttConfig(media.PCM16Mono8k(), speech.LangEnglishIN)
	cfg.Timeout = 100 * time.Millisecond
	st := open(t, p, cfg)
	collect(t, st, 5*time.Second)
	if err := st.Err(); !errors.Is(err, speech.ErrProviderTimeout) {
		t.Fatalf("Err = %v, want ErrProviderTimeout", err)
	}
}

func TestWrite_NeverBlocksAndAFullQueueIsBackpressure(t *testing.T) {
	f := newFake(t, block) // reads nothing: flow control stalls the sender
	p := newProvider(t, f, func(c *Config) { c.MaxPendingFrames = 2 })
	format := media.PCM16Mono16k()
	// Opened without open's Close cleanup: a Write that blocks may hold the
	// stream's lock, and a Close waiting on it would turn this failure into a
	// hang. The provider's cleanup still ends the call.
	s, err := p.OpenSTT(context.Background(), sttConfig(format, speech.LangEnglishIN))
	if err != nil {
		t.Fatal(err)
	}
	big := make([]byte, maxAudioPerRequest)
	for i := range 500 {
		done := make(chan error, 1)
		go func() { done <- s.Write(media.Frame{Sequence: uint64(i), Format: format, Payload: big}) }()
		select {
		case err := <-done:
			if errors.Is(err, speech.ErrBackpressure) {
				if cerr := s.Close(); cerr != nil {
					t.Errorf("Close: %v", cerr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Write %d: %v", i, err)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("Write %d blocked for more than 500ms on a stalled stream", i)
		}
	}
	t.Fatal("500 writes into a stalled stream never reported ErrBackpressure")
}

func TestWrite_AfterCloseSendOrCloseIsRefused(t *testing.T) {
	f := newFake(t, echoFinal)
	p := newProvider(t, f, nil)
	cfg := sttConfig(media.PCM16Mono8k(), speech.LangEnglishIN)
	st := open(t, p, cfg)
	if err := st.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if err := st.CloseSend(); err != nil {
		t.Errorf("a second CloseSend: %v", err)
	}
	if err := st.Write(frame(cfg.Format, 0, 1)); !errors.Is(err, speech.ErrSpeechSessionClosed) {
		t.Errorf("Write after CloseSend = %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := st.Write(frame(cfg.Format, 1, 1)); !errors.Is(err, speech.ErrSpeechSessionClosed) {
		t.Errorf("Write after Close = %v", err)
	}
	if err := st.CloseSend(); !errors.Is(err, speech.ErrSpeechSessionClosed) {
		t.Errorf("CloseSend after Close = %v", err)
	}
}

func TestWrite_AFrameInTheWrongFormatIsInvalidAudio(t *testing.T) {
	f := newFake(t, echoFinal)
	p := newProvider(t, f, nil)
	cfg := sttConfig(media.PCM16Mono8k(), speech.LangEnglishIN)
	st := open(t, p, cfg)
	if err := st.Write(frame(media.PCM16Mono16k(), 0, 1)); !errors.Is(err, speech.ErrInvalidAudio) {
		t.Errorf("a 16 kHz frame on an 8 kHz stream = %v", err)
	}
	if err := st.Write(media.Frame{Format: cfg.Format, Payload: []byte{1, 2, 3}}); !errors.Is(err, speech.ErrInvalidAudio) {
		t.Errorf("a partial sample = %v", err)
	}
	if err := st.Write(media.Frame{Format: cfg.Format}); !errors.Is(err, speech.ErrInvalidAudio) {
		t.Errorf("an empty frame = %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("a refused frame is not a stream failure: %v", err)
	}
}

func TestOpen_RefusesWhatTheProviderCannotDo(t *testing.T) {
	f := newFake(t, echoFinal)
	p := newProvider(t, f, nil)
	stereo := media.PCM16Mono8k()
	stereo.Layout = media.LayoutStereo
	pcm32 := media.PCM16Mono16k()
	pcm32.Format = media.FormatPCM32
	opaque := media.PCM16Mono8k()
	opaque.Codec = media.CodecOpaque
	rate441 := media.PCM16Mono8k()
	rate441.Rate = 44100

	cases := []struct {
		name string
		cfg  speech.STTConfig
		want error
	}{
		{"undeclared language", sttConfig(media.PCM16Mono8k(), speech.LangTamil), speech.ErrUnsupportedLanguage},
		{"stereo", sttConfig(stereo, speech.LangEnglishIN), speech.ErrInvalidAudio},
		{"pcm32", sttConfig(pcm32, speech.LangEnglishIN), speech.ErrInvalidAudio},
		{"opaque codec", sttConfig(opaque, speech.LangEnglishIN), speech.ErrInvalidAudio},
		{"44.1 kHz", sttConfig(rate441, speech.LangEnglishIN), speech.ErrInvalidAudio},
	}
	for _, tc := range cases {
		if _, err := p.OpenSTT(context.Background(), tc.cfg); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	bad := sttConfig(media.PCM16Mono8k(), speech.LangEnglishIN)
	bad.Session = "Not Valid"
	var cfgErr *speech.ConfigError
	if _, err := p.OpenSTT(context.Background(), bad); !errors.As(err, &cfgErr) {
		t.Errorf("an invalid STTConfig = %v", err)
	}
	if n := len(f.calls()); n != 0 {
		t.Errorf("%d refused opens reached the server", n)
	}
}

func validConfig() Config {
	cfg := DefaultConfig()
	cfg.ID = "google-stt"
	cfg.Project = testProject
	cfg.Languages = []speech.Language{speech.LangEnglishIN}
	return cfg
}

func TestNew_ResidencyAndConfiguration(t *testing.T) {
	p, err := New(Config{ID: "google-stt", Project: testProject, Languages: []speech.Language{speech.LangHindi}})
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if got := p.Recognizer(); got != "projects/"+testProject+"/locations/asia-south1/recognizers/_" {
		t.Errorf("default recognizer %q", got)
	}
	if p.cfg.Model != "telephony" || p.cfg.Endpoint != "asia-south1-speech.googleapis.com:443" {
		t.Errorf("defaults: model %q endpoint %q", p.cfg.Model, p.cfg.Endpoint)
	}
	if _, err := New(Config{ID: "google-stt", Project: "1234567890", Languages: []speech.Language{speech.LangHindi}}); err != nil {
		t.Errorf("a project number: %v", err)
	}

	for name, edit := range map[string]func(*Config){
		"global location":       func(c *Config) { c.Location = "global" },
		"another region":        func(c *Config) { c.Location = "us-central1" },
		"another Indian region": func(c *Config) { c.Location = "asia-south2" },
		"no project":            func(c *Config) { c.Project = "" },
		"a project path":        func(c *Config) { c.Project = "p/locations/x" },
		"a recognizer path":     func(c *Config) { c.Recognizer = "../x" },
		"no languages":          func(c *Config) { c.Languages = nil },
		"a malformed language":  func(c *Config) { c.Languages = []speech.Language{"en IN"} },
		"no id":                 func(c *Config) { c.ID = "" },
		"a malformed model":     func(c *Config) { c.Model = "telephony/../x" },
		"a negative bound":      func(c *Config) { c.MaxPendingFrames = -1 },
	} {
		cfg := validConfig()
		edit(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(name, "region") || strings.Contains(name, "location") {
			if !strings.Contains(err.Error(), "ADR-0005") {
				t.Errorf("%s: the refusal does not cite ADR-0005: %v", name, err)
			}
		}
	}
}

func TestEndpoint_AudioGoesOnlyToGoogleAPIsOrLoopback(t *testing.T) {
	for _, ep := range []string{
		"asia-south1-speech.googleapis.com:443", "speech.googleapis.com:443",
		"localhost:8443", "127.0.0.1:1", "[::1]:443",
	} {
		cfg := validConfig()
		cfg.Endpoint = ep
		if _, err := New(cfg); err != nil {
			t.Errorf("%s refused: %v", ep, err)
		}
	}
	for _, ep := range []string{
		"evil.example.com:443", "speech.googleapis.com.evil.com:443", "evil.com/x.googleapis.com:443",
		"speech.googleapis.com:80", "speech.googleapis.com", "https://speech.googleapis.com:443",
		"user@speech.googleapis.com:443", "googleapis.com:443", "SPEECH.GOOGLEAPIS.COM:443", "10.0.0.1:443",
	} {
		cfg := validConfig()
		cfg.Endpoint = ep
		if _, err := New(cfg); err == nil {
			t.Errorf("%s accepted", ep)
		}
	}
}

func TestCredentials_NewReadsNothingAndTheDefaultChainIsUsedOnlyLazily(t *testing.T) {
	// A credentials file that does not exist: if anything consults Google's
	// default chain, it fails on this file before any network is touched.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", t.TempDir()+"/absent.json")
	cfg := validConfig()
	cfg.Endpoint = "127.0.0.1:1" // loopback, in case anything dialled
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New consulted credentials: %v", err)
	}
	defer p.Close() //nolint:errcheck // nothing to release
	_, err = p.OpenSTT(context.Background(), sttConfig(media.PCM16Mono8k(), speech.LangEnglishIN))
	if !errors.Is(err, speech.ErrProviderUnavailable) {
		t.Fatalf("OpenSTT with unusable default credentials = %v, want ErrProviderUnavailable", err)
	}
	if !strings.Contains(err.Error(), "creating the client") || !strings.Contains(err.Error(), "absent.json") {
		t.Fatalf("the failure is not the default chain reading the configured file: %v", err)
	}
}

func TestClose_IsIdempotentAndPromptWhileTheServerBlocks(t *testing.T) {
	f := newFake(t, block)
	p := newProvider(t, f, nil)
	cfg := sttConfig(media.PCM16Mono8k(), speech.LangEnglishIN)
	st := open(t, p, cfg)
	if err := st.Write(frame(cfg.Format, 0, 1)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- st.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close: %v (a caller's Close is not a failure)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked while the server held the stream")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("Close took %v", d)
	}
	if err := st.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if _, ok := <-st.Results(); ok {
		t.Error("Results delivered after Close")
	}
}

func TestCancel_TheCallersContextIsNotAProviderFailure(t *testing.T) {
	f := newFake(t, block)
	p := newProvider(t, f, nil)
	cfg := sttConfig(media.PCM16Mono8k(), speech.LangEnglishIN)
	ctx, cancel := context.WithCancel(context.Background())
	s, err := p.OpenSTT(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := s.(*stream) //nolint:forcetypeassert // the adapter's own type
	if err := st.Write(frame(cfg.Format, 0, 1)); err != nil {
		t.Fatal(err)
	}
	cancel()
	collect(t, st, 5*time.Second)
	if err := st.Err(); err != nil {
		t.Errorf("Err after the caller cancelled = %v", err)
	}
	if err := st.Close(); err != nil {
		t.Errorf("Close after the caller cancelled = %v", err)
	}
}

func TestProvider_CloseRefusesFurtherStreams(t *testing.T) {
	f := newFake(t, echoFinal)
	p := newProvider(t, f, nil)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if _, err := p.OpenSTT(context.Background(), sttConfig(media.PCM16Mono8k(), speech.LangEnglishIN)); !errors.Is(err, speech.ErrProviderUnavailable) {
		t.Errorf("OpenSTT after Close = %v", err)
	}
}

func TestGoroutines_StreamsAndProvidersLeaveNothingBehind(t *testing.T) {
	f := newFake(t, echoFinal)
	cycle := func() {
		p := newProvider(t, f, nil)
		cfg := sttConfig(media.PCM16Mono8k(), speech.LangEnglishIN)
		for i := range 3 {
			s, err := p.OpenSTT(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			_ = s.Write(frame(cfg.Format, 0, 1)) //nolint:errcheck // not under test
			if i%2 == 0 {
				_ = s.CloseSend() //nolint:errcheck // not under test
			}
			_ = s.Close() //nolint:errcheck // not under test
		}
		_ = p.Close() //nolint:errcheck // not under test
	}
	cycle() // warm up the server's and gRPC's long-lived goroutines
	runtime.GC()
	base := runtime.NumGoroutine()
	for range 10 {
		cycle()
	}
	const tolerance = 4
	deadline := time.Now().Add(3 * time.Second)
	for {
		n := runtime.NumGoroutine()
		if n <= base+tolerance {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("%d goroutines, %d before (tolerance %d)\n%s", n, base, tolerance, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(20 * time.Millisecond)
	}
}
