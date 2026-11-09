package platform

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Service.Run and HTTPRunner are the lifecycle every Go service uses. The
// services' own tests exercise them indirectly; these pin the contract here,
// where it is defined.

var discardLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// lifecyclePort returns a port that was free a moment ago.
func lifecyclePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func lifecycleConfig(t *testing.T) ServiceConfig {
	t.Helper()
	return ServiceConfig{
		Name: "lifecycle-test", Environment: "development", HealthPort: lifecyclePort(t), HTTPPort: lifecyclePort(t),
		// A short budget: the drain delay is a fifth of it.
		ShutdownTimeout: 500 * time.Millisecond, ReadHeaderTimeout: 2 * time.Second,
	}
}

// orderLog records the order of runner events across runners.
type orderLog struct {
	mu     sync.Mutex
	events []string
}

func (o *orderLog) add(e string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, e)
}

func (o *orderLog) list() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.events...)
}

// eventRunner waits for cancellation, or returns runErr at once when set.
type eventRunner struct {
	name   string
	log    *orderLog
	runErr error
}

func (r *eventRunner) Name() string { return r.name }

func (r *eventRunner) Run(ctx context.Context) error {
	if r.runErr != nil {
		return r.runErr
	}
	<-ctx.Done()
	return nil
}

func (r *eventRunner) Shutdown(context.Context) error {
	r.log.add("shutdown " + r.name)
	return nil
}

// runService starts svc.Run and returns a function that waits for its result.
func runService(ctx context.Context, svc *Service) func(t *testing.T) error {
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	return func(t *testing.T) error {
		t.Helper()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return within 10 s")
			return nil
		}
	}
}

// awaitReady polls the health listener until /readyz answers 200.
func awaitReady(t *testing.T, port int) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/readyz"
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("/readyz on port %d did not answer 200 within 5 s", port)
}

// Cancellation is a clean stop: readiness was served while running, Run
// returns nil, and runners are shut down in reverse registration order.
func TestServiceRun_CancellationStopsRunnersInReverseOrder(t *testing.T) {
	cfg := lifecycleConfig(t)
	order := &orderLog{}
	svc := NewService(cfg, discardLog, NewHealth(cfg))
	for _, n := range []string{"pool", "housekeeping", "http"} {
		svc.Register(&eventRunner{name: n, log: order})
	}
	ctx, cancel := context.WithCancel(context.Background())
	wait := runService(ctx, svc)

	awaitReady(t, cfg.HealthPort)
	cancel()

	if err := wait(t); err != nil {
		t.Fatalf("Run = %v, want nil after cancellation", err)
	}
	if got := strings.Join(order.list(), ", "); got != "shutdown http, shutdown housekeeping, shutdown pool" {
		t.Errorf("shutdown order = %s, want the reverse of registration", got)
	}
	if svc.Health() == nil || svc.Config().Name != cfg.Name || svc.Logger() != discardLog {
		t.Error("the accessors do not return what the service was built with")
	}
}

// A runner that fails stops the whole service: the others are shut down, and
// Run returns the failure, wrapped with the runner's name.
func TestServiceRun_ARunnerFailureStopsTheServiceAndIsReturned(t *testing.T) {
	cfg := lifecycleConfig(t)
	order := &orderLog{}
	errBoom := errors.New("boom")
	svc := NewService(cfg, discardLog, NewHealth(cfg))
	svc.Register(&eventRunner{name: "steady", log: order})
	svc.Register(&eventRunner{name: "failing", log: order, runErr: errBoom})

	err := runService(context.Background(), svc)(t)

	if !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "runner failing failed") {
		t.Fatalf("Run = %v, want the failing runner's error, named", err)
	}
	if got := strings.Join(order.list(), ", "); got != "shutdown failing, shutdown steady" {
		t.Errorf("shutdown order = %s, want every runner shut down in reverse order", got)
	}
}

// A runner that returns without an error still stops the service (a runner
// must run until cancelled), and that is not reported as a failure.
func TestServiceRun_ARunnerExitingCleanlyStopsTheService(t *testing.T) {
	cfg := lifecycleConfig(t)
	order := &orderLog{}
	svc := NewService(cfg, discardLog, NewHealth(cfg))
	svc.Register(&eventRunner{name: "steady", log: order})
	svc.Register(exitingRunner{})

	if err := runService(context.Background(), svc)(t); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if got := strings.Join(order.list(), ", "); got != "shutdown steady" {
		t.Errorf("events = %s, want the remaining runner shut down", got)
	}
}

type exitingRunner struct{}

func (exitingRunner) Name() string                   { return "exiting" }
func (exitingRunner) Run(context.Context) error      { return nil }
func (exitingRunner) Shutdown(context.Context) error { return nil }

// If the health port cannot be bound, the service stops and says so: probes
// that cannot be answered must not leave a process running unobserved.
func TestServiceRun_AHealthListenerFailureIsReturned(t *testing.T) {
	cfg := lifecycleConfig(t)
	taken, err := net.Listen("tcp", ":"+strconv.Itoa(cfg.HealthPort))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = taken.Close() })
	svc := NewService(cfg, discardLog, NewHealth(cfg))
	svc.Register(&eventRunner{name: "steady", log: &orderLog{}})

	err = runService(context.Background(), svc)(t)

	if err == nil || !strings.Contains(err.Error(), "health listener") {
		t.Fatalf("Run = %v, want the health listener's failure", err)
	}
}

// Runners are fixed when Run starts: one registered afterwards is neither run
// nor shut down.
func TestServiceRun_ARunnerRegisteredAfterRunIsIgnored(t *testing.T) {
	cfg := lifecycleConfig(t)
	order := &orderLog{}
	svc := NewService(cfg, discardLog, NewHealth(cfg))
	svc.Register(&eventRunner{name: "early", log: order})
	ctx, cancel := context.WithCancel(context.Background())
	wait := runService(ctx, svc)
	awaitReady(t, cfg.HealthPort)

	svc.Register(&eventRunner{name: "late", log: order})
	cancel()

	if err := wait(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if got := strings.Join(order.list(), ", "); got != "shutdown early" {
		t.Errorf("events = %s, want only the runner registered before Run", got)
	}
}

// HTTPRunner serves its handler on the configured port, and Shutdown is an
// orderly stop: Run then returns nil, not http.ErrServerClosed.
func TestHTTPRunner_ServesAndStopsCleanly(t *testing.T) {
	cfg := lifecycleConfig(t)
	r := NewHTTPRunner("api", cfg, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "hello")
	}), discardLog)
	if r.Name() != "api" {
		t.Errorf("Name = %q", r.Name())
	}
	runErr := make(chan error, 1)
	go func() { runErr <- r.Run(context.Background()) }()

	client := &http.Client{Timeout: time.Second}
	url := "http://127.0.0.1:" + strconv.Itoa(cfg.HTTPPort) + "/"
	var body string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && body == "" {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
		if resp, err := client.Do(req); err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			body = string(b)
		} else {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if body != "hello" {
		t.Fatalf("body = %q, want the handler's response", body)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v", err)
	}
	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("Run after Shutdown = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Shutdown")
	}
}

// A port that cannot be bound is a runner failure, named, not a silent hang.
func TestHTTPRunner_BindFailureIsAnError(t *testing.T) {
	cfg := lifecycleConfig(t)
	taken, err := net.Listen("tcp", ":"+strconv.Itoa(cfg.HTTPPort))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = taken.Close() })
	r := NewHTTPRunner("api", cfg, http.NotFoundHandler(), discardLog)

	err = r.Run(context.Background())

	if err == nil || !strings.HasPrefix(err.Error(), "bind ") {
		t.Fatalf("Run = %v, want a bind error", err)
	}
}
