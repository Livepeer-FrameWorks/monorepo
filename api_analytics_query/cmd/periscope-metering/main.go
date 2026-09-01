package main

import (
	"context"
	"strings"
	"time"

	"frameworks/api_analytics_query/internal/database/periscopequerydb"
	"frameworks/api_analytics_query/internal/scheduler"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"
)

func main() {
	if version.HandleCLI() {
		return
	}
	logger := logging.NewLoggerWithService("periscope-metering")
	config.LoadEnv(logger)
	periscopequerydb.SetObserverService("periscope-metering")

	dbURL := config.RequireEnv("DATABASE_URL")
	clickhouseAddr := config.RequireEnv("CLICKHOUSE_ADDR")
	clickhouseDB := config.RequireEnv("CLICKHOUSE_DB")
	clickhouseUser := config.RequireEnv("CLICKHOUSE_USER")
	clickhousePassword := config.RequireEnv("CLICKHOUSE_PASSWORD")
	_ = config.RequireEnv("KAFKA_BROKERS")
	_ = config.RequireEnv("SERVICE_TOKEN")
	sourceID, sourceRegion, err := scheduler.NormalizeSourceIdentity(
		config.RequireEnv("METERING_SOURCE_ID"),
		config.RequireEnv("METERING_SOURCE_REGION"),
	)
	if err != nil {
		logger.WithError(err).Fatal("Invalid metering source identity")
	}

	dbConfig := database.DefaultConfig()
	dbConfig.ServiceName = "periscope-metering"
	dbConfig.URL = dbURL
	postgres := database.MustConnect(dbConfig, logger)
	defer func() { _ = postgres.Close() }()

	chConfig := database.DefaultClickHouseConfig()
	chConfig.ServiceName = "periscope-metering"
	chConfig.Addr = strings.Split(clickhouseAddr, ",")
	chConfig.Database = clickhouseDB
	chConfig.Username = clickhouseUser
	chConfig.Password = clickhousePassword
	clickhouse := database.MustConnectClickHouse(chConfig, logger)
	defer func() { _ = clickhouse.Close() }()

	health := monitoring.NewHealthChecker("periscope-metering", version.Version)
	metrics := monitoring.NewMetricsCollector("periscope-metering", version.Version, version.GitCommit)
	health.AddCheck("postgres", monitoring.DatabaseHealthCheck(postgres))
	health.AddCheck("clickhouse", monitoring.DatabaseHealthCheck(clickhouse))
	health.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"DATABASE_URL":           dbURL,
		"CLICKHOUSE_ADDR":        clickhouseAddr,
		"CLICKHOUSE_DB":          clickhouseDB,
		"METERING_SOURCE_ID":     sourceID,
		"METERING_SOURCE_REGION": sourceRegion,
	}))

	tasks := scheduler.NewScheduler(postgres, clickhouse, logger, sourceID, sourceRegion)
	validationCtx, cancelValidation := context.WithTimeout(context.Background(), 30*time.Second)
	if err := tasks.ValidateSource(validationCtx); err != nil {
		cancelValidation()
		logger.WithError(err).Fatal("Invalid metering source identity")
	}
	cancelValidation()
	tasks.Start()
	defer tasks.Stop()

	router := server.SetupServiceRouter(logger, "periscope-metering", health, metrics)
	server.RegisterEnvFileReload("periscope-metering", logger)
	if err := server.Start(server.DefaultConfig("periscope-metering", "18021"), router, logger); err != nil {
		logger.WithError(err).Fatal("Periscope metering server failed")
	}
}
