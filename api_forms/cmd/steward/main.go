package main

import (
	"context"

	"frameworks/api_forms/internal/appconfig"
	"frameworks/api_forms/internal/handlers"
	decklogclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/listmonk"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/email"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/turnstile"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"
	"time"
)

func main() {
	if version.HandleCLI() {
		return
	}

	logger := logging.NewLoggerWithService("steward")
	config.LoadEnv(logger)

	configOptions := config.Options{Service: "steward", Logger: logger}
	cfg, err := config.Load[appconfig.Steward](configOptions)
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}
	cfg.ApplyLogLevel(logger)
	liveBranding := config.NewLive(&cfg.EmailBranding, configOptions)

	emailConfig := email.Config{
		Host:          cfg.SMTPHost,
		Port:          cfg.SMTPPort,
		User:          cfg.SMTPUser,
		Password:      cfg.SMTPPassword,
		From:          cfg.FromEmail,
		FromName:      cfg.FromName,
		AllowInsecure: cfg.SMTPAllowInsecure,
	}
	emailSender := email.NewSender(emailConfig)

	turnstileValidator := turnstile.NewValidator(cfg.TurnstileFormsSecretKey)
	turnstileEnabled := cfg.TurnstileFormsSecretKey != ""

	healthChecker := monitoring.NewHealthChecker("steward", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("steward", version.Version, version.GitCommit)

	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"SMTP_HOST": cfg.SMTPHost,
		"TO_EMAIL":  cfg.ToEmail,
	}))

	// Steward has no dependency it needs to accept requests; readiness only
	// reports the drain window during shutdown.
	readiness := monitoring.NewReadinessChecker("steward", version.Version)

	app := server.NewServiceRouter(server.RouterSpec{
		Service: "steward",
		Logger:  logger,
		Health:  healthChecker,
		Ready:   readiness,
		Metrics: metricsCollector,
		Runtime: cfg.HTTPRuntime,
	})

	formMetrics := &handlers.FormMetrics{
		ContactRequests: metricsCollector.NewCounter(
			"contact_requests_total",
			"Contact form requests handled",
			[]string{"status"},
		),
		SubscribeRequests: metricsCollector.NewCounter(
			"subscribe_requests_total",
			"Subscribe form requests handled",
			[]string{"status"},
		),
	}

	var activityEmitter handlers.ActivityEmitter
	decklogClient, err := decklogclient.NewBatchedClient(decklogclient.BatchedClientConfig{
		Target:        cfg.DecklogGRPCAddr,
		AllowInsecure: cfg.AllowInsecure,
		CACertFile:    cfg.CAPath,
		ServerName:    cfg.DecklogGRPCTLSServerName,
		Timeout:       5 * time.Second,
		Source:        "steward",
		ServiceToken:  cfg.ServiceToken,
		ClusterID:     cfg.ClusterID,
		SourceRegion:  cfg.Region,
		Optional:      true,
	}, logger)
	if err != nil {
		logger.WithError(err).Warn("Failed to initialize optional Decklog activity emitter")
	} else {
		defer func() { _ = decklogClient.Close() }()
		activityEmitter = &handlers.DecklogActivityEmitter{Client: decklogClient}
	}

	contactHandler := handlers.NewContactHandler(
		emailSender,
		turnstileValidator,
		cfg.ContactRecipient(),
		cfg.EmailSubjectPrefix,
		cfg.ContactSuccessMessage,
		cfg.EmailBranding,
		turnstileEnabled,
		logger,
		formMetrics,
		activityEmitter,
	)

	contactHandler.BrandingSource = func() config.EmailBranding { return *liveBranding.Get() }
	app.POST("/api/contact", contactHandler.Handle)

	// Listmonk Integration (optional)
	if cfg.ListmonkURL != "" {
		if cfg.ListmonkAPIUsername == "" || cfg.ListmonkAPIToken == "" {
			logger.Warn("LISTMONK_URL is set but LISTMONK_API_USERNAME or LISTMONK_API_TOKEN is missing, subscribe endpoint disabled")
		} else {
			lmClient := listmonk.NewClient(cfg.ListmonkURL, cfg.ListmonkAPIUsername, cfg.ListmonkAPIToken)
			subHandler := handlers.NewSubscribeHandler(lmClient, turnstileValidator, cfg.DefaultMailingListID, turnstileEnabled, logger, formMetrics, activityEmitter)
			app.POST("/api/subscribe", subHandler.Handle)
		}
	} else {
		logger.Info("LISTMONK_URL not set, subscribe endpoint disabled")
	}

	server.RegisterEnvFileReload("steward", logger)
	if runErr := server.Run(context.Background(), server.RunSpec{
		Service:  "steward",
		Logger:   logger,
		Ready:    readiness,
		HTTP:     []server.HTTPListener{{Name: "http", Port: cfg.Port, Handler: app}},
		OnReload: []server.ReloadCallback{liveBranding.Reload},
	}); runErr != nil {
		logger.WithError(runErr).Fatal("Server exited with error")
	}
}
