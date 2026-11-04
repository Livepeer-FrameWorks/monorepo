package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"frameworks/api_billing/internal/appconfig"
	"frameworks/api_billing/internal/releasepreflight"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func runReleasePreflight(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: purser release-preflight")
		return 2
	}
	logger := logging.NewLoggerWithService("purser-preflight")
	config.LoadEnv(logger)
	cfg, err := config.Load[appconfig.PurserDataMigrations](config.Options{Service: "purser", Logger: logger})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	dbCfg := database.DefaultConfig()
	dbCfg.ServiceName = "purser"
	dbCfg.URL = cfg.DatabaseURL
	db, err := database.Connect(dbCfg, logger)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	report, err := releasepreflight.Run(ctx, db)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
