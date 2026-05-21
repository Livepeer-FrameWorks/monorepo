package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
)

// ReloadCallback is fired when Start receives SIGHUP. Callbacks re-read
// file-based config or rotate TLS material; they must not block (run any
// work in a goroutine) and should return any error for logging. Returning
// an error does not abort subsequent callbacks — every registered hook
// runs on every SIGHUP.
type ReloadCallback func() error

var (
	reloadMu  sync.Mutex
	reloadFns []ReloadCallback
)

// RegisterReload appends a callback that fires whenever the process
// receives SIGHUP while Start is running. Registration order is preserved;
// callbacks run sequentially. Safe to call from init() or from goroutines
// after Start blocks on its quit channel.
//
// Even when no callback is registered, Start still installs a SIGHUP
// listener — that's what neuters Go's default-terminate disposition for
// the signal cluster-wide, so `systemctl reload <service>` is a true
// no-op rather than a kill for every Go service.
func RegisterReload(fn ReloadCallback) {
	if fn == nil {
		return
	}
	reloadMu.Lock()
	reloadFns = append(reloadFns, fn)
	reloadMu.Unlock()
}

// snapshotReloadFns returns a copy of the current callback list so the
// reload loop can iterate without holding the mutex.
func snapshotReloadFns() []ReloadCallback {
	reloadMu.Lock()
	defer reloadMu.Unlock()
	out := make([]ReloadCallback, len(reloadFns))
	copy(out, reloadFns)
	return out
}

// startReloadListener installs a SIGHUP handler that dispatches every
// signal to the current snapshot of registered ReloadCallbacks. Callbacks
// run sequentially in registration order; a returned error is logged and
// does not abort the remaining callbacks for that signal.
//
// Returns a stop function that detaches the signal handler and waits for
// the dispatch goroutine to drain. Safe to call from tests directly so the
// SIGHUP-doesn't-terminate property can be verified without spinning up a
// real HTTP server.
func startReloadListener(logger logging.Logger, serviceName string) func() {
	reloadCh := make(chan os.Signal, 1)
	signal.Notify(reloadCh, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range reloadCh {
			for _, fn := range snapshotReloadFns() {
				if err := fn(); err != nil {
					logger.WithError(err).WithField("service", serviceName).
						Warn("reload callback returned error")
				}
			}
		}
	}()
	return func() {
		signal.Stop(reloadCh)
		close(reloadCh)
		<-done
	}
}

// Config represents server configuration
type Config struct {
	Port         string
	BindAddr     string // defaults to "" (all interfaces); set "127.0.0.1" for local-only
	ServiceName  string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
	TLSCertFile  string
	TLSKeyFile   string
}

type trailingSlashFallbackKey struct{}

// HandleOptionalTrailingSlash registers a route for both slash spellings.
func HandleOptionalTrailingSlash(routes gin.IRoutes, method, relativePath string, handlers ...gin.HandlerFunc) {
	routes.Handle(method, relativePath, handlers...)
	if alternate := alternateTrailingSlashPath(relativePath); alternate != relativePath {
		routes.Handle(method, alternate, handlers...)
	}
}

// DefaultConfig returns default server configuration
func DefaultConfig(serviceName, defaultPort string) Config {
	return Config{
		Port:         config.GetEnv("PORT", defaultPort),
		ServiceName:  serviceName,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
}

// Start starts the HTTP server with graceful shutdown
func Start(cfg Config, router *gin.Engine, logger logging.Logger) error {
	srv := &http.Server{
		Addr:         cfg.BindAddr + ":" + cfg.Port,
		Handler:      router,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}

	// Start server in a goroutine
	go func() {
		logger.WithFields(logging.Fields{
			"port":    cfg.Port,
			"service": cfg.ServiceName,
		}).Info("Starting HTTP server")

		var err error
		if cfg.TLSCertFile != "" || cfg.TLSKeyFile != "" {
			if cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" {
				logger.Fatal("HTTP TLS requires both certificate and key files")
			}
			err = srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			logger.WithError(err).Fatal("Failed to start server")
		}
	}()

	// Reload listener — SIGHUP fires every registered ReloadCallback.
	// Installing the listener also neuters Go's default-terminate
	// disposition for SIGHUP: with no callbacks registered, the signal
	// is silently consumed and the process keeps running.
	stopReload := startReloadListener(logger, cfg.ServiceName)
	defer stopReload()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.WithField("service", cfg.ServiceName).Info("Shutting down server...")

	// Graceful shutdown with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server forced to shutdown: %w", err)
	}

	logger.WithField("service", cfg.ServiceName).Info("Server stopped")
	return nil
}

// SetupServiceRouter creates a fully configured router with monitoring
func SetupServiceRouter(
	logger logging.Logger,
	serviceName string,
	healthChecker *monitoring.HealthChecker,
	metricsCollector *monitoring.MetricsCollector,
) *gin.Engine {
	// Set Gin mode based on environment
	if config.GetEnv("GIN_MODE", "debug") == "release" {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	router.RedirectTrailingSlash = false

	// Parse CORS allowed origins from environment
	var allowedOrigins []string
	if originsStr := config.GetEnv("ALLOWED_ORIGINS", ""); originsStr != "" {
		for o := range strings.SplitSeq(originsStr, ",") {
			if trimmed := strings.TrimSpace(o); trimmed != "" {
				allowedOrigins = append(allowedOrigins, trimmed)
			}
		}
	}
	devMode := config.GetEnv("GIN_MODE", "debug") != "release"

	// Add common middleware
	router.Use(middleware.RequestIDMiddleware())
	router.Use(middleware.LoggingMiddleware(logger))
	router.Use(middleware.RecoveryMiddleware(logger))
	router.Use(middleware.CORSMiddleware(allowedOrigins, devMode))

	// Add metrics middleware
	router.Use(metricsCollector.MetricsMiddleware())

	// Register real monitoring endpoints
	healthHandler := healthChecker.Handler()
	router.GET("/health", healthHandler)
	router.HEAD("/health", healthHandler)
	router.GET("/metrics", metricsCollector.Handler())
	router.NoRoute(func(c *gin.Context) {
		retryAlternateTrailingSlashPath(router, c)
	})

	return router
}

func retryAlternateTrailingSlashPath(router *gin.Engine, c *gin.Context) {
	if c.Request == nil || c.Request.URL == nil {
		return
	}
	if c.Request.Context().Value(trailingSlashFallbackKey{}) == true {
		return
	}
	alternate := alternateTrailingSlashPath(c.Request.URL.Path)
	if alternate == c.Request.URL.Path {
		return
	}
	c.Request.URL.Path = alternate
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), trailingSlashFallbackKey{}, true))
	c.Status(http.StatusOK)
	router.HandleContext(c)
}

func alternateTrailingSlashPath(path string) string {
	switch {
	case path == "" || path == "/":
		return path
	case strings.HasSuffix(path, "/"):
		return strings.TrimSuffix(path, "/")
	default:
		return path + "/"
	}
}
