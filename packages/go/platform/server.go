package platform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// Runner is a long-lived component managed by the service lifecycle.
//
// Every background worker — a Kafka consumer, a media session reaper, a metrics
// exporter — implements this interface so that the lifecycle can start it,
// observe its failure, and stop it deterministically. Components that manage
// their own goroutines outside this contract are invisible to shutdown and are
// the usual reason a process has to be killed rather than draining.
type Runner interface {
	// Name identifies the component in lifecycle logs.
	Name() string

	// Run executes the component, blocking until ctx is cancelled or an
	// unrecoverable error occurs. Returning nil signals orderly completion;
	// returning an error triggers shutdown of the entire service.
	Run(ctx context.Context) error

	// Shutdown releases the component's resources. It is called after Run's
	// context is cancelled and must respect the supplied deadline.
	Shutdown(ctx context.Context) error
}

// Service composes configuration, logging, health and a set of runners into a
// single managed lifecycle with correct startup and shutdown ordering.
//
// WHY THIS EXISTS RATHER THAN main() WIRING IT EACH TIME:
//
// Correct graceful shutdown is subtle and every service needs exactly the same
// behaviour. Implemented ad hoc in eight main functions it will be implemented
// eight slightly different ways, and the differences will only be discovered
// during an incident. The shutdown sequence below is the whole reason this type
// exists.
type Service struct {
	cfg     ServiceConfig
	log     *slog.Logger
	health  *Health
	runners []Runner

	// metrics is an optional exposition handler mounted on the health mux.
	//
	// Deliberately an http.Handler rather than a concrete exporter type: that
	// keeps this package's dependency set to the standard library, so a service
	// outside the AI plane does not link a metrics exporter it never serves.
	// The caller decides what to expose. See ADR-0013.
	metrics http.Handler

	// mu guards runners against concurrent registration.
	mu sync.Mutex
}

// NewService constructs a managed service from validated configuration.
func NewService(cfg ServiceConfig, log *slog.Logger, health *Health) *Service {
	return &Service{
		cfg:    cfg,
		log:    log,
		health: health,
	}
}

// Register adds a runner to the service lifecycle. It must be called before
// Run; runners registered afterwards are ignored.
func (s *Service) Register(r Runner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runners = append(s.runners, r)
}

// SetMetricsHandler mounts an exposition handler at GET /metrics on the health
// listener. It must be called before Run; a nil handler leaves the route
// unmounted.
//
// The metrics endpoint shares the health port rather than opening its own
// listener (ADR-0013). That port already exists, is already separate from the
// application listener, and is already the one that keeps answering when the
// application is saturated — which is exactly when a scrape is most worth
// having.
func (s *Service) SetMetricsHandler(h http.Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics = h
}

// Config returns the service's configuration.
func (s *Service) Config() ServiceConfig { return s.cfg }

// Logger returns the service's root logger.
func (s *Service) Logger() *slog.Logger { return s.log }

// Health returns the service's health aggregator.
func (s *Service) Health() *Health { return s.health }

// Run starts every registered runner plus the health listener and blocks until
// a shutdown signal arrives or a runner fails.
//
// THE SHUTDOWN SEQUENCE, AND WHY EACH STEP IS ORDERED AS IT IS:
//
//  1. SIGTERM arrives. Kubernetes has already begun removing this pod from
//     Endpoints, but that removal is eventually consistent: proxies on other
//     nodes may keep routing to us for several seconds.
//
//  2. Readiness begins failing immediately. This is what actually tells the
//     load balancer to stop. It is step one because everything after it is
//     wasted if traffic is still arriving.
//
//  3. We wait out the drain delay. This is the window in which the balancer
//     observes our failing readiness and removes us. Without this wait, we
//     close the listener while requests are still being routed to it, and
//     those requests fail — which is precisely the dropped-request symptom
//     that "graceful" shutdown is supposed to eliminate.
//
//  4. Runners' contexts are cancelled, so they stop accepting new work and
//     finish what is in flight.
//
//  5. Shutdown is called on each runner in reverse registration order, so that
//     a component tears down before whatever it depends on. Registration order
//     is therefore dependency order, lowest-level first.
//
//  6. If the deadline expires, we return an error and let the process exit.
//     Hanging forever is worse than an abrupt exit: the orchestrator will
//     SIGKILL us anyway, but only after a delay that stalls the rollout.
func (s *Service) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	runCtx, cancelRunners := context.WithCancel(ctx)
	defer cancelRunners()

	s.mu.Lock()
	runners := make([]Runner, len(s.runners))
	copy(runners, s.runners)
	s.mu.Unlock()

	// The health listener is separate from application traffic so that probes
	// continue to be answered while the main listener is draining or saturated.
	healthSrv := s.newHealthServer()
	healthErr := make(chan error, 1)
	go func() {
		s.log.Info("health listener starting",
			slog.Int("port", s.cfg.HealthPort))
		if err := healthSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			healthErr <- fmt.Errorf("health listener: %w", err)
			return
		}
		healthErr <- nil
	}()

	// Each runner reports its terminal outcome on this channel. Buffered to the
	// number of runners so that a runner exiting after shutdown has begun never
	// blocks on an unread channel and leaks its goroutine.
	type runnerExit struct {
		name string
		err  error
	}
	exits := make(chan runnerExit, len(runners))

	for _, r := range runners {
		go func(r Runner) {
			s.log.Info("runner starting", slog.String("runner", r.Name()))
			exits <- runnerExit{name: r.Name(), err: r.Run(runCtx)}
		}(r)
	}

	s.health.MarkReady()
	s.log.Info("service ready",
		slog.Int("http_port", s.cfg.HTTPPort),
		slog.Int("runners", len(runners)))

	// Block until the first of: shutdown signal, runner failure, health
	// listener failure.
	var cause error
	select {
	case <-ctx.Done():
		s.log.Info("shutdown signal received")
	case exit := <-exits:
		if exit.err != nil {
			cause = fmt.Errorf("runner %s failed: %w", exit.name, exit.err)
			s.log.Error("runner failed, shutting down service",
				slog.String("runner", exit.name),
				slog.String(AttrError, exit.err.Error()))
		} else {
			s.log.Warn("runner exited unexpectedly without error",
				slog.String("runner", exit.name))
		}
	case err := <-healthErr:
		if err != nil {
			cause = err
			s.log.Error("health listener failed",
				slog.String(AttrError, err.Error()))
		}
	}

	return s.shutdown(runners, healthSrv, cause)
}

// shutdown performs the ordered teardown described on Run.
func (s *Service) shutdown(runners []Runner, healthSrv *http.Server, cause error) error {
	// Step 2: begin failing readiness before anything else.
	s.health.MarkShuttingDown()
	s.log.Info("readiness disabled, draining")

	// Step 3: allow the load balancer to observe the readiness change. The
	// delay is derived from the shutdown budget rather than hard-coded so that
	// a service with a short grace period does not spend all of it waiting.
	drainDelay := s.cfg.ShutdownTimeout / 5
	if drainDelay > 5*time.Second {
		drainDelay = 5 * time.Second
	}
	time.Sleep(drainDelay)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()

	// Step 5: tear down runners in reverse registration order.
	var shutdownErrs []error
	for i := len(runners) - 1; i >= 0; i-- {
		r := runners[i]
		s.log.Info("stopping runner", slog.String("runner", r.Name()))

		if err := r.Shutdown(shutdownCtx); err != nil {
			s.log.Error("runner shutdown failed",
				slog.String("runner", r.Name()),
				slog.String(AttrError, err.Error()))
			shutdownErrs = append(shutdownErrs,
				fmt.Errorf("shutdown %s: %w", r.Name(), err))
		}

		// Stop early if the budget is exhausted; continuing would guarantee a
		// SIGKILL mid-teardown.
		if shutdownCtx.Err() != nil {
			shutdownErrs = append(shutdownErrs,
				fmt.Errorf("shutdown budget exhausted with %d runner(s) remaining", i))
			break
		}
	}

	// The health listener is stopped last so probes are answered honestly for
	// as long as possible.
	if err := healthSrv.Shutdown(shutdownCtx); err != nil {
		shutdownErrs = append(shutdownErrs, fmt.Errorf("health listener shutdown: %w", err))
	}

	s.log.Info("shutdown complete")

	if cause != nil {
		shutdownErrs = append(shutdownErrs, cause)
	}
	return errors.Join(shutdownErrs...)
}

// newHealthServer builds the dedicated probe listener.
func (s *Service) newHealthServer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health.LivenessHandler())
	mux.HandleFunc("GET /readyz", s.health.ReadinessHandler())

	s.mu.Lock()
	metricsHandler := s.metrics
	s.mu.Unlock()
	if metricsHandler != nil {
		// Isolated from the probes. Liveness and readiness are how an
		// orchestrator decides whether to kill this process, so a defect in an
		// exposition handler must not be able to take them down with it.
		mux.Handle("GET /metrics", recoverHandler(s.log, metricsHandler))
	}

	return &http.Server{
		Addr:    fmt.Sprintf(":%d", s.cfg.HealthPort),
		Handler: mux,
		// Bounded so that a stalled probe connection cannot occupy the listener
		// indefinitely. gosec flags a server without this, correctly.
		ReadHeaderTimeout: s.cfg.ReadHeaderTimeout,
	}
}

// recoverHandler contains a panic in an observability handler.
//
// Observability is not on the call path and must never become a dependency of
// one. Without this, a nil map or an index slip inside an exporter would take
// down the listener that answers liveness probes, and the orchestrator would
// restart a process whose actual work was fine.
func recoverHandler(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if log != nil {
					log.Error("metrics handler panicked",
						slog.Any("panic", rec), slog.String("path", r.URL.Path))
				}
				w.WriteHeader(http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// HTTPRunner adapts an http.Server to the Runner interface.
//
// It exists so that a service's application listener participates in the same
// lifecycle as every other component, rather than being started and stopped by
// bespoke code in main.
type HTTPRunner struct {
	name string
	srv  *http.Server
	log  *slog.Logger
}

// NewHTTPRunner wraps handler in a server bound to the configured HTTP port.
//
// Timeouts are set from configuration rather than left at Go's defaults, which
// are unlimited. An unlimited read or write timeout allows a single slow client
// to hold a connection open indefinitely, which is a trivial denial of service.
func NewHTTPRunner(name string, cfg ServiceConfig, handler http.Handler, log *slog.Logger) *HTTPRunner {
	return &HTTPRunner{
		name: name,
		log:  log,
		srv: &http.Server{
			Addr:              fmt.Sprintf(":%d", cfg.HTTPPort),
			Handler:           handler,
			ReadHeaderTimeout: cfg.ReadHeaderTimeout,
			// IdleTimeout bounds keep-alive connections. Without it, idle
			// connections accumulate until the file-descriptor limit is hit.
			IdleTimeout: 120 * time.Second,
			ErrorLog:    slog.NewLogLogger(log.Handler(), slog.LevelError),
		},
	}
}

// Name returns the runner's identifier.
func (r *HTTPRunner) Name() string { return r.name }

// Run serves HTTP until the listener is closed by Shutdown.
//
// ErrServerClosed is the expected outcome of an orderly stop and is translated
// to nil, so that a normal shutdown is not reported as a runner failure.
func (r *HTTPRunner) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", r.srv.Addr)
	if err != nil {
		return fmt.Errorf("bind %s: %w", r.srv.Addr, err)
	}

	r.log.Info("http listener bound", slog.String("addr", r.srv.Addr))

	if err := r.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

// Shutdown stops accepting connections and waits for in-flight requests to
// complete, bounded by ctx.
func (r *HTTPRunner) Shutdown(ctx context.Context) error {
	if err := r.srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	return nil
}
