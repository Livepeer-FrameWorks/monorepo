package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"frameworks/api_consultant/internal/chat"
	skipperconfig "frameworks/api_consultant/internal/config"
	"frameworks/api_consultant/internal/heartbeat"
	"frameworks/api_consultant/internal/knowledge"
	"frameworks/api_consultant/internal/mcpclient"
	"frameworks/api_consultant/internal/mcpspoke"
	"frameworks/api_consultant/internal/metering"
	"frameworks/api_consultant/internal/notify"
	"frameworks/pkg/auth"
	commodoreclient "frameworks/pkg/clients/commodore"
	decklogclient "frameworks/pkg/clients/decklog"
	periscopeclient "frameworks/pkg/clients/periscope"
	purserclient "frameworks/pkg/clients/purser"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/llm"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/search"
	"frameworks/pkg/server"
	"frameworks/pkg/version"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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

	var usagePublisher *metering.Publisher
	if len(cfg.KafkaBrokers) > 0 {
		publisher, err := metering.NewPublisher(metering.PublisherConfig{
			Brokers:   cfg.KafkaBrokers,
			ClusterID: cfg.KafkaClusterID,
			Topic:     cfg.BillingKafkaTopic,
			Source:    "skipper",
			Logger:    logger,
		})
		if err != nil {
			logger.WithError(err).Warn("Failed to create billing Kafka publisher - usage events disabled")
		} else {
			usagePublisher = publisher
			defer func() { _ = usagePublisher.Close() }()
		}
	} else {
		logger.Warn("KAFKA_BROKERS not set - billing usage events disabled")
	}

	clusterID := strings.TrimSpace(config.GetEnv("CLUSTER_ID", ""))
	usageTracker := metering.NewUsageTracker(metering.UsageTrackerConfig{
		DB:            db,
		Publisher:     usagePublisher,
		Logger:        logger,
		Model:         cfg.LLMModel,
		ClusterID:     clusterID,
		FlushInterval: time.Minute,
	})
	usageTracker.Start()
	defer usageTracker.Stop()

	rateLimiter := metering.NewRateLimiter(cfg.ChatRateLimitHour, cfg.RateLimitOverrides)
	rateLimiter.StartCleanup(context.Background())

	// Periscope gRPC client — used by the heartbeat agent for direct diagnostics.
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

	// Commodore gRPC client — used by the heartbeat agent for tenant/stream context.
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

	// Create Purser gRPC client for tier checks
	purserGRPCAddr := config.GetEnv("PURSER_GRPC_ADDR", "purser:19003")
	purserClient, err := purserclient.NewGRPCClient(purserclient.GRPCConfig{
		GRPCAddr:     purserGRPCAddr,
		Timeout:      10 * time.Second,
		Logger:       logger,
		ServiceToken: serviceToken,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Purser gRPC client - tier gating unavailable")
		purserClient = nil
	} else {
		defer func() { _ = purserClient.Close() }()
	}

	// Create Decklog gRPC client for usage metering
	decklogGRPCAddr := config.GetEnv("DECKLOG_GRPC_ADDR", "decklog:18006")
	decklogClient, err := decklogclient.NewBatchedClient(decklogclient.BatchedClientConfig{
		Target:        decklogGRPCAddr,
		AllowInsecure: true,
		Timeout:       5 * time.Second,
		Source:        "skipper",
		ServiceToken:  serviceToken,
	}, logger)
	if err != nil {
		logger.WithError(err).Warn("Failed to create Decklog client - usage metering disabled")
		decklogClient = nil
	} else {
		defer func() { _ = decklogClient.Close() }()
	}

	// Create Quartermaster gRPC client for tenant listings
	qmGRPCAddr := config.GetEnv("QUARTERMASTER_GRPC_ADDR", "quartermaster:19002")
	qmClient, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:     qmGRPCAddr,
		Timeout:      10 * time.Second,
		Logger:       logger,
		ServiceToken: serviceToken,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Quartermaster gRPC client - heartbeat disabled")
		qmClient = nil
	} else {
		defer func() { _ = qmClient.Close() }()
	}

	llmProvider, err := llm.NewProvider(llm.Config{
		Provider: cfg.LLMProvider,
		Model:    cfg.LLMModel,
		APIKey:   cfg.LLMAPIKey,
		APIURL:   cfg.LLMAPIURL,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to initialize LLM provider")
		llmProvider = nil
	}

	embeddingClient, err := llm.NewEmbeddingClient(llm.Config{
		Provider: cfg.EmbeddingProvider,
		Model:    cfg.EmbeddingModel,
		APIKey:   cfg.EmbeddingAPIKey,
		APIURL:   cfg.EmbeddingAPIURL,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to initialize embedding client")
		embeddingClient = nil
	}

	var embedder *knowledge.Embedder
	if embeddingClient != nil {
		embedder, err = knowledge.NewEmbedder(embeddingClient)
		if err != nil {
			logger.WithError(err).Warn("Failed to initialize knowledge embedder")
		}
	}

	searchProvider, err := search.NewProvider(search.Config{
		Provider: cfg.SearchProvider,
		APIKey:   cfg.SearchAPIKey,
		APIURL:   cfg.SearchAPIURL,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to initialize search provider")
		searchProvider = nil
	}

	// Connect to the Gateway MCP for platform tools (diagnostics, streams, etc.).
	var gatewayClient *mcpclient.GatewayClient
	if mcpURL := cfg.GatewayMCPURL(); mcpURL != "" {
		var connectErr error
		gatewayClient, connectErr = mcpclient.New(context.Background(), mcpclient.Config{
			GatewayURL:   mcpURL,
			ServiceToken: serviceToken,
			ToolDenylist: []string{"search_knowledge", "search_web"},
			Logger:       logger,
		})
		if connectErr != nil {
			logger.WithError(connectErr).Warn("Failed to connect to Gateway MCP - platform tools disabled")
			gatewayClient = nil
		} else {
			defer func() { _ = gatewayClient.Close() }()
		}
	} else {
		logger.Warn("GATEWAY_PUBLIC_URL not set - platform tools disabled")
	}

	conversationStore := chat.NewConversationStore(db)
	knowledgeStore := knowledge.NewStore(db)
	searchTool := chat.NewSearchWebTool(searchProvider)
	orchestrator := chat.NewOrchestrator(chat.OrchestratorConfig{
		LLMProvider: llmProvider,
		Logger:      logger,
		SearchWeb:   searchTool,
		Knowledge:   knowledgeStore,
		Embedder:    embedder,
		Gateway:     gatewayClient,
	})
	chatHandler := chat.NewChatHandler(conversationStore, orchestrator, decklogClient, logger)

	heartbeatInterval := config.GetEnv("HEARTBEAT_INTERVAL", "30m")
	heartbeatDuration, err := time.ParseDuration(heartbeatInterval)
	if err != nil {
		logger.WithError(err).WithField("value", heartbeatInterval).Warn("Invalid HEARTBEAT_INTERVAL; using default")
		heartbeatDuration = 30 * time.Minute
	}
	notifyConfig := notify.LoadConfig()
	mcpManager := notify.NewTenantMCPManager(logger)
	dispatcher := notify.NewDispatcher(notify.DispatcherConfig{
		EmailNotifier:     notify.NewEmailNotifier(notifyConfig, logger),
		WebsocketNotifier: notify.NewWebsocketNotifier(decklogClient, logger),
		MCPNotifier:       notify.NewMCPNotifier(mcpManager, logger),
		Defaults:          notifyConfig.DefaultPreferences,
		Logger:            logger,
	})
	reportStore := heartbeat.NewReportStore(db)
	heartbeatReporter := &heartbeat.Reporter{
		Store:      reportStore,
		Billing:    purserClient,
		Dispatcher: dispatcher,
		Logger:     logger,
		WebAppURL:  notifyConfig.WebAppURL,
	}
	heartbeatAgent := heartbeat.NewAgent(heartbeat.AgentConfig{
		Interval:          heartbeatDuration,
		Orchestrator:      orchestrator,
		Commodore:         commodoreClient,
		Periscope:         periscopeClient,
		Purser:            purserClient,
		Quartermaster:     qmClient,
		Decklog:           decklogClient,
		Reporter:          heartbeatReporter,
		Logger:            logger,
		RequiredTierLevel: cfg.RequiredTierLevel,
	})
	go heartbeatAgent.Start(context.Background())

	// Setup router with unified monitoring (health/metrics only)
	router := server.SetupServiceRouter(logger, "skipper", healthChecker, metricsCollector)
	apiGroup := router.Group("/api/skipper")
	apiGroup.Use(auth.JWTAuthMiddleware([]byte(jwtSecret)))
	apiGroup.Use(metering.AccessMiddleware(metering.AccessMiddlewareConfig{
		Purser:            purserClient,
		RequiredTierLevel: cfg.RequiredTierLevel,
		RateLimiter:       rateLimiter,
		Tracker:           usageTracker,
		Logger:            logger,
	}))
	chat.RegisterRoutes(apiGroup, chatHandler)

	// Knowledge admin endpoints require an embedding client. Do not hard-fail startup
	// when LLM config is unset; keep the base service (health/metrics) running.
	embedderClient, err := llm.NewEmbeddingClient(llm.Config{
		Provider: cfg.EmbeddingProvider,
		Model:    cfg.EmbeddingModel,
		APIKey:   cfg.EmbeddingAPIKey,
		APIURL:   cfg.EmbeddingAPIURL,
	})
	if err != nil {
		logger.WithError(err).Warn("Skipping knowledge admin API: embedding client not configured")
	} else {
		adminEmbedder, embedderErr := knowledge.NewEmbedder(embedderClient)
		if embedderErr != nil {
			logger.WithError(embedderErr).Warn("Skipping knowledge admin API: failed to initialize knowledge embedder")
		} else {
			crawler, crawlerErr := knowledge.NewCrawler(nil, adminEmbedder, knowledgeStore)
			if crawlerErr != nil {
				logger.WithError(crawlerErr).Warn("Skipping knowledge admin API: failed to initialize knowledge crawler")
			} else {
				adminAPI, adminErr := knowledge.NewAdminAPI(db, knowledgeStore, adminEmbedder, crawler, logger)
				if adminErr != nil {
					logger.WithError(adminErr).Warn("Skipping knowledge admin API: failed to initialize knowledge admin API")
				} else {
					adminAPI.RegisterRoutes(router, []byte(jwtSecret))
				}
			}
		}
	}

	// Spoke MCP endpoint — exposes search_knowledge and search_web for the Gateway hub.
	spokeMCPServer := mcpspoke.NewServer(mcpspoke.Config{
		Knowledge:      knowledgeStore,
		Embedder:       embedder,
		SearchProvider: searchProvider,
		Logger:         logger,
	})
	spokeHandler := mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if token == "" || token != serviceToken {
				return nil
			}
			return spokeMCPServer
		},
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
	router.Any("/mcp/spoke", gin.WrapH(http.Handler(spokeHandler)))
	router.Any("/mcp/spoke/*path", gin.WrapH(http.Handler(spokeHandler)))

	// MCP notification endpoint — per-tenant server for tenant-isolated sessions.
	jwtSecretBytes := []byte(jwtSecret)
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if token == "" {
				return nil
			}
			claims, err := auth.ValidateJWT(token, jwtSecretBytes)
			if err != nil || claims.TenantID == "" {
				return nil
			}
			return mcpManager.ServerForTenant(claims.TenantID)
		},
		&mcp.StreamableHTTPOptions{Stateless: false},
	)
	router.Any("/mcp/*path", gin.WrapH(http.Handler(mcpHandler)))

	// Start HTTP server with graceful shutdown
	serverConfig := server.DefaultConfig("skipper", cfg.Port)

	// Best-effort service registration in Quartermaster (using gRPC)
	go func() {
		if qmClient == nil {
			logger.Warn("Quartermaster bootstrap skipped: client unavailable")
			return
		}

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
