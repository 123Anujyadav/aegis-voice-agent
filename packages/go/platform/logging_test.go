package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// logging.go enforces the redaction policy that ADR-0012 and ADR-0002 §10's
// SENSITIVE classification depend on, and had no test file.
//
// The policy is not restated here — logging.go's own lists (sensitiveKeys,
// hashedKeys, alwaysAllowed) are the specification, and these tests assert the
// behaviour they already define. Nothing new is introduced.

const testHashKey = "test-hash-key-at-least-16-bytes"

// capture builds a logger writing JSON into a buffer and returns both.
func capture(t *testing.T, level string) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	cfg := ServiceConfig{
		Name:        "edge-api",
		Version:     "v1.2.3",
		Environment: "development",
		Region:      "in-south-1",
		LogLevel:    level,
		LogFormat:   "json",
	}
	return NewLogger(cfg, &buf, []byte(testHashKey)), &buf
}

// lastRecord decodes the final JSON log line in buf.
func lastRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatal("no log record was written")
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &rec); err != nil {
		t.Fatalf("decoding log record %q: %v", lines[len(lines)-1], err)
	}
	return rec
}

// ---------------------------------------------------------------------------
// Redaction
// ---------------------------------------------------------------------------

// TestRedaction_SensitiveKeysAreSuppressed walks the categories logging.go
// lists as sensitive. Matching is documented as case-insensitive and
// substring-based, so the variants below must all be caught.
func TestRedaction_SensitiveKeysAreSuppressed(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"password", "passwd", "secret", "token", "authorization",
		"api_key", "apikey", "private_key", "credential",
		"refresh_token", "access_token", "otp", "pin", "cvv",
		"attestation", "cookie", "set-cookie",
		// Case and substring variants — the documented matching rule.
		"Authorization", "X-Authorization", "userPassword", "DEVICE_ATTESTATION",
	} {
		log, buf := capture(t, "info")
		log.Info("attempt", slog.String(key, "super-secret-value"))

		rec := lastRecord(t, buf)
		got, ok := rec[key]
		if !ok {
			t.Errorf("key %q vanished from the record entirely", key)
			continue
		}
		if got != redactedMarker {
			t.Errorf("key %q = %v, want %q — the raw value reached the sink",
				key, got, redactedMarker)
		}
		if strings.Contains(buf.String(), "super-secret-value") {
			t.Errorf("key %q: the secret value appears in the output", key)
		}
	}
}

// TestRedaction_PersonalIdentifiersArePseudonymised confirms personal
// identifiers are replaced by a keyed-hash pseudonym rather than dropped.
//
// From logging.go: "Rather than dropping them, the handler replaces the value with a
// keyed hash: two log lines about the same phone number can still be joined,
// but the number itself is not recoverable from the logs".
func TestRedaction_PersonalIdentifiersArePseudonymised(t *testing.T) {
	t.Parallel()

	const number = "+919876543210"

	for _, key := range []string{
		"phone", "msisdn", "caller_number", "callee_number", "email",
		"install_id", "device_id", "ip", "source_ip", "contact",
	} {
		log, buf := capture(t, "info")
		log.Info("call", slog.String(key, number))

		rec := lastRecord(t, buf)
		got, _ := rec[key].(string)

		if !strings.HasPrefix(got, "pseudo:") {
			t.Errorf("key %q = %q, want a pseudo: prefix", key, got)
		}
		if strings.Contains(buf.String(), number) {
			t.Errorf("key %q: the raw identifier %q reached the sink", key, number)
		}
		if got == redactedMarker {
			t.Errorf("key %q was suppressed rather than pseudonymised; "+
				"correlation is lost", key)
		}
	}
}

// TestRedaction_PseudonymIsStableAndDistinguishing — the property that makes
// pseudonymisation useful: same input joins, different input does not collide.
func TestRedaction_PseudonymIsStableAndDistinguishing(t *testing.T) {
	t.Parallel()

	log, buf := capture(t, "info")
	log.Info("first", slog.String("msisdn", "+919876543210"))
	log.Info("second", slog.String("msisdn", "+919876543210"))
	log.Info("third", slog.String("msisdn", "+919000000000"))

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 records, got %d", len(lines))
	}
	value := func(line string) string {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		s, _ := rec["msisdn"].(string)
		return s
	}

	if value(lines[0]) != value(lines[1]) {
		t.Error("the same identifier produced different pseudonyms; log lines " +
			"about one caller can no longer be joined")
	}
	if value(lines[0]) == value(lines[2]) {
		t.Error("two different identifiers produced the same pseudonym")
	}
}

// TestRedaction_EmptyValueIsNotPseudonymised pins the guard in pseudonymise:
// hashing an empty string would emit a constant that looks like a real
// identifier.
func TestRedaction_EmptyValueIsNotPseudonymised(t *testing.T) {
	t.Parallel()

	log, buf := capture(t, "info")
	log.Info("call", slog.String("msisdn", ""))

	if got, _ := lastRecord(t, buf)["msisdn"].(string); got != "" {
		t.Errorf("empty msisdn = %q, want an empty string", got)
	}
}

// TestRedaction_AllowListBeatsSubstringMatch is the subtle one.
//
// "call_session_id" CONTAINS "session_id", which is on the sensitive list. Only
// the exact-match allow list — consulted first, precisely so that "an internal
// correlation key is never caught by an incidental substring match" — keeps it
// readable. Without it, every log line loses its call correlation.
func TestRedaction_AllowListBeatsSubstringMatch(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"call_session_id", "trace_id", "span_id",
		"request_id", "correlation_id", "idempotency_key",
	} {
		log, buf := capture(t, "info")
		log.Info("turn", slog.String(key, "value-42"))

		if got, _ := lastRecord(t, buf)[key].(string); got != "value-42" {
			t.Errorf("allow-listed key %q = %q, want %q — correlation is lost",
				key, got, "value-42")
		}
	}

	// The bare sensitive key is still caught, proving the allow list is exact
	// rather than a blanket exemption for anything ending in _id.
	log, buf := capture(t, "info")
	log.Info("turn", slog.String("session_id", "value-42"))
	if got, _ := lastRecord(t, buf)["session_id"].(string); got != redactedMarker {
		t.Errorf("session_id = %q, want %q", got, redactedMarker)
	}
}

// TestRedaction_OrdinaryValuesArePreserved — redaction must not be so broad
// that ordinary operational fields are lost.
func TestRedaction_OrdinaryValuesArePreserved(t *testing.T) {
	t.Parallel()

	log, buf := capture(t, "info")
	log.Info("done",
		slog.String("outcome", "completed"),
		slog.Int("duration_ms", 42),
		slog.String("runner", "consumer"),
	)

	rec := lastRecord(t, buf)
	if rec["outcome"] != "completed" {
		t.Errorf("outcome = %v, want completed", rec["outcome"])
	}
	if rec["duration_ms"] != float64(42) {
		t.Errorf("duration_ms = %v, want 42", rec["duration_ms"])
	}
	if rec["runner"] != "consumer" {
		t.Errorf("runner = %v, want consumer", rec["runner"])
	}
}

// TestRedaction_GroupsAreRecursed confirms redaction applies to attributes
// nested inside groups.
//
// redact: it recurses into groups "so that nesting cannot be used to smuggle a
// sensitive value past the filter".
func TestRedaction_GroupsAreRecursed(t *testing.T) {
	t.Parallel()

	log, buf := capture(t, "info")
	log.Info("request", slog.Group("headers",
		slog.String("authorization", "Bearer super-secret-value"),
		slog.String("user_agent", "curl/8"),
	))

	if strings.Contains(buf.String(), "super-secret-value") {
		t.Error("a sensitive value nested in a group reached the sink; nesting " +
			"smuggles secrets past the filter")
	}
	if !strings.Contains(buf.String(), "curl/8") {
		t.Error("a non-sensitive value inside the group was lost")
	}
}

// TestRedaction_WithAttrsRedactsAtAttachTime confirms attributes bound with
// WithAttrs are redacted.
//
// WithAttrs redacts "at the point they are attached rather than on every
// record", so a secret bound once must not leak on any subsequent record.
func TestRedaction_WithAttrsRedactsAtAttachTime(t *testing.T) {
	t.Parallel()

	log, buf := capture(t, "info")
	bound := log.With(slog.String("api_key", "super-secret-value"))
	bound.Info("first")
	bound.Info("second")

	if strings.Contains(buf.String(), "super-secret-value") {
		t.Error("a secret bound via With reached the sink")
	}
	if got, _ := lastRecord(t, buf)["api_key"].(string); got != redactedMarker {
		t.Errorf("api_key = %q, want %q", got, redactedMarker)
	}
}

// ---------------------------------------------------------------------------
// Context promotion
// ---------------------------------------------------------------------------

// TestLogging_ContextIdentifiersArePromoted confirms trace and call-session
// identifiers carried in the context appear on the record.
//
// Handle: correlation identifiers are promoted from the context "so that every
// record is joinable to its trace without the caller restating them".
func TestLogging_ContextIdentifiersArePromoted(t *testing.T) {
	t.Parallel()

	log, buf := capture(t, "info")

	ctx := WithTraceID(context.Background(), "trace-abc")
	ctx = WithCallSessionID(ctx, "call-xyz")

	log.InfoContext(ctx, "turn complete")

	rec := lastRecord(t, buf)
	if rec[AttrTraceID] != "trace-abc" {
		t.Errorf("%s = %v, want trace-abc", AttrTraceID, rec[AttrTraceID])
	}
	if rec[AttrCallSessionID] != "call-xyz" {
		t.Errorf("%s = %v, want call-xyz", AttrCallSessionID, rec[AttrCallSessionID])
	}
}

// TestLogging_ContextWithoutIdentifiersOmitsThem confirms promotion is
// conditional rather than emitting empty keys.
func TestLogging_ContextWithoutIdentifiersOmitsThem(t *testing.T) {
	t.Parallel()

	log, buf := capture(t, "info")
	log.InfoContext(context.Background(), "no ids")

	rec := lastRecord(t, buf)
	if _, ok := rec[AttrTraceID]; ok {
		t.Error("trace_id was emitted with no trace in context")
	}
	if _, ok := rec[AttrCallSessionID]; ok {
		t.Error("call_session_id was emitted with no session in context")
	}
}

// ---------------------------------------------------------------------------
// Logger construction
// ---------------------------------------------------------------------------

// TestNewLogger_AttachesServiceIdentity — the base attributes every record
// carries, which are what make a log line attributable to a service and build.
func TestNewLogger_AttachesServiceIdentity(t *testing.T) {
	t.Parallel()

	log, buf := capture(t, "info")
	log.Info("hello")

	rec := lastRecord(t, buf)
	for key, want := range map[string]string{
		AttrService:     "edge-api",
		AttrVersion:     "v1.2.3",
		AttrEnvironment: "development",
		AttrRegion:      "in-south-1",
	} {
		if rec[key] != want {
			t.Errorf("%s = %v, want %q", key, rec[key], want)
		}
	}
}

// TestNewLogger_LevelFiltering confirms the configured level actually gates
// output rather than being recorded and ignored.
func TestNewLogger_LevelFiltering(t *testing.T) {
	t.Parallel()

	log, buf := capture(t, "warn")
	log.Debug("debug message")
	log.Info("info message")
	log.Warn("warn message")
	log.Error("error message")

	out := buf.String()
	for _, suppressed := range []string{"debug message", "info message"} {
		if strings.Contains(out, suppressed) {
			t.Errorf("%q was emitted at level warn", suppressed)
		}
	}
	for _, emitted := range []string{"warn message", "error message"} {
		if !strings.Contains(out, emitted) {
			t.Errorf("%q was suppressed at level warn", emitted)
		}
	}
}

// TestNewLogger_TextFormatIsHonoured covers the non-default branch of NewLogger.
func TestNewLogger_TextFormatIsHonoured(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	cfg := ServiceConfig{
		Name: "edge-api", Version: "dev", Environment: "development",
		Region: "in-south-1", LogLevel: "info", LogFormat: "text",
	}
	NewLogger(cfg, &buf, []byte(testHashKey)).Info("hello")

	out := buf.String()
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("text format produced JSON: %s", out)
	}
	if !strings.Contains(out, "service=edge-api") {
		t.Errorf("text output missing service attribute: %s", out)
	}
}

// TestNewLogger_RedactionAppliesToTextFormatToo — redaction is enforced in the
// handler, so it cannot be bypassed by choosing a different sink format.
func TestNewLogger_RedactionAppliesToTextFormatToo(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	cfg := ServiceConfig{
		Name: "edge-api", Version: "dev", Environment: "development",
		Region: "in-south-1", LogLevel: "info", LogFormat: "text",
	}
	NewLogger(cfg, &buf, []byte(testHashKey)).
		Info("attempt", slog.String("password", "super-secret-value"))

	if strings.Contains(buf.String(), "super-secret-value") {
		t.Error("text format bypassed redaction")
	}
}

// TestNewLogger_ProductionOmitsSource confirms the source attribute is logged
// in development and omitted in production.
//
// NewLogger: "AddSource is enabled outside production because the file and line
// of a log statement is valuable during development, and costly at volume".
func TestNewLogger_ProductionOmitsSource(t *testing.T) {
	t.Parallel()

	base := ServiceConfig{
		Name: "edge-api", Version: "dev", Region: "in-south-1",
		LogLevel: "info", LogFormat: "json",
	}

	var dev bytes.Buffer
	devCfg := base
	devCfg.Environment = "development"
	NewLogger(devCfg, &dev, []byte(testHashKey)).Info("hello")

	var prod bytes.Buffer
	prodCfg := base
	prodCfg.Environment = "production"
	NewLogger(prodCfg, &prod, []byte(testHashKey)).Info("hello")

	if !strings.Contains(dev.String(), "source") {
		t.Error("development log has no source attribute")
	}
	if strings.Contains(prod.String(), "\"source\"") {
		t.Errorf("production log carries a source attribute: %s", prod.String())
	}
}
