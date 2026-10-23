package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Without a verified principal — only a mounting error could cause it — the
// handler fails closed: 500, fixed text, no identity synthesised from the
// request, one error log naming no request value.
func TestMe_MissingPrincipalFailsClosed(t *testing.T) {
	var logs bytes.Buffer
	h := Routes(slog.New(slog.NewTextHandler(&logs, nil)))[MePath]
	req := httptest.NewRequest("GET", MePath+"?subject=attacker", http.NoBody)
	for _, name := range []string{"X-User-ID", "X-Subject", "X-Forwarded-User", "Authorization", "DPoP"} {
		req.Header.Set(name, "attacker")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != 500 || body["code"] != "ERR_INTERNAL" || body["detail"] != "The request could not be completed." {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Errorf("headers %v", rec.Header())
	}
	if strings.Contains(rec.Body.String(), "attacker") || strings.Contains(logs.String(), "attacker") {
		t.Error("a client-supplied value reached the response or the log")
	}
	if !strings.Contains(logs.String(), "without a verified principal") {
		t.Errorf("the failure was not logged: %q", logs.String())
	}
}

func TestMe_OnlyGET(t *testing.T) {
	h := Routes(nil)[MePath]
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, MePath, strings.NewReader(`{"subject":"attacker"}`)))
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET" || rec.Header().Get("Cache-Control") != "no-store" || strings.Contains(rec.Body.String(), "attacker") {
			t.Errorf("%s: %d %v %s", m, rec.Code, rec.Header(), rec.Body.String())
		}
	}
}

func TestRoutes_ExactlyMe(t *testing.T) {
	r := Routes(nil)
	if len(r) != 1 || r[MePath] == nil || MePath != "/v1/me" {
		t.Fatalf("routes = %v", r)
	}
}
