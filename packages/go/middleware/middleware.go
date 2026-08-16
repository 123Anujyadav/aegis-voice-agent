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

const (
	UserContextKey        contextKey = "user_claims"
	CorrelationContextKey contextKey = "correlation_id"
)

type UserClaims struct {
	Subject   string   `json:"sub"`
	MSISDN    string   `json:"msisdn"`
	TenantID  string   `json:"tenant_id"`
	Roles     []string `json:"roles"`
	Scope     string   `json:"scope"`
	ExpiresAt int64    `json:"exp"`
}

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
	_ = json.NewEncoder(w).Encode(problem)
}

func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

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
