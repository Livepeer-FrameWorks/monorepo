package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"frameworks/api_realtime/internal/handlers"
	"frameworks/api_realtime/internal/websocket"
	"frameworks/pkg/config"
	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
)

func main() {
	// Setup logger
	logger := logging.NewLoggerWithService("signalman")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Signalman (WebSocket Hub)")

	// Initialize WebSocket hub
	hub := websocket.NewHub(logger)
	go hub.Run()

	// Initialize handlers
	signalmanHandlers := handlers.NewSignalmanHandlers(hub, nil, logger)

	// Setup Kafka consumer
	brokers := strings.Split(config.GetEnv("KAFKA_BROKERS", "localhost:9092"), ",")
	groupID := config.GetEnv("KAFKA_GROUP_ID", "signalman-group")
	clusterID := config.GetEnv("KAFKA_CLUSTER_ID", "frameworks")
	clientID := config.GetEnv("KAFKA_CLIENT_ID", "signalman")
	topicsEnv := config.GetEnv("KAFKA_TOPICS", "analytics_events")
	topics := strings.Split(topicsEnv, ",")

	consumer, err := kafka.NewConsumer(brokers, groupID, clusterID, clientID, logger, signalmanHandlers)
	if err != nil {
		logger.WithError(err).Fatal("Failed to initialize Kafka consumer")
	}
	defer consumer.Close()

	// Update handlers with consumer
	signalmanHandlers = handlers.NewSignalmanHandlers(hub, consumer, logger)

	// Subscribe to topics
	if err := consumer.Subscribe(topics); err != nil {
		logger.WithError(err).Fatal("Failed to subscribe to topics")
	}

	// Start Kafka consumer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := consumer.Start(ctx); err != nil {
			logger.WithError(err).Error("Kafka consumer error")
		}
	}()

	// Setup Gin router
	if config.GetEnv("GIN_MODE", "") == "release" {
		middleware.SetGinMode("release")
	}

	router := middleware.NewEngine()
	router.Use(middleware.RequestIDMiddleware())
	router.Use(middleware.LoggingMiddleware(logger))
	router.Use(middleware.RecoveryMiddleware(logger))
	router.Use(middleware.CORSMiddleware())

	// Setup WebSocket routes
	router.GET("/ws/streams", signalmanHandlers.HandleWebSocketStreams)
	router.GET("/ws/analytics", signalmanHandlers.HandleWebSocketAnalytics)
	router.GET("/ws/system", signalmanHandlers.HandleWebSocketSystem)
	router.GET("/ws", signalmanHandlers.HandleWebSocketAll)

	// Setup HTTP routes
	router.GET("/health", signalmanHandlers.HandleHealth)
	router.GET("/metrics", signalmanHandlers.HandleMetrics)

	// Admin routes with service auth
	admin := router.Group("/admin")
	admin.Use(middleware.ServiceAuthMiddleware(config.GetEnv("SERVICE_TOKEN", "")))
	router.NoRoute(signalmanHandlers.HandleNotFound)

	// Setup HTTP server
	port := config.GetEnv("PORT", "18009")
	server := &http.Server{
		Addr:         ":" + port,
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Start server
	go func() {
		logger.WithFields(logging.Fields{
			"port":    port,
			"service": "signalman",
		}).Info("Starting HTTP server")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.WithError(err).Fatal("Failed to start HTTP server")
		}
	}()

	// Wait for interrupt
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down Signalman...")

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.WithError(err).Error("Server forced to shutdown")
	}

	logger.Info("Server exiting")
}
