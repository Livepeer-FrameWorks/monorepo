package main

import (
	"frameworks/api_forms/internal/handlers"
	"frameworks/pkg/config"
	"frameworks/pkg/email"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	"frameworks/pkg/server"
	"frameworks/pkg/turnstile"
	"frameworks/pkg/version"
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

	contactHandler := handlers.NewContactHandler(
		emailSender,
		turnstileValidator,
		config.GetEnv("TO_EMAIL", "contact@frameworks.network"),
		turnstileEnabled,
		logger,
	)

	app.POST("/api/contact", contactHandler.Handle)

	serverConfig := server.DefaultConfig("forms", port)
	if err := server.Start(serverConfig, app, logger); err != nil {
		logger.Fatal(err.Error())
	}
}
