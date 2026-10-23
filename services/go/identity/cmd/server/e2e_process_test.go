package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/bootstrap"
)

// e2eProcessEnv selects this file's helper mode. It is set only by edge-api's
// end-to-end test (services/go/edge-api/cmd/server/e2e_test.go), which builds
// this package's TEST binary and runs it as a child process. Nothing here is in
// the production binary.
const e2eProcessEnv = "AEGIS_E2E_IDENTITY_PROCESS"

// e2eOtpPrefix marks the one stdout line per OTP that the parent reads; the
// service's own logs go to stderr, so stdout carries only these lines and the
// test framework's.
const e2eOtpPrefix = "E2E-OTP "

// stdoutOtpSender stands in for an SMS provider, which does not exist (see
// app.RequestOtp): it hands each code to the parent process over the stdout
// pipe, and nowhere else.
type stdoutOtpSender struct{ mu sync.Mutex }

func (s *stdoutOtpSender) SendOtp(_ context.Context, _, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := fmt.Fprintf(os.Stdout, "%s%s\n", e2eOtpPrefix, code)
	return err
}

// TestE2EIdentityProcess is not a test on its own: it is identity, run as a
// child process by edge-api's end-to-end test, and it skips otherwise.
//
//	migrate  the explicit migration path: dispatch("migrate"), exactly as
//	         `service migrate`.
//	serve    the configured service through configureIdentity and the real
//	         platform.Service lifecycle, all configuration from the CS_IDENTITY_*
//	         environment as in production. The ONE test-only difference is an
//	         OtpSender (production configures none). Shutdown begins when stdin
//	         closes: a portable stand-in for SIGTERM, which Windows cannot send
//	         to a child process.
func TestE2EIdentityProcess(t *testing.T) {
	switch mode := os.Getenv(e2eProcessEnv); mode {
	case "":
		t.Skip("helper process for edge-api's end-to-end test; runs only when that test starts it")
	case "migrate":
		if err := dispatch([]string{"migrate"}); err != nil {
			t.Fatal(err)
		}
	case "serve":
		e2eServe(t)
	default:
		t.Fatalf("%s=%q is not a mode", e2eProcessEnv, mode)
	}
}

func e2eServe(t *testing.T) {
	var cfg platform.ServiceConfig
	if err := platform.LoadConfig(&cfg, envPrefix); err != nil {
		t.Fatal(err)
	}
	cfg.Name = serviceName
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	hashKey := make([]byte, 32)
	if _, err := rand.Read(hashKey); err != nil {
		t.Fatal(err)
	}
	logger := platform.NewLogger(cfg, os.Stderr, hashKey)
	health := platform.NewHealth(cfg)
	svc := platform.NewService(cfg, logger, health)

	orig := openIdentity
	openIdentity = func(ctx context.Context, c bootstrap.ActivationConfig, now func() time.Time) (*bootstrap.Runtime, error) {
		rt, err := orig(ctx, c, now)
		if rt != nil {
			rt.Service.SetOtpSender(&stdoutOtpSender{}) // test-only: production configures none
		}
		return rt, err
	}
	defer func() { openIdentity = orig }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := configureIdentity(ctx, cfg, svc, health, logger); err != nil {
		t.Fatalf("identity: %v", err)
	}
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		cancel()
	}()
	if err := svc.Run(ctx); err != nil {
		t.Fatalf("service: %v", err)
	}
}
