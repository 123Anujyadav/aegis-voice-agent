package platform

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"
)

type shutdownCtxKey struct{}

// shutdownCtxRunner captures the context its Shutdown is given.
type shutdownCtxRunner struct {
	err         error
	value       any
	deadline    time.Time
	hasDeadline bool
}

func (r *shutdownCtxRunner) Name() string { return "recording" }

func (r *shutdownCtxRunner) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (r *shutdownCtxRunner) Shutdown(ctx context.Context) error {
	r.err = ctx.Err()
	r.value = ctx.Value(shutdownCtxKey{})
	r.deadline, r.hasDeadline = ctx.Deadline()
	return nil
}

// The teardown context is derived from Run's context but must survive its
// cancellation: shutdown usually starts BECAUSE that context was cancelled
// (SIGTERM), and runners must still get the full ShutdownTimeout to drain.
// It must also keep the context's values, which is what passing ctx (rather
// than context.Background) adds.
func TestShutdown_RunnersGetTheBudgetDespiteACancelledRunContext(t *testing.T) {
	t.Parallel()

	cfg := ServiceConfig{Name: "test", HTTPPort: 8080, HealthPort: 8081, ShutdownTimeout: 500 * time.Millisecond}
	svc := NewService(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), NewHealth(cfg))

	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), shutdownCtxKey{}, "carried"))
	cancel() // as after SIGTERM

	runner := &shutdownCtxRunner{}
	start := time.Now()
	if err := svc.shutdown(ctx, []Runner{runner}, &http.Server{}, nil); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	if runner.err != nil {
		t.Fatalf("runner's shutdown context was already done (%v); teardown must not inherit Run's cancellation", runner.err)
	}
	if runner.value != "carried" {
		t.Fatalf("runner's shutdown context lost the run context's values: got %v", runner.value)
	}
	if !runner.hasDeadline {
		t.Fatal("runner's shutdown context has no deadline; ShutdownTimeout must bound teardown")
	}
	if budget := runner.deadline.Sub(start); budget <= 0 || budget > cfg.ShutdownTimeout+time.Second {
		t.Fatalf("shutdown deadline %v after start, want within ShutdownTimeout (%v)", budget, cfg.ShutdownTimeout)
	}
}
