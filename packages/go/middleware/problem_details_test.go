package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

// WriteProblemDetails writes every application/problem+json error that
// edge-api returns (internal/auth, internal/httpapi) and many of identity's.
// The Android clients read its "code" to decide between signing out,
// refreshing and retrying, so its shape is a wire contract. These tests pin
// that contract where it is defined.

// problemFields are the keys of the body, and no others.
var problemFields = []string{"code", "detail", "instance", "status", "timestamp", "title", "traceId", "type"}

func writeProblem(t *testing.T, status int, title, detail, code, instance, traceID string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	WriteProblemDetails(rec, status, title, detail, code, instance, traceID)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("the body is not JSON: %v: %q", err, rec.Body.String())
	}
	return rec, body
}

func TestWriteProblemDetails_IsTheProblemJSONContract(t *testing.T) {
	t.Parallel()
	before := time.Now().UTC().Add(-time.Second)
	rec, body := writeProblem(t, http.StatusUnauthorized, "Unauthorized", "The access token is not valid.",
		"ERR_INVALID_TOKEN", "/v1/me", "4bf92f3577b34da6a3ce929d0e0e4736")
	after := time.Now().UTC().Add(time.Second)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != strings.Join(problemFields, ",") {
		t.Errorf("keys = %v, want exactly %v", keys, problemFields)
	}
	want := map[string]any{
		"type":     "https://api.aegisai.in/errors/ERR_INVALID_TOKEN",
		"title":    "Unauthorized",
		"status":   float64(http.StatusUnauthorized), // JSON numbers decode as float64
		"detail":   "The access token is not valid.",
		"instance": "/v1/me",
		"code":     "ERR_INVALID_TOKEN",
		"traceId":  "4bf92f3577b34da6a3ce929d0e0e4736",
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s = %#v, want %#v", k, body[k], v)
		}
	}
	// The body's status always matches the status line.
	if body["status"] != float64(rec.Code) {
		t.Errorf("body status %v differs from the status line %d", body["status"], rec.Code)
	}
	ts, ok := body["timestamp"].(string)
	if !ok {
		t.Fatalf("timestamp = %#v, want a string", body["timestamp"])
	}
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t.Fatalf("timestamp %q is not RFC 3339: %v", ts, err)
	}
	if !strings.HasSuffix(ts, "Z") || at.Before(before) || at.After(after) {
		t.Errorf("timestamp %q is not the current time in UTC", ts)
	}
}

// identity writes its problems with no trace id: the key is still present,
// empty, so clients see one shape.
func TestWriteProblemDetails_AnEmptyTraceIDIsStillPresent(t *testing.T) {
	t.Parallel()
	_, body := writeProblem(t, http.StatusTooManyRequests, "Too Many Requests", "Rate limit exceeded",
		"ERR_RATE_LIMIT_EXCEEDED", "/v1/otp/request", "")
	if v, ok := body["traceId"]; !ok || v != "" {
		t.Errorf("traceId = %#v (present %v), want an empty string", v, ok)
	}
	if body["status"] != float64(http.StatusTooManyRequests) {
		t.Errorf("status = %v", body["status"])
	}
}

// Text that reaches the body (a path, a detail) is JSON-encoded: it cannot
// break out of its string, and HTML is escaped, so a body rendered by
// mistake cannot inject markup.
func TestWriteProblemDetails_EscapesUntrustedText(t *testing.T) {
	t.Parallel()
	detail := `"},"code":"ERR_FORGED","x":"<script>alert(1)</script>`
	instance := "/v1/me?\"<x>&"
	rec, body := writeProblem(t, http.StatusBadRequest, "Bad Request", detail, "ERR_INVALID_INPUT", instance, "")
	if body["detail"] != detail || body["instance"] != instance {
		t.Errorf("text did not round-trip: detail %q, instance %q", body["detail"], body["instance"])
	}
	if body["code"] != "ERR_INVALID_INPUT" {
		t.Errorf("code = %v: the detail rewrote the code", body["code"])
	}
	if raw := rec.Body.String(); strings.Contains(raw, "<script>") || strings.Contains(raw, "<x>") {
		t.Errorf("HTML is not escaped in the raw body: %s", raw)
	}
}

// Each status is written exactly once, as given, for the statuses the
// services use.
func TestWriteProblemDetails_WritesTheGivenStatus(t *testing.T) {
	t.Parallel()
	for _, status := range []int{
		http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable,
	} {
		rec, body := writeProblem(t, status, "t", "d", "ERR_X", "/", "")
		if rec.Code != status || body["status"] != float64(status) {
			t.Errorf("status %d: line %d, body %v", status, rec.Code, body["status"])
		}
	}
}
