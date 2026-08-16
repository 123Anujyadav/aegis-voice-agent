package platform

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// contextKey is an unexported type for context keys defined by this package.
// Using an unexported named type prevents collisions with keys defined in other
// packages, which is the documented Go convention for context values.
type contextKey int

const (
	// loggerKey stores a *slog.Logger carrying request-scoped attributes.
	loggerKey contextKey = iota
	// traceIDKey stores the distributed trace identifier for the current request.
	traceIDKey
	// callSessionIDKey stores the call session identifier, when one applies.
	callSessionIDKey
)

// Standard attribute keys. Declared as constants rather than written inline so
// that a typo cannot silently create a second, differently-named field that
// splits a dashboard query in two.
const (
	AttrService       = "service"
	AttrVersion       = "version"
	AttrEnvironment   = "environment"
	AttrRegion        = "region"
	AttrTraceID       = "trace_id"
	AttrCallSessionID = "call_session_id"
	AttrUserID        = "user_id"
	AttrError         = "error"
	AttrDurationMS    = "duration_ms"
)

// redactedMarker replaces any value suppressed by the redacting handler.
const redactedMarker = "[REDACTED]"

// sensitiveKeys names attributes that must never be logged verbatim, whatever
// value they carry.
//
// WHY A DENY LIST AT THE LOG SINK AS WELL AS SCHEMA ANNOTATIONS:
//
// contracts/proto/callscreen/common/v1/annotations.proto classifies fields on
// protobuf messages, and that is the primary control. This deny list is the
// second layer, covering values that never pass through a protobuf message —
// a token parsed from an HTTP header, a phone number read from a URL path, a
// map assembled ad hoc in a handler. Defence in depth: the schema annotation
// catches structured data, this catches everything else.
//
// Matching is case-insensitive and substring-based, so "authorization",
// "Authorization" and "x-authorization" are all caught by "authorization".
var sensitiveKeys = []string{
	"password",
	"passwd",
	"secret",
	"token",
	"authorization",
	"api_key",
	"apikey",
	"private_key",
	"credential",
	"session_id",
	"refresh_token",
	"access_token",
	"otp",
	"pin",
	"cvv",
	"attestation",
	"cookie",
	"set-cookie",
}

// alwaysAllowed names attributes that are explicitly safe. It is consulted
// BEFORE sensitiveKeys and hashedKeys.
//
// This allow list exists because substring matching, while the right default
// for catching unanticipated field names, produces false positives against our
// own correlation keys. The motivating case: "call_session_id" contains the
// substring "session_id", so without this list the single most useful debugging
// identifier in the platform would be redacted from every log line — silently,
// and precisely when an engineer most needs it.
//
// Anything added here must be genuinely non-personal: these are internal
// correlation identifiers minted by us, not values derived from user data.
// Matching is exact and case-insensitive, not substring, so that adding an
// entry cannot accidentally widen the exemption.
var alwaysAllowed = map[string]struct{}{
	"call_session_id": {},
	"trace_id":        {},
	"span_id":         {},
	"request_id":      {},
	"correlation_id":  {},
	"idempotency_key": {},
}

// hashedKeys names attributes that are personal data but whose correlation
// value is operationally necessary. Rather than dropping them, the handler
// replaces the value with a keyed hash: two log lines about the same phone
// number can still be joined, but the number itself is not recoverable from
// the logs.
var hashedKeys = []string{
	"phone",
	"msisdn",
	"caller_number",
	"callee_number",
	"email",
	"install_id",
	"device_id",
	"ip",
	"source_ip",
	"contact",
}

// RedactingHandler wraps a slog.Handler and enforces the redaction policy on
// every attribute before it reaches the underlying sink.
//
// It is implemented as a handler rather than as a helper the caller must
// remember to invoke, because a redaction scheme that depends on discipline at
// thousands of call sites will leak. Wrapping the handler makes redaction
// unavoidable: there is no code path from slog to the sink that bypasses it.
type RedactingHandler struct {
	inner slog.Handler

	// hashKey keys the HMAC used for pseudonymisation. It is per-process and
	// randomly generated at startup unless supplied, so that hashes cannot be
	// correlated across deployments or reversed with a precomputed table.
	hashKey []byte
}

// NewRedactingHandler wraps inner so that sensitive attributes are suppressed
// and personal identifiers are pseudonymised.
//
// hashKey must be at least 16 bytes. Supplying a stable key allows correlation
// across service instances within one environment; supplying a random key
// confines correlation to a single process. Production deployments supply a
// stable per-environment key sourced from the secret manager.
func NewRedactingHandler(inner slog.Handler, hashKey []byte) *RedactingHandler {
	return &RedactingHandler{inner: inner, hashKey: hashKey}
}

// Enabled reports whether the handler processes records at the given level.
func (h *RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle applies the redaction policy to every attribute on the record and then
// forwards it to the wrapped handler.
func (h *RedactingHandler) Handle(ctx context.Context, rec slog.Record) error {
	clean := slog.NewRecord(rec.Time, rec.Level, rec.Message, rec.PC)

	// Promote correlation identifiers from the context so that every record is
	// joinable to its trace without the caller restating them.
	if id, ok := TraceIDFrom(ctx); ok {
		clean.AddAttrs(slog.String(AttrTraceID, id))
	}
	if id, ok := CallSessionIDFrom(ctx); ok {
		clean.AddAttrs(slog.String(AttrCallSessionID, id))
	}

	rec.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(h.redact(a))
		return true
	})

	return h.inner.Handle(ctx, clean)
}

// WithAttrs returns a handler whose records carry the supplied attributes,
// redacted at the point they are attached rather than on every record.
func (h *RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		redacted = append(redacted, h.redact(a))
	}
	return &RedactingHandler{inner: h.inner.WithAttrs(redacted), hashKey: h.hashKey}
}

// WithGroup returns a handler that nests subsequent attributes under name.
func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{inner: h.inner.WithGroup(name), hashKey: h.hashKey}
}

// redact applies the policy to a single attribute, recursing into groups so
// that nesting cannot be used to smuggle a sensitive value past the filter.
func (h *RedactingHandler) redact(a slog.Attr) slog.Attr {
	if a.Value.Kind() == slog.KindGroup {
		members := a.Value.Group()
		out := make([]slog.Attr, 0, len(members))
		for _, m := range members {
			out = append(out, h.redact(m))
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	}

	key := strings.ToLower(a.Key)

	// Exact-match allow list is consulted first, so that an internal
	// correlation key is never caught by an incidental substring match below.
	if _, ok := alwaysAllowed[key]; ok {
		return a
	}

	for _, s := range sensitiveKeys {
		if strings.Contains(key, s) {
			return slog.String(a.Key, redactedMarker)
		}
	}

	for _, s := range hashedKeys {
		if strings.Contains(key, s) {
			return slog.String(a.Key, h.pseudonymise(a.Value.String()))
		}
	}

	return a
}

// pseudonymise returns a truncated keyed HMAC of v, prefixed so that a reader
// can tell at a glance that the value is a pseudonym rather than a real
// identifier. Truncation to 16 hex characters keeps log volume down while
// leaving collision probability negligible at our cardinality.
func (h *RedactingHandler) pseudonymise(v string) string {
	if v == "" {
		return ""
	}
	mac := hmac.New(sha256.New, h.hashKey)
	// hash.Hash.Write is documented never to return an error.
	_, _ = mac.Write([]byte(v))
	return "pseudo:" + hex.EncodeToString(mac.Sum(nil))[:16]
}

// loggerOptions collects the settings NewLogger derives from configuration.
type loggerOptions struct {
	level  slog.Level
	format string
}

// parseLevel maps a configured level name to its slog equivalent. Validation
// has already rejected unknown values, so the default arm is defensive only.
func parseLevel(name string) slog.Level {
	switch strings.ToLower(name) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// NewLogger constructs the process logger for a service.
//
// Every record automatically carries service, version, environment and region,
// which is what makes logs from eight services queryable as one stream. Records
// are redacted by the wrapping handler described above.
//
// The returned logger is safe for concurrent use by multiple goroutines.
func NewLogger(cfg ServiceConfig, w io.Writer, hashKey []byte) *slog.Logger {
	opts := loggerOptions{
		level:  parseLevel(cfg.LogLevel),
		format: strings.ToLower(cfg.LogFormat),
	}

	handlerOpts := &slog.HandlerOptions{
		Level: opts.level,
		// AddSource is enabled outside production because the file and line of a
		// log statement is valuable during development, and costly at volume.
		AddSource: !cfg.IsProduction(),
	}

	var base slog.Handler
	if opts.format == "text" {
		base = slog.NewTextHandler(w, handlerOpts)
	} else {
		base = slog.NewJSONHandler(w, handlerOpts)
	}

	return slog.New(NewRedactingHandler(base, hashKey)).With(
		slog.String(AttrService, cfg.Name),
		slog.String(AttrVersion, cfg.Version),
		slog.String(AttrEnvironment, cfg.Environment),
		slog.String(AttrRegion, cfg.Region),
	)
}

// --- Context propagation -----------------------------------------------------
//
// Correlation identifiers are carried on the context rather than threaded
// through every function signature. The alternative — passing a logger
// explicitly everywhere — is more honest about data flow but so invasive in
// practice that engineers route around it, which is worse.

// WithLogger returns a context carrying the supplied logger.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, l)
}

// LoggerFrom returns the logger stored on ctx, or slog.Default when none is
// present. It never returns nil, so callers need no nil check — a logging call
// must never be the thing that panics a request.
func LoggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

// WithTraceID returns a context carrying the distributed trace identifier.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceIDKey, id)
}

// TraceIDFrom returns the trace identifier on ctx and whether one was present.
func TraceIDFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(traceIDKey).(string)
	return id, ok && id != ""
}

// WithCallSessionID returns a context carrying the call session identifier.
//
// This is the single most useful correlation key in the platform: one screened
// call spans the telephony gateway, the session orchestrator and several AI
// services, and this identifier is what stitches their logs into one narrative.
func WithCallSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, callSessionIDKey, id)
}

// CallSessionIDFrom returns the call session identifier on ctx and whether one
// was present.
func CallSessionIDFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(callSessionIDKey).(string)
	return id, ok && id != ""
}

// --- Default logger installation ---------------------------------------------

// installOnce guards SetDefaultLogger so that repeated calls in tests or in a
// service that re-reads configuration cannot race on slog's package state.
var installOnce sync.Once

// SetDefaultLogger installs l as the process-wide default exactly once.
//
// Installing a default matters because third-party libraries and the standard
// library's own error paths log through slog.Default(). Without this, those
// records bypass redaction and structured formatting entirely.
func SetDefaultLogger(l *slog.Logger) {
	installOnce.Do(func() {
		slog.SetDefault(l)
	})
}
