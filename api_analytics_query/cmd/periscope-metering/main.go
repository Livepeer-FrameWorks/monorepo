package main

import (
	"context"
	"os"
	"time"

	"frameworks/api_analytics_query/internal/appconfig"
	"frameworks/api_analytics_query/internal/database/periscopequerydb"
	"frameworks/api_analytics_query/internal/datamigrations"
	"frameworks/api_analytics_query/internal/handlers"
	"frameworks/api_analytics_query/internal/scheduler"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"
)

func main() {
	if version.HandleCLI() {
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "data-migrations" {
		logger := logging.NewLoggerWithService("periscope-metering")
		if err := runDataMigrationCommand(os.Args[1:], logger); err != nil {
			logger.WithError(err).Fatal("Data migration command failed")
		}
		return
	}
	logger := logging.NewLoggerWithService("periscope-metering")
	config.LoadEnv(logger)
	periscopequerydb.SetObserverService("periscope-metering")

	configOptions := config.Options{Service: "periscope-metering", Logger: logger}
	cfg, err := config.Load[appconfig.PeriscopeMetering](configOptions)
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}
	cfg.ApplyLogLevel(logger)
	sourceID, sourceRegion, err := scheduler.NormalizeSourceIdentity(cfg.MeteringSourceID, cfg.MeteringSourceRegion)
	if err != nil {
		logger.WithError(err).Fatal("Invalid metering source identity")
	}

	dbConfig := database.DefaultConfig()
	dbConfig.ServiceName = "periscope-metering"
	dbConfig.URL = cfg.DatabaseURL
	postgres := database.MustConnect(dbConfig, logger)
	defer func() { _ = postgres.Close() }()

	chConfig := database.DefaultClickHouseConfig()
	chConfig.ServiceName = "periscope-metering"
	chConfig.Addr = cfg.Addr
	chConfig.Database = cfg.Database
	chConfig.Username = cfg.User
	chConfig.Password = cfg.Password
	clickhouse := database.MustConnectClickHouse(chConfig, logger)
	defer func() { _ = clickhouse.Close() }()

	datamigrations.RegisterViewerServingAttribution(sourceID, func() (database.ClickHouseConn, error) { return clickhouse, nil })

	health := monitoring.NewHealthChecker("periscope-metering", version.Version)
	metrics := monitoring.NewMetricsCollector("periscope-metering", version.Version, version.GitCommit)
	health.AddCheck("postgres", monitoring.DatabaseHealthCheck(postgres))
	health.AddCheck("clickhouse", monitoring.DatabaseHealthCheck(clickhouse))

	// Metering reads finalized facts from ClickHouse and keeps leases and
	// cursors in PostgreSQL, so readiness requires both to answer.
	readiness := monitoring.NewReadinessChecker("periscope-metering", version.Version)
	readiness.AddCheck("postgres", monitoring.DatabaseHealthCheck(postgres))
	readiness.AddCheck("clickhouse", monitoring.DatabaseHealthCheck(clickhouse))

	billingMetrics := &handlers.BillingMetrics{
		ReservationTenantSnapshots: metrics.NewCounter("billing_reservation_tenant_snapshots_total", "Reservation tenant snapshots by bounded publication outcome", []string{"outcome"}),
		UnresolvedServingTenants:   metrics.NewGauge("billing_reservation_unresolved_tenants", "Tenants whose current reservation snapshot lacks unambiguous serving attribution", nil),
		UnresolvedServingOldestAge: metrics.NewGauge("billing_reservation_unresolved_oldest_age_seconds", "Age of the oldest open lifecycle with unresolved serving attribution", nil),
		ProjectionDivergences: metrics.NewCounter(
			"projection_divergence_billing_total",
			"Projection divergence rows skipped safely by billing outcome and source table",
			[]string{"outcome", "table"},
		),
		MalformedFactsSkipped: metrics.NewCounter(
			"billing_malformed_facts_skipped_total",
			"Malformed finalized facts skipped without blocking the tenant billing cursor",
			[]string{"fact_kind", "reason"},
		),
	}
	tasks := scheduler.NewScheduler(postgres, clickhouse, logger, scheduler.Config{
		Billing: handlers.BillingConfig{
			ViewerAttributionReady: func(ctx context.Context) (bool, error) {
				state, err := datamigrate.LoadJob(ctx, postgres, datamigrations.ViewerServingAttributionID)
				return state.Status == datamigrate.StatusCompleted, err
			},
			SourceID:                       sourceID,
			SourceRegion:                   sourceRegion,
			KafkaBrokers:                   cfg.KafkaBrokers,
			BillingTopic:                   cfg.BillingKafkaTopic,
			QuartermasterGRPCAddr:          cfg.QuartermasterGRPCAddr,
			QuartermasterGRPCTLSServerName: cfg.QuartermasterGRPCTLSServerName,
			ServiceToken:                   cfg.ServiceToken,
			GRPCTLSCAPath:                  cfg.GRPCTLSCAPath,
			GRPCAllowInsecure:              cfg.GRPCAllowInsecure,
			SystemTenantID:                 cfg.SystemTenantUUID(),
			Metrics:                        billingMetrics,
		},
		WorkerID: cfg.MeteringWorkerID,
	})
	validationCtx, cancelValidation := context.WithTimeout(context.Background(), 30*time.Second)
	if err := tasks.ValidateSource(validationCtx); err != nil {
		cancelValidation()
		logger.WithError(err).Fatal("Invalid metering source identity")
	}
	cancelValidation()
	tasks.Start()
	defer tasks.Stop()

	router := server.NewServiceRouter(server.RouterSpec{
		Service:            "periscope-metering",
		Logger:             logger,
		Health:             health,
		Ready:              readiness,
		Metrics:            metrics,
		Runtime:            cfg.HTTPRuntime,
		DebugToken:         cfg.ServiceToken,
		DebugConfig:        func() any { return cfg },
		DebugConfigOptions: configOptions,
	})

	server.RegisterEnvFileReload("periscope-metering", logger)
	if runErr := server.Run(context.Background(), server.RunSpec{
		Service:   "periscope-metering",
		Logger:    logger,
		Ready:     readiness,
		BindAddrs: cfg.BindAddrs,
		HTTP:      []server.HTTPListener{{Name: "http", Port: cfg.Port, Handler: router}},
	}); runErr != nil {
		logger.WithError(runErr).Fatal("Periscope metering server failed")
	}
}
