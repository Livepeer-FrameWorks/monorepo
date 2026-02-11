package agent

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"frameworks/api_mesh/internal/dns"
	"frameworks/api_mesh/internal/wireguard"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Metrics holds Prometheus metrics for the agent
type Metrics struct {
	SyncOperations   *prometheus.CounterVec
	PeersConnected   *prometheus.GaugeVec
	DNSQueries       *prometheus.CounterVec
	WireGuardResyncs *prometheus.CounterVec
}

type meshClient interface {
	SyncMesh(ctx context.Context, req *pb.InfrastructureSyncRequest) (*pb.InfrastructureSyncResponse, error)
	BootstrapInfrastructureNode(ctx context.Context, req *pb.BootstrapInfrastructureNodeRequest) (*pb.BootstrapInfrastructureNodeResponse, error)
}

type dnsService interface {
	Start()
	Stop()
	UpdateRecords(records map[string][]string) error
}

type Agent struct {
	logger           logging.Logger
	client           meshClient
	wgManager        wireguard.Manager
	dnsServer        dnsService
	nodeID           string
	nodeName         string
	nodeType         string
	enrollmentToken  string
	externalIP       string
	internalIP       string
	interfaceName    string
	listenPort       int
	syncInterval     time.Duration
	syncTimeout      time.Duration
	stopChan         chan struct{}
	metrics          *Metrics
	healthy          atomic.Bool
	lastSyncSuccess  atomic.Int64 // Unix timestamp of last successful sync
	consecutiveFails atomic.Int32
	lastConfigMu     sync.Mutex
	lastAppliedCfg   *wireguard.Config
}

type Config struct {
	QuartermasterGRPCAddr string
	ServiceToken          string // Or EnrollmentToken
	EnrollmentToken       string // Bootstrap token for initial registration (optional)
	NodeIDPath            string
	InterfaceName         string
	NodeType              string
	NodeName              string
	ExternalIP            string
	InternalIP            string
	ListenPort            int
	SyncInterval          time.Duration
	SyncTimeout           time.Duration
	DNSPort               int
	Logger                logging.Logger
	Metrics               *Metrics
	MeshClient            meshClient
	WireGuardManager      wireguard.Manager
	DNSService            dnsService
}

func New(cfg Config) (*Agent, error) {
	if cfg.SyncInterval == 0 {
		cfg.SyncInterval = 30 * time.Second
	}
	if cfg.SyncTimeout == 0 {
		cfg.SyncTimeout = 10 * time.Second
	}
	if cfg.InterfaceName == "" {
		cfg.InterfaceName = "wg0"
	}
	if cfg.NodeIDPath == "" {
		cfg.NodeIDPath = "/etc/privateer/node_id"
	}
	if cfg.ListenPort == 0 {
		cfg.ListenPort = 51820
	}
	if cfg.NodeType == "" {
		cfg.NodeType = "edge"
	}
	if cfg.NodeName == "" {
		if hostname, err := os.Hostname(); err == nil && hostname != "" {
			cfg.NodeName = hostname
		}
	}

	// Initialize WireGuard Manager
	wg := cfg.WireGuardManager
	if wg == nil {
		var err error
		wg, err = wireguard.NewManager(cfg.InterfaceName)
		if err != nil {
			return nil, fmt.Errorf("failed to create wireguard manager: %w", err)
		}
	}

	// Initialize gRPC API Client
	client := cfg.MeshClient
	if client == nil {
		var err error
		client, err = qmclient.NewGRPCClient(qmclient.GRPCConfig{
			GRPCAddr:     cfg.QuartermasterGRPCAddr,
			ServiceToken: cfg.ServiceToken,
			Logger:       cfg.Logger,
			Timeout:      10 * time.Second,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create quartermaster gRPC client: %w", err)
		}
	}

	// Initialize DNS Server
	dnsSrv := cfg.DNSService
	if dnsSrv == nil {
		dnsSrv = dns.NewServer(cfg.Logger, cfg.DNSPort)
	}

	return &Agent{
		logger:          cfg.Logger,
		client:          client,
		wgManager:       wg,
		dnsServer:       dnsSrv,
		interfaceName:   cfg.InterfaceName,
		enrollmentToken: cfg.EnrollmentToken,
		nodeType:        cfg.NodeType,
		nodeName:        cfg.NodeName,
		externalIP:      cfg.ExternalIP,
		internalIP:      cfg.InternalIP,
		listenPort:      cfg.ListenPort,
		syncInterval:    cfg.SyncInterval,
		syncTimeout:     cfg.SyncTimeout,
		stopChan:        make(chan struct{}),
		nodeID:          loadOrGenerateNodeID(cfg.NodeIDPath, cfg.Logger),
		metrics:         cfg.Metrics,
	}, nil
}

func (a *Agent) Start() error {
	a.logger.Info("Starting Privateer Agent")

	// 1. Init WireGuard Interface
	if err := a.wgManager.Init(); err != nil {
		return fmt.Errorf("failed to init wireguard: %w", err)
	}

	// 2. Start DNS Server
	go a.dnsServer.Start()

	// 3. Start Polling Loop
	go a.runLoop()

	// Mark agent as healthy after successful initialization
	a.healthy.Store(true)

	// Wait for termination signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	a.Stop()
	return nil
}

// IsHealthy returns whether the agent is healthy
// Unhealthy if: not started, >3 consecutive sync failures, or no sync in 5 minutes
func (a *Agent) IsHealthy() bool {
	if !a.healthy.Load() {
		return false
	}
	// Too many consecutive failures
	if a.consecutiveFails.Load() > 3 {
		return false
	}
	// No successful sync in 5 minutes
	lastSync := a.lastSyncSuccess.Load()
	if lastSync > 0 && time.Now().Unix()-lastSync > 300 {
		return false
	}
	return true
}

func (a *Agent) Stop() {
	close(a.stopChan)
	a.logger.Info("Stopping Privateer Agent")
	a.dnsServer.Stop()
	// Clean up if necessary
}

func (a *Agent) runLoop() {
	ticker := time.NewTicker(a.syncInterval)
	defer ticker.Stop()

	// Initial sync
	a.sync()

	for {
		select {
		case <-ticker.C:
			a.sync()
		case <-a.stopChan:
			return
		}
	}
}

func (a *Agent) syncFailed() {
	a.consecutiveFails.Add(1)
	if a.metrics != nil && a.metrics.SyncOperations != nil {
		a.metrics.SyncOperations.WithLabelValues("failed").Inc()
	}
}

func (a *Agent) syncSucceeded() {
	a.consecutiveFails.Store(0)
	a.lastSyncSuccess.Store(time.Now().Unix())
	if a.metrics != nil && a.metrics.SyncOperations != nil {
		a.metrics.SyncOperations.WithLabelValues("success").Inc()
	}
}

func (a *Agent) sync() {
	// 1. Get Public Key
	pubKey, err := a.wgManager.GetPublicKey()
	if err != nil {
		a.logger.WithError(err).Error("Failed to get public key")
		a.syncFailed()
		return
	}

	// 2. Call Quartermaster via gRPC
	req := &pb.InfrastructureSyncRequest{
		NodeId:     a.nodeID,
		PublicKey:  pubKey,
		ListenPort: safeInt32(a.listenPort),
	}

	syncCtx, cancel := context.WithTimeout(context.Background(), a.syncTimeout)
	resp, err := a.client.SyncMesh(syncCtx, req)
	cancel()
	if err != nil {
		if status.Code(err) == codes.NotFound && a.enrollmentToken != "" {
			a.logger.WithError(err).Warn("Node not registered. Attempting bootstrap...")
			bootCtx, cancel := context.WithTimeout(context.Background(), a.syncTimeout)
			bootErr := a.bootstrapNode(bootCtx)
			cancel()
			if bootErr != nil {
				a.logger.WithError(bootErr).Error("Bootstrap failed")
				a.syncFailed()
				return
			}

			// Retry sync after successful bootstrap
			req.NodeId = a.nodeID
			retryCtx, cancel := context.WithTimeout(context.Background(), a.syncTimeout)
			resp, err = a.client.SyncMesh(retryCtx, req)
			cancel()
			if err != nil {
				a.logger.WithError(err).Error("Failed to sync after bootstrap")
				a.clearMeshDNSState("post-bootstrap sync failed")
				a.syncFailed()
				return
			}
		} else {
			a.logger.WithError(err).Error("Failed to sync infrastructure")
			if status.Code(err) == codes.NotFound {
				a.clearMeshDNSState("node not found")
			}
			a.syncFailed()
			return
		}
	}

	// Get Private Key
	privKey, err := a.wgManager.GetPrivateKey()
	if err != nil {
		a.logger.WithError(err).Error("Failed to get private key")
		a.syncFailed()
		return
	}

	// 3. Convert to WireGuard Config
	peers := make([]wireguard.Peer, len(resp.Peers))
	// DNS Records map: hostname -> [WireGuard IPs]
	dnsRecords := make(map[string][]string)

	for i, p := range resp.Peers {
		peers[i] = wireguard.Peer{
			PublicKey:  p.PublicKey,
			Endpoint:   p.Endpoint,
			AllowedIPs: p.AllowedIps,
			KeepAlive:  int(p.KeepAlive),
		}

		// Assuming AllowedIPs contains the /32 IP at index 0
		if p.NodeName != "" && len(p.AllowedIps) > 0 {
			// Strip CIDR mask if present
			ip := strings.Split(p.AllowedIps[0], "/")[0]
			dnsRecords[p.NodeName] = []string{ip}
		}
	}

	// Add Service Endpoints (Aliases)
	for sName, sEndpoints := range resp.ServiceEndpoints {
		dnsRecords[sName] = append(dnsRecords[sName], sEndpoints.Ips...)
	}

	cfg := wireguard.Config{
		PrivateKey: privKey,
		Address:    fmt.Sprintf("%s/32", resp.WireguardIp),
		ListenPort: int(resp.WireguardPort),
		Peers:      peers,
	}

	// 4. Apply WireGuard Config
	if err := a.wgManager.Apply(cfg); err != nil {
		a.logger.WithError(err).Error("Failed to apply wireguard config")
		a.syncFailed()
		return
	}

	// 5. Update DNS Records
	if err := a.dnsServer.UpdateRecords(dnsRecords); err != nil {
		a.logger.WithError(err).Error("Failed to update DNS records")
		a.rollbackWireGuardConfig()
		a.syncFailed()
		return
	}

	a.setLastAppliedConfig(cfg)

	// Update metrics
	if a.metrics != nil && a.metrics.PeersConnected != nil {
		a.metrics.PeersConnected.WithLabelValues().Set(float64(len(peers)))
	}

	a.syncSucceeded()
	a.logger.Info("Successfully applied wireguard config")
}

func (a *Agent) clearMeshDNSState(reason string) {
	a.logger.WithField("reason", reason).Warn("Clearing local mesh DNS records")
	if err := a.dnsServer.UpdateRecords(map[string][]string{}); err != nil {
		a.logger.WithError(err).Warn("Failed to clear local mesh DNS records")
		return
	}
	a.lastConfigMu.Lock()
	a.lastAppliedCfg = nil
	a.lastConfigMu.Unlock()
}

func (a *Agent) rollbackWireGuardConfig() {
	previous := a.getLastAppliedConfig()
	if previous == nil {
		a.logger.Warn("No previous WireGuard config to roll back to")
		return
	}
	if err := a.wgManager.Apply(*previous); err != nil {
		a.logger.WithError(err).Error("Failed to roll back WireGuard config")
	}
}

func (a *Agent) getLastAppliedConfig() *wireguard.Config {
	a.lastConfigMu.Lock()
	defer a.lastConfigMu.Unlock()
	if a.lastAppliedCfg == nil {
		return nil
	}
	cfgCopy := cloneConfig(*a.lastAppliedCfg)
	return &cfgCopy
}

func (a *Agent) setLastAppliedConfig(cfg wireguard.Config) {
	a.lastConfigMu.Lock()
	defer a.lastConfigMu.Unlock()
	cfgCopy := cloneConfig(cfg)
	a.lastAppliedCfg = &cfgCopy
}

func cloneConfig(cfg wireguard.Config) wireguard.Config {
	peersCopy := make([]wireguard.Peer, len(cfg.Peers))
	for i, peer := range cfg.Peers {
		allowedCopy := make([]string, len(peer.AllowedIPs))
		copy(allowedCopy, peer.AllowedIPs)
		peersCopy[i] = wireguard.Peer{
			PublicKey:  peer.PublicKey,
			Endpoint:   peer.Endpoint,
			AllowedIPs: allowedCopy,
			KeepAlive:  peer.KeepAlive,
		}
	}

	return wireguard.Config{
		PrivateKey: cfg.PrivateKey,
		Address:    cfg.Address,
		ListenPort: cfg.ListenPort,
		Peers:      peersCopy,
	}
}

func (a *Agent) bootstrapNode(ctx context.Context) error {
	nodeID := a.nodeID
	req := &pb.BootstrapInfrastructureNodeRequest{
		Token:    a.enrollmentToken,
		NodeType: a.nodeType,
		NodeId:   &nodeID,
		Hostname: a.nodeName,
	}
	if a.externalIP != "" {
		req.ExternalIp = &a.externalIP
	}
	if a.internalIP != "" {
		req.InternalIp = &a.internalIP
	}

	resp, err := a.client.BootstrapInfrastructureNode(ctx, req)
	if err != nil {
		return err
	}

	if resp.GetNodeId() != "" && resp.GetNodeId() != a.nodeID {
		a.logger.WithFields(logging.Fields{
			"requested": a.nodeID,
			"assigned":  resp.GetNodeId(),
		}).Warn("Bootstrap returned a different node_id; continuing with assigned value")
		a.nodeID = resp.GetNodeId()
	}

	a.logger.WithFields(logging.Fields{
		"node_id":    resp.GetNodeId(),
		"cluster_id": resp.GetClusterId(),
	}).Info("Node bootstrapped successfully")

	return nil
}

func loadOrGenerateNodeID(path string, logger logging.Logger) string {
	// Try to read file
	data, err := os.ReadFile(path)
	if err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			logger.WithField("node_id", id).Info("Loaded Node ID")
			return id
		}
	}

	// Generate new ID
	// Use hostname-uuid combination to be safe and informative
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "node"
	}

	newID := fmt.Sprintf("%s-%s", hostname, uuid.New().String())

	// Ensure directory exists for the configured NodeID path.
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			logger.WithError(err).Fatal("Failed to create node_id directory; node identity cannot persist")
		}
	}

	if err := os.WriteFile(path, []byte(newID), 0600); err != nil {
		logger.WithError(err).Fatal("Failed to persist Node ID; agent cannot maintain stable identity")
	} else {
		logger.WithField("node_id", newID).Info("Generated and persisted new Node ID")
	}

	return newID
}

func safeInt32(v int) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int32(v)
}
