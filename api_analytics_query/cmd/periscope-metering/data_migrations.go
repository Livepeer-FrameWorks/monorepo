package main

import (
	"context"
	"database/sql"
	"os"
	"sync"

	"frameworks/api_analytics_query/internal/appconfig"
	"frameworks/api_analytics_query/internal/datamigrations"
	"frameworks/api_analytics_query/internal/scheduler"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func runDataMigrationCommand(args []string, logger logging.Logger) error {
	config.LoadEnv(logger)
	cfg, err := config.Load[appconfig.PeriscopeMeteringDataMigrations](config.Options{Service: "periscope-metering", Logger: logger})
	if err != nil {
		return err
	}
	sourceID, _, err := scheduler.NormalizeSourceIdentity(cfg.MeteringSourceID, cfg.MeteringSourceRegion)
	if err != nil {
		return err
	}
	var once sync.Once
	var ch database.ClickHouseConn
	var chErr error
	defer func() {
		if ch != nil {
			_ = ch.Close()
		}
	}()
	openClickHouse := func() (database.ClickHouseConn, error) {
		once.Do(func() {
			chConfig := database.DefaultClickHouseConfig()
			chConfig.ServiceName = "periscope-metering"
			chConfig.Addr = cfg.Addr
			chConfig.Database = cfg.Database
			chConfig.Username = cfg.User
			chConfig.Password = cfg.Password
			ch, chErr = database.ConnectClickHouse(chConfig, logger)
		})
		return ch, chErr
	}
	datamigrations.RegisterViewerServingAttribution(sourceID, openClickHouse)
	return datamigrate.HandleArgv(context.Background(), func() (*sql.DB, error) {
		dbConfig := database.DefaultConfig()
		dbConfig.ServiceName = "periscope-metering"
		dbConfig.URL = cfg.DatabaseURL
		return database.Connect(dbConfig, logger)
	}, os.Stdout, args)
}
