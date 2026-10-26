// Package httpapi is the identity service's HTTP surface: exactly four POST
// endpoints (internal/routes), each a thin adapter over one IdentityService
// method. No authentication, token or proof logic lives here: the proof is
// handed to the service's DPoP verifier as it arrived, and tokens come only
// from the service.
//
// # Contract
//
// Every endpoint: method POST (anything else is 405 with Allow: POST);
// Content-Type application/json, optionally with charset=utf-8 (else 415);
// body at most MaxBodyBytes (else 413), exactly one JSON object with exactly
// the documented fields — unknown fields, missing fields, wrong types, trailing
// data and malformed JSON are 400. Every response carries Cache-Control:
// no-store, the platform security headers and an X-Correlation-ID (an inbound
// one is propagated only if it is a platform id). Errors are
// application/problem+json with a fixed detail per code; no error text, store
// detail, token, proof, code or phone number is ever echoed. Unknown paths are
// 404.
//
//	POST /v1/otp/request      {"msisdn": "+919876543210"}
//	  202 {"requestId": "req_<64 hex>", "expiresInSeconds": 300}
//	  503 ERR_OTP_DELIVERY_UNAVAILABLE  no delivery channel is configured; no
//	      challenge is created and no allowance is used
//	  503 ERR_OTP_DELIVERY_FAILED       the channel refused; the challenge is
//	      withdrawn
//	  400 ERR_INVALID_MSISDN, 429 ERR_RATE_LIMITED (+ Retry-After)
//
//	POST /v1/otp/validate     DPoP: <proof>   {"requestId": "...", "code": "123456"}
//	  200 {"accessToken": "...", "tokenType": "DPoP", "expiresIn": 900,
//	       "refreshToken": "...", "sessionId": "sess_<64 hex>"}
//	  400 ERR_INVALID_DPOP_PROOF (missing, repeated, oversized, invalid, stale,
//	      replayed, wrong htm/htu), 401 ERR_INVALID_OTP, 401 ERR_OTP_EXPIRED,
//	  400 ERR_INVALID_INTEGRITY_TOKEN (a repeated, empty or oversized
//	      Integrity-Token header, or a token the verifier rejected),
//	  429 ERR_RATE_LIMITED (+ Retry-After)
//
//	POST /v1/token/refresh    DPoP: <proof>   {"sessionId": "...", "refreshToken": "..."}
//	  200 {"accessToken": "...", "tokenType": "DPoP", "expiresIn": 900,
//	       "refreshToken": "..."}
//	  400 ERR_INVALID_DPOP_PROOF (as above, and a proof by another key),
//	  400 ERR_INVALID_INTEGRITY_TOKEN (as above; nothing rotates),
//	  401 ERR_INVALID_TOKEN (unknown, expired, revoked or reused refresh token
//	      — reuse also revokes the session — or a session with no device key)
//
//	POST /v1/account/delete   DPoP: <proof>   {"sessionId": "...", "refreshToken": "..."}
//	  204 (no body): the account that owns the session is erased (app/erasure.go);
//	      also for a retry of an erased session with a proof by the same key
//	  400 ERR_INVALID_DPOP_PROOF (as above; the htu is the deletion URL),
//	  401 ERR_INVALID_TOKEN (as for refresh; nothing is erased)
//
// All four: 503 ERR_TEMPORARILY_UNAVAILABLE (a dependency could not answer,
// including the replay store, or the request ran out of time) and 500
// ERR_INTERNAL. Neither ever accompanies a token.
//
// The DPoP proof is the single DPoP header (RFC 9449). Its htm is checked
// against this request's method and its htu against the CONFIGURED canonical
// URL; the request's Host and forwarding headers are never consulted. The
// request body is validated BEFORE the proof is verified, because verifying a
// proof spends its jti: a malformed body never costs the client its proof.
//
// Every response is counted once in identity_auth_requests_total{endpoint,
// outcome} (metrics.go). The endpoint is one of the four routes or "unknown";
// the outcome is "success" or the response's problem code. Nothing from the
// request is a label. cmd/server exposes it at GET /metrics on the health
// listener, never on this API's port.
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/callscreen/callscreen-platform/packages/go/middleware"
	"github.com/callscreen/callscreen-platform/packages/go/runtime"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/routes"
)

// Bounds. The largest valid body is under 200 bytes; a whole request is a few
// small database statements and one signature.
const (
	MaxBodyBytes   = 1 << 10
	ReadTimeout    = 10 * time.Second
	WriteTimeout   = 15 * time.Second
	HandlerTimeout = 10 * time.Second
)

var (
	requestIDPattern    = regexp.MustCompile(`^req_[0-9a-f]{64}$`)
	sessionIDPattern    = regexp.MustCompile(`^sess_[0-9a-f]{64}$`)
	refreshTokenPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	codePattern         = regexp.MustCompile(`^\d{6}$`)
)

type handler struct {
	svc *app.IdentityService
	log *slog.Logger
	now func() time.Time
	m   *Metrics
}

// New returns the identity API. The log receives only fixed codes and paths.
// Every response it decides is counted in m (MetricRequests); a nil m counts
// nothing.
//
// Every request is one edge span (ADR-0026 step 2): identity's listener is a
// public edge, so the span is a NEW root and an inbound traceparent is never
// adopted (middleware.EdgeTraceMiddleware). It sits inside correlation, so it
// carries the correlation ID, and outside the routes and their deadlines, so
// refusals and timeouts are traced too. A nil tracer leaves the chain as it
// was.
func New(svc *app.IdentityService, log *slog.Logger, m *Metrics, tracer runtime.Tracer) http.Handler {
	h := &handler{svc: svc, log: log, now: time.Now, m: m}
	var next http.Handler = http.HandlerFunc(h.route)
	next = withDeadlines(next)
	next = middleware.EdgeTraceMiddleware(tracer)(next)
	next = middleware.CorrelationIDMiddleware(next)
	next = middleware.SecurityHeadersMiddleware(next)
	return withDecision(h.recoverer(noStore(next)))
}

func (h *handler) route(w http.ResponseWriter, r *http.Request) {
	var serve func(http.ResponseWriter, *http.Request)
	switch r.URL.Path {
	case routes.OTPRequest:
		serve = h.requestOtp
	case routes.Enrollment:
		serve = h.enroll
	case routes.Refresh:
		serve = h.refresh
	case routes.AccountDeletion:
		serve = h.deleteAccount
	default:
		h.problem(w, r, http.StatusNotFound, "ERR_NOT_FOUND")
		return
	}
	if r.Method != routes.Method {
		w.Header().Set("Allow", routes.Method)
		h.problem(w, r, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
		return
	}
	serve(w, r)
}

// ---------------------------------------------------------------------------
// Endpoints
// ---------------------------------------------------------------------------

func (h *handler) requestOtp(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MSISDN string `json:"msisdn"`
	}
	if !h.decode(w, r, &body) {
		return
	}
	if body.MSISDN == "" {
		h.problem(w, r, http.StatusBadRequest, "ERR_INVALID_MSISDN")
		return
	}
	// TRUTHFULNESS: without a delivery channel, creating a challenge would
	// tell the caller a code exists that nobody can receive. Refuse first —
	// no challenge, no allowance spent.
	if !h.svc.CanDeliverOtp() {
		h.problem(w, r, http.StatusServiceUnavailable, "ERR_OTP_DELIVERY_UNAVAILABLE")
		return
	}
	reqID, err := h.svc.RequestOtp(r.Context(), body.MSISDN)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.ok(w, r, http.StatusAccepted, map[string]any{
		"requestId":        reqID,
		"expiresInSeconds": int64(app.OtpLifetime / time.Second),
	})
}

func (h *handler) enroll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RequestID string `json:"requestId"`
		Code      string `json:"code"`
	}
	if !h.decode(w, r, &body) {
		return
	}
	if !requestIDPattern.MatchString(body.RequestID) || !codePattern.MatchString(body.Code) {
		h.problem(w, r, http.StatusBadRequest, "ERR_INVALID_INPUT")
		return
	}
	proof, ok := h.proof(w, r)
	if !ok {
		return
	}
	integrity, ok := h.integrityToken(w, r)
	if !ok {
		return
	}
	sess, token, err := h.svc.ValidateOtpWithDeviceProofAndIntegrity(r.Context(), body.RequestID, body.Code, r.Method, proof, integrity)
	if err != nil || token == "" || sess == nil || sess.RefreshToken == "" {
		h.fail(w, r, err)
		return
	}
	h.ok(w, r, http.StatusOK, map[string]any{
		"accessToken":  token,
		"tokenType":    "DPoP",
		"expiresIn":    int64(accesstoken.Lifetime / time.Second),
		"refreshToken": sess.RefreshToken,
		"sessionId":    sess.SessionID,
	})
}

func (h *handler) refresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID    string `json:"sessionId"`
		RefreshToken string `json:"refreshToken"`
	}
	if !h.decode(w, r, &body) {
		return
	}
	if !sessionIDPattern.MatchString(body.SessionID) || !refreshTokenPattern.MatchString(body.RefreshToken) {
		h.problem(w, r, http.StatusBadRequest, "ERR_INVALID_INPUT")
		return
	}
	proof, ok := h.proof(w, r)
	if !ok {
		return
	}
	integrity, ok := h.integrityToken(w, r)
	if !ok {
		return
	}
	access, refresh, err := h.svc.RefreshTokenWithDeviceProofAndIntegrity(r.Context(), body.SessionID, body.RefreshToken, r.Method, proof, integrity)
	if err != nil || access == "" || refresh == "" {
		h.fail(w, r, err)
		return
	}
	h.ok(w, r, http.StatusOK, map[string]any{
		"accessToken":  access,
		"tokenType":    "DPoP",
		"expiresIn":    int64(accesstoken.Lifetime / time.Second),
		"refreshToken": refresh,
	})
}

// deleteAccount erases the account that owns the session (app/erasure.go):
// the same body and DPoP proof as refresh, and 204 No Content once erased. A
// repeated request for an erased session, with a proof from the same key, is
// also 204, so a client that lost the answer can safely retry.
func (h *handler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID    string `json:"sessionId"`
		RefreshToken string `json:"refreshToken"`
	}
	if !h.decode(w, r, &body) {
		return
	}
	if !sessionIDPattern.MatchString(body.SessionID) || !refreshTokenPattern.MatchString(body.RefreshToken) {
		h.problem(w, r, http.StatusBadRequest, "ERR_INVALID_INPUT")
		return
	}
	proof, ok := h.proof(w, r)
	if !ok {
		return
	}
	if err := h.svc.DeleteAccountWithDeviceProof(r.Context(), body.SessionID, body.RefreshToken, r.Method, proof); err != nil {
		h.fail(w, r, err)
		return
	}
	h.m.record(r, OutcomeSuccess)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Request parsing
// ---------------------------------------------------------------------------

// decode enforces the content type, the size bound and the exact shape: one
// JSON object, only the known fields, nothing after it.
func (h *handler) decode(w http.ResponseWriter, r *http.Request, into any) bool {
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" || !onlyUTF8(params) {
		h.problem(w, r, http.StatusUnsupportedMediaType, "ERR_UNSUPPORTED_MEDIA_TYPE")
		return false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		h.problem(w, r, http.StatusRequestEntityTooLarge, "ERR_BODY_TOO_LARGE")
		return false
	}
	// An OBJECT, not merely valid JSON: `null` would decode into a struct as a
	// silent no-op and pass for an empty object.
	if trimmed := bytes.TrimLeft(data, " \t\r\n"); err != nil || len(trimmed) == 0 || trimmed[0] != '{' {
		h.problem(w, r, http.StatusBadRequest, "ERR_MALFORMED_BODY")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	err = dec.Decode(into)
	if err == nil {
		if extra := dec.Decode(&struct{}{}); !errors.Is(extra, io.EOF) {
			err = errors.New("trailing data")
		}
	}
	if err != nil {
		h.problem(w, r, http.StatusBadRequest, "ERR_MALFORMED_BODY")
		return false
	}
	return true
}

func onlyUTF8(params map[string]string) bool {
	for k, v := range params {
		if k != "charset" || !strings.EqualFold(v, "utf-8") {
			return false
		}
	}
	return true
}

// proof returns the single DPoP header. Absent, repeated or oversized is a
// proof the verifier would refuse anyway; it is refused here, unspent.
func (h *handler) proof(w http.ResponseWriter, r *http.Request) (string, bool) {
	vals := r.Header.Values("DPoP")
	if len(vals) != 1 || vals[0] == "" || len(vals[0]) > dpop.MaxProofBytes {
		h.problem(w, r, http.StatusBadRequest, "ERR_INVALID_DPOP_PROOF")
		return "", false
	}
	return vals[0], true
}

// IntegrityTokenHeader carries an optional Play Integrity token (ADR-0031
// D31.2). Its verdict must be bound to the same request's DPoP proof.
const IntegrityTokenHeader = "Integrity-Token"

// integrityToken returns the optional Integrity-Token header: absent is "",
// which the service records as unverified. Repeated, empty or oversized is
// refused here, before anything is spent. The token is never logged or
// echoed.
func (h *handler) integrityToken(w http.ResponseWriter, r *http.Request) (string, bool) {
	vals := r.Header.Values(IntegrityTokenHeader)
	switch {
	case len(vals) == 0:
		return "", true
	case len(vals) != 1 || vals[0] == "" || len(vals[0]) > app.MaxIntegrityTokenBytes:
		h.problem(w, r, http.StatusBadRequest, "ERR_INVALID_INTEGRITY_TOKEN")
		return "", false
	}
	return vals[0], true
}

// ---------------------------------------------------------------------------
// Responses
// ---------------------------------------------------------------------------

// Fixed titles and details. Nothing request- or error-specific is ever added.
var problems = map[string]struct{ title, detail string }{
	"ERR_NOT_FOUND":                {"Not Found", "No such endpoint."},
	"ERR_METHOD_NOT_ALLOWED":       {"Method Not Allowed", "Use POST."},
	"ERR_UNSUPPORTED_MEDIA_TYPE":   {"Unsupported Media Type", "Send application/json."},
	"ERR_BODY_TOO_LARGE":           {"Payload Too Large", "The request body is too large."},
	"ERR_MALFORMED_BODY":           {"Bad Request", "The request body is not the expected JSON object."},
	"ERR_INVALID_INPUT":            {"Bad Request", "A field has an invalid value."},
	"ERR_INVALID_MSISDN":           {"Bad Request", "The phone number must be in E.164 format."},
	"ERR_INVALID_DPOP_PROOF":       {"Bad Request", "A valid DPoP proof for this request is required."},
	"ERR_INVALID_INTEGRITY_TOKEN":  {"Bad Request", "The integrity token is not valid for this request."},
	"ERR_INVALID_OTP":              {"Unauthorized", "The code is not valid for this request."},
	"ERR_OTP_EXPIRED":              {"Unauthorized", "The code has expired. Request a new one."},
	"ERR_INVALID_TOKEN":            {"Unauthorized", "The session cannot be refreshed. Sign in again."},
	"ERR_RATE_LIMITED":             {"Too Many Requests", "Too many attempts. Retry later."},
	"ERR_OTP_DELIVERY_UNAVAILABLE": {"Service Unavailable", "One-time codes cannot be delivered at present."},
	"ERR_OTP_DELIVERY_FAILED":      {"Service Unavailable", "The one-time code could not be delivered."},
	"ERR_TEMPORARILY_UNAVAILABLE":  {"Service Unavailable", "The request could not be completed. Retry later."},
	"ERR_INTERNAL":                 {"Internal Error", "The request could not be processed."},
}

func (h *handler) problem(w http.ResponseWriter, r *http.Request, status int, code string) {
	p, ok := problems[code]
	if !ok {
		status, code, p = http.StatusInternalServerError, "ERR_INTERNAL", problems["ERR_INTERNAL"]
	}
	if status >= 500 {
		h.log.Warn("identity request failed", slog.String("code", code), slog.String("path", r.URL.Path))
	}
	h.m.record(r, outcomeFor(code))
	middleware.WriteProblemDetails(w, status, p.title, p.detail, code, r.URL.Path, correlationID(r))
}

// correlationID returns the request's correlation ID, or "" when none is set.
func correlationID(r *http.Request) string {
	if id, ok := r.Context().Value(middleware.CorrelationContextKey).(string); ok {
		return id
	}
	return ""
}

// fail maps a service error to a fixed problem. A nil error with no token —
// which the service never returns — is internal, never a success.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var rl *app.RateLimitError
	switch {
	case errors.As(err, &rl):
		if !rl.RetryAt.IsZero() {
			w.Header().Set("Retry-After", strconv.FormatInt(retryAfterSeconds(rl.RetryAt, h.now()), 10))
		}
		h.problem(w, r, http.StatusTooManyRequests, "ERR_RATE_LIMITED")
	case errors.Is(err, app.ErrInvalidMSISDN):
		h.problem(w, r, http.StatusBadRequest, "ERR_INVALID_MSISDN")
	case errors.Is(err, app.ErrInvalidDeviceProof):
		h.problem(w, r, http.StatusBadRequest, "ERR_INVALID_DPOP_PROOF")
	case errors.Is(err, app.ErrPlayIntegrityFailed):
		h.problem(w, r, http.StatusBadRequest, "ERR_INVALID_INTEGRITY_TOKEN")
	case errors.Is(err, app.ErrExpiredOtp):
		h.problem(w, r, http.StatusUnauthorized, "ERR_OTP_EXPIRED")
	case errors.Is(err, app.ErrInvalidOtp):
		h.problem(w, r, http.StatusUnauthorized, "ERR_INVALID_OTP")
	case errors.Is(err, app.ErrTokenReused), errors.Is(err, app.ErrInvalidToken),
		errors.Is(err, app.ErrSessionNotDeviceBound), errors.Is(err, app.ErrDeviceProofRequired):
		h.problem(w, r, http.StatusUnauthorized, "ERR_INVALID_TOKEN")
	case errors.Is(err, app.ErrOtpDeliveryFailed):
		h.problem(w, r, http.StatusServiceUnavailable, "ERR_OTP_DELIVERY_FAILED")
	case errors.Is(err, dpop.ErrReplayUnavailable), errors.Is(err, app.ErrEnrollmentUnavailable),
		errors.Is(err, app.ErrDeviceRefreshUnavailable), errors.Is(err, app.ErrTokenIssuanceUnavailable),
		errors.Is(err, app.ErrAccountDeletionUnavailable),
		errors.Is(err, context.DeadlineExceeded):
		h.problem(w, r, http.StatusServiceUnavailable, "ERR_TEMPORARILY_UNAVAILABLE")
	default:
		h.problem(w, r, http.StatusInternalServerError, "ERR_INTERNAL")
	}
}

// ok writes a success. The body is encoded before anything is sent, so an
// encoding failure is a 500, never a partial success.
func (h *handler) ok(w http.ResponseWriter, r *http.Request, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		h.problem(w, r, http.StatusInternalServerError, "ERR_INTERNAL")
		return
	}
	h.m.record(r, OutcomeSuccess)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n')) //nolint:errcheck // the status is sent; a failed body write is the client going away
}

// retryAfterSeconds is whole seconds until retryAt, rounded up, at least 1.
func retryAfterSeconds(retryAt, now time.Time) int64 {
	s := int64(math.Ceil(retryAt.Sub(now).Seconds()))
	if s < 1 {
		return 1
	}
	return s
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

// withDeadlines bounds each request: reading the body, writing the response,
// and the work in between (through the request context the service uses).
// The platform.HTTPRunner sets only header and idle timeouts, so the bounds are
// applied per request here.
func withDeadlines(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		now := time.Now()
		_ = rc.SetReadDeadline(now.Add(ReadTimeout))   //nolint:errcheck // a writer without deadlines (http.ErrNotSupported) is still bounded by HandlerTimeout below
		_ = rc.SetWriteDeadline(now.Add(WriteTimeout)) //nolint:errcheck // as above
		ctx, cancel := context.WithTimeout(r.Context(), HandlerTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// recoverer turns a panic into a fixed 500; the panic value is not logged, as
// it could carry request data.
func (h *handler) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec)
				}
				h.log.Error("identity handler panicked", slog.String("path", r.URL.Path), slog.String("type", fmt.Sprintf("%T", rec)))
				h.m.record(r, outcomeFor("ERR_INTERNAL"))
				middleware.WriteProblemDetails(w, http.StatusInternalServerError, "Internal Error",
					problems["ERR_INTERNAL"].detail, "ERR_INTERNAL", r.URL.Path, "")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
