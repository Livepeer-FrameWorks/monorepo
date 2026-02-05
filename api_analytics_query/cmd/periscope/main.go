package main

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	periscopegrpc "frameworks/api_analytics_query/internal/grpc"
	"frameworks/api_analytics_query/internal/metrics"
	"frameworks/api_analytics_query/internal/scheduler"
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
	logger := logging.NewLoggerWithService("periscope-query")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Periscope-Query (Analytics Query API)")

	// PostgreSQL is ONLY used for billing_cursors (usage summary tracking)
	// All analytics queries use ClickHouse exclusively
	dbURL := config.RequireEnv("DATABASE_URL")
	clickhouseHost := config.RequireEnv("CLICKHOUSE_HOST")
	clickhouseDB := config.RequireEnv("CLICKHOUSE_DB")
	clickhouseUser := config.RequireEnv("CLICKHOUSE_USER")
	clickhousePassword := config.RequireEnv("CLICKHOUSE_PASSWORD")
	jwtSecret := config.RequireEnv("JWT_SECRET")
	serviceToken := config.RequireEnv("SERVICE_TOKEN")
	quartermasterGRPCAddr := config.GetEnv("QUARTERMASTER_GRPC_ADDR", "quartermaster:19002")

	// PostgreSQL for billing cursors only
	dbConfig := database.DefaultConfig()
	dbConfig.URL = dbURL
	yugaDB := database.MustConnect(dbConfig, logger)
	defer func() { _ = yugaDB.Close() }()

	// Connect to ClickHouse (primary analytics database)
	chConfig := database.DefaultClickHouseConfig()
	chConfig.Addr = []string{clickhouseHost}
	chConfig.Database = clickhouseDB
	chConfig.Username = clickhouseUser
	chConfig.Password = clickhousePassword
	clickhouse := database.MustConnectClickHouse(chConfig, logger)
	defer func() { _ = clickhouse.Close() }()

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("periscope-query", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("periscope-query", version.Version, version.GitCommit)

	// Add health checks
	healthChecker.AddCheck("postgres", monitoring.DatabaseHealthCheck(yugaDB))
	healthChecker.AddCheck("clickhouse", monitoring.DatabaseHealthCheck(clickhouse))
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"DATABASE_URL":    dbURL,
		"CLICKHOUSE_HOST": clickhouseHost,
		"CLICKHOUSE_DB":   clickhouseDB,
		"JWT_SECRET":      jwtSecret,
	}))

	// Create custom analytics query metrics (used by gRPC server)
	serviceMetrics := &metrics.Metrics{
		AnalyticsQueries:  metricsCollector.NewCounter("analytics_queries_total", "Analytics queries executed", []string{"query_type", "status"}),
		QueryDuration:     metricsCollector.NewHistogram("analytics_query_duration_seconds", "Analytics query duration", []string{"query_type"}, nil),
		ClickHouseQueries: metricsCollector.NewCounter("clickhouse_queries_total", "ClickHouse queries executed", []string{"table", "status"}),
		CursorCollisions:  metricsCollector.NewCounter("analytics_cursor_collisions_total", "Cursor collisions detected during pagination", []string{"query"}),
	}

	// Initialize and start scheduler for billing summarization (uses yugaDB for cursors)
	taskScheduler := scheduler.NewScheduler(yugaDB, clickhouse, logger)
	taskScheduler.Start()
	defer taskScheduler.Stop()

	// Setup router with unified monitoring (health/metrics only - all API routes removed, now handled via gRPC)
	router := server.SetupServiceRouter(logger, "periscope-query", healthChecker, metricsCollector)

	// Start gRPC server in a goroutine
	grpcPort := config.GetEnv("GRPC_PORT", "19004")
	go func() {
		grpcAddr := fmt.Sprintf(":%s", grpcPort)
		lis, err := net.Listen("tcp", grpcAddr)
		if err != nil {
			logger.WithError(err).Fatal("Failed to listen on gRPC port")
		}

		grpcServer := periscopegrpc.NewGRPCServer(periscopegrpc.GRPCServerConfig{
			ClickHouse:   clickhouse,
			Logger:       logger,
			ServiceToken: serviceToken,
			JWTSecret:    []byte(jwtSecret),
			Metrics:      serviceMetrics,
		})
		logger.WithField("addr", grpcAddr).Info("Starting gRPC server")

		if err := grpcServer.Serve(lis); err != nil {
			logger.WithError(err).Fatal("gRPC server failed")
		}
	}()

	// Start HTTP server with graceful shutdown (health/metrics only)
	serverConfig := server.DefaultConfig("periscope-query", "18004")

	// Best-effort service registration in Quartermaster (using gRPC)
	// Must be launched BEFORE server.Start() which blocks
	go func() {
		qc, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
			GRPCAddr:     quartermasterGRPCAddr,
			Timeout:      10 * time.Second,
			Logger:       logger,
			ServiceToken: serviceToken,
		})
		if err != nil {
			logger.WithError(err).Warn("Failed to create Quartermaster gRPC client")
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
		advertiseHost := config.GetEnv("PERISCOPE_QUERY_HOST", "periscope-query")
		clusterID := config.GetEnv("CLUSTER_ID", "")
		if _, err := qc.BootstrapService(ctx, &pb.BootstrapServiceRequest{
			Type:           "periscope_query",
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
			logger.WithError(err).Warn("Quartermaster bootstrap (periscope_query) failed")
		} else {
			logger.Info("Quartermaster bootstrap (periscope_query) ok")
		}
	}()

	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}
}
