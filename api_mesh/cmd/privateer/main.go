package main

import (
	"context"
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"frameworks/api_mesh/internal/agent"
	"frameworks/api_mesh/internal/appconfig"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"
)

func main() {
	if version.HandleCLI() {
		return
	}

	// Setup logger
	logger := logging.NewLoggerWithService("privateer")

	// Load environment variables
	config.LoadEnv(logger)

	configOptions := config.Options{Service: "privateer", Logger: logger}
	cfg, err := config.Load[appconfig.Privateer](configOptions)
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}
	cfg.ApplyLogLevel(logger)
	privateKeyFile := cfg.PrivateKeyFile
	dataDir := cfg.DataDir

	// Enrollment: if no key on disk and a join token is present, generate
	// locally, register with the control plane, and persist the assigned
	// identity to disk. On subsequent starts the key already exists; the
	// persisted enrollment state below fills in what env would otherwise
	// need to provide.
	enrollCtx, enrollCancel := context.WithTimeout(context.Background(), 60*time.Second)
	enrolled, enrollErr := tryEnrollIfNeeded(enrollCtx, logger, cfg)
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

	qmGRPCAddr := cfg.QuartermasterGRPCAddr
	if qmGRPCAddr == "" && persisted != nil {
		qmGRPCAddr = persisted.QuartermasterGRPCAddr
	}
	if qmGRPCAddr == "" {
		logger.Fatal("QUARTERMASTER_GRPC_ADDR is required (set env or enroll via `frameworks mesh join`)")
	}

	staticPeersFile := cfg.StaticPeersFile
	if staticPeersFile == "" && persisted != nil {
		staticPeersFile = persisted.StaticPeersFile
	}
	meshWireguardIP := cfg.WireguardIP
	if meshWireguardIP == "" && persisted != nil {
		meshWireguardIP = persisted.WireguardIP
	}
	if meshWireguardIP == "" {
		logger.Fatal("MESH_WIREGUARD_IP is required")
	}

	listenPort := 51820
	if cfg.WireguardListenPort != 0 {
		listenPort = cfg.WireguardListenPort
	} else if persisted != nil && persisted.WireguardPort > 0 {
		listenPort = persisted.WireguardPort
	}

	nodeID := cfg.NodeID
	if nodeID == "" && persisted != nil {
		nodeID = persisted.NodeID
	}
	clusterID := cfg.ClusterID
	if clusterID == "" && persisted != nil {
		clusterID = persisted.ClusterID
	}
	if clusterID == "" {
		logger.Fatal("CLUSTER_ID is required")
	}

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("privateer", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("privateer", version.Version, version.GitCommit)

	// Create agent metrics
	agentMetrics := &agent.Metrics{
		SyncOperations: metricsCollector.NewCounter("sync_operations_total", "Mesh sync operations", []string{"status"}),
		PeersConnected: metricsCollector.NewGauge("peers_connected", "Number of connected WireGuard peers", []string{}),
		DNSQueries:     metricsCollector.NewCounter("dns_queries_total", "DNS queries processed", []string{"type", "status"}),
		CertSyncOperations: metricsCollector.NewCounter(
			"internal_cert_sync_operations_total",
			"Completed internal certificate sync operations",
			[]string{"status"},
		),
		CertIssueTokenRefreshes: metricsCollector.NewCounter(
			"internal_cert_issue_token_refreshes_total",
			"Successful internal certificate issuance token refreshes",
			[]string{"reason"},
		),
		// wireguard_resyncs_total dropped: every WG resync is a mesh apply,
		// which is already covered by mesh_apply_duration_seconds and
		// mesh_apply_failures_total with finer-grained {layer,reason} labels.
		LayerApplied:      metricsCollector.NewGauge("layer_applied", "Currently-applied mesh layer (1 = active). Labels: managed (Quartermaster-fresh), last_known (disk cache), seed (GitOps substrate).", []string{"layer"}),
		MeshApplyDuration: metricsCollector.NewHistogram("mesh_apply_duration_seconds", "Time spent applying a mesh config to wg0, by layer.", []string{"layer"}, nil),
		MeshApplyFailures: metricsCollector.NewCounter("mesh_apply_failures_total", "Mesh apply attempts that did not reach a configured device, by layer and reason.", []string{"layer", "reason"}),
		MeshPeerCount:     metricsCollector.NewGauge("mesh_peer_count", "Peers in the last successful mesh apply, by layer.", []string{"layer"}),
	}

	// Config
	bootstrapInternalCABundle(cfg.GRPCTLSCAPath, cfg.InternalCARootCertPEMB64, cfg.InternalCAIntermediateCertPEMB64)
	agentConfig := agent.Config{
		QuartermasterGRPCAddr:   qmGRPCAddr,
		NavigatorGRPCAddr:       cfg.NavigatorGRPCAddr,
		ServiceToken:            cfg.ServiceToken,
		ClusterID:               clusterID,
		CertIssueToken:          cfg.CertIssuanceToken,
		AllowInsecure:           cfg.GRPCAllowInsecure,
		CACertFile:              cfg.GRPCTLSCAPath,
		QuartermasterServerName: cfg.QuartermasterGRPCTLSServerName,
		NavigatorServerName:     cfg.NavigatorGRPCTLSServerName,
		PKIBasePath:             cfg.PKIDir,
		ExpectedServiceTypes:    parseExpectedServiceTypes(cfg.ExpectedInternalGRPCServices),
		CertSyncInterval:        cfg.CertSyncInterval,
		SyncInterval:            cfg.SyncInterval,
		SyncTimeout:             cfg.SyncTimeout,
		InterfaceName:           cfg.InterfaceName, // Defaults to wg0
		NodeType:                cfg.NodeType,
		NodeName:                cfg.NodeName,
		NodeID:                  nodeID,
		ExternalIP:              cfg.ExternalIP,
		InternalIP:              cfg.InternalIP,
		ListenPort:              listenPort,
		DNSPort:                 cfg.DNSPort,
		DNSUpstreams:            normalizeDNSUpstreams(cfg.UpstreamDNS),
		StaticPeersFile:         staticPeersFile,
		PrivateKeyFile:          privateKeyFile,
		WireguardIP:             meshWireguardIP,
		DataDir:                 dataDir,
		Logger:                  logger,
		Metrics:                 agentMetrics,
	}

	// Create Agent
	a, err := agent.New(agentConfig)
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
	healthChecker.AddCheck("internal_pki", func() monitoring.CheckResult {
		if a.IsInternalPKIHealthy() {
			return monitoring.CheckResult{Status: "healthy", Message: "internal certificate sync healthy"}
		}
		return monitoring.CheckResult{Status: "unhealthy", Message: "internal certificate sync repeatedly failing"}
	})

	// Readiness reports only local startup: the WireGuard interface, the
	// startup mesh layer, and the DNS server. Quartermaster and Navigator
	// reachability stay in the liveness checks so a control plane outage
	// never marks the mesh agent unready.
	readiness := monitoring.NewReadinessChecker("privateer", version.Version)
	readiness.AddCheck("agent_started", func() monitoring.CheckResult {
		if a.Started() {
			return monitoring.CheckResult{Status: monitoring.StatusHealthy, Message: "agent started"}
		}
		return monitoring.CheckResult{Status: monitoring.StatusUnhealthy, Message: "agent starting"}
	})

	// Start Agent in background. The agent handles SIGINT and SIGTERM itself
	// and stops its DNS server and sync loops as soon as the signal arrives.
	go func() {
		if err := a.Start(); err != nil {
			logger.WithError(err).Fatal("Agent failed")
		}
	}()

	// Serve health, readiness, and metrics over HTTP.
	router := server.NewServiceRouter(server.RouterSpec{
		Service:            "privateer",
		Logger:             logger,
		Health:             healthChecker,
		Ready:              readiness,
		Metrics:            metricsCollector,
		Runtime:            cfg.HTTPRuntime,
		DebugToken:         cfg.ServiceToken,
		DebugConfig:        func() any { return cfg },
		DebugConfigOptions: configOptions,
	})
	if err := server.Run(context.Background(), server.RunSpec{
		Service: "privateer",
		Logger:  logger,
		Ready:   readiness,
		HTTP:    []server.HTTPListener{{Name: "http", Port: cfg.ListenHTTPPort(), Handler: router}},
		// Stop is idempotent: after a signal it waits for the agent's own
		// stop to finish, and after a listener failure it stops the agent.
		OnShutdown: []func(context.Context){func(context.Context) { a.Stop() }},
	}); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}
}

// normalizeDNSUpstreams appends the default DNS port to resolver entries that
// have none, bracketing bare IPv6 addresses.
func normalizeDNSUpstreams(entries []string) []string {
	var upstreams []string
	for _, s := range entries {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, _, splitErr := net.SplitHostPort(s); splitErr != nil {
			// No port: could be IPv4 (1.1.1.1) or IPv6 (2001:db8::1).
			if net.ParseIP(s) != nil && strings.Contains(s, ":") {
				s = "[" + s + "]:53"
			} else {
				s += ":53"
			}
		}
		upstreams = append(upstreams, s)
	}
	return upstreams
}

func parseExpectedServiceTypes(entries []string) []string {
	seen := make(map[string]struct{})
	var serviceTypes []string
	for _, part := range entries {
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

// bootstrapInternalCABundle writes the internal root and intermediate
// certificates into caPath when that file is missing or empty.
func bootstrapInternalCABundle(caPath, rootPEMB64, intermediatePEMB64 string) {
	caPath = strings.TrimSpace(caPath)
	if caPath == "" {
		return
	}
	if info, err := os.Stat(caPath); err == nil && info.Size() > 0 {
		return
	}

	rootPEM, ok := decodePEMBase64(rootPEMB64)
	if !ok {
		return
	}
	intermediatePEM, ok := decodePEMBase64(intermediatePEMB64)
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

func decodePEMBase64(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", false
	}
	return string(decoded), true
}
