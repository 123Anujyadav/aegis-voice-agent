package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The start-up contract as a process (OD-15, production-readiness F12): this
// package's test binary, re-run as a child with startupHelperEnv set, does
// what `service` with no argument does, and the parent watches its exit code,
// its stderr and its ports. Nothing here is in the production binary; the
// built image is checked the same way by tools/scripts/image-smoke.sh.
const startupHelperEnv = "AEGIS_IDENTITY_STARTUP_HELPER"

// TestMain runs the tests, except in the child the start-up tests start: there
// it is the service, without the test framework, so no helper test is ever
// reported skipped. It applies main's own handling (main.go) to dispatch with
// no argument.
func TestMain(m *testing.M) {
	if os.Getenv(startupHelperEnv) == "1" {
		if err := dispatch(nil); err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// serviceProcess is a started helper. Its exited channel is closed once it has
// exited, after err holds what Wait returned, so any number of readers can
// observe the exit.
type serviceProcess struct {
	exited chan struct{}
	err    error
}

// startService starts the helper with no identity variable at all, the given
// tier (unset when tier is nil) and its own ports.
func startService(t *testing.T, tier *string, healthPort, httpPort int) (*serviceProcess, *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "CS_IDENTITY_") && !strings.HasPrefix(kv, e2eProcessEnv+"=") {
			env = append(env, kv)
		}
	}
	env = append(env, startupHelperEnv+"=1",
		"CS_IDENTITY_HEALTH_PORT="+strconv.Itoa(healthPort),
		"CS_IDENTITY_HTTP_PORT="+strconv.Itoa(httpPort))
	if tier != nil {
		env = append(env, "CS_IDENTITY_ENVIRONMENT="+*tier)
	}
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &serviceProcess{exited: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-p.exited:
		case <-time.After(10 * time.Second):
			t.Error("the helper did not exit within 10 s of being killed")
		}
	})
	return p, &stderr
}

func listening(port int) bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// Staging and production with no identity configuration: the process exits
// with code 1 and main's FATAL line, and neither port ever answers.
func TestStartup_StagingAndProductionWithoutConfigurationExitBeforeBinding(t *testing.T) {
	for _, tier := range []string{"staging", "production"} {
		t.Run(tier, func(t *testing.T) {
			healthPort, httpPort := freePort(t), freePort(t)
			p, stderr := startService(t, &tier, healthPort, httpPort)

			answered := false
			deadline := time.After(30 * time.Second)
		wait:
			for {
				select {
				case <-p.exited:
					break wait
				case <-deadline:
					t.Fatalf("still running after 30 s; stderr: %s", stderr.String())
				default:
					if listening(healthPort) || listening(httpPort) {
						answered = true
					}
					time.Sleep(10 * time.Millisecond)
				}
			}

			var exitErr *exec.ExitError
			if !errors.As(p.err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("exit = %v, want exit code 1; stderr: %s", p.err, stderr.String())
			}
			if !strings.Contains(stderr.String(), "FATAL: identity: identity is not configured: staging and production require the CS_IDENTITY_* activation variables") {
				t.Errorf("stderr = %q, want main's FATAL line with the configuration-required error", stderr.String())
			}
			if answered || listening(healthPort) || listening(httpPort) {
				t.Error("a port answered: startup must be refused before anything is bound")
			}
		})
	}
}

// Development, an empty tier and an unset tier (both of which resolve to the
// development default) keep the skeleton: /healthz 200, /readyz 200
// "identity inactive", and no application port.
func TestStartup_DevelopmentWithoutConfigurationKeepsTheSkeleton(t *testing.T) {
	for name, tier := range map[string]*string{"unset": nil, "empty": ptr(""), "development": ptr("development")} {
		t.Run(name, func(t *testing.T) {
			healthPort, httpPort := freePort(t), freePort(t)
			p, stderr := startService(t, tier, healthPort, httpPort)
			client := &http.Client{Timeout: time.Second}
			get := func(path string) (int, string) {
				resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(healthPort) + path)
				if err != nil {
					return 0, ""
				}
				defer resp.Body.Close()
				body, _ := io.ReadAll(resp.Body)
				return resp.StatusCode, string(body)
			}

			deadline := time.Now().Add(30 * time.Second)
			for {
				select {
				case <-p.exited:
					t.Fatalf("the service exited (%v); stderr: %s", p.err, stderr.String())
				default:
				}
				if code, _ := get("/healthz"); code == http.StatusOK {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("/healthz did not answer 200 within 30 s; stderr: %s", stderr.String())
				}
				time.Sleep(50 * time.Millisecond)
			}
			code, body := get("/readyz")
			if code != http.StatusOK || !strings.Contains(body, "identity inactive") {
				t.Errorf("/readyz = %d %s, want 200 with identity inactive", code, body)
			}
			if listening(httpPort) {
				t.Error("an inactive identity bound the application port")
			}
		})
	}
}
