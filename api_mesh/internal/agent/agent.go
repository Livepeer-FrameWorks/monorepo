package agent

import (
	"context"
	"fmt"
	"math"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"frameworks/api_mesh/internal/dns"
	"frameworks/api_mesh/internal/wireguard"
	navclient "frameworks/pkg/clients/navigator"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/logging"
	pkgmesh "frameworks/pkg/mesh"
	infra "frameworks/pkg/models"
	pb "frameworks/pkg/proto"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

// Metrics holds Prometheus metrics for the agent
type Metrics struct {
	SyncOperations   *prometheus.CounterVec
	PeersConnected   *prometheus.GaugeVec
	DNSQueries       *prometheus.CounterVec
	WireGuardResyncs *prometheus.CounterVec
	// LayerApplied reports which layer's config is currently on wg0:
	// labels: layer="managed" (fresh from Quartermaster)
	//       | "last_known" (disk cache from prior managed sync)
	//       | "seed"       (Ansible-rendered GitOps substrate).
	LayerApplied *prometheus.GaugeVec
}

type meshClient interface {
	SyncMesh(ctx context.Context, req *pb.InfrastructureSyncRequest) (*pb.InfrastructureSyncResponse, error)
	CreateNode(ctx context.Context, req *pb.CreateNodeRequest) (*pb.NodeResponse, error)
	CreateBootstrapToken(ctx context.Context, req *pb.CreateBootstrapTokenRequest) (*pb.CreateBootstrapTokenResponse, error)
}

type serviceRegistryClient interface {
	ListServiceInstances(ctx context.Context, clusterID, serviceID, nodeID string, pagination *pb.CursorPaginationRequest) (*pb.ListServiceInstancesResponse, error)
}

type certificateClient interface {
	GetCABundle(ctx context.Context, req *pb.GetCABundleRequest) (*pb.GetCABundleResponse, error)
	IssueInternalCert(ctx context.Context, req *pb.IssueInternalCertRequest) (*pb.IssueInternalCertResponse, error)
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
	clusterID        string
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
	registryClient   serviceRegistryClient
	navigatorClient  certificateClient
	certIssueToken   string
	pkiBasePath      string
	expectedServices []string
	certSyncInterval time.Duration
	lastCertSyncUnix atomic.Int64
	// Startup substrate inputs and persisted managed cache.
	staticPeersFile string
	privateKeyFile  string
	wireguardIP     string
	lastKnownPath   string
}

type Config struct {
	QuartermasterGRPCAddr string
	ServiceToken          string
	ClusterID             string
	// Startup seed inputs let the agent bring wg0 up from GitOps-rendered
	// state before the first successful Quartermaster sync.
	StaticPeersFile       string
	PrivateKeyFile        string
	WireguardIP           string
	LastKnownPath         string // defaults to {DataDir}/last_known_mesh.json
	DataDir               string // defaults to /var/lib/privateer
	AllowInsecure         bool
	CACertFile            string
	ServerName            string
	NavigatorGRPCAddr     string
	CertIssueToken        string
	PKIBasePath           string
	ExpectedServiceTypes  []string
	CertSyncInterval      time.Duration
	NodeIDPath            string
	NodeID                string // Explicit identity from env; skips file-based generation when set
	InterfaceName         string
	NodeType              string
	NodeName              string
	ExternalIP            string
	InternalIP            string
	ListenPort            int
	SyncInterval          time.Duration
	SyncTimeout           time.Duration
	DNSPort               int
	DNSUpstreams          []string // Upstream resolver addresses for non-.internal queries
	Logger                logging.Logger
	Metrics               *Metrics
	MeshClient            meshClient
	ServiceRegistryClient serviceRegistryClient
	NavigatorClient       certificateClient
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
	if cfg.PKIBasePath == "" {
		cfg.PKIBasePath = "/etc/frameworks/pki"
	}
	if cfg.CertSyncInterval == 0 {
		cfg.CertSyncInterval = 5 * time.Minute
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "/var/lib/privateer"
	}
	if cfg.LastKnownPath == "" {
		cfg.LastKnownPath = filepath.Join(cfg.DataDir, "last_known_mesh.json")
	}
	if cfg.NodeType == "" {
		cfg.NodeType = infra.NodeTypeCore
		if cfg.Logger != nil {
			cfg.Logger.Warnf("MESH_NODE_TYPE not set, defaulting to %q", infra.NodeTypeCore)
		}
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

	// Initialize the Quartermaster client when credentials are available.
	client := cfg.MeshClient
	registry := cfg.ServiceRegistryClient
	if client == nil && cfg.ServiceToken != "" && cfg.QuartermasterGRPCAddr != "" {
		qmGRPCClient, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
			GRPCAddr:      cfg.QuartermasterGRPCAddr,
			ServiceToken:  cfg.ServiceToken,
			Logger:        cfg.Logger,
			Timeout:       10 * time.Second,
			AllowInsecure: cfg.AllowInsecure,
			CACertFile:    cfg.CACertFile,
			ServerName:    cfg.ServerName,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create quartermaster gRPC client: %w", err)
		}
		client = qmGRPCClient
		if registry == nil {
			registry = qmGRPCClient
		}
	}
	if registry == nil {
		if qmRegistry, ok := client.(serviceRegistryClient); ok {
			registry = qmRegistry
		}
	}

	navigatorClient := cfg.NavigatorClient
	if navigatorClient == nil && cfg.NavigatorGRPCAddr != "" {
		var err error
		navigatorClient, err = navclient.NewClient(navclient.Config{
			Addr:          cfg.NavigatorGRPCAddr,
			Timeout:       10 * time.Second,
			Logger:        cfg.Logger,
			ServiceToken:  cfg.ServiceToken,
			AllowInsecure: cfg.AllowInsecure,
			CACertFile:    cfg.CACertFile,
			ServerName:    cfg.ServerName,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create navigator gRPC client: %w", err)
		}
	}

	// Initialize DNS Server
	dnsSrv := cfg.DNSService
	if dnsSrv == nil {
		dnsSrv = dns.NewServer(cfg.Logger, cfg.DNSPort, cfg.DNSUpstreams...)
	}

	return &Agent{
		logger:           cfg.Logger,
		client:           client,
		wgManager:        wg,
		dnsServer:        dnsSrv,
		interfaceName:    cfg.InterfaceName,
		nodeType:         cfg.NodeType,
		nodeName:         cfg.NodeName,
		clusterID:        cfg.ClusterID,
		externalIP:       cfg.ExternalIP,
		internalIP:       cfg.InternalIP,
		listenPort:       cfg.ListenPort,
		syncInterval:     cfg.SyncInterval,
		syncTimeout:      cfg.SyncTimeout,
		stopChan:         make(chan struct{}),
		nodeID:           resolveNodeID(cfg),
		metrics:          cfg.Metrics,
		registryClient:   registry,
		navigatorClient:  navigatorClient,
		certIssueToken:   cfg.CertIssueToken,
		pkiBasePath:      cfg.PKIBasePath,
		expectedServices: append([]string(nil), cfg.ExpectedServiceTypes...),
		certSyncInterval: cfg.CertSyncInterval,
		staticPeersFile:  cfg.StaticPeersFile,
		privateKeyFile:   cfg.PrivateKeyFile,
		wireguardIP:      cfg.WireguardIP,
		lastKnownPath:    cfg.LastKnownPath,
	}, nil
}

func (a *Agent) Start() error {
	a.logger.Info("Starting Privateer Agent")

	// 1. Init WireGuard Interface
	if err := a.wgManager.Init(); err != nil {
		return fmt.Errorf("failed to init wireguard: %w", err)
	}

	// 1b. Apply last-known / static before ever calling Quartermaster so the
	// mesh is usable on a fresh boot or while QM is down.
	a.applyStartupMesh()

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

// applyStartupMesh brings up wg0 from last_known_mesh.json (preferred) or the
// Ansible-rendered static-peers.json.
//
// Self identity (own wireguard_ip, listen port, private key) is always read
// from the identity layer on disk — never from the persisted snapshot — so
// a GitOps key rotation always propagates on reboot.
func (a *Agent) applyStartupMesh() {
	if a.lastKnownPath == "" && a.staticPeersFile == "" {
		return
	}
	lk, err := loadLastKnown(a.lastKnownPath)
	if err != nil {
		a.logger.WithError(err).Warn("Failed to load last-known mesh, will fall back to seed peers")
	}
	sp, err := loadStaticPeers(a.staticPeersFile)
	if err != nil {
		a.logger.WithError(err).Warn("Failed to load seed peers")
	}

	// Managed snapshots always apply (Quartermaster's most recent view is
	// authoritative). Seed snapshots only apply when they match the on-disk
	// seed hash (GitOps unchanged during downtime). Otherwise render the
	// current seed file.
	switch {
	case lk != nil && lk.Source == "dynamic":
		// Cache of a prior Quartermaster sync — applied at startup, before
		// the live SyncMesh loop takes over with layer="managed".
		a.applyPersistedMesh(lk, "last_known")
	case lk != nil && sp != nil && lk.Source == "seed" && lk.Version == sp.Version:
		a.applyPersistedMesh(lk, "seed")
	case sp != nil && a.privateKeyFile != "" && a.wireguardIP != "":
		a.applyStatic(sp)
	}
}

// applyStatic configures wg0 from the Ansible-rendered static peers and
// writes a fresh last_known_mesh.json tagged source=seed.
func (a *Agent) applyStatic(sp *staticPeersFile) {
	priv, err := readPrivateKey(a.privateKeyFile)
	if err != nil {
		a.logger.WithError(err).Warn("Seed layer: private key unavailable")
		return
	}
	cfg, err := selfConfig(priv, a.wireguardIP, a.listenPort)
	if err != nil {
		a.logger.WithError(err).Error("Seed layer: invalid self identity")
		return
	}
	peers, err := staticPeersToWireGuard(sp.Peers)
	if err != nil {
		a.logger.WithError(err).Error("Seed layer: invalid peer in static-peers.json")
		return
	}
	cfg.Peers = peers
	if err := wireguard.ValidateForApply(cfg); err != nil {
		a.logger.WithError(err).Error("Seed layer: policy validation failed")
		return
	}
	if err := a.wgManager.Apply(cfg); err != nil {
		a.logger.WithError(err).Error("Seed layer: failed to apply wireguard config")
		return
	}

	dns := map[string][]string{}
	for i, p := range sp.Peers {
		if p.Name != "" && len(peers[i].AllowedIPs) > 0 {
			dns[p.Name] = []string{peers[i].AllowedIPs[0].IP.String()}
		}
	}
	for name, ips := range sp.DNS {
		if name == "" || len(ips) == 0 {
			continue
		}
		dns[name] = append([]string(nil), ips...)
	}
	if err := a.dnsServer.UpdateRecords(dns); err != nil {
		a.logger.WithError(err).Warn("Seed layer: failed to update DNS")
	}

	a.setLastAppliedConfig(cfg)
	a.setLayerMetric("seed")

	lk := &lastKnownMesh{
		Source:      "seed",
		Version:     sp.Version,
		WireguardIP: a.wireguardIP,
		ListenPort:  a.listenPort,
		Peers:       staticToLastKnownPeers(sp.Peers),
		DNS:         dns,
	}
	if err := writeLastKnown(a.lastKnownPath, lk); err != nil {
		a.logger.WithError(err).Warn("Seed layer: failed to persist last-known")
	}
	a.logger.Info("Seed mesh layer applied from GitOps")
}

// applyPersistedMesh reapplies a snapshot loaded from last_known_mesh.json.
// Self identity always comes from the agent's configured identity layer
// (wireguardIP + listenPort + private key on disk) — the cached snapshot's
// copy of those fields is ignored so a GitOps key rotation propagates.
func (a *Agent) applyPersistedMesh(lk *lastKnownMesh, layer string) {
	priv, err := readPrivateKey(a.privateKeyFile)
	if err != nil {
		a.logger.WithError(err).Warn("Persisted mesh: private key unavailable")
		return
	}
	cfg, err := lastKnownToWireGuard(lk, priv, a.wireguardIP, a.listenPort)
	if err != nil {
		a.logger.WithError(err).Error("Persisted mesh: invalid snapshot")
		return
	}
	if err := wireguard.ValidateForApply(cfg); err != nil {
		a.logger.WithError(err).Error("Persisted mesh: policy validation failed")
		return
	}
	if err := a.wgManager.Apply(cfg); err != nil {
		a.logger.WithError(err).Error("Persisted mesh: apply failed")
		return
	}
	if len(lk.DNS) > 0 {
		if err := a.dnsServer.UpdateRecords(lk.DNS); err != nil {
			a.logger.WithError(err).Warn("Persisted mesh: DNS update failed")
		}
	}
	a.setLastAppliedConfig(cfg)
	a.setLayerMetric(layer)
	a.logger.Infof("Applied %s mesh layer from %s", layer, a.lastKnownPath)
}

func (a *Agent) setLayerMetric(layer string) {
	if a.metrics == nil || a.metrics.LayerApplied == nil {
		return
	}
	for _, known := range []string{"managed", "last_known", "seed"} {
		v := 0.0
		if known == layer {
			v = 1.0
		}
		a.metrics.LayerApplied.WithLabelValues(known).Set(v)
	}
}

func staticToLastKnownPeers(in []staticPeer) []lastKnownPeer {
	out := make([]lastKnownPeer, len(in))
	for i, p := range in {
		ka := p.KeepAlive
		if ka == 0 {
			ka = 25
		}
		out[i] = lastKnownPeer{
			Name:       p.Name,
			PublicKey:  p.PublicKey,
			AllowedIPs: p.AllowedIPs,
			Endpoint:   p.Endpoint,
			KeepAlive:  ka,
		}
	}
	return out
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
	// Tests may omit the Quartermaster client; startup mesh handling is
	// independent from the managed overlay.
	if a.client == nil {
		return
	}

	privKey, err := readPrivateKey(a.privateKeyFile)
	if err != nil {
		a.logger.WithError(err).Error("Failed to read mesh private key")
		a.syncFailed()
		return
	}
	pubKey, err := pkgmesh.DerivePublicKey(privKey)
	if err != nil {
		a.logger.WithError(err).Error("Failed to derive mesh public key")
		a.syncFailed()
		return
	}

	req := &pb.InfrastructureSyncRequest{
		NodeId:     a.nodeID,
		PublicKey:  pubKey,
		ListenPort: safeInt32(a.listenPort),
	}

	syncCtx, cancel := context.WithTimeout(context.Background(), a.syncTimeout)
	resp, err := a.client.SyncMesh(syncCtx, req)
	cancel()
	if err != nil {
		code := status.Code(err)
		if code == codes.NotFound || code == codes.FailedPrecondition {
			a.logger.WithError(err).Warn("Node identity missing or stale in Quartermaster. Re-registering from local identity")
			registerCtx, cancel := context.WithTimeout(context.Background(), a.syncTimeout)
			registerErr := a.registerNode(registerCtx, pubKey)
			cancel()
			if registerErr != nil {
				a.logger.WithError(registerErr).Error("Node registration failed")
				a.syncFailed()
				return
			}

			// Retry sync after successful registration.
			req.NodeId = a.nodeID
			retryCtx, cancel := context.WithTimeout(context.Background(), a.syncTimeout)
			resp, err = a.client.SyncMesh(retryCtx, req)
			cancel()
			if err != nil {
				a.logger.WithError(err).Error("Failed to sync after node registration")
				a.syncFailed()
				return
			}
		}
		if err != nil {
			a.logger.WithError(err).Error("Failed to sync infrastructure")
			a.syncFailed()
			return
		}
	}

	if err := a.validateManagedSelfIdentity(resp); err != nil {
		a.logger.WithError(err).Error("Quartermaster returned conflicting mesh self identity")
		a.syncFailed()
		return
	}

	peers := make([]wireguard.Peer, 0, len(resp.Peers))
	dnsRecords := make(map[string][]string)

	for _, p := range resp.Peers {
		label := p.NodeName
		if label == "" {
			label = p.PublicKey
		}
		peer, err := parsePeerStrings(label, p.PublicKey, p.Endpoint, p.AllowedIps, int(p.KeepAlive))
		if err != nil {
			a.logger.WithError(err).WithField("peer", label).Error("Quartermaster returned a malformed peer; failing sync")
			a.syncFailed()
			return
		}
		peers = append(peers, peer)

		if p.NodeName != "" && len(peer.AllowedIPs) > 0 {
			dnsRecords[p.NodeName] = []string{peer.AllowedIPs[0].IP.String()}
		}
	}

	// Add Service Endpoints (Aliases)
	for sName, sEndpoints := range resp.ServiceEndpoints {
		dnsRecords[sName] = append(dnsRecords[sName], sEndpoints.Ips...)
	}

	cfg, cfgErr := selfConfig(privKey, a.wireguardIP, a.listenPort)
	if cfgErr != nil {
		a.logger.WithError(cfgErr).Error("Invalid self identity for managed apply")
		a.syncFailed()
		return
	}
	cfg.Peers = peers

	if err := wireguard.ValidateForApply(cfg); err != nil {
		a.logger.WithError(err).Error("Managed apply: policy validation failed")
		a.syncFailed()
		return
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
	a.setLayerMetric("managed")
	if err := writeLastKnown(a.lastKnownPath, &lastKnownMesh{
		Source:      "dynamic",
		Version:     resp.MeshRevision,
		WireguardIP: a.wireguardIP,
		ListenPort:  a.listenPort,
		Peers:       dynamicPeersToLastKnown(resp.Peers),
		DNS:         dnsRecords,
	}); err != nil {
		a.logger.WithError(err).Warn("Failed to persist last-known mesh snapshot")
	}
	if err := a.syncInternalCertificates(); err != nil {
		a.logger.WithError(err).Warn("Failed to sync internal TLS materials")
	}
	a.logger.Info("Successfully applied wireguard config")
}

func (a *Agent) validateManagedSelfIdentity(resp *pb.InfrastructureSyncResponse) error {
	if resp == nil {
		return fmt.Errorf("empty sync response")
	}
	if strings.TrimSpace(a.wireguardIP) == "" {
		return fmt.Errorf("local wireguard_ip is not configured")
	}
	if resp.GetWireguardIp() == "" {
		return fmt.Errorf("sync response omitted wireguard_ip for node %s", a.nodeID)
	}
	if resp.GetWireguardIp() != a.wireguardIP {
		return fmt.Errorf("sync response wireguard_ip %q does not match local GitOps identity %q", resp.GetWireguardIp(), a.wireguardIP)
	}
	if a.listenPort <= 0 {
		return fmt.Errorf("local wireguard listen port is not configured")
	}
	if resp.GetWireguardPort() == 0 {
		return fmt.Errorf("sync response omitted wireguard listen port for node %s", a.nodeID)
	}
	if resp.GetWireguardPort() != int32(a.listenPort) {
		return fmt.Errorf("sync response wireguard port %d does not match local GitOps identity %d", resp.GetWireguardPort(), a.listenPort)
	}
	return nil
}

func dynamicPeersToLastKnown(peers []*pb.InfrastructurePeer) []lastKnownPeer {
	out := make([]lastKnownPeer, len(peers))
	for i, p := range peers {
		out[i] = lastKnownPeer{
			Name:       p.NodeName,
			PublicKey:  p.PublicKey,
			AllowedIPs: p.AllowedIps,
			Endpoint:   p.Endpoint,
			KeepAlive:  int(p.KeepAlive),
		}
	}
	return out
}

func (a *Agent) registerNode(ctx context.Context, publicKey string) error {
	if strings.TrimSpace(a.clusterID) == "" {
		return fmt.Errorf("cluster_id is required for node registration")
	}
	req := &pb.CreateNodeRequest{
		NodeId:    a.nodeID,
		ClusterId: a.clusterID,
		NodeName:  a.nodeName,
		NodeType:  a.nodeType,
	}
	if a.externalIP != "" {
		req.ExternalIp = &a.externalIP
	}
	if a.internalIP != "" {
		req.InternalIp = &a.internalIP
	}
	if a.wireguardIP != "" {
		req.WireguardIp = &a.wireguardIP
	}
	if strings.TrimSpace(publicKey) != "" {
		req.WireguardPublicKey = &publicKey
	}
	if a.listenPort > 0 {
		port := int32(a.listenPort)
		req.WireguardPort = &port
	}
	if _, err := a.client.CreateNode(ctx, req); err != nil {
		return err
	}
	a.logger.WithFields(logging.Fields{
		"node_id":    a.nodeID,
		"cluster_id": a.clusterID,
	}).Info("Registered node in Quartermaster from local identity")
	return nil
}

func (a *Agent) ensureCertIssueToken(ctx context.Context) error {
	if strings.TrimSpace(a.certIssueToken) != "" {
		return nil
	}
	if a.client == nil {
		return fmt.Errorf("quartermaster client unavailable for cert token minting")
	}
	if strings.TrimSpace(a.clusterID) == "" {
		return fmt.Errorf("cluster_id is required for cert token minting")
	}
	metadata, err := structpb.NewStruct(map[string]any{
		"node_id": a.nodeID,
		"purpose": "cert_sync",
	})
	if err != nil {
		return fmt.Errorf("build cert token metadata: %w", err)
	}
	resp, err := a.client.CreateBootstrapToken(ctx, &pb.CreateBootstrapTokenRequest{
		Name:      fmt.Sprintf("Internal Cert Sync Token for %s", a.nodeID),
		Kind:      "infrastructure_node",
		ClusterId: &a.clusterID,
		Ttl:       "720h",
		Metadata:  metadata,
	})
	if err != nil {
		return fmt.Errorf("create cert sync token: %w", err)
	}
	a.certIssueToken = resp.GetToken().GetToken()
	if strings.TrimSpace(a.certIssueToken) == "" {
		return fmt.Errorf("quartermaster returned an empty cert sync token")
	}
	return nil
}

func (a *Agent) syncInternalCertificates() error {
	if a.navigatorClient == nil {
		return nil
	}
	if len(a.expectedServices) == 0 && a.registryClient == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), a.syncTimeout)
	defer cancel()

	serviceTypes, err := a.resolvedServiceTypes(ctx)
	if err != nil {
		return err
	}

	now := time.Now().Unix()
	last := a.lastCertSyncUnix.Load()
	if last > 0 && time.Duration(now-last)*time.Second < a.certSyncInterval && !a.missingInternalPKIMaterials(serviceTypes) {
		return nil
	}
	if issueTokenErr := a.ensureCertIssueToken(ctx); issueTokenErr != nil {
		return issueTokenErr
	}

	bundleResp, err := a.navigatorClient.GetCABundle(ctx, &pb.GetCABundleRequest{})
	if err != nil {
		return fmt.Errorf("get ca bundle: %w", err)
	}
	if !bundleResp.GetFound() || strings.TrimSpace(bundleResp.GetCaPem()) == "" {
		return fmt.Errorf("navigator returned no internal ca bundle")
	}
	if err := a.writePKIFile("ca.crt", bundleResp.GetCaPem(), 0644); err != nil {
		return fmt.Errorf("write ca bundle: %w", err)
	}

	for _, serviceType := range serviceTypes {
		resp, issueErr := a.navigatorClient.IssueInternalCert(ctx, &pb.IssueInternalCertRequest{
			NodeId:      a.nodeID,
			ServiceType: serviceType,
			IssueToken:  a.certIssueToken,
		})
		if issueErr != nil {
			return fmt.Errorf("issue internal cert for %s: %w", serviceType, issueErr)
		}
		if !resp.GetSuccess() {
			return fmt.Errorf("issue internal cert for %s rejected: %s", serviceType, resp.GetError())
		}
		if err := a.writeServiceCertificate(serviceType, resp.GetCertPem(), resp.GetKeyPem()); err != nil {
			return fmt.Errorf("write internal cert for %s: %w", serviceType, err)
		}
	}

	a.lastCertSyncUnix.Store(now)
	return nil
}

func (a *Agent) missingInternalPKIMaterials(serviceTypes []string) bool {
	if !fileExistsAndNonEmpty(filepath.Join(a.pkiBasePath, "ca.crt")) {
		return true
	}
	for _, serviceType := range serviceTypes {
		if !fileExistsAndNonEmpty(filepath.Join(a.pkiBasePath, "services", serviceType, "tls.crt")) {
			return true
		}
		if !fileExistsAndNonEmpty(filepath.Join(a.pkiBasePath, "services", serviceType, "tls.key")) {
			return true
		}
	}
	return false
}

func fileExistsAndNonEmpty(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

func (a *Agent) resolvedServiceTypes(ctx context.Context) ([]string, error) {
	if len(a.expectedServices) == 0 {
		return a.listNodeServiceTypes(ctx)
	}

	seen := make(map[string]struct{}, len(a.expectedServices))
	serviceTypes := make([]string, 0, len(a.expectedServices))
	for _, serviceType := range a.expectedServices {
		serviceType = strings.TrimSpace(serviceType)
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
	return serviceTypes, nil
}

func (a *Agent) listNodeServiceTypes(ctx context.Context) ([]string, error) {
	pagination := &pb.CursorPaginationRequest{First: 200}
	seen := make(map[string]struct{})
	var services []string

	for {
		resp, err := a.registryClient.ListServiceInstances(ctx, "", "", a.nodeID, pagination)
		if err != nil {
			return nil, fmt.Errorf("list service instances for node %s: %w", a.nodeID, err)
		}
		for _, instance := range resp.GetInstances() {
			serviceType := strings.TrimSpace(instance.GetServiceId())
			if serviceType == "" || strings.EqualFold(instance.GetStatus(), "stopped") {
				continue
			}
			if _, ok := seen[serviceType]; ok {
				continue
			}
			seen[serviceType] = struct{}{}
			services = append(services, serviceType)
		}

		page := resp.GetPagination()
		if page == nil || !page.GetHasNextPage() || page.GetEndCursor() == "" {
			break
		}
		endCursor := page.GetEndCursor()
		pagination = &pb.CursorPaginationRequest{
			First: 200,
			After: &endCursor,
		}
	}

	return services, nil
}

func (a *Agent) writeServiceCertificate(serviceType, certPEM, keyPEM string) error {
	dir := filepath.Join(a.pkiBasePath, "services", serviceType)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := writeAtomicFile(filepath.Join(dir, "tls.crt"), []byte(certPEM), 0644); err != nil {
		return err
	}
	return writeAtomicFile(filepath.Join(dir, "tls.key"), []byte(keyPEM), 0600)
}

func (a *Agent) writePKIFile(relativePath, content string, mode os.FileMode) error {
	absPath := filepath.Join(a.pkiBasePath, relativePath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		return err
	}
	return writeAtomicFile(absPath, []byte(content), mode)
}

func writeAtomicFile(absPath string, content []byte, mode os.FileMode) error {
	tmpPath := absPath + ".tmp"
	if err := os.WriteFile(tmpPath, content, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return err
	}
	return os.Rename(tmpPath, absPath)
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
		allowedCopy := make([]net.IPNet, len(peer.AllowedIPs))
		for j, ipnet := range peer.AllowedIPs {
			ipCopy := make(net.IP, len(ipnet.IP))
			copy(ipCopy, ipnet.IP)
			maskCopy := make(net.IPMask, len(ipnet.Mask))
			copy(maskCopy, ipnet.Mask)
			allowedCopy[j] = net.IPNet{IP: ipCopy, Mask: maskCopy}
		}
		var endpointCopy *net.UDPAddr
		if peer.Endpoint != nil {
			ec := *peer.Endpoint
			if peer.Endpoint.IP != nil {
				ipCopy := make(net.IP, len(peer.Endpoint.IP))
				copy(ipCopy, peer.Endpoint.IP)
				ec.IP = ipCopy
			}
			endpointCopy = &ec
		}
		peersCopy[i] = wireguard.Peer{
			PublicKey:  peer.PublicKey,
			Endpoint:   endpointCopy,
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

func resolveNodeID(cfg Config) string {
	if cfg.NodeID != "" {
		if cfg.Logger != nil {
			cfg.Logger.WithField("node_id", cfg.NodeID).Info("Using explicit Node ID from config")
		}
		// Persist so restarts are stable even if the env var disappears
		persistNodeID(cfg.NodeIDPath, cfg.NodeID, cfg.Logger)
		return cfg.NodeID
	}
	return loadOrGenerateNodeID(cfg.NodeIDPath, cfg.Logger)
}

func persistNodeID(path, id string, logger logging.Logger) {
	if path == "" {
		return
	}
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			if logger != nil {
				logger.WithError(err).Warn("Failed to create node_id directory for persistence")
			}
			return
		}
	}
	if err := os.WriteFile(path, []byte(id), 0600); err != nil {
		if logger != nil {
			logger.WithError(err).Warn("Failed to persist explicit Node ID to disk")
		}
	}
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
