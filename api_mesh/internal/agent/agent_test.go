package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"frameworks/api_mesh/internal/wireguard"
	"frameworks/pkg/logging"
	pkgmesh "frameworks/pkg/mesh"
	pb "frameworks/pkg/proto"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeMeshClient struct {
	syncResponses        []meshSyncResult
	createNodeResponses  []meshCreateNodeResult
	createTokenResponses []meshCreateTokenResult
	syncRequests         []*pb.InfrastructureSyncRequest
	createNodeRequests   []*pb.CreateNodeRequest
	createTokenRequests  []*pb.CreateBootstrapTokenRequest
}

type fakeServiceRegistryClient struct {
	responses []*pb.ListServiceInstancesResponse
	err       error
	requests  []*pb.CursorPaginationRequest
}

func (f *fakeServiceRegistryClient) ListServiceInstances(_ context.Context, _, _, _ string, pagination *pb.CursorPaginationRequest) (*pb.ListServiceInstancesResponse, error) {
	f.requests = append(f.requests, pagination)
	if f.err != nil {
		return nil, f.err
	}
	if len(f.responses) == 0 {
		return &pb.ListServiceInstancesResponse{}, nil
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}

type fakeCertificateClient struct {
	caResponse     *pb.GetCABundleResponse
	caErr          error
	issueResponse  *pb.IssueInternalCertResponse
	issueErr       error
	issueRequests  []*pb.IssueInternalCertRequest
	caRequestCount int
}

func (f *fakeCertificateClient) GetCABundle(_ context.Context, _ *pb.GetCABundleRequest) (*pb.GetCABundleResponse, error) {
	f.caRequestCount++
	if f.caErr != nil {
		return nil, f.caErr
	}
	if f.caResponse == nil {
		return &pb.GetCABundleResponse{}, nil
	}
	return f.caResponse, nil
}

func (f *fakeCertificateClient) IssueInternalCert(_ context.Context, req *pb.IssueInternalCertRequest) (*pb.IssueInternalCertResponse, error) {
	f.issueRequests = append(f.issueRequests, req)
	if f.issueErr != nil {
		return nil, f.issueErr
	}
	if f.issueResponse == nil {
		return &pb.IssueInternalCertResponse{}, nil
	}
	return f.issueResponse, nil
}

type meshSyncResult struct {
	resp *pb.InfrastructureSyncResponse
	err  error
}

type meshCreateNodeResult struct {
	resp *pb.NodeResponse
	err  error
}

type meshCreateTokenResult struct {
	resp *pb.CreateBootstrapTokenResponse
	err  error
}

func (f *fakeMeshClient) SyncMesh(_ context.Context, req *pb.InfrastructureSyncRequest) (*pb.InfrastructureSyncResponse, error) {
	f.syncRequests = append(f.syncRequests, req)
	if len(f.syncResponses) == 0 {
		return nil, status.Error(codes.Internal, "no sync response")
	}
	result := f.syncResponses[0]
	f.syncResponses = f.syncResponses[1:]
	return result.resp, result.err
}

func (f *fakeMeshClient) CreateNode(_ context.Context, req *pb.CreateNodeRequest) (*pb.NodeResponse, error) {
	f.createNodeRequests = append(f.createNodeRequests, req)
	if len(f.createNodeResponses) == 0 {
		return nil, status.Error(codes.Internal, "no create-node response")
	}
	result := f.createNodeResponses[0]
	f.createNodeResponses = f.createNodeResponses[1:]
	return result.resp, result.err
}

func (f *fakeMeshClient) CreateBootstrapToken(_ context.Context, req *pb.CreateBootstrapTokenRequest) (*pb.CreateBootstrapTokenResponse, error) {
	f.createTokenRequests = append(f.createTokenRequests, req)
	if len(f.createTokenResponses) == 0 {
		return nil, status.Error(codes.Internal, "no create-token response")
	}
	result := f.createTokenResponses[0]
	f.createTokenResponses = f.createTokenResponses[1:]
	return result.resp, result.err
}

func TestResolveNodeID_ExplicitTakesPrecedence(t *testing.T) {
	dir := t.TempDir()
	idPath := filepath.Join(dir, "node_id")
	os.WriteFile(idPath, []byte("file-based-id"), 0600)

	cfg := Config{
		NodeID:     "explicit-id",
		NodeIDPath: idPath,
		Logger:     logging.NewLogger(),
	}
	got := resolveNodeID(cfg)
	if got != "explicit-id" {
		t.Fatalf("expected explicit-id, got %q", got)
	}

	// Explicit ID should also be persisted to disk for restart stability
	data, err := os.ReadFile(idPath)
	if err != nil {
		t.Fatalf("expected persisted file, got error: %v", err)
	}
	if string(data) != "explicit-id" {
		t.Fatalf("expected persisted explicit-id, got %q", string(data))
	}
}

func TestResolveNodeID_FallsBackToFile(t *testing.T) {
	dir := t.TempDir()
	idPath := filepath.Join(dir, "node_id")
	os.WriteFile(idPath, []byte("file-based-id"), 0600)

	cfg := Config{
		NodeIDPath: idPath,
		Logger:     logging.NewLogger(),
	}
	got := resolveNodeID(cfg)
	if got != "file-based-id" {
		t.Fatalf("expected file-based-id, got %q", got)
	}
}

func TestResolveNodeID_GeneratesWhenNoExplicitOrFile(t *testing.T) {
	dir := t.TempDir()
	idPath := filepath.Join(dir, "node_id")

	cfg := Config{
		NodeIDPath: idPath,
		Logger:     logging.NewLogger(),
	}
	got := resolveNodeID(cfg)
	if got == "" {
		t.Fatal("expected generated node ID, got empty string")
	}

	// Should be persisted
	data, err := os.ReadFile(idPath)
	if err != nil {
		t.Fatalf("expected persisted file, got error: %v", err)
	}
	if string(data) != got {
		t.Fatalf("persisted value %q doesn't match returned %q", string(data), got)
	}
}

type fakeWireguard struct {
	mu       sync.Mutex
	applyErr error
	pubKey   string
	privKey  string
	applied  []wireguard.Config
	sequence *[]string
}

func (f *fakeWireguard) Init() error {
	return nil
}

func (f *fakeWireguard) Apply(cfg wireguard.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.applyErr != nil {
		return f.applyErr
	}
	if f.sequence != nil {
		*f.sequence = append(*f.sequence, "apply")
	}
	f.applied = append(f.applied, cfg)
	return nil
}

func (f *fakeWireguard) Close() error {
	return nil
}

type fakeDNS struct {
	mu        sync.Mutex
	updates   []map[string][]string
	updateErr error
	sequence  *[]string
}

func (f *fakeDNS) Start() {}

func (f *fakeDNS) Stop() {}

func (f *fakeDNS) UpdateRecords(records map[string][]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sequence != nil {
		*f.sequence = append(*f.sequence, "dns")
	}
	copied := make(map[string][]string, len(records))
	for key, values := range records {
		valueCopy := make([]string, len(values))
		copy(valueCopy, values)
		copied[key] = valueCopy
	}
	f.updates = append(f.updates, copied)
	return f.updateErr
}

func newTestMetrics() *Metrics {
	return &Metrics{
		SyncOperations: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "test_sync_operations_total",
				Help: "mesh sync operations",
			},
			[]string{"status"},
		),
		PeersConnected: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "test_peers_connected",
				Help: "connected peers",
			},
			[]string{},
		),
		MeshApplyDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "test_mesh_apply_duration_seconds",
				Help:    "apply duration",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"layer"},
		),
		MeshApplyFailures: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "test_mesh_apply_failures_total",
				Help: "apply failures by reason",
			},
			[]string{"layer", "reason"},
		),
		MeshPeerCount: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "test_mesh_peer_count",
				Help: "peer count by layer",
			},
			[]string{"layer"},
		),
	}
}

func newTestAgent(t *testing.T, client *fakeMeshClient, wg *fakeWireguard, dns *fakeDNS) *Agent {
	t.Helper()
	privateKeyFile, _ := writeTestPrivateKey(t)
	return &Agent{
		logger:         logging.NewLogger(),
		client:         client,
		wgManager:      wg,
		dnsServer:      dns,
		nodeID:         "node-1",
		nodeName:       "node-1",
		privateKeyFile: privateKeyFile,
		wireguardIP:    "10.0.0.10",
		listenPort:     51820,
		syncTimeout:    2 * time.Second,
		syncInterval:   2 * time.Second,
		stopChan:       make(chan struct{}),
	}
}

// mustGenPubB64 returns a freshly generated wireguard public key in
// base64 form, suitable for use as a proto/JSON peer public_key field.
func mustGenPubB64(t *testing.T) string {
	t.Helper()
	priv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	return priv.PublicKey().String()
}

// genPrivateKeyB64 returns a freshly generated wireguard private key as
// base64 plus its derived public key. Used for tests that need to write
// a key file to disk and assert what comes back through the agent.
func genPrivateKeyB64(t *testing.T) (priv string, pub string) {
	t.Helper()
	k, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	return k.String(), k.PublicKey().String()
}

func writeTestPrivateKey(t *testing.T) (string, string) {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	raw[0] &= 248
	raw[31] &= 127
	raw[31] |= 64
	privateKey := base64.StdEncoding.EncodeToString(raw)
	publicKey, err := pkgmesh.DerivePublicKey(privateKey)
	if err != nil {
		t.Fatalf("derive test public key: %v", err)
	}
	path := filepath.Join(t.TempDir(), "wg.key")
	if err := os.WriteFile(path, []byte(privateKey+"\n"), 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}
	return path, publicKey
}

func TestIsHealthyNotStarted(t *testing.T) {
	agent := &Agent{}
	if agent.IsHealthy() {
		t.Fatal("expected unhealthy when not started")
	}
}

func TestIsHealthyTooManyFailures(t *testing.T) {
	agent := &Agent{}
	agent.healthy.Store(true)
	agent.consecutiveFails.Store(4)
	if agent.IsHealthy() {
		t.Fatal("expected unhealthy when consecutiveFails > 3")
	}
}

func TestIsHealthyBoundary(t *testing.T) {
	agent := &Agent{}
	agent.healthy.Store(true)
	agent.consecutiveFails.Store(3)
	if !agent.IsHealthy() {
		t.Fatal("expected healthy when consecutiveFails == 3 (threshold)")
	}
}

func TestIsHealthyStaleSync(t *testing.T) {
	agent := &Agent{}
	agent.healthy.Store(true)
	agent.consecutiveFails.Store(0)
	agent.lastSyncSuccess.Store(time.Now().Unix() - 301)
	if agent.IsHealthy() {
		t.Fatal("expected unhealthy when last sync is older than 5 minutes")
	}
}

func TestIsHealthyRecentSync(t *testing.T) {
	agent := &Agent{}
	agent.healthy.Store(true)
	agent.consecutiveFails.Store(0)
	agent.lastSyncSuccess.Store(time.Now().Unix() - 60)
	if !agent.IsHealthy() {
		t.Fatal("expected healthy when last sync was 60 seconds ago")
	}
}

func TestMissingInternalPKIMaterials(t *testing.T) {
	dir := t.TempDir()
	agent := &Agent{pkiBasePath: dir}

	if !agent.missingInternalPKIMaterials(nil) {
		t.Fatal("expected missing CA bundle to require sync")
	}

	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("ca"), 0o644); err != nil {
		t.Fatalf("write ca bundle: %v", err)
	}
	if agent.missingInternalPKIMaterials(nil) {
		t.Fatal("did not expect sync when CA bundle exists and no services are registered")
	}

	serviceDir := filepath.Join(dir, "services", "commodore")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatalf("mkdir service dir: %v", err)
	}
	if !agent.missingInternalPKIMaterials([]string{"commodore"}) {
		t.Fatal("expected missing leaf certs to require sync")
	}
	if err := os.WriteFile(filepath.Join(serviceDir, "tls.crt"), []byte("cert"), 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(filepath.Join(serviceDir, "tls.key"), []byte("key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if agent.missingInternalPKIMaterials([]string{"commodore"}) {
		t.Fatal("did not expect sync when CA bundle and leaf certs exist")
	}
}

func TestSyncInternalCertificatesBypassesCooldownWhenFilesMissing(t *testing.T) {
	dir := t.TempDir()
	registry := &fakeServiceRegistryClient{
		responses: []*pb.ListServiceInstancesResponse{
			{Instances: []*pb.ServiceInstance{{ServiceId: "commodore", Status: "running"}}},
			{Instances: []*pb.ServiceInstance{{ServiceId: "commodore", Status: "running"}}},
		},
	}
	navigator := &fakeCertificateClient{
		caResponse: &pb.GetCABundleResponse{
			Found: true,
			CaPem: "ca-pem",
		},
		issueResponse: &pb.IssueInternalCertResponse{
			Success: true,
			CertPem: "cert-pem",
			KeyPem:  "key-pem",
		},
	}
	agent := &Agent{
		logger:           logging.NewLogger(),
		nodeID:           "node-1",
		registryClient:   registry,
		navigatorClient:  navigator,
		certIssueToken:   "token",
		pkiBasePath:      dir,
		syncTimeout:      time.Second,
		certSyncInterval: 5 * time.Minute,
	}
	agent.lastCertSyncUnix.Store(time.Now().Unix())

	if err := agent.syncInternalCertificates(); err != nil {
		t.Fatalf("syncInternalCertificates returned error: %v", err)
	}

	if len(navigator.issueRequests) != 1 {
		t.Fatalf("expected 1 issue request, got %d", len(navigator.issueRequests))
	}
	if _, err := os.Stat(filepath.Join(dir, "services", "commodore", "tls.crt")); err != nil {
		t.Fatalf("expected leaf cert to be written: %v", err)
	}

	if err := agent.syncInternalCertificates(); err != nil {
		t.Fatalf("second syncInternalCertificates returned error: %v", err)
	}
	if len(navigator.issueRequests) != 1 {
		t.Fatalf("expected cooldown to skip second issuance, got %d requests", len(navigator.issueRequests))
	}
}

func TestSyncInternalCertificatesUsesExpectedServicesWithoutRegistryLookup(t *testing.T) {
	dir := t.TempDir()
	navigator := &fakeCertificateClient{
		caResponse: &pb.GetCABundleResponse{
			Found: true,
			CaPem: "ca-pem",
		},
		issueResponse: &pb.IssueInternalCertResponse{
			Success: true,
			CertPem: "cert-pem",
			KeyPem:  "key-pem",
		},
	}
	agent := &Agent{
		logger:           logging.NewLogger(),
		nodeID:           "node-1",
		navigatorClient:  navigator,
		certIssueToken:   "token",
		pkiBasePath:      dir,
		syncTimeout:      time.Second,
		certSyncInterval: 5 * time.Minute,
		expectedServices: []string{"commodore", "signalman"},
	}

	if err := agent.syncInternalCertificates(); err != nil {
		t.Fatalf("syncInternalCertificates returned error: %v", err)
	}
	if len(navigator.issueRequests) != 2 {
		t.Fatalf("expected 2 issue requests, got %d", len(navigator.issueRequests))
	}
	if navigator.issueRequests[0].GetServiceType() != "commodore" || navigator.issueRequests[1].GetServiceType() != "signalman" {
		t.Fatalf("unexpected service issue order: %+v", navigator.issueRequests)
	}
}

func TestSyncInternalCertificatesMintsTokenWhenMissing(t *testing.T) {
	dir := t.TempDir()
	navigator := &fakeCertificateClient{
		caResponse: &pb.GetCABundleResponse{
			Found: true,
			CaPem: "ca-pem",
		},
		issueResponse: &pb.IssueInternalCertResponse{
			Success: true,
			CertPem: "cert-pem",
			KeyPem:  "key-pem",
		},
	}
	mesh := &fakeMeshClient{
		createTokenResponses: []meshCreateTokenResult{{
			resp: &pb.CreateBootstrapTokenResponse{
				Token: &pb.BootstrapToken{Token: "bt_cert_sync"},
			},
		}},
	}
	agent := &Agent{
		logger:           logging.NewLogger(),
		client:           mesh,
		nodeID:           "node-1",
		clusterID:        "cluster-a",
		navigatorClient:  navigator,
		pkiBasePath:      dir,
		syncTimeout:      time.Second,
		certSyncInterval: 5 * time.Minute,
		expectedServices: []string{"commodore"},
	}

	if err := agent.syncInternalCertificates(); err != nil {
		t.Fatalf("syncInternalCertificates returned error: %v", err)
	}
	if agent.certIssueToken != "bt_cert_sync" {
		t.Fatalf("expected cached cert token bt_cert_sync, got %q", agent.certIssueToken)
	}
	if len(mesh.createTokenRequests) != 1 {
		t.Fatalf("expected 1 create-token request, got %d", len(mesh.createTokenRequests))
	}
	if got := mesh.createTokenRequests[0].GetClusterId(); got != "cluster-a" {
		t.Fatalf("expected token cluster_id cluster-a, got %q", got)
	}
	if len(navigator.issueRequests) != 1 || navigator.issueRequests[0].GetIssueToken() != "bt_cert_sync" {
		t.Fatalf("expected issued cert to use minted token, got %+v", navigator.issueRequests)
	}
}

func TestAgentSyncRegistersNodeOnNotFound(t *testing.T) {
	logger := logging.NewLogger()
	nodeIDPath := filepath.Join(t.TempDir(), "node_id")
	if err := os.WriteFile(nodeIDPath, []byte("node-old"), 0600); err != nil {
		t.Fatalf("write node id: %v", err)
	}

	peerKey := mustGenPubB64(t)
	mesh := &fakeMeshClient{
		syncResponses: []meshSyncResult{
			{err: status.Error(codes.NotFound, "missing")},
			{resp: &pb.InfrastructureSyncResponse{
				WireguardIp:   "10.0.0.8",
				WireguardPort: 51820,
				Peers: []*pb.InfrastructurePeer{
					{
						NodeName:   "node-a",
						PublicKey:  peerKey,
						Endpoint:   "1.2.3.4:51820",
						AllowedIps: []string{"10.0.0.3/32"},
						KeepAlive:  25,
					},
				},
				ServiceEndpoints: map[string]*pb.ServiceEndpoints{
					"api": {Ips: []string{"10.0.0.9"}},
				},
			}},
		},
		createNodeResponses: []meshCreateNodeResult{
			{resp: &pb.NodeResponse{}},
		},
	}

	wg := &fakeWireguard{pubKey: "pub-self", privKey: "priv-self"}
	dnsService := &fakeDNS{}
	privateKeyFile, publicKey := writeTestPrivateKey(t)

	agent, err := New(Config{
		NodeIDPath:       nodeIDPath,
		NodeName:         "privateer-1",
		NodeType:         "edge",
		ClusterID:        "cluster-1",
		WireguardIP:      "10.0.0.8",
		PrivateKeyFile:   privateKeyFile,
		ExternalIP:       "203.0.113.20",
		Logger:           logger,
		MeshClient:       mesh,
		WireGuardManager: wg,
		DNSService:       dnsService,
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	agent.sync()

	if len(mesh.createNodeRequests) != 1 {
		t.Fatalf("expected create-node request, got %d", len(mesh.createNodeRequests))
	}
	if got := mesh.createNodeRequests[0].GetNodeId(); got != "node-old" {
		t.Fatalf("expected create-node node_id node-old, got %q", got)
	}
	if got := mesh.createNodeRequests[0].GetClusterId(); got != "cluster-1" {
		t.Fatalf("expected create-node cluster_id cluster-1, got %q", got)
	}
	if got := mesh.createNodeRequests[0].GetWireguardPublicKey(); got != publicKey {
		t.Fatalf("expected create-node public key from configured private key, got %q", got)
	}
	if len(mesh.syncRequests) != 2 {
		t.Fatalf("expected two sync requests, got %d", len(mesh.syncRequests))
	}
	if mesh.syncRequests[1].NodeId != "node-old" {
		t.Fatalf("expected retry sync to use node-old, got %s", mesh.syncRequests[1].NodeId)
	}
	if len(wg.applied) != 1 {
		t.Fatalf("expected wireguard apply once, got %d", len(wg.applied))
	}
	if got := wg.applied[0].Address.String(); got != "10.0.0.8/32" {
		t.Fatalf("unexpected wireguard address %s", got)
	}
	if len(dnsService.updates) != 1 {
		t.Fatalf("expected dns update once, got %d", len(dnsService.updates))
	}
	if got := dnsService.updates[0]["node-a"][0]; got != "10.0.0.3" {
		t.Fatalf("expected dns record for node-a, got %s", got)
	}
	if got := dnsService.updates[0]["api"][0]; got != "10.0.0.9" {
		t.Fatalf("expected dns record for api, got %s", got)
	}
}

func TestAgentSyncReconcilesNodeOnFailedPrecondition(t *testing.T) {
	logger := logging.NewLogger()
	nodeIDPath := filepath.Join(t.TempDir(), "node_id")
	if err := os.WriteFile(nodeIDPath, []byte("node-stale"), 0600); err != nil {
		t.Fatalf("write node id: %v", err)
	}

	mesh := &fakeMeshClient{
		syncResponses: []meshSyncResult{
			{err: status.Error(codes.FailedPrecondition, "wireguard identity mismatch")},
			{resp: &pb.InfrastructureSyncResponse{
				WireguardIp:   "10.0.0.8",
				WireguardPort: 51820,
			}},
		},
		createNodeResponses: []meshCreateNodeResult{
			{resp: &pb.NodeResponse{}},
		},
	}

	wg := &fakeWireguard{}
	privateKeyFile, publicKey := writeTestPrivateKey(t)

	agent, err := New(Config{
		NodeIDPath:       nodeIDPath,
		NodeName:         "privateer-1",
		NodeType:         "edge",
		ClusterID:        "cluster-1",
		WireguardIP:      "10.0.0.8",
		PrivateKeyFile:   privateKeyFile,
		Logger:           logger,
		MeshClient:       mesh,
		WireGuardManager: wg,
		DNSService:       &fakeDNS{},
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	agent.sync()

	if len(mesh.createNodeRequests) != 1 {
		t.Fatalf("expected create-node request, got %d", len(mesh.createNodeRequests))
	}
	if got := mesh.createNodeRequests[0].GetNodeId(); got != "node-stale" {
		t.Fatalf("expected create-node node_id node-stale, got %q", got)
	}
	if got := mesh.createNodeRequests[0].GetWireguardPublicKey(); got != publicKey {
		t.Fatalf("expected create-node public key from configured private key, got %q", got)
	}
	if len(mesh.syncRequests) != 2 {
		t.Fatalf("expected retry sync, got %d sync requests", len(mesh.syncRequests))
	}
	if len(wg.applied) != 1 {
		t.Fatalf("expected wireguard apply after reconciliation, got %d", len(wg.applied))
	}
}

func TestAgentSyncRegistrationFailureKeepsExistingMesh(t *testing.T) {
	logger := logging.NewLogger()
	nodeIDPath := filepath.Join(t.TempDir(), "node_id")
	if err := os.WriteFile(nodeIDPath, []byte("node-x"), 0600); err != nil {
		t.Fatalf("write node id: %v", err)
	}

	mesh := &fakeMeshClient{
		syncResponses: []meshSyncResult{
			{err: status.Error(codes.NotFound, "missing")},
		},
		createNodeResponses: []meshCreateNodeResult{
			{err: status.Error(codes.PermissionDenied, "denied")},
		},
	}

	wg := &fakeWireguard{pubKey: "pub-self", privKey: "priv-self"}
	dnsService := &fakeDNS{}
	privateKeyFile, _ := writeTestPrivateKey(t)

	agent, err := New(Config{
		NodeIDPath:       nodeIDPath,
		NodeName:         "privateer-2",
		ClusterID:        "cluster-1",
		WireguardIP:      "10.0.0.10",
		PrivateKeyFile:   privateKeyFile,
		Logger:           logger,
		MeshClient:       mesh,
		WireGuardManager: wg,
		DNSService:       dnsService,
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	agent.sync()

	if len(mesh.syncRequests) != 1 {
		t.Fatalf("expected one sync request, got %d", len(mesh.syncRequests))
	}
	if len(mesh.createNodeRequests) != 1 {
		t.Fatalf("expected one create-node request, got %d", len(mesh.createNodeRequests))
	}
	if len(wg.applied) != 0 {
		t.Fatalf("expected no wireguard apply, got %d", len(wg.applied))
	}
}

func TestAgentSyncRevokedWithoutToken(t *testing.T) {
	logger := logging.NewLogger()
	nodeIDPath := filepath.Join(t.TempDir(), "node_id")
	if err := os.WriteFile(nodeIDPath, []byte("node-y"), 0600); err != nil {
		t.Fatalf("write node id: %v", err)
	}

	mesh := &fakeMeshClient{
		syncResponses: []meshSyncResult{
			{err: status.Error(codes.NotFound, "revoked")},
		},
	}

	wg := &fakeWireguard{pubKey: "pub-self", privKey: "priv-self"}
	dnsService := &fakeDNS{}
	privateKeyFile, _ := writeTestPrivateKey(t)

	agent, err := New(Config{
		NodeIDPath:       nodeIDPath,
		NodeName:         "privateer-3",
		WireguardIP:      "10.0.0.10",
		PrivateKeyFile:   privateKeyFile,
		Logger:           logger,
		MeshClient:       mesh,
		WireGuardManager: wg,
		DNSService:       dnsService,
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	agent.sync()

	if len(mesh.createNodeRequests) != 0 {
		t.Fatalf("expected no create-node request, got %d", len(mesh.createNodeRequests))
	}
	if len(wg.applied) != 0 {
		t.Fatalf("expected no wireguard apply, got %d", len(wg.applied))
	}
	if got := agent.consecutiveFails.Load(); got != 1 {
		t.Fatalf("expected consecutiveFails 1, got %d", got)
	}
}

func TestAgentSyncNotFoundKeepsExistingMeshWhenRegistrationCannotRun(t *testing.T) {
	peerKey := mustGenPubB64(t)
	client := &fakeMeshClient{
		syncResponses: []meshSyncResult{
			{resp: &pb.InfrastructureSyncResponse{
				WireguardIp:   "10.0.0.10",
				WireguardPort: 51820,
				Peers: []*pb.InfrastructurePeer{{
					PublicKey:  peerKey,
					Endpoint:   "1.2.3.4:51820",
					AllowedIps: []string{"10.0.0.2/32"},
					KeepAlive:  25,
					NodeName:   "peer-one",
				}},
				ServiceEndpoints: map[string]*pb.ServiceEndpoints{
					"metrics": {Ips: []string{"10.0.0.5"}},
				},
			}},
			{err: status.Error(codes.NotFound, "revoked")},
		},
	}
	wg := &fakeWireguard{pubKey: "pub", privKey: "priv"}
	dns := &fakeDNS{}
	agent := newTestAgent(t, client, wg, dns)

	agent.sync()
	if len(dns.updates) != 1 || len(dns.updates[0]) == 0 {
		t.Fatalf("expected initial dns update to contain records, got %+v", dns.updates)
	}
	if agent.getLastAppliedConfig() == nil {
		t.Fatal("expected initial config to be stored")
	}

	agent.sync()

	if len(wg.applied) != 1 {
		t.Fatalf("expected no clearing apply on not found, got %d applies", len(wg.applied))
	}
	if len(dns.updates) != 1 {
		t.Fatalf("expected DNS records to remain intact, got %d updates", len(dns.updates))
	}
	if agent.getLastAppliedConfig() == nil {
		t.Fatal("expected last applied config to remain after node not found")
	}
}

func TestLoadOrGenerateNodeID(t *testing.T) {
	logger := logging.NewLogger()
	root := t.TempDir()

	t.Run("load existing", func(t *testing.T) {
		nodeIDPath := filepath.Join(root, "existing")
		if err := os.WriteFile(nodeIDPath, []byte("node-existing"), 0600); err != nil {
			t.Fatalf("write node id: %v", err)
		}

		got := loadOrGenerateNodeID(nodeIDPath, logger)
		if got != "node-existing" {
			t.Fatalf("expected node-existing, got %s", got)
		}
	})

	t.Run("generate new", func(t *testing.T) {
		nodeIDPath := filepath.Join(root, "generated", "node_id")
		got := loadOrGenerateNodeID(nodeIDPath, logger)
		if got == "" {
			t.Fatal("expected generated node id")
		}
		data, err := os.ReadFile(nodeIDPath)
		if err != nil {
			t.Fatalf("read node id: %v", err)
		}
		if string(data) != got {
			t.Fatalf("expected node id persisted, got %s", string(data))
		}
	})
}

func TestAgentSyncRetryDoesNotApplyOnFailure(t *testing.T) {
	logger := logging.NewLogger()
	nodeIDPath := filepath.Join(t.TempDir(), "node_id")
	if err := os.WriteFile(nodeIDPath, []byte("node-retry"), 0600); err != nil {
		t.Fatalf("write node id: %v", err)
	}

	mesh := &fakeMeshClient{
		syncResponses: []meshSyncResult{
			{err: status.Error(codes.NotFound, "missing")},
			{err: status.Error(codes.Unavailable, "temporary")},
		},
		createNodeResponses: []meshCreateNodeResult{
			{resp: &pb.NodeResponse{}},
		},
	}

	wg := &fakeWireguard{pubKey: "pub-self", privKey: "priv-self"}
	dnsService := &fakeDNS{}
	privateKeyFile, _ := writeTestPrivateKey(t)

	agent, err := New(Config{
		NodeIDPath:       nodeIDPath,
		NodeName:         "privateer-4",
		ClusterID:        "cluster-1",
		WireguardIP:      "10.0.0.10",
		PrivateKeyFile:   privateKeyFile,
		Logger:           logger,
		MeshClient:       mesh,
		WireGuardManager: wg,
		DNSService:       dnsService,
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	agent.sync()

	if len(wg.applied) != 0 {
		t.Fatalf("expected no wireguard apply on retry failure, got %d", len(wg.applied))
	}
	if got := agent.consecutiveFails.Load(); got != 1 {
		t.Fatalf("expected consecutiveFails 1, got %d", got)
	}
}

func TestSyncAppliesConfigBeforeDNSUpdate(t *testing.T) {
	peerKey := mustGenPubB64(t)
	resp := &pb.InfrastructureSyncResponse{
		WireguardIp:   "10.0.0.10",
		WireguardPort: 51820,
		Peers: []*pb.InfrastructurePeer{
			{
				PublicKey:  peerKey,
				Endpoint:   "10.0.0.1:51820",
				AllowedIps: []string{"10.0.0.2/32"},
				KeepAlive:  25,
				NodeName:   "edge-1",
			},
		},
		ServiceEndpoints: map[string]*pb.ServiceEndpoints{
			"router": {Ips: []string{"10.0.0.10"}},
		},
	}

	client := &fakeMeshClient{syncResponses: []meshSyncResult{{resp: resp}}}
	sequence := make([]string, 0, 2)
	wg := &fakeWireguard{
		pubKey:   "public-key",
		privKey:  "private-key",
		sequence: &sequence,
	}
	dns := &fakeDNS{sequence: &sequence}
	agent := newTestAgent(t, client, wg, dns)

	agent.sync()

	if !reflect.DeepEqual(sequence, []string{"apply", "dns"}) {
		t.Fatalf("expected apply then dns sequence, got %v", sequence)
	}
	if len(wg.applied) != 1 {
		t.Fatalf("expected one apply call, got %d", len(wg.applied))
	}
	if len(dns.updates) != 1 {
		t.Fatalf("expected one dns update, got %d", len(dns.updates))
	}

	dnsRecords := dns.updates[0]
	expectedIP := "10.0.0.2"
	if got := dnsRecords["edge-1"]; len(got) != 1 || got[0] != expectedIP {
		t.Fatalf("expected dns record for edge-1 to be %s, got %v", expectedIP, got)
	}
}

func TestSyncRejectsManagedSelfIdentityMismatch(t *testing.T) {
	client := &fakeMeshClient{syncResponses: []meshSyncResult{{
		resp: &pb.InfrastructureSyncResponse{
			WireguardIp:   "10.0.0.99",
			WireguardPort: 51820,
		},
	}}}
	wg := &fakeWireguard{}
	dns := &fakeDNS{}
	agent := newTestAgent(t, client, wg, dns)

	agent.sync()

	if len(wg.applied) != 0 {
		t.Fatalf("expected no wireguard apply on self identity mismatch, got %d", len(wg.applied))
	}
	if len(dns.updates) != 0 {
		t.Fatalf("expected no dns update on self identity mismatch, got %d", len(dns.updates))
	}
	if got := agent.consecutiveFails.Load(); got != 1 {
		t.Fatalf("expected consecutive failure count 1, got %d", got)
	}
}

func TestSyncRollsBackWireGuardOnDNSFailure(t *testing.T) {
	peer1Key := mustGenPubB64(t)
	peer2Key := mustGenPubB64(t)
	initialResp := &pb.InfrastructureSyncResponse{
		WireguardIp:   "10.0.0.10",
		WireguardPort: 51820,
		Peers: []*pb.InfrastructurePeer{
			{
				PublicKey:  peer1Key,
				Endpoint:   "10.0.0.1:51820",
				AllowedIps: []string{"10.0.0.2/32"},
				KeepAlive:  25,
				NodeName:   "edge-1",
			},
		},
	}
	rotatedResp := &pb.InfrastructureSyncResponse{
		WireguardIp:   "10.0.0.10",
		WireguardPort: 51820,
		Peers: []*pb.InfrastructurePeer{
			{
				PublicKey:  peer2Key,
				Endpoint:   "10.0.0.2:51820",
				AllowedIps: []string{"10.0.0.3/32"},
				KeepAlive:  25,
				NodeName:   "edge-2",
			},
		},
	}

	client := &fakeMeshClient{syncResponses: []meshSyncResult{
		{resp: initialResp},
		{resp: rotatedResp},
	}}
	wg := &fakeWireguard{
		pubKey:  "public-key",
		privKey: "private-key",
	}
	dns := &fakeDNS{}
	agent := newTestAgent(t, client, wg, dns)

	agent.sync()

	dns.updateErr = errors.New("dns update failed")
	agent.sync()

	if len(wg.applied) != 3 {
		t.Fatalf("expected three apply calls (initial, rotated, rollback), got %d", len(wg.applied))
	}

	rolledBack := wg.applied[2]
	expected := wg.applied[0]
	if !reflect.DeepEqual(rolledBack, expected) {
		t.Fatalf("expected rollback to apply previous config, got %+v vs %+v", rolledBack, expected)
	}

	lastCfg := agent.getLastAppliedConfig()
	if lastCfg == nil || !reflect.DeepEqual(*lastCfg, expected) {
		t.Fatalf("expected last applied config to remain initial config, got %+v", lastCfg)
	}

	if agent.consecutiveFails.Load() != 1 {
		t.Fatalf("expected consecutive failure count to be 1, got %d", agent.consecutiveFails.Load())
	}
}

func TestApplyStaticIncludesSeedDNS(t *testing.T) {
	keyPath, _ := writeTestPrivateKey(t)
	wg := &fakeWireguard{}
	dns := &fakeDNS{}
	agent := &Agent{
		logger:         logging.NewLogger(),
		wgManager:      wg,
		dnsServer:      dns,
		privateKeyFile: keyPath,
		wireguardIP:    "10.88.0.2",
		listenPort:     51820,
		lastKnownPath:  filepath.Join(t.TempDir(), "last_known.json"),
	}

	peerKey := mustGenPubB64(t)
	agent.applyStatic(&staticPeersFile{
		Version: "seed-v1",
		Peers: []staticPeer{{
			Name:       "core-2",
			PublicKey:  peerKey,
			AllowedIPs: []string{"10.88.0.3/32"},
			Endpoint:   "203.0.113.3:51820",
			KeepAlive:  25,
		}},
		DNS: map[string][]string{
			"quartermaster": {"10.88.0.2"},
			"commodore":     {"10.88.0.4"},
		},
	})

	if len(dns.updates) != 1 {
		t.Fatalf("expected one DNS update, got %d", len(dns.updates))
	}
	if got := dns.updates[0]["quartermaster"]; len(got) != 1 || got[0] != "10.88.0.2" {
		t.Fatalf("expected seed DNS for quartermaster, got %v", got)
	}
	if got := dns.updates[0]["core-2"]; len(got) != 1 || got[0] != "10.88.0.3" {
		t.Fatalf("expected peer DNS for core-2, got %v", got)
	}
}

func TestAgentSyncTimeoutRecordsFailure(t *testing.T) {
	client := &fakeMeshClient{
		syncResponses: []meshSyncResult{
			{err: context.DeadlineExceeded},
		},
	}
	wg := &fakeWireguard{pubKey: "pub", privKey: "priv"}
	dns := &fakeDNS{}
	metrics := newTestMetrics()
	agent := newTestAgent(t, client, wg, dns)
	agent.metrics = metrics

	agent.sync()

	if got := testutil.ToFloat64(metrics.SyncOperations.WithLabelValues("failed")); got != 1 {
		t.Fatalf("expected failed sync counter to be 1, got %v", got)
	}
	if agent.consecutiveFails.Load() != 1 {
		t.Fatalf("expected consecutive failures to be 1, got %d", agent.consecutiveFails.Load())
	}
	if agent.lastSyncSuccess.Load() != 0 {
		t.Fatalf("expected last sync success to be 0, got %d", agent.lastSyncSuccess.Load())
	}
}

func TestAgentSyncReconcilesStaleState(t *testing.T) {
	peerKey := mustGenPubB64(t)
	client := &fakeMeshClient{
		syncResponses: []meshSyncResult{
			{resp: &pb.InfrastructureSyncResponse{
				WireguardIp:   "10.0.0.10",
				WireguardPort: 51820,
				Peers: []*pb.InfrastructurePeer{
					{
						PublicKey:  peerKey,
						Endpoint:   "1.2.3.4:51820",
						AllowedIps: []string{"10.0.0.2/32"},
						KeepAlive:  25,
						NodeName:   "peer-one",
					},
				},
				ServiceEndpoints: map[string]*pb.ServiceEndpoints{
					"metrics": {Ips: []string{"10.0.0.5"}},
				},
			}},
			{resp: &pb.InfrastructureSyncResponse{
				WireguardIp:   "10.0.0.10",
				WireguardPort: 51820,
			}},
		},
	}
	wg := &fakeWireguard{pubKey: "pub", privKey: "priv"}
	dns := &fakeDNS{}
	metrics := newTestMetrics()
	agent := newTestAgent(t, client, wg, dns)
	agent.metrics = metrics

	agent.sync()
	agent.sync()

	if len(dns.updates) != 2 {
		t.Fatalf("expected dns records updated twice, got %d", len(dns.updates))
	}
	if len(dns.updates[1]) != 0 {
		t.Fatalf("expected second dns update to clear records, got %v", dns.updates[1])
	}
	if got := testutil.ToFloat64(metrics.PeersConnected.WithLabelValues()); got != 0 {
		t.Fatalf("expected peers connected to be 0 after reconciliation, got %v", got)
	}
	if len(wg.applied) != 2 {
		t.Fatalf("expected wireguard apply to be called twice, got %d", len(wg.applied))
	}
	if len(wg.applied[1].Peers) != 0 {
		t.Fatalf("expected wireguard config peers to be empty after reconciliation, got %v", wg.applied[1].Peers)
	}
}

func TestAgentSyncApplyFailurePropagates(t *testing.T) {
	client := &fakeMeshClient{
		syncResponses: []meshSyncResult{
			{resp: &pb.InfrastructureSyncResponse{
				WireguardIp:   "10.0.0.10",
				WireguardPort: 51820,
			}},
		},
	}
	wg := &fakeWireguard{pubKey: "pub", privKey: "priv", applyErr: errors.New("apply failed")}
	dns := &fakeDNS{}
	metrics := newTestMetrics()
	agent := newTestAgent(t, client, wg, dns)
	agent.metrics = metrics

	agent.sync()

	if got := testutil.ToFloat64(metrics.SyncOperations.WithLabelValues("failed")); got != 1 {
		t.Fatalf("expected failed sync counter to be 1, got %v", got)
	}
	if got := testutil.ToFloat64(metrics.MeshApplyFailures.WithLabelValues("managed", "configure")); got != 1 {
		t.Fatalf("expected mesh_apply_failures{managed,configure} = 1, got %v", got)
	}
}

func TestAgentSyncSuccessRecordsApplyMetrics(t *testing.T) {
	peerKey := mustGenPubB64(t)
	client := &fakeMeshClient{
		syncResponses: []meshSyncResult{
			{resp: &pb.InfrastructureSyncResponse{
				WireguardIp:   "10.0.0.10",
				WireguardPort: 51820,
				Peers: []*pb.InfrastructurePeer{{
					PublicKey:  peerKey,
					Endpoint:   "1.2.3.4:51820",
					AllowedIps: []string{"10.0.0.3/32"},
					KeepAlive:  25,
					NodeName:   "peer-one",
				}},
			}},
		},
	}
	wg := &fakeWireguard{}
	dns := &fakeDNS{}
	metrics := newTestMetrics()
	agent := newTestAgent(t, client, wg, dns)
	agent.metrics = metrics

	agent.sync()

	if got := testutil.CollectAndCount(metrics.MeshApplyDuration); got != 1 {
		t.Fatalf("expected one MeshApplyDuration sample, got %d", got)
	}
	if got := testutil.ToFloat64(metrics.MeshPeerCount.WithLabelValues("managed")); got != 1 {
		t.Fatalf("expected mesh_peer_count{managed} = 1, got %v", got)
	}
	if got := testutil.CollectAndCount(metrics.MeshApplyFailures); got != 0 {
		t.Fatalf("expected no apply failures on success path, got %d", got)
	}
}

func TestAgentSyncDNSFailureRecordsDNSReasonAndDoesNotUpdatePeerCount(t *testing.T) {
	peerKey := mustGenPubB64(t)
	client := &fakeMeshClient{
		syncResponses: []meshSyncResult{
			{resp: &pb.InfrastructureSyncResponse{
				WireguardIp:   "10.0.0.10",
				WireguardPort: 51820,
				Peers: []*pb.InfrastructurePeer{{
					PublicKey:  peerKey,
					Endpoint:   "1.2.3.4:51820",
					AllowedIps: []string{"10.0.0.3/32"},
					KeepAlive:  25,
					NodeName:   "peer-one",
				}},
			}},
		},
	}
	wg := &fakeWireguard{}
	dns := &fakeDNS{updateErr: errors.New("dns down")}
	metrics := newTestMetrics()
	agent := newTestAgent(t, client, wg, dns)
	agent.metrics = metrics

	agent.sync()

	// Apply itself ran and rolled back; duration is real, peer count is not.
	if got := testutil.CollectAndCount(metrics.MeshApplyDuration); got != 1 {
		t.Errorf("expected one MeshApplyDuration sample (Apply did run), got %d", got)
	}
	if got := testutil.ToFloat64(metrics.MeshPeerCount.WithLabelValues("managed")); got != 0 {
		t.Errorf("mesh_peer_count{managed} must remain 0 when DNS fails and config is rolled back; got %v", got)
	}
	if got := testutil.ToFloat64(metrics.MeshApplyFailures.WithLabelValues("managed", "dns")); got != 1 {
		t.Errorf("expected mesh_apply_failures{managed,dns} = 1, got %v", got)
	}
}

func TestAgentSyncMalformedPeerRecordsInvalidPeerReason(t *testing.T) {
	client := &fakeMeshClient{
		syncResponses: []meshSyncResult{
			{resp: &pb.InfrastructureSyncResponse{
				WireguardIp:   "10.0.0.10",
				WireguardPort: 51820,
				Peers: []*pb.InfrastructurePeer{{
					PublicKey:  "not-a-valid-key", // forces parsePeerStrings failure
					Endpoint:   "1.2.3.4:51820",
					AllowedIps: []string{"10.0.0.3/32"},
					KeepAlive:  25,
					NodeName:   "peer-broken",
				}},
			}},
		},
	}
	wg := &fakeWireguard{}
	dns := &fakeDNS{}
	metrics := newTestMetrics()
	agent := newTestAgent(t, client, wg, dns)
	agent.metrics = metrics

	agent.sync()

	if got := testutil.ToFloat64(metrics.MeshApplyFailures.WithLabelValues("managed", "invalid_peer")); got != 1 {
		t.Fatalf("expected mesh_apply_failures{managed,invalid_peer} = 1, got %v", got)
	}
	if len(wg.applied) != 0 {
		t.Errorf("malformed peer must not reach Apply, got %d applies", len(wg.applied))
	}
}

func TestNewDefaultsNodeTypeToCore(t *testing.T) {
	nodeIDPath := filepath.Join(t.TempDir(), "node_id")
	agent, err := New(Config{
		NodeIDPath:       nodeIDPath,
		NodeName:         "test-node",
		Logger:           logging.NewLogger(),
		MeshClient:       &fakeMeshClient{},
		WireGuardManager: &fakeWireguard{},
		DNSService:       &fakeDNS{},
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	if agent.nodeType != "core" {
		t.Fatalf("expected default node type 'core', got %q", agent.nodeType)
	}
}

func TestNewPreservesExplicitNodeType(t *testing.T) {
	nodeIDPath := filepath.Join(t.TempDir(), "node_id")
	agent, err := New(Config{
		NodeIDPath:       nodeIDPath,
		NodeName:         "test-node",
		NodeType:         "api",
		Logger:           logging.NewLogger(),
		MeshClient:       &fakeMeshClient{},
		WireGuardManager: &fakeWireguard{},
		DNSService:       &fakeDNS{},
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	if agent.nodeType != "api" {
		t.Fatalf("expected node type 'api', got %q", agent.nodeType)
	}
}

func TestRegisterNodeSendsNodeType(t *testing.T) {
	nodeIDPath := filepath.Join(t.TempDir(), "node_id")
	if err := os.WriteFile(nodeIDPath, []byte("node-typed"), 0600); err != nil {
		t.Fatalf("write node id: %v", err)
	}

	mesh := &fakeMeshClient{
		syncResponses: []meshSyncResult{
			{err: status.Error(codes.NotFound, "missing")},
			{resp: &pb.InfrastructureSyncResponse{
				WireguardIp:   "10.0.0.10",
				WireguardPort: 51820,
			}},
		},
		createNodeResponses: []meshCreateNodeResult{
			{resp: &pb.NodeResponse{}},
		},
	}
	wg := &fakeWireguard{pubKey: "pub", privKey: "priv"}
	dns := &fakeDNS{}
	privateKeyFile, _ := writeTestPrivateKey(t)

	agent, err := New(Config{
		NodeIDPath:       nodeIDPath,
		NodeName:         "typed-node",
		NodeType:         "core",
		ClusterID:        "cluster-1",
		WireguardIP:      "10.0.0.10",
		PrivateKeyFile:   privateKeyFile,
		Logger:           logging.NewLogger(),
		MeshClient:       mesh,
		WireGuardManager: wg,
		DNSService:       dns,
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	agent.sync()

	if len(mesh.createNodeRequests) != 1 {
		t.Fatalf("expected 1 create-node request, got %d", len(mesh.createNodeRequests))
	}
	if got := mesh.createNodeRequests[0].GetNodeType(); got != "core" {
		t.Fatalf("expected create-node node_type 'core', got %q", got)
	}
}

func TestAgentSyncRegistrationRetryFailureKeepsMeshState(t *testing.T) {
	logger := logging.NewLogger()
	nodeIDPath := filepath.Join(t.TempDir(), "node_id")
	if err := os.WriteFile(nodeIDPath, []byte("node-retry-clear"), 0600); err != nil {
		t.Fatalf("write node id: %v", err)
	}

	peerKey := mustGenPubB64(t)
	mesh := &fakeMeshClient{
		syncResponses: []meshSyncResult{
			{resp: &pb.InfrastructureSyncResponse{
				WireguardIp:   "10.0.0.10",
				WireguardPort: 51820,
				Peers: []*pb.InfrastructurePeer{{
					PublicKey:  peerKey,
					Endpoint:   "1.2.3.4:51820",
					AllowedIps: []string{"10.0.0.2/32"},
					KeepAlive:  25,
					NodeName:   "peer-one",
				}},
				ServiceEndpoints: map[string]*pb.ServiceEndpoints{
					"metrics": {Ips: []string{"10.0.0.5"}},
				},
			}},
			{err: status.Error(codes.NotFound, "missing")},
			{err: status.Error(codes.Unavailable, "still down")},
		},
		createNodeResponses: []meshCreateNodeResult{
			{resp: &pb.NodeResponse{}},
		},
	}

	wg := &fakeWireguard{pubKey: "pub-self", privKey: "priv-self"}
	dnsService := &fakeDNS{}
	privateKeyFile, _ := writeTestPrivateKey(t)

	agent, err := New(Config{
		NodeIDPath:       nodeIDPath,
		NodeName:         "privateer-bootstrap-clear",
		ClusterID:        "cluster-1",
		WireguardIP:      "10.0.0.10",
		PrivateKeyFile:   privateKeyFile,
		Logger:           logger,
		MeshClient:       mesh,
		WireGuardManager: wg,
		DNSService:       dnsService,
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	agent.sync()
	agent.sync()

	if len(wg.applied) != 1 {
		t.Fatalf("expected only the initial managed apply, got %d", len(wg.applied))
	}
	if len(dnsService.updates) != 1 {
		t.Fatalf("expected DNS to keep prior records, got %d updates", len(dnsService.updates))
	}
	if agent.getLastAppliedConfig() == nil {
		t.Fatal("expected last applied config to remain after registration retry failure")
	}
}

func TestApplyPersistedMeshIdentityWins(t *testing.T) {
	// Writing a stale last_known with a different self IP must NOT resurrect
	// it: the agent's own configured identity is authoritative.
	tmp := t.TempDir()
	keyPath := filepath.Join(tmp, "wg.key")
	privB64, _ := genPrivateKeyB64(t)
	if err := os.WriteFile(keyPath, []byte(privB64+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	peerKey := mustGenPubB64(t)
	lastKnownPath := filepath.Join(tmp, "last_known.json")
	if err := writeLastKnown(lastKnownPath, &lastKnownMesh{
		Source:      "dynamic",
		Version:     "v1",
		WireguardIP: "10.88.0.99", // stale / rotated away
		ListenPort:  99999,
		Peers:       []lastKnownPeer{{PublicKey: peerKey, Endpoint: "1.1.1.1:51820", AllowedIPs: []string{"10.88.0.3/32"}, KeepAlive: 25}},
	}); err != nil {
		t.Fatal(err)
	}

	wg := &fakeWireguard{}
	agent := &Agent{
		logger:          logging.NewLogger(),
		wgManager:       wg,
		dnsServer:       &fakeDNS{},
		wireguardIP:     "10.88.0.2", // the real current identity
		listenPort:      51820,
		privateKeyFile:  keyPath,
		lastKnownPath:   lastKnownPath,
		staticPeersFile: "",
	}

	agent.applyStartupMesh()

	if len(wg.applied) != 1 {
		t.Fatalf("expected exactly one wgManager.Apply call, got %d", len(wg.applied))
	}
	got := wg.applied[0]
	if addr := got.Address.String(); addr != "10.88.0.2/32" {
		t.Errorf("wg0 address = %q, want 10.88.0.2/32 (identity layer wins over stale last_known 10.88.0.99)", addr)
	}
	if got.ListenPort != 51820 {
		t.Errorf("wg0 listen port = %d, want 51820 (not stale 99999)", got.ListenPort)
	}
	if got.PrivateKey.String() != privB64 {
		t.Errorf("private key = %q, want from wg.key file", got.PrivateKey.String())
	}
	if len(got.Peers) != 1 || got.Peers[0].PublicKey.String() != peerKey {
		t.Errorf("peers should come from last_known snapshot, got %+v", got.Peers)
	}
}

func TestApplyStartupMeshWithEmptySeedStillConfiguresSelfAddress(t *testing.T) {
	tmp := t.TempDir()
	keyPath := filepath.Join(tmp, "wg.key")
	privB64, _ := genPrivateKeyB64(t)
	if err := os.WriteFile(keyPath, []byte(privB64+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	staticPeersPath := filepath.Join(tmp, "static-peers.json")
	if err := os.WriteFile(staticPeersPath, []byte("{\"version\":\"seed-v1\",\"peers\":[]}"), 0o640); err != nil {
		t.Fatal(err)
	}
	lastKnownPath := filepath.Join(tmp, "last_known.json")

	wg := &fakeWireguard{}
	agent := &Agent{
		logger:          logging.NewLogger(),
		wgManager:       wg,
		dnsServer:       &fakeDNS{},
		wireguardIP:     "10.88.0.2",
		listenPort:      51820,
		privateKeyFile:  keyPath,
		staticPeersFile: staticPeersPath,
		lastKnownPath:   lastKnownPath,
	}

	agent.applyStartupMesh()

	if len(wg.applied) != 1 {
		t.Fatalf("expected exactly one wgManager.Apply call, got %d", len(wg.applied))
	}
	got := wg.applied[0]
	if addr := got.Address.String(); addr != "10.88.0.2/32" {
		t.Fatalf("wg0 address = %q, want 10.88.0.2/32", addr)
	}
	if got.ListenPort != 51820 {
		t.Fatalf("wg0 listen port = %d, want 51820", got.ListenPort)
	}
	if len(got.Peers) != 0 {
		t.Fatalf("expected zero seed peers, got %+v", got.Peers)
	}
	lk, err := loadLastKnown(lastKnownPath)
	if err != nil {
		t.Fatalf("loadLastKnown: %v", err)
	}
	if lk == nil {
		t.Fatal("expected last-known mesh to be written")
	}
	if lk.Source != "seed" {
		t.Fatalf("last-known source = %q, want seed", lk.Source)
	}
	if lk.WireguardIP != "10.88.0.2" {
		t.Fatalf("last-known wireguard_ip = %q, want 10.88.0.2", lk.WireguardIP)
	}
}
