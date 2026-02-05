package main

import (
	"context"
	"fmt"
	"net"
	"time"

	"frameworks/api_ticketing/internal/chatwoot"
	deckhandgrpc "frameworks/api_ticketing/internal/grpc"
	"frameworks/api_ticketing/internal/handlers"
	decklogclient "frameworks/pkg/clients/decklog"
	purserclient "frameworks/pkg/clients/purser"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/grpcutil"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/pkg/monitoring"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/server"
	"frameworks/pkg/version"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	logger := logging.NewLoggerWithService("deckhand")
	config.LoadEnv(logger)

	logger.Info("Starting Deckhand (Support Messaging API)")

	// Required config
	serviceToken := config.RequireEnv("SERVICE_TOKEN")
	jwtSecret := config.GetEnv("JWT_SECRET", "")
	chatwootAPIToken := config.RequireEnv("CHATWOOT_API_TOKEN")
	chatwootHost := config.GetEnv("CHATWOOT_HOST", "chatwoot")
	chatwootPort := config.GetEnv("CHATWOOT_PORT", "3000")
	chatwootAccountID := config.GetEnvInt("CHATWOOT_ACCOUNT_ID", 1)
	chatwootInboxID := config.GetEnvInt("CHATWOOT_INBOX_ID", 1)

	// gRPC addresses for dependencies
	quartermasterGRPCAddr := config.GetEnv("QUARTERMASTER_GRPC_ADDR", "quartermaster:19002")
	purserGRPCAddr := config.GetEnv("PURSER_GRPC_ADDR", "purser:19003")
	decklogGRPCAddr := config.GetEnv("DECKLOG_GRPC_ADDR", "decklog:18006")

	// Ports
	httpPort := config.GetEnv("DECKHAND_PORT", "18015")
	grpcPort := config.GetEnv("DECKHAND_GRPC_PORT", "19006")
	webhookLimitPerMin := config.GetEnvInt("DECKHAND_WEBHOOK_RATE_LIMIT_PER_MIN", 600)

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("deckhand", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("deckhand", version.Version, version.GitCommit)

	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"CHATWOOT_HOST": chatwootHost,
	}))

	// Create handler metrics
	handlerMetrics := &handlers.Metrics{
		WebhooksReceived:     metricsCollector.NewCounter("webhooks_received_total", "Chatwoot webhooks received", []string{"event_type"}),
		EnrichmentCalls:      metricsCollector.NewCounter("enrichment_calls_total", "Enrichment service calls", []string{"service", "status"}),
		ChatwootAPICalls:     metricsCollector.NewCounter("chatwoot_api_calls_total", "Chatwoot API calls", []string{"endpoint", "status"}),
		MessagesSent:         metricsCollector.NewCounter("messages_sent_total", "Messages sent via gRPC", []string{"status"}),
		ConversationsCreated: metricsCollector.NewCounter("conversations_created_total", "Conversations created", []string{"status"}),
	}

	// Create gRPC server metrics
	grpcMetrics := &deckhandgrpc.ServerMetrics{
		GRPCRequests: metricsCollector.NewCounter("grpc_requests_total", "Total gRPC requests", []string{"method", "status"}),
		GRPCDuration: metricsCollector.NewHistogram("grpc_request_duration_seconds", "gRPC request duration", []string{"method"}, nil),
	}

	// Create Quartermaster gRPC client (for tenant info)
	qmClient, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:     quartermasterGRPCAddr,
		Timeout:      10 * time.Second,
		Logger:       logger,
		ServiceToken: serviceToken,
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Quartermaster gRPC client")
	}
	defer func() { _ = qmClient.Close() }()

	// Create Purser gRPC client (for billing info)
	purserClient, err := purserclient.NewGRPCClient(purserclient.GRPCConfig{
		GRPCAddr:     purserGRPCAddr,
		Timeout:      10 * time.Second,
		Logger:       logger,
		ServiceToken: serviceToken,
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Purser gRPC client")
	}
	defer func() { _ = purserClient.Close() }()

	// Create Decklog gRPC client (for real-time events)
	decklogClient, err := decklogclient.NewBatchedClient(decklogclient.BatchedClientConfig{
		Target:        decklogGRPCAddr,
		AllowInsecure: true,
		Timeout:       5 * time.Second,
		Source:        "deckhand",
		ServiceToken:  serviceToken,
	}, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Decklog gRPC client")
	}
	defer func() { _ = decklogClient.Close() }()

	// Build Chatwoot API URL
	chatwootBaseURL := fmt.Sprintf("http://%s:%s", chatwootHost, chatwootPort)
	chatwootClient := chatwoot.NewClient(chatwoot.Config{
		BaseURL:   chatwootBaseURL,
		APIToken:  chatwootAPIToken,
		AccountID: chatwootAccountID,
		InboxID:   chatwootInboxID,
	})

	redisAddr := config.GetEnv("REDIS_ADDR", "")
	var redisClient *redis.Client
	if redisAddr != "" {
		redisClient = redis.NewClient(&redis.Options{Addr: redisAddr})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := redisClient.Ping(ctx).Err(); err != nil {
			logger.WithError(err).Warn("Failed to connect to Redis; webhook deduplication disabled")
			redisClient = nil
		}
		cancel()
	}

	// Initialize HTTP handlers
	deps := handlers.Dependencies{
		Logger:          logger,
		Metrics:         handlerMetrics,
		Quartermaster:   qmClient,
		Purser:          purserClient,
		Decklog:         decklogClient,
		Redis:           redisClient,
		ChatwootBaseURL: chatwootBaseURL,
		ChatwootToken:   chatwootAPIToken,
	}
	handlers.Init(deps)

	// Setup gRPC server
	deckhandServer := deckhandgrpc.NewServer(deckhandgrpc.Config{
		Logger:          logger,
		Metrics:         grpcMetrics,
		ChatwootBaseURL: chatwootBaseURL,
		ChatwootToken:   chatwootAPIToken,
		ChatwootAccount: chatwootAccountID,
		ChatwootInbox:   chatwootInboxID,
		Quartermaster:   qmClient,
		Purser:          purserClient,
	})

	// Create gRPC auth interceptor
	authInterceptor := middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
		ServiceToken: serviceToken,
		JWTSecret:    []byte(jwtSecret),
		Logger:       logger,
		SkipMethods: []string{
			"/grpc.health.v1.Health/Check",
			"/grpc.health.v1.Health/Watch",
		},
	})

	// Start gRPC server in goroutine
	go func() {
		grpcLis, err := net.Listen("tcp", ":"+grpcPort)
		if err != nil {
			logger.WithError(err).Fatal("Failed to listen on gRPC port")
		}

		grpcSrv := grpc.NewServer(
			grpc.ChainUnaryInterceptor(
				grpcutil.SanitizeUnaryServerInterceptor(),
				authInterceptor,
				middleware.GRPCLoggingInterceptor(logger),
			),
		)
		pb.RegisterDeckhandServiceServer(grpcSrv, deckhandServer)

		// Register gRPC health checking service
		hs := health.NewServer()
		grpc_health_v1.RegisterHealthServer(grpcSrv, hs)

		logger.WithField("port", grpcPort).Info("Starting gRPC server")
		if err := grpcSrv.Serve(grpcLis); err != nil {
			logger.WithError(err).Fatal("gRPC server failed")
		}
	}()

	// Setup HTTP router for webhooks (SetupServiceRouter adds /health and /metrics)
	router := server.SetupServiceRouter(logger, "deckhand", healthChecker, metricsCollector)

	// Webhook routes (no auth - Chatwoot calls these)
	webhooks := router.Group("/webhooks")
	{
		if webhookLimitPerMin > 0 {
			limiter := handlers.NewWebhookRateLimiter(webhookLimitPerMin, time.Minute, 10*time.Minute)
			webhooks.Use(handlers.WebhookRateLimitMiddleware(limiter))
		}
		webhooks.POST("/chatwoot", handlers.HandleChatwootWebhook)
	}

	// Health endpoint for chatwoot connectivity
	router.GET("/health/chatwoot", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		if err := chatwootClient.Ping(ctx); err != nil {
			logger.WithError(err).Warn("Chatwoot health check failed")
			c.JSON(503, gin.H{"status": "unhealthy"})
			return
		}
		c.JSON(200, gin.H{"status": "ok"})
	})

	// Start HTTP server with graceful shutdown
	serverConfig := server.DefaultConfig("deckhand", httpPort)
	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("HTTP server failed")
	}
}
