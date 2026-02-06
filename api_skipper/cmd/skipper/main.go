package main

import (
	"context"
	"strconv"
	"time"

	skipperconfig "frameworks/api_skipper/internal/config"
	"frameworks/api_skipper/internal/knowledge"
	commodoreclient "frameworks/pkg/clients/commodore"
	deckhandclient "frameworks/pkg/clients/deckhand"
	periscopeclient "frameworks/pkg/clients/periscope"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/llm"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/server"
	"frameworks/pkg/version"
)

func main() {
	// Setup logger
	logger := logging.NewLoggerWithService("skipper")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Skipper (AI Video Consultant API)")

	cfg := skipperconfig.LoadConfig()
	jwtSecret := config.RequireEnv("JWT_SECRET")
	serviceToken := config.RequireEnv("SERVICE_TOKEN")

	// Connect to database
	dbConfig := database.DefaultConfig()
	dbConfig.URL = cfg.DatabaseURL
	db := database.MustConnect(dbConfig, logger)
	defer func() { _ = db.Close() }()

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("skipper", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("skipper", version.Version, version.GitCommit)

	// Add health checks
	healthChecker.AddCheck("database", monitoring.DatabaseHealthCheck(db))
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"DATABASE_URL": cfg.DatabaseURL,
		"JWT_SECRET":   jwtSecret,
	}))

	// Create Periscope gRPC client for diagnostics
	periscopeGRPCAddr := config.GetEnv("PERISCOPE_GRPC_ADDR", "periscope-query:19004")
	periscopeClient, err := periscopeclient.NewGRPCClient(periscopeclient.GRPCConfig{
		GRPCAddr:     periscopeGRPCAddr,
		Timeout:      30 * time.Second,
		Logger:       logger,
		ServiceToken: serviceToken,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Periscope gRPC client - diagnostics disabled")
		periscopeClient = nil
	} else {
		defer func() { _ = periscopeClient.Close() }()
		logger.WithField("addr", periscopeGRPCAddr).Info("Connected to Periscope gRPC")
	}

	// Create Deckhand gRPC client for support context
	deckhandGRPCAddr := config.GetEnv("DECKHAND_GRPC_ADDR", "deckhand:19006")
	deckhandClient, err := deckhandclient.NewGRPCClient(deckhandclient.GRPCConfig{
		GRPCAddr:     deckhandGRPCAddr,
		Timeout:      30 * time.Second,
		Logger:       logger,
		ServiceToken: serviceToken,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Deckhand gRPC client - support context disabled")
		deckhandClient = nil
	} else {
		defer func() { _ = deckhandClient.Close() }()
		logger.WithField("addr", deckhandGRPCAddr).Info("Connected to Deckhand gRPC")
	}

	// Create Commodore gRPC client for tenant context
	commodoreGRPCAddr := config.GetEnv("COMMODORE_GRPC_ADDR", "commodore:19001")
	commodoreClient, err := commodoreclient.NewGRPCClient(commodoreclient.GRPCConfig{
		GRPCAddr:     commodoreGRPCAddr,
		Timeout:      30 * time.Second,
		Logger:       logger,
		ServiceToken: serviceToken,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Commodore gRPC client - tenant context disabled")
		commodoreClient = nil
	} else {
		defer func() { _ = commodoreClient.Close() }()
		logger.WithField("addr", commodoreGRPCAddr).Info("Connected to Commodore gRPC")
	}

	_ = periscopeClient
	_ = deckhandClient
	_ = commodoreClient

	// Setup router with unified monitoring (health/metrics only)
	router := server.SetupServiceRouter(logger, "skipper", healthChecker, metricsCollector)

	store := knowledge.NewStore(db)

	// Knowledge admin endpoints require an embedding client. Do not hard-fail startup
	// when LLM config is unset; keep the base service (health/metrics) running.
	embedderClient, err := llm.NewEmbeddingClient(llm.Config{
		Provider: cfg.LLMProvider,
		Model:    cfg.LLMModel,
		APIKey:   cfg.LLMAPIKey,
		APIURL:   cfg.LLMAPIURL,
	})
	if err != nil {
		logger.WithError(err).Warn("Skipping knowledge admin API: embedding client not configured")
	} else {
		embedder, embedderErr := knowledge.NewEmbedder(embedderClient)
		if embedderErr != nil {
			logger.WithError(embedderErr).Warn("Skipping knowledge admin API: failed to initialize knowledge embedder")
		} else {
			crawler, crawlerErr := knowledge.NewCrawler(nil, embedder, store)
			if crawlerErr != nil {
				logger.WithError(crawlerErr).Warn("Skipping knowledge admin API: failed to initialize knowledge crawler")
			} else {
				adminAPI, adminErr := knowledge.NewAdminAPI(store, embedder, crawler, logger)
				if adminErr != nil {
					logger.WithError(adminErr).Warn("Skipping knowledge admin API: failed to initialize knowledge admin API")
				} else {
					adminAPI.RegisterRoutes(router, []byte(jwtSecret))
				}
			}
		}
	}

	// Start HTTP server with graceful shutdown
	serverConfig := server.DefaultConfig("skipper", cfg.Port)

	// Best-effort service registration in Quartermaster (using gRPC)
	go func() {
		qmGRPCAddr := config.GetEnv("QUARTERMASTER_GRPC_ADDR", "quartermaster:19002")
		qmClient, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
			GRPCAddr:     qmGRPCAddr,
			Timeout:      10 * time.Second,
			Logger:       logger,
			ServiceToken: serviceToken,
		})
		if err != nil {
			logger.WithError(err).Warn("Failed to create Quartermaster gRPC client")
			return
		}
		defer func() { _ = qmClient.Close() }()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		healthEndpoint := "/health"
		httpPort, _ := strconv.Atoi(serverConfig.Port)
		if httpPort <= 0 || httpPort > 65535 {
			logger.Warn("Quartermaster bootstrap skipped: invalid port")
			return
		}
		advertiseHost := config.GetEnv("SKIPPER_HOST", "skipper")
		clusterID := config.GetEnv("CLUSTER_ID", "")
		if _, err := qmClient.BootstrapService(ctx, &pb.BootstrapServiceRequest{
			Type:           "skipper",
			Version:        version.Version,
			Protocol:       "http",
			HealthEndpoint: &healthEndpoint,
			Port:           int32(httpPort),
			AdvertiseHost:  &advertiseHost,
			ClusterId: func() *string {
				if clusterID != "" {
					return &clusterID
				}
				return nil
			}(),
		}); err != nil {
			logger.WithError(err).Warn("Quartermaster bootstrap (skipper) failed")
		} else {
			logger.Info("Quartermaster bootstrap (skipper) ok")
		}
	}()

	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}
}
