package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// serveCorrelation runs one request through CorrelationIDMiddleware and
// returns the ID the handler saw in its context and the response header.
func serveCorrelation(t *testing.T, inbound string) (fromContext, fromHeader string) {
	t.Helper()
	var seen string
	h := CorrelationIDMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen, _ = r.Context().Value(CorrelationContextKey).(string)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	if inbound != "" {
		req.Header.Set("X-Correlation-ID", inbound)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return seen, rec.Header().Get("X-Correlation-ID")
}

// TestCorrelationID_MintsADistinctIDPerRequest: the old minter used a
// one-second timestamp, so every request in the same second shared one ID.
func TestCorrelationID_MintsADistinctIDPerRequest(t *testing.T) {
	t.Parallel()
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id, header := serveCorrelation(t, "")
		if id != header {
			t.Fatalf("context ID %q differs from response header %q", id, header)
		}
		if !isCorrelationID(id) {
			t.Fatalf("minted ID %q is not a platform correlation ID", id)
		}
		if seen[id] {
			t.Fatalf("ID %q minted twice within %d requests", id, i+1)
		}
		seen[id] = true
	}
}

// TestCorrelationID_IsUniqueUnderConcurrency mints from many goroutines at
// once, the condition under which a timestamp-only ID collides most.
func TestCorrelationID_IsUniqueUnderConcurrency(t *testing.T) {
	t.Parallel()
	const workers, each = 32, 100
	ids := make(chan string, workers*each)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				id, _ := serveCorrelation(t, "")
				ids <- id
			}
		}()
	}
	wg.Wait()
	close(ids)

	seen := make(map[string]bool, workers*each)
	for id := range ids {
		if seen[id] {
			t.Fatalf("ID %q minted twice under concurrency", id)
		}
		seen[id] = true
	}
	if len(seen) != workers*each {
		t.Fatalf("%d distinct IDs, want %d", len(seen), workers*each)
	}
}

// TestCorrelationID_PropagatesAnInboundIDUnchanged: a valid platform ID that
// arrives on the request is the ID for the whole request.
func TestCorrelationID_PropagatesAnInboundIDUnchanged(t *testing.T) {
	t.Parallel()
	inbound := runtime.NewCorrelationID().String()
	id, header := serveCorrelation(t, inbound)
	if id != inbound || header != inbound {
		t.Fatalf("inbound %q became context %q / header %q", inbound, id, header)
	}
}

// TestCorrelationID_ReplacesAnInvalidInboundID: anything that is not exactly a
// platform ID is replaced, so an arbitrary header never reaches the logs.
func TestCorrelationID_ReplacesAnInvalidInboundID(t *testing.T) {
	t.Parallel()
	valid := runtime.NewCorrelationID().String()
	cases := map[string]string{
		"foreign":         "abc-123",
		"old timestamp":   "corr_20261022101500",
		"wrong prefix":    "sess_" + valid[len("corr_"):],
		"lowercase":       strings.ToLower(valid),
		"one char short":  valid[:len(valid)-1],
		"one char long":   valid + "0",
		"excluded letter": valid[:len(valid)-1] + "U",
		"oversized":       strings.Repeat("A", 8192),
	}
	for name, inbound := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			id, header := serveCorrelation(t, inbound)
			if id == inbound {
				t.Fatalf("invalid inbound %q was propagated", inbound)
			}
			if !isCorrelationID(id) || id != header {
				t.Fatalf("replacement %q (header %q) is not a platform correlation ID", id, header)
			}
		})
	}
}

// TestCorrelationID_ValidatorAcceptsTheRuntimeMinter pins the restated shape
// against the real minter: if runtime's format changes, this fails rather
// than every inbound ID being silently replaced.
func TestCorrelationID_ValidatorAcceptsTheRuntimeMinter(t *testing.T) {
	t.Parallel()
	for i := 0; i < 1000; i++ {
		if id := runtime.NewCorrelationID().String(); !isCorrelationID(id) {
			t.Fatalf("the validator rejects runtime's own ID %q", id)
		}
	}
}
