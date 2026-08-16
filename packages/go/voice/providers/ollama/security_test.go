package ollama

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// SEC-1: a malformed response must not carry model output into an error
// ---------------------------------------------------------------------------
//
// # The scenario, which is ordinary rather than exotic
//
// A streamed line is `{"message":{"content":"..."},...}`. If the connection is
// cut mid-line, or the daemon flushes a partial object, what arrives is
// unparseable JSON that still CONTAINS the generated text. An adapter that puts
// the offending bytes into its error message has taken model output — which is
// downstream of whatever the caller said, and which runtime.Chunk documents as
// SENSITIVE, never logged — and put it somewhere errors get logged.
//
// The diagnostic value of those bytes is low: "the daemon sent something that
// is not our wire format" is the actionable part, and the length and a JSON
// syntax error say that without quoting the payload.
func TestSecurity_MalformedResponseDoesNotLeakModelOutput(t *testing.T) {
	t.Parallel()

	// A plausible truncation: valid prefix, real content, cut mid-object.
	const sensitive = "your balance is 4111 1111 1111 1111 and the sort code is"
	partial := `{"model":"m","message":{"role":"assistant","content":"` + sensitive

	d := newDaemon(t)
	d.chat = func(w http.ResponseWriter, _ *http.Request) {
		writeLine(w, textLine("a good line"))
		_, _ = w.Write([]byte(partial + "\n"))
	}

	s, err := newProvider(t, d).Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	defer func() { _ = s.Close() }()

	_, drainErr := drain(t, s)
	if drainErr == nil {
		t.Fatal("a malformed response ended the stream cleanly")
	}

	msg := drainErr.Error()

	// The error must still be useful.
	if !strings.Contains(msg, "malformed") {
		t.Errorf("the error no longer says what went wrong: %v", drainErr)
	}

	// ...and must not quote the payload.
	if strings.Contains(msg, sensitive) {
		t.Errorf("the error carries model output verbatim.\n"+
			"  leaked: %q\n"+
			"  error:  %s\n"+
			"Model output is downstream of caller speech and is documented "+
			"SENSITIVE; an error is logged.", sensitive, msg)
	}
	for _, fragment := range []string{"4111", "sort code", "balance"} {
		if strings.Contains(msg, fragment) {
			t.Errorf("the error contains the response fragment %q: %s", fragment, msg)
		}
	}
}

// TestSecurity_TransportErrorBodyIsBounded checks the other place a response
// body reaches an error: a non-200 status.
//
// This one is a DIFFERENT risk. A non-200 means generation did not happen, so
// the body is the daemon's own error object rather than model output. It is
// bounded and kept, because "the daemon said no and here is why" is the whole
// diagnostic.
func TestSecurity_TransportErrorBodyIsBounded(t *testing.T) {
	t.Parallel()

	huge := strings.Repeat("x", 64<<10)

	d := newDaemon(t)
	d.chat = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(huge))
	}

	_, err := newProvider(t, d).Generate(context.Background(), testRequest())
	if err == nil {
		t.Fatal("a 500 was accepted as success")
	}

	// 4 KiB bound plus the surrounding message; nothing near the 64 KiB sent.
	if len(err.Error()) > 8<<10 {
		t.Errorf("the error is %d bytes; an unbounded error body is a log-volume "+
			"amplification vector", len(err.Error()))
	}
}
