package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"

	commodoregrpc "frameworks/api_control/internal/grpc"
	decklogclient "frameworks/pkg/clients/decklog"
	foghornclient "frameworks/pkg/clients/foghorn"
	"frameworks/pkg/clients/listmonk"
	purserclient "frameworks/pkg/clients/purser"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/server"
	"frameworks/pkg/version"
	"time"
)

func main() {
	// Setup logger
	logger := logging.NewLoggerWithService("commodore")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Commodore (Control API)")

	dbURL := config.RequireEnv("DATABASE_URL")
	jwtSecret := config.RequireEnv("JWT_SECRET")
	serviceToken := config.RequireEnv("SERVICE_TOKEN")
	foghornControlAddr := config.GetEnv("FOGHORN_CONTROL_ADDR", "foghorn:18019")

	// Connect to database
	dbConfig := database.DefaultConfig()
	dbConfig.URL = dbURL
	db := database.MustConnect(dbConfig, logger)
	defer db.Close()

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("commodore", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("commodore", version.Version, version.GitCommit)

	// Add health checks
	healthChecker.AddCheck("database", monitoring.DatabaseHealthCheck(db))
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"DATABASE_URL": dbURL,
		"JWT_SECRET":   jwtSecret,
	}))

	// Create custom auth and stream metrics for gRPC server
	serverMetrics := &commodoregrpc.ServerMetrics{
		AuthOperations:   metricsCollector.NewCounter("auth_operations_total", "Authentication operations", []string{"operation", "status"}),
		AuthDuration:     metricsCollector.NewHistogram("auth_operation_duration_seconds", "Authentication operation duration", []string{"operation"}, nil),
		StreamOperations: metricsCollector.NewCounter("stream_operations_total", "Stream CRUD operations", []string{"operation", "status"}),
	}

	// Create Foghorn gRPC client for clip/DVR/viewer operations
	//
	// TODO: Multi-cluster support - Currently uses a single Foghorn address.
	// In multi-cluster deployments, each tenant maps to a specific cluster with its own Foghorn.
	// Future options:
	//   1. Query Quartermaster to resolve tenant -> cluster -> foghorn_grpc_addr
	//   2. Move clip/DVR/viewer resolution to Gateway instead of proxying through Commodore
	// For now, this works for single-cluster deployments.
	foghornClient, err := foghornclient.NewGRPCClient(foghornclient.GRPCConfig{
		GRPCAddr:     foghornControlAddr,
		Timeout:      30 * time.Second,
		Logger:       logger,
		ServiceToken: serviceToken,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Foghorn gRPC client - clip/DVR/viewer operations will be unavailable")
		foghornClient = nil
	} else {
		defer foghornClient.Close()
		logger.WithField("addr", foghornControlAddr).Info("Connected to Foghorn gRPC")
	}

	// Create Quartermaster gRPC client for tenant creation during registration
	quartermasterGRPCAddr := config.GetEnv("QUARTERMASTER_GRPC_ADDR", "quartermaster:19002")
	quartermasterGRPCClient, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:     quartermasterGRPCAddr,
		Timeout:      30 * time.Second,
		Logger:       logger,
		ServiceToken: serviceToken,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Quartermaster gRPC client - tenant creation will use fallback")
		quartermasterGRPCClient = nil
	} else {
		defer quartermasterGRPCClient.Close()
		logger.WithField("addr", quartermasterGRPCAddr).Info("Connected to Quartermaster gRPC")
	}

	// Create Purser gRPC client for user limit checking during registration
	purserGRPCAddr := config.GetEnv("PURSER_GRPC_ADDR", "purser:19003")
	purserGRPCClient, err := purserclient.NewGRPCClient(purserclient.GRPCConfig{
		GRPCAddr:     purserGRPCAddr,
		Timeout:      30 * time.Second,
		Logger:       logger,
		ServiceToken: serviceToken,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Purser gRPC client - user limit checks will be skipped")
		purserGRPCClient = nil
	} else {
		defer purserGRPCClient.Close()
		logger.WithField("addr", purserGRPCAddr).Info("Connected to Purser gRPC")
	}

	// Create Decklog gRPC client for service events
	decklogGRPCAddr := config.GetEnv("DECKLOG_GRPC_ADDR", "decklog:18006")
	decklogClient, err := decklogclient.NewBatchedClient(decklogclient.BatchedClientConfig{
		Target:        decklogGRPCAddr,
		AllowInsecure: config.GetEnvBool("DECKLOG_ALLOW_INSECURE", true),
		Timeout:       5 * time.Second,
		Source:        "commodore",
		ServiceToken:  serviceToken,
	}, logger)
	if err != nil {
		logger.WithError(err).Warn("Failed to create Decklog gRPC client - service events will be disabled")
		decklogClient = nil
	} else {
		defer decklogClient.Close()
		logger.WithField("addr", decklogGRPCAddr).Info("Connected to Decklog gRPC")
	}

	// Create Listmonk client for newsletter subscription
	var listmonkClient *listmonk.Client
	defaultMailingListID := 1
	if listmonkURL := os.Getenv("LISTMONK_URL"); listmonkURL != "" {
		listmonkUser := os.Getenv("LISTMONK_USERNAME")
		listmonkPass := os.Getenv("LISTMONK_PASSWORD")
		listmonkClient = listmonk.NewClient(listmonkURL, listmonkUser, listmonkPass)
		if id, err := strconv.Atoi(os.Getenv("DEFAULT_MAILING_LIST_ID")); err == nil {
			defaultMailingListID = id
		}
		logger.WithField("url", listmonkURL).Info("Listmonk client configured")
	}

	// Setup router with unified monitoring (health/metrics only)
	// NOTE: All API routes removed - now handled via gRPC only.
	// Gateway -> Commodore gRPC for all auth, streams, clips, DVR, etc.
	app := server.SetupServiceRouter(logger, "commodore", healthChecker, metricsCollector)

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
			Logger:               logger,
			FoghornClient:        foghornClient,
			QuartermasterClient:  quartermasterGRPCClient,
			PurserClient:         purserGRPCClient,
			ListmonkClient:       listmonkClient,
			DecklogClient:        decklogClient,
			DefaultMailingListID: defaultMailingListID,
			Metrics:              serverMetrics,
			ServiceToken:         serviceToken,
			JWTSecret:            []byte(jwtSecret),
			TurnstileSecretKey:   config.GetEnv("TURNSTILE_AUTH_SECRET_KEY", ""),
			TurnstileFailOpen:    config.GetEnvBool("TURNSTILE_FAIL_OPEN", false),
			PasswordResetSecret:  []byte(config.GetEnv("PASSWORD_RESET_SECRET", "")),
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
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		healthEndpoint := "/health"
		httpPort, _ := strconv.Atoi(serverConfig.Port)
		advertiseHost := config.GetEnv("COMMODORE_HOST", "commodore")
		clusterID := config.GetEnv("CLUSTER_ID", "")
		if _, err := quartermasterGRPCClient.BootstrapService(ctx, &pb.BootstrapServiceRequest{
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
		}); err != nil {
			logger.WithError(err).Warn("Quartermaster bootstrap (commodore) failed")
		} else {
			logger.Info("Quartermaster bootstrap (commodore) ok")
		}
	}()

	if err := server.Start(serverConfig, app, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}
}
