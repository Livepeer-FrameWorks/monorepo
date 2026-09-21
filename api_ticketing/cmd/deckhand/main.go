package main

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_ticketing/internal/appconfig"
	"frameworks/api_ticketing/internal/chatwoot"
	deckhandgrpc "frameworks/api_ticketing/internal/grpc"
	"frameworks/api_ticketing/internal/handlers"
	decklogclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	purserclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/purser"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	qmevents "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	deckhandpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/deckhand"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/reflection"
)

func main() {
	if version.HandleCLI() {
		return
	}

	logger := logging.NewLoggerWithService("deckhand")
	config.LoadEnv(logger)

	logger.Info("Starting Deckhand (Support Messaging API)")

	configOptions := config.Options{Service: "deckhand", Logger: logger}
	cfg, err := config.Load[appconfig.Deckhand](configOptions)
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}
	cfg.ApplyLogLevel(logger)

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("deckhand", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("deckhand", version.Version, version.GitCommit)

	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"CHATWOOT_HOST": cfg.ChatwootHost,
	}))

	// Readiness carries no dependency checks; /health/chatwoot reports
	// Chatwoot reachability separately. It reports draining during shutdown.
	readiness := monitoring.NewReadinessChecker("deckhand", version.Version)

	// Create handler metrics. The CreateConversation gRPC method is
	// already covered by grpc_requests_total{method="CreateConversation"};
	// a separate conversations_created_total counter would only rename
	// the same axis.
	handlerMetrics := &handlers.Metrics{
		WebhooksReceived: metricsCollector.NewCounter("webhooks_received_total", "Chatwoot webhooks received", []string{"event_type"}),
		EnrichmentCalls:  metricsCollector.NewCounter("enrichment_calls_total", "Enrichment service calls", []string{"service", "status"}),
		ChatwootAPICalls: metricsCollector.NewCounter("chatwoot_api_calls_total", "Chatwoot API calls", []string{"endpoint", "status"}),
		MessagesSent:     metricsCollector.NewCounter("messages_sent_total", "Messages sent via gRPC", []string{"status"}),
	}

	// Create gRPC server metrics
	grpcMetrics := &deckhandgrpc.ServerMetrics{
		GRPCRequests: metricsCollector.NewCounter("grpc_requests_total", "Total gRPC requests", []string{"method", "status"}),
		GRPCDuration: metricsCollector.NewHistogram("grpc_request_duration_seconds", "gRPC request duration", []string{"method"}, nil),
	}

	// Create Quartermaster gRPC client (for tenant info)
	qmClient, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:      cfg.QuartermasterGRPCAddr,
		Timeout:       10 * time.Second,
		Logger:        logger,
		ServiceToken:  cfg.ServiceToken,
		AllowInsecure: cfg.AllowInsecure,
		CACertFile:    cfg.CAPath,
		ServerName:    cfg.QuartermasterGRPCTLSServerName,
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Quartermaster gRPC client")
	}
	defer func() { _ = qmClient.Close() }()

	// Service-event producer client, sharing the Quartermaster connection.
	// Kept separate so the base client stays free of ipcpb.
	qmEventsClient := qmevents.New(qmClient.Conn())

	// Create Purser gRPC client (for billing info)
	purserClient, err := purserclient.NewGRPCClient(purserclient.GRPCConfig{
		GRPCAddr:      cfg.PurserGRPCAddr,
		Timeout:       10 * time.Second,
		Logger:        logger,
		ServiceToken:  cfg.ServiceToken,
		AllowInsecure: cfg.AllowInsecure,
		CACertFile:    cfg.CAPath,
		ServerName:    cfg.PurserGRPCTLSServerName,
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Purser gRPC client")
	}
	defer func() { _ = purserClient.Close() }()

	// Create Decklog gRPC client (for real-time events)
	decklogClient, err := decklogclient.NewBatchedClient(decklogclient.BatchedClientConfig{
		Target:        cfg.DecklogGRPCAddr,
		AllowInsecure: cfg.AllowInsecure,
		CACertFile:    cfg.CAPath,
		ServerName:    cfg.DecklogGRPCTLSServerName,
		Timeout:       5 * time.Second,
		Source:        "deckhand",
		ServiceToken:  cfg.ServiceToken,
		ClusterID:     cfg.ClusterID,
		SourceRegion:  cfg.Region,
	}, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Decklog gRPC client")
	}
	defer func() { _ = decklogClient.Close() }()

	// Build Chatwoot API URL
	chatwootBaseURL := cfg.ChatwootBaseURL()
	chatwootClient := chatwoot.NewClient(chatwoot.Config{
		BaseURL:   chatwootBaseURL,
		APIToken:  cfg.ChatwootAPIToken,
		AccountID: cfg.ChatwootAccountID,
		InboxID:   cfg.ChatwootInboxID,
	})

	var redisClient *redis.Client
	if cfg.RedisAddr != "" {
		redisClient = redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := redisClient.Ping(ctx).Err(); err != nil {
			logger.WithError(err).Warn("Failed to connect to Redis; webhook deduplication disabled")
			redisClient = nil
		}
		cancel()
	}

	// Initialize HTTP handlers
	deps := handlers.Dependencies{
		Logger:              logger,
		Metrics:             handlerMetrics,
		Quartermaster:       qmClient,
		QuartermasterEvents: qmEventsClient,
		Purser:              purserClient,
		Decklog:             decklogClient,
		Redis:               redisClient,
		ChatwootBaseURL:     chatwootBaseURL,
		ChatwootToken:       cfg.ChatwootAPIToken,
	}
	handlers.Init(deps)

	// Setup gRPC server
	deckhandServer := deckhandgrpc.NewServer(deckhandgrpc.Config{
		Logger:          logger,
		Metrics:         grpcMetrics,
		ChatwootBaseURL: chatwootBaseURL,
		ChatwootToken:   cfg.ChatwootAPIToken,
		ChatwootAccount: cfg.ChatwootAccountID,
		ChatwootInbox:   cfg.ChatwootInboxID,
		Quartermaster:   qmClient,
		Purser:          purserClient,
	})

	// HTTP serves the Chatwoot webhook plus health, readiness, and metrics.
	router := server.NewServiceRouter(server.RouterSpec{
		Service:            "deckhand",
		Logger:             logger,
		Health:             healthChecker,
		Ready:              readiness,
		Metrics:            metricsCollector,
		Runtime:            cfg.HTTPRuntime,
		DebugToken:         cfg.ServiceToken,
		DebugConfig:        func() any { return cfg },
		DebugConfigOptions: configOptions,
	})

	// Webhook routes (no auth - Chatwoot calls these)
	webhooks := router.Group("/webhooks")
	{
		if cfg.WebhookRateLimitPerMin > 0 {
			limiter := handlers.NewWebhookRateLimiter(cfg.WebhookRateLimitPerMin, time.Minute, 10*time.Minute)
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Best-effort service registration in Quartermaster
	go registerWithQuartermaster(ctx, cfg, qmClient, logger)

	if runErr := server.Run(ctx, server.RunSpec{
		Service: "deckhand",
		Logger:  logger,
		Ready:   readiness,
		HTTP:    []server.HTTPListener{{Name: "http", Port: cfg.HTTPListenPort(), Handler: router}},
		// The gRPC server is built after the port is bound, so HTTP health
		// and the Chatwoot webhook serve while the TLS files are still being synced.
		GRPC: []server.GRPCListener{{Name: "grpc", Port: cfg.GRPCPort, Build: func(ctx context.Context) (*grpc.Server, error) {
			return newGRPCServer(ctx, cfg, logger, deckhandServer)
		}}},
	}); runErr != nil {
		logger.WithError(runErr).Fatal("Server exited with error")
	}
}

// newGRPCServer builds the Deckhand gRPC server. It waits up to two minutes
// for the TLS files, and stops waiting when ctx ends.
func newGRPCServer(ctx context.Context, cfg *appconfig.Deckhand, logger logging.Logger, deckhandServer deckhandpb.DeckhandServiceServer) (*grpc.Server, error) {
	metadataPolicy, policyErr := middleware.ParseMetadataPolicy(cfg.MetadataPolicy)
	if policyErr != nil {
		return nil, policyErr
	}
	authInterceptor := middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
		ServiceToken:   cfg.ServiceToken,
		JWTSecret:      []byte(cfg.JWTSecret),
		MetadataPolicy: metadataPolicy,
		Logger:         logger,
		SkipMethods: []string{
			"/grpc.health.v1.Health/Check",
			"/grpc.health.v1.Health/Watch",
		},
	})

	serverOpts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(
			grpcutil.SanitizeUnaryServerInterceptor(),
			authInterceptor,
			middleware.GRPCLoggingInterceptor(logger),
		),
	}
	tlsCfg := grpcutil.ServerTLSConfig{
		CertFile:      cfg.CertPath,
		KeyFile:       cfg.KeyPath,
		AllowInsecure: cfg.AllowInsecure,
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := grpcutil.WaitForServerTLSFiles(waitCtx, tlsCfg, logger); err != nil {
		return nil, fmt.Errorf("wait for Deckhand gRPC TLS files: %w", err)
	}
	tlsOpt, err := grpcutil.ServerTLS(tlsCfg, logger)
	if err != nil {
		return nil, fmt.Errorf("configure Deckhand gRPC TLS: %w", err)
	}
	if tlsOpt != nil {
		serverOpts = append(serverOpts, tlsOpt)
	}
	grpcSrv := grpc.NewServer(serverOpts...)
	deckhandpb.RegisterDeckhandServiceServer(grpcSrv, deckhandServer)

	// Register gRPC health checking service
	hs := health.NewServer()
	server.RegisterHealthServer(grpcSrv, hs)
	reflection.Register(grpcSrv)
	return grpcSrv, nil
}

// registerWithQuartermaster registers the HTTP port with the /health endpoint
// from servicedefs.
func registerWithQuartermaster(ctx context.Context, cfg *appconfig.Deckhand, qmClient qmbootstrap.BootstrapClient, logger logging.Logger) {
	req, err := qmbootstrap.NewServiceRequest(qmbootstrap.ServiceRegistration{
		ServiceType:   "deckhand",
		Port:          cfg.HTTPListenPort(),
		AdvertiseHost: cfg.AdvertiseHost,
		ClusterID:     cfg.ClusterID,
		NodeID:        cfg.NodeID,
	})
	if err != nil {
		logger.WithError(err).Warn("Quartermaster bootstrap skipped")
		return
	}
	if _, err := qmbootstrap.BootstrapServiceWithRetry(ctx, qmClient, req, logger, qmbootstrap.DefaultRetryConfig("deckhand")); err != nil {
		logger.WithError(err).Warn("Quartermaster bootstrap (deckhand) failed")
	} else {
		logger.Info("Quartermaster bootstrap (deckhand) ok")
	}
}
