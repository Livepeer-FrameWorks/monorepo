package main

import (
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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

	serviceToken := os.Getenv("SERVICE_TOKEN")
	enrollmentToken := os.Getenv("ENROLLMENT_TOKEN")

	if serviceToken == "" {
		logger.Fatal("SERVICE_TOKEN is required for steady-state operation")
	}

	dnsPort := 53
	if p := os.Getenv("DNS_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil {
			dnsPort = port
		}
	}

	listenPort := 51820
	if p := os.Getenv("MESH_LISTEN_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil {
			listenPort = port
		}
	}

	syncInterval := 30 * time.Second
	if d := os.Getenv("PRIVATEER_SYNC_INTERVAL"); d != "" {
		if parsed, err := time.ParseDuration(d); err == nil {
			syncInterval = parsed
		}
	}

	syncTimeout := 10 * time.Second
	if d := os.Getenv("PRIVATEER_SYNC_TIMEOUT"); d != "" {
		if parsed, err := time.ParseDuration(d); err == nil {
			syncTimeout = parsed
		}
	}

	nodeType := os.Getenv("MESH_NODE_TYPE")
	nodeName := os.Getenv("MESH_NODE_NAME")
	nodeID := os.Getenv("NODE_ID")
	externalIP := os.Getenv("MESH_EXTERNAL_IP")
	internalIP := os.Getenv("MESH_INTERNAL_IP")

	var dnsUpstreams []string
	if raw := os.Getenv("UPSTREAM_DNS"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			_, _, err := net.SplitHostPort(s)
			if err != nil {
				// No port: could be IPv4 (1.1.1.1) or IPv6 (2001:db8::1).
				if net.ParseIP(s) != nil && strings.Contains(s, ":") {
					s = "[" + s + "]:53"
				} else {
					s += ":53"
				}
			}
			dnsUpstreams = append(dnsUpstreams, s)
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
	bootstrapInternalCABundleFromEnv(config.GetEnv("GRPC_TLS_CA_PATH", ""))
	cfg := agent.Config{
		QuartermasterGRPCAddr: qmGRPCAddr,
		NavigatorGRPCAddr:     os.Getenv("NAVIGATOR_GRPC_ADDR"),
		ServiceToken:          serviceToken,
		EnrollmentToken:       enrollmentToken,
		CertIssueToken:        os.Getenv("CERT_ISSUANCE_TOKEN"),
		AllowInsecure:         config.GetEnvBool("GRPC_ALLOW_INSECURE", true),
		CACertFile:            config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:            config.GetEnv("GRPC_TLS_SERVER_NAME", ""),
		PKIBasePath:           config.GetEnv("GRPC_TLS_PKI_DIR", "/etc/frameworks/pki"),
		ExpectedServiceTypes:  parseExpectedServiceTypes(os.Getenv("EXPECTED_INTERNAL_GRPC_SERVICES")),
		CertSyncInterval:      parseDurationOrDefault(os.Getenv("PRIVATEER_CERT_SYNC_INTERVAL"), 5*time.Minute),
		SyncInterval:          syncInterval,
		SyncTimeout:           syncTimeout,
		InterfaceName:         os.Getenv("MESH_INTERFACE"), // Defaults to wg0
		NodeType:              nodeType,
		NodeName:              nodeName,
		NodeID:                nodeID,
		ExternalIP:            externalIP,
		InternalIP:            internalIP,
		ListenPort:            listenPort,
		DNSPort:               dnsPort,
		DNSUpstreams:          dnsUpstreams,
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

func parseDurationOrDefault(raw string, fallback time.Duration) time.Duration {
	if raw == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseExpectedServiceTypes(raw string) []string {
	if raw == "" {
		return nil
	}
	seen := make(map[string]struct{})
	var serviceTypes []string
	for _, part := range strings.Split(raw, ",") {
		serviceType := strings.TrimSpace(part)
		if serviceType == "" {
			continue
		}
		if _, ok := seen[serviceType]; ok {
			continue
		}
		seen[serviceType] = struct{}{}
		serviceTypes = append(serviceTypes, serviceType)
	}
	sort.Strings(serviceTypes)
	return serviceTypes
}

func bootstrapInternalCABundleFromEnv(caPath string) {
	caPath = strings.TrimSpace(caPath)
	if caPath == "" {
		return
	}
	if info, err := os.Stat(caPath); err == nil && info.Size() > 0 {
		return
	}

	rootPEM, ok := decodePEMEnv("NAVIGATOR_INTERNAL_CA_ROOT_CERT_PEM_B64")
	if !ok {
		return
	}
	intermediatePEM, ok := decodePEMEnv("NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_PEM_B64")
	if !ok {
		return
	}
	bundle := strings.TrimSpace(rootPEM) + "\n" + strings.TrimSpace(intermediatePEM) + "\n"
	if err := os.MkdirAll(filepath.Dir(caPath), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(caPath, []byte(bundle), 0o644); err != nil {
		return
	}
}

func decodePEMEnv(key string) (string, bool) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", false
	}
	return string(decoded), true
}
