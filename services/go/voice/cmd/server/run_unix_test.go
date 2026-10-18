//go:build unix

package main

import (
	"fmt"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"
)

// TestT13_RunServesHealthAndStopsCleanlyOnSIGTERM covers run()'s whole success
// path: load and validate configuration, build the logger, health endpoint and
// service, register the intelligence runner, serve, and shut down on SIGTERM —
// the signal Kubernetes sends — with a nil error.
//
// # Why this is Unix-only
//
// It signals the test process itself with syscall.Kill, which only Unix
// provides. On other platforms the same test is declared in run_other_test.go
// and skips with a reason, so a non-Unix run reports the gap instead of hiding
// it.
//
// # Why the signal cannot end the test binary
//
// The platform service's Run installs signal.NotifyContext before it starts
// serving, so an answering /healthz proves the handler is registered. The
// signal is sent only after that, and is then delivered to Run rather than
// taking the default action.
func TestT13_RunServesHealthAndStopsCleanlyOnSIGTERM(t *testing.T) {
	keepDefaultLogger(t)

	ports := freePorts(t, 2)
	health := ports[1]
	t.Setenv("CS_VOICE_ENVIRONMENT", "development")
	t.Setenv("CS_VOICE_HTTP_PORT", fmt.Sprint(ports[0]))
	t.Setenv("CS_VOICE_HEALTH_PORT", fmt.Sprint(health))
	t.Setenv("CS_VOICE_LOG_LEVEL", "error")

	done := make(chan error, 1)
	go func() { done <- run() }()

	client := &http.Client{Timeout: time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", health)
	deadline := time.Now().Add(15 * time.Second)
	for {
		if resp, err := client.Get(url); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case err := <-done:
			t.Fatalf("run() returned before it served: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the health endpoint never answered; nothing was signalled")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("sending SIGTERM: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() after SIGTERM = %v, want a clean shutdown", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("run() did not return within 30s of SIGTERM")
	}
}
