package main

import (
	"context"
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
	if version.HandleCLI() {
		return
	}

	// Setup logger
	logger := logging.NewLoggerWithService("privateer")

	// Load environment variables
	config.LoadEnv(logger)

	// Validate required config
	privateKeyFile := os.Getenv("MESH_PRIVATE_KEY_FILE")
	if privateKeyFile == "" {
		logger.Fatal("MESH_PRIVATE_KEY_FILE is required")
	}
	dataDir := os.Getenv("PRIVATEER_DATA_DIR")

	// Enrollment: if no key on disk and a join token is present, generate
	// locally, register with the control plane, and persist the assigned
	// identity to disk. On subsequent starts the key already exists; the
	// persisted enrollment state below fills in what env would otherwise
	// need to provide.
	enrollCtx, enrollCancel := context.WithTimeout(context.Background(), 60*time.Second)
	enrolled, enrollErr := tryEnrollIfNeeded(enrollCtx, logger, privateKeyFile, dataDir)
	enrollCancel()
	if enrollErr != nil {
		logger.WithError(enrollErr).Fatal("Enrollment failed")
	}

	// Load any previously-persisted enrollment state; it tells us everything
	// we need without env vars once a node has successfully joined once.
	persisted, err := loadEnrollmentState(enrollmentStatePath(dataDir))
	if err != nil {
		logger.WithError(err).Warn("Failed to load persisted enrollment state")
	}
	if enrolled != nil {
		persisted = enrolled
	}

	qmGRPCAddr := os.Getenv("QUARTERMASTER_GRPC_ADDR")
	if qmGRPCAddr == "" && persisted != nil {
		qmGRPCAddr = persisted.QuartermasterGRPCAddr
	}
	if qmGRPCAddr == "" {
		logger.Fatal("QUARTERMASTER_GRPC_ADDR is required (set env or enroll via `frameworks mesh join`)")
	}

	serviceToken := os.Getenv("SERVICE_TOKEN")
	if serviceToken == "" {
		logger.Fatal("SERVICE_TOKEN is required — on seed nodes it's rendered by Ansible; on enrolled nodes `frameworks mesh join` writes it into /etc/privateer/privateer.env")
	}

	staticPeersFile := os.Getenv("PRIVATEER_STATIC_PEERS_FILE")
	if staticPeersFile == "" && persisted != nil {
		staticPeersFile = persisted.StaticPeersFile
	}
	meshWireguardIP := os.Getenv("MESH_WIREGUARD_IP")
	if meshWireguardIP == "" && persisted != nil {
		meshWireguardIP = persisted.WireguardIP
	}
	if meshWireguardIP == "" {
		logger.Fatal("MESH_WIREGUARD_IP is required")
	}

	dnsPort := 53
	if p := os.Getenv("DNS_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil {
			dnsPort = port
		}
	}

	listenPort := 51820
	if p := os.Getenv("MESH_LISTEN_PORT"); p != "" {
		if port, parseErr := strconv.Atoi(p); parseErr == nil {
			listenPort = port
		}
	} else if persisted != nil && persisted.WireguardPort > 0 {
		listenPort = persisted.WireguardPort
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
	if nodeID == "" && persisted != nil {
		nodeID = persisted.NodeID
	}
	clusterID := os.Getenv("CLUSTER_ID")
	if clusterID == "" && persisted != nil {
		clusterID = persisted.ClusterID
	}
	if clusterID == "" {
		logger.Fatal("CLUSTER_ID is required")
	}
	externalIP := os.Getenv("MESH_EXTERNAL_IP")
	internalIP := os.Getenv("MESH_INTERNAL_IP")

	var dnsUpstreams []string
	if raw := os.Getenv("UPSTREAM_DNS"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			_, _, splitErr := net.SplitHostPort(s)
			if splitErr != nil {
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
		SyncOperations:    metricsCollector.NewCounter("sync_operations_total", "Mesh sync operations", []string{"status"}),
		PeersConnected:    metricsCollector.NewGauge("peers_connected", "Number of connected WireGuard peers", []string{}),
		DNSQueries:        metricsCollector.NewCounter("dns_queries_total", "DNS queries processed", []string{"type", "status"}),
		WireGuardResyncs:  metricsCollector.NewCounter("wireguard_resyncs_total", "WireGuard interface resyncs", []string{"status"}),
		LayerApplied:      metricsCollector.NewGauge("layer_applied", "Currently-applied mesh layer (1 = active). Labels: managed (Quartermaster-fresh), last_known (disk cache), seed (GitOps substrate).", []string{"layer"}),
		MeshApplyDuration: metricsCollector.NewHistogram("mesh_apply_duration_seconds", "Time spent applying a mesh config to wg0, by layer.", []string{"layer"}, nil),
		MeshApplyFailures: metricsCollector.NewCounter("mesh_apply_failures_total", "Mesh apply attempts that did not reach a configured device, by layer and reason.", []string{"layer", "reason"}),
		MeshPeerCount:     metricsCollector.NewGauge("mesh_peer_count", "Peers in the last successful mesh apply, by layer.", []string{"layer"}),
	}

	// Config
	bootstrapInternalCABundleFromEnv(config.GetEnv("GRPC_TLS_CA_PATH", ""))
	cfg := agent.Config{
		QuartermasterGRPCAddr:   qmGRPCAddr,
		NavigatorGRPCAddr:       os.Getenv("NAVIGATOR_GRPC_ADDR"),
		ServiceToken:            serviceToken,
		ClusterID:               clusterID,
		CertIssueToken:          os.Getenv("CERT_ISSUANCE_TOKEN"),
		AllowInsecure:           config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		CACertFile:              config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:              config.GetEnv("GRPC_TLS_SERVER_NAME", ""),
		QuartermasterServerName: config.GetEnv("QUARTERMASTER_GRPC_TLS_SERVER_NAME", ""),
		NavigatorServerName:     config.GetEnv("NAVIGATOR_GRPC_TLS_SERVER_NAME", ""),
		PKIBasePath:             config.GetEnv("GRPC_TLS_PKI_DIR", "/etc/frameworks/pki"),
		ExpectedServiceTypes:    parseExpectedServiceTypes(os.Getenv("EXPECTED_INTERNAL_GRPC_SERVICES")),
		CertSyncInterval:        parseDurationOrDefault(os.Getenv("PRIVATEER_CERT_SYNC_INTERVAL"), 5*time.Minute),
		SyncInterval:            syncInterval,
		SyncTimeout:             syncTimeout,
		InterfaceName:           os.Getenv("MESH_INTERFACE"), // Defaults to wg0
		NodeType:                nodeType,
		NodeName:                nodeName,
		NodeID:                  nodeID,
		ExternalIP:              externalIP,
		InternalIP:              internalIP,
		ListenPort:              listenPort,
		DNSPort:                 dnsPort,
		DNSUpstreams:            dnsUpstreams,
		StaticPeersFile:         staticPeersFile,
		PrivateKeyFile:          privateKeyFile,
		WireguardIP:             meshWireguardIP,
		DataDir:                 dataDir,
		Logger:                  logger,
		Metrics:                 agentMetrics,
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
