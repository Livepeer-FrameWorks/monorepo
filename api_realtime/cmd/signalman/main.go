package main

import (
	"context"
	"strings"

	"frameworks/api_realtime/internal/handlers"
	"frameworks/api_realtime/internal/metrics"
	"frameworks/api_realtime/internal/websocket"
	qmapi "frameworks/pkg/api/quartermaster"
	"frameworks/pkg/auth"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	"frameworks/pkg/server"
	"frameworks/pkg/version"
	"time"
)

func main() {
	// Setup logger
	logger := logging.NewLoggerWithService("signalman")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Signalman (WebSocket Hub)")

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("signalman", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("signalman", version.Version, version.GitCommit)

	// Create custom metrics
	serviceMetrics := &metrics.Metrics{
		HubConnections:     metricsCollector.NewGauge("websocket_hub_connections_active", "Active WebSocket hub connections", []string{"channel"}),
		HubMessages:        metricsCollector.NewCounter("websocket_hub_messages_total", "WebSocket hub messages", []string{"channel", "direction"}),
		EventsPublished:    metricsCollector.NewCounter("realtime_events_published_total", "Real-time events published", []string{"event_type", "channel"}),
		MessageDeliveryLag: metricsCollector.NewHistogram("message_delivery_lag_seconds", "Message delivery latency", []string{"channel", "type"}, nil),
	}

	// Create Kafka metrics
	serviceMetrics.KafkaMessages, serviceMetrics.KafkaDuration, serviceMetrics.KafkaLag = metricsCollector.CreateKafkaMetrics()

	// Initialize WebSocket hub with unified metrics
	hub := websocket.NewHub(logger, serviceMetrics)
	go hub.Run()

	// Initialize handlers with unified metrics
	signalmanHandlers := handlers.NewSignalmanHandlers(hub, nil, logger, serviceMetrics)

	// Setup Kafka consumer
	brokers := strings.Split(config.RequireEnv("KAFKA_BROKERS"), ",")
	groupID := config.GetEnv("KAFKA_GROUP_ID", "signalman-group")
	clusterID := config.RequireEnv("KAFKA_CLUSTER_ID")
	clientID := config.GetEnv("KAFKA_CLIENT_ID", "signalman")
	topicsEnv := config.GetEnv("KAFKA_TOPICS", "analytics_events")
	topics := strings.Split(topicsEnv, ",")
	serviceToken := config.RequireEnv("SERVICE_TOKEN")
	quartermasterURL := config.RequireEnv("QUARTERMASTER_URL")

	consumer, err := kafka.NewConsumer(brokers, groupID, clusterID, clientID, logger, signalmanHandlers)
	if err != nil {
		logger.WithError(err).Fatal("Failed to initialize Kafka consumer")
	}
	defer consumer.Close()

	// Update handlers with consumer
	signalmanHandlers = handlers.NewSignalmanHandlers(hub, consumer, logger, serviceMetrics)

	// Subscribe to topics
	if err := consumer.Subscribe(topics); err != nil {
		logger.WithError(err).Fatal("Failed to subscribe to topics")
	}

	// Add health checks
	healthChecker.AddCheck("kafka", monitoring.KafkaConsumerHealthCheck(consumer.GetClient()))
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"KAFKA_BROKERS": strings.Join(brokers, ","),
		"KAFKA_TOPICS":  topicsEnv,
	}))

	// Start Kafka consumer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := consumer.Start(ctx); err != nil {
			logger.WithError(err).Error("Kafka consumer error")
		}
	}()

	// Setup router with unified monitoring
	router := server.SetupServiceRouter(logger, "signalman", healthChecker, metricsCollector)

	// Setup WebSocket routes
	router.GET("/ws/streams", signalmanHandlers.HandleWebSocketStreams)
	router.GET("/ws/analytics", signalmanHandlers.HandleWebSocketAnalytics)
	router.GET("/ws/system", signalmanHandlers.HandleWebSocketSystem)
	router.GET("/ws", signalmanHandlers.HandleWebSocketAll)

	// Admin routes with service auth
	admin := router.Group("/admin")
	admin.Use(auth.ServiceAuthMiddleware(serviceToken))
	router.NoRoute(signalmanHandlers.HandleNotFound)

	// Start server with graceful shutdown
	serverConfig := server.DefaultConfig("signalman", "18009")
	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}

	// Best-effort service registration in Quartermaster
	go func() {
		qc := qmclient.NewClient(qmclient.Config{BaseURL: quartermasterURL, ServiceToken: serviceToken, Logger: logger})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := qc.BootstrapService(ctx, &qmapi.BootstrapServiceRequest{Type: "signalman", Version: version.Version, Protocol: "http", HealthEndpoint: func() *string { s := "/health"; return &s }(), Port: 18009}); err != nil {
			logger.WithError(err).Warn("Quartermaster bootstrap (signalman) failed")
		} else {
			logger.Info("Quartermaster bootstrap (signalman) ok")
		}
	}()
}
