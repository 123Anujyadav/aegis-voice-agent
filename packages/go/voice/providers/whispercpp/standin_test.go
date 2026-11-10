package whispercpp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
	speech "github.com/callscreen/callscreen-platform/packages/go/speech"
)

// A STAND-IN ENGINE, so the adapter's process path runs for real: the child is
// started, fed PCM on stdin, read line by line, and reaped. It speaks
// whisper.cpp's output shape (banners plus "[from --> to]  text" lines). It is
// not whisper.cpp, and it proves nothing about recognition; it proves the
// adapter carries a transcript, a crash and a shutdown truthfully. No model, no
// network.

const standInSource = `package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func main() {
	mode := "transcribe"
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, "--mode=") {
			mode = strings.TrimPrefix(a, "--mode=")
		}
	}
	fmt.Println("whisper_init_from_file_with_params: loading model")
	if mode == "hang" {
		time.Sleep(time.Hour)
	}
	n, _ := io.Copy(io.Discard, os.Stdin)
	fmt.Printf("main: processed %d bytes\n", n)
	switch mode {
	case "transcribe":
		fmt.Println("[00:00:00.000 --> 00:00:01.000]  hello from the stand-in")
	case "env":
		fmt.Printf("[00:00:00.000 --> 00:00:01.000]  secret=%s\n", os.Getenv("WHISPERCPP_TEST_SECRET"))
	case "crash":
		fmt.Fprintln(os.Stderr, "error: failed to load model")
		os.Exit(3)
	}
}
`

var (
	standInOnce sync.Once
	standInPath string
	standInErr  error
)

func standIn(t *testing.T) string {
	t.Helper()
	standInOnce.Do(func() {
		dir, err := os.MkdirTemp("", "whispercpp-standin")
		if err != nil {
			standInErr = err
			return
		}
		src := filepath.Join(dir, "main.go")
		if err := os.WriteFile(src, []byte(standInSource), 0o600); err != nil {
			standInErr = err
			return
		}
		bin := filepath.Join(dir, "whisper-standin")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
			standInErr = fmt.Errorf("building the stand-in engine: %w\n%s", err, out)
			return
		}
		standInPath = bin
	})
	if standInErr != nil {
		t.Skipf("cannot build the stand-in engine: %v", standInErr)
	}
	return standInPath
}

// openStandIn opens a stream on the stand-in in the given mode.
func openStandIn(t *testing.T, mode string) *stream {
	t.Helper()
	cfg := testConfig(t)
	cfg.Executable = standIn(t)
	cfg.Process.Args = []string{"--mode=" + mode}
	cfg.Process.StartTimeout = 10 * time.Second
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := p.OpenSTT(context.Background(), speech.STTConfig{
		Session: "ss-standin", Turn: "st-standin", Language: "en", Format: media.PCM16Mono16k(),
	})
	if err != nil {
		t.Fatal(err)
	}
	s, ok := raw.(*stream)
	if !ok {
		t.Fatalf("OpenSTT returned %T", raw)
	}
	return s
}

// speakAndDrain writes a second of audio, ends it, and collects the results.
func speakAndDrain(t *testing.T, s *stream) []speech.TranscriptSegment {
	t.Helper()
	for i := 0; i < 50; i++ {
		if err := s.Write(media.Frame{Format: media.PCM16Mono16k(), Payload: make([]byte, 640)}); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := s.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}
	var segs []speech.TranscriptSegment
	deadline := time.After(10 * time.Second)
	for {
		select {
		case seg, ok := <-s.Results():
			if !ok {
				return segs
			}
			segs = append(segs, seg)
		case <-deadline:
			t.Fatal("Results did not close within ten seconds")
		}
	}
}

func TestStandIn_ATranscriptReachesTheCallerAndBannersDoNot(t *testing.T) {
	s := openStandIn(t, "transcribe")
	segs := speakAndDrain(t, s)
	if len(segs) != 1 || segs[0].Text != "hello from the stand-in" {
		t.Fatalf("segments = %+v, want exactly the one transcript line", segs)
	}
	if err := s.Err(); err != nil {
		t.Errorf("a clean run reported %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("a second Close: %v", err)
	}
}

func TestStandIn_SilenceIsNotAnError(t *testing.T) {
	s := openStandIn(t, "silent")
	if segs := speakAndDrain(t, s); len(segs) != 0 {
		t.Fatalf("silence produced %+v", segs)
	}
	if err := s.Err(); err != nil {
		t.Errorf("silence reported %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestStandIn_ACrashedEngineIsNotSilence(t *testing.T) {
	s := openStandIn(t, "crash")
	if segs := speakAndDrain(t, s); len(segs) != 0 {
		t.Fatalf("a crash produced %+v", segs)
	}
	err := s.Err()
	if err == nil {
		t.Fatal("an engine that exited 3 reported no error: a crash is indistinguishable from silence")
	}
	if !strings.Contains(err.Error(), "failed to load model") {
		t.Errorf("the error carries no stderr diagnostics: %v", err)
	}
	if cerr := s.Close(); cerr == nil {
		t.Error("Close returned nil after the engine crashed")
	}
}

func TestStandIn_CloseStopsAHungEngineWithoutCallingItAFailure(t *testing.T) {
	s := openStandIn(t, "hang")
	start := time.Now()
	if err := s.Close(); err != nil {
		// A killed child may report how it was stopped; that is not a
		// recognition failure, and Err below is what says so.
		t.Logf("Close reported how the engine was stopped: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Close took %v", elapsed)
	}
	if s.Err() != nil {
		t.Errorf("a stream the caller closed recorded a failure: %v", s.Err())
	}
	for range s.Results() {
		t.Error("a hung engine produced a segment")
	}
}

func TestStandIn_TheChildDoesNotSeeTheParentsSecrets(t *testing.T) {
	t.Setenv("WHISPERCPP_TEST_SECRET", "do-not-leak")
	s := openStandIn(t, "env")
	segs := speakAndDrain(t, s)
	if len(segs) != 1 {
		t.Fatalf("segments = %+v", segs)
	}
	if strings.Contains(segs[0].Text, "do-not-leak") {
		t.Errorf("the child read a parent environment variable: %q", segs[0].Text)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
