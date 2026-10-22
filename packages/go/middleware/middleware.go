// Package middleware holds cross-cutting HTTP handlers: problem-details
// errors, security headers, correlation IDs and rate limiting. It performs no
// real authentication; see AuthMiddleware.
package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/redis"
)

type contextKey string

// Context keys under which the middleware stores request values.
const (
	// UserContextKey holds the request's *UserClaims.
	UserContextKey contextKey = "user_claims"
	// CorrelationContextKey holds the request's correlation ID string.
	CorrelationContextKey contextKey = "correlation_id"
)

// UserClaims is the shape of a caller's identity claims. It is a data shape
// only: nothing in this package verifies it.
type UserClaims struct {
	Subject   string   `json:"sub"`
	MSISDN    string   `json:"msisdn"`
	TenantID  string   `json:"tenant_id"`
	Roles     []string `json:"roles"`
	Scope     string   `json:"scope"`
	ExpiresAt int64    `json:"exp"`
}

// ProblemDetails is an RFC 7807 problem-details error body.
type ProblemDetails struct {
	Type      string    `json:"type"`
	Title     string    `json:"title"`
	Status    int       `json:"status"`
	Detail    string    `json:"detail"`
	Instance  string    `json:"instance"`
	Code      string    `json:"code"`
	Timestamp time.Time `json:"timestamp"`
	TraceID   string    `json:"traceId"`
}

// WriteProblemDetails writes status and a problem-details JSON body.
func WriteProblemDetails(w http.ResponseWriter, status int, title, detail, code, instance, traceID string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	problem := ProblemDetails{
		Type:      "https://api.aegisai.in/errors/" + code,
		Title:     title,
		Status:    status,
		Detail:    detail,
		Instance:  instance,
		Code:      code,
		Timestamp: time.Now().UTC(),
		TraceID:   traceID,
	}
	_ = json.NewEncoder(w).Encode(problem) //nolint:errcheck // the status line is already written, so an encoding error cannot reach the client
}

// SecurityHeadersMiddleware sets nosniff, frame-deny, XSS-protection and HSTS
// response headers before calling next.
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

// CorrelationIDMiddleware propagates the inbound X-Correlation-ID header, or
// mints one, and stores it in the request context and the response header.
// Minted IDs have one-second resolution and no random part, so requests minted
// in the same second share an ID.
func CorrelationIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		correlationID := r.Header.Get("X-Correlation-ID")
		if correlationID == "" {
			correlationID = "corr_" + time.Now().Format("20060102150405")
		}
		w.Header().Set("X-Correlation-ID", correlationID)
		ctx := context.WithValue(r.Context(), CorrelationContextKey, correlationID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// AuthMiddleware performs NO authentication. It rejects requests without a
// "Bearer " Authorization prefix and attaches a hardcoded identity to every
// other request, verifying no signature, issuer or expiry. It has no callers
// and must not be used as an access control.
func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			WriteProblemDetails(w, http.StatusUnauthorized, "Unauthorized", "Missing or invalid Bearer token", "ERR_UNAUTHORIZED", r.URL.Path, r.Header.Get("X-Correlation-ID"))
			return
		}
		claims := &UserClaims{
			Subject:  "usr_subscriber",
			Roles:    []string{"subscriber"},
			TenantID: "ten_in_south",
		}
		ctx := context.WithValue(r.Context(), UserContextKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole rejects requests whose context claims lack requiredRole. The
// claims come from AuthMiddleware, which fabricates them, so this enforces
// nothing about the real caller.
func RequireRole(requiredRole string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := r.Context().Value(UserContextKey).(*UserClaims)
		if !ok || claims == nil {
			WriteProblemDetails(w, http.StatusUnauthorized, "Unauthorized", "User claims absent", "ERR_UNAUTHORIZED", r.URL.Path, r.Header.Get("X-Correlation-ID"))
			return
		}
		hasRole := false
		for _, role := range claims.Roles {
			if role == requiredRole {
				hasRole = true
				break
			}
		}
		if !hasRole {
			WriteProblemDetails(w, http.StatusForbidden, "Forbidden", "Insufficient privileges for role: "+requiredRole, "ERR_FORBIDDEN", r.URL.Path, r.Header.Get("X-Correlation-ID"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RateLimitingMiddleware rejects requests once cache.Increment reports more
// than limitPerMin requests in a minute for the client's remote address
// (IP and port). With redis.MemoryCache, whose Increment always returns 1, it
// never limits.
func RateLimitingMiddleware(cache redis.Cache, limitPerMin int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			clientIP := r.RemoteAddr
			key := "ratelimit:" + clientIP
			count, err := cache.Increment(r.Context(), key, time.Minute)
			if err == nil && count > limitPerMin {
				WriteProblemDetails(w, http.StatusTooManyRequests, "Too Many Requests", "Rate limit exceeded", "ERR_RATE_LIMIT_EXCEEDED", r.URL.Path, r.Header.Get("X-Correlation-ID"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
