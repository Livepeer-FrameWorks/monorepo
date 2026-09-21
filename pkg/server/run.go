package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

const (
	defaultShutdownTimeout = 30 * time.Second
	defaultReadTimeout     = 30 * time.Second
	defaultWriteTimeout    = 30 * time.Second
	defaultIdleTimeout     = 120 * time.Second
)

// ReadinessPropagationDelay is how long Run keeps serving after a stop signal
// flips /ready to 503, so the pollers that route by readiness see the drain
// before the listeners close. The slowest is Navigator's Cloudflare
// load-balancer monitor, which probes every 60 s with a 5 s timeout
// (NAVIGATOR_CF_MONITOR_INTERVAL and _TIMEOUT defaults); Quartermaster's
// health poller probes every 30 s with up to 25 % jitter
// (QM_HEALTH_POLL_INTERVAL_SECONDS default). The go_service systemd unit's
// TimeoutStopSec and the compose_stack stop_grace_period leave room for this
// delay plus the shutdown timeout.
const ReadinessPropagationDelay = 65 * time.Second

// preStopDelay is ReadinessPropagationDelay; tests shorten it.
var preStopDelay = ReadinessPropagationDelay

// HTTPListener is one HTTP server run by Run.
type HTTPListener struct {
	Name     string
	BindAddr string
	Port     string
	// Listener, when set, is served instead of binding BindAddr:Port.
	Listener     net.Listener
	Handler      http.Handler
	TLSCertFile  string
	TLSKeyFile   string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
}

// GRPCListener is one gRPC server run by Run. Set exactly one of Server and
// Build.
type GRPCListener struct {
	Name     string
	BindAddr string
	Port     string
	// Listener, when set, is served instead of binding BindAddr:Port.
	Listener net.Listener
	// Server is served as soon as every listener is bound.
	Server *grpc.Server
	// Build constructs the server in the background after the port is bound,
	// so HTTP listeners (and /health) serve while construction waits on
	// external state such as TLS files. Its context is cancelled when
	// shutdown starts. A Build error stops the process.
	Build func(ctx context.Context) (*grpc.Server, error)
}

// RunSpec is everything one service process serves.
type RunSpec struct {
	Service string
	Logger  logging.Logger
	HTTP    []HTTPListener
	GRPC    []GRPCListener
	// Ready flips to draining before any listener stops. Run also adds one
	// check per Build listener that stays unhealthy until that server serves.
	Ready *monitoring.ReadinessChecker
	// OnReload callbacks run on SIGHUP, after callbacks registered earlier
	// such as RegisterEnvFileReload.
	OnReload []ReloadCallback
	// OnSignalStop hooks run when SIGINT or SIGTERM stopped Run, which is a
	// planned stop by the supervisor, with the signal that arrived. They run
	// after readiness flips to draining and before the pre-stop delay and
	// OnDrain. A listener failure or a cancelled Run context skips them.
	OnSignalStop []func(context.Context, os.Signal)
	// OnDrain hooks run after readiness flips to draining and before any
	// listener stops, for work such as ending long-lived streams that would
	// otherwise hold a graceful gRPC stop open until the shutdown timeout.
	OnDrain []func(context.Context)
	// OnShutdown hooks run after every listener has stopped.
	OnShutdown      []func(context.Context)
	ShutdownTimeout time.Duration
}

type runningHTTP struct {
	cfg HTTPListener
	lis net.Listener
	srv *http.Server
}

type runningGRPC struct {
	cfg     GRPCListener
	lis     net.Listener
	serving atomic.Bool

	mu       sync.Mutex
	srv      *grpc.Server
	stopping bool
}

// Run binds every listener before serving any of them, so a port conflict
// fails startup instead of leaving a half-started process. It serves until ctx
// is cancelled, SIGINT or SIGTERM arrives, or a listener fails; then it marks
// the instance as draining and cancels pending gRPC builds. After a signal it
// runs the OnSignalStop hooks and, for a service whose readiness pollers probe
// /ready (servicedefs ReadinessPolled), keeps serving the draining 503 for
// ReadinessPropagationDelay. It then stops HTTP and gRPC listeners
// concurrently, letting in-flight requests and RPCs finish within
// ShutdownTimeout before forcing the gRPC servers closed.
func Run(ctx context.Context, spec RunSpec) error {
	if spec.Logger == nil {
		return errors.New("server: RunSpec.Logger is required")
	}
	if len(spec.HTTP)+len(spec.GRPC) == 0 {
		return errors.New("server: at least one HTTP or gRPC listener is required")
	}
	httpRunning, grpcRunning, err := bindListeners(spec)
	if err != nil {
		return err
	}

	if spec.Ready != nil {
		for _, r := range grpcRunning {
			if r.cfg.Build == nil {
				continue
			}
			spec.Ready.AddCheck("grpc_"+r.cfg.Name, func() monitoring.CheckResult {
				if r.serving.Load() {
					return monitoring.CheckResult{Status: monitoring.StatusHealthy}
				}
				return monitoring.CheckResult{Status: monitoring.StatusUnhealthy, Message: "gRPC listener is starting"}
			})
		}
	}

	for _, fn := range spec.OnReload {
		RegisterReload(fn)
	}
	// Stop signals are captured before any listener serves, so a signal that
	// arrives while the first requests are answered still drains.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	stopReload := startReloadListener(spec.Logger, spec.Service)
	defer stopReload()

	buildCtx, cancelBuilds := context.WithCancel(context.Background())
	defer cancelBuilds()

	errCh := make(chan error, len(httpRunning)+len(grpcRunning))
	for _, r := range httpRunning {
		spec.Logger.WithFields(logging.Fields{"service": spec.Service, "listener": r.cfg.Name, "address": r.lis.Addr().String()}).Info("Starting HTTP listener")
		go func() {
			var serveErr error
			if r.cfg.TLSCertFile != "" {
				serveErr = r.srv.ServeTLS(r.lis, r.cfg.TLSCertFile, r.cfg.TLSKeyFile)
			} else {
				serveErr = r.srv.Serve(r.lis)
			}
			if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				errCh <- fmt.Errorf("HTTP listener %s (%s): %w", r.cfg.Name, r.lis.Addr(), serveErr)
			}
		}()
	}
	for _, r := range grpcRunning {
		go serveGRPC(buildCtx, spec, r, errCh)
	}

	var (
		serveErr   error
		stopSignal os.Signal
	)
	select {
	case <-ctx.Done():
	case stopSignal = <-signals:
	case serveErr = <-errCh:
	}

	if spec.Ready != nil {
		spec.Ready.MarkShuttingDown()
	}
	if stopSignal != nil {
		spec.Logger.WithFields(logging.Fields{"service": spec.Service, "signal": stopSignal.String()}).Info("Stop signal received")
	}
	spec.Logger.WithField("service", spec.Service).Info("Shutting down listeners")
	cancelBuilds()

	timeout := spec.ShutdownTimeout
	if timeout <= 0 {
		timeout = defaultShutdownTimeout
	}

	if stopSignal != nil {
		// The hooks and the pre-stop delay run before the shutdown timeout
		// starts, so a planned stop keeps its full drain budget.
		for _, hook := range spec.OnSignalStop {
			hook(context.Background(), stopSignal)
		}
		if spec.Ready != nil && servicedefs.Services[spec.Service].ReadinessPolled() {
			spec.Logger.WithFields(logging.Fields{"service": spec.Service, "delay": preStopDelay.String()}).
				Info("Serving draining readiness before stopping listeners")
			time.Sleep(preStopDelay)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for _, hook := range spec.OnDrain {
		hook(shutdownCtx)
	}

	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	if serveErr != nil {
		errs = append(errs, serveErr)
	}
	record := func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
	}
	for _, r := range httpRunning {
		wg.Go(func() {
			if err := r.srv.Shutdown(shutdownCtx); err != nil {
				_ = r.srv.Close()
				record(fmt.Errorf("shutdown HTTP listener %s: %w", r.cfg.Name, err))
			}
		})
	}
	for _, r := range grpcRunning {
		r.mu.Lock()
		r.stopping = true
		srv := r.srv
		r.mu.Unlock()
		if srv == nil {
			// The server was never built; closing the bound port is the whole stop.
			_ = r.lis.Close()
			continue
		}
		wg.Go(func() {
			DrainGRPCHealth(srv)
			stopped := make(chan struct{})
			go func() {
				srv.GracefulStop()
				close(stopped)
			}()
			select {
			case <-stopped:
			case <-shutdownCtx.Done():
				srv.Stop()
				<-stopped
				record(fmt.Errorf("gRPC listener %s did not drain within %s; remaining RPCs were cancelled", r.cfg.Name, timeout))
			}
		})
	}
	wg.Wait()

	// Cleanup still needs a usable budget after a listener exhausted its drain.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), min(timeout, 10*time.Second))
	defer cleanupCancel()
	for _, hook := range spec.OnShutdown {
		hook(cleanupCtx)
	}
	spec.Logger.WithField("service", spec.Service).Info("Listeners stopped")
	return errors.Join(errs...)
}

func serveGRPC(buildCtx context.Context, spec RunSpec, r *runningGRPC, errCh chan<- error) {
	srv := r.cfg.Server
	if r.cfg.Build != nil {
		built, err := r.cfg.Build(buildCtx)
		if err != nil {
			if buildCtx.Err() == nil {
				errCh <- fmt.Errorf("gRPC listener %s: build server: %w", r.cfg.Name, err)
			}
			return
		}
		if built == nil {
			errCh <- fmt.Errorf("gRPC listener %s: build returned no server", r.cfg.Name)
			return
		}
		srv = built
	}

	r.mu.Lock()
	if r.stopping {
		r.mu.Unlock()
		DrainGRPCHealth(srv)
		srv.Stop()
		return
	}
	r.srv = srv
	r.mu.Unlock()

	spec.Logger.WithFields(logging.Fields{"service": spec.Service, "listener": r.cfg.Name, "address": r.lis.Addr().String()}).Info("Starting gRPC listener")
	r.serving.Store(true)
	if serveErr := srv.Serve(r.lis); serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
		errCh <- fmt.Errorf("gRPC listener %s (%s): %w", r.cfg.Name, r.lis.Addr(), serveErr)
	}
}

func bindListeners(spec RunSpec) ([]runningHTTP, []*runningGRPC, error) {
	var opened []net.Listener
	fail := func(err error) ([]runningHTTP, []*runningGRPC, error) {
		for _, lis := range opened {
			_ = lis.Close()
		}
		return nil, nil, err
	}

	httpRunning := make([]runningHTTP, 0, len(spec.HTTP))
	for _, cfg := range spec.HTTP {
		if cfg.Handler == nil {
			return fail(fmt.Errorf("server: HTTP listener %q has no handler", cfg.Name))
		}
		if (cfg.TLSCertFile == "") != (cfg.TLSKeyFile == "") {
			return fail(fmt.Errorf("server: HTTP listener %q requires both certificate and key files", cfg.Name))
		}
		lis, err := listen(cfg.Listener, cfg.BindAddr, cfg.Port)
		if err != nil {
			return fail(fmt.Errorf("server: HTTP listener %q: %w", cfg.Name, err))
		}
		opened = append(opened, lis)
		httpRunning = append(httpRunning, runningHTTP{cfg: cfg, lis: lis, srv: &http.Server{
			Handler:      cfg.Handler,
			ReadTimeout:  durationOr(cfg.ReadTimeout, defaultReadTimeout),
			WriteTimeout: durationOr(cfg.WriteTimeout, defaultWriteTimeout),
			IdleTimeout:  durationOr(cfg.IdleTimeout, defaultIdleTimeout),
		}})
	}

	grpcRunning := make([]*runningGRPC, 0, len(spec.GRPC))
	for _, cfg := range spec.GRPC {
		if (cfg.Server == nil) == (cfg.Build == nil) {
			return fail(fmt.Errorf("server: gRPC listener %q needs exactly one of Server or Build", cfg.Name))
		}
		lis, err := listen(cfg.Listener, cfg.BindAddr, cfg.Port)
		if err != nil {
			return fail(fmt.Errorf("server: gRPC listener %q: %w", cfg.Name, err))
		}
		opened = append(opened, lis)
		grpcRunning = append(grpcRunning, &runningGRPC{cfg: cfg, lis: lis})
	}
	return httpRunning, grpcRunning, nil
}

func listen(existing net.Listener, bindAddr, port string) (net.Listener, error) {
	if existing != nil {
		return existing, nil
	}
	if port == "" {
		return nil, errors.New("no port configured")
	}
	var lc net.ListenConfig
	return lc.Listen(context.Background(), "tcp", net.JoinHostPort(bindAddr, port))
}

func durationOr(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}
