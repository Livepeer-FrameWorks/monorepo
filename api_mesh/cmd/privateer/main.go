package main

import (
	"os"
	"strconv"
	"time"

	"frameworks/api_mesh/internal/agent"
	"frameworks/pkg/config"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	"frameworks/pkg/server"
	"frameworks/pkg/version"
)

func main() {
	// Setup logger
	logger := logging.NewLoggerWithService("privateer")

	// Load environment variables
	config.LoadEnv(logger)

	// Validate required config
	qmGRPCAddr := os.Getenv("QUARTERMASTER_GRPC_ADDR")
	if qmGRPCAddr == "" {
		qmGRPCAddr = "quartermaster:19002"
	}

	token := os.Getenv("ENROLLMENT_TOKEN")
	// We also accept SERVICE_TOKEN for now
	if token == "" {
		token = os.Getenv("SERVICE_TOKEN")
	}

	if token == "" {
		logger.Fatal("ENROLLMENT_TOKEN or SERVICE_TOKEN is required")
	}

	dnsPort := 5353
	if p := os.Getenv("DNS_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil {
			dnsPort = port
		}
	}

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("privateer", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("privateer", version.Version, version.GitCommit)

	// Create agent metrics
	agentMetrics := &agent.Metrics{
		SyncOperations:   metricsCollector.NewCounter("sync_operations_total", "Mesh sync operations", []string{"status"}),
		PeersConnected:   metricsCollector.NewGauge("peers_connected", "Number of connected WireGuard peers", []string{}),
		DNSQueries:       metricsCollector.NewCounter("dns_queries_total", "DNS queries processed", []string{"type", "status"}),
		WireGuardResyncs: metricsCollector.NewCounter("wireguard_resyncs_total", "WireGuard interface resyncs", []string{"status"}),
	}

	// Config
	cfg := agent.Config{
		QuartermasterGRPCAddr: qmGRPCAddr,
		ServiceToken:          token,
		SyncInterval:          30 * time.Second,
		InterfaceName:         os.Getenv("MESH_INTERFACE"), // Defaults to wg0
		DNSPort:               dnsPort,
		Logger:                logger,
		Metrics:               agentMetrics,
	}

	// Create Agent
	a, err := agent.New(cfg)
	if err != nil {
		logger.WithError(err).Fatal("Failed to initialize agent")
	}

	// Add agent health check
	healthChecker.AddCheck("agent", func() monitoring.CheckResult {
		if a.IsHealthy() {
			return monitoring.CheckResult{Status: "healthy", Message: "agent running"}
		}
		return monitoring.CheckResult{Status: "unhealthy", Message: "agent not healthy"}
	})

	// Start Agent in background
	go func() {
		if err := a.Start(); err != nil {
			logger.WithError(err).Fatal("Agent failed")
		}
	}()

	// Start HTTP server for health/metrics (standard pattern)
	router := server.SetupServiceRouter(logger, "privateer", healthChecker, metricsCollector)
	serverConfig := server.DefaultConfig("privateer", config.GetEnv("PRIVATEER_PORT", "18012"))
	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}
}
