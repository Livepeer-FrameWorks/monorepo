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

// SetupRouter creates a Gin router with common middleware
func SetupRouter(logger logging.Logger) *gin.Engine {
	return SetupRouterWithService(logger, "unknown")
}

// SetupRouterWithService creates a Gin router with common middleware and service name
func SetupRouterWithService(logger logging.Logger, serviceName string) *gin.Engine {
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

	// Health check endpoint
	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":  "healthy",
			"service": serviceName,
			"version": "1.0.0",
		})
	})

	// Basic metrics endpoint for Prometheus
	router.GET("/metrics", func(c *gin.Context) {
		c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		c.String(http.StatusOK, fmt.Sprintf("# HELP %s_up Service availability\n# TYPE %s_up gauge\n%s_up 1\n",
			serviceName, serviceName, serviceName))
	})

	return router
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
