package callback

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
)

// Phase 19 P1-12 — the provider-neutral callback HTTP handler.
//
// It turns one HTTP request into one [Request] and hands it to
// [Admission.Admit]. It adds no authentication of its own and has no way
// around the admission gate: every request goes through Admit, and only
// Admit's outcome decides the response.
//
//	carrier ─► (ingress: NOT DECIDED, B4) ─► [THIS] ─► Admission.Admit ─► Ingestor ─► lifecycle
//
// # It is not registered anywhere
//
// No service mounts it. telephony-gateway (the intended owner, A4) has no
// application listener today. Mounting it publicly is a later, separate
// decision, and pointless until a provider Verifier exists: with none
// registered, Admit refuses every request.
//
// # Trusted inputs come from configuration, never from the request
//
//   - Provider identity is HTTPConfig.Provider: one handler per provider
//     route. The body and headers cannot change it.
//   - The URL handed to the verifier is HTTPConfig.ExternalURL (scheme, host
//     and path, as configured at the carrier) plus the query string exactly as
//     it arrived in the request line. Host, X-Forwarded-*, Forwarded and
//     X-Real-IP are NEVER read. If the ingress rewrites the path or the query
//     string, a signature over the URL will not verify; that is a property of
//     the ingress (B4) and is documented, not worked around.
//
// # Nothing sensitive is returned
//
// Responses are fixed JSON bodies naming a status code word only. No error
// text, header value, body byte or identifier is echoed. The handler does not
// log.

// HTTPConfig configures one provider route. Every field without "optional" is
// required; there are no defaults.
type HTTPConfig struct {
	// Provider is the carrier this route belongs to.
	Provider telephony.ProviderID
	// Method is the only method accepted, e.g. "POST".
	Method string
	// ExternalURL is the absolute https URL the carrier is configured to call,
	// without a query string. Its path must equal the path the request
	// arrives on; a request on any other path is refused.
	ExternalURL string
	// MaxBodyBytes bounds the body. It is enforced while reading, so an
	// oversized body is never fully buffered.
	MaxBodyBytes int64
	// Timeout bounds reading the body and admission together.
	Timeout time.Duration
	// RequiredHeaders must each be present, non-empty and at most
	// maxHeaderValueBytes long. Optional.
	RequiredHeaders []string
	// ContentTypes, if set, lists the only media types accepted (parameters
	// such as charset are ignored). Optional.
	ContentTypes []string
	// RetryAfter is sent with 503 responses, in whole seconds.
	RetryAfter time.Duration
	// Recorder receives one HTTPRequest per request and one HTTPRejected per
	// request refused before admission. Optional; nil records nothing. Give
	// the Admission the same Recorder to see admission outcomes (P1-13).
	Recorder Recorder
}

const maxHeaderValueBytes = 8 << 10

// Response codes, as returned in the JSON body. Stable; documented in the
// P1-12 report.
const (
	CodeAccepted        = "accepted" // 200 — settled; do not redeliver
	CodeMethod          = "method_not_allowed"
	CodeNotFound        = "not_found"      // 404 — wrong path for this route
	CodeTooLarge        = "body_too_large" // 413
	CodeMalformed       = "malformed"      // 400 — request or event invalid
	CodeUnsupportedType = "unsupported_media_type"
	CodeUnauthenticated = "unauthenticated" // 401
	CodeEventIDReused   = "event_id_reused" // 409
	CodeUnknownCall     = "unknown_call"    // 404 — no binding (yet)
	CodeInFlight        = "in_flight"       // 503 — another worker holds it; retry
	CodeUnavailable     = "unavailable"     // 503 — durable state could not answer
	CodeTimeout         = "timeout"         // 503
	CodeInternal        = "internal_error"  // 500
)

type httpHandler struct {
	cfg  HTTPConfig
	base *url.URL
	adm  *Admission
}

// NewHTTPHandler builds the handler for one provider route.
func NewHTTPHandler(cfg HTTPConfig, adm *Admission) (http.Handler, error) {
	var problems []string
	if adm == nil {
		problems = append(problems, "an Admission is required")
	}
	if !cfg.Provider.Valid() {
		problems = append(problems, "Provider must be a valid provider ID")
	}
	switch cfg.Method {
	case http.MethodPost, http.MethodPut, http.MethodGet:
	default:
		problems = append(problems, "Method must be POST, PUT or GET")
	}
	base, err := url.Parse(cfg.ExternalURL)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.RawQuery != "" ||
		base.Fragment != "" || base.User != nil || base.Path == "" {
		problems = append(problems, "ExternalURL must be an absolute https URL with a path and no query, fragment or credentials")
	}
	if cfg.MaxBodyBytes <= 0 {
		problems = append(problems, "MaxBodyBytes must be positive")
	}
	if cfg.Timeout <= 0 {
		problems = append(problems, "Timeout must be positive")
	}
	if cfg.RetryAfter < time.Second {
		problems = append(problems, "RetryAfter must be at least one second")
	}
	for _, h := range cfg.RequiredHeaders {
		if strings.TrimSpace(h) == "" {
			problems = append(problems, "RequiredHeaders contains an empty name")
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("callback: invalid HTTP handler configuration: %s", strings.Join(problems, "; "))
	}
	return &httpHandler{cfg: cfg, base: base, adm: adm}, nil
}

// record calls f against the configured Recorder, containing any panic.
func (h *httpHandler) record(f func(Recorder)) {
	if h.cfg.Recorder != nil {
		guarded(func() { f(h.cfg.Recorder) })
	}
}

// reject counts a pre-admission refusal and answers it.
func (h *httpHandler) reject(w http.ResponseWriter, reason string, status int, code string) {
	h.record(func(rec Recorder) { rec.HTTPRejected(string(h.cfg.Provider), reason) })
	if status == http.StatusServiceUnavailable {
		h.unavailable(w, code)
		return
	}
	respond(w, status, code)
}

func (h *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.record(func(rec Recorder) { rec.HTTPRequest(string(h.cfg.Provider)) })

	if r.Method != h.cfg.Method {
		w.Header().Set("Allow", h.cfg.Method)
		h.reject(w, RejectMethod, http.StatusMethodNotAllowed, CodeMethod)
		return
	}
	if r.URL.Path != h.base.Path {
		h.reject(w, RejectPath, http.StatusNotFound, CodeNotFound)
		return
	}
	if !h.headersValid(r) {
		h.reject(w, RejectHeader, http.StatusBadRequest, CodeMalformed)
		return
	}
	if !h.contentTypeAllowed(r) {
		h.reject(w, RejectContentType, http.StatusUnsupportedMediaType, CodeUnsupportedType)
		return
	}
	if r.ContentLength > h.cfg.MaxBodyBytes {
		h.reject(w, RejectBodySize, http.StatusRequestEntityTooLarge, CodeTooLarge)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.Timeout)
	defer cancel()
	// Bound the body read by the same deadline. Not every ResponseWriter
	// supports it (httptest's recorder does not); the context still bounds
	// admission.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(h.cfg.Timeout))

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.cfg.MaxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			h.reject(w, RejectBodySize, http.StatusRequestEntityTooLarge, CodeTooLarge)
		case ctx.Err() != nil:
			h.reject(w, RejectTimeout, http.StatusServiceUnavailable, CodeTimeout)
		default:
			h.reject(w, RejectBodyRead, http.StatusBadRequest, CodeMalformed)
		}
		return
	}
	if len(body) == 0 {
		h.reject(w, RejectEmptyBody, http.StatusBadRequest, CodeMalformed)
		return
	}

	req := &Request{
		Provider:         h.cfg.Provider,
		Method:           r.Method,
		URL:              h.externalURL(r),
		Header:           r.Header.Clone(),
		Body:             body,
		PeerCertificates: verifiedPeerCertificates(r),
		ReceivedAt:       time.Now(),
	}
	res, err := h.adm.Admit(ctx, req)
	h.answer(w, res, err)
}

// externalURL is the configured external URL plus the query string as it
// arrived in the request line. Nothing else in the request contributes.
func (h *httpHandler) externalURL(r *http.Request) string {
	u := *h.base
	u.RawQuery = r.URL.RawQuery
	return u.String()
}

func (h *httpHandler) headersValid(r *http.Request) bool {
	for _, name := range h.cfg.RequiredHeaders {
		vals := r.Header.Values(name)
		if len(vals) != 1 || strings.TrimSpace(vals[0]) == "" || len(vals[0]) > maxHeaderValueBytes {
			return false
		}
	}
	return true
}

func (h *httpHandler) contentTypeAllowed(r *http.Request) bool {
	if len(h.cfg.ContentTypes) == 0 {
		return true
	}
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return false
	}
	for _, allowed := range h.cfg.ContentTypes {
		if strings.EqualFold(mt, allowed) {
			return true
		}
	}
	return false
}

// verifiedPeerCertificates returns the raw client certificates only if the
// TLS stack verified a chain. Informational: admission never treats them as
// authentication.
func verifiedPeerCertificates(r *http.Request) [][]byte {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
		return nil
	}
	out := make([][]byte, 0, len(r.TLS.PeerCertificates))
	for _, c := range r.TLS.PeerCertificates {
		out = append(out, c.Raw)
	}
	return out
}

// answer maps Admit's outcome to a response, through the same classification
// the admission metrics use (outcomeLabel), so a response and its metric can
// never disagree. A timeout or an outage wins over any other wrapped sentinel.
func (h *httpHandler) answer(w http.ResponseWriter, res Result, err error) {
	switch outcomeLabel(res, err) {
	case OutcomeLabelApplied, OutcomeLabelAlreadySatisfied, OutcomeLabelStale,
		OutcomeLabelConflictRecorded, OutcomeLabelDuplicate:
		respond(w, http.StatusOK, CodeAccepted)
	case OutcomeLabelInFlight:
		h.unavailable(w, CodeInFlight)
	case OutcomeLabelTimeout, OutcomeLabelCancelled:
		h.unavailable(w, CodeTimeout)
	case OutcomeLabelStoreUnavailable:
		h.unavailable(w, CodeUnavailable)
	case OutcomeLabelMalformedRequest, OutcomeLabelInvalidEvent, OutcomeLabelProviderMismatch:
		respond(w, http.StatusBadRequest, CodeMalformed)
	case OutcomeLabelNoVerifier, OutcomeLabelUnauthenticated:
		respond(w, http.StatusUnauthorized, CodeUnauthenticated)
	case OutcomeLabelEventIDReused:
		respond(w, http.StatusConflict, CodeEventIDReused)
	case OutcomeLabelUnknownCall:
		respond(w, http.StatusNotFound, CodeUnknownCall)
	default:
		// No normalizer, or any error not classified above. Never a success.
		respond(w, http.StatusInternalServerError, CodeInternal)
	}
}

func (h *httpHandler) unavailable(w http.ResponseWriter, code string) {
	w.Header().Set("Retry-After", strconv.Itoa(int(h.cfg.RetryAfter/time.Second)))
	respond(w, http.StatusServiceUnavailable, code)
}

func respond(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"code":"`+code+`"}`)
}
