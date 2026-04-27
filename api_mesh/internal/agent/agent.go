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
	pkgingress "frameworks/pkg/ingress"
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
	// MeshApplyDuration measures the time spent in wgManager.Apply per
	// layer. Histogram so latency outliers are visible.
	MeshApplyDuration *prometheus.HistogramVec
	// MeshApplyFailures counts apply attempts that did not reach a clean
	// device configuration, labelled by layer and reason. Reasons are the
	// stable enum documented on Agent.recordApplyFailure.
	MeshApplyFailures *prometheus.CounterVec
	// MeshPeerCount reports the peer count that was applied in the last
	// successful Apply, per layer. Updated only on success — failure paths
	// leave the previous value in place.
	MeshPeerCount *prometheus.GaugeVec
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
	GetTLSBundle(ctx context.Context, req *pb.GetTLSBundleRequest) (*pb.GetTLSBundleResponse, error)
}

// ingressClient lists ingress sites scoped to this Privateer's node so the
// agent can discover which Navigator-issued TLS bundles to sync onto disk.
type ingressClient interface {
	ListIngressSites(ctx context.Context, clusterID, nodeID string, pagination *pb.CursorPaginationRequest) (*pb.ListIngressSitesResponse, error)
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
	// appliedRevision is the mesh_revision that came back with the most
	// recent successful managed apply (post-DNS). Reported back to
	// Quartermaster on subsequent SyncMesh requests so 'mesh wg audit'
	// can spot agents stuck on stale revisions.
	appliedRevision  atomic.Pointer[string]
	registryClient   serviceRegistryClient
	ingressClient    ingressClient
	navigatorClient  certificateClient
	certIssueToken   string
	pkiBasePath      string
	ingressTLSRoot   string
	ingressTrigger   string
	expectedServices []string
	certSyncInterval time.Duration
	lastCertSyncUnix atomic.Int64
	lastIngressSync  atomic.Int64
	ingressMu        sync.Mutex
	ingressVersions  map[string]string
	cachedIngressIDs []string
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
	IngressClient         ingressClient
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
	ingress := cfg.IngressClient
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
		if ingress == nil {
			ingress = qmGRPCClient
		}
	}
	if registry == nil {
		if qmRegistry, ok := client.(serviceRegistryClient); ok {
			registry = qmRegistry
		}
	}
	if ingress == nil {
		if qmIngress, ok := client.(ingressClient); ok {
			ingress = qmIngress
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
		ingressClient:    ingress,
		navigatorClient:  navigatorClient,
		certIssueToken:   cfg.CertIssueToken,
		pkiBasePath:      cfg.PKIBasePath,
		ingressTLSRoot:   pkgingress.TLSRoot,
		ingressTrigger:   pkgingress.ReloadTrigger,
		expectedServices: append([]string(nil), cfg.ExpectedServiceTypes...),
		certSyncInterval: cfg.CertSyncInterval,
		ingressVersions:  make(map[string]string),
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
	const layer = "seed"
	priv, err := readPrivateKey(a.privateKeyFile)
	if err != nil {
		a.logger.WithError(err).Warn("Seed layer: private key unavailable")
		a.recordApplyFailure(layer, "private_key")
		return
	}
	cfg, err := selfConfig(priv, a.wireguardIP, a.listenPort)
	if err != nil {
		a.logger.WithError(err).Error("Seed layer: invalid self identity")
		a.recordApplyFailure(layer, "invalid_identity")
		return
	}
	peers, err := staticPeersToWireGuard(sp.Peers)
	if err != nil {
		a.logger.WithError(err).Error("Seed layer: invalid peer in static-peers.json")
		a.recordApplyFailure(layer, "invalid_peer")
		return
	}
	cfg.Peers = peers
	if err := wireguard.ValidateForApply(cfg); err != nil {
		a.logger.WithError(err).Error("Seed layer: policy validation failed")
		a.recordApplyFailure(layer, "policy")
		return
	}
	applyStart := time.Now()
	if err := a.wgManager.Apply(cfg); err != nil {
		a.logger.WithError(err).Error("Seed layer: failed to apply wireguard config")
		a.recordApplyFailure(layer, "configure")
		return
	}
	a.observeApplyDuration(layer, applyStart)
	a.setPeerCountMetric(layer, len(peers))

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
		a.recordApplyFailure(layer, "private_key")
		return
	}
	cfg, err := lastKnownToWireGuard(lk, priv, a.wireguardIP, a.listenPort)
	if err != nil {
		a.logger.WithError(err).Error("Persisted mesh: invalid snapshot")
		a.recordApplyFailure(layer, "invalid_peer")
		return
	}
	if err := wireguard.ValidateForApply(cfg); err != nil {
		a.logger.WithError(err).Error("Persisted mesh: policy validation failed")
		a.recordApplyFailure(layer, "policy")
		return
	}
	applyStart := time.Now()
	if err := a.wgManager.Apply(cfg); err != nil {
		a.logger.WithError(err).Error("Persisted mesh: apply failed")
		a.recordApplyFailure(layer, "configure")
		return
	}
	a.observeApplyDuration(layer, applyStart)
	a.setPeerCountMetric(layer, len(cfg.Peers))
	if len(lk.DNS) > 0 {
		if err := a.dnsServer.UpdateRecords(lk.DNS); err != nil {
			a.logger.WithError(err).Warn("Persisted mesh: DNS update failed")
		}
	}
	a.setLastAppliedConfig(cfg)
	// Reload the applied revision from disk so a post-restart agent can
	// report the revision it currently has on wg0 to Quartermaster on its
	// next SyncMesh — without this, the first sync after restart would
	// report empty even though the runtime came up on a known revision.
	if lk.Source == "dynamic" && lk.Version != "" {
		v := lk.Version
		a.appliedRevision.Store(&v)
	}
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

// observeApplyDuration records the time spent in a wgManager.Apply call.
// Called only when Apply was actually invoked — pre-Apply parse/policy
// failures don't have a meaningful duration to report.
func (a *Agent) observeApplyDuration(layer string, start time.Time) {
	if a.metrics == nil || a.metrics.MeshApplyDuration == nil {
		return
	}
	a.metrics.MeshApplyDuration.WithLabelValues(layer).Observe(time.Since(start).Seconds())
}

// recordApplyFailure increments the per-(layer, reason) failure counter.
// Reason values are stable enums:
//
//	private_key       — readPrivateKey / parse failed
//	invalid_identity  — selfConfig rejected the parsed identity
//	invalid_peer      — parsePeerStrings rejected an upstream peer record
//	policy            — wireguard.ValidateForApply rejected the assembled config
//	configure         — wgManager.Apply returned an error
//	dns               — DNS update failed after a successful Apply (rolled back)
func (a *Agent) recordApplyFailure(layer, reason string) {
	if a.metrics == nil || a.metrics.MeshApplyFailures == nil {
		return
	}
	a.metrics.MeshApplyFailures.WithLabelValues(layer, reason).Inc()
}

// setPeerCountMetric records the number of peers in the last fully
// successful apply for the given layer. "Fully successful" means
// wgManager.Apply ran and any subsequent steps that can force a rollback
// (DNS update on the managed path) also ran. Failure paths leave the
// previous value in place rather than overwriting it with a count that no
// longer reflects what is on the device.
func (a *Agent) setPeerCountMetric(layer string, count int) {
	if a.metrics == nil || a.metrics.MeshPeerCount == nil {
		return
	}
	a.metrics.MeshPeerCount.WithLabelValues(layer).Set(float64(count))
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
		a.recordApplyFailure("managed", "private_key")
		a.syncFailed()
		return
	}
	pubKey, err := pkgmesh.DerivePublicKey(privKey)
	if err != nil {
		a.logger.WithError(err).Error("Failed to derive mesh public key")
		a.recordApplyFailure("managed", "private_key")
		a.syncFailed()
		return
	}

	req := &pb.InfrastructureSyncRequest{
		NodeId:     a.nodeID,
		PublicKey:  pubKey,
		ListenPort: safeInt32(a.listenPort),
	}
	if rev := a.appliedRevision.Load(); rev != nil {
		req.AppliedMeshRevision = *rev
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

	const layer = "managed"
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
			a.recordApplyFailure(layer, "invalid_peer")
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
		a.recordApplyFailure(layer, "invalid_identity")
		a.syncFailed()
		return
	}
	cfg.Peers = peers

	if err := wireguard.ValidateForApply(cfg); err != nil {
		a.logger.WithError(err).Error("Managed apply: policy validation failed")
		a.recordApplyFailure(layer, "policy")
		a.syncFailed()
		return
	}

	// 4. Apply WireGuard Config
	applyStart := time.Now()
	if err := a.wgManager.Apply(cfg); err != nil {
		a.logger.WithError(err).Error("Failed to apply wireguard config")
		a.recordApplyFailure(layer, "configure")
		a.syncFailed()
		return
	}
	// observeApplyDuration measures the wgManager.Apply syscall, which did
	// run; record it even if the broader sync rolls back later. Peer count,
	// in contrast, reflects the device's settled state and is set only
	// after DNS succeeds (see below) so a rollback does not leave it
	// reporting peers that are no longer on the device.
	a.observeApplyDuration(layer, applyStart)

	// 5. Update DNS Records
	if err := a.dnsServer.UpdateRecords(dnsRecords); err != nil {
		a.logger.WithError(err).Error("Failed to update DNS records")
		a.recordApplyFailure(layer, "dns")
		a.rollbackWireGuardConfig()
		a.syncFailed()
		return
	}
	a.setPeerCountMetric(layer, len(peers))

	a.setLastAppliedConfig(cfg)
	if rev := resp.GetMeshRevision(); rev != "" {
		revCopy := rev
		a.appliedRevision.Store(&revCopy)
	}

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
	if err := a.syncIngressCertificates(); err != nil {
		a.logger.WithError(err).Warn("Failed to sync ingress TLS materials")
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

// syncIngressCertificates pulls Navigator-issued public TLS bundles for every
// IngressSite scoped to this node and writes them atomically beneath
// ingressTLSRoot. After any successful write it touches the reload trigger
// file so the host's systemd path unit can pick up the change and reload
// nginx — Privateer never invokes systemd directly.
func (a *Agent) syncIngressCertificates() error {
	if a.navigatorClient == nil || a.ingressClient == nil {
		return nil
	}
	if strings.TrimSpace(a.clusterID) == "" || strings.TrimSpace(a.nodeID) == "" {
		return nil
	}

	// Honor the configured cert sync cadence (default 5 min) so we don't
	// hammer Quartermaster (ListIngressSites) and Navigator (GetTLSBundle)
	// on every mesh-sync tick (default 30s). On a fresh start cachedIngressIDs
	// is empty and lastIngressSync is zero, both of which force a sync;
	// subsequent ticks within the cadence window short-circuit unless a
	// previously-known bundle's on-disk material is missing.
	now := time.Now().Unix()
	last := a.lastIngressSync.Load()
	if last > 0 && time.Duration(now-last)*time.Second < a.certSyncInterval {
		a.ingressMu.Lock()
		cached := append([]string(nil), a.cachedIngressIDs...)
		a.ingressMu.Unlock()
		if !a.missingIngressBundleMaterials(cached) {
			return nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), a.syncTimeout)
	defer cancel()

	bundleIDs, err := a.collectIngressBundleIDs(ctx)
	if err != nil {
		return err
	}
	a.ingressMu.Lock()
	a.cachedIngressIDs = append([]string(nil), bundleIDs...)
	a.ingressMu.Unlock()
	if len(bundleIDs) == 0 {
		a.lastIngressSync.Store(now)
		return nil
	}

	changed := false
	for _, bundleID := range bundleIDs {
		resp, err := a.navigatorClient.GetTLSBundle(ctx, &pb.GetTLSBundleRequest{BundleId: bundleID})
		if err != nil {
			a.logger.WithError(err).WithField("bundle_id", bundleID).Warn("Ingress sync: GetTLSBundle failed")
			continue
		}
		if !resp.GetFound() || strings.TrimSpace(resp.GetCertPem()) == "" || strings.TrimSpace(resp.GetKeyPem()) == "" {
			continue
		}
		marker := ingressBundleMarker(resp)
		if marker != "" && a.ingressVersionUnchanged(bundleID, marker) {
			continue
		}
		if err := a.writeIngressBundle(bundleID, resp.GetCertPem(), resp.GetKeyPem()); err != nil {
			return fmt.Errorf("write ingress bundle %s: %w", bundleID, err)
		}
		a.recordIngressVersion(bundleID, marker)
		changed = true
	}

	if changed {
		if err := a.touchIngressReloadTrigger(); err != nil {
			return fmt.Errorf("touch ingress reload trigger: %w", err)
		}
	}

	a.lastIngressSync.Store(now)
	return nil
}

func (a *Agent) collectIngressBundleIDs(ctx context.Context) ([]string, error) {
	seen := make(map[string]struct{})
	var pagination *pb.CursorPaginationRequest
	for {
		resp, err := a.ingressClient.ListIngressSites(ctx, a.clusterID, a.nodeID, pagination)
		if err != nil {
			return nil, fmt.Errorf("list ingress sites: %w", err)
		}
		for _, site := range resp.GetSites() {
			bundleID := strings.TrimSpace(site.GetTlsBundleId())
			if bundleID == "" {
				continue
			}
			// Quartermaster could in principle return a poisoned id; reject
			// anything that isn't safe as a path component before we touch
			// disk. The CLI also validates at registration time but defense
			// in depth here is cheap.
			if !pkgingress.IsValidBundleID(bundleID) {
				a.logger.WithField("bundle_id", bundleID).Warn("Ingress sync: ignoring bundle with unsafe id")
				continue
			}
			seen[bundleID] = struct{}{}
		}
		page := resp.GetPagination()
		if page == nil || !page.GetHasNextPage() || page.GetEndCursor() == "" {
			break
		}
		endCursor := page.GetEndCursor()
		pagination = &pb.CursorPaginationRequest{First: 200, After: &endCursor}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// missingIngressBundleMaterials reports whether any of the bundles we expect
// to keep in sync is missing its on-disk cert or key. It bypasses the cert
// sync cadence so Privateer always fetches when a bundle's files have been
// removed (e.g. fresh provision before the placeholder ran or post-rotation
// repair).
func (a *Agent) missingIngressBundleMaterials(bundleIDs []string) bool {
	for _, bundleID := range bundleIDs {
		dir := filepath.Join(a.ingressTLSRoot, bundleID)
		if !fileExistsAndNonEmpty(filepath.Join(dir, "tls.crt")) {
			return true
		}
		if !fileExistsAndNonEmpty(filepath.Join(dir, "tls.key")) {
			return true
		}
	}
	return false
}

// ingressBundleMarker returns a stable marker for change detection. Prefers
// the Navigator-supplied version field; falls back to expires_at. Empty
// marker means "always rewrite" (best-effort, no caching).
func ingressBundleMarker(resp *pb.GetTLSBundleResponse) string {
	if v := strings.TrimSpace(resp.GetVersion()); v != "" {
		return "v:" + v
	}
	if exp := resp.GetExpiresAt(); exp != 0 {
		return fmt.Sprintf("e:%d", exp)
	}
	return ""
}

func (a *Agent) ingressVersionUnchanged(bundleID, marker string) bool {
	a.ingressMu.Lock()
	defer a.ingressMu.Unlock()
	prev, ok := a.ingressVersions[bundleID]
	return ok && prev == marker
}

func (a *Agent) recordIngressVersion(bundleID, marker string) {
	a.ingressMu.Lock()
	defer a.ingressMu.Unlock()
	if marker == "" {
		delete(a.ingressVersions, bundleID)
		return
	}
	a.ingressVersions[bundleID] = marker
}

func (a *Agent) writeIngressBundle(bundleID, certPEM, keyPEM string) error {
	dir := filepath.Join(a.ingressTLSRoot, bundleID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := writeAtomicFile(filepath.Join(dir, "tls.crt"), []byte(certPEM), 0o644); err != nil {
		return err
	}
	if err := writeAtomicFile(filepath.Join(dir, "tls.key"), []byte(keyPEM), 0o640); err != nil {
		return err
	}
	// Ansible plants this sentinel next to placeholder material; remove it
	// once Privateer has installed real Navigator-issued certs so an
	// operator audit can tell genuine bundles apart from placeholders.
	_ = os.Remove(filepath.Join(dir, "tls.placeholder"))
	return nil
}

func (a *Agent) touchIngressReloadTrigger() error {
	if strings.TrimSpace(a.ingressTrigger) == "" {
		return nil
	}
	// systemd.path watches the trigger file via inotify on the parent
	// directory and reacts to PathModified, which corresponds to IN_MODIFY
	// / IN_CLOSE_WRITE on the watched path. Atomic rename (write-temp +
	// rename) replaces the file with a fresh inode and is observed as a
	// move/create on the parent — that pattern can be missed depending on
	// when systemd resolves the watch. An in-place truncate-write is what
	// the man page documents as reliable: open(O_WRONLY|O_TRUNC), write,
	// close. The Ansible reload_unit role pre-creates the trigger file with
	// privateer:privateer ownership so we always have a stable path to
	// rewrite.
	if err := os.MkdirAll(filepath.Dir(a.ingressTrigger), 0o750); err != nil {
		return err
	}
	body := []byte(time.Now().UTC().Format(time.RFC3339Nano) + "\n")
	f, err := os.OpenFile(a.ingressTrigger, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, writeErr := f.Write(body); writeErr != nil {
		_ = f.Close()
		return writeErr
	}
	return f.Close()
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
