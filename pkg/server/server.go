package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"frameworks/pkg/config"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/pkg/monitoring"
)

// Config represents server configuration
type Config struct {
	Port         string
	ServiceName  string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
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
		Addr:         ":" + cfg.Port,
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

		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.WithError(err).Fatal("Failed to start server")
		}
	}()

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

	// Add common middleware
	router.Use(middleware.RequestIDMiddleware())
	router.Use(middleware.LoggingMiddleware(logger))
	router.Use(middleware.RecoveryMiddleware(logger))
	router.Use(middleware.CORSMiddleware())

	// Add metrics middleware
	router.Use(metricsCollector.MetricsMiddleware())

	// Register real monitoring endpoints
	router.GET("/health", healthChecker.Handler())
	router.GET("/metrics", metricsCollector.Handler())

	return router
}
