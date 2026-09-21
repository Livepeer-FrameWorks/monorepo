package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"frameworks/api_consultant/internal/appconfig"
	"frameworks/api_consultant/internal/chat"
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
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	commodoreclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	decklogclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	lookoutclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/lookout"
	periscopeclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	purserclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/purser"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/email"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/llm"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	skipperpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/skipper"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/search"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/reflection"
)

func main() {
	if version.HandleCLI() {
		return
	}

	// Setup logger
	logger := logging.NewLoggerWithService("skipper")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Skipper (AI Video Consultant API)")

	configOptions := config.Options{Service: "skipper", Logger: logger}
	appCfg, err := config.Load[appconfig.Skipper](configOptions)
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}
	appCfg.ApplyLogLevel(logger)
	metadataPolicy, err := middleware.ParseMetadataPolicy(appCfg.MetadataPolicy)
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}
	liveConfig := config.NewLive(appCfg, configOptions)
	cfg, err := appCfg.Consultant()
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}
	if len(cfg.SSRFAllowedHosts) > 0 {
		knowledge.SetSSRFAllowedHosts(cfg.SSRFAllowedHosts)
		logger.WithField("hosts", cfg.SSRFAllowedHosts).Info("SSRF allowlist configured")
	}
	jwtSecret := appCfg.JWTSecret
	serviceToken := appCfg.ServiceToken

	// Connect to database
	dbConfig := database.DefaultConfig()
	dbConfig.ServiceName = "skipper"
	dbConfig.URL = cfg.DatabaseURL
	db := database.MustConnect(dbConfig, logger)
	defer func() { _ = db.Close() }()

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("skipper", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("skipper", version.Version, version.GitCommit)

	// Add health checks
	healthChecker.AddCheck("database", monitoring.DatabaseHealthCheck(db))

	// Conversations, knowledge, and reports all live in Postgres, so readiness
	// requires it to answer. LLM and peer-service reachability are not gated.
	readiness := monitoring.NewReadinessChecker("skipper", version.Version)
	readiness.AddCheck("database", monitoring.DatabaseHealthCheck(db))

	rateLimiter := metering.NewRateLimiter(cfg.ChatRateLimitHour, cfg.RateLimitOverrides)
	rateLimiter.StartCleanup(context.Background())

	// Periscope gRPC client — used by the heartbeat agent for direct diagnostics.
	periscopeClient, err := periscopeclient.NewGRPCClient(periscopeclient.GRPCConfig{
		GRPCAddr:      appCfg.PeriscopeGRPCAddr,
		Timeout:       30 * time.Second,
		Logger:        logger,
		ServiceToken:  serviceToken,
		AllowInsecure: appCfg.AllowInsecure,
		CACertFile:    appCfg.CAPath,
		ServerName:    appCfg.PeriscopeGRPCTLSServerName,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Periscope gRPC client - diagnostics disabled")
		periscopeClient = nil
	} else {
		defer func() { _ = periscopeClient.Close() }()
		logger.WithField("addr", appCfg.PeriscopeGRPCAddr).Info("Connected to Periscope gRPC")
	}

	// Create Purser gRPC client for tier checks
	purserClient, err := purserclient.NewGRPCClient(purserclient.GRPCConfig{
		GRPCAddr:      appCfg.PurserGRPCAddr,
		Timeout:       10 * time.Second,
		Logger:        logger,
		ServiceToken:  serviceToken,
		AllowInsecure: appCfg.AllowInsecure,
		CACertFile:    appCfg.CAPath,
		ServerName:    appCfg.PurserGRPCTLSServerName,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Purser gRPC client - tier gating unavailable")
		purserClient = nil
	} else {
		defer func() { _ = purserClient.Close() }()
	}

	commodoreClient, err := commodoreclient.NewGRPCClient(commodoreclient.GRPCConfig{
		GRPCAddr:      appCfg.CommodoreGRPCAddr,
		Timeout:       10 * time.Second,
		Logger:        logger,
		ServiceToken:  serviceToken,
		AllowInsecure: appCfg.AllowInsecure,
		CACertFile:    appCfg.CAPath,
		ServerName:    appCfg.CommodoreGRPCTLSServerName,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Commodore gRPC client - primary-user notification fallback unavailable")
		commodoreClient = nil
	} else {
		defer func() { _ = commodoreClient.Close() }()
	}
	var billingClient heartbeat.BillingClient
	if purserClient != nil {
		billingClient = purserClient
	}
	var tenantContacts heartbeat.TenantContactClient
	var streamMonitoring heartbeat.CommodoreClient
	if commodoreClient != nil {
		tenantContacts = commodoreClient
		streamMonitoring = commodoreClient
	}

	// Create Decklog gRPC client for usage metering
	decklogClient, err := decklogclient.NewBatchedClient(decklogclient.BatchedClientConfig{
		Target:        appCfg.DecklogGRPCAddr,
		AllowInsecure: appCfg.AllowInsecure,
		CACertFile:    appCfg.CAPath,
		ServerName:    appCfg.DecklogGRPCTLSServerName,
		Timeout:       5 * time.Second,
		Source:        "skipper",
		ServiceToken:  serviceToken,
		ClusterID:     appCfg.ClusterID,
		SourceRegion:  appCfg.Region,
	}, logger)
	if err != nil {
		logger.WithError(err).Warn("Failed to create Decklog client - usage metering disabled")
		decklogClient = nil
	} else {
		defer func() { _ = decklogClient.Close() }()
	}

	usageTracker := metering.NewUsageTracker(metering.UsageTrackerConfig{
		DB:                db,
		Logger:            logger,
		Model:             cfg.LLMModel,
		Provider:          cfg.LLMProvider,
		EmbeddingModel:    cfg.EmbeddingModel,
		EmbeddingProvider: cfg.EmbeddingProvider,
		SearchProvider:    cfg.SearchProvider,
		Publisher:         decklogClient,
		FlushInterval:     time.Minute,
	})
	usageTracker.Start()
	defer usageTracker.Stop()

	// Create Quartermaster gRPC client for tenant listings
	qmClient, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:      appCfg.QuartermasterGRPCAddr,
		Timeout:       10 * time.Second,
		Logger:        logger,
		ServiceToken:  serviceToken,
		AllowInsecure: appCfg.AllowInsecure,
		CACertFile:    appCfg.CAPath,
		ServerName:    appCfg.QuartermasterGRPCTLSServerName,
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
	promptTokenBudget := chat.ResolvePromptTokenBudget(cfg.LLMProvider, cfg.LLMModel, cfg.PromptTokenBudget, cfg.LLMContextWindow, cfg.LLMMaxTokens)
	logger.WithField("provider", cfg.LLMProvider).
		WithField("model", cfg.LLMModel).
		WithField("prompt_token_budget", promptTokenBudget).
		WithField("configured_context_window", cfg.LLMContextWindow).
		WithField("configured_prompt_token_budget", cfg.PromptTokenBudget).
		Info("Skipper prompt token budget configured")

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
	var gatewayTools chat.GatewayToolCaller
	if mcpURLs := cfg.GatewayMCPEndpoints(); len(mcpURLs) > 0 {
		var connectErr error
		const gatewayMCPConnectTimeout = 5 * time.Second
		gatewayClient, connectErr = mcpclient.New(context.Background(), mcpclient.Config{
			GatewayURLs:    mcpURLs,
			ConnectTimeout: gatewayMCPConnectTimeout,
			ToolDenylist:   []string{"ask_consultant"},
			Logger:         logger,
		})
		if connectErr != nil {
			logger.WithError(connectErr).Fatal("Failed to connect to Gateway MCP")
		} else {
			gatewayTools = gatewayClient
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
	systemTenantID := appCfg.SystemTenantUUID()
	globalTenantID := systemTenantID.String()
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
		Gateway:         gatewayTools,
		Diagnostics:     baselineEvaluator,
		SearchLimit:     cfg.SearchLimit,
		GlobalTenantID:  globalTenantID,
		PromptBudget:    promptTokenBudget,
	})
	var usageLogger skipper.UsageLogger = usageTracker
	chatHandler := chat.NewChatHandler(conversationStore, orchestrator, usageLogger, logger)
	chatHandler.MaxHistoryMessages = cfg.MaxHistoryMessages
	chatHandler.LLMProvider = llmProvider
	chatHandler.PromptTokenBudget = promptTokenBudget

	heartbeatDuration, err := appCfg.HeartbeatDuration()
	if err != nil {
		logger.WithError(err).WithField("value", appCfg.HeartbeatInterval).Warn("Invalid HEARTBEAT_INTERVAL; using default")
		heartbeatDuration = 30 * time.Minute
	}
	notifyConfig := notify.Config{
		SMTP: email.Config{
			Host:          appCfg.SMTPHost,
			Port:          appCfg.SMTPPort,
			User:          appCfg.SMTPUser,
			Password:      appCfg.SMTPPassword,
			From:          appCfg.FromEmail,
			FromName:      appCfg.FromName,
			AllowInsecure: appCfg.SMTPAllowInsecure,
		},
		Branding:       appCfg.EmailBranding,
		BrandingSource: func() config.EmailBranding { return liveConfig.Get().EmailBranding },
		DefaultPreferences: notify.PreferenceDefaults{
			Email:     appCfg.NotifyEmail,
			Websocket: appCfg.NotifyWebsocket,
			MCP:       appCfg.NotifyMCP,
		},
		DefaultRecipient: appCfg.ToEmail,
		WebAppURL:        appCfg.WebAppURL,
	}
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
		Store:            reportStore,
		Billing:          billingClient,
		Contacts:         tenantContacts,
		Dispatcher:       dispatcher,
		Logger:           logger,
		WebAppURL:        notifyConfig.WebAppURL,
		DefaultRecipient: notifyConfig.DefaultRecipient,
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
		Purser:            billingClient,
		Quartermaster:     qmClient,
		Commodore:         streamMonitoring,
		Decklog:           decklogClient,
		Reporter:          heartbeatReporter,
		Diagnostics:       baselineEvaluator,
		Logger:            logger,
		RequiredTierLevel: cfg.RequiredTierLevel,
		InfraMonitor: &heartbeat.InfraMonitorConfig{
			Nodes:            periscopeClient,
			Clusters:         qmClient,
			Billing:          billingClient,
			Contacts:         tenantContacts,
			Baselines:        baselineEvaluator,
			SMTP:             notifyConfig.SMTP,
			Branding:         notifyConfig.Branding,
			BrandingSource:   notifyConfig.BrandingSource,
			Logger:           logger,
			DefaultRecipient: notifyConfig.DefaultRecipient,
			OnNetworkStats: func(stats *periscopepb.GetNetworkLiveStatsResponse) {
				if socialCollector == nil {
					return
				}
				var viewers, streams, nodes float64
				var uploadBps, downloadBps uint64
				for _, c := range stats.GetClusters() {
					viewers += float64(c.GetCurrentViewers())
					streams += float64(c.GetActiveStreams())
					nodes += float64(c.GetActiveNodes())
					uploadBps += c.GetUploadBytesPerSec()
					downloadBps += c.GetDownloadBytesPerSec()
				}
				socialCollector.Push(social.EventSignal{
					ContentType: social.ContentPlatformStats,
					Headline:    fmt.Sprintf("Platform: %.0f viewers, %.0f streams across %.0f nodes", viewers, streams, nodes),
					Data: map[string]any{
						"current_viewers": viewers,
						"active_streams":  streams,
						"bandwidth_bps":   float64(uploadBps + downloadBps),
						"active_nodes":    nodes,
					},
					Score: 0.5,
				})
			},
			OnFederationSummary: func(ownerTenantID string, summary *periscopepb.GetFederationSummaryResponse) {
				if socialCollector == nil {
					return
				}
				s := summary.GetSummary()
				if s == nil {
					return
				}
				socialCollector.Push(social.EventSignal{
					ContentType: social.ContentFederation,
					Headline:    fmt.Sprintf("Federation for %s: %d events", ownerTenantID, s.GetTotalEvents()),
					Data: map[string]any{
						"total_events":   float64(s.GetTotalEvents()),
						"avg_latency_ms": s.GetOverallAvgLatencyMs(),
						"failure_rate":   s.GetOverallFailureRate(),
					},
					Score: 0.5,
				})
			},
		},
	})
	go heartbeatAgent.Start(context.Background())

	// Lookout-triggered investigations need both the aggregator Kafka topic and
	// Lookout's gRPC API to attach the resulting report to the incident.
	if appCfg.LookoutGRPCAddr != "" && len(appCfg.KafkaBrokers) > 0 {
		lookoutClient, lookoutErr := lookoutclient.NewGRPCClient(lookoutclient.GRPCConfig{
			GRPCAddr:      appCfg.LookoutGRPCAddr,
			Timeout:       10 * time.Second,
			Logger:        logger,
			ServiceToken:  serviceToken,
			AllowInsecure: appCfg.AllowInsecure,
			CACertFile:    appCfg.CAPath,
			ServerName:    appCfg.LookoutGRPCTLSServerName,
		})
		if lookoutErr != nil {
			logger.WithError(lookoutErr).Warn("Failed to create Lookout gRPC client - Lookout investigations disabled")
		} else {
			defer func() { _ = lookoutClient.Close() }()
			lookoutConsumer, consumerErr := kafka.NewConsumer(appCfg.KafkaBrokers, "skipper-lookout", appCfg.KafkaClusterID, "skipper", logger)
			if consumerErr != nil {
				logger.WithError(consumerErr).Warn("Failed to create Lookout Kafka consumer - Lookout investigations disabled")
			} else {
				defer func() { _ = lookoutConsumer.Close() }()
				lookoutTrigger := &heartbeat.LookoutTrigger{
					Consumer: lookoutConsumer,
					Agent:    heartbeatAgent,
					Lookout:  lookoutClient,
					Logger:   logger,
					Topic:    topology.TopicLookoutIncidents,
				}
				go func() {
					// Start returns when group membership is lost; restarting the
					// process rejoins the consumer group from committed offsets.
					if startErr := lookoutTrigger.Start(context.Background()); startErr != nil {
						logger.WithError(startErr).Fatal("Lookout incident consumer exited")
					}
				}()
				logger.WithField("topic", topology.TopicLookoutIncidents).Info("Lookout incident trigger started")
			}
		}
	}

	// Start social posting agent (optional, off by default)
	if cfg.SocialEnabled && cfg.SocialNotifyEmail != "" {
		socialLLM := utilityLLM
		if socialLLM == nil {
			socialLLM = llmProvider
		}
		if socialLLM != nil {
			socialStore := social.NewPostStore(db, globalTenantID)
			socialDetector := social.NewDetector(social.DetectorConfig{
				Store:     socialStore,
				Collector: socialCollector,
				DB:        db,
				TenantID:  globalTenantID,
				Logger:    logger,
			})
			socialComposer := social.NewComposer(social.ComposerConfig{
				LLM:    socialLLM,
				Store:  socialStore,
				Logger: logger,
			})
			socialPublisher := social.NewEmailPublisher(social.EmailPublisherConfig{
				Sender:         email.NewSender(notifyConfig.SMTP),
				SMTP:           notifyConfig.SMTP,
				Branding:       notifyConfig.Branding,
				BrandingSource: notifyConfig.BrandingSource,
				To:             cfg.SocialNotifyEmail,
				Logger:         logger,
			})
			socialAgent := social.NewAgent(social.AgentConfig{
				Interval:  cfg.SocialInterval,
				MaxPerDay: cfg.SocialMaxPerDay,
				Trigger:   socialCollector.Notify(),
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
		PromptTokenBudget:  promptTokenBudget,
		Reports:            &reportStoreAdapter{store: reportStore},
	})
	grpcAuthCfg := middleware.GRPCAuthConfig{
		ServiceToken:   serviceToken,
		JWTSecret:      []byte(jwtSecret),
		MetadataPolicy: metadataPolicy,
		Logger:         logger,
		SkipMethods:    []string{"/grpc.health.v1.Health/Check", "/grpc.health.v1.Health/Watch"},
	}
	serverOpts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(
			grpcutil.SanitizeUnaryServerInterceptor(),
			middleware.GRPCAuthInterceptor(grpcAuthCfg),
		),
		grpc.ChainStreamInterceptor(
			middleware.GRPCStreamAuthInterceptor(grpcAuthCfg),
		),
	}
	// The gRPC server waits up to 2 minutes for its TLS files, so it builds in
	// the background while HTTP health already serves.
	buildGRPCServer := func(ctx context.Context) (*grpc.Server, error) {
		tlsCfg := grpcutil.ServerTLSConfig{
			CertFile:      appCfg.CertPath,
			KeyFile:       appCfg.KeyPath,
			AllowInsecure: appCfg.AllowInsecure,
		}
		tlsWaitCtx, cancelTLSWait := context.WithTimeout(ctx, 2*time.Minute)
		defer cancelTLSWait()
		if waitErr := grpcutil.WaitForServerTLSFiles(tlsWaitCtx, tlsCfg, logger); waitErr != nil {
			return nil, waitErr
		}
		tlsOpt, tlsErr := grpcutil.ServerTLS(tlsCfg, logger)
		if tlsErr != nil {
			return nil, tlsErr
		}
		opts := append([]grpc.ServerOption{}, serverOpts...)
		if tlsOpt != nil {
			opts = append(opts, tlsOpt)
		}
		grpcSrv := grpc.NewServer(opts...)
		skipperpb.RegisterSkipperChatServiceServer(grpcSrv, grpcChatServer)
		server.RegisterHealthServer(grpcSrv, health.NewServer())
		reflection.Register(grpcSrv)
		return grpcSrv, nil
	}

	router := server.NewServiceRouter(server.RouterSpec{
		Service:            "skipper",
		Logger:             logger,
		Health:             healthChecker,
		Ready:              readiness,
		Metrics:            metricsCollector,
		Runtime:            appCfg.HTTPRuntime,
		DebugToken:         serviceToken,
		DebugConfig:        func() any { return liveConfig.Get() },
		DebugConfigOptions: configOptions,
	})
	apiGroup := router.Group("/api/skipper")
	jwtOpts := []auth.JWTOption{auth.WithServiceIdentity(serviceToken, systemTenantID)}
	apiGroup.Use(auth.JWTAuthMiddleware([]byte(jwtSecret), jwtOpts...))
	apiGroup.Use(skipperContextBridge())
	apiGroup.Use(metering.AccessMiddleware(metering.AccessMiddlewareConfig{
		Purser:            billingClient,
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
				knowledge.WithOriginRewrites(cfg.CrawlOriginRewrites),
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
					adminAPI.RegisterRoutes(router, []byte(jwtSecret), jwtOpts, skipperContextBridge())
					// A crawl settles only in the goroutine running it, so a
					// restart mid-crawl strands the row and blocks every later
					// crawl of that sitemap. Nothing else recovers it.
					go adminAPI.ReapAbandonedCrawls(context.Background())
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
			SourceVariables: map[string]string{
				"DOCS_PUBLIC_URL":      appCfg.DocsPublicURL,
				"MARKETING_PUBLIC_URL": appCfg.MarketingPublicURL,
			},
			Logger: logger,
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
			claims, err := auth.ValidateInteractiveJWT(token, jwtSecretBytes)
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
	if appCfg.WebUIEnabled {
		adminTenantID := cfg.AdminTenantID
		if adminTenantID == "" {
			adminTenantID = "local"
			logger.Info("SKIPPER_ADMIN_TENANT_ID not set; embedded WebUI will use tenant 'local'")
		}
		adminAPIKey := cfg.AdminAPIKey
		enableUI := true
		if adminAPIKey == "" {
			if !appCfg.WebUIInsecure {
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
			adminGroup.Use(adminAuthMiddleware(adminTenantID, []byte(jwtSecret), adminAPIKey, func() bool { return liveConfig.Get().IsDevelopment() }))
			adminGroup.Use(skipperContextBridge())
			chat.RegisterRoutes(adminGroup, chatHandler)

			uiHandler := webui.Handler(webui.Config{APIURL: "/admin/api"})
			router.NoRoute(gin.WrapH(uiHandler))
			logger.Info("Web UI enabled at /")
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Best-effort service registration in Quartermaster (using gRPC)
	go func() {
		if qmClient == nil {
			logger.Warn("Quartermaster bootstrap skipped: client unavailable")
			return
		}
		req, reqErr := qmbootstrap.NewServiceRequest(qmbootstrap.ServiceRegistration{
			ServiceType:   "skipper",
			Port:          appCfg.HTTPListen.Port,
			AdvertiseHost: appCfg.AdvertiseHost,
			ClusterID:     appCfg.ClusterID,
			NodeID:        appCfg.NodeID,
		})
		if reqErr != nil {
			logger.WithError(reqErr).Warn("Quartermaster bootstrap skipped")
			return
		}
		if _, bootstrapErr := qmbootstrap.BootstrapServiceWithRetry(ctx, qmClient, req, logger, qmbootstrap.DefaultRetryConfig("skipper")); bootstrapErr != nil {
			logger.WithError(bootstrapErr).Warn("Quartermaster bootstrap (skipper) failed")
		} else {
			logger.Info("Quartermaster bootstrap (skipper) ok")
		}
	}()

	server.RegisterEnvFileReload("skipper", logger)
	if runErr := server.Run(ctx, server.RunSpec{
		Service: "skipper",
		Logger:  logger,
		Ready:   readiness,
		HTTP: []server.HTTPListener{{
			Name:    "http",
			Port:    appCfg.HTTPListen.Port,
			Handler: router,
			// Chat responses stream over SSE across multi-round tool calls, so
			// the write deadline is 5 minutes instead of the 30s default.
			WriteTimeout: 5 * time.Minute,
		}},
		GRPC:     []server.GRPCListener{{Name: "grpc", Port: appCfg.GRPCListen.Port, Build: buildGRPCServer}},
		OnReload: []server.ReloadCallback{liveConfig.Reload},
	}); runErr != nil {
		logger.WithError(runErr).Fatal("Server exited with error")
	}
}

func adminAuthMiddleware(tenantID string, jwtSecret []byte, apiKey string, isDevelopment func() bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if apiKey != "" {
			// Handle login endpoint: validate key, set session cookie.
			if c.Request.Method == "POST" && strings.HasSuffix(c.Request.URL.Path, "/auth") {
				var req struct {
					Key string `json:"key"`
				}
				if err := c.ShouldBindJSON(&req); err == nil &&
					subtle.ConstantTimeCompare([]byte(req.Key), []byte(apiKey)) == 1 {
					setAdminSessionCookie(c, apiKey, isDevelopment())
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
				setAdminSessionCookie(c, apiKey, isDevelopment())
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

// setAdminSessionCookie marks the session cookie Secure outside development.
func setAdminSessionCookie(c *gin.Context, apiKey string, isDevelopment bool) {
	c.SetCookie("skipper_session", adminSessionMAC(apiKey), 86400, "/", "", !isDevelopment, true)
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
