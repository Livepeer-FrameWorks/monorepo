package main

import (
	"frameworks/api_forms/internal/handlers"
	"frameworks/pkg/clients/listmonk"
	"frameworks/pkg/config"
	"frameworks/pkg/email"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	"frameworks/pkg/server"
	"frameworks/pkg/turnstile"
	"frameworks/pkg/version"
	"strconv"
)

func main() {
	logger := logging.NewLoggerWithService("forms")
	config.LoadEnv(logger)

	port := config.GetEnv("PORT", "18032")
	turnstileKey := config.GetEnv("TURNSTILE_FORMS_SECRET_KEY", "")

	emailConfig := email.Config{
		Host:     config.GetEnv("SMTP_HOST", ""),
		Port:     config.GetEnv("SMTP_PORT", "587"),
		User:     config.GetEnv("SMTP_USER", ""),
		Password: config.GetEnv("SMTP_PASSWORD", ""),
		From:     config.GetEnv("FROM_EMAIL", "noreply@frameworks.network"),
	}
	emailSender := email.NewSender(emailConfig)

	turnstileValidator := turnstile.NewValidator(turnstileKey)
	turnstileEnabled := turnstileKey != ""

	healthChecker := monitoring.NewHealthChecker("forms", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("forms", version.Version, version.GitCommit)

	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"SMTP_HOST": emailConfig.Host,
		"TO_EMAIL":  config.GetEnv("TO_EMAIL", ""),
	}))

	app := server.SetupServiceRouter(logger, "forms", healthChecker, metricsCollector)

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

	contactHandler := handlers.NewContactHandler(
		emailSender,
		turnstileValidator,
		config.GetEnv("TO_EMAIL", "contact@frameworks.network"),
		turnstileEnabled,
		logger,
		formMetrics,
	)

	app.POST("/api/contact", contactHandler.Handle)

	// Listmonk Integration
	listmonkURL := config.RequireEnv("LISTMONK_URL")
	listmonkUser := config.GetEnv("LISTMONK_USERNAME", "admin")
	listmonkPass := config.GetEnv("LISTMONK_PASSWORD", "admin")
	listIDStr := config.GetEnv("DEFAULT_MAILING_LIST_ID", "1")
	listID, _ := strconv.Atoi(listIDStr)

	lmClient := listmonk.NewClient(listmonkURL, listmonkUser, listmonkPass)
	subHandler := handlers.NewSubscribeHandler(lmClient, turnstileValidator, listID, turnstileEnabled, logger, formMetrics)
	app.POST("/api/subscribe", subHandler.Handle)

	serverConfig := server.DefaultConfig("forms", port)
	if err := server.Start(serverConfig, app, logger); err != nil {
		logger.Fatal(err.Error())
	}
}
