// Package httpapi is edge-api's business HTTP surface. Every route here is
// PROTECTED: Routes is mounted only by cmd/server's configureAuth, and only
// behind auth.Middleware, which verifies the DPoP-bound access token and the
// request's proof (replay-checked in PostgreSQL) before any handler runs. No
// handler here authenticates, and none reads an identity from the request: the
// only source of identity is auth.PrincipalFrom.
//
// # Contract
//
//	GET /v1/me    Authorization: DPoP <access token>    DPoP: <proof>
//	  200 {"subject": "<sub>", "deviceKeyThumbprint": "<cnf.jkt>"}
//
// subject is the verified token's sub (identity's subscriber id).
// deviceKeyThumbprint is the token's cnf.jkt, which the request's proof was
// verified against: the RFC 7638 thumbprint of the caller's own device key.
// Nothing else is returned. Roles are deliberately NOT returned: the token
// carries them signed, but no role vocabulary is defined anywhere
// (accesstoken.Signer.Issue: "this package defines no role vocabulary") and no
// edge decision depends on them, so their meaning is not established. The
// token id, the token, the proof and every server-side detail are never
// returned. Query parameters, headers other than the two above, and any body
// are ignored.
//
// Before the handler (auth.Middleware): 401 ERR_UNAUTHENTICATED,
// ERR_INVALID_TOKEN or ERR_INVALID_DPOP_PROOF with WWW-Authenticate: DPoP, and
// 503 ERR_TEMPORARILY_UNAVAILABLE if replay protection cannot answer. The
// handler itself: 405 ERR_METHOD_NOT_ALLOWED (Allow: GET) for any other
// method of an authenticated request, and 500 ERR_INTERNAL if no verified
// principal is present — which only a mounting error could cause; it fails
// closed. Every response carries Cache-Control: no-store; errors are
// application/problem+json with fixed text. Unknown paths are the mux's 404.
package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/callscreen/callscreen-platform/packages/go/middleware"
	"github.com/callscreen/callscreen-platform/services/go/edge-api/internal/auth"
)

// MePath is the authenticated caller's identity endpoint.
const MePath = "/v1/me"

// Routes is edge-api's production route table: path pattern to handler. It is
// served only behind auth.Middleware (cmd/server).
func Routes(log *slog.Logger) map[string]http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return map[string]http.Handler{MePath: me{log: log}}
}

// MeResponse is the body of a successful GET /v1/me.
type MeResponse struct {
	Subject             string `json:"subject"`
	DeviceKeyThumbprint string `json:"deviceKeyThumbprint"`
}

type me struct{ log *slog.Logger }

func (h me) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		problem(w, r, http.StatusMethodNotAllowed, "Method Not Allowed", "Only GET is supported.", "ERR_METHOD_NOT_ALLOWED")
		return
	}
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok || p.Subject == "" || p.DeviceKeyThumbprint == "" {
		// Unreachable behind auth.Middleware. Nothing is synthesised from the
		// request: no principal, no identity.
		h.log.Error("protected route reached without a verified principal", slog.String("path", r.URL.Path))
		problem(w, r, http.StatusInternalServerError, "Internal Server Error", "The request could not be completed.", "ERR_INTERNAL")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(MeResponse{Subject: p.Subject, DeviceKeyThumbprint: p.DeviceKeyThumbprint}) //nolint:errcheck // the status is sent; a failed body write is the client going away
}

func problem(w http.ResponseWriter, r *http.Request, status int, title, detail, code string) {
	middleware.WriteProblemDetails(w, status, title, detail, code, r.URL.Path, correlationID(r))
}

// correlationID returns the request's correlation ID, or "" when none is set.
func correlationID(r *http.Request) string {
	if id, ok := r.Context().Value(middleware.CorrelationContextKey).(string); ok {
		return id
	}
	return ""
}
