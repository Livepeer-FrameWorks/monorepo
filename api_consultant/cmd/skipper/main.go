package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"frameworks/api_consultant/internal/chat"
	skipperconfig "frameworks/api_consultant/internal/config"
	"frameworks/api_consultant/internal/diagnostics"
	"frameworks/api_consultant/internal/heartbeat"
	"frameworks/api_consultant/internal/knowledge"
	"frameworks/api_consultant/internal/mcpclient"
	"frameworks/api_consultant/internal/mcpspoke"
	"frameworks/api_consultant/internal/metering"
	"frameworks/api_consultant/internal/notify"
	"frameworks/api_consultant/internal/skipper"
	"frameworks/api_consultant/internal/social"
	"frameworks/api_consultant/internal/webui"
	"frameworks/pkg/auth"
	decklogclient "frameworks/pkg/clients/decklog"
	periscopeclient "frameworks/pkg/clients/periscope"
	purserclient "frameworks/pkg/clients/purser"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/database"
	"frameworks/pkg/email"
	"frameworks/pkg/grpcutil"
	"frameworks/pkg/llm"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/pkg/monitoring"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/qmbootstrap"
	"frameworks/pkg/search"
	"frameworks/pkg/server"
	"frameworks/pkg/tenants"
	"frameworks/pkg/version"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
)

func main() {
	// Setup logger
	logger := logging.NewLoggerWithService("skipper")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Skipper (AI Video Consultant API)")

	cfg := skipperconfig.LoadConfig()
	if len(cfg.SSRFAllowedHosts) > 0 {
		knowledge.SetSSRFAllowedHosts(cfg.SSRFAllowedHosts)
		logger.WithField("hosts", cfg.SSRFAllowedHosts).Info("SSRF allowlist configured")
	}
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
		Provider:  cfg.LLMProvider,
		Model:     cfg.LLMModel,
		APIKey:    cfg.LLMAPIKey,
		APIURL:    cfg.LLMAPIURL,
		MaxTokens: cfg.LLMMaxTokens,
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

	// Auto-detect embedding dimensions and migrate the DB column if needed.
	if embeddingClient != nil {
		dims := cfg.EmbeddingDimensions
		if dims == 0 {
			probedDims, probeErr := llm.ProbeEmbeddingDimensions(context.Background(), embeddingClient)
			if probeErr != nil {
				logger.WithError(probeErr).Fatal("Failed to probe embedding dimensions — set EMBEDDING_DIMENSIONS to skip")
			}
			dims = probedDims
			logger.WithField("dimensions", dims).Info("Probed embedding dimensions from model")
		} else {
			logger.WithField("dimensions", dims).Info("Using configured embedding dimensions")
		}
		migrated, migrateErr := knowledge.EnsureEmbeddingDimensions(context.Background(), db, dims)
		if migrateErr != nil {
			logger.WithError(migrateErr).Fatal("Failed to ensure embedding dimensions")
		}
		if migrated {
			logger.WithField("dimensions", dims).Warn("Embedding dimensions changed — knowledge data truncated, re-crawl required")
		}
	}

	// Utility LLM for background tasks (contextual retrieval, future: title generation).
	// Falls back to main LLM if UTILITY_LLM_* env vars are not set.
	var utilityLLM llm.Provider
	if cfg.UtilityLLMProvider != "" && cfg.UtilityLLMAPIKey != "" {
		up, upErr := llm.NewProvider(llm.Config{
			Provider: cfg.UtilityLLMProvider,
			Model:    cfg.UtilityLLMModel,
			APIKey:   cfg.UtilityLLMAPIKey,
			APIURL:   cfg.UtilityLLMAPIURL,
		})
		if upErr != nil {
			logger.WithError(upErr).Warn("Failed to initialize utility LLM provider")
		} else {
			utilityLLM = up
		}
	}
	// Cross-encoder reranker (optional — falls back to keyword heuristic).
	var rerankClient llm.RerankClient
	if cfg.RerankProvider != "" {
		rc, rcErr := llm.NewRerankClient(llm.RerankConfig{
			Provider: cfg.RerankProvider,
			Model:    cfg.RerankModel,
			APIKey:   cfg.RerankAPIKey,
			APIURL:   cfg.RerankAPIURL,
		})
		if rcErr != nil {
			logger.WithError(rcErr).Warn("Failed to initialize reranker - keyword fallback will be used")
		} else {
			rerankClient = rc
		}
	}
	reranker := knowledge.NewReranker(rerankClient, cfg.RerankProvider, cfg.RerankModel)

	var embedder *knowledge.Embedder
	if embeddingClient != nil {
		var embedOpts []knowledge.EmbedderOption
		embedOpts = append(embedOpts, knowledge.WithProviderInfo(cfg.EmbeddingProvider, cfg.EmbeddingModel))
		if cfg.ChunkTokenLimit > 0 {
			embedOpts = append(embedOpts, knowledge.WithTokenLimit(cfg.ChunkTokenLimit))
		}
		if cfg.ChunkTokenOverlap > 0 {
			embedOpts = append(embedOpts, knowledge.WithTokenOverlap(cfg.ChunkTokenOverlap))
		}
		embedder, err = knowledge.NewEmbedder(embeddingClient, embedOpts...)
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
			ToolDenylist: []string{"ask_consultant"},
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

	// Query rewriter (optional, uses utility LLM)
	var queryRewriter *chat.QueryRewriter
	if utilityLLM != nil {
		queryRewriter = chat.NewQueryRewriter(utilityLLM)
	}

	// HyDE — Hypothetical Document Embeddings (optional, uses utility LLM + embedder)
	var hyde *chat.HyDEGenerator
	if cfg.EnableHyDE && utilityLLM != nil && embedder != nil {
		hyde = chat.NewHyDEGenerator(utilityLLM, embedder)
		logger.Info("HyDE (Hypothetical Document Embeddings) enabled")
	}

	baselineStore := diagnostics.NewSQLBaselineStore(db)
	baselineEvaluator := diagnostics.NewBaselineEvaluator(baselineStore, 2.0, 5)

	conversationStore := chat.NewConversationStore(db)
	knowledgeStore := knowledge.NewStore(db)
	searchTool := chat.NewSearchWebTool(searchProvider)
	searchTool.SetSearchLimit(cfg.SearchLimit)
	globalTenantID := tenants.SystemTenantID.String()
	orchestrator := chat.NewOrchestrator(chat.OrchestratorConfig{
		LLMProvider:     llmProvider,
		LLMProviderName: cfg.LLMProvider,
		LLMModelName:    cfg.LLMModel,
		Logger:          logger,
		SearchWeb:       searchTool,
		Knowledge:       knowledgeStore,
		Embedder:        embedder,
		Reranker:        reranker,
		QueryRewriter:   queryRewriter,
		HyDE:            hyde,
		Gateway:         gatewayClient,
		Diagnostics:     baselineEvaluator,
		SearchLimit:     cfg.SearchLimit,
		GlobalTenantID:  globalTenantID,
	})
	var usageLogger skipper.UsageLogger
	if decklogClient != nil {
		usageLogger = &skipper.DecklogUsageLogger{Client: decklogClient, Logger: logger}
	}
	chatHandler := chat.NewChatHandler(conversationStore, orchestrator, usageLogger, logger)
	chatHandler.MaxHistoryMessages = cfg.MaxHistoryMessages
	chatHandler.LLMProvider = llmProvider

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
	// Create the social event collector early so heartbeat callbacks can
	// push signals into it. The collector is nil when social is disabled.
	var socialCollector *social.EventCollector
	if cfg.SocialEnabled && cfg.SocialNotifyEmail != "" {
		socialCollector = social.NewEventCollector()
	}

	heartbeatAgent := heartbeat.NewAgent(heartbeat.AgentConfig{
		Interval:          heartbeatDuration,
		Orchestrator:      orchestrator,
		Periscope:         periscopeClient,
		Purser:            purserClient,
		Quartermaster:     qmClient,
		Decklog:           decklogClient,
		Reporter:          heartbeatReporter,
		Diagnostics:       baselineEvaluator,
		Logger:            logger,
		RequiredTierLevel: cfg.RequiredTierLevel,
		InfraMonitor: &heartbeat.InfraMonitorConfig{
			Nodes:     periscopeClient,
			Clusters:  qmClient,
			Billing:   purserClient,
			Baselines: baselineEvaluator,
			SMTP:      notifyConfig.SMTP,
			Logger:    logger,
		},
		OnPlatformOverview: func(tenantID string, overview *pb.GetPlatformOverviewResponse) {
			if socialCollector == nil {
				return
			}
			socialCollector.Push(social.EventSignal{
				ContentType: social.ContentPlatformStats,
				Headline:    fmt.Sprintf("Platform overview for tenant %s", tenantID),
				Data: map[string]any{
					"peak_viewers":   float64(overview.GetPeakViewers()),
					"total_viewers":  float64(overview.GetTotalViewers()),
					"active_streams": float64(overview.GetActiveStreams()),
					"egress_gb":      overview.GetEgressGb(),
				},
				Score: 0.5,
			})
		},
		OnFederationSummary: func(tenantID string, summary *pb.GetFederationSummaryResponse) {
			if socialCollector == nil {
				return
			}
			s := summary.GetSummary()
			if s == nil {
				return
			}
			socialCollector.Push(social.EventSignal{
				ContentType: social.ContentFederation,
				Headline:    fmt.Sprintf("Federation summary for tenant %s", tenantID),
				Data: map[string]any{
					"total_events":   float64(s.GetTotalEvents()),
					"avg_latency_ms": s.GetOverallAvgLatencyMs(),
					"failure_rate":   s.GetOverallFailureRate(),
				},
				Score: 0.5,
			})
		},
	})
	go heartbeatAgent.Start(context.Background())

	// Start social posting agent (optional, off by default)
	if cfg.SocialEnabled && cfg.SocialNotifyEmail != "" {
		socialLLM := utilityLLM
		if socialLLM == nil {
			socialLLM = llmProvider
		}
		if socialLLM != nil {
			socialStore := social.NewPostStore(db)
			socialDetector := social.NewDetector(social.DetectorConfig{
				Store:     socialStore,
				Collector: socialCollector,
				DB:        db,
				Logger:    logger,
			})
			socialComposer := social.NewComposer(social.ComposerConfig{
				LLM:    socialLLM,
				Store:  socialStore,
				Logger: logger,
			})
			socialPublisher := social.NewEmailPublisher(social.EmailPublisherConfig{
				Sender: email.NewSender(notifyConfig.SMTP),
				SMTP:   notifyConfig.SMTP,
				To:     cfg.SocialNotifyEmail,
				Logger: logger,
			})
			socialAgent := social.NewAgent(social.AgentConfig{
				Interval:  cfg.SocialInterval,
				MaxPerDay: cfg.SocialMaxPerDay,
				Detector:  socialDetector,
				Composer:  socialComposer,
				Publisher: socialPublisher,
				Store:     socialStore,
				Logger:    logger,
			})
			go socialAgent.Start(context.Background())
			logger.Info("Social posting agent started")
		} else {
			logger.Warn("Social posting agent: no LLM provider available, skipping")
		}
	}

	// Start gRPC server for Bridge gateway integration
	grpcChatServer := chat.NewGRPCServer(chat.GRPCServerConfig{
		Conversations:      conversationStore,
		Orchestrator:       orchestrator,
		UsageLogger:        usageLogger,
		Logger:             logger,
		MaxHistoryMessages: cfg.MaxHistoryMessages,
		Reports:            &reportStoreAdapter{store: reportStore},
	})
	grpcAuthCfg := middleware.GRPCAuthConfig{
		ServiceToken: serviceToken,
		JWTSecret:    []byte(jwtSecret),
		Logger:       logger,
		SkipMethods:  []string{"/grpc.health.v1.Health/Check", "/grpc.health.v1.Health/Watch"},
	}
	go func() {
		grpcLis, listenErr := net.Listen("tcp", ":"+cfg.GRPCPort)
		if listenErr != nil {
			logger.WithError(listenErr).Fatal("Failed to listen on gRPC port")
		}
		grpcSrv := grpc.NewServer(
			grpc.ChainUnaryInterceptor(
				grpcutil.SanitizeUnaryServerInterceptor(),
				middleware.GRPCAuthInterceptor(grpcAuthCfg),
			),
			grpc.ChainStreamInterceptor(
				middleware.GRPCStreamAuthInterceptor(grpcAuthCfg),
			),
		)
		pb.RegisterSkipperChatServiceServer(grpcSrv, grpcChatServer)
		logger.WithField("port", cfg.GRPCPort).Info("Starting Skipper gRPC server")
		if serveErr := grpcSrv.Serve(grpcLis); serveErr != nil {
			logger.WithError(serveErr).Fatal("Skipper gRPC server failed")
		}
	}()

	// Setup router with unified monitoring (health/metrics only)
	router := server.SetupServiceRouter(logger, "skipper", healthChecker, metricsCollector)
	apiGroup := router.Group("/api/skipper")
	apiGroup.Use(auth.JWTAuthMiddleware([]byte(jwtSecret)))
	apiGroup.Use(skipperContextBridge())
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
	pageCacheStore := knowledge.NewPageCacheStore(db)
	var crawler *knowledge.Crawler
	crawlHealth := knowledge.NewHealthTracker()
	embedderClient, err := llm.NewEmbeddingClient(llm.Config{
		Provider: cfg.EmbeddingProvider,
		Model:    cfg.EmbeddingModel,
		APIKey:   cfg.EmbeddingAPIKey,
		APIURL:   cfg.EmbeddingAPIURL,
	})
	if err != nil {
		logger.WithError(err).Warn("Skipping knowledge admin API: embedding client not configured")
	} else {
		var adminEmbedOpts []knowledge.EmbedderOption
		adminEmbedOpts = append(adminEmbedOpts, knowledge.WithProviderInfo(cfg.EmbeddingProvider, cfg.EmbeddingModel))
		if cfg.ChunkTokenLimit > 0 {
			adminEmbedOpts = append(adminEmbedOpts, knowledge.WithTokenLimit(cfg.ChunkTokenLimit))
		}
		if cfg.ChunkTokenOverlap > 0 {
			adminEmbedOpts = append(adminEmbedOpts, knowledge.WithTokenOverlap(cfg.ChunkTokenOverlap))
		}
		if cfg.ContextualRetrieval && utilityLLM != nil {
			summarizer := knowledge.NewLLMContextualSummarizer(utilityLLM, cfg.UtilityLLMProvider, cfg.UtilityLLMModel)
			adminEmbedOpts = append(adminEmbedOpts, knowledge.WithContextualRetrieval(summarizer))
			logger.Info("Contextual retrieval enabled for crawler embedder")
		}
		adminEmbedder, embedderErr := knowledge.NewEmbedder(embedderClient, adminEmbedOpts...)
		if embedderErr != nil {
			logger.WithError(embedderErr).Warn("Skipping knowledge admin API: failed to initialize knowledge embedder")
		} else {
			crawlerOpts := []knowledge.CrawlerOption{
				knowledge.WithPageCache(pageCacheStore),
				knowledge.WithLogger(logger),
				knowledge.WithLinkDiscovery(cfg.LinkDiscovery),
			}
			if cfg.EnableRendering {
				renderer, renderErr := knowledge.NewRodRenderer()
				if renderErr != nil {
					logger.WithError(renderErr).Warn("Headless rendering disabled: Chrome not available")
				} else {
					crawlerOpts = append(crawlerOpts, knowledge.WithRenderer(renderer))
					logger.Info("Headless rendering enabled")
				}
			}
			var crawlerErr error
			crawler, crawlerErr = knowledge.NewCrawler(nil, adminEmbedder, knowledgeStore, crawlerOpts...)
			if crawlerErr != nil {
				logger.WithError(crawlerErr).Warn("Skipping knowledge admin API: failed to initialize knowledge crawler")
			} else {
				defer crawler.Close()
				adminAPI, adminErr := knowledge.NewAdminAPI(db, knowledgeStore, adminEmbedder, crawler, pageCacheStore, logger)
				if adminErr != nil {
					logger.WithError(adminErr).Warn("Skipping knowledge admin API: failed to initialize knowledge admin API")
				} else {
					adminAPI.SetHealth(crawlHealth)
					adminAPI.RegisterRoutes(router, []byte(jwtSecret), skipperContextBridge())
				}
			}
		}
	}

	if crawler != nil && (len(cfg.Sitemaps) > 0 || cfg.SitemapsDir != "") {
		scheduler := knowledge.NewCrawlScheduler(knowledge.SchedulerConfig{
			Crawler:     crawler,
			DB:          db,
			PageCache:   pageCacheStore,
			Health:      crawlHealth,
			Interval:    cfg.CrawlInterval,
			TenantID:    globalTenantID,
			Sitemaps:    cfg.Sitemaps,
			SitemapsDir: cfg.SitemapsDir,
			Logger:      logger,
			OnPageEmbedded: func(evt knowledge.PageEmbeddedEvent) {
				if socialCollector != nil {
					socialCollector.Push(social.EventSignal{
						ContentType: social.ContentKnowledge,
						Headline:    fmt.Sprintf("New knowledge embedded: %s", evt.PageURL),
						Data: map[string]any{
							"page_url":    evt.PageURL,
							"source_root": evt.SourceRoot,
							"tenant_id":   evt.TenantID,
						},
						Score: 0.5,
					})
				}
			},
		})
		go scheduler.Start(context.Background())
		logger.Info("Knowledge crawl scheduler started")
	}

	// Spoke MCP endpoint — exposes search_knowledge and search_web for the Gateway hub.
	spokeMCPServer := mcpspoke.NewServer(mcpspoke.Config{
		Knowledge:      knowledgeStore,
		Embedder:       embedder,
		Reranker:       reranker,
		SearchProvider: searchProvider,
		Orchestrator:   orchestrator,
		Logger:         logger,
		GlobalTenantID: globalTenantID,
		SearchLimit:    cfg.SearchLimit,
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
	router.Any("/mcp/notify", gin.WrapH(http.Handler(mcpHandler)))
	router.Any("/mcp/notify/*path", gin.WrapH(http.Handler(mcpHandler)))

	// Embedded web UI — enabled by default, set SKIPPER_WEB_UI=false to disable.
	if config.GetEnv("SKIPPER_WEB_UI", "true") != "false" {
		adminTenantID := cfg.AdminTenantID
		if adminTenantID == "" {
			adminTenantID = "local"
			logger.Warn("SKIPPER_ADMIN_TENANT_ID not set — WebUI will use tenant 'local' (Gateway tools unavailable)")
		}
		adminAPIKey := cfg.AdminAPIKey
		enableUI := true
		if adminAPIKey == "" {
			if config.GetEnv("SKIPPER_WEB_UI_INSECURE", "") != "true" {
				logger.Error("WebUI disabled: set SKIPPER_API_KEY or SKIPPER_WEB_UI_INSECURE=true")
				enableUI = false
			} else {
				logger.Warn("WebUI running WITHOUT authentication (SKIPPER_WEB_UI_INSECURE=true)")
			}
		}
		if !enableUI {
			logger.Info("Web UI skipped (no auth configured)")
		} else {
			adminGroup := router.Group("/admin/api")
			adminGroup.Use(adminAuthMiddleware(adminTenantID, []byte(jwtSecret), adminAPIKey))
			adminGroup.Use(skipperContextBridge())
			chat.RegisterRoutes(adminGroup, chatHandler)

			uiHandler := webui.Handler(webui.Config{APIURL: "/admin/api"})
			router.NoRoute(gin.WrapH(uiHandler))
			logger.Info("Web UI enabled at /")
		}
	}

	// Start HTTP server with graceful shutdown
	serverConfig := server.DefaultConfig("skipper", cfg.Port)
	serverConfig.WriteTimeout = 5 * time.Minute // SSE streams need time for multi-round tool calling

	// Best-effort service registration in Quartermaster (using gRPC)
	go func() {
		if qmClient == nil {
			logger.Warn("Quartermaster bootstrap skipped: client unavailable")
			return
		}

		healthEndpoint := "/health"
		httpPort, _ := strconv.Atoi(serverConfig.Port)
		if httpPort <= 0 || httpPort > 65535 {
			logger.Warn("Quartermaster bootstrap skipped: invalid port")
			return
		}
		advertiseHost := config.GetEnv("SKIPPER_HOST", "skipper")
		clusterID := config.GetEnv("CLUSTER_ID", "")
		req := &pb.BootstrapServiceRequest{
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
		}
		if nodeID := config.GetEnv("NODE_ID", ""); nodeID != "" {
			req.NodeId = &nodeID
		}
		if _, err := qmbootstrap.BootstrapServiceWithRetry(context.Background(), qmClient, req, logger, qmbootstrap.DefaultRetryConfig("skipper")); err != nil {
			logger.WithError(err).Warn("Quartermaster bootstrap (skipper) failed")
		} else {
			logger.Info("Quartermaster bootstrap (skipper) ok")
		}
	}()

	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}
}

func adminAuthMiddleware(tenantID string, jwtSecret []byte, apiKey string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if apiKey != "" {
			// Handle login endpoint: validate key, set session cookie.
			if c.Request.Method == "POST" && strings.HasSuffix(c.Request.URL.Path, "/auth") {
				var req struct {
					Key string `json:"key"`
				}
				if err := c.ShouldBindJSON(&req); err == nil &&
					subtle.ConstantTimeCompare([]byte(req.Key), []byte(apiKey)) == 1 {
					setAdminSessionCookie(c, apiKey)
					c.JSON(http.StatusOK, gin.H{"ok": true})
					c.Abort()
					return
				}
				c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid key"})
				c.Abort()
				return
			}

			// Check session cookie.
			if cookie, err := c.Cookie("skipper_session"); err == nil && validAdminSession(cookie, apiKey) {
				// valid session
			} else if bearer := c.GetHeader("Authorization"); strings.HasPrefix(bearer, "Bearer ") &&
				subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(bearer, "Bearer ")), []byte(apiKey)) == 1 {
				setAdminSessionCookie(c, apiKey)
			} else {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
				c.Abort()
				return
			}
		}

		c.Set(string(ctxkeys.KeyTenantID), tenantID)
		c.Set(string(ctxkeys.KeyUserID), "webui-admin")
		c.Set(string(ctxkeys.KeyRole), "admin")
		c.Set(string(ctxkeys.KeyAuthType), "admin")
		token, err := auth.GenerateJWT("webui-admin", tenantID, "", "admin", jwtSecret)
		if err == nil {
			c.Set(string(ctxkeys.KeyJWTToken), token)
		}
		c.Next()
	}
}

func adminSessionMAC(apiKey string) string {
	mac := hmac.New(sha256.New, []byte(apiKey))
	mac.Write([]byte("skipper-admin-session"))
	return hex.EncodeToString(mac.Sum(nil))
}

func setAdminSessionCookie(c *gin.Context, apiKey string) {
	isDev := os.Getenv("ENV") == "development" ||
		os.Getenv("BUILD_ENV") == "development" ||
		os.Getenv("GO_ENV") == "development"
	secure := !isDev
	c.SetCookie("skipper_session", adminSessionMAC(apiKey), 86400, "/", "", secure, true)
}

func validAdminSession(cookie, apiKey string) bool {
	return subtle.ConstantTimeCompare([]byte(cookie), []byte(adminSessionMAC(apiKey))) == 1
}

func skipperContextBridge() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		ctx = skipper.WithTenantID(ctx, c.GetString(string(ctxkeys.KeyTenantID)))
		ctx = skipper.WithUserID(ctx, c.GetString(string(ctxkeys.KeyUserID)))
		ctx = skipper.WithAuthType(ctx, c.GetString(string(ctxkeys.KeyAuthType)))
		if token := c.GetString(string(ctxkeys.KeyJWTToken)); token != "" {
			ctx = skipper.WithJWTToken(ctx, token)
		}
		if tokenHash, ok := c.Get(string(ctxkeys.KeyAPITokenHash)); ok {
			ctx = context.WithValue(ctx, ctxkeys.KeyAPITokenHash, tokenHash)
		}
		if role := c.GetString(string(ctxkeys.KeyRole)); role != "" {
			ctx = skipper.WithRole(ctx, role)
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// reportStoreAdapter bridges heartbeat.SQLReportStore → chat.ReportQuerier,
// avoiding the import cycle (heartbeat test files import chat).
type reportStoreAdapter struct {
	store *heartbeat.SQLReportStore
}

func (a *reportStoreAdapter) ListPaginated(ctx context.Context, tenantID string, limit, offset int) ([]chat.ReportData, int, error) {
	records, total, err := a.store.ListByTenantPaginated(ctx, tenantID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	out := make([]chat.ReportData, len(records))
	for i, r := range records {
		out[i] = convertReport(r)
	}
	return out, total, nil
}

func (a *reportStoreAdapter) GetByID(ctx context.Context, tenantID, reportID string) (chat.ReportData, error) {
	r, err := a.store.GetByID(ctx, tenantID, reportID)
	if err != nil {
		return chat.ReportData{}, err
	}
	return convertReport(r), nil
}

func (a *reportStoreAdapter) MarkRead(ctx context.Context, tenantID string, ids []string) (int, error) {
	return a.store.MarkRead(ctx, tenantID, ids)
}

func (a *reportStoreAdapter) UnreadCount(ctx context.Context, tenantID string) (int, error) {
	return a.store.UnreadCount(ctx, tenantID)
}

func convertReport(r heartbeat.ReportRecord) chat.ReportData {
	recs := make([]chat.ReportRecommendation, len(r.Recommendations))
	for i, rec := range r.Recommendations {
		recs[i] = chat.ReportRecommendation{Text: rec.Text, Confidence: rec.Confidence}
	}
	return chat.ReportData{
		ID:              r.ID,
		Trigger:         r.Trigger,
		Summary:         r.Summary,
		MetricsReviewed: r.MetricsReviewed,
		RootCause:       r.RootCause,
		Recommendations: recs,
		CreatedAt:       r.CreatedAt,
		ReadAt:          r.ReadAt,
	}
}
