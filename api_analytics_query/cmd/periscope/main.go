package main

import (
	"context"
	"time"

	"frameworks/api_analytics_query/internal/appconfig"
	"frameworks/api_analytics_query/internal/database/periscopequerydb"
	periscopegrpc "frameworks/api_analytics_query/internal/grpc"
	"frameworks/api_analytics_query/internal/metrics"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"
	"google.golang.org/grpc"
)

func main() {
	if version.HandleCLI() {
		return
	}

	// Setup logger
	logger := logging.NewLoggerWithService("periscope-query")
	periscopequerydb.SetObserverService("periscope-query")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Periscope-Query (Analytics Query API)")

	configOptions := config.Options{Service: "periscope-query", Logger: logger}
	cfg, err := config.Load[appconfig.PeriscopeQuery](configOptions)
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}
	cfg.ApplyLogLevel(logger)

	// Connect to ClickHouse (primary analytics database)
	chConfig := database.DefaultClickHouseConfig()
	chConfig.ServiceName = "periscope-query"
	chConfig.Addr = cfg.Addr
	chConfig.Database = cfg.Database
	chConfig.Username = cfg.User
	chConfig.Password = cfg.Password
	clickhouse := database.MustConnectClickHouse(chConfig, logger)
	defer func() { _ = clickhouse.Close() }()
	dbConfig := database.DefaultConfig()
	dbConfig.ServiceName = "periscope-query"
	dbConfig.URL = cfg.DatabaseURL
	replayDB := database.MustConnect(dbConfig, logger)
	defer func() { _ = replayDB.Close() }()

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("periscope-query", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("periscope-query", version.Version, version.GitCommit)

	// Add health checks
	healthChecker.AddCheck("clickhouse", monitoring.DatabaseHealthCheck(clickhouse))
	healthChecker.AddCheck("postgres", monitoring.DatabaseHealthCheck(replayDB))

	// Queries need both stores, so readiness requires both to answer.
	readiness := monitoring.NewReadinessChecker("periscope-query", version.Version)
	readiness.AddCheck("clickhouse", monitoring.DatabaseHealthCheck(clickhouse))
	readiness.AddCheck("postgres", monitoring.DatabaseHealthCheck(replayDB))

	// Per-RPC counts + duration come from GRPCMetricsInterceptor on the
	// GRPCRequests / GRPCDuration vectors; a parallel analytics_queries
	// counter where query_type maps 1:1 to the method would just rename
	// the same axis. clickhouse_queries_total{table} would be meaningful
	// (table != method) but every QueryContext call site is bespoke; wire
	// it when there is a single chokepoint to instrument, not before.
	serviceMetrics := &metrics.Metrics{
		CursorCollisions: metricsCollector.NewCounter("analytics_cursor_collisions_total", "Cursor collisions detected during pagination", []string{"query"}),
		GRPCRequests:     metricsCollector.NewCounter("grpc_requests_total", "Total gRPC requests", []string{"method", "status"}),
		GRPCDuration:     metricsCollector.NewHistogram("grpc_request_duration_seconds", "gRPC request duration", []string{"method"}, nil),
	}

	// Expose health, readiness, and metrics over HTTP; query APIs are served over gRPC.
	router := server.NewServiceRouter(server.RouterSpec{
		Service:            "periscope-query",
		Logger:             logger,
		Health:             healthChecker,
		Ready:              readiness,
		Metrics:            metricsCollector,
		Runtime:            cfg.HTTPRuntime,
		DebugToken:         cfg.ServiceToken,
		DebugConfig:        func() any { return cfg },
		DebugConfigOptions: configOptions,
	})

	// NewGRPCServer waits for the gRPC TLS files, so the server builds in the
	// background while HTTP health already serves.
	buildGRPCServer := func(buildCtx context.Context) (*grpc.Server, error) {
		return periscopegrpc.NewGRPCServer(buildCtx, periscopegrpc.GRPCServerConfig{
			ClickHouse:    clickhouse,
			ReplayDB:      replayDB,
			Logger:        logger,
			ServiceToken:  cfg.ServiceToken,
			JWTSecret:     []byte(cfg.JWTSecret),
			Metrics:       serviceMetrics,
			CertFile:      cfg.CertPath,
			KeyFile:       cfg.KeyPath,
			AllowInsecure: cfg.AllowInsecure,
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Best-effort service registration in Quartermaster (using gRPC)
	go func() {
		qc, qcErr := qmclient.NewGRPCClient(qmclient.GRPCConfig{
			GRPCAddr:      cfg.QuartermasterGRPCAddr,
			Timeout:       10 * time.Second,
			Logger:        logger,
			ServiceToken:  cfg.ServiceToken,
			AllowInsecure: cfg.AllowInsecure,
			CACertFile:    cfg.CAPath,
			ServerName:    cfg.QuartermasterGRPCTLSServerName,
		})
		if qcErr != nil {
			logger.WithError(qcErr).Warn("Failed to create Quartermaster gRPC client")
			return
		}
		defer func() { _ = qc.Close() }()
		req, reqErr := qmbootstrap.NewServiceRequest(qmbootstrap.ServiceRegistration{
			ServiceType:   "periscope-query",
			Port:          cfg.HTTPListen.Port,
			AdvertiseHost: cfg.AdvertiseHost,
			ClusterID:     cfg.ClusterID,
			NodeID:        cfg.NodeID,
		})
		if reqErr != nil {
			logger.WithError(reqErr).Warn("Quartermaster bootstrap skipped")
			return
		}
		if _, bootstrapErr := qmbootstrap.BootstrapServiceWithRetry(ctx, qc, req, logger, qmbootstrap.DefaultRetryConfig("periscope-query")); bootstrapErr != nil {
			logger.WithError(bootstrapErr).Warn("Quartermaster bootstrap (periscope-query) failed")
		} else {
			logger.Info("Quartermaster bootstrap (periscope-query) ok")
		}
	}()

	server.RegisterEnvFileReload("periscope-query", logger)
	if runErr := server.Run(ctx, server.RunSpec{
		Service: "periscope-query",
		Logger:  logger,
		Ready:   readiness,
		HTTP:    []server.HTTPListener{{Name: "http", Port: cfg.HTTPListen.Port, Handler: router}},
		GRPC:    []server.GRPCListener{{Name: "grpc", Port: cfg.GRPCListen.Port, Build: buildGRPCServer}},
	}); runErr != nil {
		logger.WithError(runErr).Fatal("Server exited with error")
	}
}
