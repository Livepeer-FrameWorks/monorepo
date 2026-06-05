package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"frameworks/api_control/internal/clusterurls"
	commodoregrpc "frameworks/api_control/internal/grpc"
	decklogclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/listmonk"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/navigator"
	purserclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/purser"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"
	"time"
)

func main() {
	if version.HandleCLI() {
		return
	}

	// Bootstrap subcommand dispatcher. The Ansible go_service role invokes the
	// binary with no args to start the gRPC+HTTP server; "bootstrap" is the
	// only subcommand and is invoked explicitly by the bootstrap role.
	if len(os.Args) > 1 && os.Args[1] == "bootstrap" {
		os.Exit(runBootstrapCommand(os.Args[2:]))
	}

	// Setup logger
	logger := logging.NewLoggerWithService("commodore")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Commodore (Control API)")

	dbURL := config.RequireEnv("DATABASE_URL")
	jwtSecret := config.RequireEnv("JWT_SECRET")
	serviceToken := config.RequireEnv("SERVICE_TOKEN")

	// Connect to database
	dbConfig := database.DefaultConfig()
	dbConfig.URL = dbURL
	db := database.MustConnect(dbConfig, logger)
	defer func() { _ = db.Close() }()

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("commodore", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("commodore", version.Version, version.GitCommit)

	// Add health checks
	healthChecker.AddCheck("database", monitoring.DatabaseHealthCheck(db))
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"DATABASE_URL": dbURL,
		"JWT_SECRET":   jwtSecret,
	}))

	// Per-method counters live on commodore_grpc_requests_total{method,status}
	// produced by middleware.GRPCMetricsInterceptor (wired into the gRPC
	// server below); separate auth_/stream_operations counters would just
	// rename the same axis.
	serverMetrics := &commodoregrpc.ServerMetrics{
		GRPCRequests: metricsCollector.NewCounter("grpc_requests_total", "Total gRPC requests", []string{"method", "status"}),
		GRPCDuration: metricsCollector.NewHistogram("grpc_request_duration_seconds", "gRPC request duration", []string{"method"}, nil),
	}

	foghornPool := foghornclient.NewPool(foghornclient.PoolConfig{
		ServiceToken:  serviceToken,
		Timeout:       30 * time.Second,
		Logger:        logger,
		MaxIdleTime:   10 * time.Minute,
		CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:    config.GetServiceGRPCTLSServerName("foghorn"),
		AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
	})
	defer foghornPool.Close()

	// Create Quartermaster gRPC client for tenant creation during registration
	quartermasterGRPCAddr := config.GetEnv("QUARTERMASTER_GRPC_ADDR", "quartermaster:19002")
	quartermasterGRPCClient, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:      quartermasterGRPCAddr,
		Timeout:       30 * time.Second,
		Logger:        logger,
		ServiceToken:  serviceToken,
		AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:    config.GetServiceGRPCTLSServerName("quartermaster"),
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Quartermaster gRPC client - tenant creation will use fallback")
		quartermasterGRPCClient = nil
	} else {
		defer func() { _ = quartermasterGRPCClient.Close() }()
		logger.WithField("addr", quartermasterGRPCAddr).Info("Connected to Quartermaster gRPC")
	}

	// Optional Navigator gRPC client. Used for GetTenantAliasStatus when
	// populating CreateStreamResponse.tenant_*_domain fields. Without
	// Navigator, those fields stay unset and clients fall back to
	// global / cluster-concrete URLs.
	navigatorGRPCAddr := config.GetEnv("NAVIGATOR_GRPC_ADDR", "")
	var navigatorGRPCClient *navigator.Client
	if navigatorGRPCAddr != "" {
		navigatorGRPCClient, err = navigator.NewClient(navigator.Config{
			Addr:          navigatorGRPCAddr,
			Timeout:       5 * time.Second,
			Logger:        logger,
			ServiceToken:  serviceToken,
			AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
			CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
			ServerName:    config.GetServiceGRPCTLSServerName("navigator"),
		})
		if err != nil {
			logger.WithError(err).Warn("Failed to create Navigator gRPC client - tenant alias status lookups disabled")
			navigatorGRPCClient = nil
		} else {
			defer func() { _ = navigatorGRPCClient.Close() }()
			logger.WithField("addr", navigatorGRPCAddr).Info("Connected to Navigator gRPC")
		}
	}

	// Create Purser gRPC client for user limit checking during registration
	purserGRPCAddr := config.GetEnv("PURSER_GRPC_ADDR", "purser:19003")
	purserGRPCClient, err := purserclient.NewGRPCClient(purserclient.GRPCConfig{
		GRPCAddr:      purserGRPCAddr,
		Timeout:       30 * time.Second,
		Logger:        logger,
		ServiceToken:  serviceToken,
		AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:    config.GetServiceGRPCTLSServerName("purser"),
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Purser gRPC client - user limit checks will be skipped")
		purserGRPCClient = nil
	} else {
		defer func() { _ = purserGRPCClient.Close() }()
		logger.WithField("addr", purserGRPCAddr).Info("Connected to Purser gRPC")
	}

	// Create Decklog gRPC client for service events
	decklogGRPCAddr := config.GetEnv("DECKLOG_GRPC_ADDR", "decklog:18006")
	decklogClient, err := decklogclient.NewBatchedClient(decklogclient.BatchedClientConfig{
		Target:        decklogGRPCAddr,
		AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:    config.GetServiceGRPCTLSServerName("decklog"),
		Timeout:       5 * time.Second,
		Source:        "commodore",
		ServiceToken:  serviceToken,
		ClusterID:     config.GetEnv("CLUSTER_ID", ""),
		SourceRegion:  config.GetEnv("REGION", ""),
	}, logger)
	if err != nil {
		logger.WithError(err).Warn("Failed to create Decklog gRPC client - service events will be disabled")
		decklogClient = nil
	} else {
		defer func() { _ = decklogClient.Close() }()
		logger.WithField("addr", decklogGRPCAddr).Info("Connected to Decklog gRPC")
	}

	// Create Listmonk client for newsletter subscription
	var listmonkClient *listmonk.Client
	defaultMailingListID := 1
	if listmonkURL := os.Getenv("LISTMONK_URL"); listmonkURL != "" {
		listmonkUser := strings.TrimSpace(os.Getenv("LISTMONK_API_USERNAME"))
		listmonkToken := strings.TrimSpace(os.Getenv("LISTMONK_API_TOKEN"))
		if listmonkUser == "" || listmonkToken == "" {
			logger.Warn("LISTMONK_URL is set but LISTMONK_API_USERNAME or LISTMONK_API_TOKEN is missing; newsletter integration disabled")
		} else {
			listmonkClient = listmonk.NewClient(listmonkURL, listmonkUser, listmonkToken)
			logger.WithField("url", listmonkURL).Info("Listmonk client configured")
		}
		if id, err := strconv.Atoi(os.Getenv("DEFAULT_MAILING_LIST_ID")); err == nil {
			defaultMailingListID = id
		}
	}

	// Expose health and metrics over HTTP; product APIs are served over gRPC.
	app := server.SetupServiceRouter(logger, "commodore", healthChecker, metricsCollector)

	// Cluster routing snapshot: read paths derive Chandler URLs from cluster_id
	// without a per-row network call. Refreshes from Quartermaster every 60s.
	clusterURLsResolver := clusterurls.NewResolver(quartermasterGRPCClient, logger)
	clusterURLsResolver.Start(context.Background(), 60*time.Second)

	// Start gRPC server in a goroutine
	grpcPort := config.GetEnv("GRPC_PORT", "19001")
	go func() {
		grpcAddr := fmt.Sprintf(":%s", grpcPort)
		lis, err := net.Listen("tcp", grpcAddr)
		if err != nil {
			logger.WithError(err).Fatal("Failed to listen on gRPC port")
		}

		grpcServer := commodoregrpc.NewGRPCServer(commodoregrpc.CommodoreServerConfig{
			DB:                   db,
			DBMaxIdleConns:       dbConfig.MaxIdleConns,
			Logger:               logger,
			FoghornPool:          foghornPool,
			QuartermasterClient:  quartermasterGRPCClient,
			NavigatorClient:      navigatorGRPCClient,
			PurserClient:         purserGRPCClient,
			ListmonkClient:       listmonkClient,
			DecklogClient:        decklogClient,
			ClusterURLs:          clusterURLsResolver,
			DefaultMailingListID: defaultMailingListID,
			Metrics:              serverMetrics,
			ServiceToken:         serviceToken,
			JWTSecret:            []byte(jwtSecret),
			TurnstileSecretKey:   config.GetEnv("TURNSTILE_AUTH_SECRET_KEY", ""),
			TurnstileFailOpen:    config.GetEnvBool("TURNSTILE_FAIL_OPEN", false),
			PasswordResetSecret:  []byte(config.GetEnv("PASSWORD_RESET_SECRET", "")),
			CertFile:             config.GetEnv("GRPC_TLS_CERT_PATH", ""),
			KeyFile:              config.GetEnv("GRPC_TLS_KEY_PATH", ""),
			AllowInsecure:        config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		})
		logger.WithField("addr", grpcAddr).Info("Starting gRPC server")

		if err := grpcServer.Serve(lis); err != nil {
			logger.WithError(err).Fatal("gRPC server failed")
		}
	}()

	// Start HTTP server with graceful shutdown
	serverConfig := server.DefaultConfig("commodore", "18001")

	// Best-effort service registration in Quartermaster (using gRPC client)
	// Must be launched BEFORE server.Start() which blocks
	go func() {
		if quartermasterGRPCClient == nil {
			logger.Warn("Quartermaster gRPC client not available, skipping bootstrap")
			return
		}
		healthEndpoint := "/health"
		httpPort, _ := strconv.Atoi(serverConfig.Port)
		if httpPort <= 0 || httpPort > 65535 {
			logger.Warn("Quartermaster bootstrap skipped: invalid port")
			return
		}
		advertiseHost := config.GetEnv("COMMODORE_HOST", "commodore")
		clusterID := config.GetEnv("CLUSTER_ID", "")
		req := &quartermasterpb.BootstrapServiceRequest{
			Type:           "commodore",
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
		if _, err := qmbootstrap.BootstrapServiceWithRetry(context.Background(), quartermasterGRPCClient, req, logger, qmbootstrap.DefaultRetryConfig("commodore")); err != nil {
			logger.WithError(err).Warn("Quartermaster bootstrap (commodore) failed")
		} else {
			logger.Info("Quartermaster bootstrap (commodore) ok")
		}
	}()

	server.RegisterEnvFileReload("commodore", logger)
	if err := server.Start(serverConfig, app, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}
}
