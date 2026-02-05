package main

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	qmgrpc "frameworks/api_tenants/internal/grpc"
	"frameworks/api_tenants/internal/handlers"
	decklogclient "frameworks/pkg/clients/decklog"
	"frameworks/pkg/clients/navigator"
	purserclient "frameworks/pkg/clients/purser"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/server"
	"frameworks/pkg/version"
)

func main() {
	// Setup logger
	logger := logging.NewLoggerWithService("quartermaster")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Quartermaster (Tenant Management API)")

	dbURL := config.RequireEnv("DATABASE_URL")
	serviceToken := config.RequireEnv("SERVICE_TOKEN")
	jwtSecret := config.RequireEnv("JWT_SECRET")
	quartermasterGRPCAddr := config.GetEnv("QUARTERMASTER_GRPC_ADDR", "localhost:19002")
	navigatorURL := config.GetEnv("NAVIGATOR_URL", "") // Load Navigator URL (optional)

	// Connect to database
	dbConfig := database.DefaultConfig()
	dbConfig.URL = dbURL
	db := database.MustConnect(dbConfig, logger)
	defer func() { _ = db.Close() }()

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("quartermaster", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("quartermaster", version.Version, version.GitCommit)

	// Add health checks
	healthChecker.AddCheck("database", monitoring.DatabaseHealthCheck(db))
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"DATABASE_URL":  dbURL,
		"SERVICE_TOKEN": serviceToken,
	}))

	// Create gRPC server metrics
	serverMetrics := &qmgrpc.ServerMetrics{
		TenantOperations:  metricsCollector.NewCounter("grpc_tenant_operations_total", "gRPC tenant operations", []string{"operation", "status"}),
		ClusterOperations: metricsCollector.NewCounter("grpc_cluster_operations_total", "gRPC cluster operations", []string{"operation", "status"}),
		NodeOperations:    metricsCollector.NewCounter("grpc_node_operations_total", "gRPC node operations", []string{"operation", "status"}),
		ServiceOperations: metricsCollector.NewCounter("grpc_service_operations_total", "gRPC service registry operations", []string{"operation", "status"}),
		GRPCRequests:      metricsCollector.NewCounter("grpc_requests_total", "Total gRPC requests", []string{"method", "status"}),
		GRPCDuration:      metricsCollector.NewHistogram("grpc_request_duration_seconds", "gRPC request duration", []string{"method"}, nil),
	}

	// Initialize Navigator client
	var navigatorClient *navigator.Client
	var err error

	if navigatorURL != "" {
		navigatorClient, err = navigator.NewClient(navigator.Config{
			Addr:         navigatorURL,
			Timeout:      5 * time.Second,
			Logger:       logger,
			ServiceToken: serviceToken,
		})
		if err != nil {
			logger.WithError(err).Error("Failed to create Navigator client - DNS features will be disabled")
		} else {
			defer func() { _ = navigatorClient.Close() }() // Ensure the client connection is closed
		}
	} else {
		logger.Info("NAVIGATOR_URL not set - DNS features will be disabled")
	}

	// Create Decklog gRPC client for service events
	decklogGRPCAddr := config.GetEnv("DECKLOG_GRPC_ADDR", "decklog:18006")
	decklogClient, err := decklogclient.NewBatchedClient(decklogclient.BatchedClientConfig{
		Target:        decklogGRPCAddr,
		AllowInsecure: config.GetEnvBool("DECKLOG_ALLOW_INSECURE", true),
		Timeout:       5 * time.Second,
		Source:        "quartermaster",
		ServiceToken:  serviceToken,
	}, logger)
	if err != nil {
		logger.WithError(err).Warn("Failed to create Decklog gRPC client - service events will be disabled")
		decklogClient = nil
	} else {
		defer func() { _ = decklogClient.Close() }()
		logger.WithField("addr", decklogGRPCAddr).Info("Connected to Decklog gRPC")
	}

	// Create Purser gRPC client for billing status lookups (cross-service via gRPC, not DB)
	purserGRPCAddr := config.GetEnv("PURSER_GRPC_ADDR", "purser:19003")
	var purserClient *purserclient.GRPCClient
	purserClient, err = purserclient.NewGRPCClient(purserclient.GRPCConfig{
		GRPCAddr:     purserGRPCAddr,
		Timeout:      5 * time.Second,
		Logger:       logger,
		ServiceToken: serviceToken,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Purser gRPC client - billing status lookups will use defaults")
		purserClient = nil
	} else {
		defer func() { _ = purserClient.Close() }()
		logger.WithField("addr", purserGRPCAddr).Info("Connected to Purser gRPC")
	}

	// Initialize handlers (for health poller)
	handlers.Init(db, logger)

	// Setup router with unified monitoring
	router := server.SetupServiceRouter(logger, "quartermaster", healthChecker, metricsCollector)

	// NOTE: All API routes removed - now handled via gRPC only.
	// Gateway -> Quartermaster gRPC for all tenant, cluster, node, service operations.

	// Start health poller before serving
	handlers.StartHealthPoller()

	// Start gRPC server in a goroutine
	grpcPort := config.GetEnv("GRPC_PORT", "19002")
	go func() {
		grpcAddr := fmt.Sprintf(":%s", grpcPort)
		lis, err := net.Listen("tcp", grpcAddr)
		if err != nil {
			logger.WithError(err).Fatal("Failed to listen on gRPC port")
		}

		grpcServer := qmgrpc.NewGRPCServer(qmgrpc.GRPCServerConfig{
			DB:              db,
			Logger:          logger,
			ServiceToken:    serviceToken,
			JWTSecret:       []byte(jwtSecret),
			NavigatorClient: navigatorClient,
			DecklogClient:   decklogClient,
			PurserClient:    purserClient,
			Metrics:         serverMetrics,
		})
		logger.WithField("addr", grpcAddr).Info("Starting gRPC server")

		if err := grpcServer.Serve(lis); err != nil {
			logger.WithError(err).Fatal("gRPC server failed")
		}
	}()

	// Start HTTP server with graceful shutdown
	serverConfig := server.DefaultConfig("quartermaster", "18002")

	// Best-effort self-registration in Quartermaster (idempotent, using gRPC)
	// Must be launched BEFORE server.Start() which blocks
	go func() {
		qc, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
			GRPCAddr:     quartermasterGRPCAddr,
			Timeout:      10 * time.Second,
			Logger:       logger,
			ServiceToken: serviceToken,
		})
		if err != nil {
			logger.WithError(err).Warn("Failed to create Quartermaster gRPC client for self-registration")
			return
		}
		defer func() { _ = qc.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		healthEndpoint := "/health"
		httpPort, _ := strconv.Atoi(serverConfig.Port)
		if httpPort <= 0 || httpPort > 65535 {
			logger.Warn("Quartermaster bootstrap skipped: invalid port")
			return
		}
		advertiseHost := config.GetEnv("QUARTERMASTER_HOST", "quartermaster")
		clusterID := config.GetEnv("CLUSTER_ID", "")
		_, _ = qc.BootstrapService(ctx, &pb.BootstrapServiceRequest{
			Type:           "quartermaster",
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
		})
	}()

	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}
}
